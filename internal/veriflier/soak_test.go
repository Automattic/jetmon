package veriflier

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestV2SoakHighConcurrencyMixedOutcomes(t *testing.T) {
	const (
		maxConcurrency = 8
		batches        = 24
		sitesPerBatch  = 5
	)

	var active atomic.Int64
	var peak atomic.Int64
	var total atomic.Int64
	srv, ts := newV2TestServer(func(ctx context.Context, req CheckRequest) ProbeResult {
		now := active.Add(1)
		updatePeak(&peak, now)
		defer active.Add(-1)
		total.Add(1)

		select {
		case <-time.After(2 * time.Millisecond):
		case <-ctx.Done():
			return ProbeResult{
				CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL, Success: false, ErrorCode: 1},
				Outcome:     OutcomeTimeout,
			}
		}

		success := req.BlogID%7 != 0
		httpCode := int32(200)
		outcome := OutcomeUp
		if !success {
			httpCode = 500
			outcome = OutcomeDown
		}
		return ProbeResult{
			CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL, Success: success, HTTPCode: httpCode, RTTMs: 2},
			Outcome:     outcome,
			TimingsMS:   TimingsMS{DNS: 1, TCP: 1, TTFB: 1},
		}
	}, ServerOptions{
		Vantage:        Vantage{ID: "soak-vantage", Region: "test-region", Provider: "test-provider"},
		AgentID:        "soak-agent",
		MaxConcurrency: maxConcurrency,
		QueueCapacity:  batches * sitesPerBatch,
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, batches)
	var wg sync.WaitGroup
	for batch := range batches {
		wg.Add(1)
		go func(batch int) {
			defer wg.Done()
			reqs := make([]CheckRequest, 0, sitesPerBatch)
			for site := range sitesPerBatch {
				blogID := int64(batch*sitesPerBatch + site + 1)
				reqs = append(reqs, CheckRequest{BlogID: blogID, URL: "https://example.com/soak"})
			}
			results, err := client.CheckBatch(ctx, reqs)
			if err != nil {
				errCh <- err
				return
			}
			if len(results) != len(reqs) {
				t.Errorf("batch %d result len = %d, want %d", batch, len(results), len(reqs))
				return
			}
			for _, result := range results {
				if result.Host != "soak-vantage" {
					t.Errorf("batch %d result host = %q, want soak-vantage", batch, result.Host)
					return
				}
				if result.RequestID == "" {
					t.Errorf("batch %d result request id is empty", batch)
					return
				}
			}
		}(batch)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("soak request failed: %v", err)
		}
	}

	wantTotal := int64(batches * sitesPerBatch)
	if got := total.Load(); got != wantTotal {
		t.Fatalf("checks executed = %d, want %d", got, wantTotal)
	}
	if got := peak.Load(); got < 2 || got > maxConcurrency {
		t.Fatalf("peak concurrency = %d, want between 2 and %d", got, maxConcurrency)
	}
	waitForCapacity(t, client, func(c Capacity) bool {
		return c.Active == 0 && c.InFlight == 0 && c.QueueDepth == 0
	})
}

func TestV2SoakOverloadThenRecovers(t *testing.T) {
	block := make(chan struct{})
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		<-block
		return ProbeResult{
			CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL, Success: true, HTTPCode: 200},
			Outcome:     OutcomeUp,
		}
	}, ServerOptions{
		Vantage:        Vantage{ID: "overload-vantage"},
		AgentID:        "overload-agent",
		MaxConcurrency: 1,
		QueueCapacity:  1,
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	firstDone := make(chan error, 1)
	go func() {
		status, body := postV2Batch(t, ts.URL, "secret", CheckV2BatchRequest{
			Requests: []CheckV2Request{
				{BlogID: 1, URL: "https://example.com/1"},
				{BlogID: 2, URL: "https://example.com/2"},
			},
		})
		if status != http.StatusOK {
			firstDone <- statusBodyError{status: status, body: body}
			return
		}
		firstDone <- nil
	}()

	waitForCapacity(t, client, func(c Capacity) bool {
		return c.InFlight == 2
	})

	status, body := postV2Batch(t, ts.URL, "secret", CheckV2BatchRequest{
		Requests: []CheckV2Request{{BlogID: 3, URL: "https://example.com/3"}},
	})
	if status != http.StatusOK {
		t.Fatalf("overload status = %d body=%s, want 200 with agent_overloaded result", status, body)
	}
	var overloadResp CheckV2BatchResponse
	if err := json.Unmarshal(body, &overloadResp); err != nil {
		t.Fatalf("decode overload response: %v", err)
	}
	if len(overloadResp.Results) != 1 || overloadResp.Results[0].Outcome != OutcomeAgentOverloaded {
		t.Fatalf("overload response = %+v, want one agent_overloaded result", overloadResp.Results)
	}

	close(block)
	if err := <-firstDone; err != nil {
		t.Fatalf("blocked request failed: %v", err)
	}
	waitForCapacity(t, client, func(c Capacity) bool {
		return c.Active == 0 && c.InFlight == 0 && c.QueueDepth == 0
	})

	result, err := client.Check(context.Background(), CheckRequest{BlogID: 4, URL: "https://example.com/4"})
	if err != nil {
		t.Fatalf("post-overload Check() error = %v", err)
	}
	if !result.Success || result.Host != "overload-vantage" {
		t.Fatalf("post-overload result = %+v", result)
	}
}

func TestV2SoakDeadlineTimeoutThenRecovers(t *testing.T) {
	srv, ts := newV2TestServer(func(ctx context.Context, req CheckRequest) ProbeResult {
		if req.URL == "https://example.com/slow" {
			<-ctx.Done()
			return ProbeResult{
				CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL, Success: false, ErrorCode: 1},
				Outcome:     OutcomeTimeout,
			}
		}
		return ProbeResult{
			CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL, Success: true, HTTPCode: 200},
			Outcome:     OutcomeUp,
		}
	}, ServerOptions{
		Vantage:        Vantage{ID: "deadline-vantage"},
		AgentID:        "deadline-agent",
		MaxConcurrency: 1,
		QueueCapacity:  4,
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	status, body := postV2Batch(t, ts.URL, "secret", CheckV2BatchRequest{
		DeadlineMS: 100,
		Requests:   []CheckV2Request{{BlogID: 1, URL: "https://example.com/slow"}},
	})
	if status != http.StatusOK {
		t.Fatalf("deadline status = %d body=%s, want 200", status, body)
	}
	var deadlineResp CheckV2BatchResponse
	if err := json.Unmarshal(body, &deadlineResp); err != nil {
		t.Fatalf("decode deadline body: %v", err)
	}
	if len(deadlineResp.Results) != 1 {
		t.Fatalf("deadline results len = %d, want 1", len(deadlineResp.Results))
	}
	if deadlineResp.Results[0].Outcome != OutcomeTimeout || deadlineResp.Results[0].Success {
		t.Fatalf("deadline result = %+v, want per-request timeout", deadlineResp.Results[0])
	}

	waitForCapacity(t, client, func(c Capacity) bool {
		return c.Active == 0 && c.InFlight == 0 && c.QueueDepth == 0
	})
	result, err := client.Check(context.Background(), CheckRequest{BlogID: 2, URL: "https://example.com/fast"})
	if err != nil {
		t.Fatalf("post-timeout Check() error = %v", err)
	}
	if !result.Success || result.Host != "deadline-vantage" {
		t.Fatalf("post-timeout result = %+v", result)
	}
}

func TestV2SoakUnauthorizedRequestsDoNotConsumeCapacity(t *testing.T) {
	var called atomic.Int64
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		called.Add(1)
		return ProbeResult{
			CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL, Success: true, HTTPCode: 200},
			Outcome:     OutcomeUp,
		}
	}, ServerOptions{
		Vantage:        Vantage{ID: "auth-vantage"},
		AgentID:        "auth-agent",
		MaxConcurrency: 2,
		QueueCapacity:  2,
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	for i := range 20 {
		status, body := postV2Batch(t, ts.URL, "wrong-token", CheckV2BatchRequest{
			Requests: []CheckV2Request{{BlogID: int64(i + 1), URL: "https://example.com/auth"}},
		})
		if status != http.StatusUnauthorized {
			t.Fatalf("unauthorized status = %d body=%s, want 401", status, body)
		}
	}
	if got := called.Load(); got != 0 {
		t.Fatalf("check function called %d times for unauthorized requests", got)
	}
	waitForCapacity(t, client, func(c Capacity) bool {
		return c.Active == 0 && c.InFlight == 0 && c.QueueDepth == 0
	})

	result, err := client.Check(context.Background(), CheckRequest{BlogID: 99, URL: "https://example.com/auth-ok"})
	if err != nil {
		t.Fatalf("authorized Check() error = %v", err)
	}
	if !result.Success || result.Host != "auth-vantage" {
		t.Fatalf("authorized result = %+v", result)
	}
}

func updatePeak(peak *atomic.Int64, value int64) {
	for {
		old := peak.Load()
		if value <= old || peak.CompareAndSwap(old, value) {
			return
		}
	}
}

func postV2Batch(t *testing.T, baseURL, token string, batch CheckV2BatchRequest) (int, []byte) {
	t.Helper()
	body := bytes.NewBuffer(nil)
	if err := json.NewEncoder(body).Encode(batch); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v2/check", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, respBody
}

func waitForCapacity(t *testing.T, client *VeriflierClient, ok func(Capacity) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last Capacity
	for time.Now().Before(deadline) {
		status, err := client.Status(context.Background())
		if err == nil && status != nil {
			last = status.Capacity
			if ok(last) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for capacity condition, last=%+v", last)
}

type statusBodyError struct {
	status int
	body   []byte
}

func (e statusBodyError) Error() string {
	return http.StatusText(e.status) + ": " + string(e.body)
}

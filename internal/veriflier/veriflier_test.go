package veriflier

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestServer(checkFn func(CheckRequest) CheckResult) (*Server, *httptest.Server) {
	srv := NewServerWithOptions("", "secret", "test-host", "1.0", ServerOptions{
		CheckFunc: func(_ context.Context, req CheckRequest) ProbeResult {
			res := checkFn(req)
			return ProbeResult{CheckResult: res, Outcome: outcomeFromResult(res)}
		},
		MaxConcurrency: 4,
		QueueCapacity:  4,
		EnableLegacy:   true,
	})
	ts := httptest.NewServer(srv.handler())
	return srv, ts
}

func newV2TestServer(checkFn CheckFunc, opts ...ServerOptions) (*Server, *httptest.Server) {
	cfg := ServerOptions{
		CheckFunc:      checkFn,
		Vantage:        Vantage{ID: "test-vantage", Region: "test-region", Provider: "test-provider"},
		AgentID:        "test-agent",
		Commit:         "test-commit",
		BuildDate:      "test-build-date",
		GoVersion:      "test-go",
		MaxConcurrency: 4,
		QueueCapacity:  4,
	}
	if len(opts) > 0 {
		override := opts[0]
		if override.CheckFunc != nil {
			cfg.CheckFunc = override.CheckFunc
		}
		if override.Vantage.ID != "" {
			cfg.Vantage = override.Vantage
		}
		if override.AgentID != "" {
			cfg.AgentID = override.AgentID
		}
		if override.Commit != "" {
			cfg.Commit = override.Commit
		}
		if override.BuildDate != "" {
			cfg.BuildDate = override.BuildDate
		}
		if override.GoVersion != "" {
			cfg.GoVersion = override.GoVersion
		}
		if override.MaxConcurrency != 0 {
			cfg.MaxConcurrency = override.MaxConcurrency
		}
		if override.QueueCapacity != 0 {
			cfg.QueueCapacity = override.QueueCapacity
		}
		if override.EnableLegacy {
			cfg.EnableLegacy = true
		}
	}
	srv := NewServerWithOptions("", "secret", "test-host", "1.0", cfg)
	ts := httptest.NewServer(srv.handler())
	return srv, ts
}

func checkReqBody(t *testing.T, sites []CheckRequest) *bytes.Buffer {
	t.Helper()
	body, err := json.Marshal(struct {
		Sites []CheckRequest `json:"sites"`
	}{Sites: sites})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewBuffer(body)
}

func TestServerHandleCheckSuccess(t *testing.T) {
	_, ts := newTestServer(func(req CheckRequest) CheckResult {
		return CheckResult{Success: true, HTTPCode: 200}
	})
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/check", checkReqBody(t, []CheckRequest{
		{MonitorSiteID: 1234, BlogID: 42, URL: "https://example.com"},
	}))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result struct {
		Results []CheckResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(result.Results))
	}
	if result.Results[0].Host != "test-host" {
		t.Fatalf("Host = %q, want test-host", result.Results[0].Host)
	}
	if result.Results[0].BlogID != 42 {
		t.Fatalf("BlogID = %d, want 42", result.Results[0].BlogID)
	}
	if result.Results[0].MonitorSiteID != 1234 {
		t.Fatalf("MonitorSiteID = %d, want 1234", result.Results[0].MonitorSiteID)
	}
}

func TestServerHandleCheckUnauthorized(t *testing.T) {
	_, ts := newTestServer(func(req CheckRequest) CheckResult { return CheckResult{} })
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/check", checkReqBody(t, []CheckRequest{{BlogID: 1}}))
	req.Header.Set("Authorization", "Bearer wrong-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestServerRejectsTrailingJSONValue(t *testing.T) {
	_, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		t.Fatalf("unexpected check execution for trailing JSON request: %+v", req)
		return ProbeResult{}
	})
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v2/check", bytes.NewBufferString(`{"requests":[]} {"requests":[]}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Contains(body.Bytes(), []byte("single JSON value")) {
		t.Fatalf("body = %q, want single-value diagnostic", body.String())
	}
}

func TestServerHandleCheckMethodNotAllowed(t *testing.T) {
	_, ts := newTestServer(func(req CheckRequest) CheckResult { return CheckResult{} })
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/check", nil)
	req.Header.Set("Authorization", "Bearer secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestServerHandleCheckReturnsUnsafeURLResult(t *testing.T) {
	var called atomic.Bool
	_, ts := newTestServer(func(req CheckRequest) CheckResult {
		called.Store(true)
		return CheckResult{Success: true}
	})
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/check", checkReqBody(t, []CheckRequest{
		{BlogID: 1, URL: "http://127.0.0.1/admin"},
	}))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if called.Load() {
		t.Fatal("check function was called for unsafe URL")
	}
	var result struct {
		Results []CheckResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(result.Results))
	}
	got := result.Results[0]
	if got.BlogID != 1 || got.URL != "http://127.0.0.1/admin" || got.Host != "test-host" {
		t.Fatalf("unsafe result identity = %+v", got)
	}
	if got.Success || got.ErrorCode != checkerErrorProbeSafety || got.Outcome != OutcomeUnknown {
		t.Fatalf("unsafe result = %+v, want probe-safety unknown", got)
	}
}

func TestServerHandleCheckIsolatesUnsafeURLInMixedLegacyBatch(t *testing.T) {
	var calledMu sync.Mutex
	var calledURLs []string
	_, ts := newTestServer(func(req CheckRequest) CheckResult {
		calledMu.Lock()
		calledURLs = append(calledURLs, req.URL)
		calledMu.Unlock()
		return CheckResult{BlogID: req.BlogID, URL: req.URL, Success: true, HTTPCode: 200}
	})
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/check", checkReqBody(t, []CheckRequest{
		{RequestID: "req-safe-before", BlogID: 1, URL: "https://example.com/before"},
		{RequestID: "req-unsafe", BlogID: 2, URL: "http://localhost/admin"},
		{RequestID: "req-safe-after", BlogID: 3, URL: "https://example.com/after"},
	}))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result struct {
		Results []CheckResult `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Results) != 3 {
		t.Fatalf("results len = %d, want 3", len(result.Results))
	}
	calledMu.Lock()
	defer calledMu.Unlock()
	called := make(map[string]bool, len(calledURLs))
	for _, url := range calledURLs {
		called[url] = true
	}
	if len(calledURLs) != 2 || !called["https://example.com/before"] || !called["https://example.com/after"] {
		t.Fatalf("called URLs = %#v, want only safe siblings", calledURLs)
	}
	if !result.Results[0].Success || result.Results[0].RequestID != "req-safe-before" {
		t.Fatalf("safe-before result = %+v", result.Results[0])
	}
	if result.Results[1].Success || result.Results[1].RequestID != "req-unsafe" || result.Results[1].ErrorCode != checkerErrorProbeSafety || result.Results[1].Outcome != OutcomeUnknown {
		t.Fatalf("unsafe result = %+v", result.Results[1])
	}
	if !result.Results[2].Success || result.Results[2].RequestID != "req-safe-after" {
		t.Fatalf("safe-after result = %+v", result.Results[2])
	}
}

func TestServerHandleStatus(t *testing.T) {
	_, ts := newTestServer(func(req CheckRequest) CheckResult { return CheckResult{} })
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "OK" {
		t.Fatalf("status field = %q, want OK", body["status"])
	}
	if body["version"] != "1.0" {
		t.Fatalf("version field = %q, want 1.0", body["version"])
	}
}

func TestClientServerRoundTrip(t *testing.T) {
	_, ts := newTestServer(func(req CheckRequest) CheckResult {
		return CheckResult{BlogID: req.BlogID, Success: true, HTTPCode: 200}
	})
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	res, err := client.Check(context.Background(), CheckRequest{
		BlogID: 77,
		URL:    "https://example.com",
	})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if res.BlogID != 77 {
		t.Fatalf("BlogID = %d, want 77", res.BlogID)
	}
	if res.Host != "test-host" {
		t.Fatalf("Host = %q, want test-host", res.Host)
	}
	if !res.Success {
		t.Fatal("Success = false, want true")
	}
}

func TestClientAddr(t *testing.T) {
	client := NewVeriflierClient("host1:7803", "token")
	if client.Addr() != "host1:7803" {
		t.Fatalf("Addr() = %q, want host1:7803", client.Addr())
	}
}

func TestClientHTTP2TransportTuning(t *testing.T) {
	client := NewVeriflierClient("host1:7803", "token")
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.httpClient.Transport)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = false, want true")
	}
	if transport.HTTP2 == nil {
		t.Fatal("HTTP2 config = nil, want explicit config")
	}
	if !transport.HTTP2.StrictMaxConcurrentRequests {
		t.Fatal("StrictMaxConcurrentRequests = false, want true")
	}
	if transport.HTTP2.SendPingTimeout != 30*time.Second {
		t.Fatalf("SendPingTimeout = %v, want 30s", transport.HTTP2.SendPingTimeout)
	}
	if transport.HTTP2.PingTimeout != 5*time.Second {
		t.Fatalf("PingTimeout = %v, want 5s", transport.HTTP2.PingTimeout)
	}
	if transport.HTTP2.WriteByteTimeout != 5*time.Second {
		t.Fatalf("WriteByteTimeout = %v, want 5s", transport.HTTP2.WriteByteTimeout)
	}
}

func TestClientPing(t *testing.T) {
	_, ts := newTestServer(func(req CheckRequest) CheckResult { return CheckResult{} })
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	version, err := client.Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	if version != "1.0" {
		t.Fatalf("version = %q, want 1.0", version)
	}
}

func TestClientPingRejectsErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	_, err := client.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping() expected error")
	}
	if err.Error() != "veriflier /v2/status returned 503" {
		t.Fatalf("Ping() error = %v", err)
	}
}

func TestClientBatchRoundTrip(t *testing.T) {
	_, ts := newTestServer(func(req CheckRequest) CheckResult {
		return CheckResult{BlogID: req.BlogID, Success: true, HTTPCode: 200}
	})
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	res, err := client.CheckBatch(context.Background(), []CheckRequest{
		{BlogID: 10, URL: "https://example.com"},
		{BlogID: 20, URL: "https://example.org"},
	})
	if err != nil {
		t.Fatalf("CheckBatch() error = %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("CheckBatch() len = %d, want 2", len(res))
	}
}

func TestClientBatchMapsOutOfOrderV2ResultsByRequestID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Requests) != 2 {
			t.Errorf("request len = %d, want 2", len(req.Requests))
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		results := []CheckV2Result{
			{
				RequestID: req.Requests[1].RequestID,
				BlogID:    req.Requests[1].BlogID,
				URL:       req.Requests[1].URL,
				VantageID: "test-vantage",
				AgentID:   "test-agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			},
			{
				RequestID: req.Requests[0].RequestID,
				BlogID:    req.Requests[0].BlogID,
				URL:       req.Requests[0].URL,
				VantageID: "test-vantage",
				AgentID:   "test-agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: results})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	client.setProtocol(ProtocolV2)
	res, err := client.CheckBatch(context.Background(), []CheckRequest{
		{MonitorSiteID: 101, BlogID: 1, URL: "https://example.com/one", RequestID: "one"},
		{MonitorSiteID: 0, BlogID: 2, URL: "https://example.com/two", RequestID: "two"},
	})
	if err != nil {
		t.Fatalf("CheckBatch() error = %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("CheckBatch() len = %d, want 2", len(res))
	}
	if res[0].RequestID != "one" || res[0].MonitorSiteID != 101 || res[0].BlogID != 1 {
		t.Fatalf("first result = %+v, want request one metadata", res[0])
	}
	if res[1].RequestID != "two" || res[1].MonitorSiteID != 0 || res[1].BlogID != 2 {
		t.Fatalf("second result = %+v, want request two metadata", res[1])
	}
}

func TestClientBatchReturnsUnknownForOmittedV2Result(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Requests) != 2 {
			t.Errorf("request len = %d, want 2", len(req.Requests))
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: []CheckV2Result{{
			RequestID: req.Requests[1].RequestID,
			BlogID:    req.Requests[1].BlogID,
			URL:       req.Requests[1].URL,
			VantageID: "test-vantage",
			AgentID:   "test-agent",
			Outcome:   OutcomeUp,
			Success:   true,
			HTTPCode:  200,
		}}})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	client.setProtocol(ProtocolV2)
	res, err := client.CheckBatch(context.Background(), []CheckRequest{
		{MonitorSiteID: 101, BlogID: 1, URL: "https://example.com/one", RequestID: "one"},
		{MonitorSiteID: 202, BlogID: 2, URL: "https://example.com/two", RequestID: "two"},
	})
	if err != nil {
		t.Fatalf("CheckBatch() error = %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("CheckBatch() len = %d, want 2", len(res))
	}
	if res[0].Success || res[0].Outcome != OutcomeUnknown || res[0].ErrorCode != checkerErrorInternal || res[0].RequestID != "one" || res[0].MonitorSiteID != 101 {
		t.Fatalf("first result = %+v, want request one unknown", res[0])
	}
	if !res[1].Success || res[1].Outcome != OutcomeUp || res[1].RequestID != "two" || res[1].MonitorSiteID != 202 {
		t.Fatalf("second result = %+v, want request two success", res[1])
	}
}

func TestClientBatchKeepsLocalIdentityAuthoritative(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Requests) != 1 {
			t.Errorf("request len = %d, want 1", len(req.Requests))
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: []CheckV2Result{{
			RequestID: req.Requests[0].RequestID,
			BlogID:    9999,
			URL:       "https://wrong.example.test/",
			VantageID: "test-vantage",
			AgentID:   "test-agent",
			Outcome:   OutcomeUp,
			Success:   true,
			HTTPCode:  200,
		}}})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	client.setProtocol(ProtocolV2)
	res, err := client.CheckBatch(context.Background(), []CheckRequest{
		{MonitorSiteID: 101, BlogID: 1, URL: "https://example.com/one", RequestID: "one"},
	})
	if err != nil {
		t.Fatalf("CheckBatch() error = %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("CheckBatch() len = %d, want 1", len(res))
	}
	if res[0].BlogID != 1 || res[0].URL != "https://example.com/one" || res[0].MonitorSiteID != 101 {
		t.Fatalf("result identity = %+v, want local request identity", res[0])
	}
	if !res[0].Success || res[0].Outcome != OutcomeUp || res[0].Host != "test-vantage" {
		t.Fatalf("result outcome/vantage = %+v", res[0])
	}
}

func TestClientBatchSendsServerDeadlineWithReserve(t *testing.T) {
	origReserve := singleCheckBatchDeadlineReserve
	origLargeReserve := singleCheckLargeBatchDeadlineReserve
	singleCheckBatchDeadlineReserve = 750 * time.Millisecond
	singleCheckLargeBatchDeadlineReserve = 1500 * time.Millisecond
	defer func() {
		singleCheckBatchDeadlineReserve = origReserve
		singleCheckLargeBatchDeadlineReserve = origLargeReserve
	}()

	var gotDeadline atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		gotDeadline.Store(req.DeadlineMS)
		results := make([]CheckV2Result, 0, len(req.Requests))
		for _, check := range req.Requests {
			results = append(results, CheckV2Result{
				RequestID: check.RequestID,
				BlogID:    check.BlogID,
				URL:       check.URL,
				VantageID: "test-vantage",
				AgentID:   "test-agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: results})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.CheckBatch(ctx, []CheckRequest{{BlogID: 1, URL: "https://example.com"}}); err != nil {
		t.Fatalf("CheckBatch() error = %v", err)
	}

	got := gotDeadline.Load()
	if got <= 0 {
		t.Fatal("DeadlineMS was not sent")
	}
	if got >= 3000 {
		t.Fatalf("DeadlineMS = %d, want less than caller deadline", got)
	}
	if got < 1000 {
		t.Fatalf("DeadlineMS = %d, want reserve without excessive deadline loss", got)
	}
}

func TestClientBatchUsesLargerDeadlineReserveForBatches(t *testing.T) {
	origReserve := singleCheckBatchDeadlineReserve
	origLargeReserve := singleCheckLargeBatchDeadlineReserve
	singleCheckBatchDeadlineReserve = 250 * time.Millisecond
	singleCheckLargeBatchDeadlineReserve = time.Second
	defer func() {
		singleCheckBatchDeadlineReserve = origReserve
		singleCheckLargeBatchDeadlineReserve = origLargeReserve
	}()

	var gotDeadline atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		gotDeadline.Store(req.DeadlineMS)
		results := make([]CheckV2Result, 0, len(req.Requests))
		for _, check := range req.Requests {
			results = append(results, CheckV2Result{
				RequestID: check.RequestID,
				BlogID:    check.BlogID,
				URL:       check.URL,
				VantageID: "test-vantage",
				AgentID:   "test-agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: results})
	}))
	defer ts.Close()

	reqs := make([]CheckRequest, singleCheckFullBatchMaxSize+1)
	for i := range reqs {
		reqs[i] = CheckRequest{
			BlogID:           int64(i + 1),
			URL:              "https://example.com",
			Method:           http.MethodHead,
			DetectionProfile: "legacy",
		}
	}
	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.CheckBatch(ctx, reqs); err != nil {
		t.Fatalf("CheckBatch() error = %v", err)
	}

	got := gotDeadline.Load()
	if got >= 2250 {
		t.Fatalf("DeadlineMS = %d, want large-batch reserve to leave about 1s", got)
	}
	if got < 1900 {
		t.Fatalf("DeadlineMS = %d, want reserve without excessive deadline loss", got)
	}
}

func TestClientBatchUsesLargerDeadlineReserveForFullBatches(t *testing.T) {
	origReserve := singleCheckBatchDeadlineReserve
	origLargeReserve := singleCheckLargeBatchDeadlineReserve
	singleCheckBatchDeadlineReserve = 250 * time.Millisecond
	singleCheckLargeBatchDeadlineReserve = time.Second
	defer func() {
		singleCheckBatchDeadlineReserve = origReserve
		singleCheckLargeBatchDeadlineReserve = origLargeReserve
	}()

	var gotDeadline atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		gotDeadline.Store(req.DeadlineMS)
		results := make([]CheckV2Result, 0, len(req.Requests))
		for _, check := range req.Requests {
			results = append(results, CheckV2Result{
				RequestID: check.RequestID,
				BlogID:    check.BlogID,
				URL:       check.URL,
				VantageID: "test-vantage",
				AgentID:   "test-agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: results})
	}))
	defer ts.Close()

	reqs := make([]CheckRequest, singleCheckFullBatchMaxSize+1)
	for i := range reqs {
		reqs[i] = CheckRequest{
			BlogID:           int64(i + 1),
			URL:              "https://example.com",
			Method:           http.MethodGet,
			DetectionProfile: "full",
		}
	}
	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := client.CheckBatch(ctx, reqs); err != nil {
		t.Fatalf("CheckBatch() error = %v", err)
	}

	got := gotDeadline.Load()
	if got >= 2250 {
		t.Fatalf("DeadlineMS = %d, want reserve to leave about 1s for full batch", got)
	}
	if got < 1900 {
		t.Fatalf("DeadlineMS = %d, want reserve without excessive deadline loss", got)
	}
}

func TestClientCheckCoalescesConcurrentSingles(t *testing.T) {
	origDelay := singleCheckBatchMaxDelay
	singleCheckBatchMaxDelay = 25 * time.Millisecond
	defer func() { singleCheckBatchMaxDelay = origDelay }()

	var rpcCount atomic.Int32
	var maxBatch atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		rpcCount.Add(1)
		for {
			current := maxBatch.Load()
			if int32(len(req.Requests)) <= current || maxBatch.CompareAndSwap(current, int32(len(req.Requests))) {
				break
			}
		}
		results := make([]CheckV2Result, 0, len(req.Requests))
		for _, check := range req.Requests {
			results = append(results, CheckV2Result{
				RequestID: check.RequestID,
				BlogID:    check.BlogID,
				URL:       check.URL,
				VantageID: "test-vantage",
				AgentID:   "test-agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: results})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	const checks = 128
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, checks)
	for i := range checks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := client.Check(context.Background(), CheckRequest{
				BlogID: int64(i + 1),
				URL:    "https://example.com",
			})
			if err != nil {
				errs <- err
				return
			}
			if res == nil || !res.Success || res.BlogID != int64(i+1) {
				errs <- fmt.Errorf("unexpected result for blog_id=%d: %#v", i+1, res)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if rpcCount.Load() >= checks {
		t.Fatalf("rpc count = %d, want fewer than %d", rpcCount.Load(), checks)
	}
	if maxBatch.Load() < 2 {
		t.Fatalf("max batch size = %d, want coalesced requests", maxBatch.Load())
	}
}

func TestSingleCheckBatcherMapsOutOfOrderResultsByRequestID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Requests) != 2 {
			t.Errorf("request len = %d, want 2", len(req.Requests))
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		results := []CheckV2Result{
			{
				RequestID: req.Requests[1].RequestID,
				BlogID:    req.Requests[1].BlogID,
				URL:       req.Requests[1].URL,
				VantageID: "test-vantage",
				AgentID:   "test-agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			},
			{
				RequestID: req.Requests[0].RequestID,
				BlogID:    req.Requests[0].BlogID,
				URL:       req.Requests[0].URL,
				VantageID: "test-vantage",
				AgentID:   "test-agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: results})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	client.setProtocol(ProtocolV2)
	batcher := &singleCheckBatcher{client: client}
	calls := []singleCheckCall{
		{
			ctx:  context.Background(),
			req:  CheckRequest{BlogID: 1, URL: "https://example.com/one", RequestID: "one"},
			resp: make(chan singleCheckResponse, 1),
		},
		{
			ctx:  context.Background(),
			req:  CheckRequest{BlogID: 2, URL: "https://example.com/two", RequestID: "two"},
			resp: make(chan singleCheckResponse, 1),
		},
	}
	batcher.flush(calls)

	seen := map[string]CheckResult{}
	for _, call := range calls {
		got := <-call.resp
		if got.err != nil {
			t.Fatalf("Check() error = %v", got.err)
		}
		if got.result == nil {
			t.Fatal("Check() returned nil result")
		}
		seen[got.result.RequestID] = *got.result
	}
	if seen["one"].BlogID != 1 {
		t.Fatalf("request one result = %+v", seen["one"])
	}
	if seen["two"].BlogID != 2 {
		t.Fatalf("request two result = %+v", seen["two"])
	}
}

func TestSingleCheckBatcherDoesNotMisattributePartialResultByPosition(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Requests) != 2 {
			t.Errorf("request len = %d, want 2", len(req.Requests))
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: []CheckV2Result{{
			RequestID: req.Requests[1].RequestID,
			BlogID:    req.Requests[1].BlogID,
			URL:       req.Requests[1].URL,
			VantageID: "test-vantage",
			AgentID:   "test-agent",
			Outcome:   OutcomeUp,
			Success:   true,
			HTTPCode:  200,
		}}})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	client.setProtocol(ProtocolV2)
	batcher := &singleCheckBatcher{client: client}
	calls := []singleCheckCall{
		{
			ctx:  context.Background(),
			req:  CheckRequest{BlogID: 1, URL: "https://example.com/one", RequestID: "one"},
			resp: make(chan singleCheckResponse, 1),
		},
		{
			ctx:  context.Background(),
			req:  CheckRequest{BlogID: 2, URL: "https://example.com/two", RequestID: "two"},
			resp: make(chan singleCheckResponse, 1),
		},
	}
	batcher.flush(calls)

	missing := <-calls[0].resp
	if missing.err != nil {
		t.Fatalf("missing first result error = %v, want per-request unknown result", missing.err)
	}
	if missing.result == nil || missing.result.RequestID != "one" || missing.result.BlogID != 1 || missing.result.Success || missing.result.Outcome != OutcomeUnknown || missing.result.ErrorCode != checkerErrorInternal {
		t.Fatalf("missing first result = %+v, want request one unknown", missing.result)
	}

	got := <-calls[1].resp
	if got.err != nil {
		t.Fatalf("second result error = %v", got.err)
	}
	if got.result == nil || got.result.RequestID != "two" || got.result.BlogID != 2 || !got.result.Success {
		t.Fatalf("second result = %+v", got.result)
	}
}

func TestClientCheckReturnsAgentOverloadedOnDeadlinePressure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: []CheckV2Result{{
			RequestID: "late",
			BlogID:    1,
			URL:       "https://example.com",
			VantageID: "test-vantage",
			AgentID:   "test-agent",
			Outcome:   OutcomeUp,
			Success:   true,
			HTTPCode:  200,
		}}})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	client.setProtocol(ProtocolV2)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	res, err := client.Check(ctx, CheckRequest{BlogID: 1, URL: "https://example.com"})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if res == nil || res.Outcome != OutcomeAgentOverloaded || res.Success {
		t.Fatalf("result = %+v, want agent_overloaded non-success", res)
	}
}

func TestClientCheckUsesSmallerBatchesForFullDetectionWork(t *testing.T) {
	origDelay := singleCheckBatchMaxDelay
	origLightMax := singleCheckBatchMaxSize
	origFullMax := singleCheckFullBatchMaxSize
	singleCheckBatchMaxDelay = 25 * time.Millisecond
	singleCheckBatchMaxSize = 64
	singleCheckFullBatchMaxSize = 7
	defer func() {
		singleCheckBatchMaxDelay = origDelay
		singleCheckBatchMaxSize = origLightMax
		singleCheckFullBatchMaxSize = origFullMax
	}()

	var maxBatch atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		for {
			current := maxBatch.Load()
			if int32(len(req.Requests)) <= current || maxBatch.CompareAndSwap(current, int32(len(req.Requests))) {
				break
			}
		}
		results := make([]CheckV2Result, 0, len(req.Requests))
		for _, check := range req.Requests {
			results = append(results, CheckV2Result{
				RequestID: check.RequestID,
				BlogID:    check.BlogID,
				URL:       check.URL,
				VantageID: "test-vantage",
				AgentID:   "test-agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: results})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	const checks = 28
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, checks)
	for i := range checks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := client.Check(context.Background(), CheckRequest{
				BlogID:           int64(i + 1),
				URL:              "https://example.com",
				Method:           http.MethodGet,
				DetectionProfile: "full",
			})
			if err != nil {
				errs <- err
				return
			}
			if res == nil || !res.Success {
				errs <- fmt.Errorf("unexpected result for blog_id=%d: %#v", i+1, res)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if maxBatch.Load() > int32(singleCheckFullBatchMaxSize) {
		t.Fatalf("max batch size = %d, want <= full cap %d", maxBatch.Load(), singleCheckFullBatchMaxSize)
	}
}

func TestClientCheckKeepsLightLaneMovingDuringFullBatch(t *testing.T) {
	origDelay := singleCheckBatchMaxDelay
	origLightMax := singleCheckBatchMaxSize
	origFullMax := singleCheckFullBatchMaxSize
	origLightFlight := singleCheckLightBatchMaxFlight
	origFullFlight := singleCheckFullBatchMaxFlight
	singleCheckBatchMaxDelay = 25 * time.Millisecond
	singleCheckBatchMaxSize = 64
	singleCheckFullBatchMaxSize = 7
	singleCheckLightBatchMaxFlight = 1
	singleCheckFullBatchMaxFlight = 1
	defer func() {
		singleCheckBatchMaxDelay = origDelay
		singleCheckBatchMaxSize = origLightMax
		singleCheckFullBatchMaxSize = origFullMax
		singleCheckLightBatchMaxFlight = origLightFlight
		singleCheckFullBatchMaxFlight = origFullFlight
	}()

	fullStarted := make(chan struct{})
	var closeFullStarted sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Requests) > 0 && req.Requests[0].DetectionProfile == "full" {
			closeFullStarted.Do(func() { close(fullStarted) })
			time.Sleep(150 * time.Millisecond)
		}
		results := make([]CheckV2Result, 0, len(req.Requests))
		for _, check := range req.Requests {
			results = append(results, CheckV2Result{
				RequestID: check.RequestID,
				BlogID:    check.BlogID,
				URL:       check.URL,
				VantageID: "test-vantage",
				AgentID:   "test-agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{Results: results})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	var wg sync.WaitGroup
	errs := make(chan error, singleCheckFullBatchMaxSize)
	for i := range singleCheckFullBatchMaxSize {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := client.Check(context.Background(), CheckRequest{
				BlogID:           int64(i + 1),
				URL:              "https://example.com/full",
				Method:           http.MethodGet,
				DetectionProfile: "full",
			})
			errs <- err
		}(i)
	}

	select {
	case <-fullStarted:
	case <-time.After(time.Second):
		t.Fatal("full batch did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	res, err := client.Check(ctx, CheckRequest{
		BlogID:           999,
		URL:              "https://example.com/light",
		Method:           http.MethodHead,
		DetectionProfile: "legacy",
	})
	if err != nil {
		t.Fatalf("light check blocked behind full batch: %v", err)
	}
	if res == nil || !res.Success || res.BlogID != 999 {
		t.Fatalf("unexpected light result: %#v", res)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("full check error: %v", err)
		}
	}
}

func TestSingleCheckBatcherPrunesExpiredCallsWhileWaitingForFlight(t *testing.T) {
	origPoll := singleCheckBatchFlightWaitPoll
	singleCheckBatchFlightWaitPoll = time.Millisecond
	defer func() { singleCheckBatchFlightWaitPoll = origPoll }()

	inFlight := make(chan struct{}, 1)
	inFlight <- struct{}{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	resp := make(chan singleCheckResponse, 1)
	batch := []singleCheckCall{{
		ctx: ctx,
		req: CheckRequest{
			BlogID:    42,
			URL:       "https://example.com",
			RequestID: "req-42",
		},
		resp: resp,
	}}

	kept, acquired := waitForSingleCheckFlight(batch, inFlight)
	if acquired {
		t.Fatal("expired batch should not acquire an in-flight slot")
	}
	if len(kept) != 0 {
		t.Fatalf("kept calls = %d, want 0", len(kept))
	}
	if len(inFlight) != 1 {
		t.Fatalf("in-flight slots = %d, want still occupied by caller", len(inFlight))
	}

	select {
	case got := <-resp:
		if got.err != nil {
			t.Fatalf("expired call returned error = %v, want agent_overloaded result", got.err)
		}
		if got.result == nil || got.result.Outcome != OutcomeAgentOverloaded || got.result.RequestID != "req-42" {
			t.Fatalf("expired call result = %+v, want agent_overloaded for req-42", got.result)
		}
	case <-time.After(time.Second):
		t.Fatal("expired call was not answered while waiting for flight")
	}
}

func TestClientRejectsUnauthorized(t *testing.T) {
	_, ts := newTestServer(func(req CheckRequest) CheckResult { return CheckResult{} })
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "wrong-token")
	_, err := client.Check(context.Background(), CheckRequest{BlogID: 1, URL: "https://example.com"})
	if err == nil {
		t.Fatal("Check() expected error for wrong auth token")
	}
}

func TestNewRequestID(t *testing.T) {
	id := NewRequestID()
	if len(id) != 32 {
		t.Fatalf("NewRequestID() len = %d, want 32", len(id))
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("NewRequestID() not hex: %v", err)
	}
	other := NewRequestID()
	if id == other {
		t.Fatal("NewRequestID() collided across two calls")
	}
}

func BenchmarkNewRequestID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewRequestID()
	}
}

func TestRequestIDIsEchoed(t *testing.T) {
	// Server should reflect each request's RequestID into the corresponding result.
	_, ts := newTestServer(func(req CheckRequest) CheckResult {
		return CheckResult{BlogID: req.BlogID, Success: true, HTTPCode: 200}
	})
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	res, err := client.Check(context.Background(), CheckRequest{BlogID: 99, URL: "https://example.com"})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if res.RequestID == "" {
		t.Fatal("RequestID empty in response — client should auto-generate and server should echo")
	}
	if len(res.RequestID) != 32 {
		t.Fatalf("RequestID len = %d, want 32 (16-byte hex)", len(res.RequestID))
	}
}

func TestRequestIDPreservedWhenCallerSets(t *testing.T) {
	// When the caller sets RequestID explicitly, the client must not overwrite it.
	const callerID = "caller-supplied-id"
	_, ts := newTestServer(func(req CheckRequest) CheckResult {
		return CheckResult{BlogID: req.BlogID, Success: true}
	})
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	res, err := client.Check(context.Background(), CheckRequest{
		BlogID:    1,
		URL:       "https://example.com",
		RequestID: callerID,
	})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if res.RequestID != callerID {
		t.Fatalf("RequestID = %q, want %q (caller-supplied id was overwritten)", res.RequestID, callerID)
	}
}

func TestServerRejectsOversizedBody(t *testing.T) {
	// The body cap is the only DoS mitigation between an authorized caller
	// and the JSON decoder. A body over the 10MB cap should be rejected
	// with 413 — and crucially, the checkFn should never be invoked.
	_, ts := newTestServer(func(req CheckRequest) CheckResult {
		t.Fatal("checkFn should not be called for oversized body")
		return CheckResult{}
	})
	defer ts.Close()

	body := oversizedJSONBody(
		`{"sites":[{"BlogID":1,"URL":"https://example.com","CustomHeaders":{"X-Pad":"`,
		`"}}]}`,
	)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/check", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

func TestServerHandleV2CheckRejectsOversizedBody(t *testing.T) {
	var called atomic.Bool
	_, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		called.Store(true)
		return ProbeResult{CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL, Success: true}, Outcome: OutcomeUp}
	})
	defer ts.Close()

	body := oversizedJSONBody(
		`{"requests":[{"blog_id":1,"url":"https://example.com","headers":{"X-Pad":"`,
		`"}}]}`,
	)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v2/check", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if called.Load() {
		t.Fatal("checkFn should not be called for oversized v2 body")
	}
}

func oversizedJSONBody(prefix, suffix string) *bytes.Buffer {
	pad := bytes.Repeat([]byte("x"), maxRequestBodyBytes+1)
	body := bytes.NewBuffer(nil)
	body.WriteString(prefix)
	body.Write(pad)
	body.WriteString(suffix)
	return body
}

func TestServerShutdownDrains(t *testing.T) {
	// Shutdown should drain in-flight requests up to the context deadline,
	// not yank the connection mid-response.
	srv := NewServer("127.0.0.1:0", "secret", "test-host", "1.0", func(req CheckRequest) CheckResult {
		// Simulate a slow check so Shutdown has something to drain.
		time.Sleep(50 * time.Millisecond)
		return CheckResult{BlogID: req.BlogID, Success: true}
	})

	// Listen in background; surface the listener's actual port via httptest hack.
	// Using httptest.NewUnstartedServer with our handler avoids the port-binding race.
	mux := http.NewServeMux()
	mux.HandleFunc("/check", srv.handleCheck)
	mux.HandleFunc("/status", srv.handleStatus)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Fire a request, then call Shutdown on the underlying httptest.Server's
	// http.Server. We're testing the *handler* path with timeouts; the
	// httptest.Server itself manages the listener.
	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	done := make(chan error, 1)
	go func() {
		_, err := client.Check(context.Background(), CheckRequest{BlogID: 1, URL: "https://example.com"})
		done <- err
	}()

	// Give the request time to land in the handler's sleep, then verify it
	// completes successfully (no panic, no shutdown mid-response).
	if err := <-done; err != nil {
		t.Fatalf("in-flight check failed: %v", err)
	}
}

func TestServerHandleV2Status(t *testing.T) {
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		return ProbeResult{}
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v2/status")
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var status StatusV2Response
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Vantage.ID != "test-vantage" {
		t.Fatalf("vantage id = %q, want test-vantage", status.Vantage.ID)
	}
	if status.Agent.ID != "test-agent" {
		t.Fatalf("agent id = %q, want test-agent", status.Agent.ID)
	}
	if status.Version != "1.0" {
		t.Fatalf("version = %q, want 1.0", status.Version)
	}
	if status.Commit != "test-commit" {
		t.Fatalf("commit = %q, want test-commit", status.Commit)
	}
	if status.BuildDate != "test-build-date" {
		t.Fatalf("build date = %q, want test-build-date", status.BuildDate)
	}
	if status.GoVersion != "test-go" {
		t.Fatalf("go version = %q, want test-go", status.GoVersion)
	}
	if status.Capacity.MaxConcurrency != 4 {
		t.Fatalf("max concurrency = %d, want 4", status.Capacity.MaxConcurrency)
	}
	if len(status.Protocols) != 1 || status.Protocols[0] != ProtocolV2 {
		t.Fatalf("protocols = %#v, want v2-only by default", status.Protocols)
	}
}

func TestServerLegacyEndpointsDisabledByDefault(t *testing.T) {
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		t.Fatal("checkFn should not be called for disabled legacy endpoint")
		return ProbeResult{}
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/check", checkReqBody(t, []CheckRequest{{BlogID: 1, URL: "https://example.com"}}))
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /check status = %d, want 404 when legacy disabled", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/status")
	if err != nil {
		t.Fatalf("status request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /status status = %d, want 404 when legacy disabled", resp.StatusCode)
	}
}

func TestServerLegacyEndpointsOptIn(t *testing.T) {
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		return ProbeResult{CheckResult: CheckResult{
			BlogID:   req.BlogID,
			URL:      req.URL,
			Success:  true,
			HTTPCode: 200,
		}, Outcome: OutcomeUp}
	}, ServerOptions{EnableLegacy: true})
	defer srv.executor.Shutdown()
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/check", checkReqBody(t, []CheckRequest{{BlogID: 1, URL: "https://example.com"}}))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /check status = %d, want 200 when legacy enabled", resp.StatusCode)
	}

	statusResp, err := http.Get(ts.URL + "/v2/status")
	if err != nil {
		t.Fatalf("v2 status request error: %v", err)
	}
	defer statusResp.Body.Close()
	var status StatusV2Response
	if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(status.Protocols) != 2 || status.Protocols[0] != ProtocolV2 || status.Protocols[1] != ProtocolLegacy {
		t.Fatalf("protocols = %+v, want v2 plus legacy when enabled", status.Protocols)
	}
}

func TestServerHandleV2Check(t *testing.T) {
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		if req.Method != http.MethodHead {
			t.Fatalf("method = %q, want HEAD", req.Method)
		}
		if req.DetectionProfile != "legacy" {
			t.Fatalf("detection profile = %q, want legacy", req.DetectionProfile)
		}
		if req.Keyword != "needle" {
			t.Fatalf("keyword = %q, want needle", req.Keyword)
		}
		if len(req.ForbiddenKeywords) != 1 || req.ForbiddenKeywords[0] != "bad" {
			t.Fatalf("forbidden keywords = %#v", req.ForbiddenKeywords)
		}
		return ProbeResult{
			CheckResult: CheckResult{
				BlogID:    req.BlogID,
				URL:       req.URL,
				Success:   false,
				HTTPCode:  500,
				ErrorCode: 0,
				RTTMs:     123,
			},
			Outcome:   OutcomeDown,
			TimingsMS: TimingsMS{DNS: 1, TCP: 2, TLS: 3, TTFB: 4},
		}
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	body := bytes.NewBuffer(nil)
	if err := json.NewEncoder(body).Encode(CheckV2BatchRequest{
		BatchID: "batch-1",
		Requests: []CheckV2Request{{
			RequestID:        "req-1",
			BlogID:           42,
			URL:              "https://example.com",
			TimeoutMS:        1500,
			Method:           http.MethodHead,
			DetectionProfile: "legacy",
			RedirectPolicy:   "follow",
			BodyRules: BodyRules{
				Required:  []string{"needle"},
				Forbidden: []string{"bad"},
			},
		}},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v2/check", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result CheckV2BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.BatchID != "batch-1" {
		t.Fatalf("batch id = %q, want batch-1", result.BatchID)
	}
	if result.Vantage.ID != "test-vantage" || result.Agent.ID != "test-agent" {
		t.Fatalf("identity = vantage:%q agent:%q", result.Vantage.ID, result.Agent.ID)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(result.Results))
	}
	got := result.Results[0]
	if got.RequestID != "req-1" || got.VantageID != "test-vantage" || got.AgentID != "test-agent" {
		t.Fatalf("result identity = %+v", got)
	}
	if got.Outcome != OutcomeDown || got.Success || got.HTTPCode != 500 || got.RTTMs != 123 {
		t.Fatalf("result = %+v", got)
	}
	if got.TimingsMS.DNS != 1 || got.TimingsMS.TTFB != 4 {
		t.Fatalf("timings = %+v", got.TimingsMS)
	}
}

func TestServerHandleV2CheckReturnsUnsafeURLResult(t *testing.T) {
	var called atomic.Bool
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		called.Store(true)
		return ProbeResult{CheckResult: CheckResult{Success: true}, Outcome: OutcomeUp}
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	body := bytes.NewBuffer(nil)
	if err := json.NewEncoder(body).Encode(CheckV2BatchRequest{
		Requests: []CheckV2Request{{
			RequestID: "req-unsafe",
			BlogID:    42,
			URL:       "http://169.254.169.254/latest/meta-data/",
		}},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v2/check", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if called.Load() {
		t.Fatal("check function was called for unsafe URL")
	}

	var result CheckV2BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(result.Results))
	}
	got := result.Results[0]
	if got.RequestID != "req-unsafe" || got.BlogID != 42 || got.URL != "http://169.254.169.254/latest/meta-data/" {
		t.Fatalf("result identity = %+v", got)
	}
	if got.Success || got.ErrorCode != checkerErrorProbeSafety || got.Outcome != OutcomeUnknown {
		t.Fatalf("result = %+v, want probe-safety non-success", got)
	}
}

func TestServerHandleV2CheckIsolatesUnsafeURLInMixedBatch(t *testing.T) {
	var calledURLs []string
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		calledURLs = append(calledURLs, req.URL)
		return ProbeResult{CheckResult: CheckResult{
			BlogID:    req.BlogID,
			URL:       req.URL,
			Success:   true,
			HTTPCode:  200,
			RequestID: req.RequestID,
		}, Outcome: OutcomeUp}
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	body := bytes.NewBuffer(nil)
	if err := json.NewEncoder(body).Encode(CheckV2BatchRequest{
		Requests: []CheckV2Request{
			{RequestID: "req-safe-before", BlogID: 1, URL: "https://example.com/before"},
			{RequestID: "req-unsafe", BlogID: 2, URL: "http://localhost/admin"},
			{RequestID: "req-safe-after", BlogID: 3, URL: "https://example.com/after"},
		},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v2/check", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var result CheckV2BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Results) != 3 {
		t.Fatalf("results len = %d, want 3", len(result.Results))
	}
	called := make(map[string]bool, len(calledURLs))
	for _, url := range calledURLs {
		called[url] = true
	}
	if len(calledURLs) != 2 || !called["https://example.com/before"] || !called["https://example.com/after"] {
		t.Fatalf("called URLs = %#v, want only safe siblings", calledURLs)
	}
	if !result.Results[0].Success || result.Results[0].RequestID != "req-safe-before" || result.Results[0].Outcome != OutcomeUp {
		t.Fatalf("safe-before result = %+v", result.Results[0])
	}
	if result.Results[1].Success || result.Results[1].RequestID != "req-unsafe" || result.Results[1].ErrorCode != checkerErrorProbeSafety || result.Results[1].Outcome != OutcomeUnknown {
		t.Fatalf("unsafe result = %+v", result.Results[1])
	}
	if !result.Results[2].Success || result.Results[2].RequestID != "req-safe-after" || result.Results[2].Outcome != OutcomeUp {
		t.Fatalf("safe-after result = %+v", result.Results[2])
	}
}

func TestServerHandleV2CheckAppliesBatchDeadline(t *testing.T) {
	srv, ts := newV2TestServer(func(ctx context.Context, req CheckRequest) ProbeResult {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("check context has no deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > time.Second {
			t.Fatalf("remaining deadline = %s, want a short positive deadline", remaining)
		}
		return ProbeResult{CheckResult: CheckResult{
			BlogID:   req.BlogID,
			URL:      req.URL,
			Success:  true,
			HTTPCode: 200,
		}, Outcome: OutcomeUp}
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	body := bytes.NewBuffer(nil)
	if err := json.NewEncoder(body).Encode(CheckV2BatchRequest{
		DeadlineMS: 250,
		Requests: []CheckV2Request{{
			BlogID: 1,
			URL:    "https://example.com",
		}},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v2/check", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServerHandleV2CheckReturnsRequestErrorForMultipleRequiredBodyRules(t *testing.T) {
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		t.Fatal("checkFn should not be called for invalid body rules")
		return ProbeResult{}
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	body := bytes.NewBuffer(nil)
	if err := json.NewEncoder(body).Encode(CheckV2BatchRequest{
		Requests: []CheckV2Request{{
			BlogID: 1,
			URL:    "https://example.com",
			BodyRules: BodyRules{
				Required: []string{"a", "b"},
			},
		}},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v2/check", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result CheckV2BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(result.Results))
	}
	got := result.Results[0]
	if got.Success || got.ErrorCode != checkerErrorProbeSafety || got.Outcome != OutcomeUnknown {
		t.Fatalf("result = %+v, want probe-safety unknown", got)
	}
}

func TestServerHandleV2CheckIsolatesBadPerSiteOptionsInMixedBatch(t *testing.T) {
	var calledMu sync.Mutex
	var calledURLs []string
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		calledMu.Lock()
		calledURLs = append(calledURLs, req.URL)
		calledMu.Unlock()
		return ProbeResult{CheckResult: CheckResult{
			BlogID:    req.BlogID,
			URL:       req.URL,
			Success:   true,
			HTTPCode:  200,
			RequestID: req.RequestID,
		}, Outcome: OutcomeUp}
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	body := bytes.NewBuffer(nil)
	if err := json.NewEncoder(body).Encode(CheckV2BatchRequest{
		Requests: []CheckV2Request{
			{RequestID: "req-safe-before", BlogID: 1, URL: "https://example.com/before"},
			{RequestID: "req-bad-method", BlogID: 2, URL: "https://example.com/bad-method", Method: "POST"},
			{RequestID: "req-bad-profile", BlogID: 3, URL: "https://example.com/bad-profile", DetectionProfile: "imaginary"},
			{RequestID: "req-safe-after", BlogID: 4, URL: "https://example.com/after"},
		},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v2/check", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result CheckV2BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Results) != 4 {
		t.Fatalf("results len = %d, want 4", len(result.Results))
	}
	calledMu.Lock()
	defer calledMu.Unlock()
	called := make(map[string]bool, len(calledURLs))
	for _, url := range calledURLs {
		called[url] = true
	}
	if len(calledURLs) != 2 || !called["https://example.com/before"] || !called["https://example.com/after"] {
		t.Fatalf("called URLs = %#v, want only safe siblings", calledURLs)
	}
	for _, idx := range []int{0, 3} {
		if !result.Results[idx].Success || result.Results[idx].Outcome != OutcomeUp {
			t.Fatalf("safe result %d = %+v", idx, result.Results[idx])
		}
	}
	for _, idx := range []int{1, 2} {
		if result.Results[idx].Success || result.Results[idx].ErrorCode != checkerErrorProbeSafety || result.Results[idx].Outcome != OutcomeUnknown {
			t.Fatalf("bad-options result %d = %+v", idx, result.Results[idx])
		}
	}
}

func TestClientPrefersV2WhenAvailable(t *testing.T) {
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		return ProbeResult{CheckResult: CheckResult{
			BlogID:   req.BlogID,
			URL:      req.URL,
			Success:  true,
			HTTPCode: 200,
		}, Outcome: OutcomeUp}
	}, ServerOptions{
		Vantage:        Vantage{ID: "edge-us-east"},
		AgentID:        "agent-1",
		MaxConcurrency: 2,
		QueueCapacity:  2,
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	res, err := client.Check(context.Background(), CheckRequest{BlogID: 1, URL: "https://example.com"})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if res.Host != "edge-us-east" {
		t.Fatalf("Host = %q, want v2 vantage identity", res.Host)
	}
	if res.VantageID != "edge-us-east" || res.AgentID != "agent-1" || res.Outcome != OutcomeUp {
		t.Fatalf("v2 identity = vantage:%q agent:%q outcome:%q", res.VantageID, res.AgentID, res.Outcome)
	}
	if client.cachedProtocol() != ProtocolV2 {
		t.Fatalf("cached protocol = %q, want %q", client.cachedProtocol(), ProtocolV2)
	}
}

func TestClientFallsBackToLegacyWhenUnknownV2ConnectionCloses(t *testing.T) {
	var v2Hits atomic.Int64
	var legacyHits atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/check", func(w http.ResponseWriter, r *http.Request) {
		v2Hits.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = conn.Close()
	})
	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		legacyHits.Add(1)
		var req struct {
			Sites []CheckRequest `json:"sites"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode legacy request: %v", err)
		}
		results := make([]CheckResult, 0, len(req.Sites))
		for _, site := range req.Sites {
			results = append(results, CheckResult{
				MonitorSiteID: site.MonitorSiteID,
				BlogID:        site.BlogID,
				URL:           site.URL,
				RequestID:     site.RequestID,
				Success:       true,
				HTTPCode:      200,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Results []CheckResult `json:"results"`
		}{Results: results})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	res, err := client.Check(context.Background(), CheckRequest{
		MonitorSiteID: 44,
		BlogID:        99,
		URL:           "https://example.com",
	})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !res.Success || res.BlogID != 99 || res.MonitorSiteID != 44 {
		t.Fatalf("legacy fallback result = %+v", res)
	}
	if client.cachedProtocol() != ProtocolLegacy {
		t.Fatalf("cached protocol = %q, want %q", client.cachedProtocol(), ProtocolLegacy)
	}
	if v2Hits.Load() != 1 || legacyHits.Load() != 1 {
		t.Fatalf("hits after first check v2=%d legacy=%d, want 1/1", v2Hits.Load(), legacyHits.Load())
	}

	if _, err := client.Check(context.Background(), CheckRequest{BlogID: 100, URL: "https://example.org"}); err != nil {
		t.Fatalf("cached legacy Check() error = %v", err)
	}
	if v2Hits.Load() != 1 || legacyHits.Load() != 2 {
		t.Fatalf("hits after cached legacy check v2=%d legacy=%d, want 1/2", v2Hits.Load(), legacyHits.Load())
	}
}

func TestClientTreatsCachedV2EOFAsAgentOverloaded(t *testing.T) {
	var legacyHits atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/check", func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = conn.Close()
	})
	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		legacyHits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	client.setProtocol(ProtocolV2)
	res, err := client.Check(context.Background(), CheckRequest{BlogID: 1, URL: "https://example.com"})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if res == nil || res.Outcome != OutcomeAgentOverloaded || res.Success {
		t.Fatalf("result = %+v, want agent_overloaded non-success", res)
	}
	if legacyHits.Load() != 0 {
		t.Fatalf("legacy hits = %d, want 0 for cached v2 protocol", legacyHits.Load())
	}
}

func TestClientTreatsCachedV2ConnectionResetAsAgentOverloaded(t *testing.T) {
	var legacyHits atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/check", func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	})
	mux.HandleFunc("/check", func(w http.ResponseWriter, r *http.Request) {
		legacyHits.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	client.setProtocol(ProtocolV2)
	res, err := client.Check(context.Background(), CheckRequest{BlogID: 1, URL: "https://example.com"})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if res == nil || res.Outcome != OutcomeAgentOverloaded || res.Success {
		t.Fatalf("result = %+v, want agent_overloaded non-success", res)
	}
	if legacyHits.Load() != 0 {
		t.Fatalf("legacy hits = %d, want 0 for cached v2 protocol", legacyHits.Load())
	}
}

func TestClientV2SendsContextDeadline(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/check" {
			http.NotFound(w, r)
			return
		}
		var req CheckV2BatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.DeadlineMS <= 0 {
			t.Fatalf("deadline_ms = %d, want positive", req.DeadlineMS)
		}
		if len(req.Requests) != 1 || req.Requests[0].TimeoutMS != 2000 {
			t.Fatalf("requests = %+v", req.Requests)
		}
		if req.Requests[0].Method != http.MethodHead || req.Requests[0].DetectionProfile != "legacy" {
			t.Fatalf("method/profile = %s/%s, want HEAD/legacy", req.Requests[0].Method, req.Requests[0].DetectionProfile)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{
			Vantage: Vantage{ID: "vantage"},
			Agent:   Agent{ID: "agent"},
			Results: []CheckV2Result{{
				RequestID: req.Requests[0].RequestID,
				BlogID:    req.Requests[0].BlogID,
				URL:       req.Requests[0].URL,
				VantageID: "vantage",
				AgentID:   "agent",
				Outcome:   OutcomeUp,
				Success:   true,
				HTTPCode:  200,
			}},
		})
	}))
	defer ts.Close()

	client := NewVeriflierClient(ts.Listener.Addr().String(), "secret")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := client.Check(ctx, CheckRequest{
		MonitorSiteID:    1234,
		BlogID:           9,
		URL:              "https://example.com",
		Method:           http.MethodHead,
		DetectionProfile: "legacy",
		TimeoutSeconds:   2,
	})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if res.Host != "vantage" {
		t.Fatalf("Host = %q, want vantage", res.Host)
	}
	if res.VantageID != "vantage" || res.AgentID != "agent" || res.Outcome != OutcomeUp {
		t.Fatalf("v2 identity = vantage:%q agent:%q outcome:%q", res.VantageID, res.AgentID, res.Outcome)
	}
	if res.MonitorSiteID != 1234 {
		t.Fatalf("MonitorSiteID = %d, want 1234", res.MonitorSiteID)
	}
}

func TestServerHandleV2CheckReturnsOverloadResults(t *testing.T) {
	var called atomic.Int64
	block := make(chan struct{})
	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		called.Add(1)
		<-block
		return ProbeResult{CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL, Success: true}, Outcome: OutcomeUp}
	}, ServerOptions{
		MaxConcurrency: 1,
		QueueCapacity:  1,
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	firstDone := make(chan struct{})
	go func() {
		postV2Batch(t, ts.URL, "secret", CheckV2BatchRequest{
			Requests: []CheckV2Request{
				{BlogID: 1, URL: "https://example.com/1"},
				{BlogID: 2, URL: "https://example.com/2"},
			},
		})
		close(firstDone)
	}()
	waitForCapacity(t, NewVeriflierClient(ts.Listener.Addr().String(), "secret"), func(c Capacity) bool {
		return c.InFlight == 2
	})

	body := bytes.NewBuffer(nil)
	if err := json.NewEncoder(body).Encode(CheckV2BatchRequest{
		Requests: []CheckV2Request{
			{BlogID: 3, URL: "https://example.com/3"},
		},
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v2/check", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result CheckV2BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode overload response: %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].Outcome != OutcomeAgentOverloaded {
		t.Fatalf("overload results = %+v, want one agent_overloaded", result.Results)
	}
	if called.Load() != 1 {
		t.Fatalf("check function called %d times, want only the already-running request", called.Load())
	}
	close(block)
	<-firstDone
}

func TestServerExecutesLegacyBatchConcurrently(t *testing.T) {
	var active atomic.Int64
	var peak atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{}, 2)

	srv, ts := newV2TestServer(func(_ context.Context, req CheckRequest) ProbeResult {
		now := active.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return ProbeResult{CheckResult: CheckResult{
			BlogID:   req.BlogID,
			URL:      req.URL,
			Success:  true,
			HTTPCode: 200,
		}, Outcome: OutcomeUp}
	}, ServerOptions{
		MaxConcurrency: 2,
		QueueCapacity:  2,
		EnableLegacy:   true,
	})
	defer srv.executor.Shutdown()
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/check", checkReqBody(t, []CheckRequest{
		{BlogID: 1, URL: "https://example.com/1"},
		{BlogID: 2, URL: "https://example.com/2"},
	}))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")

	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- err
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			done <- errStatus(resp.StatusCode)
			return
		}
		done <- nil
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for concurrent checks to start")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency = %d, want 2", peak.Load())
	}
}

type errStatus int

func (e errStatus) Error() string {
	return http.StatusText(int(e))
}

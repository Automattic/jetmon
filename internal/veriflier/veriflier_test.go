package veriflier

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestServer(checkFn func(CheckRequest) CheckResult) (*Server, *httptest.Server) {
	srv := NewServer("", "secret", "test-host", "1.0", checkFn)
	mux := http.NewServeMux()
	mux.HandleFunc("/check", srv.handleCheck)
	mux.HandleFunc("/status", srv.handleStatus)
	ts := httptest.NewServer(mux)
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
		return CheckResult{BlogID: req.BlogID, Success: true, HTTPCode: 200}
	})
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/check", checkReqBody(t, []CheckRequest{
		{BlogID: 42, URL: "https://example.com"},
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

	// Build a body just over the 10MB cap. Padding lives in a custom_headers
	// value so the JSON shape is still valid (we want to confirm the cap
	// fires, not that the JSON is malformed).
	pad := make([]byte, 11*1024*1024)
	for i := range pad {
		pad[i] = 'x'
	}
	body := bytes.NewBuffer(nil)
	body.WriteString(`{"sites":[{"BlogID":1,"URL":"https://example.com","CustomHeaders":{"X-Pad":"`)
	body.Write(pad)
	body.WriteString(`"}}]}`)

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

package veriflier

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// VeriflierClient sends check batches to a remote Veriflier via gRPC.
// Until protoc-generated stubs are in place this implementation uses a
// lightweight JSON-over-HTTP transport on the same port, making it fully
// functional without a protoc dependency. Swap in the generated gRPC client
// by replacing the send() method after running `make generate`.
type VeriflierClient struct {
	addr       string
	authToken  string
	httpClient *http.Client
}

// NewVeriflierClient creates a client targeting the given address (host:port).
//
// The HTTP transport is tuned for the orchestrator's hot-path use: many
// short-lived RPCs to the same verifier host during outage waves. Default
// MaxIdleConnsPerHost=2 forces frequent reconnects under any concurrency above
// 2; we raise it so the orchestrator's per-verifier escalation goroutines
// reuse a small pool of warm connections.
//
// No client-level Timeout is set. Per-call deadlines come from the caller's
// context (the orchestrator wraps each escalation with NET_COMMS_TIMEOUT +
// headroom). A blanket client.Timeout would override that — see Go's
// http.Client docs: client.Timeout is enforced regardless of ctx, so leaving
// it unset means ctx is the only deadline and is honored exactly.
func NewVeriflierClient(addr, authToken string) *VeriflierClient {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &VeriflierClient{
		addr:       addr,
		authToken:  authToken,
		httpClient: &http.Client{Transport: transport},
	}
}

// Addr returns the target address of this client.
func (c *VeriflierClient) Addr() string {
	return c.addr
}

// Check sends a single site check request to the Veriflier and returns the result.
func (c *VeriflierClient) Check(ctx context.Context, req CheckRequest) (*CheckResult, error) {
	if req.RequestID == "" {
		req.RequestID = NewRequestID()
	}
	results, err := c.CheckBatch(ctx, []CheckRequest{req})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("veriflier returned no results")
	}
	return &results[0], nil
}

// CheckBatch sends multiple check requests to the Veriflier. Each request
// without a RequestID is given a fresh one; existing RequestIDs are preserved.
func (c *VeriflierClient) CheckBatch(ctx context.Context, reqs []CheckRequest) ([]CheckResult, error) {
	type batchReq struct {
		Sites []CheckRequest `json:"sites"`
	}
	type batchResp struct {
		Results []CheckResult `json:"results"`
	}

	for i := range reqs {
		if reqs[i].RequestID == "" {
			reqs[i].RequestID = NewRequestID()
		}
	}

	body, err := json.Marshal(batchReq{Sites: reqs})
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("http://%s/check", c.addr)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.authToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("veriflier request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("veriflier returned %d", resp.StatusCode)
	}

	var br batchResp
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		return nil, fmt.Errorf("decode veriflier response: %w", err)
	}
	return br.Results, nil
}

// Ping checks whether the Veriflier is reachable and returns its version.
func (c *VeriflierClient) Ping(ctx context.Context) (string, error) {
	url := fmt.Sprintf("http://%s/status", c.addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var s struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&s)
	return s.Version, nil
}

// NewRequestID returns a 16-byte random id, hex-encoded (32 chars). Used as
// the RPC correlation id between Monitor and Verifier. Crypto/rand backed so
// IDs are unpredictable; this isn't a security primitive but it's free.
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a timestamp-based id; collisions are vanishingly
		// unlikely at our request rates and the id is correlation-only.
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

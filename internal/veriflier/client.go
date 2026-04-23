package veriflier

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// VeriflierClient sends check batches to a remote Veriflier via gRPC.
// Until protoc-generated stubs are in place this implementation uses a
// lightweight JSON-over-HTTP transport on the same port, making it fully
// functional without a protoc dependency. Swap in the generated gRPC client
// by replacing the send() method after running `make generate`.
type VeriflierClient struct {
	addr      string
	authToken string
	httpClient *http.Client
}

// NewVeriflierClient creates a client targeting the given address (host:port).
func NewVeriflierClient(addr, authToken string) *VeriflierClient {
	return &VeriflierClient{
		addr:      addr,
		authToken: authToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// Addr returns the target address of this client.
func (c *VeriflierClient) Addr() string {
	return c.addr
}

// Check sends a single site check request to the Veriflier and returns the result.
func (c *VeriflierClient) Check(ctx context.Context, req CheckRequest) (*CheckResult, error) {
	results, err := c.CheckBatch(ctx, []CheckRequest{req})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("veriflier returned no results")
	}
	return &results[0], nil
}

// CheckBatch sends multiple check requests to the Veriflier.
func (c *VeriflierClient) CheckBatch(ctx context.Context, reqs []CheckRequest) ([]CheckResult, error) {
	type batchReq struct {
		Sites []CheckRequest `json:"sites"`
	}
	type batchResp struct {
		Results []CheckResult `json:"results"`
	}

	body, err := json.Marshal(batchReq{Sites: reqs})
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("http://%s/check", c.addr)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
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

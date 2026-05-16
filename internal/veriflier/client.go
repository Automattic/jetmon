package veriflier

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Automattic/jetmon/internal/checkmode"
)

// VeriflierClient sends check batches to a remote Veriflier over the v2
// production JSON-over-HTTP transport.
type VeriflierClient struct {
	addr          string
	authToken     string
	httpClient    *http.Client
	mu            sync.RWMutex
	protocol      string
	singleOnce    sync.Once
	singleBatcher *singleCheckBatcher
}

var (
	singleCheckBatchMaxSize              = 128
	singleCheckFullBatchMaxSize          = 64
	singleCheckBatchMaxDelay             = 2 * time.Millisecond
	singleCheckBatchDeadlineReserve      = 250 * time.Millisecond
	singleCheckLargeBatchDeadlineReserve = time.Second
	singleCheckBatchFlightWaitPoll       = 5 * time.Millisecond
	singleCheckLightBatchMaxFlight       = 32
	singleCheckFullBatchMaxFlight        = 32
	singleCheckBatchQueueSize            = 32768
	singleCheckFullBatchQueueSize        = 32768
)

var (
	requestIDPrefix  = newRequestIDPrefix()
	requestIDCounter atomic.Uint64
)

// NewVeriflierClient creates a client targeting the given address (host:port).
//
// The HTTP transport is tuned for the orchestrator's hot-path use: many
// short-lived RPCs to the same verifier host during outage waves. Default
// MaxIdleConnsPerHost=2 forces frequent reconnects under any concurrency above
// 2; we raise it so the orchestrator's per-verifier escalation goroutines can
// reuse warm connections even when client-side batches are intentionally kept
// smaller to limit head-of-line blocking during flood tests.
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
		MaxIdleConns:          4096,
		MaxIdleConnsPerHost:   1024,
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
	call := singleCheckCall{
		ctx:  ctx,
		req:  req,
		resp: make(chan singleCheckResponse, 1),
	}
	if err := c.singleChecks().submit(ctx, call); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return agentOverloadedCheckResult(req), nil
		}
		return nil, err
	}
	select {
	case resp := <-call.resp:
		return resp.result, resp.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return agentOverloadedCheckResult(req), nil
		}
		return nil, ctx.Err()
	}
}

// CheckBatch sends multiple check requests to the Veriflier. Each request
// without a RequestID is given a fresh one; existing RequestIDs are preserved.
func (c *VeriflierClient) CheckBatch(ctx context.Context, reqs []CheckRequest) ([]CheckResult, error) {
	switch c.cachedProtocol() {
	case ProtocolLegacy:
		return c.checkBatchLegacy(ctx, reqs)
	case ProtocolV2:
		return c.checkBatchV2(ctx, reqs)
	default:
		results, err := c.checkBatchV2(ctx, reqs)
		if err == nil {
			c.setProtocol(ProtocolV2)
			return results, nil
		}
		if !isV2Unsupported(err) {
			return nil, err
		}
		c.setProtocol(ProtocolLegacy)
		return c.checkBatchLegacy(ctx, reqs)
	}
}

func (c *VeriflierClient) singleChecks() *singleCheckBatcher {
	c.singleOnce.Do(func() {
		c.singleBatcher = newSingleCheckBatcher(c)
	})
	return c.singleBatcher
}

type singleCheckCall struct {
	ctx  context.Context
	req  CheckRequest
	resp chan singleCheckResponse
}

type singleCheckResponse struct {
	result *CheckResult
	err    error
}

type singleCheckBatcher struct {
	client        *VeriflierClient
	lightIn       chan singleCheckCall
	fullIn        chan singleCheckCall
	lightInFlight chan struct{}
	fullInFlight  chan struct{}
}

func newSingleCheckBatcher(client *VeriflierClient) *singleCheckBatcher {
	b := &singleCheckBatcher{
		client:        client,
		lightIn:       make(chan singleCheckCall, singleCheckBatchQueueSize),
		fullIn:        make(chan singleCheckCall, singleCheckFullBatchQueueSize),
		lightInFlight: make(chan struct{}, singleCheckLightBatchMaxFlight),
		fullInFlight:  make(chan struct{}, singleCheckFullBatchMaxFlight),
	}
	go b.run(b.lightIn, singleCheckBatchMaxSize, b.lightInFlight)
	go b.run(b.fullIn, singleCheckFullBatchMaxSize, b.fullInFlight)
	return b
}

func (b *singleCheckBatcher) submit(ctx context.Context, call singleCheckCall) error {
	in := b.lightIn
	if usesFullDetectionWork(call.req) {
		in = b.fullIn
	}
	select {
	case in <- call:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *singleCheckBatcher) run(in <-chan singleCheckCall, maxBatchSize int, inFlight chan struct{}) {
	if maxBatchSize <= 0 {
		maxBatchSize = 1
	}
	if inFlight == nil {
		inFlight = make(chan struct{}, 1)
	}
	for first := range in {
		batch := []singleCheckCall{first}
		timer := time.NewTimer(singleCheckBatchMaxDelay)
		collecting := true
		for collecting && len(batch) < maxBatchSize {
			select {
			case call := <-in:
				batch = append(batch, call)
			case <-timer.C:
				collecting = false
			}
		}
		if !timer.Stop() && collecting {
			<-timer.C
		}
		var acquired bool
		batch, acquired = waitForSingleCheckFlight(batch, inFlight)
		if !acquired {
			continue
		}
		go func() {
			defer func() { <-inFlight }()
			b.flush(batch)
		}()
	}
}

func waitForSingleCheckFlight(batch []singleCheckCall, inFlight chan struct{}) ([]singleCheckCall, bool) {
	batch = pruneExpiredSingleCheckCalls(batch)
	if len(batch) == 0 {
		return nil, false
	}
	ticker := time.NewTicker(singleCheckBatchFlightWaitPoll)
	defer ticker.Stop()
	for {
		select {
		case inFlight <- struct{}{}:
			batch = pruneExpiredSingleCheckCalls(batch)
			if len(batch) == 0 {
				<-inFlight
				return nil, false
			}
			return batch, true
		case <-ticker.C:
			batch = pruneExpiredSingleCheckCalls(batch)
			if len(batch) == 0 {
				return nil, false
			}
		}
	}
}

func pruneExpiredSingleCheckCalls(calls []singleCheckCall) []singleCheckCall {
	kept := calls[:0]
	for _, call := range calls {
		if err := call.ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				call.respond(agentOverloadedCheckResult(call.req), nil)
				incrementMetric("verifier.client.batch.expired_while_waiting.count", 1)
			} else {
				call.respond(nil, err)
				incrementMetric("verifier.client.batch.canceled_while_waiting.count", 1)
			}
			continue
		}
		kept = append(kept, call)
	}
	return kept
}

func usesFullDetectionWork(req CheckRequest) bool {
	method, err := checkmode.NormalizeMethod(req.Method, checkmode.MethodGET)
	if err != nil {
		method = checkmode.MethodGET
	}
	profile, err := checkmode.NormalizeProfile(req.DetectionProfile, checkmode.ProfileFull)
	if err != nil {
		profile = checkmode.ProfileFull
	}
	return checkmode.EffectiveProfile(method, profile) == checkmode.ProfileFull
}

func (b *singleCheckBatcher) flush(batch []singleCheckCall) {
	reqs := make([]CheckRequest, 0, len(batch))
	calls := make([]singleCheckCall, 0, len(batch))
	var latestDeadline time.Time
	for _, call := range batch {
		if err := call.ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				call.respond(agentOverloadedCheckResult(call.req), nil)
			} else {
				call.respond(nil, err)
			}
			continue
		}
		if call.req.RequestID == "" {
			call.req.RequestID = NewRequestID()
		}
		if deadline, ok := call.ctx.Deadline(); ok {
			if latestDeadline.IsZero() || deadline.After(latestDeadline) {
				latestDeadline = deadline
			}
		}
		reqs = append(reqs, call.req)
		calls = append(calls, call)
	}
	if len(reqs) == 0 {
		return
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if !latestDeadline.IsZero() {
		ctx, cancel = context.WithDeadline(ctx, latestDeadline)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	results, err := b.client.CheckBatch(ctx, reqs)
	cancel()
	if err != nil {
		for _, call := range calls {
			if operationalOverloadError(err) {
				call.respond(agentOverloadedCheckResult(call.req), nil)
			} else {
				call.respond(nil, err)
			}
		}
		return
	}

	if len(results) == len(calls) && resultsMatchCallOrder(results, calls) {
		for i, call := range calls {
			result := results[i]
			call.respond(&result, nil)
		}
		return
	}

	resultsByRequestID := checkResultsByRequestID(results)
	for i, call := range calls {
		result, ok := resultsByRequestID[call.req.RequestID]
		if !ok {
			if i >= len(results) {
				call.respond(nil, fmt.Errorf("veriflier returned %d results for %d requests", len(results), len(calls)))
				continue
			}
			result = results[i]
		}
		call.respond(&result, nil)
	}
}

func resultsMatchCallOrder(results []CheckResult, calls []singleCheckCall) bool {
	if len(results) != len(calls) {
		return false
	}
	for i := range results {
		if results[i].RequestID != "" && results[i].RequestID != calls[i].req.RequestID {
			return false
		}
	}
	return true
}

func checkResultsByRequestID(results []CheckResult) map[string]CheckResult {
	out := make(map[string]CheckResult, len(results))
	for _, result := range results {
		if result.RequestID != "" {
			out[result.RequestID] = result
		}
	}
	return out
}

func (c singleCheckCall) respond(result *CheckResult, err error) {
	select {
	case c.resp <- singleCheckResponse{result: result, err: err}:
	default:
	}
}

func agentOverloadedCheckResult(req CheckRequest) *CheckResult {
	return &CheckResult{
		MonitorSiteID: req.MonitorSiteID,
		BlogID:        req.BlogID,
		URL:           req.URL,
		Outcome:       OutcomeAgentOverloaded,
		Success:       false,
		ErrorCode:     1,
		RequestID:     req.RequestID,
	}
}

func operationalOverloadError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var transportErr v2TransportError
	if errors.As(err, &transportErr) {
		return errors.Is(transportErr.err, io.EOF) ||
			errors.Is(transportErr.err, io.ErrUnexpectedEOF) ||
			errors.Is(transportErr.err, syscall.ECONNRESET)
	}
	return false
}

func (c *VeriflierClient) checkBatchLegacy(ctx context.Context, reqs []CheckRequest) ([]CheckResult, error) {
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

func (c *VeriflierClient) checkBatchV2(ctx context.Context, reqs []CheckRequest) ([]CheckResult, error) {
	type batchResp struct {
		Results []CheckV2Result `json:"results"`
	}

	v2Reqs := make([]CheckV2Request, len(reqs))
	for i := range reqs {
		if reqs[i].RequestID == "" {
			reqs[i].RequestID = NewRequestID()
		}
		v2Reqs[i] = legacyRequestToV2(reqs[i])
	}
	batchReq := CheckV2BatchRequest{
		BatchID:  NewRequestID(),
		Requests: v2Reqs,
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			serverRemaining := remaining
			if reserve := checkBatchDeadlineReserve(reqs); remaining > reserve {
				serverRemaining = remaining - reserve
			}
			if serverRemaining < time.Millisecond {
				serverRemaining = time.Millisecond
			}
			batchReq.DeadlineMS = serverRemaining.Milliseconds()
		}
	}
	body, err := json.Marshal(batchReq)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("http://%s/v2/check", c.addr)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.authToken)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, v2TransportError{endpoint: "/v2/check", err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, statusError{endpoint: "/v2/check", status: resp.StatusCode}
	}

	var br batchResp
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		return nil, fmt.Errorf("decode veriflier v2 response: %w", err)
	}

	results := make([]CheckResult, 0, len(br.Results))
	var reqByRequestID map[string]CheckRequest
	for i, res := range br.Results {
		var orig CheckRequest
		if i < len(reqs) && (res.RequestID == "" || res.RequestID == reqs[i].RequestID) {
			orig = reqs[i]
		} else {
			if reqByRequestID == nil {
				reqByRequestID = checkRequestsByRequestID(reqs)
			}
			var ok bool
			orig, ok = reqByRequestID[res.RequestID]
			if !ok && i < len(reqs) {
				orig = reqs[i]
			}
		}
		results = append(results, CheckResult{
			MonitorSiteID: orig.MonitorSiteID,
			BlogID:        res.BlogID,
			URL:           res.URL,
			Host:          res.VantageID,
			VantageID:     res.VantageID,
			AgentID:       res.AgentID,
			Outcome:       res.Outcome,
			Success:       res.Success,
			HTTPCode:      res.HTTPCode,
			ErrorCode:     res.ErrorCode,
			RTTMs:         res.RTTMs,
			RequestID:     res.RequestID,
		})
	}
	return results, nil
}

func checkRequestsByRequestID(reqs []CheckRequest) map[string]CheckRequest {
	out := make(map[string]CheckRequest, len(reqs))
	for _, req := range reqs {
		if req.RequestID != "" {
			out[req.RequestID] = req
		}
	}
	return out
}

func checkBatchDeadlineReserve(_ []CheckRequest) time.Duration {
	reserve := singleCheckBatchDeadlineReserve
	if singleCheckLargeBatchDeadlineReserve > reserve {
		reserve = singleCheckLargeBatchDeadlineReserve
	}
	return reserve
}

// Ping checks whether the Veriflier is reachable and returns its version.
func (c *VeriflierClient) Ping(ctx context.Context) (string, error) {
	status, err := c.Status(ctx)
	if err != nil {
		return "", err
	}
	return status.Version, nil
}

func (c *VeriflierClient) Status(ctx context.Context) (*StatusV2Response, error) {
	status, err := c.statusV2(ctx)
	if err == nil {
		c.setProtocol(ProtocolV2)
		return status, nil
	}
	if !isV2Unsupported(err) {
		return nil, err
	}
	url := fmt.Sprintf("http://%s/status", c.addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, v2TransportError{endpoint: "/v2/status", err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("veriflier status returned %d", resp.StatusCode)
	}

	var s struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&s)
	c.setProtocol(ProtocolLegacy)
	return &StatusV2Response{
		Status:    s.Status,
		Version:   s.Version,
		Protocols: []string{ProtocolLegacy},
	}, nil
}

func (c *VeriflierClient) statusV2(ctx context.Context) (*StatusV2Response, error) {
	url := fmt.Sprintf("http://%s/v2/status", c.addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError{endpoint: "/v2/status", status: resp.StatusCode}
	}
	var s StatusV2Response
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode veriflier v2 status: %w", err)
	}
	return &s, nil
}

// NewRequestID returns a 16-byte correlation id, hex-encoded (32 chars). Used
// as the RPC correlation id between Monitor and Verifier. It is not a security
// token; use the bearer auth token for authorization. Keeping a random
// per-process prefix plus an atomic counter avoids kernel randomness on every
// hot-path check while preserving the existing 32-character hex shape.
func NewRequestID() string {
	var b [16]byte
	copy(b[:8], requestIDPrefix[:])
	binary.BigEndian.PutUint64(b[8:], requestIDCounter.Add(1))
	return hex.EncodeToString(b[:])
}

func newRequestIDPrefix() [8]byte {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return b
	}
	binary.BigEndian.PutUint64(b[:], uint64(time.Now().UnixNano()))
	return b
}

func (c *VeriflierClient) cachedProtocol() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.protocol
}

func (c *VeriflierClient) setProtocol(protocol string) {
	c.mu.Lock()
	c.protocol = protocol
	c.mu.Unlock()
}

type statusError struct {
	endpoint string
	status   int
}

func (e statusError) Error() string {
	return fmt.Sprintf("veriflier %s returned %d", e.endpoint, e.status)
}

type v2TransportError struct {
	endpoint string
	err      error
}

func (e v2TransportError) Error() string {
	return fmt.Sprintf("veriflier %s request failed: %v", e.endpoint, e.err)
}

func (e v2TransportError) Unwrap() error {
	return e.err
}

func isV2Unsupported(err error) bool {
	var se statusError
	if errors.As(err, &se) {
		return se.status == http.StatusNotFound ||
			se.status == http.StatusMethodNotAllowed ||
			se.status == http.StatusNotImplemented
	}

	var te v2TransportError
	if errors.As(err, &te) {
		return errors.Is(te.err, io.EOF) || errors.Is(te.err, io.ErrUnexpectedEOF)
	}
	return false
}

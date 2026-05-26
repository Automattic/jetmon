package veriflier

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Automattic/jetmon/internal/checkmode"
	"github.com/Automattic/jetmon/internal/metrics"
	"github.com/Automattic/jetmon/internal/netguard"
)

// Server listens for inbound connections from the Monitor and dispatches
// check batches to the local checker. Used by the Veriflier binary.
//
// This is the server-side counterpart to VeriflierClient. It implements
// the v2 production JSON-over-HTTP transport.
//
// The HTTP server is configured with read/write/idle timeouts so a slow or
// stalled client cannot pin a goroutine indefinitely (slowloris-style DoS).
// Shutdown(ctx) drains in-flight requests up to the caller's deadline before
// closing the listener.
type Server struct {
	authToken string
	addr      string
	hostname  string
	version   string
	commit    string
	buildDate string
	goVersion string
	vantage   Vantage
	agent     Agent
	executor  *Executor
	httpSrv   *http.Server
	legacy    bool
}

type ServerOptions struct {
	CheckFunc      CheckFunc
	Vantage        Vantage
	AgentID        string
	Commit         string
	BuildDate      string
	GoVersion      string
	MaxConcurrency int
	QueueCapacity  int
	EnableLegacy   bool
}

// Timeout defaults for the verifier HTTP server. These are conservative — the
// expected pattern is a small batch POST that completes in well under a
// second. Longer values would make slowloris cheaper.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 35 * time.Second // > readTimeout so the response can flush
	idleTimeout       = 120 * time.Second
)

// maxRequestBodyBytes caps an inbound POST /check body. A typical batch is
// ~200 sites × ~250 bytes/site ≈ 50KB, so 10MB is generous headroom and
// closes a trivial DoS vector (an attacker that has the auth token can't
// stream gigabytes through the JSON decoder before we notice).
const maxRequestBodyBytes = 10 * 1024 * 1024

// NewServer creates a Server that calls checkFn for each check request.
//
// authToken must be non-empty. An empty token would create a
// dangerous edge case where any request with `Authorization: Bearer ` (with
// a trailing space and nothing else) would be accepted. The handler rejects
// empty server tokens even if a caller constructs a Server directly.
func NewServer(addr, authToken, hostname, version string, checkFn func(CheckRequest) CheckResult) *Server {
	return NewServerWithOptions(addr, authToken, hostname, version, ServerOptions{
		CheckFunc: func(_ context.Context, req CheckRequest) ProbeResult {
			if checkFn == nil {
				return ProbeResult{CheckResult: CheckResult{
					BlogID:    req.BlogID,
					URL:       req.URL,
					Success:   false,
					ErrorCode: 1,
				}, Outcome: OutcomeUnknown}
			}
			res := checkFn(req)
			return ProbeResult{
				CheckResult: res,
				Outcome:     outcomeFromResult(res),
			}
		},
		EnableLegacy: true,
	})
}

func NewServerWithOptions(addr, authToken, hostname, version string, opts ServerOptions) *Server {
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	vantage := opts.Vantage
	if vantage.ID == "" {
		vantage.ID = hostname
	}
	agentID := opts.AgentID
	if agentID == "" {
		agentID = hostname
	}
	commit := opts.Commit
	if commit == "" {
		commit = "unknown"
	}
	buildDate := opts.BuildDate
	if buildDate == "" {
		buildDate = "unknown"
	}
	goVersion := opts.GoVersion
	if goVersion == "" {
		goVersion = "unknown"
	}
	executor := NewExecutor(opts.CheckFunc, opts.MaxConcurrency, opts.QueueCapacity)
	return &Server{
		addr:      addr,
		authToken: authToken,
		hostname:  hostname,
		version:   version,
		commit:    commit,
		buildDate: buildDate,
		goVersion: goVersion,
		vantage:   vantage,
		agent: Agent{
			ID:       agentID,
			Host:     hostname,
			Version:  version,
			Protocol: ProtocolV2,
		},
		executor: executor,
		legacy:   opts.EnableLegacy,
	}
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	if s.legacy {
		mux.HandleFunc("/check", s.handleCheck)
		mux.HandleFunc("/status", s.handleStatus)
	}
	mux.HandleFunc("/v2/check", s.handleV2Check)
	mux.HandleFunc("/v2/status", s.handleV2Status)
	return mux
}

// Listen starts the HTTP server. Blocks until the server exits via Shutdown
// or an unrecoverable error. Returns http.ErrServerClosed on a clean Shutdown.
func (s *Server) Listen() error {
	s.httpSrv = &http.Server{
		Addr:              s.addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	log.Printf("veriflier: listening on %s", s.addr)
	return s.httpSrv.ListenAndServe()
}

// ListenTLS starts the Veriflier HTTP server with native TLS. This is intended
// for public-web Veriflier deployments that are not fronted by a separate TLS
// terminating proxy or load balancer.
func (s *Server) ListenTLS(certPath, keyPath string) error {
	s.httpSrv = &http.Server{
		Addr:              s.addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	log.Printf("veriflier: listening on %s with TLS", s.addr)
	return s.httpSrv.ListenAndServeTLS(certPath, keyPath)
}

// Shutdown gracefully stops the server, allowing in-flight requests to
// complete up to the context's deadline. Safe to call before Listen — the
// underlying http.Server is nil-checked.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		if s.executor != nil {
			s.executor.Shutdown()
		}
		return nil
	}
	err := s.httpSrv.Shutdown(ctx)
	if s.executor != nil {
		s.executor.Shutdown()
	}
	return err
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.authorized(r) {
		incrementMetric("verifier.auth.rejected.count", 1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	type batchReq struct {
		Sites []CheckRequest `json:"sites"`
	}
	type batchResp struct {
		Results []CheckResult `json:"results"`
	}

	var req batchReq
	if !decodeLimitedJSON(w, r, &req) {
		return
	}

	results := make([]CheckResult, len(req.Sites))
	legacyReqs := make([]CheckRequest, 0, len(req.Sites))
	legacyResultIndexes := make([]int, 0, len(req.Sites))
	for i := range req.Sites {
		if req.Sites[i].RequestID == "" {
			req.Sites[i].RequestID = NewRequestID()
		}
		if err := validateCheckURL(req.Sites[i].URL); err != nil {
			results[i] = legacyRequestErrorResult(req.Sites[i], s.hostname)
			continue
		}
		legacyReqs = append(legacyReqs, req.Sites[i])
		legacyResultIndexes = append(legacyResultIndexes, i)
	}

	if len(legacyReqs) > 0 {
		probeResults, err := s.executor.ExecuteBatch(r.Context(), legacyReqs)
		if err != nil {
			if errors.Is(err, ErrOverloaded) {
				incrementMetric("verifier.checks.overloaded.count", 1)
				http.Error(w, "veriflier overloaded", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
		for i, probeResult := range probeResults {
			if i >= len(legacyResultIndexes) {
				break
			}
			res := probeResult.CheckResult
			res.Host = s.hostname
			if res.RequestID == "" {
				res.RequestID = probeResult.RequestID
			}
			results[legacyResultIndexes[i]] = res
		}
	}

	incrementMetric("verifier.checks.received.count", len(req.Sites))
	timingMetric("verifier.checks.duration.timer", time.Since(start))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(batchResp{Results: results})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "OK",
		"version": s.version,
	})
}

func (s *Server) handleV2Check(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		incrementMetric("verifier.auth.rejected.count", 1)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req CheckV2BatchRequest
	if !decodeLimitedJSON(w, r, &req) {
		return
	}
	if len(req.Requests) == 0 {
		http.Error(w, "requests is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	var cancel context.CancelFunc
	if req.DeadlineMS > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.DeadlineMS)*time.Millisecond)
		defer cancel()
	}

	results := make([]CheckV2Result, len(req.Requests))
	legacyReqs := make([]CheckRequest, 0, len(req.Requests))
	legacyResultIndexes := make([]int, 0, len(req.Requests))
	for i, site := range req.Requests {
		legacyReq, err := v2RequestToLegacy(site)
		if err != nil {
			var perRequestErr *perRequestValidationError
			if errors.As(err, &perRequestErr) {
				results[i] = s.v2RequestErrorResult(site)
				continue
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		legacyReqs = append(legacyReqs, legacyReq)
		legacyResultIndexes = append(legacyResultIndexes, i)
	}

	if len(legacyReqs) > 0 {
		probeResults, err := s.executor.ExecuteBatch(ctx, legacyReqs)
		if err != nil {
			if errors.Is(err, ErrOverloaded) {
				incrementMetric("verifier.checks.overloaded.count", 1)
				writeV2Error(w, http.StatusServiceUnavailable, OutcomeAgentOverloaded, "veriflier overloaded")
				return
			}
			writeV2Error(w, http.StatusGatewayTimeout, OutcomeUnknown, err.Error())
			return
		}

		for i, probeResult := range probeResults {
			if i >= len(legacyResultIndexes) {
				break
			}
			results[legacyResultIndexes[i]] = s.v2Result(probeResult)
		}
	}

	incrementMetric("verifier.checks.received.count", len(req.Requests))
	timingMetric("verifier.checks.duration.timer", time.Since(start))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(CheckV2BatchResponse{
		BatchID: req.BatchID,
		Vantage: s.vantage,
		Agent:   s.agent,
		Results: results,
	})
}

func (s *Server) handleV2Status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Status())
}

func decodeLimitedJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := decodeSingleJSONValue(json.NewDecoder(r.Body), dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
		return false
	}
	return true
}

func decodeSingleJSONValue(dec *json.Decoder, dst any) error {
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain a single JSON value")
		}
		return err
	}
	return nil
}

func (s *Server) Status() StatusV2Response {
	protocols := []string{ProtocolV2}
	if s.legacy {
		protocols = append(protocols, ProtocolLegacy)
	}
	return StatusV2Response{
		Status:    "OK",
		Version:   s.version,
		Commit:    s.commit,
		BuildDate: s.buildDate,
		GoVersion: s.goVersion,
		Protocols: protocols,
		Vantage:   s.vantage,
		Agent:     s.agent,
		Capacity:  s.executor.Capacity(),
	}
}

func (s *Server) authorized(r *http.Request) bool {
	if s.authToken == "" {
		return false
	}
	got := r.Header.Get("Authorization")
	want := "Bearer " + s.authToken
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (s *Server) v2Result(res ProbeResult) CheckV2Result {
	outcome := res.Outcome
	if outcome == "" {
		outcome = outcomeFromResult(res.CheckResult)
	}
	return CheckV2Result{
		RequestID:   res.RequestID,
		BlogID:      res.BlogID,
		URL:         res.URL,
		VantageID:   s.vantage.ID,
		AgentID:     s.agent.ID,
		Outcome:     outcome,
		Success:     res.Success,
		HTTPCode:    res.HTTPCode,
		ErrorCode:   res.ErrorCode,
		RTTMs:       res.RTTMs,
		TimingsMS:   res.TimingsMS,
		Diagnostics: res.Diagnostics,
	}
}

func (s *Server) v2RequestErrorResult(req CheckV2Request) CheckV2Result {
	requestID := req.RequestID
	if requestID == "" {
		requestID = NewRequestID()
	}
	return CheckV2Result{
		RequestID: requestID,
		BlogID:    req.BlogID,
		URL:       req.URL,
		VantageID: s.vantage.ID,
		AgentID:   s.agent.ID,
		Outcome:   OutcomeUnknown,
		Success:   false,
		ErrorCode: checkerErrorProbeSafety,
	}
}

func legacyRequestErrorResult(req CheckRequest, host string) CheckResult {
	return CheckResult{
		MonitorSiteID: req.MonitorSiteID,
		BlogID:        req.BlogID,
		URL:           req.URL,
		Host:          host,
		Outcome:       OutcomeUnknown,
		Success:       false,
		ErrorCode:     checkerErrorProbeSafety,
		RequestID:     req.RequestID,
	}
}

type perRequestValidationError struct {
	err error
}

func (e *perRequestValidationError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *perRequestValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func v2RequestToLegacy(req CheckV2Request) (CheckRequest, error) {
	if err := validateCheckURL(req.URL); err != nil {
		return CheckRequest{}, &perRequestValidationError{err: err}
	}
	method, err := checkmode.NormalizeMethod(req.Method, checkmode.MethodGET)
	if err != nil {
		return CheckRequest{}, &perRequestValidationError{err: fmt.Errorf("unsupported method %q", req.Method)}
	}
	profile, err := checkmode.NormalizeProfile(req.DetectionProfile, checkmode.ProfileFull)
	if err != nil {
		return CheckRequest{}, &perRequestValidationError{err: fmt.Errorf("unsupported detection_profile %q", req.DetectionProfile)}
	}
	profile = checkmode.EffectiveProfile(method, profile)
	requestID := req.RequestID
	if requestID == "" {
		requestID = NewRequestID()
	}
	timeoutSeconds := int32(0)
	if req.TimeoutMS > 0 {
		timeoutSeconds = int32((req.TimeoutMS + 999) / 1000)
	}
	legacyReq := CheckRequest{
		BlogID:              req.BlogID,
		URL:                 req.URL,
		Method:              method,
		DetectionProfile:    profile,
		TimeoutSeconds:      timeoutSeconds,
		BodyReadMaxBytes:    req.BodyReadMaxBytes,
		BodyReadMaxMS:       req.BodyReadMaxMS,
		KeywordReadMaxBytes: req.KeywordReadMaxBytes,
		KeywordReadMaxMS:    req.KeywordReadMaxMS,
		CustomHeaders:       req.Headers,
		RedirectPolicy:      req.RedirectPolicy,
		RequestID:           requestID,
	}
	if len(req.BodyRules.Required) > 1 {
		return CheckRequest{}, &perRequestValidationError{err: fmt.Errorf("only one required body rule is supported")}
	}
	if len(req.BodyRules.Required) > 0 {
		legacyReq.Keyword = req.BodyRules.Required[0]
	}
	if len(req.BodyRules.Forbidden) > 0 {
		legacyReq.ForbiddenKeywords = append([]string(nil), req.BodyRules.Forbidden...)
	}
	return legacyReq, nil
}

func validateCheckURL(rawURL string) error {
	_, err := netguard.ParsePublicHTTPURL(rawURL, "url")
	return err
}

func legacyRequestToV2(req CheckRequest) CheckV2Request {
	method, err := checkmode.NormalizeMethod(req.Method, checkmode.MethodGET)
	if err != nil {
		method = checkmode.MethodGET
	}
	profile, err := checkmode.NormalizeProfile(req.DetectionProfile, checkmode.ProfileFull)
	if err != nil {
		profile = checkmode.ProfileFull
	}
	profile = checkmode.EffectiveProfile(method, profile)

	out := CheckV2Request{
		RequestID:           req.RequestID,
		BlogID:              req.BlogID,
		URL:                 req.URL,
		Method:              method,
		DetectionProfile:    profile,
		Headers:             req.CustomHeaders,
		RedirectPolicy:      req.RedirectPolicy,
		BodyReadMaxBytes:    req.BodyReadMaxBytes,
		BodyReadMaxMS:       req.BodyReadMaxMS,
		KeywordReadMaxBytes: req.KeywordReadMaxBytes,
		KeywordReadMaxMS:    req.KeywordReadMaxMS,
	}
	if req.TimeoutSeconds > 0 {
		out.TimeoutMS = int64(req.TimeoutSeconds) * 1000
	}
	if req.Keyword != "" {
		out.BodyRules.Required = []string{req.Keyword}
	}
	if req.ForbiddenKeyword != "" {
		out.BodyRules.Forbidden = append(out.BodyRules.Forbidden, req.ForbiddenKeyword)
	}
	if len(req.ForbiddenKeywords) > 0 {
		out.BodyRules.Forbidden = append(out.BodyRules.Forbidden, req.ForbiddenKeywords...)
	}
	return out
}

func writeV2Error(w http.ResponseWriter, status int, outcome, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"outcome": outcome,
		"error":   message,
	})
}

// incrementMetric and timingMetric are nil-safe wrappers around the global
// StatsD client. The verifier binary may run without metrics configured, in
// which case these are no-ops.
func incrementMetric(name string, value int) {
	if m := metrics.Global(); m != nil {
		m.Increment(name, value)
	}
}

func timingMetric(name string, d time.Duration) {
	if m := metrics.Global(); m != nil {
		m.Timing(name, d)
	}
}

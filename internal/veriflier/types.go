// Package veriflier provides the client and server for Monitor↔Veriflier
// communication. The current transport is JSON-over-HTTP; proto/veriflier.proto
// is retained as a schema reference for a possible future transport.
package veriflier

// CheckRequest is a single site to check, sent from Monitor to Veriflier.
//
// RequestID is a client-generated correlation id (16-byte hex). The verifier
// echoes it back in the response so the monitor can join dispatch audit rows to
// verifier results without timestamp matching.
type CheckRequest struct {
	MonitorSiteID       int64
	BlogID              int64
	URL                 string
	Method              string
	DetectionProfile    string
	TimeoutSeconds      int32
	BodyReadMaxBytes    int64
	BodyReadMaxMS       int32
	KeywordReadMaxBytes int64
	KeywordReadMaxMS    int32
	Keyword             string
	ForbiddenKeyword    string
	ForbiddenKeywords   []string
	CustomHeaders       map[string]string
	RedirectPolicy      string
	RequestID           string
}

// CheckResult is a single check outcome returned by the Veriflier.
type CheckResult struct {
	MonitorSiteID int64
	BlogID        int64
	URL           string
	Host          string
	VantageID     string
	AgentID       string
	Outcome       string
	Success       bool
	HTTPCode      int32
	ErrorCode     int32
	RTTMs         int64
	RequestID     string            // echoed from CheckRequest.RequestID
	Diagnostics   *CheckDiagnostics `json:"diagnostics,omitempty"`
}

const (
	ProtocolLegacy = "legacy-json-http"
	ProtocolV2     = "v2-json-http"

	OutcomeUp              = "up"
	OutcomeDown            = "down"
	OutcomeTimeout         = "timeout"
	OutcomeProbeError      = "probe_error"
	OutcomeAgentOverloaded = "agent_overloaded"
	OutcomeUnknown         = "unknown"
)

// Vantage identifies the quorum-counted perspective represented by a Veriflier
// endpoint. Multiple horizontally scaled agents can share one Vantage; quorum
// logic should count Vantage identity, not individual agent processes.
type Vantage struct {
	ID       string `json:"id"`
	Region   string `json:"region,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// Agent identifies the concrete process that served a request. This is
// diagnostic metadata only; it must not be used as a quorum identity.
type Agent struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Version  string `json:"version"`
	Protocol string `json:"protocol,omitempty"`
}

type Capacity struct {
	MaxConcurrency int `json:"max_concurrency"`
	QueueCapacity  int `json:"queue_capacity"`
	QueueDepth     int `json:"queue_depth"`
	Active         int `json:"active"`
	InFlight       int `json:"in_flight"`
	Completed      int `json:"completed,omitempty"`
	Rejected       int `json:"rejected,omitempty"`
	AvgCheckMS     int `json:"avg_check_ms,omitempty"`
}

// Capabilities are behavior-level guarantees advertised by a Veriflier. Rollout
// gates should key off these flags instead of a Git commit whenever possible so
// cherry-picks and backports can still prove the behavior that production needs.
type Capabilities struct {
	BatchErrorIsolation bool  `json:"batch_error_isolation"`
	AuthRequired        bool  `json:"auth_required"`
	ProbeSafetyNonVote  bool  `json:"probe_safety_non_vote"`
	StatusDetailAuth    bool  `json:"status_detail_auth"`
	MaxRequestBodyBytes int64 `json:"max_request_body_bytes,omitempty"`
}

type StatusV2Response struct {
	Status       string       `json:"status"`
	Version      string       `json:"version"`
	Commit       string       `json:"commit"`
	BuildDate    string       `json:"build_date"`
	GoVersion    string       `json:"go_version"`
	Protocols    []string     `json:"protocols"`
	Vantage      Vantage      `json:"vantage"`
	Agent        Agent        `json:"agent"`
	Capacity     Capacity     `json:"capacity"`
	Capabilities Capabilities `json:"capabilities"`
}

type BodyRules struct {
	Required  []string `json:"required,omitempty"`
	Forbidden []string `json:"forbidden,omitempty"`
}

type CheckV2Request struct {
	RequestID           string            `json:"request_id,omitempty"`
	BlogID              int64             `json:"blog_id"`
	URL                 string            `json:"url"`
	TimeoutMS           int64             `json:"timeout_ms,omitempty"`
	Method              string            `json:"method,omitempty"`
	DetectionProfile    string            `json:"detection_profile,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
	BodyRules           BodyRules         `json:"body_rules,omitempty"`
	RedirectPolicy      string            `json:"redirect_policy,omitempty"`
	BodyReadMaxBytes    int64             `json:"body_read_max_bytes,omitempty"`
	BodyReadMaxMS       int32             `json:"body_read_max_ms,omitempty"`
	KeywordReadMaxBytes int64             `json:"keyword_read_max_bytes,omitempty"`
	KeywordReadMaxMS    int32             `json:"keyword_read_max_ms,omitempty"`
}

type CheckV2BatchRequest struct {
	BatchID    string           `json:"batch_id,omitempty"`
	DeadlineMS int64            `json:"deadline_ms,omitempty"`
	Requests   []CheckV2Request `json:"requests"`
}

type TimingsMS struct {
	DNS  int64 `json:"dns,omitempty"`
	TCP  int64 `json:"tcp,omitempty"`
	TLS  int64 `json:"tls,omitempty"`
	TTFB int64 `json:"ttfb,omitempty"`
}

// CheckDiagnostics carries bounded, operator-facing details from the shared
// checker path. It is intentionally smaller than checker.Result so Veriflier
// responses stay compact while still explaining remote confirmations and
// disagreements.
type CheckDiagnostics struct {
	KeywordRule      string               `json:"keyword_rule,omitempty"`
	ErrorDetail      string               `json:"error_detail,omitempty"`
	DNSFailureKind   string               `json:"dns_failure_kind,omitempty"`
	DNSFailureName   string               `json:"dns_failure_name,omitempty"`
	DNSFailureServer string               `json:"dns_failure_server,omitempty"`
	FinalURL         string               `json:"final_url,omitempty"`
	RedirectCount    int                  `json:"redirect_count,omitempty"`
	BodyRead         *BodyReadDiagnostics `json:"body_read,omitempty"`
	TLSVersion       uint16               `json:"tls_version,omitempty"`
	CipherSuite      uint16               `json:"cipher_suite,omitempty"`
}

type BodyReadDiagnostics struct {
	Mode          string `json:"mode,omitempty"`
	BytesRead     int64  `json:"bytes_read,omitempty"`
	ExpectedBytes int64  `json:"expected_bytes,omitempty"`
	LimitBytes    int64  `json:"limit_bytes,omitempty"`
	Error         string `json:"error,omitempty"`
}

type CheckV2Result struct {
	RequestID   string            `json:"request_id"`
	BlogID      int64             `json:"blog_id"`
	URL         string            `json:"url"`
	VantageID   string            `json:"vantage_id"`
	AgentID     string            `json:"agent_id"`
	Outcome     string            `json:"outcome"`
	Success     bool              `json:"success"`
	HTTPCode    int32             `json:"http_code"`
	ErrorCode   int32             `json:"error_code"`
	RTTMs       int64             `json:"rtt_ms"`
	TimingsMS   TimingsMS         `json:"timings_ms,omitempty"`
	Diagnostics *CheckDiagnostics `json:"diagnostics,omitempty"`
}

type CheckV2BatchResponse struct {
	BatchID string          `json:"batch_id,omitempty"`
	Vantage Vantage         `json:"vantage"`
	Agent   Agent           `json:"agent"`
	Results []CheckV2Result `json:"results"`
}

// ProbeResult is the server-internal result shape. It carries the shared check
// outcome plus v2-only timing data.
type ProbeResult struct {
	CheckResult
	Outcome   string
	TimingsMS TimingsMS
}

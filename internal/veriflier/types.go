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
	RequestID     string // echoed from CheckRequest.RequestID
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

type StatusV2Response struct {
	Status    string   `json:"status"`
	Version   string   `json:"version"`
	Commit    string   `json:"commit"`
	BuildDate string   `json:"build_date"`
	GoVersion string   `json:"go_version"`
	Protocols []string `json:"protocols"`
	Vantage   Vantage  `json:"vantage"`
	Agent     Agent    `json:"agent"`
	Capacity  Capacity `json:"capacity"`
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

type CheckV2Result struct {
	RequestID string    `json:"request_id"`
	BlogID    int64     `json:"blog_id"`
	URL       string    `json:"url"`
	VantageID string    `json:"vantage_id"`
	AgentID   string    `json:"agent_id"`
	Outcome   string    `json:"outcome"`
	Success   bool      `json:"success"`
	HTTPCode  int32     `json:"http_code"`
	ErrorCode int32     `json:"error_code"`
	RTTMs     int64     `json:"rtt_ms"`
	TimingsMS TimingsMS `json:"timings_ms,omitempty"`
}

type CheckV2BatchResponse struct {
	BatchID string          `json:"batch_id,omitempty"`
	Vantage Vantage         `json:"vantage"`
	Agent   Agent           `json:"agent"`
	Results []CheckV2Result `json:"results"`
}

// ProbeResult is the server-internal result shape. It carries the legacy
// CheckResult plus diagnostics that are only emitted by the v2 contract.
type ProbeResult struct {
	CheckResult
	Outcome   string
	TimingsMS TimingsMS
}

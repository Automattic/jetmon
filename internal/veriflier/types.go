// Package veriflier provides the client and server for Monitor↔Veriflier
// communication. The current transport is JSON-over-HTTP; types mirror the
// schema shape in proto/veriflier.proto, which is retained as a reference for
// a possible future transport.
package veriflier

// CheckRequest is a single site to check, sent from Monitor to Veriflier.
//
// RequestID is a client-generated correlation id (16-byte hex). The verifier
// echoes it back in the response and stamps it on its server-side log line so
// that "the orchestrator escalated → this verifier observed → this audit row
// in the monitor DB" can be reconstructed without timestamp matching.
type CheckRequest struct {
	BlogID            int64
	URL               string
	TimeoutSeconds    int32
	Keyword           string
	ForbiddenKeyword  string
	ForbiddenKeywords []string
	CustomHeaders     map[string]string
	RedirectPolicy    string
	RequestID         string
}

// CheckResult is a single check outcome returned by the Veriflier.
type CheckResult struct {
	BlogID    int64
	URL       string
	Host      string
	Success   bool
	HTTPCode  int32
	ErrorCode int32
	RTTMs     int64
	RequestID string // echoed from CheckRequest.RequestID
}

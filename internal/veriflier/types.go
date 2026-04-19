// Package veriflier provides the client and server for Monitor↔Veriflier
// communication. The current transport is JSON-over-HTTP; types mirror the
// proto definitions in proto/veriflier.proto. Run `make generate` after
// installing protoc to replace this with generated gRPC stubs.
package veriflier

// CheckRequest is a single site to check, sent from Monitor to Veriflier.
type CheckRequest struct {
	BlogID         int64
	URL            string
	TimeoutSeconds int32
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
}

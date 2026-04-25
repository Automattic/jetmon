package veriflier

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Automattic/jetmon/internal/metrics"
)

// Server listens for inbound connections from the Monitor and dispatches
// check batches to the local checker. Used by the Veriflier binary.
//
// This is the server-side counterpart to VeriflierClient. It implements
// the same JSON-over-HTTP transport and is replaced by a generated gRPC
// server after running `make generate`.
//
// The HTTP server is configured with read/write/idle timeouts so a slow or
// stalled client cannot pin a goroutine indefinitely (slowloris-style DoS).
// Shutdown(ctx) drains in-flight requests up to the caller's deadline before
// closing the listener.
type Server struct {
	authToken string
	checkFn   func(req CheckRequest) CheckResult
	addr      string
	hostname  string
	version   string
	httpSrv   *http.Server
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

// NewServer creates a Server that calls checkFn for each check request.
func NewServer(addr, authToken, hostname, version string, checkFn func(CheckRequest) CheckResult) *Server {
	return &Server{
		addr:      addr,
		authToken: authToken,
		hostname:  hostname,
		version:   version,
		checkFn:   checkFn,
	}
}

// Listen starts the HTTP server. Blocks until the server exits via Shutdown
// or an unrecoverable error. Returns http.ErrServerClosed on a clean Shutdown.
func (s *Server) Listen() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/check", s.handleCheck)
	mux.HandleFunc("/status", s.handleStatus)

	s.httpSrv = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	log.Printf("veriflier: listening on %s", s.addr)
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the server, allowing in-flight requests to
// complete up to the context's deadline. Safe to call before Listen — the
// underlying http.Server is nil-checked.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := r.Header.Get("Authorization")
	if token != "Bearer "+s.authToken {
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
		return
	}

	results := make([]CheckResult, 0, len(req.Sites))
	for _, site := range req.Sites {
		// Echo RequestID so the orchestrator can correlate this reply with the
		// audit row it wrote when escalating.
		log.Printf("veriflier: check blog_id=%d request_id=%s url=%s", site.BlogID, site.RequestID, site.URL)
		res := s.checkFn(site)
		res.Host = s.hostname
		res.RequestID = site.RequestID
		results = append(results, res)
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

// incrementMetric and timingMetric are nil-safe wrappers around the global
// StatsD client. The verifier binary may run without metrics configured (no
// STATSD_ADDR env var), in which case these are no-ops.
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

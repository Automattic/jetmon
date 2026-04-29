// Package api implements the internal Jetmon REST API.
//
// The API is internal-only — a separate gateway service handles all
// customer-facing concerns (tenant isolation, public errors, customer rate
// limiting). See docs/internal-api-reference.md for the full design rationale and endpoint reference.
//
// Authentication is per-consumer Bearer tokens managed via the apikeys
// package. Every authenticated request is logged to jetmon_audit_log under
// event_type=api_access for accountability.
package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Automattic/jetmon/internal/alerting"
)

// Timeout defaults for the API HTTP server. These are generous compared to
// the verifier's defaults because some endpoints (uptime stats over long
// windows, full transition lists) legitimately do more work.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 60 * time.Second
	writeTimeout      = 65 * time.Second
	idleTimeout       = 120 * time.Second
)

// Server hosts the API on a single addr. Lifecycle mirrors the verifier:
// Listen blocks; Shutdown drains gracefully up to the caller's deadline.
type Server struct {
	db          *sql.DB
	addr        string
	hostname    string
	httpSrv     *http.Server
	limiter     *rateLimiter
	idempotency *idempotencyStore

	// alertDispatchers is the per-transport dispatcher map used by the
	// alert-contact send-test endpoint. The same map is shared with the
	// alerting worker so a successful send-test is a true smoke test of
	// the path real alerts will take. Wired by main.go via
	// SetAlertDispatchers; nil if alerting is disabled.
	alertDispatchers map[alerting.Transport]alerting.Dispatcher
}

// New constructs a Server. Caller is responsible for ensuring db is connected
// and migrated before Listen is called.
func New(addr string, db *sql.DB, hostname string) *Server {
	return &Server{
		db:          db,
		addr:        addr,
		hostname:    hostname,
		limiter:     newRateLimiter(),
		idempotency: newIdempotencyStore(),
	}
}

// Listen starts the API HTTP server. Returns http.ErrServerClosed on a clean
// Shutdown. Callers wrap with errors.Is(err, http.ErrServerClosed) to
// distinguish graceful shutdown from a real failure.
func (s *Server) Listen() error {
	mux := s.routes()

	s.httpSrv = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	log.Printf("api: listening on %s", s.addr)
	return s.httpSrv.ListenAndServe()
}

// Shutdown drains in-flight requests up to ctx's deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

// SetAlertDispatchers wires the per-transport dispatcher map for the
// alert-contact send-test endpoint. Call this before Listen if
// alerting is enabled. The worker should share the same map so a
// successful send-test exercises the real production code path.
func (s *Server) SetAlertDispatchers(d map[alerting.Transport]alerting.Dispatcher) {
	s.alertDispatchers = d
}

// routes builds the request multiplexer. Uses Go 1.22's pattern-based routing
// (method + path + path-value capture).
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	for _, route := range apiRoutes() {
		route.register(s, mux)
	}

	// Catch-all → 404 with a useful message rather than the default empty body.
	mux.HandleFunc("/", s.handleNotFound)

	return mux
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, "endpoint_not_found",
		fmt.Sprintf("no route for %s %s", r.Method, r.URL.Path))
}

// IsServerClosed returns true if err is the sentinel returned by Listen
// after a clean Shutdown. Callers use this to distinguish drain-completed
// from a real listen failure.
func IsServerClosed(err error) bool {
	return errors.Is(err, http.ErrServerClosed)
}

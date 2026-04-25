// Package api implements the internal Jetmon REST API.
//
// The API is internal-only — a separate gateway service handles all
// customer-facing concerns (tenant isolation, public errors, customer rate
// limiting). See API.md for the full design rationale and endpoint reference.
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
	db       *sql.DB
	addr     string
	hostname string
	httpSrv  *http.Server
	limiter  *rateLimiter
}

// New constructs a Server. Caller is responsible for ensuring db is connected
// and migrated before Listen is called.
func New(addr string, db *sql.DB, hostname string) *Server {
	return &Server{
		db:       db,
		addr:     addr,
		hostname: hostname,
		limiter:  newRateLimiter(),
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

// routes builds the request multiplexer. Uses Go 1.22's pattern-based routing
// (method + path + path-value capture).
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Unauthenticated.
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)

	// Identity — any valid key.
	mux.HandleFunc("GET /api/v1/me", s.requireScope(scopeRead, s.handleMe))

	// Sites.
	mux.HandleFunc("GET /api/v1/sites", s.requireScope(scopeRead, s.handleListSites))
	mux.HandleFunc("GET /api/v1/sites/{id}", s.requireScope(scopeRead, s.handleGetSite))

	// Events — both site-scoped and direct lookup.
	mux.HandleFunc("GET /api/v1/sites/{id}/events", s.requireScope(scopeRead, s.handleListSiteEvents))
	mux.HandleFunc("GET /api/v1/sites/{id}/events/{event_id}", s.requireScope(scopeRead, s.handleGetEventBySite))
	mux.HandleFunc("GET /api/v1/sites/{id}/events/{event_id}/transitions", s.requireScope(scopeRead, s.handleListTransitions))
	mux.HandleFunc("GET /api/v1/events/{event_id}", s.requireScope(scopeRead, s.handleGetEvent))

	// SLA / statistics.
	mux.HandleFunc("GET /api/v1/sites/{id}/uptime", s.requireScope(scopeRead, s.handleSiteUptime))
	mux.HandleFunc("GET /api/v1/sites/{id}/response-time", s.requireScope(scopeRead, s.handleSiteResponseTime))
	mux.HandleFunc("GET /api/v1/sites/{id}/timing-breakdown", s.requireScope(scopeRead, s.handleSiteTimingBreakdown))

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

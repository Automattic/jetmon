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
	db          *sql.DB
	addr        string
	hostname    string
	httpSrv     *http.Server
	limiter     *rateLimiter
	idempotency *idempotencyStore
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

// routes builds the request multiplexer. Uses Go 1.22's pattern-based routing
// (method + path + path-value capture).
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Unauthenticated.
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)

	// Identity — any valid key.
	mux.HandleFunc("GET /api/v1/me", s.requireScope(scopeRead, s.handleMe))

	// Sites — read.
	mux.HandleFunc("GET /api/v1/sites", s.requireScope(scopeRead, s.handleListSites))
	mux.HandleFunc("GET /api/v1/sites/{id}", s.requireScope(scopeRead, s.handleGetSite))

	// Sites — write. POST endpoints route through the idempotency middleware
	// so retries with the same Idempotency-Key are safe; PATCH/DELETE skip
	// it because they're inherently idempotent on this schema.
	mux.HandleFunc("POST /api/v1/sites",
		s.requireScope(scopeWrite, s.withIdempotency(s.handleCreateSite)))
	mux.HandleFunc("PATCH /api/v1/sites/{id}",
		s.requireScope(scopeWrite, s.handleUpdateSite))
	mux.HandleFunc("DELETE /api/v1/sites/{id}",
		s.requireScope(scopeWrite, s.handleDeleteSite))
	mux.HandleFunc("POST /api/v1/sites/{id}/pause",
		s.requireScope(scopeWrite, s.withIdempotency(s.handlePauseSite)))
	mux.HandleFunc("POST /api/v1/sites/{id}/resume",
		s.requireScope(scopeWrite, s.withIdempotency(s.handleResumeSite)))
	mux.HandleFunc("POST /api/v1/sites/{id}/trigger-now",
		s.requireScope(scopeWrite, s.withIdempotency(s.handleTriggerNow)))

	// Events — both site-scoped and direct lookup.
	mux.HandleFunc("GET /api/v1/sites/{id}/events", s.requireScope(scopeRead, s.handleListSiteEvents))
	mux.HandleFunc("GET /api/v1/sites/{id}/events/{event_id}", s.requireScope(scopeRead, s.handleGetEventBySite))
	mux.HandleFunc("GET /api/v1/sites/{id}/events/{event_id}/transitions", s.requireScope(scopeRead, s.handleListTransitions))
	mux.HandleFunc("GET /api/v1/events/{event_id}", s.requireScope(scopeRead, s.handleGetEvent))

	// Events — write (manual close).
	mux.HandleFunc("POST /api/v1/sites/{id}/events/{event_id}/close",
		s.requireScope(scopeWrite, s.withIdempotency(s.handleCloseEvent)))

	// SLA / statistics.
	mux.HandleFunc("GET /api/v1/sites/{id}/uptime", s.requireScope(scopeRead, s.handleSiteUptime))
	mux.HandleFunc("GET /api/v1/sites/{id}/response-time", s.requireScope(scopeRead, s.handleSiteResponseTime))
	mux.HandleFunc("GET /api/v1/sites/{id}/timing-breakdown", s.requireScope(scopeRead, s.handleSiteTimingBreakdown))

	// Webhooks — read.
	mux.HandleFunc("GET /api/v1/webhooks",
		s.requireScope(scopeRead, s.handleListWebhooks))
	mux.HandleFunc("GET /api/v1/webhooks/{id}",
		s.requireScope(scopeRead, s.handleGetWebhook))

	// Webhooks — write. POST endpoints route through idempotency middleware
	// so a retry after a network blip doesn't double-create a webhook.
	mux.HandleFunc("POST /api/v1/webhooks",
		s.requireScope(scopeWrite, s.withIdempotency(s.handleCreateWebhook)))
	mux.HandleFunc("PATCH /api/v1/webhooks/{id}",
		s.requireScope(scopeWrite, s.handleUpdateWebhook))
	mux.HandleFunc("DELETE /api/v1/webhooks/{id}",
		s.requireScope(scopeWrite, s.handleDeleteWebhook))
	mux.HandleFunc("POST /api/v1/webhooks/{id}/rotate-secret",
		s.requireScope(scopeWrite, s.withIdempotency(s.handleRotateWebhookSecret)))

	// Deliveries — read history, manually retry abandoned rows.
	mux.HandleFunc("GET /api/v1/webhooks/{id}/deliveries",
		s.requireScope(scopeRead, s.handleListDeliveries))
	mux.HandleFunc("POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/retry",
		s.requireScope(scopeWrite, s.withIdempotency(s.handleRetryDelivery)))

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

package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/Automattic/jetmon/internal/fleethealth"
)

// readinessHeartbeatStaleAfter is the freshness window for the orchestrator's
// process_health row. Snapshots are published every STATS_UPDATE_INTERVAL_MS
// (10s default); 60s gives 6x headroom before /ready reports stale.
const readinessHeartbeatStaleAfter = 60 * time.Second

// handleHealth is unauthenticated and used by load balancers / external
// monitors. Returns 200 if the API can ping the database within 1s, else 503.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, r, http.StatusServiceUnavailable, "db_unavailable",
			"database not reachable")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "db_unavailable",
			"database not reachable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyResponse is the body of GET /api/v1/ready. status is "ready" only when
// the orchestrator has published a fresh, running, green snapshot for this
// host; otherwise the 503 body explains which condition failed so a load
// balancer's operator can read the response and know what to look at.
type readyResponse struct {
	Status       string `json:"status"`
	State        string `json:"state,omitempty"`
	HealthStatus string `json:"health_status,omitempty"`
	HeartbeatAge string `json:"heartbeat_age,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// handleReady reports whether this Monitor host is ready to take traffic.
// Distinct from /health (which only verifies DB reachability): readiness also
// requires the local orchestrator to have published a recent snapshot in
// state=running, health_status=green. Load balancers that probe /health for
// liveness should probe /ready for traffic admission, so a freshly-restarted
// process isn't sent live work before it has finished claiming buckets and
// confirming Veriflier discovery.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeError(w, r, http.StatusServiceUnavailable, "db_unavailable",
			"database not reachable")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "db_unavailable",
			"database not reachable: "+err.Error())
		return
	}

	snap, err := fleethealth.LookupReadiness(ctx, s.db, s.hostname, fleethealth.ProcessMonitor)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusServiceUnavailable, readyResponse{
			Status: "starting",
			Reason: "orchestrator has not yet published a process_health snapshot",
		})
		return
	}
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "readiness_lookup_failed",
			"reading process health failed: "+err.Error())
		return
	}

	age := time.Since(snap.UpdatedAt)
	resp := readyResponse{
		State:        snap.State,
		HealthStatus: snap.HealthStatus,
		HeartbeatAge: age.Round(time.Second).String(),
	}
	switch {
	case age > readinessHeartbeatStaleAfter:
		resp.Status = "stale"
		resp.Reason = "orchestrator snapshot is stale"
		writeJSON(w, http.StatusServiceUnavailable, resp)
	case snap.State != fleethealth.StateRunning:
		resp.Status = "not_running"
		resp.Reason = "orchestrator state is not running"
		writeJSON(w, http.StatusServiceUnavailable, resp)
	case snap.HealthStatus != fleethealth.HealthGreen:
		resp.Status = "unhealthy"
		resp.Reason = "orchestrator health is not green"
		writeJSON(w, http.StatusServiceUnavailable, resp)
	default:
		resp.Status = "ready"
		writeJSON(w, http.StatusOK, resp)
	}
}

// meResponse is what GET /api/v1/me returns. Same shape as the spec in docs/internal-api-reference.md.
type meResponse struct {
	ConsumerName       string  `json:"consumer_name"`
	Scope              string  `json:"scope"`
	RateLimitPerMinute int     `json:"rate_limit_per_minute"`
	ExpiresAt          *string `json:"expires_at"`
}

// handleMe returns the identity associated with the request's token.
// Used by consumers to verify their key works and check what scope it has.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	key := keyFromRequest(r)
	if key == nil {
		writeError(w, r, http.StatusInternalServerError, "auth_state_missing",
			"authenticated key not found in request context")
		return
	}

	resp := meResponse{
		ConsumerName:       key.ConsumerName,
		Scope:              string(key.Scope),
		RateLimitPerMinute: key.RateLimitPerMinute,
	}
	if key.ExpiresAt != nil {
		formatted := key.ExpiresAt.UTC().Format(time.RFC3339)
		resp.ExpiresAt = &formatted
	}
	writeJSON(w, http.StatusOK, resp)
}

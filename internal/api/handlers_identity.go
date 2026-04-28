package api

import (
	"context"
	"net/http"
	"time"
)

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

// meResponse is what GET /api/v1/me returns. Same shape as the spec in API.md.
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

package api

import (
	"errors"
	"net/http"

	"github.com/Automattic/jetmon/internal/dashboard"
)

func (s *Server) handleDashboardState(w http.ResponseWriter, r *http.Request) {
	if s.dashboard == nil {
		writeError(w, r, http.StatusServiceUnavailable, "dashboard_unavailable", "dashboard state is not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.dashboard.StateSnapshot())
}

func (s *Server) handleDashboardHealth(w http.ResponseWriter, r *http.Request) {
	if s.dashboard == nil {
		writeError(w, r, http.StatusServiceUnavailable, "dashboard_unavailable", "dashboard health is not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.dashboard.HealthSnapshot())
}

func (s *Server) handleDashboardHost(w http.ResponseWriter, r *http.Request) {
	if s.dashboard == nil {
		writeError(w, r, http.StatusServiceUnavailable, "dashboard_unavailable", "dashboard host snapshot is not configured")
		return
	}
	writeJSON(w, http.StatusOK, s.dashboard.HostSnapshot())
}

func (s *Server) handleDashboardFleet(w http.ResponseWriter, r *http.Request) {
	if s.dashboard == nil {
		writeError(w, r, http.StatusServiceUnavailable, "dashboard_unavailable", "dashboard fleet snapshot is not configured")
		return
	}
	snapshot, err := s.dashboard.FleetSnapshot(r.Context())
	if err != nil {
		if errors.Is(err, dashboard.ErrFleetSourceNotConfigured) {
			writeError(w, r, http.StatusServiceUnavailable, "dashboard_unavailable", "dashboard fleet source is not configured")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "dashboard_query_failed", "dashboard fleet query failed")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

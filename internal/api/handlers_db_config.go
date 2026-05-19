package api

import (
	"net/http"
	"time"

	"github.com/Automattic/jetmon/internal/db"
)

type dbConfigStatusResponse struct {
	Mode                  string   `json:"mode"`
	Source                string   `json:"source"`
	ReloadEnabled         bool     `json:"reload_enabled"`
	ReloadIntervalSeconds int64    `json:"reload_interval_seconds,omitempty"`
	LoadedAt              *string  `json:"loaded_at,omitempty"`
	LastCheckedAt         *string  `json:"last_checked_at,omitempty"`
	NextCheckAt           *string  `json:"next_check_at,omitempty"`
	LastChangeSeenAt      *string  `json:"last_change_seen_at,omitempty"`
	LastReloadedAt        *string  `json:"last_reloaded_at,omitempty"`
	LastReloadError       string   `json:"last_reload_error,omitempty"`
	LastReloadErrorAt     *string  `json:"last_reload_error_at,omitempty"`
	ActiveFingerprint     string   `json:"active_fingerprint,omitempty"`
	ReadEndpoints         []string `json:"read_endpoints,omitempty"`
	WriteEndpoints        []string `json:"write_endpoints,omitempty"`
}

func (s *Server) handleMonitorDBConfigStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, dbConfigStatusFromSnapshot(db.ConfigStatusSnapshot()))
}

func dbConfigStatusFromSnapshot(status db.ConfigStatus) dbConfigStatusResponse {
	return dbConfigStatusResponse{
		Mode:                  status.Mode,
		Source:                status.Source,
		ReloadEnabled:         status.ReloadEnabled,
		ReloadIntervalSeconds: status.ReloadIntervalSeconds,
		LoadedAt:              formatOptionalTime(status.LoadedAt),
		LastCheckedAt:         formatOptionalTime(status.LastCheckedAt),
		NextCheckAt:           formatOptionalTime(status.NextCheckAt),
		LastChangeSeenAt:      formatOptionalTime(status.LastChangeSeenAt),
		LastReloadedAt:        formatOptionalTime(status.LastReloadedAt),
		LastReloadError:       status.LastReloadError,
		LastReloadErrorAt:     formatOptionalTime(status.LastReloadErrorAt),
		ActiveFingerprint:     status.ActiveFingerprint,
		ReadEndpoints:         append([]string(nil), status.ReadEndpoints...),
		WriteEndpoints:        append([]string(nil), status.WriteEndpoints...),
	}
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

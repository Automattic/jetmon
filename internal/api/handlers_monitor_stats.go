package api

import (
	"net/http"
	"time"

	"github.com/Automattic/jetmon/internal/metrics"
)

type monitorStatsResponse struct {
	Available   bool                       `json:"available"`
	UpdatedAt   *string                    `json:"updated_at,omitempty"`
	SitesPerSec int                        `json:"sites_per_sec"`
	QueueSize   int                        `json:"queue_size"`
	Working     int                        `json:"working"`
	Waiting     int                        `json:"waiting"`
	Halting     int                        `json:"halting"`
	Error       int                        `json:"error"`
	Offline     int                        `json:"offline"`
	Success     int                        `json:"success"`
	Total       int                        `json:"total"`
	Legacy      monitorStatsLegacyResponse `json:"legacy"`
}

type monitorStatsLegacyResponse struct {
	SitesPerSec string `json:"sitespersec"`
	SitesQueue  string `json:"sitesqueue"`
	Totals      string `json:"totals"`
}

func (s *Server) handleMonitorStats(w http.ResponseWriter, r *http.Request) {
	snapshot, updatedAt, ok := metrics.LastStatsFilesSnapshot()
	if !ok {
		writeError(w, r, http.StatusServiceUnavailable, "stats_unavailable",
			"monitor stats have not been published yet")
		return
	}

	files := metrics.RenderLegacyStatsFiles(snapshot)
	if file := r.URL.Query().Get("file"); file != "" {
		writeLegacyStatsFile(w, r, file, files)
		return
	}

	updated := updatedAt.UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, monitorStatsResponse{
		Available:   true,
		UpdatedAt:   &updated,
		SitesPerSec: snapshot.SitesPerSec,
		QueueSize:   snapshot.QueueSize,
		Working:     snapshot.Working,
		Waiting:     snapshot.Waiting,
		Halting:     snapshot.Halting,
		Error:       snapshot.Error,
		Offline:     snapshot.Offline,
		Success:     snapshot.Success,
		Total:       snapshot.Total,
		Legacy: monitorStatsLegacyResponse{
			SitesPerSec: files.SitesPerSec,
			SitesQueue:  files.SitesQueue,
			Totals:      files.Totals,
		},
	})
}

func writeLegacyStatsFile(w http.ResponseWriter, r *http.Request, file string, files metrics.LegacyStatsFiles) {
	var content string
	switch file {
	case "sitespersec":
		content = files.SitesPerSec
	case "sitesqueue":
		content = files.SitesQueue
	case "totals":
		content = files.Totals
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_stats_file",
			"file must be one of: sitespersec, sitesqueue, totals")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(content))
}

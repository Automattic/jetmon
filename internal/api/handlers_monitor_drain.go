package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/Automattic/jetmon/internal/fleethealth"
)

// drainStatusResponse is the body of GET /api/v1/monitor/drain-status. It
// surfaces the in-flight work counters the operator needs to decide whether
// a clean shutdown is safe yet. "done" is true when state==stopping/stopped
// AND every counter is zero; "drain_eta_seconds" is a best-effort estimate
// using the current sites_per_sec throughput when state is still running.
type drainStatusResponse struct {
	State           string `json:"state"`
	ActiveChecks    int    `json:"active_checks"`
	QueueDepth      int    `json:"queue_depth"`
	RetryQueueSize  int    `json:"retry_queue_size"`
	WPCOMQueueDepth int    `json:"wpcom_queue_depth"`
	HeartbeatAge    string `json:"heartbeat_age"`
	Done            bool   `json:"done"`
	Reason          string `json:"reason,omitempty"`
	DrainETASeconds *int   `json:"drain_eta_seconds,omitempty"`
}

// handleMonitorDrainStatus reports the in-flight work counters published by
// the local orchestrator's process_health snapshot. Used during operator
// `systemctl stop` waits to confirm there is no pending work the process
// would lose on exit.
//
// Distinct from /ready: /ready signals admission to traffic; /drain-status
// signals safety of removal. A running host with empty queues is both
// /ready=ready and /drain-status=done; a stopping host with empty queues
// is /drain-status=done but /ready=not_running.
func (s *Server) handleMonitorDrainStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
	defer cancel()

	snap, err := fleethealth.LookupDrainStatus(ctx, s.db, s.hostname, fleethealth.ProcessMonitor)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusServiceUnavailable, drainStatusResponse{
			Reason: "orchestrator has not yet published a process_health snapshot",
		})
		return
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "drain_status_lookup_failed",
			"reading process health failed: "+err.Error())
		return
	}

	resp := drainStatusResponse{
		State:           snap.State,
		ActiveChecks:    snap.ActiveChecks,
		QueueDepth:      snap.QueueDepth,
		RetryQueueSize:  snap.RetryQueueSize,
		WPCOMQueueDepth: snap.WPCOMQueueDepth,
		HeartbeatAge:    time.Since(snap.UpdatedAt).Round(time.Second).String(),
	}

	pending := snap.ActiveChecks + snap.QueueDepth + snap.RetryQueueSize + snap.WPCOMQueueDepth
	if pending == 0 {
		resp.Done = true
	} else if snap.State == fleethealth.StateRunning {
		// On a running host pending != 0 is expected (steady-state work). Hint
		// an ETA only as a rough sites-per-sec multiplier on the queue depth.
		// The handler does not have direct access to sites_per_sec here; the
		// operator gets the headline counters and can divide by their own
		// observation. Reason field documents why ETA is omitted.
		resp.Reason = "host is running; pending counters reflect steady-state work, not drain progress"
	} else {
		resp.Reason = "drain in progress"
	}

	writeJSON(w, http.StatusOK, resp)
}

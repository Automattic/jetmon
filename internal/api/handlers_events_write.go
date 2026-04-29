package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
)

// closeEventRequest is the body for POST .../events/{event_id}/close.
//
// reason is a free-form short label per docs/internal-api-reference.md transition vocabulary
// (manual_override, false_alarm, maintenance_swallowed, etc.) — we don't
// constrain it to a strict allowlist server-side because the orchestrator
// and the operator might legitimately use different reason vocabularies
// over time. The audit log carries enough context.
//
// note ends up in the closing transition's metadata for postmortem context.
type closeEventRequest struct {
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

// handleCloseEvent implements POST /api/v1/sites/{id}/events/{event_id}/close.
//
// Manual operator override path: closes an open event with an explicit
// resolution reason. If the event was the only active one for the site,
// projects v1 site_status back to running. Already-closed events return
// a 200 with the existing event (idempotent close).
func (s *Server) handleCloseEvent(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_site_id",
			"site id must be a positive integer")
		return
	}
	eventID, err := strconv.ParseInt(r.PathValue("event_id"), 10, 64)
	if err != nil || eventID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_event_id",
			"event id must be a positive integer")
		return
	}
	if !s.ensureSiteVisibleForRequest(w, r, siteID) {
		return
	}

	var body closeEventRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// Empty body is OK — defaults below kick in. json.NewDecoder
		// surfaces io.EOF for an empty/missing body.
		if !errors.Is(err, io.EOF) {
			writeError(w, r, http.StatusBadRequest, "invalid_body",
				"request body must be valid JSON: "+err.Error())
			return
		}
	}
	reason := body.Reason
	if reason == "" {
		reason = "manual_override"
	}

	ctx := r.Context()
	// Verify the event exists and belongs to the named site before closing.
	var (
		eventBlogID int64
		endedAt     sql.NullTime
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT blog_id, ended_at FROM jetmon_events WHERE id = ?`, eventID,
	).Scan(&eventBlogID, &endedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "event_not_found",
				fmt.Sprintf("Event %d does not exist", eventID))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"event lookup failed: "+err.Error())
		return
	}
	if eventBlogID != siteID {
		writeError(w, r, http.StatusNotFound, "event_not_found",
			fmt.Sprintf("Event %d does not belong to site %d", eventID, siteID))
		return
	}
	if endedAt.Valid {
		// Idempotent close — return the existing event.
		ev, transitions, err := s.readEventWithTransitions(ctx, eventID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"read-back failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, eventDetailResponse{eventResponse: ev, Transitions: transitions})
		return
	}

	meta, _ := json.Marshal(map[string]any{
		"note":   body.Note,
		"source": "api",
	})
	if err := s.closeEvent(ctx, eventID, siteID, reason, meta); err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"close event failed: "+err.Error())
		return
	}

	ev, transitions, err := s.readEventWithTransitions(ctx, eventID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"read-back failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, eventDetailResponse{eventResponse: ev, Transitions: transitions})
}

// readEventWithTransitions reads an event row plus all of its transitions.
// Used by the close endpoint's read-back step.
func (s *Server) readEventWithTransitions(ctx context.Context, eventID int64) (eventResponse, []transitionResponse, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, blog_id, endpoint_id, check_type, discriminator,
		       severity, state, started_at, ended_at, resolution_reason,
		       cause_event_id, metadata
		  FROM jetmon_events
		 WHERE id = ?`, eventID)
	ev, err := scanEventRow(row)
	if err != nil {
		return ev, nil, err
	}
	transitions, err := s.queryTransitions(ctx, eventID)
	if err != nil {
		return ev, nil, err
	}
	ev.TransitionCount = len(transitions)
	return ev, transitions, nil
}

// triggerNowResponse is the shape returned by POST /api/v1/sites/{id}/trigger-now.
type triggerNowResponse struct {
	Result             checkResultPayload `json:"result"`
	CurrentState       string             `json:"current_state"`
	ActiveEventsClosed []int64            `json:"active_events_closed"`
}

// checkResultPayload is the subset of checker.Result we return inline.
type checkResultPayload struct {
	HTTPCode     int    `json:"http_code"`
	ErrorCode    int    `json:"error_code"`
	Success      bool   `json:"success"`
	RTTMs        int64  `json:"rtt_ms"`
	DNSMs        int64  `json:"dns_ms"`
	TCPMs        int64  `json:"tcp_ms"`
	TLSMs        int64  `json:"tls_ms"`
	TTFBMs       int64  `json:"ttfb_ms"`
	SSLExpiresAt string `json:"ssl_expires_at,omitempty"`
}

// triggerNowTimeout is the synchronous deadline for a POST /trigger-now
// call. Long enough to cover the slowest legitimate check; short enough that
// a hung target site doesn't pin a connection forever.
const triggerNowTimeout = 30 * time.Second

// handleTriggerNow implements POST /api/v1/sites/{id}/trigger-now.
//
// Runs a single HTTP check inline using the checker package, returns the
// raw result, and — if the check succeeds and an open event exists —
// closes that event with reason=probe_cleared (matches the orchestrator's
// recovery semantics for "no verifier round-trip on recovery").
//
// trigger-now does NOT open a new event on failure. The orchestrator
// handles that on its next regular round so the failure-detection state
// machine has a single owner.
func (s *Server) handleTriggerNow(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_site_id",
			"site id must be a positive integer")
		return
	}
	if !s.ensureSiteVisibleForRequest(w, r, siteID) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), triggerNowTimeout)
	defer cancel()

	site, err := s.readSiteForCheck(ctx, siteID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "site_not_found",
				fmt.Sprintf("Site %d does not exist", siteID))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site lookup failed: "+err.Error())
		return
	}

	// Run the check directly via the checker package.
	headers := map[string]string{}
	if site.customHeadersJSON != "" {
		_ = json.Unmarshal([]byte(site.customHeadersJSON), &headers)
	}
	timeoutSec := site.timeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 10
	}
	redirectPolicy := site.redirectPolicy
	if redirectPolicy == "" {
		redirectPolicy = "follow"
	}

	res := checker.Check(ctx, checker.Request{
		BlogID:         siteID,
		URL:            site.monitorURL,
		TimeoutSeconds: timeoutSec,
		Keyword:        site.checkKeywordPtr(),
		CustomHeaders:  headers,
		RedirectPolicy: checker.RedirectPolicy(redirectPolicy),
	})

	payload := checkResultPayload{
		HTTPCode:  res.HTTPCode,
		ErrorCode: res.ErrorCode,
		Success:   res.Success,
		RTTMs:     res.RTT.Milliseconds(),
		DNSMs:     res.DNS.Milliseconds(),
		TCPMs:     res.TCP.Milliseconds(),
		TLSMs:     res.TLS.Milliseconds(),
		TTFBMs:    res.TTFB.Milliseconds(),
	}
	if res.SSLExpiry != nil {
		payload.SSLExpiresAt = res.SSLExpiry.UTC().Format(time.RFC3339)
	}

	closed := []int64{}
	currentState := site.deriveState()

	if res.Success {
		// Probe came back clean — close any open events the orchestrator
		// hasn't reconciled yet. probe_cleared matches the recovery semantics
		// the orchestrator already uses (see docs/events.md: "verifier wasn't
		// involved in this recovery").
		ids, err := s.queryActiveEventIDs(ctx, siteID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"active events lookup failed: "+err.Error())
			return
		}
		for _, eventID := range ids {
			meta, _ := json.Marshal(map[string]any{
				"http_code": res.HTTPCode,
				"rtt_ms":    res.RTT.Milliseconds(),
				"source":    "api_trigger",
			})
			if err := s.closeEvent(ctx, eventID, siteID, "probe_cleared", meta); err != nil {
				writeError(w, r, http.StatusInternalServerError, "db_error",
					fmt.Sprintf("close event %d failed: %v", eventID, err))
				return
			}
			closed = append(closed, eventID)
		}
		if len(ids) > 0 {
			currentState = "Up"
		}
	}

	writeJSON(w, http.StatusOK, triggerNowResponse{
		Result:             payload,
		CurrentState:       currentState,
		ActiveEventsClosed: closed,
	})
}

// queryActiveEventIDs returns the ids of all open events for a site.
// Helper for trigger-now's clear-on-success path.
func (s *Server) queryActiveEventIDs(ctx context.Context, blogID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM jetmon_events WHERE blog_id = ? AND ended_at IS NULL`, blogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// siteForCheck is a slim subset of jetpack_monitor_sites carrying only the
// fields the trigger-now path needs. Defined here rather than reusing
// db.Site so the api package doesn't grow a dependency on internal/db
// beyond the *sql.DB handle it already has.
type siteForCheck struct {
	monitorURL        string
	timeoutSeconds    int
	checkKeyword      sql.NullString
	customHeadersJSON string
	redirectPolicy    string
	siteStatus        int
}

func (s siteForCheck) checkKeywordPtr() *string {
	if !s.checkKeyword.Valid || s.checkKeyword.String == "" {
		return nil
	}
	return &s.checkKeyword.String
}

func (s siteForCheck) deriveState() string {
	state, _ := deriveStateFromSiteStatus(s.siteStatus)
	return state
}

func (s *Server) readSiteForCheck(ctx context.Context, blogID int64) (siteForCheck, error) {
	var (
		out            siteForCheck
		timeoutSeconds sql.NullInt64
		customHeaders  sql.NullString
		redirectPolicy sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT monitor_url, timeout_seconds, check_keyword, custom_headers,
		       redirect_policy, site_status
		  FROM jetpack_monitor_sites
		 WHERE blog_id = ?`, blogID,
	).Scan(&out.monitorURL, &timeoutSeconds, &out.checkKeyword, &customHeaders,
		&redirectPolicy, &out.siteStatus)
	if err != nil {
		return out, err
	}
	if timeoutSeconds.Valid {
		out.timeoutSeconds = int(timeoutSeconds.Int64)
	}
	if customHeaders.Valid {
		out.customHeadersJSON = customHeaders.String
	}
	if redirectPolicy.Valid {
		out.redirectPolicy = redirectPolicy.String
	}
	return out, nil
}

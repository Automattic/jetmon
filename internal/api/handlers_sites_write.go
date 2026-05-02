package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Automattic/jetmon/internal/config"
)

// validRedirectPolicies bounds the redirect_policy field. Matches the ENUM
// in jetpack_monitor_sites schema (migration 3).
var validRedirectPolicies = map[string]struct{}{
	"follow": {},
	"alert":  {},
	"fail":   {},
}

const (
	maxForbiddenKeywords     = 20
	maxForbiddenKeywordBytes = 500
)

// createSiteRequest is the body shape for POST /api/v1/sites.
//
// Fields use pointers where "absent in JSON" needs to be distinguishable
// from "explicitly zero/empty" — for example, alert_cooldown_minutes might
// legitimately be 0 (meaning "no cooldown") vs missing (meaning "use the
// global default"). monitor_active is a pointer for the same reason: the
// default is true if absent, but an explicit false has to be honored.
type createSiteRequest struct {
	BlogID               *int64             `json:"blog_id"`
	MonitorURL           string             `json:"monitor_url"`
	MonitorActive        *bool              `json:"monitor_active"`
	BucketNo             *int               `json:"bucket_no"`
	CheckKeyword         *string            `json:"check_keyword"`
	ForbiddenKeyword     *string            `json:"forbidden_keyword"`
	ForbiddenKeywords    *[]string          `json:"forbidden_keywords"`
	RedirectPolicy       *string            `json:"redirect_policy"`
	TimeoutSeconds       *int               `json:"timeout_seconds"`
	CustomHeaders        *map[string]string `json:"custom_headers"`
	AlertCooldownMinutes *int               `json:"alert_cooldown_minutes"`
	CheckInterval        *int               `json:"check_interval"`
}

// handleCreateSite implements POST /api/v1/sites.
//
// blog_id is caller-supplied (it's the canonical identity from WPCOM) and
// must not already exist in jetpack_monitor_sites. Successful creation
// returns 201 with the full site object.
func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request) {
	var body createSiteRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body",
			"request body must be valid JSON: "+err.Error())
		return
	}

	if body.BlogID == nil || *body.BlogID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_blog_id",
			"blog_id is required and must be a positive integer")
		return
	}
	if err := validateMonitorURL(body.MonitorURL); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_url", err.Error())
		return
	}
	if body.RedirectPolicy != nil {
		if _, ok := validRedirectPolicies[*body.RedirectPolicy]; !ok {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_redirect_policy",
				"redirect_policy must be one of: follow, alert, fail")
			return
		}
	}

	ctx := r.Context()

	// Fast-path duplicate check. The actual race is closed by the UNIQUE
	// constraint on blog_id + the INSERT below; this just produces a clean
	// 409 for the common case.
	exists, err := s.siteExists(ctx, *body.BlogID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site existence check failed: "+err.Error())
		return
	}
	if exists {
		writeError(w, r, http.StatusConflict, "site_exists",
			fmt.Sprintf("Site %d already exists", *body.BlogID))
		return
	}

	// Apply defaults for optional fields. Pointers stay nil if the column
	// is nullable in the schema; non-nullable columns get explicit defaults.
	monitorActive := true
	if body.MonitorActive != nil {
		monitorActive = *body.MonitorActive
	}
	bucketNo := 0
	if body.BucketNo != nil {
		bucketNo = *body.BucketNo
	}
	checkInterval := 5
	if body.CheckInterval != nil {
		checkInterval = *body.CheckInterval
	}
	redirectPolicy := "follow"
	if body.RedirectPolicy != nil {
		redirectPolicy = *body.RedirectPolicy
	}

	customHeadersJSON, err := encodeCustomHeaders(body.CustomHeaders)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_custom_headers",
			err.Error())
		return
	}
	forbiddenKeywordsJSON, err := encodeForbiddenKeywords(body.ForbiddenKeywords)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_forbidden_keywords",
			err.Error())
		return
	}

	insertArgs := []any{
		*body.BlogID, bucketNo, body.MonitorURL, boolToTinyint(monitorActive), checkInterval,
		nullableStringPtr(body.CheckKeyword),
		nullableStringPtr(body.ForbiddenKeyword),
		forbiddenKeywordsJSON,
		redirectPolicy,
		nullableIntPtr(body.TimeoutSeconds),
		customHeadersJSON,
		nullableIntPtr(body.AlertCooldownMinutes),
	}
	if tenantID, ok := ownerTenantIDFromRequest(r); ok {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"site transaction failed: "+err.Error())
			return
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `
		INSERT INTO jetpack_monitor_sites
			(blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval,
			 check_keyword, forbidden_keyword, forbidden_keywords, redirect_policy, timeout_seconds, custom_headers,
			 alert_cooldown_minutes)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`, insertArgs...); err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"site insert failed: "+err.Error())
			return
		}
		if err := s.assignSiteTenant(ctx, tx, *body.BlogID, tenantID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				err.Error())
			return
		}
		if err := tx.Commit(); err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"site transaction commit failed: "+err.Error())
			return
		}
	} else if _, err = s.db.ExecContext(ctx, `
		INSERT INTO jetpack_monitor_sites
			(blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval,
			 check_keyword, forbidden_keyword, forbidden_keywords, redirect_policy, timeout_seconds, custom_headers,
			 alert_cooldown_minutes)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`,
		insertArgs...); err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site insert failed: "+err.Error())
		return
	}

	// Read back the row to return it as the response body.
	site, err := s.readSite(ctx, *body.BlogID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"read-back failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, site)
}

// updateSiteRequest is the body shape for PATCH /api/v1/sites/{id}. Every
// field is a pointer — absent fields are left unchanged, explicit nulls
// clear nullable columns.
type updateSiteRequest struct {
	MonitorURL           *string            `json:"monitor_url"`
	MonitorActive        *bool              `json:"monitor_active"`
	BucketNo             *int               `json:"bucket_no"`
	CheckKeyword         *string            `json:"check_keyword"`
	ForbiddenKeyword     *string            `json:"forbidden_keyword"`
	ForbiddenKeywords    *[]string          `json:"forbidden_keywords"`
	RedirectPolicy       *string            `json:"redirect_policy"`
	TimeoutSeconds       *int               `json:"timeout_seconds"`
	CustomHeaders        *map[string]string `json:"custom_headers"`
	AlertCooldownMinutes *int               `json:"alert_cooldown_minutes"`
	CheckInterval        *int               `json:"check_interval"`
	MaintenanceStart     *string            `json:"maintenance_start"`
	MaintenanceEnd       *string            `json:"maintenance_end"`
}

// handleUpdateSite implements PATCH /api/v1/sites/{id}.
func (s *Server) handleUpdateSite(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_site_id",
			"site id must be a positive integer")
		return
	}

	var body updateSiteRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body",
			"request body must be valid JSON: "+err.Error())
		return
	}

	// Validate the inputs we got. Validation happens before the existence
	// check so a bad request shape returns 400/422 even for nonexistent
	// sites — easier to debug.
	if body.MonitorURL != nil {
		if err := validateMonitorURL(*body.MonitorURL); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_url", err.Error())
			return
		}
	}
	if body.RedirectPolicy != nil {
		if _, ok := validRedirectPolicies[*body.RedirectPolicy]; !ok {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_redirect_policy",
				"redirect_policy must be one of: follow, alert, fail")
			return
		}
	}

	ctx := r.Context()
	if !s.ensureSiteVisibleForRequest(w, r, siteID) {
		return
	}
	exists, err := s.siteExists(ctx, siteID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site lookup failed: "+err.Error())
		return
	}
	if !exists {
		writeSiteNotFound(w, r, siteID)
		return
	}

	// Build the UPDATE dynamically from non-nil fields.
	setClauses, args, err := buildUpdateSetClause(body)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_field", err.Error())
		return
	}
	if len(setClauses) == 0 {
		// No fields to change — return the current state without touching the row.
		site, err := s.readSite(ctx, siteID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"read-back failed: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, site)
		return
	}

	args = append(args, siteID)
	query := "UPDATE jetpack_monitor_sites SET " + joinSetClauses(setClauses) + " WHERE blog_id = ?"
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site update failed: "+err.Error())
		return
	}

	site, err := s.readSite(ctx, siteID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"read-back failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, site)
}

// handleDeleteSite implements DELETE /api/v1/sites/{id}.
//
// Soft delete: monitor_active=0 + close any open events with reason
// manual_override. Returns 204 No Content. The row is preserved so audit
// trails (jetmon_audit_log, jetmon_check_history) keep their foreign-key
// targets and historical state remains queryable.
func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_site_id",
			"site id must be a positive integer")
		return
	}

	ctx := r.Context()
	if !s.ensureSiteVisibleForRequest(w, r, siteID) {
		return
	}
	exists, err := s.siteExists(ctx, siteID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site lookup failed: "+err.Error())
		return
	}
	if !exists {
		writeSiteNotFound(w, r, siteID)
		return
	}

	if err := s.closeAllActiveEvents(ctx, siteID, "manual_override", "site deleted via API"); err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"close events failed: "+err.Error())
		return
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE jetpack_monitor_sites SET monitor_active = 0 WHERE blog_id = ?`, siteID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site delete failed: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handlePauseSite implements POST /api/v1/sites/{id}/pause.
//
// Equivalent to monitor_active=false but with the closing reason explicitly
// labeled. The orchestrator's next round will see monitor_active=0 and
// stop checking the site.
func (s *Server) handlePauseSite(w http.ResponseWriter, r *http.Request) {
	s.toggleSiteActive(w, r, false, "site paused via API")
}

// handleResumeSite implements POST /api/v1/sites/{id}/resume.
//
// Sets monitor_active=true. Does not reopen previously-closed events; the
// orchestrator's regular flow will detect any genuine current failure on
// the next round and open a fresh event then.
func (s *Server) handleResumeSite(w http.ResponseWriter, r *http.Request) {
	s.toggleSiteActive(w, r, true, "")
}

func (s *Server) toggleSiteActive(w http.ResponseWriter, r *http.Request, active bool, closeNote string) {
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_site_id",
			"site id must be a positive integer")
		return
	}

	ctx := r.Context()
	if !s.ensureSiteVisibleForRequest(w, r, siteID) {
		return
	}
	exists, err := s.siteExists(ctx, siteID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site lookup failed: "+err.Error())
		return
	}
	if !exists {
		writeSiteNotFound(w, r, siteID)
		return
	}

	// On pause, close any active events first so the site_status projection
	// can move cleanly to "running" (which we then stamp as paused via the
	// monitor_active flag).
	if !active {
		if err := s.closeAllActiveEvents(ctx, siteID, "manual_override", closeNote); err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"close events failed: "+err.Error())
			return
		}
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE jetpack_monitor_sites SET monitor_active = ?, last_status_change = ? WHERE blog_id = ?`,
		boolToTinyint(active), time.Now().UTC(), siteID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site update failed: "+err.Error())
		return
	}

	site, err := s.readSite(ctx, siteID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"read-back failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, site)
}

// closeAllActiveEvents closes every open event for a site in a single tx
// using the eventstore. Used by delete/pause/resume paths and any other
// "the site is going away cleanly" flow.
func (s *Server) closeAllActiveEvents(ctx context.Context, siteID int64, reason, note string) error {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM jetmon_events WHERE blog_id = ? AND ended_at IS NULL`, siteID)
	if err != nil {
		return err
	}
	var eventIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		eventIDs = append(eventIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, eventID := range eventIDs {
		meta, _ := json.Marshal(map[string]any{"note": note, "source": "api"})
		// Use eventstore.Store directly — the api.Server doesn't have a
		// reference to it, but the standalone Close handles its own tx.
		// For now, run the write inline with the orchestrator's eventstore
		// shape.
		if err := s.closeEvent(ctx, eventID, siteID, reason, meta); err != nil {
			return fmt.Errorf("close event %d: %w", eventID, err)
		}
	}
	return nil
}

// closeEvent writes an event close + transition row and, while enabled,
// projects the legacy v1 site_status back to running in one transaction when
// this was the site's last active event.
// Mirrors what eventstore.Tx.Close does without pulling the package in
// here — keeps the import graph flat.
func (s *Server) closeEvent(ctx context.Context, eventID, blogID int64, reason string, metadata []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		severity uint8
		state    string
		endedAt  sql.NullTime
	)
	err = tx.QueryRowContext(ctx,
		`SELECT severity, state, ended_at FROM jetmon_events WHERE id = ? FOR UPDATE`, eventID,
	).Scan(&severity, &state, &endedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("event %d not found", eventID)
		}
		return err
	}
	if endedAt.Valid {
		// Already closed; treat as success — idempotent close.
		return tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE jetmon_events
		   SET ended_at = CURRENT_TIMESTAMP(3),
		       resolution_reason = ?
		 WHERE id = ?`, reason, eventID); err != nil {
		return fmt.Errorf("update event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO jetmon_event_transitions
			(event_id, blog_id, severity_before, severity_after,
			 state_before, state_after, reason, source, metadata)
		VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?)`,
		eventID, blogID, severity, state, "Resolved", reason, "api", metadata,
	); err != nil {
		return fmt.Errorf("insert transition: %w", err)
	}

	if config.LegacyStatusProjectionEnabled() {
		var activeCount int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM jetmon_events WHERE blog_id = ? AND ended_at IS NULL`, blogID,
		).Scan(&activeCount); err != nil {
			return fmt.Errorf("count active events: %w", err)
		}
		if activeCount == 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE jetpack_monitor_sites SET site_status = 1, last_status_change = ? WHERE blog_id = ?`,
				time.Now().UTC(), blogID); err != nil {
				return fmt.Errorf("project site_status: %w", err)
			}
		}
	}
	return tx.Commit()
}

// readSite returns the API-shaped site object for blog_id. Used by the
// write handlers' read-back step.
func (s *Server) readSite(ctx context.Context, blogID int64) (siteResponse, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+siteSelectColumns("", false)+`
		  FROM jetpack_monitor_sites
		 WHERE blog_id = ?`, blogID)
	return scanSiteRow(row, false)
}

// validateMonitorURL accepts only http and https URLs with a non-empty host.
func validateMonitorURL(s string) error {
	if s == "" {
		return errors.New("monitor_url is required")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("monitor_url is not a valid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("monitor_url must use http or https")
	}
	if u.Host == "" {
		return errors.New("monitor_url must include a host")
	}
	return nil
}

// encodeCustomHeaders marshals a map[string]string into the JSON shape the
// custom_headers column expects. Returns nil if the input is nil or empty.
func encodeCustomHeaders(h *map[string]string) (any, error) {
	if h == nil || len(*h) == 0 {
		return nil, nil
	}
	for k := range *h {
		if k == "" {
			return nil, errors.New("custom_headers must not contain empty header names")
		}
	}
	b, err := json.Marshal(*h)
	if err != nil {
		return nil, fmt.Errorf("encode custom_headers: %v", err)
	}
	return string(b), nil
}

// encodeForbiddenKeywords marshals explicit bad-content body strings into the
// JSON array stored in forbidden_keywords. Empty arrays clear the column.
func encodeForbiddenKeywords(values *[]string) (any, error) {
	if values == nil || len(*values) == 0 {
		return nil, nil
	}
	if len(*values) > maxForbiddenKeywords {
		return nil, fmt.Errorf("forbidden_keywords supports at most %d entries", maxForbiddenKeywords)
	}
	out := make([]string, 0, len(*values))
	seen := make(map[string]struct{}, len(*values))
	for _, value := range *values {
		if value == "" {
			return nil, errors.New("forbidden_keywords must not contain empty strings")
		}
		if len([]byte(value)) > maxForbiddenKeywordBytes {
			return nil, fmt.Errorf("forbidden_keywords entries must be %d bytes or fewer", maxForbiddenKeywordBytes)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode forbidden_keywords: %v", err)
	}
	return string(b), nil
}

// buildUpdateSetClause turns a sparse updateSiteRequest into SQL fragments.
// Returned slices are aligned: setClauses[i] applies args[i].
func buildUpdateSetClause(body updateSiteRequest) ([]string, []any, error) {
	var (
		clauses []string
		args    []any
	)
	if body.MonitorURL != nil {
		clauses = append(clauses, "monitor_url = ?")
		args = append(args, *body.MonitorURL)
	}
	if body.MonitorActive != nil {
		clauses = append(clauses, "monitor_active = ?")
		args = append(args, boolToTinyint(*body.MonitorActive))
	}
	if body.BucketNo != nil {
		clauses = append(clauses, "bucket_no = ?")
		args = append(args, *body.BucketNo)
	}
	if body.CheckKeyword != nil {
		clauses = append(clauses, "check_keyword = ?")
		args = append(args, nullableEmpty(*body.CheckKeyword))
	}
	if body.ForbiddenKeyword != nil {
		clauses = append(clauses, "forbidden_keyword = ?")
		args = append(args, nullableEmpty(*body.ForbiddenKeyword))
	}
	if body.ForbiddenKeywords != nil {
		v, err := encodeForbiddenKeywords(body.ForbiddenKeywords)
		if err != nil {
			return nil, nil, err
		}
		clauses = append(clauses, "forbidden_keywords = ?")
		args = append(args, v)
	}
	if body.RedirectPolicy != nil {
		clauses = append(clauses, "redirect_policy = ?")
		args = append(args, *body.RedirectPolicy)
	}
	if body.TimeoutSeconds != nil {
		clauses = append(clauses, "timeout_seconds = ?")
		args = append(args, *body.TimeoutSeconds)
	}
	if body.CustomHeaders != nil {
		v, err := encodeCustomHeaders(body.CustomHeaders)
		if err != nil {
			return nil, nil, err
		}
		clauses = append(clauses, "custom_headers = ?")
		args = append(args, v)
	}
	if body.AlertCooldownMinutes != nil {
		clauses = append(clauses, "alert_cooldown_minutes = ?")
		args = append(args, *body.AlertCooldownMinutes)
	}
	if body.CheckInterval != nil {
		clauses = append(clauses, "check_interval = ?")
		args = append(args, *body.CheckInterval)
	}
	if body.MaintenanceStart != nil {
		t, err := parseMaintenanceTime(*body.MaintenanceStart, "maintenance_start")
		if err != nil {
			return nil, nil, err
		}
		clauses = append(clauses, "maintenance_start = ?")
		args = append(args, t)
	}
	if body.MaintenanceEnd != nil {
		t, err := parseMaintenanceTime(*body.MaintenanceEnd, "maintenance_end")
		if err != nil {
			return nil, nil, err
		}
		clauses = append(clauses, "maintenance_end = ?")
		args = append(args, t)
	}
	return clauses, args, nil
}

// parseMaintenanceTime accepts an empty string (clears the column to NULL)
// or an RFC3339 timestamp. Anything else is a 422.
func parseMaintenanceTime(s, field string) (any, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf("%s must be RFC3339 timestamp or empty string", field)
	}
	return t.UTC(), nil
}

func joinSetClauses(clauses []string) string {
	out := ""
	for i, c := range clauses {
		if i > 0 {
			out += ", "
		}
		out += c
	}
	return out
}

func boolToTinyint(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableStringPtr(p *string) any {
	if p == nil {
		return nil
	}
	return nullableEmpty(*p)
}

func nullableEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableIntPtr(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/eventstore"
)

// siteResponse is the JSON shape for a site in list and single-site responses.
// Field ordering kept human-friendly (id and url first, configuration fields
// after, computed fields last). See API.md "Family 1: Sites and current state".
type siteResponse struct {
	ID                   int64   `json:"id"`
	BlogID               int64   `json:"blog_id"`
	MonitorURL           string  `json:"monitor_url"`
	MonitorActive        bool    `json:"monitor_active"`
	CurrentState         string  `json:"current_state"`
	CurrentSeverity      uint8   `json:"current_severity"`
	ActiveEventID        *int64  `json:"active_event_id"`
	LastCheckedAt        *string `json:"last_checked_at"`
	LastStatusChangeAt   *string `json:"last_status_change_at"`
	SSLExpiryDate        *string `json:"ssl_expiry_date"`
	CheckKeyword         *string `json:"check_keyword"`
	RedirectPolicy       string  `json:"redirect_policy"`
	MaintenanceStart     *string `json:"maintenance_start"`
	MaintenanceEnd       *string `json:"maintenance_end"`
	AlertCooldownMinutes *int    `json:"alert_cooldown_minutes"`
}

// activeEventSummary is the compact event shape embedded in single-site
// responses under "active_events". Full event detail comes from
// GET /api/v1/sites/{id}/events/{event_id}.
type activeEventSummary struct {
	ID        int64  `json:"id"`
	CheckType string `json:"check_type"`
	Severity  uint8  `json:"severity"`
	State     string `json:"state"`
	StartedAt string `json:"started_at"`
}

// singleSiteResponse extends siteResponse with the active_events array.
type singleSiteResponse struct {
	siteResponse
	ActiveEvents []activeEventSummary `json:"active_events"`
}

// handleListSites implements GET /api/v1/sites with cursor pagination.
//
// Cursor encodes the (id) of the last row on the previous page; we use id
// because it's the stable monotonically-increasing primary key. State filter
// is applied post-derivation so consumers see filtering in the same vocabulary
// they read.
func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit, err := parseLimit(q.Get("limit"), 50, 200)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}

	cursor, err := decodeIDCursor(q.Get("cursor"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}

	// Filters.
	stateFilter, err := parseStateFilter(q)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_state_filter", err.Error())
		return
	}
	severityGTE, err := parseUintQuery(q.Get("severity__gte"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_severity", err.Error())
		return
	}
	monitorActive := q.Get("monitor_active")
	urlSubstr := q.Get("q")

	// Build the query. Filter on monitor_active and on URL substring at the SQL
	// level; state/severity filtering happens post-derivation since current
	// state is derived from site_status (and later from active events).
	args := []any{cursor}
	sb := strings.Builder{}
	sb.WriteString(`
		SELECT blog_id, blog_id AS public_id, monitor_url, monitor_active, site_status,
		       last_checked_at, last_status_change, ssl_expiry_date, check_keyword,
		       redirect_policy, maintenance_start, maintenance_end, alert_cooldown_minutes
		  FROM jetpack_monitor_sites
		 WHERE blog_id > ?`)

	switch monitorActive {
	case "true", "1":
		sb.WriteString(" AND monitor_active = 1")
	case "false", "0":
		sb.WriteString(" AND monitor_active = 0")
	case "":
		// no filter
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_monitor_active",
			"monitor_active must be 'true' or 'false'")
		return
	}
	if urlSubstr != "" {
		sb.WriteString(" AND monitor_url LIKE ?")
		args = append(args, "%"+urlSubstr+"%")
	}
	sb.WriteString(" ORDER BY blog_id ASC LIMIT ?")
	// Fetch limit+1 so we know whether there's a next page without an extra count query.
	args = append(args, limit+1)

	ctx := r.Context()
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site list query failed: "+err.Error())
		return
	}
	defer rows.Close()

	results := make([]siteResponse, 0, limit)
	var lastID int64
	for rows.Next() {
		s, err := scanSiteRow(rows)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"site row scan failed: "+err.Error())
			return
		}
		results = append(results, s)
		lastID = s.ID
	}
	rawCount := len(results)
	rawLastID := lastID
	fetchedMore := rawCount > limit

	if err := s.applyActiveEventRollups(ctx, results); err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"active event rollup query failed: "+err.Error())
		return
	}

	// Apply post-derivation filters after active events have been reflected
	// into the response. This keeps the API correct after legacy projection
	// writes are disabled.
	results = filterByState(results, stateFilter, severityGTE)

	// Trim to the requested limit and decide on next-cursor.
	var nextCursor *string
	if len(results) > limit {
		results = results[:limit]
		lastID = results[len(results)-1].ID
		c := encodeIDCursor(lastID)
		nextCursor = &c
	} else if fetchedMore {
		c := encodeIDCursor(rawLastID)
		nextCursor = &c
	}

	writeJSON(w, http.StatusOK, ListEnvelope{
		Data: results,
		Page: Page{Next: nextCursor, Limit: limit},
	})
}

// handleGetSite implements GET /api/v1/sites/{id}. Returns the site plus
// any open events as active_events, ordered by severity descending.
func (s *Server) handleGetSite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_site_id",
			"site id must be a positive integer")
		return
	}

	ctx := r.Context()
	row := s.db.QueryRowContext(ctx, `
		SELECT blog_id, blog_id AS public_id, monitor_url, monitor_active, site_status,
		       last_checked_at, last_status_change, ssl_expiry_date, check_keyword,
		       redirect_policy, maintenance_start, maintenance_end, alert_cooldown_minutes
		  FROM jetpack_monitor_sites
		 WHERE blog_id = ?`, id)

	site, err := scanSiteRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "site_not_found",
				fmt.Sprintf("Site %d does not exist", id))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site query failed: "+err.Error())
		return
	}

	active, err := s.queryActiveEvents(ctx, id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"active events query failed: "+err.Error())
		return
	}

	// Reflect the worst active event back into the site projection for
	// consumers reading from this single endpoint. Falls back to the v1
	// site_status mapping when there's no event (e.g. fresh site that hasn't
	// been checked yet).
	if len(active) > 0 {
		worst := active[0]
		site.CurrentSeverity = worst.Severity
		site.CurrentState = worst.State
		eventID := worst.ID
		site.ActiveEventID = &eventID
	}

	writeJSON(w, http.StatusOK, singleSiteResponse{
		siteResponse: site,
		ActiveEvents: active,
	})
}

// queryActiveEvents returns all open events for a site, ordered by severity
// desc then started_at asc. Used by the single-site endpoint.
func (s *Server) queryActiveEvents(ctx context.Context, blogID int64) ([]activeEventSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, check_type, severity, state, started_at
		  FROM jetmon_events
		 WHERE blog_id = ? AND ended_at IS NULL
		 ORDER BY severity DESC, started_at ASC`, blogID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []activeEventSummary{}
	for rows.Next() {
		var e activeEventSummary
		var startedAt time.Time
		if err := rows.Scan(&e.ID, &e.CheckType, &e.Severity, &e.State, &startedAt); err != nil {
			return nil, err
		}
		e.StartedAt = startedAt.UTC().Format(time.RFC3339Nano)
		out = append(out, e)
	}
	return out, rows.Err()
}

type activeEventRollup struct {
	id        int64
	severity  uint8
	state     string
	startedAt time.Time
}

// applyActiveEventRollups reflects each site's worst open event into list
// responses. List queries still page through jetpack_monitor_sites because that
// remains the site/config table during migration, but current state comes from
// v2 events when an event is open.
//
// The query intentionally avoids window functions so it stays compatible with
// MySQL 5.7. Pagination caps the IN list at the API's max page size, and a
// site rarely has more than one open event, so reducing in Go is cheap.
func (s *Server) applyActiveEventRollups(ctx context.Context, sites []siteResponse) error {
	if len(sites) == 0 {
		return nil
	}
	ids := make([]any, 0, len(sites))
	placeholders := make([]string, 0, len(sites))
	for _, site := range sites {
		ids = append(ids, site.BlogID)
		placeholders = append(placeholders, "?")
	}

	q := fmt.Sprintf(`
		SELECT id, blog_id, severity, state, started_at
		  FROM jetmon_events
		 WHERE ended_at IS NULL
		   AND blog_id IN (%s)`, strings.Join(placeholders, ","))

	rows, err := s.db.QueryContext(ctx, q, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()

	rollups := make(map[int64]activeEventRollup)
	for rows.Next() {
		var blogID int64
		var r activeEventRollup
		if err := rows.Scan(&r.id, &blogID, &r.severity, &r.state, &r.startedAt); err != nil {
			return err
		}
		existing, ok := rollups[blogID]
		if !ok ||
			r.severity > existing.severity ||
			(r.severity == existing.severity && r.startedAt.Before(existing.startedAt)) {
			rollups[blogID] = r
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for i := range sites {
		r, ok := rollups[sites[i].BlogID]
		if !ok {
			continue
		}
		sites[i].CurrentSeverity = r.severity
		sites[i].CurrentState = r.state
		eventID := r.id
		sites[i].ActiveEventID = &eventID
	}
	return nil
}

// rowScanner accepts both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanSiteRow scans the columns selected by the site queries into a
// siteResponse. SiteStatus is not exposed directly; it is used only as a
// fallback for sites with no active v2 event during the shadow migration.
func scanSiteRow(s rowScanner) (siteResponse, error) {
	var (
		out            siteResponse
		monitorActive  uint8
		siteStatus     int
		lastCheckedAt  sql.NullTime
		lastStatusChg  sql.NullTime
		sslExpiry      sql.NullTime
		checkKeyword   sql.NullString
		redirectPolicy sql.NullString
		maintStart     sql.NullTime
		maintEnd       sql.NullTime
		alertCooldown  sql.NullInt64
	)
	if err := s.Scan(
		&out.ID, &out.BlogID, &out.MonitorURL, &monitorActive, &siteStatus,
		&lastCheckedAt, &lastStatusChg, &sslExpiry, &checkKeyword,
		&redirectPolicy, &maintStart, &maintEnd, &alertCooldown,
	); err != nil {
		return out, err
	}
	out.MonitorActive = monitorActive == 1
	if config.LegacyStatusProjectionEnabled() {
		out.CurrentState, out.CurrentSeverity = deriveStateFromSiteStatus(siteStatus)
	} else {
		out.CurrentState, out.CurrentSeverity = eventstore.StateUp, eventstore.SeverityUp
	}
	if lastCheckedAt.Valid {
		v := lastCheckedAt.Time.UTC().Format(time.RFC3339)
		out.LastCheckedAt = &v
	}
	if lastStatusChg.Valid {
		v := lastStatusChg.Time.UTC().Format(time.RFC3339)
		out.LastStatusChangeAt = &v
	}
	if sslExpiry.Valid {
		v := sslExpiry.Time.UTC().Format("2006-01-02")
		out.SSLExpiryDate = &v
	}
	if checkKeyword.Valid {
		out.CheckKeyword = &checkKeyword.String
	}
	if redirectPolicy.Valid {
		out.RedirectPolicy = redirectPolicy.String
	} else {
		out.RedirectPolicy = "follow"
	}
	if maintStart.Valid {
		v := maintStart.Time.UTC().Format(time.RFC3339)
		out.MaintenanceStart = &v
	}
	if maintEnd.Valid {
		v := maintEnd.Time.UTC().Format(time.RFC3339)
		out.MaintenanceEnd = &v
	}
	if alertCooldown.Valid {
		v := int(alertCooldown.Int64)
		out.AlertCooldownMinutes = &v
	}
	return out, nil
}

// deriveStateFromSiteStatus maps the v1 site_status integer to the v2
// (current_state, current_severity) tuple. It is only a fallback when there is
// no active v2 event for the site (fresh sites, or legacy-only rows during
// migration).
//
// Mapping (matches AGENTS.md):
//   - 0 (SITE_DOWN) → Seems Down, severity 3
//   - 1 (SITE_RUNNING) → Up, severity 0
//   - 2 (SITE_CONFIRMED_DOWN) → Down, severity 4
//   - other → Unknown, severity 0
func deriveStateFromSiteStatus(siteStatus int) (state string, severity uint8) {
	switch siteStatus {
	case 0:
		return "Seems Down", 3
	case 1:
		return "Up", 0
	case 2:
		return "Down", 4
	default:
		return "Unknown", 0
	}
}

// parseStateFilter returns the state values requested via ?state=X or
// ?state__in=A,B,C (mutually exclusive — only one or the other).
func parseStateFilter(q map[string][]string) ([]string, error) {
	single := first(q["state"])
	multi := first(q["state__in"])
	if single != "" && multi != "" {
		return nil, fmt.Errorf("use either ?state= or ?state__in=, not both")
	}
	if single != "" {
		return []string{single}, nil
	}
	if multi != "" {
		return strings.Split(multi, ","), nil
	}
	return nil, nil
}

// filterByState applies state and severity__gte filters in-memory after the
// SQL query. Cheap because the SQL query already bounds the result to the page
// limit.
func filterByState(in []siteResponse, states []string, severityGTE int) []siteResponse {
	if len(states) == 0 && severityGTE <= 0 {
		return in
	}
	stateSet := make(map[string]struct{}, len(states))
	for _, s := range states {
		stateSet[s] = struct{}{}
	}
	out := in[:0]
	for _, s := range in {
		if len(stateSet) > 0 {
			if _, ok := stateSet[s.CurrentState]; !ok {
				continue
			}
		}
		if int(s.CurrentSeverity) < severityGTE {
			continue
		}
		out = append(out, s)
	}
	return out
}

// parseLimit returns a clamped limit value for list endpoints. Empty falls
// back to defaultLimit; values above maxLimit are clamped silently (the API
// docs say so, and a 400 here would be hostile to common pagination loops).
func parseLimit(s string, defaultLimit, maxLimit int) (int, error) {
	if s == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("limit must be an integer")
	}
	if n < 1 {
		return 0, fmt.Errorf("limit must be >= 1")
	}
	if n > maxLimit {
		n = maxLimit
	}
	return n, nil
}

// parseUintQuery parses an optional unsigned int query parameter. Empty
// returns 0 with no error.
func parseUintQuery(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("must be a non-negative integer")
	}
	return n, nil
}

func first(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// idCursor is the cursor schema for list endpoints keyed on a single int64
// (id, blog_id). Encoded as base64-JSON so consumers don't poke at internals.
type idCursor struct {
	ID int64 `json:"id"`
}

func encodeIDCursor(id int64) string {
	b, _ := json.Marshal(idCursor{ID: id})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeIDCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, fmt.Errorf("cursor not valid base64: %v", err)
	}
	var c idCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return 0, fmt.Errorf("cursor not valid JSON: %v", err)
	}
	return c.ID, nil
}

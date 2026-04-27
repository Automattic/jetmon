package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// eventResponse is the JSON shape for an event in list and detail responses.
// Field ordering loosely matches the schema (jetmon_events): identity first,
// then severity/state, then timing, then closure data, then metadata.
type eventResponse struct {
	ID               int64           `json:"id"`
	SiteID           int64           `json:"site_id"`
	EndpointID       *int64          `json:"endpoint_id"`
	CheckType        string          `json:"check_type"`
	Discriminator    *string         `json:"discriminator"`
	Severity         uint8           `json:"severity"`
	State            string          `json:"state"`
	StartedAt        string          `json:"started_at"`
	EndedAt          *string         `json:"ended_at"`
	ResolutionReason *string         `json:"resolution_reason"`
	CauseEventID     *int64          `json:"cause_event_id"`
	Metadata         json.RawMessage `json:"metadata"`
	DurationMs       int64           `json:"duration_ms"`
	TransitionCount  int             `json:"transition_count"`
}

// transitionResponse is one row from jetmon_event_transitions.
type transitionResponse struct {
	ID             int64           `json:"id"`
	EventID        int64           `json:"event_id"`
	SeverityBefore *uint8          `json:"severity_before"`
	SeverityAfter  *uint8          `json:"severity_after"`
	StateBefore    *string         `json:"state_before"`
	StateAfter     *string         `json:"state_after"`
	Reason         string          `json:"reason"`
	Source         string          `json:"source"`
	Metadata       json.RawMessage `json:"metadata"`
	ChangedAt      string          `json:"changed_at"`
}

// eventDetailResponse is the single-event response with embedded transitions.
type eventDetailResponse struct {
	eventResponse
	Transitions []transitionResponse `json:"transitions"`
}

// handleListSiteEvents implements GET /api/v1/sites/{id}/events.
func (s *Server) handleListSiteEvents(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_site_id",
			"site id must be a positive integer")
		return
	}
	s.listEvents(w, r, &siteID)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request, siteID *int64) {
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
	checkTypeFilter := parseCSV(q, "check_type", "check_type__in")
	startedGTE, err := parseTimeQuery(q.Get("started_at__gte"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_started_at__gte", err.Error())
		return
	}
	startedLT, err := parseTimeQuery(q.Get("started_at__lt"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_started_at__lt", err.Error())
		return
	}

	// activeFilter: true → only open, false → only closed, "" → both.
	var activeFilter *bool
	switch q.Get("active") {
	case "true", "1":
		t := true
		activeFilter = &t
	case "false", "0":
		f := false
		activeFilter = &f
	case "":
		// no filter
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_active",
			"active must be 'true' or 'false'")
		return
	}

	// Build the query. Events list walks backwards on id (id desc) — id is
	// monotonically increasing because it's an auto-increment PK, so id desc
	// matches started_at desc within the resolution we care about.
	args := []any{}
	sb := strings.Builder{}
	sb.WriteString(`
		SELECT id, blog_id, endpoint_id, check_type, discriminator,
		       severity, state, started_at, ended_at, resolution_reason,
		       cause_event_id, metadata
		  FROM jetmon_events
		 WHERE 1=1`)

	if siteID != nil {
		sb.WriteString(" AND blog_id = ?")
		args = append(args, *siteID)
	}
	if cursor > 0 {
		sb.WriteString(" AND id < ?")
		args = append(args, cursor)
	}
	if len(stateFilter) > 0 {
		sb.WriteString(" AND state IN (")
		for i, v := range stateFilter {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, v)
		}
		sb.WriteString(")")
	}
	if len(checkTypeFilter) > 0 {
		sb.WriteString(" AND check_type IN (")
		for i, v := range checkTypeFilter {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, v)
		}
		sb.WriteString(")")
	}
	if startedGTE != nil {
		sb.WriteString(" AND started_at >= ?")
		args = append(args, *startedGTE)
	}
	if startedLT != nil {
		sb.WriteString(" AND started_at < ?")
		args = append(args, *startedLT)
	}
	if activeFilter != nil {
		if *activeFilter {
			sb.WriteString(" AND ended_at IS NULL")
		} else {
			sb.WriteString(" AND ended_at IS NOT NULL")
		}
	}

	sb.WriteString(" ORDER BY id DESC LIMIT ?")
	args = append(args, limit+1)

	ctx := r.Context()
	rows, err := s.db.QueryContext(ctx, sb.String(), args...)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"event list query failed: "+err.Error())
		return
	}
	defer rows.Close()

	results := make([]eventResponse, 0, limit)
	for rows.Next() {
		ev, err := scanEventRow(rows)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"event row scan failed: "+err.Error())
			return
		}
		results = append(results, ev)
	}

	// Compute transition_count for each event in one batch query. Avoid n+1.
	if len(results) > 0 {
		ids := make([]any, len(results))
		for i, e := range results {
			ids[i] = e.ID
		}
		counts, err := s.queryTransitionCounts(ctx, ids)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"transition count query failed: "+err.Error())
			return
		}
		for i := range results {
			results[i].TransitionCount = counts[results[i].ID]
		}
	}

	var nextCursor *string
	if len(results) > limit {
		results = results[:limit]
		c := encodeIDCursor(results[len(results)-1].ID)
		nextCursor = &c
	}

	writeJSON(w, http.StatusOK, ListEnvelope{
		Data: results,
		Page: Page{Next: nextCursor, Limit: limit},
	})
}

// handleGetEventBySite implements GET /api/v1/sites/{id}/events/{event_id}.
// Validates that the event belongs to the named site so /sites/X/events/Y
// can't sneakily access an event from a different site.
func (s *Server) handleGetEventBySite(w http.ResponseWriter, r *http.Request) {
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
	s.respondEvent(w, r, eventID, &siteID)
}

// handleGetEvent implements GET /api/v1/events/{event_id}, the standalone
// lookup. Useful for webhook payloads that want to link directly to an
// incident page without the consumer needing the site id.
func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	eventID, err := strconv.ParseInt(r.PathValue("event_id"), 10, 64)
	if err != nil || eventID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_event_id",
			"event id must be a positive integer")
		return
	}
	s.respondEvent(w, r, eventID, nil)
}

func (s *Server) respondEvent(w http.ResponseWriter, r *http.Request, eventID int64, siteIDFilter *int64) {
	ctx := r.Context()
	row := s.db.QueryRowContext(ctx, `
		SELECT id, blog_id, endpoint_id, check_type, discriminator,
		       severity, state, started_at, ended_at, resolution_reason,
		       cause_event_id, metadata
		  FROM jetmon_events
		 WHERE id = ?`, eventID)

	ev, err := scanEventRow(row)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "event_not_found",
				fmt.Sprintf("Event %d does not exist", eventID))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"event query failed: "+err.Error())
		return
	}
	if siteIDFilter != nil && ev.SiteID != *siteIDFilter {
		writeError(w, r, http.StatusNotFound, "event_not_found",
			fmt.Sprintf("Event %d does not belong to site %d", eventID, *siteIDFilter))
		return
	}

	transitions, err := s.queryTransitions(ctx, eventID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"transition query failed: "+err.Error())
		return
	}
	ev.TransitionCount = len(transitions)

	writeJSON(w, http.StatusOK, eventDetailResponse{
		eventResponse: ev,
		Transitions:   transitions,
	})
}

// handleListTransitions implements GET /api/v1/sites/{id}/events/{event_id}/transitions.
// Useful when an event has accumulated many transitions and the inline list
// in the event detail response is too large.
func (s *Server) handleListTransitions(w http.ResponseWriter, r *http.Request) {
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

	// Verify the event exists and belongs to the site before we paginate.
	var blogID int64
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT blog_id FROM jetmon_events WHERE id = ?`, eventID).Scan(&blogID); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "event_not_found",
				fmt.Sprintf("Event %d does not exist", eventID))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"event lookup failed: "+err.Error())
		return
	}
	if blogID != siteID {
		writeError(w, r, http.StatusNotFound, "event_not_found",
			fmt.Sprintf("Event %d does not belong to site %d", eventID, siteID))
		return
	}

	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"), 100, 200)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	cursor, err := decodeIDCursor(q.Get("cursor"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}

	args := []any{eventID}
	query := `
		SELECT id, event_id, severity_before, severity_after,
		       state_before, state_after, reason, source, metadata, changed_at
		  FROM jetmon_event_transitions
		 WHERE event_id = ?`
	if cursor > 0 {
		query += " AND id > ?"
		args = append(args, cursor)
	}
	query += " ORDER BY id ASC LIMIT ?"
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"transition list query failed: "+err.Error())
		return
	}
	defer rows.Close()

	results := make([]transitionResponse, 0, limit)
	for rows.Next() {
		t, err := scanTransitionRow(rows)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"transition row scan failed: "+err.Error())
			return
		}
		results = append(results, t)
	}

	var nextCursor *string
	if len(results) > limit {
		results = results[:limit]
		c := encodeIDCursor(results[len(results)-1].ID)
		nextCursor = &c
	}
	writeJSON(w, http.StatusOK, ListEnvelope{
		Data: results,
		Page: Page{Next: nextCursor, Limit: limit},
	})
}

// queryTransitions returns all transitions for an event in chronological order.
// Used by the single-event endpoint where the count is bounded.
func (s *Server) queryTransitions(ctx context.Context, eventID int64) ([]transitionResponse, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_id, severity_before, severity_after,
		       state_before, state_after, reason, source, metadata, changed_at
		  FROM jetmon_event_transitions
		 WHERE event_id = ?
		 ORDER BY id ASC`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []transitionResponse{}
	for rows.Next() {
		t, err := scanTransitionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// queryTransitionCounts batches the transition count for many events into a
// single GROUP BY query — avoids n+1 lookups when listing events.
func (s *Server) queryTransitionCounts(ctx context.Context, eventIDs []any) (map[int64]int, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(eventIDs)-1) + "?"
	rows, err := s.db.QueryContext(ctx,
		`SELECT event_id, COUNT(*) FROM jetmon_event_transitions
		  WHERE event_id IN (`+placeholders+`)
		  GROUP BY event_id`, eventIDs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]int, len(eventIDs))
	for rows.Next() {
		var id int64
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		out[id] = count
	}
	return out, rows.Err()
}

func scanEventRow(s rowScanner) (eventResponse, error) {
	var (
		out              eventResponse
		endpointID       sql.NullInt64
		discriminator    sql.NullString
		startedAt        time.Time
		endedAt          sql.NullTime
		resolutionReason sql.NullString
		causeEventID     sql.NullInt64
		metadata         sql.NullString
	)
	if err := s.Scan(
		&out.ID, &out.SiteID, &endpointID, &out.CheckType, &discriminator,
		&out.Severity, &out.State, &startedAt, &endedAt, &resolutionReason,
		&causeEventID, &metadata,
	); err != nil {
		return out, err
	}
	if endpointID.Valid {
		out.EndpointID = &endpointID.Int64
	}
	if discriminator.Valid {
		out.Discriminator = &discriminator.String
	}
	out.StartedAt = startedAt.UTC().Format(time.RFC3339Nano)

	now := time.Now().UTC()
	if endedAt.Valid {
		out.EndedAt = ptrStr(endedAt.Time.UTC().Format(time.RFC3339Nano))
		out.DurationMs = endedAt.Time.Sub(startedAt).Milliseconds()
	} else {
		out.DurationMs = now.Sub(startedAt).Milliseconds()
	}
	if resolutionReason.Valid {
		out.ResolutionReason = &resolutionReason.String
	}
	if causeEventID.Valid {
		out.CauseEventID = &causeEventID.Int64
	}
	if metadata.Valid && metadata.String != "" {
		out.Metadata = json.RawMessage(metadata.String)
	} else {
		out.Metadata = json.RawMessage("null")
	}
	return out, nil
}

func scanTransitionRow(s rowScanner) (transitionResponse, error) {
	var (
		out            transitionResponse
		severityBefore sql.NullInt64
		severityAfter  sql.NullInt64
		stateBefore    sql.NullString
		stateAfter     sql.NullString
		metadata       sql.NullString
		changedAt      time.Time
	)
	if err := s.Scan(
		&out.ID, &out.EventID, &severityBefore, &severityAfter,
		&stateBefore, &stateAfter, &out.Reason, &out.Source, &metadata, &changedAt,
	); err != nil {
		return out, err
	}
	if severityBefore.Valid {
		v := uint8(severityBefore.Int64)
		out.SeverityBefore = &v
	}
	if severityAfter.Valid {
		v := uint8(severityAfter.Int64)
		out.SeverityAfter = &v
	}
	if stateBefore.Valid {
		out.StateBefore = &stateBefore.String
	}
	if stateAfter.Valid {
		out.StateAfter = &stateAfter.String
	}
	if metadata.Valid && metadata.String != "" {
		out.Metadata = json.RawMessage(metadata.String)
	} else {
		out.Metadata = json.RawMessage("null")
	}
	out.ChangedAt = changedAt.UTC().Format(time.RFC3339Nano)
	return out, nil
}

// parseCSV returns the union of values from ?key= and ?key__in=A,B,C, or nil
// if neither was provided. Used for state and check_type filters.
func parseCSV(q map[string][]string, single, multi string) []string {
	if v := first(q[single]); v != "" {
		return []string{v}
	}
	if v := first(q[multi]); v != "" {
		return strings.Split(v, ",")
	}
	return nil
}

// parseTimeQuery parses an optional ISO8601 timestamp query parameter.
func parseTimeQuery(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf("must be RFC3339 timestamp")
	}
	return &t, nil
}

func ptrStr(s string) *string { return &s }

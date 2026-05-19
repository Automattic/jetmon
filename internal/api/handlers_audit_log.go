package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// auditLogRow is one jetmon_audit_log row in API form. blog_id and event_id
// are NULL in the table for system-level events (e.g. config_change); we
// surface them as nullable pointers so consumers can distinguish "not linked"
// from "linked to row 0".
type auditLogRow struct {
	ID        int64           `json:"id"`
	BlogID    *int64          `json:"blog_id"`
	EventID   *int64          `json:"event_id"`
	EventType string          `json:"event_type"`
	Source    string          `json:"source"`
	Detail    string          `json:"detail,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt string          `json:"created_at"`
}

// handleListAuditLog implements GET /api/v1/audit-log — operator-facing
// query API over the audit trail. Replaces the previous "log into MySQL and
// SELECT" workflow with a paginated, filterable read endpoint.
//
// Filters: blog_id (exact), event_id (exact), event_type (CSV — repeat with
// commas or use the event_type__in alias), source (exact), since / until
// (RFC3339). Newest-first by id. Pagination via opaque cursor matching the
// rest of the API.
func (s *Server) handleListAuditLog(w http.ResponseWriter, r *http.Request) {
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

	var blogID *int64
	if raw := q.Get("blog_id"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_blog_id",
				"blog_id must be a positive integer")
			return
		}
		blogID = &v
	}

	var eventID *int64
	if raw := q.Get("event_id"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_event_id",
				"event_id must be a positive integer")
			return
		}
		eventID = &v
	}

	eventTypes := parseCSV(q, "event_type", "event_type__in")
	source := strings.TrimSpace(q.Get("source"))

	since, err := parseTimeQuery(q.Get("since"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_since", err.Error())
		return
	}
	until, err := parseTimeQuery(q.Get("until"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_until", err.Error())
		return
	}

	args := make([]any, 0, 8)
	sb := strings.Builder{}
	sb.WriteString(`
		SELECT id, blog_id, event_id, event_type, source, detail, metadata, created_at
		  FROM jetmon_audit_log
		 WHERE 1=1`)

	if cursor > 0 {
		sb.WriteString(" AND id < ?")
		args = append(args, cursor)
	}
	if blogID != nil {
		sb.WriteString(" AND blog_id = ?")
		args = append(args, *blogID)
	}
	if eventID != nil {
		sb.WriteString(" AND event_id = ?")
		args = append(args, *eventID)
	}
	if len(eventTypes) > 0 {
		sb.WriteString(" AND event_type IN (")
		for i, v := range eventTypes {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("?")
			args = append(args, v)
		}
		sb.WriteString(")")
	}
	if source != "" {
		sb.WriteString(" AND source = ?")
		args = append(args, source)
	}
	if since != nil {
		sb.WriteString(" AND created_at >= ?")
		args = append(args, *since)
	}
	if until != nil {
		sb.WriteString(" AND created_at < ?")
		args = append(args, *until)
	}

	sb.WriteString(" ORDER BY id DESC LIMIT ?")
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(r.Context(), sb.String(), args...)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"audit log query failed: "+err.Error())
		return
	}
	defer rows.Close()

	results := make([]auditLogRow, 0, limit)
	for rows.Next() {
		row, err := scanAuditLogRow(rows)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "db_error",
				"audit log row scan failed: "+err.Error())
			return
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"audit log iteration failed: "+err.Error())
		return
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

func scanAuditLogRow(rows *sql.Rows) (auditLogRow, error) {
	var (
		row       auditLogRow
		blogID    sql.NullInt64
		eventID   sql.NullInt64
		detail    sql.NullString
		metadata  sql.NullString
		createdAt time.Time
	)
	if err := rows.Scan(&row.ID, &blogID, &eventID, &row.EventType, &row.Source, &detail, &metadata, &createdAt); err != nil {
		return auditLogRow{}, fmt.Errorf("scan audit row: %w", err)
	}
	if blogID.Valid {
		v := blogID.Int64
		row.BlogID = &v
	}
	if eventID.Valid {
		v := eventID.Int64
		row.EventID = &v
	}
	if detail.Valid {
		row.Detail = detail.String
	}
	if metadata.Valid && strings.TrimSpace(metadata.String) != "" {
		row.Metadata = json.RawMessage(metadata.String)
	}
	row.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	return row, nil
}

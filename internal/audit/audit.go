// Package audit writes the operational trail to jetmon_audit_log: WPCOM
// notification sends and retries, verifier RPC dispatch, retry-queue dispatch,
// alert and maintenance suppression decisions, and config reloads. These are
// things the monitor *did*, not things that happened to a site.
//
// Site-state changes (incidents opening, severity escalating, state changing,
// events closing) flow through the eventstore package and the
// jetmon_events / jetmon_event_transitions tables. They do not go through this
// package. See docs/events.md for the split.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Event types written to jetmon_audit_log. All values are operational — none
// of them describe site state directly. Site-state transitions live in
// jetmon_event_transitions.
const (
	EventWPCOMSent         = "wpcom_sent"
	EventWPCOMRetry        = "wpcom_retry"
	EventRetryDispatched   = "retry_dispatched"
	EventVeriflierSent     = "veriflier_sent"
	EventMaintenanceActive = "maintenance_active"
	EventAlertSuppressed   = "alert_suppressed"
	EventConfigChange      = "config_change"
	EventAPIAccess         = "api_access"
)

var db *sql.DB

// Init sets the database connection used by the audit log.
func Init(conn *sql.DB) {
	db = conn
}

// Entry carries the fields written for one audit row. blog_id and event_id are
// both optional: system-level events (e.g. config reloads) carry neither, and
// most operational rows for a site carry blog_id but not event_id. Linking a
// row to an event id (e.g. "this WPCOM retry was for event 12345") lets
// operators pivot from incident → operational context with one query.
type Entry struct {
	BlogID    int64           // 0 for system-level events; written as NULL
	EventID   int64           // 0 if not linked to an incident; written as NULL
	EventType string          // one of the Event* constants above
	Source    string          // "local", "veriflier:us-west", "operator:user@host", …
	Detail    string          // human-readable one-liner; truncated at 1024 chars
	Metadata  json.RawMessage // optional structured context (e.g. retry attempt, region)
}

// Log writes an entry to jetmon_audit_log. ctx propagates cancellation and
// deadlines into the underlying INSERT. Callers control the context lifetime:
// the orchestrator passes its long-lived shutdown context; the API middleware
// uses a short bounded timeout derived from context.Background so audits fire
// regardless of client disconnect but cannot block on a wedged DB.
func Log(ctx context.Context, e Entry) error {
	if db == nil {
		return nil
	}
	if e.EventType == "" {
		return fmt.Errorf("audit: EventType is required")
	}
	source := e.Source
	if source == "" {
		source = "local"
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO jetmon_audit_log
			(blog_id, event_id, event_type, source, detail, metadata)
		VALUES (?, ?, ?, ?, ?, ?)`,
		nullableInt64(e.BlogID),
		nullableInt64(e.EventID),
		e.EventType,
		source,
		nullableString(e.Detail),
		nullableJSON(e.Metadata),
	)
	if err != nil {
		return fmt.Errorf("audit log insert: %w", err)
	}
	return nil
}

// Query returns audit log entries for a blog within the given time range.
// The caller must close the returned *sql.Rows.
func Query(db *sql.DB, blogID int64, since, until string) (*sql.Rows, error) {
	q := `SELECT id, blog_id, event_id, event_type, source, detail, metadata, created_at
	      FROM jetmon_audit_log
	      WHERE blog_id = ?`
	args := []any{blogID}

	if since != "" {
		q += " AND created_at >= ?"
		args = append(args, since)
	}
	if until != "" {
		q += " AND created_at <= ?"
		args = append(args, until)
	}
	q += " ORDER BY created_at ASC"

	return db.Query(q, args...)
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

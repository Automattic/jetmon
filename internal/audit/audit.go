package audit

import (
	"database/sql"
	"fmt"
)

// Event types written to jetmon_audit_log.
const (
	EventCheck            = "check"
	EventStatusTransition = "status_transition"
	EventWPCOMSent        = "wpcom_sent"
	EventWPCOMRetry       = "wpcom_retry"
	EventRetryDispatched  = "retry_dispatched"
	EventVeriflierSent    = "veriflier_sent"
	EventVeriflierResult  = "veriflier_result"
	EventMaintenanceActive = "maintenance_active"
	EventAlertSuppressed  = "alert_suppressed"
	EventConfigChange     = "config_change"
)

var db *sql.DB

// Init sets the database connection used by the audit log.
func Init(conn *sql.DB) {
	db = conn
}

// Log writes an event to jetmon_audit_log.
func Log(blogID int64, eventType, source string, httpCode, errorCode int, rttMs int64, detail string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(
		`INSERT INTO jetmon_audit_log
		    (blog_id, event_type, source, http_code, error_code, rtt_ms, detail)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		blogID, eventType, source,
		nullInt(httpCode), nullInt(errorCode), nullInt64(rttMs),
		nullString(detail),
	)
	if err != nil {
		return fmt.Errorf("audit log insert: %w", err)
	}
	return nil
}

// LogTransition writes a status transition event.
func LogTransition(blogID int64, oldStatus, newStatus int, reason string) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(
		`INSERT INTO jetmon_audit_log
		    (blog_id, event_type, source, old_status, new_status, detail)
		 VALUES (?, ?, 'local', ?, ?, ?)`,
		blogID, EventStatusTransition, oldStatus, newStatus, reason,
	)
	return err
}

// Query returns audit log entries for a blog within the given time range.
// The caller must close the returned *sql.Rows.
func Query(db *sql.DB, blogID int64, since, until string) (*sql.Rows, error) {
	q := `SELECT id, blog_id, event_type, source, http_code, error_code, rtt_ms,
	             old_status, new_status, detail, created_at
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

func nullInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

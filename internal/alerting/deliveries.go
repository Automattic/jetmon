package alerting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Delivery is the in-memory shape of a jetmon_alert_deliveries row.
type Delivery struct {
	ID             int64
	AlertContactID int64
	TransitionID   int64
	EventID        int64
	EventType      string
	Severity       uint8
	Payload        json.RawMessage
	Status         Status
	Attempt        int
	NextAttemptAt  *time.Time
	LastStatusCode *int
	LastResponse   *string
	LastAttemptAt  *time.Time
	DeliveredAt    *time.Time
	CreatedAt      time.Time
}

// EnqueueInput carries everything needed to insert a delivery row.
type EnqueueInput struct {
	AlertContactID int64
	TransitionID   int64
	EventID        int64
	EventType      string
	Severity       uint8
	Payload        json.RawMessage
}

// Enqueue inserts a pending delivery with attempt=0 and
// next_attempt_at=now. Uses INSERT IGNORE against the
// (alert_contact_id, transition_id) UNIQUE KEY so concurrent
// dispatchers don't create duplicate deliveries. Returns the new id,
// or 0 if the row was a duplicate.
func Enqueue(ctx context.Context, db *sql.DB, in EnqueueInput) (int64, error) {
	res, err := db.ExecContext(ctx, `
		INSERT IGNORE INTO jetmon_alert_deliveries
			(alert_contact_id, transition_id, event_id, event_type, severity,
			 payload, status, attempt, next_attempt_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, CURRENT_TIMESTAMP)`,
		in.AlertContactID, in.TransitionID, in.EventID, in.EventType, in.Severity,
		[]byte(in.Payload),
	)
	if err != nil {
		return 0, fmt.Errorf("alerting: enqueue delivery: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("alerting: last insert id: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return 0, nil
	}
	return id, nil
}

// claimLockDuration is how far ClaimReady pushes next_attempt_at out
// when it claims a row. Must outlast the worker's per-delivery wall
// clock so an in-flight goroutine has time to write its real result
// before the soft lock would expire. The default DispatchTimeout is
// 30s with a 5s buffer; 60s gives comfortable headroom. A crashed
// goroutine that never updates the row recovers naturally when the
// lock expires.
const claimLockDuration = 60 * time.Second

// ClaimReady returns up to limit pending deliveries whose
// next_attempt_at is in the past. Each claimed row is soft-locked by
// pushing next_attempt_at to NOW + claimLockDuration so subsequent
// ticks don't re-claim a row whose dispatch is still in-flight. The
// dispatch goroutine overwrites next_attempt_at with its real value
// when it finishes.
//
// Without the soft lock, the deliver loop's 1-second tick re-claims
// any in-flight row up to the per-contact cap, producing concurrent
// dispatches that inflate the attempt counter and effectively skip
// retry-schedule steps. The soft lock prevents that.
//
// Multi-instance caveat: same as webhooks — two instances polling
// simultaneously can still pick up a row in the SELECT phase. The readiness
// predicate on UPDATE and the RowsAffected check below mean only rows that won
// the soft lock are returned, but fully multi-instance delivery should still
// move to SELECT ... FOR UPDATE SKIP LOCKED within a transaction. Tracked
// alongside the deliverer-binary extraction.
func ClaimReady(ctx context.Context, db *sql.DB, limit int) ([]Delivery, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, alert_contact_id, transition_id, event_id, event_type, severity, payload,
		       status, attempt, next_attempt_at, last_status_code, last_response,
		       last_attempt_at, delivered_at, created_at
		  FROM jetmon_alert_deliveries
		 WHERE status = 'pending'
		   AND (next_attempt_at IS NULL OR next_attempt_at <= CURRENT_TIMESTAMP)
		 ORDER BY next_attempt_at ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("alerting: claim ready: %w", err)
	}
	var candidates []Delivery
	for rows.Next() {
		d, err := scanDeliveryRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, *d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(candidates) == 0 {
		return nil, nil
	}

	// Return only rows whose lock update won; stale SELECT results can happen
	// if another claimant already moved next_attempt_at between our SELECT and
	// UPDATE.
	lockUntil := time.Now().Add(claimLockDuration).UTC()
	// claimed reuses candidates' backing array — safe because the write index
	// is always ≤ the read index and candidates is not returned in its
	// original form.
	claimed := candidates[:0]
	for i := range candidates {
		res, err := db.ExecContext(ctx, `
			UPDATE jetmon_alert_deliveries
			   SET next_attempt_at = ?
			 WHERE id = ?
			   AND status = 'pending'
			   AND (next_attempt_at IS NULL OR next_attempt_at <= CURRENT_TIMESTAMP)`,
			lockUntil, candidates[i].ID)
		if err != nil {
			return nil, fmt.Errorf("alerting: soft-lock row %d: %w", candidates[i].ID, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("alerting: soft-lock row %d rows affected: %w", candidates[i].ID, err)
		}
		if affected == 0 {
			continue
		}
		claimed = append(claimed, candidates[i])
	}
	return claimed, nil
}

// MarkDelivered records a successful delivery.
func MarkDelivered(ctx context.Context, db *sql.DB, id int64, statusCode int, responseBody string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE jetmon_alert_deliveries
		   SET status = 'delivered',
		       last_status_code = ?,
		       last_response = ?,
		       last_attempt_at = CURRENT_TIMESTAMP,
		       delivered_at = CURRENT_TIMESTAMP,
		       attempt = attempt + 1,
		       next_attempt_at = NULL
		 WHERE id = ?`,
		statusCode, truncate(responseBody, 2048), id)
	if err != nil {
		return fmt.Errorf("alerting: mark delivered: %w", err)
	}
	return nil
}

// MarkSuppressed records a delivery that was dropped by the per-contact
// rate cap. The delivery never went out and is terminal — there's no
// useful retry because by the time the cap re-opens, the alert is
// stale. Status='abandoned' with a distinguishing last_response so
// operators can see why.
func MarkSuppressed(ctx context.Context, db *sql.DB, id int64, reason string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE jetmon_alert_deliveries
		   SET status = 'abandoned',
		       last_status_code = 429,
		       last_response = ?,
		       last_attempt_at = CURRENT_TIMESTAMP,
		       attempt = attempt + 1,
		       next_attempt_at = NULL
		 WHERE id = ?`, truncate(reason, 2048), id)
	if err != nil {
		return fmt.Errorf("alerting: mark suppressed: %w", err)
	}
	return nil
}

// ScheduleRetry bumps the attempt counter and sets next_attempt_at
// per the retry schedule. abandon=true marks the row terminal instead.
func ScheduleRetry(ctx context.Context, db *sql.DB, id int64, statusCode int, responseBody string, nextAttempt time.Time, abandon bool) error {
	if abandon {
		_, err := db.ExecContext(ctx, `
			UPDATE jetmon_alert_deliveries
			   SET status = 'abandoned',
			       last_status_code = ?,
			       last_response = ?,
			       last_attempt_at = CURRENT_TIMESTAMP,
			       attempt = attempt + 1,
			       next_attempt_at = NULL
			 WHERE id = ?`,
			statusCode, truncate(responseBody, 2048), id)
		if err != nil {
			return fmt.Errorf("alerting: abandon: %w", err)
		}
		return nil
	}
	_, err := db.ExecContext(ctx, `
		UPDATE jetmon_alert_deliveries
		   SET last_status_code = ?,
		       last_response = ?,
		       last_attempt_at = CURRENT_TIMESTAMP,
		       attempt = attempt + 1,
		       next_attempt_at = ?
		 WHERE id = ?`,
		statusCode, truncate(responseBody, 2048), nextAttempt.UTC(), id)
	if err != nil {
		return fmt.Errorf("alerting: schedule retry: %w", err)
	}
	return nil
}

// GetDelivery returns a single delivery row by id.
func GetDelivery(ctx context.Context, db *sql.DB, id int64) (*Delivery, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, alert_contact_id, transition_id, event_id, event_type, severity, payload,
		       status, attempt, next_attempt_at, last_status_code, last_response,
		       last_attempt_at, delivered_at, created_at
		  FROM jetmon_alert_deliveries
		 WHERE id = ?`, id)
	d, err := scanDeliveryRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDeliveryNotFound
		}
		return nil, err
	}
	return d, nil
}

// ListDeliveries returns deliveries for a contact, optionally filtered
// by status, ordered by id DESC. Cursor-paginated on id.
func ListDeliveries(ctx context.Context, db *sql.DB, contactID int64, status Status, cursorID int64, limit int) ([]Delivery, error) {
	args := []any{contactID}
	q := `
		SELECT id, alert_contact_id, transition_id, event_id, event_type, severity, payload,
		       status, attempt, next_attempt_at, last_status_code, last_response,
		       last_attempt_at, delivered_at, created_at
		  FROM jetmon_alert_deliveries
		 WHERE alert_contact_id = ?`
	if status != "" {
		q += " AND status = ?"
		args = append(args, string(status))
	}
	if cursorID > 0 {
		q += " AND id < ?"
		args = append(args, cursorID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("alerting: list deliveries: %w", err)
	}
	defer rows.Close()
	var out []Delivery
	for rows.Next() {
		d, err := scanDeliveryRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// RetryDelivery resets an abandoned delivery to pending so the worker
// picks it up on the next tick. Mirrors webhooks.RetryDelivery — only
// abandoned deliveries can be retried.
func RetryDelivery(ctx context.Context, db *sql.DB, id int64) error {
	res, err := db.ExecContext(ctx, `
		UPDATE jetmon_alert_deliveries
		   SET status = 'pending',
		       attempt = 0,
		       next_attempt_at = CURRENT_TIMESTAMP,
		       last_status_code = NULL,
		       last_response = NULL,
		       last_attempt_at = NULL
		 WHERE id = ? AND status = 'abandoned'`, id)
	if err != nil {
		return fmt.Errorf("alerting: retry delivery: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		d, getErr := GetDelivery(ctx, db, id)
		if getErr != nil {
			return getErr
		}
		return fmt.Errorf("alerting: delivery %d is %s, only abandoned deliveries can be retried", id, d.Status)
	}
	return nil
}

func scanDeliveryRow(s rowScanner) (*Delivery, error) {
	var (
		d              Delivery
		payload        sql.NullString
		nextAttemptAt  sql.NullTime
		lastStatusCode sql.NullInt64
		lastResponse   sql.NullString
		lastAttemptAt  sql.NullTime
		deliveredAt    sql.NullTime
		statusStr      string
	)
	if err := s.Scan(
		&d.ID, &d.AlertContactID, &d.TransitionID, &d.EventID, &d.EventType, &d.Severity,
		&payload, &statusStr, &d.Attempt, &nextAttemptAt, &lastStatusCode, &lastResponse,
		&lastAttemptAt, &deliveredAt, &d.CreatedAt,
	); err != nil {
		return nil, err
	}
	d.Status = Status(statusStr)
	if payload.Valid {
		d.Payload = json.RawMessage(payload.String)
	}
	if nextAttemptAt.Valid {
		d.NextAttemptAt = &nextAttemptAt.Time
	}
	if lastStatusCode.Valid {
		v := int(lastStatusCode.Int64)
		d.LastStatusCode = &v
	}
	if lastResponse.Valid {
		d.LastResponse = &lastResponse.String
	}
	if lastAttemptAt.Valid {
		d.LastAttemptAt = &lastAttemptAt.Time
	}
	if deliveredAt.Valid {
		d.DeliveredAt = &deliveredAt.Time
	}
	return &d, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

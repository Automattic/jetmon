package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrDeliveryNotFound is returned by Get / Retry when the delivery row
// doesn't exist.
var ErrDeliveryNotFound = errors.New("webhooks: delivery not found")

// Delivery is the in-memory shape of a jetmon_webhook_deliveries row.
type Delivery struct {
	ID             int64
	WebhookID      int64
	TransitionID   int64
	EventID        int64
	EventType      string
	Payload        json.RawMessage // frozen at create time
	Status         Status
	Attempt        int
	NextAttemptAt  *time.Time
	LastStatusCode *int
	LastResponse   *string
	LastAttemptAt  *time.Time
	DeliveredAt    *time.Time
	CreatedAt      time.Time
}

// EnqueueInput carries everything needed to insert a delivery row. payload
// is captured by the caller (the dispatcher builds it from the event +
// transition + site context) and stored verbatim.
type EnqueueInput struct {
	WebhookID    int64
	TransitionID int64
	EventID      int64
	EventType    string
	Payload      json.RawMessage
}

// Enqueue inserts a pending delivery with attempt=0 and next_attempt_at=now,
// signaling the worker to pick it up on the next tick. Uses INSERT IGNORE
// against the (webhook_id, transition_id) UNIQUE KEY so concurrent
// dispatchers don't create duplicate deliveries.
//
// Returns the new delivery's id, or 0 if the row was a duplicate (in which
// case some other dispatcher already enqueued this combination).
func Enqueue(ctx context.Context, db *sql.DB, in EnqueueInput) (int64, error) {
	res, err := db.ExecContext(ctx, `
		INSERT IGNORE INTO jetmon_webhook_deliveries
			(webhook_id, transition_id, event_id, event_type, payload,
			 status, attempt, next_attempt_at)
		VALUES (?, ?, ?, ?, ?, 'pending', 0, CURRENT_TIMESTAMP)`,
		in.WebhookID, in.TransitionID, in.EventID, in.EventType, []byte(in.Payload),
	)
	if err != nil {
		return 0, fmt.Errorf("webhooks: enqueue: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		// MySQL's LastInsertId after INSERT IGNORE that didn't insert returns
		// 0 with no error; getting an error here is an unusual driver quirk.
		return 0, fmt.Errorf("webhooks: last insert id: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Row was a duplicate — another dispatcher already enqueued this
		// (webhook, transition) combination. Not an error condition.
		return 0, nil
	}
	return id, nil
}

// claimLockDuration is how far ClaimReady pushes next_attempt_at out
// when it claims a row. It must outlast the worker's per-delivery wall
// clock so the in-flight goroutine has time to write its real result
// (delivered → next_attempt_at NULL, failed → next_attempt_at = retry
// time) before this soft lock would expire. The default worker
// HTTPTimeout is 30s with a 5s buffer; 60s gives comfortable headroom.
//
// If a goroutine crashes without updating the row (panic without
// recovery, OOM kill, etc.), the soft lock expires naturally and the
// row becomes claimable again — natural recovery without operator
// intervention.
const claimLockDuration = 60 * time.Second

// ClaimReady returns up to limit pending deliveries whose next_attempt_at
// is in the past, ordered by next_attempt_at ASC (oldest first). Each
// claimed row is soft-locked by pushing next_attempt_at to NOW +
// claimLockDuration so subsequent ticks don't re-claim a row whose
// dispatch is still in-flight. The dispatch goroutine overwrites
// next_attempt_at with its real value (NULL on success, retry time on
// failure) when it finishes.
//
// Without the soft lock, the deliver loop's 1-second tick re-claims
// any in-flight row up to the per-contact in-flight cap, producing
// concurrent dispatches and inflating the attempt counter — three
// concurrent claims followed by three failures end up at attempt=3
// after a single round. The soft lock prevents that.
//
// Multi-instance safety: this implementation does NOT use row-level
// locks (SELECT ... FOR UPDATE SKIP LOCKED). Two instances polling
// simultaneously could pick up the same row in the SELECT phase. The
// UPDATE that follows is per-row, so only one of them will actually
// transition next_attempt_at — but both still see the original
// pre-claim row in their result set. For single-instance deployment
// that's fine; for multi-instance the claim-and-lock should move to
// SELECT ... FOR UPDATE SKIP LOCKED within a transaction. Tracked
// alongside the deliverer-binary extraction in ROADMAP.md.
func ClaimReady(ctx context.Context, db *sql.DB, limit int) ([]Delivery, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, webhook_id, transition_id, event_id, event_type, payload,
		       status, attempt, next_attempt_at, last_status_code, last_response,
		       last_attempt_at, delivered_at, created_at
		  FROM jetmon_webhook_deliveries
		 WHERE status = 'pending'
		   AND (next_attempt_at IS NULL OR next_attempt_at <= CURRENT_TIMESTAMP)
		 ORDER BY next_attempt_at ASC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("webhooks: claim ready: %w", err)
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

	// Soft-lock each claimed row so the next tick won't re-pick it.
	lockUntil := time.Now().Add(claimLockDuration).UTC()
	for i := range candidates {
		if _, err := db.ExecContext(ctx, `
			UPDATE jetmon_webhook_deliveries
			   SET next_attempt_at = ?
			 WHERE id = ? AND status = 'pending'`,
			lockUntil, candidates[i].ID); err != nil {
			return nil, fmt.Errorf("webhooks: soft-lock row %d: %w", candidates[i].ID, err)
		}
	}
	return candidates, nil
}

// MarkDelivered records a successful delivery with the response status.
// Sets status=delivered, captures last_status_code, last_response, and
// delivered_at. Subsequent retries are not scheduled — the row is terminal.
func MarkDelivered(ctx context.Context, db *sql.DB, id int64, statusCode int, responseBody string) error {
	_, err := db.ExecContext(ctx, `
		UPDATE jetmon_webhook_deliveries
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
		return fmt.Errorf("webhooks: mark delivered: %w", err)
	}
	return nil
}

// ScheduleRetry bumps the attempt counter and sets next_attempt_at per the
// retry schedule. Captures the status/response from the failed attempt.
// If the next attempt would exceed maxAttempts, the row is marked
// abandoned instead.
func ScheduleRetry(ctx context.Context, db *sql.DB, id int64, statusCode int, responseBody string, nextAttempt time.Time, abandon bool) error {
	if abandon {
		_, err := db.ExecContext(ctx, `
			UPDATE jetmon_webhook_deliveries
			   SET status = 'abandoned',
			       last_status_code = ?,
			       last_response = ?,
			       last_attempt_at = CURRENT_TIMESTAMP,
			       attempt = attempt + 1,
			       next_attempt_at = NULL
			 WHERE id = ?`,
			statusCode, truncate(responseBody, 2048), id)
		if err != nil {
			return fmt.Errorf("webhooks: abandon: %w", err)
		}
		return nil
	}
	_, err := db.ExecContext(ctx, `
		UPDATE jetmon_webhook_deliveries
		   SET last_status_code = ?,
		       last_response = ?,
		       last_attempt_at = CURRENT_TIMESTAMP,
		       attempt = attempt + 1,
		       next_attempt_at = ?
		 WHERE id = ?`,
		statusCode, truncate(responseBody, 2048), nextAttempt.UTC(), id)
	if err != nil {
		return fmt.Errorf("webhooks: schedule retry: %w", err)
	}
	return nil
}

// GetDelivery returns a single delivery row by id.
func GetDelivery(ctx context.Context, db *sql.DB, id int64) (*Delivery, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, webhook_id, transition_id, event_id, event_type, payload,
		       status, attempt, next_attempt_at, last_status_code, last_response,
		       last_attempt_at, delivered_at, created_at
		  FROM jetmon_webhook_deliveries
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

// ListDeliveries returns deliveries for a webhook, optionally filtered by
// status, ordered by created_at DESC. Cursor-paginated on id.
func ListDeliveries(ctx context.Context, db *sql.DB, webhookID int64, status Status, cursorID int64, limit int) ([]Delivery, error) {
	args := []any{webhookID}
	q := `
		SELECT id, webhook_id, transition_id, event_id, event_type, payload,
		       status, attempt, next_attempt_at, last_status_code, last_response,
		       last_attempt_at, delivered_at, created_at
		  FROM jetmon_webhook_deliveries
		 WHERE webhook_id = ?`
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
		return nil, fmt.Errorf("webhooks: list deliveries: %w", err)
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
// picks it up on the next tick. Manual operator path: consumer fixed
// their endpoint, wants the previously-failed delivery to fire again.
//
// Resets attempt to 0 (new retry sequence) so the consumer gets the full
// 6 attempts again — they may have just brought their service back and a
// transient failure deserves a fresh budget.
//
// Only abandoned deliveries can be retried via this path. pending
// deliveries are already in the worker's queue; delivered deliveries
// were already accepted by the consumer.
func RetryDelivery(ctx context.Context, db *sql.DB, id int64) error {
	res, err := db.ExecContext(ctx, `
		UPDATE jetmon_webhook_deliveries
		   SET status = 'pending',
		       attempt = 0,
		       next_attempt_at = CURRENT_TIMESTAMP,
		       last_status_code = NULL,
		       last_response = NULL,
		       last_attempt_at = NULL
		 WHERE id = ? AND status = 'abandoned'`, id)
	if err != nil {
		return fmt.Errorf("webhooks: retry delivery: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Either the row doesn't exist or it isn't abandoned. Distinguish
		// for a useful error message.
		d, getErr := GetDelivery(ctx, db, id)
		if getErr != nil {
			return getErr
		}
		return fmt.Errorf("webhooks: delivery %d is %s, only abandoned deliveries can be retried", id, d.Status)
	}
	return nil
}

func scanDeliveryRow(s rowScanner) (*Delivery, error) {
	var (
		d                Delivery
		payload          sql.NullString
		nextAttemptAt    sql.NullTime
		lastStatusCode   sql.NullInt64
		lastResponse     sql.NullString
		lastAttemptAt    sql.NullTime
		deliveredAt      sql.NullTime
		statusStr        string
	)
	if err := s.Scan(
		&d.ID, &d.WebhookID, &d.TransitionID, &d.EventID, &d.EventType, &payload,
		&statusStr, &d.Attempt, &nextAttemptAt, &lastStatusCode, &lastResponse,
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

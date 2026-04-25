package alerting

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const selectClaimReadySQL = ` SELECT id, alert_contact_id, transition_id, event_id, event_type, severity, payload, status, attempt, next_attempt_at, last_status_code, last_response, last_attempt_at, delivered_at, created_at FROM jetmon_alert_deliveries WHERE status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= CURRENT_TIMESTAMP) ORDER BY next_attempt_at ASC LIMIT ?`

const softLockClaimedSQL = ` UPDATE jetmon_alert_deliveries SET next_attempt_at = ? WHERE id = ? AND status = 'pending'`

var columnsClaimedDelivery = []string{
	"id", "alert_contact_id", "transition_id", "event_id", "event_type", "severity",
	"payload", "status", "attempt", "next_attempt_at", "last_status_code", "last_response",
	"last_attempt_at", "delivered_at", "created_at",
}

// TestClaimReadySoftLocksEachRow verifies the contract that ClaimReady
// follows its SELECT with one UPDATE per claimed row, pushing
// next_attempt_at out so subsequent ticks won't re-claim the still-
// in-flight row. Without this, the deliver loop's 1s tick re-claims
// pending rows and produces concurrent dispatches that inflate the
// attempt counter (the bug that motivated the soft lock).
func TestClaimReadySoftLocksEachRow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	rows := sqlmock.NewRows(columnsClaimedDelivery).
		AddRow(int64(1), int64(11), int64(100), int64(900), "alert.opened", uint8(4),
			[]byte(`{}`), "pending", 0, now, nil, nil, nil, nil, now).
		AddRow(int64(2), int64(11), int64(101), int64(901), "alert.opened", uint8(4),
			[]byte(`{}`), "pending", 0, now, nil, nil, nil, nil, now)

	mock.ExpectQuery(selectClaimReadySQL).WithArgs(50).WillReturnRows(rows)

	// Each candidate gets a soft-lock UPDATE.
	mock.ExpectExec(softLockClaimedSQL).
		WithArgs(sqlmock.AnyArg(), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(softLockClaimedSQL).
		WithArgs(sqlmock.AnyArg(), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	out, err := ClaimReady(context.Background(), db, 50)
	if err != nil {
		t.Fatalf("ClaimReady: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("got %d claimed, want 2", len(out))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestClaimReadyNoCandidatesSkipsLockUpdates verifies that when the
// SELECT returns nothing, ClaimReady issues no UPDATEs (no extra DB
// traffic on idle ticks).
func TestClaimReadyNoCandidatesSkipsLockUpdates(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(selectClaimReadySQL).WithArgs(50).
		WillReturnRows(sqlmock.NewRows(columnsClaimedDelivery))

	out, err := ClaimReady(context.Background(), db, 50)
	if err != nil {
		t.Fatalf("ClaimReady: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("got %d claimed, want 0", len(out))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

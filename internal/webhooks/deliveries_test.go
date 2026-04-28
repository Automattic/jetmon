package webhooks

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const selectClaimReadySQL = ` SELECT id, webhook_id, transition_id, event_id, event_type, payload, status, attempt, next_attempt_at, last_status_code, last_response, last_attempt_at, delivered_at, created_at FROM jetmon_webhook_deliveries WHERE status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= CURRENT_TIMESTAMP) ORDER BY next_attempt_at ASC LIMIT ? FOR UPDATE`

const leaseClaimedSQL = ` UPDATE jetmon_webhook_deliveries SET next_attempt_at = ? WHERE id = ? AND status = 'pending'`

var columnsClaimedDelivery = []string{
	"id", "webhook_id", "transition_id", "event_id", "event_type",
	"payload", "status", "attempt", "next_attempt_at", "last_status_code", "last_response",
	"last_attempt_at", "delivered_at", "created_at",
}

// TestClaimReadyClaimsRowsTransactionally verifies that ClaimReady uses
// row-level locks and then leases each claimed row so subsequent ticks do not
// re-claim a still-in-flight delivery.
func TestClaimReadyClaimsRowsTransactionally(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	rows := sqlmock.NewRows(columnsClaimedDelivery).
		AddRow(int64(1), int64(7), int64(100), int64(900), "event.opened",
			[]byte(`{}`), "pending", 0, now, nil, nil, nil, nil, now).
		AddRow(int64(2), int64(7), int64(101), int64(901), "event.opened",
			[]byte(`{}`), "pending", 0, now, nil, nil, nil, nil, now)

	mock.ExpectBegin()
	mock.ExpectQuery(selectClaimReadySQL).WithArgs(50).WillReturnRows(rows)
	mock.ExpectExec(leaseClaimedSQL).
		WithArgs(sqlmock.AnyArg(), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(leaseClaimedSQL).
		WithArgs(sqlmock.AnyArg(), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

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

func TestClaimReadyRollsBackWhenLeaseUpdateMisses(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	rows := sqlmock.NewRows(columnsClaimedDelivery).
		AddRow(int64(1), int64(7), int64(100), int64(900), "event.opened",
			[]byte(`{}`), "pending", 0, now, nil, nil, nil, nil, now)

	mock.ExpectBegin()
	mock.ExpectQuery(selectClaimReadySQL).WithArgs(50).WillReturnRows(rows)
	mock.ExpectExec(leaseClaimedSQL).
		WithArgs(sqlmock.AnyArg(), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	out, err := ClaimReady(context.Background(), db, 50)
	if err == nil {
		t.Fatal("ClaimReady succeeded after lease update missed")
	}
	if len(out) != 0 {
		t.Fatalf("got %d claimed rows with failed lease update, want 0", len(out))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestClaimReadyNoCandidatesCommitsWithoutLeaseUpdates verifies that when the
// SELECT returns nothing, ClaimReady issues no UPDATEs.
func TestClaimReadyNoCandidatesCommitsWithoutLeaseUpdates(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(selectClaimReadySQL).WithArgs(50).
		WillReturnRows(sqlmock.NewRows(columnsClaimedDelivery))
	mock.ExpectCommit()

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

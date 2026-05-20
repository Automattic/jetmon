package webhooks

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	_ "github.com/go-sql-driver/mysql"
)

const selectClaimReadySQL = ` SELECT id, webhook_id, transition_id, event_id, event_type, payload, status, attempt, next_attempt_at, last_status_code, last_response, last_attempt_at, delivered_at, created_at FROM jetpack_monitor_webhook_deliveries WHERE status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= CURRENT_TIMESTAMP) ORDER BY next_attempt_at ASC LIMIT ? FOR UPDATE SKIP LOCKED`

const leaseClaimedSQL = ` UPDATE jetpack_monitor_webhook_deliveries SET next_attempt_at = ? WHERE id = ? AND status = 'pending'`

var columnsClaimedDelivery = []string{
	"id", "webhook_id", "transition_id", "event_id", "event_type",
	"payload", "status", "attempt", "next_attempt_at", "last_status_code", "last_response",
	"last_attempt_at", "delivered_at", "created_at",
}

// TestClaimReadyClaimsRowsTransactionally verifies that ClaimReady uses
// row-level locks that skip rows claimed elsewhere and then leases each claimed
// row so subsequent ticks do not re-claim a still-in-flight delivery.
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

func TestClaimReadySkipsLockedRowsMariaDB(t *testing.T) {
	dsn := os.Getenv("JETMON_DELIVERY_CLAIM_TEST_DSN")
	if dsn == "" {
		t.Skip("JETMON_DELIVERY_CLAIM_TEST_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("PingContext: %v", err)
	}
	requireDeliveryClaimSmokeDB(t, ctx, db)

	webhookID := time.Now().UnixNano()
	ids := make([]int64, 0, 3)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM jetpack_monitor_webhook_deliveries WHERE webhook_id = ?`, webhookID)
	})
	for i := int64(0); i < 3; i++ {
		res, err := db.ExecContext(ctx, `
			INSERT INTO jetpack_monitor_webhook_deliveries
				(webhook_id, transition_id, event_id, event_type, payload, status, attempt, next_attempt_at)
			VALUES (?, ?, ?, 'event.opened', ?, 'pending', 0, ?)`,
			webhookID,
			webhookID+i,
			webhookID+i,
			[]byte(`{"smoke":true}`),
			time.Now().Add(-time.Duration(10-i)*time.Second).UTC(),
		)
		if err != nil {
			t.Fatalf("insert delivery %d: %v", i, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("insert delivery %d last id: %v", i, err)
		}
		ids = append(ids, id)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()
	lockedID := ids[0]
	if err := tx.QueryRowContext(ctx, `
		SELECT id
		  FROM jetpack_monitor_webhook_deliveries
		 WHERE id = ?
		 FOR UPDATE`, lockedID).Scan(&lockedID); err != nil {
		t.Fatalf("lock oldest delivery: %v", err)
	}

	claimCtx, claimCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer claimCancel()
	claimed, err := ClaimReady(claimCtx, db, 2)
	if err != nil {
		t.Fatalf("ClaimReady with one locked row: %v", err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed len = %d, want 2", len(claimed))
	}
	for _, delivery := range claimed {
		if delivery.ID == lockedID {
			t.Fatalf("ClaimReady claimed locked row id=%d", lockedID)
		}
	}
}

func TestClaimReadyConcurrentClaimersMariaDB(t *testing.T) {
	dsn := os.Getenv("JETMON_DELIVERY_CLAIM_TEST_DSN")
	if dsn == "" {
		t.Skip("JETMON_DELIVERY_CLAIM_TEST_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("PingContext: %v", err)
	}
	requireDeliveryClaimSmokeDB(t, ctx, db)

	const claimers = 8
	const perClaimer = 4
	const total = claimers * perClaimer
	webhookID := time.Now().UnixNano()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM jetpack_monitor_webhook_deliveries WHERE webhook_id = ?`, webhookID)
	})
	for i := int64(0); i < total; i++ {
		_, err := db.ExecContext(ctx, `
			INSERT INTO jetpack_monitor_webhook_deliveries
				(webhook_id, transition_id, event_id, event_type, payload, status, attempt, next_attempt_at)
			VALUES (?, ?, ?, 'event.opened', ?, 'pending', 0, ?)`,
			webhookID,
			webhookID+i,
			webhookID+i,
			[]byte(`{"smoke":true}`),
			time.Now().Add(-time.Second).UTC(),
		)
		if err != nil {
			t.Fatalf("insert delivery %d: %v", i, err)
		}
	}

	start := make(chan struct{})
	results := make(chan []Delivery, claimers)
	errs := make(chan error, claimers)
	var wg sync.WaitGroup
	wg.Add(claimers)
	begin := time.Now()
	for i := 0; i < claimers; i++ {
		go func() {
			defer wg.Done()
			<-start
			claimCtx, claimCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer claimCancel()
			out, err := ClaimReady(claimCtx, db, perClaimer)
			if err != nil {
				errs <- err
				return
			}
			results <- out
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	elapsed := time.Since(begin)
	for err := range errs {
		t.Fatalf("ClaimReady: %v", err)
	}

	seen := make(map[int64]struct{}, total)
	for deliveries := range results {
		for _, delivery := range deliveries {
			if _, ok := seen[delivery.ID]; ok {
				t.Fatalf("delivery id %d claimed more than once", delivery.ID)
			}
			seen[delivery.ID] = struct{}{}
		}
	}
	if len(seen) != total {
		t.Fatalf("claimed %d unique deliveries, want %d", len(seen), total)
	}
	t.Logf("claimed %d webhook deliveries with %d concurrent claimers in %s", total, claimers, elapsed)
}

func requireDeliveryClaimSmokeDB(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var name string
	if err := db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&name); err != nil {
		t.Fatalf("SELECT DATABASE(): %v", err)
	}
	if !strings.Contains(name, "smoke") && os.Getenv("JETMON_DELIVERY_CLAIM_TEST_UNSAFE_ALLOW") != "1" {
		t.Fatalf("refusing delivery claim integration test against database %q; use a smoke database or set JETMON_DELIVERY_CLAIM_TEST_UNSAFE_ALLOW=1", name)
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

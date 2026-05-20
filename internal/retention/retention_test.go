package retention

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRunSkipsDisabledTables(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Both days = 0 → both tables skipped, no queries issued.
	result, err := Run(context.Background(), db, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Tables) != 2 {
		t.Fatalf("tables = %d, want 2", len(result.Tables))
	}
	for _, tr := range result.Tables {
		if !tr.Skipped {
			t.Errorf("%s not skipped; want skipped (days=0)", tr.Table)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected queries issued: %v", err)
	}
}

func TestRunDeletesInChunks(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// Only check_history enabled.
	mock.ExpectQuery("SELECT GET_LOCK").
		WithArgs("jetmon_retention_check_history", lockAcquireTimeoutSec).
		WillReturnRows(sqlmock.NewRows([]string{"l"}).AddRow(1))
	// First chunk deletes a full chunk → loop continues.
	mock.ExpectExec("DELETE FROM jetpack_monitor_check_history").
		WillReturnResult(sqlmock.NewResult(0, 3))
	// Second chunk deletes fewer than chunk size → loop stops.
	mock.ExpectExec("DELETE FROM jetpack_monitor_check_history").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT RELEASE_LOCK").
		WithArgs("jetmon_retention_check_history").
		WillReturnRows(sqlmock.NewRows([]string{"r"}).AddRow(1))

	result, err := Run(context.Background(), db, Options{
		CheckHistoryDays: 30,
		ChunkSize:        3,
		ChunkPause:       time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var checkHistory TableResult
	for _, tr := range result.Tables {
		if tr.Table == "jetpack_monitor_check_history" {
			checkHistory = tr
		}
	}
	if checkHistory.DeletedRows != 4 {
		t.Errorf("deleted = %d, want 4 (3+1)", checkHistory.DeletedRows)
	}
	if checkHistory.Skipped {
		t.Error("check_history should not be skipped with days=30")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRunSkipsWhenLockHeldElsewhere(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	// GET_LOCK returns 0 → another host holds it → skip, no DELETE.
	mock.ExpectQuery("SELECT GET_LOCK").
		WithArgs("jetmon_retention_audit_log", lockAcquireTimeoutSec).
		WillReturnRows(sqlmock.NewRows([]string{"l"}).AddRow(0))

	result, err := Run(context.Background(), db, Options{AuditLogDays: 90})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var auditLog TableResult
	for _, tr := range result.Tables {
		if tr.Table == "jetpack_monitor_audit_log" {
			auditLog = tr
		}
	}
	if !auditLog.Skipped {
		t.Error("audit_log should be skipped when lock held elsewhere")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRunDryRunCountsWithoutDeleting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT GET_LOCK").
		WithArgs("jetmon_retention_check_history", lockAcquireTimeoutSec).
		WillReturnRows(sqlmock.NewRows([]string{"l"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM jetpack_monitor_check_history WHERE checked_at < ?")).
		WillReturnRows(sqlmock.NewRows([]string{"c"}).AddRow(int64(123)))
	mock.ExpectQuery("SELECT RELEASE_LOCK").
		WithArgs("jetmon_retention_check_history").
		WillReturnRows(sqlmock.NewRows([]string{"r"}).AddRow(1))

	result, err := Run(context.Background(), db, Options{CheckHistoryDays: 7, DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var checkHistory TableResult
	for _, tr := range result.Tables {
		if tr.Table == "jetpack_monitor_check_history" {
			checkHistory = tr
		}
	}
	if !checkHistory.DryRun || checkHistory.DeletedRows != 123 {
		t.Errorf("dry-run result = %+v, want DryRun=true DeletedRows=123", checkHistory)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestRunReturnsErrorOnDeleteFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT GET_LOCK").
		WithArgs("jetmon_retention_check_history", lockAcquireTimeoutSec).
		WillReturnRows(sqlmock.NewRows([]string{"l"}).AddRow(1))
	mock.ExpectExec("DELETE FROM jetpack_monitor_check_history").
		WillReturnError(errors.New("deadlock"))
	mock.ExpectQuery("SELECT RELEASE_LOCK").
		WithArgs("jetmon_retention_check_history").
		WillReturnRows(sqlmock.NewRows([]string{"r"}).AddRow(1))

	_, err = Run(context.Background(), db, Options{CheckHistoryDays: 30, ChunkSize: 100})
	if err == nil {
		t.Fatal("Run should return error when DELETE fails")
	}
}

func TestRunNilDB(t *testing.T) {
	if _, err := Run(context.Background(), nil, Options{CheckHistoryDays: 30}); err == nil {
		t.Fatal("Run(nil db) should error")
	}
}

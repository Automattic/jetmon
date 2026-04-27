package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestNewWithNilDB(t *testing.T) {
	s := New(nil)
	if s == nil {
		t.Fatal("New(nil) returned nil Store")
	}

	// All write operations should be no-ops when db is nil.
	ctx := context.Background()

	res, err := s.Open(ctx, OpenInput{
		Identity: Identity{BlogID: 1, CheckType: "http"},
		Severity: SeveritySeemsDown,
		State:    StateSeemsDown,
	})
	if err != nil {
		t.Fatalf("Open with nil db: %v", err)
	}
	if res.EventID != 0 || res.Opened {
		t.Fatalf("Open with nil db = %+v, want zero", res)
	}

	if changed, err := s.UpdateSeverity(ctx, 42, SeverityDown, ReasonSeverityEscalation, "local", nil); err != nil || changed {
		t.Fatalf("UpdateSeverity with nil db = (%v, %v)", changed, err)
	}

	if changed, err := s.UpdateState(ctx, 42, StateDown, ReasonVerifierConfirmed, "local", nil); err != nil || changed {
		t.Fatalf("UpdateState with nil db = (%v, %v)", changed, err)
	}

	if changed, err := s.Promote(ctx, 42, SeverityDown, StateDown, ReasonVerifierConfirmed, "local", nil); err != nil || changed {
		t.Fatalf("Promote with nil db = (%v, %v)", changed, err)
	}

	if changed, err := s.LinkCause(ctx, 42, 99, "local"); err != nil || changed {
		t.Fatalf("LinkCause with nil db = (%v, %v)", changed, err)
	}

	if err := s.Close(ctx, 42, ReasonVerifierCleared, "local", nil); err != nil {
		t.Fatalf("Close with nil db: %v", err)
	}
}

func TestNilDBTxIsNoOp(t *testing.T) {
	// Begin on a nil-db Store returns a no-op Tx whose methods all short-circuit
	// without touching a database.
	s := New(nil)
	ctx := context.Background()

	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if tx == nil {
		t.Fatal("Begin returned nil Tx")
	}
	if tx.Tx() != nil {
		t.Fatal("nil-db Tx should expose nil *sql.Tx")
	}

	// All Tx methods should run without panicking.
	res, err := tx.Open(ctx, OpenInput{
		Identity: Identity{BlogID: 1, CheckType: "http"},
		Severity: SeveritySeemsDown,
		State:    StateSeemsDown,
	})
	if err != nil || res.EventID != 0 {
		t.Fatalf("Tx.Open with nil db = (%+v, %v)", res, err)
	}
	if _, err := tx.UpdateSeverity(ctx, 1, SeverityDown, ReasonSeverityEscalation, "local", nil); err != nil {
		t.Fatalf("Tx.UpdateSeverity: %v", err)
	}
	if _, err := tx.Promote(ctx, 1, SeverityDown, StateDown, ReasonVerifierConfirmed, "local", nil); err != nil {
		t.Fatalf("Tx.Promote: %v", err)
	}
	if _, err := tx.UpdateState(ctx, 1, StateDown, ReasonStateChange, "local", nil); err != nil {
		t.Fatalf("Tx.UpdateState: %v", err)
	}
	if _, err := tx.LinkCause(ctx, 1, 2, "local"); err != nil {
		t.Fatalf("Tx.LinkCause: %v", err)
	}
	if err := tx.Close(ctx, 1, ReasonVerifierCleared, "local", nil); err != nil {
		t.Fatalf("Tx.Close: %v", err)
	}
	ae, err := tx.FindActiveByBlog(ctx, 1, "http")
	if err != nil {
		t.Fatalf("Tx.FindActiveByBlog: %v", err)
	}
	if ae.ID != 0 {
		t.Fatalf("FindActiveByBlog on nil-db = %+v, want zero", ae)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// Rollback after Commit should also be a no-op.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback after Commit: %v", err)
	}
}

func TestSQLTxBeginCommitAndRollback(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	s := New(db)
	ctx := context.Background()

	mock.ExpectBegin()
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin for commit: %v", err)
	}
	if tx.Tx() == nil {
		t.Fatal("sql-backed Tx should expose *sql.Tx")
	}
	mock.ExpectCommit()
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// Rollback after Commit should swallow sql.ErrTxDone so callers can defer it.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback after Commit: %v", err)
	}

	mock.ExpectBegin()
	tx, err = s.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin for rollback: %v", err)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// A second Rollback after the transaction is closed is also a no-op.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("second Rollback: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

var eventSnapshotColumns = []string{"blog_id", "severity", "state", "ended_at", "cause_event_id"}

func eventSnapshotRow(blogID int64, severity uint8, state string, cause any) *sqlmock.Rows {
	return sqlmock.NewRows(eventSnapshotColumns).
		AddRow(blogID, severity, state, nil, cause)
}

func TestStoreOpenInsertedEventWritesTransition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO jetmon_events").
		WithArgs(int64(42), nil, "http", nil, SeveritySeemsDown, StateSeemsDown, nil).
		WillReturnResult(sqlmock.NewResult(99, 1))
	mock.ExpectExec("INSERT INTO jetmon_event_transitions").
		WithArgs(int64(99), int64(42), nil, SeveritySeemsDown, nil, StateSeemsDown, ReasonOpened, "local", nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	res, err := New(db).Open(context.Background(), OpenInput{
		Identity: Identity{BlogID: 42, CheckType: "http"},
		Severity: SeveritySeemsDown,
		State:    StateSeemsDown,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if res.EventID != 99 || !res.Opened || res.CurrentSeverity != SeveritySeemsDown || res.CurrentState != StateSeemsDown {
		t.Fatalf("Open result = %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestStoreOpenExistingEventReadsCurrentState(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO jetmon_events").
		WithArgs(int64(42), nil, "http", nil, SeveritySeemsDown, StateSeemsDown, nil).
		WillReturnResult(sqlmock.NewResult(99, 2))
	mock.ExpectQuery("SELECT severity, state FROM jetmon_events").
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"severity", "state"}).AddRow(SeverityDown, StateDown))
	mock.ExpectCommit()

	res, err := New(db).Open(context.Background(), OpenInput{
		Identity: Identity{BlogID: 42, CheckType: "http"},
		Severity: SeveritySeemsDown,
		State:    StateSeemsDown,
	})
	if err != nil {
		t.Fatalf("Open existing: %v", err)
	}
	if res.Opened || res.CurrentSeverity != SeverityDown || res.CurrentState != StateDown {
		t.Fatalf("Open existing result = %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestStoreUpdateSeverityNoopSkipsTransition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blog_id, severity, state, ended_at, cause_event_id").
		WithArgs(int64(99)).
		WillReturnRows(eventSnapshotRow(42, SeverityDown, StateDown, nil))
	mock.ExpectCommit()

	changed, err := New(db).UpdateSeverity(context.Background(), 99, SeverityDown, ReasonSeverityEscalation, "tester", nil)
	if err != nil {
		t.Fatalf("UpdateSeverity: %v", err)
	}
	if changed {
		t.Fatal("UpdateSeverity reported change for same severity")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestStorePromoteWritesEventAndTransition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blog_id, severity, state, ended_at, cause_event_id").
		WithArgs(int64(99)).
		WillReturnRows(eventSnapshotRow(42, SeveritySeemsDown, StateSeemsDown, nil))
	mock.ExpectExec("UPDATE jetmon_events SET severity").
		WithArgs(SeverityDown, StateDown, int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO jetmon_event_transitions").
		WithArgs(int64(99), int64(42), SeveritySeemsDown, SeverityDown, StateSeemsDown, StateDown, ReasonVerifierConfirmed, "tester", nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	changed, err := New(db).Promote(context.Background(), 99, SeverityDown, StateDown, ReasonVerifierConfirmed, "tester", nil)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !changed {
		t.Fatal("Promote reported no change")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestStoreLinkCauseWritesMetadataTransition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blog_id, severity, state, ended_at, cause_event_id").
		WithArgs(int64(99)).
		WillReturnRows(eventSnapshotRow(42, SeverityDown, StateDown, nil))
	mock.ExpectExec("UPDATE jetmon_events SET cause_event_id").
		WithArgs(int64(123), int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO jetmon_event_transitions").
		WithArgs(int64(99), int64(42), SeverityDown, SeverityDown, StateDown, StateDown, ReasonCauseLinked, "tester", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	changed, err := New(db).LinkCause(context.Background(), 99, 123, "tester")
	if err != nil {
		t.Fatalf("LinkCause: %v", err)
	}
	if !changed {
		t.Fatal("LinkCause reported no change")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestStoreCloseWritesResolvedTransition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT blog_id, severity, state, ended_at, cause_event_id").
		WithArgs(int64(99)).
		WillReturnRows(eventSnapshotRow(42, SeverityDown, StateDown, nil))
	mock.ExpectExec("UPDATE jetmon_events").
		WithArgs(ReasonVerifierCleared, int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO jetmon_event_transitions").
		WithArgs(int64(99), int64(42), SeverityDown, nil, StateDown, StateResolved, ReasonVerifierCleared, "tester", nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	if err := New(db).Close(context.Background(), 99, ReasonVerifierCleared, "tester", nil); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestTxFindActiveByBlog(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, severity, state FROM jetmon_events").
		WithArgs(int64(42), "http").
		WillReturnRows(sqlmock.NewRows([]string{"id", "severity", "state"}).AddRow(int64(99), SeverityDown, StateDown))
	mock.ExpectRollback()

	tx, err := New(db).Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	active, err := tx.FindActiveByBlog(context.Background(), 42, "http")
	if err != nil {
		t.Fatalf("FindActiveByBlog: %v", err)
	}
	if active.ID != 99 || active.Severity != SeverityDown || active.State != StateDown {
		t.Fatalf("active = %+v", active)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSeverityScale(t *testing.T) {
	// Severity is intentionally a small ordered scale; relative ordering matters
	// more than the exact numbers, but the constants must agree with what the
	// orchestrator and dashboards expect.
	if SeverityUp >= SeverityWarning ||
		SeverityWarning >= SeverityDegraded ||
		SeverityDegraded >= SeveritySeemsDown ||
		SeveritySeemsDown >= SeverityDown {
		t.Fatalf("severity scale not strictly increasing: %d %d %d %d %d",
			SeverityUp, SeverityWarning, SeverityDegraded, SeveritySeemsDown, SeverityDown)
	}
}

func TestStateAndReasonConstants(t *testing.T) {
	if StateSeemsDown != "Seems Down" {
		t.Fatalf("StateSeemsDown = %q, want %q", StateSeemsDown, "Seems Down")
	}
	if ReasonOpened != "opened" {
		t.Fatalf("ReasonOpened = %q, want %q", ReasonOpened, "opened")
	}
	if ReasonProbeCleared != "probe_cleared" {
		t.Fatalf("ReasonProbeCleared = %q, want %q", ReasonProbeCleared, "probe_cleared")
	}
	if ReasonFalseAlarm != "false_alarm" {
		t.Fatalf("ReasonFalseAlarm = %q, want %q", ReasonFalseAlarm, "false_alarm")
	}
}

func TestNullableHelpers(t *testing.T) {
	if nullableEndpoint(nil) != nil {
		t.Fatal("nullableEndpoint(nil) should be nil")
	}
	id := int64(7)
	if nullableEndpoint(&id) != int64(7) {
		t.Fatalf("nullableEndpoint(&7) = %v, want 7", nullableEndpoint(&id))
	}

	if nullableDiscriminator("") != nil {
		t.Fatal("nullableDiscriminator(\"\") should be nil")
	}
	if nullableDiscriminator("abc") != "abc" {
		t.Fatal("nullableDiscriminator(\"abc\") should be \"abc\"")
	}

	if nullableJSON(nil) != nil {
		t.Fatal("nullableJSON(nil) should be nil")
	}
	if nullableJSON(json.RawMessage("")) != nil {
		t.Fatal("nullableJSON(empty) should be nil")
	}
	if nullableJSON(json.RawMessage(`{"a":1}`)) == nil {
		t.Fatal("nullableJSON(non-empty) should not be nil")
	}

	if nullableUint8(nil) != nil {
		t.Fatal("nullableUint8(nil) should be nil")
	}
	v := uint8(3)
	if nullableUint8(&v) != uint8(3) {
		t.Fatalf("nullableUint8(&3) = %v, want 3", nullableUint8(&v))
	}

	if nullableString("") != nil {
		t.Fatal("nullableString(\"\") should be nil")
	}
	if nullableString("x") != "x" {
		t.Fatal("nullableString(\"x\") should be \"x\"")
	}

	if nullableInt64(sql.NullInt64{}) != nil {
		t.Fatal("nullableInt64(invalid) should be nil")
	}
	validInt := sql.NullInt64{Int64: 12, Valid: true}
	if nullableInt64(validInt) != int64(12) {
		t.Fatalf("nullableInt64(valid 12) = %v, want 12", nullableInt64(validInt))
	}
	if nullableInt64ToAny(sql.NullInt64{}) != nil {
		t.Fatal("nullableInt64ToAny(invalid) should be nil")
	}
	if nullableInt64ToAny(validInt) != int64(12) {
		t.Fatalf("nullableInt64ToAny(valid 12) = %v, want 12", nullableInt64ToAny(validInt))
	}
}

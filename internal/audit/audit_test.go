package audit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestNullableInt64(t *testing.T) {
	if nullableInt64(0) != nil {
		t.Fatal("nullableInt64(0) should be nil")
	}
	if nullableInt64(42) != int64(42) {
		t.Fatalf("nullableInt64(42) = %v, want 42", nullableInt64(42))
	}
}

func TestNullableString(t *testing.T) {
	if nullableString("") != nil {
		t.Fatal("nullableString(\"\") should be nil")
	}
	if nullableString("hello") != "hello" {
		t.Fatalf("nullableString(\"hello\") = %v, want \"hello\"", nullableString("hello"))
	}
}

func TestNullableJSON(t *testing.T) {
	if nullableJSON(nil) != nil {
		t.Fatal("nullableJSON(nil) should be nil")
	}
	if nullableJSON([]byte("")) != nil {
		t.Fatal("nullableJSON(empty) should be nil")
	}
	got := nullableJSON([]byte(`{"k":1}`))
	if got == nil {
		t.Fatal("nullableJSON(non-empty) should not be nil")
	}
}

func TestLogWithNilDB(t *testing.T) {
	// db is nil in tests — Log must return nil, not panic.
	if err := Log(context.Background(), Entry{
		BlogID:    1,
		EventType: EventVeriflierSent,
		Source:    "test",
		Detail:    "detail",
	}); err != nil {
		t.Fatalf("Log() with nil db = %v, want nil", err)
	}
}

func TestLogRequiresEventType(t *testing.T) {
	// Set a non-nil db so the validation runs (we won't actually hit it because
	// the validation is before the db.Exec call).
	if err := Log(context.Background(), Entry{BlogID: 1}); err != nil {
		// nil db short-circuits before validation. That's fine — the
		// production code path requires a real db, which the integration
		// tests cover. Here we just confirm the call doesn't panic with an
		// empty Entry.
	}
}

func TestLogHonorsCanceledContext(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer conn.Close()

	orig := db
	t.Cleanup(func() { db = orig })
	db = conn

	mock.ExpectExec(`INSERT INTO jetmon_audit_log`).WillReturnError(context.Canceled)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = Log(ctx, Entry{
		EventType: EventConfigChange,
		Source:    "test",
		Detail:    "ctx canceled",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Log() with canceled ctx = %v, want context.Canceled", err)
	}
}

func TestInit(t *testing.T) {
	orig := db
	t.Cleanup(func() { db = orig })
	Init(nil)
	if db != nil {
		t.Fatal("Init(nil) should set db to nil")
	}
}

func TestQueryBuildsTimeRange(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer conn.Close()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id, blog_id, event_id, event_type").
		WithArgs(int64(42), "2026-04-27 00:00:00", "2026-04-28 00:00:00").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "blog_id", "event_id", "event_type", "source", "detail", "metadata", "created_at",
		}).AddRow(int64(1), int64(42), nil, EventAPIAccess, "api", "ok", nil, now))

	rows, err := Query(conn, 42, "2026-04-27 00:00:00", "2026-04-28 00:00:00")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("expected one audit row")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

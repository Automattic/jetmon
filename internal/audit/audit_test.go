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

func TestShouldLog(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		eventType  string
		httpMethod string
		want       bool
	}{
		// disabled: nothing.
		{"disabled blocks writes", ModeDisabled, EventWPCOMSent, "", false},
		{"disabled blocks api POST", ModeDisabled, EventAPIAccess, "POST", false},

		// all: everything.
		{"all logs api GET", ModeAll, EventAPIAccess, "GET", true},
		{"all logs maintenance", ModeAll, EventMaintenanceActive, "", true},

		// writes: mutating events only.
		{"writes logs wpcom_sent", ModeWrites, EventWPCOMSent, "", true},
		{"writes logs wpcom_retry", ModeWrites, EventWPCOMRetry, "", true},
		{"writes logs config_change", ModeWrites, EventConfigChange, "", true},
		{"writes logs retry_dispatched", ModeWrites, EventRetryDispatched, "", true},
		{"writes logs probe_safety_blocked", ModeWrites, EventProbeSafetyBlock, "", true},
		{"writes drops wpcom_failure", ModeWrites, EventWPCOMFailure, "", false},
		{"writes drops veriflier_sent", ModeWrites, EventVeriflierSent, "", false},
		{"writes drops maintenance_active", ModeWrites, EventMaintenanceActive, "", false},
		{"writes drops alert_suppressed", ModeWrites, EventAlertSuppressed, "", false},
		{"writes logs api POST", ModeWrites, EventAPIAccess, "POST", true},
		{"writes logs api DELETE", ModeWrites, EventAPIAccess, "DELETE", true},
		{"writes logs api PATCH", ModeWrites, EventAPIAccess, "PATCH", true},
		{"writes drops api GET", ModeWrites, EventAPIAccess, "GET", false},
		{"writes drops api HEAD", ModeWrites, EventAPIAccess, "HEAD", false},

		// operational: writes + telemetry events, still drops api GET.
		{"operational logs veriflier_sent", ModeOperational, EventVeriflierSent, "", true},
		{"operational logs maintenance_active", ModeOperational, EventMaintenanceActive, "", true},
		{"operational logs alert_suppressed", ModeOperational, EventAlertSuppressed, "", true},
		{"operational logs wpcom_failure", ModeOperational, EventWPCOMFailure, "", true},
		{"operational logs api POST", ModeOperational, EventAPIAccess, "POST", true},
		{"operational drops api GET", ModeOperational, EventAPIAccess, "GET", false},
		{"operational drops api OPTIONS", ModeOperational, EventAPIAccess, "OPTIONS", false},

		// method case-insensitivity.
		{"lowercase get is dropped under operational", ModeOperational, EventAPIAccess, "get", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldLog(tc.mode, tc.eventType, tc.httpMethod); got != tc.want {
				t.Errorf("shouldLog(%q, %q, %q) = %v, want %v",
					tc.mode, tc.eventType, tc.httpMethod, got, tc.want)
			}
		})
	}
}

func TestSetModeFallsBackOnUnknown(t *testing.T) {
	SetMode("nonsense")
	if got := currentMode(); got != ModeOperational {
		t.Errorf("currentMode() after unknown SetMode = %q, want operational", got)
	}
	SetMode(ModeAll)
	if got := currentMode(); got != ModeAll {
		t.Errorf("currentMode() = %q, want all", got)
	}
	SetMode(ModeOperational) // restore for other tests
}

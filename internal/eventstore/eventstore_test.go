package eventstore

import (
	"context"
	"encoding/json"
	"testing"
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
}

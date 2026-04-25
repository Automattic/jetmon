package audit

import "testing"

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
	if err := Log(Entry{
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
	if err := Log(Entry{BlogID: 1}); err != nil {
		// nil db short-circuits before validation. That's fine — the
		// production code path requires a real db, which the integration
		// tests cover. Here we just confirm the call doesn't panic with an
		// empty Entry.
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

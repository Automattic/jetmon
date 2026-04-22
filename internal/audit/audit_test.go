package audit

import "testing"

func TestNullInt(t *testing.T) {
	if nullInt(0) != nil {
		t.Fatal("nullInt(0) should be nil")
	}
	if nullInt(42) != 42 {
		t.Fatalf("nullInt(42) = %v, want 42", nullInt(42))
	}
}

func TestNullInt64(t *testing.T) {
	if nullInt64(0) != nil {
		t.Fatal("nullInt64(0) should be nil")
	}
	if nullInt64(100) != int64(100) {
		t.Fatalf("nullInt64(100) = %v, want 100", nullInt64(100))
	}
}

func TestNullString(t *testing.T) {
	if nullString("") != nil {
		t.Fatal("nullString(\"\") should be nil")
	}
	if nullString("hello") != "hello" {
		t.Fatalf("nullString(\"hello\") = %v, want \"hello\"", nullString("hello"))
	}
}

func TestLogWithNilDB(t *testing.T) {
	// db is nil in tests — Log must return nil, not panic.
	if err := Log(1, EventCheck, "test", 200, 0, 50, "detail"); err != nil {
		t.Fatalf("Log() with nil db = %v, want nil", err)
	}
}

func TestLogTransitionWithNilDB(t *testing.T) {
	if err := LogTransition(1, 1, 2, "recovered"); err != nil {
		t.Fatalf("LogTransition() with nil db = %v, want nil", err)
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

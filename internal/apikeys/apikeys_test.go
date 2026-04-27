package apikeys

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestGenerateTokenFormat(t *testing.T) {
	raw, hashed, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if !strings.HasPrefix(raw, TokenPrefix) {
		t.Fatalf("token missing prefix: %q", raw)
	}
	// 32 random bytes → 52 base32 chars (no padding) + "jm_" = 55.
	if len(raw) != len(TokenPrefix)+52 {
		t.Fatalf("token length = %d, want %d", len(raw), len(TokenPrefix)+52)
	}
	if len(hashed) != 64 {
		t.Fatalf("hash length = %d, want 64 (sha256 hex)", len(hashed))
	}
	if HashToken(raw) != hashed {
		t.Fatal("HashToken doesn't match GenerateToken's returned hash")
	}
}

func TestGenerateTokenUnique(t *testing.T) {
	a, _, _ := GenerateToken()
	b, _, _ := GenerateToken()
	if a == b {
		t.Fatal("two generated tokens collided — entropy source is broken")
	}
}

func TestScopeIncludes(t *testing.T) {
	cases := []struct {
		have     Scope
		required Scope
		want     bool
	}{
		{ScopeRead, ScopeRead, true},
		{ScopeRead, ScopeWrite, false},
		{ScopeRead, ScopeAdmin, false},
		{ScopeWrite, ScopeRead, true},
		{ScopeWrite, ScopeWrite, true},
		{ScopeWrite, ScopeAdmin, false},
		{ScopeAdmin, ScopeRead, true},
		{ScopeAdmin, ScopeWrite, true},
		{ScopeAdmin, ScopeAdmin, true},
	}
	for _, c := range cases {
		got := c.have.Includes(c.required)
		if got != c.want {
			t.Errorf("Scope(%q).Includes(%q) = %v, want %v", c.have, c.required, got, c.want)
		}
	}
}

func TestScopeValid(t *testing.T) {
	for _, s := range AllScopes() {
		if !s.Valid() {
			t.Errorf("AllScopes()[%q].Valid() = false", s)
		}
	}
	if Scope("anything-else").Valid() {
		t.Error("invalid scope should not be Valid()")
	}
	if Scope("").Valid() {
		t.Error("empty scope should not be Valid()")
	}
}

func TestHashTokenStability(t *testing.T) {
	// HashToken must be deterministic — Lookup compares the hash of an
	// incoming token against the stored hash, so a non-deterministic hash
	// would break auth entirely.
	a := HashToken("jm_some-fixed-token")
	b := HashToken("jm_some-fixed-token")
	if a != b {
		t.Fatal("HashToken is not deterministic")
	}
}

func TestLookupAllowsFutureRevokedAtDuringRotationGrace(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	raw := TokenPrefix + strings.Repeat("A", 52)
	hash := HashToken(raw)
	now := time.Now().UTC()
	futureRevocation := now.Add(time.Hour)

	rows := sqlmock.NewRows([]string{
		"id", "consumer_name", "scope", "rate_limit_per_minute",
		"expires_at", "revoked_at", "last_used_at", "created_at", "created_by",
	}).AddRow(int64(42), "gateway", string(ScopeRead), 60, nil, futureRevocation, nil, now, "test")

	mock.ExpectQuery("SELECT id, consumer_name, scope, rate_limit_per_minute").
		WithArgs(hash).
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE jetmon_api_keys SET last_used_at").
		WithArgs(int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	key, err := Lookup(context.Background(), db, raw)
	if err != nil {
		t.Fatalf("Lookup returned error for grace-period key: %v", err)
	}
	if key.ID != 42 {
		t.Fatalf("key.ID = %d, want 42", key.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestLookupRejectsPastRevokedAt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	raw := TokenPrefix + strings.Repeat("B", 52)
	hash := HashToken(raw)
	now := time.Now().UTC()
	pastRevocation := now.Add(-time.Minute)

	rows := sqlmock.NewRows([]string{
		"id", "consumer_name", "scope", "rate_limit_per_minute",
		"expires_at", "revoked_at", "last_used_at", "created_at", "created_by",
	}).AddRow(int64(43), "gateway", string(ScopeRead), 60, nil, pastRevocation, nil, now, "test")

	mock.ExpectQuery("SELECT id, consumer_name, scope, rate_limit_per_minute").
		WithArgs(hash).
		WillReturnRows(rows)

	_, err = Lookup(context.Background(), db, raw)
	if !errors.Is(err, ErrKeyRevoked) {
		t.Fatalf("Lookup error = %v, want ErrKeyRevoked", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

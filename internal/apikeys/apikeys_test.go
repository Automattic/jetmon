package apikeys

import (
	"context"
	"database/sql"
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

func TestLookupAllowsFutureExpiresAt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	raw := TokenPrefix + strings.Repeat("C", 52)
	hash := HashToken(raw)
	now := time.Now().UTC()
	futureExpiry := now.Add(time.Hour)

	rows := sqlmock.NewRows([]string{
		"id", "consumer_name", "scope", "rate_limit_per_minute",
		"expires_at", "revoked_at", "last_used_at", "created_at", "created_by",
	}).AddRow(int64(44), "gateway", string(ScopeRead), 60, futureExpiry, nil, nil, now, "test")

	mock.ExpectQuery("SELECT id, consumer_name, scope, rate_limit_per_minute").
		WithArgs(hash).
		WillReturnRows(rows)
	mock.ExpectExec("UPDATE jetmon_api_keys SET last_used_at").
		WithArgs(int64(44)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	key, err := Lookup(context.Background(), db, raw)
	if err != nil {
		t.Fatalf("Lookup returned error for not-yet-expired key: %v", err)
	}
	if key.ID != 44 {
		t.Fatalf("key.ID = %d, want 44", key.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestLookupRejectsPastExpiresAt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	raw := TokenPrefix + strings.Repeat("D", 52)
	hash := HashToken(raw)
	now := time.Now().UTC()
	pastExpiry := now.Add(-time.Minute)

	rows := sqlmock.NewRows([]string{
		"id", "consumer_name", "scope", "rate_limit_per_minute",
		"expires_at", "revoked_at", "last_used_at", "created_at", "created_by",
	}).AddRow(int64(45), "gateway", string(ScopeRead), 60, pastExpiry, nil, nil, now, "test")

	mock.ExpectQuery("SELECT id, consumer_name, scope, rate_limit_per_minute").
		WithArgs(hash).
		WillReturnRows(rows)

	_, err = Lookup(context.Background(), db, raw)
	if !errors.Is(err, ErrKeyExpired) {
		t.Fatalf("Lookup error = %v, want ErrKeyExpired", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

var keyColumns = []string{
	"id", "consumer_name", "scope", "rate_limit_per_minute",
	"expires_at", "revoked_at", "last_used_at", "created_at", "created_by",
}

func keyRow(id int64, consumer string, scope Scope, rate int, createdAt time.Time, createdBy string) *sqlmock.Rows {
	return sqlmock.NewRows(keyColumns).
		AddRow(id, consumer, string(scope), rate, nil, nil, nil, createdAt, createdBy)
}

func TestCreateUsesDefaultsAndFetchesPersistedKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectExec("INSERT INTO jetmon_api_keys").
		WithArgs(sqlmock.AnyArg(), "gateway", string(ScopeWrite), 30, sqlmock.AnyArg(), "cli").
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectQuery("SELECT id, consumer_name, scope, rate_limit_per_minute").
		WithArgs(int64(7)).
		WillReturnRows(keyRow(7, "gateway", ScopeWrite, 30, now, "cli"))

	raw, key, err := Create(context.Background(), db, CreateInput{
		ConsumerName: "gateway",
		Scope:        ScopeWrite,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(raw, TokenPrefix) {
		t.Fatalf("raw token = %q, want %s prefix", raw, TokenPrefix)
	}
	if key.ID != 7 || key.RateLimitPerMinute != 30 || key.CreatedBy != "cli" {
		t.Fatalf("key = %+v", key)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCreateRejectsInvalidInputBeforeDB(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	if _, _, err := Create(context.Background(), db, CreateInput{Scope: ScopeRead}); err == nil {
		t.Fatal("Create accepted empty consumer_name")
	}
	if _, _, err := Create(context.Background(), db, CreateInput{ConsumerName: "gateway", Scope: Scope("root")}); err == nil {
		t.Fatal("Create accepted invalid scope")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected sql calls: %v", err)
	}
}

func TestListScansKeys(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	expiresAt := now.Add(time.Hour)
	revokedAt := now.Add(-time.Minute)
	lastUsedAt := now.Add(-time.Second)
	rows := sqlmock.NewRows(keyColumns).
		AddRow(int64(1), "gateway", string(ScopeRead), 60, nil, nil, nil, now, "ops").
		AddRow(int64(2), "admin", string(ScopeAdmin), 60, expiresAt, revokedAt, lastUsedAt, now, "ops")
	mock.ExpectQuery("SELECT id, consumer_name, scope, rate_limit_per_minute").
		WillReturnRows(rows)

	keys, err := List(context.Background(), db)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("List len = %d, want 2", len(keys))
	}
	if keys[1].ExpiresAt == nil || keys[1].RevokedAt == nil || keys[1].LastUsedAt == nil {
		t.Fatalf("second key did not scan nullable timestamps: %+v", keys[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestRevokeAlreadyRevokedIsSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectExec("UPDATE jetmon_api_keys SET revoked_at").
		WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT id, consumer_name, scope, rate_limit_per_minute").
		WithArgs(int64(9)).
		WillReturnRows(sqlmock.NewRows(keyColumns).
			AddRow(int64(9), "gateway", string(ScopeRead), 60, nil, now, nil, now, "ops"))

	if err := Revoke(context.Background(), db, 9); err != nil {
		t.Fatalf("Revoke already revoked: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestRevokeMissingKeyReturnsNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE jetmon_api_keys SET revoked_at").
		WithArgs(int64(404)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT id, consumer_name, scope, rate_limit_per_minute").
		WithArgs(int64(404)).
		WillReturnError(sql.ErrNoRows)

	err = Revoke(context.Background(), db, 404)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Revoke missing error = %v, want not found", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestRotateSchedulesOldKeyRevocation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT id, consumer_name, scope, rate_limit_per_minute").
		WithArgs(int64(3)).
		WillReturnRows(keyRow(3, "gateway", ScopeAdmin, 60, now, "ops"))
	mock.ExpectExec("INSERT INTO jetmon_api_keys").
		WithArgs(sqlmock.AnyArg(), "gateway", string(ScopeAdmin), 60, sqlmock.AnyArg(), "operator").
		WillReturnResult(sqlmock.NewResult(4, 1))
	mock.ExpectQuery("SELECT id, consumer_name, scope, rate_limit_per_minute").
		WithArgs(int64(4)).
		WillReturnRows(keyRow(4, "gateway", ScopeAdmin, 60, now, "operator"))
	mock.ExpectExec("UPDATE jetmon_api_keys").
		WithArgs(300, int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	raw, key, err := Rotate(context.Background(), db, 3, 5*time.Minute, "operator")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if !strings.HasPrefix(raw, TokenPrefix) || key.ID != 4 {
		t.Fatalf("Rotate returned raw=%q key=%+v", raw, key)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// Package apikeys manages API tokens stored in jetmon_api_keys.
//
// Tokens are 32 bytes of crypto/rand entropy, base32-encoded with a "jm_"
// prefix (e.g. "jm_NBSWY3DPEHPK3PXP..."). Storage is sha256-hashed; the raw
// token is only ever returned at creation time via the CLI.
//
// This package is the only writer for jetmon_api_keys. The HTTP API exposes
// no key management endpoints — see API.md "Authentication".
package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Scope is the coarse permission level granted to a key. See API.md.
type Scope string

const (
	ScopeRead  Scope = "read"
	ScopeWrite Scope = "write"
	ScopeAdmin Scope = "admin"
)

// AllScopes returns the full set of valid scopes in increasing privilege order.
func AllScopes() []Scope {
	return []Scope{ScopeRead, ScopeWrite, ScopeAdmin}
}

// Includes reports whether s grants at least the privileges of required.
// scope ordering: read < write < admin. admin includes write includes read.
func (s Scope) Includes(required Scope) bool {
	rank := map[Scope]int{ScopeRead: 0, ScopeWrite: 1, ScopeAdmin: 2}
	return rank[s] >= rank[required]
}

// Valid reports whether s is one of the recognized scope values.
func (s Scope) Valid() bool {
	switch s {
	case ScopeRead, ScopeWrite, ScopeAdmin:
		return true
	}
	return false
}

// TokenPrefix is prepended to every generated token. The prefix is part of
// the auth check — tokens without it are rejected at parse time.
const TokenPrefix = "jm_"

// Sentinel errors returned by Lookup. Callers translate to HTTP status codes.
var (
	ErrInvalidToken = errors.New("apikeys: invalid token")
	ErrKeyRevoked   = errors.New("apikeys: key revoked")
	ErrKeyExpired   = errors.New("apikeys: key expired")
)

// Key is the in-memory representation of a jetmon_api_keys row. The raw
// token is never stored here — it's hashed on the way in and discarded.
type Key struct {
	ID                 int64
	ConsumerName       string
	Scope              Scope
	RateLimitPerMinute int
	ExpiresAt          *time.Time
	RevokedAt          *time.Time
	LastUsedAt         *time.Time
	CreatedAt          time.Time
	CreatedBy          string
}

// CreateInput carries the fields needed to create a new key.
type CreateInput struct {
	ConsumerName       string
	Scope              Scope
	RateLimitPerMinute int           // 0 → server default
	TTL                time.Duration // 0 → never expires
	CreatedBy          string        // operator identity for audit; falls back to "cli"
}

// GenerateToken returns a fresh raw token and its sha256 hash. The raw token
// is what the consumer puts in their Authorization header; the hash is what
// goes in the database.
func GenerateToken() (raw, hashed string, err error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", "", fmt.Errorf("apikeys: read entropy: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:])
	raw = TokenPrefix + encoded
	hashed = HashToken(raw)
	return raw, hashed, nil
}

// HashToken returns the sha256 hex digest of token. Used both at creation
// (to store) and at lookup (to compare). sha256 is the right hash here because
// tokens are high-entropy random; bcrypt's deliberate slowness is for human
// passwords.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Create inserts a new key and returns the raw token (one-time view) plus
// the persisted Key record.
func Create(ctx context.Context, db *sql.DB, in CreateInput) (raw string, k *Key, err error) {
	if in.ConsumerName == "" {
		return "", nil, errors.New("apikeys: consumer_name is required")
	}
	if !in.Scope.Valid() {
		return "", nil, fmt.Errorf("apikeys: invalid scope %q (want one of: read, write, admin)", in.Scope)
	}
	rateLimit := in.RateLimitPerMinute
	if rateLimit <= 0 {
		rateLimit = defaultRateLimitForScope(in.Scope)
	}
	createdBy := in.CreatedBy
	if createdBy == "" {
		createdBy = "cli"
	}

	raw, hashed, err := GenerateToken()
	if err != nil {
		return "", nil, err
	}

	var expiresAt sql.NullTime
	if in.TTL > 0 {
		expiresAt = sql.NullTime{Time: time.Now().UTC().Add(in.TTL), Valid: true}
	}

	res, err := db.ExecContext(ctx, `
		INSERT INTO jetmon_api_keys
			(key_hash, consumer_name, scope, rate_limit_per_minute, expires_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?)`,
		hashed, in.ConsumerName, string(in.Scope), rateLimit, expiresAt, createdBy,
	)
	if err != nil {
		return "", nil, fmt.Errorf("apikeys: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, fmt.Errorf("apikeys: last insert id: %w", err)
	}

	k, err = getByID(ctx, db, id)
	if err != nil {
		return "", nil, err
	}
	return raw, k, nil
}

// Lookup resolves a raw token to its Key. Returns ErrInvalidToken for any
// case where the token shouldn't be trusted (malformed, no matching row,
// revoked, expired). Updates last_used_at on success.
func Lookup(ctx context.Context, db *sql.DB, raw string) (*Key, error) {
	if !strings.HasPrefix(raw, TokenPrefix) || len(raw) < len(TokenPrefix)+10 {
		return nil, ErrInvalidToken
	}
	hashed := HashToken(raw)

	k, err := getByHash(ctx, db, hashed)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidToken
		}
		return nil, fmt.Errorf("apikeys: lookup: %w", err)
	}

	// revoked_at and expires_at are half-open cutoffs: the key is valid for
	// times strictly before the cutoff, and rejected at or after it. A future
	// revoked_at therefore acts as a rotation grace window.
	now := time.Now().UTC()
	if k.RevokedAt != nil && !now.Before(*k.RevokedAt) {
		return nil, ErrKeyRevoked
	}
	if k.ExpiresAt != nil && !now.Before(*k.ExpiresAt) {
		return nil, ErrKeyExpired
	}

	// Best-effort last_used_at touch. We swallow errors here so a transient
	// write failure doesn't fail the auth check — last_used_at is observability,
	// not security.
	_, _ = db.ExecContext(ctx,
		`UPDATE jetmon_api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, k.ID)
	return k, nil
}

// List returns all keys for ops display. Hash is never exposed.
func List(ctx context.Context, db *sql.DB) ([]Key, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, consumer_name, scope, rate_limit_per_minute,
		       expires_at, revoked_at, last_used_at, created_at, created_by
		  FROM jetmon_api_keys
		 ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("apikeys: list: %w", err)
	}
	defer rows.Close()

	var out []Key
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// Revoke sets revoked_at on the given key. Idempotent — re-revoking is a no-op.
func Revoke(ctx context.Context, db *sql.DB, id int64) error {
	res, err := db.ExecContext(ctx,
		`UPDATE jetmon_api_keys SET revoked_at = CURRENT_TIMESTAMP WHERE id = ? AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("apikeys: revoke: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Either the key doesn't exist or it was already revoked. Look up
		// to distinguish, so the CLI can give a useful message.
		k, lookupErr := getByID(ctx, db, id)
		if lookupErr != nil {
			if errors.Is(lookupErr, sql.ErrNoRows) {
				return fmt.Errorf("apikeys: key %d not found", id)
			}
			return lookupErr
		}
		if k.RevokedAt != nil {
			// Already revoked — treat as success.
			return nil
		}
	}
	return nil
}

// Rotate creates a new key matching the existing key's consumer/scope/rate-limit
// and schedules the old one to be revoked after gracePeriod. Returns the new
// raw token. If gracePeriod is zero, the old key is revoked immediately.
func Rotate(ctx context.Context, db *sql.DB, oldID int64, gracePeriod time.Duration, createdBy string) (newRaw string, newKey *Key, err error) {
	old, err := getByID(ctx, db, oldID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, fmt.Errorf("apikeys: key %d not found", oldID)
		}
		return "", nil, err
	}
	if old.RevokedAt != nil {
		return "", nil, fmt.Errorf("apikeys: key %d already revoked; create a fresh key instead", oldID)
	}

	// Honor any TTL the original key had — the rotated key inherits it.
	var ttl time.Duration
	if old.ExpiresAt != nil {
		ttl = time.Until(*old.ExpiresAt)
		if ttl < 0 {
			ttl = 0
		}
	}

	newRaw, newKey, err = Create(ctx, db, CreateInput{
		ConsumerName:       old.ConsumerName,
		Scope:              old.Scope,
		RateLimitPerMinute: old.RateLimitPerMinute,
		TTL:                ttl,
		CreatedBy:          createdBy,
	})
	if err != nil {
		return "", nil, err
	}

	if gracePeriod <= 0 {
		if err := Revoke(ctx, db, oldID); err != nil {
			return "", nil, fmt.Errorf("apikeys: rotate (revoke old): %w", err)
		}
	} else {
		// Schedule revocation: set revoked_at to now+grace. Lookup checks
		// revoked_at against the current time, so a future revoked_at is
		// effectively "scheduled."
		_, err := db.ExecContext(ctx,
			`UPDATE jetmon_api_keys
			    SET revoked_at = DATE_ADD(CURRENT_TIMESTAMP, INTERVAL ? SECOND)
			  WHERE id = ?`,
			int(gracePeriod.Seconds()), oldID)
		if err != nil {
			return "", nil, fmt.Errorf("apikeys: rotate (schedule revoke): %w", err)
		}
	}
	return newRaw, newKey, nil
}

func defaultRateLimitForScope(s Scope) int {
	switch s {
	case ScopeWrite:
		return 30
	case ScopeAdmin:
		return 60
	default:
		return 60
	}
}

func getByID(ctx context.Context, db *sql.DB, id int64) (*Key, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, consumer_name, scope, rate_limit_per_minute,
		       expires_at, revoked_at, last_used_at, created_at, created_by
		  FROM jetmon_api_keys
		 WHERE id = ?`, id)
	return scanKey(row)
}

func getByHash(ctx context.Context, db *sql.DB, hash string) (*Key, error) {
	row := db.QueryRowContext(ctx, `
		SELECT id, consumer_name, scope, rate_limit_per_minute,
		       expires_at, revoked_at, last_used_at, created_at, created_by
		  FROM jetmon_api_keys
		 WHERE key_hash = ?`, hash)
	return scanKey(row)
}

// rowScanner accepts both *sql.Row and *sql.Rows so scanKey can be reused.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanKey(s rowScanner) (*Key, error) {
	var k Key
	var scope string
	var expiresAt, revokedAt, lastUsedAt sql.NullTime
	if err := s.Scan(
		&k.ID, &k.ConsumerName, &scope, &k.RateLimitPerMinute,
		&expiresAt, &revokedAt, &lastUsedAt, &k.CreatedAt, &k.CreatedBy,
	); err != nil {
		return nil, err
	}
	k.Scope = Scope(scope)
	if expiresAt.Valid {
		k.ExpiresAt = &expiresAt.Time
	}
	if revokedAt.Valid {
		k.RevokedAt = &revokedAt.Time
	}
	if lastUsedAt.Valid {
		k.LastUsedAt = &lastUsedAt.Time
	}
	return &k, nil
}

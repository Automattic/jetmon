package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/apikeys"
	"github.com/DATA-DOG/go-sqlmock"
)

// keyLookupSQL matches the query used by apikeys.Lookup to resolve a token.
const keyLookupSQL = ` SELECT id, consumer_name, scope, rate_limit_per_minute, expires_at, revoked_at, last_used_at, created_at, created_by FROM jetmon_api_keys WHERE key_hash = ?`

const keyTouchSQL = `UPDATE jetmon_api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`

// columnsKey is the column set returned by getByHash.
var columnsKey = []string{
	"id", "consumer_name", "scope", "rate_limit_per_minute",
	"expires_at", "revoked_at", "last_used_at", "created_at", "created_by",
}

func makeKeyRow(id int64, scope string, rateLimit int, revokedAt, expiresAt *time.Time) *sqlmock.Rows {
	rows := sqlmock.NewRows(columnsKey)
	var rev, exp any
	if revokedAt != nil {
		rev = *revokedAt
	}
	if expiresAt != nil {
		exp = *expiresAt
	}
	rows.AddRow(id, "test-consumer", scope, rateLimit, exp, rev, nil, time.Now().UTC(), "test")
	return rows
}

func TestRequireScopeMissingToken(t *testing.T) {
	s, _, _, cleanup := newTestServer(t)
	defer cleanup()

	called := false
	wrapped := s.requireScope(scopeRead, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := httptest.NewRequest("GET", "/api/v1/anything", nil)
	rec := httptest.NewRecorder()
	wrapped(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("handler should not run without token")
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "missing_token" {
		t.Errorf("error code = %q, want missing_token", body.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("X-Request-ID header should be set")
	}
}

func TestRequireScopeInvalidToken(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	// Lookup will return ErrInvalidToken (no rows).
	mock.ExpectQuery(keyLookupSQL).
		WillReturnRows(sqlmock.NewRows(columnsKey))

	wrapped := s.requireScope(scopeRead, func(w http.ResponseWriter, r *http.Request) {})

	req := httptest.NewRequest("GET", "/api/v1/anything", nil)
	req.Header.Set("Authorization", "Bearer jm_INVALID-LOOKING-TOKEN-XXXXX")
	rec := httptest.NewRecorder()
	wrapped(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "invalid_token" {
		t.Errorf("error code = %q, want invalid_token", body.Code)
	}
}

func TestRequireScopeRevokedToken(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	revokedAt := time.Now().UTC().Add(-time.Hour)
	mock.ExpectQuery(keyLookupSQL).WillReturnRows(makeKeyRow(1, "read", 60, &revokedAt, nil))

	wrapped := s.requireScope(scopeRead, func(w http.ResponseWriter, r *http.Request) {})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer jm_ANYTOKENWILLDOFORTHISTESTXX")
	rec := httptest.NewRecorder()
	wrapped(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "token_revoked" {
		t.Errorf("error code = %q, want token_revoked", body.Code)
	}
}

func TestRequireScopeExpiredToken(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	expiredAt := time.Now().UTC().Add(-time.Hour)
	mock.ExpectQuery(keyLookupSQL).WillReturnRows(makeKeyRow(1, "read", 60, nil, &expiredAt))
	// Lookup also touches last_used_at — but with expired key the expiry check fires first.
	mock.ExpectExec(keyTouchSQL).WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))

	wrapped := s.requireScope(scopeRead, func(w http.ResponseWriter, r *http.Request) {})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer jm_ANYTOKENWILLDOFORTHISTESTXX")
	rec := httptest.NewRecorder()
	wrapped(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "token_expired" {
		t.Errorf("error code = %q, want token_expired", body.Code)
	}
}

func TestRequireScopeInsufficientScope(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(keyLookupSQL).WillReturnRows(makeKeyRow(1, "read", 60, nil, nil))
	mock.ExpectExec(keyTouchSQL).WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))

	called := false
	wrapped := s.requireScope(scopeWrite, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer jm_ANYTOKENWILLDOFORTHISTESTXX")
	rec := httptest.NewRecorder()
	wrapped(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if called {
		t.Fatal("handler should not run with insufficient scope")
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "insufficient_scope" {
		t.Errorf("error code = %q, want insufficient_scope", body.Code)
	}
}

func TestRequireScopeAllowsValidToken(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(keyLookupSQL).WillReturnRows(makeKeyRow(1, "read", 60, nil, nil))
	mock.ExpectExec(keyTouchSQL).WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))

	called := false
	wrapped := s.requireScope(scopeRead, func(w http.ResponseWriter, r *http.Request) {
		called = true
		// Confirm key reached the handler context.
		if k := keyFromRequest(r); k == nil || k.ConsumerName != "test-consumer" {
			t.Errorf("key in handler context = %+v, want test-consumer", k)
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer jm_ANYTOKENWILLDOFORTHISTESTXX")
	rec := httptest.NewRecorder()
	wrapped(rec, req)

	if !called {
		t.Fatal("handler should have run")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-RateLimit-Limit"); got != "60" {
		t.Errorf("X-RateLimit-Limit = %q, want 60", got)
	}
	if got := rec.Header().Get("X-RateLimit-Remaining"); got == "" {
		t.Errorf("X-RateLimit-Remaining missing")
	}
}

func TestRequireScopeRateLimit429(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	// Limit = 1/min — second request should 429. We have to set up two
	// lookup expectations because the limiter check runs after auth.
	mock.MatchExpectationsInOrder(false)
	mock.ExpectQuery(keyLookupSQL).WillReturnRows(makeKeyRow(2, "read", 1, nil, nil))
	mock.ExpectExec(keyTouchSQL).WithArgs(int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(keyLookupSQL).WillReturnRows(makeKeyRow(2, "read", 1, nil, nil))
	mock.ExpectExec(keyTouchSQL).WithArgs(int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))

	wrapped := s.requireScope(scopeRead, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// First request — allowed.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer jm_ANYTOKENWILLDOFORTHISTESTXX")
	rec := httptest.NewRecorder()
	wrapped(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec.Code)
	}

	// Second request — rate limited.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Authorization", "Bearer jm_ANYTOKENWILLDOFORTHISTESTXX")
	rec2 := httptest.NewRecorder()
	wrapped(rec2, req2)

	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429; body=%s", rec2.Code, rec2.Body.String())
	}
	if got := rec2.Header().Get("Retry-After"); got == "" {
		t.Error("Retry-After header missing on 429")
	}
	body := readErrorBody(t, rec2.Body)
	if body.Code != "rate_limited" {
		t.Errorf("error code = %q, want rate_limited", body.Code)
	}
}

func TestStatusRecorderCapturesCode(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	sr.WriteHeader(http.StatusBadRequest)
	if sr.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", sr.status)
	}
	// Second WriteHeader should be a no-op.
	sr.WriteHeader(http.StatusInternalServerError)
	if sr.status != http.StatusBadRequest {
		t.Errorf("status changed after second WriteHeader = %d", sr.status)
	}
}

func TestMapAuthError(t *testing.T) {
	cases := []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{apikeys.ErrInvalidToken, http.StatusUnauthorized, "invalid_token"},
		{apikeys.ErrKeyRevoked, http.StatusUnauthorized, "token_revoked"},
		{apikeys.ErrKeyExpired, http.StatusUnauthorized, "token_expired"},
	}
	for _, c := range cases {
		gotStatus, gotCode, _ := mapAuthError(c.err)
		if gotStatus != c.wantStatus || gotCode != c.wantCode {
			t.Errorf("mapAuthError(%v) = (%d, %q), want (%d, %q)",
				c.err, gotStatus, gotCode, c.wantStatus, c.wantCode)
		}
	}
}

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Automattic/jetmon/internal/apikeys"
)

func TestHealthOK(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectPing()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	readJSON(t, rec.Body, &body)
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want 'ok'", body["status"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestHealthDBDown(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectPing().WillReturnError(errPing{})

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "db_unavailable" {
		t.Errorf("error code = %q, want db_unavailable", body.Code)
	}
}

// errPing is a stand-in error type for db.PingContext failures since sqlmock's
// ExpectPing accepts any error.
type errPing struct{}

func (errPing) Error() string { return "ping failed" }

func TestMeReturnsAuthenticatedKey(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()
	key.ConsumerName = "alerts-worker"
	key.Scope = apikeys.ScopeRead
	key.RateLimitPerMinute = 600

	req := requestWithKey("GET", "/api/v1/me", key)
	rec := httptest.NewRecorder()
	s.handleMe(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body meResponse
	readJSON(t, rec.Body, &body)
	if body.ConsumerName != "alerts-worker" {
		t.Errorf("consumer_name = %q, want alerts-worker", body.ConsumerName)
	}
	if body.Scope != "read" {
		t.Errorf("scope = %q, want read", body.Scope)
	}
	if body.RateLimitPerMinute != 600 {
		t.Errorf("rate_limit_per_minute = %d, want 600", body.RateLimitPerMinute)
	}
	if body.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want nil", *body.ExpiresAt)
	}
}

func TestMeMissingKeyReturns500(t *testing.T) {
	// /me running without an authenticated key in context indicates middleware
	// was bypassed in error. The handler refuses to guess.
	s, _, _, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	rec := httptest.NewRecorder()
	s.handleMe(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "auth_state_missing" {
		t.Errorf("error code = %q, want auth_state_missing", body.Code)
	}
}

func TestKeyFromRequestNilContext(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if k := keyFromRequest(req); k != nil {
		t.Errorf("keyFromRequest(no ctx) = %+v, want nil", k)
	}
}

func TestKeyFromRequestPopulated(t *testing.T) {
	want := &apikeys.Key{ID: 7, ConsumerName: "x"}
	req := httptest.NewRequest("GET", "/", nil)
	ctx := context.WithValue(req.Context(), ctxKeyAPIKey, want)
	got := keyFromRequest(req.WithContext(ctx))
	if got == nil || got.ID != 7 {
		t.Errorf("keyFromRequest = %+v, want %+v", got, want)
	}
}

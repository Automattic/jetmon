package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/apikeys"
	"github.com/DATA-DOG/go-sqlmock"
)

// newTestServer builds a Server backed by sqlmock plus a stub key the tests
// can use as the authenticated identity. Returns the Server, the mock
// (for setting expectations), the canned key, and a cleanup func.
//
// The stub key has scope=admin and a high rate limit so individual tests
// don't have to set those up. Tests that exercise scope/rate-limit edges
// override key.Scope or key.RateLimitPerMinute directly.
//
// QueryMatcherEqual is used so tests assert the exact production SQL string.
// If a query in production gets reformatted, the test fails — which is the
// behavior we want for an internal API where SQL is part of the contract
// with the schema.
func newTestServer(t *testing.T) (*Server, sqlmock.Sqlmock, *apikeys.Key, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(
		sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual),
		sqlmock.MonitorPingsOption(true),
	)
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	s := New(":0", db, "test-host")

	key := &apikeys.Key{
		ID:                 1,
		ConsumerName:       "test-consumer",
		Scope:              apikeys.ScopeAdmin,
		RateLimitPerMinute: 1000,
		CreatedAt:          time.Now().UTC(),
	}

	cleanup := func() {
		_ = db.Close()
	}
	return s, mock, key, cleanup
}

// requestWithKey returns an *http.Request whose context already has the
// authenticated key and a request id attached, so handlers can be invoked
// directly without going through requireScope.
func requestWithKey(method, target string, key *apikeys.Key) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	ctx := context.WithValue(req.Context(), ctxKeyAPIKey, key)
	ctx = context.WithValue(ctx, ctxKeyRequestID, "test-request-id")
	return req.WithContext(ctx)
}

// setAuthCtx attaches an authenticated key + request id to an existing
// request, preserving its body and path values. Used for handler tests
// that need to send a JSON body alongside an authenticated context.
func setAuthCtx(req *http.Request, key *apikeys.Key) *http.Request {
	ctx := context.WithValue(req.Context(), ctxKeyAPIKey, key)
	ctx = context.WithValue(ctx, ctxKeyRequestID, "test-request-id")
	return req.WithContext(ctx)
}

// readJSON decodes the response body into the target struct.
func readJSON(t *testing.T, body io.Reader, target any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// readErrorBody parses the standard error envelope.
func readErrorBody(t *testing.T, body io.Reader) errorBody {
	t.Helper()
	var env errorEnvelope
	readJSON(t, body, &env)
	return env.Error
}

// invokeAuthed runs an authenticated request through the mux by injecting
// the key into the request context first. The mux's requireScope wrapper
// will still fire — but bearerToken returns "" so we'd hit "missing token".
// Instead, we bypass auth entirely by serving the handler directly. For
// tests that need the auth middleware, use newAuthRequest with a real token
// hash expectation.
//
// This indirection exists because requireScope tightly couples auth, scope,
// rate limiting, and audit — and we test those independently.
func invokeAuthed(_ *Server, req *http.Request, h http.HandlerFunc) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// columnsSite is the column set returned by the site list query.
var columnsSite = []string{
	"blog_id", "public_id", "monitor_url", "monitor_active", "site_status",
	"last_checked_at", "last_status_change", "ssl_expiry_date", "check_keyword",
	"redirect_policy", "maintenance_start", "maintenance_end", "alert_cooldown_minutes",
}

// columnsActiveEvent is the column set returned by queryActiveEvents.
var columnsActiveEvent = []string{
	"id", "check_type", "severity", "state", "started_at",
}

// columnsEvent is the column set returned by event queries.
var columnsEvent = []string{
	"id", "blog_id", "endpoint_id", "check_type", "discriminator",
	"severity", "state", "started_at", "ended_at", "resolution_reason",
	"cause_event_id", "metadata",
}

// columnsTransition is the column set returned by transition queries.
var columnsTransition = []string{
	"id", "event_id", "severity_before", "severity_after",
	"state_before", "state_after", "reason", "source", "metadata", "changed_at",
}

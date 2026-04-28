package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/apikeys"
	"github.com/DATA-DOG/go-sqlmock"
)

// scopeProbeKey returns the key-lookup row a token check should yield for
// a given scope. The middleware also touches last_used_at, so every test
// expecting a successful auth has to expect both the SELECT and the UPDATE.
func scopeProbeKey(scope string, rateLimit int) *sqlmock.Rows {
	return sqlmock.NewRows(columnsKey).AddRow(
		int64(1), "test-consumer", scope, rateLimit,
		nil, nil, nil, time.Now().UTC(), "test",
	)
}

// expectAuthLookup primes mock with the SELECT + UPDATE pair the middleware
// runs on a successful token resolution. Returns nothing — the call sets
// up the expectation directly.
func expectAuthLookup(mock sqlmock.Sqlmock, scope string) {
	mock.ExpectQuery(keyLookupSQL).WillReturnRows(scopeProbeKey(scope, 1000))
	mock.ExpectExec(keyTouchSQL).WithArgs(int64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
}

// phase2WriteEndpoints lists every Phase 2 write route that should require
// scope=write. Each entry uses path values that won't actually exist in the
// DB — we only care about the auth/scope decision, which fires before any
// DB access.
var phase2WriteEndpoints = []struct {
	method, path string
}{
	{"POST", "/api/v1/sites"},
	{"PATCH", "/api/v1/sites/42"},
	{"DELETE", "/api/v1/sites/42"},
	{"POST", "/api/v1/sites/42/pause"},
	{"POST", "/api/v1/sites/42/resume"},
	{"POST", "/api/v1/sites/42/trigger-now"},
	{"POST", "/api/v1/sites/42/events/7/close"},
}

// phase2ReadEndpoints covers the read side. read scope should pass the
// scope check (we don't assert a specific 200 because the DB call after the
// check would need its own mocks — what we want here is "not 403").
var phase2ReadEndpoints = []struct {
	method, path string
}{
	{"GET", "/api/v1/openapi.json"},
	{"GET", "/api/v1/sites"},
	{"GET", "/api/v1/sites/42"},
	{"GET", "/api/v1/sites/42/events"},
	{"GET", "/api/v1/sites/42/events/7"},
	{"GET", "/api/v1/sites/42/events/7/transitions"},
	{"GET", "/api/v1/events/7"},
	{"GET", "/api/v1/sites/42/uptime"},
	{"GET", "/api/v1/sites/42/response-time"},
	{"GET", "/api/v1/sites/42/timing-breakdown"},
}

func TestPhase2WriteEndpointsRejectReadToken(t *testing.T) {
	// A read-scope token hitting a write endpoint must get 403
	// insufficient_scope, not pass through to the handler.
	for _, ep := range phase2WriteEndpoints {
		t.Run(ep.method+"_"+ep.path, func(t *testing.T) {
			s, mock, _, cleanup := newTestServer(t)
			defer cleanup()

			// Auth lookup succeeds with read scope.
			expectAuthLookup(mock, "read")

			req := httptest.NewRequest(ep.method, ep.path, bytes.NewReader([]byte(`{}`)))
			req.Header.Set("Authorization", "Bearer jm_TOKENXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			s.routes().ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}
			if got := readErrorBody(t, rec.Body).Code; got != "insufficient_scope" {
				t.Errorf("code = %q, want insufficient_scope", got)
			}
		})
	}
}

func TestPhase2WriteEndpointsAcceptWriteToken(t *testing.T) {
	// Write-scope tokens should pass scope enforcement and reach the
	// handler. We assert that the response is NOT 403 (the handler
	// itself may then 400/404 due to missing DB rows, but that's a
	// downstream concern; the gate we care about here is the scope check).
	for _, ep := range phase2WriteEndpoints {
		t.Run(ep.method+"_"+ep.path, func(t *testing.T) {
			s, mock, _, cleanup := newTestServer(t)
			defer cleanup()

			expectAuthLookup(mock, "write")
			// We don't know exactly which DB queries each handler will run
			// after the scope check (each endpoint is different), so allow
			// any further queries to fail without causing test failure.
			mock.MatchExpectationsInOrder(false)

			req := httptest.NewRequest(ep.method, ep.path, bytes.NewReader([]byte(`{}`)))
			req.Header.Set("Authorization", "Bearer jm_TOKENXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			s.routes().ServeHTTP(rec, req)

			if rec.Code == http.StatusForbidden {
				t.Errorf("write scope unexpectedly hit 403 on %s %s; body=%s",
					ep.method, ep.path, rec.Body.String())
			}
			if rec.Code == http.StatusUnauthorized {
				t.Errorf("write scope unexpectedly hit 401 on %s %s; body=%s",
					ep.method, ep.path, rec.Body.String())
			}
		})
	}
}

func TestPhase2ReadEndpointsAcceptReadToken(t *testing.T) {
	// Read-scope tokens should pass scope enforcement on read endpoints.
	for _, ep := range phase2ReadEndpoints {
		t.Run(ep.method+"_"+ep.path, func(t *testing.T) {
			s, mock, _, cleanup := newTestServer(t)
			defer cleanup()

			expectAuthLookup(mock, "read")
			mock.MatchExpectationsInOrder(false)

			req := httptest.NewRequest(ep.method, ep.path, nil)
			req.Header.Set("Authorization", "Bearer jm_TOKENXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX")
			rec := httptest.NewRecorder()
			s.routes().ServeHTTP(rec, req)

			if rec.Code == http.StatusForbidden {
				t.Errorf("read scope unexpectedly hit 403 on %s %s; body=%s",
					ep.method, ep.path, rec.Body.String())
			}
		})
	}
}

func TestPhase2WriteEndpointsRejectMissingToken(t *testing.T) {
	// No Authorization header → 401 missing_token (no DB lookup expected).
	s, _, _, cleanup := newTestServer(t)
	defer cleanup()

	for _, ep := range phase2WriteEndpoints {
		req := httptest.NewRequest(ep.method, ep.path, bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", ep.method, ep.path, rec.Code)
		}
		if got := readErrorBody(t, rec.Body).Code; got != "missing_token" {
			t.Errorf("%s %s code = %q, want missing_token", ep.method, ep.path, got)
		}
	}
}

func TestAdminTokenCanReachAllScopes(t *testing.T) {
	// admin includes write includes read — verify by hitting both a read
	// and a write endpoint with an admin token.
	scopes := []apikeys.Scope{apikeys.ScopeAdmin}
	for _, scope := range scopes {
		s, mock, _, cleanup := newTestServer(t)
		defer cleanup()

		expectAuthLookup(mock, string(scope))
		expectAuthLookup(mock, string(scope))
		mock.MatchExpectationsInOrder(false)

		// Read endpoint
		readReq := httptest.NewRequest("GET", "/api/v1/me", nil)
		readReq.Header.Set("Authorization", "Bearer jm_ADMINXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX")
		readRec := httptest.NewRecorder()
		s.routes().ServeHTTP(readRec, readReq)
		if readRec.Code == http.StatusForbidden {
			t.Errorf("admin scope unexpectedly hit 403 on /me with scope=%s", scope)
		}

		// Write endpoint
		writeReq := httptest.NewRequest("POST", "/api/v1/sites/42/pause", nil)
		writeReq.Header.Set("Authorization", "Bearer jm_ADMINXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX")
		writeRec := httptest.NewRecorder()
		s.routes().ServeHTTP(writeRec, writeReq)
		if writeRec.Code == http.StatusForbidden {
			t.Errorf("admin scope unexpectedly hit 403 on POST /pause with scope=%s", scope)
		}
	}
}

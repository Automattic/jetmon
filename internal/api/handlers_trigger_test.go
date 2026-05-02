package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/DATA-DOG/go-sqlmock"
)

const readSiteForCheckSQL = ` SELECT monitor_url, timeout_seconds, check_keyword, forbidden_keyword, forbidden_keywords, custom_headers, redirect_policy, site_status FROM jetpack_monitor_sites WHERE blog_id = ?`

var columnsSiteForCheck = []string{"monitor_url", "timeout_seconds", "check_keyword", "forbidden_keyword", "forbidden_keywords", "custom_headers", "redirect_policy", "site_status"}

func TestTriggerNowSiteNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// readSiteForCheck returns no rows.
	mock.ExpectQuery(readSiteForCheckSQL).WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows(columnsSiteForCheck))

	req := httptest.NewRequest("POST", "/api/v1/sites/99/trigger-now", nil)
	req.SetPathValue("id", "99")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleTriggerNow)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if got := readErrorBody(t, rec.Body).Code; got != "site_not_found" {
		t.Errorf("code = %q, want site_not_found", got)
	}
}

func TestTriggerNowSuccessNoActiveEvents(t *testing.T) {
	// Spin up a fake target that returns 200 OK so checker.Check returns success.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	defer target.Close()

	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(readSiteForCheckSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows(columnsSiteForCheck).
			AddRow(target.URL, nil, nil, nil, nil, nil, "follow", 1))
	mock.ExpectQuery(`SELECT id FROM jetmon_events WHERE blog_id = ? AND ended_at IS NULL`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	req := httptest.NewRequest("POST", "/api/v1/sites/42/trigger-now", nil)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleTriggerNow)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp triggerNowResponse
	readJSON(t, rec.Body, &resp)
	if !resp.Result.Success {
		t.Errorf("expected success=true; got %+v", resp.Result)
	}
	if resp.Result.HTTPCode != 200 {
		t.Errorf("http_code = %d, want 200", resp.Result.HTTPCode)
	}
	if len(resp.ActiveEventsClosed) != 0 {
		t.Errorf("active_events_closed = %v, want empty", resp.ActiveEventsClosed)
	}
}

func TestTriggerNowForbiddenKeywordFailsCheck(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK FORBIDDEN OK"))
	}))
	defer target.Close()

	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(readSiteForCheckSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows(columnsSiteForCheck).
			AddRow(target.URL, nil, nil, "FORBIDDEN", nil, nil, "follow", 1))

	req := httptest.NewRequest("POST", "/api/v1/sites/42/trigger-now", nil)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleTriggerNow)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp triggerNowResponse
	readJSON(t, rec.Body, &resp)
	if resp.Result.Success {
		t.Fatalf("expected success=false; got %+v", resp.Result)
	}
	if resp.Result.ErrorCode != checker.ErrorKeyword {
		t.Fatalf("error_code = %d, want %d", resp.Result.ErrorCode, checker.ErrorKeyword)
	}
	if len(resp.ActiveEventsClosed) != 0 {
		t.Fatalf("active_events_closed = %v, want empty", resp.ActiveEventsClosed)
	}
}

func TestTriggerNowWithGatewayTenantAllowsMappedSite(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteTenantCheckSQL).
		WithArgs("tenant-a", int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(readSiteForCheckSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows(columnsSiteForCheck).
			AddRow(target.URL, nil, nil, nil, nil, nil, "follow", 1))
	mock.ExpectQuery(`SELECT id FROM jetmon_events WHERE blog_id = ? AND ended_at IS NULL`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))

	req := httptest.NewRequest("POST", "/api/v1/sites/42/trigger-now", nil)
	req.SetPathValue("id", "42")
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleTriggerNow)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp triggerNowResponse
	readJSON(t, rec.Body, &resp)
	if !resp.Result.Success {
		t.Errorf("expected success=true; got %+v", resp.Result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestTriggerNowSuccessClosesActiveEvent(t *testing.T) {
	// Same as above but with one active event that should be closed
	// with reason=probe_cleared on success.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(readSiteForCheckSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows(columnsSiteForCheck).
			AddRow(target.URL, nil, nil, nil, nil, nil, "follow", 2))
	mock.ExpectQuery(`SELECT id FROM jetmon_events WHERE blog_id = ? AND ended_at IS NULL`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))

	expectCloseEventTx(mock, 7, 42, 4, "Down", "probe_cleared")

	req := httptest.NewRequest("POST", "/api/v1/sites/42/trigger-now", nil)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleTriggerNow)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp triggerNowResponse
	readJSON(t, rec.Body, &resp)
	if len(resp.ActiveEventsClosed) != 1 || resp.ActiveEventsClosed[0] != 7 {
		t.Errorf("active_events_closed = %v, want [7]", resp.ActiveEventsClosed)
	}
	if resp.CurrentState != "Up" {
		t.Errorf("current_state = %q, want Up after close-on-success", resp.CurrentState)
	}
}

func TestTriggerNowInvalidSiteID(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("POST", "/api/v1/sites/abc/trigger-now", nil)
	req.SetPathValue("id", "abc")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleTriggerNow)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRoutesIncludePhase2WriteEndpoints(t *testing.T) {
	// Sanity: every Phase 2 write endpoint resolves through the mux and
	// reaches an authenticated handler (which then returns 401 because no
	// token is provided — that's fine, it confirms the route exists and
	// runs through requireScope rather than hitting the catch-all 404).
	s := New(":0", nil, "test")
	mux := s.routes()

	cases := []struct {
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
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			body := readErrorBody(t, rec.Body)
			if body.Code == "endpoint_not_found" {
				t.Errorf("%s %s hit catch-all 404; route not registered", c.method, c.path)
			}
		}
	}
}

package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const siteExistsCheckSQL = `SELECT 1 FROM jetpack_monitor_sites WHERE blog_id = ? LIMIT 1`

const insertSiteSQL = ` INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval, check_keyword, forbidden_keyword, forbidden_keywords, redirect_policy, timeout_seconds, custom_headers, alert_cooldown_minutes) VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?)`

func newPOSTWithBody(target string, body []byte) *http.Request {
	req := httptest.NewRequest("POST", target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newPATCHWithBody(target string, body []byte) *http.Request {
	req := httptest.NewRequest("PATCH", target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestParseMaintenanceTime(t *testing.T) {
	if got, err := parseMaintenanceTime("", "maintenance_start"); err != nil || got != nil {
		t.Fatalf("parseMaintenanceTime(empty) = (%v, %v), want (nil, nil)", got, err)
	}

	got, err := parseMaintenanceTime("2026-04-27T12:00:00-05:00", "maintenance_start")
	if err != nil {
		t.Fatalf("parseMaintenanceTime(valid): %v", err)
	}
	want := time.Date(2026, 4, 27, 17, 0, 0, 0, time.UTC)
	if got != want {
		t.Fatalf("parseMaintenanceTime(valid) = %v, want %v", got, want)
	}

	if _, err := parseMaintenanceTime("April 27", "maintenance_start"); err == nil {
		t.Fatal("parseMaintenanceTime(invalid) returned nil error")
	}
}

func TestEncodeForbiddenKeywords(t *testing.T) {
	values := []string{"metrics.evil-cdn.example/collect.js", "buy cheap viagra", "buy cheap viagra"}
	got, err := encodeForbiddenKeywords(&values)
	if err != nil {
		t.Fatalf("encodeForbiddenKeywords() error = %v", err)
	}
	if got != `["metrics.evil-cdn.example/collect.js","buy cheap viagra"]` {
		t.Fatalf("encoded forbidden_keywords = %#v", got)
	}

	empty := []string{}
	got, err = encodeForbiddenKeywords(&empty)
	if err != nil {
		t.Fatalf("encodeForbiddenKeywords(empty) error = %v", err)
	}
	if got != nil {
		t.Fatalf("encodeForbiddenKeywords(empty) = %#v, want nil", got)
	}
}

func TestEncodeForbiddenKeywordsRejectsEmptyValue(t *testing.T) {
	values := []string{"ok", ""}
	if _, err := encodeForbiddenKeywords(&values); err == nil {
		t.Fatal("encodeForbiddenKeywords() error = nil, want empty value error")
	}
}

func TestCreateSiteHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// existence check returns no rows
	mock.ExpectQuery(siteExistsCheckSQL).WithArgs(int64(12345)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	// insert
	mock.ExpectExec(insertSiteSQL).
		WithArgs(int64(12345), 12, "https://example.com", 1, 9,
			nil, nil, nil, "follow", nil, nil, nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	// read-back
	mock.ExpectQuery(singleSiteSQL).WithArgs(int64(12345)).
		WillReturnRows(makeSiteRowWithSchedule(12345, "https://example.com", 1, 12, 9))

	body := []byte(`{"blog_id": 12345, "monitor_url": "https://example.com", "bucket_no": 12, "check_interval": 9}`)
	req := newPOSTWithBody("/api/v1/sites", body)
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCreateSite)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp siteResponse
	readJSON(t, rec.Body, &resp)
	if resp.ID != 12345 || resp.MonitorURL != "https://example.com" {
		t.Errorf("response site = %+v", resp)
	}
	if resp.BucketNo != 12 || resp.CheckInterval != 9 {
		t.Errorf("scheduling fields = (%d, %d), want (12, 9)", resp.BucketNo, resp.CheckInterval)
	}
}

func TestCreateSiteWithGatewayTenantAssignsMapping(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsCheckSQL).WithArgs(int64(12345)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
	mock.ExpectBegin()
	mock.ExpectExec(insertSiteSQL).
		WithArgs(int64(12345), 0, "https://example.com", 1, 5,
			nil, nil, nil, "follow", nil, nil, nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(insertSiteTenantTestSQL).
		WithArgs("tenant-a", int64(12345)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(singleSiteSQL).WithArgs(int64(12345)).
		WillReturnRows(makeSiteRow(12345, "https://example.com", 1))

	body := []byte(`{"blog_id": 12345, "monitor_url": "https://example.com"}`)
	req := newPOSTWithBody("/api/v1/sites", body)
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleCreateSite)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestCreateSiteRejectsMissingBlogID(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte(`{"monitor_url": "https://example.com"}`)
	req := setAuthCtx(newPOSTWithBody("/api/v1/sites", body), key)
	rec := invokeAuthed(s, req, s.handleCreateSite)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := readErrorBody(t, rec.Body).Code; got != "invalid_blog_id" {
		t.Errorf("code = %q, want invalid_blog_id", got)
	}
}

func TestCreateSiteRejectsBadURL(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	cases := []struct {
		body string
		want string
	}{
		{`{"blog_id": 1, "monitor_url": "not-a-url"}`, "invalid_url"},
		{`{"blog_id": 1, "monitor_url": ""}`, "invalid_url"},
		{`{"blog_id": 1, "monitor_url": "ftp://example.com"}`, "invalid_url"},
	}
	for _, c := range cases {
		req := setAuthCtx(newPOSTWithBody("/api/v1/sites", []byte(c.body)), key)
		rec := invokeAuthed(s, req, s.handleCreateSite)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body=%s status=%d want 400", c.body, rec.Code)
			continue
		}
		if got := readErrorBody(t, rec.Body).Code; got != c.want {
			t.Errorf("body=%s code=%q want %q", c.body, got, c.want)
		}
	}
}

func TestCreateSiteRejectsBadRedirectPolicy(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte(`{"blog_id": 1, "monitor_url": "https://x", "redirect_policy": "bounce"}`)
	req := setAuthCtx(newPOSTWithBody("/api/v1/sites", body), key)
	rec := invokeAuthed(s, req, s.handleCreateSite)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if got := readErrorBody(t, rec.Body).Code; got != "invalid_redirect_policy" {
		t.Errorf("code = %q, want invalid_redirect_policy", got)
	}
}

func TestCreateSiteConflictOnExisting(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsCheckSQL).WithArgs(int64(12345)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	body := []byte(`{"blog_id": 12345, "monitor_url": "https://example.com"}`)
	req := setAuthCtx(newPOSTWithBody("/api/v1/sites", body), key)
	rec := invokeAuthed(s, req, s.handleCreateSite)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got := readErrorBody(t, rec.Body).Code; got != "site_exists" {
		t.Errorf("code = %q, want site_exists", got)
	}
}

func TestUpdateSiteHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsCheckSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectExec(`UPDATE jetpack_monitor_sites SET monitor_url = ?, redirect_policy = ? WHERE blog_id = ?`).
		WithArgs("https://new.example.com", "alert", int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(singleSiteSQL).WithArgs(int64(42)).
		WillReturnRows(makeSiteRow(42, "https://new.example.com", 1))

	body := []byte(`{"monitor_url": "https://new.example.com", "redirect_policy": "alert"}`)
	req := newPATCHWithBody("/api/v1/sites/42", body)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleUpdateSite)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateSiteWithGatewayTenantRejectsUnmappedSite(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteTenantCheckSQL).
		WithArgs("tenant-a", int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))

	body := []byte(`{"monitor_url": "https://new.example.com"}`)
	req := newPATCHWithBody("/api/v1/sites/42", body)
	req.SetPathValue("id", "42")
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleUpdateSite)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if got := readErrorBody(t, rec.Body).Code; got != "site_not_found" {
		t.Fatalf("code = %q, want site_not_found", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestUpdateSiteEmptyBodyReturnsCurrent(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsCheckSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(singleSiteSQL).WithArgs(int64(42)).
		WillReturnRows(makeSiteRow(42, "https://x", 1))

	body := []byte(`{}`)
	req := newPATCHWithBody("/api/v1/sites/42", body)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleUpdateSite)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestUpdateSiteNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsCheckSQL).WithArgs(int64(999)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))

	body := []byte(`{"monitor_url": "https://x"}`)
	req := newPATCHWithBody("/api/v1/sites/999", body)
	req.SetPathValue("id", "999")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleUpdateSite)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDeleteSiteSoftDeletes(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsCheckSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	// closeAllActiveEvents queries for active events; return none.
	mock.ExpectQuery(`SELECT id FROM jetmon_events WHERE blog_id = ? AND ended_at IS NULL`).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	// soft-delete UPDATE
	mock.ExpectExec(`UPDATE jetpack_monitor_sites SET monitor_active = 0 WHERE blog_id = ?`).
		WithArgs(int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))

	req := httptest.NewRequest("DELETE", "/api/v1/sites/42", nil)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleDeleteSite)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestPauseSiteClosesActiveEvents(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsCheckSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	// One active event to close.
	mock.ExpectQuery(`SELECT id FROM jetmon_events WHERE blog_id = ? AND ended_at IS NULL`).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))

	// closeEvent runs in a tx: BeginTx → SELECT FOR UPDATE → UPDATE event → INSERT transition → Commit
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT severity, state, ended_at FROM jetmon_events WHERE id = ? FOR UPDATE`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"severity", "state", "ended_at"}).
			AddRow(uint8(4), "Down", nil))
	mock.ExpectExec(` UPDATE jetmon_events SET ended_at = CURRENT_TIMESTAMP(3), resolution_reason = ? WHERE id = ?`).
		WithArgs("manual_override", int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(` INSERT INTO jetmon_event_transitions (event_id, blog_id, severity_before, severity_after, state_before, state_after, reason, source, metadata) VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?)`).
		WithArgs(int64(7), int64(42), uint8(4), "Down", "Resolved", "manual_override", "api", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(countActiveEventsSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(projectRunningSQL).
		WithArgs(sqlmock.AnyArg(), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	mock.ExpectExec(`UPDATE jetpack_monitor_sites SET monitor_active = ?, last_status_change = ? WHERE blog_id = ?`).
		WithArgs(0, sqlmock.AnyArg(), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(singleSiteSQL).WithArgs(int64(42)).
		WillReturnRows(makeSiteRow(42, "https://x", 1))

	req := newPOSTWithBody("/api/v1/sites/42/pause", nil)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handlePauseSite)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestResumeSiteSetsActive(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsCheckSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectExec(`UPDATE jetpack_monitor_sites SET monitor_active = ?, last_status_change = ? WHERE blog_id = ?`).
		WithArgs(1, sqlmock.AnyArg(), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(singleSiteSQL).WithArgs(int64(42)).
		WillReturnRows(makeSiteRow(42, "https://x", 1))

	req := newPOSTWithBody("/api/v1/sites/42/resume", nil)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleResumeSite)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestValidateMonitorURL(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
	}{
		{"https://example.com", true},
		{"http://example.com/path", true},
		{"https://example.com:8080/api", true},
		{"", false},
		{"not-a-url", false},
		{"ftp://example.com", false},
		{"http://", false}, // empty host
		{"https:///path", false},
	}
	for _, c := range cases {
		err := validateMonitorURL(c.in)
		if c.valid && err != nil {
			t.Errorf("validateMonitorURL(%q) errored: %v", c.in, err)
		}
		if !c.valid && err == nil {
			t.Errorf("validateMonitorURL(%q) accepted, want rejection", c.in)
		}
	}
}

func TestEncodeCustomHeaders(t *testing.T) {
	if v, err := encodeCustomHeaders(nil); v != nil || err != nil {
		t.Errorf("nil input = (%v, %v), want (nil, nil)", v, err)
	}
	empty := map[string]string{}
	if v, err := encodeCustomHeaders(&empty); v != nil || err != nil {
		t.Errorf("empty input = (%v, %v), want (nil, nil)", v, err)
	}
	full := map[string]string{"X-Foo": "bar"}
	v, err := encodeCustomHeaders(&full)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}
	if v == nil {
		t.Fatal("expected non-nil JSON")
	}
	bad := map[string]string{"": "bad"}
	if _, err := encodeCustomHeaders(&bad); err == nil {
		t.Error("empty header name should error")
	}
}

func TestBoolToTinyint(t *testing.T) {
	if boolToTinyint(true) != 1 {
		t.Error("true → 1")
	}
	if boolToTinyint(false) != 0 {
		t.Error("false → 0")
	}
}

func TestBuildUpdateSetClauseEmpty(t *testing.T) {
	clauses, args, err := buildUpdateSetClause(updateSiteRequest{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(clauses) != 0 || len(args) != 0 {
		t.Errorf("empty body should produce no clauses; got %v / %v", clauses, args)
	}
}

func TestBuildUpdateSetClauseHandlesAllFields(t *testing.T) {
	url := "https://x"
	active := true
	bucket := 5
	keyword := "ok"
	forbiddenKeywords := []string{"metrics.evil-cdn.example/collect.js", "buy cheap viagra"}
	policy := "alert"
	timeout := 30
	headers := map[string]string{"X-A": "1"}
	cooldown := 10
	interval := 7
	clauses, args, err := buildUpdateSetClause(updateSiteRequest{
		MonitorURL:           &url,
		MonitorActive:        &active,
		BucketNo:             &bucket,
		CheckKeyword:         &keyword,
		ForbiddenKeywords:    &forbiddenKeywords,
		RedirectPolicy:       &policy,
		TimeoutSeconds:       &timeout,
		CustomHeaders:        &headers,
		AlertCooldownMinutes: &cooldown,
		CheckInterval:        &interval,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(clauses) != 10 || len(args) != 10 {
		t.Errorf("expected 10 clauses, got clauses=%d args=%d", len(clauses), len(args))
	}
}

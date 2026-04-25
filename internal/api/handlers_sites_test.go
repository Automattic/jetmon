package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const sitesListSQL = ` SELECT blog_id, blog_id AS public_id, monitor_url, monitor_active, site_status, last_checked_at, last_status_change, ssl_expiry_date, check_keyword, redirect_policy, maintenance_start, maintenance_end, alert_cooldown_minutes FROM jetpack_monitor_sites WHERE blog_id > ? ORDER BY blog_id ASC LIMIT ?`

const singleSiteSQL = ` SELECT blog_id, blog_id AS public_id, monitor_url, monitor_active, site_status, last_checked_at, last_status_change, ssl_expiry_date, check_keyword, redirect_policy, maintenance_start, maintenance_end, alert_cooldown_minutes FROM jetpack_monitor_sites WHERE blog_id = ?`

const activeEventsSQL = ` SELECT id, check_type, severity, state, started_at FROM jetmon_events WHERE blog_id = ? AND ended_at IS NULL ORDER BY severity DESC, started_at ASC`

// makeSiteRow returns a row builder pre-loaded with sane defaults the tests
// can override. blog_id is the only required field.
func makeSiteRow(blogID int64, monitorURL string, siteStatus int) *sqlmock.Rows {
	return sqlmock.NewRows(columnsSite).AddRow(
		blogID, blogID, monitorURL, 1, siteStatus,
		nil, nil, nil, nil,
		"follow", nil, nil, nil,
	)
}

func TestListSitesEmpty(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(sitesListSQL).
		WithArgs(int64(0), 51).
		WillReturnRows(sqlmock.NewRows(columnsSite))

	req := requestWithKey("GET", "/api/v1/sites", key)
	rec := invokeAuthed(s, req, s.handleListSites)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp ListEnvelope
	readJSON(t, rec.Body, &resp)
	if data, ok := resp.Data.([]any); !ok || len(data) != 0 {
		t.Errorf("data = %v, want empty list", resp.Data)
	}
	if resp.Page.Next != nil {
		t.Errorf("expected no next cursor, got %v", *resp.Page.Next)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestListSitesReturnsRows(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	rows := makeSiteRow(101, "https://example.com", 1)
	rows.AddRow(102, 102, "https://other.com", 1, 2,
		nil, nil, nil, nil, "follow", nil, nil, nil)

	mock.ExpectQuery(sitesListSQL).
		WithArgs(int64(0), 51).
		WillReturnRows(rows)

	req := requestWithKey("GET", "/api/v1/sites", key)
	rec := invokeAuthed(s, req, s.handleListSites)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []siteResponse `json:"data"`
		Page Page           `json:"page"`
	}
	readJSON(t, rec.Body, &resp)
	if len(resp.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].ID != 101 || resp.Data[0].CurrentState != "Up" {
		t.Errorf("first site = %+v, want id=101 state=Up", resp.Data[0])
	}
	if resp.Data[1].ID != 102 || resp.Data[1].CurrentState != "Down" {
		t.Errorf("second site = %+v, want id=102 state=Down", resp.Data[1])
	}
}

func TestListSitesAppliesPaginationCursor(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// Three rows; limit=2 → should return 2 + a next cursor.
	rows := makeSiteRow(10, "a", 1)
	rows.AddRow(20, 20, "b", 1, 1, nil, nil, nil, nil, "follow", nil, nil, nil)
	rows.AddRow(30, 30, "c", 1, 1, nil, nil, nil, nil, "follow", nil, nil, nil)

	mock.ExpectQuery(sitesListSQL).
		WithArgs(int64(0), 3). // limit+1 = 3
		WillReturnRows(rows)

	req := requestWithKey("GET", "/api/v1/sites?limit=2", key)
	rec := invokeAuthed(s, req, s.handleListSites)

	var resp struct {
		Data []siteResponse `json:"data"`
		Page Page           `json:"page"`
	}
	readJSON(t, rec.Body, &resp)
	if len(resp.Data) != 2 {
		t.Fatalf("data len = %d, want 2", len(resp.Data))
	}
	if resp.Page.Next == nil {
		t.Fatal("expected a next cursor")
	}
	// Decoded cursor should be the id of the last row returned.
	id, err := decodeIDCursor(*resp.Page.Next)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if id != 20 {
		t.Errorf("next cursor id = %d, want 20", id)
	}
}

func TestListSitesFiltersByMonitorActive(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	expected := ` SELECT blog_id, blog_id AS public_id, monitor_url, monitor_active, site_status, last_checked_at, last_status_change, ssl_expiry_date, check_keyword, redirect_policy, maintenance_start, maintenance_end, alert_cooldown_minutes FROM jetpack_monitor_sites WHERE blog_id > ? AND monitor_active = 1 ORDER BY blog_id ASC LIMIT ?`
	mock.ExpectQuery(expected).
		WithArgs(int64(0), 51).
		WillReturnRows(sqlmock.NewRows(columnsSite))

	req := requestWithKey("GET", "/api/v1/sites?monitor_active=true", key)
	rec := invokeAuthed(s, req, s.handleListSites)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestListSitesRejectsBadCursor(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := requestWithKey("GET", "/api/v1/sites?cursor=not-base64!!", key)
	rec := invokeAuthed(s, req, s.handleListSites)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "invalid_cursor" {
		t.Errorf("error code = %q, want invalid_cursor", body.Code)
	}
}

func TestListSitesRejectsBadLimit(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := requestWithKey("GET", "/api/v1/sites?limit=abc", key)
	rec := invokeAuthed(s, req, s.handleListSites)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestGetSiteFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(singleSiteSQL).WithArgs(int64(42)).WillReturnRows(makeSiteRow(42, "https://x", 2))

	// active_events query — return one active event.
	startedAt := time.Date(2026, 4, 25, 3, 18, 38, 329_000_000, time.UTC)
	mock.ExpectQuery(activeEventsSQL).WithArgs(int64(42)).WillReturnRows(
		sqlmock.NewRows(columnsActiveEvent).
			AddRow(int64(7), "http", uint8(4), "Down", startedAt),
	)

	req := requestWithKey("GET", "/api/v1/sites/42", key)
	req.SetPathValue("id", "42")
	rec := invokeAuthed(s, req, s.handleGetSite)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp singleSiteResponse
	readJSON(t, rec.Body, &resp)
	if resp.ID != 42 {
		t.Errorf("id = %d, want 42", resp.ID)
	}
	if len(resp.ActiveEvents) != 1 || resp.ActiveEvents[0].ID != 7 {
		t.Fatalf("active_events = %+v, want one with id=7", resp.ActiveEvents)
	}
	if resp.ActiveEventID == nil || *resp.ActiveEventID != 7 {
		t.Errorf("active_event_id = %v, want pointer to 7", resp.ActiveEventID)
	}
	// Worst event should be reflected on the projection.
	if resp.CurrentState != "Down" || resp.CurrentSeverity != 4 {
		t.Errorf("projection = (%q, %d), want (Down, 4)", resp.CurrentState, resp.CurrentSeverity)
	}
}

func TestGetSiteNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(singleSiteSQL).WithArgs(int64(99999)).WillReturnRows(sqlmock.NewRows(columnsSite))

	req := requestWithKey("GET", "/api/v1/sites/99999", key)
	req.SetPathValue("id", "99999")
	rec := invokeAuthed(s, req, s.handleGetSite)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "site_not_found" {
		t.Errorf("error code = %q, want site_not_found", body.Code)
	}
	// Internal API style: error message names the resource type.
	if !contains(body.Message, "Site 99999") {
		t.Errorf("message %q should name resource type and id", body.Message)
	}
}

func TestGetSiteInvalidID(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := requestWithKey("GET", "/api/v1/sites/abc", key)
	req.SetPathValue("id", "abc")
	rec := invokeAuthed(s, req, s.handleGetSite)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// trimSQL is a shorthand for tests; sqlmock with QueryMatcherEqual matches
// strings byte-for-byte, so we have to assemble queries the same way the
// handler does.
func trimSQL(s string) string { return s }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// httptest dependency reference suppresses unused-import checks if a future
// edit removes the only direct httptest usage above.
var _ = httptest.NewRecorder

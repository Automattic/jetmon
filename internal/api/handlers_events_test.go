package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const eventsBaseSQL = ` SELECT id, blog_id, endpoint_id, check_type, discriminator, severity, state, started_at, ended_at, resolution_reason, cause_event_id, metadata FROM jetmon_events WHERE blog_id = ?`

const transitionsListSQL = ` SELECT id, event_id, severity_before, severity_after, state_before, state_after, reason, source, metadata, changed_at FROM jetmon_event_transitions WHERE event_id = ?`

const transitionsAllSQL = ` SELECT id, event_id, severity_before, severity_after, state_before, state_after, reason, source, metadata, changed_at FROM jetmon_event_transitions WHERE event_id = ? ORDER BY id ASC`

func makeEventRow(id, blogID int64, severity uint8, state string, startedAt time.Time, ended *time.Time) *sqlmock.Rows {
	rows := sqlmock.NewRows(columnsEvent)
	var endedAt any
	if ended != nil {
		endedAt = *ended
	}
	rows.AddRow(
		id, blogID, nil, "http", nil,
		severity, state, startedAt, endedAt, nil,
		nil, []byte(`{"http_code":503}`),
	)
	return rows
}

func TestListSiteEventsHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	startedAt := time.Date(2026, 4, 25, 3, 0, 0, 0, time.UTC)
	rows := makeEventRow(7, 42, 4, "Down", startedAt, nil)

	mock.ExpectQuery(eventsBaseSQL+` ORDER BY id DESC LIMIT ?`).
		WithArgs(int64(42), 51).
		WillReturnRows(rows)

	// transition_count batch query
	mock.ExpectQuery(`SELECT event_id, COUNT(*) FROM jetmon_event_transitions WHERE event_id IN (?) GROUP BY event_id`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"event_id", "count"}).AddRow(int64(7), 3))

	req := requestWithKey("GET", "/api/v1/sites/42/events", key)
	req.SetPathValue("id", "42")
	rec := invokeAuthed(s, req, s.handleListSiteEvents)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []eventResponse `json:"data"`
		Page Page            `json:"page"`
	}
	readJSON(t, rec.Body, &resp)
	if len(resp.Data) != 1 || resp.Data[0].ID != 7 {
		t.Fatalf("data = %+v, want one event with id=7", resp.Data)
	}
	if resp.Data[0].TransitionCount != 3 {
		t.Errorf("transition_count = %d, want 3", resp.Data[0].TransitionCount)
	}
	// Open events report duration_ms based on now-started_at; just check it's positive.
	if resp.Data[0].DurationMs <= 0 {
		t.Errorf("duration_ms = %d, want > 0 for open event", resp.Data[0].DurationMs)
	}
}

func TestListSiteEventsAppliesActiveFilter(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(eventsBaseSQL+` AND ended_at IS NULL ORDER BY id DESC LIMIT ?`).
		WithArgs(int64(42), 51).
		WillReturnRows(sqlmock.NewRows(columnsEvent))

	req := requestWithKey("GET", "/api/v1/sites/42/events?active=true", key)
	req.SetPathValue("id", "42")
	rec := invokeAuthed(s, req, s.handleListSiteEvents)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestListSiteEventsWithGatewayTenantRejectsUnmappedSite(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteTenantCheckSQL).
		WithArgs("tenant-a", int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))

	req := httptest.NewRequest("GET", "/api/v1/sites/42/events", nil)
	req.SetPathValue("id", "42")
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleListSiteEvents)

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

func TestListSiteEventsWithGatewayTenantAllowsMappedSite(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteTenantCheckSQL).
		WithArgs("tenant-a", int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(eventsBaseSQL+` ORDER BY id DESC LIMIT ?`).
		WithArgs(int64(42), 51).
		WillReturnRows(sqlmock.NewRows(columnsEvent))

	req := httptest.NewRequest("GET", "/api/v1/sites/42/events", nil)
	req.SetPathValue("id", "42")
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleListSiteEvents)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestListSiteEventsRejectsBadActive(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := requestWithKey("GET", "/api/v1/sites/42/events?active=maybe", key)
	req.SetPathValue("id", "42")
	rec := invokeAuthed(s, req, s.handleListSiteEvents)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "invalid_active" {
		t.Errorf("error code = %q, want invalid_active", body.Code)
	}
}

func TestGetEventBySiteHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	startedAt := time.Date(2026, 4, 25, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(` SELECT id, blog_id, endpoint_id, check_type, discriminator, severity, state, started_at, ended_at, resolution_reason, cause_event_id, metadata FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(7)).
		WillReturnRows(makeEventRow(7, 42, 4, "Down", startedAt, nil))

	// Transitions inline (no LIMIT, ORDER BY id ASC)
	mock.ExpectQuery(transitionsAllSQL).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows(columnsTransition).
			AddRow(int64(1), int64(7), nil, uint8(3), nil, "Seems Down", "opened", "host", []byte("null"), startedAt))

	req := requestWithKey("GET", "/api/v1/sites/42/events/7", key)
	req.SetPathValue("id", "42")
	req.SetPathValue("event_id", "7")
	rec := invokeAuthed(s, req, s.handleGetEventBySite)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp eventDetailResponse
	readJSON(t, rec.Body, &resp)
	if resp.ID != 7 || resp.SiteID != 42 {
		t.Errorf("event = (id=%d site=%d), want (7, 42)", resp.ID, resp.SiteID)
	}
	if len(resp.Transitions) != 1 {
		t.Errorf("transitions len = %d, want 1", len(resp.Transitions))
	}
	if resp.TransitionCount != 1 {
		t.Errorf("transition_count = %d, want 1", resp.TransitionCount)
	}
}

func TestGetEventWithGatewayTenantRejectsUnmappedEventSite(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	startedAt := time.Date(2026, 4, 25, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(` SELECT id, blog_id, endpoint_id, check_type, discriminator, severity, state, started_at, ended_at, resolution_reason, cause_event_id, metadata FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(7)).
		WillReturnRows(makeEventRow(7, 42, 4, "Down", startedAt, nil))
	mock.ExpectQuery(siteTenantCheckSQL).
		WithArgs("tenant-a", int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))

	req := httptest.NewRequest("GET", "/api/v1/events/7", nil)
	req.SetPathValue("event_id", "7")
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleGetEvent)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if got := readErrorBody(t, rec.Body).Code; got != "event_not_found" {
		t.Fatalf("code = %q, want event_not_found", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetEventBySiteCrossSite404(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// Event 7 belongs to site 42, but consumer is asking under site 99.
	startedAt := time.Date(2026, 4, 25, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(` SELECT id, blog_id, endpoint_id, check_type, discriminator, severity, state, started_at, ended_at, resolution_reason, cause_event_id, metadata FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(7)).
		WillReturnRows(makeEventRow(7, 42, 4, "Down", startedAt, nil))

	req := requestWithKey("GET", "/api/v1/sites/99/events/7", key)
	req.SetPathValue("id", "99")
	req.SetPathValue("event_id", "7")
	rec := invokeAuthed(s, req, s.handleGetEventBySite)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "event_not_found" {
		t.Errorf("error code = %q, want event_not_found", body.Code)
	}
	if !contains(body.Message, "Event 7 does not belong to site 99") {
		t.Errorf("message %q should explain cross-site mismatch", body.Message)
	}
}

func TestGetEventNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(` SELECT id, blog_id, endpoint_id, check_type, discriminator, severity, state, started_at, ended_at, resolution_reason, cause_event_id, metadata FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(999)).
		WillReturnRows(sqlmock.NewRows(columnsEvent))

	req := requestWithKey("GET", "/api/v1/events/999", key)
	req.SetPathValue("event_id", "999")
	rec := invokeAuthed(s, req, s.handleGetEvent)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "event_not_found" {
		t.Errorf("error code = %q, want event_not_found", body.Code)
	}
}

func TestListTransitionsCrossSiteProtection(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(`SELECT blog_id FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id"}).AddRow(int64(42)))

	req := requestWithKey("GET", "/api/v1/sites/99/events/7/transitions", key)
	req.SetPathValue("id", "99")
	req.SetPathValue("event_id", "7")
	rec := invokeAuthed(s, req, s.handleListTransitions)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "event_not_found" {
		t.Errorf("error code = %q, want event_not_found", body.Code)
	}
}

func TestListTransitionsHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(`SELECT blog_id FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id"}).AddRow(int64(42)))

	startedAt := time.Date(2026, 4, 25, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(transitionsListSQL+` ORDER BY id ASC LIMIT ?`).
		WithArgs(int64(7), 101).
		WillReturnRows(sqlmock.NewRows(columnsTransition).
			AddRow(int64(1), int64(7), nil, uint8(3), nil, "Seems Down", "opened", "host", []byte("null"), startedAt))

	req := requestWithKey("GET", "/api/v1/sites/42/events/7/transitions", key)
	req.SetPathValue("id", "42")
	req.SetPathValue("event_id", "7")
	rec := invokeAuthed(s, req, s.handleListTransitions)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []transitionResponse `json:"data"`
		Page Page                 `json:"page"`
	}
	readJSON(t, rec.Body, &resp)
	if len(resp.Data) != 1 || resp.Data[0].Reason != "opened" {
		t.Errorf("transitions = %+v, want one with reason=opened", resp.Data)
	}
}

func TestListTransitionsWithGatewayTenantRejectsUnmappedEventSite(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(`SELECT blog_id FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id"}).AddRow(int64(42)))
	mock.ExpectQuery(siteTenantCheckSQL).
		WithArgs("tenant-a", int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))

	req := httptest.NewRequest("GET", "/api/v1/sites/42/events/7/transitions", nil)
	req.SetPathValue("id", "42")
	req.SetPathValue("event_id", "7")
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleListTransitions)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if got := readErrorBody(t, rec.Body).Code; got != "event_not_found" {
		t.Fatalf("code = %q, want event_not_found", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestListTransitionsWithGatewayTenantAllowsMappedEventSite(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(`SELECT blog_id FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id"}).AddRow(int64(42)))
	mock.ExpectQuery(siteTenantCheckSQL).
		WithArgs("tenant-a", int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	startedAt := time.Date(2026, 4, 25, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(transitionsListSQL+` ORDER BY id ASC LIMIT ?`).
		WithArgs(int64(7), 101).
		WillReturnRows(sqlmock.NewRows(columnsTransition).
			AddRow(int64(1), int64(7), nil, uint8(3), nil, "Seems Down", "opened", "host", []byte("null"), startedAt))

	req := httptest.NewRequest("GET", "/api/v1/sites/42/events/7/transitions", nil)
	req.SetPathValue("id", "42")
	req.SetPathValue("event_id", "7")
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleListTransitions)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestParseCSVCombinations(t *testing.T) {
	q := map[string][]string{
		"state__in": {"Down,Seems Down"},
	}
	got := parseCSV(q, "state", "state__in")
	if len(got) != 2 || got[0] != "Down" || got[1] != "Seems Down" {
		t.Errorf("parseCSV = %+v, want [Down, 'Seems Down']", got)
	}

	q2 := map[string][]string{"check_type": {"http"}}
	got2 := parseCSV(q2, "check_type", "check_type__in")
	if len(got2) != 1 || got2[0] != "http" {
		t.Errorf("parseCSV single = %+v, want [http]", got2)
	}
}

func TestParseTimeQuery(t *testing.T) {
	if got, err := parseTimeQuery(""); err != nil || got != nil {
		t.Errorf("empty input = (%v, %v), want (nil, nil)", got, err)
	}
	if _, err := parseTimeQuery("not-a-date"); err == nil {
		t.Error("malformed date should error")
	}
	t1, err := parseTimeQuery("2026-04-25T00:00:00Z")
	if err != nil || t1 == nil {
		t.Fatalf("valid date errored: %v", err)
	}
	if t1.Year() != 2026 {
		t.Errorf("parsed year = %d, want 2026", t1.Year())
	}
}

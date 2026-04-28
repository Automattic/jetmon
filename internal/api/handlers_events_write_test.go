package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const eventLookupMinSQL = `SELECT blog_id, ended_at FROM jetmon_events WHERE id = ?`

const closeEventTxSelectSQL = `SELECT severity, state, ended_at FROM jetmon_events WHERE id = ? FOR UPDATE`

const closeEventUpdateSQL = ` UPDATE jetmon_events SET ended_at = CURRENT_TIMESTAMP(3), resolution_reason = ? WHERE id = ?`

const closeEventInsertTransitionSQL = ` INSERT INTO jetmon_event_transitions (event_id, blog_id, severity_before, severity_after, state_before, state_after, reason, source, metadata) VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?)`

const countActiveEventsSQL = `SELECT COUNT(*) FROM jetmon_events WHERE blog_id = ? AND ended_at IS NULL`

const projectRunningSQL = `UPDATE jetpack_monitor_sites SET site_status = 1, last_status_change = ? WHERE blog_id = ?`

func expectCloseEventTx(mock sqlmock.Sqlmock, eventID, blogID int64, severity uint8, state, reason string) {
	mock.ExpectBegin()
	mock.ExpectQuery(closeEventTxSelectSQL).
		WithArgs(eventID).
		WillReturnRows(sqlmock.NewRows([]string{"severity", "state", "ended_at"}).
			AddRow(severity, state, nil))
	mock.ExpectExec(closeEventUpdateSQL).
		WithArgs(reason, eventID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(closeEventInsertTransitionSQL).
		WithArgs(eventID, blogID, severity, state, "Resolved", reason, "api", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(countActiveEventsSQL).WithArgs(blogID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(projectRunningSQL).
		WithArgs(sqlmock.AnyArg(), blogID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

func TestCloseEventHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// Event exists and belongs to site 42, currently open.
	mock.ExpectQuery(eventLookupMinSQL).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id", "ended_at"}).
			AddRow(int64(42), nil))

	expectCloseEventTx(mock, 7, 42, 4, "Down", "manual_override")

	// Read-back: full event + transitions.
	startedAt := time.Date(2026, 4, 25, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(` SELECT id, blog_id, endpoint_id, check_type, discriminator, severity, state, started_at, ended_at, resolution_reason, cause_event_id, metadata FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(7)).
		WillReturnRows(makeEventRow(7, 42, 4, "Down", startedAt, &startedAt))
	mock.ExpectQuery(transitionsAllSQL).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows(columnsTransition))

	body := []byte(`{"reason":"manual_override","note":"close from API"}`)
	req := newPOSTWithBody("/api/v1/sites/42/events/7/close", body)
	req.SetPathValue("id", "42")
	req.SetPathValue("event_id", "7")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCloseEvent)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCloseEventNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(eventLookupMinSQL).WithArgs(int64(999)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id", "ended_at"}))

	body := []byte(`{}`)
	req := newPOSTWithBody("/api/v1/sites/42/events/999/close", body)
	req.SetPathValue("id", "42")
	req.SetPathValue("event_id", "999")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCloseEvent)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestCloseEventCrossSiteRejected(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// Event 7 belongs to site 42, request says site 99.
	mock.ExpectQuery(eventLookupMinSQL).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id", "ended_at"}).
			AddRow(int64(42), nil))

	body := []byte(`{}`)
	req := newPOSTWithBody("/api/v1/sites/99/events/7/close", body)
	req.SetPathValue("id", "99")
	req.SetPathValue("event_id", "7")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCloseEvent)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := readErrorBody(t, rec.Body).Code; got != "event_not_found" {
		t.Errorf("code = %q, want event_not_found", got)
	}
}

func TestCloseEventAlreadyClosedIsIdempotent(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// Event already has ended_at set.
	closedAt := time.Date(2026, 4, 25, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(eventLookupMinSQL).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id", "ended_at"}).
			AddRow(int64(42), closedAt))

	// Read-back happens directly without re-closing.
	startedAt := closedAt.Add(-1 * time.Hour)
	mock.ExpectQuery(` SELECT id, blog_id, endpoint_id, check_type, discriminator, severity, state, started_at, ended_at, resolution_reason, cause_event_id, metadata FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(7)).
		WillReturnRows(makeEventRow(7, 42, 4, "Down", startedAt, &closedAt))
	mock.ExpectQuery(transitionsAllSQL).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows(columnsTransition))

	body := []byte(`{"reason":"manual_override"}`)
	req := newPOSTWithBody("/api/v1/sites/42/events/7/close", body)
	req.SetPathValue("id", "42")
	req.SetPathValue("event_id", "7")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCloseEvent)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (idempotent close); body=%s", rec.Code, rec.Body.String())
	}
}

func TestCloseEventDefaultReason(t *testing.T) {
	// An empty body produces reason=manual_override per the handler defaults.
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(eventLookupMinSQL).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id", "ended_at"}).
			AddRow(int64(42), nil))
	expectCloseEventTx(mock, 7, 42, 4, "Down", "manual_override")
	startedAt := time.Date(2026, 4, 25, 3, 0, 0, 0, time.UTC)
	mock.ExpectQuery(` SELECT id, blog_id, endpoint_id, check_type, discriminator, severity, state, started_at, ended_at, resolution_reason, cause_event_id, metadata FROM jetmon_events WHERE id = ?`).
		WithArgs(int64(7)).
		WillReturnRows(makeEventRow(7, 42, 4, "Down", startedAt, &startedAt))
	mock.ExpectQuery(transitionsAllSQL).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows(columnsTransition))

	// Empty body — handler should default reason to manual_override.
	req := httptest.NewRequest("POST", "/api/v1/sites/42/events/7/close", nil)
	req.SetPathValue("id", "42")
	req.SetPathValue("event_id", "7")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCloseEvent)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCloseEventInvalidIDs(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	cases := []struct {
		siteID, eventID, code string
	}{
		{"abc", "7", "invalid_site_id"},
		{"42", "xyz", "invalid_event_id"},
		{"-1", "7", "invalid_site_id"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("POST", "/api/v1/sites/"+c.siteID+"/events/"+c.eventID+"/close", nil)
		req.SetPathValue("id", c.siteID)
		req.SetPathValue("event_id", c.eventID)
		req = setAuthCtx(req, key)
		rec := invokeAuthed(s, req, s.handleCloseEvent)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("siteID=%s eventID=%s status=%d want 400", c.siteID, c.eventID, rec.Code)
			continue
		}
		if got := readErrorBody(t, rec.Body).Code; got != c.code {
			t.Errorf("siteID=%s eventID=%s code=%q want %q", c.siteID, c.eventID, got, c.code)
		}
	}
}

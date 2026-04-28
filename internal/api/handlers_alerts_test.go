package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Automattic/jetmon/internal/alerting"
)

const insertAlertContactSQL = ` INSERT INTO jetmon_alert_contacts (label, active, transport, destination, destination_preview, site_filter, min_severity, max_per_hour, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

const selectAlertContactOneSQL = ` SELECT id, label, active, transport, destination_preview, site_filter, min_severity, max_per_hour, created_by, created_at, updated_at FROM jetmon_alert_contacts WHERE id = ?`

const loadAlertDestinationSQL = `SELECT destination FROM jetmon_alert_contacts WHERE id = ?`

var columnsAlertContact = []string{
	"id", "label", "active", "transport", "destination_preview",
	"site_filter", "min_severity", "max_per_hour",
	"created_by", "created_at", "updated_at",
}

func makeAlertContactRow(id int64, label string, transport string, active uint8, minSev uint8) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows(columnsAlertContact).AddRow(
		id, label, active, transport, "abcd",
		[]byte(`{"site_ids":[]}`), minSev, 60,
		"test-consumer", now, now,
	)
}

// recordingDispatcher is a Dispatcher used by send-test tests. It
// records every Send call and returns a configurable status/body/err.
type recordingDispatcher struct {
	calls    int
	gotDest  json.RawMessage
	gotN     alerting.Notification
	respCode int
	respBody string
	respErr  error
}

func (d *recordingDispatcher) Send(_ context.Context, dest json.RawMessage, n alerting.Notification) (int, string, error) {
	d.calls++
	d.gotDest = dest
	d.gotN = n
	return d.respCode, d.respBody, d.respErr
}

// ─── Create ───────────────────────────────────────────────────────────

func TestCreateAlertContactHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectExec(insertAlertContactSQL).
		WithArgs(
			"oncall", 1, "pagerduty",
			sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), uint8(4), 60,
			"test-consumer",
		).
		WillReturnResult(sqlmock.NewResult(11, 1))
	mock.ExpectQuery(selectAlertContactOneSQL).WithArgs(int64(11)).
		WillReturnRows(makeAlertContactRow(11, "oncall", "pagerduty", 1, 4))

	body := []byte(`{
		"label":"oncall",
		"transport":"pagerduty",
		"destination":{"integration_key":"PDKEY-12345"}
	}`)
	req := newPOSTWithBody("/api/v1/alert-contacts", body)
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCreateAlertContact)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp alertContactResponse
	readJSON(t, rec.Body, &resp)
	if resp.ID != 11 {
		t.Errorf("ID = %d, want 11", resp.ID)
	}
	if resp.Transport != "pagerduty" {
		t.Errorf("Transport = %q, want pagerduty", resp.Transport)
	}
	if resp.MinSeverity != "Down" {
		t.Errorf("MinSeverity = %q, want Down (default)", resp.MinSeverity)
	}
}

func TestCreateAlertContactRejectsBadTransport(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte(`{"label":"x","transport":"sms","destination":{}}`)
	req := newPOSTWithBody("/api/v1/alert-contacts", body)
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCreateAlertContact)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := readErrorBody(t, rec.Body).Code; got != "invalid_transport" {
		t.Errorf("code = %q, want invalid_transport", got)
	}
}

func TestCreateAlertContactRejectsMissingDestinationFields(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	cases := []struct {
		body string
		code string
	}{
		{`{"label":"x","transport":"email","destination":{}}`, "invalid_alert_contact"},
		{`{"label":"x","transport":"slack","destination":{"webhook_url":""}}`, "invalid_alert_contact"},
		{`{"label":"","transport":"slack","destination":{"webhook_url":"https://x"}}`, "invalid_alert_contact"},
	}
	for _, c := range cases {
		req := newPOSTWithBody("/api/v1/alert-contacts", []byte(c.body))
		req = setAuthCtx(req, key)
		rec := invokeAuthed(s, req, s.handleCreateAlertContact)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("body=%s status=%d, want 422", c.body, rec.Code)
			continue
		}
		if got := readErrorBody(t, rec.Body).Code; got != c.code {
			t.Errorf("body=%s code = %q, want %q", c.body, got, c.code)
		}
	}
}

func TestCreateAlertContactRejectsBadSeverity(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte(`{"label":"x","transport":"email","destination":{"address":"a@b"},"min_severity":"Critical"}`)
	req := newPOSTWithBody("/api/v1/alert-contacts", body)
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCreateAlertContact)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := readErrorBody(t, rec.Body).Code; got != "invalid_severity" {
		t.Errorf("code = %q, want invalid_severity", got)
	}
}

// ─── Get ──────────────────────────────────────────────────────────────

func TestGetAlertContactHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(selectAlertContactOneSQL).WithArgs(int64(11)).
		WillReturnRows(makeAlertContactRow(11, "oncall", "pagerduty", 1, 4))

	req := httptest.NewRequest("GET", "/api/v1/alert-contacts/11", nil)
	req.SetPathValue("id", "11")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleGetAlertContact)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestGetAlertContactNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(selectAlertContactOneSQL).WithArgs(int64(999)).
		WillReturnRows(sqlmock.NewRows(columnsAlertContact))

	req := httptest.NewRequest("GET", "/api/v1/alert-contacts/999", nil)
	req.SetPathValue("id", "999")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleGetAlertContact)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// ─── Update ───────────────────────────────────────────────────────────

func TestUpdateAlertContactHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// Update first reads the current row to know the transport.
	mock.ExpectQuery(selectAlertContactOneSQL).WithArgs(int64(11)).
		WillReturnRows(makeAlertContactRow(11, "oncall", "pagerduty", 1, 4))
	mock.ExpectExec(`UPDATE jetmon_alert_contacts SET active = ? WHERE id = ?`).
		WithArgs(0, int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(selectAlertContactOneSQL).WithArgs(int64(11)).
		WillReturnRows(makeAlertContactRow(11, "oncall", "pagerduty", 0, 4))

	body := []byte(`{"active": false}`)
	req := newPATCHWithBody("/api/v1/alert-contacts/11", body)
	req.SetPathValue("id", "11")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleUpdateAlertContact)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// TestUpdateAlertContactRejectsEmptyLabel verifies an empty label
// PATCH gets rejected at the package's input-validation layer
// without hitting the DB. Mirrors Create's "label is required" rule.
func TestUpdateAlertContactRejectsEmptyLabel(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()
	// No DB expectations — validation should fail before any query.

	body := []byte(`{"label": ""}`)
	req := newPATCHWithBody("/api/v1/alert-contacts/11", body)
	req.SetPathValue("id", "11")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleUpdateAlertContact)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if got := readErrorBody(t, rec.Body).Code; got != "invalid_alert_contact" {
		t.Errorf("code = %q, want invalid_alert_contact", got)
	}
}

// TestUpdateAlertContactRejectsNegativeMaxPerHour verifies that PATCH
// catches max_per_hour < 0 at input-validation time rather than letting
// MySQL reject the negative value as a generic 500.
func TestUpdateAlertContactRejectsNegativeMaxPerHour(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte(`{"max_per_hour": -10}`)
	req := newPATCHWithBody("/api/v1/alert-contacts/11", body)
	req.SetPathValue("id", "11")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleUpdateAlertContact)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if got := readErrorBody(t, rec.Body).Code; got != "invalid_alert_contact" {
		t.Errorf("code = %q, want invalid_alert_contact", got)
	}
}

func TestUpdateAlertContactNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(selectAlertContactOneSQL).WithArgs(int64(999)).
		WillReturnRows(sqlmock.NewRows(columnsAlertContact))

	body := []byte(`{"active": false}`)
	req := newPATCHWithBody("/api/v1/alert-contacts/999", body)
	req.SetPathValue("id", "999")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleUpdateAlertContact)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// ─── Delete ───────────────────────────────────────────────────────────

func TestDeleteAlertContactHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectExec(`DELETE FROM jetmon_alert_contacts WHERE id = ?`).
		WithArgs(int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	req := httptest.NewRequest("DELETE", "/api/v1/alert-contacts/11", nil)
	req.SetPathValue("id", "11")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleDeleteAlertContact)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestDeleteAlertContactNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectExec(`DELETE FROM jetmon_alert_contacts WHERE id = ?`).
		WithArgs(int64(999)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	req := httptest.NewRequest("DELETE", "/api/v1/alert-contacts/999", nil)
	req.SetPathValue("id", "999")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleDeleteAlertContact)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// ─── Send-test ────────────────────────────────────────────────────────

func TestAlertContactTestHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	disp := &recordingDispatcher{respCode: 200, respBody: "ok"}
	s.SetAlertDispatchers(map[alerting.Transport]alerting.Dispatcher{
		alerting.TransportSlack: disp,
	})

	// The test endpoint loads the contact, then loads its destination.
	mock.ExpectQuery(selectAlertContactOneSQL).WithArgs(int64(11)).
		WillReturnRows(makeAlertContactRow(11, "oncall-slack", "slack", 1, 4))
	mock.ExpectQuery(loadAlertDestinationSQL).WithArgs(int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"destination"}).
			AddRow([]byte(`{"webhook_url":"https://hooks.slack.com/x"}`)))

	req := newPOSTWithBody("/api/v1/alert-contacts/11/test", nil)
	req.SetPathValue("id", "11")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleAlertContactTest)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if disp.calls != 1 {
		t.Errorf("dispatcher called %d times, want 1", disp.calls)
	}
	if !disp.gotN.IsTest {
		t.Error("dispatched notification should have IsTest=true")
	}
}

func TestAlertContactTestSurfacesTransportError(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	disp := &recordingDispatcher{respCode: 500, respBody: "internal", respErr: errBoom("upstream")}
	s.SetAlertDispatchers(map[alerting.Transport]alerting.Dispatcher{
		alerting.TransportSlack: disp,
	})

	mock.ExpectQuery(selectAlertContactOneSQL).WithArgs(int64(11)).
		WillReturnRows(makeAlertContactRow(11, "oncall-slack", "slack", 1, 4))
	mock.ExpectQuery(loadAlertDestinationSQL).WithArgs(int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"destination"}).
			AddRow([]byte(`{"webhook_url":"https://hooks.slack.com/x"}`)))

	req := newPOSTWithBody("/api/v1/alert-contacts/11/test", nil)
	req.SetPathValue("id", "11")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleAlertContactTest)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAlertContactTestNoDispatcherConfigured(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// alertDispatchers is nil → 503 "transport_not_configured"
	mock.ExpectQuery(selectAlertContactOneSQL).WithArgs(int64(11)).
		WillReturnRows(makeAlertContactRow(11, "oncall-email", "email", 1, 4))

	req := newPOSTWithBody("/api/v1/alert-contacts/11/test", nil)
	req.SetPathValue("id", "11")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleAlertContactTest)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := readErrorBody(t, rec.Body).Code; got != "transport_not_configured" {
		t.Errorf("code = %q, want transport_not_configured", got)
	}
}

// errBoom is a tiny error helper for tests.
type errBoom string

func (e errBoom) Error() string { return string(e) }

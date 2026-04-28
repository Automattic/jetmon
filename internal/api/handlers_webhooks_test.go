package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const insertWebhookSQL = ` INSERT INTO jetmon_webhooks (url, active, events, site_filter, state_filter, secret, secret_preview, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

const selectWebhookOneSQL = ` SELECT id, url, active, events, site_filter, state_filter, secret_preview, created_by, created_at, updated_at FROM jetmon_webhooks WHERE id = ?`

// columnsWebhook is the column set returned by webhook SELECT queries.
var columnsWebhook = []string{
	"id", "url", "active", "events", "site_filter", "state_filter",
	"secret_preview", "created_by", "created_at", "updated_at",
}

func makeWebhookRow(id int64, url string, active uint8) *sqlmock.Rows {
	now := time.Now().UTC()
	return sqlmock.NewRows(columnsWebhook).AddRow(
		id, url, active, []byte(`["event.opened"]`),
		[]byte(`{"site_ids":[]}`), []byte(`{"states":[]}`),
		"abcd", "test-consumer", now, now,
	)
}

func TestCreateWebhookHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectExec(insertWebhookSQL).
		WithArgs(
			"https://example.com/hook", 1,
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), "test-consumer",
		).
		WillReturnResult(sqlmock.NewResult(7, 1))
	mock.ExpectQuery(selectWebhookOneSQL).WithArgs(int64(7)).
		WillReturnRows(makeWebhookRow(7, "https://example.com/hook", 1))

	body := []byte(`{"url":"https://example.com/hook","events":["event.opened"]}`)
	req := newPOSTWithBody("/api/v1/webhooks", body)
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCreateWebhook)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp createWebhookResponse
	readJSON(t, rec.Body, &resp)
	if resp.ID != 7 {
		t.Errorf("id = %d, want 7", resp.ID)
	}
	if resp.Secret == "" {
		t.Error("expected raw secret in response")
	}
}

func TestCreateWebhookRejectsBadURL(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	cases := [][]byte{
		[]byte(`{"url":""}`),
		[]byte(`{"url":"not-a-url"}`),
		[]byte(`{"url":"ftp://example.com"}`),
	}
	for _, body := range cases {
		req := newPOSTWithBody("/api/v1/webhooks", body)
		req = setAuthCtx(req, key)
		rec := invokeAuthed(s, req, s.handleCreateWebhook)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body=%s status=%d, want 400", body, rec.Code)
		}
	}
}

func TestCreateWebhookRejectsBadEventType(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte(`{"url":"https://x.example.com","events":["event.bogus"]}`)
	req := newPOSTWithBody("/api/v1/webhooks", body)
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleCreateWebhook)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if got := readErrorBody(t, rec.Body).Code; got != "invalid_event_type" {
		t.Errorf("code = %q, want invalid_event_type", got)
	}
}

func TestGetWebhookHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(selectWebhookOneSQL).WithArgs(int64(42)).
		WillReturnRows(makeWebhookRow(42, "https://x.example.com", 1))

	req := httptest.NewRequest("GET", "/api/v1/webhooks/42", nil)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleGetWebhook)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetWebhookNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(selectWebhookOneSQL).WithArgs(int64(999)).
		WillReturnRows(sqlmock.NewRows(columnsWebhook))

	req := httptest.NewRequest("GET", "/api/v1/webhooks/999", nil)
	req.SetPathValue("id", "999")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleGetWebhook)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestUpdateWebhookHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectExec(`UPDATE jetmon_webhooks SET active = ? WHERE id = ?`).
		WithArgs(0, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(selectWebhookOneSQL).WithArgs(int64(42)).
		WillReturnRows(makeWebhookRow(42, "https://x.example.com", 0))

	body := []byte(`{"active": false}`)
	req := newPATCHWithBody("/api/v1/webhooks/42", body)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleUpdateWebhook)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestDeleteWebhookHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectExec(`DELETE FROM jetmon_webhooks WHERE id = ?`).
		WithArgs(int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	req := httptest.NewRequest("DELETE", "/api/v1/webhooks/42", nil)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleDeleteWebhook)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestDeleteWebhookNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectExec(`DELETE FROM jetmon_webhooks WHERE id = ?`).
		WithArgs(int64(999)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	req := httptest.NewRequest("DELETE", "/api/v1/webhooks/999", nil)
	req.SetPathValue("id", "999")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleDeleteWebhook)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRotateWebhookSecretHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectExec(`UPDATE jetmon_webhooks SET secret = ?, secret_preview = ? WHERE id = ?`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(selectWebhookOneSQL).WithArgs(int64(42)).
		WillReturnRows(makeWebhookRow(42, "https://x.example.com", 1))

	req := newPOSTWithBody("/api/v1/webhooks/42/rotate-secret", nil)
	req.SetPathValue("id", "42")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleRotateWebhookSecret)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp createWebhookResponse
	readJSON(t, rec.Body, &resp)
	if resp.Secret == "" {
		t.Error("expected new raw secret in rotate response")
	}
}

func TestRotateWebhookSecretNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectExec(`UPDATE jetmon_webhooks SET secret = ?, secret_preview = ? WHERE id = ?`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), int64(999)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	req := newPOSTWithBody("/api/v1/webhooks/999/rotate-secret", nil)
	req.SetPathValue("id", "999")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleRotateWebhookSecret)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

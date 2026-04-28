package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const selectAlertDeliveriesSQL = ` SELECT id, alert_contact_id, transition_id, event_id, event_type, severity, payload, status, attempt, next_attempt_at, last_status_code, last_response, last_attempt_at, delivered_at, created_at FROM jetmon_alert_deliveries WHERE alert_contact_id = ? ORDER BY id DESC LIMIT ?`

const selectAlertDeliveryOneSQL = ` SELECT id, alert_contact_id, transition_id, event_id, event_type, severity, payload, status, attempt, next_attempt_at, last_status_code, last_response, last_attempt_at, delivered_at, created_at FROM jetmon_alert_deliveries WHERE id = ?`

var columnsAlertDelivery = []string{
	"id", "alert_contact_id", "transition_id", "event_id", "event_type", "severity",
	"payload", "status", "attempt", "next_attempt_at", "last_status_code", "last_response",
	"last_attempt_at", "delivered_at", "created_at",
}

func makeAlertDeliveryRow(id, contactID int64, status string) *sqlmock.Rows {
	now := time.Now().UTC()
	payload := []byte(`{"site_id":42,"event_id":777,"type":"alert.opened"}`)
	return sqlmock.NewRows(columnsAlertDelivery).AddRow(
		id, contactID, int64(1), int64(777), "alert.opened", uint8(4),
		payload, status, 1, nil, nil, nil, nil, nil, now,
	)
}

func TestListAlertDeliveriesHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// limit + 1 = 51 (default 50 + 1 for pagination probe).
	mock.ExpectQuery(selectAlertDeliveriesSQL).
		WithArgs(int64(11), 51).
		WillReturnRows(makeAlertDeliveryRow(101, 11, "delivered"))

	req := httptest.NewRequest("GET", "/api/v1/alert-contacts/11/deliveries", nil)
	req.SetPathValue("id", "11")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleListAlertDeliveries)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data []alertDeliveryResponse `json:"data"`
		Page json.RawMessage         `json:"page"`
	}
	readJSON(t, rec.Body, &env)
	if len(env.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(env.Data))
	}
	if env.Data[0].Severity != "Down" {
		t.Errorf("Severity = %q, want Down (uint8 4)", env.Data[0].Severity)
	}
}

func TestListAlertDeliveriesWithGatewayTenantVerifiesContactOwnership(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(selectAlertContactOneForTenantSQL).WithArgs(int64(11), "tenant-a").
		WillReturnRows(makeAlertContactRow(11, "oncall", "slack", 1, 4))
	mock.ExpectQuery(selectAlertDeliveriesSQL).
		WithArgs(int64(11), 51).
		WillReturnRows(makeAlertDeliveryRow(101, 11, "delivered"))

	req := httptest.NewRequest("GET", "/api/v1/alert-contacts/11/deliveries", nil)
	req.SetPathValue("id", "11")
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleListAlertDeliveries)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestListAlertDeliveriesRejectsBadStatus(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/alert-contacts/11/deliveries?status=bogus", nil)
	req.SetPathValue("id", "11")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleListAlertDeliveries)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := readErrorBody(t, rec.Body).Code; got != "invalid_status" {
		t.Errorf("code = %q, want invalid_status", got)
	}
}

func TestRetryAlertDeliveryHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// 1) Get delivery to verify it belongs to the contact.
	mock.ExpectQuery(selectAlertDeliveryOneSQL).WithArgs(int64(101)).
		WillReturnRows(makeAlertDeliveryRow(101, 11, "abandoned"))
	// 2) RetryDelivery UPDATE.
	mock.ExpectExec(`UPDATE jetmon_alert_deliveries SET status = 'pending', attempt = 0, next_attempt_at = CURRENT_TIMESTAMP, last_status_code = NULL, last_response = NULL, last_attempt_at = NULL WHERE id = ? AND status = 'abandoned'`).
		WithArgs(int64(101)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 3) Read-back GetDelivery.
	mock.ExpectQuery(selectAlertDeliveryOneSQL).WithArgs(int64(101)).
		WillReturnRows(makeAlertDeliveryRow(101, 11, "pending"))

	req := newPOSTWithBody("/api/v1/alert-contacts/11/deliveries/101/retry", nil)
	req.SetPathValue("id", "11")
	req.SetPathValue("delivery_id", "101")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleRetryAlertDelivery)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp alertDeliveryResponse
	readJSON(t, rec.Body, &resp)
	if resp.Status != "pending" {
		t.Errorf("Status = %q, want pending", resp.Status)
	}
}

func TestRetryAlertDeliveryWithGatewayTenantVerifiesContactOwnership(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(selectAlertContactOneForTenantSQL).WithArgs(int64(11), "tenant-a").
		WillReturnRows(makeAlertContactRow(11, "oncall", "slack", 1, 4))
	mock.ExpectQuery(selectAlertDeliveryOneSQL).WithArgs(int64(101)).
		WillReturnRows(makeAlertDeliveryRow(101, 11, "abandoned"))
	mock.ExpectExec(`UPDATE jetmon_alert_deliveries SET status = 'pending', attempt = 0, next_attempt_at = CURRENT_TIMESTAMP, last_status_code = NULL, last_response = NULL, last_attempt_at = NULL WHERE id = ? AND status = 'abandoned'`).
		WithArgs(int64(101)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(selectAlertDeliveryOneSQL).WithArgs(int64(101)).
		WillReturnRows(makeAlertDeliveryRow(101, 11, "pending"))

	req := newPOSTWithBody("/api/v1/alert-contacts/11/deliveries/101/retry", nil)
	req.SetPathValue("id", "11")
	req.SetPathValue("delivery_id", "101")
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleRetryAlertDelivery)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRetryAlertDeliveryWrongContact(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// Delivery belongs to contact 99, not 11.
	mock.ExpectQuery(selectAlertDeliveryOneSQL).WithArgs(int64(101)).
		WillReturnRows(makeAlertDeliveryRow(101, 99, "abandoned"))

	req := newPOSTWithBody("/api/v1/alert-contacts/11/deliveries/101/retry", nil)
	req.SetPathValue("id", "11")
	req.SetPathValue("delivery_id", "101")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleRetryAlertDelivery)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRetryAlertDeliveryNotAbandoned(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	// Lookup succeeds; delivery is currently 'delivered' (not retryable).
	mock.ExpectQuery(selectAlertDeliveryOneSQL).WithArgs(int64(101)).
		WillReturnRows(makeAlertDeliveryRow(101, 11, "delivered"))
	// RetryDelivery UPDATE returns 0 affected.
	mock.ExpectExec(`UPDATE jetmon_alert_deliveries SET status = 'pending', attempt = 0, next_attempt_at = CURRENT_TIMESTAMP, last_status_code = NULL, last_response = NULL, last_attempt_at = NULL WHERE id = ? AND status = 'abandoned'`).
		WithArgs(int64(101)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// RetryDelivery's error path re-reads to get the current state for the message.
	mock.ExpectQuery(selectAlertDeliveryOneSQL).WithArgs(int64(101)).
		WillReturnRows(makeAlertDeliveryRow(101, 11, "delivered"))

	req := newPOSTWithBody("/api/v1/alert-contacts/11/deliveries/101/retry", nil)
	req.SetPathValue("id", "11")
	req.SetPathValue("delivery_id", "101")
	req = setAuthCtx(req, key)
	rec := invokeAuthed(s, req, s.handleRetryAlertDelivery)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if got := readErrorBody(t, rec.Body).Code; got != "delivery_not_retryable" {
		t.Errorf("code = %q, want delivery_not_retryable", got)
	}
}

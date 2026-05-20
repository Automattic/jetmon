package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

var auditLogColumns = []string{
	"id", "blog_id", "event_id", "event_type", "source", "detail", "metadata", "created_at",
}

const auditBaseSQL = `
		SELECT id, blog_id, event_id, event_type, source, detail, metadata, created_at
		  FROM jetpack_monitor_audit_log
		 WHERE 1=1 ORDER BY id DESC LIMIT ?`

func TestAuditLogReturnsRowsWithMixedNulls(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	now := time.Now().UTC()
	mock.ExpectQuery(auditBaseSQL).
		WithArgs(51).
		WillReturnRows(sqlmock.NewRows(auditLogColumns).
			AddRow(102, nil, nil, "config_change", "local", "reload applied", `{"keys":["VERIFIERS"]}`, now).
			AddRow(101, int64(42), int64(7), "wpcom_sent", "local", "200 OK", nil, now.Add(-time.Minute)))

	req := httptest.NewRequest("GET", "/api/v1/audit-log", nil)
	rec := httptest.NewRecorder()
	s.handleListAuditLog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []auditLogRow `json:"data"`
		Page Page          `json:"page"`
	}
	readJSON(t, rec.Body, &body)
	if len(body.Data) != 2 {
		t.Fatalf("data length = %d, want 2", len(body.Data))
	}
	if body.Data[0].EventType != "config_change" {
		t.Errorf("first row event_type = %q, want config_change", body.Data[0].EventType)
	}
	if body.Data[0].BlogID != nil {
		t.Errorf("config_change BlogID = %v, want nil", *body.Data[0].BlogID)
	}
	if body.Data[1].BlogID == nil || *body.Data[1].BlogID != 42 {
		t.Errorf("wpcom_sent BlogID = %v, want 42", body.Data[1].BlogID)
	}
	if body.Data[1].EventID == nil || *body.Data[1].EventID != 7 {
		t.Errorf("wpcom_sent EventID = %v, want 7", body.Data[1].EventID)
	}
	if body.Page.Next != nil {
		t.Errorf("Next cursor should be nil when results < limit+1")
	}
}

func TestAuditLogFilters(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	expected := `
		SELECT id, blog_id, event_id, event_type, source, detail, metadata, created_at
		  FROM jetpack_monitor_audit_log
		 WHERE 1=1 AND blog_id = ? AND event_type IN (?,?) AND source = ? AND created_at >= ? AND created_at < ? ORDER BY id DESC LIMIT ?`
	since := "2026-05-19T00:00:00Z"
	until := "2026-05-20T00:00:00Z"
	sinceT, _ := time.Parse(time.RFC3339, since)
	untilT, _ := time.Parse(time.RFC3339, until)
	mock.ExpectQuery(expected).
		WithArgs(int64(42), "wpcom_sent", "wpcom_retry", "local", sinceT, untilT, 51).
		WillReturnRows(sqlmock.NewRows(auditLogColumns))

	url := "/api/v1/audit-log?blog_id=42&event_type__in=wpcom_sent,wpcom_retry&source=local&since=" + since + "&until=" + until
	req := httptest.NewRequest("GET", url, nil)
	rec := httptest.NewRecorder()
	s.handleListAuditLog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestAuditLogPaginationProducesCursor(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	rows := sqlmock.NewRows(auditLogColumns)
	now := time.Now().UTC()
	// limit=2 → request 3 rows back; 3 rows means there's another page.
	for i := int64(105); i >= 103; i-- {
		rows.AddRow(i, nil, nil, "config_change", "local", "", nil, now)
	}
	mock.ExpectQuery(auditBaseSQL).
		WithArgs(3).
		WillReturnRows(rows)

	req := httptest.NewRequest("GET", "/api/v1/audit-log?limit=2", nil)
	rec := httptest.NewRecorder()
	s.handleListAuditLog(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data []auditLogRow `json:"data"`
		Page Page          `json:"page"`
	}
	readJSON(t, rec.Body, &body)
	if len(body.Data) != 2 {
		t.Fatalf("data length = %d, want 2 (limit honored)", len(body.Data))
	}
	if body.Page.Next == nil {
		t.Fatal("Next cursor missing when more pages available")
	}
}

func TestAuditLogRejectsInvalidBlogID(t *testing.T) {
	s, _, _, cleanup := newTestServer(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/api/v1/audit-log?blog_id=-5", nil)
	rec := httptest.NewRecorder()
	s.handleListAuditLog(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

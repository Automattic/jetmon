package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const drainLookupSQL = `
		SELECT state, active_checks, queue_depth, retry_queue_size, wpcom_queue_depth, updated_at
		  FROM jetmon_process_health
		 WHERE process_id = ?`

var drainColumns = []string{"state", "active_checks", "queue_depth", "retry_queue_size", "wpcom_queue_depth", "updated_at"}

func TestDrainStatusRunningWithBacklog(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(drainLookupSQL).
		WithArgs("test-host:monitor").
		WillReturnRows(sqlmock.NewRows(drainColumns).
			AddRow("running", 12, 348, 4, 0, time.Now().UTC()))

	req := httptest.NewRequest("GET", "/api/v1/monitor/drain-status", nil)
	rec := httptest.NewRecorder()
	s.handleMonitorDrainStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body drainStatusResponse
	readJSON(t, rec.Body, &body)
	if body.Done {
		t.Errorf("Done = true with running+pending; want false")
	}
	if body.ActiveChecks != 12 || body.QueueDepth != 348 {
		t.Errorf("counters not echoed: %+v", body)
	}
	if body.Reason == "" {
		t.Errorf("Reason should explain why Done=false on a running host")
	}
}

func TestDrainStatusStoppingDone(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(drainLookupSQL).
		WithArgs("test-host:monitor").
		WillReturnRows(sqlmock.NewRows(drainColumns).
			AddRow("stopping", 0, 0, 0, 0, time.Now().UTC()))

	req := httptest.NewRequest("GET", "/api/v1/monitor/drain-status", nil)
	rec := httptest.NewRecorder()
	s.handleMonitorDrainStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body drainStatusResponse
	readJSON(t, rec.Body, &body)
	if !body.Done {
		t.Errorf("Done = false with stopping+empty queues; want true")
	}
	if body.State != "stopping" {
		t.Errorf("State = %q, want stopping", body.State)
	}
}

func TestDrainStatusStoppingInProgress(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(drainLookupSQL).
		WithArgs("test-host:monitor").
		WillReturnRows(sqlmock.NewRows(drainColumns).
			AddRow("stopping", 3, 7, 0, 2, time.Now().UTC()))

	req := httptest.NewRequest("GET", "/api/v1/monitor/drain-status", nil)
	rec := httptest.NewRecorder()
	s.handleMonitorDrainStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body drainStatusResponse
	readJSON(t, rec.Body, &body)
	if body.Done {
		t.Errorf("Done = true with stopping+nonzero queues; want false")
	}
	if body.Reason != "drain in progress" {
		t.Errorf("Reason = %q, want 'drain in progress'", body.Reason)
	}
}

func TestDrainStatusMissingSnapshot(t *testing.T) {
	s, mock, _, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(drainLookupSQL).
		WithArgs("test-host:monitor").
		WillReturnRows(sqlmock.NewRows(drainColumns))

	req := httptest.NewRequest("GET", "/api/v1/monitor/drain-status", nil)
	rec := httptest.NewRecorder()
	s.handleMonitorDrainStatus(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body drainStatusResponse
	readJSON(t, rec.Body, &body)
	if body.Reason == "" {
		t.Errorf("missing-snapshot response should include Reason")
	}
}

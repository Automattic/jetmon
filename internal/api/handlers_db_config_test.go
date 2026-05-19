package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMonitorDBConfigStatusReturnsSanitizedSnapshot(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := requestWithKey(http.MethodGet, "/api/v1/monitor/db-config", key)
	rec := httptest.NewRecorder()
	s.handleMonitorDBConfigStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body dbConfigStatusResponse
	readJSON(t, rec.Body, &body)
	if body.Mode == "" {
		t.Fatal("mode is empty")
	}
	if body.Source == "" {
		t.Fatal("source is empty")
	}
}

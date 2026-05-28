package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/dashboard"
)

type fakeDashboardFleetSource struct {
	snapshot dashboard.FleetSnapshot
	err      error
}

func (f fakeDashboardFleetSource) Snapshot(context.Context) (dashboard.FleetSnapshot, error) {
	return f.snapshot, f.err
}

func TestDashboardAPIHostSnapshot(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	dash := dashboard.New("test-host")
	dash.Update(dashboard.State{WorkerCount: 4, QueueDepth: 2})
	dash.UpdateHealth([]dashboard.HealthEntry{{
		Name:      "mysql",
		Status:    "green",
		CheckedAt: time.Now().UTC(),
	}})
	s.SetDashboard(dash)

	req := requestWithKey(http.MethodGet, "/api/v1/dashboard/host", key)
	rec := invokeAuthed(s, req, s.handleDashboardHost)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got dashboard.HostSnapshot
	readJSON(t, rec.Body, &got)
	if got.State.Hostname != "test-host" {
		t.Fatalf("hostname = %q, want test-host", got.State.Hostname)
	}
	if got.State.WorkerCount != 4 || got.State.QueueDepth != 2 {
		t.Fatalf("state = %+v, want worker_count=4 queue_depth=2", got.State)
	}
	if len(got.Health) != 1 || got.Health[0].Name != "mysql" {
		t.Fatalf("health = %+v, want mysql entry", got.Health)
	}
}

func TestDashboardAPIFleetSnapshot(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	dash := dashboard.New("test-host")
	dash.SetFleetSource(fakeDashboardFleetSource{
		snapshot: dashboard.FleetSnapshot{
			GeneratedAt: time.Now().UTC(),
			Summary:     dashboard.FleetSummary{Status: "green"},
		},
	})
	s.SetDashboard(dash)

	req := requestWithKey(http.MethodGet, "/api/v1/dashboard/fleet", key)
	rec := invokeAuthed(s, req, s.handleDashboardFleet)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got dashboard.FleetSnapshot
	readJSON(t, rec.Body, &got)
	if got.Summary.Status != "green" {
		t.Fatalf("summary status = %q, want green", got.Summary.Status)
	}
}

func TestDashboardAPIUnavailable(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := requestWithKey(http.MethodGet, "/api/v1/dashboard/host", key)
	rec := invokeAuthed(s, req, s.handleDashboardHost)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if got := readErrorBody(t, rec.Body).Code; got != "dashboard_unavailable" {
		t.Fatalf("error code = %q, want dashboard_unavailable", got)
	}
}

func TestDashboardAPIFleetErrors(t *testing.T) {
	tests := []struct {
		name     string
		source   dashboard.FleetSource
		wantCode int
		wantErr  string
	}{
		{
			name:     "missing source",
			wantCode: http.StatusServiceUnavailable,
			wantErr:  "dashboard_unavailable",
		},
		{
			name:     "source failure",
			source:   fakeDashboardFleetSource{err: errors.New("db down")},
			wantCode: http.StatusInternalServerError,
			wantErr:  "dashboard_query_failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, key, cleanup := newTestServer(t)
			defer cleanup()

			dash := dashboard.New("test-host")
			if tt.source != nil {
				dash.SetFleetSource(tt.source)
			}
			s.SetDashboard(dash)

			req := requestWithKey(http.MethodGet, "/api/v1/dashboard/fleet", key)
			rec := invokeAuthed(s, req, s.handleDashboardFleet)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantCode, rec.Body.String())
			}
			if got := readErrorBody(t, rec.Body).Code; got != tt.wantErr {
				t.Fatalf("error code = %q, want %s", got, tt.wantErr)
			}
		})
	}
}

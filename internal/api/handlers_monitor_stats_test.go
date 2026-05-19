package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Automattic/jetmon/internal/metrics"
)

func TestMonitorStatsReturnsLatestSnapshot(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	metrics.StoreStatsFilesSnapshot(metrics.StatsFilesSnapshot{
		SitesPerSec: 12,
		QueueSize:   34,
		Working:     5,
		Waiting:     55,
		Halting:     0,
		Error:       3,
		Offline:     2,
		Success:     95,
		Total:       100,
	})

	req := requestWithKey(http.MethodGet, "/api/v1/monitor/stats", key)
	rec := httptest.NewRecorder()
	s.handleMonitorStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body monitorStatsResponse
	readJSON(t, rec.Body, &body)
	if !body.Available {
		t.Fatal("available = false, want true")
	}
	if body.UpdatedAt == nil || *body.UpdatedAt == "" {
		t.Fatal("updated_at is empty")
	}
	if body.SitesPerSec != 12 || body.QueueSize != 34 || body.Total != 100 {
		t.Fatalf("snapshot fields = %+v, want sites_per_sec=12 queue_size=34 total=100", body)
	}
	if body.Legacy.Totals != ""+
		"working : 5\n"+
		"waiting : 55\n"+
		"halting : 0\n"+
		"error   : 3\n"+
		"offline : 2\n"+
		"success : 95\n"+
		"total   : 100\n" {
		t.Fatalf("legacy totals = %q", body.Legacy.Totals)
	}
}

func TestMonitorStatsReturnsLegacyFileText(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	metrics.StoreStatsFilesSnapshot(metrics.StatsFilesSnapshot{
		SitesPerSec: 12,
		QueueSize:   34,
		Working:     5,
		Waiting:     55,
		Total:       100,
	})

	req := requestWithKey(http.MethodGet, "/api/v1/monitor/stats?file=totals", key)
	rec := httptest.NewRecorder()
	s.handleMonitorStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/plain; charset=utf-8", got)
	}
	if got := rec.Body.String(); got != ""+
		"working : 5\n"+
		"waiting : 55\n"+
		"halting : 0\n"+
		"error   : 0\n"+
		"offline : 0\n"+
		"success : 0\n"+
		"total   : 100\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestMonitorStatsRejectsUnknownLegacyFile(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	metrics.StoreStatsFilesSnapshot(metrics.StatsFilesSnapshot{})

	req := requestWithKey(http.MethodGet, "/api/v1/monitor/stats?file=unknown", key)
	rec := httptest.NewRecorder()
	s.handleMonitorStats(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "invalid_stats_file" {
		t.Fatalf("error code = %q, want invalid_stats_file", body.Code)
	}
}

package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

const siteExistsSQL = `SELECT 1 FROM jetpack_monitor_sites WHERE blog_id = ? LIMIT 1`

const uptimeSQL = ` SELECT severity, state, started_at, ended_at FROM jetmon_events WHERE blog_id = ? AND started_at < ? AND (ended_at IS NULL OR ended_at > ?)`

const rttSamplesSQL = ` SELECT rtt_ms FROM jetmon_check_history WHERE blog_id = ? AND checked_at >= ? AND checked_at < ? AND rtt_ms IS NOT NULL ORDER BY checked_at DESC LIMIT ?`

const timingSamplesSQL = ` SELECT dns_ms, tcp_ms, tls_ms, ttfb_ms FROM jetmon_check_history WHERE blog_id = ? AND checked_at >= ? AND checked_at < ? ORDER BY checked_at DESC LIMIT ?`

func TestParseWindowDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"1h":  time.Hour,
		"24h": 24 * time.Hour,
		"1d":  24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"30d": 30 * 24 * time.Hour,
		"90d": 90 * 24 * time.Hour,
	}
	for s, want := range cases {
		got, err := parseWindowDuration(s)
		if err != nil || got != want {
			t.Errorf("parseWindowDuration(%q) = (%v, %v), want %v", s, got, err, want)
		}
	}
	if _, err := parseWindowDuration("12h"); err == nil {
		t.Error("unsupported window should error")
	}
}

func TestResolveWindowFromQueryDefaults(t *testing.T) {
	q := map[string][]string{}
	from, to, err := resolveWindow(q)
	if err != nil {
		t.Fatalf("resolveWindow: %v", err)
	}
	dur := to.Sub(from)
	if dur < 23*time.Hour || dur > 25*time.Hour {
		t.Errorf("default window = %v, want ~24h", dur)
	}
}

func TestResolveWindowFromTo(t *testing.T) {
	q := map[string][]string{
		"from": {"2026-04-01T00:00:00Z"},
		"to":   {"2026-04-02T00:00:00Z"},
	}
	from, to, err := resolveWindow(q)
	if err != nil {
		t.Fatalf("resolveWindow: %v", err)
	}
	if !from.Equal(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("from = %v, want 2026-04-01", from)
	}
	if !to.Equal(time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("to = %v, want 2026-04-02", to)
	}
}

func TestResolveWindowRejectsHalfRange(t *testing.T) {
	q := map[string][]string{"from": {"2026-04-01T00:00:00Z"}}
	if _, _, err := resolveWindow(q); err == nil {
		t.Error("from without to should error")
	}
}

func TestResolveWindowRejectsBackwardsRange(t *testing.T) {
	q := map[string][]string{
		"from": {"2026-04-02T00:00:00Z"},
		"to":   {"2026-04-01T00:00:00Z"},
	}
	if _, _, err := resolveWindow(q); err == nil {
		t.Error("from after to should error")
	}
}

func TestPercentileNearestRank(t *testing.T) {
	samples := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	cases := []struct {
		p    float64
		want int64
	}{
		{0.0, 10},
		{0.5, 50},
		{0.95, 100}, // ceil(9.5+0.5)-1 = 9 → 100
		{0.99, 100},
		{1.0, 100},
	}
	for _, c := range cases {
		got := percentile(samples, c.p)
		if got != c.want {
			t.Errorf("percentile(p=%.2f) = %d, want %d", c.p, got, c.want)
		}
	}
}

func TestPercentileEmpty(t *testing.T) {
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("percentile(empty) = %d, want 0", got)
	}
}

func TestRoundTo3(t *testing.T) {
	cases := map[float64]float64{
		99.999_999: 100.0,
		99.847_3:   99.847,
		0.0:        0.0,
		100.0:      100.0,
	}
	for in, want := range cases {
		got := roundTo3(in)
		if got != want {
			t.Errorf("roundTo3(%.6f) = %.6f, want %.6f", in, got, want)
		}
	}
}

func TestUptimeHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	// One closed Down event lasting 60s within a 24h window.
	now := time.Now().UTC()
	startedAt := now.Add(-2 * time.Hour)
	endedAt := startedAt.Add(60 * time.Second)
	rows := sqlmock.NewRows([]string{"severity", "state", "started_at", "ended_at"}).
		AddRow(uint8(4), "Down", startedAt, endedAt)
	mock.ExpectQuery(uptimeSQL).WillReturnRows(rows)

	req := requestWithKey("GET", "/api/v1/sites/42/uptime", key)
	req.SetPathValue("id", "42")
	rec := invokeAuthed(s, req, s.handleSiteUptime)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp uptimeResponse
	readJSON(t, rec.Body, &resp)
	if resp.DownSeconds != 60 {
		t.Errorf("down_seconds = %d, want 60", resp.DownSeconds)
	}
	if resp.IncidentCount != 1 {
		t.Errorf("incident_count = %d, want 1", resp.IncidentCount)
	}
	if resp.UptimePercent <= 99.0 || resp.UptimePercent >= 100.0 {
		t.Errorf("uptime_percent = %.3f, want between 99 and 100", resp.UptimePercent)
	}
}

func TestUptimeSiteNotFound(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsSQL).WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))

	req := requestWithKey("GET", "/api/v1/sites/99/uptime", key)
	req.SetPathValue("id", "99")
	rec := invokeAuthed(s, req, s.handleSiteUptime)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestUptimeNoEvents100Percent(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(uptimeSQL).WillReturnRows(sqlmock.NewRows([]string{"severity", "state", "started_at", "ended_at"}))

	req := requestWithKey("GET", "/api/v1/sites/42/uptime", key)
	req.SetPathValue("id", "42")
	rec := invokeAuthed(s, req, s.handleSiteUptime)

	var resp uptimeResponse
	readJSON(t, rec.Body, &resp)
	if resp.UptimePercent != 100.0 {
		t.Errorf("uptime_percent = %.3f, want 100.0", resp.UptimePercent)
	}
	if resp.IncidentCount != 0 || resp.DownSeconds != 0 {
		t.Errorf("expected no incidents; got count=%d down=%d", resp.IncidentCount, resp.DownSeconds)
	}
}

func TestResponseTimeHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	rows := sqlmock.NewRows([]string{"rtt_ms"})
	for _, v := range []int64{100, 200, 300, 400, 500} {
		rows.AddRow(v)
	}
	mock.ExpectQuery(rttSamplesSQL).WillReturnRows(rows)

	req := requestWithKey("GET", "/api/v1/sites/42/response-time", key)
	req.SetPathValue("id", "42")
	rec := invokeAuthed(s, req, s.handleSiteResponseTime)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp responseTimeResponse
	readJSON(t, rec.Body, &resp)
	if resp.Samples != 5 {
		t.Errorf("samples = %d, want 5", resp.Samples)
	}
	if resp.MaxMs != 500 {
		t.Errorf("max_ms = %d, want 500", resp.MaxMs)
	}
	if resp.MeanMs != 300 {
		t.Errorf("mean_ms = %d, want 300", resp.MeanMs)
	}
	if resp.P50Ms == 0 {
		t.Errorf("p50_ms = 0, want non-zero")
	}
}

func TestResponseTimeWithGatewayTenantChecksSiteOwnership(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteTenantCheckSQL).
		WithArgs("tenant-a", int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(siteExistsSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(rttSamplesSQL).
		WillReturnRows(sqlmock.NewRows([]string{"rtt_ms"}).AddRow(int64(123)))

	req := requestWithKey("GET", "/api/v1/sites/42/response-time", key)
	req.SetPathValue("id", "42")
	req = setGatewayTenantCtx(req, key, "tenant-a")
	rec := invokeAuthed(s, req, s.handleSiteResponseTime)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestResponseTimeNoSamples(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))
	mock.ExpectQuery(rttSamplesSQL).WillReturnRows(sqlmock.NewRows([]string{"rtt_ms"}))

	req := requestWithKey("GET", "/api/v1/sites/42/response-time", key)
	req.SetPathValue("id", "42")
	rec := invokeAuthed(s, req, s.handleSiteResponseTime)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp responseTimeResponse
	readJSON(t, rec.Body, &resp)
	if resp.Samples != 0 || resp.MeanMs != 0 || resp.MaxMs != 0 {
		t.Errorf("empty stats should be zero, got %+v", resp)
	}
}

func TestTimingBreakdownHappyPath(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(siteExistsSQL).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"1"}).AddRow(1))

	rows := sqlmock.NewRows([]string{"dns_ms", "tcp_ms", "tls_ms", "ttfb_ms"})
	for i := 0; i < 5; i++ {
		rows.AddRow(int64(10+i*5), int64(20+i*5), int64(30+i*5), int64(150+i*10))
	}
	mock.ExpectQuery(timingSamplesSQL).WillReturnRows(rows)

	req := requestWithKey("GET", "/api/v1/sites/42/timing-breakdown", key)
	req.SetPathValue("id", "42")
	rec := invokeAuthed(s, req, s.handleSiteTimingBreakdown)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp timingBreakdownResponse
	readJSON(t, rec.Body, &resp)
	if resp.Samples != 5 {
		t.Errorf("samples = %d, want 5", resp.Samples)
	}
	if resp.DNS.MaxMs == 0 || resp.TCP.MaxMs == 0 || resp.TLS.MaxMs == 0 || resp.TTFB.MaxMs == 0 {
		t.Errorf("expected non-zero per-component max; got %+v", resp)
	}
	// TTFB should be the slowest component in our test data.
	if resp.TTFB.MaxMs <= resp.DNS.MaxMs {
		t.Errorf("expected TTFB > DNS in test data, got TTFB=%d DNS=%d", resp.TTFB.MaxMs, resp.DNS.MaxMs)
	}
}

func TestStatsRejectsBadWindow(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := requestWithKey("GET", "/api/v1/sites/42/uptime?window=12h", key)
	req.SetPathValue("id", "42")
	rec := invokeAuthed(s, req, s.handleSiteUptime)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "invalid_window" {
		t.Errorf("error code = %q, want invalid_window", body.Code)
	}
}

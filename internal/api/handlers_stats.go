package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// maxSamples bounds the number of jetmon_check_history rows we'll pull into
// memory for percentile computation. 100k covers a 30d window at 26s/check
// per site — beyond that we'd want pre-aggregation, not naive sort.
const maxSamples = 100_000

// uptimeResponse is the shape returned by GET /api/v1/sites/{id}/uptime.
// See API.md "Family 3".
type uptimeResponse struct {
	Window             windowResponse `json:"window"`
	UptimePercent      float64        `json:"uptime_percent"`
	TotalSeconds       int64          `json:"total_seconds"`
	DownSeconds        int64          `json:"down_seconds"`
	DegradedSeconds    int64          `json:"degraded_seconds"`
	WarningSeconds     int64          `json:"warning_seconds"`
	MaintenanceSeconds int64          `json:"maintenance_seconds"`
	UnknownSeconds     int64          `json:"unknown_seconds"`
	IncidentCount      int            `json:"incident_count"`
	MTTRSeconds        int64          `json:"mttr_seconds"`
	MTBFSeconds        int64          `json:"mtbf_seconds"`
}

// responseTimeResponse is the shape returned by GET .../response-time.
type responseTimeResponse struct {
	Window  windowResponse `json:"window"`
	Samples int            `json:"samples"`
	P50Ms   int64          `json:"p50_ms"`
	P95Ms   int64          `json:"p95_ms"`
	P99Ms   int64          `json:"p99_ms"`
	MaxMs   int64          `json:"max_ms"`
	MeanMs  int64          `json:"mean_ms"`
	// Truncated indicates the underlying sample set hit the maxSamples cap;
	// percentiles are computed from the most recent maxSamples rows.
	Truncated bool `json:"truncated"`
}

// timingBreakdownResponse is the shape returned by GET .../timing-breakdown.
// One of Jetmon's distinctive features — most competitors only return total
// response time. Per-component percentiles let consumers pinpoint *where*
// latency is spent.
type timingBreakdownResponse struct {
	Window    windowResponse   `json:"window"`
	Samples   int              `json:"samples"`
	Truncated bool             `json:"truncated"`
	DNS       latencyComponent `json:"dns"`
	TCP       latencyComponent `json:"tcp"`
	TLS       latencyComponent `json:"tls"`
	TTFB      latencyComponent `json:"ttfb"`
}

type latencyComponent struct {
	P50Ms int64 `json:"p50_ms"`
	P95Ms int64 `json:"p95_ms"`
	P99Ms int64 `json:"p99_ms"`
	MaxMs int64 `json:"max_ms"`
}

// windowResponse describes the time window covered by the stats.
type windowResponse struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// resolveWindow returns the [from, to] timestamps for a stats query. Caller
// passes either ?window=24h|7d|30d|90d (default 24h) or both ?from and ?to
// (overrides window). Returns an error message-ready string on bad input.
func resolveWindow(q map[string][]string) (from, to time.Time, err error) {
	now := time.Now().UTC()
	to = now

	fromStr := first(q["from"])
	toStr := first(q["to"])
	if fromStr != "" || toStr != "" {
		if fromStr == "" || toStr == "" {
			return time.Time{}, time.Time{}, errors.New("?from and ?to must be provided together")
		}
		f, parseErr := time.Parse(time.RFC3339, fromStr)
		if parseErr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("?from must be RFC3339: %w", parseErr)
		}
		t, parseErr := time.Parse(time.RFC3339, toStr)
		if parseErr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("?to must be RFC3339: %w", parseErr)
		}
		if !f.Before(t) {
			return time.Time{}, time.Time{}, errors.New("?from must be before ?to")
		}
		return f.UTC(), t.UTC(), nil
	}

	window := first(q["window"])
	if window == "" {
		window = "24h"
	}
	dur, err := parseWindowDuration(window)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return now.Add(-dur), now, nil
}

func parseWindowDuration(s string) (time.Duration, error) {
	switch s {
	case "1h":
		return time.Hour, nil
	case "24h", "1d":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	case "30d":
		return 30 * 24 * time.Hour, nil
	case "90d":
		return 90 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("window must be one of: 1h, 24h, 7d, 30d, 90d")
	}
}

// handleSiteUptime computes uptime statistics over a window from the events
// table. The event log is the source of truth — we sum durations of
// (Down, Seems Down) events that overlap the window and treat the rest as
// up-time. This stays correct even if check frequency changes.
func (s *Server) handleSiteUptime(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_site_id",
			"site id must be a positive integer")
		return
	}

	from, to, werr := resolveWindow(r.URL.Query())
	if werr != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_window", werr.Error())
		return
	}
	if !s.ensureSiteVisibleForRequest(w, r, siteID) {
		return
	}

	// Verify the site exists. This guards against returning a 100% uptime
	// answer for a nonexistent site, which would be confusing.
	if exists, err := s.siteExists(r.Context(), siteID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "site lookup failed: "+err.Error())
		return
	} else if !exists {
		writeSiteNotFound(w, r, siteID)
		return
	}

	stats, err := s.computeUptime(r.Context(), siteID, from, to)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"uptime query failed: "+err.Error())
		return
	}

	resp := uptimeResponse{
		Window: windowResponse{
			From: from.Format(time.RFC3339),
			To:   to.Format(time.RFC3339),
		},
		TotalSeconds:       stats.totalSeconds,
		DownSeconds:        stats.downSeconds,
		DegradedSeconds:    stats.degradedSeconds,
		WarningSeconds:     stats.warningSeconds,
		MaintenanceSeconds: stats.maintenanceSeconds,
		UnknownSeconds:     stats.unknownSeconds,
		IncidentCount:      stats.incidentCount,
		MTTRSeconds:        stats.mttrSeconds,
		MTBFSeconds:        stats.mtbfSeconds,
	}
	if stats.totalSeconds > 0 {
		resp.UptimePercent = roundTo3((1.0 - float64(stats.downSeconds)/float64(stats.totalSeconds)) * 100.0)
	}
	writeJSON(w, http.StatusOK, resp)
}

// uptimeStats is the intermediate computed shape; mapped onto uptimeResponse
// at the handler level.
type uptimeStats struct {
	totalSeconds       int64
	downSeconds        int64
	degradedSeconds    int64
	warningSeconds     int64
	maintenanceSeconds int64
	unknownSeconds     int64
	incidentCount      int
	mttrSeconds        int64
	mtbfSeconds        int64
}

// computeUptime walks events overlapping [from, to] and accumulates per-state
// duration. Events that started before the window are clipped to from; events
// still open at to are clipped to to.
func (s *Server) computeUptime(ctx context.Context, siteID int64, from, to time.Time) (uptimeStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT severity, state, started_at, ended_at
		  FROM jetmon_events
		 WHERE blog_id = ?
		   AND started_at < ?
		   AND (ended_at IS NULL OR ended_at > ?)`,
		siteID, to, from)
	if err != nil {
		return uptimeStats{}, err
	}
	defer rows.Close()

	stats := uptimeStats{
		totalSeconds: int64(to.Sub(from).Seconds()),
	}
	var sumIncidentSeconds int64
	for rows.Next() {
		var (
			severity  uint8
			state     string
			startedAt time.Time
			endedAt   sql.NullTime
		)
		if err := rows.Scan(&severity, &state, &startedAt, &endedAt); err != nil {
			return uptimeStats{}, err
		}

		// Clip event to the window.
		eventFrom := startedAt
		if eventFrom.Before(from) {
			eventFrom = from
		}
		var eventTo time.Time
		if endedAt.Valid {
			eventTo = endedAt.Time
		} else {
			eventTo = to
		}
		if eventTo.After(to) {
			eventTo = to
		}
		dur := int64(eventTo.Sub(eventFrom).Seconds())
		if dur < 0 {
			continue
		}

		// Bucket by state. "Seems Down" counts toward downtime — the design
		// treats it as part of the incident; the operator dashboard renders
		// it as a different color, but for SLA math it's downtime.
		switch state {
		case "Down", "Seems Down":
			stats.downSeconds += dur
			stats.incidentCount++
			sumIncidentSeconds += dur
		case "Degraded":
			stats.degradedSeconds += dur
		case "Warning":
			stats.warningSeconds += dur
		case "Maintenance":
			stats.maintenanceSeconds += dur
		case "Unknown":
			stats.unknownSeconds += dur
		}
	}
	if stats.incidentCount > 0 {
		stats.mttrSeconds = sumIncidentSeconds / int64(stats.incidentCount)
	}
	// MTBF = (uptime / incident_count). If no incidents, leave 0.
	uptimeSeconds := stats.totalSeconds - stats.downSeconds
	if stats.incidentCount > 0 {
		stats.mtbfSeconds = uptimeSeconds / int64(stats.incidentCount)
	}
	return stats, rows.Err()
}

// handleSiteResponseTime returns p50/p95/p99/max/mean of total RTT over a
// window, sourced from jetmon_check_history.
func (s *Server) handleSiteResponseTime(w http.ResponseWriter, r *http.Request) {
	siteID, from, to, ok := s.parseStatsRequest(w, r)
	if !ok {
		return
	}

	samples, truncated, err := s.queryRTTSamples(r.Context(), siteID, from, to)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"response time query failed: "+err.Error())
		return
	}

	resp := responseTimeResponse{
		Window: windowResponse{
			From: from.Format(time.RFC3339),
			To:   to.Format(time.RFC3339),
		},
		Samples:   len(samples),
		Truncated: truncated,
	}
	if len(samples) > 0 {
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		resp.P50Ms = percentile(samples, 0.50)
		resp.P95Ms = percentile(samples, 0.95)
		resp.P99Ms = percentile(samples, 0.99)
		resp.MaxMs = samples[len(samples)-1]
		var sum int64
		for _, v := range samples {
			sum += v
		}
		resp.MeanMs = sum / int64(len(samples))
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSiteTimingBreakdown returns the same percentile shape as
// handleSiteResponseTime but per-component (DNS/TCP/TLS/TTFB).
func (s *Server) handleSiteTimingBreakdown(w http.ResponseWriter, r *http.Request) {
	siteID, from, to, ok := s.parseStatsRequest(w, r)
	if !ok {
		return
	}

	rows, truncated, err := s.queryTimingSamples(r.Context(), siteID, from, to)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"timing breakdown query failed: "+err.Error())
		return
	}

	resp := timingBreakdownResponse{
		Window: windowResponse{
			From: from.Format(time.RFC3339),
			To:   to.Format(time.RFC3339),
		},
		Samples:   len(rows),
		Truncated: truncated,
	}
	if len(rows) > 0 {
		dns := make([]int64, 0, len(rows))
		tcp := make([]int64, 0, len(rows))
		tls := make([]int64, 0, len(rows))
		ttfb := make([]int64, 0, len(rows))
		for _, t := range rows {
			if t.dns >= 0 {
				dns = append(dns, t.dns)
			}
			if t.tcp >= 0 {
				tcp = append(tcp, t.tcp)
			}
			if t.tls >= 0 {
				tls = append(tls, t.tls)
			}
			if t.ttfb >= 0 {
				ttfb = append(ttfb, t.ttfb)
			}
		}
		resp.DNS = computePercentiles(dns)
		resp.TCP = computePercentiles(tcp)
		resp.TLS = computePercentiles(tls)
		resp.TTFB = computePercentiles(ttfb)
	}
	writeJSON(w, http.StatusOK, resp)
}

// parseStatsRequest is the shared prelude for response-time and
// timing-breakdown handlers — validates id, parses window, verifies site
// exists. Returns (siteID, from, to, true) on success or writes the error
// response and returns ok=false.
func (s *Server) parseStatsRequest(w http.ResponseWriter, r *http.Request) (siteID int64, from, to time.Time, ok bool) {
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_site_id",
			"site id must be a positive integer")
		return 0, time.Time{}, time.Time{}, false
	}
	from, to, werr := resolveWindow(r.URL.Query())
	if werr != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_window", werr.Error())
		return 0, time.Time{}, time.Time{}, false
	}
	if !s.ensureSiteVisibleForRequest(w, r, siteID) {
		return 0, time.Time{}, time.Time{}, false
	}
	exists, err := s.siteExists(r.Context(), siteID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site lookup failed: "+err.Error())
		return 0, time.Time{}, time.Time{}, false
	}
	if !exists {
		writeSiteNotFound(w, r, siteID)
		return 0, time.Time{}, time.Time{}, false
	}
	return siteID, from, to, true
}

// siteExists is a cheap existence check used by stats handlers.
func (s *Server) siteExists(ctx context.Context, siteID int64) (bool, error) {
	var dummy int64
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM jetpack_monitor_sites WHERE blog_id = ? LIMIT 1`, siteID,
	).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// queryRTTSamples pulls rtt_ms values for a site within the window. Uses a
// hard cap (maxSamples) and orders by checked_at DESC so a window with more
// data than we can sort still returns the most recent sample.
func (s *Server) queryRTTSamples(ctx context.Context, siteID int64, from, to time.Time) ([]int64, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rtt_ms FROM jetmon_check_history
		 WHERE blog_id = ?
		   AND checked_at >= ?
		   AND checked_at < ?
		   AND rtt_ms IS NOT NULL
		 ORDER BY checked_at DESC
		 LIMIT ?`, siteID, from, to, maxSamples+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := make([]int64, 0, 1024)
	for rows.Next() {
		var v sql.NullInt64
		if err := rows.Scan(&v); err != nil {
			return nil, false, err
		}
		if v.Valid {
			out = append(out, v.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(out) > maxSamples
	if truncated {
		out = out[:maxSamples]
	}
	return out, truncated, nil
}

// timingRow is one jetmon_check_history row's per-component timings.
type timingRow struct {
	dns, tcp, tls, ttfb int64
}

func (s *Server) queryTimingSamples(ctx context.Context, siteID int64, from, to time.Time) ([]timingRow, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT dns_ms, tcp_ms, tls_ms, ttfb_ms FROM jetmon_check_history
		 WHERE blog_id = ?
		   AND checked_at >= ?
		   AND checked_at < ?
		 ORDER BY checked_at DESC
		 LIMIT ?`, siteID, from, to, maxSamples+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	out := make([]timingRow, 0, 1024)
	for rows.Next() {
		var dns, tcp, tls, ttfb sql.NullInt64
		if err := rows.Scan(&dns, &tcp, &tls, &ttfb); err != nil {
			return nil, false, err
		}
		t := timingRow{dns: -1, tcp: -1, tls: -1, ttfb: -1}
		if dns.Valid {
			t.dns = dns.Int64
		}
		if tcp.Valid {
			t.tcp = tcp.Int64
		}
		if tls.Valid {
			t.tls = tls.Int64
		}
		if ttfb.Valid {
			t.ttfb = ttfb.Int64
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(out) > maxSamples
	if truncated {
		out = out[:maxSamples]
	}
	return out, truncated, nil
}

// computePercentiles returns p50/p95/p99/max for a sample slice. Empty input
// → all-zero result.
func computePercentiles(samples []int64) latencyComponent {
	if len(samples) == 0 {
		return latencyComponent{}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return latencyComponent{
		P50Ms: percentile(samples, 0.50),
		P95Ms: percentile(samples, 0.95),
		P99Ms: percentile(samples, 0.99),
		MaxMs: samples[len(samples)-1],
	}
}

// percentile returns the value at the requested rank from a sorted slice.
// p must be in [0, 1]. Uses the nearest-rank method (no interpolation) — fine
// for our resolution and side-steps all the float edge cases.
func percentile(sortedSamples []int64, p float64) int64 {
	if len(sortedSamples) == 0 {
		return 0
	}
	if p <= 0 {
		return sortedSamples[0]
	}
	if p >= 1 {
		return sortedSamples[len(sortedSamples)-1]
	}
	// Index = ceil(p * n) - 1, clamped.
	idx := int(p*float64(len(sortedSamples))+0.5) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sortedSamples) {
		idx = len(sortedSamples) - 1
	}
	return sortedSamples[idx]
}

// roundTo3 rounds to three decimal places — enough resolution for "five 9s"
// (99.999) without producing 99.99837726391... floats in JSON output.
func roundTo3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000.0
}

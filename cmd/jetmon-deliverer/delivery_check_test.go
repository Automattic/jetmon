package main

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestParseDeliveryCheckOptions(t *testing.T) {
	opts, err := parseDeliveryCheckOptions([]string{
		"--host=deliverer-1",
		"--since=30m",
		"--output=json",
		"--max-pending=10",
		"--max-due=0",
		"--max-abandoned=1",
		"--max-failed=2",
		"--require-recent-delivery",
		"--require-recent-webhook-delivery",
		"--require-recent-alert-delivery",
	})
	if err != nil {
		t.Fatalf("parseDeliveryCheckOptions: %v", err)
	}
	if opts.HostOverride != "deliverer-1" {
		t.Fatalf("HostOverride = %q, want deliverer-1", opts.HostOverride)
	}
	if opts.Since != "30m" || opts.Output != "json" {
		t.Fatalf("parsed since/output = %q/%q", opts.Since, opts.Output)
	}
	if opts.MaxPending != 10 || opts.MaxDue != 0 || opts.MaxAbandoned != 1 || opts.MaxFailed != 2 {
		t.Fatalf("parsed thresholds = pending:%d due:%d abandoned:%d failed:%d", opts.MaxPending, opts.MaxDue, opts.MaxAbandoned, opts.MaxFailed)
	}
	if !opts.RequireRecentDelivery || !opts.RequireRecentWebhookDelivery || !opts.RequireRecentAlertDelivery {
		t.Fatalf("recent delivery flags = %+v, want all true", opts)
	}

	defaults, err := parseDeliveryCheckOptions(nil)
	if err != nil {
		t.Fatalf("parseDeliveryCheckOptions(defaults): %v", err)
	}
	if defaults.Since != deliveryCheckDefaultSince || defaults.Output != "text" {
		t.Fatalf("defaults = %+v", defaults)
	}
	if defaults.MaxPending != -1 || defaults.MaxDue != -1 || defaults.MaxAbandoned != -1 || defaults.MaxFailed != -1 {
		t.Fatalf("default thresholds = %+v, want disabled", defaults)
	}

	if _, err := parseDeliveryCheckOptions([]string{"--output=xml"}); err == nil {
		t.Fatal("parseDeliveryCheckOptions accepted invalid output")
	}
	if _, err := parseDeliveryCheckOptions([]string{"--max-due=-2"}); err == nil {
		t.Fatal("parseDeliveryCheckOptions accepted invalid threshold")
	}
	if _, err := parseDeliveryCheckOptions([]string{"--max-failed=-2"}); err == nil {
		t.Fatal("parseDeliveryCheckOptions accepted invalid failed threshold")
	}
	if _, err := parseDeliveryCheckOptions([]string{"extra"}); err == nil {
		t.Fatal("parseDeliveryCheckOptions accepted positional argument")
	}
}

func TestResolveDeliveryCheckCutoff(t *testing.T) {
	now := time.Date(2026, 4, 29, 18, 30, 0, 0, time.UTC)

	durationCutoff, err := resolveDeliveryCheckCutoff(now, "45m")
	if err != nil {
		t.Fatalf("resolveDeliveryCheckCutoff(duration): %v", err)
	}
	if want := now.Add(-45 * time.Minute); !durationCutoff.Equal(want) {
		t.Fatalf("duration cutoff = %s, want %s", durationCutoff, want)
	}

	timestampCutoff, err := resolveDeliveryCheckCutoff(now, "2026-04-29T18:00:00Z")
	if err != nil {
		t.Fatalf("resolveDeliveryCheckCutoff(timestamp): %v", err)
	}
	if want := time.Date(2026, 4, 29, 18, 0, 0, 0, time.UTC); !timestampCutoff.Equal(want) {
		t.Fatalf("timestamp cutoff = %s, want %s", timestampCutoff, want)
	}

	for _, raw := range []string{"", "0s", "-1m", "not-time", "2026-04-29T19:00:00Z"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := resolveDeliveryCheckCutoff(now, raw); err == nil {
				t.Fatalf("resolveDeliveryCheckCutoff(%q) returned nil error", raw)
			}
		})
	}
}

func TestBuildDeliveryCheckReportSummarizesAndAppliesThresholds(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	now := time.Date(2026, 4, 29, 18, 30, 0, 0, time.UTC)
	cutoff := now.Add(-15 * time.Minute)
	expectDeliverySummaryQueries(t, mock, "jetmon_webhook_deliveries", now, cutoff, deliveryTableSummary{
		Pending:             2,
		DueNow:              1,
		FutureRetry:         1,
		DeliveredSince:      4,
		AbandonedSince:      0,
		FailedSince:         2,
		OldestPendingAgeSec: 120,
		OldestDueAgeSec:     60,
	})
	expectDeliverySummaryQueries(t, mock, "jetmon_alert_deliveries", now, cutoff, deliveryTableSummary{
		Pending:             4,
		DueNow:              2,
		FutureRetry:         2,
		DeliveredSince:      0,
		AbandonedSince:      1,
		FailedSince:         0,
		OldestPendingAgeSec: 90,
		OldestDueAgeSec:     30,
	})

	opts := deliveryCheckOptions{
		Since:                 "15m",
		MaxPending:            5,
		MaxDue:                2,
		MaxAbandoned:          0,
		MaxFailed:             1,
		RequireRecentDelivery: true,
	}
	report, err := buildDeliveryCheckReport(context.Background(), sqlDB, &config.Config{
		DeliveryOwnerHost: "deliverer-1",
	}, "deliverer-1", opts, now)
	if err != nil {
		t.Fatalf("buildDeliveryCheckReport: %v", err)
	}
	if report.OK {
		t.Fatal("report.OK = true, want false because thresholds fail")
	}
	if report.Total.Pending != 6 || report.Total.DueNow != 3 || report.Total.FutureRetry != 3 {
		t.Fatalf("total queue summary = %+v", report.Total)
	}
	if report.Total.DeliveredSince != 4 || report.Total.AbandonedSince != 1 {
		t.Fatalf("total terminal summary = %+v", report.Total)
	}
	if report.Total.FailedSince != 2 || report.Total.OldestPendingAgeSec != 120 || report.Total.OldestDueAgeSec != 60 {
		t.Fatalf("total failed/age summary = %+v", report.Total)
	}
	if report.OwnerLevel != "INFO" || !strings.Contains(report.OwnerMessage, "matched") {
		t.Fatalf("owner status = %q %q", report.OwnerLevel, report.OwnerMessage)
	}
	wantFailures := []string{
		"pending deliveries total=6 exceeds max-pending=5",
		"due deliveries total=3 exceeds max-due=2",
		"abandoned deliveries since 2026-04-29T18:15:00Z total=1 exceeds max-abandoned=0",
		"failed deliveries since 2026-04-29T18:15:00Z total=2 exceeds max-failed=1",
	}
	if len(report.Failures) != len(wantFailures) {
		t.Fatalf("failures = %v, want %d failures", report.Failures, len(wantFailures))
	}
	for i, want := range wantFailures {
		if report.Failures[i] != want {
			t.Fatalf("failure[%d] = %q, want %q", i, report.Failures[i], want)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestBuildDeliveryCheckReportRequiresRecentDelivery(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	now := time.Date(2026, 4, 29, 18, 30, 0, 0, time.UTC)
	cutoff := now.Add(-15 * time.Minute)
	expectDeliverySummaryQueries(t, mock, "jetmon_webhook_deliveries", now, cutoff, deliveryTableSummary{})
	expectDeliverySummaryQueries(t, mock, "jetmon_alert_deliveries", now, cutoff, deliveryTableSummary{})

	report, err := buildDeliveryCheckReport(context.Background(), sqlDB, &config.Config{}, "deliverer-1", deliveryCheckOptions{
		Since:                 "15m",
		MaxPending:            -1,
		MaxDue:                -1,
		MaxAbandoned:          -1,
		MaxFailed:             -1,
		RequireRecentDelivery: true,
	}, now)
	if err != nil {
		t.Fatalf("buildDeliveryCheckReport: %v", err)
	}
	if report.OK {
		t.Fatal("report.OK = true, want false")
	}
	if len(report.Failures) != 1 || !strings.Contains(report.Failures[0], "no delivered rows since") {
		t.Fatalf("failures = %v", report.Failures)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestBuildDeliveryCheckReportRequiresRecentDeliveryByKind(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	now := time.Date(2026, 4, 29, 18, 30, 0, 0, time.UTC)
	cutoff := now.Add(-15 * time.Minute)
	expectDeliverySummaryQueries(t, mock, "jetmon_webhook_deliveries", now, cutoff, deliveryTableSummary{DeliveredSince: 1})
	expectDeliverySummaryQueries(t, mock, "jetmon_alert_deliveries", now, cutoff, deliveryTableSummary{})

	report, err := buildDeliveryCheckReport(context.Background(), sqlDB, &config.Config{}, "deliverer-1", deliveryCheckOptions{
		Since:                        "15m",
		MaxPending:                   -1,
		MaxDue:                       -1,
		MaxAbandoned:                 -1,
		MaxFailed:                    -1,
		RequireRecentWebhookDelivery: true,
		RequireRecentAlertDelivery:   true,
	}, now)
	if err != nil {
		t.Fatalf("buildDeliveryCheckReport: %v", err)
	}
	if report.OK {
		t.Fatal("report.OK = true, want false")
	}
	if len(report.Failures) != 1 || !strings.Contains(report.Failures[0], "no alert-contact deliveries since") {
		t.Fatalf("failures = %v", report.Failures)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestQueryRecentTerminalDeliveryCountUsesAttemptAndCreatedFallback(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	cutoff := time.Date(2026, 4, 29, 18, 15, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)FROM jetmon_webhook_deliveries.*status = \?.*last_attempt_at >= \?`).
		WithArgs("abandoned", cutoff).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`(?s)FROM jetmon_webhook_deliveries.*status = \?.*last_attempt_at IS NULL.*created_at >= \?`).
		WithArgs("abandoned", cutoff).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	got, err := queryRecentTerminalDeliveryCount(context.Background(), sqlDB, "jetmon_webhook_deliveries", "abandoned", cutoff)
	if err != nil {
		t.Fatalf("queryRecentTerminalDeliveryCount: %v", err)
	}
	if got != 3 {
		t.Fatalf("queryRecentTerminalDeliveryCount() = %d, want 3", got)
	}
	if _, err := queryRecentTerminalDeliveryCount(context.Background(), sqlDB, "bad_table", "abandoned", cutoff); err == nil {
		t.Fatal("queryRecentTerminalDeliveryCount accepted bad table")
	}
	if _, err := queryRecentTerminalDeliveryCount(context.Background(), sqlDB, "jetmon_webhook_deliveries", "delivered", cutoff); err == nil {
		t.Fatal("queryRecentTerminalDeliveryCount accepted bad status")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestRenderDeliveryCheckReport(t *testing.T) {
	report := deliveryCheckReport{
		OK:          true,
		Host:        "deliverer-1",
		GeneratedAt: time.Date(2026, 4, 29, 18, 30, 0, 0, time.UTC),
		Since:       time.Date(2026, 4, 29, 18, 15, 0, 0, time.UTC),
		Tables: []deliveryTableSummary{
			{Kind: "webhook", Pending: 1, DueNow: 0, FutureRetry: 1, DeliveredSince: 2, FailedSince: 1, OldestPendingAgeSec: 120},
			{Kind: "alert", DeliveredSince: 3},
		},
		Total: deliveryTableSummary{Kind: "total", Pending: 1, FutureRetry: 1, DeliveredSince: 5, FailedSince: 1, OldestPendingAgeSec: 120},
	}

	var textOut bytes.Buffer
	if err := renderDeliveryCheckReport(&textOut, report, "text"); err != nil {
		t.Fatalf("renderDeliveryCheckReport(text): %v", err)
	}
	text := textOut.String()
	for _, want := range []string{"INFO deliverer_host=\"deliverer-1\"", "FAILED_SINCE", "OLDEST_PENDING_SEC", "webhook", "total", "PASS delivery_check=ok"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output missing %q:\n%s", want, text)
		}
	}

	var jsonOut bytes.Buffer
	if err := renderDeliveryCheckReport(&jsonOut, report, "json"); err != nil {
		t.Fatalf("renderDeliveryCheckReport(json): %v", err)
	}
	var decoded deliveryCheckReport
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil {
		t.Fatalf("json output did not decode: %v\n%s", err, jsonOut.String())
	}
	if !decoded.OK || decoded.Host != "deliverer-1" || decoded.Total.DeliveredSince != 5 {
		t.Fatalf("decoded json = %+v", decoded)
	}
	if decoded.Total.FailedSince != 1 || decoded.Total.OldestPendingAgeSec != 120 {
		t.Fatalf("decoded json summary = %+v", decoded.Total)
	}
}

func TestRenderDeliveryCheckReportFailureText(t *testing.T) {
	report := deliveryCheckReport{
		OK:          false,
		Host:        "deliverer-1",
		GeneratedAt: time.Date(2026, 4, 29, 18, 30, 0, 0, time.UTC),
		Since:       time.Date(2026, 4, 29, 18, 15, 0, 0, time.UTC),
		Total:       deliveryTableSummary{Kind: "total"},
		Failures:    []string{"due deliveries total=1 exceeds max-due=0"},
	}

	var out bytes.Buffer
	if err := renderDeliveryCheckReport(&out, report, "text"); err != nil {
		t.Fatalf("renderDeliveryCheckReport(text): %v", err)
	}
	if !strings.Contains(out.String(), "FAIL due deliveries total=1 exceeds max-due=0") {
		t.Fatalf("failure text missing:\n%s", out.String())
	}
}

func expectDeliverySummaryQueries(t *testing.T, mock sqlmock.Sqlmock, table string, now, cutoff time.Time, summary deliveryTableSummary) {
	t.Helper()
	quotedTable := regexp.QuoteMeta(table)
	mock.ExpectQuery(`(?s)MIN\(created_at\).*FROM ` + quotedTable + `.*WHERE status = 'pending'`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"count", "oldest_pending_age_sec"}).
			AddRow(summary.Pending, summary.OldestPendingAgeSec))
	mock.ExpectQuery(`(?s)MIN\(COALESCE\(next_attempt_at, created_at\)\).*FROM `+quotedTable+`.*next_attempt_at IS NULL`).
		WithArgs(now, now).
		WillReturnRows(sqlmock.NewRows([]string{"count", "oldest_due_age_sec"}).
			AddRow(summary.DueNow, summary.OldestDueAgeSec))
	mock.ExpectQuery(`(?s)FROM ` + quotedTable + `.*status = 'pending'.*next_attempt_at > \?`).
		WithArgs(now).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(summary.FutureRetry))
	mock.ExpectQuery(`(?s)FROM ` + quotedTable + `.*status = 'delivered'.*delivered_at >= \?`).
		WithArgs(cutoff).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(summary.DeliveredSince))
	mock.ExpectQuery(`(?s)FROM `+quotedTable+`.*status = \?.*last_attempt_at >= \?`).
		WithArgs("abandoned", cutoff).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(summary.AbandonedSince))
	mock.ExpectQuery(`(?s)FROM `+quotedTable+`.*status = \?.*last_attempt_at IS NULL.*created_at >= \?`).
		WithArgs("abandoned", cutoff).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`(?s)FROM `+quotedTable+`.*status = \?.*last_attempt_at >= \?`).
		WithArgs("failed", cutoff).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(summary.FailedSince))
	mock.ExpectQuery(`(?s)FROM `+quotedTable+`.*status = \?.*last_attempt_at IS NULL.*created_at >= \?`).
		WithArgs("failed", cutoff).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
}

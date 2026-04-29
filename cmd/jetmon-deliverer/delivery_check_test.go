package main

import (
	"bytes"
	"context"
	"encoding/json"
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
		"--require-recent-delivery",
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
	if opts.MaxPending != 10 || opts.MaxDue != 0 || opts.MaxAbandoned != 1 {
		t.Fatalf("parsed thresholds = pending:%d due:%d abandoned:%d", opts.MaxPending, opts.MaxDue, opts.MaxAbandoned)
	}
	if !opts.RequireRecentDelivery {
		t.Fatal("RequireRecentDelivery = false, want true")
	}

	defaults, err := parseDeliveryCheckOptions(nil)
	if err != nil {
		t.Fatalf("parseDeliveryCheckOptions(defaults): %v", err)
	}
	if defaults.Since != deliveryCheckDefaultSince || defaults.Output != "text" {
		t.Fatalf("defaults = %+v", defaults)
	}
	if defaults.MaxPending != -1 || defaults.MaxDue != -1 || defaults.MaxAbandoned != -1 {
		t.Fatalf("default thresholds = %+v, want disabled", defaults)
	}

	if _, err := parseDeliveryCheckOptions([]string{"--output=xml"}); err == nil {
		t.Fatal("parseDeliveryCheckOptions accepted invalid output")
	}
	if _, err := parseDeliveryCheckOptions([]string{"--max-due=-2"}); err == nil {
		t.Fatal("parseDeliveryCheckOptions accepted invalid threshold")
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
	columns := []string{"pending", "due_now", "future_retry", "delivered_since", "abandoned_since"}
	mock.ExpectQuery("FROM jetmon_webhook_deliveries").
		WithArgs(now, now, cutoff, cutoff).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(int64(2), int64(1), int64(1), int64(4), int64(0)))
	mock.ExpectQuery("FROM jetmon_alert_deliveries").
		WithArgs(now, now, cutoff, cutoff).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(int64(4), int64(2), int64(2), int64(0), int64(1)))

	opts := deliveryCheckOptions{
		Since:                 "15m",
		MaxPending:            5,
		MaxDue:                2,
		MaxAbandoned:          0,
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
	if report.OwnerLevel != "INFO" || !strings.Contains(report.OwnerMessage, "matched") {
		t.Fatalf("owner status = %q %q", report.OwnerLevel, report.OwnerMessage)
	}
	wantFailures := []string{
		"pending deliveries total=6 exceeds max-pending=5",
		"due deliveries total=3 exceeds max-due=2",
		"abandoned deliveries since 2026-04-29T18:15:00Z total=1 exceeds max-abandoned=0",
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
	columns := []string{"pending", "due_now", "future_retry", "delivered_since", "abandoned_since"}
	mock.ExpectQuery("FROM jetmon_webhook_deliveries").
		WithArgs(now, now, cutoff, cutoff).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(0, 0, 0, 0, 0))
	mock.ExpectQuery("FROM jetmon_alert_deliveries").
		WithArgs(now, now, cutoff, cutoff).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(0, 0, 0, 0, 0))

	report, err := buildDeliveryCheckReport(context.Background(), sqlDB, &config.Config{}, "deliverer-1", deliveryCheckOptions{
		Since:                 "15m",
		MaxPending:            -1,
		MaxDue:                -1,
		MaxAbandoned:          -1,
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

func TestRenderDeliveryCheckReport(t *testing.T) {
	report := deliveryCheckReport{
		OK:          true,
		Host:        "deliverer-1",
		GeneratedAt: time.Date(2026, 4, 29, 18, 30, 0, 0, time.UTC),
		Since:       time.Date(2026, 4, 29, 18, 15, 0, 0, time.UTC),
		Tables: []deliveryTableSummary{
			{Kind: "webhook", Pending: 1, DueNow: 0, FutureRetry: 1, DeliveredSince: 2},
			{Kind: "alert", DeliveredSince: 3},
		},
		Total: deliveryTableSummary{Kind: "total", Pending: 1, FutureRetry: 1, DeliveredSince: 5},
	}

	var textOut bytes.Buffer
	if err := renderDeliveryCheckReport(&textOut, report, "text"); err != nil {
		t.Fatalf("renderDeliveryCheckReport(text): %v", err)
	}
	text := textOut.String()
	for _, want := range []string{"INFO deliverer_host=\"deliverer-1\"", "KIND", "webhook", "total", "PASS delivery_check=ok"} {
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

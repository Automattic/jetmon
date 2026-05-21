package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/db"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestClassifyUnsafeMonitorURL(t *testing.T) {
	if got := classifyUnsafeMonitorURL("https://example.com"); got != "" {
		t.Fatalf("safe URL classified unsafe: %q", got)
	}
	for _, raw := range []string{
		"example.com",
		"http://127.0.0.1",
		"http://0177.0.0.1",
		"http://user@example.com",
	} {
		if got := classifyUnsafeMonitorURL(raw); got == "" {
			t.Fatalf("classifyUnsafeMonitorURL(%q) = empty, want reason", raw)
		}
	}
}

func TestRunSiteSafetyUnsafeURLsDryRun(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer conn.Close()

	mock.ExpectQuery("SELECT jetpack_monitor_site_id, blog_id, monitor_url").
		WithArgs(int64(0), int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"jetpack_monitor_site_id", "blog_id", "monitor_url"}).
			AddRow(int64(1), int64(101), "https://example.com").
			AddRow(int64(2), int64(102), "http://127.0.0.1"))
	mock.ExpectQuery("SELECT jetpack_monitor_site_id, blog_id, monitor_url").
		WithArgs(int64(2), int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"jetpack_monitor_site_id", "blog_id", "monitor_url"}))

	var out bytes.Buffer
	report, err := runSiteSafetyUnsafeURLs(context.Background(), &out, conn, siteSafetyUnsafeURLOptions{
		BatchSize:  10,
		SampleSize: 5,
	})
	if err != nil {
		t.Fatalf("runSiteSafetyUnsafeURLs: %v", err)
	}
	if report.ScannedActive != 2 || report.UnsafeRows != 1 || report.Deactivated != 0 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(out.String(), "WARN unsafe_url") || !strings.Contains(out.String(), "INFO scanned_active=2 unsafe=1 flagged=0 deactivated=0") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRunSiteSafetyUnsafeURLsExecuteDeactivates(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer conn.Close()

	mock.ExpectQuery("SELECT jetpack_monitor_site_id, blog_id, monitor_url").
		WithArgs(int64(0), int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"jetpack_monitor_site_id", "blog_id", "monitor_url"}).
			AddRow(int64(2), int64(102), "http://127.0.0.1").
			AddRow(int64(3), int64(103), "http://2130706433"))
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO jetpack_monitor_site_safety_flags").
		WithArgs(int64(102), int64(2), db.SiteSafetyFlagUnsafeMonitorURL, sqlmock.AnyArg(), "http://127.0.0.1", db.SiteSafetyStatusDeactivated, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO jetpack_monitor_site_safety_flags").
		WithArgs(int64(103), int64(3), db.SiteSafetyFlagUnsafeMonitorURL, sqlmock.AnyArg(), "http://2130706433", db.SiteSafetyStatusDeactivated, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectExec("UPDATE jetpack_monitor_sites").
		WithArgs(int64(2), int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT jetpack_monitor_site_id, blog_id, monitor_url").
		WithArgs(int64(3), int64(10)).
		WillReturnRows(sqlmock.NewRows([]string{"jetpack_monitor_site_id", "blog_id", "monitor_url"}))

	report, err := runSiteSafetyUnsafeURLs(context.Background(), nil, conn, siteSafetyUnsafeURLOptions{
		BatchSize: 10,
		Execute:   true,
	})
	if err != nil {
		t.Fatalf("runSiteSafetyUnsafeURLs: %v", err)
	}
	if report.UnsafeRows != 2 || report.Flagged != 2 || report.Deactivated != 2 {
		t.Fatalf("report = %+v", report)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestDeactivateUnsafeMonitorURLsChunksLargeBatches(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer conn.Close()

	rows := make([]siteSafetyUnsafeURLRow, 1001)
	for i := range rows {
		rows[i] = siteSafetyUnsafeURLRow{
			SiteID: int64(i + 1),
			BlogID: int64(100000 + i),
			URL:    "http://127.0.0.1",
			Reason: "monitor_url host \"127.0.0.1\" is not public",
		}
	}
	mock.ExpectBegin()
	for i := 0; i < 1000; i++ {
		mock.ExpectExec("INSERT INTO jetpack_monitor_site_safety_flags").
			WillReturnResult(sqlmock.NewResult(int64(i+1), 1))
	}
	mock.ExpectExec("UPDATE jetpack_monitor_sites").
		WillReturnResult(sqlmock.NewResult(0, 1000))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO jetpack_monitor_site_safety_flags").
		WillReturnResult(sqlmock.NewResult(1001, 1))
	mock.ExpectExec("UPDATE jetpack_monitor_sites").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	flagged, deactivated, err := flagAndDeactivateUnsafeMonitorURLs(context.Background(), conn, rows)
	if err != nil {
		t.Fatalf("flagAndDeactivateUnsafeMonitorURLs: %v", err)
	}
	if flagged != 1001 {
		t.Fatalf("flagged = %d, want 1001", flagged)
	}
	if deactivated != 1001 {
		t.Fatalf("deactivated = %d, want 1001", deactivated)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRunSiteSafetyReportSummarizesFlags(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer conn.Close()

	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	firstSeen := now.Add(-2 * time.Hour)
	lastSeen := now.Add(-30 * time.Minute)

	mock.ExpectQuery("SELECT flag_type, status, COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"flag_type", "status", "count", "first_seen_at", "last_seen_at"}).
			AddRow(db.SiteSafetyFlagProbeSafetyBlock, db.SiteSafetyStatusOpen, int64(2), firstSeen, lastSeen).
			AddRow(db.SiteSafetyFlagUnsafeMonitorURL, db.SiteSafetyStatusDeactivated, int64(1), firstSeen, lastSeen))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(db.SiteSafetyStatusOpen, now.Add(-time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery("SELECT monitor_site_id, blog_id, flag_type").
		WithArgs(db.SiteSafetyStatusOpen, 2).
		WillReturnRows(sqlmock.NewRows([]string{"monitor_site_id", "blog_id", "flag_type", "status", "reason", "monitor_url", "first_seen_at", "last_seen_at"}).
			AddRow(int64(12), int64(42), db.SiteSafetyFlagProbeSafetyBlock, db.SiteSafetyStatusOpen, "blocked", "http://127.0.0.1", firstSeen, lastSeen))

	var out bytes.Buffer
	report, err := runSiteSafetyReport(context.Background(), &out, conn, siteSafetyReportOptions{
		Output:     "text",
		Status:     db.SiteSafetyStatusOpen,
		SampleSize: 2,
		StaleAfter: time.Hour,
		MaxOpen:    5,
	}, now)
	if err != nil {
		t.Fatalf("runSiteSafetyReport: %v", err)
	}
	if !report.OK || report.Total != 3 || report.Open != 2 || report.StaleOpen != 0 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(out.String(), "PASS site_safety_flags_report=green total=3 open=2 stale_open=0") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRunSiteSafetyReportFailsOnThresholds(t *testing.T) {
	conn, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer conn.Close()

	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	oldSeen := now.Add(-48 * time.Hour)

	mock.ExpectQuery("SELECT flag_type, status, COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"flag_type", "status", "count", "first_seen_at", "last_seen_at"}).
			AddRow(db.SiteSafetyFlagProbeSafetyBlock, db.SiteSafetyStatusOpen, int64(3), oldSeen, oldSeen))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(db.SiteSafetyStatusOpen, now.Add(-24*time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(3)))

	var out bytes.Buffer
	report, err := runSiteSafetyReport(context.Background(), &out, conn, siteSafetyReportOptions{
		Output:     "text",
		Status:     db.SiteSafetyStatusOpen,
		SampleSize: 0,
		StaleAfter: 24 * time.Hour,
		MaxOpen:    1,
	}, now)
	if err != nil {
		t.Fatalf("runSiteSafetyReport: %v", err)
	}
	if report.OK || report.Status != "red" || len(report.Issues) != 2 {
		t.Fatalf("report = %+v, want red with two issues", report)
	}
	if !strings.Contains(out.String(), "FAIL site_safety_flags_report=red") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

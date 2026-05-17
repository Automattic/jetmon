package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

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
	mock.ExpectExec("INSERT INTO jetmon_site_safety_flags").
		WithArgs(int64(102), int64(2), db.SiteSafetyFlagUnsafeMonitorURL, sqlmock.AnyArg(), "http://127.0.0.1", db.SiteSafetyStatusDeactivated, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO jetmon_site_safety_flags").
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
		mock.ExpectExec("INSERT INTO jetmon_site_safety_flags").
			WillReturnResult(sqlmock.NewResult(int64(i+1), 1))
	}
	mock.ExpectExec("UPDATE jetpack_monitor_sites").
		WillReturnResult(sqlmock.NewResult(0, 1000))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO jetmon_site_safety_flags").
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

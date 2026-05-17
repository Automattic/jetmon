package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

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
	if !strings.Contains(out.String(), "WARN unsafe_url") || !strings.Contains(out.String(), "INFO scanned_active=2 unsafe=1 deactivated=0") {
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
	mock.ExpectExec("UPDATE jetpack_monitor_sites").
		WithArgs(int64(2), int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 2))
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
	if report.UnsafeRows != 2 || report.Deactivated != 2 {
		t.Fatalf("report = %+v", report)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

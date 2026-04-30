package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/eventstore"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestResolveTelemetryWindow(t *testing.T) {
	now := time.Date(2026, 4, 30, 18, 0, 0, 0, time.UTC)
	window, err := resolveTelemetryWindow(now, "2h", "")
	if err != nil {
		t.Fatalf("resolveTelemetryWindow() error = %v", err)
	}
	if !window.Since.Equal(now.Add(-2*time.Hour)) || !window.Until.Equal(now) {
		t.Fatalf("window = %+v, want last 2h ending now", window)
	}

	window, err = resolveTelemetryWindow(now, "2026-04-30T10:00:00Z", "2026-04-30T12:00:00Z")
	if err != nil {
		t.Fatalf("resolveTelemetryWindow(RFC3339) error = %v", err)
	}
	if got := window.Since.Format(time.RFC3339); got != "2026-04-30T10:00:00Z" {
		t.Fatalf("Since = %s", got)
	}
	if got := window.Until.Format(time.RFC3339); got != "2026-04-30T12:00:00Z" {
		t.Fatalf("Until = %s", got)
	}

	if _, err := resolveTelemetryWindow(now, "2026-04-30T12:00:00Z", "2026-04-30T10:00:00Z"); err == nil {
		t.Fatal("resolveTelemetryWindow(inverted) error = nil, want error")
	}
}

func TestBuildTelemetryReport(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	now := time.Date(2026, 4, 30, 18, 0, 0, 0, time.UTC)
	start := now.Add(-2 * time.Hour)
	expectTelemetryReportQueries(t, mock, start, now)

	report, err := buildTelemetryReport(context.Background(), sqlDB, now, telemetryReportOptions{Since: "2h", Limit: 5})
	if err != nil {
		t.Fatalf("buildTelemetryReport() error = %v", err)
	}
	if report.Summary.Opened != 5 || report.Summary.ConfirmedDown != 2 || report.Summary.VerifierFalseAlarm != 1 {
		t.Fatalf("Summary = %+v, want opened=5 confirmed=2 false_alarm=1", report.Summary)
	}
	if len(report.Timings) != 4 || report.Timings[0].Name != "first_failure_to_down" || report.Timings[0].AvgMS != 1500 {
		t.Fatalf("Timings = %+v, want first timing avg 1500", report.Timings)
	}
	if report.Verifier.Replies != 6 || report.Verifier.ConfirmDown != 4 || len(report.Verifier.Hosts) != 2 {
		t.Fatalf("Verifier = %+v, want replies=6 confirm=4 hosts=2", report.Verifier)
	}
	if len(report.FalseAlarmClasses) != 2 || report.FalseAlarmClasses[0].Class != "server" {
		t.Fatalf("FalseAlarmClasses = %+v, want server first", report.FalseAlarmClasses)
	}
	if report.WPCOM.AttemptDelta != 0 || report.WPCOM.Retries != 1 || report.WPCOM.RetryRatePercent == 0 {
		t.Fatalf("WPCOM = %+v, want retry with no delta", report.WPCOM)
	}
	if len(report.ExplanationGaps) != 0 {
		t.Fatalf("ExplanationGaps = %+v, want none", report.ExplanationGaps)
	}
	if report.Status != "pass" || len(report.Highlights) == 0 {
		t.Fatalf("Status/Highlights = %q/%+v, want pass with highlights", report.Status, report.Highlights)
	}
	if len(report.SuggestedNextActions) == 0 || !strings.Contains(report.SuggestedNextActions[0], "consistent") {
		t.Fatalf("SuggestedNextActions = %+v, want consistency guidance", report.SuggestedNextActions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestRenderTelemetryReportText(t *testing.T) {
	report := telemetryReport{
		GeneratedAt: time.Date(2026, 4, 30, 18, 0, 0, 0, time.UTC),
		Window: telemetryWindow{
			Since: time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC),
			Until: time.Date(2026, 4, 30, 18, 0, 0, 0, time.UTC),
		},
		Status:     "pass",
		Highlights: []string{"Telemetry looks internally consistent for this window."},
		Summary:    telemetrySummary{Opened: 5, ConfirmedDown: 2, ProbeCleared: 1},
		Timings: []telemetryTiming{{
			Name:  "first_failure_to_down",
			Count: 2,
			AvgMS: 1500,
			MaxMS: 2500,
		}},
		Verifier: telemetryVerifierReport{Replies: 6, ConfirmDown: 4, Disagree: 2, ConfirmPercent: 66.7},
		FalseAlarmClasses: []telemetryClassCount{{
			Outcome: eventstore.ReasonFalseAlarm,
			Class:   "server",
			Count:   1,
		}},
		WPCOM: telemetryWPCOMReport{
			ExpectedDownTransitions:     2,
			ExpectedRecoveryTransitions: 1,
			Attempts:                    3,
			Retries:                     1,
			RetryRatePercent:            33.3,
		},
		SuggestedNextActions: []string{"Telemetry looks internally consistent for this window."},
	}
	var out bytes.Buffer
	if err := renderTelemetryReport(&out, report, "text"); err != nil {
		t.Fatalf("renderTelemetryReport() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"## Production Telemetry Report",
		"PASS status=pass explanation_gaps=0",
		"INFO highlight=\"Telemetry looks internally consistent for this window.\"",
		"window_end=exclusive",
		"INFO events opened=5 confirmed_down=2",
		"INFO timing=first_failure_to_down count=2 avg_ms=1500 max_ms=2500",
		"INFO verifier_replies=6 confirm_down=4 disagree=2",
		"INFO outcome=false_alarm class=server count=1",
		"INFO expected_down=2 expected_recovery=1 attempts=3",
		"PASS explanation_gaps=0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered report missing %q:\n%s", want, got)
		}
	}
}

func TestRenderTelemetryReportJSON(t *testing.T) {
	report := telemetryReport{
		Command:     "telemetry report",
		GeneratedAt: time.Date(2026, 4, 30, 18, 0, 0, 0, time.UTC),
		Window: telemetryWindow{
			Since: time.Date(2026, 4, 30, 16, 0, 0, 0, time.UTC),
			Until: time.Date(2026, 4, 30, 18, 0, 0, 0, time.UTC),
		},
		Summary: telemetrySummary{Opened: 5, ConfirmedDown: 2},
	}
	var out bytes.Buffer
	if err := renderTelemetryReport(&out, report, "json"); err != nil {
		t.Fatalf("renderTelemetryReport(json) error = %v", err)
	}
	var got telemetryReport
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("rendered JSON did not decode: %v\n%s", err, out.String())
	}
	if got.Command != "telemetry report" || got.Summary.ConfirmedDown != 2 {
		t.Fatalf("decoded report = %+v, want telemetry report with confirmed_down=2", got)
	}
	if err := renderTelemetryReport(&out, report, "yaml"); err == nil {
		t.Fatal("renderTelemetryReport(yaml) error = nil, want error")
	}
}

func TestTelemetryFlagUsageUsesLongDashes(t *testing.T) {
	opts := telemetryReportOptions{Since: "24h", Output: "text", Limit: 10, QueryTimeout: 30 * time.Second}
	var out bytes.Buffer
	fs := newTelemetryReportFlagSet(&opts, &out)
	fs.Usage()
	got := out.String()
	for _, want := range []string{"--limit", "--output", "--query-timeout", "--since", "--until"} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n  -since") || strings.Contains(got, "\n  -output") {
		t.Fatalf("usage used single-dash long flags:\n%s", got)
	}
}

func TestTelemetryValidation(t *testing.T) {
	if _, err := normalizeTelemetryOutput("yaml"); err == nil {
		t.Fatal("normalizeTelemetryOutput(yaml) error = nil, want error")
	}
	if err := validateTelemetryLimit(0); err == nil {
		t.Fatal("validateTelemetryLimit(0) error = nil, want error")
	}
	if err := validateTelemetryLimit(101); err == nil {
		t.Fatal("validateTelemetryLimit(101) error = nil, want error")
	}
	if err := validateTelemetryQueryTimeout(-time.Second); err == nil {
		t.Fatal("validateTelemetryQueryTimeout(-1s) error = nil, want error")
	}
	if err := validateTelemetryQueryTimeout(maxTelemetryQueryTimeout + time.Second); err == nil {
		t.Fatal("validateTelemetryQueryTimeout(too long) error = nil, want error")
	}
}

func expectTelemetryReportQueries(t *testing.T, mock sqlmock.Sqlmock, start, end time.Time) {
	t.Helper()
	mock.ExpectQuery(`SELECT reason, COUNT`).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"reason", "count"}).
			AddRow(eventstore.ReasonOpened, int64(5)).
			AddRow(eventstore.ReasonVerifierConfirmed, int64(2)).
			AddRow(eventstore.ReasonVerifierCleared, int64(1)).
			AddRow(eventstore.ReasonProbeCleared, int64(1)).
			AddRow(eventstore.ReasonFalseAlarm, int64(1)))

	for _, tc := range []struct {
		reason string
		count  int64
		avg    int64
		max    int64
	}{
		{eventstore.ReasonVerifierConfirmed, 2, 1500, 2500},
		{eventstore.ReasonFalseAlarm, 1, 800, 800},
		{eventstore.ReasonProbeCleared, 1, 600, 600},
		{eventstore.ReasonVerifierCleared, 1, 3000, 3000},
	} {
		mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jetmon_event_transitions outcome.*opened.reason = \?`).
			WithArgs(eventstore.ReasonOpened, tc.reason, start, end).
			WillReturnRows(sqlmock.NewRows([]string{"count", "avg", "max"}).AddRow(tc.count, tc.avg, tc.max))
	}

	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*FROM jetmon_audit_log.*detail = 'veriflier reply'`).
		WithArgs(audit.EventVeriflierSent, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count", "confirm", "disagree", "missing"}).
			AddRow(int64(6), int64(4), int64(2), int64(0)))
	mock.ExpectQuery(`(?s)SELECT source,.*GROUP BY source.*LIMIT 5`).
		WithArgs(audit.EventVeriflierSent, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"source", "count", "confirm", "disagree", "missing"}).
			AddRow("verifier-a", int64(4), int64(3), int64(1), int64(0)).
			AddRow("verifier-b", int64(2), int64(1), int64(1), int64(0)))

	mock.ExpectQuery(`(?s)SELECT outcome.reason AS outcome.*GROUP BY outcome.reason, class.*LIMIT 5`).
		WithArgs(eventstore.ReasonOpened, eventstore.ReasonFalseAlarm, eventstore.ReasonProbeCleared, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"outcome", "class", "count"}).
			AddRow(eventstore.ReasonFalseAlarm, "server", int64(1)).
			AddRow(eventstore.ReasonProbeCleared, "client", int64(1)))

	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*detail LIKE 'status=2 %'`).
		WithArgs(audit.EventWPCOMSent, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count", "down", "recovery"}).AddRow(int64(3), int64(2), int64(1)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*event_type = \?`).
		WithArgs(audit.EventWPCOMRetry, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*event_type IN`).
		WithArgs(audit.EventMaintenanceActive, audit.EventAlertSuppressed, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))

	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*reason = \?.*http_code`).
		WithArgs(eventstore.ReasonOpened, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*reason = \?.*verifier_results`).
		WithArgs(eventstore.ReasonVerifierConfirmed, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*reason = \?.*verifier_healthy`).
		WithArgs(eventstore.ReasonFalseAlarm, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*event_type = \?.*\$\.success`).
		WithArgs(audit.EventVeriflierSent, start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
}

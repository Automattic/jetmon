package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/eventstore"
)

const (
	defaultTelemetryQueryTimeout = 30 * time.Second
	maxTelemetryQueryTimeout     = 5 * time.Minute
)

type telemetryReportOptions struct {
	Since        string
	Until        string
	Output       string
	Limit        int
	QueryTimeout time.Duration
}

type telemetryWindow struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

type telemetryReport struct {
	Command              string                  `json:"command"`
	GeneratedAt          time.Time               `json:"generated_at"`
	Window               telemetryWindow         `json:"window"`
	TelemetryStatus      string                  `json:"telemetry_status"`
	Highlights           []string                `json:"highlights,omitempty"`
	ExplanationGapRows   int64                   `json:"explanation_gap_rows"`
	Summary              telemetrySummary        `json:"summary"`
	Timings              []telemetryTiming       `json:"timings"`
	Verifier             telemetryVerifierReport `json:"verifier"`
	FalseAlarmClasses    []telemetryClassCount   `json:"false_alarm_classes"`
	WPCOM                telemetryWPCOMReport    `json:"wpcom"`
	ExplanationGaps      []telemetryGap          `json:"explanation_gaps,omitempty"`
	SuggestedNextActions []string                `json:"suggested_next_actions,omitempty"`
}

type telemetrySummary struct {
	Opened             int64 `json:"opened"`
	ConfirmedDown      int64 `json:"confirmed_down"`
	VerifierCleared    int64 `json:"verifier_cleared"`
	ProbeCleared       int64 `json:"probe_cleared"`
	VerifierFalseAlarm int64 `json:"verifier_false_alarm"`
	ManualOverride     int64 `json:"manual_override"`
	AutoTimeout        int64 `json:"auto_timeout"`
}

type telemetryTiming struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
	AvgMS int64  `json:"avg_ms"`
	MaxMS int64  `json:"max_ms"`
}

type telemetryVerifierReport struct {
	Replies        int64                   `json:"replies"`
	ConfirmDown    int64                   `json:"confirm_down"`
	Disagree       int64                   `json:"disagree"`
	MissingOutcome int64                   `json:"missing_outcome"`
	ConfirmPercent float64                 `json:"confirm_percent"`
	Hosts          []telemetryVerifierHost `json:"hosts,omitempty"`
}

type telemetryVerifierHost struct {
	Host           string  `json:"host"`
	Replies        int64   `json:"replies"`
	ConfirmDown    int64   `json:"confirm_down"`
	Disagree       int64   `json:"disagree"`
	MissingOutcome int64   `json:"missing_outcome"`
	ConfirmPercent float64 `json:"confirm_percent"`
}

type telemetryClassCount struct {
	Outcome string `json:"outcome"`
	Class   string `json:"class"`
	Count   int64  `json:"count"`
}

type telemetryWPCOMReport struct {
	ExpectedDownTransitions     int64   `json:"expected_down_transitions"`
	ExpectedRecoveryTransitions int64   `json:"expected_recovery_transitions"`
	Attempts                    int64   `json:"attempts"`
	DownAttempts                int64   `json:"down_attempts"`
	RecoveryAttempts            int64   `json:"recovery_attempts"`
	Retries                     int64   `json:"retries"`
	Suppressed                  int64   `json:"suppressed"`
	AttemptDelta                int64   `json:"attempt_delta"`
	RetryRatePercent            float64 `json:"retry_rate_percent"`
}

type telemetryGap struct {
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Count    int64  `json:"count"`
	Detail   string `json:"detail"`
}

func cmdTelemetry(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 telemetry <report> [args]")
		os.Exit(1)
	}

	switch args[0] {
	case "report":
		cmdTelemetryReport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown telemetry subcommand %q (want: report)\n", args[0])
		os.Exit(1)
	}
}

func cmdTelemetryReport(args []string) {
	opts := telemetryReportOptions{
		Since:        "24h",
		Output:       "text",
		Limit:        10,
		QueryTimeout: defaultTelemetryQueryTimeout,
	}
	fs := newTelemetryReportFlagSet(&opts, os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "FAIL parse telemetry report flags: %v\n", err)
		os.Exit(2)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 telemetry report [--since=24h] [--until=<RFC3339>] [--output=text|json] [--limit=N] [--query-timeout=30s]")
		os.Exit(1)
	}

	outputFormat, err := normalizeTelemetryOutput(opts.Output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}
	opts.Output = outputFormat
	if err := validateTelemetryLimit(opts.Limit); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}
	if err := validateTelemetryQueryTimeout(opts.QueryTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL db connect: %v\n", err)
		os.Exit(1)
	}

	report, err := buildTelemetryReport(context.Background(), db.DB(), time.Now().UTC(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL telemetry report: %v\n", err)
		os.Exit(1)
	}
	if err := renderTelemetryReport(os.Stdout, report, opts.Output); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL render telemetry report: %v\n", err)
		os.Exit(1)
	}
}

func newTelemetryReportFlagSet(opts *telemetryReportOptions, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("telemetry report", flag.ContinueOnError)
	if out != nil {
		fs.SetOutput(out)
	}
	fs.StringVar(&opts.Since, "since", opts.Since, "report start as duration like 24h or RFC3339 timestamp")
	fs.StringVar(&opts.Until, "until", opts.Until, "report end as RFC3339 timestamp (default now)")
	fs.StringVar(&opts.Output, "output", opts.Output, "output format: text or json")
	fs.IntVar(&opts.Limit, "limit", opts.Limit, "maximum verifier hosts and false-alarm classes to show")
	fs.DurationVar(&opts.QueryTimeout, "query-timeout", opts.QueryTimeout, "maximum time for the report query set")
	fs.Usage = func() {
		printAPIFlagUsage(fs.Output(), fs)
	}
	return fs
}

func normalizeTelemetryOutput(output string) (string, error) {
	output = strings.ToLower(strings.TrimSpace(output))
	if output == "" {
		output = "text"
	}
	if output != "text" && output != "json" {
		return "", errors.New("--output must be text or json")
	}
	return output, nil
}

func validateTelemetryLimit(limit int) error {
	if limit <= 0 {
		return errors.New("--limit must be > 0")
	}
	if limit > 100 {
		return errors.New("--limit must be <= 100")
	}
	return nil
}

func validateTelemetryQueryTimeout(timeout time.Duration) error {
	if timeout < 0 {
		return errors.New("--query-timeout must be >= 0")
	}
	if timeout > maxTelemetryQueryTimeout {
		return fmt.Errorf("--query-timeout must be <= %s", maxTelemetryQueryTimeout)
	}
	return nil
}

func buildTelemetryReport(ctx context.Context, conn *sql.DB, now time.Time, opts telemetryReportOptions) (telemetryReport, error) {
	if conn == nil {
		return telemetryReport{}, errors.New("database pool is not initialized")
	}
	if err := validateTelemetryLimit(opts.Limit); err != nil {
		return telemetryReport{}, err
	}
	if err := validateTelemetryQueryTimeout(opts.QueryTimeout); err != nil {
		return telemetryReport{}, err
	}
	queryTimeout := opts.QueryTimeout
	if queryTimeout == 0 {
		queryTimeout = defaultTelemetryQueryTimeout
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	window, err := resolveTelemetryWindow(now, opts.Since, opts.Until)
	if err != nil {
		return telemetryReport{}, err
	}

	reasonCounts, err := queryTelemetryReasonCounts(ctx, conn, window)
	if err != nil {
		return telemetryReport{}, err
	}
	timings, err := queryTelemetryTimings(ctx, conn, window)
	if err != nil {
		return telemetryReport{}, err
	}
	verifier, err := queryTelemetryVerifier(ctx, conn, window, opts.Limit)
	if err != nil {
		return telemetryReport{}, err
	}
	falseAlarmClasses, err := queryTelemetryFalseAlarmClasses(ctx, conn, window, opts.Limit)
	if err != nil {
		return telemetryReport{}, err
	}
	wpcom, err := queryTelemetryWPCOM(ctx, conn, window, reasonCounts)
	if err != nil {
		return telemetryReport{}, err
	}
	gaps, err := queryTelemetryExplanationGaps(ctx, conn, window)
	if err != nil {
		return telemetryReport{}, err
	}
	gaps = append(gaps, derivedTelemetryGaps(wpcom, verifier)...)

	report := telemetryReport{
		Command:           "telemetry report",
		GeneratedAt:       now.UTC(),
		Window:            window,
		Summary:           telemetrySummaryFromReasons(reasonCounts),
		Timings:           timings,
		Verifier:          verifier,
		FalseAlarmClasses: falseAlarmClasses,
		WPCOM:             wpcom,
		ExplanationGaps:   gaps,
	}
	report.TelemetryStatus = telemetryReportStatus(report.ExplanationGaps)
	report.ExplanationGapRows = telemetryExplanationGapRows(report.ExplanationGaps)
	report.Highlights = telemetryReportHighlights(report)
	report.SuggestedNextActions = suggestTelemetryNextActions(report)
	return report, nil
}

func resolveTelemetryWindow(now time.Time, since, until string) (telemetryWindow, error) {
	now = now.UTC()
	end := now
	until = strings.TrimSpace(until)
	if until != "" {
		t, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return telemetryWindow{}, fmt.Errorf("until %q must be an RFC3339 timestamp", until)
		}
		end = t.UTC()
	}

	start, err := resolveActivityCutoff(end, since)
	if err != nil {
		return telemetryWindow{}, err
	}
	if !start.Before(end) {
		return telemetryWindow{}, errors.New("since must be before until")
	}
	return telemetryWindow{Since: start, Until: end}, nil
}

func queryTelemetryReasonCounts(ctx context.Context, conn *sql.DB, window telemetryWindow) (map[string]int64, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT reason, COUNT(*)
		  FROM jetmon_event_transitions
		 WHERE changed_at >= ?
		   AND changed_at < ?
		 GROUP BY reason`,
		window.Since, window.Until,
	)
	if err != nil {
		return nil, fmt.Errorf("query transition reason counts: %w", err)
	}
	defer rows.Close()

	counts := map[string]int64{}
	for rows.Next() {
		var reason string
		var count int64
		if err := rows.Scan(&reason, &count); err != nil {
			return nil, fmt.Errorf("scan transition reason count: %w", err)
		}
		counts[reason] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transition reason counts: %w", err)
	}
	return counts, nil
}

func queryTelemetryTimings(ctx context.Context, conn *sql.DB, window telemetryWindow) ([]telemetryTiming, error) {
	reasons := []struct {
		reason string
		name   string
	}{
		{eventstore.ReasonVerifierConfirmed, "first_failure_to_down"},
		{eventstore.ReasonFalseAlarm, "first_failure_to_false_alarm"},
		{eventstore.ReasonProbeCleared, "first_failure_to_probe_cleared"},
		{eventstore.ReasonVerifierCleared, "first_failure_to_recovery"},
	}

	out := make([]telemetryTiming, 0, len(reasons))
	for _, item := range reasons {
		var timing telemetryTiming
		timing.Name = item.name
		err := conn.QueryRowContext(ctx, `
			SELECT COUNT(*),
			       COALESCE(CAST(ROUND(AVG(TIMESTAMPDIFF(MICROSECOND, opened.changed_at, outcome.changed_at) / 1000)) AS SIGNED), 0),
			       COALESCE(MAX(TIMESTAMPDIFF(MICROSECOND, opened.changed_at, outcome.changed_at) DIV 1000), 0)
			  FROM jetmon_event_transitions outcome
			  JOIN jetmon_event_transitions opened
			    ON opened.event_id = outcome.event_id
			   AND opened.reason = ?
			 WHERE outcome.reason = ?
			   AND outcome.changed_at >= ?
			   AND outcome.changed_at < ?`,
			eventstore.ReasonOpened, item.reason, window.Since, window.Until,
		).Scan(&timing.Count, &timing.AvgMS, &timing.MaxMS)
		if err != nil {
			return nil, fmt.Errorf("query timing %s: %w", item.name, err)
		}
		out = append(out, timing)
	}
	return out, nil
}

func queryTelemetryVerifier(ctx context.Context, conn *sql.DB, window telemetryWindow, limit int) (telemetryVerifierReport, error) {
	summary := telemetryVerifierReport{}
	err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN LOWER(JSON_UNQUOTE(JSON_EXTRACT(metadata, '$.success'))) = 'false' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN LOWER(JSON_UNQUOTE(JSON_EXTRACT(metadata, '$.success'))) = 'true' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN JSON_EXTRACT(metadata, '$.success') IS NULL THEN 1 ELSE 0 END), 0)
		  FROM jetmon_audit_log
		 WHERE event_type = ?
		   AND detail = 'veriflier reply'
		   AND created_at >= ?
		   AND created_at < ?`,
		audit.EventVeriflierSent, window.Since, window.Until,
	).Scan(&summary.Replies, &summary.ConfirmDown, &summary.Disagree, &summary.MissingOutcome)
	if err != nil {
		return telemetryVerifierReport{}, fmt.Errorf("query verifier summary: %w", err)
	}
	if summary.Replies > 0 {
		summary.ConfirmPercent = float64(summary.ConfirmDown) * 100 / float64(summary.Replies)
	}

	query := fmt.Sprintf(`
		SELECT source,
		       COUNT(*),
		       COALESCE(SUM(CASE WHEN LOWER(JSON_UNQUOTE(JSON_EXTRACT(metadata, '$.success'))) = 'false' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN LOWER(JSON_UNQUOTE(JSON_EXTRACT(metadata, '$.success'))) = 'true' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN JSON_EXTRACT(metadata, '$.success') IS NULL THEN 1 ELSE 0 END), 0)
		  FROM jetmon_audit_log
		 WHERE event_type = ?
		   AND detail = 'veriflier reply'
		   AND created_at >= ?
		   AND created_at < ?
		 GROUP BY source
		 ORDER BY COUNT(*) DESC, source
		 LIMIT %d`, limit)
	rows, err := conn.QueryContext(ctx, query, audit.EventVeriflierSent, window.Since, window.Until)
	if err != nil {
		return telemetryVerifierReport{}, fmt.Errorf("query verifier hosts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var host telemetryVerifierHost
		if err := rows.Scan(&host.Host, &host.Replies, &host.ConfirmDown, &host.Disagree, &host.MissingOutcome); err != nil {
			return telemetryVerifierReport{}, fmt.Errorf("scan verifier host: %w", err)
		}
		if host.Replies > 0 {
			host.ConfirmPercent = float64(host.ConfirmDown) * 100 / float64(host.Replies)
		}
		summary.Hosts = append(summary.Hosts, host)
	}
	if err := rows.Err(); err != nil {
		return telemetryVerifierReport{}, fmt.Errorf("iterate verifier hosts: %w", err)
	}
	return summary, nil
}

func queryTelemetryFalseAlarmClasses(ctx context.Context, conn *sql.DB, window telemetryWindow, limit int) ([]telemetryClassCount, error) {
	query := fmt.Sprintf(`
		SELECT outcome.reason AS outcome,
		       CASE
		         WHEN CAST(JSON_UNQUOTE(JSON_EXTRACT(opened.metadata, '$.error_code')) AS SIGNED) IN (%d, %d) THEN 'https'
		         WHEN CAST(JSON_UNQUOTE(JSON_EXTRACT(opened.metadata, '$.error_code')) AS SIGNED) = %d THEN 'intermittent'
		         WHEN CAST(JSON_UNQUOTE(JSON_EXTRACT(opened.metadata, '$.error_code')) AS SIGNED) = %d THEN 'redirect'
		         WHEN CAST(JSON_UNQUOTE(JSON_EXTRACT(opened.metadata, '$.error_code')) AS SIGNED) = %d THEN 'keyword'
		         WHEN CAST(JSON_UNQUOTE(JSON_EXTRACT(opened.metadata, '$.http_code')) AS SIGNED) >= 500 THEN 'server'
		         WHEN CAST(JSON_UNQUOTE(JSON_EXTRACT(opened.metadata, '$.http_code')) AS SIGNED) = 403 THEN 'blocked'
		         WHEN CAST(JSON_UNQUOTE(JSON_EXTRACT(opened.metadata, '$.http_code')) AS SIGNED) >= 400 THEN 'client'
		         WHEN CAST(JSON_UNQUOTE(JSON_EXTRACT(opened.metadata, '$.error_code')) AS SIGNED) > 0 THEN 'intermittent'
		         ELSE 'unknown'
		       END AS class,
		       COUNT(*) AS count
		  FROM jetmon_event_transitions outcome
		  JOIN jetmon_event_transitions opened
		    ON opened.event_id = outcome.event_id
		   AND opened.reason = ?
		 WHERE outcome.reason IN (?, ?)
		   AND outcome.changed_at >= ?
		   AND outcome.changed_at < ?
		 GROUP BY outcome.reason, class
		 ORDER BY count DESC, outcome, class
		 LIMIT %d`,
		checker.ErrorSSL,
		checker.ErrorTLSExpired,
		checker.ErrorTimeout,
		checker.ErrorRedirect,
		checker.ErrorKeyword,
		limit,
	)
	rows, err := conn.QueryContext(ctx, query,
		eventstore.ReasonOpened,
		eventstore.ReasonFalseAlarm,
		eventstore.ReasonProbeCleared,
		window.Since,
		window.Until,
	)
	if err != nil {
		return nil, fmt.Errorf("query false-alarm classes: %w", err)
	}
	defer rows.Close()

	var out []telemetryClassCount
	for rows.Next() {
		var row telemetryClassCount
		if err := rows.Scan(&row.Outcome, &row.Class, &row.Count); err != nil {
			return nil, fmt.Errorf("scan false-alarm class: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate false-alarm classes: %w", err)
	}
	return out, nil
}

func queryTelemetryWPCOM(ctx context.Context, conn *sql.DB, window telemetryWindow, reasonCounts map[string]int64) (telemetryWPCOMReport, error) {
	report := telemetryWPCOMReport{
		ExpectedDownTransitions:     reasonCounts[eventstore.ReasonVerifierConfirmed],
		ExpectedRecoveryTransitions: reasonCounts[eventstore.ReasonVerifierCleared] + reasonCounts[eventstore.ReasonProbeCleared],
	}
	err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN detail LIKE 'status=2 %' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN detail LIKE 'status=1 %' THEN 1 ELSE 0 END), 0)
		  FROM jetmon_audit_log
		 WHERE event_type = ?
		   AND created_at >= ?
		   AND created_at < ?`,
		audit.EventWPCOMSent, window.Since, window.Until,
	).Scan(&report.Attempts, &report.DownAttempts, &report.RecoveryAttempts)
	if err != nil {
		return telemetryWPCOMReport{}, fmt.Errorf("query WPCOM attempts: %w", err)
	}

	err = conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetmon_audit_log
		 WHERE event_type = ?
		   AND created_at >= ?
		   AND created_at < ?`,
		audit.EventWPCOMRetry, window.Since, window.Until,
	).Scan(&report.Retries)
	if err != nil {
		return telemetryWPCOMReport{}, fmt.Errorf("query WPCOM retries: %w", err)
	}

	err = conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetmon_audit_log
		 WHERE event_type IN (?, ?)
		   AND created_at >= ?
		   AND created_at < ?`,
		audit.EventMaintenanceActive, audit.EventAlertSuppressed, window.Since, window.Until,
	).Scan(&report.Suppressed)
	if err != nil {
		return telemetryWPCOMReport{}, fmt.Errorf("query WPCOM suppressions: %w", err)
	}

	expectedAttempts := report.ExpectedDownTransitions + report.ExpectedRecoveryTransitions
	report.AttemptDelta = expectedAttempts - report.Attempts - report.Suppressed
	if report.Attempts > 0 {
		report.RetryRatePercent = float64(report.Retries) * 100 / float64(report.Attempts)
	}
	return report, nil
}

func queryTelemetryExplanationGaps(ctx context.Context, conn *sql.DB, window telemetryWindow) ([]telemetryGap, error) {
	gapQueries := []struct {
		name     string
		severity string
		detail   string
		query    string
		args     []any
	}{
		{
			name:     "opened_missing_failure_metadata",
			severity: "amber",
			detail:   "opened transitions should explain the local failure with http_code or error_code plus rtt_ms",
			query: `
				SELECT COUNT(*)
				  FROM jetmon_event_transitions
				 WHERE reason = ?
				   AND changed_at >= ?
				   AND changed_at < ?
				   AND (metadata IS NULL
				    OR (JSON_EXTRACT(metadata, '$.http_code') IS NULL AND JSON_EXTRACT(metadata, '$.error_code') IS NULL)
				    OR JSON_EXTRACT(metadata, '$.rtt_ms') IS NULL)`,
			args: []any{eventstore.ReasonOpened, window.Since, window.Until},
		},
		{
			name:     "confirmed_down_missing_verifier_results",
			severity: "amber",
			detail:   "verifier-confirmed transitions should include verifier_results for operator explanations",
			query: `
				SELECT COUNT(*)
				  FROM jetmon_event_transitions
				 WHERE reason = ?
				   AND changed_at >= ?
				   AND changed_at < ?
				   AND (metadata IS NULL OR JSON_EXTRACT(metadata, '$.verifier_results') IS NULL)`,
			args: []any{eventstore.ReasonVerifierConfirmed, window.Since, window.Until},
		},
		{
			name:     "false_alarm_missing_verifier_counts",
			severity: "amber",
			detail:   "false-alarm transitions should include verifier healthy/confirmed counts",
			query: `
				SELECT COUNT(*)
				  FROM jetmon_event_transitions
				 WHERE reason = ?
				   AND changed_at >= ?
				   AND changed_at < ?
				   AND (metadata IS NULL
				    OR JSON_EXTRACT(metadata, '$.verifier_healthy') IS NULL
				    OR JSON_EXTRACT(metadata, '$.verifier_confirmed') IS NULL)`,
			args: []any{eventstore.ReasonFalseAlarm, window.Since, window.Until},
		},
		{
			name:     "verifier_reply_missing_outcome",
			severity: "amber",
			detail:   "verifier reply audit rows should include metadata.success so agreement can be measured",
			query: `
				SELECT COUNT(*)
				  FROM jetmon_audit_log
				 WHERE event_type = ?
				   AND detail = 'veriflier reply'
				   AND created_at >= ?
				   AND created_at < ?
				   AND (metadata IS NULL OR JSON_EXTRACT(metadata, '$.success') IS NULL)`,
			args: []any{audit.EventVeriflierSent, window.Since, window.Until},
		},
	}

	var gaps []telemetryGap
	for _, gapQuery := range gapQueries {
		var count int64
		if err := conn.QueryRowContext(ctx, gapQuery.query, gapQuery.args...).Scan(&count); err != nil {
			return nil, fmt.Errorf("query telemetry gap %s: %w", gapQuery.name, err)
		}
		if count > 0 {
			gaps = append(gaps, telemetryGap{
				Name:     gapQuery.name,
				Severity: gapQuery.severity,
				Count:    count,
				Detail:   gapQuery.detail,
			})
		}
	}
	return gaps, nil
}

func telemetrySummaryFromReasons(counts map[string]int64) telemetrySummary {
	return telemetrySummary{
		Opened:             counts[eventstore.ReasonOpened],
		ConfirmedDown:      counts[eventstore.ReasonVerifierConfirmed],
		VerifierCleared:    counts[eventstore.ReasonVerifierCleared],
		ProbeCleared:       counts[eventstore.ReasonProbeCleared],
		VerifierFalseAlarm: counts[eventstore.ReasonFalseAlarm],
		ManualOverride:     counts[eventstore.ReasonManualOverride],
		AutoTimeout:        counts[eventstore.ReasonAutoTimeout],
	}
}

func derivedTelemetryGaps(wpcom telemetryWPCOMReport, verifier telemetryVerifierReport) []telemetryGap {
	var gaps []telemetryGap
	if wpcom.AttemptDelta != 0 {
		gaps = append(gaps, telemetryGap{
			Name:     "wpcom_attempt_delta",
			Severity: "amber",
			Count:    absInt64(wpcom.AttemptDelta),
			Detail:   "WPCOM attempts differ from down/recovery transitions after maintenance and cooldown suppressions",
		})
	}
	if verifier.Replies == 0 && wpcom.ExpectedDownTransitions > 0 {
		gaps = append(gaps, telemetryGap{
			Name:     "no_verifier_replies_for_event_window",
			Severity: "amber",
			Count:    wpcom.ExpectedDownTransitions,
			Detail:   "verifier-confirmed transitions exist but no verifier reply audit rows were recorded in the same window",
		})
	}
	return gaps
}

func telemetryReportStatus(gaps []telemetryGap) string {
	status := "pass"
	for _, gap := range gaps {
		if gap.Severity == "red" {
			return "fail"
		}
		if gap.Count > 0 {
			status = "warn"
		}
	}
	return status
}

func telemetryExplanationGapRows(gaps []telemetryGap) int64 {
	var total int64
	for _, gap := range gaps {
		total += gap.Count
	}
	return total
}

func telemetryReportHighlights(report telemetryReport) []string {
	var highlights []string
	if report.WPCOM.AttemptDelta != 0 {
		highlights = append(highlights, fmt.Sprintf("WPCOM attempt delta is %d after expected suppressions.", report.WPCOM.AttemptDelta))
	}
	for _, gap := range report.ExplanationGaps {
		switch gap.Name {
		case "no_verifier_replies_for_event_window":
			highlights = append(highlights, fmt.Sprintf("No verifier reply audit rows were recorded for %d verifier-confirmed transition(s).", gap.Count))
		case "verifier_reply_missing_outcome":
			highlights = append(highlights, fmt.Sprintf("Verifier reply outcome is missing for %d audit row(s).", gap.Count))
		}
	}
	if len(report.ExplanationGaps) > 0 {
		highlights = append(highlights, fmt.Sprintf("%d explanation gap type(s) across %d row(s) need follow-up before using this report for customer-facing explanations.", len(report.ExplanationGaps), report.ExplanationGapRows))
	}
	if report.Verifier.Replies > 0 {
		highlights = append(highlights, fmt.Sprintf("Verifier agreement is %.1f%% across %d replies.", report.Verifier.ConfirmPercent, report.Verifier.Replies))
	}
	if report.Summary.Opened == 0 {
		highlights = append(highlights, "No events opened in this window; widen --since before drawing production conclusions.")
	}
	if len(highlights) == 0 {
		highlights = append(highlights, "Telemetry looks internally consistent for this window.")
	}
	return uniqueStrings(highlights)
}

func suggestTelemetryNextActions(report telemetryReport) []string {
	var actions []string
	for _, gap := range report.ExplanationGaps {
		switch gap.Name {
		case "wpcom_attempt_delta":
			actions = append(actions, "Review WPCOM audit rows against event transitions for the same window; suppressions or missing audit writes may explain the delta.")
		case "opened_missing_failure_metadata", "confirmed_down_missing_verifier_results", "false_alarm_missing_verifier_counts", "verifier_reply_missing_outcome":
			actions = append(actions, "Fix telemetry metadata gaps before relying on this report for customer-facing explanations.")
		case "no_verifier_replies_for_event_window":
			actions = append(actions, "Check verifier audit logging and verifier configuration; transition outcomes need matching verifier reply context.")
		}
	}
	if report.Summary.Opened == 0 {
		actions = append(actions, "No events opened in this window; widen --since before drawing production conclusions.")
	}
	if report.Verifier.Replies > 0 && report.Verifier.MissingOutcome > 0 {
		actions = append(actions, "Inspect verifier reply metadata; missing success fields prevent agreement reporting.")
	}
	if len(actions) == 0 {
		actions = append(actions, "Telemetry looks internally consistent for this window; compare rates across longer windows as v2 traffic grows.")
	}
	return uniqueStrings(actions)
}

func renderTelemetryReport(out io.Writer, report telemetryReport, output string) error {
	if output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	if output == "text" {
		renderTelemetryReportText(out, report)
		return nil
	}
	return fmt.Errorf("unsupported telemetry output %q", output)
}

func renderTelemetryReportText(out io.Writer, report telemetryReport) {
	fmt.Fprintf(out, "## Production Telemetry Report\n")
	statusLevel := telemetryReportStatusLevel(report.TelemetryStatus)
	fmt.Fprintf(out, "%s telemetry_status=%s explanation_gap_types=%d explanation_gap_rows=%d suggested_actions=%d\n",
		statusLevel,
		report.TelemetryStatus,
		len(report.ExplanationGaps),
		report.ExplanationGapRows,
		len(report.SuggestedNextActions),
	)
	for _, highlight := range report.Highlights {
		fmt.Fprintf(out, "INFO highlight=%q\n", highlight)
	}
	fmt.Fprintf(out, "INFO generated_at=%s window=%s..%s window_end=exclusive\n",
		report.GeneratedAt.Format(time.RFC3339),
		report.Window.Since.Format(time.RFC3339),
		report.Window.Until.Format(time.RFC3339),
	)
	fmt.Fprintf(out, "INFO events opened=%d confirmed_down=%d verifier_cleared=%d probe_cleared=%d false_alarm=%d manual_override=%d auto_timeout=%d\n",
		report.Summary.Opened,
		report.Summary.ConfirmedDown,
		report.Summary.VerifierCleared,
		report.Summary.ProbeCleared,
		report.Summary.VerifierFalseAlarm,
		report.Summary.ManualOverride,
		report.Summary.AutoTimeout,
	)

	fmt.Fprintln(out, "## Detection Timing")
	for _, timing := range report.Timings {
		fmt.Fprintf(out, "INFO timing=%s count=%d avg_ms=%d max_ms=%d\n", timing.Name, timing.Count, timing.AvgMS, timing.MaxMS)
	}

	fmt.Fprintln(out, "## Verifier Agreement")
	fmt.Fprintf(out, "INFO verifier_replies=%d confirm_down=%d disagree=%d missing_outcome=%d confirm_percent=%.1f\n",
		report.Verifier.Replies,
		report.Verifier.ConfirmDown,
		report.Verifier.Disagree,
		report.Verifier.MissingOutcome,
		report.Verifier.ConfirmPercent,
	)
	for _, host := range report.Verifier.Hosts {
		fmt.Fprintf(out, "INFO verifier_host=%q replies=%d confirm_down=%d disagree=%d missing_outcome=%d confirm_percent=%.1f\n",
			host.Host,
			host.Replies,
			host.ConfirmDown,
			host.Disagree,
			host.MissingOutcome,
			host.ConfirmPercent,
		)
	}

	fmt.Fprintln(out, "## False Alarm Classes")
	if len(report.FalseAlarmClasses) == 0 {
		fmt.Fprintln(out, "INFO false_alarm_classes=none")
	}
	for _, row := range report.FalseAlarmClasses {
		fmt.Fprintf(out, "INFO outcome=%s class=%s count=%d\n", row.Outcome, row.Class, row.Count)
	}

	fmt.Fprintln(out, "## WPCOM Parity")
	fmt.Fprintf(out, "INFO expected_down=%d expected_recovery=%d attempts=%d down_attempts=%d recovery_attempts=%d retries=%d suppressed=%d attempt_delta=%d retry_rate=%.1f%%\n",
		report.WPCOM.ExpectedDownTransitions,
		report.WPCOM.ExpectedRecoveryTransitions,
		report.WPCOM.Attempts,
		report.WPCOM.DownAttempts,
		report.WPCOM.RecoveryAttempts,
		report.WPCOM.Retries,
		report.WPCOM.Suppressed,
		report.WPCOM.AttemptDelta,
		report.WPCOM.RetryRatePercent,
	)

	fmt.Fprintln(out, "## Explanation Gaps")
	if len(report.ExplanationGaps) == 0 {
		fmt.Fprintln(out, "PASS explanation_gaps=0")
	}
	for _, gap := range report.ExplanationGaps {
		level := "WARN"
		if gap.Severity == "red" {
			level = "FAIL"
		}
		fmt.Fprintf(out, "%s gap=%s count=%d detail=%q\n", level, gap.Name, gap.Count, gap.Detail)
	}
	for _, action := range report.SuggestedNextActions {
		fmt.Fprintf(out, "INFO suggested_next_action=%q\n", action)
	}
}

func telemetryReportStatusLevel(status string) string {
	switch status {
	case "fail":
		return "FAIL"
	case "warn":
		return "WARN"
	default:
		return "PASS"
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

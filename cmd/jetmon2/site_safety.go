package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/netguard"
)

type siteSafetyUnsafeURLOptions struct {
	BatchSize  int
	Limit      int64
	SampleSize int
	Execute    bool
}

type siteSafetyUnsafeURLReport struct {
	ScannedActive int64
	UnsafeRows    int64
	Flagged       int64
	Deactivated   int64
	Samples       []siteSafetyUnsafeURLRow
}

type siteSafetyUnsafeURLRow struct {
	SiteID int64
	BlogID int64
	URL    string
	Reason string
}

type siteSafetyReportOptions struct {
	Output     string
	Status     string
	SampleSize int
	StaleAfter time.Duration
	MaxOpen    int64
}

type siteSafetyFlagSummary struct {
	FlagType    string `json:"flag_type"`
	Status      string `json:"status"`
	Count       int64  `json:"count"`
	FirstSeenAt string `json:"first_seen_at,omitempty"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`
}

type siteSafetyFlagSample struct {
	SiteID      int64  `json:"site_id"`
	BlogID      int64  `json:"blog_id"`
	FlagType    string `json:"flag_type"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	URL         string `json:"url,omitempty"`
	FirstSeenAt string `json:"first_seen_at,omitempty"`
	LastSeenAt  string `json:"last_seen_at,omitempty"`
}

type siteSafetyReport struct {
	Command           string                  `json:"command"`
	OK                bool                    `json:"ok"`
	Status            string                  `json:"status"`
	GeneratedAt       string                  `json:"generated_at"`
	Total             int64                   `json:"total"`
	Open              int64                   `json:"open"`
	StaleOpen         int64                   `json:"stale_open"`
	StaleAfterSeconds int64                   `json:"stale_after_seconds,omitempty"`
	MaxOpen           *int64                  `json:"max_open,omitempty"`
	Summaries         []siteSafetyFlagSummary `json:"summaries"`
	Samples           []siteSafetyFlagSample  `json:"samples,omitempty"`
	Issues            []string                `json:"issues,omitempty"`
}

func cmdSiteSafety(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 site-safety <unsafe-urls|report> [args]")
		os.Exit(2)
	}
	switch args[0] {
	case "unsafe-urls":
		cmdSiteSafetyUnsafeURLs(args[1:])
	case "report":
		cmdSiteSafetyReport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown site-safety subcommand %q (want: unsafe-urls or report)\n", args[0])
		os.Exit(2)
	}
}

func cmdSiteSafetyUnsafeURLs(args []string) {
	fs := flag.NewFlagSet("site-safety unsafe-urls", flag.ExitOnError)
	opts := siteSafetyUnsafeURLOptions{}
	fs.IntVar(&opts.BatchSize, "batch-size", 1000, "active site rows to scan per query")
	fs.Int64Var(&opts.Limit, "limit", 0, "maximum active rows to scan, 0 for all")
	fs.IntVar(&opts.SampleSize, "sample-size", 50, "maximum unsafe examples to print")
	fs.BoolVar(&opts.Execute, "execute", false, "deactivate unsafe active rows; default is dry-run")
	_ = fs.Parse(args)
	if opts.BatchSize <= 0 {
		fmt.Fprintln(os.Stderr, "batch-size must be positive")
		os.Exit(2)
	}
	if opts.SampleSize < 0 {
		fmt.Fprintln(os.Stderr, "sample-size must be >= 0")
		os.Exit(2)
	}

	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "site-safety unsafe-urls: config parse: %v\n", err)
		os.Exit(1)
	}
	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		fmt.Fprintf(os.Stderr, "site-safety unsafe-urls: db connect: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	report, err := runSiteSafetyUnsafeURLs(ctx, os.Stdout, db.DB(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "site-safety unsafe-urls: %v\n", err)
		os.Exit(1)
	}
	if report.UnsafeRows > report.Deactivated && !opts.Execute {
		fmt.Fprintln(os.Stdout, "INFO dry_run=true pass --execute to deactivate unsafe active rows")
	}
}

func cmdSiteSafetyReport(args []string) {
	fs := flag.NewFlagSet("site-safety report", flag.ExitOnError)
	opts := siteSafetyReportOptions{Output: "text", Status: db.SiteSafetyStatusOpen, SampleSize: 20, StaleAfter: 24 * time.Hour, MaxOpen: -1}
	fs.StringVar(&opts.Output, "output", opts.Output, "output format: text or json")
	fs.StringVar(&opts.Status, "status", opts.Status, "sample status filter: open, deactivated, ignored, resolved, or all")
	fs.IntVar(&opts.SampleSize, "sample-size", opts.SampleSize, "maximum flag examples to print")
	fs.DurationVar(&opts.StaleAfter, "stale-after", opts.StaleAfter, "age after which open flags are reported as stale; 0 disables stale reporting")
	fs.Int64Var(&opts.MaxOpen, "max-open", opts.MaxOpen, "fail if open flags exceed this threshold; -1 disables the threshold")
	_ = fs.Parse(args)
	if opts.SampleSize < 0 {
		fmt.Fprintln(os.Stderr, "sample-size must be >= 0")
		os.Exit(2)
	}
	if opts.MaxOpen < -1 {
		fmt.Fprintln(os.Stderr, "max-open must be >= -1")
		os.Exit(2)
	}
	opts.Output = strings.TrimSpace(strings.ToLower(opts.Output))
	opts.Status = strings.TrimSpace(strings.ToLower(opts.Status))
	if !validSiteSafetyReportStatus(opts.Status) {
		fmt.Fprintln(os.Stderr, "status must be one of: open, deactivated, ignored, resolved, all")
		os.Exit(2)
	}

	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "site-safety report: config parse: %v\n", err)
		os.Exit(1)
	}
	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		fmt.Fprintf(os.Stderr, "site-safety report: db connect: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, err := runSiteSafetyReport(ctx, os.Stdout, db.DB(), opts, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "site-safety report: %v\n", err)
		os.Exit(1)
	}
	if !report.OK {
		os.Exit(1)
	}
}

func runSiteSafetyUnsafeURLs(ctx context.Context, out io.Writer, conn *sql.DB, opts siteSafetyUnsafeURLOptions) (siteSafetyUnsafeURLReport, error) {
	if out == nil {
		out = io.Discard
	}
	if conn == nil {
		return siteSafetyUnsafeURLReport{}, fmt.Errorf("db is required")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 1000
	}
	if opts.SampleSize < 0 {
		opts.SampleSize = 0
	}

	report := siteSafetyUnsafeURLReport{}
	var lastID int64
	for opts.Limit == 0 || report.ScannedActive < opts.Limit {
		remaining := int64(opts.BatchSize)
		if opts.Limit > 0 && opts.Limit-report.ScannedActive < remaining {
			remaining = opts.Limit - report.ScannedActive
		}
		if remaining <= 0 {
			break
		}
		rows, err := conn.QueryContext(ctx, `
			SELECT jetpack_monitor_site_id, blog_id, monitor_url
			  FROM jetpack_monitor_sites
			 WHERE monitor_active = 1
			   AND jetpack_monitor_site_id > ?
			 ORDER BY jetpack_monitor_site_id ASC
			 LIMIT ?`, lastID, remaining)
		if err != nil {
			return report, fmt.Errorf("query active monitor URLs: %w", err)
		}

		batchRows := 0
		var unsafeRows []siteSafetyUnsafeURLRow
		for rows.Next() {
			var row siteSafetyUnsafeURLRow
			if err := rows.Scan(&row.SiteID, &row.BlogID, &row.URL); err != nil {
				rows.Close()
				return report, fmt.Errorf("scan monitor URL row: %w", err)
			}
			batchRows++
			report.ScannedActive++
			lastID = row.SiteID
			reason := classifyUnsafeMonitorURL(row.URL)
			if reason == "" {
				continue
			}
			report.UnsafeRows++
			row.Reason = reason
			if len(report.Samples) < opts.SampleSize {
				report.Samples = append(report.Samples, row)
			}
			if opts.Execute {
				unsafeRows = append(unsafeRows, row)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return report, fmt.Errorf("iterate monitor URL rows: %w", err)
		}
		rows.Close()
		if opts.Execute && len(unsafeRows) > 0 {
			flagged, deactivated, err := flagAndDeactivateUnsafeMonitorURLs(ctx, conn, unsafeRows)
			if err != nil {
				return report, err
			}
			report.Flagged += flagged
			report.Deactivated += deactivated
		}
		if batchRows == 0 {
			break
		}
	}

	for _, row := range report.Samples {
		fmt.Fprintf(out, "WARN unsafe_url site_id=%d blog_id=%d reason=%q url=%q\n", row.SiteID, row.BlogID, row.Reason, row.URL)
	}
	fmt.Fprintf(out, "INFO scanned_active=%d unsafe=%d flagged=%d deactivated=%d\n", report.ScannedActive, report.UnsafeRows, report.Flagged, report.Deactivated)
	return report, nil
}

func runSiteSafetyReport(ctx context.Context, out io.Writer, conn *sql.DB, opts siteSafetyReportOptions, now time.Time) (siteSafetyReport, error) {
	if out == nil {
		out = io.Discard
	}
	if conn == nil {
		return siteSafetyReport{}, fmt.Errorf("db is required")
	}
	if opts.Output == "" {
		opts.Output = "text"
	}
	opts.Output = strings.TrimSpace(strings.ToLower(opts.Output))
	if opts.Status == "" {
		opts.Status = db.SiteSafetyStatusOpen
	}
	opts.Status = strings.TrimSpace(strings.ToLower(opts.Status))
	if opts.SampleSize < 0 {
		opts.SampleSize = 0
	}
	if opts.MaxOpen < -1 {
		opts.MaxOpen = -1
	}
	if !validSiteSafetyReportStatus(opts.Status) {
		return siteSafetyReport{}, fmt.Errorf("status must be one of: open, deactivated, ignored, resolved, all")
	}
	now = now.UTC()
	report := siteSafetyReport{
		Command:           "site-safety report",
		OK:                true,
		Status:            "green",
		GeneratedAt:       now.Format(time.RFC3339),
		StaleAfterSeconds: int64(opts.StaleAfter / time.Second),
	}
	if opts.MaxOpen >= 0 {
		maxOpen := opts.MaxOpen
		report.MaxOpen = &maxOpen
	}

	summaries, err := querySiteSafetySummaries(ctx, conn)
	if err != nil {
		return report, err
	}
	report.Summaries = summaries
	for _, summary := range summaries {
		report.Total += summary.Count
		if summary.Status == db.SiteSafetyStatusOpen {
			report.Open += summary.Count
		}
	}
	if opts.StaleAfter > 0 {
		staleOpen, err := querySiteSafetyStaleOpen(ctx, conn, now.Add(-opts.StaleAfter))
		if err != nil {
			return report, err
		}
		report.StaleOpen = staleOpen
	}
	samples, err := querySiteSafetySamples(ctx, conn, opts.Status, opts.SampleSize)
	if err != nil {
		return report, err
	}
	report.Samples = samples

	if opts.MaxOpen >= 0 && report.Open > opts.MaxOpen {
		report.Issues = append(report.Issues, fmt.Sprintf("open safety flags %d exceed max-open %d", report.Open, opts.MaxOpen))
	}
	if report.StaleOpen > 0 {
		report.Issues = append(report.Issues, fmt.Sprintf("%d open safety flag(s) are older than %s", report.StaleOpen, opts.StaleAfter))
	}
	if len(report.Issues) > 0 {
		report.OK = false
		report.Status = "red"
	}
	if err := renderSiteSafetyReport(out, report, opts.Output); err != nil {
		return report, err
	}
	return report, nil
}

func querySiteSafetySummaries(ctx context.Context, conn *sql.DB) ([]siteSafetyFlagSummary, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT flag_type, status, COUNT(*), MIN(first_seen_at), MAX(last_seen_at)
		  FROM jetpack_monitor_site_safety_flags
		 GROUP BY flag_type, status
		 ORDER BY flag_type, status`)
	if err != nil {
		return nil, fmt.Errorf("query site safety summary: %w", err)
	}
	defer rows.Close()

	var summaries []siteSafetyFlagSummary
	for rows.Next() {
		var summary siteSafetyFlagSummary
		var firstSeen sql.NullTime
		var lastSeen sql.NullTime
		if err := rows.Scan(&summary.FlagType, &summary.Status, &summary.Count, &firstSeen, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan site safety summary: %w", err)
		}
		summary.FirstSeenAt = formatNullTime(firstSeen)
		summary.LastSeenAt = formatNullTime(lastSeen)
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate site safety summary: %w", err)
	}
	return summaries, nil
}

func querySiteSafetyStaleOpen(ctx context.Context, conn *sql.DB, cutoff time.Time) (int64, error) {
	var count int64
	err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetpack_monitor_site_safety_flags
		 WHERE status = ?
		   AND last_seen_at < ?`, db.SiteSafetyStatusOpen, cutoff).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("query stale site safety flags: %w", err)
	}
	return count, nil
}

func querySiteSafetySamples(ctx context.Context, conn *sql.DB, status string, limit int) ([]siteSafetyFlagSample, error) {
	if limit <= 0 {
		return nil, nil
	}
	query := `
		SELECT monitor_site_id, blog_id, flag_type, status, reason, monitor_url, first_seen_at, last_seen_at
		  FROM jetpack_monitor_site_safety_flags`
	var args []any
	if status != "all" {
		query += "\n WHERE status = ?"
		args = append(args, status)
	}
	query += "\n ORDER BY last_seen_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query site safety samples: %w", err)
	}
	defer rows.Close()

	var samples []siteSafetyFlagSample
	for rows.Next() {
		var sample siteSafetyFlagSample
		var firstSeen sql.NullTime
		var lastSeen sql.NullTime
		if err := rows.Scan(&sample.SiteID, &sample.BlogID, &sample.FlagType, &sample.Status, &sample.Reason, &sample.URL, &firstSeen, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan site safety sample: %w", err)
		}
		sample.FirstSeenAt = formatNullTime(firstSeen)
		sample.LastSeenAt = formatNullTime(lastSeen)
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate site safety samples: %w", err)
	}
	return samples, nil
}

func renderSiteSafetyReport(out io.Writer, report siteSafetyReport, output string) error {
	switch output {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "text", "":
		renderSiteSafetyReportText(out, report)
		return nil
	default:
		return fmt.Errorf("unsupported output %q", output)
	}
}

func renderSiteSafetyReportText(out io.Writer, report siteSafetyReport) {
	level := "PASS"
	if !report.OK {
		level = "FAIL"
	}
	fmt.Fprintf(out, "%s site_safety_flags_report=%s total=%d open=%d stale_open=%d issues=%d\n", level, report.Status, report.Total, report.Open, report.StaleOpen, len(report.Issues))
	for _, issue := range report.Issues {
		fmt.Fprintf(out, "WARN issue=%q\n", issue)
	}
	for _, summary := range report.Summaries {
		fmt.Fprintf(out, "INFO flag_type=%s status=%s count=%d first_seen=%s last_seen=%s\n", summary.FlagType, summary.Status, summary.Count, summary.FirstSeenAt, summary.LastSeenAt)
	}
	for _, sample := range report.Samples {
		fmt.Fprintf(out, "WARN sample site_id=%d blog_id=%d flag_type=%s status=%s last_seen=%s reason=%q url=%q\n", sample.SiteID, sample.BlogID, sample.FlagType, sample.Status, sample.LastSeenAt, sample.Reason, sample.URL)
	}
}

func validSiteSafetyReportStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case db.SiteSafetyStatusOpen, db.SiteSafetyStatusDeactivated, db.SiteSafetyStatusIgnored, db.SiteSafetyStatusResolved, "all":
		return true
	default:
		return false
	}
}

func formatNullTime(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339)
}

func classifyUnsafeMonitorURL(rawURL string) string {
	if _, err := netguard.ParsePublicHTTPURL(rawURL, "monitor_url"); err != nil {
		return err.Error()
	}
	return ""
}

func flagAndDeactivateUnsafeMonitorURLs(ctx context.Context, conn *sql.DB, rows []siteSafetyUnsafeURLRow) (int64, int64, error) {
	const maxDeactivateBatch = 1000
	var flaggedTotal int64
	var deactivatedTotal int64
	for len(rows) > 0 {
		chunk := rows
		if len(chunk) > maxDeactivateBatch {
			chunk = chunk[:maxDeactivateBatch]
		}
		flagged, deactivated, err := flagAndDeactivateUnsafeMonitorURLBatch(ctx, conn, chunk)
		if err != nil {
			return flaggedTotal, deactivatedTotal, err
		}
		flaggedTotal += flagged
		deactivatedTotal += deactivated
		rows = rows[len(chunk):]
	}
	return flaggedTotal, deactivatedTotal, nil
}

func flagAndDeactivateUnsafeMonitorURLBatch(ctx context.Context, conn *sql.DB, rows []siteSafetyUnsafeURLRow) (int64, int64, error) {
	if len(rows) == 0 {
		return 0, 0, nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin unsafe monitor URL cleanup transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	deactivatedAt := time.Now().UTC()
	var flagged int64
	for _, row := range rows {
		if err := db.UpsertSiteSafetyFlag(ctx, tx, db.SiteSafetyFlag{
			BlogID:        row.BlogID,
			MonitorSiteID: row.SiteID,
			FlagType:      db.SiteSafetyFlagUnsafeMonitorURL,
			Reason:        row.Reason,
			MonitorURL:    row.URL,
			Status:        db.SiteSafetyStatusDeactivated,
			DeactivatedAt: &deactivatedAt,
		}); err != nil {
			return flagged, 0, err
		}
		flagged++
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(rows)), ",")
	args := make([]any, 0, len(rows))
	for _, row := range rows {
		args = append(args, row.SiteID)
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE jetpack_monitor_sites
		   SET monitor_active = 0
		 WHERE monitor_active = 1
		   AND jetpack_monitor_site_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return flagged, 0, fmt.Errorf("deactivate unsafe monitor URLs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return flagged, 0, fmt.Errorf("deactivate unsafe monitor URLs rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return flagged, 0, fmt.Errorf("commit unsafe monitor URL cleanup transaction: %w", err)
	}
	committed = true
	return flagged, n, nil
}

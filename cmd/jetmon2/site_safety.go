package main

import (
	"context"
	"database/sql"
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

func cmdSiteSafety(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 site-safety <unsafe-urls> [args]")
		os.Exit(2)
	}
	switch args[0] {
	case "unsafe-urls":
		cmdSiteSafetyUnsafeURLs(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown site-safety subcommand %q (want: unsafe-urls)\n", args[0])
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

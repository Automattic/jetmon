package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/retention"
)

// cmdCleanup implements `jetmon2 cleanup` — the operator-facing entry point to
// retention pruning. With no flags it applies the configured retention; flags
// override per-table day counts, restrict to one table, or dry-run.
func cmdCleanup(args []string) {
	fs := flag.NewFlagSet("cleanup", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "report what would be deleted without deleting")
	table := fs.String("table", "", "restrict to one table: check_history or audit_log")
	checkHistoryDays := fs.Int("check-history-days", -1, "override RETENTION_CHECK_HISTORY_DAYS (-1 = use config)")
	auditLogDays := fs.Int("audit-log-days", -1, "override RETENTION_AUDIT_LOG_DAYS (-1 = use config)")
	_ = fs.Parse(args)

	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		log.Fatalf("load config: %v", err)
	}
	cfg := config.Get()

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		log.Fatalf("db connect: %v", err)
	}
	audit.Init(db.DB())
	audit.SetMode(cfg.AuditLogModeDefault)

	opts := retention.Options{
		CheckHistoryDays: cfg.RetentionCheckHistoryDays,
		AuditLogDays:     cfg.RetentionAuditLogDays,
		DryRun:           *dryRun,
	}
	if *checkHistoryDays >= 0 {
		opts.CheckHistoryDays = *checkHistoryDays
	}
	if *auditLogDays >= 0 {
		opts.AuditLogDays = *auditLogDays
	}
	switch *table {
	case "":
		// both tables
	case "check_history":
		opts.AuditLogDays = 0
	case "audit_log":
		opts.CheckHistoryDays = 0
	default:
		fmt.Fprintln(os.Stderr, "--table must be 'check_history' or 'audit_log'")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	result, err := retention.Run(ctx, db.DB(), opts)
	printRetentionResult(os.Stdout, result, *dryRun)
	if !*dryRun {
		auditRetentionResult(ctx, result)
	}
	if err != nil {
		log.Fatalf("cleanup: %v", err)
	}
}

// runRetentionPass executes the configured retention once. Shared by the
// background goroutine; the CLI uses cmdCleanup so it can apply flag overrides.
func runRetentionPass(ctx context.Context, cfg *config.Config) {
	opts := retention.Options{
		CheckHistoryDays: cfg.RetentionCheckHistoryDays,
		AuditLogDays:     cfg.RetentionAuditLogDays,
	}
	if opts.CheckHistoryDays <= 0 && opts.AuditLogDays <= 0 {
		return // nothing enabled
	}
	result, err := retention.Run(ctx, db.DB(), opts)
	auditRetentionResult(ctx, result)
	for _, tr := range result.Tables {
		if tr.Skipped || tr.DeletedRows == 0 {
			continue
		}
		log.Printf("retention: %s pruned %d rows older than %dd in %s",
			tr.Table, tr.DeletedRows, tr.RetentionDays, tr.Duration.Round(time.Millisecond))
	}
	if err != nil {
		log.Printf("retention: cleanup failed: %v", err)
	}
}

// startRetentionBackground runs a daily retention pass at RETENTION_RUN_HOUR_UTC
// when background retention is enabled and at least one table has a retention
// window configured. It returns immediately; the loop exits on ctx cancel.
func startRetentionBackground(ctx context.Context, cfg *config.Config) {
	if !cfg.RetentionBackgroundEnable {
		return
	}
	if cfg.RetentionCheckHistoryDays <= 0 && cfg.RetentionAuditLogDays <= 0 {
		return // no table has retention configured; nothing to schedule
	}
	go func() {
		for {
			wait := durationUntilHourUTC(cfg.RetentionRunHourUTC, time.Now().UTC())
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
				runRetentionPass(ctx, config.Get())
			}
		}
	}()
}

// durationUntilHourUTC returns the time from now until the next occurrence of
// hour:00:00 UTC. If now is exactly at the hour it schedules the next day.
func durationUntilHourUTC(hour int, now time.Time) time.Duration {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.UTC)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

func auditRetentionResult(ctx context.Context, result retention.Result) {
	for _, tr := range result.Tables {
		if tr.Skipped {
			continue
		}
		meta, _ := json.Marshal(map[string]any{
			"table":          tr.Table,
			"deleted_rows":   tr.DeletedRows,
			"retention_days": tr.RetentionDays,
			"cutoff":         tr.Cutoff.Format(time.RFC3339),
			"duration_ms":    tr.Duration.Milliseconds(),
		})
		_ = audit.Log(ctx, audit.Entry{
			EventType: audit.EventRetentionCleanup,
			Source:    "local",
			Detail:    fmt.Sprintf("%s: deleted %d rows in %s", tr.Table, tr.DeletedRows, tr.Duration.Round(time.Millisecond)),
			Metadata:  meta,
		})
	}
}

func printRetentionResult(w *os.File, result retention.Result, dryRun bool) {
	verb := "deleted"
	if dryRun {
		verb = "would delete"
	}
	for _, tr := range result.Tables {
		if tr.Skipped {
			fmt.Fprintf(w, "%-22s skipped (%s)\n", tr.Table, tr.SkipReason)
			continue
		}
		fmt.Fprintf(w, "%-22s %s %d rows older than %dd (cutoff %s) in %s\n",
			tr.Table, verb, tr.DeletedRows, tr.RetentionDays,
			tr.Cutoff.Format(time.RFC3339), tr.Duration.Round(time.Millisecond))
	}
}

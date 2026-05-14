package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/eventstore"
)

const legacyStatusBootstrapSource = "rollout:legacy-status-bootstrap"

type productionDataAuditDeps struct {
	BuildLegacySiteTableAudit func(context.Context, int, int) (db.LegacySiteTableAudit, error)
}

type legacyStatusBootstrapDeps struct {
	BuildLegacySiteTableAudit func(context.Context, int, int) (db.LegacySiteTableAudit, error)
	ListLegacyNonRunningSites func(context.Context, int, int, int64, int) ([]db.LegacyNonRunningSite, error)
	OpenLegacyStatusEvent     func(context.Context, db.LegacyNonRunningSite) (bool, error)
}

type productionDataAuditEvaluation struct {
	Blockers []string
	Warnings []string
}

type legacyStatusBootstrapOptions struct {
	BucketMin int
	BucketMax int
	BatchSize int
	Execute   bool
	// Deprecated no-op retained so older runbooks do not break while endpoint
	// identity support rolls out.
	AllowDuplicateBlogIDs bool
}

type legacyStatusBootstrapSummary struct {
	Candidates int64
	Opened     int64
	Existing   int64
	DryRun     bool
}

func cmdRolloutProductionDataAudit(args []string) {
	fs := flag.NewFlagSet("rollout production-data-audit", flag.ExitOnError)
	bucketMin := fs.Int("bucket-min", -1, "inclusive bucket minimum (default pinned range or 0)")
	bucketMax := fs.Int("bucket-max", -1, "inclusive bucket maximum (default pinned range or BUCKET_TOTAL-1)")
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout production-data-audit [--bucket-min=N --bucket-max=N] [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	if err := runRolloutCommandOutput(os.Stdout, "rollout production-data-audit", outputFormat, func(out io.Writer) error {
		cfg, err := loadRolloutConfigAndDB(out)
		if err != nil {
			return err
		}
		deps := productionDataAuditDeps{
			BuildLegacySiteTableAudit: db.BuildLegacySiteTableAudit,
		}
		return runProductionDataAudit(context.Background(), out, cfg, *bucketMin, *bucketMax, deps)
	}); err != nil {
		exitRolloutCommandError(err, outputFormat)
	}
}

func cmdRolloutLegacyStatusBootstrap(args []string) {
	fs := flag.NewFlagSet("rollout legacy-status-bootstrap", flag.ExitOnError)
	bucketMin := fs.Int("bucket-min", -1, "inclusive bucket minimum (default pinned range or 0)")
	bucketMax := fs.Int("bucket-max", -1, "inclusive bucket maximum (default pinned range or BUCKET_TOTAL-1)")
	batchSize := fs.Int("batch-size", 1000, "rows to scan per bootstrap page")
	execute := fs.Bool("execute", false, "write missing v2 events; default is read-only dry-run")
	allowDuplicateBlogIDs := fs.Bool("allow-duplicate-blog-ids", false, "deprecated no-op; duplicate blog_id rows are endpoint-aware")
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout legacy-status-bootstrap [--bucket-min=N --bucket-max=N] [--batch-size=N] [--execute] [--allow-duplicate-blog-ids] [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	if err := runRolloutCommandOutput(os.Stdout, "rollout legacy-status-bootstrap", outputFormat, func(out io.Writer) error {
		cfg, err := loadRolloutConfigAndDB(out)
		if err != nil {
			return err
		}
		store := eventstore.New(db.DB())
		deps := legacyStatusBootstrapDeps{
			BuildLegacySiteTableAudit: db.BuildLegacySiteTableAudit,
			ListLegacyNonRunningSites: db.ListLegacyNonRunningSites,
			OpenLegacyStatusEvent: func(ctx context.Context, site db.LegacyNonRunningSite) (bool, error) {
				return openLegacyStatusEvent(ctx, store, site)
			},
		}
		opts := legacyStatusBootstrapOptions{
			BucketMin:             *bucketMin,
			BucketMax:             *bucketMax,
			BatchSize:             *batchSize,
			Execute:               *execute,
			AllowDuplicateBlogIDs: *allowDuplicateBlogIDs,
		}
		return runLegacyStatusBootstrap(context.Background(), out, cfg, opts, deps)
	}); err != nil {
		exitRolloutCommandError(err, outputFormat)
	}
}

func runProductionDataAudit(ctx context.Context, out io.Writer, cfg *config.Config, bucketMin, bucketMax int, deps productionDataAuditDeps) error {
	if cfg == nil {
		return errors.New("config is not loaded")
	}
	if deps.BuildLegacySiteTableAudit == nil {
		return errors.New("legacy table audit query is not configured")
	}
	minBucket, maxBucket, err := resolveRolloutBucketRange(cfg, bucketMin, bucketMax)
	if err != nil {
		return err
	}
	audit, err := deps.BuildLegacySiteTableAudit(ctx, minBucket, maxBucket)
	if err != nil {
		return err
	}
	eval := evaluateProductionDataAudit(cfg, audit)
	renderProductionDataAudit(out, cfg, audit, eval)
	if len(eval.Blockers) > 0 {
		return fmt.Errorf("production data audit found %d blocker(s)", len(eval.Blockers))
	}
	return nil
}

func evaluateProductionDataAudit(cfg *config.Config, audit db.LegacySiteTableAudit) productionDataAuditEvaluation {
	var eval productionDataAuditEvaluation
	if cfg != nil && audit.ObservedBucketMax != nil && *audit.ObservedBucketMax >= cfg.BucketTotal {
		eval.Blockers = append(eval.Blockers, fmt.Sprintf("observed bucket max %d is outside BUCKET_TOTAL=%d", *audit.ObservedBucketMax, cfg.BucketTotal))
	}
	if cfg != nil && audit.ObservedBucketMin != nil && audit.ObservedBucketMax != nil {
		observedTotal := *audit.ObservedBucketMax - *audit.ObservedBucketMin + 1
		if *audit.ObservedBucketMin == 0 && observedTotal != cfg.BucketTotal {
			eval.Warnings = append(eval.Warnings, fmt.Sprintf("observed legacy bucket space is 0-%d but BUCKET_TOTAL=%d", *audit.ObservedBucketMax, cfg.BucketTotal))
		}
	}
	if unexpected := unexpectedValueCounts(audit.MonitorActiveValues, map[int]bool{0: true, 1: true}); len(unexpected) > 0 {
		eval.Blockers = append(eval.Blockers, "monitor_active has unexpected value(s): "+unexpected)
	}
	if unexpected := unexpectedValueCounts(audit.SiteStatusValues, map[int]bool{0: true, 1: true, 2: true}); len(unexpected) > 0 {
		eval.Blockers = append(eval.Blockers, "site_status has unexpected value(s): "+unexpected)
	}
	if audit.ActiveDuplicateBlogs.Groups > 0 {
		eval.Warnings = append(eval.Warnings, fmt.Sprintf("active duplicate blog_id rows groups=%d rows=%d max_rows_per_blog=%d; v2 tracks endpoint runtime by jetpack_monitor_site_id", audit.ActiveDuplicateBlogs.Groups, audit.ActiveDuplicateBlogs.Rows, audit.ActiveDuplicateBlogs.MaxRowsPerBlog))
	}
	if audit.ActiveDuplicateBlogs.StatusConflicts > 0 {
		eval.Warnings = append(eval.Warnings, fmt.Sprintf("active duplicate blog_id rows have status conflicts groups=%d; inspect these before relying on the legacy site_status projection", audit.ActiveDuplicateBlogs.StatusConflicts))
	}
	if audit.ActiveNonRunningRows > 0 {
		eval.Warnings = append(eval.Warnings, fmt.Sprintf("active non-running legacy rows=%d; run legacy-status-bootstrap before projection-drift is a hard gate", audit.ActiveNonRunningRows))
	}
	if audit.ActiveMalformedURLRows > 0 {
		eval.Warnings = append(eval.Warnings, fmt.Sprintf("active malformed monitor_url rows=%d; clean up or explicitly accept these before cutover", audit.ActiveMalformedURLRows))
	}
	if audit.ActiveNullStatusChange > 0 {
		eval.Warnings = append(eval.Warnings, fmt.Sprintf("active rows with NULL last_status_change=%d; bootstrap will use current time for those rows", audit.ActiveNullStatusChange))
	}
	return eval
}

func renderProductionDataAudit(out io.Writer, cfg *config.Config, audit db.LegacySiteTableAudit, eval productionDataAuditEvaluation) {
	fmt.Fprintf(out, "INFO audit_range=%d-%d configured_bucket_total=%d\n", audit.BucketMin, audit.BucketMax, cfg.BucketTotal)
	if audit.ObservedBucketMin != nil && audit.ObservedBucketMax != nil {
		fmt.Fprintf(out, "INFO observed_bucket_space=%d-%d distinct=%d active_distinct=%d\n", *audit.ObservedBucketMin, *audit.ObservedBucketMax, audit.ObservedBucketDistinct, audit.ActiveBucketDistinct)
	} else {
		fmt.Fprintln(out, "WARN observed_bucket_space=empty")
	}
	fmt.Fprintf(out, "INFO legacy_rows_total=%d active=%d\n", audit.TotalRows, audit.ActiveRows)
	fmt.Fprintf(out, "INFO active_bucket_load distinct=%d min=%d max=%d avg=%.2f\n", audit.ActiveBucketLoad.Distinct, audit.ActiveBucketLoad.MinRows, audit.ActiveBucketLoad.MaxRows, audit.ActiveBucketLoad.AvgRows)
	fmt.Fprintf(out, "INFO monitor_active_values=%s\n", formatValueCounts(audit.MonitorActiveValues))
	fmt.Fprintf(out, "INFO site_status_values=%s\n", formatValueCounts(audit.SiteStatusValues))
	fmt.Fprintf(out, "INFO check_interval_values=%s\n", formatValueCounts(audit.CheckIntervalValues))
	fmt.Fprintf(out, "INFO active_nonrunning=%d active_null_last_status_change=%d\n", audit.ActiveNonRunningRows, audit.ActiveNullStatusChange)
	fmt.Fprintf(out, "INFO active_malformed_urls=%d url_near_column_limit=%d max_url_length=%d\n", audit.ActiveMalformedURLRows, audit.ActiveURLNearColumnLimit, audit.MaxURLLength)
	fmt.Fprintf(out, "INFO duplicate_blog_ids_all groups=%d rows=%d max_rows_per_blog=%d status_conflicts=%d\n", audit.DuplicateBlogs.Groups, audit.DuplicateBlogs.Rows, audit.DuplicateBlogs.MaxRowsPerBlog, audit.DuplicateBlogs.StatusConflicts)
	fmt.Fprintf(out, "INFO duplicate_blog_ids_active groups=%d rows=%d max_rows_per_blog=%d status_conflicts=%d\n", audit.ActiveDuplicateBlogs.Groups, audit.ActiveDuplicateBlogs.Rows, audit.ActiveDuplicateBlogs.MaxRowsPerBlog, audit.ActiveDuplicateBlogs.StatusConflicts)
	for _, warning := range eval.Warnings {
		fmt.Fprintf(out, "WARN production_data_audit=%q\n", warning)
	}
	for _, blocker := range eval.Blockers {
		fmt.Fprintf(out, "FAIL production_data_audit=%q\n", blocker)
	}
	if len(eval.Blockers) == 0 {
		fmt.Fprintln(out, "PASS production_data_audit=ready")
		return
	}
	fmt.Fprintln(out, "FAIL production_data_audit=blocked")
}

func runLegacyStatusBootstrap(ctx context.Context, out io.Writer, cfg *config.Config, opts legacyStatusBootstrapOptions, deps legacyStatusBootstrapDeps) error {
	if cfg == nil {
		return errors.New("config is not loaded")
	}
	if opts.BatchSize <= 0 {
		return errors.New("--batch-size must be > 0")
	}
	if deps.BuildLegacySiteTableAudit == nil || deps.ListLegacyNonRunningSites == nil {
		return errors.New("legacy status bootstrap queries are not configured")
	}
	minBucket, maxBucket, err := resolveRolloutBucketRange(cfg, opts.BucketMin, opts.BucketMax)
	if err != nil {
		return err
	}
	audit, err := deps.BuildLegacySiteTableAudit(ctx, minBucket, maxBucket)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "INFO bootstrap_range=%d-%d execute=%t\n", minBucket, maxBucket, opts.Execute)
	if audit.ActiveDuplicateBlogs.Groups > 0 {
		fmt.Fprintf(out, "WARN bootstrap_duplicate_blog_ids_endpoint_aware groups=%d rows=%d\n", audit.ActiveDuplicateBlogs.Groups, audit.ActiveDuplicateBlogs.Rows)
	}

	if !opts.Execute {
		fmt.Fprintf(out, "INFO bootstrap_candidates=%d\n", audit.ActiveNonRunningRows)
		fmt.Fprintln(out, "PASS legacy_status_bootstrap=dry_run")
		return nil
	}
	if deps.OpenLegacyStatusEvent == nil {
		return errors.New("legacy status event opener is not configured")
	}
	summary := legacyStatusBootstrapSummary{DryRun: false}
	var after int64
	for {
		page, err := deps.ListLegacyNonRunningSites(ctx, minBucket, maxBucket, after, opts.BatchSize)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for _, site := range page {
			summary.Candidates++
			opened, err := deps.OpenLegacyStatusEvent(ctx, site)
			if err != nil {
				return fmt.Errorf("bootstrap monitor_site_id=%d blog_id=%d: %w", site.MonitorSiteID, site.BlogID, err)
			}
			if opened {
				summary.Opened++
			} else {
				summary.Existing++
			}
			after = site.MonitorSiteID
		}
		if len(page) < opts.BatchSize {
			break
		}
	}
	fmt.Fprintf(out, "PASS legacy_status_bootstrap=complete candidates=%d opened=%d existing=%d\n", summary.Candidates, summary.Opened, summary.Existing)
	return nil
}

func openLegacyStatusEvent(ctx context.Context, store *eventstore.Store, site db.LegacyNonRunningSite) (bool, error) {
	if store == nil {
		return false, errors.New("event store is nil")
	}
	state, severity, ok := legacyStatusEventShape(site.SiteStatus)
	if !ok {
		return false, fmt.Errorf("unsupported legacy site_status=%d", site.SiteStatus)
	}
	meta, err := legacyStatusBootstrapMetadata(site)
	if err != nil {
		return false, err
	}
	endpointID := site.MonitorSiteID
	tx, err := store.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Open(ctx, eventstore.OpenInput{
		Identity:  eventstore.Identity{BlogID: site.BlogID, EndpointID: &endpointID, CheckType: "http"},
		Severity:  severity,
		State:     state,
		Source:    legacyStatusBootstrapSource,
		Metadata:  meta,
		StartedAt: site.LastStatusChange,
	})
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return res.Opened, nil
}

func legacyStatusEventShape(status int) (string, uint8, bool) {
	switch status {
	case 0:
		return eventstore.StateSeemsDown, eventstore.SeveritySeemsDown, true
	case 2:
		return eventstore.StateDown, eventstore.SeverityDown, true
	default:
		return "", 0, false
	}
}

func legacyStatusBootstrapMetadata(site db.LegacyNonRunningSite) (json.RawMessage, error) {
	meta := map[string]any{
		"source":             "jetpack_monitor_sites",
		"monitor_site_id":    site.MonitorSiteID,
		"bucket_no":          site.BucketNo,
		"legacy_site_status": site.SiteStatus,
		"bootstrapped_at":    time.Now().UTC().Format(time.RFC3339Nano),
	}
	if site.LastStatusChange != nil {
		meta["legacy_last_status_change"] = site.LastStatusChange.UTC().Format(time.RFC3339Nano)
	}
	return json.Marshal(meta)
}

func unexpectedValueCounts(counts []db.ValueCount, allowed map[int]bool) string {
	var values []string
	for _, row := range counts {
		if !allowed[row.Value] {
			values = append(values, fmt.Sprintf("%d(total=%d active=%d)", row.Value, row.Total, row.Active))
		}
	}
	return strings.Join(values, ",")
}

func formatValueCounts(counts []db.ValueCount) string {
	if len(counts) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(counts))
	for _, row := range counts {
		parts = append(parts, fmt.Sprintf("%d:total=%d,active=%d", row.Value, row.Total, row.Active))
	}
	return strings.Join(parts, ";")
}

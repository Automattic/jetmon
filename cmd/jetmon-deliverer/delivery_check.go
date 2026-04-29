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
	"text/tabwriter"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

const deliveryCheckDefaultSince = "15m"

type deliveryCheckOptions struct {
	HostOverride          string
	Since                 string
	Output                string
	MaxPending            int64
	MaxDue                int64
	MaxAbandoned          int64
	RequireRecentDelivery bool
}

type deliveryTableSummary struct {
	Kind           string `json:"kind"`
	Pending        int64  `json:"pending"`
	DueNow         int64  `json:"due_now"`
	FutureRetry    int64  `json:"future_retry"`
	DeliveredSince int64  `json:"delivered_since"`
	AbandonedSince int64  `json:"abandoned_since"`
}

type deliveryCheckReport struct {
	OK           bool                   `json:"ok"`
	Host         string                 `json:"host"`
	GeneratedAt  time.Time              `json:"generated_at"`
	Since        time.Time              `json:"since"`
	OwnerLevel   string                 `json:"owner_level,omitempty"`
	OwnerMessage string                 `json:"owner_message,omitempty"`
	Tables       []deliveryTableSummary `json:"tables"`
	Total        deliveryTableSummary   `json:"total"`
	Failures     []string               `json:"failures,omitempty"`
}

func parseDeliveryCheckOptions(args []string) (deliveryCheckOptions, error) {
	opts := deliveryCheckOptions{
		Since:        deliveryCheckDefaultSince,
		Output:       "text",
		MaxPending:   -1,
		MaxDue:       -1,
		MaxAbandoned: -1,
	}
	fs := flag.NewFlagSet("delivery-check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.HostOverride, "host", "", "host id to use for DELIVERY_OWNER_HOST context (default current hostname)")
	fs.StringVar(&opts.Since, "since", deliveryCheckDefaultSince, "report cutoff as duration like 15m or RFC3339 timestamp")
	fs.StringVar(&opts.Output, "output", "text", "output format: text or json")
	fs.Int64Var(&opts.MaxPending, "max-pending", -1, "fail when total pending deliveries exceed this count (-1 disables)")
	fs.Int64Var(&opts.MaxDue, "max-due", -1, "fail when total due deliveries exceed this count (-1 disables)")
	fs.Int64Var(&opts.MaxAbandoned, "max-abandoned", -1, "fail when abandoned deliveries since cutoff exceed this count (-1 disables)")
	fs.BoolVar(&opts.RequireRecentDelivery, "require-recent-delivery", false, "fail unless at least one delivery succeeded since cutoff")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	opts.Output = strings.ToLower(strings.TrimSpace(opts.Output))
	if opts.Output != "text" && opts.Output != "json" {
		return opts, fmt.Errorf("--output must be text or json")
	}
	if opts.MaxPending < -1 {
		return opts, fmt.Errorf("--max-pending must be >= 0, or -1 to disable")
	}
	if opts.MaxDue < -1 {
		return opts, fmt.Errorf("--max-due must be >= 0, or -1 to disable")
	}
	if opts.MaxAbandoned < -1 {
		return opts, fmt.Errorf("--max-abandoned must be >= 0, or -1 to disable")
	}
	return opts, nil
}

func cmdDeliveryCheck(args []string) {
	opts, err := parseDeliveryCheckOptions(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage: jetmon-deliverer delivery-check [--host=<host>] [--since=15m] [--max-pending=N] [--max-due=N] [--max-abandoned=N] [--require-recent-delivery] [--output=text|json]")
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}
	emitProgress := opts.Output != "json"

	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL config parse: %v\n", err)
		os.Exit(1)
	}
	if emitProgress {
		fmt.Println("PASS config parse")
	}

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL db connect: %v\n", err)
		os.Exit(1)
	}
	if emitProgress {
		fmt.Println("PASS db connect")
	}

	hostID := strings.TrimSpace(opts.HostOverride)
	if hostID == "" {
		hostID = db.Hostname()
	}
	report, err := buildDeliveryCheckReport(context.Background(), db.DB(), config.Get(), hostID, opts, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL delivery check: %v\n", err)
		os.Exit(1)
	}
	if err := renderDeliveryCheckReport(os.Stdout, report, opts.Output); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL render delivery check: %v\n", err)
		os.Exit(1)
	}
	if !report.OK {
		os.Exit(1)
	}
}

func buildDeliveryCheckReport(ctx context.Context, conn *sql.DB, cfg *config.Config, hostID string, opts deliveryCheckOptions, now time.Time) (deliveryCheckReport, error) {
	if conn == nil {
		return deliveryCheckReport{}, errors.New("database handle is nil")
	}
	now = now.UTC()
	cutoff, err := resolveDeliveryCheckCutoff(now, opts.Since)
	if err != nil {
		return deliveryCheckReport{}, err
	}
	hostID = strings.TrimSpace(hostID)

	report := deliveryCheckReport{
		Host:        hostID,
		GeneratedAt: now,
		Since:       cutoff,
		Total:       deliveryTableSummary{Kind: "total"},
	}
	if cfg != nil {
		report.OwnerLevel, report.OwnerMessage = deliveryOwnerStatus(cfg, hostID)
	}

	tables := []struct {
		kind string
		name string
	}{
		{kind: "webhook", name: "jetmon_webhook_deliveries"},
		{kind: "alert", name: "jetmon_alert_deliveries"},
	}
	for _, table := range tables {
		summary, err := queryDeliveryTableSummary(ctx, conn, table.kind, table.name, now, cutoff)
		if err != nil {
			return deliveryCheckReport{}, err
		}
		report.Tables = append(report.Tables, summary)
		report.Total.Pending += summary.Pending
		report.Total.DueNow += summary.DueNow
		report.Total.FutureRetry += summary.FutureRetry
		report.Total.DeliveredSince += summary.DeliveredSince
		report.Total.AbandonedSince += summary.AbandonedSince
	}

	report.Failures = evaluateDeliveryCheckFailures(report, opts)
	report.OK = len(report.Failures) == 0
	return report, nil
}

func resolveDeliveryCheckCutoff(now time.Time, raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("--since must not be empty")
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return time.Time{}, errors.New("--since duration must be > 0")
		}
		return now.Add(-d).UTC(), nil
	}
	cutoff, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("--since must be a duration or RFC3339 timestamp")
	}
	if cutoff.After(now) {
		return time.Time{}, errors.New("--since timestamp must not be in the future")
	}
	return cutoff.UTC(), nil
}

func queryDeliveryTableSummary(ctx context.Context, conn *sql.DB, kind, table string, now, cutoff time.Time) (deliveryTableSummary, error) {
	switch table {
	case "jetmon_webhook_deliveries", "jetmon_alert_deliveries":
	default:
		return deliveryTableSummary{}, fmt.Errorf("unsupported delivery table %q", table)
	}

	query := fmt.Sprintf(`
		SELECT
			COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0) AS pending,
			COALESCE(SUM(CASE WHEN status = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= ?) THEN 1 ELSE 0 END), 0) AS due_now,
			COALESCE(SUM(CASE WHEN status = 'pending' AND next_attempt_at > ? THEN 1 ELSE 0 END), 0) AS future_retry,
			COALESCE(SUM(CASE WHEN status = 'delivered' AND delivered_at >= ? THEN 1 ELSE 0 END), 0) AS delivered_since,
			COALESCE(SUM(CASE WHEN status = 'abandoned' AND COALESCE(last_attempt_at, created_at) >= ? THEN 1 ELSE 0 END), 0) AS abandoned_since
		  FROM %s`, table)

	var summary deliveryTableSummary
	summary.Kind = kind
	if err := conn.QueryRowContext(ctx, query, now, now, cutoff, cutoff).Scan(
		&summary.Pending,
		&summary.DueNow,
		&summary.FutureRetry,
		&summary.DeliveredSince,
		&summary.AbandonedSince,
	); err != nil {
		return deliveryTableSummary{}, fmt.Errorf("%s delivery summary: %w", kind, err)
	}
	return summary, nil
}

func evaluateDeliveryCheckFailures(report deliveryCheckReport, opts deliveryCheckOptions) []string {
	var failures []string
	if opts.MaxPending >= 0 && report.Total.Pending > opts.MaxPending {
		failures = append(failures, fmt.Sprintf("pending deliveries total=%d exceeds max-pending=%d", report.Total.Pending, opts.MaxPending))
	}
	if opts.MaxDue >= 0 && report.Total.DueNow > opts.MaxDue {
		failures = append(failures, fmt.Sprintf("due deliveries total=%d exceeds max-due=%d", report.Total.DueNow, opts.MaxDue))
	}
	if opts.MaxAbandoned >= 0 && report.Total.AbandonedSince > opts.MaxAbandoned {
		failures = append(failures, fmt.Sprintf("abandoned deliveries since %s total=%d exceeds max-abandoned=%d", report.Since.Format(time.RFC3339), report.Total.AbandonedSince, opts.MaxAbandoned))
	}
	if opts.RequireRecentDelivery && report.Total.DeliveredSince == 0 {
		failures = append(failures, fmt.Sprintf("no delivered rows since %s", report.Since.Format(time.RFC3339)))
	}
	return failures
}

func renderDeliveryCheckReport(out io.Writer, report deliveryCheckReport, output string) error {
	if output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	return renderDeliveryCheckText(out, report)
}

func renderDeliveryCheckText(out io.Writer, report deliveryCheckReport) error {
	fmt.Fprintf(out, "INFO deliverer_host=%q\n", report.Host)
	fmt.Fprintf(out, "INFO delivery_check_generated_at=%s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(out, "INFO delivery_check_since=%s\n", report.Since.Format(time.RFC3339))
	if report.OwnerMessage != "" {
		fmt.Fprintf(out, "%s %s\n", report.OwnerLevel, report.OwnerMessage)
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tPENDING\tDUE_NOW\tFUTURE_RETRY\tDELIVERED_SINCE\tABANDONED_SINCE")
	for _, summary := range report.Tables {
		writeDeliverySummaryRow(tw, summary)
	}
	writeDeliverySummaryRow(tw, report.Total)
	if err := tw.Flush(); err != nil {
		return err
	}

	if report.OK {
		fmt.Fprintln(out, "PASS delivery_check=ok")
		return nil
	}
	for _, failure := range report.Failures {
		fmt.Fprintf(out, "FAIL %s\n", failure)
	}
	return nil
}

func writeDeliverySummaryRow(out io.Writer, summary deliveryTableSummary) {
	fmt.Fprintf(
		out,
		"%s\t%d\t%d\t%d\t%d\t%d\n",
		summary.Kind,
		summary.Pending,
		summary.DueNow,
		summary.FutureRetry,
		summary.DeliveredSince,
		summary.AbandonedSince,
	)
}

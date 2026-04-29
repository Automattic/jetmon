package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

type pinnedRolloutCheckDeps struct {
	Hostname                       func() string
	HostRowExists                  func(context.Context, string) (bool, error)
	ListOverlappingHostRows        func(context.Context, int, int) ([]db.HostRow, error)
	CountActiveSitesForBucketRange func(context.Context, int, int) (int, error)
	CountLegacyProjectionDrift     func(context.Context, int, int) (int, error)
}

type rollbackCheckDeps = pinnedRolloutCheckDeps

type dynamicRolloutCheckDeps struct {
	Now                            func() time.Time
	GetAllHosts                    func() ([]db.HostRow, error)
	CountActiveSitesForBucketRange func(context.Context, int, int) (int, error)
	CountLegacyProjectionDrift     func(context.Context, int, int) (int, error)
}

type activityCheckDeps struct {
	Now                                     func() time.Time
	CountActiveSitesForBucketRange          func(context.Context, int, int) (int, error)
	CountRecentlyCheckedActiveSitesForRange func(context.Context, int, int, time.Time) (int, error)
}

type projectionDriftDeps struct {
	CountLegacyProjectionDrift func(context.Context, int, int) (int, error)
	ListLegacyProjectionDrift  func(context.Context, int, int, int) ([]db.ProjectionDriftRow, error)
}

type cutoverCheckDeps struct {
	Pinned     pinnedRolloutCheckDeps
	Activity   activityCheckDeps
	Projection projectionDriftDeps
	Status     func(int) (string, error)
}

func cmdRollout(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout <rehearsal-plan|static-plan-check|pinned-check|cutover-check|rollback-check|dynamic-check|activity-check|projection-drift> [args]")
		os.Exit(1)
	}

	switch args[0] {
	case "rehearsal-plan":
		cmdRolloutRehearsalPlan(args[1:])
	case "static-plan-check":
		cmdRolloutStaticPlanCheck(args[1:])
	case "pinned-check":
		cmdRolloutPinnedCheck(args[1:])
	case "cutover-check":
		cmdRolloutCutoverCheck(args[1:])
	case "rollback-check":
		cmdRolloutRollbackCheck(args[1:])
	case "dynamic-check":
		cmdRolloutDynamicCheck(args[1:])
	case "activity-check":
		cmdRolloutActivityCheck(args[1:])
	case "projection-drift":
		cmdRolloutProjectionDrift(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown rollout subcommand %q (want: rehearsal-plan, static-plan-check, pinned-check, cutover-check, rollback-check, dynamic-check, activity-check, projection-drift)\n", args[0])
		os.Exit(1)
	}
}

type rolloutJSONLine struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

type rolloutJSONReport struct {
	OK          bool              `json:"ok"`
	Command     string            `json:"command"`
	GeneratedAt time.Time         `json:"generated_at"`
	Lines       []rolloutJSONLine `json:"lines,omitempty"`
	Failures    []string          `json:"failures,omitempty"`
}

func rolloutOutputFlag(fs *flag.FlagSet) *string {
	return fs.String("output", "text", "output format: text or json")
}

func normalizeRolloutOutput(output string) (string, error) {
	output = strings.ToLower(strings.TrimSpace(output))
	if output == "" {
		output = "text"
	}
	if output != "text" && output != "json" {
		return "", errors.New("--output must be text or json")
	}
	return output, nil
}

func runRolloutCommandOutput(stdout io.Writer, command, output string, run func(io.Writer) error) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if output != "json" {
		return run(stdout)
	}

	var text bytes.Buffer
	err := run(&text)
	report := buildRolloutJSONReport(command, text.String(), err)
	if renderErr := renderRolloutJSONReport(stdout, report); renderErr != nil {
		return renderErr
	}
	return err
}

func buildRolloutJSONReport(command, text string, err error) rolloutJSONReport {
	report := rolloutJSONReport{
		OK:          err == nil,
		Command:     command,
		GeneratedAt: time.Now().UTC(),
		Lines:       parseRolloutOutputLines(text),
	}
	if err != nil {
		report.Failures = []string{err.Error()}
	}
	return report
}

func parseRolloutOutputLines(text string) []rolloutJSONLine {
	var lines []rolloutJSONLine
	for _, raw := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		level, message := parseRolloutOutputLine(raw)
		lines = append(lines, rolloutJSONLine{Level: level, Message: message})
	}
	return lines
}

func parseRolloutOutputLine(line string) (string, string) {
	if strings.HasPrefix(line, "## ") {
		return "section", strings.TrimSpace(strings.TrimPrefix(line, "## "))
	}
	for _, level := range []string{"PASS", "WARN", "INFO", "FAIL"} {
		prefix := level + " "
		if strings.HasPrefix(line, prefix) {
			return strings.ToLower(level), strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return "output", line
}

func renderRolloutJSONReport(out io.Writer, report rolloutJSONReport) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func exitRolloutCommandError(err error, output string) {
	if output != "json" {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
	}
	os.Exit(1)
}

func cmdRolloutRehearsalPlan(args []string) {
	fs := flag.NewFlagSet("rollout rehearsal-plan", flag.ExitOnError)
	file := fs.String("file", "", "CSV file with host,bucket_min,bucket_max rows")
	mode := fs.String("mode", "same-server", "rollout mode: same-server or fresh-server")
	host := fs.String("host", "", "host id that must appear in the static plan")
	runtimeHost := fs.String("runtime-host", "", "v2 host id for pinned/rollback checks (default --host)")
	bucketMin := fs.Int("bucket-min", -1, "expected bucket minimum for --host")
	bucketMax := fs.Int("bucket-max", -1, "expected bucket maximum for --host")
	bucketTotal := fs.Int("bucket-total", 0, "total bucket count (default BUCKET_TOTAL from config)")
	binary := fs.String("binary", "./jetmon2", "jetmon2 command path to print")
	service := fs.String("service", "jetmon2", "systemd service name to print for v2")
	since := fs.String("since", "15m", "activity cutoff to print for post-cutover checks")
	_ = fs.Parse(args)
	if fs.NArg() != 0 || strings.TrimSpace(*file) == "" {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout rehearsal-plan --file=<ranges.csv> --host=<host> --bucket-min=N --bucket-max=N [--mode=same-server|fresh-server] [--runtime-host=<v2-host>] [--bucket-total=N]")
		os.Exit(1)
	}

	resolvedBucketTotal := *bucketTotal
	if resolvedBucketTotal < 0 {
		fmt.Fprintln(os.Stderr, "FAIL bucket-total must be > 0")
		os.Exit(1)
	}
	if resolvedBucketTotal == 0 {
		configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
		if err := config.Load(configPath); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL config parse: %v\n", err)
			os.Exit(1)
		}
		resolvedBucketTotal = config.Get().BucketTotal
	}

	inputName := strings.TrimSpace(*file)
	if inputName == "-" {
		fmt.Fprintln(os.Stderr, "FAIL --file=- is not supported for rehearsal-plan; pass a reusable CSV path so the printed commands are repeatable")
		os.Exit(1)
	}
	f, err := os.Open(inputName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL open static bucket plan: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	opts := rolloutRehearsalPlanOptions{
		Mode:        *mode,
		PlanFile:    inputName,
		HostID:      *host,
		RuntimeHost: *runtimeHost,
		BucketMin:   *bucketMin,
		BucketMax:   *bucketMax,
		BucketTotal: resolvedBucketTotal,
		Binary:      *binary,
		Service:     *service,
		Since:       *since,
	}
	if err := runRolloutRehearsalPlan(os.Stdout, f, opts); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}
}

func cmdRolloutStaticPlanCheck(args []string) {
	fs := flag.NewFlagSet("rollout static-plan-check", flag.ExitOnError)
	file := fs.String("file", "", "CSV file with host,bucket_min,bucket_max rows (use - for stdin)")
	bucketTotal := fs.Int("bucket-total", 0, "total bucket count (default BUCKET_TOTAL from config)")
	host := fs.String("host", "", "optional host id that must appear in the plan")
	bucketMin := fs.Int("bucket-min", -1, "expected bucket minimum for --host")
	bucketMax := fs.Int("bucket-max", -1, "expected bucket maximum for --host")
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 || strings.TrimSpace(*file) == "" {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout static-plan-check --file=<ranges.csv> [--bucket-total=N] [--host=<host> --bucket-min=N --bucket-max=N] [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}
	assertion, err := staticPlanAssertionFromFlags(*host, *bucketMin, *bucketMax)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}

	if err := runRolloutCommandOutput(os.Stdout, "rollout static-plan-check", outputFormat, func(out io.Writer) error {
		resolvedBucketTotal := *bucketTotal
		if resolvedBucketTotal < 0 {
			return errors.New("bucket-total must be > 0")
		}
		if resolvedBucketTotal == 0 {
			configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
			if err := config.Load(configPath); err != nil {
				return fmt.Errorf("config parse: %w", err)
			}
			resolvedBucketTotal = config.Get().BucketTotal
		}

		inputName := strings.TrimSpace(*file)
		input := io.Reader(os.Stdin)
		var opened *os.File
		if inputName != "-" {
			f, err := os.Open(inputName)
			if err != nil {
				return fmt.Errorf("open static bucket plan: %w", err)
			}
			opened = f
			input = f
		}
		if opened != nil {
			defer opened.Close()
		}

		return runStaticPlanCheck(out, inputName, input, resolvedBucketTotal, assertion)
	}); err != nil {
		exitRolloutCommandError(err, outputFormat)
	}
}

func cmdRolloutPinnedCheck(args []string) {
	fs := flag.NewFlagSet("rollout pinned-check", flag.ExitOnError)
	host := fs.String("host", "", "host id to check (default current hostname)")
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout pinned-check [--host=<host_id>] [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	if err := runRolloutCommandOutput(os.Stdout, "rollout pinned-check", outputFormat, func(out io.Writer) error {
		configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
		if err := config.Load(configPath); err != nil {
			return fmt.Errorf("config parse: %w", err)
		}
		fmt.Fprintln(out, "PASS config parse")

		config.LoadDB()
		if err := db.ConnectWithRetry(3); err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		fmt.Fprintln(out, "PASS db connect")

		deps := pinnedRolloutCheckDeps{
			Hostname:                       db.Hostname,
			HostRowExists:                  db.HostRowExists,
			ListOverlappingHostRows:        db.ListHostRowsOverlappingBucketRange,
			CountActiveSitesForBucketRange: db.CountActiveSitesForBucketRange,
			CountLegacyProjectionDrift:     db.CountLegacyProjectionDrift,
		}
		return runPinnedRolloutCheck(context.Background(), out, config.Get(), *host, deps)
	}); err != nil {
		exitRolloutCommandError(err, outputFormat)
	}
}

func cmdRolloutCutoverCheck(args []string) {
	fs := flag.NewFlagSet("rollout cutover-check", flag.ExitOnError)
	host := fs.String("host", "", "host id to check (default current hostname)")
	bucketMin := fs.Int("bucket-min", -1, "inclusive bucket minimum (default pinned range)")
	bucketMax := fs.Int("bucket-max", -1, "inclusive bucket maximum (default pinned range)")
	since := fs.String("since", "15m", "activity cutoff as duration like 15m or RFC3339 timestamp")
	requireAll := fs.Bool("require-all", false, "fail unless every active site in range was checked since the cutoff")
	limit := fs.Int("limit", 100, "maximum projection drift rows to print")
	statusPort := fs.Int("status-port", -1, "dashboard port for status check (default DASHBOARD_PORT from config)")
	skipStatus := fs.Bool("skip-status", false, "skip dashboard status check")
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout cutover-check [--host=<host_id>] [--bucket-min=N --bucket-max=N] [--since=15m] [--require-all] [--limit=N] [--status-port=N] [--skip-status] [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	if err := runRolloutCommandOutput(os.Stdout, "rollout cutover-check", outputFormat, func(out io.Writer) error {
		configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
		if err := config.Load(configPath); err != nil {
			return fmt.Errorf("config parse: %w", err)
		}
		fmt.Fprintln(out, "PASS config parse")

		config.LoadDB()
		if err := db.ConnectWithRetry(3); err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		fmt.Fprintln(out, "PASS db connect")

		deps := cutoverCheckDeps{
			Pinned: pinnedRolloutCheckDeps{
				Hostname:                       db.Hostname,
				HostRowExists:                  db.HostRowExists,
				ListOverlappingHostRows:        db.ListHostRowsOverlappingBucketRange,
				CountActiveSitesForBucketRange: db.CountActiveSitesForBucketRange,
				CountLegacyProjectionDrift:     db.CountLegacyProjectionDrift,
			},
			Activity: activityCheckDeps{
				Now:                                     time.Now,
				CountActiveSitesForBucketRange:          db.CountActiveSitesForBucketRange,
				CountRecentlyCheckedActiveSitesForRange: db.CountRecentlyCheckedActiveSitesForBucketRange,
			},
			Projection: projectionDriftDeps{
				CountLegacyProjectionDrift: db.CountLegacyProjectionDrift,
				ListLegacyProjectionDrift:  db.ListLegacyProjectionDrift,
			},
			Status: dashboardStatus,
		}
		opts := cutoverCheckOptions{
			HostOverride: *host,
			BucketMin:    *bucketMin,
			BucketMax:    *bucketMax,
			Since:        *since,
			RequireAll:   *requireAll,
			Limit:        *limit,
			StatusPort:   *statusPort,
			SkipStatus:   *skipStatus,
		}
		return runCutoverCheck(context.Background(), out, config.Get(), opts, deps)
	}); err != nil {
		exitRolloutCommandError(err, outputFormat)
	}
}

func cmdRolloutRollbackCheck(args []string) {
	fs := flag.NewFlagSet("rollout rollback-check", flag.ExitOnError)
	host := fs.String("host", "", "v2 host id that must not own dynamic buckets (default current hostname)")
	bucketMin := fs.Int("bucket-min", -1, "inclusive rollback bucket minimum (default pinned range)")
	bucketMax := fs.Int("bucket-max", -1, "inclusive rollback bucket maximum (default pinned range)")
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout rollback-check [--host=<host_id>] [--bucket-min=N --bucket-max=N] [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	if err := runRolloutCommandOutput(os.Stdout, "rollout rollback-check", outputFormat, func(out io.Writer) error {
		configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
		if err := config.Load(configPath); err != nil {
			return fmt.Errorf("config parse: %w", err)
		}
		fmt.Fprintln(out, "PASS config parse")

		config.LoadDB()
		if err := db.ConnectWithRetry(3); err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		fmt.Fprintln(out, "PASS db connect")

		deps := rollbackCheckDeps{
			Hostname:                       db.Hostname,
			HostRowExists:                  db.HostRowExists,
			ListOverlappingHostRows:        db.ListHostRowsOverlappingBucketRange,
			CountActiveSitesForBucketRange: db.CountActiveSitesForBucketRange,
			CountLegacyProjectionDrift:     db.CountLegacyProjectionDrift,
		}
		return runRollbackCheck(context.Background(), out, config.Get(), *host, *bucketMin, *bucketMax, deps)
	}); err != nil {
		exitRolloutCommandError(err, outputFormat)
	}
}

func cmdRolloutDynamicCheck(args []string) {
	fs := flag.NewFlagSet("rollout dynamic-check", flag.ExitOnError)
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout dynamic-check [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	if err := runRolloutCommandOutput(os.Stdout, "rollout dynamic-check", outputFormat, func(out io.Writer) error {
		configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
		if err := config.Load(configPath); err != nil {
			return fmt.Errorf("config parse: %w", err)
		}
		fmt.Fprintln(out, "PASS config parse")

		config.LoadDB()
		if err := db.ConnectWithRetry(3); err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		fmt.Fprintln(out, "PASS db connect")

		deps := dynamicRolloutCheckDeps{
			Now:                            time.Now,
			GetAllHosts:                    db.GetAllHosts,
			CountActiveSitesForBucketRange: db.CountActiveSitesForBucketRange,
			CountLegacyProjectionDrift:     db.CountLegacyProjectionDrift,
		}
		return runDynamicRolloutCheck(context.Background(), out, config.Get(), deps)
	}); err != nil {
		exitRolloutCommandError(err, outputFormat)
	}
}

func cmdRolloutActivityCheck(args []string) {
	fs := flag.NewFlagSet("rollout activity-check", flag.ExitOnError)
	bucketMin := fs.Int("bucket-min", -1, "inclusive bucket minimum (default pinned range or 0)")
	bucketMax := fs.Int("bucket-max", -1, "inclusive bucket maximum (default pinned range or BUCKET_TOTAL-1)")
	since := fs.String("since", "15m", "activity cutoff as duration like 15m or RFC3339 timestamp")
	requireAll := fs.Bool("require-all", false, "fail unless every active site in range was checked since the cutoff")
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout activity-check [--bucket-min=N --bucket-max=N] [--since=15m] [--require-all] [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	if err := runRolloutCommandOutput(os.Stdout, "rollout activity-check", outputFormat, func(out io.Writer) error {
		configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
		if err := config.Load(configPath); err != nil {
			return fmt.Errorf("config parse: %w", err)
		}
		fmt.Fprintln(out, "PASS config parse")

		config.LoadDB()
		if err := db.ConnectWithRetry(3); err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		fmt.Fprintln(out, "PASS db connect")

		deps := activityCheckDeps{
			Now:                                     time.Now,
			CountActiveSitesForBucketRange:          db.CountActiveSitesForBucketRange,
			CountRecentlyCheckedActiveSitesForRange: db.CountRecentlyCheckedActiveSitesForBucketRange,
		}
		return runActivityCheck(context.Background(), out, config.Get(), *bucketMin, *bucketMax, *since, *requireAll, deps)
	}); err != nil {
		exitRolloutCommandError(err, outputFormat)
	}
}

func cmdRolloutProjectionDrift(args []string) {
	fs := flag.NewFlagSet("rollout projection-drift", flag.ExitOnError)
	bucketMin := fs.Int("bucket-min", -1, "inclusive bucket minimum (default pinned range or 0)")
	bucketMax := fs.Int("bucket-max", -1, "inclusive bucket maximum (default pinned range or BUCKET_TOTAL-1)")
	limit := fs.Int("limit", 50, "maximum drift rows to print")
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout projection-drift [--bucket-min=N --bucket-max=N] [--limit=N] [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	if err := runRolloutCommandOutput(os.Stdout, "rollout projection-drift", outputFormat, func(out io.Writer) error {
		configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
		if err := config.Load(configPath); err != nil {
			return fmt.Errorf("config parse: %w", err)
		}
		fmt.Fprintln(out, "PASS config parse")

		config.LoadDB()
		if err := db.ConnectWithRetry(3); err != nil {
			return fmt.Errorf("db connect: %w", err)
		}
		fmt.Fprintln(out, "PASS db connect")

		deps := projectionDriftDeps{
			CountLegacyProjectionDrift: db.CountLegacyProjectionDrift,
			ListLegacyProjectionDrift:  db.ListLegacyProjectionDrift,
		}
		return runProjectionDriftReport(context.Background(), out, config.Get(), *bucketMin, *bucketMax, *limit, deps)
	}); err != nil {
		exitRolloutCommandError(err, outputFormat)
	}
}

type staticBucketRange struct {
	HostID    string
	BucketMin int
	BucketMax int
}

type rolloutRehearsalPlanOptions struct {
	Mode        string
	PlanFile    string
	HostID      string
	RuntimeHost string
	BucketMin   int
	BucketMax   int
	BucketTotal int
	Binary      string
	Service     string
	Since       string
}

func runRolloutRehearsalPlan(out io.Writer, input io.Reader, opts rolloutRehearsalPlanOptions) error {
	if out == nil {
		out = io.Discard
	}
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	switch mode {
	case "", "same-server":
		mode = "same-server"
	case "fresh-server":
	default:
		return fmt.Errorf("--mode must be same-server or fresh-server, got %q", opts.Mode)
	}

	planFile := strings.TrimSpace(opts.PlanFile)
	if planFile == "" || planFile == "-" {
		return errors.New("--file must be a reusable CSV path")
	}
	hostID := strings.TrimSpace(opts.HostID)
	if hostID == "" {
		return errors.New("--host is required")
	}
	runtimeHost := strings.TrimSpace(opts.RuntimeHost)
	if runtimeHost == "" {
		runtimeHost = hostID
	}
	if opts.BucketMin < 0 || opts.BucketMax < 0 {
		return errors.New("--bucket-min and --bucket-max are required")
	}
	if opts.BucketMax < opts.BucketMin {
		return errors.New("--bucket-max must be >= --bucket-min")
	}
	if opts.BucketTotal <= 0 {
		return errors.New("BUCKET_TOTAL must be > 0")
	}
	binary := strings.TrimSpace(opts.Binary)
	if binary == "" {
		binary = "./jetmon2"
	}
	service := strings.TrimSpace(opts.Service)
	if service == "" {
		service = "jetmon2"
	}
	since := strings.TrimSpace(opts.Since)
	if since == "" {
		since = "15m"
	}

	ranges, err := parseStaticBucketPlanCSV(input)
	if err != nil {
		return err
	}
	if err := validateStaticBucketPlan(ranges, opts.BucketTotal); err != nil {
		return err
	}
	assertion := staticPlanAssertion{HostID: hostID, BucketMin: opts.BucketMin, BucketMax: opts.BucketMax}
	assertedRange, err := validateStaticPlanAssertion(ranges, assertion)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "# Jetmon v2 rollout rehearsal plan")
	fmt.Fprintf(out, "INFO mode=%s\n", mode)
	fmt.Fprintf(out, "INFO static_plan_file=%s ranges=%d\n", planFile, len(ranges))
	fmt.Fprintf(out, "INFO plan_host=%q runtime_host=%q range=%d-%d\n", assertedRange.HostID, runtimeHost, assertedRange.BucketMin, assertedRange.BucketMax)
	fmt.Fprintln(out)

	writeRolloutPlanSection(out, "1. Validate the copied static bucket plan",
		rolloutCommand(binary, "rollout", "static-plan-check", "--file", planFile, "--host", hostID, "--bucket-min", strconv.Itoa(opts.BucketMin), "--bucket-max", strconv.Itoa(opts.BucketMax)),
	)
	writeRolloutPlanSection(out, "2. Validate the staged v2 config and service unit",
		rolloutCommand(binary, "validate-config"),
		"systemd-analyze verify "+shellQuote("/etc/systemd/system/"+service+".service"),
	)
	writeRolloutPlanSection(out, "3. Run the pinned preflight before stopping v1",
		rolloutCommand(binary, "rollout", "pinned-check", "--host", runtimeHost),
	)

	if mode == "fresh-server" {
		writeRolloutPlanSection(out, "4. Cut over from the old v1 host to the fresh v2 host",
			"# Keep v2 stopped on the fresh server until the old v1 monitor process is stopped.",
			"# Stop v1 on "+shellQuote(hostID)+" with the documented production command.",
			"systemctl enable --now "+shellQuote(service),
		)
	} else {
		writeRolloutPlanSection(out, "4. Cut over the same server from v1 to v2",
			"# Stop the v1 service with the documented production command.",
			"systemctl enable --now "+shellQuote(service),
		)
	}

	rangeArgs := []string{"--bucket-min", strconv.Itoa(opts.BucketMin), "--bucket-max", strconv.Itoa(opts.BucketMax)}
	writeRolloutPlanSection(out, "5. Verify the v2 host after start",
		rolloutCommand(append([]string{binary, "rollout", "cutover-check", "--host", runtimeHost}, append(append([]string{}, rangeArgs...), "--since", since)...)...),
		"# After one full expected check round:",
		rolloutCommand(append([]string{binary, "rollout", "cutover-check", "--host", runtimeHost}, append(append([]string{}, rangeArgs...), "--since", since, "--require-all")...)...),
	)

	rollbackComment := "# Restart the original v1 service with its original BUCKET_NO_MIN/BUCKET_NO_MAX config."
	if mode == "fresh-server" {
		rollbackComment = "# Restart v1 on " + shellQuote(hostID) + " with its original BUCKET_NO_MIN/BUCKET_NO_MAX config."
	}
	writeRolloutPlanSection(out, "6. Rehearse the rollback path before the rollback window closes",
		"systemctl stop "+shellQuote(service),
		rolloutCommand(append([]string{binary, "rollout", "rollback-check", "--host", runtimeHost}, rangeArgs...)...),
		rollbackComment,
	)

	writeRolloutPlanSection(out, "7. Complete fleet-level pinned and dynamic checks after every host is on v2",
		rolloutCommand(append([]string{binary, "rollout", "cutover-check", "--host", runtimeHost}, append(append([]string{}, rangeArgs...), "--since", since, "--require-all")...)...),
		"# After PINNED_BUCKET_* is removed from every v2 monitor config and the fleet is restarted:",
		rolloutCommand(binary, "validate-config"),
		rolloutCommand(binary, "rollout", "dynamic-check"),
		rolloutCommand(binary, "rollout", "activity-check", "--since", since, "--require-all"),
		rolloutCommand(binary, "rollout", "projection-drift", "--limit", "100"),
	)
	return nil
}

type cutoverCheckOptions struct {
	HostOverride string
	BucketMin    int
	BucketMax    int
	Since        string
	RequireAll   bool
	Limit        int
	StatusPort   int
	SkipStatus   bool
}

func runCutoverCheck(ctx context.Context, out io.Writer, cfg *config.Config, opts cutoverCheckOptions, deps cutoverCheckDeps) error {
	if cfg == nil {
		return errors.New("config is not loaded")
	}
	if out == nil {
		out = io.Discard
	}

	writeRolloutPlanSection(out, "pinned preflight")
	if err := runPinnedRolloutCheck(ctx, out, cfg, opts.HostOverride, deps.Pinned); err != nil {
		return err
	}

	writeRolloutPlanSection(out, "activity check")
	if err := runActivityCheck(ctx, out, cfg, opts.BucketMin, opts.BucketMax, opts.Since, opts.RequireAll, deps.Activity); err != nil {
		return err
	}

	writeRolloutPlanSection(out, "dashboard status")
	if err := runCutoverStatusCheck(out, cfg, opts, deps); err != nil {
		return err
	}

	writeRolloutPlanSection(out, "projection drift")
	if err := runProjectionDriftReport(ctx, out, cfg, opts.BucketMin, opts.BucketMax, opts.Limit, deps.Projection); err != nil {
		return err
	}

	fmt.Fprintln(out, "cutover check passed")
	return nil
}

func runCutoverStatusCheck(out io.Writer, cfg *config.Config, opts cutoverCheckOptions, deps cutoverCheckDeps) error {
	if opts.SkipStatus {
		fmt.Fprintln(out, "INFO dashboard_status=skipped reason=operator")
		return nil
	}
	port := opts.StatusPort
	if port == -1 {
		port = cfg.DashboardPort
	}
	if port < 0 {
		return errors.New("status-port must be >= 0")
	}
	if port == 0 {
		fmt.Fprintln(out, "INFO dashboard_status=skipped dashboard_port=disabled")
		return nil
	}
	if deps.Status == nil {
		return errors.New("dashboard status checker is not configured")
	}
	body, err := deps.Status(port)
	if err != nil {
		return fmt.Errorf("dashboard status check on port %d: %w", port, err)
	}
	fmt.Fprintf(out, "PASS dashboard_status=http://localhost:%d/api/state\n", port)
	if body = strings.TrimSpace(body); body != "" {
		fmt.Fprintf(out, "INFO dashboard_state=%s\n", strings.Join(strings.Fields(body), " "))
	}
	return nil
}

func dashboardStatus(port int) (string, error) {
	return httpGet(fmt.Sprintf("http://localhost:%d/api/state", port))
}

func writeRolloutPlanSection(out io.Writer, title string, lines ...string) {
	fmt.Fprintf(out, "## %s\n", title)
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
	fmt.Fprintln(out)
}

func rolloutCommand(parts ...string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		switch r {
		case '_', '-', '.', '/', ':', '=', '+', ',', '@':
			continue
		default:
			return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
		}
	}
	return value
}

type staticPlanAssertion struct {
	HostID    string
	BucketMin int
	BucketMax int
}

func (a staticPlanAssertion) enabled() bool {
	return strings.TrimSpace(a.HostID) != ""
}

func (a staticPlanAssertion) checksRange() bool {
	return a.BucketMin >= 0 || a.BucketMax >= 0
}

func staticPlanAssertionFromFlags(host string, bucketMin, bucketMax int) (staticPlanAssertion, error) {
	host = strings.TrimSpace(host)
	if bucketMin < -1 || bucketMax < -1 {
		return staticPlanAssertion{}, errors.New("--bucket-min and --bucket-max must be >= 0")
	}
	if host == "" {
		if bucketMin >= 0 || bucketMax >= 0 {
			return staticPlanAssertion{}, errors.New("--host is required when --bucket-min or --bucket-max is set")
		}
		return staticPlanAssertion{}, nil
	}
	if (bucketMin >= 0) != (bucketMax >= 0) {
		return staticPlanAssertion{}, errors.New("--bucket-min and --bucket-max must be set together")
	}
	return staticPlanAssertion{
		HostID:    host,
		BucketMin: bucketMin,
		BucketMax: bucketMax,
	}, nil
}

func runStaticPlanCheck(out io.Writer, inputName string, input io.Reader, bucketTotal int, assertion staticPlanAssertion) error {
	ranges, err := parseStaticBucketPlanCSV(input)
	if err != nil {
		return err
	}
	if err := validateStaticBucketPlan(ranges, bucketTotal); err != nil {
		return err
	}
	assertedRange, err := validateStaticPlanAssertion(ranges, assertion)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "PASS static_plan_file=%s ranges=%d\n", inputName, len(ranges))
	fmt.Fprintf(out, "PASS static_bucket_coverage=0-%d hosts=%d\n", bucketTotal-1, len(ranges))
	if assertion.enabled() {
		fmt.Fprintf(out, "PASS static_plan_host=%q range=%d-%d\n", assertedRange.HostID, assertedRange.BucketMin, assertedRange.BucketMax)
	}
	fmt.Fprintln(out, "static rollout plan check passed")
	return nil
}

func parseStaticBucketPlanCSV(input io.Reader) ([]staticBucketRange, error) {
	reader := csv.NewReader(input)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read static bucket plan CSV: %w", err)
	}

	ranges := make([]staticBucketRange, 0, len(records))
	seenData := false
	for i, record := range records {
		row := i + 1
		if staticPlanRowIsBlank(record) || staticPlanRowIsComment(record) {
			continue
		}
		if !seenData && staticPlanRowIsHeader(record) {
			seenData = true
			continue
		}
		seenData = true
		if len(record) != 3 {
			return nil, fmt.Errorf("row %d: expected host,bucket_min,bucket_max", row)
		}
		hostID := strings.TrimSpace(record[0])
		if hostID == "" {
			return nil, fmt.Errorf("row %d: host is required", row)
		}
		minBucket, err := parseStaticPlanInt(row, "bucket_min", record[1])
		if err != nil {
			return nil, err
		}
		maxBucket, err := parseStaticPlanInt(row, "bucket_max", record[2])
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, staticBucketRange{
			HostID:    hostID,
			BucketMin: minBucket,
			BucketMax: maxBucket,
		})
	}
	if len(ranges) == 0 {
		return nil, errors.New("static bucket plan has no host ranges")
	}
	return ranges, nil
}

func staticPlanRowIsBlank(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

func staticPlanRowIsComment(record []string) bool {
	return len(record) > 0 && strings.HasPrefix(strings.TrimSpace(record[0]), "#")
}

func staticPlanRowIsHeader(record []string) bool {
	if len(record) != 3 {
		return false
	}
	return staticPlanHeaderField(record[0]) == "host" &&
		staticPlanHeaderField(record[1]) == "bucket_min" &&
		staticPlanHeaderField(record[2]) == "bucket_max"
}

func staticPlanHeaderField(field string) string {
	normalized := strings.ToLower(strings.TrimSpace(field))
	switch normalized {
	case "bucket_no_min", "min":
		return "bucket_min"
	case "bucket_no_max", "max":
		return "bucket_max"
	default:
		return normalized
	}
}

func parseStaticPlanInt(row int, field, value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("row %d: %s %q is not an integer", row, field, strings.TrimSpace(value))
	}
	return parsed, nil
}

func validateStaticBucketPlan(ranges []staticBucketRange, bucketTotal int) error {
	if bucketTotal <= 0 {
		return errors.New("BUCKET_TOTAL must be > 0")
	}
	if len(ranges) == 0 {
		return errors.New("static bucket plan has no host ranges")
	}

	seenHosts := make(map[string]struct{}, len(ranges))
	for _, rng := range ranges {
		if _, ok := seenHosts[rng.HostID]; ok {
			return fmt.Errorf("host %q is listed more than once", rng.HostID)
		}
		seenHosts[rng.HostID] = struct{}{}
		if rng.BucketMin < 0 || rng.BucketMax < rng.BucketMin || rng.BucketMax >= bucketTotal {
			return fmt.Errorf("host %q has invalid bucket range %d-%d for BUCKET_TOTAL=%d", rng.HostID, rng.BucketMin, rng.BucketMax, bucketTotal)
		}
	}

	sortedRanges := append([]staticBucketRange(nil), ranges...)
	sort.Slice(sortedRanges, func(i, j int) bool {
		if sortedRanges[i].BucketMin == sortedRanges[j].BucketMin {
			return sortedRanges[i].HostID < sortedRanges[j].HostID
		}
		return sortedRanges[i].BucketMin < sortedRanges[j].BucketMin
	})

	expectedMin := 0
	for _, rng := range sortedRanges {
		if rng.BucketMin > expectedMin {
			return fmt.Errorf("static bucket plan has gap %d-%d before host %q", expectedMin, rng.BucketMin-1, rng.HostID)
		}
		if rng.BucketMin < expectedMin {
			return fmt.Errorf("static bucket plan overlaps before host %q at bucket %d", rng.HostID, rng.BucketMin)
		}
		expectedMin = rng.BucketMax + 1
	}
	if expectedMin < bucketTotal {
		return fmt.Errorf("static bucket plan has trailing gap %d-%d", expectedMin, bucketTotal-1)
	}
	return nil
}

func validateStaticPlanAssertion(ranges []staticBucketRange, assertion staticPlanAssertion) (staticBucketRange, error) {
	if !assertion.enabled() {
		return staticBucketRange{}, nil
	}
	for _, rng := range ranges {
		if rng.HostID != assertion.HostID {
			continue
		}
		if assertion.checksRange() && (rng.BucketMin != assertion.BucketMin || rng.BucketMax != assertion.BucketMax) {
			return staticBucketRange{}, fmt.Errorf("host %q has bucket range %d-%d in plan, want %d-%d", assertion.HostID, rng.BucketMin, rng.BucketMax, assertion.BucketMin, assertion.BucketMax)
		}
		return rng, nil
	}
	return staticBucketRange{}, fmt.Errorf("host %q is not present in static bucket plan", assertion.HostID)
}

func runPinnedRolloutCheck(ctx context.Context, out io.Writer, cfg *config.Config, hostOverride string, deps pinnedRolloutCheckDeps) error {
	if cfg == nil {
		return errors.New("config is not loaded")
	}
	minBucket, maxBucket, ok := cfg.PinnedBucketRange()
	if !ok {
		return errors.New("pinned bucket range is not configured; set PINNED_BUCKET_MIN/PINNED_BUCKET_MAX or BUCKET_NO_MIN/BUCKET_NO_MAX")
	}
	fmt.Fprintf(out, "PASS pinned_range=%d-%d\n", minBucket, maxBucket)

	if !cfg.LegacyStatusProjectionEnable {
		return errors.New("LEGACY_STATUS_PROJECTION_ENABLE must be true during pinned v1-to-v2 rollout")
	}
	fmt.Fprintln(out, "PASS legacy_status_projection=enabled")

	if cfg.APIPort > 0 {
		fmt.Fprintf(out, "WARN api_port=%d; confirm the API/delivery ownership plan before monitor cutover\n", cfg.APIPort)
	} else {
		fmt.Fprintln(out, "PASS api_port=disabled")
	}

	hostID := strings.TrimSpace(hostOverride)
	if hostID == "" {
		if deps.Hostname == nil {
			return errors.New("hostname resolver is not configured")
		}
		hostID = strings.TrimSpace(deps.Hostname())
	}
	if hostID == "" {
		return errors.New("host id is empty")
	}

	if deps.HostRowExists == nil {
		return errors.New("host row checker is not configured")
	}
	hostRowExists, err := deps.HostRowExists(ctx, hostID)
	if err != nil {
		return fmt.Errorf("check jetmon_hosts row for %q: %w", hostID, err)
	}
	if hostRowExists {
		return fmt.Errorf("host %q still has a jetmon_hosts row; pinned hosts must not participate in dynamic bucket ownership", hostID)
	}
	fmt.Fprintf(out, "PASS jetmon_hosts row absent host=%q\n", hostID)

	if deps.ListOverlappingHostRows == nil {
		return errors.New("overlapping host row lister is not configured")
	}
	overlappingRows, err := deps.ListOverlappingHostRows(ctx, minBucket, maxBucket)
	if err != nil {
		return fmt.Errorf("list jetmon_hosts rows overlapping pinned range %d-%d: %w", minBucket, maxBucket, err)
	}
	if len(overlappingRows) > 0 {
		return fmt.Errorf("jetmon_hosts has %d row(s) overlapping pinned range %d-%d: %s", len(overlappingRows), minBucket, maxBucket, formatHostRows(overlappingRows))
	}
	fmt.Fprintln(out, "PASS jetmon_hosts overlap=0")

	if deps.CountActiveSitesForBucketRange == nil {
		return errors.New("active site counter is not configured")
	}
	activeSites, err := deps.CountActiveSitesForBucketRange(ctx, minBucket, maxBucket)
	if err != nil {
		return fmt.Errorf("count active sites in pinned range %d-%d: %w", minBucket, maxBucket, err)
	}
	fmt.Fprintf(out, "INFO active_sites_in_pinned_range=%d\n", activeSites)
	if activeSites == 0 {
		fmt.Fprintln(out, "WARN active_sites_in_pinned_range=0; confirm this v1 host range is intentionally empty")
	}

	if deps.CountLegacyProjectionDrift == nil {
		return errors.New("projection drift counter is not configured")
	}
	drift, err := deps.CountLegacyProjectionDrift(ctx, minBucket, maxBucket)
	if err != nil {
		return fmt.Errorf("count legacy projection drift in pinned range %d-%d: %w", minBucket, maxBucket, err)
	}
	if drift > 0 {
		return fmt.Errorf("legacy projection drift=%d in pinned range %d-%d", drift, minBucket, maxBucket)
	}
	fmt.Fprintln(out, "PASS legacy_projection_drift=0")
	fmt.Fprintln(out, "pinned rollout check passed")
	return nil
}

func runRollbackCheck(ctx context.Context, out io.Writer, cfg *config.Config, hostOverride string, bucketMin, bucketMax int, deps rollbackCheckDeps) error {
	if cfg == nil {
		return errors.New("config is not loaded")
	}
	minBucket, maxBucket, err := resolvePinnedOrExplicitRange(cfg, bucketMin, bucketMax, "rollback-check")
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "PASS rollback_range=%d-%d\n", minBucket, maxBucket)

	hostID := strings.TrimSpace(hostOverride)
	if hostID == "" {
		if deps.Hostname == nil {
			return errors.New("hostname resolver is not configured")
		}
		hostID = strings.TrimSpace(deps.Hostname())
	}
	if hostID == "" {
		return errors.New("host id is empty")
	}

	if deps.HostRowExists == nil {
		return errors.New("host row checker is not configured")
	}
	hostRowExists, err := deps.HostRowExists(ctx, hostID)
	if err != nil {
		return fmt.Errorf("check jetmon_hosts row for %q: %w", hostID, err)
	}
	if hostRowExists {
		return fmt.Errorf("host %q still has a jetmon_hosts row; stop v2 or clear dynamic ownership before restarting v1", hostID)
	}
	fmt.Fprintf(out, "PASS jetmon_hosts row absent host=%q\n", hostID)

	if deps.ListOverlappingHostRows == nil {
		return errors.New("overlapping host row lister is not configured")
	}
	overlappingRows, err := deps.ListOverlappingHostRows(ctx, minBucket, maxBucket)
	if err != nil {
		return fmt.Errorf("list jetmon_hosts rows overlapping rollback range %d-%d: %w", minBucket, maxBucket, err)
	}
	if len(overlappingRows) > 0 {
		return fmt.Errorf("jetmon_hosts has %d row(s) overlapping rollback range %d-%d: %s", len(overlappingRows), minBucket, maxBucket, formatHostRows(overlappingRows))
	}
	fmt.Fprintln(out, "PASS jetmon_hosts overlap=0")

	if deps.CountActiveSitesForBucketRange == nil {
		return errors.New("active site counter is not configured")
	}
	activeSites, err := deps.CountActiveSitesForBucketRange(ctx, minBucket, maxBucket)
	if err != nil {
		return fmt.Errorf("count active sites in rollback range %d-%d: %w", minBucket, maxBucket, err)
	}
	fmt.Fprintf(out, "INFO active_sites_in_rollback_range=%d\n", activeSites)
	if activeSites == 0 {
		fmt.Fprintln(out, "WARN active_sites_in_rollback_range=0; confirm this v1 host range is intentionally empty")
	}

	if deps.CountLegacyProjectionDrift == nil {
		return errors.New("projection drift counter is not configured")
	}
	drift, err := deps.CountLegacyProjectionDrift(ctx, minBucket, maxBucket)
	if err != nil {
		return fmt.Errorf("count legacy projection drift in rollback range %d-%d: %w", minBucket, maxBucket, err)
	}
	if drift > 0 {
		return fmt.Errorf("legacy projection drift=%d in rollback range %d-%d; fix drift before restarting v1 readers", drift, minBucket, maxBucket)
	}
	fmt.Fprintln(out, "PASS legacy_projection_drift=0")
	fmt.Fprintln(out, "rollback check passed")
	return nil
}

func runDynamicRolloutCheck(ctx context.Context, out io.Writer, cfg *config.Config, deps dynamicRolloutCheckDeps) error {
	if cfg == nil {
		return errors.New("config is not loaded")
	}
	if minBucket, maxBucket, ok := cfg.PinnedBucketRange(); ok {
		return fmt.Errorf("pinned bucket range %d-%d is still configured; remove PINNED_BUCKET_*/BUCKET_NO_* before dynamic ownership cutover", minBucket, maxBucket)
	}
	fmt.Fprintln(out, "PASS bucket_ownership=dynamic")

	if !cfg.LegacyStatusProjectionEnable {
		return errors.New("LEGACY_STATUS_PROJECTION_ENABLE must remain true until legacy readers have migrated")
	}
	fmt.Fprintln(out, "PASS legacy_status_projection=enabled")

	if deps.GetAllHosts == nil {
		return errors.New("host list query is not configured")
	}
	hosts, err := deps.GetAllHosts()
	if err != nil {
		return fmt.Errorf("query jetmon_hosts: %w", err)
	}
	fmt.Fprintf(out, "INFO jetmon_hosts_rows=%d\n", len(hosts))

	now := time.Now()
	if deps.Now != nil {
		now = deps.Now()
	}
	if err := validateDynamicBucketCoverage(hosts, cfg.BucketTotal, time.Duration(cfg.BucketHeartbeatGraceSec)*time.Second, now); err != nil {
		return err
	}
	fmt.Fprintf(out, "PASS dynamic_bucket_coverage=0-%d hosts=%d\n", cfg.BucketTotal-1, len(hosts))

	if deps.CountActiveSitesForBucketRange == nil {
		return errors.New("active site counter is not configured")
	}
	activeSites, err := deps.CountActiveSitesForBucketRange(ctx, 0, cfg.BucketTotal-1)
	if err != nil {
		return fmt.Errorf("count active sites in dynamic range 0-%d: %w", cfg.BucketTotal-1, err)
	}
	fmt.Fprintf(out, "INFO active_sites_dynamic_range=%d\n", activeSites)
	if activeSites == 0 {
		fmt.Fprintln(out, "WARN active_sites_dynamic_range=0; confirm the production site table is intentionally empty")
	}

	if deps.CountLegacyProjectionDrift == nil {
		return errors.New("projection drift counter is not configured")
	}
	drift, err := deps.CountLegacyProjectionDrift(ctx, 0, cfg.BucketTotal-1)
	if err != nil {
		return fmt.Errorf("count legacy projection drift in dynamic range 0-%d: %w", cfg.BucketTotal-1, err)
	}
	if drift > 0 {
		return fmt.Errorf("legacy projection drift=%d in dynamic range 0-%d", drift, cfg.BucketTotal-1)
	}
	fmt.Fprintln(out, "PASS legacy_projection_drift=0")
	fmt.Fprintln(out, "dynamic rollout check passed")
	return nil
}

func runActivityCheck(ctx context.Context, out io.Writer, cfg *config.Config, bucketMin, bucketMax int, since string, requireAll bool, deps activityCheckDeps) error {
	if cfg == nil {
		return errors.New("config is not loaded")
	}
	minBucket, maxBucket, err := resolveRolloutBucketRange(cfg, bucketMin, bucketMax)
	if err != nil {
		return err
	}
	now := time.Now()
	if deps.Now != nil {
		now = deps.Now()
	}
	cutoff, err := resolveActivityCutoff(now, since)
	if err != nil {
		return err
	}

	if deps.CountActiveSitesForBucketRange == nil {
		return errors.New("active site counter is not configured")
	}
	activeSites, err := deps.CountActiveSitesForBucketRange(ctx, minBucket, maxBucket)
	if err != nil {
		return fmt.Errorf("count active sites in activity range %d-%d: %w", minBucket, maxBucket, err)
	}

	if deps.CountRecentlyCheckedActiveSitesForRange == nil {
		return errors.New("recently checked active site counter is not configured")
	}
	checkedSince, err := deps.CountRecentlyCheckedActiveSitesForRange(ctx, minBucket, maxBucket, cutoff)
	if err != nil {
		return fmt.Errorf("count recently checked active sites in range %d-%d since %s: %w", minBucket, maxBucket, cutoff.Format(time.RFC3339), err)
	}

	fmt.Fprintf(out, "INFO activity_range=%d-%d\n", minBucket, maxBucket)
	fmt.Fprintf(out, "INFO activity_since=%s\n", cutoff.Format(time.RFC3339))
	fmt.Fprintf(out, "INFO active_sites=%d\n", activeSites)
	fmt.Fprintf(out, "INFO active_sites_checked_since=%d\n", checkedSince)

	if activeSites == 0 {
		fmt.Fprintln(out, "WARN active_sites=0; confirm this range is intentionally empty")
		fmt.Fprintln(out, "post-cutover activity check passed")
		return nil
	}
	if checkedSince == 0 {
		return fmt.Errorf("no active sites in range %d-%d have last_checked_at >= %s", minBucket, maxBucket, cutoff.Format(time.RFC3339))
	}
	if requireAll && checkedSince < activeSites {
		return fmt.Errorf("only %d/%d active sites in range %d-%d have last_checked_at >= %s", checkedSince, activeSites, minBucket, maxBucket, cutoff.Format(time.RFC3339))
	}
	if requireAll {
		fmt.Fprintln(out, "PASS rollout_activity=all_active_sites_checked")
	} else {
		fmt.Fprintln(out, "PASS rollout_activity=recent_checks_present")
	}
	fmt.Fprintln(out, "post-cutover activity check passed")
	return nil
}

func resolveActivityCutoff(now time.Time, since string) (time.Time, error) {
	since = strings.TrimSpace(since)
	if since == "" {
		return time.Time{}, errors.New("since must not be empty")
	}
	if d, err := time.ParseDuration(since); err == nil {
		if d <= 0 {
			return time.Time{}, errors.New("since duration must be > 0")
		}
		return now.Add(-d).UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339, since); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("since %q must be a duration like 15m or an RFC3339 timestamp", since)
}

func validateDynamicBucketCoverage(hosts []db.HostRow, bucketTotal int, heartbeatGrace time.Duration, now time.Time) error {
	if bucketTotal <= 0 {
		return errors.New("BUCKET_TOTAL must be > 0")
	}
	if heartbeatGrace <= 0 {
		return errors.New("BUCKET_HEARTBEAT_GRACE_SEC must be > 0")
	}
	if len(hosts) == 0 {
		return errors.New("jetmon_hosts has no rows; dynamic ownership is not established")
	}

	sortedHosts := append([]db.HostRow(nil), hosts...)
	sort.Slice(sortedHosts, func(i, j int) bool {
		if sortedHosts[i].BucketMin == sortedHosts[j].BucketMin {
			return sortedHosts[i].HostID < sortedHosts[j].HostID
		}
		return sortedHosts[i].BucketMin < sortedHosts[j].BucketMin
	})

	expectedMin := 0
	for _, host := range sortedHosts {
		if host.Status != "active" {
			return fmt.Errorf("host %q has status=%q; all dynamic ownership rows must be active", host.HostID, host.Status)
		}
		if age := now.Sub(host.LastHeartbeat); age > heartbeatGrace {
			return fmt.Errorf("host %q heartbeat is stale age=%s grace=%s", host.HostID, age.Round(time.Second), heartbeatGrace)
		}
		if host.BucketMin < 0 || host.BucketMax < host.BucketMin || host.BucketMax >= bucketTotal {
			return fmt.Errorf("host %q has invalid bucket range %d-%d for BUCKET_TOTAL=%d", host.HostID, host.BucketMin, host.BucketMax, bucketTotal)
		}
		if host.BucketMin > expectedMin {
			return fmt.Errorf("dynamic bucket coverage has gap %d-%d before host %q", expectedMin, host.BucketMin-1, host.HostID)
		}
		if host.BucketMin < expectedMin {
			return fmt.Errorf("dynamic bucket coverage overlaps before host %q at bucket %d", host.HostID, host.BucketMin)
		}
		expectedMin = host.BucketMax + 1
	}

	if expectedMin < bucketTotal {
		return fmt.Errorf("dynamic bucket coverage has trailing gap %d-%d", expectedMin, bucketTotal-1)
	}
	return nil
}

func formatHostRows(hosts []db.HostRow) string {
	parts := make([]string, 0, len(hosts))
	for _, host := range hosts {
		parts = append(parts, fmt.Sprintf("%s=%d-%d status=%s", host.HostID, host.BucketMin, host.BucketMax, host.Status))
	}
	return strings.Join(parts, ", ")
}

func runProjectionDriftReport(ctx context.Context, out io.Writer, cfg *config.Config, bucketMin, bucketMax, limit int, deps projectionDriftDeps) error {
	if cfg == nil {
		return errors.New("config is not loaded")
	}
	if limit <= 0 {
		return errors.New("limit must be > 0")
	}
	minBucket, maxBucket, err := resolveProjectionDriftRange(cfg, bucketMin, bucketMax)
	if err != nil {
		return err
	}

	if deps.CountLegacyProjectionDrift == nil {
		return errors.New("projection drift counter is not configured")
	}
	count, err := deps.CountLegacyProjectionDrift(ctx, minBucket, maxBucket)
	if err != nil {
		return fmt.Errorf("count legacy projection drift in range %d-%d: %w", minBucket, maxBucket, err)
	}
	fmt.Fprintf(out, "INFO projection_drift_range=%d-%d\n", minBucket, maxBucket)
	fmt.Fprintf(out, "INFO legacy_projection_drift=%d\n", count)

	if count == 0 {
		fmt.Fprintln(out, "PASS legacy_projection_drift=0")
		return nil
	}

	if deps.ListLegacyProjectionDrift == nil {
		return errors.New("projection drift lister is not configured")
	}
	rows, err := deps.ListLegacyProjectionDrift(ctx, minBucket, maxBucket, limit)
	if err != nil {
		return fmt.Errorf("list legacy projection drift in range %d-%d: %w", minBucket, maxBucket, err)
	}
	printProjectionDriftRows(out, rows)
	if count > len(rows) {
		fmt.Fprintf(out, "INFO projection_drift_rows_truncated=%d\n", count-len(rows))
	}
	return fmt.Errorf("legacy projection drift=%d in range %d-%d", count, minBucket, maxBucket)
}

func resolveProjectionDriftRange(cfg *config.Config, bucketMin, bucketMax int) (int, int, error) {
	return resolveRolloutBucketRange(cfg, bucketMin, bucketMax)
}

func resolvePinnedOrExplicitRange(cfg *config.Config, bucketMin, bucketMax int, command string) (int, int, error) {
	if bucketMin < -1 || bucketMax < -1 {
		return 0, 0, errors.New("bucket-min and bucket-max must be >= 0")
	}
	if bucketMin >= 0 || bucketMax >= 0 {
		return resolveExplicitRolloutBucketRange(cfg, bucketMin, bucketMax)
	}
	if minBucket, maxBucket, ok := cfg.PinnedBucketRange(); ok {
		return minBucket, maxBucket, nil
	}
	return 0, 0, fmt.Errorf("%s needs a pinned bucket config or explicit --bucket-min/--bucket-max", command)
}

func resolveRolloutBucketRange(cfg *config.Config, bucketMin, bucketMax int) (int, int, error) {
	if bucketMin < -1 || bucketMax < -1 {
		return 0, 0, errors.New("bucket-min and bucket-max must be >= 0")
	}
	if (bucketMin == -1) != (bucketMax == -1) {
		return 0, 0, errors.New("bucket-min and bucket-max must be set together")
	}
	if bucketMin >= 0 && bucketMax >= 0 {
		if bucketMax < bucketMin {
			return 0, 0, errors.New("bucket-max must be >= bucket-min")
		}
		if bucketMax >= cfg.BucketTotal {
			return 0, 0, fmt.Errorf("bucket-max must be < BUCKET_TOTAL (%d)", cfg.BucketTotal)
		}
		return bucketMin, bucketMax, nil
	}
	if minBucket, maxBucket, ok := cfg.PinnedBucketRange(); ok {
		return minBucket, maxBucket, nil
	}
	if cfg.BucketTotal <= 0 {
		return 0, 0, errors.New("BUCKET_TOTAL must be > 0")
	}
	return 0, cfg.BucketTotal - 1, nil
}

func resolveExplicitRolloutBucketRange(cfg *config.Config, bucketMin, bucketMax int) (int, int, error) {
	if bucketMin < 0 || bucketMax < 0 {
		return 0, 0, errors.New("bucket-min and bucket-max must be set together")
	}
	if bucketMax < bucketMin {
		return 0, 0, errors.New("bucket-max must be >= bucket-min")
	}
	if bucketMax >= cfg.BucketTotal {
		return 0, 0, fmt.Errorf("bucket-max must be < BUCKET_TOTAL (%d)", cfg.BucketTotal)
	}
	return bucketMin, bucketMax, nil
}

func printProjectionDriftRows(out io.Writer, rows []db.ProjectionDriftRow) {
	fmt.Fprintf(out, "%-12s %-8s %-11s %-9s %-10s %s\n",
		"BLOG_ID", "BUCKET", "SITE_STATUS", "EXPECTED", "EVENT_ID", "EVENT_STATE")
	for _, row := range rows {
		fmt.Fprintf(out, "%-12d %-8d %-11d %-9d %-10s %s\n",
			row.BlogID,
			row.BucketNo,
			row.SiteStatus,
			row.ExpectedStatus,
			formatOptionalInt(row.EventID),
			formatOptionalString(row.EventState),
		)
	}
}

func formatOptionalInt(v *int64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}

func formatOptionalString(v *string) string {
	if v == nil || *v == "" {
		return "-"
	}
	return *v
}

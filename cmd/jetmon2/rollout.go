package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

type rolloutStateReportDeps struct {
	Now                                     func() time.Time
	Hostname                                func() string
	GetAllHosts                             func() ([]db.HostRow, error)
	CountActiveSitesForBucketRange          func(context.Context, int, int) (int, error)
	CountRecentlyCheckedActiveSitesForRange func(context.Context, int, int, time.Time) (int, error)
	CountLegacyProjectionDrift              func(context.Context, int, int) (int, error)
}

type hostPreflightDeps struct {
	Pinned        pinnedRolloutCheckDeps
	SystemdVerify func(string) (string, error)
}

func cmdRollout(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout <guided|rehearsal-plan|host-preflight|static-plan-check|pinned-check|cutover-check|rollback-check|dynamic-check|activity-check|projection-drift|state-report> [args]")
		os.Exit(1)
	}

	switch args[0] {
	case "guided":
		cmdRolloutGuided(args[1:])
	case "rehearsal-plan":
		cmdRolloutRehearsalPlan(args[1:])
	case "host-preflight":
		cmdRolloutHostPreflight(args[1:])
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
	case "state-report":
		cmdRolloutStateReport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown rollout subcommand %q (want: guided, rehearsal-plan, host-preflight, static-plan-check, pinned-check, cutover-check, rollback-check, dynamic-check, activity-check, projection-drift, state-report)\n", args[0])
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

func cmdRolloutGuided(args []string) {
	fs := flag.NewFlagSet("rollout guided", flag.ExitOnError)
	file := fs.String("file", "", "CSV file with host,bucket_min,bucket_max rows")
	mode := fs.String("mode", "same-server", "rollout mode: same-server or fresh-server")
	host := fs.String("host", "", "v1 host id that must appear in the static plan")
	runtimeHost := fs.String("runtime-host", "", "v2 runtime host where guided rollout runs (default --host)")
	bucketMin := fs.Int("bucket-min", -1, "expected bucket minimum for --host")
	bucketMax := fs.Int("bucket-max", -1, "expected bucket maximum for --host")
	bucketTotal := fs.Int("bucket-total", 0, "total bucket count (default BUCKET_TOTAL from config)")
	service := fs.String("service", "jetmon2", "systemd service name for v2")
	systemdUnit := fs.String("systemd-unit", "", "systemd unit path to verify (default /etc/systemd/system/<service>.service)")
	since := fs.String("since", "15m", "activity cutoff for post-cutover checks")
	v1StopCommand := fs.String("v1-stop-command", "", "exact command to stop the v1 monitor for this range")
	v1StartCommand := fs.String("v1-start-command", "", "exact command to restart the v1 monitor during rollback")
	logDir := fs.String("log-dir", filepath.Join("logs", "rollout"), "directory for guided rollout transcripts and resume state")
	executeOperatorCommands := fs.Bool("execute-operator-commands", false, "execute v1/v2 stop/start commands after typed confirmation")
	dryRun := fs.Bool("dry-run", false, "validate inputs, log directory, and print the guided plan without running checks or commands")
	rollback := fs.Bool("rollback", false, "run the guided rollback path instead of the forward cutover path")
	skipSystemd := fs.Bool("skip-systemd", false, "skip systemd-analyze verify in host-preflight")
	skipStatus := fs.Bool("skip-status", false, "skip dashboard status check in cutover gates")
	statusPort := fs.Int("status-port", -1, "dashboard port for cutover status check (default DASHBOARD_PORT from config)")
	_ = fs.Parse(args)
	if fs.NArg() != 0 || strings.TrimSpace(*file) == "" {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout guided --file=<ranges.csv> --host=<v1-host> --bucket-min=N --bucket-max=N [--runtime-host=<v2-host>] [--bucket-total=N] [--mode=same-server|fresh-server] [--v1-stop-command=<cmd>] [--v1-start-command=<cmd>] [--log-dir=<dir>] [--execute-operator-commands] [--dry-run] [--rollback]")
		os.Exit(1)
	}

	opts := guidedRolloutOptions{
		Mode:                    *mode,
		PlanFile:                *file,
		HostID:                  *host,
		RuntimeHost:             *runtimeHost,
		BucketMin:               *bucketMin,
		BucketMax:               *bucketMax,
		BucketTotal:             *bucketTotal,
		Service:                 *service,
		SystemdUnit:             *systemdUnit,
		Since:                   *since,
		V1StopCmd:               *v1StopCommand,
		V1StartCmd:              *v1StartCommand,
		LogDir:                  *logDir,
		ExecuteOperatorCommands: *executeOperatorCommands,
		DryRun:                  *dryRun,
		Rollback:                *rollback,
		SkipSystemd:             *skipSystemd,
		SkipStatus:              *skipStatus,
		StatusPort:              *statusPort,
	}
	deps := defaultGuidedRolloutDeps()
	if err := runGuidedRollout(context.Background(), os.Stdout, os.Stdin, opts, deps); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}
}

func cmdRolloutRehearsalPlan(args []string) {
	fs := flag.NewFlagSet("rollout rehearsal-plan", flag.ExitOnError)
	file := fs.String("file", "", "CSV file with host,bucket_min,bucket_max rows")
	mode := fs.String("mode", "same-server", "rollout mode: same-server or fresh-server")
	host := fs.String("host", "", "host id that must appear in the static plan")
	runtimeHost := fs.String("runtime-host", "", "v2 runtime host where the runbook is executed (default --host)")
	bucketMin := fs.Int("bucket-min", -1, "expected bucket minimum for --host")
	bucketMax := fs.Int("bucket-max", -1, "expected bucket maximum for --host")
	bucketTotal := fs.Int("bucket-total", 0, "total bucket count (default BUCKET_TOTAL from config)")
	binary := fs.String("binary", "./jetmon2", "jetmon2 command path to print")
	service := fs.String("service", "jetmon2", "systemd service name to print for v2")
	systemdUnit := fs.String("systemd-unit", "", "systemd unit path to pass through to host-preflight (default /etc/systemd/system/<service>.service)")
	since := fs.String("since", "15m", "activity cutoff to print for post-cutover checks")
	v1StopCommand := fs.String("v1-stop-command", "", "exact command to stop the v1 monitor for this range")
	v1StartCommand := fs.String("v1-start-command", "", "exact command to restart the v1 monitor during rollback")
	_ = fs.Parse(args)
	if fs.NArg() != 0 || strings.TrimSpace(*file) == "" {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout rehearsal-plan --file=<ranges.csv> --host=<host> --bucket-min=N --bucket-max=N [--mode=same-server|fresh-server] [--runtime-host=<v2-host>] [--bucket-total=N] [--systemd-unit=<path>] [--v1-stop-command=<cmd>] [--v1-start-command=<cmd>]")
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
		SystemdUnit: *systemdUnit,
		Since:       *since,
		V1StopCmd:   *v1StopCommand,
		V1StartCmd:  *v1StartCommand,
	}
	if err := runRolloutRehearsalPlan(os.Stdout, f, opts); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}
}

func cmdRolloutHostPreflight(args []string) {
	fs := flag.NewFlagSet("rollout host-preflight", flag.ExitOnError)
	file := fs.String("file", "", "CSV file with host,bucket_min,bucket_max rows")
	host := fs.String("host", "", "v1 host id that must appear in the static plan")
	runtimeHost := fs.String("runtime-host", "", "v2 runtime host for pinned checks (default --host)")
	bucketMin := fs.Int("bucket-min", -1, "expected bucket minimum for --host")
	bucketMax := fs.Int("bucket-max", -1, "expected bucket maximum for --host")
	bucketTotal := fs.Int("bucket-total", 0, "total bucket count (default BUCKET_TOTAL from config)")
	service := fs.String("service", "jetmon2", "systemd service name for default --systemd-unit")
	systemdUnit := fs.String("systemd-unit", "", "systemd unit path to verify (default /etc/systemd/system/<service>.service)")
	skipSystemd := fs.Bool("skip-systemd", false, "skip systemd-analyze verify")
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 || strings.TrimSpace(*file) == "" {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout host-preflight --file=<ranges.csv> --host=<host> --bucket-min=N --bucket-max=N [--runtime-host=<v2-host>] [--bucket-total=N] [--service=jetmon2] [--systemd-unit=<path>] [--skip-systemd] [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	if err := runRolloutCommandOutput(os.Stdout, "rollout host-preflight", outputFormat, func(out io.Writer) error {
		inputName := strings.TrimSpace(*file)
		if inputName == "-" {
			return errors.New("--file=- is not supported for host-preflight; pass a reusable CSV path")
		}
		f, err := os.Open(inputName)
		if err != nil {
			return fmt.Errorf("open static bucket plan: %w", err)
		}
		defer f.Close()

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

		opts := hostPreflightOptions{
			PlanFile:    inputName,
			HostID:      *host,
			RuntimeHost: *runtimeHost,
			BucketMin:   *bucketMin,
			BucketMax:   *bucketMax,
			BucketTotal: *bucketTotal,
			Service:     *service,
			SystemdUnit: *systemdUnit,
			SkipSystemd: *skipSystemd,
		}
		deps := hostPreflightDeps{
			Pinned: pinnedRolloutCheckDeps{
				Hostname:                       db.Hostname,
				HostRowExists:                  db.HostRowExists,
				ListOverlappingHostRows:        db.ListHostRowsOverlappingBucketRange,
				CountActiveSitesForBucketRange: db.CountActiveSitesForBucketRange,
				CountLegacyProjectionDrift:     db.CountLegacyProjectionDrift,
			},
			SystemdVerify: systemdAnalyzeVerify,
		}
		return runHostPreflight(context.Background(), out, config.Get(), f, opts, deps)
	}); err != nil {
		exitRolloutCommandError(err, outputFormat)
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

func cmdRolloutStateReport(args []string) {
	fs := flag.NewFlagSet("rollout state-report", flag.ExitOnError)
	since := fs.String("since", "15m", "activity cutoff as duration like 15m or RFC3339 timestamp")
	output := rolloutOutputFlag(fs)
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout state-report [--since=15m] [--output=text|json]")
		os.Exit(1)
	}
	outputFormat, err := normalizeRolloutOutput(*output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		if outputFormat == "json" {
			report := rolloutStateReport{
				OK:          false,
				Command:     "rollout state-report",
				GeneratedAt: time.Now().UTC(),
				Issues:      []string{fmt.Sprintf("config parse: %v", err)},
			}
			_ = renderRolloutStateReport(os.Stdout, report, outputFormat)
		} else {
			fmt.Fprintf(os.Stderr, "FAIL config parse: %v\n", err)
		}
		os.Exit(1)
	}
	if outputFormat != "json" {
		fmt.Println("PASS config parse")
	}

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		if outputFormat == "json" {
			report := rolloutStateReport{
				OK:          false,
				Command:     "rollout state-report",
				GeneratedAt: time.Now().UTC(),
				Issues:      []string{fmt.Sprintf("db connect: %v", err)},
			}
			_ = renderRolloutStateReport(os.Stdout, report, outputFormat)
		} else {
			fmt.Fprintf(os.Stderr, "FAIL db connect: %v\n", err)
		}
		os.Exit(1)
	}
	if outputFormat != "json" {
		fmt.Println("PASS db connect")
	}

	deps := rolloutStateReportDeps{
		Now:                                     time.Now,
		Hostname:                                db.Hostname,
		GetAllHosts:                             db.GetAllHosts,
		CountActiveSitesForBucketRange:          db.CountActiveSitesForBucketRange,
		CountRecentlyCheckedActiveSitesForRange: db.CountRecentlyCheckedActiveSitesForBucketRange,
		CountLegacyProjectionDrift:              db.CountLegacyProjectionDrift,
	}
	report, err := buildRolloutStateReport(context.Background(), config.Get(), rolloutStateReportOptions{Since: *since}, deps)
	if err != nil {
		if outputFormat == "json" {
			failed := rolloutStateReport{
				OK:          false,
				Command:     "rollout state-report",
				GeneratedAt: time.Now().UTC(),
				Issues:      []string{err.Error()},
			}
			_ = renderRolloutStateReport(os.Stdout, failed, outputFormat)
		} else {
			fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		}
		os.Exit(1)
	}
	if err := renderRolloutStateReport(os.Stdout, report, outputFormat); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL render rollout state report: %v\n", err)
		os.Exit(1)
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
	SystemdUnit string
	Since       string
	V1StopCmd   string
	V1StartCmd  string
}

type guidedRolloutOptions struct {
	Mode                    string
	PlanFile                string
	HostID                  string
	RuntimeHost             string
	BucketMin               int
	BucketMax               int
	BucketTotal             int
	Service                 string
	SystemdUnit             string
	Since                   string
	V1StopCmd               string
	V1StartCmd              string
	LogDir                  string
	ExecuteOperatorCommands bool
	DryRun                  bool
	Rollback                bool
	SkipSystemd             bool
	SkipStatus              bool
	StatusPort              int
}

type guidedRolloutDeps struct {
	Now                func() time.Time
	ResolveBucketTotal func(context.Context) (int, error)
	StaticPlanCheck    func(context.Context, io.Writer, guidedRolloutOptions) error
	ValidateConfig     func(context.Context, io.Writer, guidedRolloutOptions) error
	HostPreflight      func(context.Context, io.Writer, guidedRolloutOptions) error
	CutoverCheck       func(context.Context, io.Writer, guidedRolloutOptions, bool) error
	RollbackCheck      func(context.Context, io.Writer, guidedRolloutOptions) error
	ExecCommand        func(context.Context, string) (string, error)
}

type guidedRolloutState struct {
	Version           int       `json:"version"`
	Mode              string    `json:"mode"`
	HostID            string    `json:"host_id"`
	RuntimeHost       string    `json:"runtime_host"`
	BucketMin         int       `json:"bucket_min"`
	BucketMax         int       `json:"bucket_max"`
	StartedAt         time.Time `json:"started_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	LastCompletedStep string    `json:"last_completed_step,omitempty"`
	CompletedSteps    []string  `json:"completed_steps,omitempty"`
	V1Stopped         bool      `json:"v1_stopped"`
	V1StateKnown      bool      `json:"v1_state_known,omitempty"`
	V2Started         bool      `json:"v2_started"`
	V2StateKnown      bool      `json:"v2_state_known,omitempty"`
}

type guidedRolloutSession struct {
	opts      guidedRolloutOptions
	deps      guidedRolloutDeps
	input     *bufio.Reader
	out       io.Writer
	state     guidedRolloutState
	statePath string
}

type guidedStep struct {
	ID      string
	Title   string
	Details string
	Run     func(context.Context, *guidedRolloutSession) error
}

const guidedRolloutStateVersion = 1

var (
	errGuidedStopped           = errors.New("guided rollout stopped by operator")
	errGuidedRollbackRequested = errors.New("guided rollback requested by operator")
	errGuidedForwardRolledBack = errors.New("forward rollout failed; guided rollback completed and range is back on v1")
)

type hostPreflightOptions struct {
	PlanFile    string
	HostID      string
	RuntimeHost string
	BucketMin   int
	BucketMax   int
	BucketTotal int
	Service     string
	SystemdUnit string
	SkipSystemd bool
}

func runHostPreflight(ctx context.Context, out io.Writer, cfg *config.Config, input io.Reader, opts hostPreflightOptions, deps hostPreflightDeps) error {
	if cfg == nil {
		return errors.New("config is not loaded")
	}
	if out == nil {
		out = io.Discard
	}
	if input == nil {
		return errors.New("static bucket plan input is required")
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
	bucketTotal := opts.BucketTotal
	if bucketTotal < 0 {
		return errors.New("bucket-total must be > 0")
	}
	if bucketTotal == 0 {
		bucketTotal = cfg.BucketTotal
	}
	if bucketTotal <= 0 {
		return errors.New("BUCKET_TOTAL must be > 0")
	}

	writeRolloutPlanSection(out, "static bucket plan")
	assertion := staticPlanAssertion{HostID: hostID, BucketMin: opts.BucketMin, BucketMax: opts.BucketMax}
	if err := runStaticPlanCheck(out, planFile, input, bucketTotal, assertion); err != nil {
		return err
	}

	writeRolloutPlanSection(out, "pinned pre-stop safety")
	configMin, configMax, ok := cfg.PinnedBucketRange()
	if !ok {
		return errors.New("pinned bucket range is not configured; set PINNED_BUCKET_MIN/PINNED_BUCKET_MAX or BUCKET_NO_MIN/BUCKET_NO_MAX")
	}
	if configMin != opts.BucketMin || configMax != opts.BucketMax {
		return fmt.Errorf("config pinned range %d-%d does not match requested bucket range %d-%d", configMin, configMax, opts.BucketMin, opts.BucketMax)
	}
	fmt.Fprintf(out, "PASS pinned_range_matches_request=%d-%d\n", configMin, configMax)
	if err := runPinnedRolloutCheck(ctx, out, cfg, runtimeHost, deps.Pinned); err != nil {
		return err
	}

	writeRolloutPlanSection(out, "systemd unit")
	if opts.SkipSystemd {
		fmt.Fprintln(out, "INFO systemd_verify=skipped reason=operator")
	} else {
		unitPath := hostPreflightSystemdUnit(opts)
		if deps.SystemdVerify == nil {
			return errors.New("systemd verifier is not configured")
		}
		verifyOutput, err := deps.SystemdVerify(unitPath)
		if err != nil {
			if trimmed := strings.TrimSpace(verifyOutput); trimmed != "" {
				return fmt.Errorf("systemd-analyze verify %s: %w: %s", unitPath, err, strings.Join(strings.Fields(trimmed), " "))
			}
			return fmt.Errorf("systemd-analyze verify %s: %w", unitPath, err)
		}
		fmt.Fprintf(out, "PASS systemd_unit=%s\n", unitPath)
		if verifyOutput = strings.TrimSpace(verifyOutput); verifyOutput != "" {
			fmt.Fprintf(out, "INFO systemd_verify=%s\n", strings.Join(strings.Fields(verifyOutput), " "))
		}
	}

	fmt.Fprintln(out, "PASS pre_stop_gate=ready")
	fmt.Fprintln(out, "host preflight passed")
	return nil
}

func hostPreflightSystemdUnit(opts hostPreflightOptions) string {
	if unit := strings.TrimSpace(opts.SystemdUnit); unit != "" {
		return unit
	}
	service := strings.TrimSpace(opts.Service)
	if service == "" {
		service = "jetmon2"
	}
	return "/etc/systemd/system/" + service + ".service"
}

func systemdAnalyzeVerify(unitPath string) (string, error) {
	out, err := exec.Command("systemd-analyze", "verify", unitPath).CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

func defaultGuidedRolloutDeps() guidedRolloutDeps {
	return guidedRolloutDeps{
		Now: time.Now,
		ResolveBucketTotal: func(context.Context) (int, error) {
			configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
			if err := config.Load(configPath); err != nil {
				return 0, fmt.Errorf("config parse: %w", err)
			}
			return config.Get().BucketTotal, nil
		},
		StaticPlanCheck: func(_ context.Context, out io.Writer, opts guidedRolloutOptions) error {
			f, err := os.Open(opts.PlanFile)
			if err != nil {
				return fmt.Errorf("open static bucket plan: %w", err)
			}
			defer f.Close()
			assertion := staticPlanAssertion{HostID: opts.HostID, BucketMin: opts.BucketMin, BucketMax: opts.BucketMax}
			return runStaticPlanCheck(out, opts.PlanFile, f, opts.BucketTotal, assertion)
		},
		ValidateConfig: func(_ context.Context, out io.Writer, _ guidedRolloutOptions) error {
			_, err := loadRolloutConfigAndDB(out)
			return err
		},
		HostPreflight: func(ctx context.Context, out io.Writer, opts guidedRolloutOptions) error {
			f, err := os.Open(opts.PlanFile)
			if err != nil {
				return fmt.Errorf("open static bucket plan: %w", err)
			}
			defer f.Close()

			cfg, err := loadRolloutConfigAndDB(out)
			if err != nil {
				return err
			}
			deps := hostPreflightDeps{
				Pinned: pinnedRolloutCheckDeps{
					Hostname:                       db.Hostname,
					HostRowExists:                  db.HostRowExists,
					ListOverlappingHostRows:        db.ListHostRowsOverlappingBucketRange,
					CountActiveSitesForBucketRange: db.CountActiveSitesForBucketRange,
					CountLegacyProjectionDrift:     db.CountLegacyProjectionDrift,
				},
				SystemdVerify: systemdAnalyzeVerify,
			}
			return runHostPreflight(ctx, out, cfg, f, hostPreflightOptions{
				PlanFile:    opts.PlanFile,
				HostID:      opts.HostID,
				RuntimeHost: opts.RuntimeHost,
				BucketMin:   opts.BucketMin,
				BucketMax:   opts.BucketMax,
				BucketTotal: opts.BucketTotal,
				Service:     opts.Service,
				SystemdUnit: opts.SystemdUnit,
				SkipSystemd: opts.SkipSystemd,
			}, deps)
		},
		CutoverCheck: func(ctx context.Context, out io.Writer, opts guidedRolloutOptions, requireAll bool) error {
			cfg, err := loadRolloutConfigAndDB(out)
			if err != nil {
				return err
			}
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
			return runCutoverCheck(ctx, out, cfg, cutoverCheckOptions{
				HostOverride: opts.RuntimeHost,
				BucketMin:    opts.BucketMin,
				BucketMax:    opts.BucketMax,
				Since:        opts.Since,
				RequireAll:   requireAll,
				Limit:        100,
				StatusPort:   opts.StatusPort,
				SkipStatus:   opts.SkipStatus,
			}, deps)
		},
		RollbackCheck: func(ctx context.Context, out io.Writer, opts guidedRolloutOptions) error {
			cfg, err := loadRolloutConfigAndDB(out)
			if err != nil {
				return err
			}
			deps := rollbackCheckDeps{
				Hostname:                       db.Hostname,
				HostRowExists:                  db.HostRowExists,
				ListOverlappingHostRows:        db.ListHostRowsOverlappingBucketRange,
				CountActiveSitesForBucketRange: db.CountActiveSitesForBucketRange,
				CountLegacyProjectionDrift:     db.CountLegacyProjectionDrift,
			}
			return runRollbackCheck(ctx, out, cfg, opts.RuntimeHost, opts.BucketMin, opts.BucketMax, deps)
		},
		ExecCommand: shellExecCommand,
	}
}

func loadRolloutConfigAndDB(out io.Writer) (*config.Config, error) {
	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		return nil, fmt.Errorf("config parse: %w", err)
	}
	fmt.Fprintln(out, "PASS config parse")

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		return nil, fmt.Errorf("db connect: %w", err)
	}
	fmt.Fprintln(out, "PASS db connect")
	return config.Get(), nil
}

func shellExecCommand(ctx context.Context, command string) (string, error) {
	out, err := exec.CommandContext(ctx, "/bin/sh", "-c", command).CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

func runGuidedRollout(ctx context.Context, out io.Writer, input io.Reader, opts guidedRolloutOptions, deps guidedRolloutDeps) error {
	if out == nil {
		out = io.Discard
	}
	if input == nil {
		input = strings.NewReader("")
	}
	deps = normalizeGuidedRolloutDeps(deps)
	normalized, err := normalizeGuidedRolloutOptions(opts)
	if err != nil {
		return err
	}
	opts = normalized

	transcript, logPath, statePath, closeLog, err := openGuidedRolloutTranscript(out, opts, deps.Now())
	if err != nil {
		return err
	}
	defer closeLog()

	session := &guidedRolloutSession{
		opts:      opts,
		deps:      deps,
		input:     bufio.NewReader(input),
		out:       transcript,
		statePath: statePath,
	}
	fmt.Fprintf(session.out, "PASS rollout_log_dir_writable=%s\n", opts.LogDir)
	fmt.Fprintf(session.out, "INFO rollout_log=%s\n", logPath)
	fmt.Fprintf(session.out, "INFO rollout_state=%s\n", statePath)

	if opts.BucketTotal == 0 {
		bucketTotal, err := deps.ResolveBucketTotal(ctx)
		if err != nil {
			return err
		}
		opts.BucketTotal = bucketTotal
		session.opts = opts
	}
	if err := validateGuidedRolloutOptions(opts); err != nil {
		return err
	}
	session.printRunOrigin()
	if opts.DryRun {
		session.state = newGuidedRolloutState(opts, deps.Now())
		return session.printDryRunPlan()
	}

	state, found, err := loadGuidedRolloutState(statePath)
	if err != nil {
		return err
	}
	if found {
		fmt.Fprintf(
			session.out,
			"INFO previous_state=found mode=%q host=%q runtime_host=%q range=%d-%d last_completed=%q v1_stopped=%t v1_state_known=%t v2_started=%t v2_state_known=%t\n",
			state.Mode,
			state.HostID,
			state.RuntimeHost,
			state.BucketMin,
			state.BucketMax,
			state.LastCompletedStep,
			state.V1Stopped,
			state.V1StateKnown,
			state.V2Started,
			state.V2StateKnown,
		)
		resume, err := session.chooseResumeState()
		if err != nil {
			return err
		}
		if resume {
			if err := validateGuidedStateMatchesOptions(state, opts); err != nil {
				return err
			}
			fmt.Fprintln(session.out, "INFO previous_state=resumed")
			session.state = state
		} else {
			fmt.Fprintln(session.out, "WARN previous_state=discarded reason=operator_start_over")
			session.state = newGuidedRolloutState(opts, deps.Now())
		}
	} else {
		session.state = newGuidedRolloutState(opts, deps.Now())
	}
	if err := session.saveState(); err != nil {
		return err
	}

	if opts.Rollback {
		return session.runRollback(ctx)
	}
	return session.runForward(ctx)
}

func normalizeGuidedRolloutDeps(deps guidedRolloutDeps) guidedRolloutDeps {
	defaults := defaultGuidedRolloutDeps()
	if deps.Now == nil {
		deps.Now = defaults.Now
	}
	if deps.ResolveBucketTotal == nil {
		deps.ResolveBucketTotal = defaults.ResolveBucketTotal
	}
	if deps.StaticPlanCheck == nil {
		deps.StaticPlanCheck = defaults.StaticPlanCheck
	}
	if deps.ValidateConfig == nil {
		deps.ValidateConfig = defaults.ValidateConfig
	}
	if deps.HostPreflight == nil {
		deps.HostPreflight = defaults.HostPreflight
	}
	if deps.CutoverCheck == nil {
		deps.CutoverCheck = defaults.CutoverCheck
	}
	if deps.RollbackCheck == nil {
		deps.RollbackCheck = defaults.RollbackCheck
	}
	if deps.ExecCommand == nil {
		deps.ExecCommand = defaults.ExecCommand
	}
	return deps
}

func normalizeGuidedRolloutOptions(opts guidedRolloutOptions) (guidedRolloutOptions, error) {
	opts.Mode = strings.ToLower(strings.TrimSpace(opts.Mode))
	if opts.Mode == "" {
		opts.Mode = "same-server"
	}
	if opts.Mode != "same-server" && opts.Mode != "fresh-server" {
		return opts, fmt.Errorf("--mode must be same-server or fresh-server, got %q", opts.Mode)
	}
	opts.PlanFile = strings.TrimSpace(opts.PlanFile)
	if opts.PlanFile == "" || opts.PlanFile == "-" {
		return opts, errors.New("--file must be a reusable CSV path")
	}
	opts.HostID = strings.TrimSpace(opts.HostID)
	if opts.HostID == "" {
		return opts, errors.New("--host is required")
	}
	opts.RuntimeHost = strings.TrimSpace(opts.RuntimeHost)
	if opts.RuntimeHost == "" {
		opts.RuntimeHost = opts.HostID
	}
	if opts.BucketMin < 0 || opts.BucketMax < 0 {
		return opts, errors.New("--bucket-min and --bucket-max are required")
	}
	if opts.BucketMax < opts.BucketMin {
		return opts, errors.New("--bucket-max must be >= --bucket-min")
	}
	if opts.BucketTotal < 0 {
		return opts, errors.New("bucket-total must be > 0")
	}
	opts.Service = strings.TrimSpace(opts.Service)
	if opts.Service == "" {
		opts.Service = "jetmon2"
	}
	opts.Since = strings.TrimSpace(opts.Since)
	if opts.Since == "" {
		opts.Since = "15m"
	}
	opts.LogDir = strings.TrimSpace(opts.LogDir)
	if opts.LogDir == "" {
		opts.LogDir = filepath.Join("logs", "rollout")
	}
	opts.V1StopCmd = strings.TrimSpace(opts.V1StopCmd)
	opts.V1StartCmd = strings.TrimSpace(opts.V1StartCmd)
	opts.SystemdUnit = strings.TrimSpace(opts.SystemdUnit)
	return opts, nil
}

func validateGuidedRolloutOptions(opts guidedRolloutOptions) error {
	if opts.BucketTotal <= 0 {
		return errors.New("BUCKET_TOTAL must be > 0")
	}
	if opts.BucketMax >= opts.BucketTotal {
		return fmt.Errorf("--bucket-max must be < BUCKET_TOTAL (%d)", opts.BucketTotal)
	}
	if !opts.Rollback && opts.V1StopCmd == "" {
		return errors.New("--v1-stop-command is required for guided forward rollout")
	}
	if opts.V1StartCmd == "" {
		return errors.New("--v1-start-command is required so guided rollback can return the range to v1")
	}
	return nil
}

func openGuidedRolloutTranscript(out io.Writer, opts guidedRolloutOptions, now time.Time) (io.Writer, string, string, func(), error) {
	if err := ensureGuidedLogDirWritable(opts.LogDir); err != nil {
		return nil, "", "", nil, fmt.Errorf("rollout log directory preflight failed before any rollout checks or service commands ran: %w", err)
	}
	logPath := guidedRolloutLogPathAt(opts, now)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("open rollout transcript: %w", err)
	}
	closeFn := func() {
		_ = f.Close()
	}
	return io.MultiWriter(out, f), logPath, guidedRolloutStatePath(opts), closeFn, nil
}

func ensureGuidedLogDirWritable(logDir string) error {
	if err := os.MkdirAll(logDir, 0750); err != nil {
		return fmt.Errorf("create rollout log directory %s: %w", logDir, err)
	}
	checkPath := filepath.Join(logDir, fmt.Sprintf(".write-check-%d", os.Getpid()))
	f, err := os.OpenFile(checkPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("rollout log directory %s is not writable: %w", logDir, err)
	}
	if _, err := f.WriteString("ok\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(checkPath)
		return fmt.Errorf("write rollout log directory %s: %w", logDir, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(checkPath)
		return fmt.Errorf("close rollout log directory check %s: %w", checkPath, err)
	}
	if err := os.Remove(checkPath); err != nil {
		return fmt.Errorf("remove rollout log directory check %s: %w", checkPath, err)
	}
	return nil
}

func guidedRolloutLogPathAt(opts guidedRolloutOptions, now time.Time) string {
	return filepath.Join(opts.LogDir, guidedRolloutStateBase(opts)+"-"+now.UTC().Format("20060102T150405.000000000Z")+".log")
}

func guidedRolloutStatePath(opts guidedRolloutOptions) string {
	return filepath.Join(opts.LogDir, guidedRolloutStateBase(opts)+".state.json")
}

func guidedRolloutStateBase(opts guidedRolloutOptions) string {
	return safeRolloutFilename(fmt.Sprintf("%s-%d-%d", opts.RuntimeHost, opts.BucketMin, opts.BucketMax))
}

func safeRolloutFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "rollout"
	}
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	safe := strings.Trim(b.String(), "._")
	if safe == "" {
		return "rollout"
	}
	return safe
}

func newGuidedRolloutState(opts guidedRolloutOptions, now time.Time) guidedRolloutState {
	return guidedRolloutState{
		Version:     guidedRolloutStateVersion,
		Mode:        opts.Mode,
		HostID:      opts.HostID,
		RuntimeHost: opts.RuntimeHost,
		BucketMin:   opts.BucketMin,
		BucketMax:   opts.BucketMax,
		StartedAt:   now.UTC(),
		UpdatedAt:   now.UTC(),
	}
}

func loadGuidedRolloutState(path string) (guidedRolloutState, bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return guidedRolloutState{}, false, nil
	}
	if err != nil {
		return guidedRolloutState{}, false, fmt.Errorf("open guided rollout state: %w", err)
	}
	defer f.Close()
	var state guidedRolloutState
	if err := json.NewDecoder(f).Decode(&state); err != nil {
		return guidedRolloutState{}, false, fmt.Errorf("decode guided rollout state: %w", err)
	}
	return state, true, nil
}

func validateGuidedStateMatchesOptions(state guidedRolloutState, opts guidedRolloutOptions) error {
	if state.Version != guidedRolloutStateVersion {
		return fmt.Errorf("guided rollout state version=%d is not supported", state.Version)
	}
	if state.Mode != opts.Mode || state.HostID != opts.HostID || state.RuntimeHost != opts.RuntimeHost || state.BucketMin != opts.BucketMin || state.BucketMax != opts.BucketMax {
		return fmt.Errorf(
			"guided rollout state does not match request (state mode=%q host=%q runtime_host=%q range=%d-%d; request mode=%q host=%q runtime_host=%q range=%d-%d)",
			state.Mode,
			state.HostID,
			state.RuntimeHost,
			state.BucketMin,
			state.BucketMax,
			opts.Mode,
			opts.HostID,
			opts.RuntimeHost,
			opts.BucketMin,
			opts.BucketMax,
		)
	}
	return nil
}

func (s *guidedRolloutSession) saveState() error {
	s.state.UpdatedAt = s.deps.Now().UTC()
	tmp := s.statePath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open guided rollout state for write: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.state); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write guided rollout state: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close guided rollout state: %w", err)
	}
	if err := os.Rename(tmp, s.statePath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace guided rollout state: %w", err)
	}
	return nil
}

func (s *guidedRolloutSession) markStepComplete(stepID string) error {
	if !s.stepCompleted(stepID) {
		s.state.CompletedSteps = append(s.state.CompletedSteps, stepID)
	}
	s.state.LastCompletedStep = stepID
	return s.saveState()
}

func (s *guidedRolloutSession) stepCompleted(stepID string) bool {
	for _, completed := range s.state.CompletedSteps {
		if completed == stepID {
			return true
		}
	}
	return false
}

func (s *guidedRolloutSession) printRunOrigin() {
	fmt.Fprintf(
		s.out,
		"INFO guided_run_origin=runtime_host mode=%q v1_host=%q runtime_host=%q\n",
		s.opts.Mode,
		s.opts.HostID,
		s.opts.RuntimeHost,
	)
	fmt.Fprintln(s.out, "INFO run_this_command_from=runtime_host note=\"run the guided command from the staged v2 runtime host with the jetmon2 service DB environment\"")
	if s.opts.Mode == "fresh-server" || s.opts.HostID != s.opts.RuntimeHost {
		fmt.Fprintf(
			s.out,
			"WARN remote_v1_access_required=true runtime_host=%q v1_host=%q note=\"runtime host must be able to SSH to the v1 host if v1 stop/start commands use ssh\"\n",
			s.opts.RuntimeHost,
			s.opts.HostID,
		)
		return
	}
	fmt.Fprintln(s.out, "INFO remote_v1_access_required=false reason=same_server")
}

func (s *guidedRolloutSession) printDryRunPlan() error {
	fmt.Fprintln(s.out, "INFO dry_run=true")
	fmt.Fprintln(s.out, "INFO no rollout checks or service commands will be executed")
	commandMode := "manual"
	if s.opts.ExecuteOperatorCommands {
		commandMode = "execute-after-confirmation"
	}
	fmt.Fprintf(s.out, "INFO operator_command_mode=%s\n", commandMode)
	if s.opts.Rollback {
		fmt.Fprintln(s.out, "INFO selected_path=rollback")
		s.printDryRunSteps("ROLLBACK", rollbackGuidedSteps())
		return nil
	}
	fmt.Fprintln(s.out, "INFO selected_path=forward")
	s.printDryRunSteps("FORWARD", forwardGuidedSteps())
	fmt.Fprintln(s.out, "INFO recovery_path=rollback")
	s.printDryRunSteps("ROLLBACK", rollbackGuidedSteps())
	return nil
}

func (s *guidedRolloutSession) printDryRunSteps(prefix string, steps []guidedStep) {
	for _, step := range steps {
		fmt.Fprintf(s.out, "PLAN path=%s step=%s title=%q\n", prefix, step.ID, step.Title)
		if command := s.dryRunCommandForStep(step.ID); command != "" {
			fmt.Fprintf(s.out, "PLAN path=%s step=%s command=%q\n", prefix, step.ID, command)
		}
		if phrase := s.dryRunConfirmationForStep(step.ID); phrase != "" {
			fmt.Fprintf(s.out, "PLAN path=%s step=%s typed_confirmation=%q\n", prefix, step.ID, phrase)
		}
		if manualPrompt := s.dryRunManualPromptForStep(step.ID); manualPrompt != "" && !s.opts.ExecuteOperatorCommands {
			fmt.Fprintf(s.out, "PLAN path=%s step=%s manual_checkpoint=%q\n", prefix, step.ID, manualPrompt)
		}
	}
}

func (s *guidedRolloutSession) dryRunCommandForStep(stepID string) string {
	switch stepID {
	case "stop-v1":
		return s.opts.V1StopCmd
	case "start-v2":
		return startSystemdServiceCommand(s.opts.Service)
	case "rollback-stop-v2":
		return stopSystemdServiceCommand(s.opts.Service)
	case "rollback-start-v1":
		return s.opts.V1StartCmd
	default:
		return ""
	}
}

func (s *guidedRolloutSession) dryRunConfirmationForStep(stepID string) string {
	switch stepID {
	case "stop-v1":
		return fmt.Sprintf("STOP %s %d-%d", s.opts.HostID, s.opts.BucketMin, s.opts.BucketMax)
	case "start-v2":
		return fmt.Sprintf("START V2 %s %d-%d", s.opts.RuntimeHost, s.opts.BucketMin, s.opts.BucketMax)
	case "cutover-require-all":
		return "READY"
	case "rollback-stop-v2":
		return fmt.Sprintf("STOP V2 %s %d-%d", s.opts.RuntimeHost, s.opts.BucketMin, s.opts.BucketMax)
	case "rollback-start-v1":
		return fmt.Sprintf("START V1 %s %d-%d", s.opts.HostID, s.opts.BucketMin, s.opts.BucketMax)
	default:
		return ""
	}
}

func (s *guidedRolloutSession) dryRunManualPromptForStep(stepID string) string {
	switch stepID {
	case "stop-v1":
		return "DONE after v1 is stopped and the process is no longer running"
	case "start-v2":
		return "DONE after v2 is started and logs show the pinned range"
	case "rollback-stop-v2":
		return "DONE after v2 is stopped and the process is no longer running"
	case "rollback-start-v1":
		return "DONE after v1 is started and checking the original bucket range"
	default:
		return ""
	}
}

func forwardGuidedSteps() []guidedStep {
	return []guidedStep{
		{
			ID:      "static-plan-check",
			Title:   "Validate the copied static bucket plan",
			Details: "This checks the CSV for full coverage and confirms this host owns the expected bucket range.",
			Run: func(ctx context.Context, s *guidedRolloutSession) error {
				return s.deps.StaticPlanCheck(ctx, s.out, s.opts)
			},
		},
		{
			ID:      "validate-config",
			Title:   "Validate the staged v2 config and DB connection",
			Details: "This loads the staged config using the service DB environment and confirms database connectivity.",
			Run: func(ctx context.Context, s *guidedRolloutSession) error {
				return s.deps.ValidateConfig(ctx, s.out, s.opts)
			},
		},
		{
			ID:      "host-preflight",
			Title:   "Run the pre-stop host gate",
			Details: "This bundles static plan, config, DB, pinned safety, projection drift, and systemd checks before v1 is stopped.",
			Run: func(ctx context.Context, s *guidedRolloutSession) error {
				return s.deps.HostPreflight(ctx, s.out, s.opts)
			},
		},
		{
			ID:      "stop-v1",
			Title:   "Stop v1 for this bucket range",
			Details: "This is the first destructive transition. v1 and v2 must not run against the same bucket range at the same time.",
			Run: func(ctx context.Context, s *guidedRolloutSession) error {
				phrase := fmt.Sprintf("STOP %s %d-%d", s.opts.HostID, s.opts.BucketMin, s.opts.BucketMax)
				if err := s.runOperatorCommand(
					ctx,
					"Stop v1",
					s.opts.V1StopCmd,
					"Stopping v1 prevents duplicate checks for this range.",
					phrase,
					"Type DONE after v1 is stopped and you have confirmed the process is no longer running.",
				); err != nil {
					return err
				}
				s.state.V1Stopped = true
				s.state.V1StateKnown = true
				return s.saveState()
			},
		},
		{
			ID:      "start-v2",
			Title:   "Start v2 for this bucket range",
			Details: "This starts the pinned v2 monitor after v1 has been confirmed stopped.",
			Run: func(ctx context.Context, s *guidedRolloutSession) error {
				phrase := fmt.Sprintf("START V2 %s %d-%d", s.opts.RuntimeHost, s.opts.BucketMin, s.opts.BucketMax)
				if err := s.runOperatorCommand(
					ctx,
					"Start v2",
					startSystemdServiceCommand(s.opts.Service),
					"Starting v2 begins production checks for this range.",
					phrase,
					"Type DONE after v2 is started and logs show the pinned range.",
				); err != nil {
					return err
				}
				s.state.V2Started = true
				s.state.V2StateKnown = true
				return s.saveState()
			},
		},
		{
			ID:      "cutover-smoke",
			Title:   "Run the immediate post-start smoke gate",
			Details: "This confirms startup and recent activity. Recent writes can still include v1 because the cutoff reaches back before cutover.",
			Run: func(ctx context.Context, s *guidedRolloutSession) error {
				return s.deps.CutoverCheck(ctx, s.out, s.opts, false)
			},
		},
		{
			ID:      "cutover-require-all",
			Title:   "Run the full-round v2 gate",
			Details: "Wait until one full expected v2 check round has elapsed, then require every active site in the range to have fresh activity.",
			Run: func(ctx context.Context, s *guidedRolloutSession) error {
				if err := s.confirmTyped("Run this only after one full expected v2 check round.", "READY"); err != nil {
					return err
				}
				return s.deps.CutoverCheck(ctx, s.out, s.opts, true)
			},
		},
	}
}

func rollbackGuidedSteps() []guidedStep {
	return []guidedStep{
		{
			ID:      "rollback-stop-v2",
			Title:   "Stop v2 before returning the range to v1",
			Details: "The range must not be checked by v1 and v2 at the same time.",
			Run: func(ctx context.Context, s *guidedRolloutSession) error {
				phrase := fmt.Sprintf("STOP V2 %s %d-%d", s.opts.RuntimeHost, s.opts.BucketMin, s.opts.BucketMax)
				if err := s.runOperatorCommand(
					ctx,
					"Stop v2",
					stopSystemdServiceCommand(s.opts.Service),
					"Stopping v2 is required before v1 can be restarted.",
					phrase,
					"Type DONE after v2 is stopped and you have confirmed the process is no longer running.",
				); err != nil {
					return err
				}
				s.state.V2Started = false
				s.state.V2StateKnown = true
				return s.saveState()
			},
		},
		{
			ID:      "rollback-check",
			Title:   "Run the rollback safety gate",
			Details: "This verifies the range has no dynamic ownership overlap and no legacy projection drift before v1 restarts.",
			Run: func(ctx context.Context, s *guidedRolloutSession) error {
				return s.deps.RollbackCheck(ctx, s.out, s.opts)
			},
		},
		{
			ID:      "rollback-start-v1",
			Title:   "Restart v1 for this bucket range",
			Details: "This returns the range to the original v1 monitor. Do not roll back schema migrations.",
			Run: func(ctx context.Context, s *guidedRolloutSession) error {
				phrase := fmt.Sprintf("START V1 %s %d-%d", s.opts.HostID, s.opts.BucketMin, s.opts.BucketMax)
				if err := s.runOperatorCommand(
					ctx,
					"Start v1",
					s.opts.V1StartCmd,
					"Restarting v1 returns production checks for this range to v1.",
					phrase,
					"Type DONE after v1 is started and checking the original bucket range.",
				); err != nil {
					return err
				}
				s.state.V1Stopped = false
				s.state.V1StateKnown = true
				return s.saveState()
			},
		},
	}
}

func (s *guidedRolloutSession) runForward(ctx context.Context) error {
	fmt.Fprintln(s.out, "# Guided Jetmon v2 rollout")
	fmt.Fprintf(s.out, "INFO mode=%s host=%q runtime_host=%q range=%d-%d bucket_total=%d\n", s.opts.Mode, s.opts.HostID, s.opts.RuntimeHost, s.opts.BucketMin, s.opts.BucketMax, s.opts.BucketTotal)
	for _, step := range forwardGuidedSteps() {
		if err := s.runStep(ctx, step); err != nil {
			if errors.Is(err, errGuidedRollbackRequested) {
				if rollbackErr := s.runRollback(ctx); rollbackErr != nil {
					return fmt.Errorf("rollback after failed forward step also failed: %w", rollbackErr)
				}
				fmt.Fprintln(s.out, "FAIL guided_rollout=rolled_back reason=operator_requested_after_failed_step")
				return errGuidedForwardRolledBack
			}
			return err
		}
	}
	fmt.Fprintln(s.out, "PASS guided_rollout=complete")
	fmt.Fprintln(s.out, "Host signoff is complete for this range. Keep the transcript and state file with the rollout record.")
	return nil
}

func (s *guidedRolloutSession) runRollback(ctx context.Context) error {
	fmt.Fprintln(s.out, "# Guided Jetmon v2 rollback")
	fmt.Fprintf(s.out, "INFO rollback_host=%q runtime_host=%q range=%d-%d\n", s.opts.HostID, s.opts.RuntimeHost, s.opts.BucketMin, s.opts.BucketMax)
	for _, step := range rollbackGuidedSteps() {
		if err := s.runStep(ctx, step); err != nil {
			return err
		}
	}
	fmt.Fprintln(s.out, "PASS guided_rollback=complete")
	fmt.Fprintln(s.out, "The range has been returned to v1. Leave v2 schema in place.")
	return nil
}

func (s *guidedRolloutSession) runStep(ctx context.Context, step guidedStep) error {
	if s.stepCompleted(step.ID) {
		fmt.Fprintf(s.out, "SKIP step=%s reason=completed_from_state\n", step.ID)
		return nil
	}
	if reason, ok := s.stepAlreadySatisfied(step.ID); ok {
		fmt.Fprintf(s.out, "SKIP step=%s reason=%s\n", step.ID, reason)
		return s.markStepComplete(step.ID)
	}
	for {
		writeRolloutPlanSection(s.out, step.Title)
		fmt.Fprintf(s.out, "INFO step=%s\n", step.ID)
		if step.Details != "" {
			fmt.Fprintf(s.out, "INFO %s\n", step.Details)
		}
		if !strings.Contains(step.ID, "stop-") && !strings.Contains(step.ID, "start-") && step.ID != "cutover-require-all" {
			proceed, err := s.confirmYes("Proceed with this step?")
			if err != nil {
				return err
			}
			if !proceed {
				return errGuidedStopped
			}
		}
		err := step.Run(ctx, s)
		if err == nil {
			if markErr := s.markStepComplete(step.ID); markErr != nil {
				return markErr
			}
			fmt.Fprintf(s.out, "PASS guided_step=%s\n", step.ID)
			return nil
		}
		action, actionErr := s.handleStepFailure(step, err)
		if actionErr != nil {
			return actionErr
		}
		switch action {
		case "retry":
			continue
		case "rollback":
			return errGuidedRollbackRequested
		default:
			return fmt.Errorf("step %s failed: %w", step.ID, err)
		}
	}
}

func (s *guidedRolloutSession) stepAlreadySatisfied(stepID string) (string, bool) {
	switch stepID {
	case "stop-v1":
		if s.state.V1StateKnown && s.state.V1Stopped {
			return "state_v1_already_stopped", true
		}
	case "start-v2":
		if s.state.V2StateKnown && s.state.V2Started {
			return "state_v2_already_started", true
		}
	case "rollback-stop-v2":
		if s.state.V2StateKnown && !s.state.V2Started {
			return "state_v2_already_stopped", true
		}
	case "rollback-start-v1":
		if s.state.V1StateKnown && !s.state.V1Stopped {
			return "state_v1_already_started", true
		}
	}
	return "", false
}

func (s *guidedRolloutSession) handleStepFailure(step guidedStep, stepErr error) (string, error) {
	fmt.Fprintf(s.out, "FAIL step=%s error=%v\n", step.ID, stepErr)
	if s.state.V2Started {
		fmt.Fprintln(s.out, "Options: [r] retry this step, [b] begin guided rollback, [s] stop here")
		for {
			answer, err := s.promptLine("Choose r, b, or s:")
			if err != nil {
				return "", err
			}
			switch strings.ToLower(answer) {
			case "r", "retry":
				return "retry", nil
			case "b", "rollback":
				return "rollback", nil
			case "s", "stop":
				return "stop", nil
			}
		}
	}
	fmt.Fprintln(s.out, "Options: [r] retry this step, [s] stop here")
	for {
		answer, err := s.promptLine("Choose r or s:")
		if err != nil {
			return "", err
		}
		switch strings.ToLower(answer) {
		case "r", "retry":
			return "retry", nil
		case "s", "stop":
			return "stop", nil
		}
	}
}

func (s *guidedRolloutSession) runOperatorCommand(ctx context.Context, label, command, confirmationPrompt, confirmationPhrase, manualDonePrompt string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("%s command is empty", label)
	}
	fmt.Fprintf(s.out, "COMMAND %s\n", command)
	if err := s.confirmTyped(confirmationPrompt, confirmationPhrase); err != nil {
		return err
	}
	if s.opts.ExecuteOperatorCommands {
		fmt.Fprintln(s.out, "INFO executing_operator_command=true")
		output, err := s.deps.ExecCommand(ctx, command)
		if strings.TrimSpace(output) != "" {
			fmt.Fprintf(s.out, "OUTPUT %s\n", strings.TrimSpace(output))
		}
		if err != nil {
			return fmt.Errorf("%s command failed: %w", label, err)
		}
		return nil
	}
	fmt.Fprintln(s.out, "INFO executing_operator_command=false")
	fmt.Fprintln(s.out, "Run the command above in the appropriate shell, then confirm completion.")
	return s.confirmTyped(manualDonePrompt, "DONE")
}

func (s *guidedRolloutSession) chooseResumeState() (bool, error) {
	fmt.Fprintln(s.out, "A previous guided rollout state exists for this host and range.")
	fmt.Fprintln(s.out, "Type RESUME to continue from it, or START OVER to discard it and create a new state.")
	for {
		answer, err := s.promptLine("Choose RESUME or START OVER:")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "resume":
			return true, nil
		case "start over", "start-over", "startover":
			return false, nil
		case "":
			fmt.Fprintln(s.out, "No default is selected when state exists; choose RESUME or START OVER.")
		default:
			fmt.Fprintln(s.out, "Please choose RESUME or START OVER.")
		}
	}
}

func (s *guidedRolloutSession) confirmYes(prompt string) (bool, error) {
	answer, err := s.promptLine(prompt + " [y/N]")
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, nil
	case "", "n", "no":
		return false, nil
	default:
		fmt.Fprintln(s.out, "Please answer y or n.")
		return s.confirmYes(prompt)
	}
}

func (s *guidedRolloutSession) confirmTyped(prompt, phrase string) error {
	fmt.Fprintln(s.out, prompt)
	answer, err := s.promptLine("Type " + phrase + " to continue:")
	if err != nil {
		return err
	}
	if answer != phrase {
		return fmt.Errorf("confirmation did not match %q", phrase)
	}
	return nil
}

func (s *guidedRolloutSession) promptLine(prompt string) (string, error) {
	fmt.Fprint(s.out, prompt+" ")
	line, err := s.input.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", fmt.Errorf("read operator input: %w", err)
	}
	answer := strings.TrimSpace(line)
	if answer != "" {
		fmt.Fprintf(s.out, "INPUT %s\n", answer)
	} else {
		fmt.Fprintln(s.out, "INPUT <empty>")
	}
	return answer, nil
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
	fmt.Fprintln(out, "# Run this runbook from the staged v2 runtime host, not from a separate orchestrator host.")
	fmt.Fprintln(out, "# Commands run from that runtime host unless the printed command explicitly targets another host.")
	fmt.Fprintln(out, "# Shell commands need the same DB_* environment used by the jetmon2 service.")
	if mode == "fresh-server" || hostID != runtimeHost {
		fmt.Fprintf(out, "# Fresh-server mode requires %s to have SSH access to old v1 host %s for any v1 stop/start commands that use ssh.\n", runtimeHost, hostID)
	}
	fmt.Fprintln(out)

	bucketTotalArgs := []string{"--bucket-total", strconv.Itoa(opts.BucketTotal)}
	staticPlanArgs := append([]string{binary, "rollout", "static-plan-check", "--file", planFile, "--host", hostID, "--bucket-min", strconv.Itoa(opts.BucketMin), "--bucket-max", strconv.Itoa(opts.BucketMax)}, bucketTotalArgs...)
	hostPreflightArgs := append([]string{binary, "rollout", "host-preflight", "--file", planFile, "--host", hostID, "--runtime-host", runtimeHost, "--bucket-min", strconv.Itoa(opts.BucketMin), "--bucket-max", strconv.Itoa(opts.BucketMax)}, bucketTotalArgs...)
	if systemdUnit := strings.TrimSpace(opts.SystemdUnit); systemdUnit != "" {
		hostPreflightArgs = append(hostPreflightArgs, "--systemd-unit", systemdUnit)
	} else {
		hostPreflightArgs = append(hostPreflightArgs, "--service", service)
	}

	writeRolloutPlanSection(out, "1. Validate the copied static bucket plan",
		rolloutCommand(staticPlanArgs...),
	)
	writeRolloutPlanSection(out, "2. Validate the staged v2 config with the service environment",
		rolloutCommand(binary, "validate-config"),
	)
	writeRolloutPlanSection(out, "3. Run the host preflight before stopping v1",
		rolloutCommand(hostPreflightArgs...),
	)

	v1StopLine := rolloutOperatorCommandOrComment(opts.V1StopCmd, "TODO: stop the v1 monitor for this bucket range with the documented production command.")
	if mode == "fresh-server" {
		writeRolloutPlanSection(out, "4. Cut over from the old v1 host to the fresh v2 host",
			"# HOLD: keep v2 stopped on the fresh server until the old v1 monitor process is stopped.",
			v1StopLine,
			"# HOLD: confirm v1 on "+shellQuote(hostID)+" is stopped before starting v2 on "+shellQuote(runtimeHost)+".",
			startSystemdServiceCommand(service),
		)
	} else {
		writeRolloutPlanSection(out, "4. Cut over the same server from v1 to v2",
			v1StopLine,
			"# HOLD: confirm v1 is stopped before starting v2.",
			startSystemdServiceCommand(service),
		)
	}

	rangeArgs := []string{"--bucket-min", strconv.Itoa(opts.BucketMin), "--bucket-max", strconv.Itoa(opts.BucketMax)}
	writeRolloutPlanSection(out, "5. Verify the v2 host after start",
		"# Immediate smoke gate: checks startup and recent activity; recent writes can still include v1.",
		rolloutCommand(append([]string{binary, "rollout", "cutover-check", "--host", runtimeHost}, append(append([]string{}, rangeArgs...), "--since", since)...)...),
		"# Strong gate after one full v2 check round:",
		rolloutCommand(append([]string{binary, "rollout", "cutover-check", "--host", runtimeHost}, append(append([]string{}, rangeArgs...), "--since", since, "--require-all")...)...),
	)

	rollbackComment := "# Restart the original v1 service with its original BUCKET_NO_MIN/BUCKET_NO_MAX config."
	if mode == "fresh-server" {
		rollbackComment = "# Restart v1 on " + shellQuote(hostID) + " with its original BUCKET_NO_MIN/BUCKET_NO_MAX config."
	}
	v1StartLine := rolloutOperatorCommandOrComment(opts.V1StartCmd, strings.TrimPrefix(rollbackComment, "# "))
	writeRolloutPlanSection(out, "6. Rehearse the rollback path before the rollback window closes",
		stopSystemdServiceCommand(service),
		"# HOLD: confirm the v2 process is stopped before restarting v1.",
		rolloutCommand(append([]string{binary, "rollout", "rollback-check", "--host", runtimeHost}, rangeArgs...)...),
		"# HOLD: do not restart v1 unless rollback-check passes.",
		v1StartLine,
		"# Do not roll back schema migrations.",
	)

	writeRolloutPlanSection(out, "7. Finish this host, then complete fleet-level checks",
		"# Host signoff before moving on or before the fleet dynamic cutover:",
		rolloutCommand(append([]string{binary, "rollout", "cutover-check", "--host", runtimeHost}, append(append([]string{}, rangeArgs...), "--since", since, "--require-all")...)...),
		"# After every host is on v2, remove PINNED_BUCKET_* from every monitor config and restart the fleet:",
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

type rolloutStateReportOptions struct {
	Since string
}

type rolloutStateReport struct {
	OK                  bool                       `json:"ok"`
	Command             string                     `json:"command"`
	GeneratedAt         time.Time                  `json:"generated_at"`
	Host                string                     `json:"host,omitempty"`
	Ownership           rolloutStateOwnership      `json:"ownership"`
	BucketCoverage      rolloutStateBucketCoverage `json:"bucket_coverage"`
	Activity            rolloutStateActivity       `json:"activity"`
	ProjectionDrift     rolloutStateDrift          `json:"projection_drift"`
	DeliveryOwner       rolloutStateDeliveryOwner  `json:"delivery_owner"`
	SuggestedNextAction string                     `json:"suggested_next_action,omitempty"`
	Issues              []string                   `json:"issues,omitempty"`
}

type rolloutStateOwnership struct {
	Mode      string `json:"mode"`
	BucketMin int    `json:"bucket_min"`
	BucketMax int    `json:"bucket_max"`
}

type rolloutStateBucketCoverage struct {
	Status      string                `json:"status"`
	BucketTotal int                   `json:"bucket_total"`
	HostCount   int                   `json:"host_count"`
	Error       string                `json:"error,omitempty"`
	Hosts       []rolloutStateHostRow `json:"hosts,omitempty"`
}

type rolloutStateHostRow struct {
	HostID              string    `json:"host_id"`
	BucketMin           int       `json:"bucket_min"`
	BucketMax           int       `json:"bucket_max"`
	Status              string    `json:"status"`
	LastHeartbeat       time.Time `json:"last_heartbeat"`
	LastHeartbeatAgeSec int64     `json:"last_heartbeat_age_sec"`
}

type rolloutStateActivity struct {
	Since          time.Time `json:"since"`
	ActiveSites    int       `json:"active_sites"`
	CheckedSince   int       `json:"checked_since"`
	UncheckedSince int       `json:"unchecked_since"`
	CheckedPercent float64   `json:"checked_percent"`
}

type rolloutStateDrift struct {
	Count int `json:"count"`
}

type rolloutStateDeliveryOwner struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func buildRolloutStateReport(ctx context.Context, cfg *config.Config, opts rolloutStateReportOptions, deps rolloutStateReportDeps) (rolloutStateReport, error) {
	if cfg == nil {
		return rolloutStateReport{}, errors.New("config is not loaded")
	}
	now := time.Now().UTC()
	if deps.Now != nil {
		now = deps.Now().UTC()
	}
	cutoff, err := resolveActivityCutoff(now, opts.Since)
	if err != nil {
		return rolloutStateReport{}, err
	}

	hostID := ""
	if deps.Hostname != nil {
		hostID = strings.TrimSpace(deps.Hostname())
	}
	if hostID == "" {
		hostID = "unknown"
	}

	minBucket, maxBucket, ownershipMode, err := rolloutStateRange(cfg)
	if err != nil {
		return rolloutStateReport{}, err
	}

	report := rolloutStateReport{
		Command:     "rollout state-report",
		GeneratedAt: now,
		Host:        hostID,
		Ownership: rolloutStateOwnership{
			Mode:      ownershipMode,
			BucketMin: minBucket,
			BucketMax: maxBucket,
		},
		BucketCoverage: rolloutStateBucketCoverage{
			BucketTotal: cfg.BucketTotal,
		},
		Activity: rolloutStateActivity{
			Since: cutoff,
		},
	}

	if ownershipMode == "pinned" {
		report.BucketCoverage.Status = "pinned_config"
	} else {
		if deps.GetAllHosts == nil {
			return rolloutStateReport{}, errors.New("host list query is not configured")
		}
		hosts, err := deps.GetAllHosts()
		if err != nil {
			return rolloutStateReport{}, fmt.Errorf("query jetmon_hosts: %w", err)
		}
		report.BucketCoverage.HostCount = len(hosts)
		report.BucketCoverage.Hosts = summarizeRolloutHosts(hosts, now)
		if err := validateDynamicBucketCoverage(hosts, cfg.BucketTotal, time.Duration(cfg.BucketHeartbeatGraceSec)*time.Second, now); err != nil {
			report.BucketCoverage.Status = "invalid"
			report.BucketCoverage.Error = err.Error()
			report.Issues = append(report.Issues, err.Error())
		} else {
			report.BucketCoverage.Status = "complete"
		}
	}

	if deps.CountActiveSitesForBucketRange == nil {
		return rolloutStateReport{}, errors.New("active site counter is not configured")
	}
	activeSites, err := deps.CountActiveSitesForBucketRange(ctx, minBucket, maxBucket)
	if err != nil {
		return rolloutStateReport{}, fmt.Errorf("count active sites in range %d-%d: %w", minBucket, maxBucket, err)
	}
	report.Activity.ActiveSites = activeSites

	if deps.CountRecentlyCheckedActiveSitesForRange == nil {
		return rolloutStateReport{}, errors.New("recently checked active site counter is not configured")
	}
	checkedSince, err := deps.CountRecentlyCheckedActiveSitesForRange(ctx, minBucket, maxBucket, cutoff)
	if err != nil {
		return rolloutStateReport{}, fmt.Errorf("count recently checked active sites in range %d-%d since %s: %w", minBucket, maxBucket, cutoff.Format(time.RFC3339), err)
	}
	report.Activity.CheckedSince = checkedSince
	report.Activity.UncheckedSince = maxInt(0, activeSites-checkedSince)
	if activeSites > 0 {
		report.Activity.CheckedPercent = float64(checkedSince) * 100 / float64(activeSites)
	}

	if deps.CountLegacyProjectionDrift == nil {
		return rolloutStateReport{}, errors.New("projection drift counter is not configured")
	}
	drift, err := deps.CountLegacyProjectionDrift(ctx, minBucket, maxBucket)
	if err != nil {
		return rolloutStateReport{}, fmt.Errorf("count legacy projection drift in range %d-%d: %w", minBucket, maxBucket, err)
	}
	report.ProjectionDrift.Count = drift

	level, message := deliveryOwnerStatus(cfg, hostID)
	report.DeliveryOwner = rolloutStateDeliveryOwner{Level: level, Message: message}

	report.Issues = append(report.Issues, rolloutStateIssues(report)...)
	report.SuggestedNextAction = suggestRolloutNextAction(report)
	report.OK = len(report.Issues) == 0
	return report, nil
}

func rolloutStateRange(cfg *config.Config) (int, int, string, error) {
	if minBucket, maxBucket, ok := cfg.PinnedBucketRange(); ok {
		return minBucket, maxBucket, "pinned", nil
	}
	if cfg.BucketTotal <= 0 {
		return 0, 0, "", errors.New("BUCKET_TOTAL must be > 0")
	}
	return 0, cfg.BucketTotal - 1, "dynamic", nil
}

func summarizeRolloutHosts(hosts []db.HostRow, now time.Time) []rolloutStateHostRow {
	out := make([]rolloutStateHostRow, 0, len(hosts))
	for _, host := range hosts {
		age := now.Sub(host.LastHeartbeat)
		if age < 0 {
			age = 0
		}
		out = append(out, rolloutStateHostRow{
			HostID:              host.HostID,
			BucketMin:           host.BucketMin,
			BucketMax:           host.BucketMax,
			Status:              host.Status,
			LastHeartbeat:       host.LastHeartbeat,
			LastHeartbeatAgeSec: int64(age.Round(time.Second) / time.Second),
		})
	}
	return out
}

func rolloutStateIssues(report rolloutStateReport) []string {
	var issues []string
	if report.ProjectionDrift.Count > 0 {
		issues = append(issues, fmt.Sprintf("legacy projection drift=%d", report.ProjectionDrift.Count))
	}
	if report.Activity.ActiveSites > 0 && report.Activity.CheckedSince == 0 {
		issues = append(issues, fmt.Sprintf("no active sites checked since %s", report.Activity.Since.Format(time.RFC3339)))
	} else if report.Activity.UncheckedSince > 0 {
		issues = append(issues, fmt.Sprintf("%d/%d active sites checked since %s", report.Activity.CheckedSince, report.Activity.ActiveSites, report.Activity.Since.Format(time.RFC3339)))
	}
	if report.DeliveryOwner.Level == "WARN" {
		issues = append(issues, report.DeliveryOwner.Message)
	}
	return issues
}

func suggestRolloutNextAction(report rolloutStateReport) string {
	if report.BucketCoverage.Status == "invalid" {
		return "Fix jetmon_hosts bucket coverage before relying on dynamic ownership."
	}
	if report.ProjectionDrift.Count > 0 {
		return "Run rollout projection-drift --limit=100 and fix legacy projection drift before continuing."
	}
	if report.Activity.ActiveSites == 0 {
		return "Confirm this range is intentionally empty before continuing."
	}
	if report.Activity.CheckedSince == 0 {
		return "Investigate the check loop; no active sites have fresh last_checked_at writes."
	}
	if report.Activity.UncheckedSince > 0 {
		return "Wait one full expected round, then run rollout cutover-check --require-all before moving on."
	}
	if report.DeliveryOwner.Level == "WARN" {
		return "Set DELIVERY_OWNER_HOST or explicitly approve multi-owner delivery before enabling API delivery workers."
	}
	if report.Ownership.Mode == "pinned" {
		return "Continue with the next pinned host; after every host is on v2, plan dynamic ownership cutover."
	}
	return "Dynamic ownership looks healthy; continue normal v2 rolling updates and monitoring."
}

func renderRolloutStateReport(out io.Writer, report rolloutStateReport, output string) error {
	if output == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	renderRolloutStateText(out, report)
	return nil
}

func renderRolloutStateText(out io.Writer, report rolloutStateReport) {
	fmt.Fprintf(out, "INFO rollout_state_generated_at=%s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(out, "INFO host=%q\n", report.Host)
	fmt.Fprintf(out, "INFO ownership_mode=%s bucket_range=%d-%d\n", report.Ownership.Mode, report.Ownership.BucketMin, report.Ownership.BucketMax)
	if report.BucketCoverage.Status == "invalid" {
		fmt.Fprintf(out, "WARN bucket_coverage=%s error=%q\n", report.BucketCoverage.Status, report.BucketCoverage.Error)
	} else {
		fmt.Fprintf(out, "PASS bucket_coverage=%s bucket_total=%d host_count=%d\n", report.BucketCoverage.Status, report.BucketCoverage.BucketTotal, report.BucketCoverage.HostCount)
	}
	fmt.Fprintf(out, "INFO activity_since=%s\n", report.Activity.Since.Format(time.RFC3339))
	fmt.Fprintf(out, "INFO active_sites=%d checked_since=%d unchecked_since=%d checked_percent=%.1f\n", report.Activity.ActiveSites, report.Activity.CheckedSince, report.Activity.UncheckedSince, report.Activity.CheckedPercent)
	if report.ProjectionDrift.Count > 0 {
		fmt.Fprintf(out, "WARN legacy_projection_drift=%d\n", report.ProjectionDrift.Count)
	} else {
		fmt.Fprintln(out, "PASS legacy_projection_drift=0")
	}
	if report.DeliveryOwner.Message != "" {
		fmt.Fprintf(out, "%s %s\n", report.DeliveryOwner.Level, report.DeliveryOwner.Message)
	}
	for _, issue := range report.Issues {
		fmt.Fprintf(out, "WARN rollout_state_issue=%q\n", issue)
	}
	fmt.Fprintf(out, "INFO suggested_next_action=%q\n", report.SuggestedNextAction)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func writeRolloutPlanSection(out io.Writer, title string, lines ...string) {
	fmt.Fprintf(out, "## %s\n", title)
	for _, line := range lines {
		fmt.Fprintln(out, line)
	}
	fmt.Fprintln(out)
}

func rolloutOperatorCommandOrComment(command, comment string) string {
	command = strings.TrimSpace(command)
	if command != "" {
		return command
	}
	comment = strings.TrimSpace(comment)
	if comment == "" {
		comment = "TODO: operator command required."
	}
	if strings.HasPrefix(comment, "#") {
		return comment
	}
	return "# " + comment
}

func rolloutCommand(parts ...string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func startSystemdServiceCommand(service string) string {
	quotedService := shellQuote(service)
	return "systemctl enable --now " + quotedService + " && systemctl is-active --quiet " + quotedService
}

func stopSystemdServiceCommand(service string) string {
	quotedService := shellQuote(service)
	return "systemctl stop " + quotedService + " && ! systemctl is-active --quiet " + quotedService
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

package main

import (
	"context"
	"encoding/csv"
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
	CountActiveSitesForBucketRange func(context.Context, int, int) (int, error)
	CountLegacyProjectionDrift     func(context.Context, int, int) (int, error)
}

type dynamicRolloutCheckDeps struct {
	Now                            func() time.Time
	GetAllHosts                    func() ([]db.HostRow, error)
	CountActiveSitesForBucketRange func(context.Context, int, int) (int, error)
	CountLegacyProjectionDrift     func(context.Context, int, int) (int, error)
}

type projectionDriftDeps struct {
	CountLegacyProjectionDrift func(context.Context, int, int) (int, error)
	ListLegacyProjectionDrift  func(context.Context, int, int, int) ([]db.ProjectionDriftRow, error)
}

func cmdRollout(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout <static-plan-check|pinned-check|dynamic-check|projection-drift> [args]")
		os.Exit(1)
	}

	switch args[0] {
	case "static-plan-check":
		cmdRolloutStaticPlanCheck(args[1:])
	case "pinned-check":
		cmdRolloutPinnedCheck(args[1:])
	case "dynamic-check":
		cmdRolloutDynamicCheck(args[1:])
	case "projection-drift":
		cmdRolloutProjectionDrift(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown rollout subcommand %q (want: static-plan-check, pinned-check, dynamic-check, projection-drift)\n", args[0])
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
	_ = fs.Parse(args)
	if fs.NArg() != 0 || strings.TrimSpace(*file) == "" {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout static-plan-check --file=<ranges.csv> [--bucket-total=N] [--host=<host> --bucket-min=N --bucket-max=N]")
		os.Exit(1)
	}
	assertion, err := staticPlanAssertionFromFlags(*host, *bucketMin, *bucketMax)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
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
	input := io.Reader(os.Stdin)
	var opened *os.File
	if inputName != "-" {
		f, err := os.Open(inputName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL open static bucket plan: %v\n", err)
			os.Exit(1)
		}
		opened = f
		input = f
	}
	if opened != nil {
		defer opened.Close()
	}

	if err := runStaticPlanCheck(os.Stdout, inputName, input, resolvedBucketTotal, assertion); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}
}

func cmdRolloutPinnedCheck(args []string) {
	fs := flag.NewFlagSet("rollout pinned-check", flag.ExitOnError)
	host := fs.String("host", "", "host id to check (default current hostname)")
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout pinned-check [--host=<host_id>]")
		os.Exit(1)
	}

	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL config parse: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS config parse")

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL db connect: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS db connect")

	deps := pinnedRolloutCheckDeps{
		Hostname:                       db.Hostname,
		HostRowExists:                  db.HostRowExists,
		CountActiveSitesForBucketRange: db.CountActiveSitesForBucketRange,
		CountLegacyProjectionDrift:     db.CountLegacyProjectionDrift,
	}
	if err := runPinnedRolloutCheck(context.Background(), os.Stdout, config.Get(), *host, deps); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}
}

func cmdRolloutDynamicCheck(args []string) {
	fs := flag.NewFlagSet("rollout dynamic-check", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout dynamic-check")
		os.Exit(1)
	}

	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL config parse: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS config parse")

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL db connect: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS db connect")

	deps := dynamicRolloutCheckDeps{
		Now:                            time.Now,
		GetAllHosts:                    db.GetAllHosts,
		CountActiveSitesForBucketRange: db.CountActiveSitesForBucketRange,
		CountLegacyProjectionDrift:     db.CountLegacyProjectionDrift,
	}
	if err := runDynamicRolloutCheck(context.Background(), os.Stdout, config.Get(), deps); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}
}

func cmdRolloutProjectionDrift(args []string) {
	fs := flag.NewFlagSet("rollout projection-drift", flag.ExitOnError)
	bucketMin := fs.Int("bucket-min", -1, "inclusive bucket minimum (default pinned range or 0)")
	bucketMax := fs.Int("bucket-max", -1, "inclusive bucket maximum (default pinned range or BUCKET_TOTAL-1)")
	limit := fs.Int("limit", 50, "maximum drift rows to print")
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout projection-drift [--bucket-min=N --bucket-max=N] [--limit=N]")
		os.Exit(1)
	}

	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL config parse: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS config parse")

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL db connect: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS db connect")

	deps := projectionDriftDeps{
		CountLegacyProjectionDrift: db.CountLegacyProjectionDrift,
		ListLegacyProjectionDrift:  db.ListLegacyProjectionDrift,
	}
	if err := runProjectionDriftReport(context.Background(), os.Stdout, config.Get(), *bucketMin, *bucketMax, *limit, deps); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}
}

type staticBucketRange struct {
	HostID    string
	BucketMin int
	BucketMax int
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

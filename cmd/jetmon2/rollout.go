package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

type pinnedRolloutCheckDeps struct {
	Hostname                       func() string
	HostRowExists                  func(context.Context, string) (bool, error)
	CountActiveSitesForBucketRange func(context.Context, int, int) (int, error)
	CountLegacyProjectionDrift     func(context.Context, int, int) (int, error)
}

func cmdRollout(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 rollout <pinned-check> [args]")
		os.Exit(1)
	}

	switch args[0] {
	case "pinned-check":
		cmdRolloutPinnedCheck(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown rollout subcommand %q (want: pinned-check)\n", args[0])
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

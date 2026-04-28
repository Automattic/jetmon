package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

func TestRunPinnedRolloutCheckSuccess(t *testing.T) {
	minBucket, maxBucket := 12, 34
	cfg := &config.Config{
		PinnedBucketMin:              &minBucket,
		PinnedBucketMax:              &maxBucket,
		LegacyStatusProjectionEnable: true,
	}

	var gotHost string
	var gotMin, gotMax int
	deps := pinnedRolloutCheckDeps{
		Hostname: func() string { return "host-a" },
		HostRowExists: func(_ context.Context, hostID string) (bool, error) {
			gotHost = hostID
			return false, nil
		},
		CountActiveSitesForBucketRange: func(_ context.Context, min, max int) (int, error) {
			gotMin, gotMax = min, max
			return 37, nil
		},
		CountLegacyProjectionDrift: func(_ context.Context, min, max int) (int, error) {
			if min != minBucket || max != maxBucket {
				t.Fatalf("CountLegacyProjectionDrift range = %d-%d, want %d-%d", min, max, minBucket, maxBucket)
			}
			return 0, nil
		},
	}

	var out bytes.Buffer
	if err := runPinnedRolloutCheck(context.Background(), &out, cfg, "", deps); err != nil {
		t.Fatalf("runPinnedRolloutCheck: %v", err)
	}
	if gotHost != "host-a" {
		t.Fatalf("host = %q, want host-a", gotHost)
	}
	if gotMin != minBucket || gotMax != maxBucket {
		t.Fatalf("active site range = %d-%d, want %d-%d", gotMin, gotMax, minBucket, maxBucket)
	}
	for _, want := range []string{
		"PASS pinned_range=12-34",
		"PASS legacy_status_projection=enabled",
		"PASS api_port=disabled",
		"PASS jetmon_hosts row absent host=\"host-a\"",
		"INFO active_sites_in_pinned_range=37",
		"PASS legacy_projection_drift=0",
		"pinned rollout check passed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunPinnedRolloutCheckUsesHostOverride(t *testing.T) {
	minBucket, maxBucket := 1, 2
	cfg := &config.Config{
		PinnedBucketMin:              &minBucket,
		PinnedBucketMax:              &maxBucket,
		LegacyStatusProjectionEnable: true,
	}

	var gotHost string
	deps := pinnedRolloutCheckDeps{
		Hostname: func() string { return "wrong-host" },
		HostRowExists: func(_ context.Context, hostID string) (bool, error) {
			gotHost = hostID
			return false, nil
		},
		CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
			return 1, nil
		},
		CountLegacyProjectionDrift: func(context.Context, int, int) (int, error) {
			return 0, nil
		},
	}

	var out bytes.Buffer
	if err := runPinnedRolloutCheck(context.Background(), &out, cfg, " override-host ", deps); err != nil {
		t.Fatalf("runPinnedRolloutCheck: %v", err)
	}
	if gotHost != "override-host" {
		t.Fatalf("host = %q, want override-host", gotHost)
	}
}

func TestRunPinnedRolloutCheckWarnsWhenAPIEnabled(t *testing.T) {
	minBucket, maxBucket := 1, 2
	cfg := &config.Config{
		PinnedBucketMin:              &minBucket,
		PinnedBucketMax:              &maxBucket,
		LegacyStatusProjectionEnable: true,
		APIPort:                      8090,
	}
	deps := successfulPinnedRolloutDeps()

	var out bytes.Buffer
	if err := runPinnedRolloutCheck(context.Background(), &out, cfg, "", deps); err != nil {
		t.Fatalf("runPinnedRolloutCheck: %v", err)
	}
	if !strings.Contains(out.String(), "WARN api_port=8090") {
		t.Fatalf("output missing API warning:\n%s", out.String())
	}
}

func TestRunPinnedRolloutCheckFailures(t *testing.T) {
	minBucket, maxBucket := 1, 2
	tests := []struct {
		name string
		cfg  *config.Config
		deps pinnedRolloutCheckDeps
		want string
	}{
		{
			name: "missing pinned range",
			cfg:  &config.Config{LegacyStatusProjectionEnable: true},
			deps: successfulPinnedRolloutDeps(),
			want: "pinned bucket range is not configured",
		},
		{
			name: "legacy projection disabled",
			cfg: &config.Config{
				PinnedBucketMin: &minBucket,
				PinnedBucketMax: &maxBucket,
			},
			deps: successfulPinnedRolloutDeps(),
			want: "LEGACY_STATUS_PROJECTION_ENABLE must be true",
		},
		{
			name: "host row exists",
			cfg:  pinnedRolloutTestConfig(minBucket, maxBucket),
			deps: pinnedRolloutCheckDeps{
				Hostname: func() string { return "host-a" },
				HostRowExists: func(context.Context, string) (bool, error) {
					return true, nil
				},
			},
			want: "still has a jetmon_hosts row",
		},
		{
			name: "host row query error",
			cfg:  pinnedRolloutTestConfig(minBucket, maxBucket),
			deps: pinnedRolloutCheckDeps{
				Hostname: func() string { return "host-a" },
				HostRowExists: func(context.Context, string) (bool, error) {
					return false, errors.New("db unavailable")
				},
			},
			want: "db unavailable",
		},
		{
			name: "projection drift",
			cfg:  pinnedRolloutTestConfig(minBucket, maxBucket),
			deps: pinnedRolloutCheckDeps{
				Hostname: func() string { return "host-a" },
				HostRowExists: func(context.Context, string) (bool, error) {
					return false, nil
				},
				CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
					return 10, nil
				},
				CountLegacyProjectionDrift: func(context.Context, int, int) (int, error) {
					return 2, nil
				},
			},
			want: "legacy projection drift=2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runPinnedRolloutCheck(context.Background(), &out, tt.cfg, "", tt.deps)
			if err == nil {
				t.Fatal("runPinnedRolloutCheck succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func pinnedRolloutTestConfig(minBucket, maxBucket int) *config.Config {
	return &config.Config{
		PinnedBucketMin:              &minBucket,
		PinnedBucketMax:              &maxBucket,
		LegacyStatusProjectionEnable: true,
	}
}

func successfulPinnedRolloutDeps() pinnedRolloutCheckDeps {
	return pinnedRolloutCheckDeps{
		Hostname: func() string { return "host-a" },
		HostRowExists: func(context.Context, string) (bool, error) {
			return false, nil
		},
		CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
			return 1, nil
		},
		CountLegacyProjectionDrift: func(context.Context, int, int) (int, error) {
			return 0, nil
		},
	}
}

func TestRunDynamicRolloutCheckSuccess(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		BucketTotal:                  10,
		BucketHeartbeatGraceSec:      60,
		LegacyStatusProjectionEnable: true,
	}

	var gotMin, gotMax int
	deps := dynamicRolloutCheckDeps{
		Now: func() time.Time { return now },
		GetAllHosts: func() ([]db.HostRow, error) {
			return []db.HostRow{
				{HostID: "host-b", BucketMin: 5, BucketMax: 9, LastHeartbeat: now.Add(-10 * time.Second), Status: "active"},
				{HostID: "host-a", BucketMin: 0, BucketMax: 4, LastHeartbeat: now.Add(-10 * time.Second), Status: "active"},
			}, nil
		},
		CountActiveSitesForBucketRange: func(_ context.Context, min, max int) (int, error) {
			gotMin, gotMax = min, max
			return 123, nil
		},
		CountLegacyProjectionDrift: func(_ context.Context, min, max int) (int, error) {
			if min != 0 || max != 9 {
				t.Fatalf("drift range = %d-%d, want 0-9", min, max)
			}
			return 0, nil
		},
	}

	var out bytes.Buffer
	if err := runDynamicRolloutCheck(context.Background(), &out, cfg, deps); err != nil {
		t.Fatalf("runDynamicRolloutCheck: %v", err)
	}
	if gotMin != 0 || gotMax != 9 {
		t.Fatalf("active site range = %d-%d, want 0-9", gotMin, gotMax)
	}
	for _, want := range []string{
		"PASS bucket_ownership=dynamic",
		"PASS legacy_status_projection=enabled",
		"INFO jetmon_hosts_rows=2",
		"PASS dynamic_bucket_coverage=0-9 hosts=2",
		"INFO active_sites_dynamic_range=123",
		"PASS legacy_projection_drift=0",
		"dynamic rollout check passed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunDynamicRolloutCheckFailures(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	minBucket, maxBucket := 1, 2

	tests := []struct {
		name string
		cfg  *config.Config
		deps dynamicRolloutCheckDeps
		want string
	}{
		{
			name: "pinned range still configured",
			cfg: &config.Config{
				BucketTotal:                  10,
				BucketHeartbeatGraceSec:      60,
				LegacyStatusProjectionEnable: true,
				PinnedBucketMin:              &minBucket,
				PinnedBucketMax:              &maxBucket,
			},
			deps: successfulDynamicRolloutDeps(now),
			want: "pinned bucket range 1-2 is still configured",
		},
		{
			name: "legacy projection disabled",
			cfg: &config.Config{
				BucketTotal:             10,
				BucketHeartbeatGraceSec: 60,
			},
			deps: successfulDynamicRolloutDeps(now),
			want: "LEGACY_STATUS_PROJECTION_ENABLE must remain true",
		},
		{
			name: "host query error",
			cfg:  dynamicRolloutTestConfig(),
			deps: dynamicRolloutCheckDeps{
				GetAllHosts: func() ([]db.HostRow, error) {
					return nil, errors.New("db unavailable")
				},
			},
			want: "db unavailable",
		},
		{
			name: "projection drift",
			cfg:  dynamicRolloutTestConfig(),
			deps: dynamicRolloutCheckDeps{
				Now: func() time.Time { return now },
				GetAllHosts: func() ([]db.HostRow, error) {
					return dynamicRolloutHosts(now), nil
				},
				CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
					return 10, nil
				},
				CountLegacyProjectionDrift: func(context.Context, int, int) (int, error) {
					return 3, nil
				},
			},
			want: "legacy projection drift=3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runDynamicRolloutCheck(context.Background(), &out, tt.cfg, tt.deps)
			if err == nil {
				t.Fatal("runDynamicRolloutCheck succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestValidateDynamicBucketCoverageFailures(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		hosts []db.HostRow
		want  string
	}{
		{
			name:  "no hosts",
			hosts: nil,
			want:  "jetmon_hosts has no rows",
		},
		{
			name: "inactive host",
			hosts: []db.HostRow{
				{HostID: "host-a", BucketMin: 0, BucketMax: 9, LastHeartbeat: now, Status: "draining"},
			},
			want: "status=\"draining\"",
		},
		{
			name: "stale heartbeat",
			hosts: []db.HostRow{
				{HostID: "host-a", BucketMin: 0, BucketMax: 9, LastHeartbeat: now.Add(-2 * time.Minute), Status: "active"},
			},
			want: "heartbeat is stale",
		},
		{
			name: "invalid range",
			hosts: []db.HostRow{
				{HostID: "host-a", BucketMin: 0, BucketMax: 10, LastHeartbeat: now, Status: "active"},
			},
			want: "invalid bucket range",
		},
		{
			name: "leading gap",
			hosts: []db.HostRow{
				{HostID: "host-a", BucketMin: 1, BucketMax: 9, LastHeartbeat: now, Status: "active"},
			},
			want: "gap 0-0",
		},
		{
			name: "middle gap",
			hosts: []db.HostRow{
				{HostID: "host-a", BucketMin: 0, BucketMax: 3, LastHeartbeat: now, Status: "active"},
				{HostID: "host-b", BucketMin: 5, BucketMax: 9, LastHeartbeat: now, Status: "active"},
			},
			want: "gap 4-4",
		},
		{
			name: "overlap",
			hosts: []db.HostRow{
				{HostID: "host-a", BucketMin: 0, BucketMax: 5, LastHeartbeat: now, Status: "active"},
				{HostID: "host-b", BucketMin: 5, BucketMax: 9, LastHeartbeat: now, Status: "active"},
			},
			want: "overlaps",
		},
		{
			name: "trailing gap",
			hosts: []db.HostRow{
				{HostID: "host-a", BucketMin: 0, BucketMax: 8, LastHeartbeat: now, Status: "active"},
			},
			want: "trailing gap 9-9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDynamicBucketCoverage(tt.hosts, 10, time.Minute, now)
			if err == nil {
				t.Fatal("validateDynamicBucketCoverage succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func dynamicRolloutTestConfig() *config.Config {
	return &config.Config{
		BucketTotal:                  10,
		BucketHeartbeatGraceSec:      60,
		LegacyStatusProjectionEnable: true,
	}
}

func dynamicRolloutHosts(now time.Time) []db.HostRow {
	return []db.HostRow{
		{HostID: "host-a", BucketMin: 0, BucketMax: 4, LastHeartbeat: now, Status: "active"},
		{HostID: "host-b", BucketMin: 5, BucketMax: 9, LastHeartbeat: now, Status: "active"},
	}
}

func successfulDynamicRolloutDeps(now time.Time) dynamicRolloutCheckDeps {
	return dynamicRolloutCheckDeps{
		Now: func() time.Time { return now },
		GetAllHosts: func() ([]db.HostRow, error) {
			return dynamicRolloutHosts(now), nil
		},
		CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
			return 1, nil
		},
		CountLegacyProjectionDrift: func(context.Context, int, int) (int, error) {
			return 0, nil
		},
	}
}

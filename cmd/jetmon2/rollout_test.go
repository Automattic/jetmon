package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Automattic/jetmon/internal/config"
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

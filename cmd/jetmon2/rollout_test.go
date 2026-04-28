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

func TestRunStaticPlanCheckSuccess(t *testing.T) {
	input := strings.NewReader(`
# host ranges copied from v1 config
host,bucket_min,bucket_max
jetmon-v1-b,5,9
jetmon-v1-a,0,4
	`)

	var out bytes.Buffer
	if err := runStaticPlanCheck(&out, "ranges.csv", input, 10, staticPlanAssertion{}); err != nil {
		t.Fatalf("runStaticPlanCheck: %v", err)
	}
	for _, want := range []string{
		"PASS static_plan_file=ranges.csv ranges=2",
		"PASS static_bucket_coverage=0-9 hosts=2",
		"static rollout plan check passed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunStaticPlanCheckHostAssertionSuccess(t *testing.T) {
	input := strings.NewReader(`
host,bucket_min,bucket_max
jetmon-v1-a,0,4
jetmon-v1-b,5,9
`)

	var out bytes.Buffer
	err := runStaticPlanCheck(&out, "ranges.csv", input, 10, staticPlanAssertion{
		HostID:    "jetmon-v1-b",
		BucketMin: 5,
		BucketMax: 9,
	})
	if err != nil {
		t.Fatalf("runStaticPlanCheck: %v", err)
	}
	if !strings.Contains(out.String(), `PASS static_plan_host="jetmon-v1-b" range=5-9`) {
		t.Fatalf("output missing host assertion:\n%s", out.String())
	}
}

func TestStaticPlanAssertionFromFlags(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		bucketMin int
		bucketMax int
		want      staticPlanAssertion
		wantErr   string
	}{
		{name: "none", bucketMin: -1, bucketMax: -1},
		{
			name:      "host only",
			host:      " host-a ",
			bucketMin: -1,
			bucketMax: -1,
			want:      staticPlanAssertion{HostID: "host-a", BucketMin: -1, BucketMax: -1},
		},
		{
			name:      "host and range",
			host:      "host-a",
			bucketMin: 0,
			bucketMax: 9,
			want:      staticPlanAssertion{HostID: "host-a", BucketMin: 0, BucketMax: 9},
		},
		{
			name:      "range without host",
			bucketMin: 0,
			bucketMax: 9,
			wantErr:   "--host is required",
		},
		{
			name:      "negative range",
			host:      "host-a",
			bucketMin: -2,
			bucketMax: -2,
			wantErr:   "must be >= 0",
		},
		{
			name:      "one sided range",
			host:      "host-a",
			bucketMin: 0,
			bucketMax: -1,
			wantErr:   "must be set together",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := staticPlanAssertionFromFlags(tt.host, tt.bucketMin, tt.bucketMax)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("staticPlanAssertionFromFlags succeeded")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("staticPlanAssertionFromFlags: %v", err)
			}
			if got != tt.want {
				t.Fatalf("assertion = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestValidateStaticPlanAssertionFailures(t *testing.T) {
	ranges := []staticBucketRange{
		{HostID: "host-a", BucketMin: 0, BucketMax: 4},
		{HostID: "host-b", BucketMin: 5, BucketMax: 9},
	}
	tests := []struct {
		name      string
		assertion staticPlanAssertion
		want      string
	}{
		{
			name:      "missing host",
			assertion: staticPlanAssertion{HostID: "host-c", BucketMin: -1, BucketMax: -1},
			want:      "not present",
		},
		{
			name:      "range mismatch",
			assertion: staticPlanAssertion{HostID: "host-b", BucketMin: 6, BucketMax: 9},
			want:      "want 6-9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateStaticPlanAssertion(ranges, tt.assertion)
			if err == nil {
				t.Fatal("validateStaticPlanAssertion succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestParseStaticBucketPlanCSVAcceptsLegacyHeader(t *testing.T) {
	ranges, err := parseStaticBucketPlanCSV(strings.NewReader(`
host,BUCKET_NO_MIN,BUCKET_NO_MAX
jetmon-v1-a,0,4
`))
	if err != nil {
		t.Fatalf("parseStaticBucketPlanCSV: %v", err)
	}
	if len(ranges) != 1 {
		t.Fatalf("ranges len = %d, want 1", len(ranges))
	}
	got := ranges[0]
	if got.HostID != "jetmon-v1-a" || got.BucketMin != 0 || got.BucketMax != 4 {
		t.Fatalf("range = %#v, want jetmon-v1-a 0-4", got)
	}
}

func TestParseStaticBucketPlanCSVFailures(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty",
			input: "\n# only comments\n",
			want:  "no host ranges",
		},
		{
			name:  "wrong field count",
			input: "host-a,0,9,extra\n",
			want:  "expected host,bucket_min,bucket_max",
		},
		{
			name:  "missing host",
			input: ",0,9\n",
			want:  "host is required",
		},
		{
			name:  "invalid min",
			input: "host-a,nope,9\n",
			want:  "bucket_min \"nope\" is not an integer",
		},
		{
			name:  "invalid max",
			input: "host-a,0,nope\n",
			want:  "bucket_max \"nope\" is not an integer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseStaticBucketPlanCSV(strings.NewReader(tt.input))
			if err == nil {
				t.Fatal("parseStaticBucketPlanCSV succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestValidateStaticBucketPlanFailures(t *testing.T) {
	tests := []struct {
		name        string
		ranges      []staticBucketRange
		bucketTotal int
		want        string
	}{
		{
			name:        "invalid total",
			ranges:      []staticBucketRange{{HostID: "host-a", BucketMin: 0, BucketMax: 9}},
			bucketTotal: 0,
			want:        "BUCKET_TOTAL must be > 0",
		},
		{
			name:        "duplicate host",
			ranges:      []staticBucketRange{{HostID: "host-a", BucketMin: 0, BucketMax: 4}, {HostID: "host-a", BucketMin: 5, BucketMax: 9}},
			bucketTotal: 10,
			want:        "listed more than once",
		},
		{
			name:        "invalid range",
			ranges:      []staticBucketRange{{HostID: "host-a", BucketMin: 0, BucketMax: 10}},
			bucketTotal: 10,
			want:        "invalid bucket range",
		},
		{
			name:        "leading gap",
			ranges:      []staticBucketRange{{HostID: "host-a", BucketMin: 1, BucketMax: 9}},
			bucketTotal: 10,
			want:        "gap 0-0",
		},
		{
			name:        "middle gap",
			ranges:      []staticBucketRange{{HostID: "host-a", BucketMin: 0, BucketMax: 3}, {HostID: "host-b", BucketMin: 5, BucketMax: 9}},
			bucketTotal: 10,
			want:        "gap 4-4",
		},
		{
			name:        "overlap",
			ranges:      []staticBucketRange{{HostID: "host-a", BucketMin: 0, BucketMax: 5}, {HostID: "host-b", BucketMin: 5, BucketMax: 9}},
			bucketTotal: 10,
			want:        "overlaps",
		},
		{
			name:        "trailing gap",
			ranges:      []staticBucketRange{{HostID: "host-a", BucketMin: 0, BucketMax: 8}},
			bucketTotal: 10,
			want:        "trailing gap 9-9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStaticBucketPlan(tt.ranges, tt.bucketTotal)
			if err == nil {
				t.Fatal("validateStaticBucketPlan succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

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
		ListOverlappingHostRows: func(_ context.Context, min, max int) ([]db.HostRow, error) {
			if min != minBucket || max != maxBucket {
				t.Fatalf("ListOverlappingHostRows range = %d-%d, want %d-%d", min, max, minBucket, maxBucket)
			}
			return nil, nil
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
		"PASS jetmon_hosts overlap=0",
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
		ListOverlappingHostRows: func(context.Context, int, int) ([]db.HostRow, error) {
			return nil, nil
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

func TestRunPinnedRolloutCheckWarnsWhenRangeIsEmpty(t *testing.T) {
	minBucket, maxBucket := 1, 2
	cfg := pinnedRolloutTestConfig(minBucket, maxBucket)
	deps := successfulPinnedRolloutDeps()
	deps.CountActiveSitesForBucketRange = func(context.Context, int, int) (int, error) {
		return 0, nil
	}

	var out bytes.Buffer
	if err := runPinnedRolloutCheck(context.Background(), &out, cfg, "", deps); err != nil {
		t.Fatalf("runPinnedRolloutCheck: %v", err)
	}
	if !strings.Contains(out.String(), "WARN active_sites_in_pinned_range=0") {
		t.Fatalf("output missing empty-range warning:\n%s", out.String())
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
			name: "overlapping host rows",
			cfg:  pinnedRolloutTestConfig(minBucket, maxBucket),
			deps: pinnedRolloutCheckDeps{
				Hostname: func() string { return "host-a" },
				HostRowExists: func(context.Context, string) (bool, error) {
					return false, nil
				},
				ListOverlappingHostRows: func(context.Context, int, int) ([]db.HostRow, error) {
					return []db.HostRow{
						{HostID: "host-b", BucketMin: 0, BucketMax: 5, Status: "active"},
					}, nil
				},
			},
			want: "overlapping pinned range",
		},
		{
			name: "overlapping host query error",
			cfg:  pinnedRolloutTestConfig(minBucket, maxBucket),
			deps: pinnedRolloutCheckDeps{
				Hostname: func() string { return "host-a" },
				HostRowExists: func(context.Context, string) (bool, error) {
					return false, nil
				},
				ListOverlappingHostRows: func(context.Context, int, int) ([]db.HostRow, error) {
					return nil, errors.New("db unavailable")
				},
			},
			want: "list jetmon_hosts rows overlapping",
		},
		{
			name: "projection drift",
			cfg:  pinnedRolloutTestConfig(minBucket, maxBucket),
			deps: pinnedRolloutCheckDeps{
				Hostname: func() string { return "host-a" },
				HostRowExists: func(context.Context, string) (bool, error) {
					return false, nil
				},
				ListOverlappingHostRows: func(context.Context, int, int) ([]db.HostRow, error) {
					return nil, nil
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
		ListOverlappingHostRows: func(context.Context, int, int) ([]db.HostRow, error) {
			return nil, nil
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

func TestRunDynamicRolloutCheckWarnsWhenSiteTableIsEmpty(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	cfg := dynamicRolloutTestConfig()
	deps := successfulDynamicRolloutDeps(now)
	deps.CountActiveSitesForBucketRange = func(context.Context, int, int) (int, error) {
		return 0, nil
	}

	var out bytes.Buffer
	if err := runDynamicRolloutCheck(context.Background(), &out, cfg, deps); err != nil {
		t.Fatalf("runDynamicRolloutCheck: %v", err)
	}
	if !strings.Contains(out.String(), "WARN active_sites_dynamic_range=0") {
		t.Fatalf("output missing empty-table warning:\n%s", out.String())
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

func TestRunProjectionDriftReportNoDrift(t *testing.T) {
	cfg := dynamicRolloutTestConfig()
	deps := projectionDriftDeps{
		CountLegacyProjectionDrift: func(_ context.Context, min, max int) (int, error) {
			if min != 0 || max != 9 {
				t.Fatalf("count range = %d-%d, want 0-9", min, max)
			}
			return 0, nil
		},
	}

	var out bytes.Buffer
	if err := runProjectionDriftReport(context.Background(), &out, cfg, -1, -1, 50, deps); err != nil {
		t.Fatalf("runProjectionDriftReport: %v", err)
	}
	for _, want := range []string{
		"INFO projection_drift_range=0-9",
		"INFO legacy_projection_drift=0",
		"PASS legacy_projection_drift=0",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunProjectionDriftReportListsRowsAndFails(t *testing.T) {
	cfg := dynamicRolloutTestConfig()
	eventID := int64(123)
	eventState := "Down"
	deps := projectionDriftDeps{
		CountLegacyProjectionDrift: func(context.Context, int, int) (int, error) {
			return 2, nil
		},
		ListLegacyProjectionDrift: func(_ context.Context, min, max, limit int) ([]db.ProjectionDriftRow, error) {
			if min != 2 || max != 4 || limit != 1 {
				t.Fatalf("list args = %d-%d limit=%d, want 2-4 limit=1", min, max, limit)
			}
			return []db.ProjectionDriftRow{
				{BlogID: 42, BucketNo: 3, SiteStatus: 1, ExpectedStatus: 2, EventID: &eventID, EventState: &eventState},
			}, nil
		},
	}

	var out bytes.Buffer
	err := runProjectionDriftReport(context.Background(), &out, cfg, 2, 4, 1, deps)
	if err == nil {
		t.Fatal("runProjectionDriftReport succeeded")
	}
	if !strings.Contains(err.Error(), "legacy projection drift=2") {
		t.Fatalf("error = %q, want drift count", err.Error())
	}
	for _, want := range []string{
		"BLOG_ID",
		"42",
		"Down",
		"INFO projection_drift_rows_truncated=1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestResolveProjectionDriftRange(t *testing.T) {
	minBucket, maxBucket := 2, 4
	tests := []struct {
		name    string
		cfg     *config.Config
		inMin   int
		inMax   int
		wantMin int
		wantMax int
		wantErr string
	}{
		{
			name:    "dynamic default",
			cfg:     dynamicRolloutTestConfig(),
			inMin:   -1,
			inMax:   -1,
			wantMin: 0,
			wantMax: 9,
		},
		{
			name: "pinned default",
			cfg: &config.Config{
				BucketTotal:     10,
				PinnedBucketMin: &minBucket,
				PinnedBucketMax: &maxBucket,
			},
			inMin:   -1,
			inMax:   -1,
			wantMin: 2,
			wantMax: 4,
		},
		{
			name:    "explicit range",
			cfg:     dynamicRolloutTestConfig(),
			inMin:   3,
			inMax:   5,
			wantMin: 3,
			wantMax: 5,
		},
		{
			name:    "one sided range",
			cfg:     dynamicRolloutTestConfig(),
			inMin:   3,
			inMax:   -1,
			wantErr: "must be set together",
		},
		{
			name:    "negative range",
			cfg:     dynamicRolloutTestConfig(),
			inMin:   -2,
			inMax:   -2,
			wantErr: "must be >= 0",
		},
		{
			name:    "inverted range",
			cfg:     dynamicRolloutTestConfig(),
			inMin:   7,
			inMax:   3,
			wantErr: "bucket-max must be >= bucket-min",
		},
		{
			name:    "range outside total",
			cfg:     dynamicRolloutTestConfig(),
			inMin:   0,
			inMax:   10,
			wantErr: "bucket-max must be < BUCKET_TOTAL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMin, gotMax, err := resolveProjectionDriftRange(tt.cfg, tt.inMin, tt.inMax)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("resolveProjectionDriftRange succeeded")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveProjectionDriftRange: %v", err)
			}
			if gotMin != tt.wantMin || gotMax != tt.wantMax {
				t.Fatalf("range = %d-%d, want %d-%d", gotMin, gotMax, tt.wantMin, tt.wantMax)
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

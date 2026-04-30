package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

func TestRunRolloutCommandOutputJSON(t *testing.T) {
	var out bytes.Buffer
	err := runRolloutCommandOutput(&out, "rollout test-check", "json", func(w io.Writer) error {
		fmt.Fprintln(w, "PASS config parse")
		fmt.Fprintln(w, "## activity check")
		fmt.Fprintln(w, "INFO active_sites=3")
		return errors.New("activity failed")
	})
	if err == nil {
		t.Fatal("runRolloutCommandOutput returned nil error")
	}

	var report rolloutJSONReport
	if decodeErr := json.Unmarshal(out.Bytes(), &report); decodeErr != nil {
		t.Fatalf("decode rollout JSON report: %v\n%s", decodeErr, out.String())
	}
	if report.OK {
		t.Fatal("report.OK = true, want false")
	}
	if report.Command != "rollout test-check" {
		t.Fatalf("command = %q", report.Command)
	}
	if len(report.Failures) != 1 || report.Failures[0] != "activity failed" {
		t.Fatalf("failures = %#v", report.Failures)
	}
	wantLevels := []string{"pass", "section", "info"}
	if len(report.Lines) != len(wantLevels) {
		t.Fatalf("line count = %d, want %d: %#v", len(report.Lines), len(wantLevels), report.Lines)
	}
	for i, want := range wantLevels {
		if report.Lines[i].Level != want {
			t.Fatalf("line %d level = %q, want %q", i, report.Lines[i].Level, want)
		}
	}
}

func TestNormalizeRolloutOutput(t *testing.T) {
	for _, raw := range []string{"", "text", "TEXT", " json "} {
		if _, err := normalizeRolloutOutput(raw); err != nil {
			t.Fatalf("normalizeRolloutOutput(%q): %v", raw, err)
		}
	}
	if _, err := normalizeRolloutOutput("yaml"); err == nil {
		t.Fatal("normalizeRolloutOutput(yaml) error = nil")
	}
}

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

func TestRunRolloutRehearsalPlanSameServer(t *testing.T) {
	input := strings.NewReader(`
host,bucket_min,bucket_max
jetmon-v1-a,0,4
jetmon-v1-b,5,9
`)
	opts := rolloutRehearsalPlanOptions{
		Mode:        "same-server",
		PlanFile:    "rollout-buckets.csv",
		HostID:      "jetmon-v1-a",
		BucketMin:   0,
		BucketMax:   4,
		BucketTotal: 10,
		Binary:      "./jetmon2",
		Service:     "jetmon2",
		Since:       "15m",
		V1StopCmd:   "systemctl stop jetmon",
		V1StartCmd:  "systemctl start jetmon",
	}

	var out bytes.Buffer
	if err := runRolloutRehearsalPlan(&out, input, opts); err != nil {
		t.Fatalf("runRolloutRehearsalPlan: %v", err)
	}
	for _, want := range []string{
		"INFO mode=same-server",
		`INFO plan_host="jetmon-v1-a" runtime_host="jetmon-v1-a" range=0-4`,
		"# Run this runbook from the staged v2 runtime host, not from a separate orchestrator host.",
		"# Commands run from that runtime host unless the printed command explicitly targets another host.",
		"# Shell commands need the same DB_* environment used by the jetmon2 service.",
		"./jetmon2 rollout static-plan-check --file rollout-buckets.csv --host jetmon-v1-a --bucket-min 0 --bucket-max 4 --bucket-total 10",
		"./jetmon2 validate-config",
		"./jetmon2 rollout host-preflight --file rollout-buckets.csv --host jetmon-v1-a --runtime-host jetmon-v1-a --bucket-min 0 --bucket-max 4 --bucket-total 10 --service jetmon2",
		"systemctl stop jetmon",
		"# HOLD: confirm v1 is stopped before starting v2.",
		"systemctl enable --now jetmon2 && systemctl is-active --quiet jetmon2",
		"# Immediate smoke gate: checks startup and recent activity; recent writes can still include v1.",
		"./jetmon2 rollout cutover-check --host jetmon-v1-a --bucket-min 0 --bucket-max 4 --since 15m",
		"# Strong gate after one full v2 check round:",
		"./jetmon2 rollout cutover-check --host jetmon-v1-a --bucket-min 0 --bucket-max 4 --since 15m --require-all",
		"# HOLD: confirm the v2 process is stopped before restarting v1.",
		"./jetmon2 rollout rollback-check --host jetmon-v1-a --bucket-min 0 --bucket-max 4",
		"# HOLD: do not restart v1 unless rollback-check passes.",
		"systemctl start jetmon",
		"# Do not roll back schema migrations.",
		"# Host signoff before moving on or before the fleet dynamic cutover:",
		"./jetmon2 rollout dynamic-check",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	for _, unwanted := range []string{
		"systemd-analyze verify",
		"rollout pinned-check",
	} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("output contains redundant %q:\n%s", unwanted, out.String())
		}
	}
}

func TestRunRolloutRehearsalPlanFreshServerRuntimeHost(t *testing.T) {
	input := strings.NewReader(`
host,bucket_min,bucket_max
jetmon-v1-a,0,9
`)
	opts := rolloutRehearsalPlanOptions{
		Mode:        "fresh-server",
		PlanFile:    "rollout-buckets.csv",
		HostID:      "jetmon-v1-a",
		RuntimeHost: "jetmon-v2-a",
		BucketMin:   0,
		BucketMax:   9,
		BucketTotal: 10,
		Binary:      "/opt/jetmon2/jetmon2",
		Service:     "jetmon2",
		SystemdUnit: "/tmp/staged/jetmon2.service",
		Since:       "20m",
		V1StopCmd:   "ssh jetmon-v1-a sudo systemctl stop jetmon",
		V1StartCmd:  "ssh jetmon-v1-a sudo systemctl start jetmon",
	}

	var out bytes.Buffer
	if err := runRolloutRehearsalPlan(&out, input, opts); err != nil {
		t.Fatalf("runRolloutRehearsalPlan: %v", err)
	}
	for _, want := range []string{
		"INFO mode=fresh-server",
		`INFO plan_host="jetmon-v1-a" runtime_host="jetmon-v2-a" range=0-9`,
		"# Run this runbook from the staged v2 runtime host, not from a separate orchestrator host.",
		"# Fresh-server mode requires jetmon-v2-a to have SSH access to old v1 host jetmon-v1-a for any v1 stop/start commands that use ssh.",
		"/opt/jetmon2/jetmon2 rollout static-plan-check --file rollout-buckets.csv --host jetmon-v1-a --bucket-min 0 --bucket-max 9 --bucket-total 10",
		"/opt/jetmon2/jetmon2 rollout host-preflight --file rollout-buckets.csv --host jetmon-v1-a --runtime-host jetmon-v2-a --bucket-min 0 --bucket-max 9 --bucket-total 10 --systemd-unit /tmp/staged/jetmon2.service",
		"ssh jetmon-v1-a sudo systemctl stop jetmon",
		"# HOLD: confirm v1 on jetmon-v1-a is stopped before starting v2 on jetmon-v2-a.",
		"/opt/jetmon2/jetmon2 rollout cutover-check --host jetmon-v2-a --bucket-min 0 --bucket-max 9 --since 20m",
		"/opt/jetmon2/jetmon2 rollout rollback-check --host jetmon-v2-a --bucket-min 0 --bucket-max 9",
		"ssh jetmon-v1-a sudo systemctl start jetmon",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	for _, unwanted := range []string{
		"systemd-analyze verify",
		"rollout pinned-check",
		"--service jetmon2",
	} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("output contains redundant %q:\n%s", unwanted, out.String())
		}
	}
}

func TestRunHostPreflightSuccess(t *testing.T) {
	input := strings.NewReader(`
host,bucket_min,bucket_max
jetmon-v1-a,0,4
jetmon-v1-b,5,9
`)
	cfg := pinnedRolloutTestConfig(0, 4)
	var gotUnit string
	deps := hostPreflightDeps{
		Pinned: successfulPinnedRolloutDeps(),
		SystemdVerify: func(unit string) (string, error) {
			gotUnit = unit
			return "unit verified", nil
		},
	}

	var out bytes.Buffer
	err := runHostPreflight(context.Background(), &out, cfg, input, hostPreflightOptions{
		PlanFile:    "rollout-buckets.csv",
		HostID:      "jetmon-v1-a",
		RuntimeHost: "host-a",
		BucketMin:   0,
		BucketMax:   4,
		BucketTotal: 10,
		Service:     "jetmon2",
	}, deps)
	if err != nil {
		t.Fatalf("runHostPreflight: %v", err)
	}
	if gotUnit != "/etc/systemd/system/jetmon2.service" {
		t.Fatalf("systemd unit = %q, want default jetmon2 unit", gotUnit)
	}
	for _, want := range []string{
		"## static bucket plan",
		"PASS static_plan_file=rollout-buckets.csv ranges=2",
		"PASS static_plan_host=\"jetmon-v1-a\" range=0-4",
		"## pinned pre-stop safety",
		"PASS pinned_range_matches_request=0-4",
		"pinned rollout check passed",
		"## systemd unit",
		"PASS systemd_unit=/etc/systemd/system/jetmon2.service",
		"INFO systemd_verify=unit verified",
		"PASS pre_stop_gate=ready",
		"host preflight passed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunHostPreflightSkipSystemd(t *testing.T) {
	input := strings.NewReader(`host,bucket_min,bucket_max
jetmon-v1-a,0,4
`)
	cfg := pinnedRolloutTestConfig(0, 4)
	deps := hostPreflightDeps{
		Pinned: successfulPinnedRolloutDeps(),
		SystemdVerify: func(string) (string, error) {
			t.Fatal("systemd verifier should not be called")
			return "", nil
		},
	}

	var out bytes.Buffer
	err := runHostPreflight(context.Background(), &out, cfg, input, hostPreflightOptions{
		PlanFile:    "rollout-buckets.csv",
		HostID:      "jetmon-v1-a",
		RuntimeHost: "host-a",
		BucketMin:   0,
		BucketMax:   4,
		BucketTotal: 5,
		SkipSystemd: true,
	}, deps)
	if err != nil {
		t.Fatalf("runHostPreflight: %v", err)
	}
	if !strings.Contains(out.String(), "INFO systemd_verify=skipped reason=operator") {
		t.Fatalf("output missing systemd skip:\n%s", out.String())
	}
}

func TestRunHostPreflightFailures(t *testing.T) {
	validInput := `host,bucket_min,bucket_max
jetmon-v1-a,0,4
`
	cfg := pinnedRolloutTestConfig(0, 4)

	tests := []struct {
		name  string
		input string
		opts  hostPreflightOptions
		deps  hostPreflightDeps
		cfg   *config.Config
		want  string
	}{
		{
			name:  "missing host",
			input: validInput,
			opts:  hostPreflightOptions{PlanFile: "rollout-buckets.csv", BucketMin: 0, BucketMax: 4, BucketTotal: 5, SkipSystemd: true},
			deps:  hostPreflightDeps{Pinned: successfulPinnedRolloutDeps()},
			want:  "--host is required",
		},
		{
			name:  "plan mismatch",
			input: validInput,
			opts:  hostPreflightOptions{PlanFile: "rollout-buckets.csv", HostID: "jetmon-v1-a", BucketMin: 1, BucketMax: 4, BucketTotal: 5, SkipSystemd: true},
			deps:  hostPreflightDeps{Pinned: successfulPinnedRolloutDeps()},
			want:  "has bucket range 0-4",
		},
		{
			name:  "systemd failure",
			input: validInput,
			opts:  hostPreflightOptions{PlanFile: "rollout-buckets.csv", HostID: "jetmon-v1-a", RuntimeHost: "host-a", BucketMin: 0, BucketMax: 4, BucketTotal: 5, SystemdUnit: "/tmp/bad.service"},
			deps: hostPreflightDeps{
				Pinned: successfulPinnedRolloutDeps(),
				SystemdVerify: func(string) (string, error) {
					return "bad unit", errors.New("exit status 1")
				},
			},
			want: "systemd-analyze verify /tmp/bad.service",
		},
		{
			name:  "config range mismatch",
			input: "host,bucket_min,bucket_max\njetmon-v1-a,0,5\n",
			opts:  hostPreflightOptions{PlanFile: "rollout-buckets.csv", HostID: "jetmon-v1-a", RuntimeHost: "host-a", BucketMin: 0, BucketMax: 5, BucketTotal: 6, SkipSystemd: true},
			deps:  hostPreflightDeps{Pinned: successfulPinnedRolloutDeps()},
			cfg:   pinnedRolloutTestConfig(0, 4),
			want:  "config pinned range 0-4 does not match requested bucket range 0-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			testConfig := cfg
			if tt.cfg != nil {
				testConfig = tt.cfg
			}
			err := runHostPreflight(context.Background(), &out, testConfig, strings.NewReader(tt.input), tt.opts, tt.deps)
			if err == nil {
				t.Fatal("runHostPreflight succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestRunRolloutRehearsalPlanFailures(t *testing.T) {
	validInput := `host,bucket_min,bucket_max
jetmon-v1-a,0,9
`
	tests := []struct {
		name  string
		input string
		opts  rolloutRehearsalPlanOptions
		want  string
	}{
		{
			name:  "bad mode",
			input: validInput,
			opts: rolloutRehearsalPlanOptions{
				Mode:        "auto",
				PlanFile:    "rollout-buckets.csv",
				HostID:      "jetmon-v1-a",
				BucketMin:   0,
				BucketMax:   9,
				BucketTotal: 10,
			},
			want: "--mode must be",
		},
		{
			name:  "missing range",
			input: validInput,
			opts: rolloutRehearsalPlanOptions{
				Mode:        "same-server",
				PlanFile:    "rollout-buckets.csv",
				HostID:      "jetmon-v1-a",
				BucketMin:   -1,
				BucketMax:   9,
				BucketTotal: 10,
			},
			want: "--bucket-min and --bucket-max are required",
		},
		{
			name:  "host not in plan",
			input: validInput,
			opts: rolloutRehearsalPlanOptions{
				Mode:        "same-server",
				PlanFile:    "rollout-buckets.csv",
				HostID:      "jetmon-v1-b",
				BucketMin:   0,
				BucketMax:   9,
				BucketTotal: 10,
			},
			want: "not present",
		},
		{
			name:  "range mismatch",
			input: validInput,
			opts: rolloutRehearsalPlanOptions{
				Mode:        "same-server",
				PlanFile:    "rollout-buckets.csv",
				HostID:      "jetmon-v1-a",
				BucketMin:   1,
				BucketMax:   9,
				BucketTotal: 10,
			},
			want: "want 1-9",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runRolloutRehearsalPlan(&out, strings.NewReader(tt.input), tt.opts)
			if err == nil {
				t.Fatal("runRolloutRehearsalPlan succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestRunGuidedRolloutDryRunChecksLogDir(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.DryRun = true
	opts.BucketTotal = 0
	var called bool
	deps := guidedRolloutTestDeps(t)
	deps.ResolveBucketTotal = func(context.Context) (int, error) {
		return 10, nil
	}
	deps.StaticPlanCheck = func(context.Context, io.Writer, guidedRolloutOptions) error {
		called = true
		return nil
	}

	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(""), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v", err)
	}
	if called {
		t.Fatal("dry run executed static plan check")
	}
	for _, want := range []string{
		"PASS rollout_log_dir_writable=",
		"INFO rollout_log=",
		"INFO rollout_state=",
		"INFO dry_run=true",
		`INFO guided_run_origin=runtime_host mode="same-server" v1_host="jetmon-v1-a" runtime_host="jetmon-v1-a"`,
		"INFO run_this_command_from=runtime_host",
		"INFO remote_v1_access_required=false reason=same_server",
		"INFO selected_path=forward",
		`PLAN path=FORWARD step=static-plan-check`,
		`PLAN path=FORWARD step=stop-v1 command="systemctl stop jetmon"`,
		`PLAN path=FORWARD step=stop-v1 typed_confirmation="STOP jetmon-v1-a 0-4"`,
		`PLAN path=FORWARD step=stop-v1 manual_checkpoint="DONE after v1 is stopped and the process is no longer running"`,
		`PLAN path=ROLLBACK step=rollback-start-v1`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if _, err := os.Stat(guidedRolloutStatePath(normalizeGuidedOptionsForTest(t, opts))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run state file exists or stat failed: %v", err)
	}
}

func TestRunGuidedRolloutLogDirWriteFailure(t *testing.T) {
	tempDir := t.TempDir()
	logDirFile := tempDir + "/not-a-directory"
	if err := os.WriteFile(logDirFile, []byte("not a dir"), 0600); err != nil {
		t.Fatalf("write logDirFile: %v", err)
	}
	opts := guidedRolloutTestOptions(t)
	opts.LogDir = logDirFile
	opts.DryRun = true

	var out bytes.Buffer
	err := runGuidedRollout(context.Background(), &out, strings.NewReader(""), opts, guidedRolloutTestDeps(t))
	if err == nil {
		t.Fatal("runGuidedRollout succeeded")
	}
	if !strings.Contains(err.Error(), "create rollout log directory") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestRunGuidedRolloutRollbackDryRunOnlyShowsRollbackPath(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.Rollback = true
	opts.DryRun = true

	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(""), opts, guidedRolloutTestDeps(t)); err != nil {
		t.Fatalf("runGuidedRollout: %v", err)
	}
	for _, want := range []string{
		"INFO selected_path=rollback",
		`PLAN path=ROLLBACK step=rollback-stop-v2 command="systemctl stop jetmon2 && ! systemctl is-active --quiet jetmon2"`,
		`PLAN path=ROLLBACK step=rollback-start-v1 typed_confirmation="START V1 jetmon-v1-a 0-4"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "path=FORWARD") {
		t.Fatalf("rollback dry-run included forward path:\n%s", out.String())
	}
}

func TestRunGuidedRolloutDryRunExecuteModeDoesNotRunCommands(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.DryRun = true
	opts.ExecuteOperatorCommands = true
	deps := guidedRolloutTestDeps(t)
	deps.ExecCommand = func(context.Context, string) (string, error) {
		t.Fatal("dry-run executed operator command")
		return "", nil
	}

	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(""), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v", err)
	}
	for _, want := range []string{
		"INFO operator_command_mode=execute-after-confirmation",
		`PLAN path=FORWARD step=stop-v1 command="systemctl stop jetmon"`,
		`PLAN path=FORWARD step=start-v2 command="systemctl enable --now jetmon2 && systemctl is-active --quiet jetmon2"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "manual_checkpoint=") {
		t.Fatalf("execute-mode dry-run should not print manual checkpoints:\n%s", out.String())
	}
}

func TestRunGuidedRolloutFreshServerDryRunShowsRemoteV1Commands(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.DryRun = true
	opts.Mode = "fresh-server"
	opts.RuntimeHost = "jetmon-v2-a"
	opts.V1StopCmd = "ssh jetmon-v1-a sudo systemctl stop jetmon"
	opts.V1StartCmd = "ssh jetmon-v1-a sudo systemctl start jetmon"

	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(""), opts, guidedRolloutTestDeps(t)); err != nil {
		t.Fatalf("runGuidedRollout: %v", err)
	}
	for _, want := range []string{
		`INFO rollout_state=`,
		`INFO guided_run_origin=runtime_host mode="fresh-server" v1_host="jetmon-v1-a" runtime_host="jetmon-v2-a"`,
		`WARN remote_v1_access_required=true runtime_host="jetmon-v2-a" v1_host="jetmon-v1-a"`,
		`PLAN path=FORWARD step=stop-v1 command="ssh jetmon-v1-a sudo systemctl stop jetmon"`,
		`PLAN path=FORWARD step=start-v2 typed_confirmation="START V2 jetmon-v2-a 0-4"`,
		`PLAN path=ROLLBACK step=rollback-stop-v2 typed_confirmation="STOP V2 jetmon-v2-a 0-4"`,
		`PLAN path=ROLLBACK step=rollback-start-v1 command="ssh jetmon-v1-a sudo systemctl start jetmon"`,
		`PLAN path=ROLLBACK step=rollback-start-v1 typed_confirmation="START V1 jetmon-v1-a 0-4"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(out.String(), "jetmon-v2-a-0-4.state.json") {
		t.Fatalf("fresh-server state path should use runtime host:\n%s", out.String())
	}
}

func TestRunGuidedRolloutForwardExecuteCommands(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.ExecuteOperatorCommands = true
	deps := guidedRolloutTestDeps(t)
	var calls []string
	deps.StaticPlanCheck = func(context.Context, io.Writer, guidedRolloutOptions) error {
		calls = append(calls, "static")
		return nil
	}
	deps.ValidateConfig = func(context.Context, io.Writer, guidedRolloutOptions) error {
		calls = append(calls, "validate")
		return nil
	}
	deps.HostPreflight = func(context.Context, io.Writer, guidedRolloutOptions) error {
		calls = append(calls, "preflight")
		return nil
	}
	deps.CutoverCheck = func(_ context.Context, _ io.Writer, _ guidedRolloutOptions, requireAll bool) error {
		if requireAll {
			calls = append(calls, "cutover-all")
		} else {
			calls = append(calls, "cutover-smoke")
		}
		return nil
	}
	var commands []string
	deps.ExecCommand = func(_ context.Context, command string) (string, error) {
		commands = append(commands, command)
		return "", nil
	}

	input := strings.Join([]string{
		"y",
		"y",
		"y",
		"STOP jetmon-v1-a 0-4",
		"START V2 jetmon-v1-a 0-4",
		"y",
		"READY",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	if got, want := strings.Join(calls, ","), "static,validate,preflight,cutover-smoke,cutover-all"; got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
	if got, want := strings.Join(commands, ","), "systemctl stop jetmon,systemctl enable --now jetmon2 && systemctl is-active --quiet jetmon2"; got != want {
		t.Fatalf("commands = %s, want %s", got, want)
	}
	stopCommandAt := strings.Index(out.String(), "COMMAND systemctl stop jetmon")
	stopConfirmAt := strings.Index(out.String(), "Type STOP jetmon-v1-a 0-4 to continue:")
	if stopCommandAt < 0 || stopConfirmAt < 0 || stopCommandAt > stopConfirmAt {
		t.Fatalf("stop command should be shown before typed confirmation:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "PASS guided_rollout=complete") {
		t.Fatalf("output missing completion:\n%s", out.String())
	}
	state := readGuidedStateForTest(t, opts)
	if state.LastCompletedStep != "cutover-require-all" || !state.V1Stopped || !state.V2Started {
		t.Fatalf("state = %+v", state)
	}
	if !state.V1StateKnown || !state.V2StateKnown {
		t.Fatalf("state did not mark service state as known: %+v", state)
	}
}

func TestRunGuidedRolloutFreshServerManualFlowPrintsRemoteAndLocalCommands(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.Mode = "fresh-server"
	opts.RuntimeHost = "jetmon-v2-a"
	opts.V1StopCmd = "ssh jetmon-v1-a sudo systemctl stop jetmon"
	opts.V1StartCmd = "ssh jetmon-v1-a sudo systemctl start jetmon"
	deps := guidedRolloutTestDeps(t)
	deps.ExecCommand = func(context.Context, string) (string, error) {
		t.Fatal("manual mode executed operator command")
		return "", nil
	}

	input := strings.Join([]string{
		"y",
		"y",
		"y",
		"STOP jetmon-v1-a 0-4",
		"DONE",
		"START V2 jetmon-v2-a 0-4",
		"DONE",
		"y",
		"READY",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		`INFO guided_run_origin=runtime_host mode="fresh-server" v1_host="jetmon-v1-a" runtime_host="jetmon-v2-a"`,
		`WARN remote_v1_access_required=true runtime_host="jetmon-v2-a" v1_host="jetmon-v1-a"`,
		"COMMAND ssh jetmon-v1-a sudo systemctl stop jetmon",
		"COMMAND systemctl enable --now jetmon2 && systemctl is-active --quiet jetmon2",
		"INFO executing_operator_command=false",
		"PASS guided_rollout=complete",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	stopAt := strings.Index(out.String(), "COMMAND ssh jetmon-v1-a sudo systemctl stop jetmon")
	startAt := strings.Index(out.String(), "COMMAND systemctl enable --now jetmon2 && systemctl is-active --quiet jetmon2")
	if stopAt < 0 || startAt < 0 || stopAt > startAt {
		t.Fatalf("fresh-server command order is wrong:\n%s", out.String())
	}
	state := readGuidedStateForTest(t, opts)
	if state.RuntimeHost != "jetmon-v2-a" || !state.V1Stopped || !state.V2Started {
		t.Fatalf("state = %+v", state)
	}
}

func TestRunGuidedRolloutFreshServerExecuteFlowCommandOrder(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.Mode = "fresh-server"
	opts.RuntimeHost = "jetmon-v2-a"
	opts.V1StopCmd = "ssh jetmon-v1-a sudo systemctl stop jetmon"
	opts.V1StartCmd = "ssh jetmon-v1-a sudo systemctl start jetmon"
	opts.ExecuteOperatorCommands = true
	deps := guidedRolloutTestDeps(t)
	var commands []string
	deps.ExecCommand = func(_ context.Context, command string) (string, error) {
		commands = append(commands, command)
		return "", nil
	}

	input := strings.Join([]string{
		"y",
		"y",
		"y",
		"STOP jetmon-v1-a 0-4",
		"START V2 jetmon-v2-a 0-4",
		"y",
		"READY",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	if got, want := strings.Join(commands, ","), "ssh jetmon-v1-a sudo systemctl stop jetmon,systemctl enable --now jetmon2 && systemctl is-active --quiet jetmon2"; got != want {
		t.Fatalf("commands = %s, want %s", got, want)
	}
	if !strings.Contains(out.String(), "PASS guided_rollout=complete") {
		t.Fatalf("output missing completion:\n%s", out.String())
	}
}

func TestRunGuidedRolloutWrongConfirmationDoesNotExecuteCommand(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.ExecuteOperatorCommands = true
	deps := guidedRolloutTestDeps(t)
	var commands []string
	deps.ExecCommand = func(_ context.Context, command string) (string, error) {
		commands = append(commands, command)
		return "", nil
	}

	input := strings.Join([]string{
		"y",
		"y",
		"y",
		"STOP wrong-host 0-4",
		"s",
		"",
	}, "\n")
	var out bytes.Buffer
	err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps)
	if err == nil {
		t.Fatal("runGuidedRollout succeeded")
	}
	if len(commands) != 0 {
		t.Fatalf("commands executed after wrong confirmation: %v", commands)
	}
	if !strings.Contains(err.Error(), `confirmation did not match "STOP jetmon-v1-a 0-4"`) {
		t.Fatalf("error = %q", err.Error())
	}
	if !strings.Contains(out.String(), "COMMAND systemctl stop jetmon") {
		t.Fatalf("output should show command before confirmation failure:\n%s", out.String())
	}
}

func TestRunGuidedRolloutRollbackExecuteCommands(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.Rollback = true
	opts.ExecuteOperatorCommands = true
	deps := guidedRolloutTestDeps(t)
	var calls []string
	deps.RollbackCheck = func(context.Context, io.Writer, guidedRolloutOptions) error {
		calls = append(calls, "rollback-check")
		return nil
	}
	var commands []string
	deps.ExecCommand = func(_ context.Context, command string) (string, error) {
		commands = append(commands, command)
		return "", nil
	}

	input := strings.Join([]string{
		"STOP V2 jetmon-v1-a 0-4",
		"y",
		"START V1 jetmon-v1-a 0-4",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	if got, want := strings.Join(calls, ","), "rollback-check"; got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
	if got, want := strings.Join(commands, ","), "systemctl stop jetmon2 && ! systemctl is-active --quiet jetmon2,systemctl start jetmon"; got != want {
		t.Fatalf("commands = %s, want %s", got, want)
	}
	if !strings.Contains(out.String(), "PASS guided_rollback=complete") {
		t.Fatalf("output missing rollback completion:\n%s", out.String())
	}
}

func TestRunGuidedRolloutFreshServerRollbackExecuteCommands(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.Mode = "fresh-server"
	opts.RuntimeHost = "jetmon-v2-a"
	opts.V1StartCmd = "ssh jetmon-v1-a sudo systemctl start jetmon"
	opts.Rollback = true
	opts.ExecuteOperatorCommands = true
	deps := guidedRolloutTestDeps(t)
	var commands []string
	deps.ExecCommand = func(_ context.Context, command string) (string, error) {
		commands = append(commands, command)
		return "", nil
	}

	input := strings.Join([]string{
		"STOP V2 jetmon-v2-a 0-4",
		"y",
		"START V1 jetmon-v1-a 0-4",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	if got, want := strings.Join(commands, ","), "systemctl stop jetmon2 && ! systemctl is-active --quiet jetmon2,ssh jetmon-v1-a sudo systemctl start jetmon"; got != want {
		t.Fatalf("commands = %s, want %s", got, want)
	}
	if !strings.Contains(out.String(), `WARN remote_v1_access_required=true runtime_host="jetmon-v2-a" v1_host="jetmon-v1-a"`) {
		t.Fatalf("output missing remote access warning:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "PASS guided_rollback=complete") {
		t.Fatalf("output missing rollback completion:\n%s", out.String())
	}
}

func TestRunGuidedRolloutFailureAfterV2CanRollback(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	deps := guidedRolloutTestDeps(t)
	var cutoverCalls int
	deps.CutoverCheck = func(_ context.Context, _ io.Writer, _ guidedRolloutOptions, requireAll bool) error {
		cutoverCalls++
		if !requireAll {
			return errors.New("cutover smoke failed")
		}
		return nil
	}

	input := strings.Join([]string{
		"y",
		"y",
		"y",
		"STOP jetmon-v1-a 0-4",
		"DONE",
		"START V2 jetmon-v1-a 0-4",
		"DONE",
		"y",
		"b",
		"STOP V2 jetmon-v1-a 0-4",
		"DONE",
		"y",
		"START V1 jetmon-v1-a 0-4",
		"DONE",
		"",
	}, "\n")
	var out bytes.Buffer
	err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps)
	if !errors.Is(err, errGuidedForwardRolledBack) {
		t.Fatalf("error = %v, want errGuidedForwardRolledBack\n%s", err, out.String())
	}
	if cutoverCalls != 1 {
		t.Fatalf("cutover calls = %d, want 1", cutoverCalls)
	}
	for _, want := range []string{
		"Options: [r] retry this step, [b] begin guided rollback, [s] stop here",
		"PASS guided_rollback=complete",
		"FAIL guided_rollout=rolled_back reason=operator_requested_after_failed_step",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	state := readGuidedStateForTest(t, opts)
	if state.V1Stopped || state.V2Started || state.LastCompletedStep != "rollback-start-v1" {
		t.Fatalf("state after rollback = %+v", state)
	}
}

func TestRunGuidedRolloutFailureCanStop(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	deps := guidedRolloutTestDeps(t)
	deps.StaticPlanCheck = func(context.Context, io.Writer, guidedRolloutOptions) error {
		return errors.New("static mismatch")
	}

	input := strings.Join([]string{"y", "s", ""}, "\n")
	var out bytes.Buffer
	err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps)
	if err == nil {
		t.Fatal("runGuidedRollout succeeded")
	}
	if !strings.Contains(err.Error(), "static mismatch") {
		t.Fatalf("error = %q", err.Error())
	}
	if !strings.Contains(out.String(), "Options: [r] retry this step, [s] stop here") {
		t.Fatalf("output missing failure options:\n%s", out.String())
	}
}

func TestRunGuidedRolloutResumeSkipsCompletedSteps(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	normalized := normalizeGuidedOptionsForTest(t, opts)
	state := newGuidedRolloutState(normalized, time.Date(2026, 4, 29, 17, 0, 0, 0, time.UTC))
	state.CompletedSteps = []string{"static-plan-check", "validate-config"}
	state.LastCompletedStep = "validate-config"
	writeGuidedStateForTest(t, normalized, state)

	deps := guidedRolloutTestDeps(t)
	var calls []string
	deps.StaticPlanCheck = func(context.Context, io.Writer, guidedRolloutOptions) error {
		calls = append(calls, "static")
		return nil
	}
	deps.ValidateConfig = func(context.Context, io.Writer, guidedRolloutOptions) error {
		calls = append(calls, "validate")
		return nil
	}
	deps.HostPreflight = func(context.Context, io.Writer, guidedRolloutOptions) error {
		calls = append(calls, "preflight")
		return nil
	}
	deps.CutoverCheck = func(_ context.Context, _ io.Writer, _ guidedRolloutOptions, requireAll bool) error {
		if requireAll {
			calls = append(calls, "cutover-all")
		} else {
			calls = append(calls, "cutover-smoke")
		}
		return nil
	}

	input := strings.Join([]string{
		"RESUME",
		"y",
		"STOP jetmon-v1-a 0-4",
		"DONE",
		"START V2 jetmon-v1-a 0-4",
		"DONE",
		"y",
		"READY",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	if strings.Contains(strings.Join(calls, ","), "static") || strings.Contains(strings.Join(calls, ","), "validate") {
		t.Fatalf("resume reran completed calls: %v", calls)
	}
	if got, want := strings.Join(calls, ","), "preflight,cutover-smoke,cutover-all"; got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
	if !strings.Contains(out.String(), "SKIP step=static-plan-check reason=completed_from_state") {
		t.Fatalf("output missing resume skip:\n%s", out.String())
	}
}

func TestRunGuidedRolloutResumeStateRequiresExplicitChoice(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	normalized := normalizeGuidedOptionsForTest(t, opts)
	state := newGuidedRolloutState(normalized, time.Date(2026, 4, 29, 17, 0, 0, 0, time.UTC))
	state.CompletedSteps = []string{
		"static-plan-check",
		"validate-config",
		"host-preflight",
		"stop-v1",
		"start-v2",
		"cutover-smoke",
		"cutover-require-all",
	}
	state.LastCompletedStep = "cutover-require-all"
	state.V1Stopped = true
	state.V1StateKnown = true
	state.V2Started = true
	state.V2StateKnown = true
	writeGuidedStateForTest(t, normalized, state)

	var out bytes.Buffer
	input := strings.Join([]string{"", "RESUME", ""}, "\n")
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, guidedRolloutTestDeps(t)); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "No default is selected when state exists") {
		t.Fatalf("output missing no-default warning:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "PASS guided_rollout=complete") {
		t.Fatalf("output missing completion:\n%s", out.String())
	}
}

func TestRunGuidedRolloutResumeStateRejectsYNAliases(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	normalized := normalizeGuidedOptionsForTest(t, opts)
	state := newGuidedRolloutState(normalized, time.Date(2026, 4, 29, 17, 0, 0, 0, time.UTC))
	state.CompletedSteps = []string{
		"static-plan-check",
		"validate-config",
		"host-preflight",
		"stop-v1",
		"start-v2",
		"cutover-smoke",
		"cutover-require-all",
	}
	state.LastCompletedStep = "cutover-require-all"
	state.V1Stopped = true
	state.V1StateKnown = true
	state.V2Started = true
	state.V2StateKnown = true
	writeGuidedStateForTest(t, normalized, state)

	var out bytes.Buffer
	input := strings.Join([]string{"n", "y", "RESUME", ""}, "\n")
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, guidedRolloutTestDeps(t)); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	if strings.Count(out.String(), "Please choose RESUME or START OVER.") != 2 {
		t.Fatalf("output should reject y/n aliases:\n%s", out.String())
	}
	if strings.Contains(out.String(), "previous_state=discarded") {
		t.Fatalf("y/n alias discarded state:\n%s", out.String())
	}
}

func TestRunGuidedRolloutResumeMismatchedStateRefuses(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	normalized := normalizeGuidedOptionsForTest(t, opts)
	state := newGuidedRolloutState(normalized, time.Date(2026, 4, 29, 17, 0, 0, 0, time.UTC))
	state.HostID = "jetmon-v1-other"
	writeGuidedStateForTest(t, normalized, state)

	var out bytes.Buffer
	err := runGuidedRollout(context.Background(), &out, strings.NewReader("RESUME\n"), opts, guidedRolloutTestDeps(t))
	if err == nil {
		t.Fatal("runGuidedRollout succeeded")
	}
	if !strings.Contains(err.Error(), `state mode="same-server" host="jetmon-v1-other"`) {
		t.Fatalf("error = %q", err.Error())
	}
	if !strings.Contains(out.String(), `INFO previous_state=found mode="same-server" host="jetmon-v1-other"`) {
		t.Fatalf("output missing previous state details:\n%s", out.String())
	}
}

func TestRunGuidedRolloutStartOverDiscardsPreviousState(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	normalized := normalizeGuidedOptionsForTest(t, opts)
	state := newGuidedRolloutState(normalized, time.Date(2026, 4, 29, 17, 0, 0, 0, time.UTC))
	state.CompletedSteps = []string{"static-plan-check", "validate-config"}
	state.LastCompletedStep = "validate-config"
	writeGuidedStateForTest(t, normalized, state)

	deps := guidedRolloutTestDeps(t)
	var calls []string
	deps.StaticPlanCheck = func(context.Context, io.Writer, guidedRolloutOptions) error {
		calls = append(calls, "static")
		return nil
	}
	deps.ValidateConfig = func(context.Context, io.Writer, guidedRolloutOptions) error {
		calls = append(calls, "validate")
		return nil
	}
	deps.HostPreflight = func(context.Context, io.Writer, guidedRolloutOptions) error {
		calls = append(calls, "preflight")
		return nil
	}
	deps.CutoverCheck = func(_ context.Context, _ io.Writer, _ guidedRolloutOptions, requireAll bool) error {
		if requireAll {
			calls = append(calls, "cutover-all")
		} else {
			calls = append(calls, "cutover-smoke")
		}
		return nil
	}

	input := strings.Join([]string{
		"START OVER",
		"y",
		"y",
		"y",
		"STOP jetmon-v1-a 0-4",
		"DONE",
		"START V2 jetmon-v1-a 0-4",
		"DONE",
		"y",
		"READY",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	if got, want := strings.Join(calls, ","), "static,validate,preflight,cutover-smoke,cutover-all"; got != want {
		t.Fatalf("calls = %s, want %s", got, want)
	}
	if !strings.Contains(out.String(), "WARN previous_state=discarded reason=operator_start_over") {
		t.Fatalf("output missing start-over warning:\n%s", out.String())
	}
	if strings.Contains(out.String(), "SKIP step=static-plan-check") {
		t.Fatalf("start-over reused previous completed step:\n%s", out.String())
	}
}

func TestRunGuidedRolloutResumeSkipsAlreadyStoppedV1(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	normalized := normalizeGuidedOptionsForTest(t, opts)
	state := newGuidedRolloutState(normalized, time.Date(2026, 4, 29, 17, 0, 0, 0, time.UTC))
	state.CompletedSteps = []string{"static-plan-check", "validate-config", "host-preflight"}
	state.LastCompletedStep = "host-preflight"
	state.V1Stopped = true
	state.V1StateKnown = true
	writeGuidedStateForTest(t, normalized, state)

	deps := guidedRolloutTestDeps(t)
	var commands []string
	deps.ExecCommand = func(_ context.Context, command string) (string, error) {
		commands = append(commands, command)
		return "", nil
	}

	input := strings.Join([]string{
		"RESUME",
		"START V2 jetmon-v1-a 0-4",
		"DONE",
		"y",
		"READY",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	if strings.Contains(strings.Join(commands, ","), "systemctl stop jetmon") {
		t.Fatalf("resume reran v1 stop command: %v", commands)
	}
	if !strings.Contains(out.String(), "SKIP step=stop-v1 reason=state_v1_already_stopped") {
		t.Fatalf("output missing v1 stopped skip:\n%s", out.String())
	}
}

func TestRunGuidedRolloutRollbackResumeSkipsAlreadyStoppedV2(t *testing.T) {
	opts := guidedRolloutTestOptions(t)
	opts.Rollback = true
	normalized := normalizeGuidedOptionsForTest(t, opts)
	state := newGuidedRolloutState(normalized, time.Date(2026, 4, 29, 17, 0, 0, 0, time.UTC))
	state.V2Started = false
	state.V2StateKnown = true
	writeGuidedStateForTest(t, normalized, state)

	deps := guidedRolloutTestDeps(t)
	var commands []string
	deps.ExecCommand = func(_ context.Context, command string) (string, error) {
		commands = append(commands, command)
		return "", nil
	}

	input := strings.Join([]string{
		"RESUME",
		"y",
		"START V1 jetmon-v1-a 0-4",
		"DONE",
		"",
	}, "\n")
	var out bytes.Buffer
	if err := runGuidedRollout(context.Background(), &out, strings.NewReader(input), opts, deps); err != nil {
		t.Fatalf("runGuidedRollout: %v\n%s", err, out.String())
	}
	if strings.Contains(strings.Join(commands, ","), "systemctl stop jetmon2") {
		t.Fatalf("resume reran v2 stop command: %v", commands)
	}
	if !strings.Contains(out.String(), "SKIP step=rollback-stop-v2 reason=state_v2_already_stopped") {
		t.Fatalf("output missing v2 stopped skip:\n%s", out.String())
	}
}

func guidedRolloutTestOptions(t *testing.T) guidedRolloutOptions {
	t.Helper()
	return guidedRolloutOptions{
		Mode:        "same-server",
		PlanFile:    "rollout-buckets.csv",
		HostID:      "jetmon-v1-a",
		RuntimeHost: "jetmon-v1-a",
		BucketMin:   0,
		BucketMax:   4,
		BucketTotal: 10,
		Service:     "jetmon2",
		Since:       "15m",
		V1StopCmd:   "systemctl stop jetmon",
		V1StartCmd:  "systemctl start jetmon",
		LogDir:      t.TempDir(),
	}
}

func guidedRolloutTestDeps(t *testing.T) guidedRolloutDeps {
	t.Helper()
	return guidedRolloutDeps{
		Now: func() time.Time {
			return time.Date(2026, 4, 29, 17, 30, 0, 0, time.UTC)
		},
		ResolveBucketTotal: func(context.Context) (int, error) {
			return 10, nil
		},
		StaticPlanCheck: func(context.Context, io.Writer, guidedRolloutOptions) error {
			return nil
		},
		ValidateConfig: func(context.Context, io.Writer, guidedRolloutOptions) error {
			return nil
		},
		HostPreflight: func(context.Context, io.Writer, guidedRolloutOptions) error {
			return nil
		},
		CutoverCheck: func(context.Context, io.Writer, guidedRolloutOptions, bool) error {
			return nil
		},
		RollbackCheck: func(context.Context, io.Writer, guidedRolloutOptions) error {
			return nil
		},
		ExecCommand: func(context.Context, string) (string, error) {
			return "", nil
		},
	}
}

func normalizeGuidedOptionsForTest(t *testing.T, opts guidedRolloutOptions) guidedRolloutOptions {
	t.Helper()
	normalized, err := normalizeGuidedRolloutOptions(opts)
	if err != nil {
		t.Fatalf("normalizeGuidedRolloutOptions: %v", err)
	}
	return normalized
}

func readGuidedStateForTest(t *testing.T, opts guidedRolloutOptions) guidedRolloutState {
	t.Helper()
	normalized := normalizeGuidedOptionsForTest(t, opts)
	data, err := os.ReadFile(guidedRolloutStatePath(normalized))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var state guidedRolloutState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return state
}

func writeGuidedStateForTest(t *testing.T, opts guidedRolloutOptions, state guidedRolloutState) {
	t.Helper()
	if err := os.MkdirAll(opts.LogDir, 0750); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("encode state: %v", err)
	}
	if err := os.WriteFile(guidedRolloutStatePath(opts), data, 0600); err != nil {
		t.Fatalf("write state: %v", err)
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

func TestRunCutoverCheckSuccess(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	cfg := pinnedRolloutTestConfig(12, 34)
	cfg.DashboardPort = 8080

	deps := successfulCutoverCheckDeps(now)
	var gotStatusPort int
	deps.Status = func(port int) (string, error) {
		gotStatusPort = port
		return "{\n  \"state\": \"ok\"\n}", nil
	}

	var out bytes.Buffer
	err := runCutoverCheck(context.Background(), &out, cfg, cutoverCheckOptions{
		HostOverride: "host-a",
		BucketMin:    -1,
		BucketMax:    -1,
		Since:        "15m",
		Limit:        100,
		StatusPort:   -1,
	}, deps)
	if err != nil {
		t.Fatalf("runCutoverCheck: %v", err)
	}
	if gotStatusPort != 8080 {
		t.Fatalf("status port = %d, want 8080", gotStatusPort)
	}
	for _, want := range []string{
		"## pinned preflight",
		"pinned rollout check passed",
		"## activity check",
		"PASS rollout_activity=recent_checks_present",
		"## dashboard status",
		"PASS dashboard_status=http://localhost:8080/api/state",
		`INFO dashboard_state={ "state": "ok" }`,
		"## projection drift",
		"PASS legacy_projection_drift=0",
		"cutover check passed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunCutoverCheckRequireAllAndSkipStatus(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	cfg := pinnedRolloutTestConfig(12, 34)
	deps := successfulCutoverCheckDeps(now)
	deps.Status = func(int) (string, error) {
		t.Fatal("status should not be called")
		return "", nil
	}

	var out bytes.Buffer
	err := runCutoverCheck(context.Background(), &out, cfg, cutoverCheckOptions{
		BucketMin:  -1,
		BucketMax:  -1,
		Since:      "15m",
		RequireAll: true,
		Limit:      100,
		StatusPort: -1,
		SkipStatus: true,
	}, deps)
	if err != nil {
		t.Fatalf("runCutoverCheck: %v", err)
	}
	for _, want := range []string{
		"PASS rollout_activity=all_active_sites_checked",
		"INFO dashboard_status=skipped reason=operator",
		"cutover check passed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunCutoverCheckSkipsDisabledDashboard(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	cfg := pinnedRolloutTestConfig(12, 34)
	cfg.DashboardPort = 0
	deps := successfulCutoverCheckDeps(now)
	deps.Status = func(int) (string, error) {
		t.Fatal("status should not be called")
		return "", nil
	}

	var out bytes.Buffer
	if err := runCutoverCheck(context.Background(), &out, cfg, cutoverCheckOptions{BucketMin: -1, BucketMax: -1, Since: "15m", Limit: 100, StatusPort: -1}, deps); err != nil {
		t.Fatalf("runCutoverCheck: %v", err)
	}
	if !strings.Contains(out.String(), "INFO dashboard_status=skipped dashboard_port=disabled") {
		t.Fatalf("output missing disabled dashboard skip:\n%s", out.String())
	}
}

func TestRunCutoverCheckFailures(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	cfg := pinnedRolloutTestConfig(12, 34)
	cfg.DashboardPort = 8080

	tests := []struct {
		name string
		opts cutoverCheckOptions
		deps cutoverCheckDeps
		want string
	}{
		{
			name: "status error",
			opts: cutoverCheckOptions{BucketMin: -1, BucketMax: -1, Since: "15m", Limit: 100, StatusPort: -1},
			deps: func() cutoverCheckDeps {
				deps := successfulCutoverCheckDeps(now)
				deps.Status = func(int) (string, error) {
					return "", errors.New("connection refused")
				}
				return deps
			}(),
			want: "connection refused",
		},
		{
			name: "activity require all failure",
			opts: cutoverCheckOptions{BucketMin: -1, BucketMax: -1, Since: "15m", RequireAll: true, Limit: 100, StatusPort: -1, SkipStatus: true},
			deps: func() cutoverCheckDeps {
				deps := successfulCutoverCheckDeps(now)
				deps.Activity.CountRecentlyCheckedActiveSitesForRange = func(context.Context, int, int, time.Time) (int, error) {
					return 1, nil
				}
				return deps
			}(),
			want: "only 1/3 active sites",
		},
		{
			name: "projection drift",
			opts: cutoverCheckOptions{BucketMin: -1, BucketMax: -1, Since: "15m", Limit: 100, StatusPort: -1, SkipStatus: true},
			deps: func() cutoverCheckDeps {
				deps := successfulCutoverCheckDeps(now)
				deps.Projection.CountLegacyProjectionDrift = func(context.Context, int, int) (int, error) {
					return 2, nil
				}
				deps.Projection.SummarizeLegacyProjectionDrift = func(context.Context, int, int, int) ([]db.ProjectionDriftSummaryRow, error) {
					return []db.ProjectionDriftSummaryRow{
						{BucketNo: 0, SiteStatus: 1, ExpectedStatus: 2, DriftCount: 2, SampleBlogID: 42},
					}, nil
				}
				deps.Projection.ListLegacyProjectionDrift = func(context.Context, int, int, int) ([]db.ProjectionDriftRow, error) {
					return nil, nil
				}
				return deps
			}(),
			want: "legacy projection drift=2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runCutoverCheck(context.Background(), &out, cfg, tt.opts, tt.deps)
			if err == nil {
				t.Fatal("runCutoverCheck succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestBuildRolloutStateReportPinned(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	cfg := pinnedRolloutTestConfig(12, 34)
	cfg.APIPort = 0

	report, err := buildRolloutStateReport(context.Background(), cfg, rolloutStateReportOptions{Since: "15m"}, rolloutStateReportDeps{
		Now:      func() time.Time { return now },
		Hostname: func() string { return "host-a" },
		CountActiveSitesForBucketRange: func(_ context.Context, min, max int) (int, error) {
			if min != 12 || max != 34 {
				t.Fatalf("active range = %d-%d, want 12-34", min, max)
			}
			return 3, nil
		},
		CountRecentlyCheckedActiveSitesForRange: func(_ context.Context, min, max int, cutoff time.Time) (int, error) {
			if min != 12 || max != 34 {
				t.Fatalf("activity range = %d-%d, want 12-34", min, max)
			}
			if !cutoff.Equal(now.Add(-15 * time.Minute)) {
				t.Fatalf("cutoff = %s, want %s", cutoff, now.Add(-15*time.Minute))
			}
			return 3, nil
		},
		CountLegacyProjectionDrift: func(_ context.Context, min, max int) (int, error) {
			if min != 12 || max != 34 {
				t.Fatalf("drift range = %d-%d, want 12-34", min, max)
			}
			return 0, nil
		},
	})
	if err != nil {
		t.Fatalf("buildRolloutStateReport: %v", err)
	}
	if !report.OK {
		t.Fatalf("report.OK = false issues=%v", report.Issues)
	}
	if report.Ownership.Mode != "pinned" || report.BucketCoverage.Status != "pinned_config" {
		t.Fatalf("ownership/coverage = %s/%s", report.Ownership.Mode, report.BucketCoverage.Status)
	}
	if report.Activity.CheckedPercent != 100 {
		t.Fatalf("checked percent = %f, want 100", report.Activity.CheckedPercent)
	}
	if !strings.Contains(report.SuggestedNextAction, "next pinned host") {
		t.Fatalf("suggested action = %q", report.SuggestedNextAction)
	}
}

func TestBuildRolloutStateReportDynamicIssues(t *testing.T) {
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		BucketTotal:                  10,
		BucketHeartbeatGraceSec:      60,
		LegacyStatusProjectionEnable: true,
		APIPort:                      8090,
	}

	report, err := buildRolloutStateReport(context.Background(), cfg, rolloutStateReportOptions{Since: "15m"}, rolloutStateReportDeps{
		Now:      func() time.Time { return now },
		Hostname: func() string { return "host-a" },
		GetAllHosts: func() ([]db.HostRow, error) {
			return []db.HostRow{
				{HostID: "host-a", BucketMin: 0, BucketMax: 4, LastHeartbeat: now, Status: "active"},
				{HostID: "host-b", BucketMin: 6, BucketMax: 9, LastHeartbeat: now, Status: "active"},
			}, nil
		},
		CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
			return 4, nil
		},
		CountRecentlyCheckedActiveSitesForRange: func(context.Context, int, int, time.Time) (int, error) {
			return 1, nil
		},
		CountLegacyProjectionDrift: func(context.Context, int, int) (int, error) {
			return 2, nil
		},
	})
	if err != nil {
		t.Fatalf("buildRolloutStateReport: %v", err)
	}
	if report.OK {
		t.Fatal("report.OK = true, want false")
	}
	for _, want := range []string{
		"dynamic bucket coverage has gap",
		"legacy projection drift=2",
		"1/4 active sites checked",
		"delivery_owner_host is unset",
	} {
		if !strings.Contains(strings.Join(report.Issues, "\n"), want) {
			t.Fatalf("issues missing %q: %#v", want, report.Issues)
		}
	}
	if report.BucketCoverage.Status != "invalid" {
		t.Fatalf("coverage status = %q, want invalid", report.BucketCoverage.Status)
	}
	if !strings.Contains(report.SuggestedNextAction, "Fix jetmon_hosts bucket coverage") {
		t.Fatalf("suggested action = %q", report.SuggestedNextAction)
	}
}

func pinnedRolloutTestConfig(minBucket, maxBucket int) *config.Config {
	return &config.Config{
		PinnedBucketMin:              &minBucket,
		PinnedBucketMax:              &maxBucket,
		LegacyStatusProjectionEnable: true,
	}
}

func successfulCutoverCheckDeps(now time.Time) cutoverCheckDeps {
	deps := cutoverCheckDeps{
		Pinned: successfulPinnedRolloutDeps(),
		Activity: activityCheckDeps{
			Now: func() time.Time { return now },
			CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
				return 3, nil
			},
			CountRecentlyCheckedActiveSitesForRange: func(context.Context, int, int, time.Time) (int, error) {
				return 3, nil
			},
		},
		Projection: projectionDriftDeps{
			CountLegacyProjectionDrift: func(context.Context, int, int) (int, error) {
				return 0, nil
			},
			ListLegacyProjectionDrift: func(context.Context, int, int, int) ([]db.ProjectionDriftRow, error) {
				return nil, nil
			},
		},
		Status: func(int) (string, error) {
			return `{"state":"ok"}`, nil
		},
	}
	return deps
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

func TestRunRollbackCheckSuccess(t *testing.T) {
	minBucket, maxBucket := 12, 34
	cfg := pinnedRolloutTestConfig(minBucket, maxBucket)
	var gotHost string
	var gotMin, gotMax int
	deps := rollbackCheckDeps{
		Hostname: func() string { return "host-a" },
		HostRowExists: func(_ context.Context, hostID string) (bool, error) {
			gotHost = hostID
			return false, nil
		},
		ListOverlappingHostRows: func(_ context.Context, min, max int) ([]db.HostRow, error) {
			if min != minBucket || max != maxBucket {
				t.Fatalf("overlap range = %d-%d, want %d-%d", min, max, minBucket, maxBucket)
			}
			return nil, nil
		},
		CountActiveSitesForBucketRange: func(_ context.Context, min, max int) (int, error) {
			gotMin, gotMax = min, max
			return 42, nil
		},
		CountLegacyProjectionDrift: func(_ context.Context, min, max int) (int, error) {
			if min != minBucket || max != maxBucket {
				t.Fatalf("drift range = %d-%d, want %d-%d", min, max, minBucket, maxBucket)
			}
			return 0, nil
		},
	}

	var out bytes.Buffer
	if err := runRollbackCheck(context.Background(), &out, cfg, "", -1, -1, deps); err != nil {
		t.Fatalf("runRollbackCheck: %v", err)
	}
	if gotHost != "host-a" {
		t.Fatalf("host = %q, want host-a", gotHost)
	}
	if gotMin != minBucket || gotMax != maxBucket {
		t.Fatalf("active site range = %d-%d, want %d-%d", gotMin, gotMax, minBucket, maxBucket)
	}
	for _, want := range []string{
		"PASS rollback_range=12-34",
		"PASS jetmon_hosts row absent host=\"host-a\"",
		"PASS jetmon_hosts overlap=0",
		"INFO active_sites_in_rollback_range=42",
		"PASS legacy_projection_drift=0",
		"rollback check passed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunRollbackCheckUsesExplicitRangeAndHostOverride(t *testing.T) {
	cfg := dynamicRolloutTestConfig()
	var gotHost string
	var gotMin, gotMax int
	deps := rollbackCheckDeps{
		Hostname: func() string { return "wrong-host" },
		HostRowExists: func(_ context.Context, hostID string) (bool, error) {
			gotHost = hostID
			return false, nil
		},
		ListOverlappingHostRows: func(context.Context, int, int) ([]db.HostRow, error) {
			return nil, nil
		},
		CountActiveSitesForBucketRange: func(_ context.Context, min, max int) (int, error) {
			gotMin, gotMax = min, max
			return 1, nil
		},
		CountLegacyProjectionDrift: func(context.Context, int, int) (int, error) {
			return 0, nil
		},
	}

	var out bytes.Buffer
	if err := runRollbackCheck(context.Background(), &out, cfg, " v2-host ", 2, 4, deps); err != nil {
		t.Fatalf("runRollbackCheck: %v", err)
	}
	if gotHost != "v2-host" {
		t.Fatalf("host = %q, want v2-host", gotHost)
	}
	if gotMin != 2 || gotMax != 4 {
		t.Fatalf("range = %d-%d, want 2-4", gotMin, gotMax)
	}
}

func TestRunRollbackCheckWarnsWhenRangeIsEmpty(t *testing.T) {
	minBucket, maxBucket := 12, 34
	cfg := pinnedRolloutTestConfig(minBucket, maxBucket)
	deps := rollbackCheckDeps(successfulPinnedRolloutDeps())
	deps.CountActiveSitesForBucketRange = func(context.Context, int, int) (int, error) {
		return 0, nil
	}

	var out bytes.Buffer
	if err := runRollbackCheck(context.Background(), &out, cfg, "", -1, -1, deps); err != nil {
		t.Fatalf("runRollbackCheck: %v", err)
	}
	if !strings.Contains(out.String(), "WARN active_sites_in_rollback_range=0") {
		t.Fatalf("output missing empty-range warning:\n%s", out.String())
	}
}

func TestRunRollbackCheckFailures(t *testing.T) {
	minBucket, maxBucket := 12, 34
	tests := []struct {
		name      string
		cfg       *config.Config
		host      string
		bucketMin int
		bucketMax int
		deps      rollbackCheckDeps
		want      string
	}{
		{
			name:      "no range",
			cfg:       dynamicRolloutTestConfig(),
			bucketMin: -1,
			bucketMax: -1,
			deps:      rollbackCheckDeps(successfulPinnedRolloutDeps()),
			want:      "needs a pinned bucket config",
		},
		{
			name:      "host row exists",
			cfg:       pinnedRolloutTestConfig(minBucket, maxBucket),
			bucketMin: -1,
			bucketMax: -1,
			deps: rollbackCheckDeps{
				Hostname: func() string { return "host-a" },
				HostRowExists: func(context.Context, string) (bool, error) {
					return true, nil
				},
			},
			want: "still has a jetmon_hosts row",
		},
		{
			name:      "overlapping dynamic row",
			cfg:       pinnedRolloutTestConfig(minBucket, maxBucket),
			bucketMin: -1,
			bucketMax: -1,
			deps: rollbackCheckDeps{
				Hostname: func() string { return "host-a" },
				HostRowExists: func(context.Context, string) (bool, error) {
					return false, nil
				},
				ListOverlappingHostRows: func(context.Context, int, int) ([]db.HostRow, error) {
					return []db.HostRow{{HostID: "dynamic-host", BucketMin: 10, BucketMax: 20, Status: "active"}}, nil
				},
			},
			want: "overlapping rollback range",
		},
		{
			name:      "projection drift",
			cfg:       pinnedRolloutTestConfig(minBucket, maxBucket),
			bucketMin: -1,
			bucketMax: -1,
			deps: rollbackCheckDeps{
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
			want: "fix drift before restarting v1",
		},
		{
			name:      "explicit range outside total",
			cfg:       dynamicRolloutTestConfig(),
			host:      "host-a",
			bucketMin: 0,
			bucketMax: 10,
			deps:      rollbackCheckDeps(successfulPinnedRolloutDeps()),
			want:      "bucket-max must be < BUCKET_TOTAL",
		},
		{
			name:      "negative explicit range",
			cfg:       pinnedRolloutTestConfig(minBucket, maxBucket),
			host:      "host-a",
			bucketMin: -2,
			bucketMax: -2,
			deps:      rollbackCheckDeps(successfulPinnedRolloutDeps()),
			want:      "bucket-min and bucket-max must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runRollbackCheck(context.Background(), &out, tt.cfg, tt.host, tt.bucketMin, tt.bucketMax, tt.deps)
			if err == nil {
				t.Fatal("runRollbackCheck succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
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

func TestRunActivityCheckSuccess(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	cfg := dynamicRolloutTestConfig()
	var gotCutoff time.Time
	deps := activityCheckDeps{
		Now: func() time.Time { return now },
		CountActiveSitesForBucketRange: func(_ context.Context, min, max int) (int, error) {
			if min != 0 || max != 9 {
				t.Fatalf("active range = %d-%d, want 0-9", min, max)
			}
			return 10, nil
		},
		CountRecentlyCheckedActiveSitesForRange: func(_ context.Context, min, max int, cutoff time.Time) (int, error) {
			if min != 0 || max != 9 {
				t.Fatalf("recent range = %d-%d, want 0-9", min, max)
			}
			gotCutoff = cutoff
			return 3, nil
		},
	}

	var out bytes.Buffer
	if err := runActivityCheck(context.Background(), &out, cfg, -1, -1, "15m", false, deps); err != nil {
		t.Fatalf("runActivityCheck: %v", err)
	}
	if want := now.Add(-15 * time.Minute); !gotCutoff.Equal(want) {
		t.Fatalf("cutoff = %s, want %s", gotCutoff, want)
	}
	for _, want := range []string{
		"INFO activity_range=0-9",
		"INFO active_sites=10",
		"INFO active_sites_checked_since=3",
		"PASS rollout_activity=recent_checks_present",
		"post-cutover activity check passed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunActivityCheckRequireAll(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	cfg := dynamicRolloutTestConfig()
	deps := activityCheckDeps{
		Now: func() time.Time { return now },
		CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
			return 3, nil
		},
		CountRecentlyCheckedActiveSitesForRange: func(context.Context, int, int, time.Time) (int, error) {
			return 3, nil
		},
	}

	var out bytes.Buffer
	if err := runActivityCheck(context.Background(), &out, cfg, -1, -1, "15m", true, deps); err != nil {
		t.Fatalf("runActivityCheck: %v", err)
	}
	if !strings.Contains(out.String(), "PASS rollout_activity=all_active_sites_checked") {
		t.Fatalf("output missing require-all pass:\n%s", out.String())
	}
}

func TestRunActivityCheckWarnsWhenRangeIsEmpty(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	cfg := dynamicRolloutTestConfig()
	deps := activityCheckDeps{
		Now: func() time.Time { return now },
		CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
			return 0, nil
		},
		CountRecentlyCheckedActiveSitesForRange: func(context.Context, int, int, time.Time) (int, error) {
			return 0, nil
		},
	}

	var out bytes.Buffer
	if err := runActivityCheck(context.Background(), &out, cfg, -1, -1, "15m", false, deps); err != nil {
		t.Fatalf("runActivityCheck: %v", err)
	}
	if !strings.Contains(out.String(), "WARN active_sites=0") {
		t.Fatalf("output missing empty range warning:\n%s", out.String())
	}
}

func TestRunActivityCheckFailures(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	cfg := dynamicRolloutTestConfig()
	tests := []struct {
		name string
		deps activityCheckDeps
		want string
	}{
		{
			name: "active count error",
			deps: activityCheckDeps{
				Now: func() time.Time { return now },
				CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
					return 0, errors.New("db unavailable")
				},
				CountRecentlyCheckedActiveSitesForRange: func(context.Context, int, int, time.Time) (int, error) {
					return 0, nil
				},
			},
			want: "db unavailable",
		},
		{
			name: "recent count error",
			deps: activityCheckDeps{
				Now: func() time.Time { return now },
				CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
					return 10, nil
				},
				CountRecentlyCheckedActiveSitesForRange: func(context.Context, int, int, time.Time) (int, error) {
					return 0, errors.New("db unavailable")
				},
			},
			want: "count recently checked",
		},
		{
			name: "no recent checks",
			deps: activityCheckDeps{
				Now: func() time.Time { return now },
				CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
					return 10, nil
				},
				CountRecentlyCheckedActiveSitesForRange: func(context.Context, int, int, time.Time) (int, error) {
					return 0, nil
				},
			},
			want: "no active sites",
		},
		{
			name: "require all mismatch",
			deps: activityCheckDeps{
				Now: func() time.Time { return now },
				CountActiveSitesForBucketRange: func(context.Context, int, int) (int, error) {
					return 10, nil
				},
				CountRecentlyCheckedActiveSitesForRange: func(context.Context, int, int, time.Time) (int, error) {
					return 9, nil
				},
			},
			want: "only 9/10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runActivityCheck(context.Background(), &out, cfg, -1, -1, "15m", tt.name == "require all mismatch", tt.deps)
			if err == nil {
				t.Fatal("runActivityCheck succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestResolveActivityCutoff(t *testing.T) {
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		since   string
		want    time.Time
		wantErr string
	}{
		{
			name:  "duration",
			since: "15m",
			want:  now.Add(-15 * time.Minute),
		},
		{
			name:  "rfc3339",
			since: "2026-04-28T11:30:00Z",
			want:  time.Date(2026, 4, 28, 11, 30, 0, 0, time.UTC),
		},
		{
			name:    "empty",
			since:   "",
			wantErr: "must not be empty",
		},
		{
			name:    "negative duration",
			since:   "-1m",
			wantErr: "must be > 0",
		},
		{
			name:    "invalid",
			since:   "yesterday",
			wantErr: "must be a duration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveActivityCutoff(now, tt.since)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatal("resolveActivityCutoff succeeded")
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveActivityCutoff: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Fatalf("cutoff = %s, want %s", got, tt.want)
			}
		})
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
		SummarizeLegacyProjectionDrift: func(_ context.Context, min, max, limit int) ([]db.ProjectionDriftSummaryRow, error) {
			if min != 2 || max != 4 || limit != 2 {
				t.Fatalf("summary args = %d-%d limit=%d, want 2-4 limit=2", min, max, limit)
			}
			return []db.ProjectionDriftSummaryRow{
				{BucketNo: 3, SiteStatus: 1, ExpectedStatus: 2, EventState: &eventState, MaxOpenEventCount: 1, DriftCount: 2, SampleBlogID: 42},
			}, nil
		},
		ListLegacyProjectionDrift: func(_ context.Context, min, max, limit int) ([]db.ProjectionDriftRow, error) {
			if min != 2 || max != 4 || limit != 1 {
				t.Fatalf("list args = %d-%d limit=%d, want 2-4 limit=1", min, max, limit)
			}
			return []db.ProjectionDriftRow{
				{BlogID: 42, BucketNo: 3, SiteStatus: 1, ExpectedStatus: 2, EventID: &eventID, EventState: &eventState, OpenEventCount: 1},
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
		"WARN legacy_projection_drift_requires_manual_review=2",
		"projection_drift_next_step=",
		"SAMPLE_BLOG",
		"missing_confirmed_down_projection",
		"projection_drift_cause=missing_confirmed_down_projection count=2",
		"BLOG_ID",
		"42",
		"Down",
		"INFO projection_drift_rows_truncated=1",
		"INFO projection_drift_repair=manual_confirmation_required",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunProjectionDriftReportUsesAllSummariesForCauseGuidance(t *testing.T) {
	cfg := dynamicRolloutTestConfig()
	deps := projectionDriftDeps{
		CountLegacyProjectionDrift: func(context.Context, int, int) (int, error) {
			return defaultProjectionDriftSummaryLimit + 1, nil
		},
		SummarizeLegacyProjectionDrift: func(context.Context, int, int, int) ([]db.ProjectionDriftSummaryRow, error) {
			var summaries []db.ProjectionDriftSummaryRow
			for i := range defaultProjectionDriftSummaryLimit {
				summaries = append(summaries, db.ProjectionDriftSummaryRow{
					BucketNo:       i,
					SiteStatus:     1,
					ExpectedStatus: 2,
					DriftCount:     1,
					SampleBlogID:   int64(100 + i),
				})
			}
			summaries = append(summaries, db.ProjectionDriftSummaryRow{
				BucketNo:       99,
				SiteStatus:     0,
				ExpectedStatus: 1,
				DriftCount:     1,
				SampleBlogID:   999,
			})
			return summaries, nil
		},
		ListLegacyProjectionDrift: func(context.Context, int, int, int) ([]db.ProjectionDriftRow, error) {
			return nil, nil
		},
	}

	var out bytes.Buffer
	err := runProjectionDriftReport(context.Background(), &out, cfg, 0, 9, 1, deps)
	if err == nil {
		t.Fatal("runProjectionDriftReport succeeded")
	}
	for _, want := range []string{
		"INFO projection_drift_summary_groups_truncated=1",
		"INFO projection_drift_summary_rows_hidden=1",
		"projection_drift_cause=missing_confirmed_down_projection count=20",
		"projection_drift_cause=stale_legacy_down_projection count=1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "999") {
		t.Fatalf("hidden summary sample was printed:\n%s", out.String())
	}
}

func TestFormatOptionalStringSanitizesControlCharacters(t *testing.T) {
	raw := "Down\x1b[31m\nStill Down"
	got := formatOptionalString(&raw)
	if strings.ContainsAny(got, "\x1b\n\r\t") {
		t.Fatalf("formatted string contains control characters: %q", got)
	}
	if !strings.Contains(got, "?") {
		t.Fatalf("formatted string = %q, want replacement marker", got)
	}
}

func TestClassifyProjectionDriftCause(t *testing.T) {
	eventState := "Down"
	tests := []struct {
		name       string
		status     int
		expected   int
		state      *string
		openEvents int
		want       string
	}{
		{name: "legacy down but no open event", status: 2, expected: 1, want: "stale_legacy_down_projection"},
		{name: "running with open down", status: 1, expected: 2, state: &eventState, openEvents: 1, want: "missing_confirmed_down_projection"},
		{name: "seems down not promoted", status: 0, expected: 2, state: &eventState, openEvents: 1, want: "missing_confirmed_promotion"},
		{name: "duplicate open events", status: 1, expected: 2, state: &eventState, openEvents: 2, want: "multiple_open_http_events"},
		{name: "unknown status", status: 9, expected: 1, want: "unexpected_projection_value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyProjectionDriftCause(tt.status, tt.expected, tt.state, tt.openEvents)
			if got.Code != tt.want {
				t.Fatalf("cause = %q, want %q", got.Code, tt.want)
			}
		})
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

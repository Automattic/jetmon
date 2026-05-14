package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/veriflier"
)

func TestVeriflierDiscoverySoakShadowDriftMatrix(t *testing.T) {
	now := time.Date(2026, 5, 11, 15, 0, 0, 0, time.UTC)
	const trusted = 24
	cfg := &config.Config{VeriflierDiscoveryMode: config.VeriflierDiscoveryModeShadow}
	var probes []veriflierReadinessResult
	var vantages []db.VeriflierVantage
	var agents []db.VeriflierAgent

	for i := range trusted {
		id := fmt.Sprintf("vantage-%02d", i)
		host := fmt.Sprintf("v%02d.example", i)
		cfg.Verifiers = append(cfg.Verifiers, config.VerifierConfig{
			Name:      id,
			Host:      host,
			Port:      "7803",
			AuthToken: fmt.Sprintf("static-secret-%02d", i),
		})
		probes = append(probes, discoveryProbe(id, host+":7803", id, "agent-"+id))

		registryHost := host
		if i%6 == 0 {
			registryHost = fmt.Sprintf("registry-v%02d.example", i)
		}
		authToken := fmt.Sprintf("registry-secret-%02d", i)
		if i%7 == 0 {
			authToken = ""
		}
		vantages = append(vantages, db.VeriflierVantage{
			VantageID:    id,
			Region:       "test-region",
			Provider:     "test-provider",
			EndpointHost: registryHost,
			EndpointPort: "7803",
			AuthToken:    authToken,
			Enabled:      true,
			LastSeen:     ptrTime(now.Add(-time.Duration(i) * time.Second)),
			ActiveAgents: 1,
		})
		agents = append(agents, discoveryAgent("agent-"+id, id, host, now.Add(-time.Duration(i)*time.Second)))
	}

	cfg.Verifiers = append(cfg.Verifiers,
		config.VerifierConfig{Name: "duplicate-a", Host: "dup-a.example", Port: "7803", AuthToken: "static-duplicate-a"},
		config.VerifierConfig{Name: "duplicate-b", Host: "dup-b.example", Port: "7803", AuthToken: "static-duplicate-b"},
		config.VerifierConfig{Name: "legacy", Host: "legacy.example", Port: "7803", AuthToken: "static-legacy"},
		config.VerifierConfig{Name: "error", Host: "error.example", Port: "7803", AuthToken: "static-error"},
		config.VerifierConfig{Name: "missing-id", Host: "missing.example", Port: "7803", AuthToken: "static-missing"},
		config.VerifierConfig{Name: "missing-registry", Host: "missing-registry.example", Port: "7803", AuthToken: "static-missing-registry"},
	)
	probes = append(probes,
		discoveryProbe("duplicate-a", "dup-a.example:7803", "duplicate", "agent-duplicate-a"),
		discoveryProbe("duplicate-b", "dup-b.example:7803", "duplicate", "agent-duplicate-b"),
		legacyDiscoveryProbe("legacy", "legacy.example:7803"),
		errorDiscoveryProbe("error", "error.example:7803", "connection refused"),
		discoveryProbe("missing-id", "missing.example:7803", "", "agent-missing-id"),
		discoveryProbe("missing-registry", "missing-registry.example:7803", "missing-registry", "agent-missing-registry"),
	)
	vantages = append(vantages,
		db.VeriflierVantage{VantageID: "duplicate", EndpointHost: "dup-lb.example", EndpointPort: "7803", AuthToken: "registry-duplicate", Enabled: true},
		db.VeriflierVantage{VantageID: "registry-only", EndpointHost: "registry-only.example", EndpointPort: "7803", AuthToken: "registry-only-token", Enabled: true},
		db.VeriflierVantage{VantageID: "incomplete", Enabled: true},
		db.VeriflierVantage{VantageID: "disabled", EndpointHost: "disabled.example", EndpointPort: "7803", AuthToken: "registry-disabled", Enabled: false},
	)
	agents = append(agents,
		discoveryAgent("agent-duplicate-a", "duplicate", "dup-a.example", now.Add(-5*time.Second)),
		discoveryAgent("agent-duplicate-b", "duplicate", "dup-b.example", now.Add(-4*time.Second)),
		discoveryAgent("agent-rogue", "rogue", "rogue.example", now.Add(-3*time.Second)),
	)

	report, err := buildVeriflierDiscoveryReport(
		context.Background(),
		cfg,
		veriflierDiscoveryReportOptions{StaleAfter: 90 * time.Second, QueryTimeout: time.Second, ProbeTimeout: time.Second, ProbeStatic: true},
		veriflierDiscoveryReportDeps{
			Now: func() time.Time { return now },
			ProbeConfigured: func(context.Context, *config.Config, time.Duration) []veriflierReadinessResult {
				return probes
			},
			ListSnapshot: func(context.Context, time.Duration) (db.VeriflierDiscoverySnapshot, error) {
				return db.VeriflierDiscoverySnapshot{Vantages: vantages, Agents: agents}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("buildVeriflierDiscoveryReport: %v", err)
	}
	if report.Status != "red" || report.OK {
		t.Fatalf("status=%s ok=%t issues=%+v, want red due duplicate static vantage", report.Status, report.OK, report.Issues)
	}
	if report.Static.Configured != trusted+6 || report.Static.DuplicateVantages != 2 {
		t.Fatalf("static summary = %+v", report.Static)
	}
	for _, name := range []string{
		"static_vantage_duplicate",
		"static_legacy_only",
		"static_probe_failed",
		"static_vantage_missing",
		"static_missing_enabled_registry",
		"enabled_registry_missing_static",
		"registry_enabled_incomplete",
		"static_registry_endpoint_mismatch",
		"static_registry_auth_presence_mismatch",
		"agent_registry_endpoint_mismatch",
		"agent_without_registry",
		"duplicate_active_agent_endpoints",
		"enabled_registry_without_active_agent",
	} {
		if !discoveryReportHasIssue(report, name) {
			t.Fatalf("issues missing %q: %+v", name, report.Issues)
		}
	}

	var out bytes.Buffer
	if err := renderVeriflierDiscoveryReport(&out, report, "json"); err != nil {
		t.Fatalf("render json: %v", err)
	}
	rendered := out.String()
	for _, secret := range []string{"static-secret", "registry-secret", "static-duplicate", "registry-duplicate", "registry-only-token"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("rendered JSON leaked secret fragment %q", secret)
		}
	}
}

func TestVeriflierDiscoverySoakActiveFallbackAndRecoveryStates(t *testing.T) {
	now := time.Date(2026, 5, 11, 15, 15, 0, 0, time.UTC)
	cfg := &config.Config{
		VeriflierDiscoveryMode: config.VeriflierDiscoveryModeActive,
		Verifiers: []config.VerifierConfig{
			{Name: "east", Host: "east.example", Port: "7803", AuthToken: "static-east-token"},
			{Name: "west", Host: "west.example", Port: "7803", AuthToken: "static-west-token"},
		},
	}
	probes := []veriflierReadinessResult{
		discoveryProbe("east", "east.example:7803", "us-east", "agent-east"),
		discoveryProbe("west", "west.example:7803", "us-west", "agent-west"),
	}
	opts := veriflierDiscoveryReportOptions{StaleAfter: 90 * time.Second, QueryTimeout: time.Second, ProbeTimeout: time.Second, ProbeStatic: true}

	redReport, err := buildVeriflierDiscoveryReport(context.Background(), cfg, opts, veriflierDiscoveryReportDeps{
		Now: func() time.Time { return now },
		ProbeConfigured: func(context.Context, *config.Config, time.Duration) []veriflierReadinessResult {
			return probes
		},
		ListSnapshot: func(context.Context, time.Duration) (db.VeriflierDiscoverySnapshot, error) {
			return db.VeriflierDiscoverySnapshot{
				Vantages: []db.VeriflierVantage{
					{VantageID: "us-east", Enabled: true},
					{VantageID: "us-west", Enabled: true},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("build red report: %v", err)
	}
	if redReport.Status != "red" || !discoveryReportHasIssue(redReport, "active_without_usable_registry") {
		t.Fatalf("red report status=%s issues=%+v, want active fallback red", redReport.Status, redReport.Issues)
	}

	amberReport, err := buildVeriflierDiscoveryReport(context.Background(), cfg, opts, veriflierDiscoveryReportDeps{
		Now: func() time.Time { return now },
		ProbeConfigured: func(context.Context, *config.Config, time.Duration) []veriflierReadinessResult {
			return probes
		},
		ListSnapshot: func(context.Context, time.Duration) (db.VeriflierDiscoverySnapshot, error) {
			return db.VeriflierDiscoverySnapshot{
				Vantages: []db.VeriflierVantage{
					{VantageID: "us-east", EndpointHost: "east.example", EndpointPort: "7803", AuthToken: "registry-east-token", Enabled: true},
					{VantageID: "us-west", EndpointHost: "west.example", EndpointPort: "7803", AuthToken: "registry-west-token", Enabled: true},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("build amber report: %v", err)
	}
	if amberReport.Status != "amber" || !discoveryReportHasIssue(amberReport, "enabled_registry_without_active_agent") {
		t.Fatalf("amber report status=%s issues=%+v, want missing active agent telemetry", amberReport.Status, amberReport.Issues)
	}

	greenReport, err := buildVeriflierDiscoveryReport(context.Background(), cfg, opts, veriflierDiscoveryReportDeps{
		Now: func() time.Time { return now },
		ProbeConfigured: func(context.Context, *config.Config, time.Duration) []veriflierReadinessResult {
			return probes
		},
		ListSnapshot: func(context.Context, time.Duration) (db.VeriflierDiscoverySnapshot, error) {
			return db.VeriflierDiscoverySnapshot{
				Vantages: []db.VeriflierVantage{
					{VantageID: "us-east", EndpointHost: "east.example", EndpointPort: "7803", AuthToken: "registry-east-token", Enabled: true, ActiveAgents: 1, LastSeen: ptrTime(now.Add(-10 * time.Second))},
					{VantageID: "us-west", EndpointHost: "west.example", EndpointPort: "7803", AuthToken: "registry-west-token", Enabled: true, ActiveAgents: 1, LastSeen: ptrTime(now.Add(-11 * time.Second))},
				},
				Agents: []db.VeriflierAgent{
					discoveryAgent("agent-east", "us-east", "east.example", now.Add(-10*time.Second)),
					discoveryAgent("agent-west", "us-west", "west.example", now.Add(-11*time.Second)),
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("build green report: %v", err)
	}
	if !greenReport.OK || greenReport.Status != "green" || len(greenReport.Issues) != 0 {
		t.Fatalf("green report status=%s ok=%t issues=%+v", greenReport.Status, greenReport.OK, greenReport.Issues)
	}
}

func legacyDiscoveryProbe(name, addr string) veriflierReadinessResult {
	return veriflierReadinessResult{
		Name: name,
		Addr: addr,
		Status: &veriflier.StatusV2Response{
			Status:    "ok",
			Version:   "legacy-version",
			Protocols: []string{veriflier.ProtocolLegacy},
		},
	}
}

func errorDiscoveryProbe(name, addr, err string) veriflierReadinessResult {
	return veriflierReadinessResult{Name: name, Addr: addr, Err: fmt.Errorf("%s", err)}
}

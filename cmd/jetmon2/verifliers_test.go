package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/veriflier"
)

func TestBuildVeriflierDiscoveryReportShadowClean(t *testing.T) {
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		VeriflierDiscoveryMode: config.VeriflierDiscoveryModeShadow,
		Verifiers: []config.VerifierConfig{
			{Name: "east", Host: "east.example", Port: "7803", AuthToken: "static-east-token"},
			{Name: "west", Host: "west.example", Port: "7803", AuthToken: "static-west-token"},
		},
	}
	opts := veriflierDiscoveryReportOptions{
		Output:       "text",
		StaleAfter:   90 * time.Second,
		QueryTimeout: time.Second,
		ProbeTimeout: time.Second,
		ProbeStatic:  true,
	}
	deps := veriflierDiscoveryReportDeps{
		Now: func() time.Time { return now },
		ProbeConfigured: func(context.Context, *config.Config, time.Duration) []veriflierReadinessResult {
			return []veriflierReadinessResult{
				discoveryProbe("east", "east.example:7803", "us-east", "agent-east"),
				discoveryProbe("west", "west.example:7803", "us-west", "agent-west"),
			}
		},
		ListSnapshot: func(context.Context, time.Duration) (db.VeriflierDiscoverySnapshot, error) {
			return db.VeriflierDiscoverySnapshot{
				Vantages: []db.VeriflierVantage{
					{VantageID: "us-east", Region: "iad", Provider: "test", EndpointHost: "east.example", EndpointPort: "7803", AuthToken: "registry-east-token", Enabled: true, LastSeen: ptrTime(now.Add(-10 * time.Second)), ActiveAgents: 1},
					{VantageID: "us-west", Region: "sfo", Provider: "test", EndpointHost: "west.example", EndpointPort: "7803", AuthToken: "registry-west-token", Enabled: true, LastSeen: ptrTime(now.Add(-20 * time.Second)), ActiveAgents: 1},
				},
				Agents: []db.VeriflierAgent{
					discoveryAgent("agent-east", "us-east", "east.example", now.Add(-10*time.Second)),
					discoveryAgent("agent-west", "us-west", "west.example", now.Add(-20*time.Second)),
				},
			}, nil
		},
	}

	report, err := buildVeriflierDiscoveryReport(context.Background(), cfg, opts, deps)
	if err != nil {
		t.Fatalf("buildVeriflierDiscoveryReport: %v", err)
	}
	if !report.OK || report.Status != "green" || len(report.Issues) != 0 {
		t.Fatalf("report status=%s ok=%t issues=%+v, want clean green", report.Status, report.OK, report.Issues)
	}
	if report.Static.V2 != 2 || report.Registry.Enabled != 2 || report.Agents.Active != 2 {
		t.Fatalf("summaries = static %+v registry %+v agents %+v", report.Static, report.Registry, report.Agents)
	}

	var out bytes.Buffer
	if err := renderVeriflierDiscoveryReport(&out, report, "text"); err != nil {
		t.Fatalf("render text: %v", err)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "PASS veriflier_discovery_report=green") {
		t.Fatalf("rendered text missing green pass:\n%s", rendered)
	}
	if strings.Contains(rendered, "static-east-token") || strings.Contains(rendered, "registry-east-token") {
		t.Fatalf("rendered text leaked auth token:\n%s", rendered)
	}
}

func TestBuildVeriflierDiscoveryReportFlagsShadowDrift(t *testing.T) {
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		VeriflierDiscoveryMode: config.VeriflierDiscoveryModeShadow,
		Verifiers: []config.VerifierConfig{
			{Name: "east", Host: "east.example", Port: "7803", AuthToken: "static-token"},
			{Name: "west", Host: "west.example", Port: "7803", AuthToken: "static-token"},
		},
	}
	opts := veriflierDiscoveryReportOptions{StaleAfter: 90 * time.Second, ProbeStatic: true}
	deps := veriflierDiscoveryReportDeps{
		Now: func() time.Time { return now },
		ProbeConfigured: func(context.Context, *config.Config, time.Duration) []veriflierReadinessResult {
			return []veriflierReadinessResult{
				discoveryProbe("east", "east.example:7803", "us-east", "agent-east"),
				discoveryProbe("west", "west.example:7803", "us-west", "agent-west"),
			}
		},
		ListSnapshot: func(context.Context, time.Duration) (db.VeriflierDiscoverySnapshot, error) {
			return db.VeriflierDiscoverySnapshot{
				Vantages: []db.VeriflierVantage{
					{VantageID: "us-east", EndpointHost: "east-registry.example", EndpointPort: "7803", AuthToken: "registry-token", Enabled: true},
					{VantageID: "us-north", EndpointHost: "north.example", EndpointPort: "7803", AuthToken: "registry-token", Enabled: true},
					{VantageID: "incomplete", Enabled: true},
				},
				Agents: []db.VeriflierAgent{
					discoveryAgent("agent-east", "us-east", "east.example", now.Add(-10*time.Second)),
					discoveryAgent("agent-rogue", "rogue", "rogue.example", now.Add(-10*time.Second)),
				},
			}, nil
		},
	}

	report, err := buildVeriflierDiscoveryReport(context.Background(), cfg, opts, deps)
	if err != nil {
		t.Fatalf("buildVeriflierDiscoveryReport: %v", err)
	}
	if report.Status != "amber" || report.OK {
		t.Fatalf("status=%s ok=%t issues=%+v, want amber", report.Status, report.OK, report.Issues)
	}
	for _, name := range []string{
		"static_missing_enabled_registry",
		"enabled_registry_missing_static",
		"registry_enabled_incomplete",
		"agent_without_registry",
		"agent_registry_endpoint_mismatch",
		"enabled_registry_without_active_agent",
	} {
		if !discoveryReportHasIssue(report, name) {
			t.Fatalf("issues %+v missing %q", report.Issues, name)
		}
	}
}

func TestBuildVeriflierDiscoveryReportActiveNoUsableRegistryIsRed(t *testing.T) {
	cfg := &config.Config{
		VeriflierDiscoveryMode: config.VeriflierDiscoveryModeActive,
		Verifiers:              []config.VerifierConfig{{Name: "east", Host: "east.example", Port: "7803"}},
	}
	opts := veriflierDiscoveryReportOptions{StaleAfter: 90 * time.Second, ProbeStatic: false}
	deps := veriflierDiscoveryReportDeps{
		Now: func() time.Time { return time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC) },
		ListSnapshot: func(context.Context, time.Duration) (db.VeriflierDiscoverySnapshot, error) {
			return db.VeriflierDiscoverySnapshot{
				Vantages: []db.VeriflierVantage{{VantageID: "incomplete", Enabled: true}},
			}, nil
		},
	}

	report, err := buildVeriflierDiscoveryReport(context.Background(), cfg, opts, deps)
	if err != nil {
		t.Fatalf("buildVeriflierDiscoveryReport: %v", err)
	}
	if report.Status != "red" || report.OK {
		t.Fatalf("status=%s ok=%t issues=%+v, want red", report.Status, report.OK, report.Issues)
	}
	if !discoveryReportHasIssue(report, "active_without_usable_registry") {
		t.Fatalf("issues %+v missing active_without_usable_registry", report.Issues)
	}
}

func TestRenderVeriflierDiscoveryReportJSONDoesNotExposeTokens(t *testing.T) {
	now := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		VeriflierDiscoveryMode: config.VeriflierDiscoveryModeShadow,
		Verifiers:              []config.VerifierConfig{{Name: "east", Host: "east.example", Port: "7803", AuthToken: "secret-static-token"}},
	}
	opts := veriflierDiscoveryReportOptions{StaleAfter: 90 * time.Second, ProbeStatic: true}
	deps := veriflierDiscoveryReportDeps{
		Now: func() time.Time { return now },
		ProbeConfigured: func(context.Context, *config.Config, time.Duration) []veriflierReadinessResult {
			return []veriflierReadinessResult{discoveryProbe("east", "east.example:7803", "us-east", "agent-east")}
		},
		ListSnapshot: func(context.Context, time.Duration) (db.VeriflierDiscoverySnapshot, error) {
			return db.VeriflierDiscoverySnapshot{
				Vantages: []db.VeriflierVantage{{VantageID: "us-east", EndpointHost: "east.example", EndpointPort: "7803", AuthToken: "secret-registry-token", Enabled: true, ActiveAgents: 1, LastSeen: ptrTime(now)}},
				Agents:   []db.VeriflierAgent{discoveryAgent("agent-east", "us-east", "east.example", now)},
			}, nil
		},
	}
	report, err := buildVeriflierDiscoveryReport(context.Background(), cfg, opts, deps)
	if err != nil {
		t.Fatalf("buildVeriflierDiscoveryReport: %v", err)
	}

	var out bytes.Buffer
	if err := renderVeriflierDiscoveryReport(&out, report, "json"); err != nil {
		t.Fatalf("render json: %v", err)
	}
	rendered := out.String()
	if strings.Contains(rendered, "secret-static-token") || strings.Contains(rendered, "secret-registry-token") {
		t.Fatalf("rendered JSON leaked auth token:\n%s", rendered)
	}
	if !strings.Contains(rendered, `"auth_token_present": true`) {
		t.Fatalf("rendered JSON missing auth token presence:\n%s", rendered)
	}
}

func TestValidateVeriflierDiscoveryReportOptions(t *testing.T) {
	opts := veriflierDiscoveryReportOptions{Output: "json", StaleAfter: time.Second, QueryTimeout: time.Second, ProbeTimeout: time.Second}
	if err := validateVeriflierDiscoveryReportOptions(&opts); err != nil {
		t.Fatalf("validateVeriflierDiscoveryReportOptions: %v", err)
	}
	if opts.Output != "json" {
		t.Fatalf("Output = %q, want json", opts.Output)
	}

	opts.Output = "yaml"
	if err := validateVeriflierDiscoveryReportOptions(&opts); err == nil {
		t.Fatal("validateVeriflierDiscoveryReportOptions accepted yaml")
	}
}

func discoveryProbe(name, addr, vantageID, agentID string) veriflierReadinessResult {
	return veriflierReadinessResult{
		Name: name,
		Addr: addr,
		Status: &veriflier.StatusV2Response{
			Status:    "ok",
			Version:   "test-version",
			Protocols: []string{veriflier.ProtocolV2, veriflier.ProtocolLegacy},
			Vantage:   veriflier.Vantage{ID: vantageID},
			Agent:     veriflier.Agent{ID: agentID},
			Capacity:  veriflier.Capacity{MaxConcurrency: 8, QueueCapacity: 16, QueueDepth: 1, Active: 2, InFlight: 1},
		},
	}
}

func discoveryAgent(agentID, vantageID, host string, lastSeen time.Time) db.VeriflierAgent {
	return db.VeriflierAgent{
		AgentID:        agentID,
		VantageID:      vantageID,
		Hostname:       host + "-host",
		EndpointHost:   host,
		EndpointPort:   "7803",
		Version:        "test-version",
		Protocols:      []string{veriflier.ProtocolV2},
		MaxConcurrency: 8,
		QueueCapacity:  16,
		QueueDepth:     1,
		Active:         2,
		InFlight:       1,
		Status:         "active",
		LastSeen:       lastSeen,
	}
}

func discoveryReportHasIssue(report veriflierDiscoveryReport, name string) bool {
	for _, issue := range report.Issues {
		if issue.Name == name {
			return true
		}
	}
	return false
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

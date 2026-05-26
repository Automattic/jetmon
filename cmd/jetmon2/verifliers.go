package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/veriflier"
)

const (
	defaultVeriflierDiscoveryQueryTimeout = 30 * time.Second
	defaultVeriflierDiscoveryProbeTimeout = 2 * time.Second
	maxVeriflierDiscoveryTimeout          = 5 * time.Minute
)

type veriflierDiscoveryReportOptions struct {
	Output       string
	StaleAfter   time.Duration
	QueryTimeout time.Duration
	ProbeTimeout time.Duration
	ProbeStatic  bool
}

type veriflierDiscoveryReportDeps struct {
	Now             func() time.Time
	ProbeConfigured func(context.Context, *config.Config, time.Duration) []veriflierReadinessResult
	ListSnapshot    func(context.Context, time.Duration) (db.VeriflierDiscoverySnapshot, error)
}

type veriflierDiscoveryReport struct {
	OK                  bool                              `json:"ok"`
	Status              string                            `json:"status"`
	Command             string                            `json:"command"`
	GeneratedAt         time.Time                         `json:"generated_at"`
	DiscoveryMode       string                            `json:"discovery_mode"`
	StaleAfterSeconds   int64                             `json:"stale_after_seconds"`
	ProbeStatic         bool                              `json:"probe_static"`
	Static              veriflierDiscoveryStaticSummary   `json:"static"`
	Registry            veriflierDiscoveryRegistrySummary `json:"registry"`
	Agents              veriflierDiscoveryAgentSummary    `json:"agents"`
	StaticVerifiers     []veriflierDiscoveryStaticRow     `json:"static_verifiers,omitempty"`
	Vantages            []veriflierDiscoveryVantageRow    `json:"vantages,omitempty"`
	AgentRows           []veriflierDiscoveryAgentRow      `json:"agent_rows,omitempty"`
	Issues              []veriflierDiscoveryIssue         `json:"issues,omitempty"`
	SuggestedNextAction string                            `json:"suggested_next_action,omitempty"`
}

type veriflierDiscoveryStaticSummary struct {
	Configured        int `json:"configured"`
	Probed            int `json:"probed"`
	V2                int `json:"v2"`
	LegacyOnly        int `json:"legacy_only"`
	ProbeErrors       int `json:"probe_errors"`
	UniqueVantages    int `json:"unique_vantages"`
	DuplicateVantages int `json:"duplicate_vantages"`
}

type veriflierDiscoveryRegistrySummary struct {
	Total      int `json:"total"`
	Enabled    int `json:"enabled"`
	Disabled   int `json:"disabled"`
	Usable     int `json:"usable"`
	Incomplete int `json:"incomplete"`
}

type veriflierDiscoveryAgentSummary struct {
	Recent         int   `json:"recent"`
	Active         int   `json:"active"`
	StaleAfterSec  int64 `json:"stale_after_sec"`
	MaxConcurrency int   `json:"max_concurrency"`
	QueueCapacity  int   `json:"queue_capacity"`
	QueueDepth     int   `json:"queue_depth"`
	InFlight       int   `json:"in_flight"`
}

type veriflierDiscoveryStaticRow struct {
	Name             string `json:"name"`
	Addr             string `json:"addr"`
	Host             string `json:"host,omitempty"`
	Port             string `json:"port,omitempty"`
	AuthTokenPresent bool   `json:"auth_token_present"`
	ProbeStatus      string `json:"probe_status"`
	VantageID        string `json:"vantage_id,omitempty"`
	AgentID          string `json:"agent_id,omitempty"`
	Version          string `json:"version,omitempty"`
	Protocol         string `json:"protocol,omitempty"`
	Capacity         string `json:"capacity,omitempty"`
	Error            string `json:"error,omitempty"`
}

type veriflierDiscoveryVantageRow struct {
	VantageID        string     `json:"vantage_id"`
	Region           string     `json:"region,omitempty"`
	Provider         string     `json:"provider,omitempty"`
	Endpoint         string     `json:"endpoint,omitempty"`
	Enabled          bool       `json:"enabled"`
	Usable           bool       `json:"usable"`
	AuthTokenPresent bool       `json:"auth_token_present"`
	ActiveAgents     int        `json:"active_agents"`
	LastSeen         *time.Time `json:"last_seen,omitempty"`
	LastSeenAgeSec   *int64     `json:"last_seen_age_sec,omitempty"`
}

type veriflierDiscoveryAgentRow struct {
	AgentID        string    `json:"agent_id"`
	VantageID      string    `json:"vantage_id"`
	Hostname       string    `json:"hostname,omitempty"`
	Endpoint       string    `json:"endpoint,omitempty"`
	Version        string    `json:"version,omitempty"`
	Protocols      []string  `json:"protocols,omitempty"`
	Status         string    `json:"status"`
	LastSeen       time.Time `json:"last_seen"`
	LastSeenAgeSec int64     `json:"last_seen_age_sec"`
	MaxConcurrency int       `json:"max_concurrency"`
	QueueCapacity  int       `json:"queue_capacity"`
	QueueDepth     int       `json:"queue_depth"`
	Active         int       `json:"active"`
	InFlight       int       `json:"in_flight"`
}

type veriflierDiscoveryIssue struct {
	Severity string `json:"severity"`
	Name     string `json:"name"`
	Detail   string `json:"detail"`
}

func cmdVerifliers(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 verifliers <discovery-report> [args]")
		os.Exit(1)
	}

	switch args[0] {
	case "discovery-report":
		cmdVerifliersDiscoveryReport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown verifliers subcommand %q (want: discovery-report)\n", args[0])
		os.Exit(1)
	}
}

func cmdVerifliersDiscoveryReport(args []string) {
	opts := veriflierDiscoveryReportOptions{
		Output:       "text",
		StaleAfter:   db.VeriflierDiscoveryDefaultStaleAfter,
		QueryTimeout: defaultVeriflierDiscoveryQueryTimeout,
		ProbeTimeout: defaultVeriflierDiscoveryProbeTimeout,
		ProbeStatic:  true,
	}
	fs := newVeriflierDiscoveryReportFlagSet(&opts, os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "FAIL parse verifliers discovery-report flags: %v\n", err)
		os.Exit(2)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 verifliers discovery-report [--output=text|json] [--stale-after=90s] [--query-timeout=30s] [--probe-timeout=2s] [--probe-static=true]")
		os.Exit(1)
	}
	if err := validateVeriflierDiscoveryReportOptions(&opts); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL config parse: %v\n", err)
		os.Exit(1)
	}
	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL db connect: %v\n", err)
		os.Exit(1)
	}

	deps := veriflierDiscoveryReportDeps{
		ProbeConfigured: probeConfiguredVerifliers,
		ListSnapshot:    db.ListVeriflierDiscoverySnapshot,
	}
	report, err := buildVeriflierDiscoveryReport(context.Background(), config.Get(), opts, deps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL verifliers discovery-report: %v\n", err)
		os.Exit(1)
	}
	if err := renderVeriflierDiscoveryReport(os.Stdout, report, opts.Output); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL render verifliers discovery-report: %v\n", err)
		os.Exit(1)
	}
}

func newVeriflierDiscoveryReportFlagSet(opts *veriflierDiscoveryReportOptions, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("verifliers discovery-report", flag.ContinueOnError)
	if out != nil {
		fs.SetOutput(out)
	}
	fs.StringVar(&opts.Output, "output", opts.Output, "output format: text or json")
	fs.DurationVar(&opts.StaleAfter, "stale-after", opts.StaleAfter, "maximum agent age considered recent")
	fs.DurationVar(&opts.QueryTimeout, "query-timeout", opts.QueryTimeout, "maximum time for DB discovery queries")
	fs.DurationVar(&opts.ProbeTimeout, "probe-timeout", opts.ProbeTimeout, "maximum time per static Veriflier status probe")
	fs.BoolVar(&opts.ProbeStatic, "probe-static", opts.ProbeStatic, "probe configured static Verifliers for v2 vantage identity")
	fs.Usage = func() {
		printAPIFlagUsage(fs.Output(), fs)
	}
	return fs
}

func validateVeriflierDiscoveryReportOptions(opts *veriflierDiscoveryReportOptions) error {
	output, err := normalizeVeriflierDiscoveryOutput(opts.Output)
	if err != nil {
		return err
	}
	opts.Output = output
	if opts.StaleAfter <= 0 {
		return errors.New("--stale-after must be > 0")
	}
	if opts.StaleAfter > maxVeriflierDiscoveryTimeout {
		return fmt.Errorf("--stale-after must be <= %s", maxVeriflierDiscoveryTimeout)
	}
	if opts.QueryTimeout < 0 {
		return errors.New("--query-timeout must be >= 0")
	}
	if opts.QueryTimeout > maxVeriflierDiscoveryTimeout {
		return fmt.Errorf("--query-timeout must be <= %s", maxVeriflierDiscoveryTimeout)
	}
	if opts.ProbeTimeout <= 0 {
		return errors.New("--probe-timeout must be > 0")
	}
	if opts.ProbeTimeout > maxVeriflierDiscoveryTimeout {
		return fmt.Errorf("--probe-timeout must be <= %s", maxVeriflierDiscoveryTimeout)
	}
	return nil
}

func normalizeVeriflierDiscoveryOutput(output string) (string, error) {
	output = strings.ToLower(strings.TrimSpace(output))
	if output == "" {
		output = "text"
	}
	if output != "text" && output != "json" {
		return "", errors.New("--output must be text or json")
	}
	return output, nil
}

func buildVeriflierDiscoveryReport(ctx context.Context, cfg *config.Config, opts veriflierDiscoveryReportOptions, deps veriflierDiscoveryReportDeps) (veriflierDiscoveryReport, error) {
	if cfg == nil {
		return veriflierDiscoveryReport{}, errors.New("config is not loaded")
	}
	now := time.Now().UTC()
	if deps.Now != nil {
		now = deps.Now().UTC()
	}
	if deps.ListSnapshot == nil {
		return veriflierDiscoveryReport{}, errors.New("discovery snapshot query is not configured")
	}

	queryCtx := ctx
	var cancel context.CancelFunc
	if opts.QueryTimeout > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, opts.QueryTimeout)
	} else {
		queryCtx, cancel = context.WithCancel(ctx)
	}
	snapshot, err := deps.ListSnapshot(queryCtx, opts.StaleAfter)
	cancel()
	if err != nil {
		return veriflierDiscoveryReport{}, fmt.Errorf("query veriflier discovery snapshot: %w", err)
	}

	var probes []veriflierReadinessResult
	if opts.ProbeStatic {
		if deps.ProbeConfigured == nil {
			return veriflierDiscoveryReport{}, errors.New("static Veriflier probe is not configured")
		}
		probes = deps.ProbeConfigured(ctx, cfg, opts.ProbeTimeout)
	}

	report := veriflierDiscoveryReport{
		Command:           "verifliers discovery-report",
		GeneratedAt:       now,
		DiscoveryMode:     cfg.VeriflierDiscoveryModeOrDefault(),
		StaleAfterSeconds: int64(opts.StaleAfter.Round(time.Second) / time.Second),
		ProbeStatic:       opts.ProbeStatic,
		StaticVerifiers:   buildVeriflierDiscoveryStaticRows(cfg, probes, opts.ProbeStatic),
	}
	report.Static = summarizeVeriflierDiscoveryStatic(report.StaticVerifiers)
	report.Vantages = buildVeriflierDiscoveryVantageRows(snapshot.Vantages, now)
	report.Registry = summarizeVeriflierDiscoveryRegistry(report.Vantages)
	report.AgentRows = buildVeriflierDiscoveryAgentRows(snapshot.Agents, now)
	report.Agents = summarizeVeriflierDiscoveryAgents(report.AgentRows, report.StaleAfterSeconds)
	report.Issues = veriflierDiscoveryIssues(report)
	report.Status = veriflierDiscoveryStatus(report.Issues)
	report.OK = report.Status == "green"
	report.SuggestedNextAction = suggestVeriflierDiscoveryNextAction(report)
	return report, nil
}

func buildVeriflierDiscoveryStaticRows(cfg *config.Config, probes []veriflierReadinessResult, probed bool) []veriflierDiscoveryStaticRow {
	if cfg == nil || len(cfg.Verifiers) == 0 {
		return nil
	}
	rows := make([]veriflierDiscoveryStaticRow, 0, len(cfg.Verifiers))
	if probed {
		for i, v := range cfg.Verifiers {
			row := veriflierDiscoveryStaticRow{
				Name:             configuredVeriflierName(v, i),
				Addr:             cfg.VeriflierEndpoint(v),
				Host:             strings.TrimSpace(v.Host),
				Port:             strings.TrimSpace(v.TransportPort()),
				AuthTokenPresent: strings.TrimSpace(v.AuthToken) != "",
				ProbeStatus:      "not_probed",
			}
			if i < len(probes) {
				applyVeriflierProbeToStaticRow(&row, probes[i])
			}
			rows = append(rows, row)
		}
		return rows
	}
	for i, v := range cfg.Verifiers {
		rows = append(rows, veriflierDiscoveryStaticRow{
			Name:             configuredVeriflierName(v, i),
			Addr:             cfg.VeriflierEndpoint(v),
			Host:             strings.TrimSpace(v.Host),
			Port:             strings.TrimSpace(v.TransportPort()),
			AuthTokenPresent: strings.TrimSpace(v.AuthToken) != "",
			ProbeStatus:      "not_probed",
		})
	}
	return rows
}

func applyVeriflierProbeToStaticRow(row *veriflierDiscoveryStaticRow, result veriflierReadinessResult) {
	row.Name = result.Name
	row.Addr = result.Addr
	if result.Err != nil {
		row.ProbeStatus = "error"
		row.Error = result.Err.Error()
		return
	}
	if result.Status == nil {
		row.ProbeStatus = "error"
		row.Error = "empty status response"
		return
	}
	row.Version = result.Status.Version
	row.AgentID = strings.TrimSpace(result.Status.Agent.ID)
	row.Capacity = verifierCapacitySummary(result.Status.Capacity)
	if statusSupportsProtocol(result.Status, veriflier.ProtocolV2) {
		row.ProbeStatus = "v2"
		row.Protocol = veriflier.ProtocolV2
		row.VantageID = strings.TrimSpace(result.Status.Vantage.ID)
		return
	}
	row.ProbeStatus = "legacy"
	row.Protocol = veriflier.ProtocolLegacy
}

func summarizeVeriflierDiscoveryStatic(rows []veriflierDiscoveryStaticRow) veriflierDiscoveryStaticSummary {
	summary := veriflierDiscoveryStaticSummary{Configured: len(rows)}
	counts := make(map[string]int)
	for _, row := range rows {
		if row.ProbeStatus != "not_probed" {
			summary.Probed++
		}
		switch row.ProbeStatus {
		case "v2":
			summary.V2++
		case "legacy":
			summary.LegacyOnly++
		case "error":
			summary.ProbeErrors++
		}
		if row.VantageID != "" {
			counts[row.VantageID]++
		}
	}
	for _, count := range counts {
		if count > 1 {
			summary.DuplicateVantages += count
			continue
		}
		summary.UniqueVantages++
	}
	return summary
}

func buildVeriflierDiscoveryVantageRows(vantages []db.VeriflierVantage, now time.Time) []veriflierDiscoveryVantageRow {
	rows := make([]veriflierDiscoveryVantageRow, 0, len(vantages))
	for _, v := range vantages {
		row := veriflierDiscoveryVantageRow{
			VantageID:        strings.TrimSpace(v.VantageID),
			Region:           strings.TrimSpace(v.Region),
			Provider:         strings.TrimSpace(v.Provider),
			Endpoint:         endpointString(v.EndpointHost, v.EndpointPort),
			Enabled:          v.Enabled,
			Usable:           v.Usable(),
			AuthTokenPresent: strings.TrimSpace(v.AuthToken) != "",
			ActiveAgents:     v.ActiveAgents,
			LastSeen:         v.LastSeen,
		}
		if v.LastSeen != nil {
			age := durationSeconds(now.Sub(*v.LastSeen))
			row.LastSeenAgeSec = &age
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].VantageID < rows[j].VantageID
	})
	return rows
}

func summarizeVeriflierDiscoveryRegistry(rows []veriflierDiscoveryVantageRow) veriflierDiscoveryRegistrySummary {
	summary := veriflierDiscoveryRegistrySummary{Total: len(rows)}
	for _, row := range rows {
		if row.Enabled {
			summary.Enabled++
			if row.Usable {
				summary.Usable++
			} else {
				summary.Incomplete++
			}
		} else {
			summary.Disabled++
		}
	}
	return summary
}

func buildVeriflierDiscoveryAgentRows(agents []db.VeriflierAgent, now time.Time) []veriflierDiscoveryAgentRow {
	rows := make([]veriflierDiscoveryAgentRow, 0, len(agents))
	for _, agent := range agents {
		rows = append(rows, veriflierDiscoveryAgentRow{
			AgentID:        strings.TrimSpace(agent.AgentID),
			VantageID:      strings.TrimSpace(agent.VantageID),
			Hostname:       strings.TrimSpace(agent.Hostname),
			Endpoint:       endpointString(agent.EndpointHost, agent.EndpointPort),
			Version:        strings.TrimSpace(agent.Version),
			Protocols:      append([]string(nil), agent.Protocols...),
			Status:         strings.TrimSpace(agent.Status),
			LastSeen:       agent.LastSeen,
			LastSeenAgeSec: durationSeconds(now.Sub(agent.LastSeen)),
			MaxConcurrency: agent.MaxConcurrency,
			QueueCapacity:  agent.QueueCapacity,
			QueueDepth:     agent.QueueDepth,
			Active:         agent.Active,
			InFlight:       agent.InFlight,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].VantageID == rows[j].VantageID {
			return rows[i].AgentID < rows[j].AgentID
		}
		return rows[i].VantageID < rows[j].VantageID
	})
	return rows
}

func summarizeVeriflierDiscoveryAgents(rows []veriflierDiscoveryAgentRow, staleAfterSec int64) veriflierDiscoveryAgentSummary {
	summary := veriflierDiscoveryAgentSummary{Recent: len(rows), StaleAfterSec: staleAfterSec}
	for _, row := range rows {
		if row.Status == "active" {
			summary.Active++
		}
		summary.MaxConcurrency += row.MaxConcurrency
		summary.QueueCapacity += row.QueueCapacity
		summary.QueueDepth += row.QueueDepth
		summary.InFlight += row.InFlight
	}
	return summary
}

func veriflierDiscoveryIssues(report veriflierDiscoveryReport) []veriflierDiscoveryIssue {
	var issues []veriflierDiscoveryIssue
	staticByVantage := make(map[string][]veriflierDiscoveryStaticRow)
	for _, row := range report.StaticVerifiers {
		if row.ProbeStatus == "error" {
			issues = append(issues, veriflierDiscoveryIssue{"warn", "static_probe_failed", fmt.Sprintf("%s at %s: %s", row.Name, row.Addr, row.Error)})
			continue
		}
		if row.ProbeStatus == "legacy" {
			issues = append(issues, veriflierDiscoveryIssue{"warn", "static_legacy_only", fmt.Sprintf("%s at %s does not report the v2 status contract", row.Name, row.Addr)})
			continue
		}
		if row.ProbeStatus == "v2" && row.VantageID == "" {
			issues = append(issues, veriflierDiscoveryIssue{"warn", "static_vantage_missing", fmt.Sprintf("%s at %s reports v2 without a vantage id", row.Name, row.Addr)})
			continue
		}
		if row.VantageID != "" {
			staticByVantage[row.VantageID] = append(staticByVantage[row.VantageID], row)
		}
	}
	for id, rows := range staticByVantage {
		if len(rows) <= 1 {
			continue
		}
		issues = append(issues, veriflierDiscoveryIssue{"fail", "static_vantage_duplicate", fmt.Sprintf("vantage_id=%q is reported by %d configured static Verifliers", id, len(rows))})
	}
	if !report.ProbeStatic && (report.DiscoveryMode == config.VeriflierDiscoveryModeShadow || report.DiscoveryMode == config.VeriflierDiscoveryModeActive) {
		issues = append(issues, veriflierDiscoveryIssue{"warn", "static_not_probed", "static Verifliers were not probed, so static-vs-registry vantage drift cannot be proven"})
	}

	registryEnabled := make(map[string]veriflierDiscoveryVantageRow)
	registryAll := make(map[string]veriflierDiscoveryVantageRow)
	for _, row := range report.Vantages {
		registryAll[row.VantageID] = row
		if row.Enabled {
			registryEnabled[row.VantageID] = row
			if !row.Usable {
				severity := "warn"
				if report.DiscoveryMode == config.VeriflierDiscoveryModeActive {
					severity = "fail"
				}
				issues = append(issues, veriflierDiscoveryIssue{severity, "registry_enabled_incomplete", fmt.Sprintf("vantage_id=%q is enabled but missing endpoint host, endpoint port, or auth token", row.VantageID)})
			}
		}
	}
	if report.DiscoveryMode == config.VeriflierDiscoveryModeActive && report.Registry.Usable == 0 {
		issues = append(issues, veriflierDiscoveryIssue{"fail", "active_without_usable_registry", "active discovery has zero enabled usable trusted vantages and would fall back to static config"})
	}

	for id, staticRows := range staticByVantage {
		registry, ok := registryEnabled[id]
		if !ok {
			issues = append(issues, veriflierDiscoveryIssue{"warn", "static_missing_enabled_registry", fmt.Sprintf("static vantage_id=%q is not present as an enabled trusted registry row", id)})
			continue
		}
		for _, staticRow := range staticRows {
			staticEndpoint := endpointString(staticRow.Host, staticRow.Port)
			if registry.Endpoint != "" && staticEndpoint != "" && registry.Endpoint != staticEndpoint {
				issues = append(issues, veriflierDiscoveryIssue{"warn", "static_registry_endpoint_mismatch", fmt.Sprintf("vantage_id=%q static_endpoint=%q registry_endpoint=%q", id, staticEndpoint, registry.Endpoint)})
			}
			if staticRow.AuthTokenPresent != registry.AuthTokenPresent {
				issues = append(issues, veriflierDiscoveryIssue{"warn", "static_registry_auth_presence_mismatch", fmt.Sprintf("vantage_id=%q static_auth_token_present=%t registry_auth_token_present=%t", id, staticRow.AuthTokenPresent, registry.AuthTokenPresent)})
			}
		}
	}
	for id := range registryEnabled {
		if _, ok := staticByVantage[id]; !ok && report.ProbeStatic {
			issues = append(issues, veriflierDiscoveryIssue{"warn", "enabled_registry_missing_static", fmt.Sprintf("enabled registry vantage_id=%q was not observed in static configured Verifliers", id)})
		}
	}

	activeAgentsByVantage := make(map[string][]veriflierDiscoveryAgentRow)
	activeEndpointsByVantage := make(map[string]map[string]int)
	for _, agent := range report.AgentRows {
		if _, ok := registryAll[agent.VantageID]; !ok {
			issues = append(issues, veriflierDiscoveryIssue{"warn", "agent_without_registry", fmt.Sprintf("agent_id=%q reports untrusted vantage_id=%q", agent.AgentID, agent.VantageID)})
		}
		if agent.Status != "active" {
			continue
		}
		activeAgentsByVantage[agent.VantageID] = append(activeAgentsByVantage[agent.VantageID], agent)
		if activeEndpointsByVantage[agent.VantageID] == nil {
			activeEndpointsByVantage[agent.VantageID] = make(map[string]int)
		}
		activeEndpointsByVantage[agent.VantageID][agent.Endpoint]++
		if registry, ok := registryAll[agent.VantageID]; ok && registry.Endpoint != "" && agent.Endpoint != "" && registry.Endpoint != agent.Endpoint {
			issues = append(issues, veriflierDiscoveryIssue{"warn", "agent_registry_endpoint_mismatch", fmt.Sprintf("agent_id=%q vantage_id=%q agent_endpoint=%q registry_endpoint=%q", agent.AgentID, agent.VantageID, agent.Endpoint, registry.Endpoint)})
		}
	}
	for id, registry := range registryEnabled {
		if !registry.Usable {
			continue
		}
		if len(activeAgentsByVantage[id]) == 0 {
			issues = append(issues, veriflierDiscoveryIssue{"warn", "enabled_registry_without_active_agent", fmt.Sprintf("enabled usable vantage_id=%q has no recent active agent telemetry", id)})
		}
	}
	for id, endpoints := range activeEndpointsByVantage {
		if len(endpoints) <= 1 {
			continue
		}
		issues = append(issues, veriflierDiscoveryIssue{"warn", "duplicate_active_agent_endpoints", fmt.Sprintf("vantage_id=%q has fresh active agents reporting %d different endpoints", id, len(endpoints))})
	}

	sort.SliceStable(issues, func(i, j int) bool {
		if issueSeverityRank(issues[i].Severity) == issueSeverityRank(issues[j].Severity) {
			if issues[i].Name == issues[j].Name {
				return issues[i].Detail < issues[j].Detail
			}
			return issues[i].Name < issues[j].Name
		}
		return issueSeverityRank(issues[i].Severity) > issueSeverityRank(issues[j].Severity)
	})
	return issues
}

func issueSeverityRank(severity string) int {
	switch severity {
	case "fail":
		return 3
	case "warn":
		return 2
	default:
		return 1
	}
}

func veriflierDiscoveryStatus(issues []veriflierDiscoveryIssue) string {
	status := "green"
	for _, issue := range issues {
		switch issue.Severity {
		case "fail":
			return "red"
		case "warn":
			status = "amber"
		}
	}
	return status
}

func suggestVeriflierDiscoveryNextAction(report veriflierDiscoveryReport) string {
	if report.Status == "red" {
		return "Fix red Veriflier discovery issues before enabling or relying on active discovery."
	}
	if !report.ProbeStatic && (report.DiscoveryMode == config.VeriflierDiscoveryModeShadow || report.DiscoveryMode == config.VeriflierDiscoveryModeActive) {
		return "Rerun with --probe-static=true so the static configured vantages can be compared to the trusted registry."
	}
	if report.Status == "amber" {
		return "Fix the listed Veriflier discovery drift, stale telemetry, or registry gaps, then rerun this report before changing discovery mode."
	}
	switch report.DiscoveryMode {
	case config.VeriflierDiscoveryModeStatic:
		return "Static discovery is still configured; seed trusted registry rows and run this report again in shadow mode before active mode."
	case config.VeriflierDiscoveryModeShadow:
		return "Shadow discovery matches the static vantages; keep observing fresh agent telemetry, then plan active discovery with fallback still configured."
	case config.VeriflierDiscoveryModeActive:
		return "Active discovery looks healthy; keep watching fleet dashboard Veriflier warnings during rollout."
	default:
		return "Discovery report is clean."
	}
}

func renderVeriflierDiscoveryReport(out io.Writer, report veriflierDiscoveryReport, output string) error {
	switch output {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case "text":
		renderVeriflierDiscoveryText(out, report)
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", output)
	}
}

func renderVeriflierDiscoveryText(out io.Writer, report veriflierDiscoveryReport) {
	fmt.Fprintf(out, "INFO veriflier_discovery_report_generated_at=%s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(out, "INFO veriflier_discovery_mode=%s status=%s stale_after=%ds probe_static=%t\n", report.DiscoveryMode, report.Status, report.StaleAfterSeconds, report.ProbeStatic)
	fmt.Fprintf(out, "INFO static_configured=%d static_probed=%d static_v2=%d static_legacy_only=%d static_probe_errors=%d static_unique_vantages=%d static_duplicate_vantages=%d\n",
		report.Static.Configured, report.Static.Probed, report.Static.V2, report.Static.LegacyOnly, report.Static.ProbeErrors, report.Static.UniqueVantages, report.Static.DuplicateVantages)
	fmt.Fprintf(out, "INFO registry_total=%d registry_enabled=%d registry_disabled=%d registry_usable=%d registry_incomplete=%d\n",
		report.Registry.Total, report.Registry.Enabled, report.Registry.Disabled, report.Registry.Usable, report.Registry.Incomplete)
	fmt.Fprintf(out, "INFO agents_recent=%d agents_active=%d max_concurrency=%d queue_depth=%d queue_capacity=%d in_flight=%d\n",
		report.Agents.Recent, report.Agents.Active, report.Agents.MaxConcurrency, report.Agents.QueueDepth, report.Agents.QueueCapacity, report.Agents.InFlight)
	for _, issue := range report.Issues {
		level := "INFO"
		if issue.Severity == "warn" {
			level = "WARN"
		} else if issue.Severity == "fail" {
			level = "FAIL"
		}
		fmt.Fprintf(out, "%s veriflier_discovery_issue name=%q detail=%q\n", level, issue.Name, issue.Detail)
	}
	for _, row := range report.StaticVerifiers {
		if row.Error != "" {
			fmt.Fprintf(out, "INFO static_veriflier name=%q addr=%q probe_status=%s error=%q auth_token_present=%t\n", row.Name, row.Addr, row.ProbeStatus, row.Error, row.AuthTokenPresent)
			continue
		}
		fmt.Fprintf(out, "INFO static_veriflier name=%q addr=%q probe_status=%s protocol=%q vantage_id=%q agent_id=%q version=%q capacity=%q auth_token_present=%t\n",
			row.Name, row.Addr, row.ProbeStatus, row.Protocol, row.VantageID, row.AgentID, row.Version, row.Capacity, row.AuthTokenPresent)
	}
	for _, row := range report.Vantages {
		age := ""
		if row.LastSeenAgeSec != nil {
			age = fmt.Sprintf(" last_seen_age_sec=%d", *row.LastSeenAgeSec)
		}
		fmt.Fprintf(out, "INFO registry_vantage vantage_id=%q enabled=%t usable=%t endpoint=%q region=%q provider=%q active_agents=%d auth_token_present=%t%s\n",
			row.VantageID, row.Enabled, row.Usable, row.Endpoint, row.Region, row.Provider, row.ActiveAgents, row.AuthTokenPresent, age)
	}
	for _, row := range report.AgentRows {
		fmt.Fprintf(out, "INFO veriflier_agent agent_id=%q vantage_id=%q status=%q endpoint=%q hostname=%q version=%q protocols=%q last_seen_age_sec=%d capacity=%q\n",
			row.AgentID, row.VantageID, row.Status, row.Endpoint, row.Hostname, row.Version, strings.Join(row.Protocols, ","), row.LastSeenAgeSec,
			verifierCapacitySummary(veriflier.Capacity{MaxConcurrency: row.MaxConcurrency, QueueCapacity: row.QueueCapacity, QueueDepth: row.QueueDepth, Active: row.Active, InFlight: row.InFlight}))
	}
	if report.Status == "green" {
		fmt.Fprintln(out, "PASS veriflier_discovery_report=green")
	} else if report.Status == "amber" {
		fmt.Fprintln(out, "WARN veriflier_discovery_report=amber")
	} else {
		fmt.Fprintln(out, "FAIL veriflier_discovery_report=red")
	}
	fmt.Fprintf(out, "INFO suggested_next_action=%q\n", report.SuggestedNextAction)
}

func endpointString(host, port string) string {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" && port == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s", host, port)
}

func durationSeconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d.Round(time.Second) / time.Second)
}

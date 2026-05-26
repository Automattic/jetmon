package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

type doctorResult struct {
	Name   string
	Status string
	Detail string
}

func cmdDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	requireStatsD := fs.Bool("require-statsd", false, "fail if StatsD is disabled or cannot accept a UDP smoke metric")
	skipVerifliers := fs.Bool("skip-verifliers", false, "skip configured Veriflier status probes")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage of doctor:")
		printAPIFlagDefaults(fs.Output(), fs)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(1)
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "doctor: usage: jetmon2 doctor [--require-statsd] [--skip-verifliers]")
		os.Exit(1)
	}

	results, err := runDoctor(*requireStatsD, *skipVerifliers)
	for _, result := range results {
		fmt.Printf("%s %s %s\n", result.Status, result.Name, result.Detail)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "doctor:", err)
		os.Exit(1)
	}
	fmt.Println("doctor passed")
}

func runDoctor(requireStatsD, skipVerifliers bool) ([]doctorResult, error) {
	results := make([]doctorResult, 0, 8)
	failures := make([]string, 0)
	add := func(name, status, detail string) {
		results = append(results, doctorResult{Name: name, Status: status, Detail: detail})
		if status == "FAIL" {
			failures = append(failures, name)
		}
	}

	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		add("config", "FAIL", err.Error())
		return results, errors.New("config check failed")
	}
	cfg := config.Get()
	add("config", "PASS", fmt.Sprintf("profile=%s schema_management=%s rollout_mode=%s", cfg.ConfigProfile, cfg.SchemaManagementMode, cfg.RolloutMode))
	printConfigWarnings(os.Stdout, cfg)

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		add("db", "FAIL", err.Error())
		return results, errors.New("database connection failed")
	}
	add("db", "PASS", "read/write pools pinged")

	if status, err := db.ValidateSchema(context.Background()); err != nil {
		add("schema", "FAIL", fmt.Sprintf("%v %s", err, status.Summary()))
	} else {
		add("schema", "PASS", status.Summary())
	}

	add("db_config", "PASS", doctorDBConfigDetail(db.ConfigStatusSnapshot()))

	if status, detail := doctorStatsDCheck(cfg, requireStatsD); status == "FAIL" {
		add("statsd", "FAIL", detail)
	} else {
		add("statsd", status, detail)
	}

	if status, detail := doctorWPCOMConfigCheck(cfg); status == "FAIL" {
		add("wpcom", "FAIL", detail)
	} else {
		add("wpcom", status, detail)
	}

	if !skipVerifliers {
		readiness := probeConfiguredVerifliers(context.Background(), cfg, dashboardHealthTimeout)
		lines, failed := renderVeriflierReadiness(readiness)
		for _, line := range lines {
			add("veriflier_detail", "INFO", line)
		}
		if failed {
			add("verifliers", "FAIL", "one or more configured Verifliers failed v2 readiness")
		} else {
			add("verifliers", "PASS", fmt.Sprintf("checked=%d", len(readiness)))
		}
	}

	if len(failures) > 0 {
		return results, fmt.Errorf("failed checks: %s", strings.Join(failures, ", "))
	}
	return results, nil
}

func doctorDBConfigDetail(status db.ConfigStatus) string {
	parts := []string{
		"mode=" + status.Mode,
		"source=" + status.Source,
		fmt.Sprintf("reload_enabled=%t", status.ReloadEnabled),
	}
	if len(status.ReadEndpoints) > 0 {
		parts = append(parts, "read="+strings.Join(status.ReadEndpoints, ","))
	}
	if len(status.WriteEndpoints) > 0 {
		parts = append(parts, "write="+strings.Join(status.WriteEndpoints, ","))
	}
	if status.LastReloadError != "" {
		parts = append(parts, "last_reload_error="+status.LastReloadError)
	}
	return strings.Join(parts, " ")
}

func doctorStatsDCheck(cfg *config.Config, require bool) (string, string) {
	addr := ""
	if cfg != nil {
		addr = strings.TrimSpace(cfg.StatsDAddr)
	}
	if strings.TrimSpace(addr) == "" {
		if require {
			return "FAIL", "STATSD_ADDR is unset or empty"
		}
		return "WARN", "STATSD_ADDR is unset or empty; StatsD disabled"
	}
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return "FAIL", fmt.Sprintf("resolve %s: %v", addr, err)
	}
	conn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		return "FAIL", fmt.Sprintf("dial %s: %v", addr, err)
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	metricHost := db.Hostname()
	if cfg != nil {
		metricHost = cfg.StatsDMetricHost(metricHost)
	}
	line := fmt.Sprintf("com.jetpack.jetmon.%s.doctor:1|c", metricHost)
	if _, err := conn.Write([]byte(line)); err != nil {
		return "FAIL", fmt.Sprintf("write %s: %v", addr, err)
	}
	return "PASS", fmt.Sprintf("addr=%s smoke_metric=%s", addr, line)
}

func doctorWPCOMConfigCheck(cfg *config.Config) (string, string) {
	if cfg == nil {
		return "FAIL", "config not loaded"
	}
	if !cfg.WPCOMNotifyEnable {
		return "PASS", "disabled_by_config"
	}
	switch cfg.WPCOMNotifyMode {
	case config.WPCOMNotifyModeLegacy:
		if _, err := url.ParseRequestURI(cfg.WPCOMNotifyLegacyEndpoint); err != nil {
			return "FAIL", fmt.Sprintf("legacy endpoint invalid: %v", err)
		}
		if strings.HasPrefix(strings.ToLower(cfg.WPCOMNotifyLegacyEndpoint), "https://") {
			if _, err := tls.LoadX509KeyPair(cfg.WPCOMNotifyLegacyCertPath, cfg.WPCOMNotifyLegacyKeyPath); err != nil {
				return "FAIL", fmt.Sprintf("legacy client certificate/key unreadable: %v", err)
			}
			return "PASS", fmt.Sprintf("mode=legacy endpoint=%s client_cert=readable", cfg.WPCOMNotifyLegacyEndpoint)
		}
		return "PASS", fmt.Sprintf("mode=legacy endpoint=%s non_https_fixture=true", cfg.WPCOMNotifyLegacyEndpoint)
	case config.WPCOMNotifyModeModern:
		if _, err := url.ParseRequestURI(cfg.WPCOMNotifyModernEndpoint); err != nil {
			return "FAIL", fmt.Sprintf("modern endpoint invalid: %v", err)
		}
		if strings.TrimSpace(cfg.AuthToken) == "" {
			return "FAIL", "modern mode requires AUTH_TOKEN"
		}
		return "PASS", fmt.Sprintf("mode=modern endpoint=%s auth_token_present=true", cfg.WPCOMNotifyModernEndpoint)
	default:
		return "FAIL", fmt.Sprintf("unknown mode=%s", cfg.WPCOMNotifyMode)
	}
}

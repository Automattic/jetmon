package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/metrics"
	"github.com/Automattic/jetmon/internal/opsalerts"
	"github.com/Automattic/jetmon/internal/processcontrol"
	"github.com/Automattic/jetmon/internal/veriflier"
)

// Injected at build time via -ldflags.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
	goVersion = "unknown"
)
var enforceOutboundTargetSafety = true

const shutdownGracePeriod = 30 * time.Second

type veriflierConfig struct {
	AuthToken   string `json:"auth_token"`
	Port        string `json:"port"`
	GRPCPort    string `json:"grpc_port"` // Deprecated alias for Port.
	Hostname    string `json:"hostname"`
	StatsDAddr  string `json:"statsd_addr"`
	StatsDPath  string `json:"statsd_host_path"`
	VantageID   string `json:"vantage_id"`
	Region      string `json:"region"`
	Provider    string `json:"provider"`
	TLSCertPath string `json:"tls_cert_path"`
	TLSKeyPath  string `json:"tls_key_path"`
	LegacyHTTP  bool   `json:"enable_legacy_http"`
	// CheckTargetSafetyMode uses the same values as Monitor:
	// public_only or allow_private_for_tests.
	CheckTargetSafetyMode      string `json:"check_target_safety_mode"`
	OpsAlertsEnabled           bool   `json:"ops_alerts_enabled"`
	OpsAlertsSlackWebhookURL   string `json:"ops_alerts_slack_webhook_url"`
	OpsAlertsMinSeverity       string `json:"ops_alerts_min_severity"`
	OpsAlertsRepeatIntervalSec int    `json:"ops_alerts_repeat_interval_sec"`
	OpsAlertsServiceOnline     bool   `json:"ops_alerts_service_online"`
}

func main() {
	configPath := envOrDefault("VERIFLIER_CONFIG", "config/veriflier.json")

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	cfg.StatsDAddr = strings.TrimSpace(cfg.StatsDAddr)
	if err := validateStatsDAddr(cfg.StatsDAddr); err != nil {
		log.Fatalf("statsd_addr: %v", err)
	}
	cfg.StatsDPath = strings.TrimSpace(cfg.StatsDPath)
	if err := validateStatsDHostPath(cfg.StatsDPath); err != nil {
		log.Fatalf("statsd_host_path: %v", err)
	}

	if cfg.TransportPort() == "" {
		log.Fatalf("VERIFLIER_PORT is not set")
	}
	// Reject empty auth tokens at startup. The verifier's Bearer comparison
	// would otherwise accept any request with the literal "Bearer " header
	// (no token after the space) — a subtle auth bypass if a misconfigured
	// deploy leaves the token blank. Better to fail loud at startup.
	if cfg.AuthToken == "" {
		log.Fatalf("VERIFLIER_AUTH_TOKEN is not set; refusing to start with no authentication")
	}
	enforceOutboundTargetSafety = cfg.CheckTargetSafetyMode != config.CheckTargetSafetyModeAllowPrivateForTests
	if !enforceOutboundTargetSafety {
		log.Printf("WARN: check_target_safety_mode=%s; private, localhost, reserved, and internal-only targets are allowed. Use only in isolated synthetic labs.", cfg.CheckTargetSafetyMode)
	}

	hostname := configuredHostname(cfg.Hostname)
	addr := fmt.Sprintf(":%s", cfg.TransportPort())
	agentID := veriflierAgentID(hostname, cfg.TransportPort())
	ops := newVeriflierOpsAlertClientFromConfig(cfg, hostname)

	// Optional StatsD metrics. statsd_addr is empty in standalone deploys,
	// "statsd:8125" in the docker compose stack. metrics.Init failure logs and
	// continues — the verifier should still run with metrics disabled.
	var resourceStats resourceStatsEmitter
	if statsdAddr, enabled, err := metrics.InitConfigured(cfg.StatsDAddr, veriflierStatsDMetricHost(cfg, hostname)); err != nil {
		log.Printf("metrics: init failed (%v) — running without metrics", err)
	} else if enabled {
		config.Debugf("metrics: sending to %s", statsdAddr)
		if strings.TrimSpace(cfg.StatsDPath) == "" {
			log.Printf("WARN: statsd_host_path is unset; StatsD metrics will use Veriflier hostname %q", hostname)
		}
		resourceStats = metrics.Global()
	}
	stopResourceStats := startVeriflierResourceStats(resourceStats, 10*time.Second)
	defer stopResourceStats()

	srv := veriflier.NewServerWithOptions(addr, cfg.AuthToken, hostname, version, veriflier.ServerOptions{
		Commit:    commit,
		BuildDate: buildDate,
		GoVersion: goVersion,
		CheckFunc: performCheckContext,
		Vantage: veriflier.Vantage{
			ID:       cfg.VantageID,
			Region:   cfg.Region,
			Provider: cfg.Provider,
		},
		AgentID:      agentID,
		EnableLegacy: cfg.LegacyHTTP,
	})

	// Graceful shutdown: SIGINT/SIGTERM triggers Shutdown(ctx) with a drain
	// budget so in-flight checks can complete before the listener closes.
	// SIGHUP uses the same drain path and then re-execs through the configured
	// restart target so replaced binaries and rendered config are picked up.
	var restartRequested atomic.Bool
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		sig := <-sigCh
		if processcontrol.IsGracefulRestartSignal(sig) {
			restartRequested.Store(true)
			log.Printf("veriflier2: %s received, draining before graceful restart (up to %s)", sig, shutdownGracePeriod)
		} else {
			log.Printf("veriflier2: %s received, draining (up to %s)", sig, shutdownGracePeriod)
		}
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("veriflier2: shutdown error: %v", err)
		}
	}()

	log.Printf("veriflier2 %s starting on %s legacy_http=%s target_safety=%s tls=%s", version, addr, enabledLabel(cfg.LegacyHTTP), cfg.CheckTargetSafetyMode, enabledLabel(cfg.TLSCertPath != ""))
	ops.ServiceOnline(map[string]any{
		"port":               cfg.TransportPort(),
		"legacy_http":        cfg.LegacyHTTP,
		"tls_enabled":        cfg.TLSCertPath != "",
		"target_safety":      cfg.CheckTargetSafetyMode,
		"vantage_id":         cfg.VantageID,
		"region":             cfg.Region,
		"provider":           cfg.Provider,
		"statsd_enabled":     cfg.StatsDAddr != "",
		"statsd_host_path":   cfg.StatsDPath,
		"agent_id":           agentID,
		"process_started_at": time.Now().UTC().Format(time.RFC3339),
	})
	var listenErr error
	if cfg.TLSCertPath != "" {
		listenErr = srv.ListenTLS(cfg.TLSCertPath, cfg.TLSKeyPath)
	} else {
		listenErr = srv.Listen()
	}
	if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
		log.Fatalf("listen: %v", listenErr)
	}
	stopResourceStats()
	log.Println("veriflier2: shutdown complete")
	if restartRequested.Load() {
		log.Println("veriflier2: re-executing after graceful SIGHUP")
		if err := processcontrol.ExecSelf(); err != nil {
			log.Fatalf("veriflier2: re-exec failed: %v", err)
		}
	}
}

type resourceStatsEmitter interface {
	EmitMemStats()
}

func startVeriflierResourceStats(m resourceStatsEmitter, interval time.Duration) func() {
	if isNilResourceStatsEmitter(m) || interval <= 0 {
		return func() {}
	}
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		m.EmitMemStats()
		for {
			select {
			case <-ticker.C:
				m.EmitMemStats()
			case <-stop:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(stop) })
	}
}

func isNilResourceStatsEmitter(m resourceStatsEmitter) bool {
	if m == nil {
		return true
	}
	v := reflect.ValueOf(m)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func newVeriflierOpsAlertClientFromConfig(cfg *veriflierConfig, hostname string) *opsalerts.Client {
	if cfg == nil {
		return opsalerts.New(opsalerts.Config{})
	}
	return opsalerts.New(opsalerts.Config{
		Enabled:         cfg.OpsAlertsEnabled,
		SlackWebhookURL: cfg.OpsAlertsSlackWebhookURL,
		MinSeverity:     cfg.OpsAlertsMinSeverity,
		RepeatInterval:  time.Duration(cfg.OpsAlertsRepeatIntervalSec) * time.Second,
		ServiceOnline:   cfg.OpsAlertsServiceOnline,
		Service:         "veriflier",
		Host:            hostname,
		Version:         version,
		Commit:          commit,
		BuildDate:       buildDate,
		RunbookBaseURL:  "docs/operations-guide.md",
		LogSendFailures: true,
		AdditionalContext: map[string]string{
			"vantage_id": cfg.VantageID,
			"region":     cfg.Region,
			"provider":   cfg.Provider,
		},
	})
}

func veriflierAgentID(hostname, port string) string {
	hostname = strings.TrimSpace(hostname)
	port = strings.TrimSpace(port)
	if hostname == "" {
		hostname = "unknown"
	}
	if port == "" {
		return hostname
	}
	return hostname + ":" + port
}

// performCheck runs a single HTTP check and returns the result for the server.
func performCheck(req veriflier.CheckRequest) veriflier.CheckResult {
	return performCheckContext(context.Background(), req).CheckResult
}

func performCheckContext(ctx context.Context, req veriflier.CheckRequest) veriflier.ProbeResult {
	res := checker.SafeCheck(ctx, checker.Request{
		MonitorSiteID:       req.MonitorSiteID,
		BlogID:              req.BlogID,
		URL:                 req.URL,
		Method:              req.Method,
		DetectionProfile:    req.DetectionProfile,
		TimeoutSeconds:      int(req.TimeoutSeconds),
		BodyReadMaxBytes:    req.BodyReadMaxBytes,
		BodyReadMaxMS:       int(req.BodyReadMaxMS),
		KeywordReadMaxBytes: req.KeywordReadMaxBytes,
		KeywordReadMaxMS:    int(req.KeywordReadMaxMS),
		Keyword:             stringPtr(req.Keyword),
		ForbiddenKeyword:    stringPtr(req.ForbiddenKeyword),
		ForbiddenKeywords:   req.ForbiddenKeywords,
		CustomHeaders:       req.CustomHeaders,
		RedirectPolicy:      checker.RedirectPolicy(req.RedirectPolicy),
		EnforceTargetSafety: enforceOutboundTargetSafety,
	})

	checkResult := veriflier.CheckResult{
		MonitorSiteID: res.MonitorSiteID,
		BlogID:        res.BlogID,
		URL:           res.URL,
		Success:       res.Success,
		HTTPCode:      int32(res.HTTPCode),
		ErrorCode:     int32(res.ErrorCode),
		RTTMs:         res.RTT.Milliseconds(),
		Diagnostics:   diagnosticsFromCheckerResult(res),
	}
	return veriflier.ProbeResult{
		CheckResult: checkResult,
		Outcome:     outcomeFromCheckerResult(res),
		TimingsMS: veriflier.TimingsMS{
			DNS:  res.DNS.Milliseconds(),
			TCP:  res.TCP.Milliseconds(),
			TLS:  res.TLS.Milliseconds(),
			TTFB: res.TTFB.Milliseconds(),
		},
	}
}

func diagnosticsFromCheckerResult(res checker.Result) *veriflier.CheckDiagnostics {
	var diagnostics veriflier.CheckDiagnostics
	if res.KeywordRule != "" {
		diagnostics.KeywordRule = res.KeywordRule
	}
	if res.ErrorDetail != "" {
		diagnostics.ErrorDetail = res.ErrorDetail
	}
	if res.DNSFailureKind != "" {
		diagnostics.DNSFailureKind = res.DNSFailureKind
	}
	if res.DNSFailureName != "" {
		diagnostics.DNSFailureName = res.DNSFailureName
	}
	if res.DNSFailureServer != "" {
		diagnostics.DNSFailureServer = res.DNSFailureServer
	}
	if res.FinalURL != "" {
		diagnostics.FinalURL = res.FinalURL
	}
	if res.RedirectCount > 0 {
		diagnostics.RedirectCount = res.RedirectCount
	}
	if res.BodyReadMode != "" || res.BodyBytesRead != 0 || res.BodyExpectedBytes != 0 || res.BodyReadLimitBytes != 0 || res.BodyReadError != "" {
		diagnostics.BodyRead = &veriflier.BodyReadDiagnostics{
			Mode:          res.BodyReadMode,
			BytesRead:     res.BodyBytesRead,
			ExpectedBytes: res.BodyExpectedBytes,
			LimitBytes:    res.BodyReadLimitBytes,
			Error:         res.BodyReadError,
		}
	}
	if res.TLSVersion != 0 {
		diagnostics.TLSVersion = res.TLSVersion
	}
	if res.CipherSuite != 0 {
		diagnostics.CipherSuite = res.CipherSuite
	}
	if diagnostics == (veriflier.CheckDiagnostics{}) {
		return nil
	}
	return &diagnostics
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func loadConfig(path string) (*veriflierConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return envOnlyConfig()
		}
		return nil, err
	}
	defer f.Close()

	var cfg veriflierConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	return normalizeLoadedConfig(&cfg)
}

func envOnlyConfig() (*veriflierConfig, error) {
	opsRepeatInterval, err := intEnvOrDefault("OPS_ALERTS_REPEAT_INTERVAL_SEC", 300)
	if err != nil {
		return nil, fmt.Errorf("OPS_ALERTS_REPEAT_INTERVAL_SEC: %w", err)
	}
	cfg := &veriflierConfig{
		AuthToken:   os.Getenv("VERIFLIER_AUTH_TOKEN"),
		Port:        envOrDefault("VERIFLIER_PORT", envOrDefault("VERIFLIER_GRPC_PORT", "7803")),
		Hostname:    firstNonEmpty(os.Getenv("VERIFLIER_HOSTNAME"), os.Getenv("JETMON_HOSTNAME")),
		StatsDAddr:  os.Getenv("STATSD_ADDR"),
		StatsDPath:  os.Getenv("STATSD_HOST_PATH"),
		VantageID:   os.Getenv("VERIFLIER_VANTAGE_ID"),
		Region:      os.Getenv("VERIFLIER_REGION"),
		Provider:    os.Getenv("VERIFLIER_PROVIDER"),
		TLSCertPath: os.Getenv("VERIFLIER_TLS_CERT_PATH"),
		TLSKeyPath:  os.Getenv("VERIFLIER_TLS_KEY_PATH"),
		CheckTargetSafetyMode: firstNonEmpty(
			os.Getenv("VERIFLIER_CHECK_TARGET_SAFETY_MODE"),
			os.Getenv("CHECK_TARGET_SAFETY_MODE"),
		),
		OpsAlertsSlackWebhookURL:   os.Getenv("OPS_ALERTS_SLACK_WEBHOOK_URL"),
		OpsAlertsMinSeverity:       os.Getenv("OPS_ALERTS_MIN_SEVERITY"),
		OpsAlertsRepeatIntervalSec: opsRepeatInterval,
	}
	if v := os.Getenv("VERIFLIER_ENABLE_LEGACY_HTTP"); v != "" {
		enabled, err := parseBool(v)
		if err != nil {
			return nil, fmt.Errorf("VERIFLIER_ENABLE_LEGACY_HTTP: %w", err)
		}
		cfg.LegacyHTTP = enabled
	}
	if v := os.Getenv("OPS_ALERTS_ENABLED"); v != "" {
		enabled, err := parseBool(v)
		if err != nil {
			return nil, fmt.Errorf("OPS_ALERTS_ENABLED: %w", err)
		}
		cfg.OpsAlertsEnabled = enabled
	}
	if v := os.Getenv("OPS_ALERTS_SERVICE_ONLINE"); v != "" {
		enabled, err := parseBool(v)
		if err != nil {
			return nil, fmt.Errorf("OPS_ALERTS_SERVICE_ONLINE: %w", err)
		}
		cfg.OpsAlertsServiceOnline = enabled
	}
	return normalizeLoadedConfig(cfg)
}

func (c veriflierConfig) TransportPort() string {
	if c.Port != "" {
		return c.Port
	}
	return c.GRPCPort
}

func normalizeLoadedConfig(cfg *veriflierConfig) (*veriflierConfig, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.CheckTargetSafetyMode))
	if mode == "" {
		mode = config.CheckTargetSafetyModePublicOnly
	}
	if !config.ValidCheckTargetSafetyMode(mode) {
		return nil, fmt.Errorf("check_target_safety_mode must be one of: public_only, allow_private_for_tests")
	}
	cfg.CheckTargetSafetyMode = mode
	cfg.TLSCertPath = strings.TrimSpace(cfg.TLSCertPath)
	cfg.TLSKeyPath = strings.TrimSpace(cfg.TLSKeyPath)
	if cfg.TLSCertPath == "" && cfg.TLSKeyPath != "" || cfg.TLSCertPath != "" && cfg.TLSKeyPath == "" {
		return nil, fmt.Errorf("tls_cert_path and tls_key_path must be set together")
	}
	cfg.OpsAlertsSlackWebhookURL = strings.TrimSpace(cfg.OpsAlertsSlackWebhookURL)
	cfg.OpsAlertsMinSeverity = opsalerts.NormalizeSeverity(cfg.OpsAlertsMinSeverity)
	if cfg.OpsAlertsMinSeverity == "" {
		cfg.OpsAlertsMinSeverity = opsalerts.SeverityWarning
	}
	if cfg.OpsAlertsRepeatIntervalSec == 0 {
		cfg.OpsAlertsRepeatIntervalSec = 300
	}
	if cfg.OpsAlertsRepeatIntervalSec < 0 {
		return nil, fmt.Errorf("ops_alerts_repeat_interval_sec must be >= 0")
	}
	if cfg.OpsAlertsEnabled && cfg.OpsAlertsSlackWebhookURL == "" {
		return nil, fmt.Errorf("ops_alerts_slack_webhook_url is required when ops_alerts_enabled=true")
	}
	if cfg.OpsAlertsSlackWebhookURL != "" {
		u, err := url.Parse(cfg.OpsAlertsSlackWebhookURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, fmt.Errorf("ops_alerts_slack_webhook_url must be an absolute https URL")
		}
	}
	return cfg, nil
}

func configuredHostname(configured string) string {
	if h := strings.TrimSpace(configured); h != "" {
		return h
	}
	h, err := os.Hostname()
	if err != nil || strings.TrimSpace(h) == "" {
		return "unknown"
	}
	return h
}

func veriflierStatsDMetricHost(cfg *veriflierConfig, hostname string) string {
	if cfg != nil {
		if path := strings.TrimSpace(cfg.StatsDPath); path != "" {
			return path
		}
	}
	return strings.TrimSpace(hostname)
}

func validateStatsDHostPath(path string) error {
	if path == "" {
		return nil
	}
	if strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
		return fmt.Errorf("must not start or end with a dot")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("must not contain empty path segments")
	}
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.',
			r == '_',
			r == '-':
			continue
		default:
			return fmt.Errorf("may contain only letters, numbers, dots, underscores, and hyphens")
		}
	}
	return nil
}

func validateStatsDAddr(addr string) error {
	if addr == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("must be host:port")
	}
	if strings.TrimSpace(strings.Trim(host, "[]")) == "" {
		return fmt.Errorf("host must not be empty")
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum <= 0 || portNum > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func outcomeFromCheckerResult(res checker.Result) string {
	if res.Success {
		return veriflier.OutcomeUp
	}
	if res.ErrorCode == checker.ErrorProbeSafety {
		return veriflier.OutcomeUnknown
	}
	if res.ErrorCode == checker.ErrorInternal {
		return veriflier.OutcomeUnknown
	}
	if res.ErrorCode == checker.ErrorTimeout {
		return veriflier.OutcomeTimeout
	}
	if res.HTTPCode >= http.StatusBadRequest {
		return veriflier.OutcomeDown
	}
	if res.ErrorCode != checker.ErrorNone {
		return veriflier.OutcomeProbeError
	}
	return veriflier.OutcomeUnknown
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnvOrDefault(key string, def int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("expected integer value, got %q", raw)
	}
	return value, nil
}

func parseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on", "enabled":
		return true, nil
	case "0", "f", "false", "n", "no", "off", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("expected boolean value, got %q", raw)
	}
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

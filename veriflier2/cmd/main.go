package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/metrics"
	"github.com/Automattic/jetmon/internal/veriflier"
)

var version = "dev"
var enforceOutboundTargetSafety = true

const (
	shutdownGracePeriod             = 30 * time.Second
	unsafeTargetsConfirmEnv         = "VERIFLIER_UNSAFE_TARGETS_CONFIRM"
	unsafeTargetsConfirmInternalLab = "internal-lab-only"
)

type veriflierConfig struct {
	AuthToken  string `json:"auth_token"`
	Port       string `json:"port"`
	GRPCPort   string `json:"grpc_port"` // Deprecated alias for Port.
	Hostname   string `json:"hostname"`
	StatsDPath string `json:"statsd_host_path"`
	VantageID  string `json:"vantage_id"`
	Region     string `json:"region"`
	Provider   string `json:"provider"`
	LegacyHTTP bool   `json:"enable_legacy_http"`
}

func main() {
	configPath := envOrDefault("VERIFLIER_CONFIG", "config/veriflier.json")

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	// Override auth token and port from environment if set (Docker entrypoint).
	if v := os.Getenv("VERIFLIER_AUTH_TOKEN"); v != "" {
		cfg.AuthToken = v
	}
	if v := envOrDefault("VERIFLIER_PORT", ""); v != "" {
		cfg.Port = v
	} else if v := os.Getenv("VERIFLIER_GRPC_PORT"); v != "" {
		cfg.Port = v
	}
	if v := os.Getenv("VERIFLIER_VANTAGE_ID"); v != "" {
		cfg.VantageID = v
	}
	if v := os.Getenv("VERIFLIER_REGION"); v != "" {
		cfg.Region = v
	}
	if v := os.Getenv("VERIFLIER_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if strings.TrimSpace(cfg.Hostname) == "" {
		if v := os.Getenv("VERIFLIER_HOSTNAME"); v != "" {
			cfg.Hostname = v
		} else if v := os.Getenv("JETMON_HOSTNAME"); v != "" {
			cfg.Hostname = v
		}
	}
	if strings.TrimSpace(cfg.StatsDPath) == "" {
		cfg.StatsDPath = os.Getenv("STATSD_HOST_PATH")
	}
	cfg.StatsDPath = strings.TrimSpace(cfg.StatsDPath)
	if err := validateStatsDHostPath(cfg.StatsDPath); err != nil {
		log.Fatalf("STATSD_HOST_PATH: %v", err)
	}
	if v := os.Getenv("VERIFLIER_ENABLE_LEGACY_HTTP"); v != "" {
		enabled, err := parseBool(v)
		if err != nil {
			log.Fatalf("VERIFLIER_ENABLE_LEGACY_HTTP: %v", err)
		}
		cfg.LegacyHTTP = enabled
	}
	if err := configureOutboundTargetSafetyFromEnv(); err != nil {
		log.Fatalf("VERIFLIER_ALLOW_UNSAFE_TARGETS: %v", err)
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
	hostname := configuredHostname(cfg.Hostname)
	addr := fmt.Sprintf(":%s", cfg.TransportPort())
	agentID := veriflierAgentID(hostname, cfg.TransportPort())

	// Optional StatsD metrics. STATSD_ADDR is unset in standalone deploys,
	// "statsd:8125" in the docker compose stack. metrics.Init failure logs and
	// continues — the verifier should still run with metrics disabled.
	if statsdAddr, enabled, err := metrics.InitFromEnv(veriflierStatsDMetricHost(cfg, hostname), ""); err != nil {
		log.Printf("metrics: init failed (%v) — running without metrics", err)
	} else if enabled {
		config.Debugf("metrics: sending to %s", statsdAddr)
		if strings.TrimSpace(cfg.StatsDPath) == "" {
			log.Printf("WARN: STATSD_HOST_PATH is unset; StatsD metrics will use Veriflier hostname %q", hostname)
		}
	}
	stopResourceStats := startVeriflierResourceStats(metrics.Global(), 10*time.Second)
	defer stopResourceStats()

	srv := veriflier.NewServerWithOptions(addr, cfg.AuthToken, hostname, version, veriflier.ServerOptions{
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
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("veriflier2: %s received, draining (up to %s)", sig, shutdownGracePeriod)
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("veriflier2: shutdown error: %v", err)
		}
	}()

	log.Printf("veriflier2 %s starting on %s legacy_http=%s", version, addr, enabledLabel(cfg.LegacyHTTP))
	if err := srv.Listen(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
	log.Println("veriflier2: shutdown complete")
}

type resourceStatsEmitter interface {
	EmitMemStats()
}

func startVeriflierResourceStats(m resourceStatsEmitter, interval time.Duration) func() {
	if m == nil || interval <= 0 {
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
	res := checker.Check(ctx, checker.Request{
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

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func loadConfig(path string) (*veriflierConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		// Fall back to environment-only config.
		return &veriflierConfig{
			AuthToken:  os.Getenv("VERIFLIER_AUTH_TOKEN"),
			Port:       envOrDefault("VERIFLIER_PORT", envOrDefault("VERIFLIER_GRPC_PORT", "7803")),
			Hostname:   firstNonEmpty(os.Getenv("VERIFLIER_HOSTNAME"), os.Getenv("JETMON_HOSTNAME")),
			StatsDPath: os.Getenv("STATSD_HOST_PATH"),
		}, nil
	}
	defer f.Close()

	var cfg veriflierConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c veriflierConfig) TransportPort() string {
	if c.Port != "" {
		return c.Port
	}
	return c.GRPCPort
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

func configureOutboundTargetSafetyFromEnv() error {
	enforceOutboundTargetSafety = true
	raw := os.Getenv("VERIFLIER_ALLOW_UNSAFE_TARGETS")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	allowUnsafe, err := parseBool(raw)
	if err != nil {
		return err
	}
	if !allowUnsafe {
		return nil
	}
	if os.Getenv(unsafeTargetsConfirmEnv) != unsafeTargetsConfirmInternalLab {
		return fmt.Errorf("%s must be set to %q before private/internal probe targets are allowed", unsafeTargetsConfirmEnv, unsafeTargetsConfirmInternalLab)
	}
	enforceOutboundTargetSafety = false
	log.Printf("WARN: outbound target safety disabled for internal lab use; do not enable this on production Verifliers")
	return nil
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}

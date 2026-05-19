package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/veriflier"
)

func allowUnsafeOutboundTargetsForTest(t *testing.T) {
	t.Helper()
	old := enforceOutboundTargetSafety
	enforceOutboundTargetSafety = false
	t.Cleanup(func() { enforceOutboundTargetSafety = old })
}

func TestEnvOrDefault(t *testing.T) {
	const key = "VERIFLIER_TEST_ENV_OR_DEFAULT"
	t.Setenv(key, "")
	if got := envOrDefault(key, "fallback"); got != "fallback" {
		t.Fatalf("envOrDefault(empty) = %q, want fallback", got)
	}

	t.Setenv(key, "configured")
	if got := envOrDefault(key, "fallback"); got != "configured" {
		t.Fatalf("envOrDefault(set) = %q, want configured", got)
	}
}

func TestParseBool(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "on", "enabled"} {
		got, err := parseBool(value)
		if err != nil || !got {
			t.Fatalf("parseBool(%q) = %v, %v; want true, nil", value, got, err)
		}
	}
	for _, value := range []string{"0", "false", "FALSE", "no", "off", "disabled"} {
		got, err := parseBool(value)
		if err != nil || got {
			t.Fatalf("parseBool(%q) = %v, %v; want false, nil", value, got, err)
		}
	}
	if _, err := parseBool("sometimes"); err == nil {
		t.Fatal("parseBool accepted invalid value")
	}
}

func TestStringPtr(t *testing.T) {
	if got := stringPtr(""); got != nil {
		t.Fatalf("stringPtr(empty) = %v, want nil", got)
	}
	got := stringPtr("needle")
	if got == nil || *got != "needle" {
		t.Fatalf("stringPtr(non-empty) = %v, want pointer to needle", got)
	}
}

func TestLoadConfigFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "veriflier.json")
	if err := os.WriteFile(path, []byte(`{"auth_token":"secret","port":"7804","hostname":"do-nyc3-1","statsd_host_path":"nyc3.veriflier-1","vantage_id":"us-east","region":"iad","provider":"test","enable_legacy_http":true}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.AuthToken != "secret" || cfg.TransportPort() != "7804" {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.Hostname != "do-nyc3-1" {
		t.Fatalf("Hostname = %q, want do-nyc3-1", cfg.Hostname)
	}
	if cfg.StatsDPath != "nyc3.veriflier-1" {
		t.Fatalf("StatsDPath = %q, want nyc3.veriflier-1", cfg.StatsDPath)
	}
	if cfg.VantageID != "us-east" || cfg.Region != "iad" || cfg.Provider != "test" {
		t.Fatalf("vantage config = %+v", cfg)
	}
	if !cfg.LegacyHTTP {
		t.Fatalf("LegacyHTTP = false, want true")
	}
}

func TestLoadConfigSupportsLegacyGRPCPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "veriflier.json")
	if err := os.WriteFile(path, []byte(`{"auth_token":"secret","grpc_port":"7805"}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.TransportPort() != "7805" {
		t.Fatalf("TransportPort() = %q, want 7805", cfg.TransportPort())
	}
}

func TestLoadConfigFallsBackToEnvironment(t *testing.T) {
	t.Setenv("VERIFLIER_AUTH_TOKEN", "env-secret")
	t.Setenv("VERIFLIER_PORT", "7900")
	t.Setenv("VERIFLIER_HOSTNAME", "do-nyc3-1")
	t.Setenv("STATSD_HOST_PATH", "nyc3.veriflier-1")

	cfg, err := loadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.AuthToken != "env-secret" || cfg.TransportPort() != "7900" {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.Hostname != "do-nyc3-1" {
		t.Fatalf("Hostname = %q, want do-nyc3-1", cfg.Hostname)
	}
	if cfg.StatsDPath != "nyc3.veriflier-1" {
		t.Fatalf("StatsDPath = %q, want nyc3.veriflier-1", cfg.StatsDPath)
	}
}

func TestLoadConfigHostnameEnvironmentPrecedence(t *testing.T) {
	t.Setenv("VERIFLIER_HOSTNAME", "")
	t.Setenv("JETMON_HOSTNAME", "generic-host")

	cfg, err := loadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Hostname != "generic-host" {
		t.Fatalf("Hostname = %q, want generic-host", cfg.Hostname)
	}
}

func TestConfiguredHostnameTrimsConfiguredValue(t *testing.T) {
	if got := configuredHostname(" do-nyc3-1 "); got != "do-nyc3-1" {
		t.Fatalf("configuredHostname = %q, want do-nyc3-1", got)
	}
}

func TestVeriflierStatsDMetricHostFallsBackToHostname(t *testing.T) {
	cfg := &veriflierConfig{}
	if got := veriflierStatsDMetricHost(cfg, "do-nyc3-1"); got != "do-nyc3-1" {
		t.Fatalf("veriflierStatsDMetricHost(fallback) = %q, want do-nyc3-1", got)
	}
}

func TestVeriflierStatsDMetricHostPrefersExplicitPath(t *testing.T) {
	cfg := &veriflierConfig{StatsDPath: " nyc3.veriflier-1 "}
	if got := veriflierStatsDMetricHost(cfg, "do-nyc3-1"); got != "nyc3.veriflier-1" {
		t.Fatalf("veriflierStatsDMetricHost(explicit) = %q, want nyc3.veriflier-1", got)
	}
}

func TestValidateStatsDHostPath(t *testing.T) {
	for _, path := range []string{"", "nyc3.veriflier-1", "dfw1.jetmon_prod_1"} {
		if err := validateStatsDHostPath(path); err != nil {
			t.Fatalf("validateStatsDHostPath(%q) error = %v", path, err)
		}
	}
	for _, path := range []string{".nyc3", "nyc3.", "nyc3..veriflier-1", "nyc3/veriflier-1"} {
		if err := validateStatsDHostPath(path); err == nil {
			t.Fatalf("validateStatsDHostPath(%q) = nil, want error", path)
		}
	}
}

func TestStartVeriflierResourceStats(t *testing.T) {
	emitter := &recordingResourceStatsEmitter{}
	stop := startVeriflierResourceStats(emitter, 10*time.Millisecond)
	defer stop()

	timeout := time.After(time.Second)
	for {
		if emitter.calls.Load() > 0 {
			return
		}
		select {
		case <-timeout:
			t.Fatal("timed out waiting for Veriflier resource metric")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

type recordingResourceStatsEmitter struct {
	calls atomic.Int64
}

func (r *recordingResourceStatsEmitter) EmitMemStats() {
	r.calls.Add(1)
}

func TestLoadConfigFallsBackToLegacyPortEnvironment(t *testing.T) {
	t.Setenv("VERIFLIER_AUTH_TOKEN", "env-secret")
	t.Setenv("VERIFLIER_GRPC_PORT", "7901")

	cfg, err := loadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.TransportPort() != "7901" {
		t.Fatalf("TransportPort() = %q, want 7901", cfg.TransportPort())
	}
}

func TestLoadConfigRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "veriflier.json")
	if err := os.WriteFile(path, []byte(`{"auth_token":`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := loadConfig(path); err == nil {
		t.Fatal("loadConfig accepted malformed JSON")
	}
}

func TestVeriflierAgentIDIncludesPort(t *testing.T) {
	if got := veriflierAgentID("host-a", "7803"); got != "host-a:7803" {
		t.Fatalf("veriflierAgentID() = %q, want host-a:7803", got)
	}
	if got := veriflierAgentID("", ""); got != "unknown" {
		t.Fatalf("veriflierAgentID(empty) = %q, want unknown", got)
	}
}

func TestPerformCheckSuccess(t *testing.T) {
	allowUnsafeOutboundTargetsForTest(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Test"); got != "present" {
			t.Fatalf("X-Test header = %q, want present", got)
		}
		_, _ = w.Write([]byte("needle"))
	}))
	defer srv.Close()

	res := performCheck(veriflier.CheckRequest{
		BlogID:         42,
		URL:            srv.URL,
		TimeoutSeconds: 2,
		Keyword:        "needle",
		CustomHeaders:  map[string]string{"X-Test": "present"},
		RedirectPolicy: string(checker.RedirectFollow),
	})
	if !res.Success {
		t.Fatalf("performCheck success = false; result=%+v", res)
	}
	if res.BlogID != 42 || res.HTTPCode != http.StatusOK {
		t.Fatalf("performCheck result = %+v", res)
	}
}

func TestPerformCheckContextOutcomeAndTimings(t *testing.T) {
	allowUnsafeOutboundTargetsForTest(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	res := performCheckContext(context.Background(), veriflier.CheckRequest{
		BlogID:         45,
		URL:            srv.URL,
		TimeoutSeconds: 2,
		RedirectPolicy: string(checker.RedirectFollow),
	})
	if res.Outcome != veriflier.OutcomeUp {
		t.Fatalf("outcome = %q, want up; result=%+v", res.Outcome, res)
	}
	if !res.Success || res.HTTPCode != http.StatusOK {
		t.Fatalf("check result = %+v", res.CheckResult)
	}
	if res.RTTMs < 0 || res.TimingsMS.TTFB < 0 {
		t.Fatalf("negative timings = %+v", res.TimingsMS)
	}
}

func TestPerformCheckKeywordFailure(t *testing.T) {
	allowUnsafeOutboundTargetsForTest(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("different"))
	}))
	defer srv.Close()

	res := performCheck(veriflier.CheckRequest{
		BlogID:         43,
		URL:            srv.URL,
		TimeoutSeconds: 2,
		Keyword:        "needle",
		RedirectPolicy: string(checker.RedirectFollow),
	})
	if res.Success {
		t.Fatalf("performCheck success = true; result=%+v", res)
	}
	if res.ErrorCode != int32(checker.ErrorKeyword) {
		t.Fatalf("error code = %d, want %d", res.ErrorCode, checker.ErrorKeyword)
	}
}

func TestPerformCheckTruncatedBodyFailure(t *testing.T) {
	allowUnsafeOutboundTargetsForTest(t)

	srv := truncatedBodyServer(t, "needle but incomplete")
	defer srv.Close()

	res := performCheck(veriflier.CheckRequest{
		BlogID:         44,
		URL:            srv.URL,
		TimeoutSeconds: 2,
		Keyword:        "needle",
		RedirectPolicy: string(checker.RedirectFollow),
	})
	if res.Success {
		t.Fatalf("performCheck success = true for truncated body; result=%+v", res)
	}
	if res.HTTPCode != http.StatusOK {
		t.Fatalf("http code = %d, want %d", res.HTTPCode, http.StatusOK)
	}
	if res.ErrorCode != int32(checker.ErrorBodyRead) {
		t.Fatalf("error code = %d, want %d", res.ErrorCode, checker.ErrorBodyRead)
	}
}

func TestPerformCheckProbeSafetyOutcomeUnknown(t *testing.T) {
	res := performCheckContext(context.Background(), veriflier.CheckRequest{
		BlogID:         46,
		URL:            "http://127.0.0.1/admin",
		TimeoutSeconds: 2,
		RedirectPolicy: string(checker.RedirectFollow),
	})
	if res.Outcome != veriflier.OutcomeUnknown {
		t.Fatalf("outcome = %q, want unknown; result=%+v", res.Outcome, res)
	}
	if res.ErrorCode != int32(checker.ErrorProbeSafety) {
		t.Fatalf("error code = %d, want %d", res.ErrorCode, checker.ErrorProbeSafety)
	}
}

func truncatedBodyServer(t *testing.T, body string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer does not support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
}

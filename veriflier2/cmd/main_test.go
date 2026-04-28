package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/veriflier"
)

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
	if err := os.WriteFile(path, []byte(`{"auth_token":"secret","port":"7804"}`), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.AuthToken != "secret" || cfg.TransportPort() != "7804" {
		t.Fatalf("config = %+v", cfg)
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

	cfg, err := loadConfig(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.AuthToken != "env-secret" || cfg.TransportPort() != "7900" {
		t.Fatalf("config = %+v", cfg)
	}
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

func TestPerformCheckSuccess(t *testing.T) {
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

func TestPerformCheckKeywordFailure(t *testing.T) {
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

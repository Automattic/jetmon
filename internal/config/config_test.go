package config

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	base := func() *Config {
		return &Config{
			AuthToken:         "token",
			NumWorkers:        10,
			BucketTotal:       100,
			BucketTarget:      50,
			NetCommsTimeout:   10,
			LogFormat:         "text",
			APIRateLimitRPS:   20,
			APIRateLimitBurst: 40,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name:   "valid config",
			mutate: func(_ *Config) {},
		},
		{
			name:    "missing auth token",
			mutate:  func(c *Config) { c.AuthToken = "" },
			wantErr: true,
		},
		{
			name:    "num workers zero",
			mutate:  func(c *Config) { c.NumWorkers = 0 },
			wantErr: true,
		},
		{
			name:    "num workers negative",
			mutate:  func(c *Config) { c.NumWorkers = -1 },
			wantErr: true,
		},
		{
			name:    "bucket total zero",
			mutate:  func(c *Config) { c.BucketTotal = 0 },
			wantErr: true,
		},
		{
			name:    "bucket target zero",
			mutate:  func(c *Config) { c.BucketTarget = 0 },
			wantErr: true,
		},
		{
			name:    "bucket target exceeds bucket total",
			mutate:  func(c *Config) { c.BucketTarget = 101 },
			wantErr: true,
		},
		{
			name:    "bucket target equals bucket total is valid",
			mutate:  func(c *Config) { c.BucketTarget = 100 },
			wantErr: false,
		},
		{
			name:    "net comms timeout zero",
			mutate:  func(c *Config) { c.NetCommsTimeout = 0 },
			wantErr: true,
		},
		{
			name:    "net comms timeout negative",
			mutate:  func(c *Config) { c.NetCommsTimeout = -1 },
			wantErr: true,
		},
		{
			name:    "invalid log format",
			mutate:  func(c *Config) { c.LogFormat = "xml" },
			wantErr: true,
		},
		{
			name:   "json log format is valid",
			mutate: func(c *Config) { c.LogFormat = "json" },
		},
		{
			name:    "api rate limit rps must be positive",
			mutate:  func(c *Config) { c.APIRateLimitRPS = 0 },
			wantErr: true,
		},
		{
			name:    "api rate limit burst must be positive",
			mutate:  func(c *Config) { c.APIRateLimitBurst = 0 },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(cfg)
			err := validate(cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func saveConfigState(t *testing.T) {
	t.Helper()
	origPath := path
	origCurrent := current
	t.Cleanup(func() {
		mu.Lock()
		path = origPath
		current = origCurrent
		mu.Unlock()
	})
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "config-*.json")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	t.Cleanup(func() { os.Remove(f.Name()) })
	if _, err := fmt.Fprint(f, content); err != nil {
		t.Fatalf("write config: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestLoadAndGet(t *testing.T) {
	saveConfigState(t)

	p := writeConfigFile(t, `{
		"AUTH_TOKEN": "loaded-token",
		"NUM_WORKERS": 7,
		"BUCKET_TOTAL": 100,
		"BUCKET_TARGET": 50,
		"NET_COMMS_TIMEOUT": 10,
		"LOG_FORMAT": "json"
	}`)

	if err := Load(p); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	cfg := Get()
	if cfg == nil {
		t.Fatal("Get() = nil after Load")
	}
	if cfg.AuthToken != "loaded-token" {
		t.Fatalf("AuthToken = %q, want loaded-token", cfg.AuthToken)
	}
	if cfg.NumWorkers != 7 {
		t.Fatalf("NumWorkers = %d, want 7", cfg.NumWorkers)
	}
	if cfg.LogFormat != "json" {
		t.Fatalf("LogFormat = %q, want json", cfg.LogFormat)
	}
}

func TestLoadInvalidConfigReturnsError(t *testing.T) {
	saveConfigState(t)

	p := writeConfigFile(t, `{"AUTH_TOKEN": "", "NUM_WORKERS": 5, "BUCKET_TOTAL": 100, "BUCKET_TARGET": 50, "NET_COMMS_TIMEOUT": 10, "LOG_FORMAT": "text"}`)

	if err := Load(p); err == nil {
		t.Fatal("Load() expected error for invalid config (empty AUTH_TOKEN)")
	}
}

func TestLoadNonExistentFileReturnsError(t *testing.T) {
	saveConfigState(t)
	if err := Load("/does/not/exist/config.json"); err == nil {
		t.Fatal("Load() expected error for missing file")
	}
}

func TestReload(t *testing.T) {
	saveConfigState(t)

	p := writeConfigFile(t, `{
		"AUTH_TOKEN": "first",
		"NUM_WORKERS": 5,
		"BUCKET_TOTAL": 100,
		"BUCKET_TARGET": 50,
		"NET_COMMS_TIMEOUT": 10,
		"LOG_FORMAT": "text"
	}`)

	if err := Load(p); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if Get().AuthToken != "first" {
		t.Fatalf("AuthToken before reload = %q, want first", Get().AuthToken)
	}

	if err := os.WriteFile(p, []byte(`{
		"AUTH_TOKEN": "second",
		"NUM_WORKERS": 10,
		"BUCKET_TOTAL": 100,
		"BUCKET_TARGET": 50,
		"NET_COMMS_TIMEOUT": 10,
		"LOG_FORMAT": "text"
	}`), 0600); err != nil {
		t.Fatalf("overwrite config: %v", err)
	}

	if err := Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	cfg := Get()
	if cfg.AuthToken != "second" {
		t.Fatalf("AuthToken after reload = %q, want second", cfg.AuthToken)
	}
	if cfg.NumWorkers != 10 {
		t.Fatalf("NumWorkers after reload = %d, want 10", cfg.NumWorkers)
	}
}

func TestDebugrLogsWhenEnabled(t *testing.T) {
	origCurrent := current
	t.Cleanup(func() {
		mu.Lock()
		current = origCurrent
		mu.Unlock()
	})

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	mu.Lock()
	current = &Config{Debug: true}
	mu.Unlock()

	Debugf("test message %d", 42)

	if !strings.Contains(buf.String(), "[DEBUG]") {
		t.Fatalf("Debugf did not log [DEBUG] when Debug=true, got: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "test message 42") {
		t.Fatalf("Debugf missing message body, got: %q", buf.String())
	}
}

func TestDebugfSilentWhenDisabled(t *testing.T) {
	origCurrent := current
	t.Cleanup(func() {
		mu.Lock()
		current = origCurrent
		mu.Unlock()
	})

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	mu.Lock()
	current = &Config{Debug: false}
	mu.Unlock()

	Debugf("should not appear")

	if buf.Len() != 0 {
		t.Fatalf("Debugf logged when Debug=false: %q", buf.String())
	}
}

func TestLoadDBAndGetDB(t *testing.T) {
	mu.Lock()
	origDB := dbConf
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		dbConf = origDB
		mu.Unlock()
	})

	t.Setenv("DB_HOST", "testhost")
	t.Setenv("DB_PORT", "3307")
	t.Setenv("DB_USER", "testuser")
	t.Setenv("DB_PASSWORD", "testpass")
	t.Setenv("DB_NAME", "testdb")

	cfg := LoadDB()
	if cfg == nil {
		t.Fatal("LoadDB() = nil")
	}
	if cfg.Host != "testhost" {
		t.Fatalf("Host = %q, want testhost", cfg.Host)
	}
	if cfg.Port != "3307" {
		t.Fatalf("Port = %q, want 3307", cfg.Port)
	}

	got := GetDB()
	if got == nil {
		t.Fatal("GetDB() = nil after LoadDB")
	}
	if got.User != "testuser" {
		t.Fatalf("GetDB().User = %q, want testuser", got.User)
	}
}

func TestEnvOrDefaultConfig(t *testing.T) {
	const key = "JETMON_CONFIG_TEST_VAR"
	t.Setenv(key, "")

	if got := envOrDefault(key, "default"); got != "default" {
		t.Fatalf("envOrDefault() = %q, want default", got)
	}

	t.Setenv(key, "override")
	if got := envOrDefault(key, "default"); got != "override" {
		t.Fatalf("envOrDefault() = %q, want override", got)
	}
}

func TestDefaults(t *testing.T) {
	cfg := defaults()
	if cfg.NumWorkers <= 0 {
		t.Fatalf("defaults().NumWorkers = %d, want > 0", cfg.NumWorkers)
	}
	if cfg.BucketTotal <= 0 {
		t.Fatalf("defaults().BucketTotal = %d, want > 0", cfg.BucketTotal)
	}
	if cfg.BucketTarget <= 0 || cfg.BucketTarget > cfg.BucketTotal {
		t.Fatalf("defaults().BucketTarget = %d out of range [1, %d]", cfg.BucketTarget, cfg.BucketTotal)
	}
	if cfg.NetCommsTimeout <= 0 {
		t.Fatalf("defaults().NetCommsTimeout = %d, want > 0", cfg.NetCommsTimeout)
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		t.Fatalf("defaults().LogFormat = %q, want text or json", cfg.LogFormat)
	}
}

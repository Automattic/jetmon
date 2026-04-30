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
			AuthToken:           "token",
			NumWorkers:          10,
			BucketTotal:         100,
			BucketTarget:        50,
			NetCommsTimeout:     10,
			BodyReadMaxBytes:    262144,
			BodyReadMaxMS:       250,
			KeywordReadMaxBytes: 1048576,
			KeywordReadMaxMS:    0,
			LogFormat:           "text",
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
			name: "pinned bucket range is valid",
			mutate: func(c *Config) {
				min, max := 10, 19
				c.PinnedBucketMin = &min
				c.PinnedBucketMax = &max
			},
		},
		{
			name: "legacy bucket range alias is valid",
			mutate: func(c *Config) {
				min, max := 10, 19
				c.BucketNoMin = &min
				c.BucketNoMax = &max
			},
		},
		{
			name: "pinned bucket range requires min and max",
			mutate: func(c *Config) {
				min := 10
				c.PinnedBucketMin = &min
			},
			wantErr: true,
		},
		{
			name: "legacy bucket range requires min and max",
			mutate: func(c *Config) {
				max := 19
				c.BucketNoMax = &max
			},
			wantErr: true,
		},
		{
			name: "pinned bucket range rejects max before min",
			mutate: func(c *Config) {
				min, max := 20, 19
				c.PinnedBucketMin = &min
				c.PinnedBucketMax = &max
			},
			wantErr: true,
		},
		{
			name: "pinned bucket range rejects max outside total",
			mutate: func(c *Config) {
				min, max := 90, 100
				c.PinnedBucketMin = &min
				c.PinnedBucketMax = &max
			},
			wantErr: true,
		},
		{
			name: "pinned and legacy ranges must agree",
			mutate: func(c *Config) {
				pMin, pMax := 10, 19
				lMin, lMax := 20, 29
				c.PinnedBucketMin = &pMin
				c.PinnedBucketMax = &pMax
				c.BucketNoMin = &lMin
				c.BucketNoMax = &lMax
			},
			wantErr: true,
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
			name:    "body read max bytes zero",
			mutate:  func(c *Config) { c.BodyReadMaxBytes = 0 },
			wantErr: true,
		},
		{
			name:    "body read max ms zero",
			mutate:  func(c *Config) { c.BodyReadMaxMS = 0 },
			wantErr: true,
		},
		{
			name:    "keyword read max bytes zero",
			mutate:  func(c *Config) { c.KeywordReadMaxBytes = 0 },
			wantErr: true,
		},
		{
			name:    "keyword read max ms negative",
			mutate:  func(c *Config) { c.KeywordReadMaxMS = -1 },
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
			name:   "empty dashboard bind address falls back to localhost",
			mutate: func(c *Config) { c.DashboardBindAddr = "" },
		},
		{
			name:   "remote dashboard bind address is explicit and valid",
			mutate: func(c *Config) { c.DashboardBindAddr = "0.0.0.0" },
		},
		{
			name:   "stub email transport is valid",
			mutate: func(c *Config) { c.EmailTransport = "stub" },
		},
		{
			name:   "empty email transport uses default stub behavior",
			mutate: func(c *Config) { c.EmailTransport = "" },
		},
		{
			name:    "invalid email transport",
			mutate:  func(c *Config) { c.EmailTransport = "sendmail" },
			wantErr: true,
		},
		{
			name: "smtp email transport requires host",
			mutate: func(c *Config) {
				c.EmailTransport = "smtp"
				c.SMTPPort = 1025
			},
			wantErr: true,
		},
		{
			name: "smtp email transport requires port",
			mutate: func(c *Config) {
				c.EmailTransport = "smtp"
				c.SMTPHost = "mailhog"
			},
			wantErr: true,
		},
		{
			name: "smtp email transport with host and port is valid",
			mutate: func(c *Config) {
				c.EmailTransport = "smtp"
				c.SMTPHost = "mailhog"
				c.SMTPPort = 1025
			},
		},
		{
			name: "wpcom email transport requires endpoint",
			mutate: func(c *Config) {
				c.EmailTransport = "wpcom"
			},
			wantErr: true,
		},
		{
			name: "wpcom email transport with endpoint is valid",
			mutate: func(c *Config) {
				c.EmailTransport = "wpcom"
				c.WPCOMEmailEndpoint = "https://example.test/email"
			},
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

func TestPinnedBucketRange(t *testing.T) {
	pMin, pMax := 10, 19
	lMin, lMax := 20, 29
	cfg := &Config{
		PinnedBucketMin: &pMin,
		PinnedBucketMax: &pMax,
		BucketNoMin:     &lMin,
		BucketNoMax:     &lMax,
	}
	min, max, ok := cfg.PinnedBucketRange()
	if !ok || min != 10 || max != 19 {
		t.Fatalf("PinnedBucketRange explicit = %d-%d ok=%v, want 10-19 true", min, max, ok)
	}

	cfg.PinnedBucketMin = nil
	cfg.PinnedBucketMax = nil
	min, max, ok = cfg.PinnedBucketRange()
	if !ok || min != 20 || max != 29 {
		t.Fatalf("PinnedBucketRange legacy = %d-%d ok=%v, want 20-29 true", min, max, ok)
	}
}

func TestValidateDefaultsDashboardBindAddr(t *testing.T) {
	cfg := &Config{
		AuthToken:       "token",
		NumWorkers:      10,
		BucketTotal:     100,
		BucketTarget:    50,
		NetCommsTimeout: 10,
		LogFormat:       "text",
	}
	if err := validate(cfg); err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if cfg.DashboardBindAddr != "127.0.0.1" {
		t.Fatalf("DashboardBindAddr = %q, want 127.0.0.1", cfg.DashboardBindAddr)
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
		"LOG_FORMAT": "json",
		"DELIVERY_OWNER_HOST": "jetmon-api-1"
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
	if cfg.DeliveryOwnerHost != "jetmon-api-1" {
		t.Fatalf("DeliveryOwnerHost = %q, want jetmon-api-1", cfg.DeliveryOwnerHost)
	}
	if cfg.BodyReadMaxBytes != 262144 {
		t.Fatalf("BodyReadMaxBytes = %d, want 262144", cfg.BodyReadMaxBytes)
	}
	if cfg.BodyReadMaxMS != 250 {
		t.Fatalf("BodyReadMaxMS = %d, want 250", cfg.BodyReadMaxMS)
	}
	if cfg.KeywordReadMaxBytes != 1048576 {
		t.Fatalf("KeywordReadMaxBytes = %d, want 1048576", cfg.KeywordReadMaxBytes)
	}
	if cfg.KeywordReadMaxMS != 0 {
		t.Fatalf("KeywordReadMaxMS = %d, want 0", cfg.KeywordReadMaxMS)
	}
	if !cfg.LegacyStatusProjectionEnable {
		t.Fatal("LegacyStatusProjectionEnable default should be true")
	}
}

func TestSampleConfigLoads(t *testing.T) {
	saveConfigState(t)

	if err := Load("../../config/config-sample.json"); err != nil {
		t.Fatalf("config-sample.json should load: %v", err)
	}
	cfg := Get()
	if cfg == nil {
		t.Fatal("Get() = nil after loading sample config")
	}
	if cfg.EmailTransport != "stub" {
		t.Fatalf("EmailTransport = %q, want stub", cfg.EmailTransport)
	}
}

func TestLegacyStatusProjectionConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "new key disables projection",
			body: `"LEGACY_STATUS_PROJECTION_ENABLE": false`,
			want: false,
		},
		{
			name: "old key remains alias when new key absent",
			body: `"DB_UPDATES_ENABLE": false`,
			want: false,
		},
		{
			name: "new key wins over old key",
			body: `"DB_UPDATES_ENABLE": false, "LEGACY_STATUS_PROJECTION_ENABLE": true`,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saveConfigState(t)
			p := writeConfigFile(t, `{
				"AUTH_TOKEN": "token",
				"NUM_WORKERS": 7,
				"BUCKET_TOTAL": 100,
				"BUCKET_TARGET": 50,
				"NET_COMMS_TIMEOUT": 10,
				"LOG_FORMAT": "text",
				`+tt.body+`
			}`)

			if err := Load(p); err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got := LegacyStatusProjectionEnabled(); got != tt.want {
				t.Fatalf("LegacyStatusProjectionEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	if got := displayName(VerifierConfig{Name: "us-west"}, 2); got != "us-west" {
		t.Fatalf("displayName(named) = %q, want us-west", got)
	}
	if got := displayName(VerifierConfig{}, 2); got != "verifier #2" {
		t.Fatalf("displayName(unnamed) = %q, want verifier #2", got)
	}
}

func TestVerifierTransportPort(t *testing.T) {
	if got := (VerifierConfig{Port: "7803"}).TransportPort(); got != "7803" {
		t.Fatalf("TransportPort(port) = %q, want 7803", got)
	}
	if got := (VerifierConfig{GRPCPort: "7804"}).TransportPort(); got != "7804" {
		t.Fatalf("TransportPort(grpc_port alias) = %q, want 7804", got)
	}
	if got := (VerifierConfig{Port: "7803", GRPCPort: "7804"}).TransportPort(); got != "7803" {
		t.Fatalf("TransportPort(prefer port) = %q, want 7803", got)
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

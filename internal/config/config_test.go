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
			DatasetSize:         100,
			BucketTotal:         100,
			BucketTarget:        50,
			NetCommsTimeout:     10,
			BodyReadMaxBytes:    1048576,
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
			name:   "num workers zero uses default floor",
			mutate: func(c *Config) { c.NumWorkers = 0 },
		},
		{
			name:    "num workers negative",
			mutate:  func(c *Config) { c.NumWorkers = -1 },
			wantErr: true,
		},
		{
			name:   "dataset size zero uses default",
			mutate: func(c *Config) { c.DatasetSize = 0 },
		},
		{
			name:    "dataset size negative",
			mutate:  func(c *Config) { c.DatasetSize = -1 },
			wantErr: true,
		},
		{
			name:    "bucket total zero",
			mutate:  func(c *Config) { c.BucketTotal = 0 },
			wantErr: true,
		},
		{
			name:   "bucket target zero uses bucket total",
			mutate: func(c *Config) { c.BucketTarget = 0 },
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
			name: "statsd host path accepts v1-compatible path",
			mutate: func(c *Config) {
				c.StatsDHostPath = "dfw1.jetmon-prod-1"
			},
		},
		{
			name: "statsd host path trims whitespace",
			mutate: func(c *Config) {
				c.StatsDHostPath = " dfw1.jetmon-prod-1 "
			},
		},
		{
			name: "statsd host path rejects spaces",
			mutate: func(c *Config) {
				c.StatsDHostPath = "dfw1 jetmon-prod-1"
			},
			wantErr: true,
		},
		{
			name: "statsd host path rejects empty segments",
			mutate: func(c *Config) {
				c.StatsDHostPath = "dfw1..jetmon-prod-1"
			},
			wantErr: true,
		},
		{
			name: "check dns resolver accepts ip with port",
			mutate: func(c *Config) {
				c.CheckDNSResolvers = []string{"10.0.0.176:5353", "[2001:db8::1]:53"}
			},
		},
		{
			name: "check dns resolver rejects hostnames",
			mutate: func(c *Config) {
				c.CheckDNSResolvers = []string{"resolver.internal:53"}
			},
			wantErr: true,
		},
		{
			name: "check dns resolver rejects bad port",
			mutate: func(c *Config) {
				c.CheckDNSResolvers = []string{"10.0.0.176:0"}
			},
			wantErr: true,
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
			name:   "body read max bytes zero uses default",
			mutate: func(c *Config) { c.BodyReadMaxBytes = 0 },
		},
		{
			name:    "body read max ms negative",
			mutate:  func(c *Config) { c.BodyReadMaxMS = -1 },
			wantErr: true,
		},
		{
			name:   "keyword read max bytes zero uses default",
			mutate: func(c *Config) { c.KeywordReadMaxBytes = 0 },
		},
		{
			name:    "keyword read max ms negative",
			mutate:  func(c *Config) { c.KeywordReadMaxMS = -1 },
			wantErr: true,
		},
		{
			name:    "min time between rounds negative",
			mutate:  func(c *Config) { c.MinTimeBetweenRoundsSec = -1 },
			wantErr: true,
		},
		{
			name:   "empty veriflier discovery mode defaults to static",
			mutate: func(c *Config) { c.VeriflierDiscoveryMode = "" },
		},
		{
			name:   "shadow veriflier discovery mode is valid",
			mutate: func(c *Config) { c.VeriflierDiscoveryMode = "shadow" },
		},
		{
			name:   "active veriflier discovery mode is valid",
			mutate: func(c *Config) { c.VeriflierDiscoveryMode = "ACTIVE" },
		},
		{
			name:    "invalid veriflier discovery mode",
			mutate:  func(c *Config) { c.VeriflierDiscoveryMode = "auto" },
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
		AuthToken:           "token",
		NumWorkers:          10,
		DatasetSize:         100,
		BucketTotal:         100,
		BucketTarget:        50,
		NetCommsTimeout:     10,
		BodyReadMaxBytes:    1048576,
		BodyReadMaxMS:       250,
		KeywordReadMaxBytes: 1048576,
		KeywordReadMaxMS:    0,
		LogFormat:           "text",
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
		"HOSTNAME": "dfw1.jetmon-prod-1",
		"STATSD_HOST_PATH": "dfw1.jetmon-prod-1",
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
	if cfg.Hostname != "dfw1.jetmon-prod-1" {
		t.Fatalf("Hostname = %q, want dfw1.jetmon-prod-1", cfg.Hostname)
	}
	if cfg.StatsDHostPath != "dfw1.jetmon-prod-1" {
		t.Fatalf("StatsDHostPath = %q, want dfw1.jetmon-prod-1", cfg.StatsDHostPath)
	}
	if got := cfg.StatsDMetricHost("container-id"); got != "dfw1.jetmon-prod-1" {
		t.Fatalf("StatsDMetricHost(explicit) = %q, want dfw1.jetmon-prod-1", got)
	}
	if cfg.LogFormat != "json" {
		t.Fatalf("LogFormat = %q, want json", cfg.LogFormat)
	}
	if cfg.DeliveryOwnerHost != "jetmon-api-1" {
		t.Fatalf("DeliveryOwnerHost = %q, want jetmon-api-1", cfg.DeliveryOwnerHost)
	}
	if cfg.VeriflierDiscoveryMode != VeriflierDiscoveryModeStatic {
		t.Fatalf("VeriflierDiscoveryMode = %q, want static", cfg.VeriflierDiscoveryMode)
	}
	if cfg.BodyReadMaxBytes != 1048576 {
		t.Fatalf("BodyReadMaxBytes = %d, want 1048576", cfg.BodyReadMaxBytes)
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
	if !cfg.WPCOMNotifyEnable {
		t.Fatal("WPCOMNotifyEnable default should be true")
	}
	if cfg.WPCOMNotifyMode != WPCOMNotifyModeLegacy {
		t.Fatalf("WPCOMNotifyMode = %q, want legacy", cfg.WPCOMNotifyMode)
	}
}

func TestStatsDMetricHostFallsBackToResolvedHostname(t *testing.T) {
	cfg := &Config{}
	if got := cfg.StatsDMetricHost("jetmon-prod-1.dfw1.example.com"); got != "jetmon-prod-1.dfw1.example.com" {
		t.Fatalf("StatsDMetricHost(fallback) = %q", got)
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

func TestLoadWarnsForDeprecatedNoopAndUnknownKeys(t *testing.T) {
	saveConfigState(t)

	p := writeConfigFile(t, `{
		"AUTH_TOKEN": "token",
		"NUM_WORKERS": 7,
		"NUM_TO_PROCESS": 40,
		"WORKER_MAX_CHECKS": 10000,
		"TIMEOUT_FOR_REQUESTS_SEC": 60,
		"BUCKET_TOTAL": 100,
		"BUCKET_TARGET": 50,
		"BUCKET_NO_MIN": 0,
		"BUCKET_NO_MAX": 49,
		"BATCH_SIZE": 32,
		"VERIFLIER_BATCH_SIZE": 200,
		"SQL_UPDATE_BATCH": 1,
		"TIME_BETWEEN_CHECKS_SEC": 30,
		"TIME_BETWEEN_NOTICES_MIN": 59,
		"NET_COMMS_TIMEOUT": 10,
		"LOG_FORMAT": "text",
		"UNEXPECTED_V1_KEY": true,
		"VERIFIERS": [
			{
				"name": "legacy verifier",
				"host": "veriflier",
				"grpc_port": "7803",
				"auth_token": "token"
			}
		]
	}`)

	if err := Load(p); err != nil {
		t.Fatalf("Load() should warn but not fail: %v", err)
	}
	warnings := warningsByKey(Get().Warnings)
	for _, key := range []string{
		"NUM_TO_PROCESS",
		"WORKER_MAX_CHECKS",
		"TIMEOUT_FOR_REQUESTS_SEC",
		"BUCKET_NO_MIN",
		"BUCKET_NO_MAX",
		"BATCH_SIZE",
		"VERIFLIER_BATCH_SIZE",
		"SQL_UPDATE_BATCH",
		"TIME_BETWEEN_CHECKS_SEC",
		"TIME_BETWEEN_NOTICES_MIN",
		"UNEXPECTED_V1_KEY",
		"VERIFIERS[0].grpc_port",
	} {
		if warnings[key] == "" {
			t.Fatalf("missing warning for %s; got %#v", key, warnings)
		}
	}
}

func TestLoadWarnsWhenStatsDHostPathLooksLikeRawHostname(t *testing.T) {
	saveConfigState(t)

	p := writeConfigFile(t, `{
		"AUTH_TOKEN": "token",
		"NUM_WORKERS": 7,
		"STATSD_HOST_PATH": "jetmon-prod-1.dfw1.example.com",
		"BUCKET_TOTAL": 100,
		"BUCKET_TARGET": 50,
		"NET_COMMS_TIMEOUT": 10,
		"LOG_FORMAT": "text"
	}`)

	if err := Load(p); err != nil {
		t.Fatalf("Load() should warn but not fail: %v", err)
	}
	warnings := warningsByKey(Get().Warnings)
	if warnings["STATSD_HOST_PATH"] == "" {
		t.Fatalf("missing STATSD_HOST_PATH warning; got %#v", warnings)
	}
}

func warningsByKey(warnings []ConfigWarning) map[string]string {
	out := make(map[string]string, len(warnings))
	for _, warning := range warnings {
		out[warning.Key] = warning.Message
	}
	return out
}

func TestWPCOMNotifyConfig(t *testing.T) {
	saveConfigState(t)
	p := writeConfigFile(t, `{
		"AUTH_TOKEN": "token",
		"NUM_WORKERS": 7,
		"BUCKET_TOTAL": 100,
		"BUCKET_TARGET": 50,
		"NET_COMMS_TIMEOUT": 10,
		"LOG_FORMAT": "text",
		"WPCOM_NOTIFY_ENABLE": false
	}`)

	if err := Load(p); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if WPCOMNotifyEnabled() {
		t.Fatal("WPCOMNotifyEnabled() = true, want false")
	}
}

func TestWPCOMNotifyModeConfig(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "default is legacy", body: "", want: WPCOMNotifyModeLegacy},
		{name: "legacy accepted", body: `"WPCOM_NOTIFY_MODE": "legacy"`, want: WPCOMNotifyModeLegacy},
		{name: "modern accepted", body: `"WPCOM_NOTIFY_MODE": "modern"`, want: WPCOMNotifyModeModern},
		{name: "modern normalized", body: `"WPCOM_NOTIFY_MODE": " Modern "`, want: WPCOMNotifyModeModern},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saveConfigState(t)
			extra := tt.body
			if extra != "" {
				extra = "," + extra
			}
			p := writeConfigFile(t, `{
				"AUTH_TOKEN": "token",
				"NUM_WORKERS": 7,
				"BUCKET_TOTAL": 100,
				"BUCKET_TARGET": 50,
				"NET_COMMS_TIMEOUT": 10,
				"LOG_FORMAT": "text"
				`+extra+`
			}`)

			if err := Load(p); err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if got := Get().WPCOMNotifyMode; got != tt.want {
				t.Fatalf("WPCOMNotifyMode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWPCOMNotifyModeRejectsInvalidValue(t *testing.T) {
	saveConfigState(t)
	p := writeConfigFile(t, `{
		"AUTH_TOKEN": "token",
		"NUM_WORKERS": 7,
		"BUCKET_TOTAL": 100,
		"BUCKET_TARGET": 50,
		"NET_COMMS_TIMEOUT": 10,
		"LOG_FORMAT": "text",
		"WPCOM_NOTIFY_MODE": "both"
	}`)

	if err := Load(p); err == nil {
		t.Fatal("Load() expected WPCOM_NOTIFY_MODE validation error")
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
	t.Setenv("DB_SERVER_MAP_PATH", "/jetmon/config-source/db-servers.php")
	t.Setenv("DB_SERVER_MAP_DATASET", "misc")
	t.Setenv("DB_SERVER_MAP_DATACENTER", "dfw")
	t.Setenv("DB_SERVER_MAP_ADDRESS", "internal")

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
	if cfg.ServerMapPath != "/jetmon/config-source/db-servers.php" {
		t.Fatalf("ServerMapPath = %q", cfg.ServerMapPath)
	}
	if cfg.ServerMapDatacenter != "dfw" {
		t.Fatalf("ServerMapDatacenter = %q, want dfw", cfg.ServerMapDatacenter)
	}
	if cfg.ServerMapAddress != "internal" {
		t.Fatalf("ServerMapAddress = %q, want internal", cfg.ServerMapAddress)
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
	if got := cfg.VeriflierDiscoveryModeOrDefault(); got != VeriflierDiscoveryModeStatic {
		t.Fatalf("defaults().VeriflierDiscoveryMode = %q, want static", got)
	}
}

package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
)

// VerifierConfig holds connection details for a single Veriflier instance.
type VerifierConfig struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      string `json:"port"`
	GRPCPort  string `json:"grpc_port"` // Deprecated alias for Port.
	AuthToken string `json:"auth_token"`
}

// TransportPort returns the canonical JSON-over-HTTP Veriflier port,
// accepting grpc_port as a deprecated config alias.
func (v VerifierConfig) TransportPort() string {
	if v.Port != "" {
		return v.Port
	}
	return v.GRPCPort
}

// Config holds all runtime configuration for Jetmon 2.
type Config struct {
	Debug bool `json:"DEBUG"`

	NumWorkers     int `json:"NUM_WORKERS"`
	NumToProcess   int `json:"NUM_TO_PROCESS"`
	DatasetSize    int `json:"DATASET_SIZE"`
	WorkerMaxMemMB int `json:"WORKER_MAX_MEM_MB"`

	// LegacyStatusProjectionEnable controls compatibility writes to the
	// v1 status projection on jetpack_monitor_sites (site_status +
	// last_status_change). Jetmon v2 event/check/delivery tables remain
	// authoritative and are written independently of this switch.
	LegacyStatusProjectionEnable bool `json:"LEGACY_STATUS_PROJECTION_ENABLE"`

	// DBUpdatesEnable is the deprecated name for LegacyStatusProjectionEnable.
	// It remains as a config alias so older configs keep their behavior until
	// they can be rewritten.
	DBUpdatesEnable bool `json:"DB_UPDATES_ENABLE"`

	BucketTotal             int `json:"BUCKET_TOTAL"`
	BucketTarget            int `json:"BUCKET_TARGET"`
	BucketHeartbeatGraceSec int `json:"BUCKET_HEARTBEAT_GRACE_SEC"`

	// PinnedBucketMin/Max let a v2 host temporarily use the exact static bucket
	// range of the v1 host it replaces during host-by-host migration. While set,
	// the orchestrator does not participate in jetmon_hosts dynamic ownership.
	PinnedBucketMin *int `json:"PINNED_BUCKET_MIN"`
	PinnedBucketMax *int `json:"PINNED_BUCKET_MAX"`

	// BucketNoMin/Max are the legacy v1 config names. They are accepted as
	// aliases for the pinned migration mode so operators can copy a v1 host's
	// bucket range directly into v2 config during cutover.
	BucketNoMin *int `json:"BUCKET_NO_MIN"`
	BucketNoMax *int `json:"BUCKET_NO_MAX"`

	BatchSize int    `json:"BATCH_SIZE"`
	AuthToken string `json:"AUTH_TOKEN"`

	VeriflierBatchSize int `json:"VERIFLIER_BATCH_SIZE"`
	SQLUpdateBatch     int `json:"SQL_UPDATE_BATCH"`
	DBConfigUpdatesMin int `json:"DB_CONFIG_UPDATES_MIN"`
	PeerOfflineLimit   int `json:"PEER_OFFLINE_LIMIT"`

	NumOfChecks          int `json:"NUM_OF_CHECKS"`
	TimeBetweenChecksSec int `json:"TIME_BETWEEN_CHECKS_SEC"`

	AlertCooldownMinutes int `json:"ALERT_COOLDOWN_MINUTES"`

	StatsUpdateIntervalMS     int  `json:"STATS_UPDATE_INTERVAL_MS"`
	StatsdSendMemUsage        bool `json:"STATSD_SEND_MEM_USAGE"`
	TimeBetweenNoticesMin     int  `json:"TIME_BETWEEN_NOTICES_MIN"`
	MinTimeBetweenRoundsSec   int  `json:"MIN_TIME_BETWEEN_ROUNDS_SEC"`
	NetCommsTimeout           int  `json:"NET_COMMS_TIMEOUT"`
	UseVariableCheckIntervals bool `json:"USE_VARIABLE_CHECK_INTERVALS"`

	LogFormat     string `json:"LOG_FORMAT"`
	DashboardPort int    `json:"DASHBOARD_PORT"`
	DebugPort     int    `json:"DEBUG_PORT"`
	APIPort       int    `json:"API_PORT"` // 0 = API server disabled

	// DeliveryOwnerHost constrains webhook and alert-contact delivery workers
	// to a single named host while the v2 single-binary deployment still uses
	// soft delivery locks. Empty preserves the legacy API_PORT behavior.
	DeliveryOwnerHost string `json:"DELIVERY_OWNER_HOST"`

	// Email transport selection for alert contacts. "stub" = log only
	// (default; safe for environments where email is not configured),
	// "smtp" = direct SMTP send (dev / staging with MailHog or similar),
	// "wpcom" = POST to a WPCOM-owned email API endpoint (production).
	// See docs/internal-api-reference.md "Family 5 → Email delivery".
	EmailTransport      string `json:"EMAIL_TRANSPORT"`
	EmailFrom           string `json:"EMAIL_FROM"`
	WPCOMEmailEndpoint  string `json:"WPCOM_EMAIL_ENDPOINT"`
	WPCOMEmailAuthToken string `json:"WPCOM_EMAIL_AUTH_TOKEN"`
	SMTPHost            string `json:"SMTP_HOST"`
	SMTPPort            int    `json:"SMTP_PORT"`
	SMTPUsername        string `json:"SMTP_USERNAME"`
	SMTPPassword        string `json:"SMTP_PASSWORD"`
	SMTPUseTLS          bool   `json:"SMTP_USE_TLS"`

	Verifiers []VerifierConfig `json:"VERIFIERS"`
}

// DBConfig holds MySQL connection parameters loaded from environment variables.
type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
}

var (
	mu      sync.RWMutex
	current *Config
	dbConf  *DBConfig
	path    string
)

// Load reads the config file at the given path and stores it.
func Load(configPath string) error {
	path = configPath
	return reload()
}

// Reload re-reads the config file from the path passed to Load.
func Reload() error {
	return reload()
}

func reload() error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("open config: %w", err)
	}

	cfg := defaults()
	if err := json.Unmarshal(raw, cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	applyDeprecatedAliases(raw, cfg)

	if err := validate(cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	mu.Lock()
	current = cfg
	mu.Unlock()
	return nil
}

// Get returns a snapshot of the current config. Safe for concurrent use.
func Get() *Config {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// LoadDB reads the database config from environment variables set by Docker,
// systemd EnvironmentFile, or the operator shell running CLI preflight commands.
func LoadDB() *DBConfig {
	db := &DBConfig{
		Host:     envOrDefault("DB_HOST", "localhost"),
		Port:     envOrDefault("DB_PORT", "3306"),
		User:     envOrDefault("DB_USER", "root"),
		Password: envOrDefault("DB_PASSWORD", ""),
		Name:     envOrDefault("DB_NAME", "jetmon_db"),
	}
	mu.Lock()
	dbConf = db
	mu.Unlock()
	return db
}

// GetDB returns the database config.
func GetDB() *DBConfig {
	mu.RLock()
	defer mu.RUnlock()
	return dbConf
}

func defaults() *Config {
	return &Config{
		NumWorkers:                   60,
		NumToProcess:                 40,
		DatasetSize:                  100,
		WorkerMaxMemMB:               53,
		LegacyStatusProjectionEnable: true,
		BucketTotal:                  1000,
		BucketTarget:                 500,
		BucketHeartbeatGraceSec:      600,
		BatchSize:                    32,
		VeriflierBatchSize:           200,
		SQLUpdateBatch:               1,
		DBConfigUpdatesMin:           10,
		PeerOfflineLimit:             3,
		NumOfChecks:                  3,
		TimeBetweenChecksSec:         30,
		AlertCooldownMinutes:         30,
		StatsUpdateIntervalMS:        10000,
		TimeBetweenNoticesMin:        59,
		MinTimeBetweenRoundsSec:      300,
		NetCommsTimeout:              10,
		LogFormat:                    "text",
		DashboardPort:                8080,
		DebugPort:                    6060,
		EmailTransport:               "stub",
		EmailFrom:                    "jetmon@noreply.invalid",
	}
}

func applyDeprecatedAliases(raw []byte, cfg *Config) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return
	}
	if _, hasNew := keys["LEGACY_STATUS_PROJECTION_ENABLE"]; hasNew {
		return
	}
	if _, hasOld := keys["DB_UPDATES_ENABLE"]; hasOld {
		cfg.LegacyStatusProjectionEnable = cfg.DBUpdatesEnable
	}
}

// LegacyStatusProjectionEnabled reports whether v2 should maintain the legacy
// v1 status projection on jetpack_monitor_sites. It defaults to true so a
// loaded-but-minimal config remains migration-compatible.
func LegacyStatusProjectionEnabled() bool {
	cfg := Get()
	if cfg == nil {
		return true
	}
	return cfg.LegacyStatusProjectionEnable
}

// PinnedBucketRange returns the migration-only static bucket range configured
// on this host. Explicit PINNED_BUCKET_* keys take precedence over legacy
// BUCKET_NO_* aliases after validation has checked for conflicts.
func (cfg *Config) PinnedBucketRange() (int, int, bool) {
	if cfg == nil {
		return 0, 0, false
	}
	if cfg.PinnedBucketMin != nil && cfg.PinnedBucketMax != nil {
		return *cfg.PinnedBucketMin, *cfg.PinnedBucketMax, true
	}
	if cfg.BucketNoMin != nil && cfg.BucketNoMax != nil {
		return *cfg.BucketNoMin, *cfg.BucketNoMax, true
	}
	return 0, 0, false
}

func validate(cfg *Config) error {
	if cfg.AuthToken == "" {
		return fmt.Errorf("AUTH_TOKEN is required")
	}
	if cfg.NumWorkers <= 0 {
		return fmt.Errorf("NUM_WORKERS must be > 0")
	}
	if cfg.BucketTotal <= 0 {
		return fmt.Errorf("BUCKET_TOTAL must be > 0")
	}
	if cfg.BucketTarget <= 0 || cfg.BucketTarget > cfg.BucketTotal {
		return fmt.Errorf("BUCKET_TARGET must be between 1 and BUCKET_TOTAL")
	}
	if err := validatePinnedBucketRange(cfg); err != nil {
		return err
	}
	if cfg.NetCommsTimeout <= 0 {
		return fmt.Errorf("NET_COMMS_TIMEOUT must be > 0")
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return fmt.Errorf("LOG_FORMAT must be 'text' or 'json'")
	}
	switch cfg.EmailTransport {
	case "", "stub":
		// Empty remains a compatibility alias for the safe default.
	case "smtp":
		if cfg.SMTPHost == "" {
			return fmt.Errorf("SMTP_HOST is required when EMAIL_TRANSPORT is 'smtp'")
		}
		if cfg.SMTPPort <= 0 {
			return fmt.Errorf("SMTP_PORT must be > 0 when EMAIL_TRANSPORT is 'smtp'")
		}
	case "wpcom":
		if cfg.WPCOMEmailEndpoint == "" {
			return fmt.Errorf("WPCOM_EMAIL_ENDPOINT is required when EMAIL_TRANSPORT is 'wpcom'")
		}
	default:
		return fmt.Errorf("EMAIL_TRANSPORT must be one of: stub, smtp, wpcom")
	}
	for i, v := range cfg.Verifiers {
		// host and port are required. Empty values silently parse to ""
		// then the orchestrator dials "host:" which resolves to port 80 — the
		// most common cause of "verifier connection refused" in dev configs
		// (typo: "ports" instead of "port").
		if v.Host == "" {
			return fmt.Errorf("VERIFIERS[%d] (%s): host is required", i, displayName(v, i))
		}
		if v.TransportPort() == "" {
			return fmt.Errorf("VERIFIERS[%d] (%s): port is required", i, displayName(v, i))
		}
	}
	return nil
}

func validatePinnedBucketRange(cfg *Config) error {
	hasPinned := cfg.PinnedBucketMin != nil || cfg.PinnedBucketMax != nil
	hasLegacy := cfg.BucketNoMin != nil || cfg.BucketNoMax != nil

	if hasPinned && (cfg.PinnedBucketMin == nil || cfg.PinnedBucketMax == nil) {
		return fmt.Errorf("PINNED_BUCKET_MIN and PINNED_BUCKET_MAX must be set together")
	}
	if hasLegacy && (cfg.BucketNoMin == nil || cfg.BucketNoMax == nil) {
		return fmt.Errorf("BUCKET_NO_MIN and BUCKET_NO_MAX must be set together")
	}
	if hasPinned && hasLegacy &&
		(*cfg.PinnedBucketMin != *cfg.BucketNoMin || *cfg.PinnedBucketMax != *cfg.BucketNoMax) {
		return fmt.Errorf("PINNED_BUCKET_* conflicts with legacy BUCKET_NO_* range")
	}

	min, max, ok := cfg.PinnedBucketRange()
	if !ok {
		return nil
	}
	if min < 0 {
		return fmt.Errorf("pinned bucket min must be >= 0")
	}
	if max < min {
		return fmt.Errorf("pinned bucket max must be >= min")
	}
	if max >= cfg.BucketTotal {
		return fmt.Errorf("pinned bucket max must be < BUCKET_TOTAL")
	}
	return nil
}

func displayName(v VerifierConfig, i int) string {
	if v.Name != "" {
		return v.Name
	}
	return fmt.Sprintf("verifier #%d", i)
}

// Debugf logs a debug message when DEBUG is true in the current config.
func Debugf(format string, args ...any) {
	mu.RLock()
	d := current != nil && current.Debug
	mu.RUnlock()
	if d {
		log.Printf("[DEBUG] "+format, args...)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

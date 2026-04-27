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
	GRPCPort  string `json:"grpc_port"`
	AuthToken string `json:"auth_token"`
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

	// Email transport selection for alert contacts. "stub" = log only
	// (default; safe for environments where email is not configured),
	// "smtp" = direct SMTP send (dev / staging with MailHog or similar),
	// "wpcom" = POST to a WPCOM-owned email API endpoint (production).
	// See API.md "Family 5 → Email delivery".
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

// DBConfig holds MySQL connection parameters loaded from db-config.conf.
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

// LoadDB reads the database config from environment variables (set by the
// Docker entrypoint) or falls back to the legacy db-config.conf format.
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
	if cfg.NetCommsTimeout <= 0 {
		return fmt.Errorf("NET_COMMS_TIMEOUT must be > 0")
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return fmt.Errorf("LOG_FORMAT must be 'text' or 'json'")
	}
	for i, v := range cfg.Verifiers {
		// host and grpc_port are required. Empty values silently parse to ""
		// then the orchestrator dials "host:" which resolves to port 80 — the
		// most common cause of "verifier connection refused" in dev configs
		// (typo: "port" instead of "grpc_port").
		if v.Host == "" {
			return fmt.Errorf("VERIFIERS[%d] (%s): host is required", i, displayName(v, i))
		}
		if v.GRPCPort == "" {
			return fmt.Errorf("VERIFIERS[%d] (%s): grpc_port is required", i, displayName(v, i))
		}
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

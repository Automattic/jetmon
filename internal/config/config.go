package config

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Automattic/jetmon/internal/checkmode"
)

// VerifierConfig holds connection details for a single Veriflier instance.
type VerifierConfig struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      string `json:"port"`
	GRPCPort  string `json:"grpc_port"` // Deprecated alias for Port.
	AuthToken string `json:"auth_token"`
}

const (
	VeriflierDiscoveryModeStatic = "static"
	VeriflierDiscoveryModeShadow = "shadow"
	VeriflierDiscoveryModeActive = "active"
)

const (
	RolloutModeActive        = "active"
	RolloutModeStandby       = "standby"
	RolloutModeAPIControlled = "api-controlled"
)

const (
	WPCOMNotifyModeLegacy = "legacy"
	WPCOMNotifyModeModern = "modern"

	defaultWPCOMNotifyModernEndpoint = "https://public-api.wordpress.com/wpcom/v2/jetpack-monitor/status-change"
	defaultWPCOMNotifyLegacyEndpoint = "https://jetpack.wordpress.com/jetmon/"
	defaultWPCOMNotifyLegacyCertPath = "certs/jetmon.crt"
	defaultWPCOMNotifyLegacyKeyPath  = "certs/jetmon.key"
)

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

	// Hostname is the stable Jetmon identity used for host ownership, process
	// health, and outbound notification identity.
	// Leave empty to use the runtime OS hostname.
	Hostname string `json:"HOSTNAME"`

	// StatsDHostPath is the explicit host segment used in the StatsD metric
	// prefix. Leave empty to use Hostname/runtime hostname as the fallback.
	StatsDHostPath string `json:"STATSD_HOST_PATH"`

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

	// VeriflierDiscoveryMode controls whether the monitor reads Veriflier
	// endpoints from the trusted DB registry. "static" preserves the VERIFIERS
	// list behavior, "shadow" reports registry drift without changing traffic,
	// and "active" uses the registry with static fallback if discovery fails.
	VeriflierDiscoveryMode string `json:"VERIFLIER_DISCOVERY_MODE"`

	NumOfChecks          int `json:"NUM_OF_CHECKS"`
	TimeBetweenChecksSec int `json:"TIME_BETWEEN_CHECKS_SEC"`

	AlertCooldownMinutes int `json:"ALERT_COOLDOWN_MINUTES"`

	StatsUpdateIntervalMS     int      `json:"STATS_UPDATE_INTERVAL_MS"`
	StatsdSendMemUsage        bool     `json:"STATSD_SEND_MEM_USAGE"`
	TimeBetweenNoticesMin     int      `json:"TIME_BETWEEN_NOTICES_MIN"`
	WPCOMNotifyEnable         bool     `json:"WPCOM_NOTIFY_ENABLE"`
	WPCOMNotifyMode           string   `json:"WPCOM_NOTIFY_MODE"`
	WPCOMNotifyModernEndpoint string   `json:"WPCOM_NOTIFY_MODERN_ENDPOINT"`
	WPCOMNotifyLegacyEndpoint string   `json:"WPCOM_NOTIFY_LEGACY_ENDPOINT"`
	WPCOMNotifyLegacyCertPath string   `json:"WPCOM_NOTIFY_LEGACY_CERT_PATH"`
	WPCOMNotifyLegacyKeyPath  string   `json:"WPCOM_NOTIFY_LEGACY_KEY_PATH"`
	WPCOMNotifyLegacyInsecure bool     `json:"WPCOM_NOTIFY_LEGACY_INSECURE_SKIP_VERIFY"`
	MinTimeBetweenRoundsSec   int      `json:"MIN_TIME_BETWEEN_ROUNDS_SEC"`
	NetCommsTimeout           int      `json:"NET_COMMS_TIMEOUT"`
	CheckDNSResolvers         []string `json:"CHECK_DNS_RESOLVERS"`
	BodyReadMaxBytes          int64    `json:"BODY_READ_MAX_BYTES"`
	BodyReadMaxMS             int      `json:"BODY_READ_MAX_MS"`
	KeywordReadMaxBytes       int64    `json:"KEYWORD_READ_MAX_BYTES"`
	KeywordReadMaxMS          int      `json:"KEYWORD_READ_MAX_MS"`
	DefaultCheckMethod        string   `json:"DEFAULT_CHECK_METHOD"`
	DefaultDetectionProfile   string   `json:"DEFAULT_DETECTION_PROFILE"`
	UseVariableCheckIntervals bool     `json:"USE_VARIABLE_CHECK_INTERVALS"`
	SchedulerEngine           string   `json:"SCHEDULER_ENGINE"`
	RolloutMode               string   `json:"ROLLOUT_MODE"`

	// StreamingLegacyProjectionIntervalMin controls the coarse compatibility
	// freshness write interval used by the streaming scheduler. It intentionally
	// does not affect check cadence; it only bounds jetmon_site_runtime
	// freshness staleness for rollback to the legacy scheduler.
	StreamingLegacyProjectionIntervalMin int `json:"STREAMING_LEGACY_PROJECTION_INTERVAL_MIN"`
	StreamingTargetReloadSec             int `json:"STREAMING_TARGET_RELOAD_SEC"`

	LogFormat         string `json:"LOG_FORMAT"`
	DashboardPort     int    `json:"DASHBOARD_PORT"`
	DashboardBindAddr string `json:"DASHBOARD_BIND_ADDR"`
	DebugPort         int    `json:"DEBUG_PORT"`
	APIPort           int    `json:"API_PORT"` // 0 = API server disabled

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

	Warnings []ConfigWarning `json:"-"`
}

// ConfigWarning reports a compatibility or ignored-key issue discovered while
// loading a config file. Warnings never block parsing.
type ConfigWarning struct {
	Key     string
	Message string
}

// DBConfig holds MySQL connection parameters loaded from environment variables.
type DBConfig struct {
	Host                string
	Port                string
	User                string
	Password            string
	Name                string
	ServerMapPath       string
	ServerMapDataset    string
	ServerMapDatacenter string
	ServerMapAddress    string
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
	cfg.Warnings = collectConfigWarnings(raw)
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
		Host:                envOrDefault("DB_HOST", "localhost"),
		Port:                envOrDefault("DB_PORT", "3306"),
		User:                envOrDefault("DB_USER", "root"),
		Password:            envOrDefault("DB_PASSWORD", ""),
		Name:                envOrDefault("DB_NAME", "jetmon_db"),
		ServerMapPath:       strings.TrimSpace(os.Getenv("DB_SERVER_MAP_PATH")),
		ServerMapDataset:    envOrDefault("DB_SERVER_MAP_DATASET", "misc"),
		ServerMapDatacenter: strings.TrimSpace(os.Getenv("DB_SERVER_MAP_DATACENTER")),
		ServerMapAddress:    envOrDefault("DB_SERVER_MAP_ADDRESS", "internet"),
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
		NumWorkers:                           60,
		NumToProcess:                         40,
		DatasetSize:                          100,
		WorkerMaxMemMB:                       0,
		LegacyStatusProjectionEnable:         true,
		BucketTotal:                          1000,
		BucketTarget:                         500,
		BucketHeartbeatGraceSec:              600,
		BatchSize:                            32,
		VeriflierBatchSize:                   200,
		SQLUpdateBatch:                       1,
		DBConfigUpdatesMin:                   10,
		PeerOfflineLimit:                     3,
		VeriflierDiscoveryMode:               VeriflierDiscoveryModeStatic,
		NumOfChecks:                          3,
		TimeBetweenChecksSec:                 30,
		AlertCooldownMinutes:                 30,
		StatsUpdateIntervalMS:                10000,
		TimeBetweenNoticesMin:                59,
		WPCOMNotifyEnable:                    true,
		WPCOMNotifyMode:                      WPCOMNotifyModeLegacy,
		WPCOMNotifyModernEndpoint:            defaultWPCOMNotifyModernEndpoint,
		WPCOMNotifyLegacyEndpoint:            defaultWPCOMNotifyLegacyEndpoint,
		WPCOMNotifyLegacyCertPath:            defaultWPCOMNotifyLegacyCertPath,
		WPCOMNotifyLegacyKeyPath:             defaultWPCOMNotifyLegacyKeyPath,
		WPCOMNotifyLegacyInsecure:            true,
		MinTimeBetweenRoundsSec:              300,
		NetCommsTimeout:                      10,
		BodyReadMaxBytes:                     1048576,
		BodyReadMaxMS:                        250,
		KeywordReadMaxBytes:                  1048576,
		KeywordReadMaxMS:                     0,
		DefaultCheckMethod:                   checkmode.MethodGET,
		DefaultDetectionProfile:              checkmode.ProfileFull,
		SchedulerEngine:                      "legacy",
		RolloutMode:                          RolloutModeActive,
		StreamingLegacyProjectionIntervalMin: 15,
		StreamingTargetReloadSec:             300,
		LogFormat:                            "text",
		DashboardPort:                        8080,
		DashboardBindAddr:                    "127.0.0.1",
		DebugPort:                            6060,
		EmailTransport:                       "stub",
		EmailFrom:                            "jetmon@noreply.invalid",
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

type deprecatedConfigKeyWarning struct {
	key     string
	message string
}

var deprecatedConfigKeyWarnings = []deprecatedConfigKeyWarning{
	{
		key:     "WORKER_MAX_CHECKS",
		message: "ignored by Jetmon v2; Node worker recycle-by-check-count does not apply to Go goroutine workers",
	},
	{
		key:     "TIMEOUT_FOR_REQUESTS_SEC",
		message: "ignored by Jetmon v2; Veriflier retry queue expiration is handled by the v2 retry and escalation flow",
	},
	{
		key:     "DB_UPDATES_ENABLE",
		message: "deprecated alias; use LEGACY_STATUS_PROJECTION_ENABLE",
	},
	{
		key:     "BUCKET_NO_MIN",
		message: "deprecated migration alias; use PINNED_BUCKET_MIN during v1-to-v2 pinned rollout",
	},
	{
		key:     "BUCKET_NO_MAX",
		message: "deprecated migration alias; use PINNED_BUCKET_MAX during v1-to-v2 pinned rollout",
	},
	{
		key:     "NUM_TO_PROCESS",
		message: "parsed for copied v1 config compatibility but does not cap v2 scheduler throughput; tune NUM_WORKERS, DATASET_SIZE, and scheduler mode instead",
	},
	{
		key:     "BATCH_SIZE",
		message: "parsed for copied v1 config compatibility but is not used by the v2 scheduler; use DATASET_SIZE for database fetch paging",
	},
	{
		key:     "VERIFLIER_BATCH_SIZE",
		message: "parsed for copied v1 config compatibility but has no production tuning effect on the current v2 Veriflier transport",
	},
	{
		key:     "SQL_UPDATE_BATCH",
		message: "parsed for copied v1 config compatibility but does not control v2 database write batching",
	},
	{
		key:     "TIME_BETWEEN_CHECKS_SEC",
		message: "parsed for copied v1 config compatibility but does not control v2 retry cadence; v2 uses per-site check intervals, runtime due state, and bounded retry scheduling",
	},
	{
		key:     "TIME_BETWEEN_NOTICES_MIN",
		message: "parsed for copied v1 config compatibility but does not gate v2 WPCOM status-change notifications; v2 notification timing follows incident state and Veriflier confirmation",
	},
}

func collectConfigWarnings(raw []byte) []ConfigWarning {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil
	}

	warnings := make([]ConfigWarning, 0)
	deprecated := make(map[string]struct{}, len(deprecatedConfigKeyWarnings))
	for _, item := range deprecatedConfigKeyWarnings {
		deprecated[item.key] = struct{}{}
		if _, ok := keys[item.key]; ok {
			warnings = append(warnings, ConfigWarning{Key: item.key, Message: item.message})
		}
	}

	known := knownConfigJSONKeys()
	var unknown []string
	for key := range keys {
		if _, ok := known[key]; ok {
			continue
		}
		if _, ok := deprecated[key]; ok {
			continue
		}
		unknown = append(unknown, key)
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		warnings = append(warnings, ConfigWarning{
			Key:     key,
			Message: "not recognized by Jetmon v2 and will be ignored; check for typos or remove it",
		})
	}

	warnings = append(warnings, collectStatsDHostPathWarnings(keys["STATSD_HOST_PATH"])...)
	warnings = append(warnings, collectVerifierConfigWarnings(keys["VERIFIERS"])...)
	return warnings
}

func collectStatsDHostPathWarnings(raw json.RawMessage) []ConfigWarning {
	if len(raw) == 0 {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.Count(value, ".") >= 2 {
		return []ConfigWarning{{
			Key:     "STATSD_HOST_PATH",
			Message: "looks like a raw hostname; Monitor production should use the v1-compatible metric host path <datacenter>.<node>, for example dfw1.jetmon-prod-1",
		}}
	}
	return nil
}

func knownConfigJSONKeys() map[string]struct{} {
	known := make(map[string]struct{})
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		known[name] = struct{}{}
	}
	return known
}

func collectVerifierConfigWarnings(raw json.RawMessage) []ConfigWarning {
	if len(raw) == 0 {
		return nil
	}
	var verifiers []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &verifiers); err != nil {
		return nil
	}
	warnings := make([]ConfigWarning, 0)
	for i, verifier := range verifiers {
		if _, ok := verifier["grpc_port"]; ok {
			warnings = append(warnings, ConfigWarning{
				Key:     fmt.Sprintf("VERIFIERS[%d].grpc_port", i),
				Message: "deprecated Veriflier port alias; use port",
			})
		}
	}
	return warnings
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

// WPCOMNotifyEnabled reports whether the legacy WPCOM status-change
// notification path should make outbound calls. It defaults to true for
// production compatibility; test fleets can set WPCOM_NOTIFY_ENABLE=false.
func WPCOMNotifyEnabled() bool {
	cfg := Get()
	if cfg == nil {
		return true
	}
	return cfg.WPCOMNotifyEnable
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
	cfg.Hostname = strings.TrimSpace(cfg.Hostname)
	cfg.StatsDHostPath = strings.TrimSpace(cfg.StatsDHostPath)
	if err := validateStatsDHostPath(cfg.StatsDHostPath); err != nil {
		return err
	}
	if cfg.NumWorkers < 0 {
		return fmt.Errorf("NUM_WORKERS must be >= 0")
	}
	if cfg.NumWorkers == 0 {
		cfg.NumWorkers = 60
	}
	if cfg.DatasetSize < 0 {
		return fmt.Errorf("DATASET_SIZE must be >= 0")
	}
	if cfg.DatasetSize == 0 {
		cfg.DatasetSize = 100
	}
	if cfg.BucketTotal <= 0 {
		return fmt.Errorf("BUCKET_TOTAL must be > 0")
	}
	if cfg.BucketTarget < 0 {
		return fmt.Errorf("BUCKET_TARGET must be >= 0")
	}
	if cfg.BucketTarget == 0 {
		cfg.BucketTarget = cfg.BucketTotal
	}
	if cfg.BucketTarget > cfg.BucketTotal {
		return fmt.Errorf("BUCKET_TARGET must be <= BUCKET_TOTAL")
	}
	if err := validatePinnedBucketRange(cfg); err != nil {
		return err
	}
	if cfg.NetCommsTimeout <= 0 {
		return fmt.Errorf("NET_COMMS_TIMEOUT must be > 0")
	}
	if err := validateCheckDNSResolvers(cfg.CheckDNSResolvers); err != nil {
		return err
	}
	if cfg.BodyReadMaxBytes < 0 {
		return fmt.Errorf("BODY_READ_MAX_BYTES must be >= 0")
	}
	if cfg.BodyReadMaxBytes == 0 {
		cfg.BodyReadMaxBytes = 1048576
	}
	if cfg.BodyReadMaxMS < 0 {
		return fmt.Errorf("BODY_READ_MAX_MS must be >= 0")
	}
	if cfg.BodyReadMaxMS == 0 {
		cfg.BodyReadMaxMS = 250
	}
	if cfg.KeywordReadMaxBytes < 0 {
		return fmt.Errorf("KEYWORD_READ_MAX_BYTES must be >= 0")
	}
	if cfg.KeywordReadMaxBytes == 0 {
		cfg.KeywordReadMaxBytes = 1048576
	}
	if cfg.KeywordReadMaxMS < 0 {
		return fmt.Errorf("KEYWORD_READ_MAX_MS must be >= 0")
	}
	method, err := checkmode.NormalizeMethod(cfg.DefaultCheckMethod, checkmode.MethodGET)
	if err != nil {
		return fmt.Errorf("DEFAULT_CHECK_METHOD: %w", err)
	}
	cfg.DefaultCheckMethod = method
	profile, err := checkmode.NormalizeProfile(cfg.DefaultDetectionProfile, checkmode.ProfileFull)
	if err != nil {
		return fmt.Errorf("DEFAULT_DETECTION_PROFILE: %w", err)
	}
	cfg.DefaultDetectionProfile = profile
	if cfg.MinTimeBetweenRoundsSec < 0 {
		return fmt.Errorf("MIN_TIME_BETWEEN_ROUNDS_SEC must be >= 0")
	}
	applyWPCOMNotifyDefaults(cfg)
	switch cfg.WPCOMNotifyMode {
	case WPCOMNotifyModeLegacy:
		if strings.TrimSpace(cfg.WPCOMNotifyLegacyEndpoint) == "" {
			return fmt.Errorf("WPCOM_NOTIFY_LEGACY_ENDPOINT is required when WPCOM_NOTIFY_MODE is 'legacy'")
		}
		if strings.TrimSpace(cfg.WPCOMNotifyLegacyCertPath) == "" {
			return fmt.Errorf("WPCOM_NOTIFY_LEGACY_CERT_PATH is required when WPCOM_NOTIFY_MODE is 'legacy'")
		}
		if strings.TrimSpace(cfg.WPCOMNotifyLegacyKeyPath) == "" {
			return fmt.Errorf("WPCOM_NOTIFY_LEGACY_KEY_PATH is required when WPCOM_NOTIFY_MODE is 'legacy'")
		}
	case WPCOMNotifyModeModern:
		if strings.TrimSpace(cfg.WPCOMNotifyModernEndpoint) == "" {
			return fmt.Errorf("WPCOM_NOTIFY_MODERN_ENDPOINT is required when WPCOM_NOTIFY_MODE is 'modern'")
		}
	default:
		return fmt.Errorf("WPCOM_NOTIFY_MODE must be one of: legacy, modern")
	}
	switch cfg.SchedulerEngine {
	case "", "legacy":
		cfg.SchedulerEngine = "legacy"
	case "streaming":
	default:
		return fmt.Errorf("SCHEDULER_ENGINE must be 'legacy' or 'streaming'")
	}
	cfg.RolloutMode = normalizeRolloutMode(cfg.RolloutMode)
	switch cfg.RolloutMode {
	case RolloutModeActive, RolloutModeStandby, RolloutModeAPIControlled:
	default:
		return fmt.Errorf("ROLLOUT_MODE must be one of: active, standby, api-controlled")
	}
	if cfg.StreamingLegacyProjectionIntervalMin == 0 {
		cfg.StreamingLegacyProjectionIntervalMin = 15
	}
	if cfg.StreamingLegacyProjectionIntervalMin < 5 {
		return fmt.Errorf("STREAMING_LEGACY_PROJECTION_INTERVAL_MIN must be between 5 and 15")
	}
	if cfg.StreamingLegacyProjectionIntervalMin > 15 {
		return fmt.Errorf("STREAMING_LEGACY_PROJECTION_INTERVAL_MIN must be between 5 and 15")
	}
	if cfg.StreamingTargetReloadSec == 0 {
		cfg.StreamingTargetReloadSec = 300
	}
	if cfg.StreamingTargetReloadSec < 0 {
		return fmt.Errorf("STREAMING_TARGET_RELOAD_SEC must be > 0")
	}
	cfg.VeriflierDiscoveryMode = normalizeVeriflierDiscoveryMode(cfg.VeriflierDiscoveryMode)
	switch cfg.VeriflierDiscoveryMode {
	case VeriflierDiscoveryModeStatic, VeriflierDiscoveryModeShadow, VeriflierDiscoveryModeActive:
	default:
		return fmt.Errorf("VERIFLIER_DISCOVERY_MODE must be one of: static, shadow, active")
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return fmt.Errorf("LOG_FORMAT must be 'text' or 'json'")
	}
	if strings.TrimSpace(cfg.DashboardBindAddr) == "" {
		cfg.DashboardBindAddr = "127.0.0.1"
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

// StatsDMetricHost returns the host path segment used in StatsD metric names.
// An explicit STATSD_HOST_PATH wins so production can preserve v1 Graphite
// paths without relying on hostname parsing. Empty falls back to the resolved
// process identity for local/dev compatibility.
func (cfg *Config) StatsDMetricHost(resolvedHostname string) string {
	if cfg != nil {
		if path := strings.TrimSpace(cfg.StatsDHostPath); path != "" {
			return path
		}
	}
	return strings.TrimSpace(resolvedHostname)
}

func validateStatsDHostPath(path string) error {
	if path == "" {
		return nil
	}
	if strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
		return fmt.Errorf("STATSD_HOST_PATH must not start or end with a dot")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("STATSD_HOST_PATH must not contain empty path segments")
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
			return fmt.Errorf("STATSD_HOST_PATH may contain only letters, numbers, dots, underscores, and hyphens")
		}
	}
	return nil
}

func validateCheckDNSResolvers(servers []string) error {
	for i, raw := range servers {
		if _, err := normalizeCheckDNSResolver(raw); err != nil {
			return fmt.Errorf("CHECK_DNS_RESOLVERS[%d]: %w", i, err)
		}
	}
	return nil
}

func normalizeCheckDNSResolver(raw string) (string, error) {
	server := strings.TrimSpace(raw)
	if server == "" {
		return "", fmt.Errorf("resolver must not be empty")
	}
	host := server
	port := "53"
	if splitHost, splitPort, err := net.SplitHostPort(server); err == nil {
		host = strings.Trim(splitHost, "[]")
		port = splitPort
	} else if strings.Contains(server, ":") {
		if ip := net.ParseIP(strings.Trim(server, "[]")); ip == nil || ip.To4() != nil {
			return "", fmt.Errorf("resolver must be an IP literal with optional port")
		}
		host = strings.Trim(server, "[]")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("resolver must be an IP literal with optional port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return "", fmt.Errorf("resolver port must be between 1 and 65535")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(n)), nil
}

func normalizeVeriflierDiscoveryMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return VeriflierDiscoveryModeStatic
	}
	return mode
}

func normalizeRolloutMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return RolloutModeActive
	}
	return mode
}

func normalizeWPCOMNotifyMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return WPCOMNotifyModeLegacy
	}
	return mode
}

func applyWPCOMNotifyDefaults(cfg *Config) {
	cfg.WPCOMNotifyMode = normalizeWPCOMNotifyMode(cfg.WPCOMNotifyMode)
	if strings.TrimSpace(cfg.WPCOMNotifyModernEndpoint) == "" {
		cfg.WPCOMNotifyModernEndpoint = defaultWPCOMNotifyModernEndpoint
	}
	if strings.TrimSpace(cfg.WPCOMNotifyLegacyEndpoint) == "" {
		cfg.WPCOMNotifyLegacyEndpoint = defaultWPCOMNotifyLegacyEndpoint
	}
	if strings.TrimSpace(cfg.WPCOMNotifyLegacyCertPath) == "" {
		cfg.WPCOMNotifyLegacyCertPath = defaultWPCOMNotifyLegacyCertPath
	}
	if strings.TrimSpace(cfg.WPCOMNotifyLegacyKeyPath) == "" {
		cfg.WPCOMNotifyLegacyKeyPath = defaultWPCOMNotifyLegacyKeyPath
	}
}

func (cfg *Config) VeriflierDiscoveryModeOrDefault() string {
	if cfg == nil {
		return VeriflierDiscoveryModeStatic
	}
	return normalizeVeriflierDiscoveryMode(cfg.VeriflierDiscoveryMode)
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

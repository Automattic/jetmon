package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/config"
)

var (
	db      *sql.DB
	readDB  *sql.DB
	manager *Manager
)

// Site combines the v1-shaped jetpack_monitor_sites row with Jetmon-owned
// sidecar config/runtime tables.
type Site struct {
	ID               int64
	BlogID           int64
	BucketNo         int
	MonitorURL       string
	MonitorActive    bool
	SiteStatus       int
	LastStatusChange *time.Time
	CheckInterval    int
	LastCheckedAt    *time.Time
	NextCheckAt      *time.Time

	SSLExpiryDate        *time.Time
	CheckKeyword         *string
	ForbiddenKeyword     *string
	ForbiddenKeywords    *string // raw JSON array
	MaintenanceStart     *time.Time
	MaintenanceEnd       *time.Time
	CustomHeaders        *string // raw JSON
	TimeoutSeconds       *int
	RedirectPolicy       string
	AlertCooldownMinutes *int
	LastAlertSentAt      *time.Time
	RequestMethod        string
	DetectionProfile     string

	// CheckHistoryMode / CheckHistorySampleRate are per-site overrides for
	// jetmon_check_history recording. nil means "use the configured default".
	CheckHistoryMode       *string
	CheckHistorySampleRate *int
}

// Connect opens the MySQL connection pool using the loaded DBConfig.
func Connect() error {
	cfg := config.GetDB()
	if cfg == nil {
		cfg = config.LoadDB()
	}

	next, err := NewManager(cfg, config.Get())
	if err != nil {
		return fmt.Errorf("open db manager: %w", err)
	}
	if err := next.Ping(context.Background()); err != nil {
		_ = next.Close()
		return err
	}
	old := manager
	manager = next
	db = next.WriteDB()
	readDB = next.ReadDB()
	if old != nil {
		_ = old.Close()
	}
	log.Printf("db connected: %s", next.Summary())
	return nil
}

func maxOpenConnectionsForConfig(cfg *config.Config, gomaxprocs int) int {
	if gomaxprocs < 1 {
		gomaxprocs = 1
	}
	multiplier := 8
	minConns := 16
	if cfg != nil && cfg.SchedulerEngine == "streaming" {
		multiplier = 16
		minConns = 64
	}
	maxOpenConns := gomaxprocs * multiplier
	if maxOpenConns < minConns {
		return minConns
	}
	if maxOpenConns > 256 {
		return 256
	}
	return maxOpenConns
}

// ConnectWithRetry retries Connect with exponential backoff.
func ConnectWithRetry(maxAttempts int) error {
	var err error
	for i := range maxAttempts {
		err = Connect()
		if err == nil {
			return nil
		}
		wait := time.Duration(1<<i) * time.Second
		if wait > 30*time.Second {
			wait = 30 * time.Second
		}
		log.Printf("db connect attempt %d failed: %v, retrying in %s", i+1, err, wait)
		time.Sleep(wait)
	}
	return fmt.Errorf("db connect failed after %d attempts: %w", maxAttempts, err)
}

// DB returns the underlying *sql.DB for direct use when needed.
func DB() *sql.DB {
	return db
}

// ReadDB returns the read pool. If no read-only database is configured, it
// points at the same endpoint configuration as the write pool.
func ReadDB() *sql.DB {
	if readDB != nil {
		return readDB
	}
	return db
}

// WriteDB returns the write pool.
func WriteDB() *sql.DB {
	return db
}

// Ping checks database connectivity.
func Ping() error {
	if manager != nil {
		return manager.Ping(context.Background())
	}
	return db.Ping()
}

// Hostname returns the system hostname used as the host_id in jetmon_hosts.
func Hostname() string {
	if cfg := config.Get(); cfg != nil {
		if h := strings.TrimSpace(cfg.Hostname); h != "" {
			return h
		}
	}
	if h := strings.TrimSpace(os.Getenv("JETMON_HOSTNAME")); h != "" {
		return h
	}
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

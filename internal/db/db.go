package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Automattic/jetmon/internal/config"
)

var db *sql.DB

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
}

// Connect opens the MySQL connection pool using the loaded DBConfig.
func Connect() error {
	cfg := config.GetDB()
	if cfg == nil {
		cfg = config.LoadDB()
	}

	// Use mysql.Config.FormatDSN so the password is never interpolated into
	// a format string (prevents accidental exposure in error chains or logs).
	mc := mysql.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = cfg.Host + ":" + cfg.Port
	mc.DBName = cfg.Name
	mc.ParseTime = true
	mc.Timeout = 10 * time.Second
	mc.ReadTimeout = 30 * time.Second
	mc.WriteTimeout = 30 * time.Second

	var err error
	db, err = sql.Open("mysql", mc.FormatDSN())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	maxOpenConns := maxOpenConnectionsForConfig(config.Get(), runtime.GOMAXPROCS(0))
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns / 2)
	db.SetConnMaxLifetime(5 * time.Minute)

	return db.Ping()
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

// Ping checks database connectivity.
func Ping() error {
	return db.Ping()
}

// Hostname returns the system hostname used as the host_id in jetmon_hosts.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

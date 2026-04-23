package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Automattic/jetmon/internal/config"
)

var db *sql.DB

// Site mirrors the jetpack_monitor_sites row plus new Jetmon 2 columns.
type Site struct {
	ID               int64
	BlogID           int64
	BucketNo         int
	MonitorURL       string
	MonitorActive    bool
	SiteStatus       int
	LastStatusChange time.Time
	CheckInterval    int
	LastCheckedAt    *time.Time

	SSLExpiryDate        *time.Time
	CheckKeyword         *string
	MaintenanceStart     *time.Time
	MaintenanceEnd       *time.Time
	CustomHeaders        *string // raw JSON
	TimeoutSeconds       *int
	RedirectPolicy       string
	AlertCooldownMinutes *int
	LastAlertSentAt      *time.Time
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

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	return db.Ping()
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

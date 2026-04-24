package db

import (
	"fmt"
	"log"
)

// migration holds a single idempotent schema change.
type migration struct {
	id  int
	sql string
}

var migrations = []migration{
	{id: 1, sql: `CREATE TABLE IF NOT EXISTS jetmon_schema_migrations (
		id           INT UNSIGNED NOT NULL PRIMARY KEY,
		applied_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{id: 2, sql: `CREATE TABLE IF NOT EXISTS jetpack_monitor_sites (
		jetpack_monitor_site_id  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		blog_id                  BIGINT UNSIGNED NOT NULL,
		bucket_no                SMALLINT UNSIGNED NOT NULL DEFAULT 0,
		monitor_url              VARCHAR(2083) NOT NULL DEFAULT '',
		monitor_active           TINYINT UNSIGNED NOT NULL DEFAULT 0,
		site_status              TINYINT NOT NULL DEFAULT 1,
		last_status_change       DATETIME NULL,
		check_interval           SMALLINT UNSIGNED NOT NULL DEFAULT 5,
		INDEX idx_bucket_active (bucket_no, monitor_active)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{id: 3, sql: `ALTER TABLE jetpack_monitor_sites
		ADD COLUMN ssl_expiry_date        DATE NULL,
		ADD COLUMN check_keyword          VARCHAR(500) NULL,
		ADD COLUMN maintenance_start      DATETIME NULL,
		ADD COLUMN maintenance_end        DATETIME NULL,
		ADD COLUMN custom_headers         JSON NULL,
		ADD COLUMN timeout_seconds        TINYINT UNSIGNED NULL,
		ADD COLUMN redirect_policy        ENUM('follow','alert','fail') NULL DEFAULT 'follow',
		ADD COLUMN alert_cooldown_minutes SMALLINT UNSIGNED NULL`},

	{id: 4, sql: `CREATE TABLE IF NOT EXISTS jetmon_hosts (
		host_id        VARCHAR(255) NOT NULL PRIMARY KEY,
		bucket_min     SMALLINT UNSIGNED NOT NULL,
		bucket_max     SMALLINT UNSIGNED NOT NULL,
		last_heartbeat TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		status         ENUM('active','draining') NOT NULL DEFAULT 'active'
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{id: 5, sql: `CREATE TABLE IF NOT EXISTS jetmon_audit_log (
		id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		blog_id      BIGINT UNSIGNED NOT NULL,
		event_type   VARCHAR(64) NOT NULL,
		source       VARCHAR(255) NOT NULL DEFAULT 'local',
		http_code    SMALLINT NULL,
		error_code   TINYINT NULL,
		rtt_ms       INT NULL,
		old_status   TINYINT NULL,
		new_status   TINYINT NULL,
		detail       TEXT NULL,
		created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_blog_id_created (blog_id, created_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{id: 6, sql: `CREATE TABLE IF NOT EXISTS jetmon_check_history (
		id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		blog_id    BIGINT UNSIGNED NOT NULL,
		http_code  SMALLINT NULL,
		error_code TINYINT NULL,
		rtt_ms     INT NULL,
		dns_ms     INT NULL,
		tcp_ms     INT NULL,
		tls_ms     INT NULL,
		ttfb_ms    INT NULL,
		checked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_blog_id_checked (blog_id, checked_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{id: 7, sql: `CREATE TABLE IF NOT EXISTS jetmon_false_positives (
		id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		blog_id    BIGINT UNSIGNED NOT NULL,
		http_code  SMALLINT NULL,
		error_code TINYINT NULL,
		rtt_ms     INT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_blog_id (blog_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{id: 8, sql: `ALTER TABLE jetpack_monitor_sites
		ADD COLUMN last_checked_at DATETIME NULL,
		ADD COLUMN last_alert_sent_at DATETIME NULL,
		ADD INDEX idx_bucket_monitor_last_checked (bucket_no, monitor_active, last_checked_at)`},

	{id: 9, sql: `CREATE TABLE IF NOT EXISTS jetmon_site_events (
		id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		jetpack_monitor_site_id BIGINT UNSIGNED NOT NULL,
		endpoint_id BIGINT UNSIGNED NULL,
		check_type TINYINT UNSIGNED NULL,
		event_type TINYINT UNSIGNED NOT NULL,
		severity TINYINT UNSIGNED NOT NULL,
		started_at DATETIME NOT NULL,
		ended_at DATETIME NULL,
		cause_event_id BIGINT UNSIGNED NULL,
		resolution_reason TINYINT UNSIGNED NULL,
		metadata JSON NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_site_event_type (jetpack_monitor_site_id, event_type),
		INDEX idx_event_type_started (event_type, started_at),
		INDEX idx_site_event_open (jetpack_monitor_site_id, check_type, ended_at),
		INDEX idx_endpoint_check_open (endpoint_id, check_type, ended_at),
		CONSTRAINT fk_jetmon_site_events_site
			FOREIGN KEY (jetpack_monitor_site_id)
			REFERENCES jetpack_monitor_sites (jetpack_monitor_site_id)
			ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},
}

// Migrate applies all pending migrations idempotently.
func Migrate() error {
	// Ensure the migrations table exists first (migration 1 is special).
	if _, err := db.Exec(migrations[0].sql); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	if err := markApplied(migrations[0].id); err != nil {
		return err
	}

	for _, m := range migrations[1:] {
		applied, err := isApplied(m.id)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		log.Printf("applying migration %d", m.id)
		if _, err := db.Exec(m.sql); err != nil {
			return fmt.Errorf("migration %d: %w", m.id, err)
		}
		if err := markApplied(m.id); err != nil {
			return err
		}
	}
	return nil
}

func isApplied(id int) (bool, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM jetmon_schema_migrations WHERE id = ?`, id).Scan(&count)
	return count > 0, err
}

func markApplied(id int) error {
	_, err := db.Exec(
		`INSERT IGNORE INTO jetmon_schema_migrations (id) VALUES (?)`, id,
	)
	return err
}

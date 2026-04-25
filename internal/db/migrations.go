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
	{1, `CREATE TABLE IF NOT EXISTS jetmon_schema_migrations (
		id           INT UNSIGNED NOT NULL PRIMARY KEY,
		applied_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{2, `CREATE TABLE IF NOT EXISTS jetpack_monitor_sites (
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

	{3, `ALTER TABLE jetpack_monitor_sites
		ADD COLUMN ssl_expiry_date        DATE NULL,
		ADD COLUMN check_keyword          VARCHAR(500) NULL,
		ADD COLUMN maintenance_start      DATETIME NULL,
		ADD COLUMN maintenance_end        DATETIME NULL,
		ADD COLUMN custom_headers         JSON NULL,
		ADD COLUMN timeout_seconds        TINYINT UNSIGNED NULL,
		ADD COLUMN redirect_policy        ENUM('follow','alert','fail') NULL DEFAULT 'follow',
		ADD COLUMN alert_cooldown_minutes SMALLINT UNSIGNED NULL`},

	{4, `CREATE TABLE IF NOT EXISTS jetmon_hosts (
		host_id        VARCHAR(255) NOT NULL PRIMARY KEY,
		bucket_min     SMALLINT UNSIGNED NOT NULL,
		bucket_max     SMALLINT UNSIGNED NOT NULL,
		last_heartbeat TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		status         ENUM('active','draining') NOT NULL DEFAULT 'active'
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{5, `CREATE TABLE IF NOT EXISTS jetmon_audit_log (
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

	{6, `CREATE TABLE IF NOT EXISTS jetmon_check_history (
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

	{7, `CREATE TABLE IF NOT EXISTS jetmon_false_positives (
		id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		blog_id    BIGINT UNSIGNED NOT NULL,
		http_code  SMALLINT NULL,
		error_code TINYINT NULL,
		rtt_ms     INT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_blog_id (blog_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{8, `ALTER TABLE jetpack_monitor_sites
		ADD COLUMN last_checked_at DATETIME NULL,
		ADD COLUMN last_alert_sent_at DATETIME NULL,
		ADD INDEX idx_bucket_monitor_last_checked (bucket_no, monitor_active, last_checked_at)`},

	// Migration 9 retires jetmon_audit_log's site-state columns. Per-probe data lives in
	// jetmon_check_history; status transitions move to jetmon_event_transitions (migration 11).
	// What remains is purely operational: WPCOM, retries, verifier RPC, suppression, config.
	{9, `ALTER TABLE jetmon_audit_log
		DROP COLUMN http_code,
		DROP COLUMN error_code,
		DROP COLUMN rtt_ms,
		DROP COLUMN old_status,
		DROP COLUMN new_status,
		MODIFY COLUMN blog_id BIGINT UNSIGNED NULL,
		MODIFY COLUMN detail VARCHAR(1024) NULL,
		ADD COLUMN event_id BIGINT UNSIGNED NULL AFTER blog_id,
		ADD COLUMN metadata JSON NULL AFTER detail,
		ADD INDEX idx_event_id (event_id),
		ADD INDEX idx_event_type_created (event_type, created_at)`},

	// Migration 10 creates the events table — current authoritative state of every incident.
	// dedup_key is a generated column that is NULL while ended_at IS NULL, full identity tuple while open.
	// The UNIQUE KEY enforces "one open event per tuple" without requiring partial indexes (which MySQL lacks).
	{10, `CREATE TABLE IF NOT EXISTS jetmon_events (
		id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		blog_id             BIGINT UNSIGNED NOT NULL,
		endpoint_id         BIGINT UNSIGNED NULL,
		check_type          VARCHAR(64) NOT NULL,
		discriminator       VARCHAR(128) NULL,
		severity            TINYINT UNSIGNED NOT NULL,
		state               VARCHAR(32) NOT NULL,
		started_at          TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
		ended_at            TIMESTAMP(3) NULL,
		resolution_reason   VARCHAR(64) NULL,
		cause_event_id      BIGINT UNSIGNED NULL,
		metadata            JSON NULL,
		updated_at          TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
		dedup_key           VARCHAR(255) GENERATED ALWAYS AS (
			IF(ended_at IS NULL,
			   CONCAT_WS(':', blog_id, COALESCE(endpoint_id, 0), check_type, COALESCE(discriminator, '')),
			   NULL)
		) STORED,
		UNIQUE KEY uk_open_dedup (dedup_key),
		INDEX idx_blog_id_started (blog_id, started_at),
		INDEX idx_blog_id_active (blog_id, ended_at),
		INDEX idx_check_type_started (check_type, started_at),
		INDEX idx_cause_event_id (cause_event_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 11 creates the append-only history of every mutation to jetmon_events.
	// One row per change; never updated, never deleted. Together with jetmon_events,
	// this is the full event-sourced record. blog_id is denormalized to keep SLA queries
	// off the events table.
	{11, `CREATE TABLE IF NOT EXISTS jetmon_event_transitions (
		id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		event_id          BIGINT UNSIGNED NOT NULL,
		blog_id           BIGINT UNSIGNED NOT NULL,
		severity_before   TINYINT UNSIGNED NULL,
		severity_after    TINYINT UNSIGNED NULL,
		state_before      VARCHAR(32) NULL,
		state_after       VARCHAR(32) NULL,
		reason            VARCHAR(64) NOT NULL,
		source            VARCHAR(255) NOT NULL DEFAULT 'local',
		metadata          JSON NULL,
		changed_at        TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
		INDEX idx_event_id_changed (event_id, changed_at),
		INDEX idx_blog_id_changed (blog_id, changed_at),
		INDEX idx_changed_at (changed_at)
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

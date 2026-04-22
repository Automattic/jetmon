-- Jetmon 2 schema migrations.
-- Applied automatically by `jetmon2 migrate` via internal/db/migrations.go.
-- This file is provided for reference and manual application if needed.

CREATE TABLE IF NOT EXISTS jetmon_schema_migrations (
    id           INT UNSIGNED NOT NULL PRIMARY KEY,
    applied_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- New columns on jetpack_monitor_sites (additive only).
ALTER TABLE jetpack_monitor_sites
    ADD COLUMN IF NOT EXISTS ssl_expiry_date        DATE NULL,
    ADD COLUMN IF NOT EXISTS check_keyword          VARCHAR(500) NULL,
    ADD COLUMN IF NOT EXISTS maintenance_start      DATETIME NULL,
    ADD COLUMN IF NOT EXISTS maintenance_end        DATETIME NULL,
    ADD COLUMN IF NOT EXISTS custom_headers         JSON NULL,
    ADD COLUMN IF NOT EXISTS timeout_seconds        TINYINT UNSIGNED NULL,
    ADD COLUMN IF NOT EXISTS redirect_policy        ENUM('follow','alert','fail') NULL DEFAULT 'follow',
    ADD COLUMN IF NOT EXISTS alert_cooldown_minutes SMALLINT UNSIGNED NULL,
    ADD COLUMN IF NOT EXISTS last_checked_at        DATETIME NULL,
    ADD COLUMN IF NOT EXISTS last_alert_sent_at     DATETIME NULL,
    ADD INDEX IF NOT EXISTS idx_bucket_monitor_last_checked (bucket_no, monitor_active, last_checked_at);

-- MySQL-coordinated bucket ownership.
CREATE TABLE IF NOT EXISTS jetmon_hosts (
    host_id        VARCHAR(255) NOT NULL PRIMARY KEY,
    bucket_min     SMALLINT UNSIGNED NOT NULL,
    bucket_max     SMALLINT UNSIGNED NOT NULL,
    last_heartbeat TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status         ENUM('active','draining') NOT NULL DEFAULT 'active'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Full event history per site.
CREATE TABLE IF NOT EXISTS jetmon_audit_log (
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- RTT and timing samples for trending.
CREATE TABLE IF NOT EXISTS jetmon_check_history (
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Veriflier non-confirmation events (false positives).
CREATE TABLE IF NOT EXISTS jetmon_false_positives (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id    BIGINT UNSIGNED NOT NULL,
    http_code  SMALLINT NULL,
    error_code TINYINT NULL,
    rtt_ms     INT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_blog_id (blog_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

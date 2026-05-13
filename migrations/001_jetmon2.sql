-- Jetmon 2 schema migrations.
-- Applied automatically by `jetmon2 migrate` via internal/db/migrations.go.
-- This file is provided for reference and manual application if needed.

CREATE TABLE IF NOT EXISTS jetmon_schema_migrations (
    id           INT UNSIGNED NOT NULL PRIMARY KEY,
    applied_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Jetmon v2 intentionally keeps jetpack_monitor_sites v1-shaped during
-- rollout. V2-only site config and runtime state live in side tables below;
-- the legacy table is still the source for monitor_url, bucket_no,
-- monitor_active, check_interval, site_status, and last_status_change.

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
    request_method VARCHAR(16) NOT NULL DEFAULT 'GET',
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

-- Per-site v2 check policy and rich probe config live outside the legacy site
-- table so rollout batches can switch HEAD/GET and detection profiles without
-- another ALTER on jetpack_monitor_sites. NULL values inherit process defaults
-- or built-in checker defaults.
CREATE TABLE IF NOT EXISTS jetmon_site_check_config (
    blog_id                BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    request_method         ENUM('HEAD','GET') NULL,
    detection_profile      ENUM('legacy','simple_http','full') NULL,
    check_keyword          VARCHAR(500) NULL,
    forbidden_keyword      VARCHAR(500) NULL,
    forbidden_keywords     JSON NULL,
    maintenance_start      DATETIME NULL,
    maintenance_end        DATETIME NULL,
    custom_headers         JSON NULL,
    timeout_seconds        TINYINT UNSIGNED NULL,
    redirect_policy        ENUM('follow','alert','fail') NULL DEFAULT NULL,
    alert_cooldown_minutes SMALLINT UNSIGNED NULL,
    created_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_request_method (request_method),
    INDEX idx_detection_profile (detection_profile)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- V2 runtime/projection state. These fields are useful for API display,
-- rollback freshness checks, and the legacy round scheduler, but they are not
-- part of the v1 compatibility table.
CREATE TABLE IF NOT EXISTS jetmon_site_runtime (
    blog_id            BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    last_checked_at    DATETIME NULL,
    next_check_at      DATETIME NULL,
    last_alert_sent_at DATETIME NULL,
    ssl_expiry_date    DATE NULL,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_next_check (next_check_at, blog_id),
    INDEX idx_last_checked (last_checked_at, blog_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

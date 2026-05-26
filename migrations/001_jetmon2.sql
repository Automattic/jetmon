-- Jetmon 2 historical schema reference.
--
-- The migration source of truth is internal/db/migrations.go. For production,
-- use migrations/production-v2-baseline.sql instead of this historical file.
-- Production validation checks required tables, columns, and indexes directly
-- and does not require jetpack_monitor_schema_migrations. The migration ledger
-- below is legacy local/lab bookkeeping and is scheduled for removal after the
-- schema reconciler fully replaces the old migrate path.

CREATE TABLE IF NOT EXISTS jetpack_monitor_schema_migrations (
    id           INT UNSIGNED NOT NULL PRIMARY KEY,
    applied_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Jetmon v2 intentionally keeps jetpack_monitor_sites v1-shaped during
-- rollout. V2-only site config and runtime state live in side tables below;
-- the legacy table is still the source for monitor_url, bucket_no,
-- monitor_active, check_interval, site_status, and last_status_change.

-- MySQL-coordinated bucket ownership.
CREATE TABLE IF NOT EXISTS jetpack_monitor_hosts (
    host_id        VARCHAR(255) NOT NULL PRIMARY KEY,
    bucket_min     SMALLINT UNSIGNED NOT NULL,
    bucket_max     SMALLINT UNSIGNED NOT NULL,
    last_heartbeat TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status         VARCHAR(16) NOT NULL DEFAULT 'active'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Full event history per site.
CREATE TABLE IF NOT EXISTS jetpack_monitor_audit_log (
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
    INDEX idx_blog_id_created (blog_id, created_at),
    INDEX idx_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- RTT and timing samples for trending.
CREATE TABLE IF NOT EXISTS jetpack_monitor_check_history (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id        BIGINT UNSIGNED NOT NULL,
    source_site_id BIGINT UNSIGNED NULL,
    request_method VARCHAR(16) NOT NULL DEFAULT 'GET',
    http_code      SMALLINT NULL,
    error_code     TINYINT NULL,
    rtt_ms         INT NULL,
    dns_ms         INT NULL,
    tcp_ms         INT NULL,
    tls_ms         INT NULL,
    ttfb_ms        INT NULL,
    checked_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_blog_id_checked (blog_id, checked_at),
    INDEX idx_source_site_checked (source_site_id, checked_at),
    INDEX idx_checked_at (checked_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_events (
    id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id           BIGINT UNSIGNED NOT NULL,
    endpoint_id       BIGINT UNSIGNED NULL,
    check_type        VARCHAR(64) NOT NULL,
    discriminator     VARCHAR(128) NULL,
    severity          TINYINT UNSIGNED NOT NULL,
    state             VARCHAR(32) NOT NULL,
    started_at        TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    ended_at          TIMESTAMP(3) NULL,
    resolution_reason VARCHAR(64) NULL,
    cause_event_id    BIGINT UNSIGNED NULL,
    metadata          JSON NULL,
    updated_at        TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    dedup_key         VARCHAR(255) GENERATED ALWAYS AS (
        IF(ended_at IS NULL,
           CONCAT_WS(':', blog_id, COALESCE(endpoint_id, 0), check_type, COALESCE(discriminator, '')),
           NULL)
    ) STORED,
    UNIQUE KEY uk_open_dedup (dedup_key),
    INDEX idx_blog_id_started (blog_id, started_at),
    INDEX idx_blog_id_active (blog_id, ended_at),
    INDEX idx_check_type_started (check_type, started_at),
    INDEX idx_cause_event_id (cause_event_id),
    INDEX idx_blog_id_check_type_active (blog_id, check_type, ended_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_event_transitions (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    event_id        BIGINT UNSIGNED NOT NULL,
    blog_id         BIGINT UNSIGNED NOT NULL,
    endpoint_id     BIGINT UNSIGNED NULL,
    severity_before TINYINT UNSIGNED NULL,
    severity_after  TINYINT UNSIGNED NULL,
    state_before    VARCHAR(32) NULL,
    state_after     VARCHAR(32) NULL,
    reason          VARCHAR(64) NOT NULL,
    source          VARCHAR(255) NOT NULL DEFAULT 'local',
    metadata        JSON NULL,
    changed_at      TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    INDEX idx_event_id_changed (event_id, changed_at),
    INDEX idx_blog_id_changed (blog_id, changed_at),
    INDEX idx_endpoint_id_changed (endpoint_id, changed_at),
    INDEX idx_changed_at (changed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Veriflier non-confirmation events (false positives).
CREATE TABLE IF NOT EXISTS jetpack_monitor_false_positives (
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
CREATE TABLE IF NOT EXISTS jetpack_monitor_site_check_config (
    source_site_id         BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    blog_id                BIGINT UNSIGNED NOT NULL,
    request_method         VARCHAR(16) NULL,
    detection_profile      VARCHAR(32) NULL,
    check_keyword          VARCHAR(500) NULL,
    forbidden_keyword      VARCHAR(500) NULL,
    forbidden_keywords     JSON NULL,
    maintenance_start      DATETIME NULL,
    maintenance_end        DATETIME NULL,
    custom_headers         JSON NULL,
    timeout_seconds        TINYINT UNSIGNED NULL,
    redirect_policy        VARCHAR(16) NULL DEFAULT NULL,
    alert_cooldown_minutes SMALLINT UNSIGNED NULL,
    check_history_mode     VARCHAR(32) NULL,
    check_history_sample_rate INT UNSIGNED NULL,
    created_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_blog_id (blog_id),
    INDEX idx_request_method (request_method),
    INDEX idx_detection_profile (detection_profile)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- V2 runtime/projection state. These fields are useful for API display,
-- rollback freshness checks, and the legacy round scheduler, but they are not
-- part of the v1 compatibility table.
CREATE TABLE IF NOT EXISTS jetpack_monitor_site_runtime (
    source_site_id     BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    blog_id            BIGINT UNSIGNED NOT NULL,
    last_checked_at    DATETIME NULL,
    next_check_at      DATETIME NULL,
    last_alert_sent_at DATETIME NULL,
    ssl_expiry_date    DATE NULL,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_blog_id (blog_id),
    INDEX idx_next_check (next_check_at, source_site_id),
    INDEX idx_last_checked (last_checked_at, source_site_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Durable, non-downtime safety findings for unsafe legacy monitor URLs and
-- runtime probe-safety blocks. These rows let operators remediate or ignore
-- unsafe targets without representing them as customer-site downtime events.
CREATE TABLE IF NOT EXISTS jetpack_monitor_site_safety_flags (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id         BIGINT UNSIGNED NOT NULL,
    monitor_site_id BIGINT UNSIGNED NOT NULL,
    flag_type       VARCHAR(32) NOT NULL,
    reason          VARCHAR(1024) NOT NULL,
    monitor_url     VARCHAR(2083) NOT NULL,
    status          VARCHAR(16) NOT NULL DEFAULT 'open',
    first_seen_at   TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    last_seen_at    TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    deactivated_at  TIMESTAMP(3) NULL,
    created_at      TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at      TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uniq_site_flag (monitor_site_id, flag_type),
    INDEX idx_blog_status (blog_id, status),
    INDEX idx_status_seen (status, last_seen_at),
    INDEX idx_type_status (flag_type, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Trusted Veriflier vantage registry for monitor-side discovery.
CREATE TABLE IF NOT EXISTS jetpack_monitor_veriflier_vantages (
    vantage_id    VARCHAR(128) NOT NULL PRIMARY KEY,
    region        VARCHAR(128) NOT NULL DEFAULT '',
    provider      VARCHAR(128) NOT NULL DEFAULT '',
    endpoint_host VARCHAR(255) NOT NULL DEFAULT '',
    endpoint_port VARCHAR(16) NOT NULL DEFAULT '',
    auth_token    VARCHAR(255) NOT NULL DEFAULT '',
    enabled       TINYINT UNSIGNED NOT NULL DEFAULT 0,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_enabled (enabled),
    INDEX idx_endpoint (endpoint_host, endpoint_port)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Concrete Veriflier process telemetry and capacity hints.
CREATE TABLE IF NOT EXISTS jetpack_monitor_veriflier_agents (
    agent_id        VARCHAR(128) NOT NULL PRIMARY KEY,
    vantage_id      VARCHAR(128) NOT NULL,
    hostname        VARCHAR(255) NOT NULL DEFAULT '',
    endpoint_host   VARCHAR(255) NOT NULL DEFAULT '',
    endpoint_port   VARCHAR(16) NOT NULL DEFAULT '',
    version         VARCHAR(64) NOT NULL DEFAULT '',
    protocols       JSON NULL,
    max_concurrency INT UNSIGNED NOT NULL DEFAULT 0,
    queue_capacity  INT UNSIGNED NOT NULL DEFAULT 0,
    queue_depth     INT UNSIGNED NOT NULL DEFAULT 0,
    active          INT UNSIGNED NOT NULL DEFAULT 0,
    in_flight       INT UNSIGNED NOT NULL DEFAULT 0,
    status          VARCHAR(16) NOT NULL DEFAULT 'active',
    last_seen       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_vantage_seen (vantage_id, last_seen),
    INDEX idx_status_seen (status, last_seen)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

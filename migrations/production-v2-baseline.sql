-- Jetmon 2 production baseline schema.
--
-- This file is intended for the reviewed production database-change process.
-- It creates the v2-owned side tables that Jetmon 2 needs while leaving the
-- legacy jetpack_monitor_sites table untouched.
--
-- Production validation uses information_schema to verify required tables,
-- columns, and indexes. It does not require jetpack_monitor_schema_migrations;
-- that table is only for local/lab environments that run ./jetmon2 migrate.
--
-- Existing legacy table expected by v2, but not created or altered here:
--   jetpack_monitor_sites
-- Required legacy columns:
--   jetpack_monitor_site_id, blog_id, bucket_no, monitor_url,
--   monitor_active, site_status, last_status_change, check_interval
-- Required legacy index shape:
--   any index whose first columns are (bucket_no, monitor_active)
--   The current v1 index bucket_no_monitor_active_check_interval satisfies it.

CREATE TABLE IF NOT EXISTS jetpack_monitor_hosts (
    host_id        VARCHAR(255) NOT NULL PRIMARY KEY,
    bucket_min     SMALLINT UNSIGNED NOT NULL,
    bucket_max     SMALLINT UNSIGNED NOT NULL,
    last_heartbeat TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status         VARCHAR(16) NOT NULL DEFAULT 'active'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_audit_log (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id    BIGINT UNSIGNED NULL,
    event_id   BIGINT UNSIGNED NULL,
    event_type VARCHAR(64) NOT NULL,
    source     VARCHAR(255) NOT NULL DEFAULT 'local',
    detail     VARCHAR(1024) NULL,
    metadata   JSON NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_blog_id_created (blog_id, created_at),
    INDEX idx_created_at (created_at),
    INDEX idx_event_id (event_id),
    INDEX idx_event_type_created (event_type, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

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

CREATE TABLE IF NOT EXISTS jetpack_monitor_false_positives (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id    BIGINT UNSIGNED NOT NULL,
    http_code  SMALLINT NULL,
    error_code TINYINT NULL,
    rtt_ms     INT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_blog_id (blog_id)
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

CREATE TABLE IF NOT EXISTS jetpack_monitor_api_keys (
    id                    BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    key_hash              CHAR(64) NOT NULL,
    consumer_name         VARCHAR(128) NOT NULL,
    scope                 VARCHAR(16) NOT NULL DEFAULT 'read',
    rate_limit_per_minute INT NOT NULL DEFAULT 60,
    expires_at            TIMESTAMP NULL,
    revoked_at            TIMESTAMP NULL,
    last_used_at          TIMESTAMP NULL,
    created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by            VARCHAR(128) NOT NULL DEFAULT 'cli',
    UNIQUE KEY uk_key_hash (key_hash),
    INDEX idx_consumer (consumer_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_webhooks (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    url             VARCHAR(2083) NOT NULL,
    active          TINYINT UNSIGNED NOT NULL DEFAULT 1,
    owner_tenant_id VARCHAR(128) NULL,
    events          JSON NULL,
    site_filter     JSON NULL,
    state_filter    JSON NULL,
    secret          VARCHAR(80) NOT NULL,
    secret_preview  VARCHAR(8) NOT NULL DEFAULT '',
    created_by      VARCHAR(128) NOT NULL DEFAULT '',
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_active (active),
    INDEX idx_owner_tenant_id (owner_tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_webhook_deliveries (
    id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    webhook_id       BIGINT UNSIGNED NOT NULL,
    transition_id    BIGINT UNSIGNED NOT NULL,
    event_id         BIGINT UNSIGNED NOT NULL,
    event_type       VARCHAR(64) NOT NULL,
    payload          JSON NOT NULL,
    status           VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempt          INT UNSIGNED NOT NULL DEFAULT 0,
    next_attempt_at  TIMESTAMP NULL,
    last_status_code INT NULL,
    last_response    VARCHAR(2048) NULL,
    last_attempt_at  TIMESTAMP NULL,
    delivered_at     TIMESTAMP NULL,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_webhook_transition (webhook_id, transition_id),
    INDEX idx_status_next_attempt (status, next_attempt_at),
    INDEX idx_webhook_id_created (webhook_id, created_at),
    INDEX idx_event_id (event_id),
    INDEX idx_status_delivered_at (status, delivered_at),
    INDEX idx_status_last_attempt_at (status, last_attempt_at),
    INDEX idx_status_created_at (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_webhook_dispatch_progress (
    instance_id        VARCHAR(255) NOT NULL PRIMARY KEY,
    last_transition_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_alert_contacts (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    label               VARCHAR(128) NOT NULL,
    active              TINYINT UNSIGNED NOT NULL DEFAULT 1,
    owner_tenant_id     VARCHAR(128) NULL,
    transport           VARCHAR(32) NOT NULL,
    destination         JSON NOT NULL,
    destination_preview VARCHAR(8) NOT NULL DEFAULT '',
    site_filter         JSON NULL,
    min_severity        TINYINT UNSIGNED NOT NULL DEFAULT 4,
    max_per_hour        INT UNSIGNED NOT NULL DEFAULT 60,
    created_by          VARCHAR(128) NOT NULL DEFAULT '',
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_active (active),
    INDEX idx_owner_tenant_id (owner_tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_alert_deliveries (
    id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    alert_contact_id BIGINT UNSIGNED NOT NULL,
    transition_id    BIGINT UNSIGNED NOT NULL,
    event_id         BIGINT UNSIGNED NOT NULL,
    event_type       VARCHAR(64) NOT NULL,
    severity         TINYINT UNSIGNED NOT NULL,
    payload          JSON NOT NULL,
    status           VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempt          INT UNSIGNED NOT NULL DEFAULT 0,
    next_attempt_at  TIMESTAMP NULL,
    last_status_code INT NULL,
    last_response    VARCHAR(2048) NULL,
    last_attempt_at  TIMESTAMP NULL,
    delivered_at     TIMESTAMP NULL,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_alert_transition (alert_contact_id, transition_id),
    INDEX idx_status_next_attempt (status, next_attempt_at),
    INDEX idx_contact_id_created (alert_contact_id, created_at),
    INDEX idx_event_id (event_id),
    INDEX idx_status_delivered_at (status, delivered_at),
    INDEX idx_status_last_attempt_at (status, last_attempt_at),
    INDEX idx_status_created_at (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_alert_dispatch_progress (
    instance_id        VARCHAR(255) NOT NULL PRIMARY KEY,
    last_transition_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
    updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_site_tenants (
    tenant_id  VARCHAR(128) NOT NULL,
    blog_id    BIGINT UNSIGNED NOT NULL,
    source     VARCHAR(64) NOT NULL DEFAULT 'gateway',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, blog_id),
    INDEX idx_blog_id (blog_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_process_health (
    process_id                  VARCHAR(255) NOT NULL PRIMARY KEY,
    host_id                     VARCHAR(255) NOT NULL,
    process_type                VARCHAR(64) NOT NULL,
    pid                         INT UNSIGNED NOT NULL DEFAULT 0,
    version                     VARCHAR(64) NOT NULL DEFAULT '',
    build_date                  VARCHAR(64) NOT NULL DEFAULT '',
    go_version                  VARCHAR(64) NOT NULL DEFAULT '',
    state                       VARCHAR(32) NOT NULL DEFAULT 'starting',
    health_status               VARCHAR(16) NOT NULL DEFAULT 'green',
    started_at                  TIMESTAMP NULL,
    updated_at                  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    bucket_min                  SMALLINT UNSIGNED NULL,
    bucket_max                  SMALLINT UNSIGNED NULL,
    bucket_ownership            VARCHAR(128) NOT NULL DEFAULT '',
    api_port                    INT UNSIGNED NULL,
    dashboard_port              INT UNSIGNED NULL,
    delivery_workers_enabled    TINYINT UNSIGNED NOT NULL DEFAULT 0,
    delivery_owner_host         VARCHAR(255) NOT NULL DEFAULT '',
    worker_count                INT UNSIGNED NOT NULL DEFAULT 0,
    active_checks               INT UNSIGNED NOT NULL DEFAULT 0,
    queue_depth                 INT UNSIGNED NOT NULL DEFAULT 0,
    retry_queue_size            INT UNSIGNED NOT NULL DEFAULT 0,
    wpcom_circuit_open          TINYINT UNSIGNED NOT NULL DEFAULT 0,
    wpcom_queue_depth           INT UNSIGNED NOT NULL DEFAULT 0,
    go_sys_mem_mb               INT UNSIGNED NOT NULL DEFAULT 0,
    rss_mem_mb                  INT UNSIGNED NOT NULL DEFAULT 0,
    runtime_goroutines          INT UNSIGNED NOT NULL DEFAULT 0,
    runtime_goroutines_runnable INT UNSIGNED NOT NULL DEFAULT 0,
    runtime_goroutines_running  INT UNSIGNED NOT NULL DEFAULT 0,
    runtime_goroutines_waiting  INT UNSIGNED NOT NULL DEFAULT 0,
    runtime_goroutines_not_in_go INT UNSIGNED NOT NULL DEFAULT 0,
    runtime_goroutines_created  BIGINT UNSIGNED NOT NULL DEFAULT 0,
    runtime_threads             INT UNSIGNED NOT NULL DEFAULT 0,
    dependency_health           JSON NULL,
    created_at                  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_process_type_updated (process_type, updated_at),
    INDEX idx_host_updated (host_id, updated_at),
    INDEX idx_state_updated (state, updated_at),
    INDEX idx_health_status_updated (health_status, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_check_targets (
    target_id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id             BIGINT UNSIGNED NOT NULL,
    source_site_id      BIGINT UNSIGNED NOT NULL,
    bucket_no           SMALLINT UNSIGNED NOT NULL,
    monitor_url         VARCHAR(2083) NOT NULL,
    monitor_active      TINYINT UNSIGNED NOT NULL DEFAULT 1,
    check_interval_sec  INT UNSIGNED NOT NULL DEFAULT 300,
    phase_slot_sec      INT UNSIGNED NOT NULL DEFAULT 0,
    config_hash         CHAR(64) NOT NULL DEFAULT '',
    last_config_sync_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    last_checked_at     TIMESTAMP(3) NULL,
    last_success_at     TIMESTAMP(3) NULL,
    last_failure_at     TIMESTAMP(3) NULL,
    last_outcome        VARCHAR(16) NOT NULL DEFAULT 'unknown',
    updated_at          TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_source_site_id (source_site_id),
    INDEX idx_bucket_phase (bucket_no, phase_slot_sec, source_site_id),
    INDEX idx_bucket_active (bucket_no, monitor_active, source_site_id),
    INDEX idx_blog_id (blog_id),
    INDEX idx_config_sync (last_config_sync_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_site_check_config (
    source_site_id            BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    blog_id                   BIGINT UNSIGNED NOT NULL,
    request_method            VARCHAR(16) NULL,
    detection_profile         VARCHAR(32) NULL,
    check_keyword             VARCHAR(500) NULL,
    forbidden_keyword         VARCHAR(500) NULL,
    forbidden_keywords        JSON NULL,
    maintenance_start         DATETIME NULL,
    maintenance_end           DATETIME NULL,
    custom_headers            JSON NULL,
    timeout_seconds           TINYINT UNSIGNED NULL,
    redirect_policy           VARCHAR(16) NULL DEFAULT NULL,
    alert_cooldown_minutes    SMALLINT UNSIGNED NULL,
    check_history_mode        VARCHAR(32) NULL,
    check_history_sample_rate INT UNSIGNED NULL,
    created_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_blog_id (blog_id),
    INDEX idx_request_method (request_method),
    INDEX idx_detection_profile (detection_profile)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

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

CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_sessions (
    run_id     VARCHAR(64) NOT NULL PRIMARY KEY,
    bucket_min SMALLINT UNSIGNED NOT NULL,
    bucket_max SMALLINT UNSIGNED NOT NULL,
    owner_host VARCHAR(255) NOT NULL DEFAULT '',
    change_ref VARCHAR(255) NOT NULL DEFAULT '',
    operator   VARCHAR(128) NOT NULL DEFAULT '',
    status     VARCHAR(16) NOT NULL DEFAULT 'open',
    metadata   JSON NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_status_created (status, created_at),
    INDEX idx_range (bucket_min, bucket_max)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_range_locks (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    run_id       VARCHAR(64) NOT NULL,
    bucket_min   SMALLINT UNSIGNED NOT NULL,
    bucket_max   SMALLINT UNSIGNED NOT NULL,
    owner_host   VARCHAR(255) NOT NULL,
    change_ref   VARCHAR(255) NOT NULL DEFAULT '',
    status       VARCHAR(16) NOT NULL DEFAULT 'active',
    activated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    released_at  TIMESTAMP NULL,
    metadata     JSON NULL,
    INDEX idx_status_range (status, bucket_min, bucket_max),
    INDEX idx_owner_status (owner_host, status),
    INDEX idx_run_id (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_bucket_locks (
    bucket_no     SMALLINT UNSIGNED NOT NULL PRIMARY KEY,
    run_id        VARCHAR(64) NOT NULL,
    range_lock_id BIGINT UNSIGNED NOT NULL,
    owner_host    VARCHAR(255) NOT NULL,
    status        VARCHAR(16) NOT NULL DEFAULT 'active',
    activated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_owner (owner_host),
    INDEX idx_run_id (run_id),
    INDEX idx_range_lock (range_lock_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_jobs (
    job_id        VARCHAR(64) NOT NULL PRIMARY KEY,
    run_id        VARCHAR(64) NOT NULL DEFAULT '',
    operation     VARCHAR(64) NOT NULL,
    status        VARCHAR(16) NOT NULL DEFAULT 'completed',
    progress      TINYINT UNSIGNED NOT NULL DEFAULT 100,
    summary       VARCHAR(1024) NOT NULL DEFAULT '',
    result        JSON NULL,
    error_code    VARCHAR(64) NOT NULL DEFAULT '',
    error_message VARCHAR(1024) NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_run_operation (run_id, operation),
    INDEX idx_status_created (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_confirmation_tokens (
    token_hash   CHAR(64) NOT NULL PRIMARY KEY,
    run_id       VARCHAR(64) NOT NULL DEFAULT '',
    operation    VARCHAR(64) NOT NULL,
    request_hash CHAR(64) NOT NULL,
    bucket_min   SMALLINT UNSIGNED NOT NULL,
    bucket_max   SMALLINT UNSIGNED NOT NULL,
    operator     VARCHAR(128) NOT NULL DEFAULT '',
    expires_at   TIMESTAMP NOT NULL,
    used_at      TIMESTAMP NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_run_operation (run_id, operation),
    INDEX idx_expires (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_comparison_results (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    job_id          VARCHAR(64) NOT NULL,
    run_id          VARCHAR(64) NOT NULL DEFAULT '',
    blog_id         BIGINT UNSIGNED NOT NULL,
    source_site_id  BIGINT UNSIGNED NOT NULL,
    bucket_no       SMALLINT UNSIGNED NOT NULL,
    monitor_url     VARCHAR(2083) NOT NULL,
    from_method     VARCHAR(16) NOT NULL,
    from_profile    VARCHAR(32) NOT NULL,
    to_method       VARCHAR(16) NOT NULL,
    to_profile      VARCHAR(32) NOT NULL,
    from_success    TINYINT(1) NOT NULL,
    to_success      TINYINT(1) NOT NULL,
    from_http_code  INT NOT NULL DEFAULT 0,
    to_http_code    INT NOT NULL DEFAULT 0,
    from_error_code INT NOT NULL DEFAULT 0,
    to_error_code   INT NOT NULL DEFAULT 0,
    from_rtt_ms     INT NOT NULL DEFAULT 0,
    to_rtt_ms       INT NOT NULL DEFAULT 0,
    delta_class     VARCHAR(32) NOT NULL DEFAULT 'same',
    created_at      TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    INDEX idx_job (job_id),
    INDEX idx_run_created (run_id, created_at),
    INDEX idx_delta_created (delta_class, created_at),
    INDEX idx_site (blog_id, source_site_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_policy_stage_rows (
    id                         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    job_id                     VARCHAR(64) NOT NULL,
    run_id                     VARCHAR(64) NOT NULL DEFAULT '',
    blog_id                    BIGINT UNSIGNED NOT NULL,
    source_site_id             BIGINT UNSIGNED NOT NULL,
    bucket_no                  SMALLINT UNSIGNED NOT NULL,
    previous_request_method    VARCHAR(16) NULL,
    previous_detection_profile VARCHAR(32) NULL,
    new_request_method         VARCHAR(16) NOT NULL,
    new_detection_profile      VARCHAR(32) NOT NULL,
    rolled_back_at             TIMESTAMP(3) NULL,
    created_at                 TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    INDEX idx_job (job_id),
    INDEX idx_run_created (run_id, created_at),
    INDEX idx_rollback (run_id, rolled_back_at, created_at),
    INDEX idx_blog (blog_id),
    INDEX idx_source_site (source_site_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

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

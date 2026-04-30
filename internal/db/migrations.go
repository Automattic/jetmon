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

	// Migration 12 creates the API key registry. Keys are sha256-hashed at rest;
	// the raw token is shown only once at creation time via the CLI. Per-key rate
	// limit, scope, expiry, and revocation are all stored here. consumer_name is
	// the audit-log key — every authenticated API request logs against it so we
	// can track and revoke specific internal systems. See docs/internal-api-reference.md "Authentication".
	{12, `CREATE TABLE IF NOT EXISTS jetmon_api_keys (
		id                    BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		key_hash              CHAR(64) NOT NULL,
		consumer_name         VARCHAR(128) NOT NULL,
		scope                 ENUM('read','write','admin') NOT NULL DEFAULT 'read',
		rate_limit_per_minute INT NOT NULL DEFAULT 60,
		expires_at            TIMESTAMP NULL,
		revoked_at            TIMESTAMP NULL,
		last_used_at          TIMESTAMP NULL,
		created_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		created_by            VARCHAR(128) NOT NULL DEFAULT 'cli',
		UNIQUE KEY uk_key_hash (key_hash),
		INDEX idx_consumer (consumer_name)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 13 creates the webhook registry. secret_hash is sha256 of the
	// raw secret (which is shown once at creation, mirrors jetmon_api_keys).
	// events / site_filter / state_filter are JSON to allow flexible filter
	// shapes without per-filter columns; semantics: empty = match all, AND
	// across dimensions, whitelist within each. See docs/internal-api-reference.md "Family 4".
	// secret stores the raw HMAC signing key in plaintext. Unlike
	// jetmon_api_keys (sha256-hashed at rest, used for inbound auth where
	// hash is sufficient), webhook secrets are used to SIGN outbound
	// deliveries — HMAC needs the actual key material in memory, not its
	// hash. We never verify inbound signatures with this secret, so
	// hash-at-rest would buy us no verification benefit while making
	// signing impossible.
	//
	// Threat model: anyone with read access to jetmon_webhooks can mint
	// valid deliveries. For the internal API behind a gateway, that's
	// equivalent to the existing access-to-events threat. Encryption at
	// rest with a master key (KMS-style) is in docs/roadmap.md as a future
	// hardening step.
	{13, `CREATE TABLE IF NOT EXISTS jetmon_webhooks (
		id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		url             VARCHAR(2083) NOT NULL,
		active          TINYINT UNSIGNED NOT NULL DEFAULT 1,
		events          JSON NULL,
		site_filter     JSON NULL,
		state_filter    JSON NULL,
		secret          VARCHAR(80) NOT NULL,
		secret_preview  VARCHAR(8) NOT NULL DEFAULT '',
		created_by      VARCHAR(128) NOT NULL DEFAULT '',
		created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		INDEX idx_active (active)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 14 creates the per-fire delivery records. One row per
	// (webhook, transition) match — transition_id is the fan-in point: a
	// single jetmon_event_transitions row can produce many deliveries (one
	// per matching webhook), but a webhook gets at most one delivery per
	// transition (enforced by uk_webhook_transition).
	//
	// payload is frozen at row creation: consumer sees the event as it was
	// when the webhook fired, not as it is now (closed-and-amended events
	// don't retroactively change delivery contents — that's the contract).
	//
	// status lifecycle: pending → (delivered | abandoned). "failed" is reserved
	// for permanent client/server errors that we wouldn't retry (currently
	// unused; pending captures the in-retry case).
	{14, `CREATE TABLE IF NOT EXISTS jetmon_webhook_deliveries (
		id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		webhook_id       BIGINT UNSIGNED NOT NULL,
		transition_id    BIGINT UNSIGNED NOT NULL,
		event_id         BIGINT UNSIGNED NOT NULL,
		event_type       VARCHAR(64) NOT NULL,
		payload          JSON NOT NULL,
		status           ENUM('pending','delivered','failed','abandoned') NOT NULL DEFAULT 'pending',
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
		INDEX idx_event_id (event_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 15 records the webhook dispatcher's progress. One row per
	// jetmon2 instance keeps last_transition_id high-water mark so the
	// dispatcher polls only new transitions. The UNIQUE KEY on instance_id
	// makes upsert (INSERT … ON DUPLICATE KEY UPDATE) trivial.
	{15, `CREATE TABLE IF NOT EXISTS jetmon_webhook_dispatch_progress (
		instance_id          VARCHAR(255) NOT NULL PRIMARY KEY,
		last_transition_id   BIGINT UNSIGNED NOT NULL DEFAULT 0,
		updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 16 creates the alert contacts registry. Same shape as the
	// webhook registry but with a simpler filter model (site_filter +
	// min_severity, no event-type / state filter — see docs/internal-api-reference.md Family 5).
	//
	// destination is JSON because each transport has a different shape:
	//   email     → {"address":"ops@example.com"}
	//   pagerduty → {"integration_key":"<events-v2 routing key>"}
	//   slack     → {"webhook_url":"https://hooks.slack.com/..."}
	//   teams     → {"webhook_url":"https://outlook.office.com/webhook/..."}
	// destination stores the credential in plaintext for the same reason
	// jetmon_webhooks.secret does (see migration 13): outbound dispatch
	// needs the raw value at every send. A hash is useless because we'd
	// have to recover the original to call the transport. Threat model and
	// future encryption-at-rest plan are identical.
	//
	// min_severity is a TINYINT matching internal/eventstore.Severity*
	// (0=Up, 1=Warning, 2=Degraded, 3=SeemsDown, 4=Down). Default 4 (Down)
	// avoids accidental noise from new contacts. The API serializes by
	// string name; the column stores the underlying uint8.
	//
	// max_per_hour caps notification rate per contact (default 60, 0 =
	// unlimited). Per-contact because different destinations have
	// different tolerance — a Slack channel can take far more than a
	// PagerDuty oncall can.
	{16, `CREATE TABLE IF NOT EXISTS jetmon_alert_contacts (
		id                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		label                VARCHAR(128) NOT NULL,
		active               TINYINT UNSIGNED NOT NULL DEFAULT 1,
		transport            ENUM('email','pagerduty','slack','teams') NOT NULL,
		destination          JSON NOT NULL,
		destination_preview  VARCHAR(8) NOT NULL DEFAULT '',
		site_filter          JSON NULL,
		min_severity         TINYINT UNSIGNED NOT NULL DEFAULT 4,
		max_per_hour         INT UNSIGNED NOT NULL DEFAULT 60,
		created_by           VARCHAR(128) NOT NULL DEFAULT '',
		created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		INDEX idx_active (active)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 17 creates the per-fire alert delivery records. One row per
	// (alert_contact, transition) match — same fan-in shape as
	// jetmon_webhook_deliveries: one transition produces many deliveries
	// (one per matching contact), one contact gets at most one delivery
	// per transition (enforced by uk_alert_transition).
	//
	// payload is frozen at row creation: contact sees the event as it was
	// when the alert fired, not as it is now.
	//
	// status lifecycle and 'failed' semantics are identical to
	// jetmon_webhook_deliveries.
	{17, `CREATE TABLE IF NOT EXISTS jetmon_alert_deliveries (
		id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		alert_contact_id  BIGINT UNSIGNED NOT NULL,
		transition_id     BIGINT UNSIGNED NOT NULL,
		event_id          BIGINT UNSIGNED NOT NULL,
		event_type        VARCHAR(64) NOT NULL,
		severity          TINYINT UNSIGNED NOT NULL,
		payload           JSON NOT NULL,
		status            ENUM('pending','delivered','failed','abandoned') NOT NULL DEFAULT 'pending',
		attempt           INT UNSIGNED NOT NULL DEFAULT 0,
		next_attempt_at   TIMESTAMP NULL,
		last_status_code  INT NULL,
		last_response     VARCHAR(2048) NULL,
		last_attempt_at   TIMESTAMP NULL,
		delivered_at      TIMESTAMP NULL,
		created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE KEY uk_alert_transition (alert_contact_id, transition_id),
		INDEX idx_status_next_attempt (status, next_attempt_at),
		INDEX idx_contact_id_created (alert_contact_id, created_at),
		INDEX idx_event_id (event_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 18 records the alert dispatcher's progress. Mirrors
	// jetmon_webhook_dispatch_progress — one row per jetmon2 instance with
	// the high-water mark for jetmon_event_transitions.id.
	{18, `CREATE TABLE IF NOT EXISTS jetmon_alert_dispatch_progress (
		instance_id          VARCHAR(255) NOT NULL PRIMARY KEY,
		last_transition_id   BIGINT UNSIGNED NOT NULL DEFAULT 0,
		updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 19 adds a nullable tenant owner to webhooks. Internal v2
	// callers leave it NULL, preserving the shared internal registry from
	// ADR-0002. Gateway-routed API paths set owner_tenant_id and use
	// tenant-scoped repository helpers so customer-owned webhooks are filtered
	// in Jetmon as defense in depth.
	{19, `ALTER TABLE jetmon_webhooks
		ADD COLUMN owner_tenant_id VARCHAR(128) NULL AFTER active,
		ADD INDEX idx_owner_tenant_id (owner_tenant_id)`},

	// Migration 20 mirrors webhook ownership on alert contacts. Deliveries
	// derive visibility through their parent contact; this column owns the
	// customer-managed registration itself.
	{20, `ALTER TABLE jetmon_alert_contacts
		ADD COLUMN owner_tenant_id VARCHAR(128) NULL AFTER active,
		ADD INDEX idx_owner_tenant_id (owner_tenant_id)`},

	// Migration 21 adds a many-to-many tenant mapping for sites. Sites are
	// still stored in the legacy jetpack_monitor_sites table; this mapping is
	// the public/gateway ownership projection Jetmon can enforce without
	// changing the drop-in v1-compatible site row. A site can appear under
	// multiple tenants if the gateway's product model allows shared ownership
	// or delegation.
	{21, `CREATE TABLE IF NOT EXISTS jetmon_site_tenants (
		tenant_id  VARCHAR(128) NOT NULL,
		blog_id    BIGINT UNSIGNED NOT NULL,
		source     VARCHAR(64) NOT NULL DEFAULT 'gateway',
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		PRIMARY KEY (tenant_id, blog_id),
		INDEX idx_blog_id (blog_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 22 adds delivery-check support indexes for webhook deliveries.
	// idx_status_next_attempt already covers ready/future pending rows; these
	// indexes keep recent terminal outcome counts and queue-age checks from
	// scanning historical delivery rows as the audit trail grows.
	{22, `ALTER TABLE jetmon_webhook_deliveries
		ADD INDEX idx_status_delivered_at (status, delivered_at),
		ADD INDEX idx_status_last_attempt_at (status, last_attempt_at),
		ADD INDEX idx_status_created_at (status, created_at)`},

	// Migration 23 mirrors delivery-check support indexes for alert-contact
	// deliveries.
	{23, `ALTER TABLE jetmon_alert_deliveries
		ADD INDEX idx_status_delivered_at (status, delivered_at),
		ADD INDEX idx_status_last_attempt_at (status, last_attempt_at),
		ADD INDEX idx_status_created_at (status, created_at)`},

	// Migration 24 creates the durable process heartbeat table used as the
	// foundation for fleet-wide operator dashboards. Each long-running Jetmon
	// process owns one process_id and periodically upserts a compact snapshot of
	// its local state. Fleet views should treat stale updated_at values as
	// unknown/unhealthy rather than assuming the last state is still current.
	{24, `CREATE TABLE IF NOT EXISTS jetmon_process_health (
		process_id               VARCHAR(255) NOT NULL PRIMARY KEY,
		host_id                  VARCHAR(255) NOT NULL,
		process_type             VARCHAR(64) NOT NULL,
		pid                      INT UNSIGNED NOT NULL DEFAULT 0,
		version                  VARCHAR(64) NOT NULL DEFAULT '',
		build_date               VARCHAR(64) NOT NULL DEFAULT '',
		go_version               VARCHAR(64) NOT NULL DEFAULT '',
		state                    VARCHAR(32) NOT NULL DEFAULT 'starting',
		started_at               TIMESTAMP NULL,
		updated_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		bucket_min               SMALLINT UNSIGNED NULL,
		bucket_max               SMALLINT UNSIGNED NULL,
		bucket_ownership         VARCHAR(128) NOT NULL DEFAULT '',
		api_port                 INT UNSIGNED NULL,
		dashboard_port           INT UNSIGNED NULL,
		delivery_workers_enabled TINYINT UNSIGNED NOT NULL DEFAULT 0,
		delivery_owner_host      VARCHAR(255) NOT NULL DEFAULT '',
		worker_count             INT UNSIGNED NOT NULL DEFAULT 0,
		active_checks            INT UNSIGNED NOT NULL DEFAULT 0,
		queue_depth              INT UNSIGNED NOT NULL DEFAULT 0,
		retry_queue_size         INT UNSIGNED NOT NULL DEFAULT 0,
		wpcom_circuit_open       TINYINT UNSIGNED NOT NULL DEFAULT 0,
		wpcom_queue_depth        INT UNSIGNED NOT NULL DEFAULT 0,
		mem_rss_mb               INT UNSIGNED NOT NULL DEFAULT 0,
		dependency_health        JSON NULL,
		created_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_process_type_updated (process_type, updated_at),
		INDEX idx_host_updated (host_id, updated_at),
		INDEX idx_state_updated (state, updated_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 25 splits process lifecycle from health rollup and renames
	// the memory column to the metric it actually stores. The value comes from
	// runtime.MemStats.Sys, not operating-system RSS.
	{25, `ALTER TABLE jetmon_process_health
		ADD COLUMN health_status VARCHAR(16) NOT NULL DEFAULT 'green' AFTER state,
		CHANGE COLUMN mem_rss_mb go_sys_mem_mb INT UNSIGNED NOT NULL DEFAULT 0,
		ADD INDEX idx_health_status_updated (health_status, updated_at)`},
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

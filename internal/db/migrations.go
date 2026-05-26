package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

const (
	migrationLockName           = "jetmon2_schema_migrations"
	migrationLockTimeoutSeconds = 300
)

// migration holds a single idempotent schema change.
type migration struct {
	id  int
	sql string
}

// MigrationStatus summarizes which embedded migrations the connected database
// reports as applied. It is intentionally read-only so production containers
// can validate an externally applied schema without attempting any DDL.
type MigrationStatus struct {
	ExpectedCount int
	ExpectedMaxID int
	AppliedCount  int
	CurrentMaxID  int
	PendingIDs    []int
	UnknownIDs    []int
}

var migrations = []migration{
	{1, `CREATE TABLE IF NOT EXISTS jetpack_monitor_schema_migrations (
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

	// Migration 3 previously added v2-only config columns to
	// jetpack_monitor_sites. That hot table is now kept v1-shaped for rollout;
	// v2-only config lives in jetpack_monitor_site_check_config.
	{3, `SELECT 1`},

	{4, `CREATE TABLE IF NOT EXISTS jetpack_monitor_hosts (
		host_id        VARCHAR(255) NOT NULL PRIMARY KEY,
		bucket_min     SMALLINT UNSIGNED NOT NULL,
		bucket_max     SMALLINT UNSIGNED NOT NULL,
		last_heartbeat TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		status         VARCHAR(16) NOT NULL DEFAULT 'active'
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{5, `CREATE TABLE IF NOT EXISTS jetpack_monitor_audit_log (
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
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{6, `CREATE TABLE IF NOT EXISTS jetpack_monitor_check_history (
		id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		blog_id        BIGINT UNSIGNED NOT NULL,
		source_site_id BIGINT UNSIGNED NULL,
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
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	{7, `CREATE TABLE IF NOT EXISTS jetpack_monitor_false_positives (
		id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		blog_id    BIGINT UNSIGNED NOT NULL,
		http_code  SMALLINT NULL,
		error_code TINYINT NULL,
		rtt_ms     INT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_blog_id (blog_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 8 previously added v2 runtime/freshness fields to
	// jetpack_monitor_sites. Runtime state now lives in jetpack_monitor_site_runtime.
	{8, `SELECT 1`},

	// Migration 9 retires jetpack_monitor_audit_log's site-state columns. Per-probe data lives in
	// jetpack_monitor_check_history; status transitions move to jetpack_monitor_event_transitions (migration 11).
	// What remains is purely operational: WPCOM, retries, verifier RPC, suppression, config.
	{9, `ALTER TABLE jetpack_monitor_audit_log
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
	{10, `CREATE TABLE IF NOT EXISTS jetpack_monitor_events (
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

	// Migration 11 creates the append-only history of every mutation to jetpack_monitor_events.
	// One row per change; never updated, never deleted. Together with jetpack_monitor_events,
	// this is the full event-sourced record. blog_id is denormalized to keep SLA queries
	// off the events table.
	{11, `CREATE TABLE IF NOT EXISTS jetpack_monitor_event_transitions (
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
	{12, `CREATE TABLE IF NOT EXISTS jetpack_monitor_api_keys (
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
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 13 creates the webhook registry. secret_hash is sha256 of the
	// raw secret (which is shown once at creation, mirrors jetpack_monitor_api_keys).
	// events / site_filter / state_filter are JSON to allow flexible filter
	// shapes without per-filter columns; semantics: empty = match all, AND
	// across dimensions, whitelist within each. See docs/internal-api-reference.md "Family 4".
	// secret stores the raw HMAC signing key in plaintext. Unlike
	// jetpack_monitor_api_keys (sha256-hashed at rest, used for inbound auth where
	// hash is sufficient), webhook secrets are used to SIGN outbound
	// deliveries — HMAC needs the actual key material in memory, not its
	// hash. We never verify inbound signatures with this secret, so
	// hash-at-rest would buy us no verification benefit while making
	// signing impossible.
	//
	// Threat model: anyone with read access to jetpack_monitor_webhooks can mint
	// valid deliveries. For the internal API behind a gateway, that's
	// equivalent to the existing access-to-events threat. Encryption at
	// rest with a master key (KMS-style) is in docs/roadmap.md as a future
	// hardening step.
	{13, `CREATE TABLE IF NOT EXISTS jetpack_monitor_webhooks (
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
	// single jetpack_monitor_event_transitions row can produce many deliveries (one
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
	{14, `CREATE TABLE IF NOT EXISTS jetpack_monitor_webhook_deliveries (
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
		INDEX idx_event_id (event_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 15 records the webhook dispatcher's progress. One row per
	// jetmon2 instance keeps last_transition_id high-water mark so the
	// dispatcher polls only new transitions. The UNIQUE KEY on instance_id
	// makes upsert (INSERT … ON DUPLICATE KEY UPDATE) trivial.
	{15, `CREATE TABLE IF NOT EXISTS jetpack_monitor_webhook_dispatch_progress (
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
	// jetpack_monitor_webhooks.secret does (see migration 13): outbound dispatch
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
	{16, `CREATE TABLE IF NOT EXISTS jetpack_monitor_alert_contacts (
		id                   BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		label                VARCHAR(128) NOT NULL,
		active               TINYINT UNSIGNED NOT NULL DEFAULT 1,
		transport            VARCHAR(32) NOT NULL,
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
	// jetpack_monitor_webhook_deliveries: one transition produces many deliveries
	// (one per matching contact), one contact gets at most one delivery
	// per transition (enforced by uk_alert_transition).
	//
	// payload is frozen at row creation: contact sees the event as it was
	// when the alert fired, not as it is now.
	//
	// status lifecycle and 'failed' semantics are identical to
	// jetpack_monitor_webhook_deliveries.
	{17, `CREATE TABLE IF NOT EXISTS jetpack_monitor_alert_deliveries (
		id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		alert_contact_id  BIGINT UNSIGNED NOT NULL,
		transition_id     BIGINT UNSIGNED NOT NULL,
		event_id          BIGINT UNSIGNED NOT NULL,
		event_type        VARCHAR(64) NOT NULL,
		severity          TINYINT UNSIGNED NOT NULL,
		payload           JSON NOT NULL,
		status            VARCHAR(16) NOT NULL DEFAULT 'pending',
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
	// jetpack_monitor_webhook_dispatch_progress — one row per jetmon2 instance with
	// the high-water mark for jetpack_monitor_event_transitions.id.
	{18, `CREATE TABLE IF NOT EXISTS jetpack_monitor_alert_dispatch_progress (
		instance_id          VARCHAR(255) NOT NULL PRIMARY KEY,
		last_transition_id   BIGINT UNSIGNED NOT NULL DEFAULT 0,
		updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 19 adds a nullable tenant owner to webhooks. Internal v2
	// callers leave it NULL, preserving the shared internal registry from
	// ADR-0002. Gateway-routed API paths set owner_tenant_id and use
	// tenant-scoped repository helpers so customer-owned webhooks are filtered
	// in Jetmon as defense in depth.
	{19, `ALTER TABLE jetpack_monitor_webhooks
		ADD COLUMN owner_tenant_id VARCHAR(128) NULL AFTER active,
		ADD INDEX idx_owner_tenant_id (owner_tenant_id)`},

	// Migration 20 mirrors webhook ownership on alert contacts. Deliveries
	// derive visibility through their parent contact; this column owns the
	// customer-managed registration itself.
	{20, `ALTER TABLE jetpack_monitor_alert_contacts
		ADD COLUMN owner_tenant_id VARCHAR(128) NULL AFTER active,
		ADD INDEX idx_owner_tenant_id (owner_tenant_id)`},

	// Migration 21 adds a many-to-many tenant mapping for sites. Sites are
	// still stored in the legacy jetpack_monitor_sites table; this mapping is
	// the public/gateway ownership projection Jetmon can enforce without
	// changing the drop-in v1-compatible site row. A site can appear under
	// multiple tenants if the gateway's product model allows shared ownership
	// or delegation.
	{21, `CREATE TABLE IF NOT EXISTS jetpack_monitor_site_tenants (
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
	{22, `ALTER TABLE jetpack_monitor_webhook_deliveries
		ADD INDEX idx_status_delivered_at (status, delivered_at),
		ADD INDEX idx_status_last_attempt_at (status, last_attempt_at),
		ADD INDEX idx_status_created_at (status, created_at)`},

	// Migration 23 mirrors delivery-check support indexes for alert-contact
	// deliveries.
	{23, `ALTER TABLE jetpack_monitor_alert_deliveries
		ADD INDEX idx_status_delivered_at (status, delivered_at),
		ADD INDEX idx_status_last_attempt_at (status, last_attempt_at),
		ADD INDEX idx_status_created_at (status, created_at)`},

	// Migration 24 creates the durable process heartbeat table used as the
	// foundation for fleet-wide operator dashboards. Each long-running Jetmon
	// process owns one process_id and periodically upserts a compact snapshot of
	// its local state. Fleet views should treat stale updated_at values as
	// unknown/unhealthy rather than assuming the last state is still current.
	{24, `CREATE TABLE IF NOT EXISTS jetpack_monitor_process_health (
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
	{25, `ALTER TABLE jetpack_monitor_process_health
		ADD COLUMN health_status VARCHAR(16) NOT NULL DEFAULT 'green' AFTER state,
		CHANGE COLUMN mem_rss_mb go_sys_mem_mb INT UNSIGNED NOT NULL DEFAULT 0,
		ADD INDEX idx_health_status_updated (health_status, updated_at)`},

	// Migration 26 adds true operating-system RSS beside Go runtime system
	// memory so dashboards can show both the host-observed resident set and the
	// runtime allocator footprint.
	{26, `ALTER TABLE jetpack_monitor_process_health
		ADD COLUMN rss_mem_mb INT UNSIGNED NOT NULL DEFAULT 0 AFTER go_sys_mem_mb`},

	// Migration 27 previously added a scheduler index on jetpack_monitor_sites.
	// The legacy table is no longer altered for v2 scheduler internals.
	{27, `SELECT 1`},

	// Migration 28 previously added next_check_at to jetpack_monitor_sites.
	// Due-time projection now lives in jetpack_monitor_site_runtime.
	{28, `SELECT 1`},

	// Migration 29 previously backfilled jetpack_monitor_sites.next_check_at.
	// No backfill is needed now because the sidecar runtime table is populated
	// lazily as checks complete.
	{29, `SELECT 1`},

	// Migration 30 previously added a next_check_at scheduler index to the
	// legacy table. Runtime due queries now use jetpack_monitor_site_runtime.
	{30, `SELECT 1`},

	// Migration 31 adds an explicit forbidden-content check alongside the
	// existing required keyword. The two columns intentionally stay separate:
	// check_keyword means "must be present"; forbidden_keyword means "must be
	// absent".
	// Migration 31 previously added forbidden_keyword to jetpack_monitor_sites.
	// V2 body-check config now lives in jetpack_monitor_site_check_config.
	{31, `SELECT 1`},

	// Migration 32 records the actual HTTP method used for each timing sample.
	// This keeps the high-volume check history compact while giving operators
	// durable evidence that v2 probes are exercising the GET path rather than
	// the HEAD-only behavior that caused v1 false positives and false negatives.
	{32, `ALTER TABLE jetpack_monitor_check_history
		ADD COLUMN request_method VARCHAR(16) NOT NULL DEFAULT 'GET' AFTER source_site_id`},

	// Migration 33 adds an array form for explicit forbidden body-content
	// checks. forbidden_keyword remains for compatibility and simple one-off
	// rules; forbidden_keywords lets operators provision multiple known-bad
	// strings without overloading one column.
	// Migration 33 previously added forbidden_keywords to jetpack_monitor_sites.
	// V2 body-check config now lives in jetpack_monitor_site_check_config.
	{33, `SELECT 1`},

	// Migration 34 creates the v2-native scheduling target table. The legacy
	// jetpack_monitor_sites row remains the source of truth during migration,
	// but this table gives the streaming scheduler a compact place to persist
	// derived scheduling state without turning the legacy table into the hot
	// write path again.
	{34, `CREATE TABLE IF NOT EXISTS jetpack_monitor_check_targets (
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
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 35 supports streaming scheduler reloads. The scheduler pages
	// active rows by blog_id inside its bucket range; this keeps periodic config
	// refreshes from depending on the older last_checked_at/next_check_at
	// indexes.
	// Migration 35 previously added a streaming reload index to the legacy
	// table. The streaming engine can use the existing v1 bucket/active shape
	// during rollout without requiring another hot ALTER.
	{35, `SELECT 1`},

	// Migration 36 stores v2 rollout check policy outside the legacy
	// jetpack_monitor_sites table. NULL means "inherit the process default",
	// letting operators migrate in phases without another hot ALTER on the
	// largest v1 compatibility table.
	{36, `CREATE TABLE IF NOT EXISTS jetpack_monitor_site_check_config (
		source_site_id    BIGINT UNSIGNED NOT NULL PRIMARY KEY,
		blog_id           BIGINT UNSIGNED NOT NULL,
		request_method    VARCHAR(16) NULL,
		detection_profile VARCHAR(32) NULL,
		created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		INDEX idx_blog_id (blog_id),
		INDEX idx_request_method (request_method),
		INDEX idx_detection_profile (detection_profile)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 37 stores v2 runtime/projection fields outside the legacy site
	// table. These values are useful for API display and rollback freshness
	// checks, but they do not need to change the v1 table shape.
	{37, `CREATE TABLE IF NOT EXISTS jetpack_monitor_site_runtime (
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
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 38 extends the Jetmon-owned check config table with every
	// v2-only per-site setting that previously lived on jetpack_monitor_sites.
	// Keeping this as a separate migration lets databases that already applied
	// migration 36 receive the expanded sidecar shape.
	{38, `ALTER TABLE jetpack_monitor_site_check_config
		ADD COLUMN check_keyword          VARCHAR(500) NULL AFTER detection_profile,
		ADD COLUMN forbidden_keyword      VARCHAR(500) NULL AFTER check_keyword,
		ADD COLUMN forbidden_keywords     JSON NULL AFTER forbidden_keyword,
		ADD COLUMN maintenance_start      DATETIME NULL AFTER forbidden_keywords,
		ADD COLUMN maintenance_end        DATETIME NULL AFTER maintenance_start,
		ADD COLUMN custom_headers         JSON NULL AFTER maintenance_end,
		ADD COLUMN timeout_seconds        TINYINT UNSIGNED NULL AFTER custom_headers,
		ADD COLUMN redirect_policy        VARCHAR(16) NULL DEFAULT NULL AFTER timeout_seconds,
		ADD COLUMN alert_cooldown_minutes SMALLINT UNSIGNED NULL AFTER redirect_policy`},

	// Migration 39 previously changed jetpack_monitor_check_targets from
	// blog_id uniqueness to source_site_id uniqueness. Production has not
	// received the v2 schema yet, so migration 34 now creates the correct
	// endpoint-keyed shape directly.
	{39, `SELECT 1`},

	// Migration 40 creates the trusted Veriflier vantage registry used by
	// monitor-side discovery. Vantages are quorum-counted identities, not
	// individual processes. enabled defaults to 0 so agent telemetry can never
	// create its own trusted vote.
	{40, `CREATE TABLE IF NOT EXISTS jetpack_monitor_veriflier_vantages (
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
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 41 records concrete Veriflier agent telemetry collected by
	// monitors from /v2/status. These rows are operational telemetry and
	// capacity hints; only pre-approved enabled rows in
	// jetpack_monitor_veriflier_vantages are eligible for quorum or traffic.
	{41, `CREATE TABLE IF NOT EXISTS jetpack_monitor_veriflier_agents (
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
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 42 creates durable rollout sessions for the API-driven
	// container rollout. A session is the audit/root object for one operator
	// handoff range and lets the CLI resume without relying only on local state.
	{42, `CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_sessions (
		run_id       VARCHAR(64) NOT NULL PRIMARY KEY,
		bucket_min   SMALLINT UNSIGNED NOT NULL,
		bucket_max   SMALLINT UNSIGNED NOT NULL,
		owner_host   VARCHAR(255) NOT NULL DEFAULT '',
		change_ref   VARCHAR(255) NOT NULL DEFAULT '',
		operator     VARCHAR(128) NOT NULL DEFAULT '',
		status       VARCHAR(16) NOT NULL DEFAULT 'open',
		metadata     JSON NULL,
		created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		INDEX idx_status_created (status, created_at),
		INDEX idx_range (bucket_min, bucket_max)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 43 records range-level activation/release history. It is
	// intentionally append-friendly: released rows stay behind as rollout
	// evidence, while live overlap prevention happens in jetpack_monitor_rollout_bucket_locks.
	{43, `CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_range_locks (
		id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		run_id        VARCHAR(64) NOT NULL,
		bucket_min    SMALLINT UNSIGNED NOT NULL,
		bucket_max    SMALLINT UNSIGNED NOT NULL,
		owner_host    VARCHAR(255) NOT NULL,
		change_ref    VARCHAR(255) NOT NULL DEFAULT '',
		status        VARCHAR(16) NOT NULL DEFAULT 'active',
		activated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		released_at   TIMESTAMP NULL,
		metadata      JSON NULL,
		INDEX idx_status_range (status, bucket_min, bucket_max),
		INDEX idx_owner_status (owner_host, status),
		INDEX idx_run_id (run_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 44 stores one row per active rollout bucket. The primary key is
	// the range-lock guardrail: overlapping activations conflict even if two
	// operators race after both dry-runs looked clean.
	{44, `CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_bucket_locks (
		bucket_no    SMALLINT UNSIGNED NOT NULL PRIMARY KEY,
		run_id       VARCHAR(64) NOT NULL,
		range_lock_id BIGINT UNSIGNED NOT NULL,
		owner_host   VARCHAR(255) NOT NULL,
		status       VARCHAR(16) NOT NULL DEFAULT 'active',
		activated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_owner (owner_host),
		INDEX idx_run_id (run_id),
		INDEX idx_range_lock (range_lock_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 45 records rollout API jobs. Most current operations complete
	// synchronously, but the job shape lets the API grow into async smoke,
	// seed, comparison, and policy migration work without changing clients.
	{45, `CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_jobs (
		job_id      VARCHAR(64) NOT NULL PRIMARY KEY,
		run_id      VARCHAR(64) NOT NULL DEFAULT '',
		operation   VARCHAR(64) NOT NULL,
		status      VARCHAR(16) NOT NULL DEFAULT 'completed',
		progress    TINYINT UNSIGNED NOT NULL DEFAULT 100,
		summary     VARCHAR(1024) NOT NULL DEFAULT '',
		result      JSON NULL,
		error_code  VARCHAR(64) NOT NULL DEFAULT '',
		error_message VARCHAR(1024) NOT NULL DEFAULT '',
		created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		INDEX idx_run_operation (run_id, operation),
		INDEX idx_status_created (status, created_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 46 stores short-lived dry-run confirmation tokens. Tokens are
	// sha256-hashed at rest and bound to operation, bucket range, run id,
	// operator, and request hash so execute calls cannot reuse stale plans.
	{46, `CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_confirmation_tokens (
		token_hash    CHAR(64) NOT NULL PRIMARY KEY,
		run_id        VARCHAR(64) NOT NULL DEFAULT '',
		operation     VARCHAR(64) NOT NULL,
		request_hash  CHAR(64) NOT NULL,
		bucket_min    SMALLINT UNSIGNED NOT NULL,
		bucket_max    SMALLINT UNSIGNED NOT NULL,
		operator      VARCHAR(128) NOT NULL DEFAULT '',
		expires_at    TIMESTAMP NOT NULL,
		used_at       TIMESTAMP NULL,
		created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_run_operation (run_id, operation),
		INDEX idx_expires (expires_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 47 stores non-authoritative method/profile comparison samples
	// collected during rollout. These rows let operators compare HEAD legacy
	// behavior with GET simple/full behavior without changing the authoritative
	// site state or firing customer alerts.
	{47, `CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_comparison_results (
		id                 BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		job_id             VARCHAR(64) NOT NULL,
		run_id             VARCHAR(64) NOT NULL DEFAULT '',
		blog_id            BIGINT UNSIGNED NOT NULL,
		source_site_id     BIGINT UNSIGNED NOT NULL,
		bucket_no          SMALLINT UNSIGNED NOT NULL,
		monitor_url        VARCHAR(2083) NOT NULL,
		from_method        VARCHAR(16) NOT NULL,
		from_profile       VARCHAR(32) NOT NULL,
		to_method          VARCHAR(16) NOT NULL,
		to_profile         VARCHAR(32) NOT NULL,
		from_success       TINYINT(1) NOT NULL,
		to_success         TINYINT(1) NOT NULL,
		from_http_code     INT NOT NULL DEFAULT 0,
		to_http_code       INT NOT NULL DEFAULT 0,
		from_error_code    INT NOT NULL DEFAULT 0,
		to_error_code      INT NOT NULL DEFAULT 0,
		from_rtt_ms        INT NOT NULL DEFAULT 0,
		to_rtt_ms          INT NOT NULL DEFAULT 0,
		delta_class        VARCHAR(32) NOT NULL DEFAULT 'same',
		created_at         TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
		INDEX idx_job (job_id),
		INDEX idx_run_created (run_id, created_at),
		INDEX idx_delta_created (delta_class, created_at),
		INDEX idx_site (blog_id, source_site_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 48 records staged rollout policy mutations so a batch can be
	// rolled back without relying on local CLI transcripts. The previous values
	// are nullable because NULL means "inherit the fleet default" in
	// jetpack_monitor_site_check_config.
	{48, `CREATE TABLE IF NOT EXISTS jetpack_monitor_rollout_policy_stage_rows (
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
		rolled_back_at            TIMESTAMP(3) NULL,
		created_at                 TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
		INDEX idx_job (job_id),
		INDEX idx_run_created (run_id, created_at),
		INDEX idx_rollback (run_id, rolled_back_at, created_at),
		INDEX idx_blog (blog_id),
		INDEX idx_source_site (source_site_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 49 records non-downtime probe safety findings separately from
	// customer incident events. This gives operators a durable remediation
	// trail for unsafe legacy monitor URLs without mutating v1 site-table
	// semantics beyond an explicit deactivation when requested.
	{49, `CREATE TABLE IF NOT EXISTS jetpack_monitor_site_safety_flags (
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
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`},

	// Migration 50 persists Go 1.26 scheduler metrics in process heartbeat
	// rows so fleet views can spot goroutine or OS-thread growth without
	// requiring pprof access during an incident.
	{50, `ALTER TABLE jetpack_monitor_process_health
		ADD COLUMN runtime_goroutines INT UNSIGNED NOT NULL DEFAULT 0 AFTER rss_mem_mb,
		ADD COLUMN runtime_goroutines_runnable INT UNSIGNED NOT NULL DEFAULT 0 AFTER runtime_goroutines,
		ADD COLUMN runtime_goroutines_running INT UNSIGNED NOT NULL DEFAULT 0 AFTER runtime_goroutines_runnable,
		ADD COLUMN runtime_goroutines_waiting INT UNSIGNED NOT NULL DEFAULT 0 AFTER runtime_goroutines_running,
		ADD COLUMN runtime_goroutines_not_in_go INT UNSIGNED NOT NULL DEFAULT 0 AFTER runtime_goroutines_waiting,
		ADD COLUMN runtime_goroutines_created BIGINT UNSIGNED NOT NULL DEFAULT 0 AFTER runtime_goroutines_not_in_go,
		ADD COLUMN runtime_threads INT UNSIGNED NOT NULL DEFAULT 0 AFTER runtime_goroutines_created`},

	// Migration 51 adds a composite index that covers eventstore.FindActive,
	// which filters jetpack_monitor_events by (blog_id, check_type, ended_at IS NULL).
	// The existing idx_blog_id_active is only (blog_id, ended_at), so a
	// blog with many open events across check types had to filter by
	// check_type after the index seek. The new index lets the engine reach
	// the matching row in one descent on the recovery path used after a
	// process restart or projection rebuild.
	{51, `ALTER TABLE jetpack_monitor_events
		ADD INDEX idx_blog_id_check_type_active (blog_id, check_type, ended_at)`},

	// Migration 52 adds per-site overrides for check-history recording. Both
	// columns are NULL by default, meaning "use CHECK_HISTORY_MODE_DEFAULT /
	// CHECK_HISTORY_SAMPLE_RATE_DEFAULT". A site can opt into a different mode
	// (e.g. 'all' for a site under investigation, 'disabled' for a low-value
	// test site) without affecting the fleet default. check_history_mode is a
	// free-form VARCHAR rather than an ENUM so adding modes later does not
	// require an ALTER; unknown values fall back to the default at read time.
	{52, `ALTER TABLE jetpack_monitor_site_check_config
		ADD COLUMN check_history_mode VARCHAR(32) NULL AFTER alert_cooldown_minutes,
		ADD COLUMN check_history_sample_rate INT UNSIGNED NULL AFTER check_history_mode`},

	// Migration 53 denormalizes endpoint identity onto event transitions.
	// Existing dev/lab databases may already have migration 11 applied, so this
	// stays as a separate additive migration while the production baseline DDL
	// creates the final shape directly.
	{53, `ALTER TABLE jetpack_monitor_event_transitions
		ADD COLUMN endpoint_id BIGINT UNSIGNED NULL AFTER blog_id,
		ADD INDEX idx_endpoint_id_changed (endpoint_id, changed_at)`},
}

// Migrate applies all pending migrations idempotently.
func Migrate() error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migration connection: %w", err)
	}
	defer conn.Close()

	if err := acquireMigrationLock(ctx, conn); err != nil {
		return err
	}
	defer func() {
		if err := releaseMigrationLock(ctx, conn); err != nil {
			log.Printf("release migration lock: %v", err)
		}
	}()

	// Ensure the migrations table exists first (migration 1 is special).
	if _, err := conn.ExecContext(ctx, migrations[0].sql); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	if err := markApplied(ctx, conn, migrations[0].id); err != nil {
		return err
	}

	for _, m := range migrations[1:] {
		applied, err := isApplied(ctx, conn, m.id)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		log.Printf("applying migration %d", m.id)
		if _, err := conn.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("migration %d: %w", m.id, err)
		}
		if err := markApplied(ctx, conn, m.id); err != nil {
			return err
		}
	}
	return nil
}

// ExpectedSchemaMigration returns the highest embedded migration ID in this
// binary. Migration IDs are append-only and ordered in the migrations slice.
func ExpectedSchemaMigration() int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].id
}

// SchemaMigrationStatus reads the migration ledger without attempting any DDL.
func SchemaMigrationStatus(ctx context.Context) (MigrationStatus, error) {
	status := MigrationStatus{
		ExpectedCount: len(migrations),
		ExpectedMaxID: ExpectedSchemaMigration(),
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return status, fmt.Errorf("schema validation connection: %w", err)
	}
	defer conn.Close()

	rows, err := conn.QueryContext(ctx, `SELECT id FROM jetpack_monitor_schema_migrations ORDER BY id`)
	if err != nil {
		return status, fmt.Errorf("read schema migrations: %w", err)
	}
	defer rows.Close()

	expected := make(map[int]struct{}, len(migrations))
	for _, migration := range migrations {
		expected[migration.id] = struct{}{}
	}
	applied := make(map[int]struct{}, len(migrations))
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return status, fmt.Errorf("scan schema migration: %w", err)
		}
		status.AppliedCount++
		if id > status.CurrentMaxID {
			status.CurrentMaxID = id
		}
		if _, ok := expected[id]; ok {
			applied[id] = struct{}{}
			continue
		}
		status.UnknownIDs = append(status.UnknownIDs, id)
	}
	if err := rows.Err(); err != nil {
		return status, fmt.Errorf("read schema migrations: %w", err)
	}

	for _, migration := range migrations {
		if _, ok := applied[migration.id]; !ok {
			status.PendingIDs = append(status.PendingIDs, migration.id)
		}
	}
	return status, nil
}

func acquireMigrationLock(ctx context.Context, conn *sql.Conn) error {
	var got sql.NullInt64
	err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, ?)`, migrationLockName, migrationLockTimeoutSeconds).Scan(&got)
	if err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if !got.Valid || got.Int64 != 1 {
		return fmt.Errorf("migration lock %q unavailable after %d seconds", migrationLockName, migrationLockTimeoutSeconds)
	}
	return nil
}

func releaseMigrationLock(ctx context.Context, conn *sql.Conn) error {
	var released sql.NullInt64
	err := conn.QueryRowContext(ctx, `SELECT RELEASE_LOCK(?)`, migrationLockName).Scan(&released)
	if err != nil {
		return err
	}
	if !released.Valid || released.Int64 != 1 {
		return fmt.Errorf("migration lock %q was not held by this connection", migrationLockName)
	}
	return nil
}

func isApplied(ctx context.Context, conn *sql.Conn, id int) (bool, error) {
	var count int
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM jetpack_monitor_schema_migrations WHERE id = ?`, id).Scan(&count)
	return count > 0, err
}

func markApplied(ctx context.Context, conn *sql.Conn, id int) error {
	_, err := conn.ExecContext(ctx,
		`INSERT IGNORE INTO jetpack_monitor_schema_migrations (id) VALUES (?)`, id,
	)
	return err
}

# Data Model

Jetmon 2 keeps the legacy site table v1-shaped during the
[v1-to-v2 migration](v1-to-v2-migration.md) and adds Jetmon-owned side tables
for v2-only configuration, runtime freshness, and event-sourced incident state.
New schema changes are additive and applied by `./jetmon2 migrate`.

## Time Zones

All temporal columns store UTC. Jetmon writes UTC `time.Time` values and the
MySQL driver is configured with `parseTime=true`, `loc=UTC`, and a pinned
session `time_zone='+00:00'` (`internal/db/server_map.go`). Pinning the session
zone is what guarantees consistency: it makes `DATETIME`, `TIMESTAMP`, and
server-evaluated `CURRENT_TIMESTAMP` defaults all interpret and return UTC
regardless of the database host's OS time zone, so the same row reads back
identically on every Monitor host. `DATETIME` columns (e.g. site-runtime
freshness, maintenance windows) are intentionally kept rather than converted to
`TIMESTAMP`, both because the session pin already makes them UTC-consistent and
because `TIMESTAMP` carries a 2038 epoch ceiling that `DATETIME` does not.

## Legacy Site Table

The primary site table remains `jetpack_monitor_sites`.

```sql
CREATE TABLE `jetpack_monitor_sites` (
  `jetpack_monitor_site_id` bigint(20) unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `blog_id` bigint(20) unsigned NOT NULL,
  `bucket_no` smallint(2) unsigned NOT NULL,
  `monitor_url` varchar(300) NOT NULL,
  `monitor_active` tinyint(1) unsigned NOT NULL DEFAULT 1,
  `site_status` tinyint(1) unsigned NOT NULL DEFAULT 1,
  `last_status_change` timestamp NULL DEFAULT current_timestamp(),
  `check_interval` tinyint(1) unsigned NOT NULL DEFAULT 5,
  INDEX `blog_id_monitor_url` (`blog_id`, `monitor_url`),
  INDEX `bucket_no_monitor_active_check_interval`
    (`bucket_no`, `monitor_active`, `check_interval`)
);
```

Jetmon v2 does not require new columns or indexes on an existing production
`jetpack_monitor_sites` table. It continues to read v1-owned fields such as
`monitor_url`, `monitor_active`, `bucket_no`, and `check_interval`, and it
writes only the v1 compatibility projection fields `site_status` and
`last_status_change` while `LEGACY_STATUS_PROJECTION_ENABLE` is on.

## New Tables

All v2-owned tables use the `jetpack_monitor_` prefix, matching the legacy
`jetpack_monitor_sites` table and the WPCOM `jetpack_` dataset/pool-routing
convention. Do not introduce `jetmon_`-prefixed tables. See
[ADR-0011](adr/0011-table-naming-jetpack-monitor-prefix.md).

| Table | Purpose |
|---|---|
| `jetpack_monitor_schema_migrations` | Applied migration tracking |
| `jetpack_monitor_hosts` | MySQL-coordinated bucket ownership and heartbeat |
| `jetpack_monitor_events` | Authoritative current state of every incident |
| `jetpack_monitor_event_transitions` | Append-only mutation history for events |
| `jetpack_monitor_audit_log` | Operational trail for checks, retries, WPCOM calls, suppression, API access, and reloads |
| `jetpack_monitor_check_history` | Request method plus RTT and timing samples for trending |
| `jetpack_monitor_false_positives` | Veriflier non-confirmation records |
| `jetpack_monitor_veriflier_vantages` | Trusted quorum-counted Veriflier vantage registry |
| `jetpack_monitor_veriflier_agents` | Concrete Veriflier process telemetry and capacity hints |
| `jetpack_monitor_api_keys` | Internal REST API Bearer-token registry |
| `jetpack_monitor_webhooks` | Webhook registrations and HMAC signing secrets |
| `jetpack_monitor_webhook_deliveries` | Outbound webhook delivery attempts and retry state |
| `jetpack_monitor_webhook_dispatch_progress` | Webhook worker high-water marks over transitions |
| `jetpack_monitor_alert_contacts` | Managed destinations such as email, PagerDuty, Slack, and Teams |
| `jetpack_monitor_alert_deliveries` | Outbound alert-contact attempts and retry state |
| `jetpack_monitor_alert_dispatch_progress` | Alert worker high-water marks over transitions |
| `jetpack_monitor_site_tenants` | Tenant-to-site mapping for gateway-scoped API access |
| `jetpack_monitor_process_health` | Durable per-process heartbeat snapshots for host and fleet dashboards |
| `jetpack_monitor_check_targets` | V2-native scheduling target state for the streaming monitor engine |
| `jetpack_monitor_site_check_config` | Per-site v2 check config: rollout method/profile, body rules, maintenance windows, custom headers, timeout, redirect policy, and cooldown overrides |
| `jetpack_monitor_site_runtime` | V2 runtime freshness and derived observation state such as last checked time, next due time, last alert time, and SSL expiry |
| `jetpack_monitor_site_safety_flags` | Non-downtime remediation state for unsafe legacy monitor URLs and runtime probe-safety blocks |
| `jetpack_monitor_rollout_sessions` | API-driven container rollout sessions bound to bucket ranges and operator/change metadata |
| `jetpack_monitor_rollout_range_locks` | Durable activation/release history for API-controlled bucket ranges |
| `jetpack_monitor_rollout_bucket_locks` | One active lock row per bucket, used to prevent overlapping v2 range activation |
| `jetpack_monitor_rollout_jobs` | Synchronous rollout operation audit records and result payloads |
| `jetpack_monitor_rollout_confirmation_tokens` | Short-lived hashed dry-run confirmation tokens for mutating rollout operations |
| `jetpack_monitor_rollout_comparison_results` | Non-authoritative sampled HEAD/GET comparison deltas collected during policy migration |
| `jetpack_monitor_rollout_policy_stage_rows` | Per-cohort policy mutation history used by rollback-last-stage and rollback-all |

## Site Check Policy

`jetpack_monitor_site_check_config` keeps staged rollout policy and rich v2 probe config
out of `jetpack_monitor_sites`:

```sql
CREATE TABLE `jetpack_monitor_site_check_config` (
  `blog_id` bigint(20) unsigned NOT NULL PRIMARY KEY,
  `request_method` enum('HEAD','GET') NULL,
  `detection_profile` enum('legacy','simple_http','full') NULL,
  `check_keyword` varchar(500) NULL,
  `forbidden_keyword` varchar(500) NULL,
  `forbidden_keywords` json NULL,
  `maintenance_start` datetime NULL,
  `maintenance_end` datetime NULL,
  `custom_headers` json NULL,
  `timeout_seconds` tinyint unsigned NULL,
  `redirect_policy` enum('follow','alert','fail') NULL,
  `alert_cooldown_minutes` smallint unsigned NULL,
  `created_at` timestamp NOT NULL DEFAULT current_timestamp(),
  `updated_at` timestamp NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp()
);
```

NULL values inherit process defaults from `DEFAULT_CHECK_METHOD` and
`DEFAULT_DETECTION_PROFILE`. During rollout, use `HEAD` + `legacy` for
v1-compatible replacement, `GET` + `simple_http` for visitor-path migration,
and `GET` + `full` for the complete v2 detection set. A `HEAD` request
automatically caps the effective profile to `simple_http`; body-based keyword
and forbidden-content checks require `GET`.

The API can expose a derived `cli_batch` field for local API CLI test data when
`include_cli_metadata=true` is requested and `custom_headers` contains
`X-Jetmon-CLI-Batch`; it is not a dedicated database column.

## Site Safety Flags

`jetpack_monitor_site_safety_flags` tracks unsafe targets separately from customer
downtime events. Runtime probe-safety blocks are inserted as open flags with
`flag_type='probe_safety_block'`; `jetmon2 site-safety unsafe-urls --execute`
inserts `flag_type='unsafe_monitor_url'` rows with `status='deactivated'`
before it sets the legacy row's `monitor_active` value to false.

The table preserves the monitor row id, blog id, URL, reason, first/last seen
timestamps, and remediation status. It intentionally does not replace the
legacy site table or event tables: `monitor_active` still controls whether a
site is checked, and probe-safety blocks remain excluded from SLA downtime,
WPCOM down/recovery notifications, webhooks, and alert-contact delivery.
Use `jetmon2 site-safety report` for read-only operator reporting over these
flags, and `jetmon2 site-safety unsafe-urls` when scanning active legacy rows
for shape-unsafe stored URLs.

## Site Runtime

`jetpack_monitor_site_runtime` keeps v2 freshness and derived observations out of the
legacy table:

```sql
CREATE TABLE `jetpack_monitor_site_runtime` (
  `blog_id` bigint(20) unsigned NOT NULL PRIMARY KEY,
  `last_checked_at` datetime NULL,
  `next_check_at` datetime NULL,
  `last_alert_sent_at` datetime NULL,
  `ssl_expiry_date` date NULL,
  `updated_at` timestamp NOT NULL DEFAULT current_timestamp() ON UPDATE current_timestamp(),
  INDEX `idx_next_check` (`next_check_at`, `blog_id`),
  INDEX `idx_last_checked` (`last_checked_at`, `blog_id`)
);
```

`last_checked_at` and `next_check_at` support API display, rollout freshness
checks, and rollback visibility without requiring v2 to rewrite the v1
compatibility table after every probe. The streaming scheduler keeps its hot
due-time state in memory and in `jetpack_monitor_check_targets`;
`jetpack_monitor_site_runtime` is a compatibility/readability projection, not the
high-frequency source of truth for streaming mode.

## Streaming Check Targets

`jetpack_monitor_check_targets` is additive scheduling infrastructure for the
streaming scheduler. During migration, `jetpack_monitor_sites` remains the
source of truth for v1-owned site identity and current legacy status, while
`jetpack_monitor_site_check_config` carries v2-only probe config. The target table stores
derived scheduling details such as source site row, bucket, interval, stable
phase slot, config hash, and coarse last outcome fields so later iterations can
sync scheduling state without repeatedly scanning the legacy table or writing
healthy probe freshness back into it.

The current streaming engine creates the table but still reloads active config
from `jetpack_monitor_sites`. That keeps correctness and rollback behavior easy
to validate before moving config-sync reads fully onto the v2-native target
table in a later scaling branch.

Production data can contain more than one active monitor URL for the same
`blog_id`. Monitor execution therefore treats
`jetpack_monitor_sites.jetpack_monitor_site_id` as the endpoint identity for
HTTP checks while retaining `blog_id` as the WPCOM/site identity. HTTP events
write that row id to `jetpack_monitor_events.endpoint_id`, scheduler/retry in-memory
state keys by the row id when available, and the v1 compatibility projection is
updated by `jetpack_monitor_site_id` so two active URLs for one site do not
overwrite each other's rollout state. The v2-native target table is unique on
`source_site_id` for the same reason.

## Process Health

`jetpack_monitor_process_health` is the durable source for fleet-level operator views.
Each long-running process owns one stable `process_id` such as
`<host>:monitor` or `<host>:deliverer` and periodically upserts a compact
snapshot:

- process identity: host, process type, PID, version, build date, Go version
- lifecycle state: `running`, `idle`, `stopping`, or `stopped`
- health rollup: `green`, `amber`, or `red`, derived from local dependency
  health and rollout-relevant warnings
- monitor state: bucket range, ownership mode, worker counts, queue depths,
  WPCOM circuit/queue state, delivery-owner state, API/dashboard ports, RSS
  memory, Go runtime system memory, and Go scheduler counters for goroutines
  and OS threads
- dependency health JSON: MySQL, Verifliers, WPCOM, StatsD, and local writable
  directories where applicable

Fleet dashboards must treat stale `updated_at` values as unknown or unhealthy.
The row says what the process last reported; it is not proof that the process is
still alive after the heartbeat age exceeds the dashboard threshold.

The fleet dashboard combines this table with `jetpack_monitor_hosts`, outbound delivery
queues, and projection-drift counts. Dependency health stored in the process
snapshot is also used to roll up shared dependencies such as Verifliers, MySQL,
WPCOM, and StatsD across hosts.

## Veriflier Discovery

`jetpack_monitor_veriflier_vantages` stores the trusted identities that monitors may use
for downtime quorum. `enabled` defaults to false, so a newly running Veriflier
cannot mint its own vote. Usable active-discovery rows need `vantage_id`,
`endpoint_host`, `endpoint_port`, and `auth_token`.

`jetpack_monitor_veriflier_agents` stores concrete process telemetry collected by
monitors from authenticated Veriflier `/v2/status` responses. Agents report
`agent_id`, `vantage_id`, version, supported protocols, endpoint host/port,
capacity, and `last_seen`. These rows are operational telemetry and endpoint
hints only; they are ignored for quorum unless the matching vantage is
pre-approved and enabled.

## Check History

`jetpack_monitor_check_history` records one compact timing sample per local check. The
`request_method` column records the actual HTTP method used by the probe. This
is primarily operational evidence for v2 rollout and uptime-bench review: v2
should show `HEAD` during the initial legacy-compatible replacement phase,
`GET` during the visitor-path migration phase, and the actual effective method
for any per-site exceptions. Failure events carry richer per-incident metadata
such as URL and error reason.

## Event Source Of Truth

Incident state is authoritative in:

- `jetpack_monitor_events`: one mutable row per live incident identity, frozen after
  close.
- `jetpack_monitor_event_transitions`: one append-only row for every mutation.

Every open, severity change, state change, cause-link change, and close writes a
transition row in the same transaction as the event update. The `eventstore`
package is the only writer for these tables.

The lifecycle is:

```text
Up -> Seems Down -> Down -> Resolved
         |
         +-> Up (false alarm or probe-cleared)
```

`Seems Down` is first-class. It opens on the first local failure so incident
duration starts when the user impact began, not when Verifliers later confirmed
the outage.

## Legacy Projection

During the shadow-state portion of the
[v1-to-v2 migration](v1-to-v2-migration.md),
`jetpack_monitor_sites.site_status` and `last_status_change` are compatibility
projections. With `LEGACY_STATUS_PROJECTION_ENABLE` enabled, every v2 event
mutation also updates the legacy fields in the same transaction.

Projection mapping:

| v2 state | Legacy `site_status` |
|---|---:|
| Open `Seems Down` | `0` (`SITE_DOWN`) |
| Open `Down` | `2` (`SITE_CONFIRMED_DOWN`) |
| Closed or no open incident | `1` (`SITE_RUNNING`) |

If drift is suspected, inspect mismatches with:

```bash
./jetmon2 rollout projection-drift
./jetmon2 rollout projection-drift --bucket-min=0 --bucket-max=99 --limit=100
```

The drift report summarizes mismatches by bucket, projected status, expected
status, likely cause, and sample blog before listing individual rows. It is
read-only: use the likely-cause and repair guidance to confirm the event rows
and transition history before making any reviewed database repair.

Watch for repeated drift classes during rollout rehearsal and early production
operation. Do not add an automated or dry-run repair planner until those real
examples show which mismatch classes are safe to repair mechanically and which
ones require eventstore investigation first.

After legacy readers move to the v2 API or event tables, disable the projection.

## Status And Failure Types

Legacy status values:

| Value | Meaning |
|---:|---|
| `0` | Local checks failed, retry or verification in progress |
| `1` | Site is running |
| `2` | Verifliers confirmed the site down |

Failure classifications:

| Type | Meaning |
|---|---|
| `server` | 5xx response |
| `blocked` | 403 response |
| `client` | 4xx response other than 403 |
| `https` | SSL/TLS problem |
| `intermittent` | Request timeout |
| `redirect` | Redirect policy failure |
| `ssl_expiry` | Certificate expiration threshold crossed |
| `tls_deprecated` | TLS 1.0 or 1.1 |
| `keyword_missing` | Required keyword was not present |
| `keyword_forbidden` | Forbidden keyword was present |
| `success` | Recovery |

## Tenant Mapping

`jetpack_monitor_site_tenants` maps gateway tenant IDs to `blog_id` values. The import
tool upserts known mappings and intentionally does not delete missing mappings:

```bash
./jetmon2 site-tenants import --file site-tenants.csv --dry-run
./jetmon2 site-tenants import --file site-tenants.csv --source gateway
```

The CSV format is `tenant_id,blog_id` with an optional header row.

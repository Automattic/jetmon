# Data Model

Jetmon 2 keeps the legacy site table as the configuration source during the
[v1-to-v2 migration](v1-to-v2-migration.md) and adds event-sourced incident
tables around it. New schema changes are additive and applied by
`./jetmon2 migrate`.

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

Jetmon 2 adds these columns:

| Column | Type | Purpose |
|---|---|---|
| `ssl_expiry_date` | `DATE NULL` | Last observed HTTPS certificate expiry |
| `check_keyword` | `VARCHAR(500) NULL` | Required response-body string |
| `maintenance_start` | `DATETIME NULL` | Maintenance window start |
| `maintenance_end` | `DATETIME NULL` | Maintenance window end |
| `custom_headers` | `JSON NULL` | Per-site request headers |
| `timeout_seconds` | `TINYINT NULL` | Per-site timeout override |
| `redirect_policy` | `ENUM NULL` | `follow`, `alert`, or `fail` |
| `alert_cooldown_minutes` | `SMALLINT NULL` | Per-site alert cooldown override |

The API can expose a derived `cli_batch` field for local API CLI test data when
`include_cli_metadata=true` is requested and `custom_headers` contains
`X-Jetmon-CLI-Batch`; it is not a dedicated database column.

## New Tables

| Table | Purpose |
|---|---|
| `jetmon_schema_migrations` | Applied migration tracking |
| `jetmon_hosts` | MySQL-coordinated bucket ownership and heartbeat |
| `jetmon_events` | Authoritative current state of every incident |
| `jetmon_event_transitions` | Append-only mutation history for events |
| `jetmon_audit_log` | Operational trail for checks, retries, WPCOM calls, suppression, API access, and reloads |
| `jetmon_check_history` | RTT and timing samples for trending |
| `jetmon_false_positives` | Veriflier non-confirmation records |
| `jetmon_api_keys` | Internal REST API Bearer-token registry |
| `jetmon_webhooks` | Webhook registrations and HMAC signing secrets |
| `jetmon_webhook_deliveries` | Outbound webhook delivery attempts and retry state |
| `jetmon_webhook_dispatch_progress` | Webhook worker high-water marks over transitions |
| `jetmon_alert_contacts` | Managed destinations such as email, PagerDuty, Slack, and Teams |
| `jetmon_alert_deliveries` | Outbound alert-contact attempts and retry state |
| `jetmon_alert_dispatch_progress` | Alert worker high-water marks over transitions |
| `jetmon_site_tenants` | Tenant-to-site mapping for gateway-scoped API access |
| `jetmon_process_health` | Durable per-process heartbeat snapshots for host and fleet dashboards |

## Process Health

`jetmon_process_health` is the durable source for fleet-level operator views.
Each long-running process owns one stable `process_id` such as
`<host>:monitor` or `<host>:deliverer` and periodically upserts a compact
snapshot:

- process identity: host, process type, PID, version, build date, Go version
- lifecycle state: `running`, `idle`, `stopping`, or `stopped`
- health rollup: `green`, `amber`, or `red`, derived from local dependency
  health and rollout-relevant warnings
- monitor state: bucket range, ownership mode, worker counts, queue depths,
  WPCOM circuit/queue state, delivery-owner state, API/dashboard ports, RSS
  memory, and Go runtime system memory
- dependency health JSON: MySQL, Verifliers, WPCOM, StatsD, and local writable
  directories where applicable

Fleet dashboards must treat stale `updated_at` values as unknown or unhealthy.
The row says what the process last reported; it is not proof that the process is
still alive after the heartbeat age exceeds the dashboard threshold.

The fleet dashboard combines this table with `jetmon_hosts`, outbound delivery
queues, and projection-drift counts. Dependency health stored in the process
snapshot is also used to roll up shared dependencies such as Verifliers, MySQL,
WPCOM, and StatsD across hosts.

## Event Source Of Truth

Incident state is authoritative in:

- `jetmon_events`: one mutable row per live incident identity, frozen after
  close.
- `jetmon_event_transitions`: one append-only row for every mutation.

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
| `success` | Recovery |

## Tenant Mapping

`jetmon_site_tenants` maps gateway tenant IDs to `blog_id` values. The import
tool upserts known mappings and intentionally does not delete missing mappings:

```bash
./jetmon2 site-tenants import --file site-tenants.csv --dry-run
./jetmon2 site-tenants import --file site-tenants.csv --source gateway
```

The CSV format is `tenant_id,blog_id` with an optional header row.

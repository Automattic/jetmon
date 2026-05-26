# Data Model

Jetmon 2 keeps the legacy `jetpack_monitor_sites` table v1-shaped and stores
new behavior in Jetmon-owned side tables. That split is the rollout contract:
v2 can run beside v1, can roll back cleanly, and can add richer state without a
hot ALTER on the largest compatibility table.

Local and lab environments can apply migrations with `./jetmon2 migrate`.
Production schema changes are expected to be applied through the approved
database-change process, then validated by Jetmon before the service starts.
`SCHEMA_MANAGEMENT_MODE=validate` checks the physical schema contract directly:
the required tables, columns, and indexes must exist, but production does not
need to maintain Jetmon's local migration ledger. This document describes
ownership and operational meaning. See
[production-schema-package.md](production-schema-package.md) for the
Systems-facing DDL package and validation checklist.

## Time Zones

All temporal columns store UTC. Jetmon writes UTC `time.Time` values and opens
MySQL sessions with `parseTime=true`, `loc=UTC`, and
`time_zone='+00:00'`. The pinned session zone keeps `DATETIME`, `TIMESTAMP`,
and server-side `CURRENT_TIMESTAMP` defaults consistent across Monitor hosts.

`DATETIME` is still used for maintenance windows and freshness projections
where the 2038 `TIMESTAMP` ceiling would be a bad tradeoff.

## Table Ownership

All v2-owned tables use the `jetpack_monitor_` prefix. Do not introduce
`jetmon_` tables. See
[ADR-0011](adr/0011-table-naming-jetpack-monitor-prefix.md).

| Area | Tables |
| --- | --- |
| Legacy compatibility | `jetpack_monitor_sites` |
| Local/lab migration tracking | `jetpack_monitor_schema_migrations` |
| Bucket ownership | `jetpack_monitor_hosts` |
| Incidents | `jetpack_monitor_events`, `jetpack_monitor_event_transitions` |
| Operational audit | `jetpack_monitor_audit_log` |
| Probe samples | `jetpack_monitor_check_history`, `jetpack_monitor_false_positives` |
| Site policy/runtime | `jetpack_monitor_site_check_config`, `jetpack_monitor_site_runtime`, `jetpack_monitor_check_targets`, `jetpack_monitor_site_safety_flags` |
| Veriflier discovery | `jetpack_monitor_veriflier_vantages`, `jetpack_monitor_veriflier_agents` |
| API/auth/gateway | `jetpack_monitor_api_keys`, `jetpack_monitor_site_tenants` |
| Webhooks | `jetpack_monitor_webhooks`, `jetpack_monitor_webhook_deliveries`, `jetpack_monitor_webhook_dispatch_progress` |
| Alert contacts | `jetpack_monitor_alert_contacts`, `jetpack_monitor_alert_deliveries`, `jetpack_monitor_alert_dispatch_progress` |
| Process dashboards | `jetpack_monitor_process_health` |
| API rollout | `jetpack_monitor_rollout_sessions`, `jetpack_monitor_rollout_range_locks`, `jetpack_monitor_rollout_bucket_locks`, `jetpack_monitor_rollout_jobs`, `jetpack_monitor_rollout_confirmation_tokens`, `jetpack_monitor_rollout_comparison_results`, `jetpack_monitor_rollout_policy_stage_rows` |

## Legacy Site Table

`jetpack_monitor_sites` remains the source for v1-owned site identity and
cadence:

- `jetpack_monitor_site_id`
- `blog_id`
- `bucket_no`
- `monitor_url`
- `monitor_active`
- `check_interval`

V2 does not require new columns or indexes on an existing production
`jetpack_monitor_sites` table. During the compatibility phase, v2 writes only
the legacy projection fields:

- `site_status`
- `last_status_change`

Production data can contain more than one active monitor URL for the same
`blog_id`. Monitor execution therefore treats
`jetpack_monitor_sites.jetpack_monitor_site_id` as the endpoint identity for
HTTP checks and keeps `blog_id` as the WPCOM/site identity. Event rows store
the endpoint row id in `endpoint_id`, scheduler state keys by that row id when
available, and projection updates target the legacy row id so duplicate
`blog_id` rows do not overwrite each other.

## Site Policy

`jetpack_monitor_site_check_config` keeps v2-only policy out of the legacy site
table. It is keyed by `source_site_id`, which is the legacy
`jetpack_monitor_sites.jetpack_monitor_site_id` endpoint row id. `blog_id` is
kept as indexed context for support queries, not as a uniqueness constraint. It
owns:

- staged rollout method/profile: `request_method`, `detection_profile`
- body rules: `check_keyword`, `forbidden_keyword`, `forbidden_keywords`
- operator controls: maintenance windows, custom headers, timeout, redirect
  policy, and alert cooldown
- check-history overrides: `check_history_mode`,
  `check_history_sample_rate`

NULL values inherit fleet defaults. During rollout, the intended path is:

1. `HEAD` + `legacy` for v1-compatible replacement.
2. `GET` + `simple_http` for visitor-path migration.
3. `GET` + `full` for the complete v2 detection set.

A `HEAD` request automatically caps the effective profile to `simple_http`.
Body-based keyword and forbidden-content checks require `GET`. In `GET` +
`full`, Jetmon also applies conservative semantic body checks for successful
HTML responses, including common WordPress database repair, fatal PHP,
unsupported PHP/database, Redis object-cache connection errors, maintenance,
setup/configuration, and critical-error pages, default virtual-host pages, host
suspension pages, Jetpack probe echo pages, and near-empty HTML documents. Those
checks are intentionally not active in
`HEAD`/legacy or `GET`/`simple_http` so rollout cohorts can separate method
migration from the full v2 detection surface.

The API can expose a derived `cli_batch` field for local API CLI test data when
`include_cli_metadata=true` and `custom_headers` contains
`X-Jetmon-CLI-Batch`. It is not a dedicated database column.

## Runtime And Scheduling State

`jetpack_monitor_site_runtime` stores coarse v2 runtime projections:

- last checked time
- next due time
- last alert time
- SSL expiry observation

These fields support API display, rollout freshness checks, and rollback
visibility. They are not the high-frequency source of truth for the streaming
engine. Like policy, runtime rows are keyed by `source_site_id` so multiple
monitor endpoints for the same `blog_id` keep independent freshness and SSL
state.

`jetpack_monitor_check_targets` is the v2-native scheduling table. It stores
derived target state such as source site row, bucket, interval, phase slot,
config hash, and coarse last outcome fields. The table is unique on
`source_site_id` because the legacy data can contain multiple active monitor
URLs for one `blog_id`.

During migration, `jetpack_monitor_sites` remains the source of truth for
active sites and cadence. The target table exists so later scaling work can
sync scheduling state without repeatedly scanning the legacy table or writing
healthy-probe freshness on every check.

## Incidents And Projection

Incident state is authoritative in:

- `jetpack_monitor_events`: one mutable row per live incident identity, frozen
  after close
- `jetpack_monitor_event_transitions`: one append-only row for every mutation

Every open, severity change, state change, cause-link change, and close goes
through `internal/eventstore` and writes the event row, transition row, and
legacy projection in one transaction when projection is enabled.

Both tables carry `blog_id` and endpoint identity. `blog_id` remains the legacy
WPCOM/site identity used for tenant ownership and legacy notification payloads;
`endpoint_id` is the monitor endpoint row id used by v2 API paths, rollout
cohorts, and endpoint-specific diagnostics. Transition rows denormalize
`endpoint_id` so webhook, alert, and API consumers can stay endpoint-aware
without joining back to the event row for every dispatch.

The public lifecycle is:

```text
Up -> Seems Down -> Down -> Resolved
         |
         +-> Up (false alarm or probe-cleared)
```

Projection mapping while `LEGACY_STATUS_PROJECTION_ENABLE` is on:

| v2 state | Legacy `site_status` |
| --- | ---: |
| Open `Seems Down` | `0` (`SITE_DOWN`) |
| Open `Down` | `2` (`SITE_CONFIRMED_DOWN`) |
| Closed or no open incident | `1` (`SITE_RUNNING`) |

Use `./jetmon2 rollout projection-drift` to inspect suspected mismatches. The
report is read-only; confirm the event rows and transition history before any
reviewed database repair. After legacy readers move to the v2 API/event tables,
disable the projection.

See [events.md](events.md) for the detailed event lifecycle, idempotency model,
transition reasons, and metadata rules.

## Check History And Audit

`jetpack_monitor_check_history` records compact timing samples for local
checks. Rows keep both `blog_id` and `source_site_id`: `blog_id` supports
legacy/site-level reporting, while `source_site_id` supports endpoint-specific
latency and rollout analysis. The `request_method` column records the actual
method used by the probe, which is useful during the HEAD-to-GET rollout.
Depending on fleet and per-site sampling policy, healthy checks may be sampled
rather than written on every run.

Failure events carry richer incident metadata such as URL, error reason,
redirect details, TLS details, DNS error information, and bounded body-read
diagnostics. Response bodies are not stored in event metadata.

`jetpack_monitor_audit_log` records what Jetmon did: WPCOM attempts, retry
dispatch, Veriflier RPCs, alert suppression, maintenance-window decisions, API
access, and config reloads. Site-state changes belong in the event tables, not
the audit log.

`jetpack_monitor_false_positives` records Veriflier non-confirmation events for
rollout and tuning review.

## Site Safety Flags

`jetpack_monitor_site_safety_flags` tracks unsafe targets separately from
customer downtime events. Runtime probe-safety blocks use
`flag_type='probe_safety_block'`; unsafe legacy monitor URL deactivations use
`flag_type='unsafe_monitor_url'`.

The table preserves the monitor row id, blog id, URL, reason, first/last seen
timestamps, and remediation status. It does not replace the legacy site table:
`monitor_active` still controls whether a target is checked. Probe-safety
blocks remain excluded from SLA downtime, WPCOM notifications, webhooks, and
alert-contact delivery.

Use:

```bash
./jetmon2 site-safety report
./jetmon2 site-safety unsafe-urls --execute
```

## Bucket Ownership

`jetpack_monitor_hosts` coordinates dynamic bucket ownership. Monitors claim
bucket ranges transactionally and heartbeat while active. Stale heartbeats let
surviving hosts reclaim coverage.

Pinned migration hosts intentionally bypass dynamic ownership when
`PINNED_BUCKET_MIN/MAX` or legacy `BUCKET_NO_MIN/MAX` is set.

## Process Health

`jetpack_monitor_process_health` is the durable source for host and fleet
dashboards. Each process owns a stable `process_id` such as
`<host>:monitor` or `<host>:deliverer` and periodically upserts:

- process identity and build/runtime version
- lifecycle state and health rollup
- bucket ownership mode, queue depths, worker counts, WPCOM state, delivery
  owner state, API/dashboard ports, RSS memory, Go memory, goroutine counts,
  and OS-thread counts
- dependency health JSON for MySQL, Verifliers, WPCOM, StatsD, and writable
  paths where applicable

Dashboards must treat stale `updated_at` values as unknown or unhealthy. The
row is the last self-report, not proof the process is still alive.

## Veriflier Discovery

`jetpack_monitor_veriflier_vantages` stores trusted quorum-counted Veriflier
identities. `enabled` defaults to false so a newly running Veriflier cannot
mint its own vote. Active discovery rows need `vantage_id`, endpoint host/port,
and auth token.

`jetpack_monitor_veriflier_agents` stores concrete process telemetry collected
from authenticated Veriflier `/v2/status` responses. Agent rows are endpoint
hints and capacity telemetry only; they are ignored for quorum unless the
matching vantage is pre-approved and enabled.

## Delivery And API Tables

API keys are sha256-hashed in `jetpack_monitor_api_keys`. Webhook and alert
contact secrets are stored in their respective registry tables because outbound
dispatch needs the plaintext secret material.

Webhook and alert workers read event transitions through high-water mark tables
and claim delivery rows transactionally. This lets multiple workers run without
double-sending the same delivery row.

`jetpack_monitor_site_tenants` maps gateway tenant IDs to `blog_id` values. The
import tool upserts known mappings and intentionally does not delete missing
mappings:

```bash
./jetmon2 site-tenants import --file site-tenants.csv --dry-run
./jetmon2 site-tenants import --file site-tenants.csv --source gateway
```

The CSV format is `tenant_id,blog_id` with an optional header row.

## Rollout Tables

The `jetpack_monitor_rollout_*` tables support API-driven container rollout:

- sessions bind an operator/change reference to a bucket range
- range and bucket locks prevent overlapping v2 activation
- jobs record synchronous operation results and future async operation shape
- confirmation tokens bind dry-run plans to execute calls
- comparison results store non-authoritative HEAD/GET sample deltas
- policy stage rows record cohort changes so the last stage or all stages can
  be rolled back

These tables are operational rollout state. They do not replace
`jetpack_monitor_sites` as the source of production site membership.

## Status And Failure Values

Legacy `site_status` values:

| Value | Meaning |
| ---: | --- |
| `0` | Local checks failed; retry or verification is in progress |
| `1` | Site is running |
| `2` | Verifliers confirmed the site down |

Common failure classifications:

| Type | Meaning |
| --- | --- |
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

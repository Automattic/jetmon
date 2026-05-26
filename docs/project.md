# Jetmon Project And Architecture

Jetmon 2 is the Go rewrite of Jetpack's uptime monitoring service. It replaces
the v1 Node.js plus C++ native-addon Monitor and the Qt/C++ Veriflier with Go
binaries while preserving the production-facing contracts needed for a cautious
v1-to-v2 rollout.

Use this file for the system shape and invariants. Use
[operations-guide.md](operations-guide.md) for day-to-day commands,
[v1-to-v2-migration.md](v1-to-v2-migration.md) for rollout, and
[internal-api-reference.md](internal-api-reference.md) for API details.

## Compatibility Contract

The rollout premise is "additive and reversible." Do not change these contracts
without explicit review:

| Surface | Rule |
| --- | --- |
| MySQL schema | Additive migrations only. `jetpack_monitor_sites` remains the v1-shaped identity, bucket, cadence, and compatibility projection table. |
| WPCOM notifications | Keep the legacy status-change payload and v1-compatible notification path during drop-in rollout. |
| StatsD | Preserve `com.jetpack.jetmon.<hostname>` style metric names. New metrics are additive. |
| Config | Existing v1-style config keys must parse where retained. New keys are additive. |
| Shutdown/reload | SIGINT drains; SIGHUP drains and re-execs through the configured restart target. |
| Legacy stats | `sitespersec`, `sitesqueue`, and `totals` remain available through the compatibility surface. |

Runtime logs go to stdout/stderr. V2 intentionally does not write v1-owned log
files unless a future consumer need is proven.

## Runtime Shape

`jetmon2` is a single binary that can host the monitor loop, REST API,
dashboard, and embedded delivery workers when configured.

```text
MySQL + WPCOM + StatsD
        ^
        |
jetmon2 monitor
  orchestrator -> checker pool -> eventstore
       |              |               |
       |              |               +-> events, transitions, projection
       |              +-> HTTP checks, timing, TLS observations
       +-> retry queue, Veriflier RPC, WPCOM legacy notify

optional in same process:
  REST API + dashboard + webhook worker + alert-contact worker

remote:
  veriflier2 fleet over JSON/HTTP /v2/check and /v2/status
```

The standalone `jetmon-deliverer` binary uses the same database-backed delivery
queues as the embedded workers and is the migration path for separating
outbound dispatch from monitor checks.

## Package Map

| Path | Responsibility |
| --- | --- |
| `cmd/jetmon2` | Main binary, CLI subcommands, signals, startup. |
| `internal/orchestrator` | Scheduling, DB target fetches, retry queue, Veriflier confirmation, WPCOM notification. |
| `internal/checker` | Bounded worker pool, HTTP checks, DNS/TCP/TLS/TTFB timing through `httptrace`. |
| `internal/eventstore` | Sole writer for `jetpack_monitor_events` and `jetpack_monitor_event_transitions`. |
| `internal/db` | MySQL connection handling, migrations, host heartbeat, bucket ownership. |
| `internal/api` | Internal REST API, auth, rate limits, idempotency, rollout API, OpenAPI route contract. |
| `internal/apikeys` | API key hashing, validation, and CLI key management. |
| `internal/dashboard` | Host and fleet dashboard plus SSE updates. |
| `internal/webhooks` | HMAC-signed event webhooks and retry worker. |
| `internal/alerting` | Managed email, PagerDuty, Slack, and Teams contact delivery. |
| `internal/veriflier` | Monitor-to-Veriflier JSON/HTTP client/server transport. |
| `veriflier2` | Remote confirmation service. |

## Data Model

V2 keeps customer/site identity compatible with v1 while moving operational
truth into v2-owned tables.

| Table | Purpose |
| --- | --- |
| `jetpack_monitor_sites` | v1-shaped site identity, bucket, cadence, and compatibility projection. |
| `jetpack_monitor_site_check_config` | V2 check policy: method, profile, redirects, keywords, headers, timeout, cooldown, maintenance. |
| `jetpack_monitor_site_runtime` | Runtime freshness, next check, last alert, SSL observation. |
| `jetpack_monitor_hosts` | Dynamic bucket ownership and heartbeat. |
| `jetpack_monitor_events` | Current incident projection, one row per open event identity. |
| `jetpack_monitor_event_transitions` | Append-only mutation history for every event change. |
| `jetpack_monitor_check_history` | Per-check timing and status samples. |
| `jetpack_monitor_audit_log` | Operational actions: WPCOM, retries, verifier RPCs, suppression, API access, config reloads. |
| `jetpack_monitor_site_safety_flags` | Unsafe URL and probe-safety state. |
| `jetpack_monitor_false_positives` | Veriflier non-confirmation evidence. |
| `jetpack_monitor_api_keys` | Internal API service tokens, sha256-hashed at rest. |
| `jetpack_monitor_webhooks` / `jetpack_monitor_webhook_deliveries` | Raw signed event delivery. |
| `jetpack_monitor_alert_contacts` / `jetpack_monitor_alert_deliveries` | Managed human notification delivery. |

All timestamps are UTC. Application code should treat MySQL values as UTC even
when a server session default differs.

## Event Model

Events are the source of truth. The legacy `site_status` field is a rollout
projection, not the authoritative state.

Every event mutation must write exactly one transition row in the same
transaction:

```text
jetpack_monitor_events             mutable current incident row
jetpack_monitor_event_transitions  append-only mutation history
```

`internal/eventstore` is the only writer for both tables. Callers may use the
store methods directly or open an eventstore transaction when they also need to
update the legacy projection atomically.

Event identity is idempotent: repeated detection of the same condition updates
the same open event instead of opening duplicates. The key is:

```text
(blog_id, endpoint_id, check_type, discriminator)
```

The lifecycle for downtime is:

```text
Up -> Seems Down -> Down -> Resolved
         |
         +-> Resolved false_alarm
```

A first local failure opens `Seems Down` so `started_at` records the actual
first observed failure. Veriflier confirmation promotes the same event to
`Down`; it does not close and reopen. Recovery closes the event with an
explicit resolution reason.

Important reasons:

| Reason | Meaning |
| --- | --- |
| `opened` | First event row created. |
| `verifier_confirmed` | Verifliers met quorum and promoted Seems Down to Down. |
| `verifier_cleared` | A confirmed outage recovered. |
| `probe_cleared` | Local probe recovered before Veriflier confirmation. |
| `false_alarm` | Verifliers rejected the local failure. |
| `manual_override` | Operator/API closed the event. |
| `auto_timeout` | Future automatic stale-event cleanup. |

Resolution reason is required on close.

## Severity And State

Severity is numeric and ordered; state is the lifecycle/display label. They are
stored separately because a condition can worsen without becoming a different
kind of incident.

| Severity | State | Use |
| --- | --- | --- |
| 0 | `Up` | No active issue. |
| 1 | `Warning` | Low-risk issue such as SSL expiry warning. |
| 2 | `Degraded` | Partial impact or significant anomaly. |
| 3 | `Seems Down` | Local failure under retry or verifier confirmation. |
| 4 | `Down` | Confirmed outage. |

Additional states such as `Paused`, `Maintenance`, `Unknown`, and `Resolved`
exist for lifecycle and display. Unknown is not downtime: monitor-side crashes,
regional network loss, or probe infrastructure failure must never be reported as
customer-site downtime.

## Detection Model

Jetmon uses a layered vocabulary so incidents can be explained consistently:

| Layer | Examples |
| --- | --- |
| Reachability | Domain, DNS resolution, network path, connection refused/timeouts. |
| Transport and security | TCP, TLS handshake, certificate expiry, deprecated TLS. |
| Infrastructure and edge | CDN, load balancer, WAF, suspension/default-host pages. |
| Application response | HTTP status, redirects, TTFB, response body rules. |
| WordPress and Jetpack | WP fatal/database/config pages, Jetpack probe echo, agent signals. |

Current local checks support `HEAD` or `GET`, redirect policies
`follow` / `alert` / `fail`, optional required and forbidden keywords, custom
headers, per-site timeouts, maintenance windows, and SSL observation.

Rollout policy stages:

1. `HEAD` + `legacy` for v1-compatible drop-in behavior.
2. `GET` + `simple_http` for visitor-like HTTP behavior without full body-rule
   sensitivity.
3. `GET` + `full` after production evidence supports richer detection.

## Bucket Ownership

Dynamic ownership uses `jetpack_monitor_hosts`. Hosts claim buckets inside
`SELECT ... FOR UPDATE` transactions, heartbeat each round, and absorb stale
host buckets after `BUCKET_HEARTBEAT_GRACE_SEC`.

Pinned migration mode (`PINNED_BUCKET_MIN/MAX` or legacy
`BUCKET_NO_MIN/MAX`) intentionally bypasses dynamic ownership so one v2 host can
replace one v1 host's exact range.

## Veriflier Model

V2 Monitors call v2 Verifliers over JSON/HTTP. The production contract is:

| Endpoint | Purpose |
| --- | --- |
| `/v2/check` | Execute a batch of checks and return per-target results. |
| `/v2/status` | Report service health and capacity. |

The proto schema remains a reference for a possible future transport. Legacy
Veriflier-compatible HTTP endpoints are lab/emergency tools, not the normal
production path.

Veriflier quorum may exclude unhealthy vantages, but must respect a configured
floor so one surviving Veriflier cannot confirm downtime by itself.

## Delivery Model

Webhooks and alert contacts both consume
`jetpack_monitor_event_transitions` through high-water marks. Workers create
delivery rows, claim pending rows transactionally, and retry on the shared
ladder:

```text
immediate -> 1m -> 5m -> 30m -> 1h -> 6h -> abandoned
```

`DELIVERY_OWNER_HOST` is a rollout guard for intentionally keeping delivery
single-owner. It is not required for correctness: row claims prevent duplicate
claiming when multiple workers are active.

## API And Dashboard

The REST API is internal only. A separate gateway owns customer auth,
tenant isolation, public error vocabulary, and plan gating. The API exposes
sites, events, transitions, SLA/timing stats, check history, rollout controls,
webhooks, alert contacts, identity, health, readiness, and OpenAPI.

The dashboard is operator-facing and should stay on loopback or behind trusted
operator-network controls.

## Load-Bearing Invariants

- `internal/eventstore` is the only writer for event rows and transition rows.
- Every event mutation writes a transition in the same transaction.
- Legacy projection updates must be in the same transaction as event mutations
  while `LEGACY_STATUS_PROJECTION_ENABLE` is enabled.
- Retry queue state persists across rounds; do not reset counters at round
  start.
- Dynamic bucket claiming happens only inside row-locking transactions.
- Maintenance windows suppress alerts, not checks or evidence writes.
- WPCOM circuit breaker queues are bounded; oldest pending notifications are
  dropped when full and must be logged.
- Monitor-side failures are `Unknown`, never customer downtime.

Accepted decision history is summarized in [decisions.md](decisions.md).

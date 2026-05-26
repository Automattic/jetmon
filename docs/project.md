# Jetmon Project And Architecture

Jetmon 2 is the Go rewrite of Jetpack's uptime monitor. It replaces the v1
Node.js/C++ Monitor and Qt/C++ Veriflier with Go binaries while keeping the
contracts needed for a reversible v1-to-v2 rollout.

Use this file for system shape and invariants. Use
[operations-guide.md](operations-guide.md) for production Docker runtime care,
[v1-to-v2-migration.md](v1-to-v2-migration.md) for rollout, and
[internal-api-reference.md](internal-api-reference.md) for API details.

## Compatibility Contract

Do not change these without explicit review:

| Surface | Rule |
| --- | --- |
| MySQL | Additive migrations only; `jetpack_monitor_sites` remains the v1-shaped identity/bucket/cadence/projection table. |
| WPCOM | Keep legacy status-change payload and auth path during drop-in rollout. |
| StatsD | Preserve `com.jetpack.jetmon.<hostname>` naming; add metrics, do not rename. |
| Config | Existing v1-style keys parse where retained; new keys are additive. |
| Signals | SIGINT drains; SIGHUP drains and re-execs through the container entrypoint. |
| Legacy stats | `sitespersec`, `sitesqueue`, `totals` remain available through the compatibility surface. |

Runtime logs go to stdout/stderr. V2 does not write v1-owned log files unless a
future consumer need is proven.

## Runtime Shape

```text
jetmon2
  orchestrator -> checker pool -> eventstore -> MySQL
       |              |              |
       |              |              + events, transitions, projection
       |              + HTTP checks, timing, TLS observations
       + retry queue, Veriflier RPC, WPCOM legacy notify

optional in the same process:
  REST API, dashboard, webhook worker, alert-contact worker

remote:
  veriflier2 over JSON/HTTP /v2/check and /v2/status
```

`jetmon-deliverer` is the extraction path for outbound dispatch. It uses the
same DB-backed delivery queues as the embedded workers.

## Package Map

| Path | Responsibility |
| --- | --- |
| `cmd/jetmon2` | Main binary, CLI, signals, startup. |
| `internal/orchestrator` | Scheduling, target fetch, retries, Veriflier confirmation, WPCOM notify. |
| `internal/checker` | HTTP checks and DNS/TCP/TLS/TTFB timing. |
| `internal/eventstore` | Sole writer for event and transition tables. |
| `internal/db` | MySQL, migrations, host heartbeat, bucket ownership. |
| `internal/api` / `internal/apikeys` | Internal REST API, auth, rate limits, idempotency, rollout API, OpenAPI. |
| `internal/dashboard` | Host and fleet dashboard. |
| `internal/webhooks` / `internal/alerting` | Raw signed webhooks and managed human alert contacts. |
| `internal/veriflier`, `veriflier2` | Monitor-to-Veriflier transport and remote worker. |

## Data Model

| Table | Purpose |
| --- | --- |
| `jetpack_monitor_sites` | v1 identity, bucket, cadence, compatibility projection. |
| `jetpack_monitor_site_check_config` | V2 check policy. |
| `jetpack_monitor_site_runtime` | Freshness, next check, SSL/runtime observations. |
| `jetpack_monitor_check_targets` | V2-derived scheduling state keyed by legacy row id. |
| `jetpack_monitor_hosts` | Dynamic bucket ownership and heartbeat. |
| `jetpack_monitor_process_health` | Per-process readiness, queues, buckets, and runtime health. |
| `jetpack_monitor_events` | Current incident projection. |
| `jetpack_monitor_event_transitions` | Append-only event mutation history. |
| `jetpack_monitor_check_history` | Per-check timing/status samples. |
| `jetpack_monitor_audit_log` | Operational actions and evidence. |
| `jetpack_monitor_site_safety_flags` | Unsafe URL and probe-safety state. |
| `jetpack_monitor_api_keys` | Internal API service tokens. |
| Veriflier tables | Vantage registry and agent heartbeat/discovery state. |
| rollout tables | Sessions, jobs, locks, confirmation tokens, comparisons, and staged policy rows. |
| delivery tables | Webhook and alert-contact registrations, progress, and deliveries. |

V2 side tables that describe one monitored endpoint use
`jetpack_monitor_sites.jetpack_monitor_site_id` / `source_site_id`.
`blog_id` remains the WPCOM/site identity and is not assumed unique. All
timestamps are UTC.

## Event Model

Events are the source of truth; legacy `site_status` is a rollout projection.

```text
jetpack_monitor_events             mutable current incident row
jetpack_monitor_event_transitions  append-only mutation history
```

`internal/eventstore` is the only writer for both tables. Every event mutation
must write exactly one transition row in the same transaction. While
`LEGACY_STATUS_PROJECTION_ENABLE` is on, the legacy projection must update in
that same transaction.

Event identity is idempotent:

```text
(blog_id, endpoint_id, check_type, discriminator)
```

Downtime lifecycle:

```text
Up -> Seems Down -> Down -> Resolved
         |
         +-> Resolved false_alarm
```

A first local failure opens `Seems Down`; Veriflier confirmation promotes the
same row to `Down`; recovery closes it with an explicit reason such as
`probe_cleared`, `verifier_cleared`, `false_alarm`, or `manual_override`.

## Severity, State, And Detection

Severity is ordered; state is the lifecycle/display label.

| Severity | State | Use |
| --- | --- | --- |
| 0 | `Up` | No active issue. |
| 1 | `Warning` | Low-risk warning such as SSL expiry. |
| 2 | `Degraded` | Partial impact or significant anomaly. |
| 3 | `Seems Down` | Local failure under retry or confirmation. |
| 4 | `Down` | Confirmed outage. |

`Unknown` is not downtime. Monitor crashes, probe infrastructure failures, or
regional network loss must never be reported as customer-site downtime.

Detection is layered:

| Layer | Examples |
| --- | --- |
| Reachability | Domain, DNS, network path, connection failures. |
| Transport/security | TCP, TLS, certificate expiry, deprecated TLS. |
| Infrastructure/edge | CDN, load balancer, WAF, default/suspension pages. |
| Application response | HTTP status, redirects, TTFB, body rules. |
| WordPress/Jetpack | WP fatal/database/config pages, Jetpack probe signals. |

Rollout policy stages are `HEAD` + `legacy`, then `GET` + `simple_http`, then
`GET` + `full`.

## Ownership, Verifliers, And Delivery

Dynamic bucket ownership uses `jetpack_monitor_hosts` with row-locking
transactions and heartbeats. Pinned migration mode bypasses dynamic ownership so
one v2 host can replace one v1 range.

V2 Monitors call v2 Verifliers over JSON/HTTP. Quorum may exclude unhealthy
vantages but must respect a configured floor so one Veriflier cannot confirm
downtime alone.

Webhooks and alert contacts consume event transitions through high-water marks,
create delivery rows, claim rows transactionally, and retry:

```text
immediate -> 1m -> 5m -> 30m -> 1h -> 6h -> abandoned
```

`DELIVERY_OWNER_HOST` is a rollout guard, not a correctness requirement.

## Load-Bearing Invariants

- Eventstore is the only writer for events and transitions.
- Every event mutation writes one transition in the same transaction.
- Retry queue state persists across rounds.
- Dynamic bucket claiming happens only inside row-locking transactions.
- Maintenance windows suppress alerts, not checks or evidence writes.
- WPCOM circuit breaker queues are bounded and drops are logged.
- Monitor-side failures are `Unknown`, never downtime.

Accepted decision history is summarized in [decisions.md](decisions.md).

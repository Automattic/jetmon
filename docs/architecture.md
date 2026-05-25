# Jetmon 2 Architecture

This document describes how the current Jetmon 2 system fits together at
runtime. It is intentionally higher level than the package internals and lower
level than the product overview.

Related references:

- [project.md](project.md) - project goals and operator-facing value
- [data-model.md](data-model.md) - table ownership and schema details
- [events.md](events.md) - incident lifecycle and transition semantics
- [internal-api-reference.md](internal-api-reference.md) - Monitor REST API
- [operations-guide.md](operations-guide.md) - production operations
- [adr/](adr/) - load-bearing design decisions

## Runtime Shape

Jetmon 2 has three deployable binaries:

- `jetmon2`: Monitor, scheduler, local checker, event writer, WPCOM notifier,
  dashboard, internal API, and optionally embedded delivery workers.
- `jetmon-deliverer`: standalone webhook and alert-contact delivery worker.
- `veriflier2`: remote checker used for independent downtime confirmation.

Typical production shape:

```text
                  MySQL
                   ^
                   |
                   v
          +------------------+
          |     jetmon2      |
          |------------------|
          | scheduler        |
          | checker pool     |---- HTTP/TLS/DNS probes ----> customer sites
          | eventstore       |
          | WPCOM client     |---- legacy notify ----------> WPCOM
          | API/dashboard    |
          +--------+---------+
                   |
                   | JSON over HTTP
                   v
             v2 Veriflier fleet
```

Multiple Monitor instances coordinate through MySQL. During normal v2 operation
they own non-overlapping bucket ranges through `jetpack_monitor_hosts`. During
API-controlled rollout, ranges are activated deliberately so v2 does not check a
range still owned by v1.

## Package Map

```text
cmd/jetmon2/              main Monitor binary and CLI commands
cmd/jetmon-deliverer/     standalone delivery-worker binary
internal/orchestrator/    scheduler, bucket coordination, retries, WPCOM flow
internal/checker/         HTTP/TLS/DNS checks and checker pool
internal/db/              MySQL access and migrations
internal/config/          JSON config loading, validation, hot reload
internal/eventstore/      authoritative event and transition writer
internal/audit/           operational audit log access
internal/wpcom/           legacy WPCOM notification client
internal/veriflier/       Monitor-to-Veriflier client/server transport
internal/api/             internal REST API, auth, rate limits, idempotency
internal/dashboard/       host and fleet dashboards
internal/deliverer/       shared delivery-worker wiring
internal/webhooks/        webhook registry and HMAC delivery
internal/alerting/        managed alert-contact delivery
internal/metrics/         StatsD client and v1-style stats output
veriflier2/               standalone Go Veriflier service
```

## Data Ownership

Jetmon 2 keeps the v1 site table compatible while moving v2 runtime state into
v2-owned tables.

- `jetpack_monitor_sites` remains the v1-shaped site identity, bucket, cadence,
  and legacy projection table.
- `jetpack_monitor_site_check_config` owns v2 check method/profile and rich
  per-site probe options.
- `jetpack_monitor_site_runtime` owns v2 freshness and SSL observation
  projection.
- `jetpack_monitor_events` and `jetpack_monitor_event_transitions` are the
  authoritative incident state.
- `jetpack_monitor_audit_log` is operational evidence, not state truth.
- `jetpack_monitor_check_history` stores method and timing samples.
- `jetpack_monitor_hosts` owns dynamic bucket coverage.
- `jetpack_monitor_process_health` feeds host/fleet dashboards.
- `jetpack_monitor_veriflier_vantages` and
  `jetpack_monitor_veriflier_agents` separate trusted quorum identity from
  observed Veriflier process telemetry.

See [data-model.md](data-model.md) for the full table list and rollout notes.

## Scheduler And Check Flow

The Monitor uses the streaming scheduler. The old round/page scheduler is no
longer a runtime option.

```text
load active targets from MySQL
spread targets across stable phases in the time wheel
pop due targets
submit bounded work to checker pool
collect results
write side effects in batches
publish stats and dashboard state
```

The scheduler keeps active targets in memory and uses stable phase spreading so
large fleets do not stampede at interval boundaries. Healthy checks avoid hot
per-check writes where possible; compatibility freshness projection is batched.

Each checker request performs the configured site probe:

- `HEAD` or `GET`
- `legacy`, `simple_http`, or `full` detection profile
- DNS/TCP/TLS/TTFB/total timing through `net/http/httptrace`
- redirect handling according to policy
- bounded body reads when needed
- keyword and forbidden-keyword checks in full GET profile
- TLS certificate expiry and deprecated TLS observation
- public-target safety checks before untrusted dials

HTTP responses below 400 are availability successes unless a full-profile rule
turns the result into a failure. TLS deprecation is advisory and does not open
customer downtime.

## Incident Flow

The event model is shared across local checks and Veriflier confirmation:

```text
local success
  -> close open Seems Down / Down events as recovered

first local failure
  -> open or update Seems Down
  -> keep retry state

enough local failures
  -> ask Verifliers

Verifliers confirm quorum
  -> promote same event to Down
  -> send legacy WPCOM notification when enabled

Verifliers disagree or quorum is not met
  -> close as false alarm

later local success
  -> close Down and send recovery notification when needed
```

Event mutations go through `internal/eventstore`. The event row, transition row,
and legacy projection update are written transactionally while
`LEGACY_STATUS_PROJECTION_ENABLE` is enabled. This prevents the v2 incident
state and v1 compatibility projection from drifting.

See [events.md](events.md) for lifecycle states, severity, resolution reasons,
and transition invariants.

## Veriflier Transport

The production Monitor-to-Veriflier transport is JSON over HTTP:

```text
GET  /v2/status
POST /v2/check
Authorization: Bearer <token>
```

`/v2/status` exposes service health, supported protocols, `vantage.id`,
`agent.id`, and capacity. `/v2/check` accepts batches of site probe requests and
returns typed per-request outcomes.

Batch isolation is a contract requirement. Unsafe URLs, unsupported per-site
probe options, checker panics, and omitted identified results are scoped to the
affected request as `unknown` / probe-safety or internal-error outcomes. They
must not fail healthy siblings in the same batch or shift results onto the
wrong site. Batch-level failures are reserved for malformed envelopes,
authentication failures, or Veriflier endpoint overload.

Important identity rules:

- `vantage.id` is the quorum vote identity.
- `agent.id` is the process identity for diagnostics.
- Multiple replicas behind one regional endpoint should share a `vantage.id`.
- Duplicate `vantage.id` replies are audited but count as one vote.
- Multi-Veriflier layouts keep a two-healthy-vantage floor unless
  `PEER_OFFLINE_LIMIT=1` is intentionally configured.

Verifliers execute accepted batches through a bounded executor. If local
capacity is exhausted, the Veriflier returns HTTP 503 for the batch. The Monitor
treats that endpoint as unhealthy/no-vote, never as customer-site downtime.

`veriflier2` can expose legacy-compatible `/check` and `/status` endpoints for
lab or emergency compatibility testing, but normal v2 production traffic should
use `/v2/check` and `/v2/status`. Original v1 Verifliers use the old TLS/custom
transport and are not supported v2 Monitor fallback targets.

`proto/veriflier.proto` remains a schema reference for a possible future
transport. Generated gRPC stubs are not required to build or deploy v2.

## Veriflier Discovery

Monitor discovery mode controls where Veriflier endpoints come from:

- `static`: use the configured `VERIFLIERS` list.
- `shadow`: read the trusted DB registry and report drift without changing
  traffic.
- `active`: use enabled usable registry rows, with fallback to static config if
  discovery is unavailable or empty.

Monitors poll `/v2/status` and write liveness/capacity data to
`jetpack_monitor_veriflier_agents`. Those telemetry rows do not create trusted
quorum votes by themselves; operators must explicitly approve vantages in
`jetpack_monitor_veriflier_vantages`.

## Bucket Ownership

Dynamic bucket ownership uses MySQL as the coordination point:

```text
jetpack_monitor_hosts
  host_id
  bucket_min
  bucket_max
  last_heartbeat
  status
```

Hosts heartbeat their ownership rows. When a host stops cleanly, it releases its
range. When a host disappears, peers treat stale heartbeat rows as expired and
claim uncovered ranges inside locked transactions. `SELECT ... FOR UPDATE`
prevents two hosts from claiming overlapping coverage.

During rollout, API-controlled range activation protects against v1 and v2
checking the same bucket range at the same time. After full v2 cutover, dynamic
ownership handles normal coverage and failover.

## Delivery Architecture

Webhook and alert-contact delivery are database-backed pull workers.

```text
event transition written
  -> delivery worker sees high-water mark
  -> matching webhooks / alert contacts create delivery rows
  -> workers claim due rows transactionally
  -> outbound POST/email/API send
  -> retry or mark delivered/abandoned
```

Embedded delivery can run inside an API-enabled `jetmon2` process. Standalone
delivery uses `jetmon-deliverer`. Delivery rows are claimed transactionally, so
multiple workers do not process the same pending row.

`DELIVERY_OWNER_HOST` is a rollout guard when operators want only one host to
run embedded delivery workers.

## API And Dashboard

The internal Monitor API lives under `/api/v1/...`. It is distinct from the
Veriflier `/v2/...` transport. API features include:

- Bearer-token authentication
- per-key rate limits
- in-process idempotency replay cache
- sites/events/SLA/read APIs
- rollout APIs
- webhook and alert-contact management
- monitor stats and dependency health

Because idempotency and rate-limit state are in-memory per process, a single
consumer should be pinned to one Monitor host. Do not fan out mutating API calls
across multiple Monitor hosts unless idempotency is moved to a shared durable
store.

Dashboards are unauthenticated operator surfaces and should stay bound to
loopback or a trusted management network:

```text
/          host dashboard
/fleet     fleet dashboard
/api/host  host JSON snapshot
/api/fleet fleet JSON snapshot
```

Fleet views read shared MySQL state; they do not scrape other hosts over HTTP.

## Metrics And Observability

StatsD keeps the v1 dotted prefix shape:

```text
com.jetpack.jetmon.<statsd_host_path>
```

Additive v2 metrics cover:

- scheduler queue, lag, dispatch, result, and backpressure pressure
- method/profile cohorts for staged rollout
- check timing and phase timing
- Veriflier latency, vote, and overload outcomes
- WPCOM attempts, retries, circuit-open queues, and final failures
- event lifecycle and false-alarm classes
- process RSS, Go runtime memory, file descriptors, goroutines, and threads
- SQL pool pressure for Monitor and deliverer processes

Authoritative incident state belongs in event tables. Operational explanation
belongs in audit, check history, telemetry reports, dashboards, and StatsD.

## Config And Signal Handling

Config is loaded from rendered JSON. SIGHUP and `jetmon2 reload` re-read config
under a lock; new settings apply to subsequent scheduler ticks or rebuilt
clients where supported.

Shutdown is graceful:

```text
SIGINT/SIGTERM
  -> cancel scheduler context
  -> mark host draining where applicable
  -> stop accepting new checker work
  -> wait for in-flight checks
  -> release bucket ownership
  -> exit
```

Hard process loss is handled by process supervision plus stale-heartbeat bucket
reclaim by surviving hosts.

## Checker Error Codes

| Code | Meaning |
| --- | --- |
| `ErrorNone` | Success |
| `ErrorTimeout` | Context deadline exceeded |
| `ErrorConnect` | TCP connection, DNS, or dial failure |
| `ErrorSSL` | TLS handshake or certificate error |
| `ErrorRedirect` | Redirect failure when policy is `fail` |
| `ErrorKeyword` | Required keyword missing, forbidden keyword present, or GET/full semantic body failure |
| `ErrorTLSExpired` | Certificate expired |
| `ErrorTLSDeprecated` | TLS 1.0 or 1.1 observed; advisory only |
| `ErrorBodyRead` | GET response body closed early or could not be read |
| `ErrorProbeSafety` | Probe blocked by public-target safety guard |
| `ErrorInternal` | Monitor-side checker panic recovered as an operational unknown |

`ErrorTLSDeprecated`, `ErrorProbeSafety`, and `ErrorInternal` do not open
customer downtime. `ErrorProbeSafety` is an operator safety finding, and
`ErrorInternal` is a Monitor-side fault to investigate; neither is a signal that
the customer site is down.

# Jetmon Development Guidelines

You are an expert Go developer with extensive knowledge about WordPress, enterprise-level web services, and high-performance network programming.

## Project Overview

Jetmon is a parallel HTTP uptime monitoring service that checks Jetpack websites at scale. Jetmon 2 is a complete rewrite of the original Node.js + C++ native addon service into a single Go binary. It retains full drop-in compatibility with all external interfaces — MySQL schema, WPCOM API payload, StatsD metric names, and log file format — while dramatically increasing concurrency, reducing memory usage, and eliminating the native addon compilation dependency.

The Veriflier is rewritten in Go as well, replacing the Qt C++ dependency. The protocol between Monitor and Verifliers is upgraded from custom HTTPS to gRPC.

See `PROJECT.md` for the full project description, feature list, and performance benefit estimates.

## Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                       jetmon2 (single binary)                        │
│                                                                      │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────┐                  │
│  │ Orchestrator│  │ Check Pool  │  │  Veriflier   │                  │
│  │  goroutine  │  │ (goroutines)│  │  transport   │                  │
│  └──────┬──────┘  └──────┬──────┘  └──────┬───────┘                  │
│         │                │                │                          │
│  ┌──────┴────────────────┴────────────────┴───────┐                  │
│  │                 Internal channels              │                  │
│  └─────────────────────┬──────────────────────────┘                  │
│                        │                                             │
│   ┌────────────────────┴────────────────────┐                        │
│   │   eventstore (jetmon_events +           │                        │
│   │    jetmon_event_transitions writes)     │                        │
│   └────────────────────┬────────────────────┘                        │
│                        │                                             │
│   ┌────────────┐  ┌────┴────────────┐  ┌──────────────────────┐      │
│   │  REST API  │  │  Webhook        │  │  Alerting            │      │
│   │  /api/v1/  │  │  delivery       │  │  delivery            │      │
│   │  + auth +  │  │  worker         │  │  worker              │      │
│   │  ratelimit │  │  (HMAC POST)    │  │  (email/PD/Slack/Tm) │      │
│   └─────┬──────┘  └────────┬────────┘  └──────────┬───────────┘      │
│         │                  │                      │                  │
│   ┌─────┴──────┐    ┌──────┴──────────┐  ┌────────┴──────────────┐   │
│   │  Operator  │    │  Webhook        │  │  Alert contact        │   │
│   │  dashboard │    │  receivers      │  │  destinations         │   │
│   │  (SSE)     │    │  (HTTPS)        │  │  (HTTPS / SMTP / API) │   │
│   └────────────┘    └─────────────────┘  └───────────────────────┘   │
└────────────┬──────────────────────────┬──────────────────────────────┘
             │                          │
          MySQL                    WPCOM API
          StatsD                   (legacy notification path,
          Log files                 still active alongside
                                    alert contacts)
```

**Orchestrator goroutine** (`internal/orchestrator/`): Fetches site batches from MySQL, dispatches work to the check pool via channels, processes results, manages the local retry queue, coordinates Veriflier confirmation requests, and emits WPCOM legacy notifications. Owns all DB access for site state and writes events through `eventstore`.

**Check Pool** (`internal/checker/`): A bounded goroutine pool that performs HTTP checks using Go's `net/http` and `net/http/httptrace`. Records DNS, TCP connect, TLS handshake, and TTFB timings for every check. Pool size auto-scales against queue depth within configured min/max bounds.

**Eventstore** (`internal/eventstore/`): The single writer for `jetmon_events` and `jetmon_event_transitions`. Every status / severity / state change is written transactionally so the event row's projection and the transition log can never disagree. Both downstream workers (webhooks, alerting) consume `jetmon_event_transitions` via a high-water mark.

**REST API** (`internal/api/`): The internal API surface (`/api/v1/...`) used by the gateway, alerting workers, dashboards, and CI tooling. Per-consumer Bearer-token auth (`internal/apikeys/`), per-key rate limiting, Stripe-style idempotency keys on POSTs. Sites CRUD, events list / single / transitions, SLA stats, webhooks CRUD, alert-contacts CRUD, manual delivery retry.

**Webhook delivery worker** (`internal/webhooks/`): Polls `jetmon_event_transitions`, matches each new transition against active webhooks (event-type + site + state filters), and POSTs HMAC-signed payloads to consumer URLs. Retry ladder 1m / 5m / 30m / 1h / 6h then abandon. Per-webhook in-flight cap and shared dispatch pool.

**Alerting delivery worker** (`internal/alerting/`): Same shape as the webhook worker but for managed channels — email (via `wpcom`/`smtp`/`stub` senders), PagerDuty Events API v2, Slack incoming webhooks, Microsoft Teams. Filter is simpler (`site_filter` + `min_severity`); per-contact `max_per_hour` rate cap absorbs pager storms. Send-test endpoint exercises the same dispatch path without requiring a real event.

**Current delivery-owner constraint:** In the single-binary v2 deployment, `API_PORT > 0` starts the API server plus webhook and alert-contact delivery workers. Run that on only one active `jetmon2` instance per database cluster; additional monitor hosts should leave `API_PORT = 0` until delivery claiming moves to transactional row locks or the deliverer binary is split out.

**Veriflier transport** (`internal/veriflier/`): JSON-over-HTTP client/server for Monitor↔Veriflier communication. Replaces the previous SSL server and custom HTTPS protocol. Run `make generate` to swap in generated gRPC stubs once protoc is set up.

**Veriflier** (`veriflier2/`): Standalone Go binary deployed at remote locations. Receives check batches from the Monitor, performs HTTP checks, and returns results. Replaces the Qt C++ Veriflier.

**Future shape:** the API server, webhook worker, and alerting worker are independently scalable concerns and the natural target for the multi-binary split tracked in `ROADMAP.md`. Today they coexist in `jetmon2` and the MySQL schema is the bus between them; tomorrow the deliverer becomes its own binary handling all outbound dispatch (webhooks + alerting + WPCOM legacy migrated behind it).

## Key Files

| Path | Purpose |
|------|---------|
| `cmd/jetmon2/main.go` | Binary entry point, signal handling, startup |
| `internal/orchestrator/` | Round scheduling, DB fetch, work dispatch, WPCOM notifications |
| `internal/checker/` | Goroutine pool, HTTP checks, httptrace timing |
| `internal/veriflier/` | JSON-over-HTTP client/server for Veriflier communication |
| `internal/db/` | MySQL access, `jetmon_hosts` heartbeat, connection pooling |
| `internal/config/` | Config loading, SIGHUP hot-reload |
| `internal/metrics/` | StatsD client, stats file writer |
| `internal/wpcom/` | WPCOM API client, circuit breaker |
| `internal/audit/` | Operational log writes to `jetmon_audit_log` (WPCOM, retries, verifier RPCs, config reloads) |
| `internal/eventstore/` | Event-sourced site state — manages `jetmon_events` + `jetmon_event_transitions` writes in single transactions |
| `internal/api/` | Internal REST API server (`/api/v1/...`) — auth, rate limiting, idempotency, sites/events/SLA/webhooks/alert-contacts handlers |
| `internal/apikeys/` | API key registry, sha256-hashed at rest; `./jetmon2 keys` CLI |
| `internal/webhooks/` | Webhook registry + delivery worker — outbound HMAC-signed POSTs of event transitions, retry ladder 1m/5m/30m/1h/6h |
| `internal/alerting/` | Alert contact registry + delivery worker — managed channels (email/PagerDuty/Slack/Teams) with site_filter + severity gate + per-hour rate cap |
| `internal/dashboard/` | Operator dashboard, SSE handler |
| `veriflier2/` | Go Veriflier binary |
| `API.md` | Internal REST API reference (auth, all endpoints, payload shapes) |
| `ROADMAP.md` | Deferred features and architectural roadmap (multi-binary split, public-API path) |
| `docs/adr/` | Architecture Decision Records — load-bearing decisions ("why is X like this") with context, decision, and consequences |
| `PROJECT.md` | Full project description and feature specification |

## Build and Run

```bash
# Docker development (recommended)
cd docker && docker compose up -d         # Start all services
docker compose up --build                 # Rebuild binary and start
docker compose down                       # Stop services
docker compose down -v                    # Stop and remove volumes (fresh start)

# Build binary directly
go build ./cmd/jetmon2/

# Run tests
go test ./...
go test -race ./...

# Run with race detector
go run -race ./cmd/jetmon2/

# Validate config
./jetmon2 validate-config

# CLI subcommands
./jetmon2 version
./jetmon2 migrate
./jetmon2 status
./jetmon2 audit --blog-id 12345 --since 2h
./jetmon2 drain
./jetmon2 reload
```

## Configuration

Copy `config/config-sample.json` to `config/config.json`. All keys from the original Jetmon are honoured; new keys are additive. Send SIGHUP to hot-reload config without restarting.

**Existing keys (unchanged behaviour):**
- `NUM_WORKERS`: Goroutine pool size (replaces worker process count)
- `NUM_TO_PROCESS`: Parallel checks per pool slot
- `MIN_TIME_BETWEEN_ROUNDS_SEC`: Minimum interval between check rounds
- `NET_COMMS_TIMEOUT`: Default per-check HTTP timeout in seconds
- `PEER_OFFLINE_LIMIT`: Veriflier agreements required to confirm downtime
- `WORKER_MAX_MEM_MB`: RSS threshold that triggers pool drain (replaces worker recycling)

**New keys:**
- `BUCKET_TOTAL`: Total bucket range (e.g. 1000); replaces static `BUCKET_NO_MIN/MAX`
- `BUCKET_TARGET`: Maximum buckets this host should own
- `BUCKET_HEARTBEAT_GRACE_SEC`: Seconds before an unresponsive host's buckets are reclaimed (suggested: 2× round time)
- `ALERT_COOLDOWN_MINUTES`: Default cooldown between repeated alerts for the same site
- `LEGACY_STATUS_PROJECTION_ENABLE`: Keep v1 `site_status` / `last_status_change` projection updated during shadow-v2-state migration
- `LOG_FORMAT`: `text` (default, drop-in compatible) or `json` (structured logging)
- `DASHBOARD_PORT`: Internal port for the operator dashboard (0 to disable)
- `DEBUG_PORT`: localhost-only pprof port, default 6060 (0 to disable; never exposed remotely)

See `config/config.readme` for the full option reference.

## Drop-in Compatibility Requirements

These interfaces must remain identical to the original Jetmon. Do not change them without explicit discussion:

| Interface | Constraint |
|-----------|-----------|
| MySQL schema | Read same columns; additive migrations only |
| WPCOM notification payload | Same JSON structure and field names |
| StatsD metric names | Same dotted paths; new metrics may be added |
| Log file paths and format | `logs/jetmon.log`, `logs/status-change.log` |
| `stats/` file outputs | `sitespersec`, `sitesqueue`, `totals` — same format |
| `config/config.json` keys | All existing keys honoured |
| SIGHUP config reload | Same behaviour |
| SIGINT graceful shutdown | Same behaviour |

## Site Status Values

- `0` SITE_DOWN: Local checks failed, retry/verification in progress
- `1` SITE_RUNNING: Confirmed online
- `2` SITE_CONFIRMED_DOWN: Verified down by Verifliers, WPCOM notified

## Monitoring Behaviour

**Check Process:**
- Default timeout: `NET_COMMS_TIMEOUT` seconds (configurable per-site via `timeout_seconds` column)
- HTTP response code < 400 is success
- Redirect policy configurable per site: `follow` (default), `alert` (warn on chain change), `fail`
- Max redirects when following: 10
- Keyword check: if `check_keyword` is set, GET the body and confirm the string is present
- User-Agent: `jetmon/2.0 (Jetpack Site Uptime Monitor by WordPress.com)`
- Per-site custom headers merged from `custom_headers` JSON column

**Timing Breakdown (via `net/http/httptrace`):**
Every check records: DNS lookup, TCP connect, TLS handshake, request sent, first response byte (TTFB). All six timings are stored in the audit log and emitted as StatsD metrics. The composite RTT is retained for backwards compatibility.

**SSL Monitoring:**
Every HTTPS check inspects `tls.ConnectionState` for:
- Certificate `NotAfter` — alerts at 30, 14, and 7 days before expiry
- TLS version — flags TLS 1.0/1.1 as deprecated
- Cipher suite — recorded in audit log

**Downtime Verification:**
1. Local check fails → open a `Seems Down` event (severity 3) and enter the local retry queue. The event opens on the **first** failure so `started_at` reflects the actual incident start. Subsequent failures during retry are no-ops on the events table (idempotent dedup).
2. After `NUM_OF_CHECKS` local failures → dispatch to Verifliers (event stays Seems Down)
3. `PEER_OFFLINE_LIMIT` Veriflier agreements required to confirm
4. Verifier outcomes:
   - **Confirms** → Promote event to `Down` (severity 4) with `reason = verifier_confirmed`. WPCOM notification via same payload as original.
   - **Disagrees** → Close event with `resolution_reason = false_alarm`.
5. Recovery (any successful probe while an event is open):
   - From `Seems Down` → close with `resolution_reason = probe_cleared`.
   - From `Down` → close with `resolution_reason = verifier_cleared` and send recovery notification.

Shadow-v2-state migration keeps incidents authoritative in `jetmon_events` + `jetmon_event_transitions` while `jetpack_monitor_sites` remains the legacy site/config table. When `LEGACY_STATUS_PROJECTION_ENABLE` is true, the `jetpack_monitor_sites.site_status` / `last_status_change` projection is updated in the same transaction as every event mutation (no drift). v1 mapping: open Seems Down → `site_status = SITE_DOWN (0)`; promoted to Down → `site_status = SITE_CONFIRMED_DOWN (2)`; closed → `site_status = SITE_RUNNING (1)`. After legacy readers move to the v2 API/event tables, this projection can be disabled.

**Alert Deduplication:**
After an alert fires, subsequent alerts for the same site are suppressed for `alert_cooldown_minutes`. Suppression is recorded in the audit log.

**Status Change Types (unchanged):**
- `server`: 5xx response
- `blocked`: 403 response
- `client`: 4xx other than 403
- `https`: SSL/TLS problems
- `intermittent`: Request timeout
- `redirect`: Redirect policy failure
- `success`: Site recovered

## Database Schema

Sites are stored in `jetpack_monitor_sites` with bucket-based sharding. The `bucket_no` field enables horizontal scaling. New additive columns introduced by Jetmon 2:

| Column | Type | Purpose |
|--------|------|---------|
| `ssl_expiry_date` | DATE NULL | Updated each HTTPS check |
| `check_keyword` | VARCHAR(500) NULL | String to verify in response body |
| `maintenance_start` | DATETIME NULL | Maintenance window start |
| `maintenance_end` | DATETIME NULL | Maintenance window end |
| `custom_headers` | JSON NULL | Per-site request headers |
| `timeout_seconds` | TINYINT NULL | Per-site timeout override |
| `redirect_policy` | ENUM NULL | `follow`, `alert`, `fail` |
| `alert_cooldown_minutes` | SMALLINT NULL | Per-site cooldown override |

New tables introduced by Jetmon 2:

| Table | Purpose |
|-------|---------|
| `jetmon_hosts` | MySQL-coordinated bucket ownership and heartbeat |
| `jetmon_events` | Current state of every incident — one row per `(blog_id, endpoint_id, check_type, discriminator)` while open; mutable until `ended_at` is set, then frozen |
| `jetmon_event_transitions` | Append-only history of every mutation to `jetmon_events` (open, severity change, state change, cause link, close) |
| `jetmon_audit_log` | Operational trail — WPCOM notifications, retry dispatch, verifier RPCs, alert/maintenance suppression, config reloads. Site-state changes do **not** flow through here |
| `jetmon_check_history` | RTT and timing samples for trending |
| `jetmon_false_positives` | Veriflier non-confirmation events |

## Multi-Host Bucket Coordination

Jetmon 2 replaces static `BUCKET_NO_MIN/MAX` config with runtime bucket ownership via the `jetmon_hosts` table. On startup, each instance claims unclaimed or expired bucket ranges using `SELECT ... FOR UPDATE` transactions. A heartbeat query runs each round; hosts with stale heartbeats (older than `BUCKET_HEARTBEAT_GRACE_SEC`) have their buckets absorbed by surviving peers. On SIGINT, the instance releases its buckets immediately.

This enables zero-config horizontal scaling (spin up a host, it claims buckets) and self-healing coverage (a failed host's buckets are absorbed within one grace period) without a cluster orchestrator.

## Metrics

StatsD metrics retain the same prefix and dotted path format as Jetmon 1: `com.jetpack.jetmon.<hostname>`. New metrics added by Jetmon 2 follow the same naming convention and are additive.

StatsD is the primary metrics transport. No Prometheus endpoint is provided.

## WPCOM Integration

Jetmon notifies WPCOM of status changes via the same JSON payload format as Jetmon 1. The `jetpack_monitor_site_status_change` hook on WPCOM is triggered for consumers (notifications, Activity Log, etc.). A circuit breaker protects against WPCOM API failures: after N consecutive failures the circuit opens, pending notifications are queued in memory, and retries are attempted on a backoff schedule.

## Production Deployment

Jetmon runs on production hosts managed by the Systems team. To deploy changes:
1. Test locally using the Docker environment (`go test ./...`, manual Docker verification)
2. Create a PR and request a Systems Request with PR links
3. Systems team performs a rolling update: one host at a time, SIGINT → drain → deploy binary → restart
4. Surviving hosts absorb the draining host's buckets during each update window

Rolling updates require no simultaneous restart of all hosts and leave no sites unchecked during the update.

## Architectural Decisions — Event and State Model

These decisions govern how Jetmon models site state. They must be maintained consistently across all changes. Full design rationale is in [`TAXONOMY.md`](TAXONOMY.md) (Parts 2–3) and [`EVENTS.md`](EVENTS.md).

**Events are the source of truth.** Site status is event-sourced across two tables: `jetmon_events` (one row per incident, holding the current severity/state/metadata) and `jetmon_event_transitions` (append-only history of every mutation). The site row stores a denormalized projection for read performance. Update events, transitions, and the projection in the same transaction — they must not drift. If the projection is ever suspect, rebuild it from the events tables.

**Every event mutation writes a transition row in the same transaction.** Open, severity bump, state change, cause-link change, close — no carve-outs. The `eventstore` package is the only writer for `jetmon_events` and `jetmon_event_transitions`; external callers must go through it. This keeps the invariant testable with one integration test surface.

**Severity and state are separate fields.** Severity is numeric — use it for ordering, thresholds, and rollup. State is a human-readable label — use it for display and lifecycle transitions. A live event's severity can be updated in place without changing its state (a worsening degradation is not a new kind of problem).

**"Seems Down" is a first-class lifecycle state.** Between first probe failure and verifier confirmation, a site is Seems Down. It is not an implementation detail — dashboards show it, alert rules can key off it. The lifecycle is:
```
Up → Seems Down → Down → Resolved
         ↓
         Up (false alarm)
```

**Events update in place on severity change.** When a Seems Down event is verifier-confirmed to Down, update the same event row — do not close and open a new one. The event's `started_at` stays at first-failure time. Incident duration is honest: it starts from first failure, not from confirmation.

**Event identity is idempotent.** The same underlying failure must not produce duplicate events. Deduplication lives in the shared probe runner, not in individual check types. Key events by `(site_id, endpoint_id, check_type, [discriminator])` so repeated detection of the same condition updates the existing open event.

**Resolution reason is required on close.** When an event closes, record why: `verifier_cleared`, `false_alarm`, `manual_override`, `auto_timeout`. Don't just set `end_timestamp` — capture the cause. This affects uptime calculations and report accuracy.

**Causal links are separate from hierarchical rollup.** An endpoint event rolling up to site level is a hierarchy relationship. A Layer-3 event caused by a Layer-1 failure is a causal relationship. Store these in separate structures. Conflating them creates bugs where dismissing a cause accidentally dismisses a rollup.

**Unknown is not downtime.** If the probe crashes, a region loses network, or the Jetpack agent stops reporting, the result is Unknown — not Down. Monitor-side failures must never be reported as customer-site downtime.

## Known Pitfalls

**Retry Queue Persistence:** The local retry queue must persist between rounds. Do not flush it at round start — a site must accumulate `NUM_OF_CHECKS` failures before Veriflier escalation, and flushing resets that counter, preventing downtime confirmation.

**Bucket Claiming Races:** The `SELECT ... FOR UPDATE` transaction on `jetmon_hosts` is the only safe way to claim buckets. Do not claim buckets outside a transaction — two hosts starting simultaneously will both see the same unclaimed range and must not both write it.

**Circuit Breaker Floor:** The WPCOM API circuit breaker queue is bounded. If the queue fills, the oldest pending notifications are dropped with an error log. Monitor the circuit breaker state in the operator dashboard during any WPCOM API incident.

**Veriflier Quorum Floor:** When Verifliers are marked unhealthy and excluded, `PEER_OFFLINE_LIMIT` adjusts dynamically, but there is a configured floor to prevent a single healthy Veriflier from confirming downtime alone. Ensure the floor is set appropriately for the number of deployed Verifliers.

**Single Active Delivery Owner:** Webhook and alert-contact workers currently soft-lock delivery rows inside one process. Do not run multiple active API/delivery owners against the same database unless `ClaimReady` has been upgraded to transactional `SELECT ... FOR UPDATE SKIP LOCKED`; otherwise duplicate outbound deliveries are possible.

**Maintenance Windows:** Checks continue during a maintenance window and data is recorded in the audit log, but no alerts fire. Verify that `maintenance_end` is correctly set — an open-ended maintenance window silently suppresses all alerts for that site indefinitely.

**Memory Pressure Drain:** If RSS exceeds the configured threshold, the goroutine pool shrinks by 10% via graceful drain. This reduces throughput temporarily. If memory pressure is sustained, investigate for goroutine leaks using the pprof endpoint at `http://localhost:<DEBUG_PORT>/debug/pprof/` (localhost only) before increasing `WORKER_MAX_MEM_MB`.

# Jetmon Development Guidelines

You are an expert Go developer with extensive knowledge about WordPress, enterprise-level web services, and high-performance network programming.

## Project Overview

Jetmon is a parallel HTTP uptime monitoring service that checks Jetpack websites at scale. Jetmon 2 is a complete rewrite of the original Node.js + C++ native addon service into a single Go binary. It retains full drop-in compatibility with all external interfaces — MySQL schema, WPCOM API payload, StatsD metric names, and log file format — while dramatically increasing concurrency, reducing memory usage, and eliminating the native addon compilation dependency.

The Veriflier is rewritten in Go as well, replacing the Qt C++ dependency. The protocol between Monitor and Verifliers is upgraded from custom HTTPS to gRPC.

See `PROJECT.md` for the full project description, feature list, and performance benefit estimates.

## Architecture

```
┌───────────────────────────────────────────────────────┐
│                  jetmon2 (single binary)              │
│                                                       │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────┐   │
│  │ Orchestrator│  │ Check Pool  │  │  gRPC Server │   │
│  │  goroutine  │  │ (goroutines)│  │  (Veriflier) │   │
│  └──────┬──────┘  └──────┬──────┘  └──────┬───────┘   │
│         │                │                │           │
│  ┌──────┴────────────────┴────────────────┴───────┐   │
│  │                 Internal channels              │   │
│  └────────────────────────────────────────────────┘   │
└────────────┬──────────────────────────┬───────────────┘
             │                          │
          MySQL                    WPCOM API
          StatsD                   (unchanged)
          Log files
          (all unchanged)
```

**Orchestrator goroutine** (`internal/orchestrator/`): Fetches site batches from MySQL, dispatches work to the check pool via channels, processes results, manages the local retry queue, coordinates Veriflier confirmation requests, and sends WPCOM status-change notifications. Owns all DB access and all outbound WPCOM calls.

**Check Pool** (`internal/checker/`): A bounded goroutine pool that performs HTTP checks using Go's `net/http` and `net/http/httptrace`. Records DNS, TCP connect, TLS handshake, and TTFB timings for every check. Pool size auto-scales against queue depth within configured min/max bounds. No process spawning — adding a worker is a channel send.

**Veriflier transport** (`internal/veriflier/`): JSON-over-HTTP client/server for Monitor↔Veriflier communication. Replaces the previous SSL server and custom HTTPS protocol. Run `make generate` to swap in generated gRPC stubs once protoc is set up.

**Veriflier** (`veriflier2/`): Standalone Go binary deployed at remote locations. Receives check batches from the Monitor via gRPC, performs HTTP checks, and returns results. Replaces the Qt C++ Veriflier.

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
| `internal/audit/` | Audit log writes to `jetmon_audit_log` |
| `internal/dashboard/` | Operator dashboard, SSE handler |
| `veriflier2/` | Go Veriflier binary |
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
1. Local check fails → enter local retry queue
2. After `NUM_OF_CHECKS` local failures → dispatch to Verifliers
3. `PEER_OFFLINE_LIMIT` Veriflier agreements required to confirm
4. Confirmed down → WPCOM notification via same payload as original

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
| `jetmon_audit_log` | Full event history per site |
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

**Events are the source of truth.** Site status is event-sourced. The event log is canonical; the site row stores a denormalized projection for read performance. Update both in the same transaction — they must not drift. If the projection is ever suspect, rebuild it from the log.

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

**Maintenance Windows:** Checks continue during a maintenance window and data is recorded in the audit log, but no alerts fire. Verify that `maintenance_end` is correctly set — an open-ended maintenance window silently suppresses all alerts for that site indefinitely.

**Memory Pressure Drain:** If RSS exceeds the configured threshold, the goroutine pool shrinks by 10% via graceful drain. This reduces throughput temporarily. If memory pressure is sustained, investigate for goroutine leaks using the pprof endpoint at `http://localhost:<DEBUG_PORT>/debug/pprof/` (localhost only) before increasing `WORKER_MAX_MEM_MB`.

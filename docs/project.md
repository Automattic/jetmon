# Jetmon 2 — Project Description

## Executive Summary

Jetmon 2 is a complete rewrite of the Jetmon uptime monitoring service, replacing the Node.js + C++ native addon architecture with a single Go binary. The rewrite retains full compatibility with existing external interfaces — MySQL schema, WPCOM API notification format, StatsD metric names, and log file structure — making it a genuine drop-in replacement on production infrastructure. Internally, the process-per-worker model is replaced by a goroutine pool, eliminating the overhead of forked processes and native addon compilation while dramatically increasing the number of concurrent checks per host. The rewrite is accompanied by a comprehensive tooling suite designed to make the system easier to test, deploy, operate, and interrogate.

---

## Why Go

The current architecture uses forked Node.js processes (8–16MB RSS each at startup, 53MB limit before recycling) as workers, plus a compiled C++ addon to escape Node's event loop for blocking network I/O. Go eliminates both constraints:

- **Goroutines** start at ~4KB of stack and grow on demand, making 50,000 concurrent checks on a single host practical without the memory overhead of forked processes or libuv thread pools
- **`net/http` and `crypto/tls`** are first-class stdlib packages — no native addon, no node-gyp, no compilation step during deployment
- **`net/http/httptrace`** provides DNS, TCP, TLS, and TTFB timing hooks as separate measurements within each check, for free
- **Single static binary** deployment with no runtime dependencies, no `node_modules`, and no addon rebuild on Node.js version upgrades
- **Built-in profiling** via `pprof`, race detector via `go test -race`, and a mature testing ecosystem
- **Graceful goroutine lifecycle management** replaces the fragile worker spawn/recycle/evaporate lifecycle

The Veriflier is rewritten in Go as well, replacing the Qt C++ dependency with a lightweight Go HTTP service. The v2 production Monitor-to-Veriflier transport is JSON-over-HTTP on the configured Veriflier port. The proto contract is kept in `proto/` as a schema reference for a possible future transport, not as the v2 deployment path.

---

## Architecture Overview

```
┌──────────────────────────────────────────────────────┐
│                       jetmon2                        │
│                                                      │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────┐  │
│  │ Orchestrator│  │ Check Pool  │  │  Veriflier   │  │
│  │  goroutine  │  │ (goroutines)│  │  transport   │  │
│  └──────┬──────┘  └──────┬──────┘  └──────┬───────┘  │
│         │                │                │          │
│  ┌──────┴────────────────┴────────────────┴───────┐  │
│  │                 Internal channels              │  │
│  └────────────────────────────────────────────────┘  │
└────────────┬──────────────────────────┬──────────────┘
             │                          │
          MySQL                    WPCOM API
          StatsD                   (unchanged)
          Log files
          (all unchanged)
```

The monitor process replaces the master/worker/SSL-cluster process tree. Concurrency is managed through Go channels and a bounded goroutine worker pool. The orchestrator goroutine owns DB access and WPCOM notifications. The check pool goroutines own HTTP connections. The Veriflier client/server code handles remote confirmation batches over JSON-over-HTTP and is isolated behind `internal/veriflier/`. Outbound webhook and alert-contact delivery can run embedded in one API-enabled `jetmon2` process today, or through the standalone `jetmon-deliverer` entry point as that responsibility moves toward its own deployable process.

---

## Benefits of the Rewrite

### Memory

The current architecture forks Node.js worker processes that start at 8–16MB RSS and are recycled once they reach 53MB. With a typical deployment of 8–16 workers, the process tree consumes 240–850MB of resident memory just for worker overhead, before any check data is counted. The master process, SSL server, and associated IPC buffers add further overhead.

Jetmon 2 runs as a single process. Go goroutines start at 4KB of stack and grow on demand. A pool of 1,000 concurrent goroutines costs roughly 4MB of stack. Total process RSS for an equivalent workload is estimated at 50–150MB — a **75–90% reduction** in memory consumption per host.

### Concurrent Checks

Current concurrency is bounded by the number of worker processes. Each worker is a single-threaded Node.js process; even with the C++ addon offloading blocking I/O to a thread pool, practical concurrency per host is in the low hundreds. Scaling beyond that requires adding more hosts and manually partitioning bucket ranges.

Go's goroutine scheduler makes 10,000+ concurrent in-flight checks on a single host practical with no additional configuration. At a conservative network timeout of 10 seconds and average site response time of 200ms, a pool of 1,000 goroutines sustains approximately 5,000 check completions per second. This represents an estimated **10–50× increase in concurrent checks per host**, meaning significantly fewer hosts are required to cover the same fleet.

### Throughput

The current architecture crosses a process boundary on every unit of work: the master dispatches via IPC, the worker receives, processes, and replies via IPC, and the master aggregates. Each crossing involves serialisation, a context switch, and V8 event loop scheduling on both ends.

Jetmon 2 replaces all IPC with Go channel sends, which are in-process and order-of-magnitude cheaper. V8 GC pauses, which can delay check scheduling and RTT measurement in the current system, are eliminated. Estimated throughput improvement: **3–10× more sites checked per second per host** under equivalent conditions.

### Check Scheduling Accuracy

The current system uses `setTimeout` and `setInterval` for round scheduling. These are subject to V8 event loop delay — a busy event loop can delay a scheduled callback by tens to hundreds of milliseconds, introducing jitter into check timing and RTT measurements.

Go's `time.Ticker` fires with OS-level timer precision. RTT measurements from `net/http/httptrace` are taken inside the HTTP stack with no event loop between the measurement point and the timer, making them more accurate and consistent.

### Deployment Speed

Current deployment requires `npm install`, a `node-gyp` rebuild of the native C++ addon (which must match the installed Node.js version), and a coordinated process restart. A failed addon compilation blocks deployment entirely.

Jetmon 2 deploys as static Go binaries with no runtime language dependencies. The conservative v2 monitor deployment is: copy `jetmon2`, run migrations, and `systemctl restart jetmon2`. Total deployment time drops from several minutes to under 30 seconds. There is no compilation step on the target host and no dependency on a matching Node.js version.

### Mean Time to Recovery

A worker process crash in the current system requires the master to detect the exit, spawn a replacement, and wait for the new process to initialise — a sequence that takes several seconds and leaves that worker's in-flight checks unresolved.

In Jetmon 2, a panicking goroutine is recovered by a deferred handler, the result is counted as an error, and a replacement goroutine is immediately spawned from the pool — recovery is in the low milliseconds. For a full process crash, systemd restarts the binary; with Go's fast startup, the process is accepting work again in under 2 seconds.

### Operational Complexity

The current system requires managing Node.js version compatibility, native addon compilation, npm dependency trees, and the fragile worker spawn/recycle lifecycle. The `node_modules` directory and compiled `.node` addon must be present and consistent on every host.

Jetmon 2 eliminates all of this. There is one artifact to manage: the Go binary. It carries its own runtime, has no external dependencies, and produces a reproducible build from `go build`. The `node-gyp`, `npm`, and Node.js version management concerns disappear entirely.

---

## Drop-in Compatibility Requirements

These interfaces must remain byte-for-byte identical to the current implementation:

| Interface | Constraint |
|-----------|-----------|
| MySQL schema | Read same columns; additive migrations (new columns, new tables) are permitted |
| WPCOM notification payload | Same JSON structure and field names |
| StatsD metric names | Same dotted paths; new metrics may be added |
| Log file paths and format | `logs/jetmon.log`, `logs/status-change.log`; same line format |
| `stats/` file outputs | `sitespersec`, `sitesqueue`, `totals` — same format |
| `config/config.json` keys | All existing keys honoured; new keys additive |
| SIGHUP config reload | Same signal handling behaviour |
| SIGINT graceful shutdown | Same behaviour |

---

## New Features — Competitive Parity

These features address the most significant gaps against competing solutions and are scoped to be implementable without new server infrastructure.

**SSL Certificate Expiry Monitoring**
During each HTTPS check, inspect the peer certificate chain via Go's `tls.ConnectionState`. Extract `NotAfter` from the leaf certificate and store it in a new `ssl_expiry_date` column on `jetpack_monitor_sites`. Emit alerts at configurable thresholds (30, 14, and 7 days before expiry) through the same WPCOM notification path as downtime alerts. Zero additional network requests — the data is present in every existing HTTPS connection.

**GET-Based Site Checks**
Jetmon 1's HEAD-only verification was a major source of customer-visible false positives and false negatives because many production stacks block, special-case, or incorrectly implement HEAD. Jetmon 2 uses GET requests for local monitor checks and Veriflier checks so uptime decisions are based on the same class of request a browser and customer-facing uptime product normally make.

**Keyword / Content Checking**
For sites with a `check_keyword` value set in the database, perform a GET request and search the response body for the configured string. A missing keyword on an otherwise-200 response counts as a failure and enters the same retry and confirmation pipeline as an HTTP error. Builds directly on the GET request mode used by v2 checks.

**Maintenance Windows**
Add `maintenance_start` and `maintenance_end` (nullable `DATETIME`) columns to `jetpack_monitor_sites`. During a maintenance window, checks continue and RTT data is collected, but status-change notifications are suppressed. The check result is logged internally so the audit trail is complete, but no alert fires. Configurable via the WPCOM API or direct DB write.

**Granular Timing Breakdown**
Go's `net/http/httptrace` provides discrete callbacks for DNS start/done, TCP connect start/done, TLS handshake start/done, request written, and first response byte. Each check records all six timings at no additional cost. The single composite "RTT" is retained for backwards compatibility; the component timings are emitted as new StatsD metrics and stored for the operator dashboard and audit log.

**Per-Site Request Headers**
Add a `custom_headers` JSON column to `jetpack_monitor_sites`. The check engine merges these into the outgoing request, allowing sites that require an `Authorization` header or a specific `Host` value to be checked correctly.

**Configurable Timeout Per Site**
Add a `timeout_seconds` column, defaulting to the global `NET_COMMS_TIMEOUT`. Premium sites can have shorter timeouts for faster failure detection; sites on slow infrastructure can have longer ones to reduce false positives.

**Sub-Minute Check Intervals (Premium)**
The goroutine scheduler handles arbitrary intervals natively. A dedicated premium worker pool with its own configuration runs at high frequency without affecting the general pool. Routing is via the existing bucket range mechanism — premium buckets are assigned to the premium pool configuration.

**False Positive Suppression Tuning**
Expose `NUM_OF_CHECKS` and `TIME_BETWEEN_CHECKS_SEC` as per-site overrides in the database. Sites with a history of transient failures can be tuned to require more local confirmations before escalating to Verifliers, without changing the global defaults.

**Response Time History**
Each check result — including the granular DNS/TCP/TLS/TTFB breakdown — is written to a `jetmon_check_history` table with a timestamp. This enables response time trending over configurable windows (1h, 24h, 30d) queryable via the operator dashboard and the audit CLI. The data is already being collected as part of the granular timing breakdown; this feature is purely a storage and query layer on top of it. Provides the response time graphs that customers expect from competing services.

**Alert Deduplication and Cooldown**
Add a `alert_cooldown_minutes` column to `jetpack_monitor_sites`, defaulting to a global `ALERT_COOLDOWN_MINUTES` config value. After an alert fires for a site, subsequent alerts for the same site are suppressed until the cooldown expires, even if the site flaps up and down repeatedly. The suppression is recorded in the audit log. Prevents alert fatigue on flapping sites without requiring manual maintenance window configuration.

**TLS Version and Cipher Reporting**
Alongside SSL certificate expiry monitoring, inspect `tls.ConnectionState` for the negotiated TLS version and cipher suite. Flag sites still serving TLS 1.0 or TLS 1.1 (deprecated) via a dedicated alert threshold, and record the TLS version and cipher in the audit log. Zero additional network requests — this data is present in every existing HTTPS connection alongside the certificate chain.

**Redirect Policy Configuration**
Add a `redirect_policy` column to `jetpack_monitor_sites` with three options: `follow` (current behaviour — follow redirects and treat the final response code as the result), `alert` (follow the redirect but record a warning in the audit log when the redirect target or chain changes from a stored baseline), and `fail` (treat any redirect as a failure). Detecting unexpected redirect changes is valuable for catching misconfigured CDN rules, accidental HTTP-to-HTTPS regressions, and domain hijacking scenarios.

---

## Tooling and Developer Experience

**Docker Compose Environment**
The existing Docker Compose setup is updated for the Go binary. A single `docker compose up` starts MySQL, the Jetmon 2 binary, one or more Veriflier instances, Mailpit for local email capture, StatsD + Graphite, the operator dashboard, and the deterministic API fixture. No npm, no node-gyp, no manual build steps. `docker compose up --build` rebuilds the Go binaries in a reproducible multi-stage Docker build.

**Docker-Local API Fixture**
The Docker Compose environment includes an `api-fixture` service for deterministic local API CLI and event-flow rehearsals without depending on public endpoint timing. It exposes:

- static response-code endpoints for success, client error, and server error cases
- configurable slow responses for timeout paths
- keyword-present and keyword-missing responses
- redirect paths for redirect-policy checks
- HTTPS with a self-signed certificate for TLS failure paths
- webhook capture endpoints that record deliveries and verify
  `X-Jetmon-Signature` when a shared secret is supplied

`make api-cli-smoke` exercises the normal local API smoke path, and
`make api-cli-validate` runs the broader guide validation with fixture-backed
failure simulation and optional webhook signature verification.

**Structured Logging**
All log output is available in two formats: the existing plain-text line format (for drop-in compatibility with current log consumers) and an optional structured JSON format enabled via `config.json`. The JSON format emits the same fields — level, timestamp, message, blog_id, http_code, error_code, RTT — as a machine-readable object, making log ingestion into Elasticsearch, Loki, or any log aggregation platform straightforward without a custom parser. Both formats write to the same log file paths.

**Alert Flow Replay**
Given a site `blog_id` and a time range, the replay tool reconstructs the full detection and notification sequence from the audit log: when each check ran, what it found, which local retries fired, which Verifliers were queried and what they returned, and what was sent to the WPCOM API. Outputs a human-readable timeline. Intended for Happiness Engineers debugging "why didn't I get an alert?" or "why did I get an alert when the site was fine?"

**Automated Test Suite**
End-to-end integration tests that run against the Docker Compose environment:

- Unit tests for the check logic (status classification, retry transitions, COMPARE mode comparison)
- Integration tests that insert sites into the test database, configure deterministic local test endpoints to return specific states, and assert that the correct WPCOM notification is sent within a defined time window
- Timeout and TLS failure scenarios
- Maintenance window suppression
- SSL expiry detection
- Keyword check pass/fail
- Worker pool scale-up and scale-down
- Graceful shutdown mid-round
- MySQL-coordinated bucket claiming: two hosts starting simultaneously claim non-overlapping ranges
- MySQL-coordinated bucket failover: a host's heartbeat is artificially expired and surviving hosts absorb its buckets within one grace period
- Alert cooldown suppression: a flapping site does not fire repeated alerts within the cooldown window
- Redirect policy: `follow`, `alert`, and `fail` modes behave correctly against deterministic local test endpoints

All tests run with `go test ./...` and are included in CI.

**Config Validation Tool**
A standalone binary (`jetmon2 validate-config`) that:

- Parses `config.json` and checks all required keys are present
- Validates value ranges and required per-mode settings
- Attempts a test connection to MySQL
- Reports legacy projection and email transport modes
- Prints the matching rollout preflight and projection-drift investigation
  commands for the configured bucket ownership mode
- Warns when the email transport resolves to the log-only `stub` sender
- Lists configured Verifliers as best-effort operator context
- Outputs a pass/fail summary with specific error messages

Intended to run as a pre-deployment check in CI and as an operator tool when diagnosing connectivity issues.

**Operator Dashboard**
A lightweight web UI served by the binary itself (no separate process) on a configurable internal port. Displays in real time:

- Worker goroutine count and active checks
- Check queue depth and drain rate
- Sites per second
- Round completion time
- Local retry queue depth
- Owned bucket range
- Bucket ownership mode, legacy projection mode, delivery-worker ownership, and
  rollout preflight / projection-drift commands
- Go runtime system memory usage
- WPCOM circuit-breaker state and queued notification depth
- Live dependency health for MySQL, configured Verifliers, WPCOM, StatsD, and
  log/stats directory writes
- Combined `/api/host` snapshot with local state, dependency health, and a
  red/amber/green host summary for operator tooling

Updates via server-sent events and lightweight JSON polling — no WebSocket library needed, no JavaScript framework. A plain HTML page with `<EventSource>` and `fetch` is sufficient and has no build toolchain dependency.

**System Health Map**
The operator dashboard health grid publishes:

- MySQL: connection state and ping latency
- Each configured Veriflier: reachability and status latency
- WPCOM API: circuit-breaker state and queued notification depth
- StatsD: local client initialization state
- Disk: writable `logs/` and `stats/` directories

Future refinements can add primary/replica breakdowns, last successful
orchestrator batch, WPCOM request error-rate windows, and disk free-space
thresholds once production operating data shows which signals are worth paging
on.

Long-running `jetmon2` and `jetmon-deliverer` processes also publish compact
heartbeat snapshots into `jetmon_process_health`. The `/fleet` dashboard uses
those snapshots alongside `jetmon_hosts`, outbound delivery queues, projection
drift, and dependency rollups to summarize monitor hosts, standalone
deliverers, stale process heartbeats, lifecycle state, red/amber/green health
rollups, delivery-owner posture, Go runtime system memory, and local dependency
health without polling every host dashboard directly.

**False Positive Tracker**
Every time the system escalates a site to Veriflier confirmation and the Verifliers do NOT confirm it as down (i.e., the queue entry times out or all Verifliers report the site as up), the event is recorded in a `jetmon_false_positives` table with timestamp, site, HTTP code, error code, and RTT from the local check. A view in the operator dashboard surfaces sites with high false positive rates, helping operators tune per-site `NUM_OF_CHECKS` or `TIME_BETWEEN_CHECKS_SEC` settings.

**Internal Audit Log**
Operational activity for every site is written to a `jetmon_audit_log` table:

- Check performed: timestamp, source (local/veriflier name), result (HTTP code, error code, RTT)
- WPCOM notification sent: timestamp, payload hash, response code
- WPCOM notification retry: timestamp, reason
- Local retry dispatched: timestamp, retry count
- Veriflier request sent: timestamp, which verifliers
- Veriflier result received: timestamp, veriflier name, result
- Maintenance window active: timestamp, window end
- Config change: timestamp, which keys changed

Authoritative incident state transitions live in `jetmon_event_transitions`, written by the `eventstore` package in the same transaction as the matching `jetmon_events` mutation. The audit log is intentionally operational context, not the source of truth for site state.

Queryable by `blog_id` and time range via a CLI tool (`jetmon2 audit --blog-id 12345 --since 2h`) and via the operator dashboard. Designed specifically for Happiness Engineers investigating customer-reported alert issues.

**Deployment Tooling**
- `jetmon2 version` — prints binary version, build date, Go version, and git commit hash
- `jetmon2 migrate` — applies pending DB schema migrations idempotently
- `jetmon2 status` — connects to a running instance's internal API and prints a one-line health summary (equivalent to reading `stats/totals` but richer)
- `jetmon2 rollout guided` — interactive host rollout and rollback walkthrough with transcript logging, resume state, typed destructive confirmations, and fail-closed gates
- `jetmon2 rollout rehearsal-plan` — prints the ordered same-server or fresh-server command sequence for a host replacement from the approved bucket CSV
- `jetmon2 rollout host-preflight` — bundles the pre-stop host gate: static plan match, config parse, DB connectivity, pinned safety checks, and systemd validation
- `jetmon2 rollout static-plan-check` — validates a CSV host-to-bucket plan before any v1 host is stopped
- `jetmon2 rollout pinned-check` — validates a pinned v1-to-v2 cutover host before or during host replacement
- `jetmon2 rollout cutover-check` — bundles the read-only post-start pinned preflight, activity, dashboard status, and projection-drift checks
- `jetmon2 rollout activity-check` — verifies recent check activity for a bucket range after cutover
- `jetmon2 rollout rollback-check` — verifies a pinned v2 range is safe to hand back to v1
- `jetmon2 rollout dynamic-check` — validates full `jetmon_hosts` coverage after the fleet transitions from pinned to dynamic ownership
- `jetmon2 rollout projection-drift` — lists active sites whose legacy `site_status` projection disagrees with the authoritative event state
- `jetmon2 rollout state-report` — summarizes ownership mode, bucket coverage, recent activity, projection drift, delivery-owner state, and the suggested next action
- `jetmon2 drain --worker N` — gracefully removes one worker pool slot, waiting for in-flight checks to complete before reducing concurrency
- `jetmon2 reload` — sends SIGHUP to the running process (convenience wrapper)

Rollout gate commands accept `--output=json` for Systems automation. JSON output
keeps the command's pass/fail state, generated timestamp, parsed output lines,
and failure messages on stdout while preserving non-zero exit status on failed
checks.

The complete v1-to-v2 production process is documented in
[`v1-to-v2-migration.md`](v1-to-v2-migration.md).

**Zero-Downtime Rolling Updates**
Because bucket ownership is coordinated via MySQL, a multi-host deployment can be updated one host at a time with no coverage gap. The procedure for each host: send SIGINT to release its buckets, wait for the drain to complete, deploy the new binary, start the new process. Surviving hosts absorb the draining host's buckets during the update window and release them back once the updated host rejoins and reclaims its range. No simultaneous restart of all hosts is required, and no sites are left unchecked during the update.

---

## Auto-Scale and Auto-Heal

Jetmon 2 achieves maximum uptime without requiring a Kubernetes cluster. Scaling and healing operate at three levels: within the process, at the host level via systemd, and across hosts via MySQL-coordinated bucket ownership.

**Goroutine Pool Auto-Scaling**
The worker pool monitors queue depth against a configurable high-water mark. When queue depth exceeds the threshold for more than N seconds, new goroutine workers are added up to a configured maximum without any restart. When depth falls below a low-water mark for a sustained period, excess goroutines are drained gracefully. No process spawning, no IPC overhead — adding a worker is a channel send. This handles the vast majority of load variation entirely within a single process.

**systemd Process Supervision**
The binary ships with a systemd unit file. `Restart=on-failure` with a short `RestartSec` ensures the process is automatically restarted if it crashes or exits unexpectedly. `StartLimitIntervalSec` and `StartLimitBurst` prevent restart loops from hammering a broken dependency. The unit file also enforces resource limits (`MemoryMax`, `LimitNOFILE`) to keep the process within safe bounds on shared hosts. A watchdog integration via `sd_notify` lets systemd detect and restart a process that has stopped making progress without actually crashing.

**MySQL-Coordinated Bucket Ownership**
A `jetmon_hosts` table replaces the static `BUCKET_NO_MIN`/`BUCKET_NO_MAX` config values with runtime-negotiated bucket ownership. Hosts claim, hold, and release bucket ranges autonomously using MySQL transactions as the coordination mechanism — no cluster orchestrator required. For the initial v1-to-v2 production migration, `PINNED_BUCKET_MIN`/`PINNED_BUCKET_MAX` (with `BUCKET_NO_MIN`/`BUCKET_NO_MAX` accepted as aliases) temporarily pins a v2 host to the exact static range of the v1 host it replaces; remove those keys after the fleet is on v2 to enable dynamic ownership.

Table structure:
```sql
CREATE TABLE jetmon_hosts (
    host_id        VARCHAR(255) NOT NULL PRIMARY KEY,
    bucket_min     SMALLINT UNSIGNED NOT NULL,
    bucket_max     SMALLINT UNSIGNED NOT NULL,
    last_heartbeat TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    status         ENUM('active', 'draining') NOT NULL DEFAULT 'active'
);
```

In dynamic ownership mode, on startup the instance upserts its own row, then scans for rows whose `last_heartbeat` is older than the grace period (suggested: 2× normal round time). Expired rows are presumed dead. The instance claims their uncovered bucket ranges by deleting the dead rows and inserting its own covering range inside a `SELECT ... FOR UPDATE` transaction, preventing two hosts from racing to claim the same range simultaneously. The instance derives its active range from what it successfully claimed — `BUCKET_NO_MIN`/`BUCKET_NO_MAX` are only needed as aliases for the temporary pinned migration mode.

In dynamic ownership mode, each round the orchestrator issues a single `UPDATE jetmon_hosts SET last_heartbeat = NOW() WHERE host_id = ?`. If a host stalls, is OOM-killed, or loses network, its heartbeat stops updating. Surviving hosts detect the stale row at the start of their next round and absorb its buckets up to their configured `BUCKET_TARGET` maximum. In pinned migration mode, the host skips `jetmon_hosts` entirely and checks only its configured static range.

On SIGINT, the instance sets `status = 'draining'`, completes in-flight checks, then deletes its own row. Surviving hosts can reclaim those buckets at the start of their next round without waiting for heartbeat expiry. A hard-killed host leaves its row in place; the grace period determines how long before its buckets are reclaimed.

Bucket capacity is configured via `BUCKET_TOTAL` (total range, e.g. 1000) and `BUCKET_TARGET` per host (e.g. 500). Hosts with spare capacity absorb buckets from failed peers up to their own maximum. Live hosts are never rebalanced — only dead hosts' buckets are redistributed — which avoids race conditions and unnecessary churn.

Benefits over the current static configuration:
- **Zero-config horizontal scaling**: spin up a new host and it claims unclaimed buckets automatically; no operator coordination required
- **Self-healing coverage**: a failed host's buckets are absorbed by surviving peers within one grace period, with no manual intervention and no gap in monitoring
- **Clean decommissioning**: sending SIGINT releases buckets immediately rather than waiting for expiry, minimising the coverage gap during planned maintenance
- **No external orchestrator**: MySQL is already a hard dependency; the coordination mechanism costs one extra table and one heartbeat query per round

**Auto-Heal**
- **DB connection loss**: The DB pool retries connections with exponential backoff. In-flight batch work is held in the queue; no work is lost.
- **Veriflier unreachable**: A Veriflier that fails to respond is marked unhealthy and excluded from confirmation requests. Remaining healthy Verifliers continue; the `PEER_OFFLINE_LIMIT` threshold adjusts dynamically to the number of healthy Verifliers (with a floor to prevent false confirmations).
- **WPCOM API failures**: Circuit breaker pattern. After N consecutive failures the circuit opens, pending notifications are queued in memory with timestamps, and the circuit is retried on a backoff schedule. Queue is bounded; oldest entries are dropped with an error log if it fills.
- **Stuck check goroutine**: A watchdog goroutine tracks the last activity time of each check. A goroutine that exceeds `NET_COMMS_TIMEOUT * 2` without completing is cancelled via context cancellation, its result counted as a timeout, and a new goroutine is allocated to replace it.
- **Memory pressure**: The binary exposes Go runtime system memory via the health endpoint. If that exceeds a configurable threshold, the pool size is reduced by 10% via graceful drain until pressure eases — the equivalent of the current worker recycling mechanism, but without process death. True operating-system RSS can still be checked with host tooling when investigating sustained memory pressure.

---

## Stretch Goals and Future Add-ons

These are intentionally out of scope for the initial rewrite. They represent the path to making Jetmon 2 a fully competitive standalone monitoring platform rather than a reliable internal Jetpack service.

**DNS Monitoring**
Check that a domain resolves to expected IPs on a schedule, using Go's `net.LookupHost()`. Alert when the answer changes or when resolution fails. Particularly valuable for detecting DNS hijacking and nameserver misconfigurations before they cause HTTP failures. New monitor type stored as a separate DB table.

**TCP Port Monitoring**
Attempt a TCP connection to an arbitrary host:port on a schedule. No HTTP layer — a successful connection is "up". Useful for database ports, SMTP, and custom application services. A small extension of the existing connection logic.

**Heartbeat / Cron Monitoring**
New inbound endpoint on the Monitor's HTTP/API surface where monitored jobs ping on completion. If the expected ping doesn't arrive within the configured interval plus grace period, an alert fires. Deep integration with the Jetpack heartbeat for zero-configuration WP-Cron health detection.

**Response Time Anomaly Detection**
Using the granular timing breakdown (DNS/TCP/TLS/TTFB) collected in the rewrite, build a per-site baseline over a rolling window and alert when response time exceeds N standard deviations from baseline — even if the site is technically returning 200. Detects slow-but-not-down conditions that users notice but current monitoring misses.

**Status Page Generator**
Generate a static status page (or a hosted dynamic one) showing uptime history, current status, and incident timeline for a site or group of sites. Embeddable on the customer's WordPress site via a Jetpack block. The incident history data would be available from the audit log.

**Incident History and SLA Reporting**
Derive uptime percentage and incident history from the audit log. Expose via a read API that the Jetpack dashboard can query to show customers their site's uptime over the last 30/90 days. This is primarily a query layer over data the audit log already captures.

**Per-Location Downtime Visibility**
Surface which Veriflier locations saw the site as down vs. up during a downtime event. Already collected internally in `queuedRetries[].checks[]` — this is an exposure and storage change, not a new data collection effort. Highly valuable for diagnosing CDN or regional routing issues.

**Synthetic / Transaction Monitoring**
Simulate a real user journey (login, add to cart, checkout) using a headless browser via Playwright or Chromedp. Completely different architecture from HTTP checks — requires browser infrastructure — but represents the most valuable monitoring capability for e-commerce and membership sites. Long-term roadmap item.

**On-Call and Escalation Policy**
Within-Jetpack on-call scheduling: route alerts to different contacts at different times of day, with escalation if the primary contact doesn't acknowledge within N minutes. Would require a new data model and notification pipeline but no new infrastructure.

**Distributed Tracing**
Instrument the full check pipeline with OpenTelemetry spans: DB fetch → work dispatch → HTTP check (with DNS/TCP/TLS sub-spans) → Veriflier request → WPCOM notification. Export to Jaeger or any OTLP-compatible backend. Makes debugging latency anomalies and check delays straightforward without relying on log correlation.

# Changelog

Format: date or release bucket, summary, and PR/commit reference when useful.
Breaking changes are marked **BREAKING**.

## Unreleased

### Jetmon 2

Jetmon 2 is the Go rewrite of the Monitor and Veriflier. It keeps the rollout
contracts that matter for replacing v1 while moving runtime state, diagnostics,
and operator control into clearer v2-owned surfaces.

#### Runtime and Scheduling

- Replaced the Node.js/C++ process tree with the `jetmon2` Go binary.
- Replaced worker-process spawning with a bounded Go check pool and streaming
  scheduler.
- Added dynamic bucket ownership through `jetpack_monitor_hosts`, with pinned
  bucket ranges retained for migration windows.
- Added graceful drain/reload commands, PID-file fixes, and localhost-only
  pprof on `DEBUG_PORT`. SIGHUP reload now drains and self-reexecs
  long-running v2 services so startup-only config and replaced binaries take
  effect cleanly; Docker containers re-exec through their entrypoint so config
  rendering and startup validation run again before the binary restarts.
- Added process resource metrics, true RSS reporting, and StatsD host-path
  configuration compatible with v1 metric naming.

#### Event and State Model

- Added authoritative event state in `jetpack_monitor_events` and append-only
  transition history in `jetpack_monitor_event_transitions`.
- Added `internal/eventstore` as the single writer for event rows, transition
  rows, and the optional legacy projection transaction.
- Added shadow-v2-state rollout support:
  `LEGACY_STATUS_PROJECTION_ENABLE=true` keeps the v1 `site_status` and
  `last_status_change` projection updated for legacy readers.
- Added explicit severity/state lifecycle: `Up`, `Warning`, `Degraded`,
  `Seems Down`, `Down`, and resolved events.
- Added telemetry reporting for lifecycle counts, false alarms, verifier
  evidence, WPCOM parity, and explanation gaps.

#### Check Behavior

- Added staged check policy support:
  `HEAD` + `legacy`, `GET` + `simple_http`, then `GET` + `full`.
- Added per-site check config for method/profile, timeout, headers, keyword
  checks, redirect policy, cooldown, and maintenance windows.
- Added timing breakdowns for DNS, TCP connect, TLS handshake, and TTFB.
- Added SSL observation for expiry, deprecated TLS versions, and cipher data.
- Added safety handling for unsafe legacy monitor URLs.

#### Veriflier

- Rebuilt the Veriflier as `veriflier2`, a Go binary with JSON-over-HTTP
  production endpoints.
- Added `POST /v2/check` and `GET /v2/status` with batch/request IDs, deadlines,
  typed outcomes, timing breakdowns, bounded diagnostics, vantage IDs, agent
  IDs, and capacity data.
- Added bounded Veriflier execution and overload behavior: saturated Verifliers
  return HTTP 503 for a batch, which the Monitor treats as no-vote/unhealthy
  rather than customer-site downtime.
- Added v2-only rollout posture: v2 Monitors should point at v2 Verifliers; v1
  Veriflier transport is not a v2 Monitor fallback, and v2 check calls do not
  automatically downgrade to the legacy HTTP endpoint.
- Added quorum by unique trusted `vantage.id`, duplicate-vote auditing, and a
  configurable healthy-vantage floor.
- Added trusted Veriflier discovery tables, monitor-collected agent telemetry,
  discovery reports, dashboard visibility, and local soak tests.

#### Internal API and CLI

- Added the internal `/api/v1` REST API behind operator/gateway access.
- Added Bearer-token auth, scopes, sha256-hashed API keys, token rotation,
  rate limiting, and idempotency keys on write POSTs.
- Added API and CLI support for sites, events, transitions, SLA/timing stats,
  alert contacts, webhooks, telemetry, rollout sessions, rollout gates, and
  local CLI config.
- Added API-driven rollout flow for container deployments: standby preflight,
  read-only smoke probes, side-table seed/final reconcile, bucket activation,
  release/rollback, policy staging, and method/profile comparison.

#### Webhooks and Alerting

- Added `jetpack_monitor_webhooks` and delivery records with HMAC-SHA256
  signatures and retry scheduling.
- Added managed alert contacts for email, PagerDuty, Slack, and Microsoft Teams.
- Added per-contact filters, severity gates, rate caps, synthetic send tests,
  and manual retry endpoints.
- Added standalone `jetmon-deliverer` for webhook and alert-contact delivery,
  with delivery ownership guards and queue health checks.
- Kept legacy WPCOM notification delivery active for rollout compatibility.

#### Dashboards and Operations

- Added host and fleet dashboards backed by process health, bucket ownership,
  delivery queues, projection drift, Veriflier telemetry, and dependency rollup.
- Moved dashboard snapshots behind authenticated API endpoints and made the
  legacy HTML/JSON dashboard listener opt-in.
- Added rollout and lab docs for API-driven production rollout, Docker image
  behavior, VM/container labs, TeamCity deployment assumptions, and support
  explanation workflows.
- Added config validation for production profiles, DB server-map paths, StatsD
  host paths, WPCOM legacy mode, Veriflier discovery, and rollout posture.

#### Build and Developer Tooling

- `make all` builds `jetmon2`, `jetmon-deliverer`, and `veriflier2`.
- Generated Veriflier proto stubs remain an explicit future-transport step
  rather than part of the default build.
- Makefile targets support configurable Go binaries and temp build caches.
- Added repeatable smoke targets for rollout docs, Veriflier soak coverage,
  production rollout labs, and local Docker/VM workflows.

#### Notable Fixes and Hardening

- Fixed soft-lock delivery claiming so in-flight webhook and alert-contact rows
  are not repeatedly reclaimed by fast polling loops.
- Rechecked readiness predicates during claim updates to avoid dispatching stale
  SELECT results.
- Added alert-contact validation for empty labels and negative rate caps.
- Added MIME header CR/LF stripping for email defense-in-depth.
- Added idempotency support to alert-contact send-test.
- Removed API list-site dependency on MySQL 8 window functions.
- Standardized half-open semantics for API key expiration and revocation.
- Added empty-token and body-size guards to Veriflier transport.
- Moved v2-only config and runtime state out of `jetpack_monitor_sites` and
  into side tables so the legacy table remains rollout-safe.

## Jetmon 2 Initial Rewrite

The initial Go rewrite delivered:

- single static `jetmon2` binary
- embedded schema migrations and `validate-config`
- CLI drain/reload/audit commands
- operator dashboard with SSE
- non-root Docker images
- healthcheck-gated local MySQL dependency
- context-aware DB calls
- DSN construction through `mysql.Config.FormatDSN()`
- startup validation for required auth config
- logged audit/history/SSL write errors

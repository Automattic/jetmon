CHANGELOG
=========

Format: date (YYYY-MM-DD), change summary, PR or commit reference where available.
Breaking changes are marked **BREAKING**.

---

## Unreleased

### v2 branch — site health platform

The v2 branch builds on the Go rewrite to turn Jetmon from a status-flipper
into a full event-sourced health platform with an internal REST API,
HMAC-signed webhooks, and managed alert contacts. Kept on a parallel branch
because it is intentionally **not** drop-in with the Jetmon 1 wire format
(see PR #61 — DO NOT MERGE).

**New — event sourcing:**
- `jetmon_events` (current authoritative state per incident) and
  `jetmon_event_transitions` (every status/severity change, append-only)
  tables; `internal/eventstore` writes both in a single transaction
- Shadow-v2-state migration: while `LEGACY_STATUS_PROJECTION_ENABLE` is
  true, event mutations also maintain the v1 `site_status` /
  `last_status_change` projection for legacy consumers
- Five-layer severity ladder: `Up < Warning < Degraded < SeemsDown < Down`
  matching `internal/eventstore.Severity*` constants

**New — internal REST API (`/api/v1/`, internal-only behind a gateway):**
- Per-consumer Bearer token auth with three scopes (`read` / `write` /
  `admin`); `./jetmon2 keys create/list/revoke/rotate` CLI
- Per-key token-bucket rate limiter with `X-RateLimit-*` headers
- Stripe-style idempotency keys on POST endpoints
- Sites CRUD + pause/resume/trigger-now
- Events list + single + transitions list + manual close
- SLA endpoints: uptime, response-time, timing-breakdown
- Audit logging via `jetmon_audit_log` with `event_type=api_access`
- See internal-api-reference.md for full surface and design rationale

**New — Veriflier v2 contract:**
- Added versioned JSON-over-HTTP endpoints `POST /v2/check` and `GET /v2/status`
  while keeping `veriflier2` legacy-compatible `/check` and `/status`
  endpoints available behind the opt-in `VERIFLIER_ENABLE_LEGACY_HTTP` switch.
- `/v2/check` carries batch/request IDs, request deadlines, body rules, typed
  outcomes, timing breakdowns, quorum `vantage.id`, and diagnostic `agent.id`.
- Veriflier checks now run through a bounded concurrent executor. Saturated
  Verifliers reject whole batches with HTTP 503 so overload is treated as
  no-vote/unhealthy, not as customer-site downtime.
- Monitor clients prefer the v2 contract and fall back to the `veriflier2`
  legacy-compatible HTTP contract only for transition-safe unsupported-v2
  responses. The preferred rollout deploys a fresh v2 Veriflier fleet first and
  points v2 Monitors only at that fleet; the original v1 Veriflier TLS/custom
  transport is not a v2 Monitor fallback target.
- Downtime quorum now counts unique v2 `vantage.id` values rather than raw
  Veriflier agent replies. Duplicate vantage replies are audited but ignored
  for quorum, and multi-Veriflier fleets retain a two-healthy-vantage floor
  unless `PEER_OFFLINE_LIMIT=1` is explicitly configured.
- `jetmon2 validate-config` and dashboard health now surface Veriflier v2
  contract status, vantage/agent/capacity metadata, and duplicate or missing
  vantage IDs.
- Added Veriflier auto-discovery plumbing: trusted
  `jetmon_veriflier_vantages`, agent telemetry rows in
  `jetmon_veriflier_agents`, `VERIFLIER_DISCOVERY_MODE=static|shadow|active`,
  shadow-mode drift reporting, and active-mode fallback to static config.
- Added `jetmon2 verifliers discovery-report`, a read-only shadow-mode gate
  that compares configured static Verifliers, trusted registry rows, and recent
  agent telemetry without printing auth token values.
- Documented the Veriflier discovery trust model in ADR-0010 and added an
  operator checklist for dashboard/report green, amber, and red discovery
  warnings.
- Monitors collect Veriflier liveness/capacity telemetry from authenticated
  `/v2/status` responses and write `jetmon_veriflier_agents`, so Veriflier
  hosts do not need database credentials. Agent telemetry is not trust;
  operators must pre-approve and enable vantages before monitors count them for
  quorum.
- Added `make test-veriflier-soak` for local v2 contract soak coverage:
  high-concurrency mixed outcomes, overload recovery, auth rejection, and
  deadline timeout recovery. The same target now also runs Veriflier discovery
  drift soak cases for duplicate static vantages, registry mismatch, stale or
  missing agent telemetry, untrusted agents, duplicate active endpoints,
  active-mode fallback, and recovery to green.
- `jetmon2 telemetry report` now includes v2 Veriflier vote-evidence rollups:
  duplicate votes ignored for quorum, duplicate-vote transitions,
  minimum-healthy-floor blocks, and max observed quorum/healthy-vantage counts.
- Documented `veriflier2` legacy-compatible fallback removal gates and the v2
  naming decision: keep `veriflier` / `veriflier2` through rollout, use a
  clearer probe-agent name only for a future v3 architecture.

**New — webhooks (Phase 3):**
- `jetmon_webhooks` registry + `jetmon_webhook_deliveries` per-fire records
- Stripe-style HMAC-SHA256 signatures (`t=<unix>,v1=<hex>` over
  `{ts}.{body}`); plaintext secret storage with documented threat model
- Filter dimensions: `events` + `site_filter` + `state_filter` (AND across,
  whitelist within, empty=match all)
- Delivery worker with per-webhook in-flight cap (default 3) and shared
  pool (default 50), retry ladder 1m / 5m / 30m / 1h / 6h then abandon
- Frozen-at-fire-time payload contract — consumer sees the event as it was
  when the webhook fired, not as it is now
- POST `/webhooks/{id}/rotate-secret` (immediate revocation; grace-period
  rotation deferred — see roadmap.md)
- POST `/webhooks/{id}/deliveries/{delivery_id}/retry` for operator manual
  retry of abandoned rows

**New — alert contacts (Phase 3.x):**
- Managed channels for human destinations: `email`, `pagerduty`, `slack`,
  `teams`. Boundary with webhooks: alert contacts deliver Jetmon-rendered
  notifications through Jetmon-owned transports; webhooks deliver the raw
  signed event stream for custom rendering
- Filter shape: `site_filter` + `min_severity` (default `Down`); per-contact
  `max_per_hour` rate cap (default 60) as pager-storm insurance
- POST `/alert-contacts/{id}/test` for synthetic send-tests through the
  same dispatch path
- Email transport pluggable via `EMAIL_TRANSPORT` config: `wpcom`
  (production), `smtp` (dev / staging with MailHog), `stub` (default
  log-only / tests, with startup and validate-config warnings)
- PagerDuty Events API v2 with severity mapping and event_action
  trigger/resolve based on the recovery flag
- Slack Block Kit + Microsoft Teams Adaptive Card rendering
- Plaintext credential storage in `destination` JSON; same outbound-dispatch
  rationale as webhook secrets, threat model documented inline
- Legacy WPCOM notification flow continues alongside; migration tracked
  in roadmap.md

**Verifier hardening:**
- Body size cap and empty-token guard on the JSON-over-HTTP transport
- Verifier config validation: required `host` and `port` per entry, with
  deprecated `grpc_port` accepted as a warning compatibility alias. PID file
  location now respects `JETMON_PID_FILE` env var

**Worker fixes:**
- Soft-lock fix for both webhooks and alerting deliver loops: `ClaimReady`
  pushes `next_attempt_at` out by 60s so the 1s tick doesn't re-claim a
  still-in-flight row. Without this, the per-contact in-flight cap (3)
  was producing concurrent dispatches that inflated the attempt counter
  and effectively skipped retry-schedule steps; the documented 7h36m
  retry window was being collapsed to ~1h.
- `ClaimReady` now repeats the readiness predicate during the soft-lock
  update and returns only rows whose update affected a row, so overlapping
  claim attempts skip stale SELECT results instead of doing duplicate
  dispatch work. Multi-instance row-claim caveat (SELECT ... FOR UPDATE
  SKIP LOCKED) still tracked alongside the deliverer-binary extraction in
  roadmap.md.

**Docs / tooling:**
- Host dashboard now has a combined `/api/host` snapshot endpoint, stronger
  red/amber/green summary behavior, clearer rollout-command visibility, and a
  durable `jetmon_process_health` heartbeat table that `jetmon2` and
  `jetmon-deliverer` publish to for fleet dashboards.
- Host dashboard exposure now defaults to localhost, host summaries include
  named red/amber issues, process lifecycle is stored separately from health
  rollup, and the runtime memory value is clearly labeled as Go Sys memory.
- Fleet dashboard now has `/fleet` and `/api/fleet` views backed by
  `jetmon_process_health`, `jetmon_hosts`, delivery queues, projection drift,
  and dependency rollups so operators can see stale heartbeats, bucket coverage,
  delivery-owner posture, and suggested next actions in one place.
- Fleet dashboard now has a dedicated Veriflier fleet section showing trusted
  vantages, monitor-collected agent telemetry, capacity, discovery modes,
  incomplete registry rows, stale telemetry, and duplicate endpoint warnings
  without exposing Veriflier auth tokens.
- Host and fleet dashboards now publish true process RSS beside Go runtime
  system memory, and `process.rss_mb` again reports operating-system resident
  memory when procfs is available.
- Added `jetmon2 telemetry report`, a read-only production report that
  summarizes event lifecycle counts, detection timing, verifier agreement,
  false-alarm classes, WPCOM parity, and operator explanation gaps from durable
  event/audit tables. The report starts with an explicit `telemetry_status`,
  explanation-gap type/row counts, window-edge context for WPCOM parity, and
  bounded, half-open query windows so scheduled runs are safer and easier to
  compare.
- `make all` now builds the currently implemented `jetmon2` and
  `veriflier2` binaries without requiring `protoc`; generated Veriflier
  gRPC stubs remain an explicit `make generate` step for the future
  transport swap.
- Makefile targets now share a configurable `GO` command and fall back to
  `/usr/local/go/bin/go` when `go` is not on `PATH`; they also use an
  overrideable `/tmp` Go build cache so checks do not depend on a
  writable home-directory cache.
- Developer docs now point at the Makefile build path and document why
  code generation is separate from the default build.
- Added a top-level docs index and a post-v2 probe-agent architecture
  options document for revisiting the v3 direction after v2 is stable in
  production.
- Clarified that the current Veriflier transport is JSON-over-HTTP and
  that the public API roadmap is about a future customer-facing contract,
  not the already-implemented internal `/api/v1`.

**Polish:**
- `alerting.Update` now validates `label` (must be non-empty) and
  `max_per_hour` (must be ≥ 0) at input time, surfacing 422
  `invalid_alert_contact` instead of letting an empty label silently
  persist or a negative `max_per_hour` surface as a generic 500 from
  MySQL's `INT UNSIGNED` constraint. Validations that don't depend on
  the existing row run before the DB lookup so obviously bad PATCH
  bodies don't pay for a round-trip.
- Email transport strips CR and LF from MIME header values
  (`From` / `To` / `Subject`) as defense-in-depth against header
  injection via untrusted strings (`monitor_url` is operator-controlled
  but the column doesn't enforce CRLF-free). Body content with newlines
  is unaffected.
- `POST /api/v1/alert-contacts/{id}/test` now honors `Idempotency-Key`
  like the other write POSTs, so a retried "click to test" during a
  network blip doesn't double-page the destination.
- API list-site rollup of the worst open event no longer relies on
  `ROW_NUMBER()` window functions, so the query is compatible with
  MySQL 5.7. Pagination caps the IN list and a site rarely has more
  than one open event, so reducing in Go is cheap.
- API key cutoffs (`revoked_at` and `expires_at`) now share half-open
  semantics: a key is valid for times strictly before the cutoff and
  rejected at or after it. Future `revoked_at` continues to act as a
  rotation grace window. See internal-api-reference.md.
- `LEGACY_STATUS_PROJECTION_ENABLE` is announced at startup
  (`config: legacy_status_projection=enabled|disabled`) and surfaced by
  `./jetmon2 validate-config`, so operators can confirm projection
  state without reading the running config file.

### Jetmon 2 — initial Go rewrite

Complete rewrite of the Node.js + C++ uptime monitor as a single static Go binary.
Drop-in replacement for Jetmon 1; all existing MySQL schema columns are preserved.

**New:**
- Single binary (`jetmon2`) — no process tree, no node_modules
- Auto-scaling goroutine pool replaces worker process spawning
- `jetmon2 migrate` — schema migrations embedded in binary
- `jetmon2 validate-config` — config + DB connectivity check before deploy
- `jetmon2 drain` / `jetmon2 reload` — signal running process via PID file
- `jetmon2 audit` — query per-site audit log from CLI
- Operator dashboard on configurable port with SSE state stream
- pprof debug server on localhost-only `DEBUG_PORT` (default 6060)
- `LEGACY_STATUS_PROJECTION_ENABLE` controls v1 `site_status` /
  `last_status_change` compatibility writes; `DB_UPDATES_ENABLE` remains
  as a deprecated alias
- Graceful shutdown with 30-second hard-exit backstop
- Non-root Docker images (`jetmon` / `veriflier` system users)
- Healthcheck-gated MySQL dependency in docker-compose

**Changed:**
- Veriflier transport package renamed `internal/grpc` → `internal/veriflier`
- Auth token moved from JSON request body to `Authorization: Bearer` header
- MySQL DSN built via `mysql.Config.FormatDSN()` — password never in format strings
- `internal/db` functions accept `context.Context` for cancellation
- `DEBUG` config flag now controls log verbosity via `config.Debugf()`
- `AUTH_TOKEN` is now a required config field (validated at startup)
- `config-sample.json` ships with `DEBUG: false`

**Fixed:**
- `cmdDrain` / `cmdReload` now read PID path from `JETMON_PID_FILE` env var
  (previously hardcoded to wrong path `/var/run/jetmon2.pid`)
- Audit log failures are now logged rather than silently discarded
- DB write errors (`RecordCheckHistory`, `UpdateSSLExpiry`) are now logged

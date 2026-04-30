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
- Verifier config validation: required `host` and `grpc_port` per entry,
  PID file location now respects `JETMON_PID_FILE` env var

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
  rollup, and memory is labeled as Go runtime system memory rather than RSS.
- Fleet dashboard now has `/fleet` and `/api/fleet` views backed by
  `jetmon_process_health`, `jetmon_hosts`, delivery queues, projection drift,
  and dependency rollups so operators can see stale heartbeats, bucket coverage,
  delivery-owner posture, and suggested next actions in one place.
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

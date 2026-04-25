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
- See API.md for full surface and design rationale

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
  rotation deferred — see ROADMAP.md)
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
  (production), `smtp` (dev / staging with MailHog), `stub` (unit tests)
- PagerDuty Events API v2 with severity mapping and event_action
  trigger/resolve based on the recovery flag
- Slack Block Kit + Microsoft Teams Adaptive Card rendering
- Plaintext credential storage in `destination` JSON; same outbound-dispatch
  rationale as webhook secrets, threat model documented inline
- Legacy WPCOM notification flow continues alongside; migration tracked
  in ROADMAP.md

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
  retry window was being collapsed to ~1h. Multi-instance row-claim
  caveat (SELECT ... FOR UPDATE SKIP LOCKED) still tracked alongside the
  deliverer-binary extraction in ROADMAP.md.

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
- `DB_UPDATES_ENABLE` double-gate: requires both config flag and `JETMON_UNSAFE_DB_UPDATES=1` env var
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

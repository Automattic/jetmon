# Jetmon Internal API — Reference and Design Notes

This document is the reference for Jetmon 2's internal REST API and the design notes behind it. The API server, Bearer-token auth, site/event/SLA endpoints, webhooks, alert contacts, idempotency handling, and delivery retry surfaces are implemented in `internal/api/`, `internal/apikeys/`, `internal/webhooks/`, and `internal/alerting/`. Sections that describe future expansion or deferred behavior call that out explicitly.

**Audience: internal systems only.** Jetmon does not expose this API to end customers directly. A separate gateway service handles all customer-facing access — authentication, tenant isolation, customer rate limiting, plan-based feature gating, public error vocabulary, etc. — and calls Jetmon over this internal interface. Other internal services (operator dashboard, alerting workers, batch reporting jobs, the gateway itself) are the only direct callers. The gateway/tenant boundary and remaining public-exposure prerequisites are documented in [`public-api-gateway-tenant-contract.md`](public-api-gateway-tenant-contract.md).

**Gateway tenant context.** Requests from the internal consumer named `gateway`
may include `X-Jetmon-Tenant-ID`, `X-Jetmon-Public-Scopes`, and
`X-Jetmon-Gateway-Request-ID` (plus optional actor/plan headers). Jetmon
rejects those headers from any other consumer. When accepted, the context is
recorded in API audit metadata and used to owner-scope webhook and alert-contact
CRUD, delivery history, manual delivery retry, and alert-contact send-test
routes. Site, event, SLA/stat, and trigger-now routes are scoped through the
`jetpack_monitor_site_tenants` mapping table. Normal internal callers that omit these
headers keep the unscoped operator behavior described below.

This shapes several design choices: authentication is per-consumer rather than per-customer, scopes are coarse rather than granular, error messages are verbose rather than guarded, and key management is an ops-only concern rather than a self-service feature. The trust boundary is "is this a known internal system?", not "is this user allowed to see this site?".

The goal is to expose Jetmon's distinctive data model — the five-layer test taxonomy, the site → endpoint → event hierarchy, the multi-state vocabulary, and the event-sourced architecture (`taxonomy.md`, `events.md`) — over a shape that internal consumers can integrate against confidently. We took inspiration from Better Stack, UptimeRobot v3, Pingdom, and Atlassian Statuspage but did not copy any of their shapes wholesale; Jetmon's richer model (multi-state, layered tests, causal links, separate severity) wouldn't fit cleanly into a flat "monitors" API.

## Principles

1. **Read API is source-of-truth, not just a snapshot.** Consumers should be able to ask "what is the current state of this site?" and "how did this incident evolve from severity 3 to 4 to closed?" with separate, narrow endpoints — not by polling a coarse "monitor" record. That's what the events/transitions tables exist for.

2. **Severity and state are both first-class.** Many competitor APIs collapse to a single "status" string (UptimeRobot returns `up`/`down`; Better Stack adds `paused`/`maintenance`/`validating`). Jetmon exposes both: numeric severity for ordering, thresholds, and SLA math; human-readable state for display. They never disagree because they're stored as separate columns updated in lockstep.

3. **Cursor pagination, never offset.** Offset pagination breaks under concurrent writes (an event closing during traversal shifts page boundaries). Cursors keyed on stable timestamps (`started_at`, `changed_at`) survive that.

4. **Versioned URLs, conservative additions.** All endpoints under `/api/v1/`. New fields on existing responses are additive (consumers ignore unknowns); shape-breaking changes get `/api/v2/` and a deprecation window. Severity values 0–4 today, room to add new values up to 255 without a version bump.

5. **No shape-shifting based on permissions.** A read-scope token sees the same JSON shape for `GET /api/v1/sites/{id}` as an admin token — fields aren't hidden, they're empty/null where data isn't applicable. Easier to test, easier to document.

6. **Errors carry a stable code, a human message, and (when relevant) a reference id.** Consumers branch on the `code` field, not on parsing the message.

7. **Bulk operations must be explicit when added.** v1 currently exposes single-resource write endpoints only. If bulk updates are added later, they should have dedicated request and response shapes instead of encouraging "list 10,000 sites and then loop one update at a time" client behavior.

## Authentication

**Per-consumer Bearer tokens.** Each calling system gets one (or more) tokens identifying it. The tokens are not user-delegated — there's no concept of "an end user authenticated via this token." A token *is* a service identity.

```
Authorization: Bearer jm_a1b2c3d4e5f6...
```

Tokens are 32-byte high-entropy random strings, sha256-hashed at rest (sha256 not bcrypt — bcrypt is for human-chosen passwords; high-entropy tokens just need a fast cryptographic hash). Stored in `jetpack_monitor_api_keys`:

```
jetpack_monitor_api_keys:
  id              BIGINT PK
  key_hash        CHAR(64)         -- sha256 hex
  consumer_name   VARCHAR(128)     -- e.g. "gateway", "alerts-worker", "dashboard"
  scope           ENUM('read','write','admin')
  rate_limit_per_minute INT
  expires_at      TIMESTAMP NULL   -- NULL = never
  revoked_at      TIMESTAMP NULL   -- revoke time; future value = rotation grace window
  last_used_at    TIMESTAMP NULL
  created_at      TIMESTAMP
  created_by      VARCHAR(128)     -- ops user / automation that created the key
```

**Scopes — three coarse buckets:**

- `read` — every GET endpoint.
- `write` — every POST/PATCH/DELETE on sites, events, webhooks, and alert contacts.
- `admin` — write + ability to force operations like "recompute SLA from event log" or "close all events in maintenance mode." Reserved for ops tooling, not regular consumers.

We deliberately did not split into `sites:read` / `events:read` / `webhooks:read` etc. Internal consumers tend to need the whole read surface — the gateway needs to read everything to mediate it; an alerts worker reads sites, events, *and* webhooks. Granular scopes would create more configuration burden than they solve.

**Per-consumer audit logging.** Every authenticated request is logged to `jetpack_monitor_audit_log` with the consumer name, endpoint, status code, and latency. This is the load-bearing accountability mechanism — if "alerts-worker is hammering the trigger-now endpoint," that's visible in the audit log without parsing access logs. The audit log already exists for operational events (`events.md`); API access becomes another `event_type` value (`api_access`).

**Key management is ops-only.** No `/api/v1/keys` endpoints. Keys are created and revoked via the `./jetmon2` CLI:

```
./jetmon2 keys create --consumer gateway --scope read [--ttl 2160h]
./jetmon2 keys list
./jetmon2 keys revoke <key_id>
./jetmon2 keys rotate <key_id>     # creates a new key for the same consumer; revokes old after grace
```

The CLI talks to the database directly (via `jetpack_monitor_api_keys`), prints the new token once, and never exposes hashes. There's no self-service surface because there are no end customers — keys are infrastructure config, not user-managed credentials.

`revoked_at` and `expires_at` are both half-open cutoffs: a key is valid for times strictly before the cutoff and rejected at or after it. During key rotation, the CLI may set `revoked_at` in the future so the old key remains valid for the grace window while consumers deploy the replacement. Immediate revocation sets `revoked_at` to the current time.

**Single key format.** No live/test split. The token format is `jm_<base32 of 32 random bytes>`. The gateway is responsible for any environment separation (dev/staging/prod) at its own layer.

**Why not mTLS / IP allowlists alone?** Either could replace Bearer tokens for service-to-service auth, but tokens make per-consumer identity trivial to log and revoke. mTLS rotation is heavier; IP allowlists don't survive containerized deployments cleanly. Bearer tokens are the lowest-friction option that gives us per-consumer accountability.

**Why not OAuth?** Same reasoning as before, now stronger: there are no user delegations to model. Every caller is a server.

## API CLI helper

`jetmon2 api` is the local developer/operator helper for this API. It defaults
to the Docker-local API listener. It can read operator defaults from
`~/.config/jetmon2.conf` or a path named by `JETMON_API_CONFIG`, then environment
variables, then command flags:

```bash
./bin/jetmon2 local-config init \
  --base-url=http://localhost:8090 \
  --token-file=jetmon2-api-token
./bin/jetmon2 local-config show
./bin/jetmon2 local-config keys
```

```conf
base_url = http://localhost:8090
token_file = jetmon2-api-token
auth_policy = same-origin
timeout = 10s
output = json
```

The config file supports `base_url`, `token`, `token_file`, `auth_policy`,
`allow_remote`, `timeout`, `output`, and `pretty`. If `token` or `token_file`
is present, the config file must be mode `0600`; token files must also be mode
`0600`. `JETMON_API_URL`, `JETMON_API_TOKEN`, and
`JETMON_API_AUTH_POLICY` override the config file:

```bash
export JETMON_API_URL=http://localhost:8090
export JETMON_API_TOKEN=jm_replace_with_a_local_key

./bin/jetmon2 api health --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api commands --output table
./bin/jetmon2 api sites list --output table
./bin/jetmon2 api sites get --pretty 12345
```

For Docker-local rehearsals, `make api-cli-token-create`,
`make api-cli-token-list`, and
`API_CLI_TOKEN_ID=<id> make api-cli-token-revoke` wrap the in-container
`jetmon2 keys` commands from the repository root.

Typed commands cover sites, events, webhooks, alert contacts, local smoke runs,
and failure simulation. Use `api request` as the escape hatch for new API routes
before a typed command exists. See [`api-cli-guide.md`](api-cli-guide.md)
for a fuller feature guide and workflow examples:

```bash
./bin/jetmon2 api request --output table GET '/api/v1/sites?limit=5'
./bin/jetmon2 api sites bulk-add --count 3 --batch local-smoke --dry-run --pretty
./bin/jetmon2 api smoke --batch local-smoke --pretty
./bin/jetmon2 api sites simulate-failure --batch local-smoke --mode http-500 --wait 15s --pretty
./bin/jetmon2 api sites simulate-failure --batch local-smoke --mode http-500 --wait 30s --expect-event-state 'Seems Down' --expect-transition-reason opened --pretty
./bin/jetmon2 api sites cleanup --batch local-smoke --count 3 --output table
```

For containerized production rollout control, `jetmon2 api rollout guided`
wraps the rollout API primitives into an interactive operator flow with typed
confirmations and dry-run plans:

```bash
./bin/jetmon2 api rollout guided --bucket-min=0 --bucket-max=99 --allow-remote
./bin/jetmon2 api rollout guided --bucket-min=0 --bucket-max=99 --canary-file=rollout-canaries.json --allow-remote
./bin/jetmon2 api rollout guided --bucket-min=0 --bucket-max=99 --dry-run
./bin/jetmon2 api rollout guided --bucket-min=0 --bucket-max=99 --rollback --allow-remote
```

The backing API surface is read-scoped for passive status gates. Anything that
mutates state or causes the Monitor to run probes is admin-scoped:

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/api/v1/rollout/capabilities` | API contract version, supported rollout features, required config mode, and token TTL. |
| `POST` | `/api/v1/rollout/sessions` | Create a durable rollout session bound to a bucket range and optional change reference. |
| `GET` | `/api/v1/rollout/jobs/{job_id}` | Fetch a rollout job record and stored result payload. |
| `POST` | `/api/v1/rollout/preflight` | Validate standby/API-controlled config, DB access, schema version, v2 Veriflier contract/quorum coverage, and rollout blockers. |
| `POST` | `/api/v1/rollout/smoke` | Run read-only sampled rollout smoke probes for HEAD/legacy compatibility coverage. |
| `POST` | `/api/v1/rollout/seed` | Dry-run or execute v2 side-table seeding and legacy non-running projection adoption for the requested range. |
| `POST` | `/api/v1/rollout/final-reconcile` | Repeat seed/adopt immediately before activation to catch changes since the first seed. |
| `POST` | `/api/v1/rollout/activate-buckets` | Dry-run or execute durable bucket activation locks for this Monitor. |
| `POST` | `/api/v1/rollout/release-buckets` | Dry-run or execute release of an activated v2 bucket range. |
| `GET` | `/api/v1/rollout/status` | Current rollout mode, active ranges, and open session count. |
| `GET` | `/api/v1/rollout/bucket-coverage` | Gate payload for active bucket coverage. |
| `GET` | `/api/v1/rollout/activity-check` | Gate payload for recent check activity. |
| `GET` | `/api/v1/rollout/projection-drift` | Gate payload for legacy projection drift. |
| `POST` | `/api/v1/rollout/compare-methods` | Run non-authoritative sampled HEAD/GET comparison probes and persist delta rows. |
| `POST` | `/api/v1/rollout/stage-policy` | Dry-run or execute staged check-policy migration, pause checkpoints, and rollback-last-stage / rollback-all operations. |

The mutating operations use two-step execution. A dry-run request returns a
short-lived confirmation token that is hashed at rest and bound to operation,
range, run ID, authenticated API key identity, and request shape. The matching
execute request must provide that token before any bucket lock or side-table
mutation runs. One Monitor owner may hold only one contiguous active
API-controlled range at a time; use another Monitor host for a separate range,
or release the existing range before activating a different one for the same
host. The guided CLI sends idempotency keys on execute requests, so operators
can retry after a lost HTTP response without accidentally applying the same
mutation twice.

`/api/v1/rollout/smoke` and `/api/v1/rollout/compare-methods` run synchronous
sampled probes, so `sample_size` defaults to `100` and is capped at `1000` per
request. These probes are intentionally non-authoritative: they do not write
incident state, runtime freshness, check history, WPCOM notifications, or the
legacy projection. Comparison deltas are persisted separately in
`jetpack_monitor_rollout_comparison_results` for rollout analysis. If the selected
bucket range has no active sites, the API returns a warning instead of treating
an empty sample as proof that the range is healthy.

`/api/v1/rollout/preflight` and `/api/v1/rollout/smoke` can also run synthetic
canaries supplied by the operator. Use this for approved controlled sites or
uptime-bench fixtures that prove direct Monitor probe expectations such as
known-up, controlled-down, and WAF-style responses before activation. Canary
probes are read-only and use the same target-safety guardrails as rollout smoke
probes. They do not send WPCOM notifications or exercise the full incident
lifecycle; keep separate launch evidence for recovery and notification parity
where required. The API never hard-codes production canary URLs; the operator
passes them at runtime:

```json
{
  "canaries": [
    {
      "name": "known-up",
      "url": "https://canary.example.com/",
      "mode": "head-legacy",
      "expect_success": true,
      "expect_http_code": 200
    },
    {
      "name": "controlled-down",
      "url": "https://canary.example.com/down",
      "method": "GET",
      "profile": "simple_http",
      "expect_success": false,
      "expect_http_code": 503
    }
  ]
}
```

Pass that file to the guided flow or individual gates:

```bash
cp docs/rollout-canaries.example.json rollout-canaries.json
# Edit rollout-canaries.json so every URL points at an approved controlled
# canary or uptime-bench fixture before using it against a Monitor API.
./bin/jetmon2 api rollout guided --bucket-min=0 --bucket-max=99 --canary-file=rollout-canaries.json --allow-remote
./bin/jetmon2 api rollout preflight --bucket-min=0 --bucket-max=99 --canary-file=rollout-canaries.json --allow-remote
./bin/jetmon2 api rollout smoke --bucket-min=0 --bucket-max=99 --mode=head-legacy --sample-size=100 --read-only --canary-file=rollout-canaries.json --allow-remote
```

Each canary may use `mode` (`head-legacy`, `get-simple`, or `get-full`) or the
equivalent `method` plus `profile` fields. Optional fields include
`expect_error_code`, `keyword`, `forbidden_keyword`, `forbidden_keywords`,
`headers`, `timeout_seconds`, and `redirect_policy` (`follow`, `alert`, or
`fail`; full profile only). If `expect_success` is omitted, success is expected.

`/api/v1/rollout/stage-policy` writes cohort changes to
`jetpack_monitor_site_check_config` and records previous values in
`jetpack_monitor_rollout_policy_stage_rows`. Use `mode=rollback-last-stage` to restore
the most recent staged batch in the range, or `mode=rollback-all` to unwind all
unrolled-back stage rows for the run/range. NULL previous values are restored
as NULL so sites return to inheriting the fleet default. Staging requires an
explicit `size` value, either an integer cohort count or a percentage such as
`1%`; omitting `size` is rejected so a typo cannot migrate the whole eligible
range.

JSON is the default output for scripts. Add `--pretty` for readable JSON or
`--output table` for stable human-readable tables on list and workflow summary
commands.

Use `make api-cli-validate` with `JETMON_API_URL` and `JETMON_API_TOKEN` set for
a live Docker-local validation pass covering the guide's core examples, the
smoke workflow, webhook delivery/signature verification, and a deterministic
failure-simulation assertion. When target-safety behavior is part of the
validation, prefer `make api-cli-public-fixture-validate`: it starts an isolated
Docker stack with WPCOM disabled, Mailpit-only email, and a public-looking
Docker-internal fixture IP so Monitor and Veriflier safety checks stay enabled
without sending target traffic off-host. Set `API_VALIDATE_SKIP_WEBHOOK=1` when
you need a shorter pass that avoids the outbound webhook worker.

When plain Docker Compose is running, `sites simulate-failure` probes
`http://localhost:18091/health` and can use the Docker-internal fixture URL
`http://api-fixture:8091` for deterministic HTTP 500, HTTP 403, redirect,
keyword, timeout, and TLS scenarios. That Docker hostname resolves to a private
container address, so target-safety-enabled Monitor checks will block it as a
non-public target. Use `--fixture-url=off` to force the public endpoint
fallback, set `JETMON_API_FIXTURE_URL` / `JETMON_API_FIXTURE_PROBE_URL` for a
custom safe fixture, or use `make api-cli-public-fixture-validate` for the
standard target-safety-preserving Docker setup.

For strict rehearsal or CI checks, add `--expect-event-state`,
`--expect-event-severity`, `--require-transition`, or
`--expect-transition-reason`. When an expectation is set, the command keeps
polling until the expectation matches or `--wait` expires, then returns non-zero
with the last observed events/transitions in the summary.

## Common patterns

### Base URL and versioning

```
https://api.jetmon.example.com/api/v1
```

Hosted in the `jetmon2` binary on a dedicated port (`API_PORT`), separate from the operator dashboard (`DASHBOARD_PORT`) and the Veriflier transport port (`VERIFLIER_PORT`).

This version applies to the Monitor's internal REST API only. The Veriflier
`/v2/check` and `/v2/status` paths are a separate Monitor-to-Veriflier transport
contract, named `v2` because they replace the original v1 Veriflier protocol.
They are intentionally not under `/api/v1`.

### Content negotiation

`Content-Type: application/json` for both request and response. UTF-8. No XML, no form-encoded, no JSON-API envelope (Better Stack uses JSON:API; we don't because it adds an `attributes` indirection that obscures field names without buying us anything Jetmon-specific).

### Response envelope

Every list response wraps the data in a small envelope:

```json
{
  "data": [ ... ],
  "page": {
    "next": "eyJzdGFydGVkX2F0IjoiMjAyNi0wNC0yMVQxNjo...",
    "limit": 50
  }
}
```

Every single-resource response is just the resource:

```json
{
  "id": 487291,
  "blog_id": 12345,
  ...
}
```

Reasoning: keeping list and single-resource shapes distinct means consumers don't write `if (Array.isArray(response.data))` everywhere. The list envelope holds pagination; the resource envelope is the resource.

### Resource IDs

All resource `id` fields are raw `BIGINT UNSIGNED` integers serialized as JSON numbers (not strings). Sites use the existing `blog_id`; events, transitions, webhooks, deliveries, and contacts use their respective table's auto-increment primary key. There is no type prefix or ULID encoding.

Type context comes from the **endpoint path** (`/api/v1/sites/12345` vs `/api/v1/events/12345`) and from explicit `type` fields where ambiguity would otherwise hurt — for example, error messages always name the resource type:

```json
{ "error": { "code": "event_not_found", "message": "Event 12345 does not exist", "request_id": "..." } }
```

Webhook payloads include `"type": "event.opened"` so the consumer never has to infer from a bare numeric id which table the id refers to. Operational/trace identifiers (request IDs, webhook delivery IDs, idempotency keys) follow their own conventions described in the relevant sections.

### Pagination

Cursor-based, opaque tokens. Each list endpoint accepts `?cursor=...&limit=N`. Default limit 50, max 200.

```
GET /api/v1/sites?cursor=eyJzdGFydGVkX2F0IjoiMjAyNi0wNC0yMVQxNjo...&limit=100
```

The cursor is an opaque base64-encoded JSON of `{started_at, id}` (or `{changed_at, id}` for transition lists). Consumers shouldn't decode it; we reserve the right to change the encoding inside it.

`page.next` is null on the last page. `page.prev` is intentionally not provided — most consumers walk forward, and offering prev would force us to support reverse iteration in indexes we don't currently have.

### Filtering and sorting

Most list endpoints accept filter query params. The convention:

- Equality filters: `?state=Down&check_type=http`
- Range filters: `?started_at__gte=2026-04-01T00:00:00Z&started_at__lt=2026-05-01T00:00:00Z`
- Set filters: `?state__in=Down,Seems%20Down`

Sorting is fixed per endpoint to one of two sensible defaults (newest-first for incidents, alphabetical for sites). We do not expose `?order_by=...` — letting consumers pick arbitrary sort columns means we have to maintain indexes for all of them.

### Error model

```json
{
  "error": {
    "code": "site_not_found",
    "message": "Site with id 12345 does not exist or is not visible to this token",
    "request_id": "req_018f9a2c..."
  }
}
```

Error `code` values are documented per endpoint and stable across versions. The `message` is for humans and may improve over time. `request_id` matches a server-side log line for support tickets.

HTTP status codes used:

- `200` — success
- `201` — resource created (CRUD POST)
- `204` — success, no body (DELETE)
- `400` — malformed request (bad JSON, invalid filter syntax, unknown field)
- `401` — missing or invalid token
- `403` — token valid but lacks required scope
- `404` — resource genuinely doesn't exist
- `409` — idempotent re-attempt with different body (state already different)
- `422` — semantic validation failure (e.g. invalid URL format)
- `429` — rate limit exceeded
- `500` — server error
- `503` — temporarily unavailable (DB down, etc.)

403 vs 404 are honest here: a `read`-scope token hitting a `write`-only endpoint gets a real 403, not a 404. Internal consumers benefit from accurate semantics over the "hide existence" pattern public APIs use to avoid information leakage — and the gateway in front of Jetmon handles any customer-facing 403↔404 collapsing it wants.

Error messages are verbose by design — for an internal API, "table 'jetpack_monitor_events' is locked, retry in 30s" beats "internal server error" by a wide margin during incident response. The gateway can sanitize before forwarding to customers.

### Rate limiting

Per-key bucket, configurable per consumer at key-creation time. The current implementation uses one in-memory bucket per key, sized by that key's `rate_limit_per_minute`. Defaults are 60 req/min for `read` and `admin`, and 30 req/min for `write`. Internal consumers usually need higher limits than the default — the gateway and dashboard might be set to 600 req/min, while a daily batch job stays at 60.

Standard headers on every response:

```
X-RateLimit-Limit: 60
X-RateLimit-Remaining: 47
X-RateLimit-Reset: 1714685400
```

`429` responses include `Retry-After` in seconds.

This is service-protection rate limiting, not customer-fairness rate limiting — the gateway handles the latter. If trigger-now traffic needs a separate bucket later, add it as a route-specific extension rather than overloading the base per-key limit.

### Idempotency

POST endpoints that create, trigger, test, retry, rotate, or manually close resources accept an `Idempotency-Key` header. PATCH and DELETE endpoints are already idempotent on this schema and do not use the idempotency cache. The server stores `(token_id, idempotency_key) → response` for 24 hours. Replays with the same body return the cached response; replays with a different body return `409 idempotency_conflict`.

This is the same pattern Stripe uses; it's the right call for monitor management where retries are common.

### Time

All timestamps are ISO 8601 with millisecond precision and `Z` suffix:

```
"started_at": "2026-04-25T03:18:38.329Z"
```

The server is always UTC. Clients converting to local time is their problem.

---

## Status and state vocabulary

The API exposes the same vocabulary the orchestrator and event store use. From `taxonomy.md` Part 3 and `events.md`:

**State** (string, human-readable):

| Value | Meaning |
|-------|---------|
| `Up` | All checks passing. |
| `Warning` | Something needs attention but isn't user-facing yet (cert expiring, version behind). |
| `Degraded` | Some checks failing or thresholds exceeded; site is serving content. |
| `Seems Down` | First failure detected, awaiting verifier confirmation. Transient. |
| `Down` | Confirmed failures on critical checks. |
| `Paused` | Monitoring suspended by user. |
| `Maintenance` | Scheduled maintenance window active. |
| `Unknown` | Monitor couldn't determine state (probe crashed, region offline, agent silent). |
| `Resolved` | (Events only) The condition cleared; event is closed. |

**Severity** (integer 0–255, ordered):

| Value | Default state mapping |
|-------|----------------------|
| 0 | Up |
| 1 | Warning |
| 2 | Degraded |
| 3 | Seems Down |
| 4 | Down |

Higher severity = worse. Severity climbs independently of state — a worsening Degraded event bumps severity without changing state. New severity values can be added (e.g. 5 for "data loss confirmed") without breaking ordering. Consumers should treat severity as a numeric comparison, not a switch on specific values.

**Why expose both?** Severity is for thresholds (`severity >= 3 ? page on-call : email digest`); state is for human-readable rendering (`incident.state == "Seems Down" ? badge.color = yellow`). Competitors that collapse to one field force consumers to either parse a string for ordering or build their own numeric mapping.

---

## Endpoints

The full surface is grouped into five capability families, matching `roadmap.md`. The implemented route table lives in `internal/api/routes.go`; design-only additions and deferred behavior are called out where they appear.

### Family 1: Sites and current state

#### `GET /api/v1/sites`

List sites visible to this token.

**Scopes:** `read`

Normal internal callers see the full site table. Gateway-routed requests only
see rows mapped to `X-Jetmon-Tenant-ID` in `jetpack_monitor_site_tenants`.

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `cursor` | string | Pagination cursor |
| `limit` | int (1–200) | Default 50 |
| `state` | string | Filter by current state (e.g. `Down`) |
| `state__in` | csv | Multiple states |
| `severity__gte` | int | Minimum severity |
| `monitor_active` | bool | Filter active vs paused |
| `q` | string | URL substring search |
| `include_cli_metadata` | bool | Optional local-tooling projection; when true, includes `cli_batch` if the site carries the CLI batch marker |

**Response 200:**

```json
{
  "data": [
    {
      "id": 12345,
      "blog_id": 12345,
      "monitor_url": "https://example.com",
      "monitor_active": true,
      "bucket_no": 0,
      "check_interval": 5,
      "current_state": "Up",
      "current_severity": 0,
      "active_event_id": null,
      "last_checked_at": "2026-04-25T03:24:11.123Z",
      "last_status_change_at": "2026-04-21T09:14:00.000Z",
      "ssl_expiry_date": "2026-08-12",
      "check_keyword": null,
      "forbidden_keyword": null,
      "forbidden_keywords": null,
      "redirect_policy": "follow",
      "request_method": "GET",
      "detection_profile": "full",
      "maintenance_start": null,
      "maintenance_end": null,
      "alert_cooldown_minutes": null
    }
  ],
  "page": { "next": "eyJ...", "limit": 50 }
}
```

`id` and `blog_id` are the same value for now; `id` is the public field name (`blog_id` is the historical column name). Consumers should rely on `id`.

The response intentionally merges v1-shaped `jetpack_monitor_sites` fields with
v2-owned sidecar state from `jetpack_monitor_site_check_config` and
`jetpack_monitor_site_runtime`; callers should use the API contract instead of assuming
all fields live in the legacy site table.

`cli_batch` is an opt-in local-tooling projection. It is present only when
`include_cli_metadata=true` and the site's `custom_headers` include
`X-Jetmon-CLI-Batch`; the API does not expose the rest of `custom_headers`.

`current_state`, `current_severity`, and `active_event_id` are derived from
open rows in `jetpack_monitor_events`. During the
[v1-to-v2 migration](v1-to-v2-migration.md), the legacy `site_status`
column is only a fallback for sites with no active v2 event while
`LEGACY_STATUS_PROJECTION_ENABLE` is true; once the projection is disabled, a
site with no active v2 event is reported as `Up` regardless of stale legacy
status values.

#### `GET /api/v1/sites/{id}`

Single site, same shape as a list entry plus an `active_events` array for any open events:

Accepts `include_cli_metadata=true` with the same `cli_batch` behavior as
`GET /api/v1/sites`.

```json
{
  "id": 12345,
  ...
  "active_events": [
    {
      "id": 487291,
      "check_type": "http",
      "severity": 4,
      "state": "Down",
      "started_at": "2026-04-25T03:18:38.329Z"
    },
    {
      "id": 487288,
      "check_type": "tls_expiry",
      "severity": 1,
      "state": "Warning",
      "started_at": "2026-04-23T00:00:00.000Z"
    }
  ]
}
```

`active_events` is the simplest answer to "tell me everything wrong with this site right now." Ordered by severity descending.

Gateway-routed single-site, event/history, SLA/stat, and trigger-now routes all
derive visibility through `jetpack_monitor_site_tenants`. A site or event outside the
tenant mapping is returned as not found.

#### `POST /api/v1/sites`

Create a site.

**Scopes:** `write`

**Request body:**

```json
{
  "blog_id": 12345,
  "monitor_url": "https://example.com",
  "monitor_active": true,
  "bucket_no": 0,
  "check_keyword": null,
  "forbidden_keyword": null,
  "forbidden_keywords": [
    "metrics.evil-cdn.example/collect.js",
    "buy cheap viagra"
  ],
  "redirect_policy": "follow",
  "request_method": "GET",
  "detection_profile": "full",
  "timeout_seconds": null,
  "custom_headers": {},
  "alert_cooldown_minutes": null,
  "check_interval": 5
}
```

**Response 201:** the site object.

When the `gateway` consumer creates a site with tenant context, Jetmon inserts
the site row and the `(tenant_id, blog_id)` mapping in one transaction. Internal
creates without tenant context keep the existing unscoped behavior.

`request_method` accepts `HEAD` or `GET`. `detection_profile` accepts
`legacy`, `simple_http`, or `full`. Omit either field to inherit the process
default. During rollout, use `HEAD` + `legacy`, then `GET` + `simple_http`,
then `GET` + `full`.

`legacy` here means v1-compatible probe behavior for that site. It does not
mean the Monitor or Veriflier should use the optional legacy-compatible
Veriflier HTTP endpoints; v2 Verifliers carry `HEAD` and `GET` requests through
the `/v2/check` contract.

**Errors:**

| Code | Meaning |
|------|---------|
| `invalid_blog_id` | `blog_id` is missing or not a positive integer |
| `invalid_url` | `monitor_url` doesn't parse |
| `invalid_redirect_policy` | `redirect_policy` is not `follow`, `alert`, or `fail` |
| `invalid_check_policy` | `request_method` or `detection_profile` is not supported |
| `invalid_custom_headers` | `custom_headers` is not a valid string map |
| `invalid_forbidden_keywords` | `forbidden_keywords` is too large or contains invalid entries |
| `site_exists` | A site with this `blog_id` already exists |

#### `PATCH /api/v1/sites/{id}`

Partial update. Send only the fields you want to change.
Send `"forbidden_keywords": []` to clear the multi-keyword forbidden-content
list. The legacy `forbidden_keyword` string remains supported for simple
one-off rules and compatibility.

#### `DELETE /api/v1/sites/{id}`

Soft-delete (sets `monitor_active = false` and tombstones). Closes any active events with `resolution_reason = manual_override`.

Delete is intentionally idempotent and preserves the site row. Repeating
`DELETE /api/v1/sites/{id}` returns `204 No Content`, and a later
`GET /api/v1/sites/{id}` returns `200 OK` with the same site object and
`monitor_active: false`. Consumers should treat `monitor_active:false` as the
readable deleted/paused state rather than expecting a `404` after delete.

#### `POST /api/v1/sites/{id}/pause`, `POST /api/v1/sites/{id}/resume`

Convenience verbs for the common pause/resume flow. Pause closes any active events with `resolution_reason = manual_override` and sets `current_state = "Paused"`. Resume reverts.

#### `POST /api/v1/sites/{id}/trigger-now`

Force an immediate check, returning the result inline under the caller's normal per-key rate limit. Useful for "I just deployed a fix, is it back up?"

```json
{
  "result": {
    "http_code": 200,
    "error_code": 0,
    "success": true,
    "rtt_ms": 412,
    "dns_ms": 8,
    "tcp_ms": 22,
    "tls_ms": 35,
    "ttfb_ms": 142,
    "ssl_expires_at": "2026-08-12T00:00:00.000Z"
  },
  "current_state": "Up",
  "active_events_closed": [487291]
}
```

Trigger-now runs one synchronous check with a 30-second server-side timeout.
On success it closes any open events with `resolution_reason=probe_cleared`.
On failure it returns the failed check result but does not open a new event;
the orchestrator remains the single owner of failure detection and event
opening on its regular round.

### Family 2: Events and history

#### `GET /api/v1/sites/{id}/events`

Incident history for a site. Default sort: most recent `started_at` first.

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `cursor`, `limit` | | Standard |
| `state` / `state__in` | string | Filter by state |
| `check_type` / `check_type__in` | string | `http`, `tls_expiry`, etc. |
| `started_at__gte` / `started_at__lt` | ISO timestamp | Time range |
| `active` | bool | `true` → only open events; `false` → only closed |

**Response:**

```json
{
  "data": [
    {
      "id": 487291,
      "site_id": 12345,
      "endpoint_id": null,
      "check_type": "http",
      "discriminator": null,
      "severity": 4,
      "state": "Down",
      "started_at": "2026-04-25T03:18:38.329Z",
      "ended_at": "2026-04-25T03:21:17.290Z",
      "resolution_reason": "verifier_cleared",
      "cause_event_id": null,
      "metadata": {
        "http_code": 503,
        "error_code": 0,
        "failure_class": "server",
        "method": "GET",
        "rtt_ms": 84,
        "url": "https://example.com",
        "redirect_policy": "follow",
        "tls_version": "TLS 1.3",
        "tls_version_code": "0x0304",
        "cipher_suite": "TLS_AES_128_GCM_SHA256",
        "cipher_suite_id": "0x1301",
        "observation": {
          "checked_at": "2026-04-25T03:18:38.329Z",
          "first_failed_at": "2026-04-25T03:18:38.329Z",
          "previous_observed_at": "2026-04-25T03:15:38.018Z",
          "previous_known_good_at": "2026-04-25T03:15:38.018Z",
          "normal_check_interval_seconds": 180,
          "next_check_interval_seconds": 60
        }
      },
      "duration_ms": 158961,
      "transition_count": 5
    }
  ],
  "page": { "next": "eyJ...", "limit": 50 }
}
```

`duration_ms` is a server-computed convenience: `(ended_at or now) - started_at`. `transition_count` lets the consumer decide whether to fetch the full transition log.

When a failed HTTP probe reaches the resolver and Go exposes DNS details,
`metadata` may also include `dns_error_kind` (`nxdomain`, `servfail`, `timeout`,
or `resolver_error`), `dns_error_name`, and `dns_error_server`. These fields are
diagnostic context for operators; dedicated DNS monitors remain a separate
future check type.

#### `GET /api/v1/sites/{id}/events/{event_id}`

Single event, same shape, plus a `transitions` array (full history, no pagination — events have bounded transition counts).

```json
{
  "id": 487291,
  ...
  "transitions": [
    {
      "id": 1,
      "severity_before": null,
      "severity_after": 3,
      "state_before": null,
      "state_after": "Seems Down",
      "reason": "opened",
      "source": "host-us-west-1",
      "metadata": {
        "http_code": 503,
        "error_code": 0,
        "failure_class": "server",
        "rtt_ms": 84,
        "observation": {
          "checked_at": "2026-04-25T03:18:38.329Z",
          "first_failed_at": "2026-04-25T03:18:38.329Z",
          "previous_known_good_at": "2026-04-25T03:15:38.018Z"
        }
      },
      "changed_at": "2026-04-25T03:18:38.329Z"
    },
    {
      "id": 2,
      "severity_before": 3,
      "severity_after": 4,
      "state_before": "Seems Down",
      "state_after": "Down",
      "reason": "verifier_confirmed",
      "source": "host-us-west-1",
      "metadata": { "verifier_results": [...], "verifier_confirmed": 2 },
      "changed_at": "2026-04-25T03:18:55.412Z"
    }
  ]
}
```

#### `GET /api/v1/sites/{id}/events/{event_id}/transitions`

Same transition data, but as its own paginated list when an event has accumulated many transitions (long-running degradation events with hundreds of severity bumps).

#### `GET /api/v1/events/{event_id}`

Direct event lookup without site context. Useful for webhook payloads that link directly to an incident page.

#### `POST /api/v1/sites/{id}/events/{event_id}/close`

Manually close an open event (for the operator dashboard or for handling false alarms the verifier missed).

**Scopes:** `write`

**Request body:**

```json
{
  "reason": "manual_override",
  "note": "Confirmed maintenance was running, alert fired before window started"
}
```

`note` ends up in the closing transition's metadata.

### Family 3: SLA and statistics

#### `GET /api/v1/sites/{id}/uptime`

Uptime and downtime stats over a rolling window.

**Query parameters:**

| Param | Type | Description |
|-------|------|-------------|
| `window` | enum | `1h`, `24h` / `1d`, `7d`, `30d`, `90d` |
| `from` / `to` | ISO timestamp | Custom range; overrides `window` |

**Response:**

```json
{
  "window": { "from": "2026-03-26T00:00:00Z", "to": "2026-04-25T00:00:00Z" },
  "uptime_percent": 99.847,
  "total_seconds": 2592000,
  "down_seconds": 3960,
  "degraded_seconds": 600,
  "warning_seconds": 86400,
  "maintenance_seconds": 0,
  "unknown_seconds": 0,
  "incident_count": 4,
  "mttr_seconds": 990,
  "mtbf_seconds": 647760
}
```

**How uptime is computed:** sum of `(ended_at or now) - started_at` for events with `state in (Down, Seems Down)` within the window, divided by total window duration. Degraded, Warning, Maintenance, and Unknown durations are returned separately but are not subtracted from the denominator in the current implementation. The math is event-driven, not check-driven, which means SLA reports stay accurate even if check frequency changes.

#### `GET /api/v1/sites/{id}/response-time`

Response time percentiles over a window, sourced from `jetpack_monitor_check_history`.

**Response:**

```json
{
  "window": { "from": "2026-04-24T00:00:00Z", "to": "2026-04-25T00:00:00Z" },
  "samples": 17280,
  "p50_ms": 187,
  "p95_ms": 412,
  "p99_ms": 891,
  "max_ms": 4200,
  "mean_ms": 215,
  "truncated": false,
  "check_history_mode": "all",
  "percentiles_meaningful": true
}
```

Percentiles are computed from raw `jetpack_monitor_check_history` samples in the window. The handler caps the in-memory sample set at 100,000 rows; `truncated: true` means the response used the most recent capped subset.

`check_history_mode` is the effective recording mode for this site (per-site override in `jetpack_monitor_site_check_config.check_history_mode`, else `CHECK_HISTORY_MODE_DEFAULT`). `percentiles_meaningful` is `false` when the mode is `status_change` (incident-edge probes only) or `disabled` (no rows) — in those modes the percentile fields reflect too few samples to represent the site's true latency distribution, and a dashboard should hide or annotate them. Use `/check-history` for raw rows under any mode.

#### `GET /api/v1/sites/{id}/timing-breakdown`

DNS / TCP / TLS / TTFB breakdown — one of Jetmon's distinctive features (most competitors only return total response time).

**Response:**

```json
{
  "window": { "from": "2026-04-24T00:00:00Z", "to": "2026-04-25T00:00:00Z" },
  "samples": 17280,
  "truncated": false,
  "check_history_mode": "all",
  "percentiles_meaningful": true,
  "dns": { "p50_ms": 8, "p95_ms": 45, "p99_ms": 80, "max_ms": 120 },
  "tcp": { "p50_ms": 22, "p95_ms": 78, "p99_ms": 140, "max_ms": 220 },
  "tls": { "p50_ms": 35, "p95_ms": 110, "p99_ms": 180, "max_ms": 260 },
  "ttfb": { "p50_ms": 142, "p95_ms": 391, "p99_ms": 760, "max_ms": 1200 }
}
```

`check_history_mode` / `percentiles_meaningful` carry the same meaning as in `/response-time`.

#### `GET /api/v1/sites/{id}/check-history`

Raw per-check timing rows for a site, newest-first, with cursor pagination. Unlike the percentile endpoints, this works under any `CHECK_HISTORY_MODE`: at `status_change` it returns the incident-edge probes that were recorded; at `sample`/`all` it returns the fuller stream. Query params: `window` (or `from`+`to`), `limit` (default 50, max 200), `cursor`.

```bash
curl -H "Authorization: Bearer $JETMON_API_TOKEN" \
  "$JETMON_API_URL/api/v1/sites/42/check-history?window=24h&limit=100"
```

```json
{
  "data": [
    {
      "id": 90183,
      "request_method": "GET",
      "http_code": 200,
      "error_code": 0,
      "rtt_ms": 187,
      "dns_ms": 8,
      "tcp_ms": 22,
      "tls_ms": 35,
      "ttfb_ms": 142,
      "checked_at": "2026-04-24T18:03:11Z"
    }
  ],
  "page": { "next": "…", "limit": 100 }
}
```

Component timings (`dns_ms`/`tcp_ms`/`tls_ms`/`ttfb_ms`) are `null` for checks that failed before that phase completed. This is the right endpoint for "what did the check look like at the moment the site went down" forensics.

### Family 4: Alert contacts and webhooks

#### Webhook management endpoints

Implemented routes:

- `GET /api/v1/webhooks`
- `POST /api/v1/webhooks`
- `GET /api/v1/webhooks/{id}`
- `PATCH /api/v1/webhooks/{id}`
- `DELETE /api/v1/webhooks/{id}`
- `POST /api/v1/webhooks/{id}/rotate-secret`
- `GET /api/v1/webhooks/{id}/deliveries`
- `POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/retry`

Standard CRUD. A webhook is:

```json
{
  "id": 42,
  "url": "https://hooks.slack.com/...",
  "active": true,
  "events": ["event.opened", "event.severity_changed", "event.closed"],
  "site_filter": { "site_ids": [12345, 67890] },
  "state_filter": { "states": ["Down", "Seems Down"] },
  "secret": "whsec_a1b2c3...",
  "created_at": "2026-04-01T00:00:00Z"
}
```

`secret` is the only string-prefixed identifier in the API surface — it's a shared secret, not a resource id, and the `whsec_` prefix is a Stripe-style hint to anyone scanning logs/leaks ("this is a webhook signing secret, treat as sensitive"). It is shown only on creation; afterward only `secret_preview` is returned (last 4 chars).

#### Filter semantics

Filters compose **AND across dimensions, whitelist within each, empty = match all**. A delivery fires when:

```
event_type ∈ events (or events == [])
AND site_id  ∈ site_filter.site_ids (or site_filter == {})
AND state    ∈ state_filter.states (or state_filter == {})
```

Empty fields mean "no restriction on this dimension," matching the everyday English meaning of an empty filter. Same convention as Stripe, GitHub, and Slack webhooks — consumers can omit dimensions they don't care about and progressively narrow as needed. Blacklist/exclude fields are not supported in v1.

#### Webhook delivery format

When an event fires, Jetmon POSTs to the webhook URL:

```json
{
  "type": "event.opened",
  "delivered_at": "2026-04-25T03:18:38.500Z",
  "delivery_id": 9182734,
  "event": { ... full event object ... },
  "site": { ... full site object ... }
}
```

Headers:

```
Content-Type: application/json
X-Jetmon-Event: event.opened
X-Jetmon-Delivery: 9182734
X-Jetmon-Signature: t=1714685400,v1=5257a869e7ec...
```

The signature is HMAC-SHA256 of `{timestamp}.{body}` with the webhook's `secret`, formatted Stripe-style (timestamp + scheme version + signature). The timestamp prevents replay; consumers should reject deliveries older than 5 minutes.

#### Webhook event types

- `event.opened` — new event row inserted
- `event.severity_changed` — severity escalated or de-escalated
- `event.state_changed` — state changed (e.g. Seems Down → Down)
- `event.cause_linked` / `event.cause_unlinked`
- `event.closed` — event resolved (any reason)

`event.*` types fire once per transition row written to `jetpack_monitor_event_transitions` — i.e., once per actual mutation. The 1:1 invariant the eventstore maintains is what makes detection reliable.

**Deferred:** `site.state_changed` (rollup from events to the site-row projection) is **not** in v1. Rolling up cleanly without races requires changes to the orchestrator, and event-level webhooks already give consumers everything they need. Tracked in roadmap.md.

#### Detection mechanism

Webhook delivery uses **pull-based detection**: a worker polls `jetpack_monitor_event_transitions WHERE id > last_seen` on a 1s interval and creates one delivery row per matching transition. This is the long-term answer for Jetmon's architecture — the orchestrator's flap suppression already adds 10s+ between detection and confirmed events, so 1s poll latency is invisible in the practical budget.

Current v2 deployment constraint: in the single-binary shape, `API_PORT` makes webhook and alert-contact workers eligible to run. Delivery rows are claimed transactionally, so multiple active delivery workers do not claim the same pending row. `DELIVERY_OWNER_HOST` can still restrict actual delivery to one named host when operators want a single-owner rollout while moving from embedded `jetmon2` delivery to standalone `jetmon-deliverer`.

Push-based or hybrid detection is not on the roadmap. If a future consumer demands sub-second webhook latency, that's the trigger to introduce a pub/sub layer — not before.

#### Retry policy

Each `jetpack_monitor_webhook_deliveries` row is one webhook firing. Each delivery has up to 6 attempts on this exponential schedule:

| Attempt | Delay from previous |
|---------|---------------------|
| 1       | immediate           |
| 2       | 1m                  |
| 3       | 5m                  |
| 4       | 30m                 |
| 5       | 1h                  |
| 6       | 6h                  |

A delivery succeeds when any attempt returns 2xx. After 6 failed attempts, the row is marked `status = 'abandoned'`. Abandoned rows stay in the table — `GET /api/v1/webhooks/{id}/deliveries?status=abandoned` lists them, and `POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/retry` lets a consumer re-fire after fixing their endpoint.

`GET /api/v1/webhooks/{id}/deliveries` returns the full delivery history with `status` (`pending` / `delivered` / `failed` / `abandoned`), `attempt`, `last_status_code`, and a truncated `last_response` body for debugging.

#### Signing and secret rotation

Signature: HMAC-SHA256 of `{timestamp}.{body}` with the webhook's secret, sent as `X-Jetmon-Signature: t=<unix_ts>,v1=<hex>`. The timestamp prevents replay; consumers must reject deliveries older than 5 minutes (`webhooks.VerifyMaxAge`) or more than 1 minute in the future (`webhooks.VerifyMaxClockSkew`).

**Verifying a delivery (reference implementations).** All three examples enforce the same two checks: constant-time signature equality and the timestamp window. Pick the language for your consumer; the algorithm is identical.

Go (drop-in via `internal/webhooks`):

```go
import "github.com/Automattic/jetmon/internal/webhooks"

if err := webhooks.Verify(
    r.Header.Get("X-Jetmon-Signature"),
    body,
    secret,
    0,            // 0 = use webhooks.VerifyMaxAge (5 min)
    time.Now(),
); err != nil {
    http.Error(w, "invalid signature", http.StatusUnauthorized)
    return
}
```

PHP:

```php
function verify_jetmon_signature(string $header, string $body, string $secret): bool {
    if (!preg_match('/^t=(\d+),v1=([a-f0-9]+)$/', $header, $m)) {
        return false;
    }
    [$_, $ts, $sig] = $m;
    $age = time() - (int) $ts;
    if ($age > 300 || $age < -60) { // 5 min stale, 1 min skew
        return false;
    }
    $expected = hash_hmac('sha256', $ts . '.' . $body, $secret);
    return hash_equals($expected, $sig);
}
```

Python:

```python
import hashlib, hmac, time, re

_SIG_RE = re.compile(r"^t=(\d+),v1=([a-f0-9]+)$")

def verify_jetmon_signature(header: str, body: bytes, secret: str) -> bool:
    m = _SIG_RE.match(header)
    if not m:
        return False
    ts, sig = int(m.group(1)), m.group(2)
    age = time.time() - ts
    if age > 300 or age < -60:  # 5 min stale, 1 min skew
        return False
    expected = hmac.new(
        secret.encode(), f"{ts}.".encode() + body, hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(expected, sig)
```

All three reject on: malformed header, missing `t=` or `v1=`, signed timestamp older than 5 min or further than 1 min ahead, mismatched HMAC. Don't skip either gate: signature-only allows replay; timestamp-only allows forgery.

Format chosen for: wide library support across consumer languages, explicit version (`v1=`) to allow future algorithm rotation without breaking consumers, replay protection via timestamp baked into the signature input, and the ability to coexist with multiple `v1=` values during a grace-period rotation (deferred). Alternatives considered and not chosen: GitHub-style (no replay protection), Slack-style (functionally equivalent, two-header form), JWT-based (wrong abstraction for "POST JSON + signature header"), HTTP Message Signatures / RFC 9421 (over-engineered for our scope), asymmetric / Ed25519 (compelling for public APIs without a gateway in front; not warranted while a gateway re-signs for end customers).

When to revisit: a public-API-without-gateway requirement (then asymmetric becomes attractive — no per-consumer secret distribution), or a standards-driven third-party integration that requires RFC 9421. Migration path in either case is "add a `v2=` signature alongside `v1=` for a transition window, switch consumers, deprecate `v1=`" — same shape as algorithm rotation we already designed for.

Secret rotation in v1: **immediate revocation only**. `POST /api/v1/webhooks/{id}/rotate-secret` returns a new secret once, replaces the stored hash, and the old secret stops working immediately. Failed deliveries during the consumer's deploy window go into the retry queue.

**Deferred:** grace-period rotation (server signs with both old and new secrets for a configurable window so consumers can roll over without coordinated downtime) is in roadmap.md. The signature header format already supports multiple `v1=...,v1=...` values per Stripe convention, so adding grace-period rotation later is non-breaking.

#### Backpressure

Delivery uses a **shared worker pool** (default 50 goroutines, configurable) with a **per-webhook in-flight cap** (default 3 concurrent). The shared pool bounds total goroutine count; the per-webhook cap prevents a slow or hung webhook URL from monopolizing the pool and starving other webhooks' deliveries.

Implementation: at dispatch time, the worker checks a `map[webhook_id]int` counter under a mutex. If a webhook is already at its cap, the row stays `pending` and is picked up on the next poll tick. The counter decrements when a delivery attempt completes (success or failure).

#### Schema

```
jetpack_monitor_webhooks:
  id, url, active, owner_tenant_id VARCHAR(128) NULL,
  events JSON, site_filter JSON, state_filter JSON,
  secret VARCHAR(80), secret_preview VARCHAR(8),
  created_by VARCHAR(128), created_at, updated_at

jetpack_monitor_webhook_deliveries:
  id, webhook_id, transition_id, event_id, event_type,
  payload JSON,                       -- frozen at fire time, never updated
  status ENUM('pending','delivered','failed','abandoned'),
  attempt INT,
  next_attempt_at TIMESTAMP NULL,     -- when the worker should pick up
  last_status_code INT NULL,
  last_response VARCHAR(2048) NULL,   -- truncated body, debugging aid
  last_attempt_at TIMESTAMP NULL,
  delivered_at TIMESTAMP NULL,
  created_at
```

Indexes:
- `(status, next_attempt_at)` on deliveries — the worker's "what's ready?" query
- `(webhook_id, created_at)` on deliveries — the deliveries-list endpoint
- `(active)` on webhooks — the dispatcher's filter for live webhooks
- `(owner_tenant_id)` on webhooks — scopes gateway-routed CRUD and delivery visibility while normal internal callers remain unscoped

`payload` is **frozen at delivery creation**: the consumer sees the event as it was when the webhook fired, not as it is now. A closed-and-amended event would not change a delivery's payload — that's the contract consumers expect ("this is what I was told happened, not whatever it became").

#### Webhook ownership and scope

Webhooks are managed by any `write`-scope token. `created_by` records the consumer name from the API key for audit purposes only — there is no per-consumer ownership boundary, and any `write`-scope token can read/edit/delete any webhook.

This is appropriate **only** because Jetmon is internal-only with all consumers trusted. Per-consumer ownership doesn't add value at this scale; the gateway in front of Jetmon handles tenant isolation for any customer-facing webhooks.

The table includes nullable `owner_tenant_id`. Normal internal handlers remain
unscoped when no gateway context is present, so existing internal behavior is
unchanged. Gateway-routed creates set `owner_tenant_id`, and gateway-routed
list/get/update/delete/rotate-secret paths filter by it. Delivery history and
manual retry visibility are derived by first verifying ownership of the parent
webhook.

**Ramifications if Jetmon ever becomes a public API:**

- This model would need to change. Customer-facing consumers cannot be allowed to read or modify each other's webhooks.
- Migration path: continue requiring `owner_tenant_id` on gateway-routed
  creates; add granular public `webhooks` scopes or a formal account/tenant
  boundary before any direct customer exposure.
- The `created_by` field is forward-compatible — it's already capturing the consumer identity, just not enforcing it.
- Existing webhooks would need a backfill migration before being exposed publicly.
- Webhook secrets would need stronger isolation (currently any write-scope can rotate any secret; in a public API this would be a privilege escalation).

The decision to defer ownership today should be reread before any public-API conversation actually starts.

### Family 5: Alert contacts

Managed notification channels for human destinations: email, PagerDuty, Slack, Microsoft Teams. Where webhooks (Family 4) deliver a raw signed event stream that the consumer renders, alert contacts deliver a Jetmon-rendered notification through a transport Jetmon owns end-to-end (subject lines, message formatting, transport-specific quirks).

#### When to use which

- **Alert contact** — you want a person notified through a managed channel (their email, your team's PagerDuty service, your team's Slack channel). You don't want to operate a receiver, you want Jetmon to handle rendering and retries.
- **Webhook** — you want a *system* notified, you control the receiver, and you want the raw signed event payload to render or route however you want. Use this for custom Slack bots that aren't a vanilla incoming-webhook URL, internal SIEM ingestion, custom alerting middleware, or anything that wants the structured event rather than a pre-formatted message.

The two surfaces share the same event source (`jetpack_monitor_event_transitions`); a customer can use both simultaneously without dedup concerns at the source.

#### Alert contact management endpoints

Implemented routes:

- `GET /api/v1/alert-contacts`
- `POST /api/v1/alert-contacts`
- `GET /api/v1/alert-contacts/{id}`
- `PATCH /api/v1/alert-contacts/{id}`
- `DELETE /api/v1/alert-contacts/{id}`
- `POST /api/v1/alert-contacts/{id}/test`
- `GET /api/v1/alert-contacts/{id}/deliveries`
- `POST /api/v1/alert-contacts/{id}/deliveries/{delivery_id}/retry`

Standard CRUD. An alert contact is:

```json
{
  "id": 17,
  "label": "platform-oncall",
  "active": true,
  "transport": "pagerduty",
  "destination": { "integration_key": "***" },
  "site_filter": { "site_ids": [12345, 67890] },
  "min_severity": "Down",
  "max_per_hour": 60,
  "destination_preview": "abcd",
  "created_by": "alerts-admin",
  "created_at": "2026-04-25T00:00:00Z"
}
```

`destination` shape varies by transport (see below); credential fields are write-only and only `destination_preview` (last 4 chars of the credential) is returned on subsequent reads.

#### Transports

| Transport | `destination` shape | Notes |
|-----------|---------------------|-------|
| `email` | `{ "address": "ops@example.com" }` | Rendered as a plain-text + HTML email. Sent via the configured email transport (see "Email delivery" below). |
| `pagerduty` | `{ "integration_key": "<events-v2 routing key>" }` | Posts to PagerDuty Events API v2. Jetmon severity maps to PagerDuty severity: `Down`/`SeemsDown` → `critical`, `Degraded` → `warning`, `Warning` → `info`, `Up` → resolves the alert. |
| `slack` | `{ "webhook_url": "https://hooks.slack.com/..." }` | Posts to a Slack incoming-webhook URL. Renders a Block Kit message with site, state, severity, and an event link. |
| `teams` | `{ "webhook_url": "https://outlook.office.com/webhook/..." }` | Posts to a Microsoft Teams incoming-webhook URL. Renders an Adaptive Card with the same fields as Slack. |

Custom transports (Slack via OAuth bot, OpsGenie, internal SIEM, etc.) go through the webhooks API instead — register a webhook, render however you want.

#### Filter semantics

Alert contacts use a simpler filter model than webhooks: **site list + severity gate**. A contact fires when:

```
site_id ∈ site_filter.site_ids   (or site_filter == {} → all sites)
AND new_severity >= min_severity (Up=0 < Warning=1 < Degraded=2 < SeemsDown=3 < Down=4)
```

Empty `site_filter` means "all sites." `min_severity` is required and defaults to `Down` on create — this is the most common case (page me only on real outages) and avoids accidental noise from new contacts.

The severity values match `internal/eventstore.Severity*` constants directly; the API exposes them by string name in JSON (`"Down"`, `"SeemsDown"`, etc.) and stores them as the underlying `uint8` in the database.

The simpler filter model is intentional. Most alert contact configs are "this person, these sites, only when something serious happens"; event-type and state filters (which webhooks support) are rarely useful for human pagers — if you got the open page you almost always want the close page too. Customers who need finer-grained filtering register a webhook instead.

#### Severity gate

Severity ordering: `Up < Warning < Degraded < SeemsDown < Down`. The gate matches `new_severity >= min_severity` on each transition; events that *increase* into the gated band send a page, events that *resolve back to `Up`* send a recovery notification, events that move between two severities both below the gate are silently dropped.

This lets agencies and VIPs configure low-severity contacts (e.g. `min_severity: "Warning"`) that catch every flicker while still letting normal users configure `Down`-only contacts that only fire on real outages — both from the same plumbing.

#### Per-contact rate cap

`max_per_hour` (default 60, set to `0` for unlimited) caps how many notifications a single contact can receive per rolling hour. Designed against the pager-storm scenario where a regional outage flips 200 sites at once; without a cap, on-call gets paged 200 times in 30 seconds. When the cap is hit, further transitions for that contact are marked `abandoned` with a rate-limit note and are not dispatched. Digest notifications are deferred.

This is a per-contact field, not global — different contacts have different tolerance (a Slack channel can take far more than a PagerDuty oncall can).

#### Send-test

```
POST /api/v1/alert-contacts/{id}/test
```

Sends a synthetic notification through the contact's transport — same rendering, same dispatch path, but with payload `{"test": true, "message": "Jetmon test notification", ...}`. Used by operators to verify a newly-created contact actually reaches its destination. Test sends are exempt from `max_per_hour`, are logged in `jetpack_monitor_audit_log` under `event_type=alert_test`, and bypass the severity gate (always delivered).

Honors `Idempotency-Key` like the other write POSTs — a retried request with the same key returns the original response without re-firing the test, so a network blip during the operator's "click to test" doesn't double-page the destination.

Returns `200 OK` with the test delivery row, or surfaces the transport error (e.g. invalid Slack webhook URL) directly so operators can debug without spelunking through worker logs.

#### Email delivery

Email is unique among the transports in that there is no equivalent of "post to this URL" — it requires a sender. Three implementations selectable at startup via `EMAIL_TRANSPORT` config:

| `EMAIL_TRANSPORT` | Use case | Behavior |
|-------------------|----------|----------|
| `wpcom` | Production | Calls existing WPCOM email infrastructure. Default in production deploys. |
| `smtp` | Local dev / staging | Connects to an SMTP server (e.g. Mailpit in the Docker Compose stack). Configurable host/port/auth. |
| `stub` | Local dev / unit testing / disabled email | Logs the rendered email; no actual send. |

The `Sender` interface is internal to the alerting package, so swapping transports is a config change — no code path differences. SMTP support specifically exists so docker-based integration tests can verify rendering and addressing end-to-end without depending on WPCOM infrastructure.

`stub` is the default and the empty-string compatibility alias. Startup and `jetmon2 validate-config` both warn when the resolved transport is `stub` so operators know any alert contact with `transport="email"` will be logged but not delivered.

#### Subscription assignment

Site assignment is via `site_filter.site_ids` on the contact row itself, not a separate join table. Mirrors the webhooks API. Empty list = all sites. Setting `site_filter: {"site_ids": []}` or `{}` is "subscribe to all sites." On create, omitting `site_filter` also produces the empty match-all filter; on PATCH, omitting `site_filter` leaves the existing filter unchanged.

#### Detection mechanism

Same as webhooks — pull-only, polling `jetpack_monitor_event_transitions` on a high-water mark. Different worker (`internal/alerting/`) with the same dispatch shape: claim → match contacts → enqueue per-contact deliveries in `jetpack_monitor_alert_deliveries` → dispatch with retry. Worker placement is intentionally parallel to webhooks rather than unified; see ROADMAP for the rationale and the future revisit point.

#### Retry policy

Same schedule as webhooks: 1m, 5m, 30m, 1h, 6h, then abandon. Different transports have different idempotency stories — PagerDuty Events API is idempotent on `dedup_key`, Slack webhooks are not — so each transport implementation owns its retry-safety guarantee. Worker-level retry is conservative; if the transport library returns success, we never re-send.

#### Relationship to legacy WPCOM notifications

The existing WPCOM notification flow (orchestrator-side, hard-coded recipients)
**continues to operate independently**. During the v2 drop-in rollout,
`WPCOM_NOTIFY_MODE=legacy` preserves the v1-compatible client-certificate
`/jetmon/` notification path; `modern` mode is reserved for WPCOM contract
testing until that endpoint/auth model is approved. Alert contacts are a
parallel programmable path; they don't replace WPCOM notifications, they
coexist.

This means:
- An incident may notify the same human twice if they're configured in both paths. Document this on the operator side and avoid duplicate configuration.
- The two paths have separate retry state, separate metrics, separate audit trails.
- Migrating WPCOM notifications behind alert contacts is a future cleanup tracked in the roadmap, gated on alert contacts proving out in production.

The boundary is: WPCOM = built-in path for existing internal Jetpack notifications; alert contacts = customer-managed destinations through the API. Anything new should go through alert contacts.

#### Schema

```sql
jetpack_monitor_alert_contacts (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  label VARCHAR(80) NOT NULL,
  active TINYINT(1) NOT NULL DEFAULT 1,
  owner_tenant_id VARCHAR(128) NULL,
  transport ENUM('email','pagerduty','slack','teams') NOT NULL,
  destination JSON NOT NULL,          -- transport-specific, secret in plaintext (outbound dispatch needs raw value)
  destination_preview VARCHAR(8) NOT NULL,
  site_filter JSON NOT NULL,          -- {"site_ids":[...]} or {} for all
  min_severity TINYINT UNSIGNED NOT NULL DEFAULT 4,  -- matches eventstore.Severity* (0=Up..4=Down); default 4=Down
  max_per_hour INT NOT NULL DEFAULT 60,
  created_by VARCHAR(80) NOT NULL,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
)

jetpack_monitor_alert_deliveries (
  -- mirrors jetpack_monitor_webhook_deliveries; dedup on (alert_contact_id, transition_id)
)

jetpack_monitor_alert_dispatch_progress (
  -- mirrors jetpack_monitor_webhook_dispatch_progress; high-water mark for the worker
)
```

`destination` stores the credential in plaintext. Same rationale as `jetpack_monitor_webhooks.secret`: outbound dispatch needs the raw value (PagerDuty integration key, Slack webhook URL, SMTP password) at every send — a hash is useless because we'd have to recover the original to call the transport. The threat model is the database itself; encryption-at-rest on the storage layer is the correct mitigation, not application-level hashing.

#### Alert contact ownership

Same internal model as webhooks: any `write`-scope token can manage any alert
contact when no gateway context is present, and `created_by` is audit-only.
Gateway-routed creates set `owner_tenant_id`; gateway-routed
list/get/update/delete/test paths filter by it. Delivery history and manual
retry visibility are derived by first verifying ownership of the parent alert
contact.

### Family 6: Identity and utility

#### `GET /api/v1/me`

Returns the identity associated with the current token: consumer name, scope, rate limit. Useful for a service to confirm at startup that its token is valid and has the expected permission level.

```json
{
  "consumer_name": "alerts-worker",
  "scope": "read",
  "rate_limit_per_minute": 600,
  "expires_at": null
}
```

This is the only API surface for keys. **Creation, listing, and revocation are CLI-only** (`./jetmon2 keys ...`); see Authentication above. There is no `/api/v1/keys` endpoint.

#### `GET /api/v1/health`

Unauthenticated. Returns `{ "status": "ok" }` if the API can talk to the database. For load balancers and external uptime monitors (yes, including external monitors monitoring the monitor).

#### `GET /api/v1/monitor/drain-status`

Requires `read` scope. Returns the in-flight work counters the local orchestrator publishes to `jetpack_monitor_process_health`. Used to confirm a clean shutdown is safe.

```json
{
  "state": "stopping",
  "active_checks": 0,
  "queue_depth": 0,
  "retry_queue_size": 0,
  "wpcom_queue_depth": 0,
  "heartbeat_age": "3s",
  "done": true
}
```

`done` is true when every counter is zero. A running host with non-zero counters is steady-state (response includes a `reason` explaining); a stopping host with non-zero counters is `"reason": "drain in progress"`.

#### `GET /api/v1/verifliers/quorum-report`

Requires `read` scope. Reports per-vantage health for quorum diagnostics. Auth tokens are never included in the response (only `auth_token_present` boolean).

```json
{
  "generated_at": "2026-05-19T12:34:56Z",
  "stale_after_seconds": 90,
  "total_vantages": 3,
  "enabled_count": 2,
  "usable_count": 2,
  "healthy_count": 2,
  "vantages": [
    {
      "vantage_id": "v-us-east",
      "region": "us-east",
      "endpoint_host": "10.0.0.10",
      "endpoint_port": "7803",
      "auth_token_present": true,
      "enabled": true,
      "usable": true,
      "healthy": true,
      "active_agents": 2,
      "last_seen": "2026-05-19T12:34:41Z",
      "last_seen_age_sec": 15
    }
  ]
}
```

A vantage is `healthy` when it is enabled, usable (has host+port+token), and has at least one agent that heartbeat within the last 90 seconds. Use `healthy_count` against the configured detection quorum to decide whether a vote is meaningful.

#### `GET /api/v1/audit-log`

Requires `read` scope. Paginated query over `jetpack_monitor_audit_log`. Filters: `blog_id`, `event_id`, `event_type` / `event_type__in=A,B,C`, `source`, `since` / `until` (RFC3339). Newest-first by id. Opaque `cursor` pagination, `limit` default 50 max 200.

```bash
curl -H "Authorization: Bearer $JETMON_API_TOKEN" \
  "$JETMON_API_URL/api/v1/audit-log?event_type__in=wpcom_sent,wpcom_failure&since=2026-05-19T00:00:00Z&limit=100"
```

Each row carries `id`, `blog_id` (nullable for system events like `config_change`), `event_id` (nullable when not linked), `event_type`, `source`, `detail`, optional `metadata` JSON, and `created_at`. Replaces the previous "log into MySQL and `SELECT * FROM jetpack_monitor_audit_log`" workflow.

#### `GET /api/v1/ready`

Unauthenticated. Returns 200 with `{ "status": "ready", ... }` only when this Monitor host has finished starting up: the API can talk to the database, the local orchestrator has published a `jetpack_monitor_process_health` snapshot, and that snapshot is fresh (< 60 s), `state = running`, and `health_status = green`. Otherwise returns 503 with one of:

- `"status": "starting"` — orchestrator has not yet published a snapshot
- `"status": "stale"` — snapshot is older than 60 seconds (orchestrator likely stopped publishing)
- `"status": "not_running"` — process is draining or stopped
- `"status": "unhealthy"` — orchestrator reports `health_status != green` (Veriflier discovery missing, MySQL degraded, etc.)

Distinct from `/health` so a load balancer can keep the process registered as live (`/health`) while not sending traffic to a host that has just restarted and is still claiming buckets or has hit a dependency failure. Both endpoints are unauthenticated and intended for infrastructure probes.

```json
{
  "status": "ready",
  "state": "running",
  "health_status": "green",
  "heartbeat_age": "5s"
}
```

#### `GET /api/v1/monitor/stats`

Requires `read` scope. Returns the latest in-memory Monitor stats snapshot used
to write the legacy `stats/sitespersec`, `stats/sitesqueue`, and `stats/totals`
files. The handler does not read those files from disk; it renders from the same
snapshot so Docker deployments do not need host bind mounts for read-only stats
consumers.

```json
{
  "available": true,
  "updated_at": "2026-05-18T18:12:03Z",
  "sites_per_sec": 12,
  "queue_size": 34,
  "working": 5,
  "waiting": 55,
  "halting": 0,
  "error": 3,
  "offline": 2,
  "success": 95,
  "total": 100,
  "legacy": {
    "sitespersec": "sites per second: 12\n",
    "sitesqueue": "sites in queue: 34\n",
    "totals": "working : 5\nwaiting : 55\nhalting : 0\nerror   : 3\noffline : 2\nsuccess : 95\ntotal   : 100\n"
  }
}
```

To migrate a consumer that currently reads one legacy stats file, pass `file` to
receive the exact file body as `text/plain`:

```bash
curl -H "Authorization: Bearer $JETMON_API_TOKEN" \
  "$JETMON_API_URL/api/v1/monitor/stats?file=totals"
```

`file` must be one of `sitespersec`, `sitesqueue`, or `totals`.

The endpoint returns `503 stats_unavailable` if the Monitor API starts before
the first scheduler stats snapshot has been published. Treat that as a warm-up
state; use `/api/v1/health` for process/liveness checks that must be green
before the first monitoring round completes.

#### `GET /api/v1/monitor/db-config`

Requires `read` scope. Returns the active database config source and sanitized
server-map reload status. It does not expose DSNs or passwords. Use this during
production rollout to confirm when the next `db-servers.php` check is scheduled,
when a changed credential/endpoint map was last observed, and when a changed map
was last hot-reloaded into the running read/write pools.

```json
{
  "mode": "server_map",
  "source": "server-map:/jetmon/config-source/db-servers.php dataset=misc dc=dfw address=internet",
  "reload_enabled": true,
  "reload_interval_seconds": 600,
  "loaded_at": "2026-05-18T18:00:00Z",
  "last_checked_at": "2026-05-18T18:20:03Z",
  "next_check_at": "2026-05-18T18:30:03Z",
  "last_change_seen_at": "2026-05-18T18:10:03Z",
  "last_reloaded_at": "2026-05-18T18:10:03Z",
  "active_fingerprint": "7b08c2a5981d",
  "read_endpoints": ["misc-ro-a:3306/misc"],
  "write_endpoints": ["misc-rw-a:3306/misc"]
}
```

When `DB_SERVER_MAP_PATH` is unset, `mode` is `env`, `reload_enabled` is
`false`, and read/write endpoint labels describe the explicit `DB_*`
environment configuration. If parsing or ping validation fails during a reload,
`last_reload_error` and `last_reload_error_at` are set while the previously
working pools stay active.

#### `GET /api/v1/openapi.json`

Returns the route-driven OpenAPI 3.1 contract for the internal API. Requires `read` scope like other internal introspection routes. The spec is generated from the same route table used to build the running server mux, so new routes must be added to that table before they can be served or documented.

The current contract publishes paths, methods, auth scope, idempotency headers, path parameters, request/response component schemas derived from the handler structs, and the standard error envelope. `internal/api` tests resolve every component `$ref` and type-check a generated Go client smoke source from the published operation IDs and component names. Stricter public compatibility checks are tracked in `roadmap.md`.

---

## What we deliberately did not include

- **No Statuspage-style public status pages.** That's a separate product; Jetmon focuses on monitoring. If you want a public status page, the API gives you what you need to build one.
- **No customer-facing "monitor groups" / "tags" in the current API.** Most
  existing consumers organize by `owner_blog_id`. Tag-scoped API authorization
  for roles such as VIP or Agency is now tracked as a pre-rollout design review
  item before any broader direct API exposure.
- **No GraphQL.** REST + cursor pagination + filters covers everything the v1 use cases need. If a future consumer needs nested-fetch optimization (sites + active events + recent transitions in one round-trip), we'd add a single `/api/v1/sites/{id}/full` endpoint before reaching for GraphQL.
- **No per-region SLA breakdown.** All sites are checked from the orchestrator's bucket assignment, not a multi-region fleet (yet — see `taxonomy.md` v2/v3 vantage-point work). When that ships, the SLA endpoint gains a `?vantage_point=us-west-1` filter.
- **No streaming.** Webhooks cover event-driven needs; long-poll/SSE/WebSocket support is overkill for the current consumer set. Could be added on `/api/v1/sites/{id}/events/stream` if a consumer asks.

## Implementation Phase Map

Phase 1 (read-only foundation, implemented):
- `jetpack_monitor_api_keys` migration + sha256 hashing helpers
- `./jetmon2 keys create/list/revoke/rotate` CLI
- Auth middleware (Bearer token validation, scope enforcement, audit logging via `jetpack_monitor_audit_log`)
- Health check + `GET /api/v1/me`
- Family 1 read endpoints (sites list, single site)
- Family 2 (events list, single event with transitions, transitions list)
- Family 3 (uptime, response-time, timing-breakdown)
- Per-key rate limiting + standard headers

Phase 2 (write surface, implemented):
- Family 1 write endpoints (POST/PATCH/DELETE sites, pause/resume, trigger-now)
- Family 2 manual close
- Idempotency keys on POST routes
- Route-driven OpenAPI 3.1 contract at `GET /api/v1/openapi.json`

Phase 3 (webhook delivery, implemented):
- Family 4 webhooks (CRUD + delivery infrastructure with HMAC signing + retry backoff)

Phase 3.x (alert contacts, implemented):
- Family 5 alert contacts: managed channels (email, PagerDuty, Slack, Teams)
- `internal/alerting/` package — parallel to `internal/webhooks/`, same dispatch shape
- Email transport interface with `wpcom` / `smtp` / `stub` implementations
- Per-contact severity gate + per-hour rate cap
- `POST /alert-contacts/{id}/test` send-test endpoint
- Legacy WPCOM notification flow continues to operate in parallel; future migration tracked in ROADMAP

Phase 4 (polish, future):
- Consumer-specific OpenAPI generator validation if API consumers standardize on a tool
- Bulk endpoints if real consumers need them
- Per-region filters when vantage-point work ships

---

## Resolved design questions

These were the open questions from the original draft. All resolved during review; recorded here so the rationale doesn't get lost when the doc evolves.

1. **Resource ID format → raw numeric integers across all resources.** Initially proposed type-prefixed ids (`evt_12345`, `whk_42`) for self-documenting log lines, but on review the costs outweighed the benefits: dual representation between logs/DB/API, JSON type inconsistency (sites as numbers, others as strings), a real silent-coercion bug class under default MySQL `SQL_MODE`, and forward-sharding friction not actually solved by prefixes. Resolution: every resource `id` is a raw `BIGINT UNSIGNED` serialized as a JSON number. Type context is provided by endpoint paths and explicit `type` fields in error messages and webhook payloads, not embedded in the id. (Webhook signing secrets keep the `whsec_` prefix because they're shared secrets, not resource ids — the prefix is a leak-detection hint.)

2. **Bulk site list cap → 200/page, no `include_inactive` opt-in flag.** The existing `monitor_active` filter does the same job; a separate flag would duplicate it. The 200-page cap alone is sufficient guardrail for full-table walks (100k sites at 200/page = 500 round trips, adequate for daily SLA batch jobs). If a consumer ever needs higher per-page volume, we add a `?limit_max=1000` opt-in tied to a special scope at that point — not now.

3. **Webhook signing → Stripe-style versioned HMAC, single algorithm at a time.** Header format `t=<unix_ts>,v1=<hmac_sha256_hex>`. The `v1=` prefix reserves space for a v2 algorithm rotation (e.g. ed25519) without breaking consumer parsers. Don't build multi-algorithm signing upfront — when rotation is actually triggered, transition period emits both `v1=...,v2=...` so consumers verify whichever they support.

4. **`trigger-now` semantics → synchronous with a 30s server-side timeout, no async path in v1.** Matches operator and gateway expectations ("I just deployed, is it up?"), keeps the API surface narrow (one request → one response), and the existing trigger-now rate limit (1/min default per consumer) bounds connection-pool exposure. If a batch-verification consumer ever shows up, we add `?async=true` returning a 202 with a job id — but not before there's a real consumer for it.

5. **Event metadata sanitization → single `metadata` field, no public/private split.** With this being an internal API and a gateway in front of any customer-facing surface, the `metadata` JSON can carry full operational detail (verifier hostnames, internal RPC ids, full HTTP response excerpts). The gateway is responsible for any redaction before forwarding to customers.

---

## Sources / inspiration

The patterns above were informed by reviewing the documented APIs of:

- [Better Stack Uptime API](https://betterstack.com/docs/uptime/api/) — JSON:API envelope (we rejected), incident status enum (we extended), Bearer token auth (we adopted).
- [UptimeRobot v3 API](https://uptimerobot.com/api/v3/) — Bearer JWT, REST verbs, cursor pagination (we adopted), JSON-only (we adopted).
- [Pingdom API 3.1](https://docs.pingdom.com/api/) — OpenAPI 3.0 spec (we adopted), `summary.average` SLA endpoint shape (informed our `/uptime` design).
- [Atlassian Statuspage API](https://developer.statuspage.io/) — incident updates timeline (we extended into transitions table), component status enum `operational/degraded/partial_outage/major_outage` (we rejected — too coarse for our taxonomy).
- [Stripe API](https://stripe.com/docs/api) — error model with stable codes (we adopted), idempotency keys (we adopted), webhook signing scheme (we adopted).

None of these were copied; each pattern was evaluated against Jetmon's data model and either adopted, modified, or rejected with rationale.

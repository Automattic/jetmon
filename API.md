# Jetmon Internal API — Design Document

This is a **design proposal**, not yet implemented. It describes the REST API that Jetmon 2 will expose for status reads, incident history, SLA reports, monitor management, and alert delivery.

**Audience: internal systems only.** Jetmon does not expose this API to end customers directly. A separate gateway service handles all customer-facing access — authentication, tenant isolation, customer rate limiting, plan-based feature gating, public error vocabulary, etc. — and calls Jetmon over this internal interface. Other internal services (operator dashboard, alerting workers, batch reporting jobs, the gateway itself) are the only direct callers.

This shapes several design choices: authentication is per-consumer rather than per-customer, scopes are coarse rather than granular, error messages are verbose rather than guarded, and key management is an ops-only concern rather than a self-service feature. The trust boundary is "is this a known internal system?", not "is this user allowed to see this site?".

The goal is to expose Jetmon's distinctive data model — the five-layer test taxonomy, the site → endpoint → event hierarchy, the multi-state vocabulary, and the event-sourced architecture (`TAXONOMY.md`, `EVENTS.md`) — over a shape that internal consumers can integrate against confidently. We took inspiration from Better Stack, UptimeRobot v3, Pingdom, and Atlassian Statuspage but did not copy any of their shapes wholesale; Jetmon's richer model (multi-state, layered tests, causal links, separate severity) wouldn't fit cleanly into a flat "monitors" API.

## Principles

1. **Read API is source-of-truth, not just a snapshot.** Consumers should be able to ask "what is the current state of this site?" and "how did this incident evolve from severity 3 to 4 to closed?" with separate, narrow endpoints — not by polling a coarse "monitor" record. That's what the events/transitions tables exist for.

2. **Severity and state are both first-class.** Many competitor APIs collapse to a single "status" string (UptimeRobot returns `up`/`down`; Better Stack adds `paused`/`maintenance`/`validating`). Jetmon exposes both: numeric severity for ordering, thresholds, and SLA math; human-readable state for display. They never disagree because they're stored as separate columns updated in lockstep.

3. **Cursor pagination, never offset.** Offset pagination breaks under concurrent writes (an event closing during traversal shifts page boundaries). Cursors keyed on stable timestamps (`started_at`, `changed_at`) survive that.

4. **Versioned URLs, conservative additions.** All endpoints under `/api/v1/`. New fields on existing responses are additive (consumers ignore unknowns); shape-breaking changes get `/api/v2/` and a deprecation window. Severity values 0–4 today, room to add new values up to 255 without a version bump.

5. **No shape-shifting based on permissions.** A read-scope token sees the same JSON shape for `GET /api/v1/sites/{id}` as an admin token — fields aren't hidden, they're empty/null where data isn't applicable. Easier to test, easier to document.

6. **Errors carry a stable code, a human message, and (when relevant) a reference id.** Consumers branch on the `code` field, not on parsing the message.

7. **Bulk operations are explicit.** No "list 10,000 sites and then loop one update at a time" anti-pattern — `PATCH /api/v1/sites` accepts a body of changes and returns per-site results. Avoids client-side rate-limit workarounds.

## Authentication

**Per-consumer Bearer tokens.** Each calling system gets one (or more) tokens identifying it. The tokens are not user-delegated — there's no concept of "an end user authenticated via this token." A token *is* a service identity.

```
Authorization: Bearer jm_a1b2c3d4e5f6...
```

Tokens are 32-byte high-entropy random strings, sha256-hashed at rest (sha256 not bcrypt — bcrypt is for human-chosen passwords; high-entropy tokens just need a fast cryptographic hash). Stored in `jetmon_api_keys`:

```
jetmon_api_keys:
  id              BIGINT PK
  key_hash        CHAR(64)         -- sha256 hex
  consumer_name   VARCHAR(128)     -- e.g. "gateway", "alerts-worker", "dashboard"
  scope           ENUM('read','write','admin')
  rate_limit_per_minute INT
  expires_at      TIMESTAMP NULL   -- NULL = never
  revoked_at      TIMESTAMP NULL   -- soft-revoke for audit trail
  last_used_at    TIMESTAMP NULL
  created_at      TIMESTAMP
  created_by      VARCHAR(128)     -- ops user / automation that created the key
```

**Scopes — three coarse buckets:**

- `read` — every GET endpoint.
- `write` — every POST/PATCH/DELETE on sites, endpoints, events, webhooks.
- `admin` — write + ability to force operations like "recompute SLA from event log" or "close all events in maintenance mode." Reserved for ops tooling, not regular consumers.

We deliberately did not split into `sites:read` / `events:read` / `webhooks:read` etc. Internal consumers tend to need the whole read surface — the gateway needs to read everything to mediate it; an alerts worker reads sites, events, *and* webhooks. Granular scopes would create more configuration burden than they solve.

**Per-consumer audit logging.** Every authenticated request is logged to `jetmon_audit_log` with the consumer name, endpoint, status code, and latency. This is the load-bearing accountability mechanism — if "alerts-worker is hammering the trigger-now endpoint," that's visible in the audit log without parsing access logs. The audit log already exists for operational events (`EVENTS.md`); API access becomes another `event_type` value (`api_access`).

**Key management is ops-only.** No `/api/v1/keys` endpoints. Keys are created and revoked via the `./jetmon2` CLI:

```
./jetmon2 keys create --consumer gateway --scope read [--expires 90d]
./jetmon2 keys list
./jetmon2 keys revoke <key_id>
./jetmon2 keys rotate <key_id>     # creates a new key for the same consumer; revokes old after grace
```

The CLI talks to the database directly (via `jetmon_api_keys`), prints the new token once, and never exposes hashes. There's no self-service surface because there are no end customers — keys are infrastructure config, not user-managed credentials.

**Single key format.** No live/test split. The token format is `jm_<base32 of 32 random bytes>`. The gateway is responsible for any environment separation (dev/staging/prod) at its own layer.

**Why not mTLS / IP allowlists alone?** Either could replace Bearer tokens for service-to-service auth, but tokens make per-consumer identity trivial to log and revoke. mTLS rotation is heavier; IP allowlists don't survive containerized deployments cleanly. Bearer tokens are the lowest-friction option that gives us per-consumer accountability.

**Why not OAuth?** Same reasoning as before, now stronger: there are no user delegations to model. Every caller is a server.

## Common patterns

### Base URL and versioning

```
https://api.jetmon.example.com/api/v1
```

Hosted in the `jetmon2` binary on a dedicated port (`API_PORT`), separate from the operator dashboard (`DASHBOARD_PORT`) and the verifier transport (`VERIFLIER_GRPC_PORT`).

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

Error messages are verbose by design — for an internal API, "table 'jetmon_events' is locked, retry in 30s" beats "internal server error" by a wide margin during incident response. The gateway can sanitize before forwarding to customers.

### Rate limiting

Per-key bucket, configurable per consumer at key-creation time. Default 60 req/min for `read`, 10 req/min for `write`, 1 req/min for `trigger-now`. Internal consumers usually need higher limits than this — the gateway and dashboard might be set to 600 req/min, while a daily batch job stays at 60.

Standard headers on every response:

```
X-RateLimit-Limit: 60
X-RateLimit-Remaining: 47
X-RateLimit-Reset: 1714685400
```

`429` responses include `Retry-After` in seconds.

The trigger-now bucket is separate so a runaway "force-check every site" loop in one consumer can't starve the read API for others. This is service-protection rate limiting, not customer-fairness rate limiting — the gateway handles the latter.

### Idempotency

POST/PATCH endpoints accept an `Idempotency-Key` header. The server stores `(token_id, idempotency_key) → response` for 24 hours. Replays with the same body return the cached response; replays with a different body return `409 idempotency_conflict`.

This is the same pattern Stripe uses; it's the right call for monitor management where retries are common.

### Time

All timestamps are ISO 8601 with millisecond precision and `Z` suffix:

```
"started_at": "2026-04-25T03:18:38.329Z"
```

The server is always UTC. Clients converting to local time is their problem.

---

## Status and state vocabulary

The API exposes the same vocabulary the orchestrator and event store use. From `TAXONOMY.md` Part 3 and `EVENTS.md`:

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

The full surface is grouped into five capability families, matching `ROADMAP.md`. All endpoints listed below are part of the proposed v1; build order is suggested but not prescriptive.

### Family 1: Sites and current state

#### `GET /api/v1/sites`

List sites visible to this token.

**Scopes:** `sites:read`

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

**Response 200:**

```json
{
  "data": [
    {
      "id": 12345,
      "blog_id": 12345,
      "monitor_url": "https://example.com",
      "monitor_active": true,
      "current_state": "Up",
      "current_severity": 0,
      "active_event_id": null,
      "last_checked_at": "2026-04-25T03:24:11.123Z",
      "last_status_change_at": "2026-04-21T09:14:00.000Z",
      "ssl_expiry_date": "2026-08-12",
      "check_keyword": null,
      "redirect_policy": "follow",
      "maintenance_start": null,
      "maintenance_end": null,
      "alert_cooldown_minutes": null,
      "endpoints_count": 3
    }
  ],
  "page": { "next": "eyJ...", "limit": 50 }
}
```

`id` and `blog_id` are the same value for now; `id` is the public field name (`blog_id` is the historical column name). Consumers should rely on `id`.

#### `GET /api/v1/sites/{id}`

Single site, same shape as a list entry plus an `active_events` array for any open events:

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

#### `POST /api/v1/sites`

Create a site.

**Scopes:** `sites:write`

**Request body:**

```json
{
  "monitor_url": "https://example.com",
  "monitor_active": true,
  "check_keyword": null,
  "redirect_policy": "follow",
  "timeout_seconds": null,
  "custom_headers": {},
  "alert_cooldown_minutes": null
}
```

**Response 201:** the site object.

**Errors:**

| Code | Meaning |
|------|---------|
| `invalid_url` | `monitor_url` doesn't parse |
| `duplicate_site` | A site with this URL already exists for this owner |
| `quota_exceeded` | The owner has hit their site quota |

#### `PATCH /api/v1/sites/{id}`

Partial update. Send only the fields you want to change.

#### `DELETE /api/v1/sites/{id}`

Soft-delete (sets `monitor_active = false` and tombstones). Closes any active events with `resolution_reason = manual_override`.

#### `POST /api/v1/sites/{id}/pause`, `POST /api/v1/sites/{id}/resume`

Convenience verbs for the common pause/resume flow. Pause closes any active events with `resolution_reason = manual_override` and sets `current_state = "Paused"`. Resume reverts.

#### `POST /api/v1/sites/{id}/trigger-now`

Force an immediate check, returning the result inline (subject to a tighter rate limit). Useful for "I just deployed a fix, is it back up?"

```json
{
  "result": {
    "http_code": 200,
    "rtt_ms": 412,
    "ssl_expires_at": "2026-08-12T00:00:00.000Z"
  },
  "current_state": "Up",
  "active_events_closed": [487291]
}
```

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
        "rtt_ms": 84,
        "url": "https://example.com"
      },
      "duration_ms": 158961,
      "transition_count": 5
    }
  ],
  "page": { "next": "eyJ...", "limit": 50 }
}
```

`duration_ms` is a server-computed convenience: `(ended_at or now) - started_at`. `transition_count` lets the consumer decide whether to fetch the full transition log.

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
      "metadata": { "http_code": 503, "rtt_ms": 84 },
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

**Scopes:** `sites:write`

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
| `window` | enum | `1h`, `24h`, `7d`, `30d`, `90d` |
| `from` / `to` | ISO timestamp | Custom range; overrides `window` |
| `exclude_states` | csv | Default `Maintenance,Paused` (these don't count against uptime) |

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

**How uptime is computed:** sum of `(ended_at or now) - started_at` for events with `state in (Down, Seems Down)` within the window, divided by total window duration. Configurable via `exclude_states`. The math is event-driven, not check-driven, which means SLA reports stay accurate even if check frequency changes.

#### `GET /api/v1/sites/{id}/response-time`

Response time percentiles over a window, sourced from `jetmon_check_history`.

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
  "buckets": [
    { "ts": "2026-04-24T00:00:00Z", "p50_ms": 180, "p95_ms": 401, "p99_ms": 850 },
    { "ts": "2026-04-24T01:00:00Z", "p50_ms": 184, "p95_ms": 395, "p99_ms": 820 }
  ]
}
```

The `buckets` array is hourly for `window <= 7d`, daily for longer windows. Bucket size isn't tunable — supporting arbitrary granularity would require pre-aggregation we don't have yet.

#### `GET /api/v1/sites/{id}/timing-breakdown`

DNS / TCP / TLS / TTFB breakdown — one of Jetmon's distinctive features (most competitors only return total response time).

**Response:**

```json
{
  "window": { "from": "2026-04-24T00:00:00Z", "to": "2026-04-25T00:00:00Z" },
  "samples": 17280,
  "dns_p50_ms": 8,
  "dns_p95_ms": 45,
  "tcp_p50_ms": 22,
  "tcp_p95_ms": 78,
  "tls_p50_ms": 35,
  "tls_p95_ms": 110,
  "ttfb_p50_ms": 142,
  "ttfb_p95_ms": 391
}
```

### Family 4: Alert contacts and webhooks

#### `GET /api/v1/webhooks` / `POST /api/v1/webhooks` / `PATCH /api/v1/webhooks/{id}` / `DELETE /api/v1/webhooks/{id}`

Standard CRUD. A webhook is:

```json
{
  "id": 42,
  "url": "https://hooks.slack.com/...",
  "active": true,
  "events": ["event.opened", "event.promoted", "event.closed"],
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

`event.*` types fire once per transition row written to `jetmon_event_transitions` — i.e., once per actual mutation. The 1:1 invariant the eventstore maintains is what makes detection reliable.

**Deferred:** `site.state_changed` (rollup from events to the site-row projection) is **not** in v1. Rolling up cleanly without races requires changes to the orchestrator, and event-level webhooks already give consumers everything they need. Tracked in ROADMAP.md.

#### Detection mechanism

Webhook delivery uses **pull-based detection**: a worker polls `jetmon_event_transitions WHERE id > last_seen` on a 1s interval and creates one delivery row per matching transition. This is the long-term answer for Jetmon's architecture — the orchestrator's flap suppression already adds 10s+ between detection and confirmed events, so 1s poll latency is invisible in the practical budget. Pull also handles multi-instance deployment cleanly (any jetmon2 instance can claim work via row lock; no pub/sub layer needed).

Push-based or hybrid detection is not on the roadmap. If a future consumer demands sub-second webhook latency, that's the trigger to introduce a pub/sub layer — not before.

#### Retry policy

Each `jetmon_webhook_deliveries` row is one webhook firing. Each delivery has up to 6 attempts on this exponential schedule:

| Attempt | Delay from previous |
|---------|---------------------|
| 1       | immediate           |
| 2       | 1m                  |
| 3       | 5m                  |
| 4       | 30m                 |
| 5       | 1h                  |
| 6       | 6h                  |
| (drop)  | 24h after attempt 6 |

A delivery succeeds when any attempt returns 2xx. After 6 failed attempts, the row is marked `status = 'abandoned'`. Abandoned rows stay in the table — `GET /api/v1/webhooks/{id}/deliveries?status=abandoned` lists them, and `POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/retry` lets a consumer re-fire after fixing their endpoint.

`GET /api/v1/webhooks/{id}/deliveries` returns the full delivery history with `status` (`pending` / `delivered` / `failed` / `abandoned`), `attempt`, `last_status_code`, and a truncated `last_response` body for debugging.

#### Signing and secret rotation

Signature: HMAC-SHA256 of `{timestamp}.{body}` with the webhook's secret, sent as `X-Jetmon-Signature: t=<unix_ts>,v1=<hex>`. The timestamp prevents replay; consumers should reject deliveries older than 5 minutes.

Format chosen for: wide library support across consumer languages, explicit version (`v1=`) to allow future algorithm rotation without breaking consumers, replay protection via timestamp baked into the signature input, and the ability to coexist with multiple `v1=` values during a grace-period rotation (deferred). Alternatives considered and not chosen: GitHub-style (no replay protection), Slack-style (functionally equivalent, two-header form), JWT-based (wrong abstraction for "POST JSON + signature header"), HTTP Message Signatures / RFC 9421 (over-engineered for our scope), asymmetric / Ed25519 (compelling for public APIs without a gateway in front; not warranted while a gateway re-signs for end customers).

When to revisit: a public-API-without-gateway requirement (then asymmetric becomes attractive — no per-consumer secret distribution), or a standards-driven third-party integration that requires RFC 9421. Migration path in either case is "add a `v2=` signature alongside `v1=` for a transition window, switch consumers, deprecate `v1=`" — same shape as algorithm rotation we already designed for.

Secret rotation in v1: **immediate revocation only**. `POST /api/v1/webhooks/{id}/rotate-secret` returns a new secret once, replaces the stored hash, and the old secret stops working immediately. Failed deliveries during the consumer's deploy window go into the retry queue.

**Deferred:** grace-period rotation (server signs with both old and new secrets for a configurable window so consumers can roll over without coordinated downtime) is in ROADMAP.md. The signature header format already supports multiple `v1=...,v1=...` values per Stripe convention, so adding grace-period rotation later is non-breaking.

#### Backpressure

Delivery uses a **shared worker pool** (default 50 goroutines, configurable) with a **per-webhook in-flight cap** (default 3 concurrent). The shared pool bounds total goroutine count; the per-webhook cap prevents a slow or hung webhook URL from monopolizing the pool and starving other webhooks' deliveries.

Implementation: at dispatch time, the worker checks a `map[webhook_id]int` counter under a mutex. If a webhook is already at its cap, the row stays `pending` and is picked up on the next poll tick. The counter decrements when a delivery attempt completes (success or failure).

#### Schema

```
jetmon_webhooks:
  id, url, active, events JSON, site_filter JSON, state_filter JSON,
  secret_hash CHAR(64), secret_preview VARCHAR(8),
  created_by VARCHAR(128), created_at, updated_at

jetmon_webhook_deliveries:
  id, webhook_id, event_id (FK to jetmon_events), event_type,
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

`payload` is **frozen at delivery creation**: the consumer sees the event as it was when the webhook fired, not as it is now. A closed-and-amended event would not change a delivery's payload — that's the contract consumers expect ("this is what I was told happened, not whatever it became").

#### Webhook ownership and scope

Webhooks are managed by any `write`-scope token. `created_by` records the consumer name from the API key for audit purposes only — there is no per-consumer ownership boundary, and any `write`-scope token can read/edit/delete any webhook.

This is appropriate **only** because Jetmon is internal-only with all consumers trusted. Per-consumer ownership doesn't add value at this scale; the gateway in front of Jetmon handles tenant isolation for any customer-facing webhooks.

**Ramifications if Jetmon ever becomes a public API:**

- This model would need to change. Customer-facing consumers cannot be allowed to read or modify each other's webhooks.
- Migration path: add `owner_consumer_id` (or `owner_account_id`) column to `jetmon_webhooks`; require it on create; filter list/get/update/delete by it; introduce a `webhooks` scope or formal account/tenant boundary.
- The `created_by` field is forward-compatible — it's already capturing the consumer identity, just not enforcing it.
- Existing webhooks would need a backfill migration to populate the new ownership column with their original `created_by` value.
- Webhook secrets would need stronger isolation (currently any write-scope can rotate any secret; in a public API this would be a privilege escalation).

The decision to defer ownership today should be reread before any public-API conversation actually starts.

### Family 5: Alert contacts (legacy bridge)

For drop-in compatibility with the existing WPCOM notification flow, alert contacts are first-class but optional. Most modern integrations use webhooks. Documented here for completeness:

- `GET /api/v1/contacts` / `POST` / `PATCH` / `DELETE`
- Contact types: `email`, `sms`, `webhook` (delegates to webhook system above), `slack`
- Per-site contact assignment via `POST /api/v1/sites/{id}/contacts/{contact_id}`

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

#### `GET /api/v1/openapi.json`

OpenAPI 3.1 spec for client codegen. Updated on every deploy; matches what the running server actually accepts. Internal consumers can regenerate clients automatically — particularly useful for the gateway, which will likely re-export a sanitized subset of this surface.

---

## What we deliberately did not include

- **No Statuspage-style public status pages.** That's a separate product; Jetmon focuses on monitoring. If you want a public status page, the API gives you what you need to build one.
- **No "monitor groups" / "tags" in v1.** Most consumers organize by `owner_blog_id`; tagging is a complexity multiplier we'd rather defer until requested.
- **No GraphQL.** REST + cursor pagination + filters covers everything the v1 use cases need. If a future consumer needs nested-fetch optimization (sites + active events + recent transitions in one round-trip), we'd add a single `/api/v1/sites/{id}/full` endpoint before reaching for GraphQL.
- **No per-region SLA breakdown.** All sites are checked from the orchestrator's bucket assignment, not a multi-region fleet (yet — see `TAXONOMY.md` v2/v3 vantage-point work). When that ships, the SLA endpoint gains a `?vantage_point=us-west-1` filter.
- **No streaming.** Webhooks cover event-driven needs; long-poll/SSE/WebSocket support is overkill for the current consumer set. Could be added on `/api/v1/sites/{id}/events/stream` if a consumer asks.

## Build order recommendation

Phase 1 (read-only foundation):
- `jetmon_api_keys` migration + sha256 hashing helpers
- `./jetmon2 keys create/list/revoke/rotate` CLI
- Auth middleware (Bearer token validation, scope enforcement, audit logging via `jetmon_audit_log`)
- Health check + `GET /api/v1/me`
- Family 1 read endpoints (sites list, single site)
- Family 2 (events list, single event with transitions, transitions list)
- Family 3 (uptime, response-time, timing-breakdown)
- Per-key rate limiting + standard headers

Phase 2 (write surface):
- Family 1 write endpoints (POST/PATCH/DELETE sites, pause/resume, trigger-now)
- Family 2 manual close
- Idempotency keys + tighter rate limit on triggers

Phase 3 (delivery):
- Family 4 webhooks (CRUD + delivery infrastructure with HMAC signing + retry backoff)
- Family 5 alert contacts (bridge to existing WPCOM flow)

Phase 4 (polish):
- OpenAPI spec generation
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

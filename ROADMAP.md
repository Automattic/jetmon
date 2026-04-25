# Jetmon Roadmap

Deferred features that are intentionally out of scope for the current implementation but have been identified as important future work. Items here are tracked so they are not forgotten and can be designed with future compatibility in mind.

---

## Public REST API

**Status:** Not started. No existing API surface covers this scope.

### What it is

A versioned, authenticated REST API (`/api/v1/`) on competitive parity with established uptime monitoring services (Pingdom, UptimeRobot, Better Uptime, Datadog Synthetics). Users and integrations interact with Jetmon entirely through this API — reading current health state, pulling event history and SLA statistics, managing what gets monitored, configuring alerts, and triggering on-demand checks.

Currently, Jetmon has no public API. The operator dashboard exposes real-time state via SSE for human consumption. Check configuration requires direct writes to `jetpack_monitor_sites`. Event and audit data requires direct DB queries or use of the `jetmon2 audit` CLI. There is no programmatic interface for users or external tooling to interact with Jetmon.

### Why it matters

**User-facing self-service.** Customers need to provision monitors, adjust what is checked, retrieve their monitoring data, and receive alerts through their own tooling — without requiring direct database access or bespoke internal integrations from the Jetpack team. This is table stakes for a monitoring product: Pingdom, UptimeRobot, Better Uptime, and every serious competitor ship a public API as a first-class product surface.

**CI/CD and deployment tooling.** Teams that deploy frequently need to pause monitors before a deploy, resume them after, and verify that no events opened during the deployment window — all from a deploy script. That use case requires management and query capabilities together.

**uptime-bench integration.** The uptime-bench benchmark harness treats every service it evaluates as a first-class API client. The Jetmon adapter uses the manage API to provision a monitor before a scenario run and the query API to retrieve detection data afterward. Without a public API, uptime-bench must read directly from MySQL, which is fragile and tightly coupled to the internal schema.

**Jetpack feature surface.** The Jetpack dashboard, mobile apps, and any future Jetpack products that surface monitoring data consume this API rather than requiring their own DB integrations.

### Capability 1: Status and state

Read-only endpoints for current site and endpoint health.

| Endpoint | Description |
|---|---|
| `GET /api/v1/sites` | List all monitored sites with current state, severity, and active event count |
| `GET /api/v1/sites/{blog_id}` | Full current state for one site: state, severity, active events, check summary |
| `GET /api/v1/sites/{blog_id}/endpoints` | List all endpoints configured for the site with per-endpoint state |
| `GET /api/v1/sites/{blog_id}/endpoints/{endpoint_id}` | Current state for a specific endpoint |

### Capability 2: Events and history

Read-only endpoints for event timelines and raw check results.

| Endpoint | Description |
|---|---|
| `GET /api/v1/sites/{blog_id}/events` | Event history; supports `since`, `until`, `state`, `severity`, and `check_type` filters |
| `GET /api/v1/sites/{blog_id}/events/active` | Currently open (unresolved) events only |
| `GET /api/v1/sites/{blog_id}/events/{event_id}` | Single event with full metadata and causal links |
| `GET /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/events` | Events scoped to one endpoint |
| `GET /api/v1/sites/{blog_id}/history` | Raw check timing history (DNS, TCP, TLS, TTFB, status code) with time range params |
| `GET /api/v1/sites/{blog_id}/audit` | Audit log entries with time range and event type filters |

Event response schema follows `EVENTS.md`: `started_at`, `ended_at`, `severity`, `state`, `resolution_reason`, `check_type`, `cause_event_id`, `metadata`.

### Capability 3: Statistics and SLA reporting

Aggregate calculations for uptime, response time, and incident summary. This is what competitors expose for SLA reporting dashboards and customer-facing status summaries.

| Endpoint | Description |
|---|---|
| `GET /api/v1/sites/{blog_id}/uptime` | Uptime percentage for a given time range; returns total, by state (Down, Degraded, Unknown separately), and per-endpoint breakdown |
| `GET /api/v1/sites/{blog_id}/response-times` | Response time statistics for a time range: mean, p50, p95, p99, min, max, bucketed by interval |
| `GET /api/v1/sites/{blog_id}/incidents` | Incident summary: count, total duration, and MTTR for a time range |

**Design note on Unknown vs. Downtime.** The uptime calculation must honour the Unknown/Downtime distinction from `TAXONOMY.md`: Unknown periods (monitor-side failures, agent not reporting) are excluded from the denominator, not counted as downtime. Conflating these breaks SLA calculations and erodes user trust. The response must return the three separately: `downtime_seconds`, `degraded_seconds`, `unknown_seconds`, and `monitored_seconds` (the denominator).

### Capability 4: Monitor management

Write endpoints for programmatic endpoint and check lifecycle management.

| Endpoint | Description |
|---|---|
| `POST /api/v1/sites/{blog_id}/endpoints` | Add a new endpoint to monitor (URL, label, check types, frequency, timeout) |
| `GET /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/checks` | List checks configured on an endpoint |
| `POST /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/checks` | Add a check to an endpoint (check type, keyword, redirect policy, expected status, etc.) |
| `PUT /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/checks/{check_id}` | Update check configuration |
| `DELETE /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/checks/{check_id}` | Remove a check |
| `POST /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/pause` | Suspend all checks on an endpoint without deleting |
| `POST /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/resume` | Resume a paused endpoint |
| `PUT /api/v1/sites/{blog_id}/maintenance` | Set a maintenance window (start, end, optional recurrence) |
| `DELETE /api/v1/sites/{blog_id}/maintenance` | Clear an active maintenance window |
| `POST /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/trigger` | Trigger an immediate on-demand check outside the normal schedule |

Creating or modifying checks via the API must go through the same orchestrator pickup path as a direct DB write. The API is a frontend to the system, not a bypass.

### Capability 5: Notifications and alert contacts

Programmatic management of where alerts go. Competitors that omit this force users back to a web UI for a critical configuration task.

| Endpoint | Description |
|---|---|
| `GET /api/v1/alert-contacts` | List configured alert contacts (email, webhook, PagerDuty, Slack, etc.) |
| `POST /api/v1/alert-contacts` | Create an alert contact |
| `PUT /api/v1/alert-contacts/{contact_id}` | Update a contact (endpoint URL, credentials, enabled state) |
| `DELETE /api/v1/alert-contacts/{contact_id}` | Remove a contact |
| `GET /api/v1/sites/{blog_id}/alert-contacts` | List which contacts are subscribed to a site |
| `PUT /api/v1/sites/{blog_id}/alert-contacts` | Set the alert contact list for a site |

**Alert contact types (v1):** email, webhook (generic HTTP POST with configurable payload template). Later: Slack, PagerDuty, OpsGenie, SMS.

**Webhook contract.** Outbound webhook POSTs carry a standard envelope: `event_type`, `site_id`, `blog_id`, `timestamp`, `event` (the full event object). `event_type` values: `site.seems_down`, `site.down`, `site.recovered`, `site.degraded`, `maintenance.started`, `maintenance.ended`. The payload structure is versioned and must not break existing webhook consumers when new fields are added.

### Design decisions to make before building

**Authentication.** API keys stored in a `jetmon_api_keys` table (hashed, scoped, with optional expiry). The `Authorization: Bearer <token>` pattern from the Veriflier transport is the reference. Scopes: `read` (Capabilities 1–3), `write` (Capabilities 4–5), `admin` (key management). OAuth is overkill for an internal service; API keys are sufficient and match what competitors use for programmatic access.

**Key lifecycle CLI.** `jetmon2 apikey create [--scope read|write|admin] [--expires 90d] [--label "CI deploy script"]`, `jetmon2 apikey revoke <key-id>`, `jetmon2 apikey list`. Keys are never returned after creation; only the ID and label are stored.

**Hosting.** API runs within the `jetmon2` binary on a dedicated port (separate from the operator dashboard port). Embedding keeps deployment to one artifact. The operator dashboard's existing HTTP server in `internal/dashboard/` is the starting point — the API mounts alongside it or on a configurable separate port.

**Pagination.** Cursor-based pagination for all list endpoints, using `event_id` or `timestamp` as the cursor. Offset-based pagination is rejected for append-only log tables. `limit` defaults to 100, max 1000. Response includes `next_cursor` when more results exist.

**Rate limiting.** Per API key. Default limits: 60 requests/minute for read, 20 requests/minute for write, 5 requests/minute for trigger. Configurable per key in the DB. The `trigger` endpoint has its own bucket separate from read/write to prevent it from being used as a DoS vector against the check pipeline. Rate limit headers (`X-RateLimit-Limit`, `X-RateLimit-Remaining`, `X-RateLimit-Reset`) returned on every response.

**Schema versioning.** `/api/v1/`. Breaking changes require a new version prefix. Additive changes (new fields, new endpoints) are backwards-compatible within v1. The version prefix is in the URL, not a header, to make it unambiguous in logs.

**Trigger-now semantics.** The trigger endpoint enqueues an immediate check for the endpoint; it does not wait for the result. The response returns a `request_id`. The caller polls `GET /api/v1/sites/{blog_id}/history?request_id=<id>` or waits for the event stream to observe the result. This avoids holding HTTP connections open for the duration of a check.

**Relationship to SLA Reporting.** The statistics capability (Capability 3) is a superset of the "Incident History and SLA Reporting" stretch goal from `PROJECT.md`. Building Capability 3 makes that stretch goal a subset of what's already available.

### What needs to be built

- API key management: `jetmon_api_keys` table, key generation/revocation CLI, request authentication middleware with scope enforcement.
- Alert contacts: `jetmon_alert_contacts` table, `jetmon_site_alert_contacts` join table, outbound webhook dispatcher with retry queue.
- Query handlers: thin layer over existing DB functions in `internal/db/`, with response serialisation and cursor pagination.
- Statistics handlers: uptime/response-time aggregation queries; must be pre-aggregated or cached to avoid slow queries on large history tables.
- Manage handlers: validated writes to endpoint and check tables, triggering orchestrator pickup.
- Trigger handler: enqueue immediate check; return `request_id` for polling.
- Rate limiting middleware: per-key token bucket, separate buckets for read/write/trigger, rate-limit headers.
- Integration tests in the Docker Compose environment covering auth, pagination, state consistency, and webhook delivery.

---

## Deferred from Phase 3 (webhooks)

These were considered during Phase 3 design and intentionally left out of v1 with clean upgrade paths.

### `site.state_changed` webhook events

Phase 3 v1 ships only `event.*` webhooks (one per `jetmon_event_transitions` row). A `site.state_changed` rollup webhook — fires when the site row's `current_state` projection flips — was punted because:

- Detecting site-level transitions cleanly without races requires changes to the orchestrator (it currently writes `site_status` but doesn't compute deltas)
- Event-level webhooks already give consumers everything they need to compute site-level rollup themselves
- The schema for site state is downstream of the events tables; we'd be adding a second source of truth for "the site is now Down"

**When to revisit:** a real consumer asks for site-level rollup webhooks specifically. Likely shape: orchestrator emits a "previous_state → new_state" signal alongside the projection write; a delivery worker translates that into `site.state_changed` deliveries. Same retry/filter/signature plumbing as `event.*` webhooks — the only new piece is the orchestrator-side delta computation.

### Grace-period webhook secret rotation

Phase 3 v1 ships immediate-revocation only: rotating a webhook secret invalidates the old secret immediately. Brief signature-verification failures during the consumer's deploy window go into the retry queue and resolve once the consumer rolls.

A future Phase 3.x extension is **grace-period rotation**: server signs with both old and new secrets for a configurable window (24h default), consumer verifies whichever they support, then the old secret expires. This matches Stripe's webhook signing roll model and lets consumers deploy at their own pace.

**Why this is a clean future addition:**
- Schema extension only: add `previous_secret_hash` and `previous_secret_expires_at` columns to `jetmon_webhooks`
- Header format already supports multiple `v1=` values (Stripe-compatible)
- New endpoint shape: `POST /webhooks/{id}/rotate-secret?grace=24h`
- No migration of existing webhooks needed; immediate-revocation is the default if `?grace` is absent

**When to revisit:** a customer-managing consumer (not the gateway, not internal alerting) registers webhooks and asks for graceful rotation, or a compliance requirement forces routine secret rotation.

---

## Architectural roadmap

### Multi-repo / multi-binary split

Today everything lives in one repo and the `jetmon2` binary contains the orchestrator, the API server, the operator dashboard, and (after Phase 3) the webhook delivery worker. The `veriflier2` binary is already separate but in the same repo.

This is fine for now but won't scale operationally. Different concerns have very different deployment shapes:

| Concern | Scaling axis | Deployment shape |
|---------|--------------|------------------|
| Orchestrator | bucket count, check rate | stateful (claims buckets in `jetmon_hosts`); horizontal via bucket coordination |
| API server | request rate | stateless; horizontal behind a load balancer |
| Outbound delivery | event volume + slow third parties | stateless; horizontal via row-claim on per-transport delivery tables |
| Operator dashboard | one-off operator sessions | one per ops region |
| Veriflier | geo-distributed vantage points | one per region |

Putting everything in one binary means scaling the most expensive concern scales the cheap ones with it (CPU and memory headroom that's only used for one purpose). It also concentrates failure modes — a panic in the API server takes down the orchestrator.

**Plausible split:**
- `jetmon-orchestrator` — round loop, check pool, DB writes
- `jetmon-api` — REST API server, auth, rate limiting (read/write surface)
- `jetmon-deliverer` — all outbound dispatch: webhooks (Phase 3), alert contacts, WPCOM notifications
- `jetmon-dashboard` — operator UI / SSE state stream
- `jetmon-verifier` — standalone HTTP check executor (today: `veriflier2`; rename TBD)

**Why `jetmon-deliverer` is one binary, not three.** Webhooks, alert contacts, and WPCOM notifications all share the same plumbing: poll `jetmon_event_transitions` (or a similar source), build a frozen-at-fire-time payload, dispatch with a per-destination in-flight cap, retry on failure with exponential backoff, mark abandoned after N attempts. Only the transport differs (HTTPS POST + HMAC for webhooks, transport-specific protocols for PagerDuty/Slack/email/SMS, internal RPC for WPCOM). Splitting them into separate binaries would triple the operational surface (three deploy units, three retry queues, three sets of metrics) for what is fundamentally one job — outbound dispatch — with pluggable transports. Keeping them in one process also means a single circuit-breaker registry across destinations, which is the natural place to enforce shared-resource caps (e.g. "don't open 5,000 outbound connections during a regional outage").

What this means concretely:
- The Phase 3 webhook worker (`internal/webhooks/worker.go`) is the seed. Its `dispatchTick` / `deliverTick` shape generalizes — the matching, claiming, retry, and abandon logic is transport-agnostic.
- A future refactor abstracts the transport behind a `Dispatcher` interface (`Send(ctx, dest, payload) (status, error)`), with concrete implementations per channel.
- Per-channel state (webhook subscriptions, alert contacts, WPCOM circuit breaker counters) stays in its own table; the worker loops over each.

**Trigger that justifies the split.** A single outbound transport doesn't justify its own binary — webhooks alone could stay co-located with the orchestrator. The argument gets compelling once there are *multiple* transports to dispatch and a shared retry/circuit-breaker substrate to amortize. Adding alert contacts is the moment the abstraction earns its keep; pulling WPCOM notifications out of the orchestrator at the same time is the cleanup that pays off the extraction.

The MySQL schema is already the implicit bus between these — each service reads/writes specific tables. Splitting would mostly be:
1. Extract each concern into its own `cmd/<name>/` directory with a thin main
2. Move shared types into `pkg/` (currently `internal/`) so the binaries can depend on them across repos
3. Decide on repo boundaries (one monorepo with multiple binaries, vs. multiple repos sharing a `pkg/` module)

**Naming opportunity:** "veriflier" is a long-standing typo of "verifier" that has stuck around through the rewrite. A split is a natural moment to rename. Candidates: `verifier`, `witness`, `probe-worker`, `vantage`. Worth deciding before the split happens, not during.

**When to revisit:** when a single binary's resource needs (CPU, memory, restart blast radius) starts working against the operational sweet spot for one of the concerns. The deliverer split specifically becomes worthwhile when alert contacts ship — that's the second outbound transport, and a third (WPCOM notifications) follows for free since they already exist as code that wants to live next to the others.

### Path to a public API

Today's API is internal-only — every caller is a known service (gateway, alerting workers, dashboard) and tenant isolation lives at the gateway. Several Phase 1–3 design decisions take advantage of that and would have to change if Jetmon ever exposes its API directly to end customers without a gateway in front.

The decisions affected:

| Decision | Internal-API form | Public-API form |
|----------|-------------------|-----------------|
| Auth scopes | Three coarse: `read` / `write` / `admin` | Granular per-resource (e.g. `sites:read`, `events:read`, `webhooks:write`) so customer keys can be scoped tightly |
| Error semantics | Honest 401/403/404 (no info-leak hiding) | 404-on-unauthorized (don't leak existence of resources owned by other tenants) |
| Error message verbosity | Verbose (DB error class, query stage) for incident response | Sanitized — internal detail belongs in server logs only |
| Webhook ownership | Any `write`-scope token can manage any webhook (`created_by` audit only) | Per-tenant ownership column; reads/writes filtered by owner |
| Webhook signing | HMAC-SHA256 with shared secret per webhook | Asymmetric (Ed25519) becomes more attractive — public key at a well-known URL, no per-customer secret to leak |
| Rate limiting | Per-key bucket sized for service protection | Per-tenant bucket sized for commerce/abuse |
| Idempotency keys | Scoped by `(api_key_id, key)` | Scoped by `(tenant_id, api_key_id, key)` to prevent cross-tenant collisions |
| Site `id` (= `blog_id`) | Numeric, canonical from WPCOM | Probably still numeric, but tenant-scoped on lookup |

The migrations are individually clean (each is "add a column, filter on it, deprecate the unscoped version") but they touch most of the API surface. A public-API exposure would be a significant project, not a flag flip.

**When to revisit:** if a stakeholder asks "can a customer integration call Jetmon directly?" — the answer should be "let's design that" rather than "yes, here's the URL."

The Q9 (webhook ownership) section in API.md captures the most concrete piece of this; the rest is captured here for visibility when the conversation comes up.

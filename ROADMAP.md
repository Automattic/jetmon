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
Incident closure/recovery is determined by `ended_at` being set; do not use `updated_at` to infer recovery.

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

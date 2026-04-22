# Jetmon Roadmap

Deferred features that are intentionally out of scope for the current implementation but have been identified as important future work. Items here are tracked so they are not forgotten and can be designed with future compatibility in mind.

---

## Public REST API

**Status:** Not started. No existing API surface covers this scope.

### What it is

A versioned, authenticated REST API (`/api/v1/`) that exposes two capabilities:

1. **Query** — read site state, active events, event history, check history, and audit log entries.
2. **Manage** — create, update, and delete checks programmatically, without requiring direct database access.

Currently, Jetmon has no public API. The operator dashboard exposes real-time state via SSE for human consumption. Check configuration requires direct writes to `jetpack_monitor_sites`. Event and audit data requires direct DB queries or use of the `jetmon2 audit` CLI. There is no programmatic interface for external tools to provision monitors or retrieve detection data.

### Why it matters

**uptime-bench integration.** The uptime-bench benchmark harness needs both capabilities to treat Jetmon as a first-class service under evaluation, on equal footing with Pingdom, Datadog Synthetics, and others:

- The uptime-bench Jetmon adapter uses the **manage** API to provision a monitor against a target URL before a scenario run and deprovision it afterward.
- The uptime-bench Jetmon adapter uses the **query** API to retrieve what Jetmon detected and when after the scenario completes — the same data the harness collects from every other service's API.

Without a public API, the Jetmon adapter must read directly from MySQL, which is fragile, requires database credentials in the benchmark tool, and couples uptime-bench to Jetmon's internal schema.

**General automation.** Beyond uptime-bench, a public API enables any tooling — CI pipelines, deployment scripts, customer dashboards, Jetpack features — to interact with Jetmon programmatically without requiring direct DB access or bespoke internal integrations.

### Capability 1: Query API

Read-only endpoints for retrieving monitoring state and history.

| Endpoint | Description |
|---|---|
| `GET /api/v1/sites` | List all monitored sites with current state |
| `GET /api/v1/sites/{blog_id}` | Current state and active event count for one site |
| `GET /api/v1/sites/{blog_id}/events` | Event history; supports `since`, `until`, and `state` query params |
| `GET /api/v1/sites/{blog_id}/events/active` | Currently active (unresolved) events only |
| `GET /api/v1/sites/{blog_id}/checks` | Check configuration for the site |
| `GET /api/v1/sites/{blog_id}/history` | Check timing history (DNS, TCP, TLS, TTFB) with time range params |
| `GET /api/v1/sites/{blog_id}/audit` | Audit log entries with time range and event type filters |

The response schema for events must reflect the event model in `EVENTS.md`: `start_timestamp`, `end_timestamp`, `severity`, `state`, `resolution_reason`, `probe_type`. The query API is the primary interface for the uptime-bench Jetmon adapter.

### Capability 2: Manage API

Write endpoints for programmatic check lifecycle management.

| Endpoint | Description |
|---|---|
| `POST /api/v1/sites/{blog_id}/checks` | Create a new check for a site (URL, check type, frequency, keyword, timeout, redirect policy) |
| `GET /api/v1/sites/{blog_id}/checks/{check_id}` | Get configuration for a specific check |
| `PUT /api/v1/sites/{blog_id}/checks/{check_id}` | Update check configuration |
| `DELETE /api/v1/sites/{blog_id}/checks/{check_id}` | Delete a check and its associated history |
| `POST /api/v1/sites/{blog_id}/checks/{check_id}/pause` | Suspend checking without deleting |
| `POST /api/v1/sites/{blog_id}/checks/{check_id}/resume` | Resume a paused check |
| `PUT /api/v1/sites/{blog_id}/maintenance` | Set a maintenance window (start, end) |
| `DELETE /api/v1/sites/{blog_id}/maintenance` | Clear an active maintenance window |

Creating a check via the API must trigger the same bucket assignment and orchestrator pickup that a direct DB write currently does. The API must not bypass Jetmon's internal coordination — it is a frontend to the same system, not a separate path.

### Design decisions to make before building

**Authentication.** API keys stored in a new `jetmon_api_keys` table (hashed, with optional per-key scope and expiry) are the natural fit. The existing `Authorization: Bearer <token>` pattern from the Veriflier transport is a reference point. OAuth is likely overkill for an internal service.

**Hosting.** Whether the API runs within the `jetmon2` binary (on a separate port from the operator dashboard) or as a standalone binary. Embedding it is simpler and keeps deployment to one artifact; a standalone binary gives independent scaling and failure isolation. The operator dashboard's existing HTTP server is a starting point for the embedded approach.

**Pagination.** Event history and audit log queries can return large result sets. Cursor-based pagination (using `event_id` or `timestamp` as the cursor) is preferable to offset-based pagination for append-only log tables.

**Rate limiting.** Per API key, with limits generous enough for polling adapters (uptime-bench will call Retrieve in a loop) but tight enough to prevent accidental bulk queries from affecting check performance. The orchestrator and API server share the MySQL connection pool — the API must not starve the check pipeline.

**Schema versioning.** The API is `/api/v1/`. Breaking changes require a new version prefix. Additive changes (new fields, new endpoints) are backwards-compatible within v1.

**Relationship to "Incident History and SLA Reporting" stretch goal** (from `PROJECT.md`). That item describes a narrower read API scoped to the Jetpack dashboard. This public API is a superset — it covers the same read use case and adds check management. Building this item makes the SLA reporting stretch goal a subset of what's already available.

### What needs to be built

- API key management: `jetmon_api_keys` table, key generation CLI (`jetmon2 apikey create/revoke/list`), request authentication middleware.
- Query handlers: thin layer over the existing DB query functions in `internal/db/`, with response serialisation and pagination.
- Manage handlers: validated writes to `jetpack_monitor_sites`, triggering bucket re-evaluation where needed.
- Rate limiting middleware: per-key request counting, configurable limits in `config.json`.
- Integration tests: added to the existing test suite in the Docker Compose environment, using the simulated site server.

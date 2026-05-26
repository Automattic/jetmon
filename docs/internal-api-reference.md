# Internal API And CLI

Jetmon's REST API is internal only. Customer auth, tenant isolation, public
errors, plan gates, and public rate limits belong in the gateway. The route
driven OpenAPI contract is available at `GET /api/v1/openapi.json`.

## Rules

| Area | Rule |
| --- | --- |
| Base path | `/api/v1/`; breaking changes need a new version. |
| Auth | `Authorization: Bearer jm_...` for protected routes. |
| Scopes | `read`, `write`, `admin`. |
| Pagination | Cursor pagination for lists. |
| Errors | Stable `code`, human `message`, optional metadata. |
| Idempotency | Mutating POST routes use `Idempotency-Key` where retry can duplicate work. |
| Time | RFC3339 UTC. |
| Envelope | Lists use `{ "data": [...], "page": {...} }`; single resources are bare objects. |

## Authentication

API keys identify internal systems, not users. Tokens are high-entropy `jm_...`
strings stored as sha256 hashes in `jetpack_monitor_api_keys`.

```bash
./bin/jetmon2 keys create --consumer gateway --scope read
./bin/jetmon2 keys list
./bin/jetmon2 keys revoke <key_id>
./bin/jetmon2 keys rotate <key_id>
```

`revoked_at` and `expires_at` are half-open cutoffs: valid strictly before the
cutoff, rejected at or after it.

## Gateway Tenant Context

Only the `gateway` consumer may send:

```text
X-Jetmon-Tenant-ID
X-Jetmon-Public-Scopes
X-Jetmon-Gateway-Request-ID
```

Tenant context scopes site/event/stat routes through
`jetpack_monitor_site_tenants` and webhook/contact routes through
`owner_tenant_id`. Other internal callers remain unscoped operators.

## API CLI

Config precedence: flags, environment, `JETMON_API_CONFIG` or
`~/.config/jetmon2.conf`, then Docker-local defaults.

```bash
make api-cli-token-create
export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token

./bin/jetmon2 api health --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api sites list --output table
./bin/jetmon2 api request GET '/api/v1/sites?limit=5'
```

Token-bearing config files must be mode `0600`. Use `api request` for routes
without typed helpers.

## Endpoint Map

Use `/api/v1/openapi.json` for schemas and operation IDs.

### Utility And Identity

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/health` | none | API and DB liveness. |
| `GET` | `/api/v1/ready` | none | Host readiness. |
| `GET` | `/api/v1/openapi.json` | read | OpenAPI 3.1 contract. |
| `GET` | `/api/v1/me` | read | Current key identity. |
| `GET` | `/api/v1/monitor/stats` | read | Monitor stats and legacy file bodies. |
| `GET` | `/api/v1/monitor/drain-status` | read | In-flight work and drain state. |
| `GET` | `/api/v1/monitor/db-config` | read | Sanitized DB config reload status. |
| `GET` | `/api/v1/verifliers/quorum-report` | read | Vantage health and quorum. |
| `GET` | `/api/v1/audit-log` | read | Audit log query. |

### Rollout

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/rollout/capabilities` | read | Supported rollout features. |
| `POST` | `/api/v1/rollout/sessions` | admin | Create rollout session. |
| `GET` | `/api/v1/rollout/jobs/{job_id}` | read | Fetch stored job result. |
| `POST` | `/api/v1/rollout/preflight` | admin | Config/schema/Veriflier/blocker gate. |
| `POST` | `/api/v1/rollout/smoke` | admin | Read-only sampled probes. |
| `POST` | `/api/v1/rollout/seed` | admin | Seed/adopt side state. |
| `POST` | `/api/v1/rollout/final-reconcile` | admin | Reconcile after v1 stops. |
| `POST` | `/api/v1/rollout/activate-buckets` | admin | Activate v2 bucket range. |
| `POST` | `/api/v1/rollout/release-buckets` | admin | Release v2 bucket range. |
| `GET` | `/api/v1/rollout/status` | read | Rollout mode/ranges/sessions. |
| `GET` | `/api/v1/rollout/bucket-coverage` | read | Coverage gate. |
| `GET` | `/api/v1/rollout/activity-check` | read | Recent activity gate. |
| `GET` | `/api/v1/rollout/projection-drift` | read | Legacy projection drift gate. |
| `POST` | `/api/v1/rollout/compare-methods` | admin | Non-authoritative HEAD/GET compare. |
| `POST` | `/api/v1/rollout/stage-policy` | admin | Stage check-policy migration. |

Mutating rollout operations are dry-run/execute. Dry-run returns a confirmation
token bound to operation, range, request shape, run ID, and API key identity.

### Sites, Events, And Stats

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/sites` | read | List monitored sites. |
| `GET` | `/api/v1/sites/{id}` | read | Site with active events. |
| `POST` | `/api/v1/sites` | write | Create site/config. |
| `PATCH` | `/api/v1/sites/{id}` | write | Partial update. |
| `DELETE` | `/api/v1/sites/{id}` | write | Soft-delete/pause. |
| `POST` | `/api/v1/sites/{id}/pause` | write | Pause and close active events. |
| `POST` | `/api/v1/sites/{id}/resume` | write | Resume monitoring. |
| `POST` | `/api/v1/sites/{id}/trigger-now` | write | One synchronous check. |
| `GET` | `/api/v1/sites/{id}/events` | read | Site event history. |
| `GET` | `/api/v1/sites/{id}/events/{event_id}` | read | Event with transitions. |
| `GET` | `/api/v1/sites/{id}/events/{event_id}/transitions` | read | Transition list. |
| `GET` | `/api/v1/events/{event_id}` | read | Direct event lookup. |
| `POST` | `/api/v1/sites/{id}/events/{event_id}/close` | write | Manual close. |
| `GET` | `/api/v1/sites/{id}/uptime` | read | Event-duration uptime stats. |
| `GET` | `/api/v1/sites/{id}/response-time` | read | Check-history response percentiles. |
| `GET` | `/api/v1/sites/{id}/timing-breakdown` | read | DNS/TCP/TLS/TTFB percentiles. |
| `GET` | `/api/v1/sites/{id}/check-history` | read | Raw timing rows. |

`trigger-now` closes open events on success with `probe_cleared`; on failure it
returns the result but does not open an event. The orchestrator owns event
opening.

### Webhooks

| Method | Path | Scope |
| --- | --- | --- |
| `GET` / `POST` | `/api/v1/webhooks` | read / write |
| `GET` / `PATCH` / `DELETE` | `/api/v1/webhooks/{id}` | read / write / write |
| `POST` | `/api/v1/webhooks/{id}/rotate-secret` | write |
| `GET` | `/api/v1/webhooks/{id}/deliveries` | read |
| `POST` | `/api/v1/webhooks/{id}/deliveries/{delivery_id}/retry` | write |

Filters compose AND across dimensions and whitelist within each dimension:
event type, site IDs, and states. Empty means match all.

Event types: `event.opened`, `event.severity_changed`, `event.state_changed`,
`event.cause_linked`, `event.cause_unlinked`, `event.closed`.

Signature header:

```text
X-Jetmon-Signature: t=<unix>,v1=<hmac_sha256(t.body)>
```

Consumers must validate shape, timestamp age/skew, and HMAC using constant-time
comparison. Reject deliveries older than 5 minutes or more than 1 minute in the
future. Secret rotation is immediate in v1; grace-period dual signing is
deferred.

### Alert Contacts

| Method | Path | Scope |
| --- | --- | --- |
| `GET` / `POST` | `/api/v1/alert-contacts` | read / write |
| `GET` / `PATCH` / `DELETE` | `/api/v1/alert-contacts/{id}` | read / write / write |
| `POST` | `/api/v1/alert-contacts/{id}/test` | write |
| `GET` | `/api/v1/alert-contacts/{id}/deliveries` | read |
| `POST` | `/api/v1/alert-contacts/{id}/deliveries/{delivery_id}/retry` | write |

Transports: `email`, `pagerduty`, `slack`, `teams`. Contacts match
`site_filter` plus `new_severity >= min_severity`. `max_per_hour` caps noise;
test sends bypass severity and rate gates but write audit evidence.

Email uses `EMAIL_TRANSPORT`: `wpcom`, `smtp`, or `stub`.

## Delivery Contract

Webhooks and contacts poll event transitions, enqueue frozen payloads, claim
delivery rows transactionally, and retry:

```text
immediate -> 1m -> 5m -> 30m -> 1h -> 6h -> abandoned
```

Abandoned rows can be manually retried.

## Non-Goals

- No direct customer-facing API without the gateway.
- No customer-visible key management.
- No GraphQL or streaming API.
- No public status-page product inside Jetmon.
- No bulk writes until a real consumer needs them.

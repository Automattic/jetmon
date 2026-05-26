# Internal API And CLI

Jetmon's REST API is internal only. Customer-facing auth, tenant isolation,
plan gating, public error vocabulary, and public rate limits belong in a
separate gateway. Jetmon trusts authenticated internal consumers and records
their actions in audit metadata.

The implementation lives in `internal/api`, `internal/apikeys`,
`internal/webhooks`, and `internal/alerting`. The route-driven OpenAPI contract
is available from a running API at `GET /api/v1/openapi.json`.

## Base Rules

| Area | Rule |
| --- | --- |
| Base path | `/api/v1/`. Shape-breaking changes require a new version. |
| Auth | `Authorization: Bearer jm_...` for all protected routes. |
| Scopes | `read`, `write`, `admin`; no granular per-resource scopes in the internal API. |
| Pagination | Cursor pagination for list routes; default limit is route-specific and capped. |
| Errors | JSON error object with stable `code`, human `message`, and optional reference metadata. |
| Idempotency | Mutating POST routes use `Idempotency-Key` where retries can duplicate work. |
| Time | RFC3339 UTC timestamps. |
| Response envelope | Lists use `{ "data": [...], "page": {...} }`; single resources are bare objects. |

## Authentication

Keys are service identities, not user-delegated credentials. They are stored in
`jetpack_monitor_api_keys` as sha256 hashes of high-entropy random tokens.

```bash
./bin/jetmon2 keys create --consumer gateway --scope read
./bin/jetmon2 keys list
./bin/jetmon2 keys revoke <key_id>
./bin/jetmon2 keys rotate <key_id>
```

`revoked_at` and `expires_at` are half-open cutoffs: a key is valid strictly
before the cutoff and rejected at or after it.

## Gateway Tenant Context

Only the internal consumer named `gateway` may send tenant headers:

```text
X-Jetmon-Tenant-ID
X-Jetmon-Public-Scopes
X-Jetmon-Gateway-Request-ID
```

Optional actor/plan headers may also be recorded. Jetmon rejects these headers
from other consumers.

Tenant context scopes:

- site, event, SLA/stat, check-history, and trigger-now routes through
  `jetpack_monitor_site_tenants`;
- webhook and alert-contact CRUD through `owner_tenant_id`;
- delivery history and retry by first checking ownership of the parent
  webhook/contact.

Normal internal callers that omit tenant context keep unscoped operator
behavior.

## API CLI

`jetmon2 api` is the local developer/operator helper. Config precedence is:

1. command flags,
2. environment variables,
3. `JETMON_API_CONFIG` or `~/.config/jetmon2.conf`,
4. Docker-local defaults.

Local setup:

```bash
make build
make api-cli-token-create

export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token

./bin/jetmon2 api health --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api commands --output table
./bin/jetmon2 api sites list --output table
```

Operator config file:

```bash
./bin/jetmon2 local-config init \
  --base-url=http://localhost:8090 \
  --token-file=jetmon2-api-token
./bin/jetmon2 local-config show
```

Token-bearing config files must be mode `0600`. `api request` is the escape
hatch for routes that do not yet have typed CLI helpers:

```bash
./bin/jetmon2 api request --output table GET '/api/v1/sites?limit=5'
```

## Endpoint Map

This table mirrors `internal/api/routes.go` at a high level. Use
`/api/v1/openapi.json` for generated schemas and operation IDs.

### Utility And Identity

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/health` | none | API and DB liveness. |
| `GET` | `/api/v1/ready` | none | Host readiness for traffic. |
| `GET` | `/api/v1/openapi.json` | read | Route-driven OpenAPI 3.1 contract. |
| `GET` | `/api/v1/me` | read | Current API key identity, scope, rate limit. |
| `GET` | `/api/v1/monitor/stats` | read | Latest Monitor stats snapshot and optional legacy file body. |
| `GET` | `/api/v1/monitor/drain-status` | read | In-flight work counters and drain completion. |
| `GET` | `/api/v1/monitor/db-config` | read | Sanitized DB config and reload status. |
| `GET` | `/api/v1/verifliers/quorum-report` | read | Per-vantage Veriflier health. |
| `GET` | `/api/v1/audit-log` | read | Paginated audit log query. |

### Rollout

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/rollout/capabilities` | read | Contract version and supported rollout features. |
| `POST` | `/api/v1/rollout/sessions` | admin | Create durable rollout session. |
| `GET` | `/api/v1/rollout/jobs/{job_id}` | read | Fetch stored rollout job result. |
| `POST` | `/api/v1/rollout/preflight` | admin | Validate standby config, DB/schema, Verifliers, blockers. |
| `POST` | `/api/v1/rollout/smoke` | admin | Read-only sampled probe smoke. |
| `POST` | `/api/v1/rollout/seed` | admin | Dry-run or execute side-state seed/adoption. |
| `POST` | `/api/v1/rollout/final-reconcile` | admin | Final side-state reconcile after v1 stops. |
| `POST` | `/api/v1/rollout/activate-buckets` | admin | Activate API-controlled v2 bucket range. |
| `POST` | `/api/v1/rollout/release-buckets` | admin | Release active v2 bucket range. |
| `GET` | `/api/v1/rollout/status` | read | Rollout mode, active ranges, sessions. |
| `GET` | `/api/v1/rollout/bucket-coverage` | read | Coverage gate payload. |
| `GET` | `/api/v1/rollout/activity-check` | read | Recent activity gate payload. |
| `GET` | `/api/v1/rollout/projection-drift` | read | Legacy projection drift gate. |
| `POST` | `/api/v1/rollout/compare-methods` | admin | Non-authoritative HEAD/GET comparison. |
| `POST` | `/api/v1/rollout/stage-policy` | admin | Plan/execute staged check-policy migration. |

Mutating rollout operations use two-step execution: dry-run returns a
short-lived confirmation token bound to operation, range, request shape, run ID,
and API key identity; execute must provide that token.

Guided CLI:

```bash
./bin/jetmon2 api rollout guided --bucket-min=0 --bucket-max=99 --dry-run
./bin/jetmon2 api rollout guided --bucket-min=0 --bucket-max=99 --allow-remote
./bin/jetmon2 api rollout guided --bucket-min=0 --bucket-max=99 --rollback --allow-remote
```

### Family 1: Sites And Current State

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/sites` | read | List monitored sites. |
| `GET` | `/api/v1/sites/{id}` | read | Fetch one site with active events. |
| `POST` | `/api/v1/sites` | write | Create site and optional check config. |
| `PATCH` | `/api/v1/sites/{id}` | write | Partial site/config update. |
| `DELETE` | `/api/v1/sites/{id}` | write | Soft-delete and pause monitoring. |
| `POST` | `/api/v1/sites/{id}/pause` | write | Pause monitoring and close active events. |
| `POST` | `/api/v1/sites/{id}/resume` | write | Resume monitoring. |
| `POST` | `/api/v1/sites/{id}/trigger-now` | write | Run one synchronous check. |

Important site fields:

```json
{
  "id": 12345,
  "blog_id": 12345,
  "monitor_url": "https://example.com",
  "monitor_active": true,
  "bucket_no": 0,
  "current_state": "Up",
  "current_severity": 0,
  "active_event_id": null,
  "last_checked_at": "2026-04-25T03:24:11Z",
  "redirect_policy": "follow",
  "request_method": "GET",
  "detection_profile": "full",
  "maintenance_start": null,
  "maintenance_end": null
}
```

`id` and `blog_id` are currently the same value. Consumers should use `id`.
`current_state`, `current_severity`, and `active_event_id` are derived from
open v2 events; legacy `site_status` is only a rollout fallback.

`trigger-now` closes open events on success with `probe_cleared`. On failure it
returns the failed check result but does not open a new event; the orchestrator
owns failure detection and event opening.

### Family 2: Events And History

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/sites/{id}/events` | read | Site event history. |
| `GET` | `/api/v1/sites/{id}/events/{event_id}` | read | Site-scoped event with transitions. |
| `GET` | `/api/v1/sites/{id}/events/{event_id}/transitions` | read | Paginated transition list. |
| `GET` | `/api/v1/events/{event_id}` | read | Direct event lookup. |
| `POST` | `/api/v1/sites/{id}/events/{event_id}/close` | write | Manual event close. |

Event object essentials:

```json
{
  "id": 487291,
  "site_id": 12345,
  "check_type": "http",
  "severity": 4,
  "state": "Down",
  "started_at": "2026-04-25T03:18:38Z",
  "ended_at": null,
  "resolution_reason": null,
  "metadata": {
    "http_code": 503,
    "failure_class": "server",
    "method": "GET",
    "rtt_ms": 84
  }
}
```

Manual close request:

```json
{
  "reason": "manual_override",
  "note": "Confirmed maintenance was running"
}
```

### Family 3: SLA And Statistics

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/sites/{id}/uptime` | read | Uptime/downtime stats from event durations. |
| `GET` | `/api/v1/sites/{id}/response-time` | read | Response-time percentiles from check history. |
| `GET` | `/api/v1/sites/{id}/timing-breakdown` | read | DNS/TCP/TLS/TTFB percentiles. |
| `GET` | `/api/v1/sites/{id}/check-history` | read | Raw per-check timing rows. |

Uptime currently subtracts `Down` and `Seems Down` durations. Warning,
degraded, maintenance, and unknown durations are returned separately.

### Family 4: Webhooks

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/webhooks` | read | List webhooks. |
| `POST` | `/api/v1/webhooks` | write | Create webhook and return secret once. |
| `GET` | `/api/v1/webhooks/{id}` | read | Fetch webhook. |
| `PATCH` | `/api/v1/webhooks/{id}` | write | Update webhook. |
| `DELETE` | `/api/v1/webhooks/{id}` | write | Delete webhook. |
| `POST` | `/api/v1/webhooks/{id}/rotate-secret` | write | Replace signing secret immediately. |
| `GET` | `/api/v1/webhooks/{id}/deliveries` | read | Delivery history. |
| `POST` | `/api/v1/webhooks/{id}/deliveries/{delivery_id}/retry` | write | Retry abandoned delivery. |

Webhook filters compose AND across dimensions and whitelist within each
dimension. Empty means match all:

```text
event_type in events or events empty
AND site_id in site_filter.site_ids or site_filter empty
AND state in state_filter.states or state_filter empty
```

Event types:

- `event.opened`
- `event.severity_changed`
- `event.state_changed`
- `event.cause_linked`
- `event.cause_unlinked`
- `event.closed`

Delivery payload:

```json
{
  "type": "event.opened",
  "delivered_at": "2026-04-25T03:18:38Z",
  "delivery_id": 9182734,
  "event": {},
  "site": {}
}
```

Headers:

```text
Content-Type: application/json
X-Jetmon-Event: event.opened
X-Jetmon-Delivery: 9182734
X-Jetmon-Signature: t=1714685400,v1=<hex_hmac_sha256>
```

#### Signing And Secret Rotation

Signature input is `{timestamp}.{body}` using the webhook secret and HMAC-SHA256.
Consumers should reject malformed headers, mismatched signatures, timestamps
older than 5 minutes, and timestamps more than 1 minute in the future. Use
constant-time comparison.

Secret rotation is immediate in v1. Grace-period dual signing is deferred.

### Family 5: Alert Contacts

| Method | Path | Scope | Purpose |
| --- | --- | --- | --- |
| `GET` | `/api/v1/alert-contacts` | read | List managed alert contacts. |
| `POST` | `/api/v1/alert-contacts` | write | Create contact. |
| `GET` | `/api/v1/alert-contacts/{id}` | read | Fetch contact. |
| `PATCH` | `/api/v1/alert-contacts/{id}` | write | Update contact. |
| `DELETE` | `/api/v1/alert-contacts/{id}` | write | Delete contact. |
| `POST` | `/api/v1/alert-contacts/{id}/test` | write | Send synthetic test notification. |
| `GET` | `/api/v1/alert-contacts/{id}/deliveries` | read | Delivery history. |
| `POST` | `/api/v1/alert-contacts/{id}/deliveries/{delivery_id}/retry` | write | Retry abandoned delivery. |

Transports:

| Transport | Destination shape |
| --- | --- |
| `email` | `{ "address": "ops@example.com" }` |
| `pagerduty` | `{ "integration_key": "<events-v2 routing key>" }` |
| `slack` | `{ "webhook_url": "https://hooks.slack.com/..." }` |
| `teams` | `{ "webhook_url": "https://outlook.office.com/webhook/..." }` |

Contacts use a simpler filter than webhooks:

```text
site_id in site_filter.site_ids or site_filter empty
AND new_severity >= min_severity
```

`max_per_hour` caps notifications per contact. Test sends bypass the severity
gate and rate cap but still log audit evidence. Destination credentials are
write-only in API responses; reads return previews.

#### Email Delivery

Email alert contacts use the configured `EMAIL_TRANSPORT`: `wpcom` in
production, `smtp` for local/staging mail capture, or `stub` for logging-only
delivery.

## Delivery And Retry

Webhook and alert-contact delivery workers poll
`jetpack_monitor_event_transitions`, enqueue delivery rows, and claim pending
rows transactionally. Retry schedule:

| Attempt | Delay |
| --- | --- |
| 1 | immediate |
| 2 | 1m |
| 3 | 5m |
| 4 | 30m |
| 5 | 1h |
| 6 | 6h |

After six failed attempts, the row is `abandoned` and can be manually retried.
Payloads are frozen when the delivery row is created. This is the
frozen-at-fire-time contract: a later event update does not mutate a delivery
that has already been queued.

## Current Non-Goals

- No direct customer-facing API without the gateway.
- No customer-visible key management endpoints.
- No GraphQL.
- No streaming/SSE API surface; webhooks are the event-driven integration.
- No public status-page product inside Jetmon.
- No bulk write endpoints until a real consumer requires them.

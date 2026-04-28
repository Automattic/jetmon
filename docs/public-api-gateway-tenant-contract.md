# Public API Gateway Tenant Contract

**Status:** Planning note. Not implemented by the v2 internal API.

This document defines the expected boundary between a future customer-facing
gateway and Jetmon if the internal API is exposed through that gateway. It is
the first public-API prerequisite before adding tenant ownership columns,
filtered queries, public scopes, or public error redaction inside Jetmon.

ADR-0002 remains the current implementation decision: Jetmon's API is internal
only, every caller is a trusted service, and tenant isolation lives outside
Jetmon. This contract describes the next shape if a gateway turns Jetmon into a
customer-facing product surface.

## Boundary Summary

The gateway owns customer identity. Jetmon owns monitoring correctness.

| Concern | Gateway responsibility | Jetmon responsibility |
|---|---|---|
| Customer authentication | Authenticate the customer, user, team, app, or service token. | Accept only trusted internal service credentials. |
| Tenant identity | Derive a stable tenant id from the authenticated customer context. Never accept tenant ids from the public request body. | Treat tenant id as gateway-derived metadata until Jetmon-side ownership enforcement is intentionally added. |
| Public authorization | Enforce customer plan, feature flags, public scopes, and role membership. | Enforce internal `read` / `write` / `admin` service scopes and resource relationship invariants. |
| Resource ownership | Decide whether the public caller may see or mutate a site, webhook, alert contact, or delivery. | Eventually enforce owner columns on resources that Jetmon manages directly. |
| Error vocabulary | Collapse or sanitize 403/404 and internal errors for customers. | Return operator-accurate internal errors to the gateway. |
| Rate limits | Apply customer fairness, abuse, plan, and route-specific limits. | Keep per-service-key rate limits for internal service protection. |
| Auditing | Record public actor, tenant, OAuth/client app, and gateway decision details. | Record internal consumer, Jetmon request id, and any gateway-derived tenant context that reaches Jetmon. |

## Request Context

When the gateway calls Jetmon on behalf of a customer, it should authenticate
with its normal internal Bearer token and attach public request context as
headers. These headers are not trusted customer input; they are assertions from
the gateway service.

| Header | Required | Meaning |
|---|---|---|
| `X-Jetmon-Tenant-ID` | Yes for customer-routed requests | Stable opaque tenant id derived by the gateway. |
| `X-Jetmon-Actor-ID` | Yes when a human or customer app initiated the request | Stable opaque actor id for audit correlation. |
| `X-Jetmon-Public-Scopes` | Yes for public API calls | Space-separated public scopes that the gateway has already granted, such as `sites:read events:read`. |
| `X-Jetmon-Gateway-Request-ID` | Yes | Gateway request id to correlate public support tickets with Jetmon logs. |
| `X-Jetmon-Plan` | Optional | Plan/tier snapshot useful for audit and abuse investigations. |

Jetmon should only honor these headers from the configured gateway consumer
identity. A non-gateway API key sending public-context headers should be
rejected. Jetmon currently treats `consumer_name = "gateway"` as that trusted
gateway identity, requires tenant id, public scopes, and gateway request id
when any public-context header is present, and records accepted gateway context
in API audit metadata.

## Tenant Checks

The gateway should remain the first and strongest tenant boundary. Jetmon-side
tenant enforcement is still useful as defense in depth and becomes required if
Jetmon ever serves customers without a gateway in front.

| Route family | Gateway checks | Jetmon checks before public exposure |
|---|---|---|
| Sites list/detail | Caller can access each `blog_id`; plan allows monitoring data. | Implemented through `jetmon_site_tenants` when gateway context is present. |
| Event/history/SLA reads | Caller can access the parent site; requested time range and filters are allowed. | Implemented through the parent site's `jetmon_site_tenants` mapping. |
| Site/check writes | Caller can manage the parent site; plan permits monitor mutation and trigger-now. | Implemented through the parent site's `jetmon_site_tenants` mapping; orchestrator/eventstore invariants remain unchanged. |
| Webhook CRUD/deliveries | Caller can manage tenant-owned webhooks; endpoint URL policy is satisfied. | Add `owner_tenant_id` to webhooks and deliveries or derive delivery visibility through the webhook. |
| Alert contact CRUD/deliveries | Caller can manage tenant-owned alert contacts; transport is allowed by plan. | Add `owner_tenant_id` to alert contacts and deliveries or derive delivery visibility through the contact. |
| Manual retries/tests | Caller owns the parent webhook/contact and route-specific abuse limits allow the operation. | Verify parent ownership before enqueueing or dispatching. |
| Health, `/me`, OpenAPI | Gateway decides whether to expose them at all. | No tenant filtering; these remain service introspection routes unless a public variant is designed. |

## Ownership Model

The tenant id should be opaque to Jetmon. It should not encode a WPCOM user id,
blog id, plan, or account type. If those concepts change, the gateway can keep
the same tenant id stable.

For customer-owned resources created in Jetmon, prefer explicit ownership:

- `jetmon_site_tenants(tenant_id, blog_id)` for monitored-site visibility
- `jetmon_webhooks.owner_tenant_id`
- `jetmon_alert_contacts.owner_tenant_id`
- delivery visibility derived from the owned webhook/contact
- idempotency cache scoped by `(tenant_id, api_key_id, idempotency_key)` if the
  cache is made durable or shared across public tenants

For monitored sites, do not assume ownership is always one-to-one with
`blog_id`. Jetmon now enforces site visibility for gateway-routed requests with
the `jetmon_site_tenants(tenant_id, blog_id)` mapping table, which preserves
room for shared ownership or gateway-derived delegation.

Do not use `created_by` as ownership. It records the internal API key consumer
that created a row and is audit-only.

## Public Error Shape

Jetmon can keep returning honest internal errors to the gateway. The gateway is
responsible for public-safe behavior:

- return 404 instead of 403 when a customer tries to access a resource outside
  their tenant
- redact DB stages, verifier names, hostnames, SQL messages, and internal
  delivery errors
- keep Jetmon's `request_id` or gateway request id available for support
  escalation

If Jetmon later implements a native public mode, that mode should have its own
error rendering path instead of weakening the internal API's operator-friendly
errors.

## Migration Path

1. Keep the v2 internal API unchanged while the gateway is the only public
   entry point.
2. Request-context parsing for the headers above is implemented in the API
   middleware and restricted to the gateway API key. Accepted context is logged
   in audit metadata; non-gateway keys asserting it are rejected.
3. Gateway-routed webhook and alert-contact CRUD now set/filter
   `owner_tenant_id`. Delivery history and manual retry visibility are derived
   through the owned webhook/contact, and alert-contact send-test verifies the
   contact owner before loading the destination credential.
4. Gateway-routed site, event/history, SLA/stat, and trigger-now routes now use
   `jetmon_site_tenants` for defense-in-depth ownership checks.
5. Backfill/reconcile `jetmon_site_tenants` from the gateway's source of truth
   before any customer traffic depends on direct Jetmon enforcement.
6. Add public-scope and redaction tests route family by route family.
7. Only after those checks exist, consider exposing Jetmon without a gateway.

## Non-Goals

- This does not add customer authentication to Jetmon.
- This does not change the current internal `read` / `write` / `admin` API key
  scopes.
- This does not decide the customer-facing OAuth, app-token, or WordPress.com
  auth model.
- This does not require tenant columns before the v2 production rollout.

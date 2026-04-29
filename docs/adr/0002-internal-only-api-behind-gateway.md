# 0002 — Internal-only API behind a gateway

**Status:** Accepted (2026-04-22)

## Context

The v2 branch ships a versioned REST API (`/api/v1/...`) covering
sites, events, SLA stats, webhooks, and alert contacts. The API was
originally scoped as "the public API," and several Phase 1 design
decisions were drafted with public-API constraints in mind (granular
per-resource scopes, 404-on-unauthorized to avoid leaking resource
existence, sanitized error messages, per-tenant ownership on every
write surface, etc.).

Mid-Phase-1 the scope changed: a separate gateway service will sit in
front of Jetmon and handle all customer-facing concerns (tenant
isolation, public errors, customer rate limiting, per-tenant
analytics, OAuth, billing). Jetmon's API becomes internal — every
caller is a known service (the gateway, alerting workers, the
operator dashboard, CI tooling, the uptime-bench harness). This
materially changes the appropriate trade-offs across most of the API
surface.

## Decision

We will treat Jetmon's API as **internal-only**. Specifically:

- **Auth scopes are coarse:** `read` / `write` / `admin`. Granular
  per-resource scopes (e.g. `webhooks:write`, `events:read`) are
  unnecessary because all callers are trusted services that operate
  at a single privilege level.
- **Errors are honest.** 401 vs 403 vs 404 are reported correctly
  (no info-leak hiding). Error messages can include operational
  detail (DB error class, the SQL stage that failed) because the
  audience is operators and the gateway, not customers.
- **Webhook and alert-contact ownership is shared.** Any `write`-scope
  token can manage any registration; `created_by` is recorded for
  audit but does not gate access.
- **Idempotency-Key scope is `(api_key_id, key)`.** No tenant in the
  scope tuple because there's no tenant abstraction.
- **Rate limits are per-key, sized for service protection** (preventing
  one buggy caller from DoS-ing the rest), not for commerce or abuse.
- **Resource IDs are raw integers.** No type-prefixed IDs (`evt_`,
  `whk_`); see the "Resolved design questions" section in
  [`../internal-api-reference.md`](../internal-api-reference.md) for
  the full rationale.

Each of these is the appropriate choice for an internal service and
not the appropriate choice for a public API.

## Consequences

**Wins:**
- The implementation is dramatically simpler than a public API. No
  per-tenant isolation, no oauth surface, no analytics events on
  every request, no per-customer rate limit configuration.
- Operators can debug from the API surface directly — error messages
  carry the information needed to diagnose problems.
- Schema design is unconstrained by tenant-scoping concerns, which
  keeps queries fast and indexes simple.

**Costs:**
- If Jetmon's API is ever exposed to customers without a gateway in
  front, several decisions need to be unwound. The migration path is
  documented in [`../roadmap.md`](../roadmap.md) "Path to a public API." Each change is
  individually clean (add a column, filter on it, deprecate the
  unscoped version) but they touch most of the surface, so it would
  be a significant project rather than a flag flip.
- Documentation has to be careful not to leak the internal surface to
  external readers. [`../internal-api-reference.md`](../internal-api-reference.md) is checked-in but is unambiguous about
  internal-only scope; the gateway will re-export a sanitized subset.

## Related

- [`../internal-api-reference.md`](../internal-api-reference.md) — full API reference; the "Resolved design questions"
  section captures the trade-offs that fall out of this decision.
- [`../roadmap.md`](../roadmap.md) "Path to a public API" — what would change if this
  decision is reversed.
- ADR-0003 (Plaintext credentials) — depends on this; if customers
  managed their own webhooks the credential storage threat model
  would shift.

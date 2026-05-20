# 0010 — Trusted Veriflier discovery with monitor-collected telemetry

**Status:** Accepted (2026-05-11)

Operational rollout steps live in
[`../v1-to-v2-migration.md`](../v1-to-v2-migration.md). This ADR explains why
Veriflier discovery is monitor-owned and why Veriflier hosts do not get
database credentials.

## Context

Jetmon v2 needs a safer way to evolve the Veriflier fleet than a static
`VERIFIERS` list on every monitor host. Operators need to add, remove, and
scale Veriflier capacity without copying large config lists everywhere, and the
fleet dashboard needs enough telemetry to show whether Veriflier capacity is
fresh, stale, overloaded, duplicated, or mismatched.

At the same time, Veriflier identity is part of the downtime quorum. A
Veriflier vantage is not just a reachable endpoint; it is a trusted perspective
that can help confirm a customer's site is down. If a Veriflier process could
self-register as a trusted vantage, a bad config, compromised host, duplicate
replica, or test process could accidentally create extra quorum votes.

The security and rollout constraints are:

- Veriflier hosts previously did not need MySQL access.
- Agent liveness and capacity telemetry is useful, but telemetry is not trust.
- Horizontal replicas behind one vantage should add capacity without adding
  independent downtime votes.
- Discovery must be reversible during rollout, with static config available as
  a fallback.
- Operator tools must not print Veriflier auth token values.

## Decision

Jetmon v2 will use an **operator-trusted registry plus monitor-collected
telemetry** for Veriflier discovery.

- `jetpack_monitor_veriflier_vantages` is the trusted registry. Operators create and
  enable one row per quorum-counted Veriflier vantage.
- Only enabled, usable registry rows are eligible for active discovery traffic
  and downtime quorum. A row is usable only when it has an endpoint host,
  endpoint port, and auth token.
- `jetpack_monitor_veriflier_agents` is telemetry only. Monitor hosts poll authenticated
  Veriflier `/v2/status` endpoints and write agent liveness, protocol, version,
  endpoint, queue, and capacity data.
- Veriflier hosts do not write to MySQL and do not self-register trusted
  vantages.
- Agent rows never create quorum votes. A fresh active agent matters only when
  its `vantage_id` matches an operator-approved registry row.
- `VERIFLIER_DISCOVERY_MODE=static|shadow|active` controls rollout:
  - `static` uses config only.
  - `shadow` compares static config, registry rows, and recent agent telemetry
    without changing traffic.
  - `active` uses enabled usable registry rows, but falls back to static config
    if discovery is unavailable or empty during rollout.
- `jetmon2 validate-config`, `jetmon2 verifliers discovery-report`, and the
  fleet dashboard expose discovery status, static-vs-registry drift, stale
  telemetry, duplicate vantage/endpoint warnings, and capacity. They report
  token presence only, never token values.

## Consequences

**Wins:**
- Veriflier hosts keep a smaller privilege surface because they do not need
  database credentials.
- Operators retain explicit control over which vantages can affect downtime
  quorum.
- Horizontal scaling is safe: replicas can share a `vantage_id` and add
  capacity without increasing quorum weight.
- Shadow mode gives a reversible gate before active discovery changes traffic.
- The dashboard and discovery report can explain fleet health using durable
  MySQL state rather than scraping every Veriflier host directly.

**Costs:**
- Operators must seed and maintain the trusted registry. Agent telemetry alone
  is intentionally insufficient.
- A new Veriflier agent may be fresh and healthy but ignored until its
  `vantage_id` is approved in `jetpack_monitor_veriflier_vantages`.
- Discovery depends on monitor hosts polling Verifliers. If monitors cannot
  reach `/v2/status`, agent telemetry becomes stale even if Verifliers are
  healthy.
- Active discovery has a static fallback during rollout, so operators must keep
  static config correct until the fallback removal is explicitly approved.
- Registry/token drift can only be reported as presence mismatch; the tooling
  must not expose token values for direct comparison.

## Alternatives considered

- **Veriflier self-registration into MySQL.** Easier auto-discovery, but it
  gives Veriflier hosts database credentials and allows a process to create or
  modify the trusted identity used for quorum. Rejected.
- **Trust any fresh agent telemetry row.** Operationally simple, but a duplicate
  or rogue agent could create extra votes. Rejected because quorum identity must
  be operator-approved.
- **External service discovery as the source of trust.** DNS, Consul,
  Kubernetes, or another registry could discover endpoints, but Jetmon
  production does not require a cluster orchestrator, and endpoint discovery
  still would not answer which vantages are trusted quorum identities. Deferred.
- **Static config only.** Lowest moving parts, but it keeps horizontal scaling
  and stale fleet visibility tied to manual config rollout on every monitor.

## Related

- [`../v1-to-v2-migration.md`](../v1-to-v2-migration.md) — rollout gates for
  Veriflier contract and discovery.
- [`../operations-guide.md`](../operations-guide.md) — dashboard and
  discovery-report operations.
- [`../data-model.md`](../data-model.md) — `jetpack_monitor_veriflier_vantages` and
  `jetpack_monitor_veriflier_agents`.
- [`../roadmap.md`](../roadmap.md) — deferred production-like discovery soak
  and future probe-agent work.
- `internal/orchestrator` — monitor-side Veriflier discovery and telemetry
  polling.
- `internal/dashboard` — fleet Veriflier dashboard summaries.

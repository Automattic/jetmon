# 0008 — Shadow-v2-state migration with legacy status projection

**Status:** Accepted (2026-04-27)

Operational rollout steps live in
[`../v1-to-v2-migration.md`](../v1-to-v2-migration.md). This ADR explains the
state-model decision behind that runbook.

## Context

Jetmon 2 replaces mutable v1 status handling with event-sourced incident
state (`jetmon_events` + `jetmon_event_transitions`). Production consumers,
however, still read the legacy `jetpack_monitor_sites.site_status` and
`last_status_change` fields. A hard cutover would require every consumer
to migrate at the same time as the monitor binary, which is operationally
fragile.

We considered creating a completely separate v2 sites table, but that
would immediately introduce bidirectional config sync, backfill, and
reconciliation problems. The site/config row is not the hardest part of
the migration; incident state is.

## Decision

Jetmon 2 will use a **shadow-v2-state** migration model:

- `jetmon_events` and `jetmon_event_transitions` are the authoritative
  incident state.
- `jetpack_monitor_sites` remains the legacy site/config table during
  migration.
- While `LEGACY_STATUS_PROJECTION_ENABLE` is true, event mutations also
  update the v1-compatible `site_status` / `last_status_change`
  projection in the same transaction.
- The internal API derives current state from active v2 events first. It
  falls back to legacy `site_status` only while the legacy projection is
  enabled; after disabling projection, "no active v2 event" means `Up`
  regardless of stale legacy status values.
- After downstream readers move to the v2 API/event tables,
  `LEGACY_STATUS_PROJECTION_ENABLE` can be disabled. V2 incident writes
  continue unchanged.

`DB_UPDATES_ENABLE` remains as a deprecated config alias for older local
configs, but `LEGACY_STATUS_PROJECTION_ENABLE` is the real switch.

## Consequences

**Wins:**
- We can deploy v2 without requiring a simultaneous consumer migration.
- Rollback is straightforward: legacy readers still see familiar status
  values while projection is enabled.
- The v2 event model becomes the source of truth immediately, so new API,
  webhook, alerting, and SLA work does not depend on the legacy status
  column.
- Disabling legacy status writes later is a config change, not a schema
  rewrite.

**Costs:**
- During migration, there are two readable state surfaces. The event tables
  are authoritative; the legacy status fields are only a projection.
- Projection drift must be treated as a bug while
  `LEGACY_STATUS_PROJECTION_ENABLE` is true.
- `jetpack_monitor_sites` still carries site configuration and some v2
  additive bookkeeping columns (`last_checked_at`, `ssl_expiry_date`,
  cooldown fields). Disabling legacy status projection does not remove the
  table from the system.

## Alternatives considered

- **Full v2 sites table now.** Cleaner isolation, but much more migration
  machinery: config sync, ownership rules, backfill, reconciliation, and
  dual-write failure handling. Deferred until legacy schema constraints
  actually block v2 feature work.
- **Only additive migrations on the legacy table.** Simpler schema, but it
  keeps incident state conceptually tied to `site_status` and makes the
  eventual cutover harder to reason about.
- **Hard cutover to v2 event tables.** Cleanest end state, highest rollout
  risk.

## Related

- ADR-0001 — Event-sourced state model.
- `EVENTS.md` — event lifecycle and projection invariants.
- `internal/eventstore` — sole writer for event rows and transitions.

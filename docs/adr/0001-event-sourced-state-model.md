# 0001 — Event-sourced state model with dedicated transitions table

**Status:** Accepted (2026-04-22)

## Context

Jetmon 1 stored the current site status as a column on
`jetpack_monitor_sites` (`site_status`, with a `last_status_change`
timestamp) and emitted a notification on every transition. There was
no durable history of state changes — the WPCOM API was the only
record of what happened. This made several common questions hard or
impossible to answer:

- "Why was site X notified as down at 04:12 UTC? What were the check
  results that led to that?"
- "How many times did site X flap between Down and SeemsDown over the
  last hour?"
- "Did the verifier confirm the down at 04:12 or was it a single-host
  decision?"
- "Did this row's status change because of a new check, a verifier
  update, an operator close, or a maintenance window?"

The site row was a projection — useful for "is this site up right
now?" — but it had no audit story. Every customer escalation that
touched "what happened" required digging through StatsD, application
logs, and WPCOM-side records.

The v2 redesign needed a durable, queryable record of every state
change to support the planned events / SLA / webhooks / alert-contacts
surface. We considered three shapes during design:

- **Option 1 — Reuse `jetmon_audit_log`.** Add `old_status` /
  `new_status` columns and emit one audit row per status change. Single
  table, no schema growth. Rejected because audit log was operational
  ("who did what to the system") and conflating it with site state
  history made both queries slower and the schema confusing — the
  audit log is for actions, not state.

- **Option 2 — Dedicated `jetmon_event_transitions` table.** One row
  per transition with `severity_before` / `severity_after` /
  `state_before` / `state_after` / `reason` / `source` / `metadata`.
  Append-only. Pairs with a `jetmon_events` table holding the current
  authoritative state of each open incident.

- **Option 3 — Synthesize from `jetmon_check_history`.** Compute
  state changes by walking the check history table. Rejected because
  not every check produces a transition, the verifier's outcome can
  override individual check results, and operator manual closes don't
  appear in check history at all.

## Decision

We will store every site state change in a dedicated, append-only
`jetmon_event_transitions` table, paired with a current-state
projection in `jetmon_events`. `internal/eventstore` is the single
writer for both, writing each transition + projection update in one
transaction so they cannot disagree.

Each transition row records:
- `event_id` (the open incident this transition belongs to)
- `severity_before`, `severity_after` (uint8 from
  `internal/eventstore.Severity*`)
- `state_before`, `state_after` (string state names)
- `reason` (e.g. `opened`, `verifier_confirmed`, `manual_override`,
  `superseded`)
- `source` (which jetmon2 instance or which API caller wrote it)
- `metadata` (JSON blob with check results, verifier outputs, etc.)
- `changed_at` (timestamp with millisecond precision)

`jetmon_events` rows have a generated `dedup_key` column that is
non-NULL only while `ended_at IS NULL`, with a `UNIQUE KEY` enforcing
"one open event per (blog_id, endpoint_id, check_type, discriminator)
tuple" without requiring partial indexes (which MySQL lacks).

## Consequences

**Wins:**
- Every customer-facing question about site history has a single,
  authoritative source.
- The webhook and alerting workers consume `jetmon_event_transitions`
  via a high-water mark — no in-process pub/sub needed (see ADR-0005).
- The transition table is naturally auditable: who/what/when for every
  change is on the row.
- The five-layer severity ladder (`Up < Warning < Degraded <
  SeemsDown < Down`) is uniformly applied and queryable; severity
  evolves independently of state.

**Costs:**
- Two tables instead of a column. Storage cost is bounded — one row
  per real state change, not one per check — but non-zero.
- Writes are now transactional across two tables. Mitigated by
  `internal/eventstore` owning the contract.
- Migration path from Jetmon 1 is non-trivial. Acceptable because
  v2 is a separate branch (PR #61) intentionally not drop-in
  compatible.

## Alternatives considered

See Context. The audit-log overload (Option 1) was the most tempting
shortcut and is the path most projects regret later — once the audit
log mixes operational events with state-change events, every query
gets harder.

## Related

- `internal/eventstore/` — the single writer
- Migrations 10 (`jetmon_events`) and 11 (`jetmon_event_transitions`)
- ADR-0005 (Pull-only delivery via event transitions)

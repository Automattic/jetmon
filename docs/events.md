# Jetmon Event Model

This document defines the event-sourced model that underlies Jetmon site
state. Table ownership is summarized in [data-model.md](data-model.md);
detection vocabulary and scope live in [taxonomy.md](taxonomy.md).

## Why Event-Sourced

Mutable site-row status loses history, makes retries ambiguous, and couples
severity changes to state changes. The event log fixes that:

- open/close history and intra-event mutations are preserved
- severity can change without inventing a new incident
- duplicate probe results become idempotent
- the legacy site projection can be rebuilt if it ever drifts

## Two Tables

`jetpack_monitor_events` is the authoritative current/final row for an
incident. It is mutable while open and frozen after close.

`jetpack_monitor_event_transitions` is the append-only history of every event
mutation. Every open, severity change, state change, cause-link change, and
close writes exactly one transition in the same transaction as the event row
update.

Operational evidence belongs in `jetpack_monitor_audit_log`, not in the event
tables. Audit rows record what Jetmon did, such as WPCOM retries, Veriflier
RPCs, config reloads, and alert suppression. Site state changes go through
`internal/eventstore`.

## Event Row

`jetpack_monitor_events` represents one condition affecting a site over a time
range. There is at most one open row per
`(blog_id, endpoint_id, check_type, discriminator)` tuple.

| Field | Notes |
| --- | --- |
| `id` | Primary key. |
| `blog_id` | Site identity. |
| `endpoint_id` | Endpoint row id when applicable; null for site-level events. |
| `check_type` | Probe type such as `http`, `dns`, `tls_expiry`, or `tls_deprecated`. |
| `discriminator` | Optional tiebreaker for multiple concurrent failures of one check type. |
| `severity` | Numeric ordering for thresholds and escalation. |
| `state` | Human-readable lifecycle label. |
| `started_at` | When the condition began; frozen across severity/state changes. |
| `ended_at` | When the condition resolved; null while open. |
| `resolution_reason` | Why the event ended; null while open. |
| `cause_event_id` | Causal link to a root-cause event, separate from rollup. |
| `metadata` | Check-type-specific payload. |
| `dedup_key` | Generated open-event identity used by the unique index. |

## Transition Row

`jetpack_monitor_event_transitions` records how an event changed.

| Field | Notes |
| --- | --- |
| `id` | Primary key. |
| `event_id` | Event being mutated. |
| `blog_id` | Denormalized for SLA/report queries. |
| `severity_before` / `severity_after` | Null on open/close as appropriate. |
| `state_before` / `state_after` | Null on open/close as appropriate. |
| `reason` | Why the transition happened. |
| `source` | Actor, such as `local`, `veriflier:us-west`, or `operator:user@host`. |
| `metadata` | Transition-specific context. |
| `changed_at` | Millisecond-precision ordering. |

## Severity, State, And Identity

Severity is numeric. It orders events and drives thresholds. It can change on a
live event without changing state.

State is the lifecycle label: `Up -> Seems Down -> Down -> Resolved`.

Event identity is `(blog_id, endpoint_id, check_type, discriminator)`. Repeated
results for the same condition update the existing open row instead of opening
duplicates.

MySQL has no partial unique indexes, so `dedup_key` is generated only while an
event is open and becomes `NULL` after close. The unique index rejects two open
events with the same tuple while allowing multiple closed historical rows.

The insert/update path is intentionally simple:

```sql
INSERT INTO jetpack_monitor_events (blog_id, endpoint_id, check_type, discriminator, severity, state, ...)
VALUES (?, ?, ?, ?, ?, ?, ...)
ON DUPLICATE KEY UPDATE
    severity = VALUES(severity),
    state    = VALUES(state),
    metadata = VALUES(metadata);
```

`eventstore` wraps this path so callers do not touch the event tables directly.

## Lifecycle

```text
          first failure                verifier confirms
    Up ------------------> Seems Down -------------------> Down
                              |                            |
                              | verifier disagrees         | condition clears
                              v                            v
                              Up                        Resolved
```

### Up

No active event. Probes are succeeding.

### Seems Down

`Seems Down` is first-class. It opens on the first local failure, not when the
local retry queue eventually escalates to Verifliers. This keeps `started_at`
honest: incident duration starts when Jetmon first saw evidence of impact.

The first failure writes the event row and an `opened` transition in one
transaction. Later identical local-retry failures are no-ops on the event table
unless severity, state, metadata, or lifecycle reason changes.

HTTP failure metadata includes the legacy failure class, detector class, status
code, method, RTT, URL, keyword rule, bounded error detail, redirect detail, TLS
detail, DNS error detail, and bounded body-read diagnostics when available.
Response bodies are not stored.

Each HTTP failure also stores an observation window: checked time, first failed
time, previous observed time, previous known-good time, normal interval, and
next interval. Recovery transitions similarly store first recovered and closed
times. This lets operators explain what Jetmon observed without pretending the
exact customer-impact start time is known.

Outcomes:

- local probe recovers before verifier confirmation: close with
  `probe_cleared`
- Veriflier confirms: update the same event to `Down`, bump severity, and write
  `verifier_confirmed`
- Veriflier disagrees: close with `false_alarm`

### Down

The outage is confirmed. Severity can still evolve in place as additional
evidence arrives; every severity change writes a transition. Recovery from
`Down` closes the event with `verifier_cleared`.

### Resolved

The condition has cleared. `ended_at` and `resolution_reason` are set, a final
transition is appended, and the event row is historical.

## Legacy Projection

During migration, `jetpack_monitor_sites.site_status` and
`last_status_change` are compatibility projections. While
`LEGACY_STATUS_PROJECTION_ENABLE` is true, event row, transition row, and
legacy projection update in the same transaction.

Once downstream readers move to the v2 API/event tables, disable the legacy
projection and stop treating the legacy status fields as source of truth.

## Causal Links

Events can reference other events as causes. A DNS failure cascading into HTTP
failures can create separate HTTP events whose `cause_event_id` points at the
DNS event.

Causal links are not rollup. Rollup answers "what should this site summary
show?" Causality answers "what caused this event?" Keep those query shapes and
retention needs separate.

## Shared Deduplication

All probe types use the shared runner and eventstore path. New probes must not
implement their own deduplication. The shared path owns:

- event identity
- duplicate collapse
- dispatch batching/rate limiting
- event write ordering

## Transition Reasons

Every transition row records why the change happened. Current reasons:

- `opened`: first transition for a new event
- `severity_escalation`: severity increased without changing state
- `severity_deescalation`: severity decreased without changing state
- `verifier_confirmed`: `Seems Down -> Down`
- `verifier_cleared`: confirmed outage recovered
- `probe_cleared`: local probe recovered before confirmation, or an advisory
  condition cleared
- `false_alarm`: Verifliers disagreed with the local failure
- `manual_override`: operator changed or closed the event
- `maintenance_swallowed`: maintenance window suppressed the event
- `superseded`: broader event replaced this one
- `auto_timeout`: event aged out by retention/timeout policy
- `cause_linked` / `cause_unlinked`: cause relationship changed

Closed reasons are also written to `jetpack_monitor_events.resolution_reason`
so the current/final row answers why it closed without a join.

Add new reasons as explicit code constants, not free text. The column is
`VARCHAR(64)` rather than MySQL `ENUM` so new reasons do not require schema
migrations.

## Open Questions

- Retention: how long should closed events stay at full fidelity before rollup?
- Causal graph consumers: which queries should causal links support?
- Cross-probe severity: should API rollup use max severity, a weighted score,
  or another model when multiple probe types fire?

## Invariants

1. Event write and legacy projection update are atomic while
   `LEGACY_STATUS_PROJECTION_ENABLE` is true.
2. Every event mutation writes exactly one transition in the same transaction.
3. Replaying the same probe result twice produces one event and one `opened`
   transition.
4. `Seems Down -> Up` closes with `resolution_reason = false_alarm` when
   Verifliers disagree.
5. Severity updates on a live event do not create a new event row, but do
   create a transition row.
6. Closed events are never mutated except by audited backfill/migration.
7. After closing tuple T, a new failure for tuple T can immediately open a new
   event without conflicting on `dedup_key`.
8. Replaying transitions in `changed_at` order reconstructs the event's current
   severity and state.

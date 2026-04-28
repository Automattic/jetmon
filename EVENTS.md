# Jetmon Event Model

This document describes the event-sourced architecture that underlies site state in Jetmon.

## Why event-sourced

Early designs used a mutable `state` column on the site row as the primary record of truth. That approach loses history, makes retries ambiguous, and couples severity changes to state changes in ways that don't reflect reality (a worsening degradation isn't a new outage). Moving to an event log fixes this:

- Full history is preserved for free.
- Severity can evolve within a single event without inventing artificial state transitions.
- Retries and duplicate probe results become idempotent rather than destructive.
- Derived/denormalized fields on the site row can be rebuilt from the log if they ever drift.

## The event

An event represents a condition affecting a site over a time range.

| Field                | Type            | Notes                                                      |
|----------------------|-----------------|------------------------------------------------------------|
| `id`                 | identifier      | Idempotent — see "Identity" below.                         |
| `site_id`            | FK              | The site this event is about.                              |
| `start_timestamp`    | timestamp       | When the condition began.                                  |
| `end_timestamp`      | timestamp, null | When the condition resolved. Null while active.            |
| `severity`           | numeric         | Ordered, suitable for thresholds and escalation.           |
| `state`              | enum/string     | Human-readable lifecycle label.                            |
| `resolution_reason`  | enum, null      | Why the event ended. Null while active.                    |
| `probe_type`         | enum            | Which probe observed this (HTTP, DNS, TCP, etc.).          |

### Severity vs. state

**Severity** is numeric. It orders events and drives thresholds. It can be updated on a live event without changing `state` — if a degradation worsens, bump severity, leave state alone.

**State** is a human-readable label tied to the lifecycle. It changes at lifecycle boundaries: `Up → Seems Down → Down → Resolved`.

Keeping these separate avoids conflating "this got worse" with "this is a different kind of problem."

### Identity and idempotency

Event `id` is derived from a stable set of inputs — typically `(site_id, check_type, start_timestamp_bucket)` or equivalent — so that repeated check results for the same underlying condition resolve to the same event row. This makes writes idempotent: a retried check result updates the existing event rather than creating a new one.

## Lifecycle

```
          first failure                verifier confirms
    Up ─────────────────▶ Seems Down ───────────────────▶ Down
                              │                            │
                              │  verifier disagrees        │  condition clears
                              │  (false alarm)             │
                              ▼                            ▼
                              Up                        Resolved
```

### Up

No active event. Probes are succeeding.

### Seems Down (transient)

A probe has failed but the verifier has not yet confirmed. This is a **real state**, not an implementation detail — dashboards show it, alert rules can key off it, and it has its own severity range.

The verifier path has two outcomes:
- **Confirmed** → close `Seems Down` with `resolution_reason = promoted_to_confirmed_down`, then open a new `Down` event carrying the same original `start_timestamp`.
- **Disagreed** → event ends with `resolution_reason = false_alarm`, site returns to `Up`.

### Down

Outage confirmed. The event row remains immutable except closure fields (`end_timestamp`, `resolution_reason`); worsening conditions should close/open distinct events per lifecycle policy.

### Resolved

Condition has cleared. `end_timestamp` is set, `resolution_reason` is recorded. The event row is now historical — it is not deleted or mutated further.

## The site row projection

For read performance (dashboards, API queries, bulk lists), the current derived state is denormalized onto the site row:

- `current_state`
- `current_severity`
- `active_event_id` (null when Up)

**This projection is updated in the same transaction as the event write.** Always. There is no eventual consistency here — if they drift, we have a bug.

The projection is rebuildable from the event log. If it's ever suspected to be wrong, rebuild it; don't patch it.

## Causal links

Events can reference other events as causes. A DNS failure cascading into HTTP failures creates multiple events with causal links from the HTTP events back to the DNS event.

Causal links are stored as a separate structure (e.g., `event_causes`) with `(effect_event_id, cause_event_id)`. They are **not** the same as rollup.

### Why not rollup?

Rollup aggregates events for display ("this site had 3 events in the last hour"). Causal linking explains relationships ("the HTTP outage was caused by the DNS outage"). They have different query patterns, different retention needs, and different consumers. Keep them separate.

## Deduplication

All probe types share a single runner. The runner is responsible for:

- Applying idempotent event identity so duplicate results collapse into one event.
- Batching and rate-limiting probe dispatch.
- Feeding results into the event writer with the correct ordering guarantees.

New probe types plug into this runner. They do not implement their own dedup.

## Resolution reasons

Every event close records why. Current reasons:

- `promoted_to_confirmed_down` — Seems Down was verifier-confirmed and transitioned to a Down event.
- `verifier_cleared` — verifier confirms the site is back up.
- `false_alarm` — verifier disagreed with the initial failure signal.
- `manual_override` — an operator closed the event.
- `auto_timeout` — event aged out per retention/timeout policy.

New reasons should be added as explicit enum values, not free-text.

## Open questions

- **Retention**: how long do we keep closed events at full fidelity before rolling them up?
- **Causal graph consumers**: who reads the causal links and what query shapes do they need? That dictates indexing.
- **Cross-probe severity**: when multiple probe types fire on the same site, does the site-row `current_severity` take the max, a weighted sum, or something else?

## Invariants worth testing

1. Event write and site-row projection update are atomic.
2. Replaying the same probe result twice produces the same single event.
3. `Seems Down → Up` (false alarm) correctly closes the event with `resolution_reason = false_alarm`.
4. Severity updates on a live event do not create a new event row.
5. Closed events are never mutated (except possibly by a backfill/migration, which should be audited).

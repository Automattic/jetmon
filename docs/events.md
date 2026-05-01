# Jetmon Event Model

This document describes the event-sourced architecture that underlies site state in Jetmon.

## Why event-sourced

Early designs used a mutable `state` column on the site row as the primary record of truth. That approach loses history, makes retries ambiguous, and couples severity changes to state changes in ways that don't reflect reality (a worsening degradation isn't a new outage). Moving to an event log fixes this:

- Full history is preserved across both event boundaries (open/close) and intra-event mutations (severity bumps, state transitions, cause links).
- Severity can evolve within a single event without inventing artificial state transitions.
- Retries and duplicate probe results become idempotent rather than destructive.
- Derived/denormalized fields on the site row can be rebuilt from the log if they ever drift.

## The two-table split

The model splits the event into two tables:

- **`jetmon_events`** — one row per incident, holding the *current* (or final) severity, state, and metadata. Mutable while the incident is open; frozen on close.
- **`jetmon_event_transitions`** — append-only history of every mutation made to a `jetmon_events` row. One row per change, never updated, never deleted.

The events row is the authoritative current-state projection. The transitions table is the full audit trail of how it got there. Together they give you:

- Cheap "what's the current state of incident X" reads (single row in `jetmon_events`).
- Complete "how did incident X evolve over time" reads (`SELECT * FROM jetmon_event_transitions WHERE event_id = ? ORDER BY changed_at`).
- Independent retention policies — incidents can be pruned aggressively for the live table while transitions are kept long enough for SLA reports.

**Operational logging stays in `jetmon_audit_log`.** That table records what the *monitor* did (WPCOM retries, verifier RPCs, config reloads, alert suppressions). Site-state changes do not flow through it — those go to the events tables. See "Relationship to `jetmon_audit_log`" below.

## The event row

`jetmon_events` represents a condition affecting a site over a time range. There is at most one *open* row per `(blog_id, endpoint_id, check_type, discriminator)` tuple at any given time (see "Identity and idempotency").

| Field                | Type             | Notes                                                                    |
|----------------------|------------------|--------------------------------------------------------------------------|
| `id`                 | BIGINT UNSIGNED  | Primary key.                                                             |
| `blog_id`            | BIGINT UNSIGNED  | The site this event is about. (`site_id` in taxonomy.md terms.)          |
| `endpoint_id`        | BIGINT UNSIGNED, null | The endpoint, when applicable. Null for site-level events.          |
| `check_type`         | VARCHAR(64)      | Which probe observed this — `http`, `dns`, `tls_expiry`, `tls_deprecated`, etc. |
| `discriminator`      | VARCHAR(128), null | Optional tiebreaker for tuples that can have multiple concurrent failures (e.g. multiple keyword checks on the same endpoint). |
| `severity`           | TINYINT UNSIGNED | Ordered, suitable for thresholds and escalation.                         |
| `state`              | VARCHAR(32)      | Human-readable lifecycle label.                                          |
| `started_at`         | TIMESTAMP(3)     | When the condition began. Frozen across severity/state changes.          |
| `ended_at`           | TIMESTAMP(3), null | When the condition resolved. Null while active.                        |
| `resolution_reason`  | VARCHAR(64), null | Why the event ended. Null while active.                                 |
| `cause_event_id`     | BIGINT UNSIGNED, null | Causal link to a root-cause event (separate from rollup).           |
| `metadata`           | JSON, null       | Check-type-specific payload (HTTP code, RTT, days-to-expiry, etc.).      |
| `updated_at`         | TIMESTAMP(3)     | ON UPDATE CURRENT_TIMESTAMP — convenience for the dedup path.            |
| `dedup_key`          | VARCHAR generated | Stored generated column carrying the identity tuple while the event is open, NULL once closed. Backed by a unique index — see "Identity and idempotency". |

## The transition row

`jetmon_event_transitions` is the append-only history. Every mutation to a `jetmon_events` row writes exactly one transition row, in the same database transaction.

| Field              | Type             | Notes                                                                          |
|--------------------|------------------|--------------------------------------------------------------------------------|
| `id`               | BIGINT UNSIGNED  | Primary key.                                                                   |
| `event_id`         | BIGINT UNSIGNED  | The event this transition applies to.                                          |
| `blog_id`          | BIGINT UNSIGNED  | Denormalized from `jetmon_events.blog_id` — avoids a join for SLA queries.     |
| `severity_before`  | TINYINT UNSIGNED, null | Severity before the change. Null on `opened`.                            |
| `severity_after`   | TINYINT UNSIGNED, null | Severity after the change. Null on `closed`.                             |
| `state_before`     | VARCHAR(32), null | State before the change. Null on `opened`.                                    |
| `state_after`      | VARCHAR(32), null | State after the change. Null on `closed` (or set to `Resolved`).              |
| `reason`           | VARCHAR(64)      | Why the transition occurred. See "Transition reasons" below.                   |
| `source`           | VARCHAR(255)     | Who caused it: `local`, `veriflier:us-west`, `operator:user@host`, `system:timeout`. |
| `metadata`         | JSON, null       | Transition-specific context (HTTP code on escalation, cause id on link, etc.). |
| `changed_at`       | TIMESTAMP(3)     | Millisecond precision; SLA report ordering needs sub-second tiebreakers.       |

### Severity vs. state

**Severity** is numeric. It orders events and drives thresholds. It can be updated on a live event without changing `state` — if a degradation worsens, bump severity, leave state alone.

**State** is a human-readable label tied to the lifecycle. It changes at lifecycle boundaries: `Up → Seems Down → Down → Resolved`.

Keeping these separate avoids conflating "this got worse" with "this is a different kind of problem."

### Identity and idempotency

Event identity is the tuple `(blog_id, endpoint_id, check_type, discriminator)`. Repeated probe results for the same underlying condition must resolve to the same `jetmon_events` row — a retried result updates the existing row rather than creating a new one.

MySQL has no partial unique indexes, so the schema enforces "at most one *open* event per tuple" with a generated column trick:

- `dedup_key` is a `VARCHAR GENERATED ALWAYS AS (... ) STORED` column.
- It evaluates to a `CONCAT_WS` of the tuple while `ended_at IS NULL`, and to `NULL` once the event is closed.
- A `UNIQUE KEY` on `dedup_key` rejects two open rows with the same tuple. Multiple `NULL`s are allowed by MySQL's unique-index semantics, so closed events never conflict.

The probe runner's insert path collapses to a single statement:

```sql
INSERT INTO jetmon_events (blog_id, endpoint_id, check_type, discriminator, severity, state, ...)
VALUES (?, ?, ?, ?, ?, ?, ...)
ON DUPLICATE KEY UPDATE
    severity = VALUES(severity),
    state    = VALUES(state),
    metadata = VALUES(metadata);
```

No `SELECT … FOR UPDATE` dance, no optimistic-concurrency loop. The dedup logic is enforced by the schema and the `eventstore` package wraps it so external callers never touch the table directly.

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

**The event opens on the first local failure**, not when the local retry queue eventually escalates to verifiers. This is non-negotiable: `started_at` must equal "first time we saw something wrong" so incident duration is honest. Subsequent local-retry failures are no-ops on the events table — the schema's idempotent `dedup_key` collapses them into the same row, and the `eventstore` writer skips a transition row when severity and state are unchanged.

The first failure writes both an event row (`state = Seems Down`, `severity = 3`, `started_at = now`) and an `opened` transition row in one transaction.

Three outcomes from Seems Down:

- **Local probe recovers** before reaching verifier escalation → event closes with `resolution_reason = probe_cleared`. No verifier was involved; this is the "transient blip the local retry caught" path. The count of these is itself a useful signal — a baseline rate of probe-cleared closes tells you how noisy your detection is.
- **Verifier confirms** → state changes to `Down` in place, severity bumps to 4; one transition row records `state_before = Seems Down`, `state_after = Down`, `severity_before = 3`, `severity_after = 4`, `reason = verifier_confirmed`. `started_at` does not change.
- **Verifier disagrees** → event closes with `resolution_reason = false_alarm`; one transition row records `state_after = Resolved`, `reason = false_alarm`.

### Down

Outage confirmed. Severity may continue to evolve in place as additional probes report. **Each severity bump writes a transition row** (`severity_before`, `severity_after`, `reason = severity_escalation` or `severity_deescalation`). The `jetmon_events` row stores only the latest severity; the history lives in `jetmon_event_transitions`.

Recovery from Down — the next successful local probe — closes the event with `resolution_reason = verifier_cleared`. (V1 of the integration trusts the local probe on the recovery path; a future "verifier-on-recovery" check would distinguish probe-cleared from verifier-cleared on this path too.)

### Resolved

Condition has cleared. `ended_at` is set, `resolution_reason` is recorded, and a transition row with `reason = <resolution_reason>` is appended. The event row is now historical — it is not deleted or mutated further.

## The site row projection

During the [v1-to-v2 migration](v1-to-v2-migration.md),
`jetpack_monitor_sites` remains the legacy site/config table and compatibility
projection. The authoritative incident state is the v2 event model:

- `jetmon_events` stores the current incident row.
- `jetmon_event_transitions` stores every mutation.
- `jetpack_monitor_sites.site_status` and `last_status_change` are derived
  compatibility fields for v1 readers.

While `LEGACY_STATUS_PROJECTION_ENABLE` is true, the legacy projection is updated
in the same transaction as the event write. There is no eventual consistency in
migration mode: event mutation, transition row, and v1 projection commit or roll
back together.

Once all downstream readers have moved to the v2 API/event tables,
`LEGACY_STATUS_PROJECTION_ENABLE` can be set to false. At that point the legacy
status fields stop being maintained and must not be treated as source of truth.

The compatibility projection is rebuildable from `jetmon_events` (current state)
plus `jetmon_event_transitions` (full history). If the projection is ever
suspected to be wrong during migration, rebuild it; don't patch it by hand.

## Relationship to `jetmon_audit_log`

`jetmon_audit_log` is the **operational** log — it records what the monitor did, not what happened to a site:

- WPCOM notification sends and retries
- Verifier RPC dispatch
- Retry-queue dispatch
- Alert suppression and maintenance-window swallowing decisions
- Config reloads

Site-state changes do **not** go through the audit log. Those flow through `jetmon_events` (current state) and `jetmon_event_transitions` (history). The audit log links to events through a nullable `event_id` so an operator can pivot from "this WPCOM retry" to "the incident it was for" with one query.

The split exists because the two trails have different consumers and different retention needs:

| Trail | Consumer | Retention shape |
|-------|----------|-----------------|
| `jetmon_events` + `jetmon_event_transitions` | Public API incident timelines, SLA reports | Long — 30/90 days at full fidelity, then rolled up |
| `jetmon_audit_log` | Operators investigating "why did the alert fire" | Short — aggressive pruning is fine once the incident is closed |
| `jetmon_check_history` | Response-time trending, baseline learning | Medium — granular timing is high volume |

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

## Transition reasons

Every transition row records *why* the change happened. The seeded vocabulary, in approximate order of frequency:

- `opened` — first transition for a new event.
- `severity_escalation` — severity went up on the same state (e.g. degradation worsening).
- `severity_deescalation` — severity went down on the same state.
- `verifier_confirmed` — Seems Down → Down.
- `verifier_cleared` — site returns to Up after a verifier-confirmed Down; closes the event.
- `probe_cleared` — site returns to Up while still in Seems Down (verifier was never invoked or never confirmed); closes the event. Count of these per site over time is the false-positive rate of local detection.
- `false_alarm` — verifier disagreed with the initial failure signal; closes the event.
- `manual_override` — an operator changed state or closed the event.
- `maintenance_swallowed` — event closed because a maintenance window started; failures detected inside the active window are recorded operationally but do not open a downtime event.
- `superseded` — closed because a broader event subsumed it.
- `auto_timeout` — event aged out per retention/timeout policy.
- `cause_linked` / `cause_unlinked` — `cause_event_id` was set or cleared on an open event.

The "closed" reasons (`verifier_cleared`, `probe_cleared`, `false_alarm`, `manual_override`, `maintenance_swallowed`, `superseded`, `auto_timeout`) are also written to `jetmon_events.resolution_reason` on close, so the live row carries the immediate "why is this closed" answer without needing a join.

New reasons should be added as explicit enum values in code, not free-text. The column is `VARCHAR(64)` (not MySQL `ENUM`) so adding a value doesn't require a schema migration.

## Open questions

- **Retention**: how long do we keep closed events at full fidelity before rolling them up?
- **Causal graph consumers**: who reads the causal links and what query shapes do they need? That dictates indexing.
- **Cross-probe severity**: when multiple probe types fire on the same site, should the API rollup use max severity, a weighted sum, or something else?

## Invariants worth testing

1. Event write and legacy status projection update are atomic while `LEGACY_STATUS_PROJECTION_ENABLE` is true.
2. **Every** mutation of a `jetmon_events` row writes exactly one row into `jetmon_event_transitions` in the same transaction. Open, severity change, state change, cause-link change, close — no carve-outs.
3. Replaying the same probe result twice produces the same single event and a single `opened` transition row (idempotent insert path).
4. `Seems Down → Up` (false alarm) correctly closes the event with `resolution_reason = false_alarm` and writes a transition row with `reason = false_alarm`.
5. Severity updates on a live event do not create a new event row, but **do** create a transition row.
6. Closed events are never mutated (except possibly by a backfill/migration, which should be audited).
7. After closing an event for tuple T, a new failure for tuple T can immediately open a new event without conflicting on `dedup_key`.
8. Replaying every transition row for an event in `changed_at` order reconstructs the event's current `severity` and `state`.

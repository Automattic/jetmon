# 0005 — Pull-only webhook and alerting delivery

**Status:** Accepted (2026-04-23)

## Context

When an event transition happens (a site goes Down, recovers,
escalates from Degraded to SeemsDown, etc.), the webhook delivery
worker and the alerting delivery worker each need to fan that
transition out to matching subscribers. There were two viable shapes:

- **In-process pub/sub.** The eventstore notifies subscribers
  in-process via a Go channel; each worker is a subscriber. The
  workers wake on every transition with no polling latency.
- **Pull from `jetmon_event_transitions`.** Workers maintain a
  high-water mark in their own progress table and poll the
  transitions table on a tick (default 1s). Transitions are
  durable; new transitions are picked up on the next poll.

Pub/sub is faster (no polling latency) and avoids a poll loop. Pull
is slower (up to 1s tick latency) but has several properties that
matter at the architectural scale:

- The MySQL schema is the bus. No in-process state has to survive
  a restart — the high-water mark is in the DB. A worker that
  crashes resumes from where it left off.
- Multiple worker instances are trivially supported. Each instance
  has its own row in the progress table and polls independently.
  (Multi-instance does need row-level claim semantics on the
  delivery table; see ADR-0007.)
- Workers don't have to live in the same process as the eventstore
  writer. The deliverer-binary extraction ([`../roadmap.md`](../roadmap.md),
  Architectural roadmap) becomes a clean cut: the worker code moves
  to its own binary, points at the same MySQL, and continues
  working without the eventstore writer being aware.
- "I want to replay deliveries since timestamp T" is a SELECT, not a
  bus replay primitive.

## Decision

We will use **pull-only delivery** for both the webhook worker
(`internal/webhooks`) and the alerting worker (`internal/alerting`).
Both workers:

- Maintain a high-water mark of the last `jetmon_event_transitions.id`
  they processed, in their own per-instance progress table
  (`jetmon_webhook_dispatch_progress`,
  `jetmon_alert_dispatch_progress`).
- Poll on a 1-second tick by default for new transition rows after
  the mark.
- For each new transition, match against active subscribers and
  enqueue per-(subscriber, transition) deliveries.
- Then dispatch with retries on a shared retry ladder
  (1m / 5m / 30m / 1h / 6h, then abandon).

The MySQL schema is the bus between writers (eventstore) and readers
(webhook worker, alerting worker).

## Consequences

**Wins:**
- Crash-safe by design. A worker that dies mid-tick resumes
  correctly when restarted; in-flight deliveries are caught by the
  retry path.
- Multi-instance friendly with a small claim-locking addition
  (ADR-0007). The basic shape doesn't change.
- Each worker can be extracted into its own binary without
  modifying the eventstore. The deliverer-binary roadmap entry
  builds on this.
- Replay and audit are SQL queries.
- Consumers of the events table (audit tooling, ad-hoc reporting,
  the SLA endpoints) see the same source of truth as the workers.

**Costs:**
- 1-second tick latency is acceptable for outage notifications but
  not for sub-second user-interactive flows. Jetmon's notification
  use case tolerates seconds; this would be wrong for, say, a chat
  message delivery system.
- Tight tick + lots of subscribers + lots of transitions = noticeable
  DB query rate. The per-tick SELECT is bounded by `BatchSize` (200
  by default) and uses indexed columns. Watching this at scale and
  tuning the tick is in scope for future operational work.
- The dispatcher and the deliverer are two coupled poll loops in
  one process. The webhook worker poll-and-enqueue tick is separate
  from the poll-pending-deliveries tick. This is documented in
  worker.go but is more complex than a single-loop in-process
  pub/sub would be.

## Alternatives considered

- **In-process pub/sub.** Faster, simpler in single-process
  deployment, but creates an in-process dependency between the
  eventstore writer and the workers, breaks the multi-instance
  story, and complicates the deliverer-binary extraction. The
  latency win does not pay for those costs in our use case.
- **MySQL `LISTEN`/`NOTIFY` (PostgreSQL pattern).** MySQL has no
  equivalent. Ruled out.
- **Outbox-pattern with explicit fan-out at write time.** The
  eventstore writer would compute matching subscribers and write
  per-(subscriber, transition) rows directly. Rejected because
  matching changes when subscribers are added or removed; precomputing
  at write time would mean a configuration change has to wait for
  the next transition before taking effect. Pull-with-match-at-tick
  picks up registry changes immediately.

## Related

- ADR-0001 (Event-sourced state model) — defines the
  `jetmon_event_transitions` table the workers consume.
- ADR-0007 (Soft-lock claim) — the row-level locking that makes
  multi-instance pull safe.
- `internal/webhooks/worker.go`, `internal/alerting/worker.go` — the
  two pull-loop implementations.
- [`../roadmap.md`](../roadmap.md) "Multi-repo / multi-binary split" — the deliverer
  binary that builds on this decision.

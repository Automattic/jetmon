# 0007 — Soft-lock claim vs `SELECT … FOR UPDATE SKIP LOCKED`

**Status:** Accepted (2026-04-25)

## Context

The webhook and alerting deliver loops (per ADR-0005) tick every
1 second. Each tick:

1. SELECTs up to N pending deliveries whose `next_attempt_at` has
   passed.
2. For each, spawns a goroutine to dispatch (subject to a per-
   subscriber in-flight cap).
3. The goroutine eventually calls `MarkDelivered` (success) or
   `ScheduleRetry` (failure) to update the row's `next_attempt_at`.

Two correctness questions arise:

- **Within a single process**, the dispatch goroutine takes seconds
  (HTTP timeout default 30s). If the next tick fires while the
  dispatch is still in flight, the SELECT returns the same row
  again — its status is still `pending` and its `next_attempt_at`
  hasn't been updated. The goroutine hasn't finished yet. The
  per-subscriber in-flight cap (default 3) bounds this, but lets
  up to 3 concurrent dispatches of the same row. Each computes a
  retry delay from the same `d.Attempt = N` value, all run
  `attempt = attempt + 1` in SQL, and the row ends with
  `attempt = N+3`. The retry ladder collapses: we go from 1m to
  abandoned in roughly an hour instead of the documented 7h36m.

- **Across multiple instances**, two jetmon2 processes hitting the
  same MySQL would both see the same pending row in their SELECTs
  and both spawn dispatch goroutines. We'd send each delivery N+1
  times where N is the number of instances.

There are two well-known fixes:

- **Soft lock by pushing `next_attempt_at` out** before the
  goroutine starts. The next tick's SELECT (which gates on
  `next_attempt_at <= NOW()`) won't match the row again until the
  soft lock expires. The dispatch goroutine overwrites the soft
  lock with its real result.
- **Row-level locking via `SELECT … FOR UPDATE SKIP LOCKED`** in a
  transaction. Two concurrent SELECTs against the same row only
  return it to one caller; the other gets a different row or
  nothing.

## Decision

Today we use the **soft lock** in both
`internal/webhooks/deliveries.go` and `internal/alerting/deliveries.go`.
`ClaimReady` follows its SELECT with a per-row UPDATE that pushes
`next_attempt_at` to NOW + `claimLockDuration` (60 seconds). The
dispatch goroutine overwrites with its real value when it finishes.

Multi-instance row-level locking via
`SELECT … FOR UPDATE SKIP LOCKED` is **deferred** until the
deliverer-binary extraction. Today we run a single jetmon2 instance,
so the multi-instance failure mode is hypothetical. The soft lock
solves the real production-shape failure mode (in-process
re-claiming).

A crashed goroutine that never updates the row recovers naturally
when the soft lock expires after 60s — the row becomes claimable
again. This is intentional rollback behavior.

## Consequences

**Wins:**
- The retry ladder behaves as documented in single-instance
  deployments. The visible regression that motivated the fix
  (~1h-then-abandon instead of 7h36m) is gone.
- Single SELECT + per-row UPDATE is straightforward to reason about
  and easy to test (sqlmock contract tests exist for both packages).
- Crash recovery is automatic — a process kill mid-dispatch leaves
  the row recoverable.

**Costs:**
- Multi-instance deployments still have the original failure mode.
  Two instances would still both see a pending row in their SELECTs
  before either runs the soft-lock UPDATE; the row's status hasn't
  changed yet. The losing UPDATE silently overwrites the winning
  UPDATE's `next_attempt_at`. Both instances proceed. The fix is
  to switch to `SELECT … FOR UPDATE SKIP LOCKED` in a transaction,
  which is a real change but is well-contained — only `ClaimReady`
  in each package needs updating.
- The soft lock duration is a tuning parameter. Too short and a
  slow dispatch can race with the next tick; too long and a crashed
  goroutine takes longer to recover. 60s is a comfortable margin
  for the default 30s + 5s dispatch timeout.

## Alternatives considered

- **`SELECT … FOR UPDATE SKIP LOCKED` immediately.** Correct for
  multi-instance, more complex (requires a transaction around the
  claim), and over-engineered for the current single-instance
  deployment. The migration is a small contained change in the two
  `ClaimReady` functions when the deliverer binary extracts.
- **Reduce the per-subscriber in-flight cap to 1.** Doesn't fix
  the bug; the second tick still sees the same row, the cap just
  prevents the second goroutine from starting. The row stays pending
  with stale `next_attempt_at` and the dispatch is delayed by the
  cap rather than re-attempted concurrently. Slightly better
  observable behavior, same underlying issue.
- **A separate "claim ID" column with CAS semantics.** Equivalent
  to the soft lock but with more schema and more code. Same
  correctness; not worth the additional complexity at single-instance
  scale.

## Related

- ADR-0005 (Pull-only delivery) — the worker shape that creates
  this concurrency question.
- ADR-0006 (Separate alerting and webhooks packages) — the fix
  had to land in both packages, illustrating the duplication cost.
- `internal/webhooks/deliveries.go` `ClaimReady` and the matching
  test (`TestClaimReadySoftLocksEachRow`).
- `internal/alerting/deliveries.go` `ClaimReady` and matching test.
- ROADMAP.md "Multi-repo / multi-binary split" — the deliverer
  extraction that reopens this decision.

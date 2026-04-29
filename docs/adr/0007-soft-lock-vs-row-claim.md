# 0007 — Soft-lock claim vs transactional row claim

**Status:** Accepted (2026-04-25), amended (2026-04-28)

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
- **Transactional row claiming via `SELECT … FOR UPDATE`**. Two
  concurrent claim transactions cannot claim the same row; the second
  claimant waits briefly for the first transaction to commit, then sees
  the updated `next_attempt_at` and skips that in-flight delivery.
- **Transactional row claiming via `SELECT … FOR UPDATE SKIP LOCKED`**.
  Same correctness property, but concurrent claimers skip locked rows
  rather than waiting. This is better for high delivery concurrency but
  requires newer MySQL than the current 5.7+ compatibility target.

## Decision

`internal/webhooks/deliveries.go` and `internal/alerting/deliveries.go`
now use a transactional row claim. `ClaimReady` starts a transaction,
selects ready rows with `SELECT … FOR UPDATE`, pushes each selected
row's `next_attempt_at` to NOW + `claimLockDuration` (60 seconds), and
commits. The dispatch goroutine overwrites that in-flight lease with
its real value when it finishes.

We intentionally use plain `FOR UPDATE` rather than `SKIP LOCKED` so
the delivery claim path remains compatible with the MySQL 5.7+
production target. The claim transaction is short: it only scans rows,
updates their in-flight lease, and commits before any outbound network
I/O begins. A competing worker may block briefly during that claim, but
it will not duplicate the delivery.

A crashed goroutine that never updates the row recovers naturally
when the in-flight lease expires after 60s — the row becomes claimable
again. This is intentional rollback behavior.

## Consequences

**Wins:**
- The retry ladder behaves as documented; the visible regression that
  motivated the original soft lock (~1h-then-abandon instead of 7h36m)
  stays fixed.
- Active-active delivery workers no longer duplicate the same pending
  delivery row.
- The implementation remains MySQL 5.7+ compatible.
- Crash recovery is automatic — a process kill mid-dispatch leaves
  the row recoverable.

**Costs:**
- `FOR UPDATE` can make one worker wait briefly behind another worker's
  claim transaction. This is acceptable while the transaction is kept
  short and contains no network I/O.
- `SKIP LOCKED` would use high-concurrency workers more efficiently, but
  it is deferred until the production database compatibility target
  allows it.
- The in-flight lease duration is a tuning parameter. Too short and a
  slow dispatch can race with the next tick; too long and a crashed
  goroutine takes longer to recover. 60s is a comfortable margin
  for the default 30s + 5s dispatch timeout.

## Alternatives considered

- **`SELECT … FOR UPDATE SKIP LOCKED`.** Correct for multi-instance and
  avoids blocking behind already-claimed rows, but would raise the MySQL
  requirement beyond the current compatibility target.
- **Keep the soft lock only.** Simple and MySQL-compatible, but two
  workers can both read the same pending row before either moves
  `next_attempt_at`, so active-active delivery still duplicates work.
- **Reduce the per-subscriber in-flight cap to 1.** Doesn't fix
  the bug; the second tick still sees the same row, the cap just
  prevents the second goroutine from starting. The row stays pending
  with stale `next_attempt_at` and the dispatch is delayed by the
  cap rather than re-attempted concurrently. Slightly better
  observable behavior, same underlying issue.
- **A separate "claim ID" column with CAS semantics.** Similar
  correctness with more schema and more code. Not worth the additional
  complexity when row locks already provide the claim primitive.

## Related

- ADR-0005 (Pull-only delivery) — the worker shape that creates
  this concurrency question.
- ADR-0006 (Separate alerting and webhooks packages) — the fix
  had to land in both packages, illustrating the duplication cost.
- `internal/webhooks/deliveries.go` `ClaimReady` and the matching
  `TestClaimReadyClaimsRowsTransactionally`.
- `internal/alerting/deliveries.go` `ClaimReady` and matching test.
- [`../roadmap.md`](../roadmap.md) post-v2 platform refinement items for the deliverer split
  and active-active delivery.

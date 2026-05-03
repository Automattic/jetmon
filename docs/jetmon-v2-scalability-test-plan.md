# Jetmon v2 Scalability Test Plan

This is the repeatable checklist for validating scheduler and check-path
efficiency changes after the successful 1,000-site capacity run.

## Current Branch Under Test

`feature/jetmon-v2-scalability-efficiency` adds these scaling changes on top of
the completed 1,000-site capacity branch:

- Maintained `next_check_at` timestamps for indexed variable-interval due
  selection.
- One-minute sampling for exact due-count and projection-drift reporting in
  variable-interval mode.
- Shared bounded HTTP transport for local site checks.
- Batched `ssl_expiry_date` writes when observed certificate dates change.

Do not stack larger persistence changes, such as async check-history writes, on
top of this branch before a real capacity run. The next test should isolate
these changes from the previous successful 1,000-site baseline.

## Pre-Test Checks

1. Confirm migrations have run through migration 30.
2. Confirm the test service is running this branch's `jetmon2` binary.
3. Confirm the Veriflier service is reachable from the monitor host.
4. Confirm `WORKER_MAX_MEM_MB=0` for capacity tests unless intentionally
   testing memory-pressure drain.
5. Confirm `USE_VARIABLE_CHECK_INTERVALS=true`.
6. Confirm API-enabled test hosts set `DELIVERY_OWNER_HOST` explicitly.

## Query Plan Checks

Capture `EXPLAIN` for both scheduler modes before the capacity run.

Variable-interval selection should use `idx_monitor_next_check_blog_bucket` and
should not show `Using filesort`:

```sql
EXPLAIN
SELECT jetpack_monitor_site_id, blog_id, bucket_no, monitor_url,
       monitor_active, site_status, last_status_change, check_interval,
       last_checked_at, next_check_at
  FROM jetpack_monitor_sites
 WHERE monitor_active = 1
   AND bucket_no BETWEEN 0 AND 999
   AND (next_check_at IS NULL OR next_check_at <= NOW())
 ORDER BY next_check_at ASC, blog_id ASC
 LIMIT 100;
```

Fixed-cadence selection should continue to use
`idx_monitor_last_checked_blog_bucket` and should not show `Using filesort`:

```sql
EXPLAIN
SELECT jetpack_monitor_site_id, blog_id, bucket_no, monitor_url,
       monitor_active, site_status, last_status_change, check_interval,
       last_checked_at, next_check_at
  FROM jetpack_monitor_sites
 WHERE monitor_active = 1
   AND bucket_no BETWEEN 0 AND 999
 ORDER BY last_checked_at ASC, blog_id ASC
 LIMIT 100;
```

## StatsD Signals To Capture

Freshness and scheduler pressure:

- `scheduler.round.pages.count`
- `scheduler.round.selected.count`
- `scheduler.round.dispatched.count`
- `scheduler.round.completed.count`
- `scheduler.round.outstanding.count`
- `scheduler.round.due_count_sampled.count`
- `scheduler.round.due_start.count`
- `scheduler.round.due_remaining.count`
- `scheduler.round.selected_never_checked.count`
- `scheduler.round.selected_oldest_age_sec`
- `scheduler.dispatch.backpressure_wait.count`
- `scheduler.result.stale.count`
- `scheduler.result.duplicate.count`
- `scheduler.fetch.error.count`
- `scheduler.due_count.error.count`

Phase timing and write volume:

- `scheduler.page.dispatch.time`
- `scheduler.page.wait.time`
- `scheduler.page.process.time`
- `scheduler.page.mark_checked.time`
- `scheduler.page.history.time`
- `scheduler.page.ssl.time`
- `scheduler.page.events.time`
- `scheduler.page.mark_checked.row.count`
- `scheduler.page.history.row.count`
- `scheduler.page.ssl.row.count`
- `scheduler.page.mark_checked.error.count`
- `scheduler.page.history.error.count`
- `scheduler.page.ssl.error.count`
- `scheduler.round.dispatch.time`
- `scheduler.round.wait.time`
- `scheduler.round.process.time`
- `scheduler.round.mark_checked.time`
- `scheduler.round.history.time`
- `scheduler.round.ssl.time`
- `scheduler.round.events.time`
- `scheduler.round.mark_checked.row.count`
- `scheduler.round.history.row.count`
- `scheduler.round.ssl.row.count`
- `scheduler.round.mark_checked.error.count`
- `scheduler.round.history.error.count`
- `scheduler.round.ssl.error.count`

Host/process signals:

- `round.complete.time`
- `round.sites.count`
- `round.sps.count`
- `worker.queue.active`
- `worker.queue.queue_size`
- `retry.queue.size`
- RSS memory
- Go runtime system memory
- process file descriptor count

Dependency signals:

- MySQL CPU, I/O, network, and slow-query counters
- StatsD/Graphite CPU
- Veriflier CPU/RSS/FDs
- monitor host CPU/RSS/FDs

## Expected Interpretation

- `due_count_sampled.count=0` means exact due-count gauges were intentionally
  skipped on that short variable-interval poll. It does not mean no sites were
  due.
- `due_remaining` should only be interpreted on polls where
  `due_count_sampled.count=1`.
- `ssl.row.count` should be high only during initial certificate backfills or
  real renewal waves. Sustained high SSL rows means certificate dates are
  changing or stored with incompatible precision.
- Shared HTTP transport impact should show up in process FD count, monitor CPU,
  DNS/TCP/TLS timing, and check latency rather than in scheduler row counts.
- If MySQL CPU remains high while freshness is good, compare
  `history.row.count`, `history.time`, and table growth before implementing
  async or lower-resolution history storage.

## Next Capacity Ladder

Run each step for the same duration and compare against the latest successful
1,000-site baseline:

1. 1,000 sites after this branch to isolate regressions.
2. 5,000 sites to find the next visible bottleneck.
3. 10,000 sites if freshness, FD count, and MySQL CPU remain healthy.

For each step, preserve:

- uptime-bench report directory
- Prometheus/Graphite window export
- service logs for the exact test window
- `EXPLAIN` output
- row counts for `jetmon_check_history`
- open-event count before and after test-site deactivation

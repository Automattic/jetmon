# Jetmon v2 Scalability Test Plan

This is the repeatable checklist for validating scheduler and check-path
efficiency changes after the successful 1,000-site capacity run.

## Current Branch Under Test

`feature/jetmon-v2-15k-scaling-efficiency` builds on the completed
scalability-efficiency work and targets the May 3, 2026 capacity boundary:
12,500 active sites passed, while 15,000 active sites missed 1,100 checks
inside the 5-minute freshness window. The stale rows were evenly distributed
across all 100 active buckets, and host CPU/RSS stayed low, which pointed at
scheduler pacing rather than resource exhaustion.

The branch includes the previous scaling changes:

- Maintained `next_check_at` timestamps for indexed variable-interval due
  selection.
- One-minute sampling for exact due-count and projection-drift reporting in
  variable-interval mode.
- Shared bounded HTTP transport for local site checks.
- Batched `ssl_expiry_date` writes when observed certificate dates change.

It also adds scheduler batch windows: Jetmon fetches several ordered DB pages
with a keyset cursor, then dispatches that larger batch as one check window.
With the default 60 workers and `DATASET_SIZE=100`, this changes the common
capacity-test shape from roughly 150 independent 100-site tail waits at 15,000
active sites to about nine 1,800-site windows. The 1,800 default comes from the
worker count, request timeout, and 5-minute round target so the batch remains
useful for healthy high-scale checks without creating a long processing delay
during broad timeout scenarios. `last_checked_at` and `next_check_at` are still
written only after completed checks.

The branch now also moves site-state event handling onto a bounded sharded
background queue after `last_checked_at` and `jetmon_check_history` have been
written. This was added after the 22,500-site retry run showed a polluted
preflight overlap but still isolated a scheduler freeze: a 7,200-site selected
batch wrote freshness data quickly, then spent `13m15s` in failure/event
handling after 732 connect errors. The event queue preserves per-site ordering
by hashing each blog ID to one worker, while keeping slow event/projection work
from blocking fresh checks for unrelated sites.

Do not stack larger persistence changes, such as async check-history writes or
DNS caching, on top of this branch before another real capacity run. The next
test should isolate the scheduler-batch, worker-ceiling, and event-queue
changes before adding another variable.

## Pre-Test Checks

1. Confirm migrations have run through migration 30.
2. Confirm the test service is running this branch's `jetmon2` binary.
3. Confirm the Veriflier service is reachable from the monitor host.
4. Confirm `WORKER_MAX_MEM_MB=0` for capacity tests unless intentionally
   testing memory-pressure drain.
5. Confirm `USE_VARIABLE_CHECK_INTERVALS=true`.
6. Confirm API-enabled test hosts set `DELIVERY_OWNER_HOST` explicitly.
7. Confirm the exact activated `monitor_url` pattern resolves and returns HTTP
   200 from the Jetmon service host and the Veriflier host. Do not test a
   similar hostname by hand; query one activated row from
   `jetpack_monitor_sites`, then run `dig` and `curl` for that exact hostname.
   A mismatch between the capacity runner URL pattern and the target DNS
   `generated_sites.host_pattern` will look like a Jetmon false-down storm and
   will make event handling, not checking, dominate the run.

## Query Plan Checks

Capture `EXPLAIN` for both scheduler modes before the capacity run. Also
capture keyset-cursor variants because this branch fetches follow-on DB pages
before the previous batch has updated completed-check timestamps.

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

Variable-interval follow-on pages should still use
`idx_monitor_next_check_blog_bucket`:

```sql
EXPLAIN
SELECT jetpack_monitor_site_id, blog_id, bucket_no, monitor_url,
       monitor_active, site_status, last_status_change, check_interval,
       last_checked_at, next_check_at
  FROM jetpack_monitor_sites
 WHERE monitor_active = 1
   AND bucket_no BETWEEN 0 AND 999
   AND (next_check_at IS NULL OR next_check_at <= NOW())
   AND (next_check_at > '2026-05-03 16:07:00'
        OR (next_check_at = '2026-05-03 16:07:00' AND blog_id > 8000000000009999))
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
- `scheduler.round.batches.count`
- `scheduler.round.batch_target.count`
- `scheduler.round.selected.count`
- `scheduler.round.dispatched.count`
- `scheduler.round.completed.count`
- `scheduler.round.outstanding.count`
- `scheduler.round.pool.workers.max`
- `scheduler.round.pool.active.max`
- `scheduler.round.pool.queue_depth.max`
- `scheduler.round.pool.queue_capacity.max`
- `scheduler.round.event_queue.job.count`
- `scheduler.round.event_queue.depth.max`
- `scheduler.round.event_queue.capacity`
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

Phase timing and write volume. The legacy `scheduler.page.*` metric names now
represent one scheduler check batch, which may contain multiple DB pages:

- `scheduler.page.dispatch.time`
- `scheduler.page.wait.time`
- `scheduler.page.process.time`
- `scheduler.page.mark_checked.time`
- `scheduler.page.history.time`
- `scheduler.page.ssl.time`
- `scheduler.page.events.time`
- `scheduler.page.pool.workers.max`
- `scheduler.page.pool.active.max`
- `scheduler.page.pool.queue_depth.max`
- `scheduler.page.pool.queue_capacity.max`
- `scheduler.page.event_queue.job.count`
- `scheduler.page.event_queue.depth.max`
- `scheduler.page.event_queue.capacity`
- `scheduler.page.mark_checked.row.count`
- `scheduler.page.history.row.count`
- `scheduler.page.ssl.row.count`
- `scheduler.page.mark_checked.error.count`
- `scheduler.page.history.error.count`
- `scheduler.page.ssl.error.count`
- `scheduler.page.check.success.count`
- `scheduler.page.check.failure.count`
- `scheduler.page.check.http_failure.count`
- `scheduler.page.check.timeout.count`
- `scheduler.page.check.connect_error.count`
- `scheduler.page.check.ssl_error.count`
- `scheduler.page.check.redirect.count`
- `scheduler.page.check.keyword.count`
- `scheduler.page.check.tls_deprecated.count`
- `scheduler.round.dispatch.time`
- `scheduler.round.wait.time`
- `scheduler.round.process.time`
- `scheduler.round.mark_checked.time`
- `scheduler.round.history.time`
- `scheduler.round.ssl.time`
- `scheduler.round.events.time`
- `scheduler.event_worker.process.count`
- `scheduler.event_worker.process.time`
- `scheduler.round.mark_checked.row.count`
- `scheduler.round.history.row.count`
- `scheduler.round.ssl.row.count`
- `scheduler.round.mark_checked.error.count`
- `scheduler.round.history.error.count`
- `scheduler.round.ssl.error.count`
- `scheduler.round.check.success.count`
- `scheduler.round.check.failure.count`
- `scheduler.round.check.http_failure.count`
- `scheduler.round.check.timeout.count`
- `scheduler.round.check.connect_error.count`
- `scheduler.round.check.ssl_error.count`
- `scheduler.round.check.redirect.count`
- `scheduler.round.check.keyword.count`
- `scheduler.round.check.tls_deprecated.count`
- `eventstore.mutation.retry.count`

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
- Healthy capacity targets should have `check.success.count` close to
  `completed.count`. A high `check.connect_error.count` means the monitor could
  not connect to the activated URLs; first verify the exact DB URL pattern and
  DNS delegation before treating it as a Jetmon throughput regression.
- `eventstore.mutation.retry.count` should normally be zero. Any non-zero value
  means MySQL returned a deadlock or lock-wait timeout and Jetmon retried the
  event mutation; sustained retries point to event/projection write contention.
- `scheduler.round.events.time` now measures event queueing time on the
  scheduler path, not full event mutation time. If it rises with
  `event_queue.depth.max`, event workers are falling behind and the queue is
  becoming the next bottleneck. Compare with `scheduler.event_worker.*` timings
  and `retry.queue.size`.
- Shared HTTP transport impact should show up in process FD count, monitor CPU,
  DNS/TCP/TLS timing, and check latency rather than in scheduler row counts.
- If MySQL CPU remains high while freshness is good, compare
  `history.row.count`, `history.time`, and table growth before implementing
  async or lower-resolution history storage.
- If `pool.active.max` stays close to `NUM_WORKERS` and CPU / FD usage remains
  low, the worker ceiling is probably the immediate throughput limiter. Run a
  controlled higher-worker batch before designing the adaptive worker ceiling.
- If `pool.queue_depth.max` stays near `pool.queue_capacity.max` and
  `dispatch.time` remains high, the scheduler is spending significant time
  backpressured by the check pool. Higher concurrency, larger queue capacity, or
  streaming result processing are the likely next levers.

## Next Capacity Ladder

Run each step for the same duration and compare against the latest successful
baseline. The 22,500-site retry run was not a clean capacity datapoint because
it overlapped an in-flight scheduler round from a failed preflight attempt, but
it did prove that event/projection work could block freshness. After deploying
the event queue, re-establish a clean point before climbing.

1. 20,000 sites with `NUM_WORKERS=240` and event queue enabled.
2. 22,500 sites if 20,000 keeps a healthy freshness margin.
3. 25,000 sites if 22,500 is clean.
4. Continue in 5k steps while freshness margin remains comfortable; narrow the
   ladder if p95 age approaches the 5-minute freshness window.

For each step, preserve:

- uptime-bench report directory
- Prometheus/Graphite window export
- service logs for the exact test window
- `EXPLAIN` output
- row counts for `jetmon_check_history`
- open-event count before and after test-site deactivation

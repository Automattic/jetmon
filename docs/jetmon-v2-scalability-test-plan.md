# Jetmon v2 Scalability Test Plan

This is the repeatable checklist for validating scheduler and check-path
efficiency changes after the successful 1,000-site capacity run.

## Current Branch Under Test

`feature/jetmon-v2-15k-scaling-efficiency` builds on the completed
scalability-efficiency work and targets the May 3, 2026 capacity boundary.
Successive capacity runs moved the clean point from 20,000 to 75,000 active
sites. The latest run passed 50,000 and 75,000 sites, then failed at 100,000:
Jetmon v2 needed 20,000 recent checks/minute and observed about 15,758/minute.
The 100,000-site stale rows were evenly distributed across buckets, while host
CPU/RSS, MySQL CPU, and file descriptors remained below alert thresholds. The
logs showed the check pool pinned at the 960-worker ceiling with a full queue,
which points at check-pool backpressure rather than database or host saturation.

The branch includes the previous scaling changes:

- Maintained `next_check_at` timestamps for indexed variable-interval due
  selection.
- One-minute sampling for exact due-count and projection-drift reporting in
  variable-interval mode.
- Shared bounded HTTP transport for local site checks.
- Batched `ssl_expiry_date` writes when observed certificate dates change.

It also adds scheduler batch windows: Jetmon fetches several ordered DB pages
with a keyset cursor, then dispatches that larger batch as one check window.
The batch target comes from the current check-pool ceiling, request timeout, and
freshness target. The per-batch result window is capped at 25,000 sites to
avoid unbounded in-process maps and to keep freshness writes staggered across a
large active fleet; the cap does not limit total checks per round.
`last_checked_at` and `next_check_at` are still written only after completed
checks.

The branch now also moves site-state event handling onto a bounded sharded
background queue after `last_checked_at` and `jetmon_check_history` have been
written. This was added after the 22,500-site retry run showed a polluted
preflight overlap but still isolated a scheduler freeze: a 7,200-site selected
batch wrote freshness data quickly, then spent `13m15s` in failure/event
handling after 732 connect errors. The event queue preserves per-site ordering
by hashing each blog ID to one worker, while keeping slow event/projection work
from blocking fresh checks for unrelated sites.

The bounded adaptive retest passed 75,000 sites with margin, then failed at
80,000 sites. That run kept host CPU, memory, MySQL CPU, and file descriptors
below alert thresholds, but scheduler logs still showed full check-pool
utilization, full pending-work queues, and 25,000-result persistence waves.
The next iteration keeps the 25,000-site dispatch window, but processes
completed results in 5,000-site chunks while dispatching and collecting the
window. This should smooth `last_checked_at` writes, `jetmon_check_history`
inserts, event enqueueing, and per-window memory without reducing the worker
pool's ability to keep checks in flight.

The same run also showed failure-heavy synthetic sites opening and recovering
large numbers of events, which drove WPCOM circuit-breaker and queue-full log
storms. WPCOM notifications still use the existing circuit breaker and bounded
queue, but the orchestrator no longer retries immediately when the client has
already queued a notification because the circuit is open. Queue-full logs are
coalesced so failure storms do not dominate log I/O.

The chunked-result retest passed 75,000, 80,000, and 90,000 sites. It also
showed the next bottleneck clearly: synthetic benchmark blog IDs can return
WPCOM 404s forever, and those per-site permanent failures should not poison the
global WPCOM circuit breaker or fill its bounded queue. Jetmon now treats WPCOM
404/410 responses as terminal per-notification failures, skips the immediate
retry for those responses, and leaves the circuit breaker available for real
transport/service failures. Permanent-failure logs are coalesced so synthetic
ID storms do not dominate log I/O. Event workers also have more headroom, and
slow freshness/history write logs now identify DB persistence stalls without
changing the scheduler's checking behavior.

Do not stack DNS caching or async check-history writes on top of this branch
before the next real capacity run. The next test should isolate permanent WPCOM
failure classification, event-worker headroom, and slow-write instrumentation
while preserving the 2x adaptive worker ceiling, 25,000-site dispatch window,
and 5,000-site result-processing chunks.

## Pre-Test Checks

1. Confirm migrations have run through migration 31.
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
- `scheduler.round.process.chunk.count`
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
- `scheduler.page.process.chunk.count`
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
- `scheduler.round.process.chunk.count`
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

For the `last_checked_at` / `next_check_at` write path, confirm the database has
`idx_monitor_blog_id` on `jetpack_monitor_sites(blog_id)`. The scheduler read
indexes are optimized for due-site selection, but they cannot support batched
`UPDATE ... WHERE blog_id IN (...)` efficiently because `blog_id` is not their
leftmost column. A missing `blog_id` index makes freshness writes look like a
service throughput problem even when CPU and event handling are healthy.

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
  due. If a skipped poll still finds a full first DB page, Jetmon samples due
  count for adaptive worker sizing before continuing to fill the scheduler
  batch.
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
- If `pool.active.max` stays close to the adaptive worker ceiling and CPU usage
  remains low, inspect process FD usage and the host `ulimit -n`. The adaptive
  ceiling is intentionally bounded by the file-descriptor budget.
- If `pool.queue_depth.max` stays near `pool.queue_capacity.max` and
  `dispatch.time` remains high, the scheduler is spending significant time
  backpressured by the check pool. If `pool.workers.max` is also below the
  adaptive ceiling, investigate pool scaling. If workers reach the adaptive
  ceiling, the next levers are host FD limits, bucket splitting, or a deeper
  streaming dispatch/result-processing redesign.

## Next Capacity Ladder

Run each step for the same duration and compare against the latest successful
baseline. The latest clean point is 90,000 active sites with 10.85% throughput
margin, `p95` freshness age of 218 seconds, and no stale or missed active sites.
Host CPU, memory, MySQL CPU, and file descriptors stayed below alert thresholds,
but event queue depth rose past 50,000 and some freshness writes spiked late in
the run. After adding permanent WPCOM failure classification, event-worker
headroom, and slow-write instrumentation, use a ceiling-discovery ladder:

1. 100,000 sites to confirm the service clears the original six-figure target.
2. 125,000 sites if 100,000 is clean with enough freshness margin.
3. 150,000 sites if 125,000 is clean and event queue depth does not trend toward
   saturation.
4. Narrow the ladder if `p95` age approaches the 5-minute freshness window, if
   `scheduler.mark_checked.slow.count` or `scheduler.history.slow.count` rises,
   if event queue depth remains high after a batch, or if FD usage climbs toward
   the adaptive worker cap.

For each step, preserve:

- uptime-bench report directory
- Prometheus/Graphite window export
- service logs for the exact test window
- `EXPLAIN` output
- row counts for `jetmon_check_history`
- open-event count before and after test-site deactivation

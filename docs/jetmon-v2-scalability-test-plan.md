# Jetmon v2 Scalability Test Plan

This is the repeatable checklist for validating scheduler and check-path
efficiency changes after the successful 1,000-site capacity run.

## Current Branch Under Test

`feature/jetmon-v2-scalability-efficiency` adds these scaling changes on top of
the completed 1,000-site capacity branch:

- Maintained `jetpack_monitor_site_runtime.next_check_at` timestamps for indexed
  variable-interval due selection without altering the legacy site table.
- One-minute sampling for exact due-count and projection-drift reporting in
  variable-interval mode.
- Shared bounded HTTP transport for local site checks.
- Batched `jetpack_monitor_site_runtime.ssl_expiry_date` writes when observed
  certificate dates change.

Do not stack larger persistence changes, such as async check-history writes, on
top of this branch before a real capacity run. The next test should isolate
these changes from the previous successful 1,000-site baseline.

## Pre-Test Checks

1. Confirm migrations have run through the sidecar runtime/config migrations.
2. Confirm the test service is running this branch's `jetmon2` binary.
3. Confirm the Veriflier service is reachable from the monitor host.
4. Confirm API-enabled test hosts set `DELIVERY_OWNER_HOST` explicitly.
5. Confirm the exact activated `monitor_url` pattern resolves and returns HTTP
   200 from the Jetmon service host and the Veriflier host. Do not test a
   similar hostname by hand; query one activated row from
   `jetpack_monitor_sites`, then run `dig` and `curl` for that exact hostname.
   A mismatch between the capacity runner URL pattern and the target DNS
   `generated_sites.host_pattern` will look like a Jetmon false-down storm and
   will make event handling, not checking, dominate the run.

## Query Plan Checks

Capture `EXPLAIN` for both scheduler modes before the capacity run.

Variable-interval selection should use `jetpack_monitor_site_runtime.idx_next_check` and
should not show `Using filesort`:

```sql
EXPLAIN
SELECT s.jetpack_monitor_site_id, s.blog_id, s.bucket_no, s.monitor_url,
       s.monitor_active, s.site_status, s.last_status_change, s.check_interval,
       r.last_checked_at, r.next_check_at
  FROM jetpack_monitor_sites s
  LEFT JOIN jetpack_monitor_site_runtime r ON r.blog_id = s.blog_id
 WHERE s.monitor_active = 1
   AND s.bucket_no BETWEEN 0 AND 999
   AND (r.next_check_at IS NULL OR r.next_check_at <= NOW())
 ORDER BY r.next_check_at ASC, s.blog_id ASC
 LIMIT 100;
```

Fixed-cadence selection should continue to use
`jetpack_monitor_site_runtime.idx_last_checked` and should not show `Using filesort`:

```sql
EXPLAIN
SELECT s.jetpack_monitor_site_id, s.blog_id, s.bucket_no, s.monitor_url,
       s.monitor_active, s.site_status, s.last_status_change, s.check_interval,
       r.last_checked_at, r.next_check_at
  FROM jetpack_monitor_sites s
  LEFT JOIN jetpack_monitor_site_runtime r ON r.blog_id = s.blog_id
 WHERE s.monitor_active = 1
   AND s.bucket_no BETWEEN 0 AND 999
 ORDER BY r.last_checked_at ASC, s.blog_id ASC
 LIMIT 100;
```

## StatsD Signals To Capture

Freshness and scheduler pressure:

- `scheduler.streaming.targets.count`
- `scheduler.streaming.required_rate.count`
- `scheduler.streaming.selected.count`
- `scheduler.streaming.dispatched.count`
- `scheduler.streaming.completed.count`
- `scheduler.streaming.pending.count`
- `scheduler.streaming.inflight.count`
- `scheduler.streaming.queue_depth.count`
- `scheduler.streaming.result_depth.count`
- `scheduler.streaming.side_effect_queue_depth.count`
- `scheduler.streaming.dispatch_budget_limited.count`
- `scheduler.streaming.backpressure_wait.count`
- `scheduler.streaming.side_effect_backpressure_wait.count`
- `scheduler.streaming.result_backpressure_pause.count`
- `scheduler.streaming.side_effect_backpressure_pause.count`
- `scheduler.streaming.stale_result.count`
- `scheduler.streaming.max_lag.time`

Phase timing and write volume:

- `scheduler.streaming.history.time`
- `scheduler.streaming.ssl.time`
- `scheduler.streaming.events.time`
- `scheduler.streaming.history.row.count`
- `scheduler.streaming.ssl.row.count`
- `scheduler.streaming.history.error.count`
- `scheduler.streaming.ssl.error.count`
- `scheduler.streaming.check.success.count`
- `scheduler.streaming.check.failure.count`
- `scheduler.streaming.check.error.timeout.count`
- `scheduler.streaming.check.error.connect.count`
- `scheduler.streaming.check.error.ssl.count`
- `scheduler.streaming.check.error.redirect.count`
- `scheduler.streaming.check.error.keyword.count`
- `scheduler.streaming.check.error.body_read.count`
- `scheduler.streaming.check.error.tls_expired.count`
- `scheduler.streaming.check.error.tls_deprecated.count`
- `eventstore.mutation.retry.count`

Host/process signals:

- `scheduler.streaming.sps.count`
- `scheduler.streaming.worker.count`
- `scheduler.streaming.worker_target.count`
- `scheduler.streaming.inflight.count`
- `scheduler.streaming.queue_depth.count`
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

- `scheduler.streaming.max_lag.time` shows freshness pressure. It should stay
  comfortably below the check interval for the tested cohort.
- `scheduler.streaming.ssl.row.count` should be high only during initial
  certificate backfills or real renewal waves. Sustained high SSL rows means
  certificate dates are changing or stored with incompatible precision.
- Healthy capacity targets should have `scheduler.streaming.check.success.count`
  close to `scheduler.streaming.completed.count`. A high
  `scheduler.streaming.check.error.connect.count` means the monitor could not
  connect to the activated URLs; first verify the exact DB URL pattern and DNS
  delegation before treating it as a Jetmon throughput regression.
- `eventstore.mutation.retry.count` should normally be zero. Any non-zero value
  means MySQL returned a deadlock or lock-wait timeout and Jetmon retried the
  event mutation; sustained retries point to event/projection write contention.
- Shared HTTP transport impact should show up in process FD count, monitor CPU,
  DNS/TCP/TLS timing, and check latency rather than in scheduler row counts.
- If MySQL CPU remains high while freshness is good, compare
  `history.row.count`, `history.time`, and table growth before implementing
  async or lower-resolution history storage.

## Body-Read Budget Change Verification Thresholds

Use this section only when the candidate differs from baseline by changing
`BODY_READ_MAX_BYTES` from `262144` to `1048576`.

- Body-read failure rate (`error_code=8` with HTTP `2xx`/`3xx`) must be less
  than or equal to `max(0.30%, baseline * 0.75)` and must not exceed baseline
  by more than `0.05` percentage points.
- Timeout pressure primary check requires candidate ratio less than or equal to
  baseline ratio * `1.15`, where ratio is
  `scheduler.streaming.check.error.timeout.count /
  scheduler.streaming.check.failure.count`.
  If either baseline or candidate failure count is too small for a stable ratio
  (for example `<100` failures in the window), use fallback absolute timeout
  rate `scheduler.streaming.check.error.timeout.count /
  scheduler.streaming.completed.count`, and require candidate not worse than
  baseline by more than `0.05` percentage points.
- Throughput must hold with `scheduler.streaming.sps.count` p50 at least `90%`
  of baseline and p95 at least `85%` of baseline.
- Backpressure/freshness must hold with `scheduler.streaming.queue_depth.count`
  p95 less than or equal to `1.25x` baseline, and
  `scheduler.streaming.max_lag.time` must remain comfortably below the tested
  check interval.
- Memory must hold with jetmon2 RSS p95 less than or equal to `1.20x`
  baseline, with no monotonic leak trend across the window.

Run baseline and candidate windows with the same duration, the same site mix,
and only this config delta.

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
- row counts for `jetpack_monitor_check_history`
- open-event count before and after test-site deactivation

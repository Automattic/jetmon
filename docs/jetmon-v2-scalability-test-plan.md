# Jetmon v2 Scalability Test Plan

Use this checklist when validating scheduler, checker, Veriflier, database, or
runtime-config changes that could affect throughput, freshness, CPU, memory, or
I/O. The goal is not to chase a single benchmark score; it is to prove that the
fleet can keep checks inside the configured interval without hiding failures or
creating resource cliffs.

Keep raw reports in the sibling `uptime-bench` repo. This document describes
what Jetmon needs from those runs.

## Pre-Test Checks

Before a capacity run:

1. Confirm the test fleet is running the intended Jetmon commit.
2. Confirm migrations and schema validation pass for the test database.
3. Confirm Monitor, Veriflier, StatsD/Graphite, and database hosts are mapped in
   the report metadata.
4. Confirm API-enabled test hosts set `DELIVERY_OWNER_HOST` explicitly.
5. Confirm the exact activated `monitor_url` pattern resolves and returns the
   expected HTTP status from both Monitor and Veriflier hosts.
6. Confirm WPCOM notifications and real alert delivery are disabled unless the
   test is explicitly about those paths.

Do not hand-check a similar hostname. Query an activated row from
`jetpack_monitor_sites`, then run DNS and HTTP checks for that exact hostname.
If the capacity runner URL pattern and target DNS pattern disagree, Jetmon will
look like it is failing sites when the target fleet is misconfigured.

## Database Plan Checks

Capture `EXPLAIN` before runs that change scheduling, cadence, or runtime-table
queries. Due-target selection must use indexed runtime state and must not require
a filesort across the full active site set.

Due selection should use `jetpack_monitor_site_runtime.idx_next_check`:

```sql
EXPLAIN
SELECT s.jetpack_monitor_site_id, s.blog_id, s.bucket_no, s.monitor_url,
       s.monitor_active, s.site_status, s.last_status_change, s.check_interval,
       r.last_checked_at, r.next_check_at
  FROM jetpack_monitor_sites s
  LEFT JOIN jetpack_monitor_site_runtime r ON r.source_site_id = s.jetpack_monitor_site_id
 WHERE s.monitor_active = 1
   AND s.bucket_no BETWEEN 0 AND 999
   AND (r.next_check_at IS NULL OR r.next_check_at <= NOW())
 ORDER BY r.next_check_at ASC, s.jetpack_monitor_site_id ASC
 LIMIT 100;
```

If a test intentionally changes the scheduler query, include the old and new
plans in the report.

## Signals To Capture

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
- `scheduler.streaming.result_backpressure_pause.count`
- `scheduler.streaming.side_effect_backpressure_pause.count`
- `scheduler.streaming.stale_result.count`
- `scheduler.streaming.max_lag.time`

Check and side-effect timing:

- `scheduler.streaming.history.time`
- `scheduler.streaming.ssl.time`
- `scheduler.streaming.events.time`
- `scheduler.streaming.history.row.count`
- `scheduler.streaming.ssl.row.count`
- `scheduler.streaming.history.error.count`
- `scheduler.streaming.ssl.error.count`
- `scheduler.streaming.check.success.count`
- `scheduler.streaming.check.failure.count`
- `scheduler.streaming.check.error.*`
- `eventstore.mutation.retry.count`

Host and dependency signals:

- Monitor CPU, RSS, Go memory, file descriptors, network I/O, and disk I/O
- Veriflier CPU, RSS, file descriptors, queue pressure, and HTTP 503 count
- MySQL CPU, I/O, network, slow queries, lock waits, and deadlocks
- StatsD/Graphite CPU, disk I/O, and dropped/queued metric indicators

## Interpretation

- `scheduler.streaming.max_lag.time` is the primary freshness signal. It should
  remain comfortably below the cohort check interval.
- `required_rate`, `selected`, `dispatched`, and `completed` show whether Jetmon
  is using available capacity or falling behind before checks even run.
- `queue_depth`, `result_depth`, side-effect depth, and backpressure counters
  identify the bottleneck stage.
- `check.success.count` should be close to `completed.count` for healthy target
  fleets. A high connect or DNS error rate usually means the fixture or network
  needs to be checked before blaming Jetmon throughput.
- `eventstore.mutation.retry.count` should normally be zero. Sustained retries
  point to event/projection write contention.
- `ssl.row.count` should spike only during first observation, certificate
  changes, or fixture churn.
- If freshness is healthy but MySQL CPU or I/O is high, inspect history rows,
  runtime writes, and table growth before changing concurrency.

## Capacity Ladder

Use short bracketing runs to find the next ceiling, then longer soaks at the
highest clean tier.

Suggested progression:

1. Smoke: 5k or 10k active sites, long enough to verify all code paths.
2. Baseline: last-known-good tier from the previous report.
3. Bracket: increase by large steps until freshness or failure rate breaks.
4. Soak: run the highest clean tier long enough to prove steady-state behavior.
5. Regression: rerun the previous baseline after large scheduler or database
   changes.

For each step, preserve:

- uptime-bench report directory
- Jetmon and uptime-bench commits
- host topology and resource sizes
- service logs for the exact test window
- StatsD/Graphite or Prometheus window exports
- `EXPLAIN` output when query behavior is relevant
- row counts for `jetpack_monitor_check_history`
- open-event count before and after test-site deactivation

## Focused Config Experiments

When testing a single config delta, keep the site mix, duration, resource sizes,
and branch constant. Only the target variable should change.

For a body-read budget increase, compare candidate against baseline:

- body-read failure rate should improve or remain within `0.05` percentage
  points of baseline
- timeout pressure should not rise by more than `15%` unless the absolute rate
  remains negligible
- `scheduler.streaming.sps.count` p50 should stay at least `90%` of baseline
  and p95 at least `85%`
- queue-depth p95 should stay within `1.25x` baseline
- RSS p95 should stay within `1.20x` baseline with no monotonic leak trend

Use the same pattern for future experiments: define the expected benefit, the
allowed regression budget, and the metrics that would disprove the change.

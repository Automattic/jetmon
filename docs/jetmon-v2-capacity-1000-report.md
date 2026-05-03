# Jetmon v2 1,000-Site Capacity Report

Date: 2026-05-03 UTC

Purpose: durable benchmark evidence for the scheduler, database-write, index,
and capacity-test configuration changes in the Jetmon v2 capacity branch.

## Executive Finding

The latest 1,000-site capacity run fixed the core missed-check failure seen in
the previous 1,000-site run. Jetmon v2 moved from 74.70% missed checks to 0.00%
missed checks while using much less host and MySQL CPU. The `jetmon2` process
itself used more CPU than before, but still only about 4% of one core at p95, so
the service process is not the bottleneck at 1,000 active sites.

The likely improvement is that work moved out of an expensive DB-bound failure
mode and into the Go scheduler/check loop. That is the right tradeoff: the
service did the checks, every active benchmark site stayed fresh, and cleanup
returned the benchmark range to zero active rows.

## Compared Runs

Both runs used:

- 1,000 active benchmark-owned Jetmon v2 sites.
- 30-minute measurement windows.
- `jetmon-service-host-2` as the Jetmon v2 service host.
- `steadycadence.party` generated target names.
- 1-minute configured check interval.

Artifacts:

| Run | Window | Main artifact |
|---|---|---|
| Previous failed run | `2026-05-02T23:17:13Z` to `2026-05-02T23:47:13Z` | `/home/gaarai/code/uptime-bench-capacity-bench/reports/capacity/v2-fix-1000-20260502-231644Z/v2-fix-1000-20260502-231644Z/run.json` |
| Previous corrected metrics | same window | `/home/gaarai/code/uptime-bench-capacity-bench/reports/capacity/v2-fix-1000-20260502-231644Z/prometheus-window.corrected.json` |
| Latest successful run | `2026-05-03T00:40:11Z` to `2026-05-03T01:10:11Z` | `jetmon-vm-host-3:/home/jetmon/uptime-bench-capacity/reports/run-1000-20260503-003939Z/run.json` |

The previous failed run's original capacity runner hit a Prometheus query error
after DB verification, so the comparison uses the corrected Prometheus capture
artifact for that same window.

## Success And Freshness

| Metric | Previous failed run | Latest successful run | Change |
|---|---:|---:|---:|
| Active sites at window end | 1,000 | 1,000 | same |
| Fresh active sites | 253 | 1,000 | +747 |
| Stale active sites | 747 | 0 | -747 |
| Missed check percent | 74.70% | 0.00% | -74.70 points |
| Freshness success rate | 25.30% | 100.00% | +74.70 points |
| Recent check history rows, last 5m | 300 | 4,440 | 14.8x |
| Recent checks/minute proxy | 60/min | 888/min | 14.8x |
| p50 check age | not captured | 43s | new metric |
| p95 check age | not captured | 62s | new metric |
| p99 check age | not captured | 62s | new metric |
| Oldest active check age | not captured | 62s | new metric |
| Stale scheduler buckets | not captured | 0 of 100 | new metric |

The latest run had one open event at window end, but post-deactivate verify
showed zero open events. The open event did not correlate with stale benchmark
rows or missed freshness. It is worth inspecting separately if event accuracy is
part of the next pass, but it was not the capacity failure mode.

## CPU Comparison

Percent-core values are percentages of one CPU core. Host CPU is total busy CPU
for `jetmon-service-host-2`.

| Metric | Previous failed avg / p95 / max | Latest avg / p95 / max | Interpretation |
|---|---:|---:|---|
| Host CPU busy | `25.91 / 29.52 / 30.94%` | `12.71 / 15.00 / 17.29%` | Host CPU roughly halved while correctness recovered. |
| `jetmon2` process CPU | `0.42 / 0.53 / 0.61% core` | `3.20 / 3.98 / 4.42% core` | App CPU increased, but remains tiny. This is healthy if it reflects actual checking work. |
| v2 MySQL CPU, counter-rate | `93.80 / 108.50 / 114.06% core` | `25.58 / 33.54 / 38.99% core` | Major DB CPU drop. This is the most important resource improvement. |
| v2 MySQL CPU, instantaneous dockerstats gauge | `97.49 / 108.19 / 222.45% core` | `27.45 / 99.14 / 103.99% core` | Spiky gauge still shows bursts, but average and max are much lower. |

The CPU profile changed from DB-heavy and stale to light app CPU plus much lower
DB CPU. At 1,000 sites, Jetmon v2 has substantial host CPU headroom. The next
scaling risk is more likely DB work per checked site than Go process CPU.

## Memory, FDs, And Threads

| Metric | Previous failed avg / p95 / max | Latest avg / p95 / max | Interpretation |
|---|---:|---:|---|
| Host memory used | `14.05 / 14.16 / 14.20%` | `14.35 / 14.43 / 14.46%` | Essentially flat. |
| `jetmon2` RSS | `15.33 / 16.51 / 16.53 MiB` | `18.31 / 20.11 / 20.75 MiB` | Small increase, still very low. |
| v2 MySQL working set | `576.88 / 576.89 / 579.89 MiB` | `603.70 / 605.19 / 605.30 MiB` | DB memory increased modestly while throughput improved. |
| `jetmon2` open FDs | `23.0 / 23 / 30` | `26.5 / 60 / 89` | Higher bursts, still low. Continue watching at larger batch sizes. |
| `jetmon2` threads | `10.0 / 10 / 10` | `10.8 / 11 / 11` | Stable. |

The latest FD profile is safe at 1,000 sites, but the earlier failed suite on
`2026-05-02T16:47Z` saw `jetmon2` open FDs average about 1,049 and max 1,391.
That earlier pattern did not reproduce in the latest run. Keep FD metrics in the
next larger runs to catch any regression.

## DB Network

The latest run moved more data through MySQL while using much less MySQL CPU.

| Metric | Previous failed avg / p95 / max | Latest avg / p95 / max |
|---|---:|---:|
| v2 MySQL RX | `4.35 / 4.49 / 4.51 KiB/s` | `7.66 / 8.48 / 8.53 KiB/s` |
| v2 MySQL TX | `12.15 / 12.39 / 12.50 KiB/s` | `19.13 / 20.84 / 20.97 KiB/s` |

This supports the interpretation that the latest service is doing real check
work and DB writes instead of stalling. More DB traffic with much lower DB CPU
suggests the fixed path is significantly more efficient per useful check.

## Earlier Failed-Suite Context

There was another 1,000-site failure in the earlier growth suite:

- Window: `2026-05-02T16:47:02Z` to `2026-05-02T17:17:02Z`.
- Missed check percent: 95.20%.
- Fresh active sites: 48 of 1,000.
- Recent check history rows in last 5m: 100.
- Host CPU max: 26.54%.
- v2 MySQL CPU counter-rate avg / p95 / max: `32.20 / 91.29 / 94.20% core`.
- `jetmon2` CPU avg / p95 / max: `0.49 / 0.66 / 0.70% core`.
- `jetmon2` RSS avg / p95 / max: `40.41 / 46.70 / 48.15 MiB`.
- `jetmon2` open FDs avg / p95 / max: `1,049 / 1,291 / 1,391`.

That older failure looked different from the immediate previous failed run: high
FD count and low app CPU, with most active sites stale. The latest successful
run avoids both the stale-site failure and the high-FD pattern.

## What To Improve Next

The 1,000-site fix is successful. The next work should preserve this shape as
the active count grows:

1. Run the same 30-minute test at 5,000 active sites before changing code again.
   At 1,000 sites the measured bottleneck is no longer correctness, and the CPU
   profile has enough headroom to justify the next batch.
2. Add or expose per-round scheduler metrics in Jetmon v2:
   - due rows selected per round;
   - DB fetch duration and rows returned;
   - checks dispatched, completed, failed, and skipped;
   - check queue depth and worker utilization;
   - DB write duration for site updates and check history inserts;
   - round duration and idle time;
   - stale/due backlog by bucket.
3. Keep watching DB CPU before app CPU. `jetmon2` p95 CPU is only about 4% of a
   core, but MySQL p95 counter-rate is already about 34% of a core at 1,000
   active sites. If DB CPU scales linearly, it will become limiting before the Go
   process does.
4. Validate query plans for the due-site fetch and update paths at 5,000 and
   10,000 active sites. Any query scanning the 1,000,000-row benchmark range
   every round will dominate larger batches.
5. Continue tracking FDs. Latest max was 89, which is fine, but the earlier
   failed suite reached 1,391. If the FD count climbs with active count, inspect
   HTTP transport reuse, response body close paths, MySQL connection pool
   settings, and Veriflier client connection reuse.
6. Include Veriflier host metrics in future capacity windows. The latest
   capacity runner captured service-host metrics only, while the previous
   corrected capture also included `jetmon-vm-host-2`. The previous Veriflier CPU
   was negligible, but the next larger runs should prove that remains true.
7. Inspect the one window-end open event from the latest successful run. It did
   not indicate missed checks, but it may matter for event correctness or false
   positive analysis.

## Bottom Line

Do not optimize `jetmon2` CPU first. The latest run proves `jetmon2` can spend a
few percent of a core and keep 1,000 sites fresh. The prior failure was not app
CPU exhaustion. Focus next on DB efficiency, scheduler observability, query
plans, and ensuring the low-FD/low-stale profile survives the 5,000-site and
10,000-site tests.

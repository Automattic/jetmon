# Jetmon v2 Capacity Handoff

This handoff is for an agent focused on Jetmon v2. It summarizes the
capacity-run evidence from uptime-bench and points to the Jetmon v2 code paths
most likely to explain the missed checks.

## Executive Finding

Jetmon v2 did not fail the 1,000-site capacity batch because the host ran out
of CPU, memory, disk, or scrape visibility. It failed because the deployed
Jetmon v2 scheduler is configured to check only 100 sites per round while the
round cadence is 5 minutes.

At 1,000 active benchmark sites, Jetmon v2 produced only 100 recent check
history rows in the final 5-minute freshness window, leaving 952 active sites
stale. That aligns with the live `DATASET_SIZE=100` cap rather than resource
exhaustion.

The first remediation pass changes `DATASET_SIZE` from a total per-round cap
into a database fetch page size. The orchestrator now keeps fetching pages until
due work is drained, waits under worker-pool backpressure instead of dropping
checks, and emits scheduler metrics that show due, selected, dispatched,
completed, outstanding, and remaining work.

The next thing to validate is a targeted 1,000-site retest with the existing
100-site page size. That proves the scheduler fix directly, instead of hiding
the previous failure behind a larger static batch.

Important nuance: the worker pool still has bounded channels. That is desirable
as a memory guardrail, but it must act as backpressure rather than as permission
to skip sites. Capacity retests should watch the new backpressure and
outstanding metrics so the team can distinguish healthy throttling from real
freshness pressure.

## Run Under Review

- Run ID: `big-suite-20260502-153359Z`
- Run date: 2026-05-02
- Runner host: `jetmon-service-host-2`
- Prometheus: `http://10.0.0.67:9091`
- Grafana: `http://10.0.0.67:3001`
- Local artifacts:
  - `reports/capacity/big-suite-20260502/big-suite-20260502-153359Z/`
  - `reports/capacity/big-suite-20260502/big-suite-20260502-153359Z-final-deactivate/`
  - `reports/capacity/big-suite-20260502/big-suite-20260502-153359Z-final-verify/`
- Remote artifacts:
  - `/tmp/jetmon-capacity-smoke/reports/big-suite-20260502-153359Z`
  - `/tmp/jetmon-capacity-smoke/reports/big-suite-20260502-153359Z-final-deactivate`
  - `/tmp/jetmon-capacity-smoke/reports/big-suite-20260502-153359Z-final-verify`

The suite stopped after the 1,000-site batch because the v2 missed-check
threshold tripped. The wrapper then ran final deactivate and final verify
successfully.

Final cleanup state:

- Jetmon v1 active benchmark sites: `0`
- Jetmon v2 active benchmark sites: `0`
- Final deactivate: success
- Final verify: success

## Environment

Service hosts:

- `jetmon-service-host-1`: Jetmon v1
- `jetmon-service-host-2`: Jetmon v2

Verifier hosts:

- `jetmon-vm-host-1`: VM-hosted v1 verifier on `10.0.0.172:7801`
- `jetmon-vm-host-2`: VM-hosted v2 verifier on `10.0.0.173:7803`

Monitoring host:

- `jetmon-vm-host-3`: Prometheus/Grafana on `10.0.0.67`

Jetmon v2 source context:

- Local source repo: `/home/gaarai/code/jetmon`
- Branch used for investigation: `integration/uptime-bench-v2-fixes`
- Local source commit observed: `c0693f7332b790577a23738e55319b994f84e115`
- Deployed install path: `/opt/jetmon2`
- Deployed install is not a Git checkout.
- Deployed binary checksum: `004d7849148e51f39e5533f735578755213caa00a1d8b36a4b7f9c0c3ed5ae3c`

## Batch Results

| Active sites | Window | v2 missed-check % | v2 recent check rows | v2 stale active sites | Result |
|---:|---|---:|---:|---:|---|
| 10 | `2026-05-02T15:34:29Z` to `2026-05-02T16:04:29Z` | `0.00` | `10` | `0` | Pass |
| 100 | `2026-05-02T16:10:44Z` to `2026-05-02T16:40:44Z` | `0.00` | `100` | `0` | Pass |
| 1,000 | `2026-05-02T16:47:02Z` to `2026-05-02T17:17:02Z` | `95.20` | `100` | `952` | Stop |

The key signal is that v2 stayed at 100 recent check-history rows when active
sites rose to 1,000.

Relevant artifacts:

- `reports/capacity/big-suite-20260502/big-suite-20260502-153359Z/batch-0000010/summary.txt`
- `reports/capacity/big-suite-20260502/big-suite-20260502-153359Z/batch-0000100/summary.txt`
- `reports/capacity/big-suite-20260502/big-suite-20260502-153359Z/batch-0001000/summary.txt`
- `reports/capacity/big-suite-20260502/big-suite-20260502-153359Z/batch-0001000/run.json`

## Resource Evidence

The 1,000-site batch did not approach host resource thresholds:

| Metric | Jetmon v2 value at 1,000 | Threshold |
|---|---:|---:|
| Host CPU max | `26.54%` | `85%` |
| Host memory max | `12.42%` | `85%` |
| Root disk max | `9.24%` | `90%` |
| Prometheus scrape health | `1.00` | `1.00` |
| dockerstats scrape success | `1.00` | `1.00` |

Process-level v2 trends:

| Active sites | jetmon2 CPU p95 | jetmon2 RSS max | jetmon2 open FDs max |
|---:|---:|---:|---:|
| 10 | `0.52` percent-core | `14.72 MB` | `93` |
| 100 | `0.65` percent-core | `36.27 MB` | `692` |
| 1,000 | `0.66` percent-core | `48.15 MB` | `1391` |

The low CPU is important: v2 was not compute-bound. The open FD growth is a
separate watch item and should be fixed before pushing much higher.

## Live Config That Explains The Failure

The live Jetmon v2 config on `jetmon-service-host-2` included:

```text
DATASET_SIZE=100
NUM_WORKERS=60
MIN_TIME_BETWEEN_ROUNDS_SEC=300
NET_COMMS_TIMEOUT=10
USE_VARIABLE_CHECK_INTERVALS=false
BUCKET_TOTAL=1000
BUCKET_TARGET=500
```

Capacity math:

- The benchmark rows use `check_interval=1m`.
- The capacity verifier treats a site as fresh if `last_checked_at` is within
  the last 5 minutes.
- To keep 1,000 active sites fresh under that 5-minute window, v2 must complete
  about 200 checks/minute.
- The deployed config effectively caps selection at 100 checks per 5-minute
  round, or about 20 checks/minute.
- The observed `100` recent history rows at 1,000 sites match that cap.

## Code Paths To Inspect First

In the Jetmon repo (`/home/gaarai/code/jetmon`):

1. Site selection is capped by `LIMIT ?` in:
   - `internal/db/queries.go`
   - Function: `GetSitesForBucket`
   - It orders by `last_checked_at` and limits by the caller-provided batch
     size.

2. The orchestrator passes `cfg.DatasetSize` directly into that query:
   - `internal/orchestrator/orchestrator.go`
   - Around the `dbGetSitesForBucket(..., cfg.DatasetSize, ...)` call.

3. Defaults are low for capacity:
   - `internal/config/config.go`
   - `DatasetSize: 100`
   - `MinTimeBetweenRoundsSec: 300`

4. The HTTP checker creates a fresh `http.Transport` and `http.Client` per
   check:
   - `internal/checker/checker.go`
   - This likely contributes to the rising open FD count.

## Likely Root Cause

The scheduler appears to be doing exactly what it was configured to do:

1. Select up to `DATASET_SIZE` active sites.
2. Check that slice.
3. Wait until the next round, bounded by `MIN_TIME_BETWEEN_ROUNDS_SEC`.

With `DATASET_SIZE=100` and `MIN_TIME_BETWEEN_ROUNDS_SEC=300`, 1,000 active
sites require roughly 10 rounds, or about 50 minutes, before every site gets
checked once. The benchmark's 5-minute freshness window therefore fails
predictably.

This is not currently evidence that the verifier hosts are overloaded. The
benchmark targets were healthy, so verifiers should not be heavily involved in
the successful-check path.

## Recommended Next Steps

### 1. Targeted Scheduler Retest

Run a targeted 1,000-site retest before continuing the larger growth suite. The
first retest should keep `DATASET_SIZE=100` so it proves that the scheduler
pages through due work instead of relying on a larger static cap.

Candidate config:

```json
{
  "DATASET_SIZE": 100,
  "MIN_TIME_BETWEEN_ROUNDS_SEC": 300,
  "NUM_WORKERS": 60
}
```

Alternative stress config after the scheduler fix is proven:

```json
{
  "DATASET_SIZE": 2000,
  "MIN_TIME_BETWEEN_ROUNDS_SEC": 15,
  "NUM_WORKERS": 200
}
```

Success criteria:

- v2 `missed_check_percent <= 1`
- v2 recent check-history rows close to active site count
- host CPU and memory still below thresholds
- process open FDs do not grow without bound
- `scheduler.round.due_remaining.count == 0`
- `scheduler.round.outstanding.count == 0`
- `scheduler.dispatch.backpressure_wait.count` may be non-zero, but should not
  correspond with growing due-remaining or stale-row counts

### 2. Scheduler Fix

Implemented in the first remediation pass:

- Continuously drain due sites instead of sleeping a fixed round interval.
- If `USE_VARIABLE_CHECK_INTERVALS` is enabled, select only due rows but keep
  looping until due work is drained or worker/backpressure limits are reached.
- Make `DATASET_SIZE` a safety ceiling, not the main throughput governor.
- Treat a full worker queue as backpressure, not as a dropped-check condition.

Still useful future refinement:

- Compute suggested worker and page-size values from active site count, check
  interval, and target freshness so validate-config can explain unsafe
  deployment sizing before a capacity run starts.

### 3. Add Scheduler Metrics

Metrics and logs added by the first remediation pass:

- due sites at pass start: `scheduler.round.due_start.count`
- selected sites: `scheduler.round.selected.count`
- dispatched checks: `scheduler.round.dispatched.count`
- completed checks: `scheduler.round.completed.count`
- outstanding checks at deadline: `scheduler.round.outstanding.count`
- due sites left after the pass: `scheduler.round.due_remaining.count`
- scheduler pages fetched: `scheduler.round.pages.count`
- never-checked selected sites: `scheduler.round.selected_never_checked.count`
- oldest selected checked-row age: `scheduler.round.selected_oldest_age_sec`
- worker queue backpressure waits:
  `scheduler.dispatch.backpressure_wait.count`
- ignored stale/duplicate results:
  `scheduler.result.stale.count` and `scheduler.result.duplicate.count`
- worker queue depth and active worker count:
  `worker.queue.queue_size` and `worker.queue.active`

These should be available in Prometheus/Grafana, not just logs.

These metrics should make the next failure self-explanatory:

- selected lower than due-start means the scheduler/query path is still capping
  work.
- backpressure waits with zero due-remaining means healthy throttling.
- growing due-remaining means capacity pressure or another scheduler bug.
- completed lower than dispatched means checker timeout or result processing
  pressure.

### 4. Fix HTTP Transport / FD Growth

The checker creates a new transport/client per check. Replace that with a
reused transport or bounded client pool with explicit timeouts and idle
connection limits.

Things to evaluate:

- shared `http.Transport`
- `MaxIdleConns`
- `MaxIdleConnsPerHost`
- `IdleConnTimeout`
- `DisableKeepAlives` for benchmark parity if connection reuse is not desired
- explicit `CloseIdleConnections` on shutdown

The observed FD trend was not the stop reason, but it is likely to become a
real limiter at higher monitor counts.

### 5. Improve The Capacity Verifier

The current uptime-bench DB verifier measures v2 freshness via
`last_checked_at` and `jetmon_check_history`, but v1 freshness is not measured.
For v2 work, consider adding richer per-service diagnostics to the capacity
runner or to Jetmon v2:

- freshest and oldest `last_checked_at`
- percentile lag from `last_checked_at`
- rows checked per minute over the window
- count by bucket of stale active rows
- check-history insert errors, if any

## Questions For The Jetmon v2 Agent

- Answered in the first remediation pass: `DATASET_SIZE` is now treated as a
  scheduler fetch page size. It is not a total per-round work cap.
- Answered in the first remediation pass:
  `MIN_TIME_BETWEEN_ROUNDS_SEC=300` remains the fixed-cadence full-fleet pass
  interval when variable intervals are disabled. When
  `USE_VARIABLE_CHECK_INTERVALS=true`, the scheduler uses a short idle poll and
  the SQL due predicate controls what gets checked.
- Recommended for capacity tests: use `USE_VARIABLE_CHECK_INTERVALS=true` when
  the test data sets 1-minute `check_interval` values and the goal is freshness
  against those per-site intervals.
- Should the scheduler support multiple worker hosts sharing buckets before
  single-host capacity is expected to go past 1,000?
- Are `BUCKET_TOTAL=1000` and `BUCKET_TARGET=500` correct for this single-host
  v2 test? The service claimed buckets `0-499` in logs, but the benchmark rows
  were distributed across buckets `0-99`, so the test rows were in-range.

## Retest Command Shape

The completed suite was run from `jetmon-service-host-2` because local SSH
forwarding to the v2 DB was blocked. The remote smoke workspace remains at:

```text
/tmp/jetmon-capacity-smoke
```

The wrapper used for the big suite left DB secret files under:

```text
/tmp/jetmon-capacity-smoke/secrets/v1.dsn
/tmp/jetmon-capacity-smoke/secrets/v2.dsn
```

Those files are `0600`.

After making v2 changes, run a targeted batch before restarting the full
growth suite:

```sh
cd /tmp/jetmon-capacity-smoke
JETMON_V1_DB_DSN="$(< secrets/v1.dsn)" \
JETMON_V2_DB_DSN="$(< secrets/v2.dsn)" \
bin/uptime-bench-jetmon-capacity-run \
  -config=configs/capacity/jetmon.example.toml \
  -mode=run-batch \
  -active-count=1000 \
  -duration=30m \
  -apply \
  -out-dir=reports/v2-scheduler-fix-1000-$(date -u +%Y%m%d-%H%M%SZ)
```

Then run final cleanup regardless of outcome:

```sh
cd /tmp/jetmon-capacity-smoke
JETMON_V1_DB_DSN="$(< secrets/v1.dsn)" \
JETMON_V2_DB_DSN="$(< secrets/v2.dsn)" \
bin/uptime-bench-jetmon-capacity-run \
  -config=configs/capacity/jetmon.example.toml \
  -mode=deactivate \
  -apply \
  -out-dir=reports/v2-scheduler-fix-final-deactivate-$(date -u +%Y%m%d-%H%M%SZ)

JETMON_V1_DB_DSN="$(< secrets/v1.dsn)" \
JETMON_V2_DB_DSN="$(< secrets/v2.dsn)" \
bin/uptime-bench-jetmon-capacity-run \
  -config=configs/capacity/jetmon.example.toml \
  -mode=verify \
  -apply \
  -out-dir=reports/v2-scheduler-fix-final-verify-$(date -u +%Y%m%d-%H%M%SZ)
```

## Non-Goals For The First Fix

- Do not start by increasing host size; host resources were not the bottleneck.
- Do not start by adding verifier capacity; healthy checks should not need
  verifiers.
- Do not infer that v1 outperformed v2 on freshness from this run. The current
  capacity verifier does not measure v1 freshness by DB in the same way.
- Do not run the million-site suite again until the 1,000-site case is fixed
  and v2 has enough metrics to explain its own backlog.

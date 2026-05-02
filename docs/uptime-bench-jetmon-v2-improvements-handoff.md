# Uptime-Bench Handoff: Jetmon v2 Follow-Up

This note summarizes the Jetmon v2 work suggested by the latest
`uptime-bench` runs. The benchmark harness and raw reports live in the sibling
repository:

- `/home/gaarai/code/uptime-bench`
- Latest report:
  `/home/gaarai/code/uptime-bench/reports/v2-regression-9am-20260502-063755Z/report.md`
- Prior comparison reports:
  `/home/gaarai/code/uptime-bench/reports/overnight-gapfill-20260501-034222Z/report.md`
  `/home/gaarai/code/uptime-bench/reports/cadence3m-20260430-211701Z/report.md`

Important constraint: this is Jetmon v2 work. Do not change Jetmon v1 behavior
to improve benchmark results.

## Latest Run

Run tag: `v2-regression-9am-20260502-063755Z`

- Window: `2026-05-02 01:41:02 CDT` to `2026-05-02 08:25:08 CDT`
- Services: Jetmon v1, Jetmon v2, Pingdom, UptimeRobot, Datadog Synthetics,
  Better Uptime
- Timing: 3-minute check cadence, 6-minute active failure window, 4-minute grace
- Samples: 306 scheduled, 290 executed/scored, 16 deadline-skipped
- Jetmon v2 result: `251/290` pass, no adapter errors, no capability mismatch
- Jetmon v2 successful expected-down latency:
  min/mean/max/p95 = `1.3 / 150.6 / 303.9 / 285.1` seconds

## What Improved

The latest run indicates that several issues called out in earlier handoffs are
now fixed or at least materially improved:

- `http-head-200-get-partial`: `26/26` pass
- `http-partial`: `17/17` pass
- `maintenance-http-503-full-cover`: `17/17` pass
- `http-head-timeout-get-200`: improved to `24/26` pass

Earlier reports showed partial/truncated responses and covered maintenance as
major Jetmon v2 failures. Those should now be treated as regression-test areas,
not the primary implementation targets.

## Current Jetmon v2 Gaps

### Priority 0: Deprecated TLS Advisory Detection

Latest result:

- `tls-deprecated-tls11`: `16/16` fail

Interpretation:

- Jetmon v2 did not appear to report downtime for deprecated TLS in this run,
  which is good.
- It still failed the benchmark policy because no advisory was exposed for the
  adapter to retrieve.

Expected benchmark semantics:

- Deprecated TLS 1.0/1.1 should be advisory-only.
- No advisory is a miss.
- A downtime event would be a false outage.

Relevant areas:

- `internal/checker/checker.go`
- `internal/orchestrator/orchestrator.go`
  - `processResults`
  - `checkSSLAlerts`
  - existing TLS-expiry advisory flow
- `internal/eventstore/eventstore.go`
- API event payloads consumed by the uptime-bench Jetmon v2 adapter

Recommended direction:

1. Reuse the TLS-expiry advisory shape where possible.
2. Open or update a warning/advisory event when a check negotiates TLS 1.0 or
   TLS 1.1.
3. Do not project legacy `site_status` down.
4. Close the advisory when the site negotiates TLS 1.2+ again.
5. Include negotiated TLS version and cipher suite in event metadata.

Suggested tests:

- A checker result with deprecated TLS opens or updates a warning event.
- A later TLS 1.2+ result closes the advisory.
- Deprecated TLS never enters the downtime retry or verifier-confirmation path.
- The API exposes the advisory in the same event feed used by the benchmark
  adapter.

Benchmark acceptance:

- `tls-deprecated-tls11` should pass as advisory detected.
- It must not appear as outage / downtime.

### Priority 1: Method-Specific False Downtime

Latest result:

- `http-head-405-get-200`: `15/18` pass, `3/18` false downtime
- `http-head-timeout-get-200`: `24/26` pass, `2/26` false downtime

Interpretation:

- Jetmon v2 is mostly avoiding HEAD-only false downtime, which supports the
  intended GET-based design.
- The remaining failures are intermittent and need evidence before changing
  behavior.

Recommended direction:

1. Preserve GET-based checking. Do not move v2 toward HEAD-based monitoring.
2. Add or inspect per-check history around failed samples:
   - request method
   - URL
   - status code
   - Jetmon error code
   - event open/close timestamps
3. Confirm the uptime-bench adapter filters events by the exact provisioned
   site and run window.
4. Add a regression fixture where HEAD fails/hangs but GET returns 200; assert
   no event opens and site status remains running.

Benchmark acceptance:

- `http-head-405-get-200` should pass consistently.
- `http-head-timeout-get-200` should pass consistently.

### Priority 2: Geo-Scoped HTTP Failures

Latest result:

- `http-geo-503`: `17/17` fail for Jetmon v2
- Every service failed this scenario in the latest run.

Interpretation:

- Do not treat this as a Jetmon-v2-specific bug yet.
- The benchmark's region/vantage assumptions need validation before Jetmon
  changes should be made.

Recommended direction:

1. Confirm whether Jetmon v2 probes originate from a source range that should
   have been included in the `us-east` geo failure.
2. Confirm uptime-bench `probe_ranges` are accurate for every enabled service.
3. If Jetmon is intended to be single-region or agent-local only, document that
   geo-scoped external-probe scenarios are not comparable for Jetmon.

## Regression Areas To Preserve

Do not regress these latest clean passes:

- content checks:
  - `content-defacement`: `9/9`
  - `content-keyword-missing`: `7/7`
  - `content-ransomware`: `9/9`
- ordinary HTTP:
  - `http-503`: `17/17`
  - `http-head-200-get-503`: `18/18`
  - `http-head-200-get-partial`: `26/26`
  - `http-head-200-get-redirect-loop`: `16/16`
  - `http-partial`: `17/17`
  - `http-timeout-ttfb`: `9/9`
- maintenance:
  - `maintenance-http-503-full-cover`: `17/17`
- network/TLS:
  - `tcp-refused`: `9/9`
  - `tls-expiring-5d`: `18/18`
  - `tls-handshake-version-mismatch`: `9/9`
  - `tls-invalid-hostname-mismatch`: `9/9`
  - `tls-invalid-self-signed`: `8/8`

## Suggested Implementation Order

1. Add deprecated TLS advisory lifecycle and API exposure.
2. Add HEAD-failure/GET-healthy regression logging/tests.
3. Validate geo-scoped benchmark assumptions before changing Jetmon behavior.
4. Keep partial response and maintenance suppression tests in the suite as
   regression coverage.

## Verification Plan

Local Jetmon repo checks:

```bash
make test
make lint
make build
```

Focused benchmark smoke after deployment:

- `tls-deprecated-tls11`
- `http-head-405-get-200`
- `http-head-timeout-get-200`
- `http-head-200-get-partial`
- `http-partial`
- `maintenance-http-503-full-cover`

Success criteria:

- Deprecated TLS produces advisory-only detection.
- HEAD-only failure scenarios do not report downtime when GET is healthy.
- Partial/truncated response and maintenance full-cover remain clean.
- Existing strong TLS/content/HTTP detections stay intact.

## Notes For The Next Agent

- Read `AGENTS.md` before editing.
- Jetmon v2 has compatibility constraints around schema, WPCOM payloads,
  StatsD names, log paths, and legacy projection.
- The benchmark sees event history through the API, not just WPCOM
  notifications. Suppressing a notification is not enough if a downtime event
  is still exposed.
- Preserve GET-based checking. It is a deliberate v2 design improvement over
  Jetmon v1's HEAD probes.

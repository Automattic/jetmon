# HEAD/GET Mismatch Harness Run: headget-20260429-030809Z

## Executive Summary

This run tested all configured, enabled harness services against one HTTP 503 control and six HEAD/GET mismatch scenarios. Every scenario run provisioned all enabled services simultaneously, injected one target-side failure, waited through a 10-minute failure window, waited a 5-minute grace period, retrieved service reports, and deprovisioned monitors.

Run window:

- UTC: 2026-04-29 03:08:09 to 2026-04-29 10:19:23
- America/Chicago: 2026-04-28 22:08:09 to 2026-04-29 05:19:23
- Wall time: about 7h 11m
- Target cleanup verified after the run: `active_failures=[]`

Major findings:

- Datadog Synthetics was the only service with 100% behavioral success across valid samples: 19/19.
- Jetmon v2, Pingdom, and Better Uptime all correctly avoided false-down reports for HEAD-only failures and detected GET 503, GET timeout, and GET redirect-loop failures, but all three missed the GET partial/truncated-response scenario.
- Better Uptime produced the fastest successful downtime detections among the services with complete data in this run: mean 16.5s across successful expected-down samples.
- Jetmon v1 produced no downtime reports at all, including for the HTTP 503 control. It therefore passed the false-down traps by silence, but missed every visitor-visible GET failure.
- UptimeRobot data is not comparable for the full matrix. It succeeded on the control, then produced one false-down report for `HEAD 405 / GET 200`, missed one `HEAD 200 / GET 503`, then a timed-out `newMonitor` call leaked a monitor and caused all later UptimeRobot provisions to fail with `already_exists`. The leaked monitor `802947032` was deleted after the run.

## What Was Tested

Enabled services:

- `jetmon-v1`
- `jetmon-v2`
- `pingdom`
- `uptimerobot`
- `datadog-synthetics`
- `better-uptime`

Timing used for every scenario:

- `check_frequency = "300s"`
- `duration = "600s"`
- `grace_period = "300s"`
- Gap between planned runs: 480s

Planned run set:

- 1 control run: `http-503`
- 18 HEAD/GET runs: 6 scenarios x 3 repeats
- Total: 19 scenario runs

Scenario start minute marks UTC:

`03:08`, `03:31`, `03:54`, `04:17`, `04:41`, `05:04`, `05:27`, `05:50`, `06:13`, `06:36`, `06:59`, `07:22`, `07:45`, `08:08`, `08:31`, `08:55`, `09:18`, `09:41`, `10:04`.

## Scoring Rules

This report uses custom method-sensitive scoring over the raw `ground_truth_events` and `monitor_reports` rows.

- Expected `down`: success means at least one `alert_fired` report during the active failure window.
- Expected `up`: success means no `alert_fired` report from failure start through run end.
- `adapter_error`: the service did not produce a usable monitor report row for that run, usually because provisioning failed.
- Latency is seconds from `failure_start` to first `alert_fired`.
- For expected-up scenarios, latency is shown only when the service failed by reporting downtime.

## Raw Artifacts

The full raw and derived artifacts are in this directory:

- `manifest.json`: run configuration.
- `order.tsv`: planned run order.
- `run-results.tsv`: actual per-run start/end timestamps and harness exit codes.
- `run.log`: full harness log.
- `scenario_runs.tsv`: exported `scenario_runs` rows for this run tag.
- `ground_truth_events.tsv`: exported target-side event log rows.
- `monitor_reports.tsv`: exported raw service report rows.
- `derived_metrics.tsv`: exported built-in derived metrics rows.
- `evaluation_rows.tsv`: custom per-run, per-service HEAD/GET scoring used by this report.
- `summary_by_service_scenario.json`: generated scenario/service summary.
- `services.redacted.toml`: enabled service config with secrets redacted.

## Service Summary

| Service | Valid samples | Success | Behavioral failure | Adapter error | Expected-down latency min/mean/max s | Erroneous downtime latency min/mean/max s |
|---|---:|---:|---:|---:|---:|---:|
| `jetmon-v1` | 19 | 6 | 13 | 0 | - | - |
| `jetmon-v2` | 19 | 16 | 3 | 0 | 28.8 / 166.0 / 304.4 | - |
| `pingdom` | 19 | 16 | 3 | 0 | 226.9 / 313.2 / 402.6 | - |
| `uptimerobot` | 3 | 1 | 2 | 16 | 78.0 / 78.0 / 78.0 | 75.0 / 75.0 / 75.0 |
| `datadog-synthetics` | 19 | 19 | 0 | 0 | 300.3 / 303.4 / 336.4 | - |
| `better-uptime` | 19 | 16 | 3 | 0 | 2.3 / 16.5 / 34.0 | - |

## Scenario Matrix

Latency is `min / mean / max` seconds for successful expected-down detections, or for erroneous downtime reports on expected-up scenarios.

| Scenario | Expected | Service | Result | Latency min/mean/max s |
|---|---:|---|---:|---:|
| `http-503` | `down` | `jetmon-v1` | 0 ok / 1 fail / 0 err | - |
| `http-503` | `down` | `jetmon-v2` | 1 ok / 0 fail / 0 err | 157.5 / 157.5 / 157.5 |
| `http-503` | `down` | `pingdom` | 1 ok / 0 fail / 0 err | 279.0 / 279.0 / 279.0 |
| `http-503` | `down` | `uptimerobot` | 1 ok / 0 fail / 0 err | 78.0 / 78.0 / 78.0 |
| `http-503` | `down` | `datadog-synthetics` | 1 ok / 0 fail / 0 err | 300.5 / 300.5 / 300.5 |
| `http-503` | `down` | `better-uptime` | 1 ok / 0 fail / 0 err | 3.3 / 3.3 / 3.3 |
| `http-head-405-get-200` | `up` | `jetmon-v1` | 3 ok / 0 fail / 0 err | - |
| `http-head-405-get-200` | `up` | `jetmon-v2` | 3 ok / 0 fail / 0 err | - |
| `http-head-405-get-200` | `up` | `pingdom` | 3 ok / 0 fail / 0 err | - |
| `http-head-405-get-200` | `up` | `uptimerobot` | 0 ok / 1 fail / 2 err | 75.0 / 75.0 / 75.0 |
| `http-head-405-get-200` | `up` | `datadog-synthetics` | 3 ok / 0 fail / 0 err | - |
| `http-head-405-get-200` | `up` | `better-uptime` | 3 ok / 0 fail / 0 err | - |
| `http-head-timeout-get-200` | `up` | `jetmon-v1` | 3 ok / 0 fail / 0 err | - |
| `http-head-timeout-get-200` | `up` | `jetmon-v2` | 3 ok / 0 fail / 0 err | - |
| `http-head-timeout-get-200` | `up` | `pingdom` | 3 ok / 0 fail / 0 err | - |
| `http-head-timeout-get-200` | `up` | `uptimerobot` | 0 ok / 0 fail / 3 err | - |
| `http-head-timeout-get-200` | `up` | `datadog-synthetics` | 3 ok / 0 fail / 0 err | - |
| `http-head-timeout-get-200` | `up` | `better-uptime` | 3 ok / 0 fail / 0 err | - |
| `http-head-200-get-503` | `down` | `jetmon-v1` | 0 ok / 3 fail / 0 err | - |
| `http-head-200-get-503` | `down` | `jetmon-v2` | 3 ok / 0 fail / 0 err | 28.8 / 90.5 / 159.0 |
| `http-head-200-get-503` | `down` | `pingdom` | 3 ok / 0 fail / 0 err | 226.9 / 295.3 / 333.0 |
| `http-head-200-get-503` | `down` | `uptimerobot` | 0 ok / 1 fail / 2 err | - |
| `http-head-200-get-503` | `down` | `datadog-synthetics` | 3 ok / 0 fail / 0 err | 300.4 / 300.7 / 300.9 |
| `http-head-200-get-503` | `down` | `better-uptime` | 3 ok / 0 fail / 0 err | 2.3 / 3.7 / 5.0 |
| `http-head-200-get-timeout` | `down` | `jetmon-v1` | 0 ok / 3 fail / 0 err | - |
| `http-head-200-get-timeout` | `down` | `jetmon-v2` | 3 ok / 0 fail / 0 err | 145.8 / 237.5 / 288.7 |
| `http-head-200-get-timeout` | `down` | `pingdom` | 3 ok / 0 fail / 0 err | 282.3 / 346.5 / 394.0 |
| `http-head-200-get-timeout` | `down` | `uptimerobot` | 0 ok / 0 fail / 3 err | - |
| `http-head-200-get-timeout` | `down` | `datadog-synthetics` | 3 ok / 0 fail / 0 err | 300.3 / 312.4 / 336.4 |
| `http-head-200-get-timeout` | `down` | `better-uptime` | 3 ok / 0 fail / 0 err | 32.4 / 33.0 / 34.0 |
| `http-head-200-get-redirect-loop` | `down` | `jetmon-v1` | 0 ok / 3 fail / 0 err | - |
| `http-head-200-get-redirect-loop` | `down` | `jetmon-v2` | 3 ok / 0 fail / 0 err | 98.6 / 172.9 / 304.4 |
| `http-head-200-get-redirect-loop` | `down` | `pingdom` | 3 ok / 0 fail / 0 err | 261.9 / 309.2 / 402.6 |
| `http-head-200-get-redirect-loop` | `down` | `uptimerobot` | 0 ok / 0 fail / 3 err | - |
| `http-head-200-get-redirect-loop` | `down` | `datadog-synthetics` | 3 ok / 0 fail / 0 err | 300.4 / 300.6 / 300.9 |
| `http-head-200-get-redirect-loop` | `down` | `better-uptime` | 3 ok / 0 fail / 0 err | 16.3 / 17.2 / 19.1 |
| `http-head-200-get-partial` | `down` | `jetmon-v1` | 0 ok / 3 fail / 0 err | - |
| `http-head-200-get-partial` | `down` | `jetmon-v2` | 0 ok / 3 fail / 0 err | - |
| `http-head-200-get-partial` | `down` | `pingdom` | 0 ok / 3 fail / 0 err | - |
| `http-head-200-get-partial` | `down` | `uptimerobot` | 0 ok / 0 fail / 3 err | - |
| `http-head-200-get-partial` | `down` | `datadog-synthetics` | 3 ok / 0 fail / 0 err | 300.7 / 301.0 / 301.5 |
| `http-head-200-get-partial` | `down` | `better-uptime` | 0 ok / 3 fail / 0 err | - |

## Per-Service Notes

### Datadog Synthetics

Datadog Synthetics had the cleanest result in this run: all 19 valid samples succeeded. It detected every expected-down condition, including the partial/truncated response scenario that most other services missed, and it did not report false downtime for the HEAD-only failures. Its detection latency clustered around the configured five-minute cadence, with successful expected-down detections at 300.3s to 336.4s.

### Better Uptime

Better Uptime correctly handled all false-down traps and detected GET 503, GET timeout, and GET redirect-loop failures. It missed all three partial/truncated response samples. For the expected-down cases it did detect, it was very fast in this run, with successful detection latencies from 2.3s to 34.0s.

### Jetmon v2

Jetmon v2 correctly handled both HEAD-only false-down traps and detected GET 503, GET timeout, and GET redirect-loop failures. It missed all three partial/truncated response samples. Its successful expected-down detection latencies ranged from 28.8s to 304.4s, with a mean of 166.0s.

### Pingdom

Pingdom correctly handled both HEAD-only false-down traps and detected GET 503, GET timeout, and GET redirect-loop failures. It missed all three partial/truncated response samples. Successful expected-down latency ranged from 226.9s to 402.6s, with a mean of 313.2s.

### Jetmon v1

Jetmon v1 produced no downtime alerts in this run. That means it did not false-alarm on the HEAD-only traps, but it also missed the HTTP 503 control and all visitor-visible GET failures. This result should be treated as a serious functional failure or configuration issue before Jetmon v1 is compared further.

### UptimeRobot

UptimeRobot had only three usable samples. It detected the HTTP 503 control at 78.0s, falsely reported downtime for the `HEAD 405 / GET 200` case at 75.0s, and missed the first `HEAD 200 / GET 503` case. During the fourth run, the `/newMonitor` call timed out after the API appears to have created the monitor. Because the adapter did not receive a monitor ID, it could not delete it. Every later UptimeRobot provision failed with `already_exists`.

Cleanup performed after the batch:

- Found leaked UptimeRobot monitor: `802947032`, `uptime-bench: bench-a`, `http://bench-a.harmonic.party/`
- Deleted it successfully with `/deleteMonitor`

Recommended adapter hardening: after `newMonitor` timeouts or `already_exists`, query for harness-owned monitors matching `friendly_name = "uptime-bench: bench-a"` and `url = "http://bench-a.harmonic.party/"`, then either adopt or delete the stale monitor before retrying.

## Takeaways

The test was useful as a first real long-form comparison. It produced clear separation across services:

- Datadog Synthetics handled every scenario in this set.
- Better Uptime, Jetmon v2, and Pingdom handled status, timeout, and redirect GET failures but missed partial/truncated responses.
- Jetmon v1 did not detect even the control outage.
- UptimeRobot exposed an adapter robustness issue that invalidated most of its matrix results.

For a publishable follow-up, rerun this same plan after fixing or working around the UptimeRobot leaked-monitor path. Keep the same timing, service set, and scenario set, but consider adding an automatic preflight cleanup for stale harness-owned monitors before every run.

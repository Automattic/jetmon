# Support Guide

This guide is for people explaining Jetmon behavior to customers and internal
teams. It helps answer the questions v1 often made hard:

- Why did Jetmon say the site was down?
- Why was there no notification?
- Why did the site recover before a customer noticed?
- Was this a false positive, a real outage, or a monitor-side issue?

## First Checks

Start with the site audit timeline:

```bash
./jetmon2 audit --blog-id 12345 --since 24h
```

For a specific incident window:

```bash
./jetmon2 audit \
  --blog-id 12345 \
  --since 2026-04-01T10:00:00 \
  --until 2026-04-01T11:00:00
```

The timeline shows local checks, retries, Veriflier requests, WPCOM
notifications, status transitions, maintenance suppression, and other
operator-visible notes.

For a broader production window:

```bash
./jetmon2 telemetry report --since=24h
```

The telemetry report summarizes detection timing, Veriflier agreement,
false-alarm classes, WPCOM attempt parity, and explanation gaps. If WPCOM
parity differs near the edge of the selected window, rerun with a later
`--until` before treating it as a missed notification.

## Incident States

Jetmon 2 separates local detection from independent confirmation:

| State | Meaning |
| --- | --- |
| `Seems Down` | Local checks failed and Jetmon is retrying or asking Verifliers. |
| `Down` | Verifliers confirmed the outage. |
| `Resolved` | The incident closed after recovery or manual action. |
| `Unknown` | Jetmon could not produce trustworthy site evidence. |

A brief local failure can recover before Verifliers confirm it. That closes as
`false_alarm` or `probe_cleared` instead of sending a confirmed-down
notification.

`Unknown` is not customer-site downtime. Use it when monitor infrastructure,
Veriflier quorum, database access, or telemetry gaps prevent a trustworthy
verdict.

## HEAD, GET, And Detection Profiles

Jetmon 1 used `HEAD` requests. Some customer stacks block `HEAD`, route it
differently, or return a status that does not match a real page load.

Jetmon 2 supports staged policy:

1. `HEAD` + `legacy` for v1-compatible rollout.
2. `GET` + `simple_http` for visitor-path migration.
3. `GET` + `full` for the full v2 detection set.

When behavior differs from v1, check the site's effective `request_method` and
`detection_profile`. V2 may be surfacing a real GET-path issue or a full-profile
detection that v1's HEAD-only probe never exercised.

V2 Verifliers still use the v2 `/v2/check` transport; `HEAD` versus `GET` is
the probe method inside that request, not a legacy Veriflier protocol.

## WAF And Allowlist Guidance

Jetmon 2 uses the `jetmon/2.0` user agent. During rollout it may use `HEAD` or
`GET` depending on site policy. For GET cohorts, firewalls, WAFs, bot controls,
and security plugins should allow Jetmon to reach the same application path a
visitor would reach.

Do not ask customers to broadly disable security rules. The safer request is to
allow the published Jetmon source hosts or IP ranges and the `jetmon/2.0` user
agent.

Common blocked-monitoring signals:

| Symptom | Likely explanation |
| --- | --- |
| `blocked` / HTTP 403 | The site or edge layer rejected the monitor request. |
| Captcha or bot challenge | The request hit protection instead of the site. |
| `keyword_missing` | Jetmon received a page, but not expected customer content. |
| Redirect failure | Jetmon was sent to a login, challenge, canonical URL, or unexpected host. |
| Local failure, Verifliers disagree | The block may be regional, source-specific, intermittent, or edge-specific. |

Separate "the site was down for visitors" from "the monitor could not verify
the visitor path." A WAF block is real monitor evidence, but it is not automatic
proof that all visitors saw downtime.

## Failure Types

| Type | Meaning |
| --- | --- |
| `server` | Site returned 5xx. |
| `blocked` | Site returned 403. |
| `client` | Site returned 4xx other than 403. |
| `https` | SSL/TLS problem. |
| `intermittent` | Request timed out. |
| `redirect` | Redirect policy failure. |
| `ssl_expiry` | Certificate crossed an expiry threshold. |
| `tls_deprecated` | Site serves TLS 1.0 or 1.1. |
| `keyword_missing` | Required keyword was absent. |
| `keyword_forbidden` | Forbidden keyword was present. |
| `success` | Site recovered. |

For resolver failures, inspect event metadata for `dns_error_kind`,
`dns_error_name`, and `dns_error_server`. These explain what Jetmon's resolver
saw, not what every resolver on the internet saw.

`tls_deprecated` is advisory-only: it does not mark the site down. Avoid
sensitive custom check headers on sites that only support TLS 1.0 or 1.1 until
the site is upgraded.

## Common Investigations

SSL expiry:

```sql
SELECT s.blog_id, s.monitor_url, r.ssl_expiry_date
FROM jetpack_monitor_sites s
LEFT JOIN jetpack_monitor_site_runtime r ON r.blog_id = s.blog_id
WHERE s.blog_id = 12345;
```

False positives:

```sql
SELECT *
FROM jetpack_monitor_false_positives
WHERE blog_id = 12345
ORDER BY created_at DESC
LIMIT 20;
```

A false positive means local checks failed, Verifliers were asked, and the
Verifliers did not confirm the site as down. A high rate for one site usually
points to transient network, redirect, firewall, or performance behavior worth
tuning.

## Maintenance And Sensitivity

Maintenance windows suppress downtime incidents while checks continue and
results are recorded. Always set an explicit `maintenance_end`; an open-ended
window can silently suppress alerts indefinitely.

Per-site `alert_cooldown_minutes` reduces repeated alerts from a flapping site.
Global promotion behavior is controlled by `NUM_OF_CHECKS`: that many
consecutive local failures are required before Veriflier escalation.

In variable-interval mode, failed probes get a bounded one-minute follow-up
when the site's normal interval is longer. `TIME_BETWEEN_CHECKS_SEC` is kept
for v1 config compatibility; do not promise per-site retry tuning unless the
deployed schema includes it.

## WPCOM Notifications

WPCOM status-change notifications include:

- `blog_id`
- `monitor_url`
- `status_id`: `0` down, `1` running, `2` confirmed down
- `last_check`
- `last_status_change`
- `checks`

Each `checks` entry includes checker type, host, status, RTT, and HTTP code.

## Customer Framing

- "Jetmon saw local failures, retried, then asked Verifliers before notifying."
- "The site recovered before quorum confirmation, so Jetmon recorded the event
  but did not send a confirmed-down notification."
- "The alert was suppressed because a maintenance window was active."
- "The site blocked the monitor with a 403, which is different from the site
  being down for visitors."
- "This site is in the GET cohort, so Jetmon tests the visitor path more
  closely than the v1 HEAD-only check did."
- "Jetmon could not produce a trustworthy verdict because monitor-side
  telemetry was incomplete; that is not confirmed downtime."
- "The audit trail shows exactly which checkers saw the failure and what status
  code or timeout they received."

# Support Guide

This guide is for people explaining Jetmon behavior to customers and internal
teams. It focuses on the questions v1 made hard to answer:

- Why did Jetmon say the site was down?
- Why was there no notification?
- Why did the site recover before a customer noticed?
- Was this a false positive, a real outage, or a monitor-side issue?

## Start With The Audit Timeline

Use the audit CLI to reconstruct a site's recent monitoring history:

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

The timeline shows local checks, retry attempts, Veriflier requests and results,
WPCOM notifications, status transitions, maintenance-window suppression, and
other operational notes.

## Explain The Incident State

Jetmon 2 separates detection from confirmation:

| State | Meaning |
|---|---|
| `Seems Down` | Local checks failed and Jetmon is retrying or asking Verifliers |
| `Down` | Verifliers confirmed the outage |
| `Resolved` | The incident closed after recovery or manual action |

This matters for customer conversations. A site can be briefly unreachable from
the monitor and then recover before Verifliers confirm it. That closes as a
false alarm or probe-cleared event instead of sending a customer-facing outage
notification.

## Understand Alert Types

| Type | Meaning |
|---|---|
| `server` | Site returned a 5xx response |
| `blocked` | Site returned 403, often because monitoring is blocked |
| `client` | Site returned a 4xx response other than 403 |
| `https` | SSL/TLS problem |
| `intermittent` | Request timed out |
| `redirect` | Redirect policy failure |
| `ssl_expiry` | Certificate expires within the configured threshold |
| `tls_deprecated` | Site is serving TLS 1.0 or 1.1 |
| `keyword_missing` | Response body did not contain the expected keyword |
| `success` | Site recovered |

## Check SSL Certificate Status

```sql
SELECT blog_id, monitor_url, ssl_expiry_date
FROM jetpack_monitor_sites
WHERE blog_id = 12345;
```

`ssl_expiry_date` is updated on HTTPS checks. Alerts fire at the configured
expiry thresholds, currently 30, 14, and 7 days before expiry.

## Check For False Positives

```sql
SELECT *
FROM jetmon_false_positives
WHERE blog_id = 12345
ORDER BY created_at DESC
LIMIT 20;
```

A false positive is recorded when Jetmon escalates a local failure to Veriflier
confirmation and the Verifliers do not confirm the site as down. A high rate for
one site usually means the site has transient network, redirect, firewall, or
performance behavior worth tuning.

## Maintenance Windows

Use maintenance windows for planned work:

```sql
UPDATE jetpack_monitor_sites
SET maintenance_start = '2026-04-20 02:00:00',
    maintenance_end   = '2026-04-20 04:00:00'
WHERE blog_id = 12345;
```

Checks continue and results are recorded during the window, but alerts are
suppressed. Always set an explicit `maintenance_end`; an open-ended window can
silently suppress alerts indefinitely.

Clear a window after maintenance:

```sql
UPDATE jetpack_monitor_sites
SET maintenance_start = NULL,
    maintenance_end = NULL
WHERE blog_id = 12345;
```

## Alert Sensitivity

Use per-site cooldowns to reduce repeated alerts from a flapping site:

```sql
UPDATE jetpack_monitor_sites
SET alert_cooldown_minutes = 60
WHERE blog_id = 12345;
```

Global retry behavior is controlled by `NUM_OF_CHECKS` and
`TIME_BETWEEN_CHECKS_SEC`. Per-site retry overrides are planned separately; do
not promise per-site retry tuning unless the deployed schema includes it.

## WPCOM Notification Data

Every status-change notification sent to WPCOM includes:

| Field | Description |
|---|---|
| `blog_id` | The site's WPCOM ID |
| `monitor_url` | URL that was checked |
| `status_id` | `0` down, `1` running, `2` confirmed down |
| `last_check` | Datetime of the last check |
| `last_status_change` | Datetime of the last status change |
| `checks` | Local and Veriflier check results |

Each `checks` entry includes:

| Field | Description |
|---|---|
| `type` | `1` local Jetmon check, `2` Veriflier check |
| `host` | Hostname of the checker |
| `status` | `0` down, `1` running, `2` confirmed down |
| `rtt` | Round-trip time in milliseconds |
| `code` | HTTP response code |

## Useful Customer Framing

- "Jetmon saw local failures, retried, then asked Verifliers before notifying."
- "The site recovered before quorum confirmation, so Jetmon recorded the event
  but did not send a confirmed-down notification."
- "The alert was suppressed because a maintenance window was active."
- "The site blocked the monitor with a 403, which is different from the site
  being down for visitors."
- "The audit trail shows exactly which checkers saw the failure and what status
  code or timeout they received."

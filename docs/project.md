# Jetmon 2 Project Overview

Jetmon 2 is the Go rewrite of Jetmon, the uptime monitoring service for
Jetpack-powered sites. It replaces the original Node.js plus C++ native-addon
Monitor and Qt/C++ Veriflier with Go services that are easier to deploy, easier
to reason about, and much more efficient at high check volume.

The project goal is not only to preserve Jetmon v1 behavior. Jetmon 2 must be a
drop-in replacement during rollout while also fixing v1's biggest operational
and product limitations: HEAD-only checks, weak incident evidence, limited
operator visibility, difficult Veriflier deployment, fragile worker behavior,
and poor scalability.

Detailed design lives in the focused docs:

- [architecture.md](architecture.md) - current runtime architecture
- [data-model.md](data-model.md) - tables and rollout-safe schema ownership
- [events.md](events.md) - incident state model and transition rules
- [operations-guide.md](operations-guide.md) - operator commands and runtime care
- [v1-to-v2-migration.md](v1-to-v2-migration.md) - production migration runbook
- [internal-api-reference.md](internal-api-reference.md) - API reference
- [roadmap.md](roadmap.md) - active deferred work

## What Changed From v1

Jetmon v1 used forked Node.js workers and a native C++ addon for the Monitor,
plus a Qt/C++ Veriflier service. That design worked, but it made capacity,
deployment, and support harder than they needed to be.

Jetmon 2 changes the foundation:

- The Monitor is a single Go binary with goroutine-based concurrency.
- The Veriflier is a standalone Go service using JSON over HTTP.
- Runtime logs go to stdout/stderr for container or service-manager collection.
- Site state is event-sourced in v2-owned tables while the v1 site table stays
  compatible for rollout.
- Checks can run as `HEAD` or `GET`, with staged detection profiles.
- The API, dashboard, StatsD metrics, and audit/event tables expose what Jetmon
  saw and why it acted.

The most customer-visible correction is the move beyond HEAD-only monitoring.
Jetmon v1 used HEAD requests, which caused false positives and false negatives
for sites that block, special-case, or incorrectly implement HEAD. Jetmon 2 can
roll out in v1-compatible `HEAD` + `legacy` mode, then move cohorts to
`GET` + `simple_http`, then to `GET` + `full` once production evidence supports
the transition.

## Compatibility During Rollout

Jetmon 2 keeps the production-facing contracts that matter for a safe rollout:

| Interface | Compatibility rule |
| --- | --- |
| `jetpack_monitor_sites` | Remains the v1-shaped site identity, bucket, cadence, and compatibility projection table. |
| WPCOM notifications | Initial rollout uses the v1-compatible legacy notification path and payload. |
| StatsD naming | Existing dotted path format is preserved; v2 adds metrics rather than replacing v1 names. |
| `stats/` output | v1-style `sitespersec`, `sitesqueue`, and `totals` remain available through the compatibility surface. |
| Config keys | v1-style configs still parse where needed; removed or deprecated knobs are documented in [operations-guide.md](operations-guide.md). |

Rollout-specific v2 state lives outside `jetpack_monitor_sites` in v2-owned
tables such as `jetpack_monitor_site_check_config`,
`jetpack_monitor_site_runtime`, `jetpack_monitor_events`, and
`jetpack_monitor_event_transitions`. During the drop-in phase, v2 updates only
the legacy compatibility projection fields needed by existing readers.

Production schema changes are expected to be applied through the approved
database-change process. Production Monitor containers should validate schema
state at startup rather than applying automatic DDL.

## Runtime Shape

At a high level, a v2 Monitor process is responsible for:

- loading the active target set from MySQL,
- scheduling checks according to each site's configured interval,
- performing local HTTP/TLS/DNS-observed checks,
- escalating sustained local failures to Verifliers,
- writing events, transitions, check history, runtime projection, and audit
  evidence,
- publishing host/dashboard/process health, and
- sending legacy WPCOM notifications during the rollout phase.

The current deployment keeps the API, dashboard, monitor loop, and delivery
workers in one binary where configured. The standalone `jetmon-deliverer`
exists for deployments that want webhook and alert-contact delivery separated
from Monitor checks.

The Veriflier fleet is deployed separately. V2 Monitors should point at v2
Verifliers only. The v2 Veriflier contract is `/v2/check` and `/v2/status`;
legacy-compatible Veriflier HTTP endpoints are lab/emergency compatibility
tools, not the normal production transport.

## Core Detection Capabilities

Jetmon 2 currently detects and records:

- HTTP availability failures by status code, timeout, connection failure, and
  resolver failure.
- Staged `HEAD` and `GET` probe behavior for rollout-safe migration.
- Full-profile body checks for required content, forbidden content, common
  WordPress fatal/database/configuration pages, Redis object-cache connection
  errors, default virtual-host pages, host suspension pages, Jetpack probe echo
  pages, and near-empty HTML responses.
- Redirect policy failures or warnings.
- TLS certificate expiry warnings.
- Deprecated TLS protocol observations.
- Per-check DNS, TCP connect, TLS handshake, first-byte, and total RTT timing.
- Maintenance-window suppression without losing operational evidence.
- Veriflier agreement, disagreement, overload, and no-vote outcomes.

The event model separates lifecycle state from severity. A local failure opens a
`Seems Down` event, Veriflier confirmation promotes that same event to `Down`,
and recovery closes it with an explicit resolution reason. This keeps incident
duration anchored to the first observed user-impacting failure rather than the
later confirmation time.

## Operator Visibility

Jetmon 2 is built to answer support and rollout questions that v1 made hard:

- What did the Monitor see locally?
- Which Verifliers were asked?
- Did Verifliers agree, disagree, overload, or fail to respond?
- Was a site in a maintenance window?
- Was a body rule, redirect rule, TLS issue, or HTTP status responsible?
- Did WPCOM notification delivery happen?
- Is a host stale, overloaded, missing buckets, or behind on check freshness?

The main evidence surfaces are:

- event and transition tables for authoritative incident state,
- `jetpack_monitor_check_history` for timing and check-method history,
- `jetpack_monitor_audit_log` for operational context,
- StatsD for low-overhead runtime trends,
- the host and fleet dashboards,
- the internal API, and
- rollout and telemetry CLI commands.

## Deployment Model

The current production plan deploys new v2 infrastructure beside the v1 fleet:

1. Apply approved schema changes.
2. Deploy and validate the v2 Veriflier fleet.
3. Deploy v2 Monitor containers in standby/API-controlled mode.
4. Run read-only smoke checks and dependency validation.
5. Activate bucket ranges only after the matching v1 ownership has stopped.
6. Observe each activated range before expanding.
7. Keep the initial check policy at `HEAD` + `legacy`.
8. Gradually migrate check policy to `GET` + `simple_http`, then `GET` +
   `full`.
9. Tear down v1 only after v2 coverage, alerts, dashboards, and rollback gates
   are accepted.

The detailed procedure is in [v1-to-v2-migration.md](v1-to-v2-migration.md).
TeamCity, container image, and Veriflier VPS details live in
[production-teamcity-rollout.md](production-teamcity-rollout.md),
[docker-images.md](docker-images.md), and
[production-veriflier-compose.md](production-veriflier-compose.md).

## Why This Matters

For sysadmins, Jetmon 2 reduces deployment risk: fewer runtime dependencies,
clear schema validation, explicit rollout gates, API-driven activation, and
better visibility into host and Veriflier health.

For VIP and Agency site managers, Jetmon 2 directly addresses the HEAD-only
problem that caused many v1 false positives and false negatives. It also adds
the evidence needed to explain exactly what was observed.

For Happiness Engineers, Jetmon 2 turns "Jetmon said it was down" into a
traceable story: request method, response code, timing phases, body-rule result,
Veriflier votes, notification attempts, and resolution reason.

For leadership, Jetmon 2 creates a platform for a stronger uptime product:
better reliability, richer incident history, internal APIs, webhooks, managed
alert contacts, fleet dashboards, and future Jetpack/WPCOM integrations.

For Jetpack engineers, Jetmon 2 provides the operational foundation needed to
make uptime monitoring more accurate, explainable, and extensible.

## What Is Intentionally Not Here

This document is the project overview. It should not duplicate:

- command-by-command rollout instructions,
- full API schemas,
- every config key,
- migration SQL details,
- current roadmap history,
- lab-specific test plans, or
- low-level architectural decision records.

Use [docs/README.md](README.md) as the docs index when looking for those
details.

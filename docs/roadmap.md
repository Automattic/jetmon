# Roadmap

Deferred work and future direction. Completed history belongs in
[changelog.md](changelog.md); accepted decisions belong in
[decisions.md](decisions.md).

## Current Priority

1. Finish safe v1-to-v2 production rollout.
2. Keep evidence surfaces accurate: events, transitions, audit, check history,
   metrics, dashboard, API.
3. Avoid new customer-visible surface area until internal API and gateway paths
   have production evidence.

## Rollout And Compatibility

- Keep guided rollout checks aligned with real deployment.
- Maintain approved canaries for smoke and method comparison.
- Track legacy consumers of `site_status`, stats files, and WPCOM payloads.
- Rehearse production baseline DDL on a production-like replica and compare
  column types, nullability, defaults, generated expressions, and index
  definitions beyond the startup structural validator.
- Tighten production schema reconciliation output so generated DDL is safe for
  existing v1-shaped side tables. In particular, `source_site_id` primary-key
  migrations must account for tables that already have `PRIMARY KEY(blog_id)`.
- Make rehearsal lab notification posture match the intended rollout posture:
  no real WPCOM calls, no live alerts, and clear fixture-only evidence when
  legacy notification checks are intentionally exercised.
- Retire local/lab dependence on `jetpack_monitor_schema_migrations` after the
  structural schema reconciler has enough usage evidence.
- Retire pinned bucket mode and migration aliases after cutover.
- Decide when `LEGACY_STATUS_PROJECTION_ENABLE` can default off.

## Veriflier And Probe Model

Near-term:

- validate `/v2/check` and `/v2/status` under production-like load;
- improve quorum diagnostics for disagreement, overload, and stale vantages;
- keep a quorum floor that prevents one Veriflier from confirming downtime.

Future options should wait for production evidence: stronger v2 probe metadata,
peer mesh, central scheduler plus regional agents, always-on multi-region
quorum, or hybrid external probes plus WPCOM/site signals.

## Detection Coverage

Candidates:

- richer WordPress fatal/database/config detection;
- WAF and bot-protection classification;
- redirect-chain change detection;
- default virtual-host and suspension-page detection;
- TLS warnings beyond expiry/deprecated versions;
- DNS diagnostics that never classify monitor-side failure as downtime;
- customer-tunable sensitivity profiles.

Each new rule needs event mapping, false-positive examples, support explanation,
cohort rollout, rollback criteria, and controlled fixtures.

## Scale And Operations

Continue capacity work around target reload cadence, check-history sampling,
runtime writes, checker pool sizing, MySQL indexes, StatsD/dashboard overhead,
and long-soak memory/goroutine stability.

Evidence should include phase timings, queue depth, stale-check count, DB write
latency, p95/p99 check timing, memory/goroutine profiles, and the limiting
resource.

Useful operator improvements:

- Ensure Docker-built Monitor and Veriflier images embed the same version,
  commit, build date, and Go version metadata as `make all` binaries.
- Make Veriflier StatsD/Graphite evidence reliable in rollout rehearsals so
  load and quorum checks do not have to rely on `/v2/status` alone.
- clearer dashboard reasons for not-ready hosts;
- one-command evidence packet export;
- alerts for projection drift, stale process health, and delivery backlog;
- event links to check-history and audit rows;
- visible maintenance-window state.

## Delivery And API

Near-term:

- prove webhook and alert-contact workers under production transition volume;
- keep `jetmon-deliverer` migration conservative;
- add and rehearse the `jetmon-deliverer` production container image path
  before planning standalone delivery in the first production rollout;
- monitor pending, failed, and abandoned deliveries;
- avoid duplicate human notifications between WPCOM and alert contacts.

Deferred:

- webhook grace-period secret rotation;
- application-level credential encryption with a master key/KMS;
- alert grouping or digests;
- SMS/OpsGenie/custom transports if demand appears;
- migrating WPCOM notifications behind managed delivery after alert contacts
  prove out.

Before any direct public API:

- define auth and tenant model outside Jetmon;
- define metadata redaction;
- add public error vocabulary, rate limits, and abuse controls;
- backfill owner/tenant data for webhooks and contacts;
- revisit secret exposure and rotation;
- publish compatibility and deprecation policy.

## Product Possibilities

Post-rollout candidates: customer-visible incident history, per-site
sensitivity profiles, managed contacts in product UI, customer webhooks, SLA
exports, richer maintenance windows, and status-page integration if a product
owner commits to it.

## Not Now

GraphQL, public status-page hosting inside Jetmon, direct customer API without
the gateway, new Veriflier protocols, multi-region always-on quorum as a v2
launch blocker, and large docs expansions without a canonical owner.

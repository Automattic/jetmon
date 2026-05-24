# Jetmon Roadmap

This is the active Jetmon 2 roadmap. It tracks open work, sequencing, and the
reason each item remains unfinished.

Completed implementation history belongs in [`changelog.md`](changelog.md),
merged PRs, ADRs, and the focused docs linked below. Keep this file short enough
that it can guide what to build next.

## Current Priority

1. Finish production rollout evidence and approvals.
2. Keep the config and rollout surface simple enough for Systems to operate.
3. Continue scalability work only from measured capacity and I/O data.
4. Defer customer-facing API and v3 probe-agent design until v2 has production
   behavior to learn from.

## Production Rollout Readiness

Source docs:

- [`v1-to-v2-migration.md`](v1-to-v2-migration.md)
- [`rollout-quick-reference.md`](rollout-quick-reference.md)
- [`jetmon-v2-prelaunch-readiness.md`](jetmon-v2-prelaunch-readiness.md)
- [`production-teamcity-rollout.md`](production-teamcity-rollout.md)
- [`production-rollout-lab.md`](production-rollout-lab.md)

Open work:

- [ ] Get WPCOM/Product approval for the launch posture statement before using
  it as rollout-room or support language.
- [ ] Get WPCOM/Jetpack/Support owner confirmation for the legacy consumer
  inventory, including hidden consumers not present in local sibling checkouts
  and which paths still require legacy projection during rollout.
- [ ] Get Systems/Jetmon approval for exact rollout stop/go thresholds after
  production-like rehearsal data is available.
- [ ] Record projection-drift and telemetry-parity evidence on production-like
  data before first canary. Lab evidence exists, but the known run had no active
  sites and notifications were intentionally disabled.
- [ ] Get WPCOM acceptance for WPCOM-owned notification parity cases: inactive
  site behavior, URL mismatch behavior, blacklisted site behavior,
  home-URL-only handling, and legacy hook consumers.
- [ ] Get WPCOM/Product/Support approval for the canary cohort matrix and exact
  expansion/rollback thresholds.
- [ ] Run and record approved synthetic canary checks before first production
  activation: known-up, controlled down, controlled recovery, WPCOM notification
  parity, Veriflier-confirmed down, WAF/blocked-style behavior, and one
  customer-safe false-alarm/non-confirmation case.
- [ ] Run the probe-safety integration coverage in
  [`probe-safety-integration-test-plan.md`](probe-safety-integration-test-plan.md):
  authoritative DNS rebinding, unsafe redirect DNS, Veriflier probe-safety
  parity, TLS 1.0/1.1 advisory behavior, handshake failures, certificate
  pathologies, and slow or large TLS cases with proof that no WPCOM, alert,
  webhook, or customer downtime side effects occur.
- [ ] Build and run the production rollout lab with uptime-bench coordination.
  The lab should cover the DB primary/read-replica split, SVN-synced
  `db-servers.php`, Docker bridge access to host-local Monitor StatsD,
  Veriflier Compose StatsD/Graphite, WPCOM simulation, API rollout flow, and
  rollback.
- [ ] Move monitor runtime/config/event identity from per-`blog_id` state to
  explicit endpoint identity, using the existing `jetpack_monitor_site_id` row
  as the durable source during migration. This is required for the real
  production cohort where one `blog_id` has multiple active monitor URLs.

## Config And Compatibility Cleanup

Open work:

- [ ] Either wire `LOG_FORMAT=json` to a real structured runtime logger or
  remove the accepted value. Today the key validates for forward compatibility,
  but runtime logs still use the standard text logger.
- [ ] After production rollout, remove deprecated migration aliases from normal
  operator docs: `DB_UPDATES_ENABLE`, `BUCKET_NO_MIN` / `BUCKET_NO_MAX`, and
  the misspelled `VERIFIERS` / `grpc_port` Veriflier aliases. Keep parser
  support only as long as copied v1 configs are expected during rollback
  windows.
- [ ] After downstream consumers migrate, retire the legacy status projection:
  disable `LEGACY_STATUS_PROJECTION_ENABLE` and stop treating
  `jetpack_monitor_sites.site_status` as meaningful v2 state.

## Veriflier Validation And Future Probe Model

Source docs:

- [`production-veriflier-compose.md`](production-veriflier-compose.md)
- [`adr/0010-trusted-veriflier-discovery.md`](adr/0010-trusted-veriflier-discovery.md)
- [`v3-probe-agent-architecture-options.md`](v3-probe-agent-architecture-options.md)

Open work:

- [ ] Run production-like multi-vantage Veriflier soak coverage for deployed-like
  network behavior, duplicate-vantage misconfiguration, mixed-vantage responses,
  quorum-floor behavior, and long outage promotion/recovery. Earlier smoke
  setup proved three unique vantages could be counted, but not that every
  vantage reached the same controlled target.
- [ ] Revisit durable verifier/probe jobs after v2 production data. Keep v2
  confirmation probes simple for rollout; use collected latency, overload,
  false-alarm, and mixed-vantage evidence to decide whether v3 needs a central
  job bus for regional probe agents.
- [ ] Revisit the central scheduler plus regional probe-agent architecture only
  after v2 production data shows where current Monitors and Verifliers fall
  short.

## Telemetry And Operator Reporting

Open work:

- [ ] Revisit `jetmon2 telemetry report` thresholds and suggested actions after
  v2 has enough real production traffic to show which rates are normal.
- [ ] Add low-cardinality Jetmon-native bandwidth StatsD counters so capacity
  and real-site tests can compare request bytes, response header bytes, response
  body bytes read, and total application-level transfer by check method and
  detection profile. Keep this separate from host/container network accounting.
- [ ] Consider a dedicated dry-run projection-drift repair planner after
  production rehearsals show which drift classes are safe enough to automate.

## Detection And Scenario Coverage

Open work:

- [x] Add conservative semantic body detectors for common WordPress and hosting
  failure pages before full content baselining. The shipped detector set catches
  WordPress database, missing/corrupt database table, database repair, fatal
  PHP, unsupported PHP/database, setup/configuration, maintenance,
  database-update, and critical-error pages,
  default virtual-host pages, account-suspended pages, XML-RPC / Jetpack probe
  echo pages, WordPress directory listings, and near-empty HTML while keeping
  alerts explainable.
- [ ] Design a content-integrity baseline mode separately from explicit required
  or forbidden patterns. Production users need a controlled way to detect large
  unexpected body changes without turning normal content churn into false
  positives.
- [ ] Track DNS-specific benchmark scenarios separately from HTTP DNS failures.
  DNS-record, DNSSEC, split-horizon, CNAME-chain, authoritative nameserver, and
  DNS-latency monitors need a dedicated check type and event taxonomy before
  they should become production uptime signals.
- [ ] Decide whether Jetmon should add an explicit DNS monitor that bypasses or
  complements recursive resolver cache visibility. Direct authoritative checks
  can catch short DNS outages, but they increase query load and may report
  failures users do not observe until caches expire.
- [ ] Validate geo-scoped benchmark assumptions before changing production
  behavior for `http-geo-503`. Confirm probe source ranges, intended Jetmon
  region semantics, and the support story for partial regional failures.
- [ ] Replace global `NUM_OF_CHECKS` escalation gating with failure-class-aware
  Veriflier escalation. Keep local retries for ambiguous transport, timeout,
  and low-confidence DNS failures, but escalate high-confidence HTTP, TLS,
  redirect-policy, keyword, and full-profile failures sooner. Preserve
  failure-pressure guardrails so Verifliers are not overloaded during broad
  Monitor-side impairment. Deprecate `NUM_OF_CHECKS` after production evidence
  confirms the adaptive policy is safer than a single global retry count.

## Scheduler And Scalability

Source docs:

- [`jetmon-v2-scalability-test-plan.md`](jetmon-v2-scalability-test-plan.md)
- uptime-bench raw reports and handoffs in the sibling `uptime-bench` repo

Open work:

- [ ] Prototype latency/error-aware concurrency control for the streaming
  engine. The next major scheduler iteration should reduce dispatch before
  timeout pressure cascades, recover cleanly after target or network saturation,
  and distinguish Jetmon CPU headroom from downstream request-path saturation.
- [ ] Harden the streaming worker scaler against transient target spikes and
  backlog overreaction with stronger damping, error-rate guardrails, and
  per-host resource feedback.
- [ ] Run bracket capacity tests around 2.5 million and 3 million active sites
  on internal-only targets before attempting another 4 million-plus run.
- [ ] Prototype sharded result ingestion only if bracket tests show result
  handling, not request-path saturation, is the next bottleneck.
- [ ] Redesign broad transport failure-storm suppression for the streaming
  engine. The new design must preserve the v2 `Unknown` principle for
  monitor-side impairment, keep operator-visible audit/metrics evidence, and
  avoid hiding real customer-site outages.
- [ ] Expand prepared request/runtime caches for the checker hot path. Cache
  parsed URL/host metadata, normalized headers, keyword rules, and reusable
  per-site request material in memory so repeated checks spend less CPU
  rebuilding immutable request state.
- [ ] Add memory-backed success rollups before database persistence. Keep
  event/failure writes durable, but aggregate healthy probe latency/status
  summaries in memory and flush compact rollups.
- [ ] Evaluate larger DNS and HTTP connection caches for steady-state checks.
  Future capacity runs should test whether a larger idle-connection budget,
  longer safe idle timeout, or per-resolved-target cache reduces CPU/TCP churn
  without creating FD pressure or unsafe HTTPS/SNI reuse.
- [ ] Use uptime-bench process/device I/O attribution before making the next
  storage optimization decision. Host disk I/O rises sharply in larger v2
  reports, but container block counters alone do not identify the writer/reader.
- [ ] Move streaming scheduler persistence from broad legacy-table reloads to
  `jetpack_monitor_check_targets` plus change detection. The table exists; the
  first prototype still reloads active identity/cadence from
  `jetpack_monitor_sites` plus v2 sidecar config for correctness.
- [ ] Add uptime-bench scenarios for streaming mode that explicitly validate
  phase-spread scheduling, bounded rollback freshness staleness,
  verifier promotion/recovery, failure-history retention, and steady-state
  write volume over multi-hour runs.
- [ ] Prototype a bounded asynchronous check-history writer and rollup model:
  keep runtime freshness synchronous, preserve raw rows for failures/recent
  windows, and store long-term latency/error aggregates so raw history does not
  become the 10k/100k-site storage and I/O wall.
- [ ] Run a 5k/10k capacity ladder after each major scalability change,
  recording freshness, p95 age, MySQL CPU, MySQL I/O/network, `jetmon2`
  CPU/RSS/FDs, StatsD CPU, Veriflier CPU, and check-history row growth.
- [ ] After capacity retests, add `validate-config` sizing advice that explains
  expected throughput from active site count, check interval, worker settings,
  and timeout settings.
- [ ] After capacity retests, evaluate whether checker idle-connection limits,
  response-body draining, or keep-alive policy need additional tuning. This is
  data-dependent because more aggressive connection reuse can hide DNS/TCP/TLS
  failure modes or add page-body I/O.

## Delivery, WPCOM, And Secret Handling

Source docs:

- [`jetmon-deliverer-rollout.md`](jetmon-deliverer-rollout.md)
- [`outbound-credential-encryption-plan.md`](outbound-credential-encryption-plan.md)
- [`adr/0003-plaintext-credentials-for-outbound-dispatch.md`](adr/0003-plaintext-credentials-for-outbound-dispatch.md)

Open work:

- [ ] Extract `jetmon-deliverer` in production when delivery scale or blast
  radius warrants it. The binary exists; remaining work is deployment-system
  adoption and host-specific config wiring.
- [ ] Unify webhook and alerting dispatch plumbing after production evidence.
  Keep packages separate until there are two proven implementations and WPCOM
  migration provides the third transport path.
- [ ] Migrate WPCOM notifications behind alert contacts/deliverer only after
  alert contacts have proven stable and recipient parity has been verified.
- [ ] Handle permanent WPCOM status failures without tripping the global
  circuit. Permanent responses such as 404/410 should be reported and audited,
  but should not open the shared WPCOM circuit breaker or create pointless
  retry pressure.
- [ ] Encrypt outbound credentials at rest with application-level envelope
  encryption. Webhook HMAC secrets and alert-contact destination credentials
  are plaintext today by ADR; the staged plan preserves current behavior while
  adding dual-write, backfill, encrypted-required, and plaintext-removal phases.
- [ ] Flip `WPCOM_NOTIFY_LEGACY_INSECURE_SKIP_VERIFY` to default `false` after
  the production WPCOM legacy endpoint is confirmed to pass normal TLS
  verification from the production container network.

## API And Customer-Facing Surface

Source docs:

- [`internal-api-reference.md`](internal-api-reference.md)
- [`public-api-gateway-tenant-contract.md`](public-api-gateway-tenant-contract.md)
- [`adr/0002-internal-only-api-behind-gateway.md`](adr/0002-internal-only-api-behind-gateway.md)

Open work:

- [ ] Backfill and reconcile `jetpack_monitor_site_tenants` from the
  gateway/customer source of truth before customer traffic depends on
  Jetmon-side site enforcement.
- [ ] Add public-contract integration tests for tenant success and denial paths
  across sites, events, stats, trigger-now, webhooks, and alert contacts.
- [ ] Add customer-safe error and metadata redaction paths for every route that
  could be exposed through a public/customer contract.
- [ ] Promote the internal route-driven `GET /api/v1/openapi.json` contract into
  a public compatibility policy with deprecation rules and consumer-specific
  generator validation.
- [ ] Add public-contract integration tests for auth, pagination, idempotency,
  redaction, and trigger-now abuse controls.
- [ ] Revisit response-time/SLA pre-aggregation before exposing high-volume
  public reporting queries.
- [ ] Document the migration path for consumers that currently use direct MySQL
  or bespoke internal integrations.
- [ ] Move API idempotency and rate-limit state out of process before running
  multiple API-serving instances behind a non-sticky load balancer.

## Deferred Product Features

These are intentionally not rollout blockers. Revisit them when customer demand,
compliance requirements, or production evidence justifies the added surface.

- [ ] Add `site.state_changed` rollup webhook events if consumers need
  site-level state transitions instead of deriving them from event-level
  webhooks.
- [ ] Add grace-period webhook secret rotation if customers need routine secret
  rotation without brief signature-verification failures.
- [ ] Add SMS notifications only if direct SMS is required and a stable sending
  channel is available.
- [ ] Add OpsGenie transport only when a customer asks for direct OpsGenie
  integration.
- [ ] Add quiet hours or on-call schedules only with a clear scope that does not
  duplicate PagerDuty.
- [ ] Add cross-channel alert acknowledgements only if customers need Jetmon to
  ingest PagerDuty/Slack acknowledgement callbacks.
- [ ] Add alert grouping or digest mode if real users see pager noise during
  regional outages despite per-contact `max_per_hour` limits.

## Post-v2 Architecture

Open work:

- [ ] Keep the v2 deployment target conservative until the backend replacement
  is stable in production.
- [ ] Split stateless concerns off the Monitor only when the operational trigger
  appears:
  - API hosts when public/API request volume needs independent scaling.
  - Deliverer hosts when outbound event volume or blast radius warrants it.
  - Dashboard hosts when operator sessions or deploy coupling warrant it.
  - A separate per-check telemetry store when product requirements need
    high-resolution response-time history.
- [ ] Move per-check telemetry off the shared OLTP database before enabling
  `CHECK_HISTORY_MODE=all` fleet-wide.
- [ ] Choose public names for any post-v2 probe-agent roles before introducing
  them; do not rename the v2 `veriflier2` compatibility binary in place during
  rollout.

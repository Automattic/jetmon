# Jetmon v2 Prelaunch Readiness

This is the stop/go tracker for the first production Jetmon v2 rollout. It is
not the rollout runbook. Use it to confirm that owners, evidence, and thresholds
are ready before the first production activation.

Primary runbooks and references:

- [v1-to-v2-migration.md](v1-to-v2-migration.md): staged migration and rollback
  process.
- [rollout-quick-reference.md](rollout-quick-reference.md): command sequence
  for API-guided rollout.
- [production-teamcity-rollout.md](production-teamcity-rollout.md): production
  Docker/TeamCity deployment details.
- [production-veriflier-compose.md](production-veriflier-compose.md): v2
  Veriflier Compose deployment.
- [production-rollout-lab.md](production-rollout-lab.md): production-shaped lab
  test plan.
- [operations-guide.md](operations-guide.md): steady-state operator workflows.
- [roadmap.md](roadmap.md): deferred work that should not block the drop-in
  rollout.

## Launch Posture

The first production rollout is a backend replacement for the existing
WordPress.com Monitor service.

Default launch behavior should preserve current customer-visible Monitor
semantics, WPCOM notification behavior, legacy status projection, support
workflows, and allowlist expectations. V2-only surfaces such as alert contacts,
customer webhooks, public API access, paid reports, trigger-now, and richer
customer state labels stay hidden, internal-only, or disabled unless a separate
WPCOM/Product canary explicitly enables them.

## Status And Owners

Status:

- `[ ]` not done
- `[~]` in progress
- `[x]` done
- `[!]` unresolved blocker or rollout risk

Owners:

- `Jetmon`: service code, rollout tooling, and service docs
- `Systems`: production deployment, host, DB, and observability ownership
- `WPCOM`: WordPress.com Monitor/API/platform ownership
- `Jetpack`: Jetpack Monitor ownership
- `Support`: support tooling, macros, and frontline readiness
- `Product`: customer-facing semantics, packaging, and launch language

## Launch-Critical Checklist

Do not open the first production rollout window until every item has an owner,
evidence location, and stop/go threshold.

- [ ] `WPCOM`, `Product`, `Support`: approve launch wording as a backend
  replacement, not a new customer-facing Monitor launch.
- [ ] `Systems`: approve the additive schema package and confirm that service
  rollback does not roll back schema changes.
- [ ] `Jetmon`, `Systems`: deploy a fresh v2 Veriflier fleet, validate `/v2`
  endpoints, confirm stable `vantage_id` values, and approve quorum/floor
  behavior.
- [ ] `Jetmon`, `Systems`: deploy v2 Monitors in standby or API-controlled mode
  and confirm they do not claim buckets, run scheduled checks, deliver alerts,
  notify WPCOM, or mutate site state before activation.
- [ ] `Jetmon`, `Systems`: rehearse API-guided rollout and rollback, including
  API keys/scopes, `--allow-remote`, transcript location, and operator access
  from outside the container host.
- [ ] `Jetmon`, `Systems`: validate production DB access through either
  `DB_SERVER_MAP_PATH` or explicit DB config, including bad-map rejection and
  hot reload behavior for server-map mode.
- [ ] `Jetmon`, `Systems`: validate MariaDB runtime paths against the production
  patch range, not just migrations. Cover bucket ownership, runtime freshness,
  SSL expiry updates, `ON DUPLICATE KEY UPDATE ... VALUES(...)`, and delivery
  row claims.
- [ ] `Jetmon`, `WPCOM`: verify projection parity for `Seems Down` -> `0`,
  confirmed `Down` -> `2`, and closed/up -> `1`.
- [ ] `Jetmon`, `WPCOM`: verify WPCOM legacy notification parity for down,
  confirmed down, false alarm, recovery, inactive, URL mismatch, and
  blacklisted-site cases.
- [ ] `Jetmon`, `WPCOM`: validate WPCOM legacy client certificate/key mount,
  `/jetmon/?data=...` request shape, response parsing, retries, circuit
  breaker behavior, and secret redaction in logs, dashboard/API output, and
  rollout transcripts.
- [ ] `Support`, `WPCOM`, `Jetmon`: approve support language for v2 GET checks,
  HEAD compatibility mode, `jetmon/2.0`, WAF/bot blocks, false positives,
  maintenance windows, and monitor-side `Unknown`.
- [ ] `Jetmon`, `Systems`: run approved synthetic canaries before launch:
  known-up, controlled down, controlled recovery, WPCOM notification parity,
  Veriflier-confirmed down, WAF/blocked-style case, and one customer-safe
  false-alarm/non-confirmation case.
- [ ] `Systems`, `Jetmon`: approve stop/go thresholds for projection drift,
  missed checks, oldest selected age, stale process heartbeats, WPCOM failures,
  API errors, MySQL errors, delivery backlog, Veriflier health/quorum, and
  bucket ownership.
- [ ] `Jetmon`, `Systems`: complete or explicitly waive failure drills for API
  unavailable, Veriflier degraded, WPCOM unavailable, MySQL errors, delivery
  backlog, stale heartbeat, bad deploy rollback, WAF false positive, and
  monitor-side `Unknown`.
- [ ] `Jetmon`: ensure probe-safety follow-ups are tracked before rollout:
  scheduled site-safety reporting, authoritative DNS rebinding tests, deeper
  TLS pathology tests, and optional streaming keyword short-circuiting.

## External Decisions

These can block rollout even when code and labs are green.

| Decision | Owner | Needed Output |
| --- | --- | --- |
| Launch posture wording | `WPCOM`, `Product`, `Support` | Written approval for backend-replacement launch language. |
| Legacy consumer inventory | `WPCOM`, `Jetpack`, `Support` | Confirmed readers that still need legacy projection or notification behavior. |
| WPCOM notification parity | `WPCOM`, `Jetmon` | Pass/fail evidence for inactive, URL mismatch, blacklist, hook-consumer, and home-URL-only cases. |
| Canary expansion thresholds | `WPCOM`, `Product`, `Support`, `Systems`, `Jetmon` | Starting cohort, hold duration, expansion sizes, rollback triggers, and go/no-go owner. |
| Systems thresholds | `Systems`, `Jetmon` | Numeric thresholds for freshness, drift, WPCOM failures, DB errors, Veriflier health, API errors, and backlog. |
| Probe-safety gaps | `Jetmon`, `Systems` | Accepted follow-up links or explicit rollout-owner waiver. |

## Evidence Packet

Collect links to these artifacts in the rollout record:

- Approved launch posture and support-facing wording.
- Schema approval and migration transcript.
- v2 Veriflier fleet validation: `/v2/status`, stable vantage IDs, quorum
  behavior, auth posture, and capacity evidence.
- API rollout dry run or guided transcript, including config source, auth
  scope, `--allow-remote`, transcript path, and rollback path.
- Projection drift output and telemetry parity report against production-like
  data.
- WPCOM legacy notification parity evidence for Jetmon-owned and WPCOM-owned
  cases.
- Synthetic canary evidence for direct probe expectations plus lifecycle
  evidence for recovery, WPCOM parity, and Veriflier-confirmed down.
- Failure drill notes with expected behavior, observed behavior, and waivers.
- Production rollout lab report covering DB server-map reload,
  StatsD/Graphite wiring, WPCOM simulator behavior, standby activation,
  rollback, and proof that no real WPCOM traffic escaped the lab.
- Probe-safety follow-up links for site-safety reporting, DNS rebinding, TLS
  pathology, and keyword streaming optimization.

## Stop/Go Threshold Worksheet

Use this as the starting point. Systems and Jetmon should replace "proposed"
values with approved rollout-room thresholds.

| Signal | Proposed starting point | Hold action |
| --- | --- | --- |
| Projection drift | 0 unexpected drift rows in the canary range after one full v2 round | Pause expansion; compare event rows, projection rows, and v1 expectations. |
| Missed checks | 0 missed checks for the canary range after one expected interval | Pause expansion; inspect scheduler selected/completed/outstanding metrics. |
| Oldest selected age | No selected site older than 2x its expected check interval plus timeout/retry buffer | Hold cohort; inspect scheduler queue depth and DB latency. |
| Stale heartbeat | 0 active rollout hosts stale beyond `BUCKET_HEARTBEAT_GRACE_SEC` | Stop host expansion; confirm bucket ownership. |
| WPCOM failures | 0 unexpected failures for down/confirmed-down/recovery canaries; circuit breaker closed | Pause and keep legacy projection active until WPCOM/API failure is resolved. |
| Delivery backlog | Stable or decreasing backlog; oldest due delivery inside agreed retry ladder | Hold delivery-owner changes; verify `DELIVERY_OWNER_HOST` and deliverer health. |
| API errors | Required health, dashboard, and rollout API smoke checks pass without sustained 5xx | Keep API internal; investigate before automation depends on it. |
| MySQL errors | No sustained connection failures, query errors, or lock-wait spikes | Pause host changes; review DB health. |
| Veriflier agreement | Quorum floor intact and health loss explained | Pause confirmed-down expansion; avoid customer-visible notifications from degraded quorum. |

## Canary Plan

Treat the first canary as a parity canary, not a feature canary. Expand only
when backend-replacement signals stay inside approved thresholds.

Recommended cohorts:

| Cohort | Why it matters | Rollback trigger |
| --- | --- | --- |
| WPCOM-hosted | Lowest external-network variability; validates core parity first | Any unexplained drift, notification delta, or missed-check pattern. |
| Atomic | Exercises managed hosting plus customer-specific layers | Repeated blocked/redirect classes without support-ready explanation. |
| Self-hosted Jetpack | Highest network/plugin variability | Unexplained Veriflier disagreement or customer-facing notification mismatch. |
| Agency-managed | High support impact and false-positive sensitivity | Any repeated false-positive class support cannot explain. |
| WAF/security-plugin | Validates v2 GET allowlist language | Broad block caused by v2 UA/source not being allowlisted. |
| Historically noisy/flaky | Tests retry, cooldown, and Veriflier value | Regression versus v1/v2 baseline for noisy classes. |
| High-traffic | Catches performance-sensitive GET-path behavior | Sustained timeout/intermittent class not explained by site telemetry. |
| Multi-endpoint | Exercises event identity and rollup expectations | Duplicate customer-visible incidents or unclear support explanation. |

Required prelaunch synthetic cases:

- known-up site
- controlled down
- controlled recovery
- WPCOM notification parity
- Veriflier-confirmed down
- WAF/blocked-style failure
- customer-safe false alarm or non-confirmation

## Legacy Consumer Inventory

The first-pass local-search inventory found these likely consumers. WPCOM,
Jetpack, and Support owners still need to confirm hidden consumers and decide
which paths require legacy projection during the drop-in rollout.

| Consumer group | Data source | Owner |
| --- | --- | --- |
| WPCOM Monitor library and REST status/settings/incidents/uptime endpoints | `jetpack_monitor_sites.site_status`, `last_status_change`, monitor URL, incidents | `WPCOM` |
| WPCOM notification hooks and notification senders | `jetpack_monitor_site_status_change`, raw status, checks payload | `WPCOM` |
| Activity Log monitor up/down activities | status-change hook payload | `WPCOM` |
| Jetpack Agency search/status surfaces | indexed monitor status and `last_status_change` | `WPCOM` |
| Support/explanation helpers | monitor status, incidents, explanation data | `Support`, `WPCOM` |
| Jetpack plugin Monitor module and related UI | WPCOM XML-RPC monitor methods and module state | `Jetpack` |
| Jetpack Sync defaults | `monitor_receive_notifications` option | `Jetpack` |

Do not disable `LEGACY_STATUS_PROJECTION_ENABLE` until every customer-visible
reader has migrated or explicitly accepted removal of the legacy projection.

## Current Jetmon-Owned Evidence

These items are covered by Jetmon-owned tests or labs, but still need rollout
owners to decide whether the evidence is sufficient for production:

- `make rollout-docs-verify` checks rollout help output, generated rehearsal
  plans, guided dry runs, JSON command output, and staged systemd verification.
- Docker scale/resilience labs covered multi-Monitor behavior, graceful and hard
  Monitor loss, Veriflier degradation/recovery, DB restart, runtime table lock,
  read-only DB mode, and DB pause.
- `scripts/v2-soak-lab.sh run` passed a 1,200-site 10-minute soak with no
  WPCOM audit rows, webhooks, alert contacts, or Mailpit messages.
- `scripts/api-cli-public-fixture-validate.sh run` covered alert-contact
  send-test, webhook HMAC delivery/signature verification, HTTP-500 failure
  assertions, and target safety.
- Unit tests cover legacy WPCOM payload shape, confirmed-down notification,
  recovery notification, Seems Down not notifying before confirmation, false
  alarm suppression, maintenance/cooldown suppression, and telemetry report
  down/recovery deltas.
- Host and fleet dashboards expose process heartbeats, bucket ownership,
  delivery posture, projection drift, dependency health, Veriflier v2 contract
  status, trusted vantages, agent telemetry, capacity, discovery posture, and
  next actions.
- `jetmon2 verifliers discovery-report` provides a read-only static/registry
  comparison gate without printing auth token values.

Known evidence gaps:

- Multi-physical-host drills should still run across service hosts when they
  are free, or be explicitly waived.
- WPCOM-owned inactive, URL mismatch, blacklist, hook-consumer, and
  home-URL-only cases still need WPCOM acceptance.
- MariaDB runtime validation and DB server-map reload validation need
  production-shaped evidence.

## Drop-In Non-Blockers

These are important but should not block the v1 replacement unless launch scope
changes:

- public/customer API exposure
- paid Monitor packaging and paid SLA/reporting
- rich Jetpack Monitor UI changes
- customer alert-contact, webhook, Slack, Teams, or PagerDuty self-service
- trigger-now customer access
- reverse checks, DNS/domain monitoring expansion, and v3 probe-agent work
- quiet hours, digests, grouping, and acknowledgements
- legacy projection retirement

Track these in [roadmap.md](roadmap.md) instead of expanding the launch scope.

## Launch-Day Readiness Card

Fill this out before the rollout attempt.

| Question | Answer |
| --- | --- |
| Rollout mode | TBD |
| First cohort/bucket range | TBD |
| Rollout owner | TBD |
| WPCOM owner | TBD |
| Systems owner | TBD |
| Support contact | TBD |
| Start time | TBD |
| First hold point | TBD |
| Expected first full-round duration | TBD |
| Projection drift threshold | TBD |
| WPCOM notification failure threshold | TBD |
| Missed check threshold | TBD |
| Oldest selected age threshold | TBD |
| Delivery backlog threshold | TBD |
| Rollback command source | TBD |
| Customer/support comms status | TBD |

## Immediate Closure Path

1. Get written launch-posture approval from WPCOM, Product, and Support.
2. Close external decisions for legacy consumers, WPCOM-owned parity cases,
   canary expansion thresholds, Systems thresholds, and probe-safety tracking.
3. Run projection drift and telemetry parity against production-like active-site
   data with WPCOM notification evidence.
4. Run approved synthetic canaries through the API gates and attach separate
   lifecycle evidence for recovery, WPCOM parity, and Veriflier-confirmed down.
5. Complete the production rollout lab or record explicit owner waivers for
   remaining lab gaps.
6. Fill out the launch-day readiness card and use it as the rollout-room
   stop/go source.

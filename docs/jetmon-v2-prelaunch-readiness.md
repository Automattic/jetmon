# Jetmon v2 Prelaunch Readiness

Use this as the stop/go tracker before the first production Jetmon v2
activation. It is not the rollout runbook. It answers: who approved the launch,
where is the evidence, and what thresholds stop the rollout?

Primary references:

- [v1-to-v2-migration.md](v1-to-v2-migration.md): staged migration and rollback.
- [rollout-quick-reference.md](rollout-quick-reference.md): API-guided command
  sequence.
- [production-teamcity-rollout.md](production-teamcity-rollout.md): production
  Docker/TeamCity deployment details.
- [production-veriflier-compose.md](production-veriflier-compose.md): v2
  Veriflier Compose deployment.
- [production-rollout-lab.md](production-rollout-lab.md): production-shaped lab.
- [operations-guide.md](operations-guide.md): steady-state operations.
- [roadmap.md](roadmap.md): deferred, non-blocking work.

## Launch Posture

The first rollout is a backend replacement for the existing WordPress.com
Monitor service. It must preserve customer-visible Monitor semantics, WPCOM
notification behavior, legacy status projection, support workflows, and
allowlist expectations.

Keep v2-only product surfaces internal or disabled unless a separate
WPCOM/Product canary explicitly enables them: alert contacts, customer
webhooks, public API access, paid reports, trigger-now, richer customer state
labels, and post-rollout GET/full behavior.

## Owners

- `Jetmon`: service code, rollout tooling, docs, and lab evidence.
- `Systems`: production deployment, hosts, database, observability, and rollout
  execution.
- `WPCOM`: WordPress.com Monitor/API/platform ownership and legacy consumers.
- `Jetpack`: Jetpack Monitor integration and plugin-facing behavior.
- `Support`: macros, explanation workflows, and frontline readiness.
- `Product`: customer-facing launch language and packaging.

## Launch-Critical Gates

Do not open the first production rollout window until each item has an owner,
evidence link, and stop/go threshold.

- [ ] `WPCOM`, `Product`, `Support`: approve backend-replacement launch wording.
- [ ] `Systems`: approve additive schema package and confirm service rollback
  will not roll back schema changes.
- [ ] `Jetmon`, `Systems`: deploy and validate fresh v2 Verifliers: `/v2`
  endpoints, auth, stable `vantage_id`, quorum floor, capacity, and dashboard
  visibility.
- [ ] `Jetmon`, `Systems`: deploy v2 Monitors in standby/API-controlled mode
  and prove they do not claim buckets, run scheduled checks, deliver alerts,
  notify WPCOM, or mutate site state before activation.
- [ ] `Jetmon`, `Systems`: rehearse API-guided rollout and rollback, including
  API key scope, `--allow-remote`, transcript path, and operator access from
  outside container hosts.
- [ ] `Jetmon`, `Systems`: validate DB access through `DB_SERVER_MAP_PATH` or
  explicit DB config, including bad-map rejection and hot reload in server-map
  mode.
- [ ] `Jetmon`, `Systems`: validate MariaDB runtime paths, not just migrations:
  bucket ownership, runtime freshness, SSL expiry updates, `ON DUPLICATE KEY
  UPDATE ... VALUES(...)`, and delivery row claims.
- [ ] `Jetmon`, `WPCOM`: verify projection parity:
  `Seems Down -> 0`, confirmed `Down -> 2`, closed/up -> `1`.
- [ ] `Jetmon`, `WPCOM`: verify WPCOM legacy notification parity for down,
  confirmed down, false alarm, recovery, inactive, URL mismatch, blacklisted
  site, hook consumer, and home-URL-only cases.
- [ ] `Jetmon`, `WPCOM`: validate legacy WPCOM cert/key mount, request shape,
  response parsing, retries, circuit breaker behavior, and secret redaction.
- [ ] `Support`, `WPCOM`, `Jetmon`: approve support language for v2 GET checks,
  HEAD compatibility, `jetmon/2.0`, WAF/bot blocks, false positives,
  maintenance windows, and monitor-side `Unknown`.
- [ ] `Jetmon`, `Systems`: run approved synthetic canaries: known-up,
  controlled down, controlled recovery, WPCOM parity, Veriflier-confirmed down,
  WAF/blocked-style case, and customer-safe false alarm/non-confirmation.
- [ ] `Systems`, `Jetmon`: approve numeric thresholds for projection drift,
  missed checks, oldest selected age, stale heartbeats, WPCOM failures, API
  errors, MySQL errors, delivery backlog, Veriflier health/quorum, and bucket
  ownership.
- [ ] `Jetmon`, `Systems`: complete or explicitly waive drills for API
  unavailable, Veriflier degraded, WPCOM unavailable, MySQL errors, delivery
  backlog, stale heartbeat, bad deploy rollback, WAF false positive, and
  monitor-side `Unknown`.
- [ ] `Jetmon`: track probe-safety follow-ups: scheduled site-safety reporting,
  authoritative DNS rebinding tests, deeper TLS pathology tests, and optional
  streaming keyword short-circuiting.

## External Decisions

| Decision | Owner | Needed output |
| --- | --- | --- |
| Launch wording | `WPCOM`, `Product`, `Support` | Written approval for backend-replacement language. |
| Legacy consumer inventory | `WPCOM`, `Jetpack`, `Support` | Readers that still need legacy projection or notification behavior. |
| WPCOM parity | `WPCOM`, `Jetmon` | Pass/fail evidence for inactive, URL mismatch, blacklist, hook-consumer, and home-URL-only cases. |
| Canary expansion | All rollout owners | Starting cohort, hold duration, expansion sizes, rollback triggers, and go/no-go owner. |
| Systems thresholds | `Systems`, `Jetmon` | Numeric stop/go thresholds for freshness, drift, WPCOM, DB, Veriflier, API, and backlog signals. |
| Probe-safety gaps | `Jetmon`, `Systems` | Follow-up links or explicit rollout-owner waiver. |

## Evidence Packet

Attach these to the rollout record:

- launch-posture and support-language approval
- schema approval and migration transcript
- v2 Veriflier fleet validation and capacity evidence
- API-guided rollout dry run, config source, auth scope, transcript path, and
  rollback proof
- projection drift output and telemetry parity report
- WPCOM legacy notification parity evidence
- synthetic canary results for direct probes, recovery, WPCOM parity, and
  Veriflier-confirmed down
- failure drill notes with observed behavior and waivers
- production rollout lab report covering DB server-map reload, StatsD/Graphite,
  WPCOM simulator, standby activation, rollback, and proof that no real WPCOM
  traffic escaped the lab
- probe-safety follow-up links

## Stop/Go Thresholds

Systems and Jetmon should replace the starting points with approved rollout-room
values.

| Signal | Starting point | Hold action |
| --- | --- | --- |
| Projection drift | 0 unexpected rows in the canary range after one full v2 round | Pause expansion; compare event rows, projection rows, and v1 expectations. |
| Missed checks | 0 missed checks for the canary range after one expected interval | Pause; inspect selected/completed/outstanding metrics. |
| Oldest selected age | No selected site older than 2x expected interval plus timeout/retry buffer | Hold cohort; inspect queue depth and DB latency. |
| Stale heartbeat | 0 active rollout hosts stale beyond `BUCKET_HEARTBEAT_GRACE_SEC` | Stop host expansion; confirm bucket ownership. |
| WPCOM failures | 0 unexpected failures for down/confirmed-down/recovery canaries; circuit closed | Pause and keep legacy projection active. |
| Delivery backlog | Stable/decreasing backlog; oldest due delivery inside retry ladder | Hold delivery-owner changes; verify `DELIVERY_OWNER_HOST`. |
| API errors | Required health/dashboard/rollout API checks pass without sustained 5xx | Keep API internal; investigate. |
| MySQL errors | No sustained connection failures, query errors, or lock-wait spikes | Pause host changes; review DB health. |
| Veriflier agreement | Quorum floor intact and health loss explained | Pause confirmed-down expansion. |

## Canary Plan

Treat the first canary as a parity canary, not a feature canary. Expand only
when backend-replacement signals stay inside approved thresholds.

| Cohort | Why it matters | Rollback trigger |
| --- | --- | --- |
| WPCOM-hosted | Lowest external-network variability | Unexplained drift, notification delta, or missed-check pattern. |
| Atomic | Managed hosting plus customer-specific layers | Repeated blocked/redirect class without support-ready explanation. |
| Self-hosted Jetpack | Highest network/plugin variability | Unexplained Veriflier disagreement or notification mismatch. |
| Agency-managed | High support impact and false-positive sensitivity | Repeated false-positive class Support cannot explain. |
| WAF/security-plugin | Validates v2 GET allowlist language | Broad block caused by v2 UA/source not being allowlisted. |
| Historically noisy/flaky | Tests retry, cooldown, and Veriflier value | Regression versus v1/v2 baseline. |
| High-traffic | Catches performance-sensitive GET-path behavior | Sustained timeout/intermittent class not explained by site telemetry. |
| Multi-endpoint | Exercises event identity and rollup expectations | Duplicate customer-visible incidents or unclear explanation. |

Required synthetic cases: known-up, controlled down, controlled recovery, WPCOM
parity, Veriflier-confirmed down, WAF/blocked-style failure, and customer-safe
false alarm/non-confirmation.

## Legacy Consumer Inventory

WPCOM, Jetpack, and Support owners must confirm hidden consumers and decide
which paths require legacy projection during rollout.

| Consumer group | Data source | Owner |
| --- | --- | --- |
| WPCOM Monitor library and REST status/settings/incidents/uptime endpoints | `jetpack_monitor_sites.site_status`, `last_status_change`, monitor URL, incidents | `WPCOM` |
| WPCOM notification hooks and notification senders | `jetpack_monitor_site_status_change`, raw status, checks payload | `WPCOM` |
| Activity Log monitor up/down activities | status-change hook payload | `WPCOM` |
| Jetpack Agency search/status surfaces | indexed monitor status and `last_status_change` | `WPCOM` |
| Support/explanation helpers | monitor status, incidents, explanation data | `Support`, `WPCOM` |
| Jetpack plugin Monitor module and UI | WPCOM XML-RPC monitor methods and module state | `Jetpack` |
| Jetpack Sync defaults | `monitor_receive_notifications` option | `Jetpack` |

Do not disable `LEGACY_STATUS_PROJECTION_ENABLE` until every customer-visible
reader has migrated or explicitly accepted removal of the legacy projection.

## Jetmon-Owned Evidence

Available Jetmon-owned evidence:

- `make rollout-docs-verify` checks rollout help, generated plans, guided dry
  runs, JSON command output, and staged systemd verification.
- Docker scale/resilience labs cover multi-Monitor behavior, graceful/hard
  Monitor loss, Veriflier degradation/recovery, DB restart, runtime-table lock,
  read-only DB mode, and DB pause.
- The v2 soak lab covers multi-Monitor steady state with no WPCOM audit rows,
  webhooks, alert contacts, or Mailpit messages.
- API CLI fixture validation covers alert-contact send-test, webhook HMAC
  delivery/signature verification, HTTP-500 failure assertions, and target
  safety.
- Unit tests cover legacy WPCOM payload shape, confirmed-down notification,
  recovery notification, false-alarm suppression, maintenance/cooldown
  suppression, and telemetry down/recovery deltas.
- Dashboards expose process heartbeats, bucket ownership, delivery posture,
  projection drift, dependency health, Veriflier contract status, trusted
  vantages, agent telemetry, capacity, discovery posture, and next actions.

Known evidence gaps:

- Multi-physical-host drills across service hosts, or explicit waiver.
- WPCOM-owned inactive, URL mismatch, blacklist, hook-consumer, and
  home-URL-only acceptance.
- MariaDB runtime validation and DB server-map reload evidence from a
  production-shaped lab.

## Drop-In Non-Blockers

Track these in [roadmap.md](roadmap.md), not in the launch scope:

- public/customer API exposure
- paid Monitor packaging and paid SLA/reporting
- rich Jetpack Monitor UI changes
- customer alert contacts, webhooks, Slack, Teams, or PagerDuty self-service
- trigger-now customer access
- reverse checks, DNS/domain monitoring expansion, and v3 probe-agent work
- quiet hours, digests, grouping, and acknowledgements
- legacy projection retirement

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

## Closure Path

1. Get written launch-posture approval from WPCOM, Product, and Support.
2. Close external decisions for legacy consumers, WPCOM parity cases, canary
   expansion, Systems thresholds, and probe-safety tracking.
3. Run projection drift and telemetry parity against production-like active-site
   data with WPCOM notification evidence.
4. Run approved synthetic canaries through the API gates.
5. Complete the production rollout lab or record explicit owner waivers.
6. Fill out the readiness card and use it as the rollout-room stop/go source.

# Jetmon v2 Prelaunch Readiness

This tracker captures the service-side work needed before attempting the
production Jetmon v2 rollout as a drop-in replacement for Jetmon v1.

The launch posture is intentionally conservative: this rollout upgrades the
monitoring backend. It does not launch a new customer-facing Monitor product,
public API, paid reporting surface, customer-managed alerting, or customer
webhook self-service unless those are explicitly enabled through a separate
WPCOM/Product canary.

## Draft Launch Posture Statement

Jetmon v2 should launch first as a backend replacement for the existing
WordPress.com Monitor service. The rollout should preserve current
customer-facing behavior, WPCOM notification semantics, legacy status
projection, support workflows, and allowlist expectations unless a specific
customer-visible change has an owner, a canary plan, support language, and a
rollback path.

During the drop-in rollout, v2-only surfaces such as alert contacts, customer
webhooks, public API access, paid reporting, trigger-now, and richer customer
state labels should remain hidden, internal-only, or disabled by default. Those
features can move forward in separate WPCOM/Product canaries after the backend
replacement is stable.

## Status Key

- `[ ]` not started
- `[~]` in progress
- `[x]` complete
- `[!]` blocker or unresolved launch risk

## Owner Key

- `Jetmon`: Jetmon v2 service, rollout tooling, and service documentation
- `Systems`: production deployment, host, DB, and observability ownership
- `WPCOM`: WordPress.com Monitor/API/platform ownership
- `Jetpack`: Jetpack Monitor ownership
- `Support`: support documentation, support tooling, and frontline readiness
- `Product`: customer-facing semantics, packaging, and launch language

## How To Use This Tracker

Use the launch-critical checklist as the stop/go view for the first production
activation. The detailed sections below are evidence buckets, not separate
parallel checklists. For each open launch-critical item, record three things
before the rollout window starts: owner, evidence location, and the threshold
that turns the item into a stop.

Keep approval work separate from engineering evidence. A passing lab does not
approve customer-facing semantics, and an owner approval does not replace a
rollout transcript.

## Launch Posture Gate

Hard gate.

- [x] Owner: `Jetmon`, `WPCOM`, `Product` - Draft that the first rollout
  is a backend replacement with current customer-facing behavior preserved by
  default.
- [ ] Owner: `WPCOM`, `Product` - Approve or revise the draft launch posture
  statement for the rollout room and support handoff.
- [ ] Owner: `Jetmon` - Keep `LEGACY_STATUS_PROJECTION_ENABLE=true` for the
  rollout.
- [ ] Owner: `WPCOM` - Keep the existing customer-facing status and
  notification behavior as the default.
- [ ] Owner: `Jetmon`, `WPCOM` - Keep v2 alert contacts, customer webhooks,
  public API access, and paid-reporting surfaces disabled or inaccessible by
  default.
- [ ] Owner: `Product`, `Support` - Confirm launch language such as
  "monitoring backend upgrade" rather than "new Monitor product".

Exit criteria:

- One written rollout posture statement exists.
- Every team knows which v2 features are hidden during the drop-in rollout.

## Launch-Critical Checklist

Use this as the short prelaunch list before opening the first production
rollout window. The detailed owner work remains in the sections below, but the
rollout should not start until each item here has an owner, evidence, and a
clear stop/go threshold.

- [ ] Launch posture approved by WPCOM/Product/Support: v2 starts as a backend
  replacement, not a new customer-facing Monitor product.
- [ ] Schema migrations approved by Systems, with rollback expectations clear:
  schema is additive and should not be rolled back during a service rollback.
- [ ] Fresh v2 Veriflier fleet deployed, v2-only endpoints validated, quorum
  floor understood, and discovery/static-vantage posture approved.
- [ ] Monitor API and Veriflier transport API design reviewed before rollout.
  Confirm whether the current `/api/v1/...` Monitor API and `/v2/...`
  Veriflier transport distinction is acceptable for launch, or whether a
  follow-up alignment plan is needed after the tested rollout contract is
  stable.
- [ ] v2 Monitors deployed in standby/API-controlled mode and verified to avoid
  bucket claims, scheduled checks, delivery workers, WPCOM notifications, and
  site-state writes until activation.
- [ ] Operator API config, API key scopes, `--allow-remote` usage, transcript
  location, and API-guided rollback path rehearsed.
- [ ] API authorization design reviewed before broader API rollout. Confirm
  whether coarse `read` / `write` / `admin` scopes are sufficient for launch,
  and whether a follow-up model is required for customer-scoped access by
  `blog_id`, tag-scoped access such as VIP or Agency, and per-key permission
  limits that prevent one consumer from managing unrelated sites.
- [ ] Projection parity checked on production-like data with an approved drift
  threshold.
- [ ] WPCOM notification parity checked for down, confirmed-down, false-alarm,
  recovery, inactive, URL-mismatch, and blacklisted-site cases while production
  config uses `WPCOM_NOTIFY_MODE=legacy`; `modern` mode remains blocked from
  production until WPCOM signs off on the new endpoint/auth contract.
- [ ] WPCOM legacy notification setup thoroughly tested and validated before
  production activation: mounted client certificate/key readable only by the
  Monitor, legacy `/jetmon/?data=...` request shape verified, WPCOM response
  parsing and retry/circuit behavior exercised, and no secret material emitted
  in TeamCity logs, container logs, dashboard/API output, or rollout
  transcripts.
- [ ] Support/WAF guidance approved for v2 `GET`, `HEAD` compatibility mode,
  `jetmon/2.0`, blocked requests, false positives, and `Unknown`.
- [ ] Synthetic canary tests defined and run before launch. At minimum, cover a
  known-up site, controlled down, controlled recovery, WPCOM notification
  parity, Veriflier-confirmed down, and a WAF/blocked-style case. Record the
  command sequence, expected state, observed state, and rollback trigger.
- [ ] Rollout stop/go thresholds approved for projection drift, missed checks,
  oldest selected age, WPCOM notification failures, API errors, MySQL errors,
  delivery backlog, Veriflier agreement, and stale process/bucket ownership.
- [ ] Failure-mode drills completed or explicitly waived by the rollout owner:
  API unavailable, Veriflier degraded, WPCOM unavailable, MySQL errors,
  delivery backlog, stale heartbeat, bad deploy rollback, WAF false positive,
  and monitor-side `Unknown`.
- [ ] MariaDB 11.4 runtime validation completed against the production patch
  range, not just migration smoke. Cover bucket claiming, runtime freshness
  writes, SSL expiry batches, `ON DUPLICATE KEY UPDATE ... VALUES(...)`, and
  webhook / alert delivery claims.
- [ ] DB server-map reload validation completed with a production-shaped
  `db-servers.php`: confirm read/write endpoint selection, explicit
  `DB_SERVER_MAP_DATACENTER`, fallback to write when no read rows exist, bad
  map rejection while keeping old pools, and credential/endpoint hot reload on
  `DB_CONFIG_UPDATES_MIN` or SIGHUP. Include a Systems/Jetmon review that the
  config-sync sidecar path, env var names, file permissions, reload cadence,
  and dashboard/API status output are the desired production setup.
- [ ] Probe-safety follow-up work is tracked before rollout: scheduled
  `jetpack_monitor_site_safety_flags` reporting, authoritative DNS rebinding tests,
  deeper TLS pathology tests, and optional streaming keyword short-circuiting.

The API rollout smoke gate samples real sites and can also run approved
synthetic canaries supplied with `--canary-file`. Use controlled sites or
uptime-bench fixtures for direct Monitor probe canaries and attach the API
result to the rollout record. Keep separate evidence for recovery, WPCOM
notification parity, and Veriflier-confirmed down flows.

### Remaining External Decisions

These are the items most likely to block a rollout window even if the code and
lab evidence are green.

| Decision | Required Owner | Needed Output |
| --- | --- | --- |
| Launch posture wording | WPCOM, Product, Support | Written approval that v2 starts as a backend replacement, not a new customer-facing Monitor launch. |
| Legacy consumer inventory | WPCOM, Jetpack, Support | Confirmed list of readers that still depend on legacy projection or notification behavior. |
| WPCOM-owned notification parity | WPCOM, Jetmon | Pass/fail evidence for inactive, URL mismatch, blacklist, hook-consumer, and home-URL-only cases. |
| Canary cohort and expansion thresholds | WPCOM, Product, Support, Systems, Jetmon | Starting cohort, hold duration, expansion sizes, rollback triggers, and owner for go/no-go calls. |
| Systems stop/go thresholds | Systems, Jetmon | Numeric thresholds for freshness, drift, WPCOM failures, DB errors, Veriflier health, API errors, and backlog. |
| Probe-safety follow-ups | Jetmon, Systems | Links to accepted tracking items or explicit rollout-owner waiver for non-blocking safety work. |

### Launch Evidence Packet

Before the first production activation, collect these artifacts in the rollout
record. Prefer links to command output or reports over prose summaries.

- Approved launch posture statement and support-facing wording.
- Schema migration transcript and MariaDB runtime exercise evidence.
- Fresh v2 Veriflier fleet validation: `/v2/status`, stable `vantage.id`
  values, quorum floor, auth posture, and capacity evidence.
- API rollout dry-run or guided transcript with config, auth scope,
  `--allow-remote`, transcript path, and rollback path.
- Projection parity output and telemetry parity report against production-like
  data.
- WPCOM legacy notification parity evidence for both Jetmon-owned and
  WPCOM-owned cases.
- Synthetic canary evidence covering direct Monitor probe expectations and the
  separate lifecycle cases: recovery, WPCOM parity, and Veriflier-confirmed
  down.
- Failure-mode drill notes with expected behavior, observed behavior, and any
  explicit waivers.
- Production rollout lab or equivalent rehearsal report covering DB server-map
  reload, StatsD/Graphite wiring, WPCOM simulator behavior, standby activation,
  rollback, and proof that no real WPCOM traffic escaped the lab.
- Probe-safety follow-up links for scheduled safety reporting, DNS rebinding,
  TLS pathology, and keyword streaming optimization.

## Hard Gates

### 1. Legacy Status And Projection Parity

- [ ] Owner: `Jetmon` - Run projection drift checks against production-like data
  and record the output.
- [ ] Owner: `Jetmon` - Verify `Seems Down` projects to legacy status `0`,
  confirmed `Down` projects to `2`, and closed/up projects to `1`.
- [ ] Owner: `Jetmon` - Verify `last_status_change` remains compatible with
  WPCOM expectations.
- [x] Owner: `Jetmon` - Perform a first-pass local-search inventory of known
  WPCOM and Jetpack monitor consumers from the sibling code checkouts.
- [ ] Owner: `WPCOM` - Inventory endpoints, hooks, jobs, support tools, and
  hidden consumers that still read `jetpack_monitor_sites.site_status`.
- [ ] Owner: `Jetpack` - Inventory module/UI paths that still depend on legacy
  WPCOM status or XML-RPC monitor methods.
- [ ] Owner: `Jetmon`, `WPCOM` - Sample recent v1 incidents and verify v2 would
  produce the same customer-visible up/down result.
- [ ] Owner: `Jetmon`, `WPCOM` - Decide the acceptable projection drift
  threshold for canary and broad rollout.

Evidence:

- 2026-05-17, `post-merge-validation-hardening`: internal-only Docker evidence
  covered several failure drills without WPCOM contact:
  - `scripts/scale-resilience-lab.sh run` passed with 600 fixture sites, one to
    four Monitors, graceful Monitor stop, hard-killed Monitor recovery,
    Veriflier degradation/recovery, DB restart, runtime table lock, read-only
    mode, and DB pause.
  - The same scale lab passed with extended DB disruption windows:
    `JETMON_SCALE_LAB_DB_RUNTIME_LOCK_SEC=30`,
    `JETMON_SCALE_LAB_DB_READ_ONLY_SEC=30`, and
    `JETMON_SCALE_LAB_DB_PAUSE_SEC=60`.
  - A 1,200-site, 10-minute `scripts/v2-soak-lab.sh run` pass kept all sites
    fresh on every sample and asserted zero WPCOM audit rows, webhooks, alert
    contacts, and Mailpit messages.
  - `scripts/api-cli-public-fixture-validate.sh run` passed alert-contact
    send-test through Docker-local Mailpit, webhook HMAC delivery/signature
    verification, and HTTP-500 failure assertions with target safety enabled.
  - `govulncheck ./...` reported no reachable known vulnerabilities.
- Remaining gap: these Docker labs validate multi-Monitor behavior on one
  physical host. A multi-physical-host drill across service hosts should still
  be scheduled when hosts 3-5 are not running other tests, or explicitly waived
  by the rollout owner. That drill should confirm dashboard/fleet signals,
  bucket handoff, Veriflier telemetry, and DB behavior with real host loss.
- `jetmon2 rollout projection-drift`
- `jetmon2 telemetry report`
- Sampled incident comparison table
- Legacy reader inventory

Consumer inventory status:

The table below is a first-pass local-search inventory from the sibling
`../wpcom` and `../jetpack` checkouts. It is useful for rollout planning, but it
is not owner-approved. WPCOM, Jetpack, and Support owners still need to confirm
the list, identify hidden consumers not present in the local checkout, and mark
which paths must keep using the legacy projection during the drop-in rollout.

| Consumer | Owner | Data source | Customer-visible impact | Needs legacy projection? | Migration status |
| --- | --- | --- | --- | --- | --- |
| WPCOM Monitor library: `../wpcom/wp-content/lib/jetpack-monitor/` | WPCOM | `jetpack_monitor_sites.site_status`, `last_status_change`, monitor URL, incidents | Source of truth for current WPCOM Monitor status and incident helpers | yes | first-pass candidate; needs WPCOM confirmation |
| WPCOM REST status endpoint: `../wpcom/wp-content/rest-api-plugins/endpoints/jetpack-monitor-status.php` | WPCOM | `JP_Monitor` status, monitor URL, last downtime | Customer/API-visible monitor status | yes | first-pass candidate; needs WPCOM confirmation |
| WPCOM REST incidents/uptime/settings endpoints: `../wpcom/wp-content/rest-api-plugins/endpoints/jetpack-monitor-{incidents,uptime,settings}.php` | WPCOM | `JP_Monitor`, `JP_Monitor_Incidents`, monitor URLs, notification settings, uptime windows | Customer/API-visible incidents, uptime, monitored URLs, and notification settings | yes | first-pass candidate; needs WPCOM confirmation |
| WPCOM notification hook consumers: `../wpcom/wp-content/mu-plugins/jetpack/class.jetpack-monitor-consumer-hooks.php` | WPCOM | `jetpack_monitor_site_status_change` payload, `JP_Monitor` status constants | Email/mobile notification dispatch, hosting-provider stats, status-down webhook | yes | first-pass candidate; needs WPCOM confirmation |
| WPCOM notification senders: `../wpcom/wp-content/lib/jetpack-monitor-notifications/` | WPCOM | `JP_Monitor::get_site_status_raw()`, `last_status_change`, checks payload | Email, SMS, and note content shown to customers | yes | first-pass candidate; needs WPCOM confirmation |
| Activity Log monitor up/down activities: `../wpcom/wp-content/lib/action-to-activity-log/activities/class.activity-monitor-site-{down,up}--jetpack-monitor-site-status-change.php` | WPCOM | `jetpack_monitor_site_status_change.status_id` | Activity Log entries for monitor down/up events | yes | first-pass candidate; needs WPCOM confirmation |
| Jetpack Agency Elasticsearch repository: `../wpcom/wp-content/lib/jetpack-agency/repository/class-jetpack-agency-elastic-search-repository.php` | WPCOM | `monitor_site_status_raw`, `monitor_site_status`, `monitor_last_status_change` | Agency dashboard/search status visibility | yes | first-pass candidate; needs WPCOM confirmation |
| WPCOM support/explanation helpers: `../wpcom/wp-content/lib/class.jetpack-monitor-explanations.php`, `../wpcom/wp-content/lib/ai/tools/ability.jetpack-monitor.php`, `../wpcom/wp-content/lib/guides/observer-modules/jetpack-site-down-no-jetmon/observer.php` | Support, WPCOM | Monitor status, incidents, explanation data | HE/customer explanations for why a site was or was not marked down | likely | first-pass candidate; needs Support/WPCOM confirmation |
| Jetpack plugin monitor module: `../jetpack/projects/plugins/jetpack/modules/monitor.php` | Jetpack | WPCOM XML-RPC methods `jetpack.monitor.setNotifications`, `jetpack.monitor.isUserInNotifications`, `jetpack.monitor.getLastDowntime`; local option `monitor_receive_notifications` | Site-side notification settings and last-downtime data | yes for current WPCOM responses | first-pass candidate; needs Jetpack confirmation |
| Jetpack Sync defaults: `../jetpack/projects/packages/sync/src/class-defaults.php` | Jetpack | `monitor_receive_notifications` option | Sync behavior for monitor notification preference | maybe | first-pass candidate; needs Jetpack confirmation |
| Jetpack customer UI: `../jetpack/projects/plugins/jetpack/_inc/client/security/monitor.jsx`, `../jetpack/projects/plugins/jetpack/_inc/client/at-a-glance/monitor.jsx` | Jetpack | Monitor module state and WPCOM-provided status/settings | Customer-facing Monitor status and settings UI | maybe | first-pass candidate; needs Jetpack confirmation |

Search evidence reviewed:

- `../wpcom/wp-content/lib/jetpack-monitor/`
- `../wpcom/wp-content/rest-api-plugins/endpoints/jetpack-monitor-*.php`
- `../wpcom/wp-content/mu-plugins/jetpack/class.jetpack-monitor-consumer-hooks.php`
- `../wpcom/wp-content/lib/jetpack-monitor-notifications/`
- `../wpcom/wp-content/lib/action-to-activity-log/activities/*jetpack-monitor-site-status-change.php`
- `../wpcom/wp-content/lib/jetpack-agency/repository/class-jetpack-agency-elastic-search-repository.php`
- `../wpcom/wp-content/lib/class.jetpack-monitor-explanations.php`
- `../wpcom/wp-content/lib/ai/tools/ability.jetpack-monitor.php`
- `../wpcom/wp-content/lib/guides/observer-modules/jetpack-site-down-no-jetmon/observer.php`
- `../jetpack/projects/plugins/jetpack/modules/monitor.php`
- `../jetpack/projects/packages/sync/src/class-defaults.php`
- `../jetpack/projects/plugins/jetpack/_inc/client/security/monitor.jsx`
- `../jetpack/projects/plugins/jetpack/_inc/client/at-a-glance/monitor.jsx`

### 2. WPCOM Notification Parity

- [x] Owner: `Jetmon` - Verify the legacy WPCOM notification payload shape is
  unchanged.
- [x] Owner: `Jetmon` - Default production WPCOM notifications to
  `WPCOM_NOTIFY_MODE=legacy` and preserve `modern` mode only for WPCOM
  contract testing.
- [ ] Owner: `Jetmon`, `WPCOM` - Test WPCOM notification handling for site down,
  confirmed down, recovery, inactive site, URL mismatch, and blacklisted site
  behavior.
- [ ] Owner: `WPCOM` - Confirm existing WPCOM notification hooks still fire from
  the legacy `/jetmon/` path.
- [ ] Owner: `WPCOM` - Confirm current home-URL-only notification behavior is
  preserved unless explicitly changed.
- [ ] Owner: `Jetmon`, `WPCOM` - Add or run parity reporting between v2 event
  transitions and WPCOM notification actions.
- [ ] Owner: `Jetmon`, `WPCOM` - Confirm v2 alert contacts and v2 customer
  webhooks cannot duplicate current WPCOM notifications during rollout.

Evidence:

- Golden WPCOM payload sample
- Notification parity report
- Manual or automated down/confirmed-down/recovery test log

Jetmon-owned parity coverage:

| Case | Owner | Status | Evidence |
| --- | --- | --- | --- |
| Legacy endpoint/auth mode is the default | Jetmon | covered | `internal/config` and `internal/wpcom` unit tests |
| Legacy JSON field names and auth/header shape | Jetmon | covered | `internal/wpcom` unit test |
| Confirmed-down payload with local and Veriflier checks | Jetmon | covered | `internal/orchestrator` unit test |
| Recovery notification uses legacy running status | Jetmon | covered | `internal/orchestrator` unit test |
| Seems Down does not notify before Veriflier confirmation | Jetmon | covered | `internal/orchestrator` unit test |
| False alarm does not notify WPCOM | Jetmon | covered | `internal/orchestrator` unit test |
| Maintenance and cooldown suppression do not duplicate WPCOM notifications | Jetmon | covered | `internal/orchestrator` unit tests |
| Down and recovery WPCOM parity deltas are reported separately | Jetmon | covered | `cmd/jetmon2` telemetry report unit test |
| Inactive site behavior | WPCOM, Jetmon | needs external acceptance | Jetmon only selects `monitor_active=1`; WPCOM should confirm customer-visible inactive-site semantics |
| URL mismatch behavior | WPCOM | needs external acceptance | WPCOM owns current home-URL-only handling |
| Blacklisted site behavior | WPCOM | needs external acceptance | WPCOM owns blacklist/filter response semantics |

### 3. Support, WAF, And Allowlist Readiness

- [x] Owner: `Support`, `Jetmon` - Update support guidance from v1 `HEAD`
  assumptions to v2 `GET` checks.
- [x] Owner: `Support`, `Jetmon` - Update allowlist guidance for
  `jetmon/2.0`.
- [x] Owner: `Support`, `Jetmon` - Explain customer-safe meanings for blocked
  requests, verifier confirmation, false positives, maintenance windows, and
  monitor-side uncertainty.
- [ ] Owner: `Support`, `WPCOM` - Update support macros/playbooks for
  firewall, WAF, bot-block, and security-plugin cases.
- [ ] Owner: `Support`, `WPCOM` - Update support guidance for `Unknown` so it
  is not treated as confirmed downtime.
- [ ] Owner: `Jetmon` - Verify blocked/security-plugin failures have enough
  classification evidence for support to diagnose.

Evidence:

- Links to support docs/playbooks
- Sample WAF-blocked incident explanation
- Sample false-positive incident explanation

### 4. Operational Rollout Rehearsal

- [x] Owner: `Jetmon`, `Systems` - Run `make rollout-docs-verify`.
- [x] Owner: `Jetmon`, `Systems` - Run same-server dry-run rehearsal.
- [x] Owner: `Jetmon`, `Systems` - Run fresh-server dry-run rehearsal if that
  path remains an option.
- [x] Owner: `Jetmon`, `Systems` - Run rollback dry-run rehearsal.
- [x] Owner: `Jetmon`, `Systems` - Run VM lab snapshot flow if the lab host is
  available.
- [ ] Owner: `Jetmon`, `Systems` - Confirm `DELIVERY_OWNER_HOST` posture is
  intentional for rollout.
- [ ] Owner: `Jetmon`, `Systems` - Run the approved synthetic canary sequence
  before first production activation and capture the evidence in the rollout
  record by passing the approved direct-probe canary file to the API
  preflight/smoke gates and attaching the separate lifecycle canary evidence.

Evidence:

- `make rollout-docs-verify` output
- `make rollout-rehearsal-verify` output
- VM lab transcript
- Generated rehearsal plan for the actual rollout mode, including the
  post-cutover `jetmon2 telemetry report` parity evidence command
- Synthetic canary transcript covering known-up, controlled down, recovery,
  WPCOM parity, Veriflier-confirmed down, and WAF/blocked-style behavior

Local dry-run evidence:

- `make rollout-docs-verify` passed on 2026-05-03T03:17Z.
- The verifier generated and checked same-server, fresh-server, and guided
  rollback dry-run plans.
- `make rollout-vm-lab-snapshot-all-smoke` passed on 2026-05-03T05:32Z
  against `jetmon-vm-host-1` using snapshot `pre-guided-flow`. Covered
  execute/rollback, interrupted resume, post-start rollback, bad SSH, v2 start
  failure, runtime guards, real activity, and failure gates.

### 5. Production Observability And Hold Points

- [ ] Owner: `Jetmon`, `Systems` - Define go/no-go thresholds for projection
  drift, missed checks, oldest selected age, stale heartbeats, WPCOM
  notification failures, delivery backlog, API errors, MySQL errors, and
  verifier agreement.
- [x] Owner: `Jetmon` - Confirm host and fleet dashboards expose the Jetmon-owned
  rollout signals: process heartbeats, bucket ownership, delivery posture,
  projection drift, dependency health, Veriflier v2 contract status, trusted
  vantages, agent telemetry, capacity, discovery mode posture, and suggested
  next actions.
- [ ] Owner: `Systems` - Confirm the host and fleet dashboard signals are
  sufficient for the rollout room and existing production monitoring posture.
- [ ] Owner: `Jetmon`, `Systems` - Confirm StatsD metrics and log paths remain
  compatible with existing monitoring. For Monitor containers, verify
  `STATSD_ADDR=host.docker.internal:8125`, Docker host-gateway mapping, and
  `STATSD_HOST_PATH=<datacenter>.<node>` preserve the v1 Graphite series path.
- [x] Owner: `Jetmon` - Confirm `jetpack_monitor_process_health` heartbeats are exposed
  through the fleet dashboard with stale thresholds.
- [ ] Owner: `Systems` - Confirm `jetpack_monitor_process_health` heartbeat/staleness
  thresholds are understood by operators before rollout.
- [x] Owner: `Jetmon` - Add read-only Veriflier discovery shadow comparison via
  `jetmon2 verifliers discovery-report`.
- [x] Owner: `Jetmon` - Add local Veriflier discovery-drift soak coverage for
  duplicate static vantages, missing trusted registry rows, incomplete registry
  rows, endpoint/auth-presence drift, untrusted agents, duplicate agent
  endpoints, active-mode fallback, and recovery to green.
- [ ] Owner: `Jetmon`, `Systems` - Define a written pause protocol.
- [ ] Owner: `Jetmon`, `Systems` - Define a written rollback-now protocol.

Evidence:

- Dashboard links or screenshots
- Threshold table
- Rollout room checklist
- Alert names and owners
- `make test-veriflier-soak`
- `jetmon2 verifliers discovery-report`
- ADR-0010: trusted Veriflier discovery with monitor-collected telemetry

Initial stop/go threshold worksheet:

| Signal | Proposed canary starting point | Hold action |
| --- | --- | --- |
| Projection drift | 0 unexpected drift rows in the canary bucket range after the first full v2 round | Pause expansion; compare event rows, projection rows, and v1 expectations before continuing |
| Missed checks | 0 missed checks for the canary range after the first full expected interval; any broader threshold needs explicit Systems/Jetmon approval | Pause expansion; inspect scheduler selected/completed/outstanding metrics |
| Oldest selected age | No selected site older than 2x its expected check interval plus timeout/retry buffer | Hold at current cohort; inspect scheduler queue depth and DB selection latency |
| Stale host heartbeat | 0 active rollout hosts stale beyond `BUCKET_HEARTBEAT_GRACE_SEC` | Stop host expansion; confirm bucket ownership before touching more hosts |
| WPCOM notification failures | 0 unexpected failures for down/confirmed-down/recovery canary events; circuit breaker must stay closed | Pause; keep v1-compatible projection active and resolve WPCOM/API failure |
| Delivery backlog | Stable or decreasing backlog; oldest due delivery within the agreed retry ladder for enabled delivery workers | Hold delivery-owner changes; verify `DELIVERY_OWNER_HOST` and deliverer health |
| API errors | Health, dashboard, and required API smoke checks pass with no sustained 5xx responses | Keep API internal; investigate before any gateway or automation dependency |
| MySQL errors | No sustained connection failures, query errors, or lock wait spikes during the rollout window | Pause host changes; review DB health before retrying |
| Veriflier agreement | Quorum floor remains intact and verifier health loss is explained | Pause confirmed-down expansion; avoid customer-visible downtime notifications from degraded quorum |

Jetmon-owned Veriflier discovery evidence:

- Host and fleet dashboards expose Veriflier v2 contract status, trusted
  registry state, monitor-collected agent telemetry, capacity, discovery mode
  posture, stale telemetry, and duplicate endpoint warnings.
- `jetmon2 verifliers discovery-report` provides a read-only shadow-mode gate
  for static-vs-registry-vs-agent comparison without printing auth token
  values.
- `make test-veriflier-soak` covers local Veriflier v2 contract soak scenarios
  and discovery-drift soak scenarios.
- ADR-0010 records the trust boundary: operator-approved vantages are quorum
  trust, monitor-collected agents are telemetry, and Veriflier hosts do not
  need database credentials.

### 6. Internal Consumer Inventory

- [x] Owner: `Jetmon` - Add first-pass local-search candidates from the sibling
  WPCOM and Jetpack code checkouts.
- [ ] Owner: `WPCOM` - Confirm code paths reading
  `jetpack_monitor_sites.site_status`, `last_status_change`, monitor URL, or
  active state.
- [ ] Owner: `WPCOM` - Confirm WPCOM REST endpoints exposing monitor status,
  settings, incidents, and uptime.
- [ ] Owner: `WPCOM` - Confirm Activity Log and Elasticsearch consumers of
  monitor incidents.
- [ ] Owner: `WPCOM` - Confirm hooks such as
  `jetpack_monitor_site_status_change` and consumers attached to them.
- [ ] Owner: `Jetpack` - Confirm XML-RPC monitor methods used by Jetpack.
- [ ] Owner: `Support` - Confirm support tools that display monitor status,
  incidents, or notification state.
- [ ] Owner: `WPCOM`, `Jetmon` - Mark which consumers require legacy projection
  to stay enabled.

Evidence:

- Consumer inventory table with path, owner, data source, customer-visible
  impact, and migration status.

Use the inventory table in the projection parity gate. Do not disable legacy
projection until every customer-visible reader has either migrated to v2 event
data or explicitly accepted the old projection contract no longer being present.

### 7. Failure-Mode Drills

- [ ] Owner: `Jetmon`, `Systems` - Drill Jetmon API unavailable while monitor
  checks continue.
- [ ] Owner: `Jetmon`, `Systems` - Drill Veriflier unavailable or degraded.
- [ ] Owner: `Jetmon`, `WPCOM` - Drill WPCOM notification endpoint failing or
  circuit breaker open.
- [ ] Owner: `Jetmon`, `Systems` - Drill MySQL lag or temporary DB errors.
- [ ] Owner: `Jetmon`, `Systems` - Drill delivery backlog growth.
- [ ] Owner: `Jetmon`, `Systems` - Drill stale host heartbeat and bucket
  ownership handoff.
- [ ] Owner: `Jetmon`, `Systems` - Drill bad deploy and rollback.
- [ ] Owner: `Jetmon`, `Support` - Drill a false-positive incident caused by
  customer WAF/bot blocking.
- [ ] Owner: `Jetmon`, `Support` - Drill `Unknown` or monitor-side uncertainty
  and verify it is not presented as confirmed downtime.

Evidence:

- Drill notes with command sequence, expected behavior, observed behavior, and
  follow-up items.

### 8. Probe Safety And Database Runtime Validation

- [ ] Owner: `Jetmon`, `Systems` - Run an end-to-end MariaDB 11.4 runtime
  exercise against versions matching the production patch range. Migration
  smoke on `mariadb:11.4.8` and `mariadb:11.4.10` is covered by
  `make migration-smoke`, but the rollout gate must also exercise runtime write
  paths.
- [ ] Owner: `Jetmon`, `Systems` - Include bucket claiming, runtime freshness
  writes, SSL expiry updates, `ON DUPLICATE KEY UPDATE ... VALUES(...)`, and
  webhook / alert delivery row claims in the MariaDB runtime exercise. Local
  delivery row-claim lock behavior is covered by `make delivery-claim-smoke`.
- [ ] Owner: `Jetmon`, `Systems` - Validate production DB server-map behavior:
  v1-style `misc` parsing, read/write split, datacenter read preference, bad
  map rejection, and hot reload after `db-servers.php` changes. Treat this as
  a config-design review as well as a test gate: confirm the sidecar-generated
  file location, read-only Monitor mount, `DB_SERVER_MAP_*` values, reload
  cadence, and `/api/v1/monitor/db-config` / dashboard status are acceptable
  before rollout.
- [x] Owner: `Jetmon` - Add scheduled
  `jetpack_monitor_site_safety_flags` reporting so unsafe legacy row counts and runtime
  probe-safety blocks are visible before and after API rejection rolls out.
  Use `jetmon2 site-safety report` for the durable flag table, and pair it
  with scheduled dry-run `site-safety unsafe-urls` scans when active legacy URL
  shape counts are needed.
- [x] Owner: `Jetmon` - Add follow-up tracking for authoritative DNS
  rebinding coverage: public address on first lookup, then private/reserved
  address on redirect or later check. See
  `uptime-bench-probe-safety-integration-handoff.md`.
- [x] Owner: `Jetmon` - Add follow-up tracking for deeper TLS pathology
  coverage: TLS 1.0/1.1, no common cipher, handshake close/alert, large
  certificate chains, expired/self-signed certificates, and hostname mismatch.
  See `uptime-bench-probe-safety-integration-handoff.md`.
- [ ] Owner: `Jetmon` - Decide whether streaming keyword matching should
  short-circuit when a required-only keyword is found. This is an optimization
  follow-up, not a production rollout blocker unless body-read cost becomes a
  measured canary issue.

Evidence:

- MariaDB runtime exercise transcript, including server versions and pass/fail
  output for each covered write path.
- `site-safety report` output and links to follow-up issues, roadmap entries,
  or handoff docs for safety-flag reporting, DNS rebinding tests, TLS
  pathology tests, and keyword
  short-circuiting.

## Early Canary Gates

These should be ready before broad rollout and, where feasible, before the
first controlled canary.

- [x] Owner: `WPCOM`, `Jetmon` - Draft the canary cohort matrix: WPCOM-hosted,
  Atomic, self-hosted Jetpack, agency-managed, WAF/security-plugin, historically
  noisy/flaky, high-traffic, and multi-endpoint sites.
- [ ] Owner: `WPCOM`, `Product`, `Support`, `Jetmon` - Approve the canary
  cohort matrix and exact expansion/rollback thresholds.
- [ ] Owner: `Jetmon`, `Systems` - Define canary size, duration, rollback
  threshold, and expansion threshold.
- [ ] Owner: `Jetmon`, `Systems` - Run the approved synthetic canary checks
  before launch and again before each detection-profile expansion. Required
  cases: known-up, controlled down, controlled recovery, WPCOM notification
  parity, Veriflier-confirmed down, WAF/blocked-style failure, and at least one
  customer-safe false-alarm/non-confirmation case.
- [ ] Owner: `WPCOM` - Build or script read-only shadow comparisons for v2
  status, event list/detail, and uptime summary while customers still see
  legacy output.
- [ ] Owner: `Product`, `Support`, `WPCOM`, `Jetmon` - Define customer-facing
  language for `Up`, `Seems Down`, `Down`, `Degraded`, `Warning`,
  `Maintenance`, `Paused`, `Unknown`, and `Resolved`.
- [ ] Owner: `Product`, `WPCOM`, `Jetmon` - Draft SLA/reporting semantics for
  `Seems Down`, `Degraded`, `Warning`, `Maintenance`, and `Unknown`.
- [ ] Owner: `WPCOM`, `Jetmon` - Decide whether trigger-now is available during
  rollout and to whom.
- [ ] Owner: `WPCOM` - Add per-site, per-actor, per-tenant, and per-plan
  trigger-now quotas before customer exposure.

Canary cohort matrix:

| Cohort | Why It Matters | Starting Signal | Rollback Trigger |
| --- | --- | --- | --- |
| WPCOM-hosted | Lowest external-network variability; validates core parity first | Projection drift, WPCOM notification parity, scheduler freshness | Any unexplained drift, notification delta, or missed-check pattern |
| Atomic | Exercises managed hosting plus customer-specific layers | WAF/edge blocks, GET-path behavior, false positives | Repeated blocked/redirect classes without support-ready explanation |
| Self-hosted Jetpack | Highest network/plugin variability | Veriflier agreement, timeout classes, support explanations | Unexplained verifier disagreement or customer-facing notification mismatch |
| Agency-managed | High support impact and strong sensitivity to false positives | Incident explanations, WAF allowlist readiness | Any repeated false-positive class that support cannot explain |
| WAF/security-plugin | Validates v2 GET allowlist language | 403/challenge/keyword-missing classes | Any broad block caused by v2 UA/source not being allowlisted |
| Historically noisy/flaky | Tests retry and verifier value against known difficult sites | Seems Down false-alarm rate, cooldown behavior | Regression versus v1/v2 baseline for noisy classes |
| High-traffic | Catches performance-sensitive GET-path behavior | RTT, timeout, customer reports | Sustained timeout/intermittent class not explained by site telemetry |
| Multi-endpoint | Exercises event identity and rollup expectations | Per-endpoint status, duplicate event count | Duplicate customer-visible incidents or unclear support explanation |

Treat the first canary as a parity canary, not a feature canary. Do not expand
because v2-only features look useful; expand only when the backend replacement
signals stay inside the approved thresholds.

## Decision Options To Resolve

### Rollout Scope

Option A: backend replacement only.

- Pros: lowest customer-facing risk, keeps parity measurable, and gives
  Systems/Support one change to reason about.
- Cons: delays visible product wins from v2 until after the service is stable.
- Recommendation: use Option A for the first production rollout.

Option B: backend replacement plus customer-visible feature canary.

- Pros: proves v2 value earlier.
- Cons: mixes service migration risk with product semantics, support language,
  and WPCOM gateway readiness.

### Customer-Facing State Semantics

Option A: keep current customer-facing states and treat richer v2 labels as
internal during the drop-in rollout.

- Pros: avoids surprise support changes and keeps v1/v2 parity measurable.
- Cons: customers do not immediately see `Seems Down`, `Unknown`, or richer
  degradation states.
- Recommendation: use Option A until Product, WPCOM, and Support agree on the
  public state model.

Option B: expose richer v2 state labels during the first rollout.

- Pros: makes v2 behavior more transparent.
- Cons: requires UI, copy, support macros, reporting semantics, and customer
  expectation work before backend stability is proven.

### Gateway And Public API Exposure

Option A: keep all v2 API access internal during the drop-in rollout.

- Pros: protects the backend migration from tenant-safety and quota concerns.
- Cons: WPCOM cannot validate customer-routed read paths with real traffic.

Option B: allow WPCOM-owned read-only shadow comparisons while customers still
see legacy output.

- Pros: validates gateway routing, tenant scoping, and data shape before
  customer exposure.
- Cons: requires clear WPCOM ownership and comparison tooling.
- Recommendation: use Option B after backend parity gates pass.

Option C: expose customer-facing API reads during the first rollout.

- Pros: accelerates product API validation.
- Cons: too much customer-facing blast radius for a backend replacement.

## Explicit Non-Blockers For Drop-In Rollout

These remain important but should not block the v1 replacement unless the
launch scope changes:

- Full WPCOM gateway productization
- Paid Monitor packaging
- Rich Jetpack Monitor UI
- Alert-contact self-service
- Customer webhook self-service
- Slack, Teams, and PagerDuty customer launch
- Public/customer monitoring API
- Jetpack reverse checks
- Domain/DNS monitoring expansion
- Quiet hours, digests, grouping, acknowledgements
- Long-range paid SLA/reporting surfaces
- v3 probe-agent/per-vantage architecture
- Legacy projection retirement

## Service-Side Hardening Map

These recommendations should remain Jetmon-owned even when WPCOM owns public
customer surfaces.

| Priority | Work | Rollout Blocker? | Existing Tracker |
| --- | --- | --- | --- |
| 1 | Compatibility and parity gates | yes | this doc, migration runbook, telemetry report |
| 2 | Gateway-routed API invariant tests | before gateway canary | `public-api-gateway-tenant-contract.md` |
| 3 | Reporting rollups and retention policy | before paid reports | `roadmap.md` |
| 4 | Outbound credential encryption | before broad self-service alerts/webhooks | `outbound-credential-encryption-plan.md` |
| 5 | Delivery consolidation and notification dedupe | before customer alert-contact launch | `jetmon-deliverer-rollout.md`, `roadmap.md` |
| 6 | Customer-routed target/destination safety controls | before public target/destination management | `roadmap.md` |
| 7 | Capacity ladder and storage optimization | before larger cohorts | `roadmap.md` |
| 8 | Alert/webhook lifecycle polish | before self-service launch | `roadmap.md` |
| 9 | Public-safe state and error metadata | before public API beta | `roadmap.md` |
| 10 | Reverse checks, DNS/domain incidents, rollup, suppression | post-rollout product expansion | `taxonomy.md`, `roadmap.md` |
| 11 | v3 probe-agent/per-vantage readiness | post-v2 production evidence | `v3-probe-agent-architecture-options.md` |

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

## Immediate Prelaunch Closure Path

1. Close the external decisions table: posture wording, consumer inventory,
   WPCOM-owned parity cases, canary expansion thresholds, Systems thresholds,
   and probe-safety tracking.
2. Run projection drift and telemetry parity reports against production-like
   data that includes active sites and WPCOM notification evidence.
3. Run the approved synthetic canary sequence through the API gates and attach
   separate lifecycle evidence for recovery, WPCOM parity, and
   Veriflier-confirmed down.
4. Complete the production rollout lab or explicitly waive any lab-only gaps
   with a named owner and replacement evidence.
5. Fill out the launch-day readiness card and use it as the rollout-room stop/go
   source.

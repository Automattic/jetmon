# Jetmon Roadmap

Deferred features that are intentionally out of scope for the current implementation but have been identified as important future work. Items here are tracked so they are not forgotten and can be designed with future compatibility in mind.

---

## Prioritized TODO

This is the current implementation/refinement queue. Lower-priority items are
not abandoned; they are intentionally sequenced behind the v2 production
migration and the operating data needed to make larger architecture decisions.

### Candidate follow-up branches

These are scoped branches worth considering after the merged API CLI, rollout
preflight, deliverer hardening, API CLI fixture workflow, dashboard, and
production telemetry branches:

No active candidate branch is queued here right now.

### Production Telemetry Reports TODO

- [x] Add `jetmon2 telemetry report` as a read-only production report over
  existing event, transition, audit, and verifier telemetry tables.
- [x] Summarize event lifecycle counts, detection timings, verifier agreement,
  false-alarm classes, WPCOM parity, and operator explanation gaps in one
  repeatable text/JSON command.
- [x] Keep the report safe for production use by avoiding payload/credential
  dumps, bounding query runtime, using half-open report windows, and reporting
  only aggregate counts, durations, classes, and gap names.
- [ ] Revisit report thresholds and suggested actions after v2 has enough real
  production traffic to show which rates should be considered normal.

### Uptime-Bench Scenario Coverage TODO

- [x] Wire uptime-bench's inverted keyword scenarios through Jetmon v2's
  existing `forbidden_keyword` support so cases such as
  `content-keyword-injected` can be provisioned and scored instead of skipped
  or reported as unsupported adapter capability.
- [x] Add multi-pattern body-content checks for scenarios where the page still
  contains the required canary but also includes known-bad content, such as
  injected scripts, spam links, parked-domain text, maintenance banners, and
  upstream error templates. Keep this distinct from broad visual/content
  baselining: operators need explicit, auditable rules before Jetmon can safely
  declare customer content wrong.
- [ ] Add a conservative body-size / near-empty body detector as a scoped
  follow-up before full content baselining. This should catch white-screen and
  empty-body failures while keeping alerts explainable. Defer until after the
  current explicit keyword / forbidden-keyword work has benchmark and operator
  data, because per-site thresholds can otherwise create false positives for
  intentionally tiny health pages.
- [ ] Design a content-integrity baseline mode separately from explicit
  forbidden patterns. Benchmark variants such as defacement and ransomware can
  be caught by required keywords today, but production users will eventually
  need a controlled way to detect large unexpected body changes without
  hard-coding every bad string. Defer full baseline/diff mode until after v2 is
  stable in production because dynamic WordPress pages need normalization,
  training, approval/reset workflows, and operator-visible evidence before
  Jetmon can safely alert on "content changed unexpectedly."
- [ ] Improve DNS diagnostics on HTTP lookup failures before building explicit
  DNS monitors. The v2 HTTP checker already records DNS timing and classifies
  lookup failures as connect failures; add event metadata that distinguishes
  NXDOMAIN, SERVFAIL, timeout, and resolver errors where Go/runtime resolver
  data can support it. This is the recommended near-term step because it helps
  HEs explain failures without creating a new monitor type.
- [ ] Track DNS-specific benchmark scenarios separately from HTTP DNS failures.
  Explicit DNS-record, DNSSEC, split-horizon, CNAME-chain, authoritative
  nameserver, and DNS-latency monitors need a dedicated check type and event
  taxonomy before they should be exposed as production uptime signals. Defer
  this larger feature until the product semantics are designed: some DNS
  failures should be `Warning` or `Degraded`, some should roll up to site-level
  `Down`, and monitor-side resolver impairment must remain `Unknown`.
- [ ] Validate geo-scoped benchmark assumptions before changing Jetmon
  production behavior for `http-geo-503`. Confirm the probe source ranges,
  intended Jetmon region semantics, and support story for partial regional
  failures; if Jetmon remains single-region until the probe-agent work, document
  that this benchmark class is not directly comparable yet.
- [ ] Preserve Veriflier vote evidence as an interim regional-diagnostics aid
  without exposing customer-visible regional state. A small v2 follow-up can
  store/report which Veriflier locations observed success, failure, timeout, or
  mixed outcomes. Defer customer-facing regional classifications until the
  probe-agent architecture exists because current Verifliers are confirmation
  probes after local failure, not continuous per-vantage primary checks.

### Projection Drift Tooling TODO

- [x] Compare legacy projection status against a per-blog rollup of open HTTP
  events so multiple open endpoint events cannot overcount drift.
- [x] Add bucket/status summaries to `rollout projection-drift` so operators
  can distinguish one-off rows from range-wide projection failures.
- [x] Add likely-cause labels and manual repair guidance to the drift report
  without mutating production data automatically.
- [ ] Consider a dedicated dry-run repair planner after production rehearsals
  show which drift classes are safe enough to automate.

### Rollout Simplification TODO

- [x] Add `jetmon2 rollout rehearsal-plan` so operators can generate the exact
  same-server or fresh-server command sequence from a bucket CSV, host, bucket
  range, and rollout mode.
- [x] Add `make rollout-docs-verify` so docs/tooling drift checks, command help
  checks, staged systemd validation, build, test, and lint can run as one
  repeatable gate.
- [x] Add `jetmon2 rollout cutover-check` to bundle the read-only post-start
  pinned preflight, activity, status, and projection-drift checks used after
  each host replacement.
- [x] Add JSON output to rollout checks for Systems automation gates.
- [x] Create a one-page rollout quick reference that links to the full
  migration runbook.
- [x] Add a rollout state report that summarizes ownership mode, bucket
  coverage, drift, recent activity, delivery owner state, and suggested next
  action.

### Rollout Host Preflight Polish TODO

- [x] Add `jetmon2 rollout host-preflight` to bundle the pre-stop host gate:
  static bucket plan match, config parse, DB connectivity, pinned safety
  checks, and staged systemd validation.
- [x] Let `rollout rehearsal-plan` accept explicit v1 stop/start commands so
  generated plans do not leave the most stressful cutover and rollback actions
  as comments.
- [x] Make generated rollback blocks more explicit about hold points, stop-v2
  confirmation, rollback-check success, and the no-schema-rollback rule.
- [x] Update migration docs and quick reference so operators know which checks
  are pre-stop gates, post-start gates, rollback gates, and fleet gates.
- [x] Simplify generated rehearsal plans so `host-preflight` is the single
  pre-stop gate, while preserving `--bucket-total` and custom systemd unit
  choices in the printed commands.
- [x] Clarify operator-facing docs around service environment setup, explicit
  cutover/rollback ranges, and the difference between the immediate cutover
  smoke gate and the full-round `--require-all` gate.
- [x] Add `jetmon2 rollout guided` as an interactive, idempotent rollout and
  rollback walkthrough with log-dir write checks, transcripts, resume state,
  typed confirmations for destructive transitions, dry-run rehearsal, and
  optional execution of operator commands.
- [x] Rehearse the guided rollout UX with repeated dry-run simulations and
  tighten the flow: richer dry-run plans with commands and confirmations,
  state-aware resume skips for interrupted service transitions, and an explicit
  resume/start-over prompt with no unsafe default.
- [x] Extend targeted guided rollout flow coverage for fresh-server handoff,
  execute-mode dry-runs, wrong typed confirmations, mismatched resume state,
  explicit start-over, and rollback after a failed post-start gate.
- [x] Make rollout run origin explicit in guided output and docs: run from the
  staged v2 runtime host, and require SSH from that runtime host to the old v1
  host when fresh-server v1 stop/start commands use SSH.
- [x] Add fresh-server guided happy-path simulations for manual flow, execute
  flow, and direct rollback command ordering.

### Rollout VM Lab TODO

- [x] Prepare an in-house KVM/libvirt host with passwordless sudo, QEMU,
  libvirt, cloud-init tooling, Go, MariaDB client tools, and a dedicated
  `jetmon-rollout` storage pool.
- [x] Add a repo-owned `scripts/rollout-vm-lab.sh` harness with host `doctor`,
  image fetch, VM create/destroy, topology create, SSH wait, and offline
  snapshot/revert primitives.
- [x] Document the VM lab workflow, environment overrides, topology, and
  planned rollout flow coverage.
- [x] Seed the DB VM with v1-compatible Jetmon data and v2 additive migrations.
- [x] Install built `jetmon2` artifacts and staged systemd units onto the v2 VM.
- [x] Add a v1 simulator service that models static bucket ownership and safe
  stop/start behavior for guided rollout tests.
- [x] Wire VM lab smoke targets into Makefile or a dedicated operator script.
- [x] Automate fresh-server execute-mode happy path and guided rollback smoke.
- [x] Automate failed pre-stop dynamic-overlap and bad systemd-unit refusal
  flows.
- [x] Automate interrupted resume, failed post-start rollback, and bad SSH
  flows.
- [x] Add snapshot-backed VM flow runners for full execute-mode cutover and
  rollback simulations.
- [x] Automate v2 service start failure after v1 stops, unwritable rollout log
  directory refusal, bad DB connection refusal, and real `last_checked_at`
  activity from the `jetmon2` service.
- [x] Add snapshot-backed replay for every named VM lab smoke flow.

### Dashboard and Fleet Health TODO

- [x] Split dashboard work into two PRs: first improve host dashboards and add
  fleet-dashboard plumbing, then build the global fleet dashboard on top.
- [x] Add a durable `jetmon_process_health` table for long-running process
  heartbeats and compact local health snapshots.
- [x] Publish monitor-host health from `jetmon2`, including bucket ownership,
  worker queues, WPCOM circuit state, delivery-owner state, dependency health,
  RSS memory, Go runtime system memory, version, and process lifecycle state.
- [x] Publish standalone `jetmon-deliverer` health, including active/idle
  owner state, DB/StatsD health, RSS memory, Go runtime system memory, version,
  and process lifecycle state.
- [x] Add a combined host-dashboard snapshot endpoint so host state, dependency
  health, and red/amber/green summary rules are available from one local API.
- [x] Polish the existing host dashboard so rollout blockers, delivery-owner
  warnings, dependency health, and operator commands are easier to scan.
- [x] Harden host dashboard exposure by binding to localhost by default, with
  an explicit operator-controlled bind address for trusted remote access.
- [x] Add a compact host-summary issue list so amber/red dashboard states name
  the highest-priority blockers instead of only showing aggregate counts.
- [x] Split process lifecycle state from health rollup state in
  `jetmon_process_health` so a running process can still report degraded or red
  dependencies without overloading a single field.
- [x] Wire real per-host sites-per-second and last-round duration values into
  the dashboard instead of showing placeholder zero values.
- [x] Label the dashboard memory value as Go runtime system memory so operators
  do not mistake `runtime.MemStats.Sys` for operating-system RSS.
- [x] Build the global fleet dashboard from `jetmon_process_health`,
  `jetmon_hosts`, delivery queues, projection drift, and Veriflier health.
- [x] Add stale-heartbeat thresholds and fleet-level suggested next actions for
  rollout handoffs.
- [x] Add explicit fleet delivery-ownership posture so operators can
  distinguish intentional rollout-conservative `DELIVERY_OWNER_HOST` settings
  from accidental all-host delivery eligibility.
- [x] Collect true process RSS for fleet and host dashboards while retaining Go
  runtime system memory as a separate allocator/guardrail signal.
- [x] Document and test the fleet dashboard's safe network exposure model
  before exposing it beyond trusted operator networks.

### v2 Rollout Docs Rehearsal TODO

- [x] Add a dedicated `make rollout-rehearsal-verify` target that exercises the
  operator-facing same-server, fresh-server, and rollback dry-run flows without
  requiring a database or VM lab.
- [x] Keep the rehearsal verifier inside `make rollout-docs-verify` so rollout
  docs, CLI output, and generated command plans cannot drift independently.
- [x] Cover fresh-server SSH/run-origin warnings in automated rehearsal checks
  so the runtime-host versus v1-host distinction stays explicit.
- [x] Do a full read-through of the migration runbook and quick reference after
  the new rehearsal verifier lands, then tighten any remaining wording that
  could cause operator copy/paste mistakes.
- [x] Run the VM lab snapshot flow after the docs/tooling pass if the
  `jetmon-deploy-test` host is available, and capture any mismatch between the
  text runbook and real guided execution.

Recently completed candidate branches:

- **`feature/production-telemetry-reports`** - adds `jetmon2 telemetry report`
  for repeatable production summaries of lifecycle timing, verifier agreement,
  false-alarm classes, WPCOM parity, and operator explanation gaps.
- **`feature/fleet-dashboard`** - adds `/fleet` and `/api/fleet` global
  dashboard views for monitor hosts, standalone deliverers, bucket coverage,
  stale heartbeats, delivery backlog, delivery-owner posture, projection drift,
  dependency rollups, and fleet-level rollout blockers.
- **`feature/host-dashboard-fleet-plumbing`** - improved each host dashboard as
  a clearer production rollout cockpit while publishing monitor and deliverer
  process health into MySQL for the later fleet dashboard.
- **`feature/rollout-preflight-hardening`** - merged rollout safety commands
  for static bucket plans, pinned checks, activity checks, rollback checks,
  projection drift, and operator-visible rollout guidance.
- **`feature/deliverer-rollout-hardening`** - merged standalone deliverer
  validation, owner checks, delivery backlog checks, service docs, rollback
  guidance, and service-file cleanup.
- **`feature/api-cli-fixture-workflows`** - merged deterministic fixture-backed
  API CLI validation, webhook smoke, signature checks, remote-write guardrails,
  batch-owned cleanup, and command discovery.

### P0 - v2 production hardening

- **Keep the v2 deployment target conservative.** Ship and stabilize the
  current main-server-plus-Veriflier design before moving toward a v3
  probe-agent architecture. The v2 event tables remain authoritative while
  `LEGACY_STATUS_PROJECTION_ENABLE` keeps legacy `site_status` /
  `last_status_change` consumers working during migration. Use the
  [`v1-to-v2-migration.md`](v1-to-v2-migration.md) pinned bucket
  path for the first v1-to-v2 production migration, then remove
  `PINNED_BUCKET_*` after every host is on v2 and stable.
- **Keep rollout health visible before cutover.** Operators should not have to
  infer migration-critical state from logs or config while replacing v1 hosts.
  The operator dashboard now shows bucket ownership mode, legacy projection
  mode, delivery-worker ownership, rollout preflight/activity/rollback/drift
  commands, and live dependency health for MySQL, Verifliers, WPCOM, StatsD,
  and log/stats disk writes. Keep this visible and verified during rollout
  rehearsal because it helps separate customer-site downtime from monitor-side
  impairment during cutover.
- **Use delivery ownership as a rollout guard.**
  In the single-binary deployment, `API_PORT > 0` also starts webhook and
  alert-contact delivery workers. A standalone `jetmon-deliverer` entry point
  and transactional `SELECT ... FOR UPDATE` row claims now exist; use
  `DELIVERY_OWNER_HOST` as a rollout guard when intentionally keeping delivery
  single-owner during migration from embedded to standalone delivery.
- **Run a production rollout rehearsal pass.** Validate that README,
  `v1-to-v2-migration.md`, config samples, systemd units,
  `validate-config`, `rollout guided`, `rollout static-plan-check`,
  `rollout host-preflight`, `rollout cutover-check`, `rollout activity-check`,
  `rollout rollback-check`, `rollout projection-drift`, and rollback steps line
  up exactly before the first production host replacement.
- **Instrument the data needed for the v3 decision.** During v2 production,
  measure first-failure-to-`Seems Down`, `Seems Down`-to-`Down`, false alarm
  rate by failure class, Veriflier agreement/disagreement by region, Veriflier
  latency/timeout rates, mixed-region outcomes, monitor-side `Unknown` cases,
  primary-check vs confirmation cost, operator explanation gaps, and WPCOM
  notification parity. StatsD now emits the core detection timings, outcome
  counters split by local failure class, and per-Veriflier-host RPC/vote
  counters, plus legacy WPCOM notification attempt/delivered/retry/error/failed
  counters split by status. `jetmon2 telemetry report` now provides the first
  durable report surface for these questions; tune thresholds and suggested
  actions after v2 has enough real traffic to prove which rates are normal.
- **Watch projection drift as a production bug.** While the legacy projection
  is enabled, event mutations, transition rows, and the site-row projection
  must remain transactionally consistent. `jetmon2 rollout projection-drift`
  lists the exact active sites whose legacy projection disagrees with the
  authoritative HTTP event state, so rollout failures are actionable instead of
  count-only.
- **Keep roadmap/API documentation drift out of the branch.** `internal-api-reference.md` is the
  source for the implemented internal `/api/v1` route surface. This roadmap
  should track only the remaining public/customer API work, production
  hardening, and deferred architecture choices.
- **Keep API CLI rehearsal workflows production-safe.** The focused
  `jetmon2 api` helper now exists; continue hardening fixture and smoke
  workflows so local, staging, and any approved remote runs remain explicitly
  batch-owned, easy to clean up, and difficult to aim at production by mistake.
  See [`api-cli-roadmap.md`](api-cli-roadmap.md).

### P1 - post-v2 platform refinement

- **Extract `jetmon-deliverer` when delivery scale or blast radius warrants
  it.** Move webhook delivery, alert-contact delivery, and eventually WPCOM
  notification dispatch behind one outbound-delivery binary. Initial shared
  worker wiring, a standalone `jetmon-deliverer` entry point, and
  transactional row claims exist. A sample systemd service is available at
  `systemd/jetmon-deliverer.service`. The rollout policy is captured in
  [`jetmon-deliverer-rollout.md`](jetmon-deliverer-rollout.md);
  the remaining production cutover work is deployment-system adoption and
  host-specific config wiring.
- **Unify webhook and alerting dispatch plumbing after production evidence.**
  Keep the packages separate until there are two proven implementations and a
  third transport path via WPCOM migration, then factor the shared retry,
  claim, dispatch, and circuit-breaker shape behind a transport interface.
- **Migrate WPCOM notifications behind alert contacts/deliverer.** Do this
  only after alert contacts have proven stable in production and recipient
  parity has been verified.
- **Adopt consumer-specific OpenAPI generator validation when one is chosen.**
  The route-driven `GET /api/v1/openapi.json` endpoint now includes
  handler-derived request/response component schemas, and `make test` validates
  schema refs plus a generated Go client smoke source. If production consumers
  standardize on a specific generator, add that exact tool to CI so tool-specific
  schema drift breaks before release.
- **Plan encryption-at-rest for outbound credentials before public/customer
  secret management.** Plaintext webhook secrets and alert-contact
  destination credentials are acceptable for the current internal threat
  model, but KMS-style encryption should be planned before exposing
  customer-managed secrets more broadly. See
  [`outbound-credential-encryption-plan.md`](outbound-credential-encryption-plan.md).

### P2 - v3 and product-driven extensions

- **Revisit Candidate 3 after v2 has production data.** The current leading
  v3 option is a central scheduler plus regional probe agents. The migration
  should start with richer v2 probe metadata, then durable confirmation jobs,
  generic probe agents, shadow-mode primary jobs, and gradual cutover.
- **Add regional/per-vantage status only when the support story is ready.**
  Regional classifications, per-vantage SLA, and richer `Unknown` handling
  depend on probe-agent data and taxonomy work; they should not leak to
  customers prematurely.
- **Treat alert/webhook polish as demand-driven.** Grace-period webhook secret
  rotation, `site.state_changed` webhooks, alert digest mode, quiet hours,
  external acknowledgements, SMS, and OpsGenie are clean additions, but should
  wait for customer demand or compliance pressure.
- **Retire the legacy status projection after consumers migrate.** Once
  downstream readers use the v2 API/event tables, disable
  `LEGACY_STATUS_PROJECTION_ENABLE` and stop treating stale legacy status
  values as meaningful.

---

## v3 Probe-Agent Architecture

**Status:** Parked until v2 has been deployed to production and stabilized.

The current v2 production target keeps the main-server-plus-Veriflier
confirmation model. After v2 has enough production data, revisit whether Jetmon
should evolve into a central scheduler plus regional probe-agent architecture.

See [`v3-probe-agent-architecture-options.md`](v3-probe-agent-architecture-options.md)
for the candidate architectures, data to gather during v2, and the current
recommendation.

---

## Public REST API

**Status:** Not started as a customer-facing surface. The v2 branch has an
internal `/api/v1` behind a gateway (see ADR-0002); this item is about the
public/customer contract and the gateway-facing semantics needed to expose it
safely.

### What it is

A versioned, authenticated customer-facing REST API on competitive parity with established uptime monitoring services (Pingdom, UptimeRobot, Better Uptime, Datadog Synthetics). Users and integrations interact with Jetmon entirely through this API — reading current health state, pulling event history and SLA statistics, managing what gets monitored, configuring alerts, and triggering on-demand checks.

Currently, Jetmon's API is internal-only: callers are known services, tenant isolation lives at the gateway, errors are intentionally verbose, and ownership checks are coarse. What is missing is a stable public contract with customer-scoped auth, tenant ownership, sanitized error semantics, public rate limits, and payloads safe to expose directly to customer tooling. The capability list below describes the public/customer contract target; many internal equivalents already exist and are documented in `internal-api-reference.md`.

### Why it matters

**User-facing self-service.** Customers need to provision monitors, adjust what is checked, retrieve their monitoring data, and receive alerts through their own tooling — without requiring direct database access or bespoke internal integrations from the Jetpack team. This is table stakes for a monitoring product: Pingdom, UptimeRobot, Better Uptime, and every serious competitor ship a public API as a first-class product surface.

**CI/CD and deployment tooling.** Teams that deploy frequently need to pause monitors before a deploy, resume them after, and verify that no events opened during the deployment window — all from a deploy script. That use case requires management and query capabilities together.

**uptime-bench integration.** The uptime-bench benchmark harness treats every service it evaluates as a first-class API client. The Jetmon adapter uses the manage API to provision a monitor before a scenario run and the query API to retrieve detection data afterward. Without a public API, uptime-bench must read directly from MySQL, which is fragile and tightly coupled to the internal schema.

**Jetpack feature surface.** The Jetpack dashboard, mobile apps, and any future Jetpack products that surface monitoring data consume this API rather than requiring their own DB integrations.

### Capability 1: Status and state

Read-only endpoints for current site and endpoint health.

| Endpoint | Description |
|---|---|
| `GET /api/v1/sites` | List all monitored sites with current state, severity, and active event count |
| `GET /api/v1/sites/{blog_id}` | Full current state for one site: state, severity, active events, check summary |
| `GET /api/v1/sites/{blog_id}/endpoints` | List all endpoints configured for the site with per-endpoint state |
| `GET /api/v1/sites/{blog_id}/endpoints/{endpoint_id}` | Current state for a specific endpoint |

### Capability 2: Events and history

Read-only endpoints for event timelines and raw check results.

| Endpoint | Description |
|---|---|
| `GET /api/v1/sites/{blog_id}/events` | Event history; supports `since`, `until`, `state`, `severity`, and `check_type` filters |
| `GET /api/v1/sites/{blog_id}/events/active` | Currently open (unresolved) events only |
| `GET /api/v1/sites/{blog_id}/events/{event_id}` | Single event with full metadata and causal links |
| `GET /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/events` | Events scoped to one endpoint |
| `GET /api/v1/sites/{blog_id}/history` | Raw check timing history (DNS, TCP, TLS, TTFB, status code) with time range params |
| `GET /api/v1/sites/{blog_id}/audit` | Audit log entries with time range and event type filters |

Event response schema follows `events.md`: `started_at`, `ended_at`, `severity`, `state`, `resolution_reason`, `check_type`, `cause_event_id`, `metadata`.

### Capability 3: Statistics and SLA reporting

Aggregate calculations for uptime, response time, and incident summary. This is what competitors expose for SLA reporting dashboards and customer-facing status summaries.

| Endpoint | Description |
|---|---|
| `GET /api/v1/sites/{blog_id}/uptime` | Uptime percentage for a given time range; returns total, by state (Down, Degraded, Unknown separately), and per-endpoint breakdown |
| `GET /api/v1/sites/{blog_id}/response-times` | Response time statistics for a time range: mean, p50, p95, p99, min, max, bucketed by interval |
| `GET /api/v1/sites/{blog_id}/incidents` | Incident summary: count, total duration, and MTTR for a time range |

**Design note on Unknown vs. Downtime.** The uptime calculation must honour the Unknown/Downtime distinction from `taxonomy.md`: Unknown periods (monitor-side failures, agent not reporting) are excluded from the denominator, not counted as downtime. Conflating these breaks SLA calculations and erodes user trust. The response must return the three separately: `downtime_seconds`, `degraded_seconds`, `unknown_seconds`, and `monitored_seconds` (the denominator).

### Capability 4: Monitor management

Write endpoints for programmatic endpoint and check lifecycle management.

| Endpoint | Description |
|---|---|
| `POST /api/v1/sites/{blog_id}/endpoints` | Add a new endpoint to monitor (URL, label, check types, frequency, timeout) |
| `GET /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/checks` | List checks configured on an endpoint |
| `POST /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/checks` | Add a check to an endpoint (check type, keyword, redirect policy, expected status, etc.) |
| `PUT /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/checks/{check_id}` | Update check configuration |
| `DELETE /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/checks/{check_id}` | Remove a check |
| `POST /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/pause` | Suspend all checks on an endpoint without deleting |
| `POST /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/resume` | Resume a paused endpoint |
| `PUT /api/v1/sites/{blog_id}/maintenance` | Set a maintenance window (start, end, optional recurrence) |
| `DELETE /api/v1/sites/{blog_id}/maintenance` | Clear an active maintenance window |
| `POST /api/v1/sites/{blog_id}/endpoints/{endpoint_id}/trigger` | Trigger an immediate on-demand check outside the normal schedule |

Creating or modifying checks via the API must go through the same orchestrator pickup path as a direct DB write. The API is a frontend to the system, not a bypass.

### Capability 5: Notifications and alert contacts

Programmatic management of where alerts go. Competitors that omit this force users back to a web UI for a critical configuration task.

| Endpoint | Description |
|---|---|
| `GET /api/v1/alert-contacts` | List configured alert contacts (email, webhook, PagerDuty, Slack, etc.) |
| `POST /api/v1/alert-contacts` | Create an alert contact |
| `PUT /api/v1/alert-contacts/{contact_id}` | Update a contact (endpoint URL, credentials, enabled state) |
| `DELETE /api/v1/alert-contacts/{contact_id}` | Remove a contact |
| `GET /api/v1/sites/{blog_id}/alert-contacts` | List which contacts are subscribed to a site |
| `PUT /api/v1/sites/{blog_id}/alert-contacts` | Set the alert contact list for a site |

**Alert contact types:** the internal API currently supports email, PagerDuty, Slack, and Teams. Generic customer-owned HTTP POSTs should use the HMAC-signed webhooks API instead of duplicating that surface as an alert-contact transport. Later, direct SMS or OpsGenie can be added if customer demand justifies them.

**Webhook contract.** Outbound webhook POSTs carry a standard envelope: `event_type`, `site_id`, `blog_id`, `timestamp`, `event` (the full event object). `event_type` values: `site.seems_down`, `site.down`, `site.recovered`, `site.degraded`, `maintenance.started`, `maintenance.ended`. The payload structure is versioned and must not break existing webhook consumers when new fields are added.

### Public API decisions before direct exposure

The internal API decisions are implemented in `internal/api/` and documented in
`internal-api-reference.md`. A public/customer API is a different contract and needs these
decisions before direct exposure:

**Tenant and ownership model.** The baseline gateway-to-Jetmon tenant contract
is drafted in [`public-api-gateway-tenant-contract.md`](public-api-gateway-tenant-contract.md):
the gateway remains the first tenant boundary, while Jetmon-side ownership
columns become necessary for defense in depth or any direct public exposure.
Direct customer exposure requires every read/write to be tenant-scoped.

**Auth scopes.** The internal API uses coarse `read` / `write` / `admin`
scopes. Public keys likely need granular scopes such as `sites:read`,
`events:read`, `webhooks:write`, and `alerts:write` so customer integrations can
be least-privilege.

**Error and metadata redaction.** Internal responses can expose query stages,
DB error classes, verifier names, and operational metadata. Public responses
need sanitized errors and customer-safe event metadata, with detailed context
remaining in server logs and operator-only surfaces.

**Public rate limits and abuse controls.** Internal limits are service
protection. Public limits need commerce/abuse semantics, likely per tenant plus
per key, with separate controls for expensive operations such as trigger-now.

**Webhook ownership and signing posture.** Internal HMAC signing is acceptable
today. Public customer-managed webhooks may need per-tenant ownership columns,
public-key/asymmetric signing, or stronger secret storage before direct
exposure.

**OpenAPI and compatibility policy.** The customer contract needs a generated
OpenAPI 3.1 spec, client-codegen validation, explicit deprecation rules, and
tests that fail when handler behavior drifts from the published schema.

### Public API work still to do

- Backfill and reconcile `jetmon_site_tenants` from the gateway/customer source
  of truth before customer traffic depends on Jetmon-side site enforcement.
  Initial CSV import support exists via `jetmon2 site-tenants import`; remaining
  work is agreeing on the gateway export contract and pruning/reconciliation
  policy for mappings that disappear from the source of truth.
- Add public-contract integration tests for route-level tenant success and
  denial paths across sites, events, stats, trigger-now, webhooks, and alert
  contacts.
- Add customer-safe error and metadata redaction paths for every public route.
- Promote the internal route-driven `GET /api/v1/openapi.json` contract into a
  public compatibility policy with deprecation rules and consumer-specific
  generator validation.
- Add public-contract integration tests for auth, pagination, idempotency,
  redaction, and trigger-now abuse controls.
- Revisit response-time/SLA pre-aggregation before exposing high-volume public
  reporting queries.
- Document the migration path for consumers that currently use direct MySQL or
  bespoke internal integrations.

---

## Deferred from Phase 3 (webhooks)

These were considered during Phase 3 design and intentionally left out of v1 with clean upgrade paths.

### `site.state_changed` webhook events

Phase 3 v1 ships only `event.*` webhooks (one per `jetmon_event_transitions` row). A `site.state_changed` rollup webhook — fires when the site's derived rollup state changes — was punted because:

- Detecting site-level transitions cleanly without races requires changes to the orchestrator (it currently writes `site_status` but doesn't compute deltas)
- Event-level webhooks already give consumers everything they need to compute site-level rollup themselves
- The schema for site state is downstream of the events tables; we'd be adding a second source of truth for "the site is now Down"

**When to revisit:** a real consumer asks for site-level rollup webhooks specifically. Likely shape: orchestrator computes a "previous_state → new_state" rollup from active events; a delivery worker translates that into `site.state_changed` deliveries. Same retry/filter/signature plumbing as `event.*` webhooks — the only new piece is the orchestrator-side delta computation.

### Grace-period webhook secret rotation

Phase 3 v1 ships immediate-revocation only: rotating a webhook secret invalidates the old secret immediately. Brief signature-verification failures during the consumer's deploy window go into the retry queue and resolve once the consumer rolls.

A future Phase 3.x extension is **grace-period rotation**: server signs with both old and new secrets for a configurable window (24h default), consumer verifies whichever they support, then the old secret expires. This matches Stripe's webhook signing roll model and lets consumers deploy at their own pace.

**Why this is a clean future addition:**
- Schema extension only: add `previous_secret_hash` and `previous_secret_expires_at` columns to `jetmon_webhooks`
- Header format already supports multiple `v1=` values (Stripe-compatible)
- New endpoint shape: `POST /webhooks/{id}/rotate-secret?grace=24h`
- No migration of existing webhooks needed; immediate-revocation is the default if `?grace` is absent

**When to revisit:** a customer-managing consumer (not the gateway, not internal alerting) registers webhooks and asks for graceful rotation, or a compliance requirement forces routine secret rotation.

---

## Deferred from Phase 3.x (alert contacts)

These were considered during Phase 3.x design and intentionally left out of v1. Each has a clean addition path that doesn't disturb the v1 schema or worker shape.

### Generic outbound webhook as an alert-contact transport

Phase 3.x ships four managed transports: email, PagerDuty, Slack, Teams. A "generic webhook" alert-contact transport (POST a Jetmon-formatted JSON payload to any URL) was considered and rejected because the webhooks API (Family 4) already covers it — and covers it better, with HMAC signing, configurable filters across more dimensions, and a fully programmable payload shape.

**The boundary:** alert contacts deliver Jetmon-rendered notifications through Jetmon-owned transports. Webhooks deliver the raw signed event stream for the consumer to render. A customer who wants "POST to my URL when sites change" should register a webhook; we shouldn't ship a duplicate surface that does the same thing worse.

**When to revisit:** never, unless the boundary itself shifts (e.g. webhooks API gets removed, or alert contacts grows into a fundamentally different abstraction).

### SMS notifications

Skipped in v1. WPCOM SMS infrastructure availability is unclear, and a third-party SMS provider integration (Twilio/MessageBird/etc.) is a non-trivial credentialing and billing addition. PagerDuty already offers SMS as a downstream config — the dominant SMS use case is "page me," and that's already covered.

**When to revisit:** a customer asks specifically for direct SMS without going through PagerDuty, AND a stable SMS sending channel (WPCOM-owned or vendor-procured) is available.

### OpsGenie transport

Skipped in v1. Same shape as PagerDuty but a different vendor; PagerDuty covers the dominant slice of customers who want incident-management routing. Adding OpsGenie is mechanical (new transport implementation, ~100 LoC) once a customer asks.

**When to revisit:** a customer running OpsGenie asks for direct integration. Until then, they can route via webhook to OpsGenie's events API themselves.

### Quiet hours / on-call schedules

Per-contact "don't page me between 11pm and 7am" or "route to alternate contact during my vacation" was considered and deferred. Reasons:

- PagerDuty already handles this on its end with full schedule support; customers using PagerDuty don't need it from Jetmon.
- For Slack/email/Teams contacts, channel-level mute or auto-responders work as a workaround.
- Building scheduling into Jetmon is a rabbit hole — timezone handling, recurring patterns, escalation overrides, holiday lists. Each of those is a feature in itself.

**When to revisit:** strong customer demand specifically for non-PagerDuty contacts AND a clear scope for what "scheduling" means in v1 (probably starts with a single per-contact `quiet_hours: {start, end, tz}` field, not full PagerDuty parity).

### Alert acknowledgements

"Operator acks an alert from PagerDuty/Slack and Jetmon stops re-paging" was considered and deferred because it's bidirectional — Jetmon would need to receive callbacks from each transport, store ack state, and gate further deliveries against it. That's a significant new surface (inbound webhooks from PagerDuty, Slack interactivity API, etc.) for a feature most customers handle within their incident-management tool.

**When to revisit:** a customer specifically asks for cross-channel ack state (e.g. "I acked in PagerDuty, don't keep posting to Slack"). Probably ships as a per-contact `respect_external_ack: bool` flag plus per-transport ack-receiver implementations.

### Alert grouping / digest mode

When a regional outage flips 50 sites at once, v1 sends 50 separate notifications per matching contact (modulo the per-hour rate cap, which kicks in but only as a brake, not a grouping mechanism). A real grouping/digest feature — "send one email containing all transitions in the last 5 minutes" — was deferred.

**Why deferred:** per-event delivery matches webhook semantics, is the simplest semantic to reason about, and is what most monitoring tools start with. Grouping introduces real questions (window size, group boundary criteria, what happens if a transition arrives mid-group) that benefit from real customer feedback.

**When to revisit:** real users complain about pager noise during regional outages even with `max_per_hour` set. Likely shape: per-contact `digest_window_seconds` field; transitions within the window batch into one notification at window end.

### Migrate WPCOM notifications behind alert contacts

Phase 3.x ships alert contacts alongside the existing WPCOM notification flow rather than migrating the WPCOM flow to be a transport behind alert contacts. The two paths coexist; same human can be in both and receive duplicate notifications.

**Why deferred:** drop-in compatibility with the existing v1 deployment shape is more important than architectural unification. Migrating WPCOM-flow consumers to alert contacts requires:
- Inventorying all current WPCOM notification recipients and their subscription patterns
- Building a `wpcom` transport (or reusing an existing one) that delivers through the same channel
- Migrating the per-recipient subscription data into `jetmon_alert_contacts`
- Verifying nothing regresses for the existing recipients during cutover

This is a coordinated migration, not a code change — and it's safer to do once alert contacts has proven out in production with real customers.

**Why this is a clean future addition:**
- The transport interface is already pluggable; adding a `wpcom` transport is the same shape as `email`/`pagerduty`/`slack`/`teams`.
- The orchestrator's existing WPCOM notification call site becomes a simple "delete this code path" once parity is verified.
- The deliverer-binary extraction (see Architectural roadmap below) becomes meaningfully cleaner with WPCOM unified — it's the third transport that justifies the split.

**When to revisit:** alert contacts has been in production for 1–3 months without major issues, AND the deliverer-binary extraction is being actively planned. The two are the same conversation.

---

## Architectural roadmap

### Multi-repo / multi-binary split

Today everything lives in one repo and the `jetmon2` binary contains the orchestrator, the API server, the operator dashboard, and (after Phase 3) the webhook delivery worker. The `veriflier2` binary is already separate but in the same repo.

This is fine for now but won't scale operationally. Different concerns have very different deployment shapes:

| Concern | Scaling axis | Deployment shape |
|---------|--------------|------------------|
| Orchestrator | bucket count, check rate | stateful (claims buckets in `jetmon_hosts`); horizontal via bucket coordination |
| API server | request rate | stateless; horizontal behind a load balancer |
| Outbound delivery | event volume + slow third parties | stateless; horizontal via row-claim on per-transport delivery tables |
| Operator dashboard | one-off operator sessions | one per ops region |
| Veriflier | geo-distributed vantage points | one per region |

Putting everything in one binary means scaling the most expensive concern scales the cheap ones with it (CPU and memory headroom that's only used for one purpose). It also concentrates failure modes — a panic in the API server takes down the orchestrator.

**Plausible split:**
- `jetmon-orchestrator` — round loop, check pool, DB writes
- `jetmon-api` — REST API server, auth, rate limiting (read/write surface)
- `jetmon-deliverer` — all outbound dispatch: webhooks (Phase 3), alert contacts, WPCOM notifications
- `jetmon-dashboard` — operator UI / SSE state stream
- `jetmon-verifier` — standalone HTTP check executor (today: `veriflier2`; rename TBD)

**Why `jetmon-deliverer` is one binary, not three.** Webhooks, alert contacts, and WPCOM notifications all share the same plumbing: poll `jetmon_event_transitions` (or a similar source), build a frozen-at-fire-time payload, dispatch with a per-destination in-flight cap, retry on failure with exponential backoff, mark abandoned after N attempts. Only the transport differs (HTTPS POST + HMAC for webhooks, transport-specific protocols for PagerDuty/Slack/email/SMS, internal RPC for WPCOM). Splitting them into separate binaries would triple the operational surface (three deploy units, three retry queues, three sets of metrics) for what is fundamentally one job — outbound dispatch — with pluggable transports. Keeping them in one process also means a single circuit-breaker registry across destinations, which is the natural place to enforce shared-resource caps (e.g. "don't open 5,000 outbound connections during a regional outage").

What this means concretely:
- The Phase 3 webhook worker (`internal/webhooks/worker.go`) is the seed. Its `dispatchTick` / `deliverTick` shape generalizes — the matching, claiming, retry, and abandon logic is transport-agnostic.
- A future refactor abstracts the transport behind a `Dispatcher` interface (`Send(ctx, dest, payload) (status, error)`), with concrete implementations per channel.
- Per-channel state (webhook subscriptions, alert contacts, WPCOM circuit breaker counters) stays in its own table; the worker loops over each.

**Revisit point: unify `internal/alerting/` and `internal/webhooks/`.** Phase 3.x ships alert contacts as a separate package (`internal/alerting/`) parallel to webhooks, deliberately *not* extending the webhook worker. The reasoning at the time was: alerting hadn't been built yet, we didn't know what shape it would actually take (fan-out? escalation? digest mode?), and forcing a shared abstraction with one known user (webhooks) and one guessed-at user (alerting) risked an abstraction that fits neither well. Better to build alerting concretely, see where the duplication actually lands, and factor with two real implementations in hand.

The deliverer-binary extraction is the natural moment to revisit. By then we'll have:
- Two concrete dispatch workers in production with known operational profiles.
- A clear picture of what alerting actually grew into vs. what webhooks needed.
- A real third transport on the way (WPCOM migration), which validates the abstraction against three users instead of two.

At that point, factor a `Dispatcher` interface against the three known shapes — not before. The duplication cost between `internal/webhooks/` and `internal/alerting/` is bounded (~300 lines); the cost of a wrong abstraction is unbounded.

**Trigger that justifies the split.** A single outbound transport doesn't justify its own binary — webhooks alone could stay co-located with the orchestrator. The argument gets compelling once there are *multiple* transports to dispatch and a shared retry/circuit-breaker substrate to amortize. Adding alert contacts is the moment the abstraction earns its keep; pulling WPCOM notifications out of the orchestrator at the same time is the cleanup that pays off the extraction.

The MySQL schema is already the implicit bus between these — each service reads/writes specific tables. Splitting would mostly be:
1. Extract each concern into its own `cmd/<name>/` directory with a thin main
2. Move shared types into `pkg/` (currently `internal/`) so the binaries can depend on them across repos
3. Decide on repo boundaries (one monorepo with multiple binaries, vs. multiple repos sharing a `pkg/` module)

**Naming opportunity:** "veriflier" is a long-standing typo of "verifier" that has stuck around through the rewrite. A split is a natural moment to rename. Candidates: `verifier`, `witness`, `probe-worker`, `vantage`. Worth deciding before the split happens, not during.

**When to revisit:** when a single binary's resource needs (CPU, memory, restart blast radius) starts working against the operational sweet spot for one of the concerns. The deliverer split specifically becomes worthwhile when alert contacts ship — that's the second outbound transport, and a third (WPCOM notifications) follows for free since they already exist as code that wants to live next to the others.

### Path to a public API

Today's API is internal-only — every caller is a known service (gateway, alerting workers, dashboard) and tenant isolation lives at the gateway. Several Phase 1–3 design decisions take advantage of that and would have to change if Jetmon ever exposes its API directly to end customers without a gateway in front.

The decisions affected:

| Decision | Internal-API form | Public-API form |
|----------|-------------------|-----------------|
| Auth scopes | Three coarse: `read` / `write` / `admin` | Granular per-resource (e.g. `sites:read`, `events:read`, `webhooks:write`) so customer keys can be scoped tightly |
| Error semantics | Honest 401/403/404 (no info-leak hiding) | 404-on-unauthorized (don't leak existence of resources owned by other tenants) |
| Error message verbosity | Verbose (DB error class, query stage) for incident response | Sanitized — internal detail belongs in server logs only |
| Webhook ownership | Any `write`-scope token can manage any webhook (`created_by` audit only) | Per-tenant ownership column; reads/writes filtered by owner |
| Webhook signing | HMAC-SHA256 with shared secret per webhook | Asymmetric (Ed25519) becomes more attractive — public key at a well-known URL, no per-customer secret to leak |
| Rate limiting | Per-key bucket sized for service protection | Per-tenant bucket sized for commerce/abuse |
| Idempotency keys | Scoped by `(api_key_id, key)` | Scoped by `(tenant_id, api_key_id, key)` to prevent cross-tenant collisions |
| Site `id` (= `blog_id`) | Numeric, canonical from WPCOM | Probably still numeric, but tenant-scoped on lookup |

The migrations are individually clean (each is "add a column, filter on it, deprecate the unscoped version") but they touch most of the API surface. A public-API exposure would be a significant project, not a flag flip.

**When to revisit:** if a stakeholder asks "can a customer integration call Jetmon directly?" — the answer should be "let's design that" rather than "yes, here's the URL."

The Q9 (webhook ownership) section in internal-api-reference.md captures the most concrete piece of this; the rest is captured here for visibility when the conversation comes up.

---

## Completed

This section lists major roadmap-level work completed since the v1 baseline,
including both the original `v2` rewrite and later work on this branch. It is
intentionally higher level than a changelog: entries explain what exists now,
where to look, and what each item unlocked.

### v1-to-v2 Rewrite Foundation

- **Single Go monitor binary.** Jetmon 2 replaces the Node.js master/worker
  process tree and C++ native HTTP checker addon with the Go `jetmon2` binary.
  This removes `npm`, `node-gyp`, and native-addon build friction while keeping
  the legacy external contracts intact.
- **Go check pool with bounded concurrency.** HTTP checks run through
  `internal/checker` using goroutines, `net/http`, and `httptrace` timing
  capture instead of the v1 native addon.
  The pool records DNS, TCP, TLS, TTFB, and total RTT timings and can adjust
  worker count under queue or memory pressure.
- **Go orchestrator and retry queue.** The v2 orchestrator owns round
  scheduling, local retry state, Veriflier escalation, WPCOM notifications, and
  graceful drain behavior.
  This preserves the v1 detection flow while making the retry queue and
  shutdown behavior testable in Go.
- **Go Veriflier replacement.** `veriflier2` replaces the Qt/C++ Veriflier
  with a small Go HTTP service and shared check logic.
  The old custom SSL server dependency is gone, and the transport is easier to
  test and deploy.
- **Embedded migrations and schema bootstrap.** `jetmon2 migrate` applies the
  v2 additive schema and can create the legacy `jetpack_monitor_sites` table in
  local/dev databases.
  This makes fresh Docker environments and production schema upgrades use the
  same migration path.
- **MySQL bucket coordination.** v2 introduced `jetmon_hosts` ownership and
  heartbeat logic so hosts can claim, release, and reclaim bucket ranges
  dynamically.
  Static v1 bucket ranges are still supported later through pinned rollout
  mode, but dynamic ownership is the v2 steady-state target.
- **Compatibility-preserving StatsD and stats files.** The Go metrics layer
  keeps the existing StatsD prefix shape and `stats/` file outputs used by
  legacy monitoring.
  This lets operational dashboards survive the rewrite while new metrics are
  added incrementally.
- **WPCOM client with circuit breaker.** The v2 WPCOM client preserves the
  legacy notification payload while adding bounded queueing and circuit-breaker
  behavior.
  This protects monitor rounds from prolonged WPCOM API failures.
- **Operator dashboard and health surface.** v2 added a built-in dashboard for
  worker state, queues, buckets, memory, WPCOM circuit state, and later rollout
  and dependency health.
  It gives operators a first-party view into the monitor without querying the
  database directly.
- **Systemd and logrotate packaging.** The v2 branch added production service
  and logrotate templates for the Go monitor.
  These files provide the baseline deployment shape for rolling host updates.
- **Initial Docker Go development environment.** Docker builds now compile the
  Go monitor and Veriflier, run migrations, and use the new config-rendering
  entrypoints.
  Later Docker cleanup refined ports, permissions, Mailpit, healthchecks, and
  non-root MySQL credentials.

### Core State and Detection

- **Event-sourced incident state.** Jetmon now writes authoritative incident
  state to `jetmon_events` and append-only lifecycle history to
  `jetmon_event_transitions`.
  Useful for: reconstructing incidents, API reads, webhook/alert delivery, and
  legacy projection drift checks.
- **Shadow-state migration support.** The legacy `site_status` projection is
  maintained behind `LEGACY_STATUS_PROJECTION_ENABLE` while v2 event tables
  remain authoritative.
  This keeps v1 consumers working during migration without making the legacy
  column the source of truth.
- **API state derived from v2 events.** Site API responses use open v2 events
  to report current health state instead of trusting only the legacy site row.
  This keeps the API aligned with the eventstore during the shadow migration.
- **Detection-flow instrumentation.** StatsD now captures first failure to
  Seems Down, first failure to Veriflier escalation, Seems Down to Down,
  false-alarm timing, and probe-cleared recovery timing.
  These metrics are the data set needed to evaluate future v3 probe-agent
  designs with production evidence.
- **Outcome metrics split by failure class.** False alarms and confirmed-down
  outcomes are split by local failure class such as `server`, `client`,
  `blocked`, `https`, `redirect`, and `intermittent`.
  This makes it possible to see which failure classes produce useful
  confirmations and which produce noisy escalations.
- **Veriflier hardening and observability.** Veriflier request handling now has
  stronger validation, safer body limits, clearer config behavior, and
  per-host RPC/vote metrics.
  The v2 production transport is documented as JSON-over-HTTP, with proto files
  retained only as a future schema reference.
- **WPCOM notification parity metrics.** Legacy WPCOM notification attempts,
  deliveries, retries, errors, and final failures are counted with
  status-specific splits.
  This supports production parity checks while WPCOM remains outside the new
  deliverer path.

### API and Gateway Surface

- **Internal REST API foundation.** The internal `/api/v1` surface now includes
  API-key auth, read endpoints, event detail/list endpoints, SLA/stat queries,
  and authenticated write endpoints.
  This moved Jetmon from DB-only integration toward a service boundary for
  dashboards, gateway callers, CI tooling, and delivery workers.
- **Idempotent writes and scope enforcement.** POST-style writes support
  idempotency keys, and route-level scope checks are covered through the full
  mux.
  API key revocation also honors future `revoked_at` timestamps so rotations
  can use a grace window.
- **Site management write surface.** The API can create/update/delete/pause/
  resume sites, close events, and trigger an immediate check.
  The write handlers preserve the eventstore and legacy-projection invariants
  used by the orchestrator.
- **Site scheduling fields in API responses.** API site payloads now expose
  operational scheduling/config fields such as check interval, maintenance
  window, redirect policy, keyword, SSL expiry, and alert cooldown.
  This lets API consumers inspect the settings that affect monitoring behavior.
- **Site soft-delete contract.** The soft-delete behavior is documented so
  collaborators know how disabled sites are represented and what API consumers
  should expect.
  This avoids accidental hard-delete semantics while the legacy table remains
  shared infrastructure.
- **Gateway tenant boundary.** The gateway-to-Jetmon tenant contract is
  documented, and gateway-routed requests now carry trusted tenant context
  through the API middleware.
  Non-gateway consumers cannot spoof public-context headers.
- **Tenant ownership enforcement.** Gateway-routed site, event, stats,
  trigger-now, webhook, alert-contact, delivery, and manual retry paths are
  scoped through `jetmon_site_tenants` or resource `owner_tenant_id`.
  This gives defense-in-depth behind the gateway while preserving unscoped
  internal-operator behavior.
- **Site tenant import tooling.** `jetmon2 site-tenants import` can load
  `tenant_id,blog_id` mappings from CSV, including dry-run validation.
  This provides the operator path for backfilling gateway ownership data before
  customer traffic depends on Jetmon-side checks.
- **Gateway tenant route tests.** Public-contract tests now cover mapped and
  unmapped gateway paths across the key route families, including event lists,
  transition lists, and trigger-now.
  These tests reduce the risk that future API work bypasses tenant ownership
  checks.
- **Route-driven OpenAPI contract.** `GET /api/v1/openapi.json` is generated
  from the route table with request/response component schemas.
  Tests validate schema references and smoke-check generated Go client source
  so route/schema drift is caught early.

### Delivery and Alerting

- **HMAC webhook delivery.** Webhook CRUD, HMAC-signed outbound delivery,
  filtering, retry, abandonment, delivery listing, and manual retry are
  implemented.
  Payloads are frozen at fire time so consumers see the event state that caused
  the delivery.
- **Alert contacts.** Managed alert contacts now support email, PagerDuty,
  Slack, and Teams, with send-test endpoints, delivery listing/retry, retry
  behavior, and per-contact rate caps.
  Email supports `stub`, `smtp`, and `wpcom` senders so local, staging, and
  production modes can share the same API.
- **Delivery claiming.** Webhook and alert-contact delivery workers claim rows
  before dispatch so multiple workers do not dispatch the same pending delivery.
  This is the database coordination point that makes standalone delivery
  feasible.
- **Delivery owner guard.** `DELIVERY_OWNER_HOST` constrains embedded delivery
  to the intended host during conservative rollout.
  This lets API-enabled hosts serve traffic without accidentally becoming
  outbound delivery owners.
- **Standalone deliverer entry point.** `bin/jetmon-deliverer` runs webhook
  and alert-contact workers without starting the monitor, API, dashboard, or
  bucket ownership loop.
  It is the first concrete process boundary for the future outbound-delivery
  split.
- **Deliverer service packaging.** A sample
  `systemd/jetmon-deliverer.service` now exists, and `jetmon-deliverer
  validate-config` checks config parsing, DB connectivity, email transport
  mode, and delivery ownership.
  The rollout docs describe the service, process-specific `deliverer.json`,
  and the shared `DB_*` environment expectations.
- **Deliverer rollout checks.** `jetmon-deliverer delivery-check` summarizes
  webhook and alert-contact delivery queues from the shared MySQL tables.
  Operators can inspect pending, due, retry, delivered, abandoned, failed, and
  oldest-queue-age signals in text or JSON and enforce explicit thresholds
  during standalone-deliverer cutover or rollback.

### Rollout and Operations

- **Pinned v1-to-v2 rollout mode.** v2 hosts can run pinned to the exact bucket
  range of the v1 host they replace.
  Example: `./jetmon2 rollout guided` wraps static-plan validation,
  host-preflight, cutover checks, and rollback gates with prompts, transcript
  logging, and resume state; `./jetmon2 rollout host-preflight` is the direct
  pre-stop gate for manual runs.
- **Post-start cutover check.** `./jetmon2 rollout cutover-check` bundles the
  read-only pinned preflight, recent activity check, dashboard status check,
  and projection-drift report used after each v1 host replacement.
  Operators can run it immediately after start, then again with `--require-all`
  after one full expected check round.
- **Rollout check JSON output.** Rollout gate commands accept `--output=json`
  so Systems automation can parse a stable pass/fail envelope while retaining
  the same non-zero exit behavior as text mode.
- **Rollout quick reference.** `docs/rollout-quick-reference.md` gives
  operators a one-page command checklist for rehearsal, per-host cutover,
  rollback, fleet completion, and JSON automation while linking back to the
  full migration runbook as source of truth.
- **Rollout state report.** `./jetmon2 rollout state-report` summarizes the
  current ownership mode, bucket coverage, recent activity, projection drift,
  delivery-owner state, and suggested next action for operator handoffs.
- **Host preflight gate.** `./jetmon2 rollout host-preflight` bundles the
  pre-stop static plan assertion, config/DB load, pinned safety checks, and
  staged systemd validation. Rehearsal plans can now include exact v1 stop/start
  commands and explicit rollback hold points.
- **Dynamic ownership preflight.** `./jetmon2 rollout dynamic-check` verifies
  that pinned ranges are removed, `jetmon_hosts` rows cover the full bucket
  range without gaps/overlaps, heartbeats are fresh, and projection drift is
  zero.
  This supports the second step after every host has moved safely to v2.
- **Projection drift reporting.** `./jetmon2 rollout projection-drift` prints
  bucket/status summaries, likely causes, sample rows, and the specific active
  sites whose legacy projection disagrees with the authoritative open HTTP
  event.
  Operators get actionable diagnostics and manual repair guidance instead of a
  count-only rollout failure.
- **Rollout guidance in validation and dashboard.** `validate-config` prints
  the correct rollout preflight and drift-report commands, while the operator
  dashboard shows bucket mode, projection mode, delivery ownership, rollout
  commands, and dependency health.
  This keeps migration-critical state visible before and during cutover.
- **Systemd service cleanup.** The monitor unit now places start-limit keys in
  the correct systemd section, and the deliverer unit validates with
  `systemd-analyze`.
  This removes avoidable service-file warnings before production packaging.
- **Docker development cleanup.** The Docker setup now has clearer local env
  names, hardcoded container-internal ports, explicit host-port overrides,
  non-root MySQL credentials, Mailpit, healthchecks, MySQL readiness waits, and
  runtime permission fixes.
  Local development now better matches the process and dependency shape used by
  v2.

### Documentation, Tests, and Tooling

- **Architecture and ADR refresh.** The architecture docs, API reference,
  AGENTS guidance, and ADRs were brought back in line with the current v2
  health-platform shape.
  This captures the "why" behind event-sourced state, pull-only delivery,
  webhook signatures, gateway tenant boundaries, and credential-storage tradeoffs.
- **v3 architecture options documented.** The v3 probe-agent candidates are
  parked in `v3-probe-agent-architecture-options.md` until v2 has
  production data.
  Candidate 3 remains the leading option, but the roadmap now says which data
  should be collected before revisiting it.
- **Outbound credential encryption plan.** The repo has a staged plan for
  encrypting webhook secrets and alert-contact destination credentials at rest.
  The plan preserves current internal behavior while defining dual-write,
  backfill, encrypted-required, and plaintext-removal phases.
- **Build and generation cleanup.** `make all` builds the monitor, deliverer,
  and Veriflier binaries without requiring generated gRPC code, and Makefile
  targets use an explicit Go path and writable build cache.
  This keeps normal build/test workflows reliable in local and CI-like shells.
- **API CLI and deterministic rehearsal workflows.** `jetmon2 api` now has
  typed commands, smoke workflows, batch-owned test data, remote-write
  guardrails, a Docker-local failure/webhook fixture, `make api-cli-smoke`,
  and `make api-cli-validate`.
  This gives local, staging, and CI rehearsals a repeatable way to exercise the
  internal API without hand-written curl scripts.
- **Rollout preflight and deliverer hardening branches.** The v2 branch now has
  static bucket plan validation, pinned/dynamic/cutover/activity/rollback/drift
  rollout checks, standalone `jetmon-deliverer` validation, delivery queue
  checks, and matching operator docs.
  These are the guardrails needed before replacing the first v1 production
  monitor host or splitting outbound delivery.
- **Coverage and race-test expansion.** Core packages gained coverage for
  list handlers, lifecycle helpers, API audit paths, delivery behavior,
  startup helpers, and previously racy tests.
  The branch now has broader regression coverage around the shared API and
  delivery paths that are most likely to be touched next.

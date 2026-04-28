# Jetmon Roadmap

Deferred features that are intentionally out of scope for the current implementation but have been identified as important future work. Items here are tracked so they are not forgotten and can be designed with future compatibility in mind.

---

## Prioritized TODO

This is the current implementation/refinement queue. Lower-priority items are
not abandoned; they are intentionally sequenced behind the v2 production
migration and the operating data needed to make larger architecture decisions.

### P0 - v2 production hardening

- **Keep the v2 deployment target conservative.** Ship and stabilize the
  current main-server-plus-Veriflier design before moving toward a v3
  probe-agent architecture. The v2 event tables remain authoritative while
  `LEGACY_STATUS_PROJECTION_ENABLE` keeps legacy `site_status` /
  `last_status_change` consumers working during migration. Use the pinned
  bucket rollout path for the first v1-to-v2 production migration, then remove
  `PINNED_BUCKET_*` after every host is on v2 and stable.
- **Keep rollout health visible before cutover.** Operators should not have to
  infer migration-critical state from logs or config while replacing v1 hosts.
  The operator dashboard now shows bucket ownership mode, legacy projection
  mode, delivery-worker ownership, rollout preflight commands, and live
  dependency health for MySQL, Verifliers, WPCOM, StatsD, and log/stats disk
  writes. Keep this visible and verified during rollout rehearsal because it
  helps separate customer-site downtime from monitor-side impairment during
  cutover.
- **Use delivery ownership as a rollout guard.**
  In the single-binary deployment, `API_PORT > 0` also starts webhook and
  alert-contact delivery workers. A standalone `jetmon-deliverer` entry point
  and transactional `SELECT ... FOR UPDATE` row claims now exist; use
  `DELIVERY_OWNER_HOST` as a rollout guard when intentionally keeping delivery
  single-owner during migration from embedded to standalone delivery.
- **Run a production rollout rehearsal pass.** Validate that README,
  `docs/v1-to-v2-pinned-rollout.md`, config samples, systemd units,
  `validate-config`, `rollout static-plan-check`, `rollout pinned-check`,
  `rollout activity-check`, `rollout rollback-check`,
  `rollout projection-drift`, and rollback steps line up exactly before the
  first production host replacement.
- **Instrument the data needed for the v3 decision.** During v2 production,
  measure first-failure-to-`Seems Down`, `Seems Down`-to-`Down`, false alarm
  rate by failure class, Veriflier agreement/disagreement by region, Veriflier
  latency/timeout rates, mixed-region outcomes, monitor-side `Unknown` cases,
  primary-check vs confirmation cost, operator explanation gaps, and WPCOM
  notification parity. StatsD now emits the core detection timings, outcome
  counters split by local failure class, and per-Veriflier-host RPC/vote
  counters, plus legacy WPCOM notification attempt/delivered/retry/error/failed
  counters split by status. Durable report queries should wait until v2 has
  enough real traffic to prove which questions operators actually need to ask.
- **Watch projection drift as a production bug.** While the legacy projection
  is enabled, event mutations, transition rows, and the site-row projection
  must remain transactionally consistent. `jetmon2 rollout projection-drift`
  lists the exact active sites whose legacy projection disagrees with the
  authoritative HTTP event state, so rollout failures are actionable instead of
  count-only.
- **Keep roadmap/API documentation drift out of the branch.** `API.md` is the
  source for the implemented internal `/api/v1` route surface. This roadmap
  should track only the remaining public/customer API work, production
  hardening, and deferred architecture choices.

### P1 - post-v2 platform refinement

- **Extract `jetmon-deliverer` when delivery scale or blast radius warrants
  it.** Move webhook delivery, alert-contact delivery, and eventually WPCOM
  notification dispatch behind one outbound-delivery binary. Initial shared
  worker wiring, a standalone `jetmon-deliverer` entry point, and
  transactional row claims exist. A sample systemd service is available at
  `systemd/jetmon-deliverer.service`. The rollout policy is captured in
  [`docs/jetmon-deliverer-rollout.md`](docs/jetmon-deliverer-rollout.md);
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
  [`docs/outbound-credential-encryption-plan.md`](docs/outbound-credential-encryption-plan.md).

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

See [`docs/v3-probe-agent-architecture-options.md`](docs/v3-probe-agent-architecture-options.md)
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

Currently, Jetmon's API is internal-only: callers are known services, tenant isolation lives at the gateway, errors are intentionally verbose, and ownership checks are coarse. What is missing is a stable public contract with customer-scoped auth, tenant ownership, sanitized error semantics, public rate limits, and payloads safe to expose directly to customer tooling. The capability list below describes the public/customer contract target; many internal equivalents already exist and are documented in `API.md`.

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

Event response schema follows `EVENTS.md`: `started_at`, `ended_at`, `severity`, `state`, `resolution_reason`, `check_type`, `cause_event_id`, `metadata`.

### Capability 3: Statistics and SLA reporting

Aggregate calculations for uptime, response time, and incident summary. This is what competitors expose for SLA reporting dashboards and customer-facing status summaries.

| Endpoint | Description |
|---|---|
| `GET /api/v1/sites/{blog_id}/uptime` | Uptime percentage for a given time range; returns total, by state (Down, Degraded, Unknown separately), and per-endpoint breakdown |
| `GET /api/v1/sites/{blog_id}/response-times` | Response time statistics for a time range: mean, p50, p95, p99, min, max, bucketed by interval |
| `GET /api/v1/sites/{blog_id}/incidents` | Incident summary: count, total duration, and MTTR for a time range |

**Design note on Unknown vs. Downtime.** The uptime calculation must honour the Unknown/Downtime distinction from `TAXONOMY.md`: Unknown periods (monitor-side failures, agent not reporting) are excluded from the denominator, not counted as downtime. Conflating these breaks SLA calculations and erodes user trust. The response must return the three separately: `downtime_seconds`, `degraded_seconds`, `unknown_seconds`, and `monitored_seconds` (the denominator).

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
`API.md`. A public/customer API is a different contract and needs these
decisions before direct exposure:

**Tenant and ownership model.** The baseline gateway-to-Jetmon tenant contract
is drafted in [`docs/public-api-gateway-tenant-contract.md`](docs/public-api-gateway-tenant-contract.md):
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

The Q9 (webhook ownership) section in API.md captures the most concrete piece of this; the rest is captured here for visibility when the conversation comes up.

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

### Rollout and Operations

- **Pinned v1-to-v2 rollout mode.** v2 hosts can run pinned to the exact bucket
  range of the v1 host they replace.
  Example: `./jetmon2 rollout pinned-check` verifies pinned config, projection
  writes, dynamic-ownership absence, active-site coverage, and projection drift
  before cutover.
- **Dynamic ownership preflight.** `./jetmon2 rollout dynamic-check` verifies
  that pinned ranges are removed, `jetmon_hosts` rows cover the full bucket
  range without gaps/overlaps, heartbeats are fresh, and projection drift is
  zero.
  This supports the second step after every host has moved safely to v2.
- **Projection drift reporting.** `./jetmon2 rollout projection-drift` lists
  the specific active sites whose legacy projection disagrees with the
  authoritative open HTTP event.
  Operators get actionable rows instead of a count-only rollout failure.
- **Rollout guidance in validation and dashboard.** `validate-config` prints
  the current rollout safety commands, while the operator dashboard shows bucket
  mode, projection mode, delivery ownership, rollout preflight/activity/
  rollback/drift commands, and dependency health.
  This keeps migration-critical state visible before and during cutover.
- **Static bucket plan preflight.** `./jetmon2 rollout static-plan-check`
  validates the copied v1 host bucket plan before any host is stopped.
  Operators can catch gaps, overlaps, invalid ranges, and duplicate host rows
  while the rollback surface is still just the unmodified v1 fleet, then assert
  the exact host/range being copied into each pinned v2 config.
- **Post-cutover activity preflight.** `./jetmon2 rollout activity-check`
  verifies active sites in the rollout range have fresh `last_checked_at`
  values after a host replacement. It gives operators an executable check for
  "this range is being processed now" before they move to the next v1 host.
- **Rollback safety preflight.** After the v2 service has been stopped,
  `./jetmon2 rollout rollback-check` verifies the host no longer owns dynamic
  buckets, no other dynamic host overlaps the rollback range, and the legacy
  status projection is clean before v1 is restarted for that range.
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
  parked in `docs/v3-probe-agent-architecture-options.md` until v2 has
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
- **Coverage and race-test expansion.** Core packages gained coverage for
  list handlers, lifecycle helpers, API audit paths, delivery behavior,
  startup helpers, and previously racy tests.
  The branch now has broader regression coverage around the shared API and
  delivery paths that are most likely to be touched next.

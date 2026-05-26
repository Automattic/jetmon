# Roadmap

This file tracks deferred work and future product/platform direction. Completed
implementation history belongs in [changelog.md](changelog.md). Accepted
architecture decisions belong in [decisions.md](decisions.md).

## Current Priority

1. Finish the safe v1-to-v2 production rollout.
2. Keep evidence surfaces accurate: events, transitions, audit, check history,
   StatsD, dashboard, and API.
3. Reduce rollback risk before expanding detection sensitivity.
4. Avoid introducing new customer-visible surfaces until the internal API and
   gateway contract have production evidence.

## Production Rollout Readiness

Remaining rollout work should improve confidence, not add scope.

- Keep preflight and guided rollout checks aligned with the real deployment
  path.
- Maintain a small, approved canary set for rollout smoke and method comparison.
- Make projection drift checks easy to run and explain.
- Keep Veriflier quorum reports visible to operators.
- Keep WPCOM notification parity evidence attached to rollout decisions.
- Track legacy consumers of `site_status`, legacy stats files, and WPCOM
  notification payloads until they are retired.

## Config And Compatibility Cleanup

After production cutover:

- remove migration-only aliases once all production configs have moved;
- retire pinned bucket mode where dynamic ownership is accepted;
- document and then remove unused config keys;
- decide when `LEGACY_STATUS_PROJECTION_ENABLE` can default off;
- identify any remaining consumers of legacy stats file bodies.

Compatibility cleanup should happen in small PRs with explicit rollback notes.

## Veriflier And Probe Model

Near-term:

- continue validating `/v2/check` and `/v2/status` under production-like load;
- improve quorum diagnostics when vantages disagree or overload;
- keep a floor that prevents one healthy Veriflier from confirming downtime;
- preserve JSON/HTTP as the production transport.

Future options:

- stronger probe metadata in the current v2 model;
- peer probe mesh;
- central scheduler with regional probe agents;
- always-on multi-region quorum;
- hybrid external probes plus site/WPCOM signals.

Do not start a v3 probe-agent migration until v2 production data shows the
specific failure mode it needs to solve.

## Detection And Scenario Coverage

Candidates after rollout:

- richer WordPress fatal/database/configuration detection;
- better WAF and bot-protection classification;
- redirect-chain change detection;
- default virtual-host and suspension-page detection;
- TLS operational warnings beyond expiry and deprecated versions;
- DNS diagnostic detail that does not misclassify monitor-side failures as
  downtime;
- customer-tunable sensitivity profiles with safe defaults.

Every new detection rule needs:

- an event state/severity mapping;
- false-positive and false-negative examples;
- support-facing explanation text;
- rollout strategy by cohort;
- tests with controlled fixtures.

## Scheduler And Scalability

Continue capacity work around:

- target reload cadence;
- check-history write volume and sampling mode;
- runtime freshness write volume;
- checker pool sizing and auto-scaling;
- MySQL index coverage;
- StatsD and dashboard overhead;
- memory and goroutine stability during long soaks.

Evidence should include phase timings, queue depth, stale-check count, DB write
latency, p95/p99 check timing, memory/goroutine profiles, and limiting resource.

## Delivery And Notifications

Near-term:

- prove webhook and alert-contact workers under production transition volume;
- keep standalone `jetmon-deliverer` migration conservative;
- monitor pending, failed, and abandoned delivery rows;
- avoid duplicate human notifications between WPCOM legacy path and alert
  contacts.

Deferred:

- webhook grace-period secret rotation;
- application-level credential encryption with a master key or KMS-style
  envelope;
- alert grouping/digest support;
- SMS/OpsGenie/custom transports if there is real demand;
- migrating WPCOM notifications behind the managed delivery path after alert
  contacts prove out.

## API And Gateway

Near-term:

- treat `/api/v1/openapi.json` as the authoritative machine-readable route
  contract;
- keep tenant-context behavior behind the gateway consumer;
- add consumer-specific compatibility checks only when a real consumer standard
  emerges;
- avoid bulk write endpoints until a real batch consumer needs them.

Before any direct public API conversation:

- define customer auth and tenant model outside Jetmon;
- define redaction rules for event metadata;
- add public error vocabulary;
- add customer-facing rate limits and abuse controls;
- backfill owner/tenant data for webhooks and alert contacts;
- revisit webhook secret exposure and rotation;
- define compatibility and deprecation policy.

## Operator Experience

Useful future improvements:

- clearer dashboard summaries for "why is this host not ready?";
- one-command evidence packet export for support and rollout incidents;
- alerting on projection drift, stale process health, and delivery backlog;
- dashboard links from event to check-history and audit rows;
- explicit maintenance-window visibility in support views.

## Product Features

Post-rollout product candidates:

- customer-visible incident history through the gateway;
- per-site sensitivity profiles;
- managed alert contacts exposed through a product UI;
- webhooks for customer systems;
- SLA summaries and exports;
- richer maintenance-window tooling;
- uptime status page integration if a product owner commits to it.

## Not Now

These stay out of current scope unless explicitly reprioritized:

- GraphQL;
- public status-page hosting inside Jetmon;
- direct customer API without the gateway;
- new transport protocols for Veriflier;
- multi-region always-on quorum as a v2 launch blocker;
- large docs expansions without a canonical owner.

# Production Rollout Lab

The production rollout lab is a pre-production rehearsal for the containerized
Jetmon v2 rollout shape. It should validate the behavior operators will depend
on before any production Monitor buckets are activated.

This lab is coordinated by uptime-bench. Jetmon owns the expected topology,
configuration behavior, API gates, and service assertions. Uptime-bench owns
host orchestration, report capture, fixtures, and pass/fail reporting.

## Goals

- Prove the production deployment shape without touching production hardware.
- Exercise the same read/write database split and `db-servers.php` server-map
  sync model used by production.
- Verify Docker bridge access to host-local Monitor StatsD through
  `host.docker.internal`.
- Validate separate Veriflier Docker Compose deployments with bundled StatsD
  and Graphite.
- Simulate WPCOM success and failure behavior without contacting real WPCOM.
- Prove API-driven rollout commands work from a remote admin machine without
  shell access to container hosts.
- Capture enough evidence to decide whether production rollout can proceed.

## Available Hosts

Chris made these test hosts available for this lab:

- `jetmon-service-host-1` through `jetmon-service-host-6`
- `jetmon-vm-host-1` through `jetmon-vm-host-3`

Use these as a lab pool, not as fixed assignments. A good first topology is:

- DB write primary on one VM host.
- Two DB read replicas on separate VM hosts.
- Two or more Monitor/Deliverer Docker Compose hosts on service hosts.
- Three or more Veriflier Docker Compose hosts on service or VM hosts.
- One uptime-bench controller/admin host that runs `jetmon2 api ...` commands
  against the Monitor API.

## Required Topology

### Database Tier

- One writable MariaDB primary.
- Two read replicas.
- Jetmon schema migrations applied once against the primary.
- Concurrent migration attempts are either prevented by deployment order or
  serialized by Jetmon's database migration lock.
- Internal-only fixture sites seeded into `jetpack_monitor_sites`.
- V2 side tables seeded or adopted through the rollout API, not by ad hoc SQL,
  unless the test case is explicitly about migration repair.

### Server Map Source

- An SVN repository containing a production-shaped `db-servers.php`.
- A config-sync process that checks out or updates the file into the Monitor
  runtime path.
- `DB_SERVER_MAP_PATH`, `DB_SERVER_MAP_DATASET`,
  `DB_SERVER_MAP_DATACENTER`, and `DB_SERVER_MAP_ADDRESS` configured as they
  would be in production.
- No secrets in reports. Redact credentials when recording server-map evidence.

### Monitor And Deliverer Hosts

- Jetmon Monitor and optional standalone Deliverer run through Docker Compose.
- Containers use bridge networking, not host networking.
- Compose includes `--add-host=host.docker.internal:host-gateway`.
- Monitor/Deliverer `STATSD_ADDR=host.docker.internal:8125`.
- Production-style Monitor containers should run with
  `JETMON_AUTO_MIGRATE=false` after schema has been applied explicitly.
- Host-local StatsD and Graphite are installed and running on the Monitor hosts.
- Monitor-host StatsD should bind only to localhost or a private lab interface,
  not a public interface. The lab may expose Graphite on a private/admin bind
  address for evidence capture.
- API is enabled only on the intended Monitor control surface.

### Veriflier Hosts

- Verifliers run on separate hosts through the production Veriflier Docker
  Compose stack.
- Veriflier Compose includes its own StatsD and Graphite services.
- Graphite retention should match `10s:6h, 1m:7d, 10m:5y`.
- Monitor uses only v2 Veriflier endpoints.

### Target Sites

- Use internal-only HTTP/DNS fixtures for most tests so the lab measures Jetmon
  behavior rather than internet variability.
- Preserve Veriflier target-safety behavior during internal tests. If direct
  private fixture URLs are rejected, prefer a lab-only public-looking route to
  an internal fixture over disabling Jetmon's SSRF guard.
- Seed enough sites to exercise bucket distribution, read/write split, rollout
  commands, WPCOM notification paths, and Veriflier quorum behavior.
- Include canary sites with known up, down, timeout, redirect, TLS, body, and
  recovery behavior.

### WPCOM Simulator

The lab should include a local WPCOM simulator. It must be impossible to confuse
the simulator with real WPCOM endpoints.

Exercise at least:

- Legacy WPCOM notification success.
- Legacy WPCOM auth/cert-style failure.
- Legacy WPCOM 4xx permanent failure.
- Legacy WPCOM 5xx transient failure.
- Slow WPCOM response.
- Malformed WPCOM response.
- Failure followed by recovery, proving circuit-breaker behavior.
- Modern endpoint behavior if explicitly enabled, or a gate proving it remains
  disabled.

The report must assert that no real WPCOM host was contacted.

## Required Test Coverage

### Readiness And Standby

- Monitors start in standby/API-controlled mode.
- Standby does not claim buckets, run scheduled checks, write runtime/check
  history/event rows, send WPCOM notifications, or start delivery workers.
- Preflight validates DB access, schema version, server-map state, StatsD,
  Veriflier v2 contract/quorum identity, WPCOM config, and rollout blockers.
- API-guided rollout works from a separate admin/controller host.

### Server Map And Replica Behavior

- Initial server-map parse succeeds and selects the expected write primary and
  read replicas.
- Hot reload succeeds after `db-servers.php` changes in SVN.
- Failed reload leaves the last known-good DB mapping active.
- Malformed PHP, missing dataset, missing datacenter, and unreadable config are
  reported clearly without crashing the Monitor.
- Credential rotation is tested. If hot credential rotation is expected, prove
  it. If restart is required, document the stop/start behavior and downtime
  expectations.
- Read replica outage, slow replica, and replica lag are tested.
- Writes continue to go only to the primary.
- Read failover does not corrupt scheduler, rollout, API, dashboard, or
  telemetry behavior.

### StatsD And Graphite

- Monitor and Deliverer containers can emit StatsD to the host-local service
  through `host.docker.internal:8125`.
- Verifliers emit to their bundled StatsD/Graphite services.
- Metric host paths match the expected production-compatible naming.
- Runtime resource gauges are present for Monitor, Deliverer, and Veriflier.
- DB pool metrics are present for Monitor and Deliverer.
- Graphite retention and queryability are validated where the lab owns the
  Graphite service.

### Rollout Flow

- API seed/adopt dry-run and execute are idempotent.
- Bucket activation is explicit and range-scoped.
- Bucket release/rollback restores a safe non-owned state.
- V2 takes over only the intended bucket range.
- Post-activation checks prove fresh checks, no projection drift, green
  Veriflier quorum, expected process health, and expected metrics.
- HEAD/legacy smoke runs before activation and does not mutate site state.
- Non-authoritative HEAD/GET comparison records deltas without changing alerting
  policy.
- Staged policy transitions and rollback-last/rollback-all are rehearsed.

### Delivery And Notification Safety

- WPCOM notifications are sent only to the simulator.
- Delivery ownership is single-owner where intended.
- Duplicate WPCOM, webhook, or alert-contact sends are not observed.
- WPCOM circuit breaker opens, queues, drops, and recovers as expected under
  simulator-driven failures.
- Maintenance suppression and alert cooldown behavior are preserved during
  rollout-state transitions.

### Failure And Recovery

- Monitor container restart.
- Monitor host reboot or simulated outage.
- Veriflier container restart.
- Veriflier host outage.
- DB primary restart.
- Read replica restart.
- SVN/config-sync outage.
- StatsD outage on a Monitor host.
- Veriflier StatsD/Graphite outage.
- WPCOM simulator outage.
- DNS fixture outage or controlled resolver failure.

Each failure should have a clear expected outcome: continue, degrade with
warnings, block activation, or require rollback.

### Rollback

- Release activated buckets through the API.
- Confirm v2 stops scheduled checks for released ranges.
- Confirm v1-compatible legacy table fields remain safe for rollback.
- Confirm no duplicate down/recovery notifications are emitted during rollback.
- Confirm re-running seed/adopt and activation after rollback is idempotent.

## Report Requirements

Each uptime-bench report should include:

- Jetmon commit, uptime-bench commit, host mapping, and high-level topology.
- Redacted server-map snapshot and reload history.
- DB primary/replica health and observed routing behavior.
- Monitor/Deliverer/Veriflier process health.
- StatsD and Graphite evidence.
- WPCOM simulator request log summary.
- API rollout command transcript.
- Pass/fail table for each required coverage area.
- Explicit statement that no real WPCOM endpoint was contacted.
- Explicit statement that target checks stayed internal-only unless a test
  intentionally allowed external traffic.

## Stop Conditions

Stop and report instead of continuing when:

- Any real WPCOM endpoint is contacted.
- A rollout step mutates site state while in read-only standby or smoke mode.
- Writes are observed against a read replica.
- Bucket activation affects a range outside the requested scope.
- Duplicate customer-facing notifications would have been sent.
- Server-map reload failure drops the last known-good DB mapping.
- The lab cannot prove where notifications or target checks were sent.

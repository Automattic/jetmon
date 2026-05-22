# Production Rollout Lab

The production rollout lab is the final pre-production rehearsal for the
containerized Jetmon v2 rollout shape. It should prove the behavior operators
will depend on before any production Monitor buckets are activated.

Uptime-bench coordinates host orchestration, fixtures, report capture, and
pass/fail reporting. Jetmon owns the expected topology, config behavior, API
gates, and service assertions.

## Goals

- Rehearse the production deployment shape without touching production hosts.
- Exercise read/write DB split and production-shaped `db-servers.php` sync.
- Verify Monitor/Deliverer containers reach host-local StatsD through
  `host.docker.internal:8125`.
- Verify Veriflier Compose stacks include working StatsD and Graphite.
- Simulate WPCOM behavior without contacting real WPCOM.
- Prove API-driven rollout from a remote admin host with no shell access to the
  container hosts.
- Produce enough evidence for a production stop/go decision.

## Lab Pool

Available hosts:

- `jetmon-service-host-1` through `jetmon-service-host-6`
- `jetmon-vm-host-1` through `jetmon-vm-host-3`

Treat these as a pool, not fixed assignments. A representative topology is:

- one writable MariaDB primary on a VM host
- two read replicas on separate VM hosts
- two or more Monitor/Deliverer Docker Compose hosts on service hosts
- three or more Veriflier Docker Compose hosts on service or VM hosts
- one uptime-bench controller/admin host running `jetmon2 api ...` against the
  Monitor API

For local Compose rehearsal, start with:

```bash
make rollout-docker-lab-doctor
```

If this fails, fix Docker DNS/networking first. Otherwise the lab may fail
during image build before rollout behavior is tested.

## Required Shape

### Database And Server Map

- One primary, two read replicas.
- Schema applied once against the primary.
- Jetmon runs in production/validate mode after schema is applied.
- Fixture sites are seeded into `jetpack_monitor_sites`.
- V2 side tables are seeded or adopted through rollout APIs unless the test is
  explicitly about repair.
- A lab SVN repo publishes a production-shaped `db-servers.php`.
- Config sync updates the Monitor runtime path from SVN.
- `DB_SERVER_MAP_PATH`, `DB_SERVER_MAP_DATASET`,
  `DB_SERVER_MAP_DATACENTER`, and `DB_SERVER_MAP_ADDRESS` match the intended
  production shape.
- Reports redact DB credentials and any secrets from server-map evidence.

### Monitor And Deliverer

- Run through Docker Compose with bridge networking, never host networking.
- Compose includes `--add-host=host.docker.internal:host-gateway`.
- Rendered config contains `"STATSD_ADDR": "host.docker.internal:8125"`.
- Host-local StatsD and Graphite are running on Monitor hosts.
- Monitor-host StatsD binds only to localhost or a private lab interface.
- API is enabled only on the intended Monitor control surface.
- Pre-activation evidence includes `jetmon2 schema validate` and
  `jetmon2 doctor --require-statsd`.

### Verifliers

- Run on separate hosts through the production Veriflier Compose stack.
- Compose includes bundled StatsD and Graphite.
- Graphite retention is `10s:6h, 1m:7d, 10m:5y`.
- Monitors use only v2 Veriflier endpoints.

### Targets

- Prefer internal-only HTTP/DNS fixtures so the lab measures Jetmon, not the
  internet.
- Keep default target safety (`public_only`) for production rollout rehearsals
  and any real-site data.
- If internal fixture URLs are rejected by safety checks, prefer a lab-only
  public-looking route over disabling SSRF guardrails.
- For isolated high-cardinality synthetic capacity tests only,
  `CHECK_TARGET_SAFETY_MODE=allow_private_for_tests` is acceptable when
  `WPCOM_NOTIFY_ENABLE=false`, delivery is stubbed, site rows are disposable,
  and no customer-visible alert path exists.
- Include known up, down, timeout, redirect, TLS, body-content, and recovery
  canaries.

### WPCOM Simulator

The lab must use a local WPCOM simulator that cannot be confused with real
WPCOM. Exercise:

- success
- auth/cert-style failure
- permanent 4xx
- transient 5xx
- slow response
- malformed response
- failure followed by recovery
- modern endpoint disabled, unless explicitly enabled for that test

Every report must state that no real WPCOM host was contacted.

## Required Coverage

Readiness and standby:

- Monitors start in standby/API-controlled mode.
- Standby does not claim buckets, run scheduled checks, write runtime/check
  history/event rows, send WPCOM notifications, or start delivery workers.
- Preflight validates DB, schema, server-map state, StatsD, Veriflier v2
  contract/quorum identity, WPCOM config, and rollout blockers.
- API-guided rollout works from a separate admin/controller host.

Server map and replica behavior:

- Initial parse selects the expected primary and replicas.
- Hot reload succeeds after SVN changes.
- Failed reload leaves the last known-good mapping active.
- Malformed PHP, missing dataset/datacenter, and unreadable config report clear
  failures without crashing the Monitor.
- Credential rotation behavior is proven or documented as restart-required.
- Primary, replica outage, slow replica, and replica lag behavior are tested.
- Writes go only to the primary.
- Read failover does not corrupt scheduler, rollout, API, dashboard, or
  telemetry behavior.

Metrics:

- Monitor and Deliverer emit to host-local StatsD.
- Verifliers emit to their bundled StatsD/Graphite.
- Metric host paths match the production-compatible naming plan.
- Runtime resource gauges exist for Monitor, Deliverer, and Veriflier.
- DB pool metrics exist for Monitor and Deliverer.
- Graphite retention/queryability is validated where the lab owns Graphite.

Rollout flow:

- Seed/adopt dry-run and execute are idempotent.
- Bucket activation is explicit and range-scoped.
- Release/rollback restores a safe non-owned state.
- V2 takes over only the requested range.
- Post-activation checks prove fresh checks, no projection drift, green
  Veriflier quorum, expected process health, and expected metrics.
- HEAD/legacy smoke before activation does not mutate site state.
- HEAD/GET comparison records non-authoritative deltas without changing
  alerting policy.
- Staged policy transitions, rollback-last, and rollback-all are rehearsed.

Delivery and notification safety:

- WPCOM notifications go only to the simulator.
- Delivery ownership is single-owner where intended.
- No duplicate WPCOM, webhook, or alert-contact sends are observed.
- WPCOM circuit breaker opens, queues, drops, and recovers under simulator
  failures.
- Maintenance suppression and alert cooldown survive rollout transitions.

Failure and recovery:

- Monitor container restart.
- Monitor host reboot or simulated outage.
- Veriflier container restart.
- Veriflier host outage.
- DB primary restart.
- Read replica restart.
- SVN/config-sync outage.
- Monitor-host StatsD outage.
- Veriflier StatsD/Graphite outage.
- WPCOM simulator outage.
- DNS fixture outage or controlled resolver failure.

Each failure must have an expected result: continue, degrade with warnings,
block activation, or require rollback.

Rollback:

- Release activated buckets through the API.
- Confirm v2 stops scheduled checks for released ranges.
- Confirm v1-compatible legacy fields remain safe for rollback.
- Confirm rollback does not emit duplicate down/recovery notifications.
- Confirm seed/adopt and activation can be rerun after rollback.

## Report Requirements

Each uptime-bench report should include:

- Jetmon commit, uptime-bench commit, host map, and topology summary.
- Redacted server-map snapshot and reload history.
- DB primary/replica health and observed read/write routing.
- Monitor, Deliverer, and Veriflier process health.
- StatsD and Graphite evidence.
- WPCOM simulator request summary.
- API rollout command transcript.
- Pass/fail table for every required coverage area.
- Statement that no real WPCOM endpoint was contacted.
- Statement that target checks stayed internal-only unless a test explicitly
  allowed external traffic.

## Stop Conditions

Stop and report immediately if:

- any real WPCOM endpoint is contacted
- standby or smoke mode mutates site state
- writes hit a read replica
- activation affects buckets outside the requested range
- duplicate customer-facing notifications would have been sent
- server-map reload failure drops the last known-good mapping
- the lab cannot prove where notifications or target checks were sent

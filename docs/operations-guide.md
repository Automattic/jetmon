# Operations Guide

This guide is for people running Jetmon 2 after it has been built and
configured. It focuses on the operator loop: validate config, start safely,
watch health, investigate issues, and know which deeper doc owns each detail.

Use these companion docs for full reference material:

- [config/config.readme](../config/config.readme) - complete config key list
- [docker-images.md](docker-images.md) - image usage and rendered config inputs
- [production-teamcity-rollout.md](production-teamcity-rollout.md) - production
  Monitor deployment shape
- [production-schema-package.md](production-schema-package.md) - DDL package
  and schema validation checklist
- [production-veriflier-compose.md](production-veriflier-compose.md) -
  Veriflier VPS Compose stack
- [v1-to-v2-migration.md](v1-to-v2-migration.md) - v1 to v2 production
  migration runbook
- [rollout-quick-reference.md](rollout-quick-reference.md) - rollout command
  checklist
- [internal-api-reference.md](internal-api-reference.md) - internal API details

## Operator Checklist

Before activating or changing a production Monitor:

1. Confirm the database schema was applied through the approved production
   database-change process.
2. Start the Monitor with `CONFIG_PROFILE=production` or
   `SCHEMA_MANAGEMENT_MODE=validate`.
3. Run `./jetmon2 schema validate`.
4. Run `./jetmon2 validate-config`.
5. Run `./jetmon2 doctor --require-statsd`.
6. Confirm Veriflier health with `./jetmon2 verifliers discovery-report`.
7. Confirm the host dashboard is reachable through a trusted operator path.
8. Confirm the fleet dashboard has no red blockers before moving to the next
   rollout or maintenance step.

Routine health checks:

```bash
./jetmon2 status
./jetmon2 telemetry report --since=24h
./jetmon2 verifliers discovery-report
./jetmon2 rollout state-report --since=15m
```

API-backed checks:

```bash
./jetmon2 api request --pretty GET /api/v1/monitor/stats
./jetmon2 api request --pretty GET /api/v1/monitor/db-config
```

## Configuration Posture

Jetmon reads JSON config. In Docker-based deploys, environment values are render
inputs for the config file; the binary reads the rendered JSON. This avoids
hidden direct environment overrides while keeping Compose and TeamCity settings
easy to manage.

Important production rules:

- Production Monitor containers should validate schema on startup, not apply
  DDL automatically.
- Do not combine `DB_SERVER_MAP_PATH` with explicit DB credentials.
- Keep `CHECK_TARGET_SAFETY_MODE=public_only` for production and real site data.
- Use `WPCOM_NOTIFY_MODE=legacy` for the first production rollout.
- Keep `LEGACY_STATUS_PROJECTION_ENABLE=true` until legacy consumers have moved
  away from the v1 projection.
- Keep dashboards bound to loopback unless access is restricted to a trusted
  management network.
- Keep `DEBUG_PORT=0` unless investigating a specific issue.
- Use `STATSD_HOST_PATH` for the v1-compatible Graphite path. Do not rely on
  container hostnames or generated suffixes for production metrics identity.

The most important config groups are:

| Area | Key examples | Notes |
| --- | --- | --- |
| Schema | `CONFIG_PROFILE`, `SCHEMA_MANAGEMENT_MODE` | Production should validate externally applied schema. |
| Database | `DB_SERVER_MAP_PATH`, `DB_SERVER_MAP_DATACENTER`, explicit `DB_*` keys | Use the server-map path in production when available. |
| Rollout control | `ROLLOUT_MODE`, `CONFIG_PROFILE` | Production starts `api-controlled` so bucket activation is explicit through the rollout API. |
| Check behavior | `DEFAULT_CHECK_METHOD`, `DEFAULT_DETECTION_PROFILE`, `NUM_OF_CHECKS`, `NET_COMMS_TIMEOUT` | Rollout starts with `HEAD` + `legacy`, then stages toward GET profiles. |
| Safety | `CHECK_TARGET_SAFETY_MODE` | `allow_private_for_tests` is only for isolated synthetic labs with WPCOM disabled. |
| Verifliers | `VERIFLIERS`, `VERIFLIER_DISCOVERY_MODE`, `PEER_OFFLINE_LIMIT` | Production starts in `shadow` discovery until registry drift reports are clean; quorum counts unique v2 `vantage.id` values. |
| Metrics | `STATSD_ADDR`, `STATSD_HOST_PATH` | Monitor production uses the host-local StatsD proxy through Docker bridge networking. |
| API/dashboard | `API_PORT`, `DASHBOARD_PORT`, `DASHBOARD_BIND_ADDR` | Bind dashboards locally unless protected by operator-only network access. |
| Delivery | `DELIVERY_OWNER_HOST` | Use as a rollout guard when keeping outbound delivery single-owner. |

Removed v1 scheduler tuning knobs are accepted only where needed for copied
config compatibility and should be cleaned up. The v2 scheduler is streaming
only; there is no supported legacy scheduler runtime path.

## Database And Schema

Production schema changes must be applied before production containers start in
validate mode. `schema validate` is read-only and checks the required tables,
columns, and indexes; it does not require the local/lab migration ledger. The
expected startup posture is:

```bash
./jetmon2 schema validate
./jetmon2 validate-config
```

Run `./jetmon2 migrate` only as an explicit schema-change action in environments
where the operator is allowed to apply DDL. Do not rely on automatic production
migrations.

When `DB_SERVER_MAP_PATH` is set, Jetmon reads the WPCOM-style `db-servers.php`
map, builds separate read/write pools from the `misc` dataset, and hot-reloads
validated changes on the configured refresh cadence. Check the current state
with:

```bash
./jetmon2 api request --pretty GET /api/v1/monitor/db-config
```

## Startup And Deployment

Production Monitor deployment is expected to use TeamCity/docker-deploy and the
rendered JSON config flow. The Monitor stack should use the production host's
local StatsD service through Docker bridge networking:

```text
--add-host=host.docker.internal:host-gateway
"STATSD_ADDR": "host.docker.internal:8125"
```

Do not use host networking for the Monitor container. Do not run StatsD or
Graphite in the production Monitor stack.

Veriflier production VPS hosts are different: their Compose stack owns
Veriflier plus local StatsD/Graphite. See
[production-veriflier-compose.md](production-veriflier-compose.md).

Systemd examples remain useful for VM labs and emergency fallback, but they are
not the primary production rollout path. If systemd is used, verify units after
the binary exists at the configured `ExecStart` path:

```bash
systemd-analyze verify /etc/systemd/system/jetmon2.service
```

## Rollout Operations

Use [v1-to-v2-migration.md](v1-to-v2-migration.md) for the production rollout
sequence. The current plan is:

1. Apply schema changes.
2. Deploy and validate v2 Verifliers.
3. Deploy v2 Monitors in standby/API-controlled mode.
4. Run read-only smoke checks.
5. Activate ranges through the API only after the matching v1 ownership stops.
6. Observe each range before expanding.
7. Keep the initial policy at `HEAD` + `legacy`.
8. Stage policy migration to `GET` + `simple_http`, then `GET` + `full`.

Use [rollout-quick-reference.md](rollout-quick-reference.md) for exact
commands. Prefer API-guided rollout operations when container hosts are not
directly accessible.

Useful rollout gates:

```bash
./jetmon2 rollout state-report --since=15m
./jetmon2 rollout projection-drift --limit=100
./jetmon2 telemetry report --since=15m
```

Treat red dashboard status, projection drift, missed checks, stale process
heartbeats, Veriflier identity drift, WPCOM parity gaps, and delivery backlog as
hold points.

## Runtime Health

The host dashboard is available on `DASHBOARD_BIND_ADDR:DASHBOARD_PORT` when
enabled. It is unauthenticated and exposes internal hostnames, dependency
health, rollout state, and delivery posture. Keep it on loopback and use an SSH
tunnel for remote access:

```bash
ssh -L 8080:127.0.0.1:8080 <jetmon-host>
```

Important dashboard/API views:

```text
GET /api/state   # raw host state snapshot
GET /api/health  # dependency health list
GET /api/host    # host state, dependency health, and summary
GET /api/fleet   # fleet rollup, process health, buckets, delivery, drift
```

Read dashboard status top-down:

- **Red**: stop rollout or maintenance changes. Investigate before continuing.
- **Amber**: expected during some rollout states, but must be explained.
- **Green**: no visible blocker from the dashboard's current evidence.

The fleet dashboard reads shared MySQL state. It does not scrape dashboards from
other hosts. Long-running `jetmon2` and `jetmon-deliverer` processes publish
compact snapshots to `jetpack_monitor_process_health`; stale rows are last-known
state, not proof that a process is still alive.

Direct SQL checks are still useful when debugging dashboard data:

```sql
SELECT host_id, bucket_min, bucket_max, last_heartbeat, status
FROM jetpack_monitor_hosts
ORDER BY bucket_min;

SELECT process_id, host_id, process_type, state, health_status, updated_at
FROM jetpack_monitor_process_health
ORDER BY process_type, host_id;
```

## Veriflier Health

V2 Monitors should use v2 Verifliers only. The production contract is:

```text
GET  /v2/status
POST /v2/check
```

Quorum counts unique `vantage.id` values, not raw agent replies. Multiple
replicas behind one regional endpoint should share a `vantage.id` and use
distinct `agent.id` values.

Veriflier check replies include bounded diagnostics from the shared checker
path. These diagnostics are intended for operator/audit context when a remote
vantage confirms, disagrees, or returns a site-scoped non-vote; response bodies
are not stored.

Check Veriflier posture:

```bash
./jetmon2 validate-config
./jetmon2 verifliers discovery-report --output=text
curl http://<veriflier-host>:7803/v2/status
```

Hold rollout if:

- a Veriflier is unreachable from the Monitor runtime environment,
- a v2 Veriflier lacks a stable `vantage.id`,
- two quorum-counted endpoints report the same `vantage.id`,
- active discovery has no usable trusted registry rows,
- registry rows and static config disagree without an intentional rollout
  reason, or
- a Veriflier returns HTTP 503 for `/v2/check` under normal load.

HTTP 503 from a Veriflier is capacity or routing pressure for that endpoint. It
is never counted as a customer-site down vote.

## Delivery Workers

When `API_PORT` is non-zero, embedded webhook and alert-contact delivery workers
can run inside `jetmon2`. Use `DELIVERY_OWNER_HOST` during rollout when only one
host should dispatch outbound deliveries.

`jetmon-deliverer` is the standalone process boundary for outbound delivery. It
runs webhook and alert-contact delivery without the Monitor loop, API server,
dashboard, or bucket ownership.

Conservative validation:

```bash
JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
  /opt/jetmon2/bin/jetmon-deliverer validate-config \
    --require-owner-match \
    --require-api-disabled
```

Queue check:

```bash
JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
  /opt/jetmon2/bin/jetmon-deliverer delivery-check \
    --since=15m \
    --max-due=0 \
    --max-abandoned=0 \
    --max-failed=0 \
    --output=json
```

See [jetmon-deliverer-rollout.md](jetmon-deliverer-rollout.md) for rollout and
rollback details.

## Internal API Affinity

Every Monitor with `API_PORT` enabled serves the full internal API against the
shared database. Most state is database-coordinated, but two safeguards are
local to each process:

- idempotency replay cache
- per-key rate limiting

Operating rule: each API consumer should talk to one stable Monitor host. If a
gateway fronts the API, route a given consumer or API key to a stable host. Do
not fan out mutating requests across hosts unless idempotency is moved to a
durable shared store.

## Probe Safety

Use `site-safety` commands to find unsafe legacy monitor URLs without creating
downtime events or notifications:

```bash
./jetmon2 site-safety unsafe-urls
./jetmon2 site-safety unsafe-urls --execute
./jetmon2 site-safety report --output=json --max-open=0
```

The default `unsafe-urls` mode is read-only. `--execute` records
`jetpack_monitor_site_safety_flags` rows and disables unsafe active legacy rows;
it does not delete sites, open downtime events, or send WPCOM/webhook/alert
notifications.

## Retention

Check history and audit log retention is disabled by default. Set explicit
windows to prune append-only data:

```text
RETENTION_CHECK_HISTORY_DAYS   365
RETENTION_AUDIT_LOG_DAYS       365
RETENTION_BACKGROUND_ENABLED   true
RETENTION_RUN_HOUR_UTC         4
```

Manual cleanup:

```bash
jetmon2 cleanup --dry-run
jetmon2 cleanup
jetmon2 cleanup --check-history-days=30 --audit-log-days=90
```

Cleanup deletes in paced primary-key chunks and uses MySQL advisory locks so
only one host prunes a table at a time.

## Metrics And Logs

StatsD metrics keep the v1 prefix:

```text
com.jetpack.jetmon.<statsd_host_path>
```

For production Monitors, set:

```text
HOSTNAME=jetmon-prod-1.dfw1.example.com
STATSD_HOST_PATH=dfw1.jetmon-prod-1
STATSD_ADDR=host.docker.internal:8125
```

Keep `HOSTNAME` and `STATSD_HOST_PATH` stable and low-cardinality. Do not use
container IDs, release SHAs, ports, process IDs, or random suffixes.

Important metric groups:

- scheduler queue, lag, dispatch, and backpressure pressure,
- check throughput and result timing,
- check method/profile cohorts for staged rollout,
- WPCOM attempts, retries, and final failures,
- Veriflier response times and vote counters,
- event and false-alarm transitions,
- process RSS, Go runtime memory, file descriptors, goroutines, and threads,
- SQL pool pressure for Monitors and deliverers.

StatsD is UDP. Monitor dashboard `statsd` health proves local client
configuration, not downstream Graphite ingestion. Veriflier VPS Compose owns
StatsD/Graphite and includes a metrics smoke path.

Runtime logs go to stdout/stderr. V2 does not write v1 `jetmon.log` or
`status-change.log` files by default.

## Debugging

Enable debug logging only for an investigation window:

```json
{ "DEBUG": true }
```

Use pprof locally when `DEBUG_PORT > 0`:

```bash
curl http://localhost:6060/debug/pprof/
curl http://localhost:6060/debug/pprof/heap > heap.prof
go tool pprof heap.prof
```

The pprof listener binds to `127.0.0.1` and should be disabled in steady-state
production with `DEBUG_PORT=0`.

For memory investigations, compare dashboard RSS with host tools, and use Go
runtime memory plus pprof to distinguish heap growth from runtime/socket/buffer
pressure. `WORKER_MAX_MEM_MB` is deprecated; host or container limits are the
real memory ceiling.

For high-fanout outbound checks, watch ephemeral ports, `TIME_WAIT`, and file
descriptor headroom. Large fleets may need host-level tuning for:

```text
net.ipv4.ip_local_port_range
net.ipv4.tcp_tw_reuse
fs.file-max
LimitNOFILE
```

See the scalability test plan for repeatable capacity checks:
[jetmon-v2-scalability-test-plan.md](jetmon-v2-scalability-test-plan.md).

## Local Docker Cleanup

For a clean local Docker reset:

```bash
cd docker
docker compose down -v
rm -f ../config/config.json
rm -rf ../stats/*
```

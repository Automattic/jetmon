# Operations Guide

Use this for local development, steady-state runtime care, support
investigations, and lab rehearsals. Use
[v1-to-v2-migration.md](v1-to-v2-migration.md) for production rollout sequence.

## Local Loop

```bash
cd docker
cp .env-sample .env
docker compose up --build -d
```

```bash
make all
make test
make test-race
make lint
```

API smoke:

```bash
make build
make api-cli-token-create

export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token

./bin/jetmon2 api health --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api commands --output table
make api-cli-smoke
```

Manual non-Compose run:

```bash
cp config/config-sample.json config/config.json
./bin/jetmon2 validate-config
./bin/jetmon2 migrate
./bin/jetmon2
```

## Binaries And Commands

| Binary | Purpose |
| --- | --- |
| `jetmon2` | Monitor, API, dashboard, embedded delivery workers, CLI. |
| `veriflier2` | Remote confirmation worker. |
| `jetmon-deliverer` | Standalone webhook and alert-contact delivery worker. |

Common commands:

```bash
./bin/jetmon2 version
./bin/jetmon2 validate-config
./bin/jetmon2 migrate
./bin/jetmon2 status
./bin/jetmon2 drain
./bin/jetmon2 reload
./bin/jetmon2 audit --blog-id 12345 --since 2h
```

## Runtime Config

The full config reference is `config/config.readme`; the sample is
`config/config-sample.json`.

| Key | Purpose |
| --- | --- |
| `NUM_WORKERS` | Checker pool size. |
| `DATASET_SIZE` | Active-target reload page size. |
| `NET_COMMS_TIMEOUT` | Default per-check timeout. |
| `PEER_OFFLINE_LIMIT` | Veriflier agreements needed to confirm downtime. |
| `BUCKET_TOTAL`, `BUCKET_TARGET` | Dynamic ownership range and per-host target. |
| `BUCKET_HEARTBEAT_GRACE_SEC` | Stale-host grace before bucket absorption. |
| `PINNED_BUCKET_MIN/MAX` | Migration-only static range. |
| `LEGACY_STATUS_PROJECTION_ENABLE` | Keep v1 `site_status` projection updated. |
| `API_PORT`, `DASHBOARD_PORT`, `DEBUG_PORT` | API, dashboard, and localhost pprof listeners. |
| `DELIVERY_OWNER_HOST` | Optional single-owner delivery rollout guard. |
| `EMAIL_TRANSPORT` | `wpcom`, `smtp`, or `stub`. |

Always run `validate-config` before starting a new production config. Production
containers should validate schema state at startup, not apply automatic DDL.

## Restart And Drain

| Change | Action |
| --- | --- |
| Mounted JSON config changed | `validate-config`, then `reload` or SIGHUP. |
| New image or binary | SIGINT drain, replace, start, watch readiness. |
| DB server map changed | Wait for hot reload, or SIGHUP for immediate pool rebuild. |
| Remove host from service | SIGINT drain; confirm `/api/v1/monitor/drain-status` is done. |

Useful commands:

```bash
./bin/jetmon2 drain
./bin/jetmon2 reload
curl -fsS "$JETMON_API_URL/api/v1/monitor/drain-status"
```

Deploy order for a full fleet change: Verifliers, standalone deliverer if used,
then Monitor hosts one at a time.

## Health Surfaces

| Surface | Use |
| --- | --- |
| `/api/v1/health` | API liveness and DB connectivity. |
| `/api/v1/ready` | Host readiness after process-health is fresh and green. |
| `/api/v1/monitor/stats` | Current stats snapshot and legacy file bodies. |
| `/api/v1/monitor/db-config` | Sanitized DB config reload status. |
| `/api/v1/verifliers/quorum-report` | Vantage health and quorum diagnostics. |
| Dashboard `/` and `/fleet` | Host and fleet operations. |
| `/debug/pprof/` | Localhost-only profiling when `DEBUG_PORT > 0`. |

Keep dashboard and pprof listeners on loopback unless protected by trusted
operator-network controls.

## Metrics, Logs, And Evidence

StatsD keeps the v1 prefix:

```text
com.jetpack.jetmon.<hostname>.
```

Runtime logs go to stdout/stderr. Site-state changes live in
`jetpack_monitor_events` and `jetpack_monitor_event_transitions`; operational
actions live in `jetpack_monitor_audit_log`.

## Delivery Workers

Embedded workers are eligible when `API_PORT > 0`; `jetmon-deliverer` runs the
same queues outside the Monitor process. Row claims are transactional, so
multiple workers do not double-claim. Use `DELIVERY_OWNER_HOST` only as a
rollout guard.

Retry ladder:

```text
immediate -> 1m -> 5m -> 30m -> 1h -> 6h -> abandoned
```

Watch pending, failed, abandoned, and per-worker health before expanding
delivery ownership.

## Support Workflow

For an alert or missed alert, collect:

1. Site record.
2. Recent events and transitions.
3. Check history around the transition.
4. Audit rows for Veriflier RPC, WPCOM, maintenance, suppression, and API
   access.
5. Veriflier quorum report when confirmation is relevant.

Interpretation shortcuts:

| Signal | Meaning |
| --- | --- |
| `Seems Down` then `false_alarm` | Local failure did not meet quorum. |
| `Down` then `verifier_cleared` | Confirmed outage recovered. |
| `probe_cleared` | Local probe recovered before confirmation. |
| Maintenance audit row | Checks continued; alerts were suppressed. |
| `blocked` | 403/WAF-style denial. Validate allowlisting. |
| `Unknown` | Monitor/probe infrastructure issue, not customer downtime. |

Frame findings as observations, not unsupported root cause claims.

## Labs And Rehearsals

Run labs only against local fixtures, uptime-bench fixtures, or approved
canaries. Do not change deployed services, support hosts, databases, provider
state, fleet config, or runtime config while capacity tests are active unless
Chris explicitly approves it.

| Target | Command | Proves |
| --- | --- | --- |
| Docker rollout | `make rollout-docker-lab` | API-guided rollout and rollback in local containers. |
| VM rollout | `make rollout-vm-lab-smoke` | Production-shaped systemd/KVM rollout rehearsal. |
| Scale resilience | `make scale-resilience-lab` | Dynamic ownership, host loss, DB disruption behavior. |
| Soak | `make v2-soak-lab` | Sustained operation without outbound side effects. |
| API fixture safety | `make api-cli-public-fixture-validate` | Probe-safety behavior against controlled fixtures. |

VM helpers include `rollout-vm-lab-doctor`, `rollout-vm-lab-prepare`,
`rollout-vm-lab-execute-smoke`, failure/resume smoke targets, and snapshot smoke
targets. Set `ROLLOUT_VM_LAB_HOST`, `ROLLOUT_VM_LAB_SSH`, and
`ROLLOUT_VM_LAB_SNAPSHOT` as needed.

Rehearsal reports should record commit SHA, image tags, config fingerprint,
schema version, quorum report, rollout session/job IDs, command transcript,
canary file, rollback result, and known gaps. Put long raw benchmark reports in
the sibling `uptime-bench` repo.

## Debugging

```bash
curl http://localhost:${DEBUG_PORT:-6060}/debug/pprof/goroutine?debug=2
curl -o heap.pb.gz http://localhost:${DEBUG_PORT:-6060}/debug/pprof/heap
go tool pprof ./bin/jetmon2 heap.pb.gz
```

Useful DB checks:

```sql
SELECT * FROM jetpack_monitor_hosts ORDER BY bucket_min;
SELECT * FROM jetpack_monitor_process_health ORDER BY updated_at DESC LIMIT 20;
SELECT * FROM jetpack_monitor_event_transitions ORDER BY id DESC LIMIT 20;
SELECT * FROM jetpack_monitor_audit_log ORDER BY id DESC LIMIT 20;
```

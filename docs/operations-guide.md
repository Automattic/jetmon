# Operations Guide

This guide collects production-facing details that used to live in the root
README: configuration, rollout, dashboard checks, delivery workers, metrics, and
debugging.

## Configuration

Jetmon configuration lives in `config/config.json`. Copy
`config/config-sample.json` to get started. Docker can generate this file from
`config-sample.json` and `docker/.env` when it is not present.

Use `SIGHUP` or `./jetmon2 reload` to reload configuration without restarting.

Key settings:

| Key | Default | Description |
|---|---:|---|
| `NUM_WORKERS` | 60 | Goroutine pool size |
| `NUM_TO_PROCESS` | 40 | Parallel checks per pool slot |
| `NUM_OF_CHECKS` | 3 | Local failures before Veriflier escalation |
| `TIME_BETWEEN_CHECKS_SEC` | 30 | Delay between local retry checks |
| `MIN_TIME_BETWEEN_ROUNDS_SEC` | 300 | Minimum seconds between check rounds |
| `NET_COMMS_TIMEOUT` | 10 | Default per-check HTTP timeout in seconds |
| `PEER_OFFLINE_LIMIT` | 3 | Veriflier agreements required to confirm downtime |
| `WORKER_MAX_MEM_MB` | 53 | RSS threshold that triggers worker-pool drain |
| `BUCKET_TOTAL` | 1000 | Total bucket range across all hosts |
| `BUCKET_TARGET` | 500 | Maximum buckets this host should own |
| `BUCKET_HEARTBEAT_GRACE_SEC` | 600 | Seconds before a silent host's buckets are reclaimed |
| `PINNED_BUCKET_MIN` / `PINNED_BUCKET_MAX` | unset | Static bucket range used by the [v1-to-v2 migration runbook](v1-to-v2-migration.md) |
| `ALERT_COOLDOWN_MINUTES` | 30 | Default cooldown between repeated alerts per site |
| `LEGACY_STATUS_PROJECTION_ENABLE` | true | Keep v1 status fields projected during the [v1-to-v2 migration](v1-to-v2-migration.md) |
| `LOG_FORMAT` | `text` | `text` or `json` |
| `DASHBOARD_PORT` | 8080 | Internal operator dashboard port, 0 disables it |
| `API_PORT` | 0 | Internal REST API port, 0 disables it |
| `DELIVERY_OWNER_HOST` | empty | Optional host allowed to run embedded delivery workers |
| `DEBUG_PORT` | 6060 | localhost-only pprof port, 0 disables it |
| `EMAIL_TRANSPORT` | `stub` | `stub`, `smtp`, or `wpcom` |

See [../config/config.readme](../config/config.readme) for the full option
reference.

## Production Host Setup

1. Install `jetmon2` to `/opt/jetmon2/`.
2. Install `systemd/jetmon2.service` to `/etc/systemd/system/` and run
   `systemctl daemon-reload`.
3. Install `systemd/jetmon2-logrotate` to `/etc/logrotate.d/jetmon2`.
4. Create `/opt/jetmon2/logs` and `/opt/jetmon2/stats`, owned by the `jetmon`
   service user.
5. Create `/opt/jetmon2/config/jetmon2.env` with database credentials and auth
   tokens. See `config/db-config-sample.conf`.
6. Copy or generate `config/config.json`.
7. Set `BUCKET_TARGET` to the desired maximum bucket count for the host.
8. Run `./jetmon2 migrate`.
9. Start the service with `systemctl enable --now jetmon2`.

Manual commands such as `migrate`, `validate-config`, and `rollout` need the
same `DB_*` environment that systemd reads from
`/opt/jetmon2/config/jetmon2.env`; systemd's `EnvironmentFile` is not loaded for
commands run directly from a shell.

## v1 To v2 Migration

Use [v1-to-v2-migration.md](v1-to-v2-migration.md) for the full production
migration process. It covers preparation, additive migrations, pinned bucket
mode, replacing v1 on the same server, moving a range to a fresh v2 server,
monitoring, revert paths, dynamic ownership cutover, and v1 teardown.

## v2 Rolling Updates

After all monitor hosts are on v2 dynamic bucket ownership, update one host at a
time. Surviving hosts absorb the draining host's buckets during the update
window.

```bash
systemctl stop jetmon2
./jetmon2 migrate
systemctl start jetmon2
./jetmon2 status
```

Repeat for the next host.

## Delivery Workers

In the embedded deployment, setting `API_PORT` to a non-zero value starts the
internal API and makes webhook and alert-contact delivery workers eligible to
run inside `jetmon2`.

Use `DELIVERY_OWNER_HOST` when only one API-enabled host should dispatch
outbound deliveries during rollout. If it is empty, delivery workers start on
any host with `API_PORT` enabled.

`bin/jetmon-deliverer` is the standalone process boundary for outbound delivery.
It starts the same webhook and alert-contact workers without starting the
monitor, API, dashboard, or bucket ownership loop. Delivery rows are claimed
transactionally, so multiple workers do not claim the same pending row.

For conservative single-owner rollout, validate the deliverer-specific config
before enabling the service:

```bash
JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
  /opt/jetmon2/bin/jetmon-deliverer validate-config \
    --require-owner-match \
    --require-api-disabled
```

Add `--require-email-delivery` when real alert-contact email delivery is
expected in that environment.

During rollout, inspect the shared webhook and alert-contact delivery queues
from the same environment the service uses:

```bash
JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
  /opt/jetmon2/bin/jetmon-deliverer delivery-check --since=15m
```

Use thresholds for automated gates:

```bash
JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
  /opt/jetmon2/bin/jetmon-deliverer delivery-check \
    --since=15m \
    --max-due=0 \
    --max-abandoned=0 \
    --max-failed=0 \
    --output=json
```

`delivery-check` also reports `failed_since`, `oldest_pending_age_sec`, and
`oldest_due_age_sec`. Use `--require-recent-webhook-delivery` or
`--require-recent-alert-delivery` when a rollout gate needs each delivery family
to prove a successful send independently.

See [jetmon-deliverer-rollout.md](jetmon-deliverer-rollout.md) for the rollout
and rollback path.

## Runtime Checks

Status and reload commands:

```bash
./jetmon2 status
./jetmon2 reload
./jetmon2 drain
```

The operator dashboard is available on `DASHBOARD_PORT` when enabled. It shows
worker count, active checks, queue depth, retry queue depth, throughput, round
time, owned buckets, rollout guard state, RSS, WPCOM circuit-breaker state, and
dependency health for MySQL, Verifliers, WPCOM, StatsD, and local log/stats
writes.

Bucket coverage can be inspected directly:

```sql
SELECT host_id, bucket_min, bucket_max, last_heartbeat, status
FROM jetmon_hosts
ORDER BY bucket_min;
```

A host whose heartbeat is older than `BUCKET_HEARTBEAT_GRACE_SEC` will have its
buckets reclaimed by peers on their next round.

## Metrics And Logs

StatsD metrics retain the v1 prefix:

```text
com.jetpack.jetmon.<hostname>
```

Important metric groups include:

- Worker pool capacity and active goroutines
- Sites processed per second
- Round completion time
- WPCOM API attempts, deliveries, retries, errors, and failures
- Veriflier response times and vote counters
- Detection flow timing from first failure to escalation, confirmation,
  recovery, or false alarm
- Detection outcome counters by local failure class
- Legacy projection drift
- Memory usage

StatsD is the primary metrics transport. Expose Graphite/StatsD data through the
existing metrics pipeline when external systems need it.

Use `LOG_FORMAT=json` for structured logs during investigations.

## Debugging

Enable debug logging:

```json
{ "DEBUG": true }
```

Attach pprof locally:

```bash
curl http://localhost:6060/debug/pprof/
curl http://localhost:6060/debug/pprof/heap > heap.prof
go tool pprof heap.prof
```

The debug listener binds to localhost only. Set `DEBUG_PORT` to 0 to disable it.

If RSS exceeds `WORKER_MAX_MEM_MB`, the goroutine pool shrinks by 10 percent via
graceful drain. Sustained memory pressure should be investigated with pprof
before increasing the limit.

## Veriflier Health

Verifliers that fail to respond are excluded from confirmation requests. If the
healthy set drops below `PEER_OFFLINE_LIMIT`, Jetmon cannot issue new downtime
confirmations.

Manual check:

```bash
curl http://<veriflier-host>:7803/status
```

## Docker Cleanup

```bash
cd docker
docker compose down -v
rm -f ../config/config.json
rm -rf ../logs/*.log ../stats/*
```

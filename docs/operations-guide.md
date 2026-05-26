# Production Operations Guide

Use this for Docker-based production runtime care, incident investigation, and
safe restarts. Use [development-guide.md](development-guide.md) for local setup
and labs, and [v1-to-v2-migration.md](v1-to-v2-migration.md) for rollout
sequence.

Examples use plain `docker` commands. In production, run the equivalent through
the docker-deploy/TeamCity role that owns the host, image tag, secrets, mounts,
health checks, and rollback behavior.

## Deployment Shape

Production Monitor hosts run containerized services:

| Container | Purpose |
| --- | --- |
| `jetmon` / Monitor | Runs `jetmon2`, API, dashboard, optional embedded delivery workers. |
| `config-sync` sidecar | Syncs private generated config such as `db-servers.php` into a shared runtime path. |
| `jetmon-deliverer` | Optional standalone webhook and alert-contact delivery worker. |
| `veriflier` | Remote confirmation worker, deployed separately from Monitor hosts. |

Production Monitor containers should use bridge networking, not host
networking. Reach host-local StatsD through Docker's host-gateway mapping:

```text
--add-host=host.docker.internal:host-gateway
STATSD_ADDR=host.docker.internal:8125
```

Do not set `STATSD_ADDR=127.0.0.1:8125` inside a bridge-networked container; it
points at the container itself.

Veriflier Compose stacks run their own StatsD/Graphite container. Keep
`STATSD_ADDR=statsd:8125` there, and set `VERIFLIER_STATSD_HOST_PATH` when a
Veriflier needs a metric identity that differs from the Monitor
`STATSD_HOST_PATH`.

## Images And Tags

Runtime images:

| Image | Dockerfile |
| --- | --- |
| `ghcr.io/automattic/jetmon` | `docker/Dockerfile_jetmon` |
| `ghcr.io/automattic/veriflier` | `docker/Dockerfile_veriflier` |

Tags:

- `latest`: current `v2` branch.
- `<YYYYMMDD>-<short-sha>`: immutable build for pushes to `v2`; prefer this for
  production pinning.
- `pr-<short-sha>`: PR image when the PR has the Docker Build label.

Project Dockerfiles embed version, commit, build date, and Go version metadata
in the binaries. Verify the image before collecting rollout evidence with
`./jetmon2 version` for Monitors and `/v2/status` for Verifliers.

If GHCR packages are private:

```bash
echo "$GHCR_PAT" | docker login ghcr.io -u <github-user> --password-stdin
docker pull ghcr.io/automattic/jetmon:<tag>
docker pull ghcr.io/automattic/veriflier:<tag>
```

## Runtime Config

Production images render JSON config from environment inputs at container start.
The binary reads the rendered JSON; it does not keep reading environment values
after startup.

| Render variable | Default | Meaning |
| --- | --- | --- |
| `JETMON_CONFIG_RENDER_MODE` | `always` | `always`, `missing`, or `never` for Monitor config rendering. |
| `VERIFLIER_CONFIG_RENDER_MODE` | `always` | Same for Veriflier config rendering. |

Use `never` only when mounting a complete JSON config and setting
`JETMON_CONFIG` or `VERIFLIER_CONFIG` to that path.

Production Monitor containers need:

- rendered config or a mounted complete JSON config;
- DB server-map path from the config-sync sidecar, or explicit DB config in
  non-production roles;
- WPCOM credential material when legacy notifications are enabled;
- v2 Veriflier endpoints or trusted discovery config;
- API/dashboard/debug bindings approved for the host;
- writable `/jetmon/stats` for PID, reload/drain, and any legacy stats files.

Important production render inputs:

| Variable | Production posture |
| --- | --- |
| `CONFIG_PROFILE` | `production` |
| `SCHEMA_MANAGEMENT_MODE` | `validate` |
| `DB_SERVER_MAP_PATH` | Mounted sidecar output; do not combine with explicit `DB_*`. |
| `DB_SERVER_MAP_DATACENTER` | Explicit datacenter value. |
| `DB_SERVER_MAP_ADDRESS` | Usually `internet` unless Systems approves internal DB hosts. |
| `STATSD_ADDR` | `host.docker.internal:8125` |
| `STATSD_HOST_PATH` | Stable v1-compatible metric identity. |
| `CHECK_TARGET_SAFETY_MODE` | `public_only` |
| `DEFAULT_CHECK_METHOD` / `DEFAULT_DETECTION_PROFILE` | Start rollout with `HEAD` / `legacy`. |
| `ROLLOUT_MODE` | `api-controlled` until activation. |
| `VERIFLIER_DISCOVERY_MODE` | `shadow` until registry drift is accepted. |
| `CHECK_HISTORY_MODE_DEFAULT` | `status_change` unless a focused test needs more. |
| `AUDIT_LOG_MODE_DEFAULT` | `operational` for rollout evidence without read firehose noise. |
| `LEGACY_STATUS_PROJECTION_ENABLE` | `true` while rollback or legacy readers need it. |
| `DEBUG_PORT` | `0` unless an approved localhost-only pprof window is active. |

Set production defaults explicitly even when the entrypoint has the same
fallback. Visible config review matters more than implicit defaults.

## Schema Posture

Production schema is applied by Systems before activation. The service should
run with `CONFIG_PROFILE=production` and `SCHEMA_MANAGEMENT_MODE=validate`; that
path checks tables, columns, and indexes but never applies DDL.

Use [production-schema.md](production-schema.md) for the reviewed
baseline SQL and table inventory. `jetpack_monitor_schema_migrations` is not a
production contract; validation checks the live schema shape through
`information_schema`.

## Validate A Container

Before activation, validate the exact image/config/secrets that docker-deploy
will run. Use a command override or test role that does not start the Monitor
loop.

```bash
docker run --rm \
  --add-host=host.docker.internal:host-gateway \
  --env-file jetmon-production.env \
  -v /srv/jetmon/config-source:/jetmon/config-source:ro \
  -v /srv/jetmon/stats:/jetmon/stats \
  ghcr.io/automattic/jetmon:<tag> \
  ./jetmon2 validate-config
```

Additional safe checks when the role is meant to prove dependencies:

```bash
docker run --rm \
  --add-host=host.docker.internal:host-gateway \
  --env-file jetmon-production.env \
  -v /srv/jetmon/config-source:/jetmon/config-source:ro \
  -v /srv/jetmon/stats:/jetmon/stats \
  ghcr.io/automattic/jetmon:<tag> \
  ./jetmon2 schema validate

docker run --rm \
  --add-host=host.docker.internal:host-gateway \
  --env-file jetmon-production.env \
  -v /srv/jetmon/config-source:/jetmon/config-source:ro \
  -v /srv/jetmon/stats:/jetmon/stats \
  ghcr.io/automattic/jetmon:<tag> \
  ./jetmon2 doctor --require-statsd
```

For a running container:

```bash
docker exec jetmon ./jetmon2 status
docker exec jetmon ./jetmon2 validate-config
```

Do not run `schema reconcile --execute` or deprecated `migrate` from a
production container unless that is the approved schema-change action.

## Restart, Drain, And Recreate

`jetmon2 reload` sends SIGHUP. In Docker this drains in-flight work and re-execs
through the entrypoint, so `JETMON_CONFIG_RENDER_MODE=always` re-renders config
before the binary starts again. Environment changes still require recreating the
container so the new environment exists inside it.

`jetmon2 drain` sends SIGINT and exits after the same drain path without
re-exec.

```bash
docker exec jetmon ./jetmon2 reload
docker exec jetmon ./jetmon2 drain
docker stop --time 45 jetmon
```

| Change | Production action |
| --- | --- |
| New image tag | docker-deploy rolling recreate with the pinned tag. |
| Rendered env changed | docker-deploy recreate so the container gets new env. |
| Mounted config file changed | `docker exec jetmon ./jetmon2 reload` if env is unchanged. |
| DB server map changed | Wait for hot reload, or reload for immediate pool rebuild. |
| Remove host from service | Drain/stop container; confirm `/api/v1/monitor/drain-status`. |

Set container `stop_grace_period` / stop timeout longer than Jetmon's drain
budget. The repo Compose files use `45s`.

Deploy order for a full fleet change: Verifliers, standalone deliverer if used,
then Monitor hosts one at a time.

## Health And Logs

| Surface | Use |
| --- | --- |
| `/api/v1/health` | API liveness and DB connectivity. |
| `/api/v1/ready` | Host readiness after process-health is fresh and green. |
| `/api/v1/monitor/stats` | Current stats snapshot and legacy file bodies. |
| `/api/v1/monitor/db-config` | Sanitized DB config reload status. |
| `/api/v1/verifliers/quorum-report` | Vantage health and quorum diagnostics. |
| Dashboard `/` and `/fleet` | Host and fleet operations. |
| `/debug/pprof/` | Localhost-only profiling when `DEBUG_PORT > 0`. |

```bash
curl -fsS "$JETMON_API_URL/api/v1/health"
curl -fsS "$JETMON_API_URL/api/v1/ready"
curl -fsS "$JETMON_API_URL/api/v1/monitor/drain-status"
docker logs --tail 200 jetmon
```

Runtime logs go to stdout/stderr. Site-state changes live in
`jetpack_monitor_events` and `jetpack_monitor_event_transitions`; operational
actions live in `jetpack_monitor_audit_log`.

Keep dashboard and pprof listeners on loopback unless protected by trusted
operator-network controls.

## Delivery Workers

Embedded workers are eligible when `API_PORT > 0`; `jetmon-deliverer` runs the
same queues outside the Monitor container. Row claims are transactional, so
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

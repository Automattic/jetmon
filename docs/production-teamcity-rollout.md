# Production TeamCity Rollout

This document covers the production Monitor deployment path through TeamCity
and docker-deploy. It is about the Monitor container stack only.

Use these alongside it:

- [v1-to-v2-migration.md](v1-to-v2-migration.md) for rollout sequencing.
- [production-veriflier-compose.md](production-veriflier-compose.md) for
  Veriflier VPS deployment.
- [operations-guide.md](operations-guide.md) for steady-state operations.

The production Monitor image must be secret-free. SVN credentials, DB
credentials, WPCOM credentials, generated config files, and rendered private
config must stay out of Git, image layers, TeamCity logs, and PR descriptions.

## Deployment Shape

Each production Monitor host runs a docker-deploy-managed Compose service with:

- `jetmon2` Monitor container,
- `config-sync` sidecar container,
- shared runtime path for generated private config such as `db-servers.php`,
- rendered JSON config read by the Monitor binary,
- access to host-local StatsD through Docker bridge networking.

The Monitor stack must not include StatsD or Graphite containers. Production
Monitor hosts already provide StatsD at `127.0.0.1:8125` on the host. Bridge
containers should reach it with Docker's host-gateway mapping:

```text
--add-host=host.docker.internal:host-gateway
"STATSD_ADDR": "host.docker.internal:8125"
```

Do not use host networking for production Monitor containers.

## Required Inputs

| Input | Source | Notes |
| --- | --- | --- |
| Monitor image tag | TeamCity | Prefer immutable Git SHA tags. |
| Config-sync image tag | TeamCity | Built separately from the Monitor image. |
| `config-sync.env` | Systems secret injection | SVN credentials and sync paths. Never commit it. |
| `db-servers.php` | Runtime sidecar output | Synced from SVN and mounted read-only into the Monitor. |
| `config.json` | docker-deploy role config / secure parameters | Operational config lives in JSON; env vars are render inputs only. |
| `CONFIG_PROFILE` and `SCHEMA_MANAGEMENT_MODE` | rendered config | Production uses `CONFIG_PROFILE=production`; leave schema mode unset or set it to `validate` so startup validates schema and never applies DDL. |
| `DB_SERVER_MAP_PATH` | rendered config | Points at mounted `db-servers.php`. |
| `DB_SERVER_MAP_DATACENTER` | rendered config | Explicit value; do not parse container hostname. |
| `STATSD_ADDR` | rendered config | `host.docker.internal:8125`. |
| `HOSTNAME` / `JETMON_HOSTNAME` | rendered config or render input | Stable process identity, not container ID. |
| `STATSD_HOST_PATH` | rendered config | v1-compatible Graphite path, for example `dfw1.jetmon-prod-1`. |
| `CHECK_TARGET_SAFETY_MODE` | rendered config | `public_only` in production. |
| `DEFAULT_CHECK_METHOD` / `DEFAULT_DETECTION_PROFILE` | rendered config | Start rollout with `HEAD` / `legacy`; move cohorts through API-controlled stages. |
| `ROLLOUT_MODE` | rendered config | `api-controlled` until explicit activation grants buckets. |
| `VERIFLIER_DISCOVERY_MODE` | rendered config | `shadow` until DB registry drift reports are clean, then move to `active`. |
| `CHECK_HISTORY_MODE_DEFAULT` | rendered config | `status_change` unless a focused test explicitly needs full history. |
| `AUDIT_LOG_MODE_DEFAULT` | rendered config | `operational` for rollout evidence without API/read firehose noise. |
| `LEGACY_STATUS_PROJECTION_ENABLE` | rendered config | `true` while rollback or legacy readers still need `jetpack_monitor_sites.site_status`. |
| `DEBUG_PORT` | rendered config | `0` unless an approved debugging window requires localhost-only pprof. |
| WPCOM legacy cert/key | Systems secret mount | Required for `WPCOM_NOTIFY_MODE=legacy`. |

Use [../config/jetmon-config-sync-sample.env](../config/jetmon-config-sync-sample.env)
only as a sidecar env template.

## Config-Sync Sidecar

The sidecar pulls the production DB server map from SVN and writes
`db-servers.php` to a shared runtime path. The Monitor receives that path
read-only through `DB_SERVER_MAP_PATH`.

Sidecar requirements:

- run as the unprivileged `jetmon` user where possible,
- keep SVN working copy and credentials outside the Monitor-readable path,
- log only success/failure and generated path status,
- add jitter to avoid fleet-wide refresh bursts,
- retry transient SVN failures without restarting the Monitor,
- never print database passwords or SVN credentials.

The loop uses [../scripts/jetmon-config-sync-loop.sh](../scripts/jetmon-config-sync-loop.sh),
which wraps [../scripts/jetmon-config-update.sh](../scripts/jetmon-config-update.sh).

If docker-deploy cannot support sidecar secrets and a shared runtime path, the
host-side fallback is [../systemd/jetmon-config-sync.service](../systemd/jetmon-config-sync.service)
plus [../systemd/jetmon-config-sync.timer](../systemd/jetmon-config-sync.timer).
Use that only as a fallback; the preferred production shape keeps sync inside
the docker-deploy service.

## Database Config

Jetmon supports two mutually exclusive database modes:

1. explicit `DB_*` JSON config for local/dev/test;
2. `DB_SERVER_MAP_PATH` pointing to synced `db-servers.php` for production.

Do not set `DB_SERVER_MAP_PATH` together with explicit `DB_HOST`, `DB_PORT`,
`DB_USER`, `DB_PASSWORD`, or `DB_NAME`.

With `DB_SERVER_MAP_PATH`, Jetmon reads the `misc` dataset:

- writes and transactions use the write-master row,
- reads prefer read-enabled non-`bak` rows in `DB_SERVER_MAP_DATACENTER`,
- other non-`bak` read rows are retained as failover targets,
- if no read rows exist, reads use the write endpoint,
- `DB_SERVER_MAP_ADDRESS=internet` is the conservative v1-compatible posture
  unless Systems confirms internal DB hostnames work from the container network.

The Monitor and deliverer re-parse the map on the refresh cadence. Changed maps
are validated before publication. If parsing or validation fails, the process
keeps the previous working pools and logs the failure without secrets.

SIGHUP is the graceful restart path for production containers. It stops intake,
drains in-flight work, then re-execs through the Docker entrypoint so rendered
config, startup validation, and replaced on-disk binaries are picked up without
an ungraceful kill. SIGINT and SIGTERM drain and exit without re-exec.

Inspect reload state through the API:

```bash
./jetmon2 api request --pretty GET /api/v1/monitor/db-config
```

## Schema Management

Production schema changes must go through the approved database-change process
before Monitor containers are activated. Use
[`production-schema-package.md`](production-schema-package.md) and
[`migrations/production-v2-baseline.sql`](../migrations/production-v2-baseline.sql)
as the review package.

Production config should use both an explicit production profile and read-only
schema mode:

```json
{
  "CONFIG_PROFILE": "production",
  "SCHEMA_MANAGEMENT_MODE": "validate"
}
```

Normal startup validates schema. It does not apply DDL. In approved local or
lab environments, `./jetmon2 schema reconcile --execute` can apply missing
additive objects from the reviewed baseline. If Systems applies SQL manually,
the production package must include the required tables, columns, and indexes;
the legacy local/lab `jetpack_monitor_schema_migrations` ledger is not required
for production startup validation.

## Production Defaults

Set production defaults explicitly even when the Docker entrypoint has the same
fallback. Security and rollout posture should be visible in config review:

```json
{
  "CONFIG_PROFILE": "production",
  "SCHEMA_MANAGEMENT_MODE": "validate",
  "STATSD_ADDR": "host.docker.internal:8125",
  "STATSD_HOST_PATH": "dfw1.jetmon-prod-1",
  "CHECK_TARGET_SAFETY_MODE": "public_only",
  "DEFAULT_CHECK_METHOD": "HEAD",
  "DEFAULT_DETECTION_PROFILE": "legacy",
  "ROLLOUT_MODE": "api-controlled",
  "VERIFLIER_DISCOVERY_MODE": "shadow",
  "CHECK_HISTORY_MODE_DEFAULT": "status_change",
  "AUDIT_LOG_MODE_DEFAULT": "operational",
  "LEGACY_STATUS_PROJECTION_ENABLE": true,
  "DEBUG_PORT": 0,
  "WPCOM_NOTIFY_ENABLE": false,
  "WPCOM_NOTIFY_MODE": "legacy"
}
```

`WPCOM_NOTIFY_ENABLE=false` is the standby/rehearsal posture. Enable WPCOM
notifications only during the approved activation window after canary checks
and WPCOM-owned parity cases are accepted.

## StatsD

Production Monitor containers should render:

```text
STATSD_ADDR=host.docker.internal:8125
```

and docker-deploy must include:

```text
--add-host=host.docker.internal:host-gateway
```

Do not render `127.0.0.1:8125` inside a bridge-networked Monitor container; it
points at the container itself.

Set `STATSD_HOST_PATH` explicitly to the v1-compatible metric identity:

```text
HOSTNAME=jetmon-prod-1.dfw1.example.com
STATSD_HOST_PATH=dfw1.jetmon-prod-1
```

Metrics then use:

```text
com.jetpack.jetmon.dfw1.jetmon-prod-1.<metric>
```

Keep `HOSTNAME` and `STATSD_HOST_PATH` stable and low-cardinality. Do not use
container IDs, release SHAs, ports, process IDs, or random suffixes.

StatsD is UDP. Jetmon can validate local client setup but cannot prove
downstream Graphite ingestion without an external Systems check.

## WPCOM Notifications

Initial rollout should use:

```json
{
  "WPCOM_NOTIFY_ENABLE": true,
  "WPCOM_NOTIFY_MODE": "legacy"
}
```

Legacy mode matches v1's client-certificate `/jetmon/` path. Mount the legacy
certificate and key as runtime secrets and set
`WPCOM_NOTIFY_LEGACY_CERT_PATH` / `WPCOM_NOTIFY_LEGACY_KEY_PATH`.

`WPCOM_NOTIFY_MODE=modern` is retained for WPCOM contract testing only until
WPCOM approves it for production notification traffic.

## TeamCity Job

The TeamCity job should:

1. Build the Monitor image from [../docker/Dockerfile_jetmon](../docker/Dockerfile_jetmon).
2. Push Monitor tags: `latest` and immutable Git SHA.
3. Build the config-sync image from [../docker/Dockerfile_config_sync](../docker/Dockerfile_config_sync).
4. Push config-sync tags: `latest` and immutable Git SHA.
5. Call docker-deploy for the Jetmon Monitor role, for example:

   ```text
   deploy-to-servers-by-role.sh docker-jetmon-monitor jetmon-monitor/<git-sha>
   ```

The docker-deploy role owns target hosts, image/tag mapping, secure sidecar
env injection, shared runtime path, host-gateway mapping, rendered config,
WPCOM cert/key mounts, rollout order, health checks, and rollback behavior.

## Safe Deployment Smoke

Before activation, use a dedicated test role or command override that does not
start the Monitor loop:

```bash
./jetmon2 version
./jetmon2 validate-config
./jetmon2 schema validate
./jetmon2 doctor --require-statsd
```

Safe smoke should prove:

- TeamCity built and pushed the intended image tags.
- docker-deploy deployed the selected Git SHA.
- config and secrets arrived outside image layers and logs.
- DB, StatsD, WPCOM secret files, and Verifliers are reachable.
- schema state matches the binary.
- the container stays healthy without checking sites.

Safe smoke config should disable customer-impacting paths:

```json
{
  "WPCOM_NOTIFY_ENABLE": false,
  "WPCOM_NOTIFY_MODE": "legacy",
  "CHECK_TARGET_SAFETY_MODE": "public_only",
  "EMAIL_TRANSPORT": "stub",
  "API_PORT": 0,
  "DASHBOARD_PORT": 0,
  "DEBUG_PORT": 0
}
```

Use a read-only DB user where possible. Do not run the bare server process,
rollout cutover commands, or mutating API operations until the database is
intentionally writable and the rollout window permits Monitor work.

## Activation Gates

After docker-deploy starts or recreates a Monitor during an approved rollout,
run from the operator environment:

```bash
./jetmon2 schema validate
./jetmon2 doctor --require-statsd
./jetmon2 status
./jetmon2 verifliers discovery-report
./jetmon2 rollout state-report --since=15m
./jetmon2 telemetry report --since=15m
```

During v1-to-v2 migration, also follow the range-specific gates in
[v1-to-v2-migration.md](v1-to-v2-migration.md) and
[rollout-quick-reference.md](rollout-quick-reference.md). Do not activate a v2
range until corresponding v1 ownership has stopped.

## Rollback

- **Image/config failure before activation:** redeploy the previous known-good
  Git SHA through docker-deploy.
- **Bad config-sync sidecar:** keep the Monitor on last validated DB pools, fix
  sidecar config, and confirm `/api/v1/monitor/db-config`.
- **Monitor started but not activated:** stop or redeploy the Monitor stack; no
  site checks should have occurred.
- **Range already activated:** use the rollback path in
  [v1-to-v2-migration.md](v1-to-v2-migration.md) before returning ownership to
  v1.

Do not roll back schema migrations as part of service rollback unless the
database-change process explicitly approved a reverse migration.

## Systems Confirmation Checklist

Confirm before production rollout:

- docker-deploy role name and target host list,
- Monitor and config-sync image names and tag mapping,
- how `config-sync.env` is injected and protected,
- whether the shared runtime path is a volume, tmpfs, or host mount,
- exact Monitor-readable `DB_SERVER_MAP_PATH`,
- per-host `DB_SERVER_MAP_DATACENTER`,
- host-gateway support for `host.docker.internal`,
- WPCOM cert/key mount paths,
- docker-deploy health-check and rollback behavior,
- where external stats consumers should read v1-style stats from:
  `/api/v1/monitor/stats`, StatsD/Graphite, or an explicitly approved mount.

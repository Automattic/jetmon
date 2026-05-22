# Production TeamCity Rollout

This document covers the production Monitor deployment path through TeamCity
and docker-deploy. It is specifically about the Monitor container stack. For
the full v1-to-v2 migration sequence, use
[v1-to-v2-migration.md](v1-to-v2-migration.md). For Veriflier VPS deployment,
use [production-veriflier-compose.md](production-veriflier-compose.md).

The production Monitor stack is intentionally secret-free at image-build time.
SVN credentials, database credentials, WPCOM credentials, and generated config
files must stay out of Git, image layers, TeamCity logs, and PR descriptions.

## Deployment Shape

Each production Monitor host runs a docker-deploy-managed Compose service with:

- `jetmon2` Monitor container.
- `config-sync` sidecar container.
- shared runtime path for generated private config such as `db-servers.php`.
- rendered JSON config read by the Monitor binary.
- access to the host-local StatsD proxy through Docker bridge networking.

The Monitor stack should not include StatsD or Graphite containers. Production
Monitor hosts already provide local StatsD at `127.0.0.1:8125` on the host.
Bridge-networked containers should reach that service through Docker's
host-gateway mapping, not host networking.

```text
--add-host=host.docker.internal:host-gateway
"STATSD_ADDR": "host.docker.internal:8125"
```

## Required Inputs

| Input | Source | Notes |
| --- | --- | --- |
| Monitor image tag | TeamCity build output | Use immutable Git SHA tags for rollout. |
| Config-sync image tag | TeamCity build output | Built separately from the Monitor image. |
| `config-sync.env` | Systems secret injection | SVN credentials and sync paths for the sidecar. Never commit it. |
| `db-servers.php` | Generated runtime file | Synced from SVN by the sidecar, mounted read-only into the Monitor. |
| Rendered `config.json` | docker-deploy role config / TeamCity secure parameters | The binary reads JSON config, not direct environment overrides. |
| `CONFIG_PROFILE` or `SCHEMA_MANAGEMENT_MODE` | rendered config | Production should validate schema, not apply DDL. |
| `DB_SERVER_MAP_PATH` | rendered config | Points at the mounted generated `db-servers.php`. |
| `DB_SERVER_MAP_DATACENTER` | rendered config | Set explicitly; do not depend on container hostname parsing. |
| `STATSD_ADDR` | rendered config | `host.docker.internal:8125` for production Monitor containers. |
| `HOSTNAME` / `JETMON_HOSTNAME` | rendered config or render input | Stable process identity, not container ID. |
| `STATSD_HOST_PATH` | rendered config | v1-compatible Graphite path, for example `dfw1.jetmon-prod-1`. |
| WPCOM legacy cert/key | Systems secret mount | Required when `WPCOM_NOTIFY_ENABLE=true` and `WPCOM_NOTIFY_MODE=legacy`. |

Use [../config/jetmon-config-sync-sample.env](../config/jetmon-config-sync-sample.env)
only as the template for the sidecar env file.

## Config-Sync Sidecar

The config-sync sidecar is responsible for pulling the production DB server map
from SVN and writing the generated `db-servers.php` to the shared runtime path.
The Monitor receives that path read-only.

Expected behavior:

- run as the unprivileged `jetmon` user where possible,
- keep SVN working copy and credentials outside the Monitor-readable path,
- log only success/failure and generated path status,
- add jitter to avoid fleet-wide refresh bursts,
- retry transient SVN failures without restarting the Monitor,
- never print database passwords or SVN credentials.

The sidecar loop calls
[../scripts/jetmon-config-sync-loop.sh](../scripts/jetmon-config-sync-loop.sh),
which wraps
[../scripts/jetmon-config-update.sh](../scripts/jetmon-config-update.sh).

If docker-deploy cannot support the sidecar secret mount and shared runtime
path, the host-side fallback is:

- [../systemd/jetmon-config-sync.service](../systemd/jetmon-config-sync.service)
- [../systemd/jetmon-config-sync.timer](../systemd/jetmon-config-sync.timer)

Use that only as a fallback. The preferred production shape keeps sync inside
the docker-deploy service.

## Database Server Map

Jetmon supports two database config modes:

1. Explicit DB credentials in JSON config for local/dev/test.
2. `DB_SERVER_MAP_PATH` pointing to a synced `db-servers.php` for production.

These modes are mutually exclusive. If `DB_SERVER_MAP_PATH` is set, the Monitor
must not also set explicit `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, or
`DB_NAME`.

With `DB_SERVER_MAP_PATH`, Jetmon reads the `misc` dataset:

- writes and transactions use the write-master row,
- reads prefer read-enabled non-`bak` rows in `DB_SERVER_MAP_DATACENTER`,
- other non-`bak` read rows are retained as failover targets,
- if no read rows exist, reads use the write endpoint,
- `DB_SERVER_MAP_ADDRESS=internet` matches the conservative v1-compatible
  posture unless Systems confirms internal DB hostnames are reachable from the
  container network.

The Monitor and deliverer re-parse the map on the configured refresh cadence.
Changed maps are validated before publication. If parsing or validation fails,
the process keeps the previous working pools and logs the failure without
printing secrets.

Operators can inspect reload state without entering the container:

```bash
./jetmon2 api request --pretty GET /api/v1/monitor/db-config
```

## Schema Management

Production schema changes must be applied through the approved database-change
process before Monitor containers are activated.

Production Monitor config should use one of:

```json
{ "CONFIG_PROFILE": "production" }
```

or:

```json
{ "SCHEMA_MANAGEMENT_MODE": "validate" }
```

Normal production startup should run schema validation, not automatic
migration:

```bash
./jetmon2 schema validate
./jetmon2 validate-config
```

Run `./jetmon2 migrate` only as an explicit schema-change action in an
environment where applying DDL is approved. If Systems applies SQL manually, the
matching `jetpack_monitor_schema_migrations` rows must be included in the
approved change package.

## StatsD

Production Monitor containers should render:

```text
STATSD_ADDR=host.docker.internal:8125
```

and docker-deploy must include:

```text
--add-host=host.docker.internal:host-gateway
```

Do not use host networking. Do not render `127.0.0.1:8125` inside a
bridge-networked Monitor container; that points at the container itself.

Set `STATSD_HOST_PATH` explicitly to the v1-compatible metric identity:

```text
HOSTNAME=jetmon-prod-1.dfw1.example.com
STATSD_HOST_PATH=dfw1.jetmon-prod-1
```

This emits:

```text
com.jetpack.jetmon.dfw1.jetmon-prod-1.<metric>
```

Keep `HOSTNAME` and `STATSD_HOST_PATH` stable and low-cardinality. Do not use
container IDs, release SHAs, process IDs, ports, or random suffixes.

StatsD is UDP, so Jetmon can validate local client setup but cannot prove
downstream Graphite ingestion without an external Systems-provided check.

## WPCOM Notifications

Initial production rollout should use:

```json
{
  "WPCOM_NOTIFY_ENABLE": true,
  "WPCOM_NOTIFY_MODE": "legacy"
}
```

Legacy mode matches v1's client-certificate `/jetmon/` notification path.
Mount the WPCOM legacy client certificate and key as runtime secrets and point
`WPCOM_NOTIFY_LEGACY_CERT_PATH` and `WPCOM_NOTIFY_LEGACY_KEY_PATH` at the
mounted files.

`WPCOM_NOTIFY_MODE=modern` is retained for WPCOM contract testing only until
WPCOM explicitly approves it for production notification traffic.

## TeamCity Job

The TeamCity job should follow the existing docker-deploy pattern:

1. Build the Monitor image from
   [../docker/Dockerfile_jetmon](../docker/Dockerfile_jetmon).
2. Push Monitor tags: `latest` and the immutable Git SHA.
3. Build the config-sync image from
   [../docker/Dockerfile_config_sync](../docker/Dockerfile_config_sync).
4. Push config-sync tags: `latest` and the immutable Git SHA.
5. Call docker-deploy for the Jetmon Monitor role, for example:

   ```text
   deploy-to-servers-by-role.sh docker-jetmon-monitor jetmon-monitor/<git-sha>
   ```

The docker-deploy role owns:

- target Monitor hosts,
- image names and tag mapping,
- secure injection of `config-sync.env`,
- shared runtime path between sidecar and Monitor,
- host-gateway mapping for StatsD,
- rendered JSON config values,
- WPCOM certificate/key mounts,
- per-host rollout order and health/rollback behavior.

## Safe Deployment Smoke

Before a real production activation, use a dedicated test role or command
override that does not start the Monitor loop:

```bash
./jetmon2 version
./jetmon2 validate-config
./jetmon2 schema validate
./jetmon2 doctor --require-statsd
```

This smoke should prove:

- TeamCity can build and push the intended image tags.
- docker-deploy can deploy the selected Git SHA.
- the container receives non-image config and secrets without leaking them,
- the container can reach DB, StatsD, WPCOM config files, and Verifliers,
- schema state matches the binary,
- the container can stay healthy without checking sites.

Safe smoke config should set:

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

Use a read-only DB user for safe deployment smoke where possible. Do not run the
bare `jetmon2` server process, rollout cutover commands, or mutating API
operations until the target database is intentionally writable and the rollout
window permits Monitor work.

## Production Activation Checks

After docker-deploy starts or recreates a Monitor during an approved rollout,
run the normal gates from the operator environment:

```bash
./jetmon2 schema validate
./jetmon2 doctor --require-statsd
./jetmon2 status
./jetmon2 verifliers discovery-report
./jetmon2 rollout state-report --since=15m
./jetmon2 telemetry report --since=15m
```

During the v1-to-v2 migration, also follow the range-specific gates in
[v1-to-v2-migration.md](v1-to-v2-migration.md) and
[rollout-quick-reference.md](rollout-quick-reference.md). Do not activate a v2
range until the corresponding v1 ownership has stopped.

## Rollback

Rollback depends on what changed:

- **Image/config-only failure before range activation:** redeploy the previous
  known-good Git SHA through docker-deploy.
- **Bad config-sync sidecar:** keep the Monitor on the last validated DB pools,
  fix sidecar config, and confirm `/api/v1/monitor/db-config`.
- **Monitor started but not activated:** stop/redeploy the Monitor stack; no
  site checks should have occurred.
- **Range already activated:** use the rollback path in
  [v1-to-v2-migration.md](v1-to-v2-migration.md) before returning ownership to
  v1.

Do not roll back schema migrations as part of service rollback unless the
database-change process explicitly approved a reverse migration. V2 additive
tables can remain present while v1 resumes handling traffic.

## Systems Confirmation Checklist

Before production rollout, confirm:

- docker-deploy role name and target host list,
- image names and tag mapping for Monitor and config-sync,
- how `config-sync.env` is injected and protected,
- whether the shared runtime path is a volume, tmpfs, or host mount,
- the exact Monitor-readable `DB_SERVER_MAP_PATH`,
- per-host `DB_SERVER_MAP_DATACENTER`,
- host-gateway support for `host.docker.internal`,
- WPCOM cert/key mount paths,
- whether docker-deploy performs health checks and automatic rollback,
- where external stats consumers should read v1-style stats from:
  `/api/v1/monitor/stats`, StatsD/Graphite, or an explicitly approved mount.

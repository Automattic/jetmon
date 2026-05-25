# Docker Images

Jetmon publishes runtime images to GitHub Container Registry:

| Image | Dockerfile |
| --- | --- |
| `ghcr.io/automattic/jetmon` | `docker/Dockerfile_jetmon` |
| `ghcr.io/automattic/veriflier` | `docker/Dockerfile_veriflier` |

Production TeamCity rollout may also build the config-sync sidecar from
`docker/Dockerfile_config_sync`; that sidecar is covered in
[production-teamcity-rollout.md](production-teamcity-rollout.md).

Use this doc for running pre-built images. Use
[getting-started.md](getting-started.md) for the build-from-source local Docker
stack.

## Tags

- `latest`: current `v2` branch.
- `<YYYYMMDD>-<short-sha>`: immutable tag for every push to `v2`. Use this to
  pin production deployments to a specific build.
- `pr-<short-sha>`: PR build when the PR has the `Docker Build` label.

There are no semver tags yet. Pin to a date-SHA tag when reproducibility matters.

## Authentication

If GHCR packages are private, authenticate before pulling:

```bash
echo "$GHCR_PAT" | docker login ghcr.io -u <github-user> --password-stdin
```

`GHCR_PAT` needs `read:packages`. If the package is public, anonymous pulls work.

## Config Rendering

The Docker entrypoints render JSON config from environment inputs on every
container start by default. The binaries read the rendered JSON; they do not
read these settings directly from the environment after startup.

Render modes:

| Variable | Default | Meaning |
| --- | --- | --- |
| `JETMON_CONFIG_RENDER_MODE` | `always` | `always`, `missing`, or `never` for Monitor config rendering. |
| `VERIFLIER_CONFIG_RENDER_MODE` | `always` | `always`, `missing`, or `never` for Veriflier config rendering. |

Use `never` only when mounting a complete JSON config and setting `JETMON_CONFIG`
or `VERIFLIER_CONFIG` to that path.

## Run Veriflier

```bash
docker pull ghcr.io/automattic/veriflier:latest

docker run --rm \
  --name veriflier \
  -p 7803:7803 \
  -e VERIFLIER_AUTH_TOKEN=replace_me \
  -e VERIFLIER_PORT=7803 \
  ghcr.io/automattic/veriflier:latest
```

Smoke check:

```bash
curl http://localhost:7803/v2/status
```

Common Veriflier render inputs:

| Variable | Notes |
| --- | --- |
| `VERIFLIER_AUTH_TOKEN` | Shared secret used by Monitors. |
| `VERIFLIER_PORT` | Defaults to `7803`. |
| `VERIFLIER_ENABLE_LEGACY_HTTP` | Defaults to `false`; enable only for lab/emergency compatibility testing. |
| `CHECK_TARGET_SAFETY_MODE` | Veriflier outbound target safety policy; keep `public_only` for production and real site data. |
| `STATSD_ADDR` | Optional StatsD endpoint such as `statsd:8125`; leave empty to disable metrics. |
| `JETMON_HOSTNAME` | Stable Veriflier process identity, such as `<region>.<vantage>`. |
| `STATSD_HOST_PATH` | Optional explicit Graphite path. |

Production Veriflier VPS Compose includes StatsD/Graphite. See
[production-veriflier-compose.md](production-veriflier-compose.md).

## Run Jetmon

Jetmon needs database connectivity and, for normal monitoring, at least one
reachable Veriflier.

```bash
docker pull ghcr.io/automattic/jetmon:latest

docker run --rm \
  --name jetmon \
  -p 8080:8080 \
  -p 8090:8090 \
  -e DB_HOST=mysql.internal \
  -e DB_PORT=3306 \
  -e DB_USER=jetmon \
  -e DB_PASSWORD=replace_me \
  -e DB_NAME=jetmon_db \
  -e VERIFLIER_AUTH_TOKEN=replace_me \
  -e VERIFLIER_PORT=7803 \
  -e WPCOM_AUTH_TOKEN=change_me \
  -e WPCOM_NOTIFY_ENABLE=false \
  -e EMAIL_TRANSPORT=stub \
  -v "$(pwd)/jetmon-stats:/jetmon/stats" \
  ghcr.io/automattic/jetmon:latest
```

Exposed ports:

| Port | Purpose |
| --- | --- |
| `8080` | Operator dashboard |
| `8090` | Internal REST API |

## Monitor Config Inputs

Database modes are mutually exclusive:

| Mode | Inputs |
| --- | --- |
| Explicit DSN | `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` |
| Production server map | `DB_SERVER_MAP_PATH`, `DB_SERVER_MAP_DATACENTER`, optional `DB_SERVER_MAP_ADDRESS` |

Do not set `DB_SERVER_MAP_PATH` together with explicit `DB_*` credentials.

Important Monitor render inputs:

| Variable | Notes |
| --- | --- |
| `CONFIG_PROFILE` | Use `production` for production Monitor containers. |
| `SCHEMA_MANAGEMENT_MODE` | Use `validate` in production after Systems applies schema changes. |
| `VERIFLIER_AUTH_TOKEN`, `VERIFLIER_PORT` | Shared with configured Verifliers. |
| `WPCOM_AUTH_TOKEN` | Use a placeholder outside WPCOM-connected environments. |
| `WPCOM_NOTIFY_ENABLE` | Set `false` for local, ad-hoc, and internal-only tests. |
| `WPCOM_NOTIFY_MODE` | Defaults to `legacy`; use `modern` only for WPCOM contract testing. |
| `WPCOM_NOTIFY_LEGACY_CERT_PATH`, `WPCOM_NOTIFY_LEGACY_KEY_PATH` | Required secret paths when legacy WPCOM notifications are enabled. |
| `CHECK_TARGET_SAFETY_MODE` | Keep `public_only` for production and real site data. |
| `ROLLOUT_MODE` | Use `api-controlled` for production rollout until explicit activation. |
| `VERIFLIER_DISCOVERY_MODE` | Use `shadow` until trusted registry drift reports are clean. |
| `EMAIL_TRANSPORT` | Use `stub` for dev; use `smtp` or `wpcom` only when configured. |
| `STATSD_ADDR` | StatsD endpoint; leave empty to disable. |
| `JETMON_HOSTNAME` | Docker render input for stable process identity. |
| `STATSD_HOST_PATH` | Explicit v1-compatible Graphite path. |

With `CONFIG_PROFILE=production`, the Monitor entrypoint renders safer
production fallbacks when the matching variables are omitted:

- `SCHEMA_MANAGEMENT_MODE=validate`
- `STATSD_ADDR=host.docker.internal:8125`
- `CHECK_TARGET_SAFETY_MODE=public_only`
- `DEFAULT_CHECK_METHOD=HEAD`
- `DEFAULT_DETECTION_PROFILE=legacy`
- `ROLLOUT_MODE=api-controlled`
- `VERIFLIER_DISCOVERY_MODE=shadow`
- `DEBUG_PORT=0`

Set these explicitly in production roles anyway. Visible config review is more
important than relying on implicit fallbacks.

For production Monitor containers, use the host-local StatsD proxy through
Docker bridge networking:

```text
--add-host=host.docker.internal:host-gateway
STATSD_ADDR=host.docker.internal:8125
```

Do not use host networking. Do not use `127.0.0.1:8125` from a bridge-networked
Monitor container; it points inside the container.

Example production metric identity:

```text
JETMON_HOSTNAME=jetmon-prod-1.dfw1.example.com
STATSD_HOST_PATH=dfw1.jetmon-prod-1
```

This emits under:

```text
com.jetpack.jetmon.dfw1.jetmon-prod-1.<metric>
```

Keep metric identities stable and low-cardinality. Do not include container
IDs, release SHAs, process IDs, ports, or random suffixes.

## Volume Mounts

| Path | Reason |
| --- | --- |
| `/jetmon/config` | Optional complete config mount. Use with render mode `never`. |
| `/jetmon/config-source` | Production generated private files such as `db-servers.php`; mount read-only into the Monitor. |
| `/jetmon/stats` | PID file for `reload` / `drain` and v1-style stats files. |

Production stats consumers should prefer `/api/v1/monitor/stats` or StatsD over
host filesystem reads unless Systems explicitly approves a mount.

## Validate Inside A Container

```bash
docker run --rm \
  -e DB_HOST=mysql.internal \
  -e DB_PORT=3306 \
  -e DB_USER=jetmon \
  -e DB_PASSWORD=replace_me \
  -e DB_NAME=jetmon_db \
  -e VERIFLIER_AUTH_TOKEN=replace_me \
  ghcr.io/automattic/jetmon:latest ./jetmon2 validate-config
```

Stronger smoke checks with the same rendered config:

```bash
./jetmon2 schema validate
./jetmon2 doctor --require-statsd
```

## Reload And Drain

`reload` and `drain` use `/jetmon/stats/jetmon2.pid`, so `/jetmon/stats` must be
writable:

```bash
docker exec jetmon ./jetmon2 reload
docker exec jetmon ./jetmon2 drain
```

## Test A PR Image

```bash
docker pull ghcr.io/automattic/jetmon:pr-<short-sha>
docker pull ghcr.io/automattic/veriflier:pr-<short-sha>
```

Find the short SHA in the PR's `Build and publish Docker images` workflow run.
The PR must have the `Docker Build` label before the workflow runs.

## Local Compose

For the complete local development stack, use the Compose files under
`docker/`. They include local database, Mailpit, StatsD/Graphite, fixture
services, Monitor, and Veriflier.

Ad-hoc Compose runs using GHCR images can use the same environment inputs shown
above, but the database is intentionally not embedded in this doc. Use an
existing database or the repo's local development stack.

Runtime logs go to stdout/stderr for Docker or the deployment platform to
collect. V2 does not create v1 `jetmon.log` or `status-change.log` files.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Pull is denied | Authenticate to GHCR with a PAT that has `read:packages`, or confirm package visibility. |
| DB connection fails | `localhost` inside a container is the container. Use a Docker network, host IP, or `host.docker.internal`. |
| Env changes do not apply | Confirm render mode is `always` or recreate/remove the existing rendered config when using `missing`. |
| `reload` / `drain` has no PID file | Mount a writable `/jetmon/stats`. |
| Monitor cannot reach Veriflier | Match `VERIFLIER_AUTH_TOKEN` and confirm port `7803` is reachable from the Monitor container. |
| StatsD missing in production | Use `host.docker.internal:8125` plus `host.docker.internal:host-gateway`; do not use `127.0.0.1`. |

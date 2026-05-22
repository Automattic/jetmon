# Running Jetmon And Veriflier From GHCR

The CI workflow `.github/workflows/docker-publish.yml` publishes the two runtime
application images to GitHub Container Registry:

| Image | Source Dockerfile |
|---|---|
| `ghcr.io/automattic/jetmon` | `docker/Dockerfile_jetmon` |
| `ghcr.io/automattic/veriflier` | `docker/Dockerfile_veriflier` |

Production TeamCity rollout can additionally build the config-sync sidecar from
`docker/Dockerfile_config_sync`; that image is not part of the public GHCR
development workflow.

This guide is for running those pre-built images. The build-from-source flow
for local development stays in
[getting-started.md](getting-started.md).

## Tags

- `:latest` — head of the `v2` branch. Updated on every push to `v2`.
- `:<short-sha>` — built from a pull request when the PR carries the
  `Docker Build` label. Use these for testing an unmerged change end to end.

There are no semver tags yet; pin to a specific short SHA when reproducibility
matters.

## Authenticate (If Private)

GHCR packages start out private. Until the package is made public or linked to
the repository in the GHCR UI, every host that needs to pull must authenticate:

```bash
echo "$GHCR_PAT" | docker login ghcr.io -u <github-user> --password-stdin
```

`GHCR_PAT` is a GitHub personal access token with `read:packages`. After the
package is made public, anonymous `docker pull` works and this step can be
skipped.

## Run Veriflier

Veriflier is the simpler of the two — it has no database dependency and only
needs an auth token shared with Jetmon:

```bash
docker pull ghcr.io/automattic/veriflier:latest

docker run --rm \
  --name veriflier \
  -p 7803:7803 \
  -e VERIFLIER_AUTH_TOKEN=replace_me \
  -e VERIFLIER_PORT=7803 \
  ghcr.io/automattic/veriflier:latest
```

The entrypoint renders a generated Veriflier JSON config from
`veriflier-sample.json` on every container start by default. Those env vars are
Docker template inputs; the Veriflier binary reads the rendered JSON config.
Health check: `curl http://localhost:7803/v2/status` should return
`{"status":"OK",...}`.

Required env vars:

| Var | Notes |
|---|---|
| `VERIFLIER_AUTH_TOKEN` | Must match the value Jetmon uses to call this verifier. |
| `VERIFLIER_PORT` | Defaults to `7803`. |
| `VERIFLIER_ENABLE_LEGACY_HTTP` | Optional. Defaults to `false`; set to `true` only for lab/emergency compatibility with `veriflier2`'s legacy HTTP `/check` and `/status` endpoints. |
| `STATSD_ADDR` | Optional template input for the rendered `statsd_addr` config value. Leave unset to run without Veriflier metrics, or set to `statsd:8125` / another approved endpoint. |
| `JETMON_HOSTNAME` | Optional env input used by the Docker entrypoint when rendering the Veriflier `hostname` config. Use a low-cardinality value such as `<region>.<vantage>` for process identity; do not include container IDs, release SHAs, ports, or random suffixes. |
| `STATSD_HOST_PATH` | Optional explicit Graphite host path. Leave empty to use the Veriflier hostname; set when metric grouping should differ from process identity. |
| `VERIFLIER_CONFIG_RENDER_MODE` | Optional. `always` (default) renders JSON every start from env. `missing` renders only if the target file is absent. `never` disables rendering and uses `VERIFLIER_CONFIG` or `config/veriflier.json`. |

## Run Jetmon

Jetmon needs MySQL-compatible database connectivity and (in production) at
least one reachable Veriflier. The simplest invocation against an
already-running database and Veriflier:

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
  --add-host=host.docker.internal:host-gateway \
  -e STATSD_ADDR=host.docker.internal:8125 \
  -e JETMON_HOSTNAME=jetmon-prod-1.dfw1.example.com \
  -e STATSD_HOST_PATH=dfw1.jetmon-prod-1 \
  -v "$(pwd)/jetmon-stats:/jetmon/stats" \
  ghcr.io/automattic/jetmon:latest
```

The single-container Jetmon example uses Docker's host-gateway mapping to reach
a host-local StatsD proxy without host networking. If no host-local StatsD
proxy is available, set `STATSD_ADDR=` so the entrypoint renders an empty
`"STATSD_ADDR"` config value for the smoke run. In Compose or production,
render `STATSD_ADDR` as the Compose StatsD service, the production host-local
StatsD proxy through `host.docker.internal`, or another approved UDP endpoint.

The entrypoint renders a generated JSON config from `config-sample.json` using
the env vars above, exports `JETMON_CONFIG` to that generated path, runs schema
setup, and starts the Monitor. In Docker, env values are the Compose source of
truth by default: changing them and recreating the container updates the
generated JSON. The Jetmon binary reads StatsD, database, and runtime settings
from that JSON config, not directly from the environment.

Exposed ports:

| Port | Purpose |
|---|---|
| `8080` | Operator dashboard |
| `8090` | Internal REST API and `/api/v1/health` |

Required env vars:

| Var | Notes |
|---|---|
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` | Template inputs for rendered JSON database config in local/dev and explicit-DSN deployments. Used when `DB_SERVER_MAP_PATH` is unset. |
| `DB_SERVER_MAP_PATH` | Optional template input for the rendered production path to synced `db-servers.php`; when set, the Monitor reads the `misc` dataset and builds separate read/write DB pools. Do not combine it with explicit `DB_HOST` / `DB_PORT` / `DB_USER` / `DB_PASSWORD` / `DB_NAME`. |
| `DB_SERVER_MAP_DATACENTER` | Recommended with `DB_SERVER_MAP_PATH`; render this as the host datacenter such as `dfw` or `dca` so local read replicas are preferred. |
| `DB_SERVER_MAP_ADDRESS` | `internet` (default, v1-compatible) or `internal`; use `internal` only when the container network can reach internal DB hostnames. |
| `VERIFLIER_AUTH_TOKEN`, `VERIFLIER_PORT` | Shared with each Veriflier. |
| `WPCOM_AUTH_TOKEN` | Set to `change_me` for non-WPCOM environments. |
| `WPCOM_NOTIFY_ENABLE` | Set to `false` for local, ad-hoc, and internal-only tests. Production Monitor rollout normally enables this after WPCOM parity gates pass. |
| `WPCOM_NOTIFY_MODE` | Optional. Defaults to `legacy`, which uses the v1-compatible client-certificate `/jetmon/` path. Use `modern` only for WPCOM endpoint/auth contract testing. |
| `WPCOM_NOTIFY_LEGACY_CERT_PATH`, `WPCOM_NOTIFY_LEGACY_KEY_PATH` | Required runtime secret paths when `WPCOM_NOTIFY_ENABLE=true` and `WPCOM_NOTIFY_MODE=legacy`. |
| `CHECK_TARGET_SAFETY_MODE` | Defaults to `public_only`, which keeps Monitor SSRF protections enabled. The only alternate value, `allow_private_for_tests`, is for isolated uptime-bench capacity labs with disposable synthetic rows and is rejected unless `WPCOM_NOTIFY_ENABLE=false`. Never set it for production rollout, customer data, or real alert paths. |
| `EMAIL_TRANSPORT` | `stub` for dev; `smtp` plus `SMTP_*` vars for real delivery. |
| `STATSD_ADDR` | Optional template input for the rendered JSON `STATSD_ADDR` config value. Local Compose and Veriflier production Compose render this as `statsd:8125`. For TeamCity Monitor production, render `STATSD_ADDR` as `host.docker.internal:8125` and add Docker's `host.docker.internal:host-gateway` mapping, or render it explicitly empty to disable StatsD. |
| `CONFIG_PROFILE` | Optional rendered config profile. Use `production` for production Monitor containers so startup defaults to schema validation instead of migration. Explicit config values still override profile defaults. |
| `SCHEMA_MANAGEMENT_MODE` | `migrate` applies pending migrations before service start; `validate` refuses startup unless the expected schema is already present and never applies DDL. Use `validate` in production after Systems has applied schema changes. |
| `HOSTNAME` / `JETMON_HOSTNAME` | Stable process identity. `HOSTNAME` is the rendered config key; `JETMON_HOSTNAME` is the Docker env input used by the entrypoint when rendering config. For Monitor production, use the real logical host name, for example `jetmon-prod-1.dfw1.example.com`; do not include container IDs, release SHAs, ports, or random suffixes. |
| `STATSD_HOST_PATH` | Explicit StatsD metric host path. For Monitor production, use the v1-compatible `<datacenter>.<node>` format derived by reversing the first two labels of the v1 hostname, for example `jetmon-prod-1.dfw1.example.com` -> `dfw1.jetmon-prod-1`. Leave empty only for local/dev fallback or an intentional dashboard series migration. |
| `JETMON_CONFIG_RENDER_MODE` | Optional. `always` (default) renders JSON every start from env. `missing` renders only if the target file is absent. `never` disables rendering and uses `JETMON_CONFIG` or `config/config.json`. |

Optional volume mounts:

| Path | Reason |
|---|---|
| `/jetmon/config` | Mount only when you want to manage `config.json` outside the container. Set `JETMON_CONFIG_RENDER_MODE=never` and `JETMON_CONFIG=/jetmon/config/config.json` so the entrypoint does not overwrite the mounted file. |
| `/jetmon/config-source` | Production-only mount for generated private files such as `db-servers.php`; mount read-only in the Monitor. In the recommended TeamCity rollout, the config-sync sidecar writes this path. |
| `/jetmon/stats` | Persist counters and the `jetmon2.pid` file used by `reload` / `drain`. Production TeamCity consumers should prefer `GET /api/v1/monitor/stats` or StatsD over host filesystem reads unless Systems explicitly approves a bind mount. |

For production TeamCity rollout and database server-map sync, see
[production-teamcity-rollout.md](production-teamcity-rollout.md).

## Production Config-Sync Sidecar

The config-sync sidecar image is for production docker-deploy only. It contains
`svn`, [../scripts/jetmon-config-update.sh](../scripts/jetmon-config-update.sh),
and [../scripts/jetmon-config-sync-loop.sh](../scripts/jetmon-config-sync-loop.sh).
It expects a secret `config-sync.env` file or equivalent environment injection
at runtime, syncs `db-servers.php` from SVN, and writes only the generated file
into the shared config-source path.

Do not bake `config-sync.env`, SVN credentials, or generated production
`db-servers.php` content into this image.

## Run Both Together

For an ad-hoc deploy that needs Jetmon plus a co-located Veriflier, the
following compose snippet uses the pulled images instead of the local builds in
`docker/docker-compose.yml`:

```yaml
services:
  veriflier:
    image: ghcr.io/automattic/veriflier:latest
    environment:
      VERIFLIER_AUTH_TOKEN: replace_me
      VERIFLIER_PORT: "7803"
      STATSD_ADDR: statsd:8125
      JETMON_HOSTNAME: local.veriflier
      STATSD_HOST_PATH: local.veriflier
    ports:
      - "7803:7803"

  statsd:
    image: graphiteapp/graphite-statsd
    ports:
      - "127.0.0.1:8088:80"
      - "127.0.0.1:8125:8125/udp"

  jetmon:
    image: ghcr.io/automattic/jetmon:latest
    depends_on: [veriflier, statsd]
    environment:
      DB_HOST: mysql.internal
      DB_PORT: "3306"
      DB_USER: jetmon
      DB_PASSWORD: replace_me
      DB_NAME: jetmon_db
      VERIFLIER_AUTH_TOKEN: replace_me
      VERIFLIER_PORT: "7803"
      WPCOM_AUTH_TOKEN: change_me
      WPCOM_NOTIFY_ENABLE: "false"
      EMAIL_TRANSPORT: stub
      STATSD_ADDR: statsd:8125
      JETMON_HOSTNAME: local.jetmon
      STATSD_HOST_PATH: local.jetmon
    ports:
      - "8080:8080"
      - "8090:8090"
    volumes:
      - ./jetmon-stats:/jetmon/stats
```

The database is intentionally not in this snippet; pre-built images are for
talking to an existing database. The StatsD service is shown for ad-hoc Compose
runs and Veriflier VPS deployments. TeamCity Monitor production should instead
render `STATSD_ADDR` as the existing host-local StatsD proxy and should not add
a StatsD/Graphite container to the Monitor stack. In bridge-networked
production containers, add `host.docker.internal:host-gateway` and render
`STATSD_ADDR` as `host.docker.internal:8125`. For the full local stack including
the database, Mailpit, and StatsD, keep using the build-from-source compose file
under `docker/`. For the VPS Veriflier production shape, see
[production-veriflier-compose.md](production-veriflier-compose.md). The
repo-provided Compose files mount a Graphite storage schema for Jetmon metrics
with `10s:6h, 1m:7d, 10m:5y` retention and include an optional
`metrics-smoke` profile for Veriflier StatsD-to-Graphite ingestion checks.

Runtime logs are written only to stdout/stderr for Docker or the deployment
platform to collect. The image does not create or maintain v1
`jetmon.log`/`status-change.log` files.

## Validate Config Inside The Container

```bash
docker run --rm \
  -e DB_HOST=mysql.internal -e DB_PORT=3306 -e DB_USER=jetmon \
  -e DB_PASSWORD=replace_me -e DB_NAME=jetmon_db \
  -e VERIFLIER_AUTH_TOKEN=replace_me \
  ghcr.io/automattic/jetmon:latest ./jetmon2 validate-config
```

The entrypoint renders a config first, then `validate-config` checks shape,
database connectivity, email transport mode, and Veriflier reachability. For a
stronger dependency smoke test, run `./jetmon2 schema validate` and
`./jetmon2 doctor --require-statsd` with the same environment.

## Reload And Drain

Both run via the PID file at `/jetmon/stats/jetmon2.pid`, so they only work
when `/jetmon/stats` is a writable volume:

```bash
docker exec jetmon ./jetmon2 reload   # SIGHUP — config reload
docker exec jetmon ./jetmon2 drain    # SIGINT — graceful shutdown
```

## Pin To A PR Build

To test the image built from PR #123 against your environment:

```bash
docker pull ghcr.io/automattic/jetmon:<short-sha>
```

Find the short SHA in the PR's checks tab under the `Build and publish Docker
images` workflow run summary. The PR must carry the `Docker Build` label
before the workflow runs.

## Troubleshooting

| Symptom | Check |
|---|---|
| `denied: requested access to the resource is denied` on pull | The package is still private — authenticate with `docker login ghcr.io` using a PAT with `read:packages`, or have a maintainer flip the package visibility. |
| Container starts but Jetmon exits with a database error | The rendered JSON `DB_HOST` is reachable from inside the container — remember `localhost` inside the container is not the host. Use the host IP, a docker network, or `host.docker.internal`. |
| `reload` / `drain` reports "no PID file" | Mount a writable volume at `/jetmon/stats`. The PID file lives at `/jetmon/stats/jetmon2.pid`. |
| Compose env changes do not take effect | Confirm `JETMON_CONFIG_RENDER_MODE` / `VERIFLIER_CONFIG_RENDER_MODE` is `always` or unset, then recreate the container. `missing` intentionally ignores env changes while the target JSON exists, and `never` disables rendering. |
| Jetmon cannot reach Veriflier | `VERIFLIER_AUTH_TOKEN` must match on both sides, and `VERIFLIER_PORT` (default `7803`) must be reachable from the Jetmon container. |

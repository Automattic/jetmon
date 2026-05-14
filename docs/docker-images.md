# Running Jetmon And Veriflier From GHCR

The CI workflow `.github/workflows/docker-publish.yml` publishes two images to
GitHub Container Registry:

| Image | Source Dockerfile |
|---|---|
| `ghcr.io/automattic/jetmon` | `docker/Dockerfile_jetmon` |
| `ghcr.io/automattic/veriflier` | `docker/Dockerfile_veriflier` |

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

The entrypoint renders `config/veriflier.json` from `veriflier-sample.json` on
first start using the env vars above. Health check: `curl http://localhost:7803/v2/status`
should return `{"status":"OK",...}`.

Required env vars:

| Var | Notes |
|---|---|
| `VERIFLIER_AUTH_TOKEN` | Must match the value Jetmon uses to call this verifier. |
| `VERIFLIER_PORT` | Defaults to `7803`. |
| `VERIFLIER_ENABLE_LEGACY_HTTP` | Optional. Defaults to `false`; set to `true` only for lab/emergency compatibility with `veriflier2`'s legacy HTTP `/check` and `/status` endpoints. |

## Run Jetmon

Jetmon needs MySQL connectivity and (in production) at least one reachable
Veriflier. The simplest invocation against an already-running MySQL and
Veriflier:

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
  -e EMAIL_TRANSPORT=stub \
  -v "$(pwd)/jetmon-logs:/jetmon/logs" \
  -v "$(pwd)/jetmon-stats:/jetmon/stats" \
  ghcr.io/automattic/jetmon:latest
```

The entrypoint runs `./jetmon2 migrate` before starting the monitor — migrations
are embedded and additive. The first run renders `config/config.json` from
`config-sample.json` using the env vars above; mount a real
`/jetmon/config/config.json` to override the rendered defaults.

Exposed ports:

| Port | Purpose |
|---|---|
| `8080` | Operator dashboard |
| `8090` | Internal REST API and `/api/v1/health` |

Required env vars:

| Var | Notes |
|---|---|
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` | MySQL connection. |
| `VERIFLIER_AUTH_TOKEN`, `VERIFLIER_PORT` | Shared with each Veriflier. |
| `WPCOM_AUTH_TOKEN` | Set to `change_me` for non-WPCOM environments. |
| `EMAIL_TRANSPORT` | `stub` for dev; `smtp` plus `SMTP_*` vars for real delivery. |

Optional volume mounts:

| Path | Reason |
|---|---|
| `/jetmon/config` | Mount when you want to manage `config.json` outside the container instead of relying on env-driven rendering. |
| `/jetmon/logs` | Persist `jetmon.log` and `status-change.log`. |
| `/jetmon/stats` | Persist counters and the `jetmon2.pid` file used by `reload` / `drain`. |

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
    ports:
      - "7803:7803"

  jetmon:
    image: ghcr.io/automattic/jetmon:latest
    depends_on: [veriflier]
    environment:
      DB_HOST: mysql.internal
      DB_PORT: "3306"
      DB_USER: jetmon
      DB_PASSWORD: replace_me
      DB_NAME: jetmon_db
      VERIFLIER_AUTH_TOKEN: replace_me
      VERIFLIER_PORT: "7803"
      WPCOM_AUTH_TOKEN: change_me
      EMAIL_TRANSPORT: stub
    ports:
      - "8080:8080"
      - "8090:8090"
    volumes:
      - ./jetmon-logs:/jetmon/logs
      - ./jetmon-stats:/jetmon/stats
```

MySQL is intentionally not in this snippet — pre-built images are for talking
to an existing database. For the full local stack including MySQL, Mailpit, and
StatsD, keep using the build-from-source compose file under `docker/`.

## Validate Config Inside The Container

```bash
docker run --rm \
  -e DB_HOST=mysql.internal -e DB_PORT=3306 -e DB_USER=jetmon \
  -e DB_PASSWORD=replace_me -e DB_NAME=jetmon_db \
  -e VERIFLIER_AUTH_TOKEN=replace_me \
  ghcr.io/automattic/jetmon:latest ./jetmon2 validate-config
```

The entrypoint renders a config first, then `validate-config` checks shape,
MySQL connectivity, email transport mode, and Veriflier reachability.

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
| Container starts but Jetmon exits with a MySQL error | `DB_HOST` is reachable from inside the container — remember `localhost` inside the container is not the host. Use the host IP, a docker network, or `host.docker.internal`. |
| `reload` / `drain` reports "no PID file" | Mount a writable volume at `/jetmon/stats`. The PID file lives at `/jetmon/stats/jetmon2.pid`. |
| Config changes do not persist across container restarts | Either mount `/jetmon/config` and edit the file directly, or rely on env vars — the rendered `config.json` is rebuilt from env vars on every fresh start. |
| Jetmon cannot reach Veriflier | `VERIFLIER_AUTH_TOKEN` must match on both sides, and `VERIFLIER_PORT` (default `7803`) must be reachable from the Jetmon container. |

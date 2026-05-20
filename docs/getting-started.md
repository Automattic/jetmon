# Getting Started

This guide is for local development and smoke testing. Production rollout steps
live in [operations-guide.md](operations-guide.md).

## Requirements

- Go 1.26.3 or newer
- Docker and Docker Compose
- `make`

The Docker environment provides a local database, StatsD/Graphite, Mailpit, the
monitor, the Go Veriflier, and the API failure fixture. `docker/.env-sample`
defaults to `JETMON_DB_IMAGE=mariadb:11.4`, matching the current production
database family. If you change this value for compatibility testing, recreate
the database volume before comparing behavior across engines. Existing local
volumes created with the old `mysql:8.0` default should be recreated with
`docker compose down -v` before first starting the MariaDB default.

## Start Docker

```bash
cd docker
cp .env-sample .env
docker compose up --build -d
```

Useful follow-up commands:

```bash
docker compose logs -f jetmon
docker compose exec jetmon bash
docker compose down
docker compose down --remove-orphans
```

## Local Database Selection

Local testing does not use the production SVN `db-servers.php` sync path. The
Monitor reads its database connection from `DB_HOST`, `DB_PORT`, `DB_USER`,
`DB_PASSWORD`, and `DB_NAME`.

In the default Docker Compose stack, those values point at the local
`mysqldb` service:

```yaml
DB_HOST: mysqldb
DB_PORT: "3306"
```

Use `docker/.env` to change the local database image, database name, user, and
password. If you need the Monitor container to connect to a specific external
database instead of the Compose `mysqldb` service, add a local Compose override
that changes the `jetmon.environment` `DB_*` values, or run the pre-built image
directly with explicit `DB_*` environment variables as shown in
[docker-images.md](docker-images.md). The SVN config-sync sidecar is only for
production rollout planning and is not required for local smoke tests.

Production-style DB server-map testing is available by setting
`DB_SERVER_MAP_PATH` to a synced or synthetic `db-servers.php`. In that mode,
Jetmon reads the `misc` dataset, uses the write-master row for writes, uses
read-enabled rows for reads, and hot-reloads changed connection details on the
`DB_CONFIG_UPDATES_MIN` cadence. Keep this unset for normal local development.
When testing this mode, use `GET /api/v1/monitor/db-config` or the host
dashboard `db-config` dependency to confirm the next reload check, last changed
map observed, and last successful hot reload.

## Local StatsD

The default Docker Compose stack runs a local `statsd` service backed by the
`graphiteapp/graphite-statsd` image. Monitor and Veriflier containers send UDP
metrics to `STATSD_ADDR=statsd:8125` by default in Compose. Set `STATSD_ADDR`
in `docker/.env` if you want both services to send to a different StatsD
endpoint, or set it to an empty value to disable StatsD for a smoke test. Leave
`JETMON_HOSTNAME` and `STATSD_HOST_PATH` unset locally unless you need process
identity or metrics to land under a specific Graphite path while testing
dashboard changes.

Mailpit captures local alert-contact email. Open it at
`http://localhost:8025` by default, or at the `BIND_ADDR` /
`MAILPIT_HOST_PORT` values from `docker/.env`.

The local API port also binds to loopback by default. Set
`API_BIND_ADDR=0.0.0.0` only when the Docker host is on a trusted network and
remote API access is intentional.

## Local WPCOM Notifications

The default Docker Compose stack sets `WPCOM_NOTIFY_ENABLE=false` so local
checks cannot contact WPCOM. It still renders `WPCOM_NOTIFY_MODE=legacy` by
default so local config shape matches the production rollout posture.

Production Monitor rollout should use `WPCOM_NOTIFY_MODE=legacy`, which is the
config default outside the local Docker override. That mode preserves the v1
client-certificate `/jetmon/?data=...` notification contract. Set
`WPCOM_NOTIFY_MODE=modern` explicitly only for WPCOM endpoint/auth contract
testing until WPCOM signs off.

## Build And Test

From the repository root:

```bash
make all
make test
make test-race
make lint
```

Build individual binaries when the full build is not needed:

```bash
make build
make build-deliverer
make build-veriflier
```

If `go` is not on `PATH`, the Makefile falls back to `/usr/local/go/bin/go`
when present. Override with `make GO=/path/to/go ...` for other layouts.

## Validate Config

```bash
./bin/jetmon2 validate-config
```

Validation checks required keys, value ranges, database connectivity, legacy
projection mode, email transport mode, and configured Verifliers. Veriflier
reachability is reported as operational context rather than a hard validation
failure.

The local Veriflier serves the v2 status endpoint by default:

```bash
curl http://127.0.0.1:7803/v2/status
```

The v2 response includes supported protocols, local `vantage.id`, serving
`agent.id`, and executor capacity.

`veriflier2` can also expose legacy-compatible HTTP `/check` and `/status`
endpoints for lab or emergency compatibility testing by setting
`VERIFLIER_ENABLE_LEGACY_HTTP=true`, but production v2 Verifliers should remain
v2-only unless there is an explicit rollout need.

To inspect the local Veriflier discovery registry and monitor-collected agent
telemetry without exposing auth token values:

```bash
./bin/jetmon2 verifliers discovery-report --output=text
```

## API CLI Smoke

Build the binary, create a local API key, and point the CLI at the exposed API:

```bash
make build
make api-cli-token-create

export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token

./bin/jetmon2 api health --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api request --pretty GET /api/v1/monitor/stats
./bin/jetmon2 api request GET '/api/v1/monitor/stats?file=totals'
./bin/jetmon2 api commands --output table
./bin/jetmon2 api sites list --output table
```

`/api/v1/monitor/stats` is the API migration path for consumers that used to
read `stats/sitespersec`, `stats/sitesqueue`, or `stats/totals` directly from a
host filesystem. The JSON response contains parsed counters plus the exact
legacy file bodies, and the `?file=` form returns one legacy body as
`text/plain`. It requires a normal read-scope API key.

Run the standard smoke sequence:

```bash
make api-cli-smoke
```

Run the fuller live validation pass against the guide examples, local failure
fixture, and webhook delivery/signature flow. Use the public-fixture target when
you want Monitor target safety to stay enabled during the deterministic failure
checks:

```bash
make api-cli-public-fixture-validate
```

Set `API_VALIDATE_SKIP_WEBHOOK=1` for a shorter pass that avoids the outbound
webhook worker.

Use these helper targets to manage local rehearsal tokens:

```bash
make api-cli-token-list
API_CLI_TOKEN_ID=<id> make api-cli-token-revoke
```

## Simulate A Failure

The Docker Compose environment includes `api-fixture`, a deterministic local
site fixture. Jetmon containers reach it at `http://api-fixture:8091` and
`https://api-fixture:8443`; the host can inspect it at
`http://localhost:18091` and `https://localhost:18443` by default.
Target-safety-enabled checks intentionally block that Docker hostname as a
private target, so fixture-backed failure validation should use
`make api-cli-public-fixture-validate` or pass a public-looking Docker-internal
fixture URL explicitly.

The fixture exposes endpoints for response codes, redirects, keyword mismatch,
slow responses, TLS, and webhook capture.

```bash
./bin/jetmon2 api sites bulk-add --count 3 --batch local-smoke --dry-run --pretty
./bin/jetmon2 api smoke --batch local-smoke --pretty
./bin/jetmon2 api sites simulate-failure \
  --batch local-smoke \
  --mode http-500 \
  --wait 30s \
  --expect-event-state 'Seems Down' \
  --expect-transition-reason opened \
  --pretty
./bin/jetmon2 api sites cleanup --batch local-smoke --count 3 --output table
```

Set `--fixture-url=off` to force public endpoint fallback behavior.

## Add Manual Test Sites

```bash
cd docker
docker compose exec mysqldb mysql -u jetmon -pjetmon_dev_password jetmon_db
```

```sql
INSERT INTO jetpack_monitor_sites
  (blog_id, bucket_no, monitor_url, monitor_active, site_status)
VALUES
  (1, 0, 'https://wordpress.com', 1, 1),
  (2, 0, 'https://httpstat.us/200', 1, 1),
  (3, 0, 'https://httpstat.us/500', 1, 1),
  (4, 0, 'https://httpstat.us/200?sleep=15000', 1, 1);
```

## Import Tenant Mapping

Gateway-routed site reads and writes are scoped through
`jetpack_monitor_site_tenants`. Import the gateway or customer source of truth before
customer traffic depends on Jetmon-side tenant enforcement:

```bash
./bin/jetmon2 site-tenants import --file site-tenants.csv --dry-run
./bin/jetmon2 site-tenants import --file site-tenants.csv --source gateway
```

The CSV format is `tenant_id,blog_id` with an optional header row. Imports
upsert mappings and skip duplicate input rows; they do not delete missing
mappings.

# Getting Started

This guide is for local development and smoke testing. Production rollout steps
live in [operations-guide.md](operations-guide.md).

## Requirements

- Go 1.26.3 or newer
- Docker and Docker Compose
- `make`

The Docker environment provides MariaDB, StatsD/Graphite, Mailpit, the Monitor,
the Go Veriflier, and the API failure fixture. The default database image is
`mariadb:11.4`, matching the current production database family. If an old local
volume was created with the previous MySQL default, reset it before comparing
behavior:

```bash
cd docker
docker compose down -v
```

## Start Docker

```bash
cd docker
cp .env-sample .env
docker compose up --build -d
```

Useful commands:

```bash
docker compose logs -f jetmon
docker compose exec jetmon bash
docker compose down
docker compose down --remove-orphans
```

## Build And Test

From the repository root:

```bash
make all
make test
make test-race
make lint
```

Build individual binaries when that is faster:

```bash
make build
make build-deliverer
make build-veriflier
```

If `go` is not on `PATH`, the Makefile falls back to `/usr/local/go/bin/go`
when present. Override with `make GO=/path/to/go ...` for other layouts.

## Validate The Local Stack

```bash
./bin/jetmon2 validate-config
curl http://127.0.0.1:7803/v2/status
./bin/jetmon2 verifliers discovery-report --output=text
```

Config validation checks required keys, ranges, DB connectivity, projection
mode, email transport, and configured Verifliers. Veriflier reachability is
reported as operational context rather than a hard validation failure.

The Veriflier status response includes supported protocols, `vantage.id`,
`agent.id`, and executor capacity. Production v2 Verifliers should remain
v2-only; set `VERIFLIER_ENABLE_LEGACY_HTTP=true` only for lab or emergency
compatibility testing.

## API CLI Smoke

Build the binary, create a local API key, and point the CLI at the local API:

```bash
make build
make api-cli-token-create

export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token

./bin/jetmon2 api health --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api request --pretty GET /api/v1/monitor/stats
./bin/jetmon2 api commands --output table
./bin/jetmon2 api sites list --output table
```

Run the standard smoke sequence:

```bash
make api-cli-smoke
```

Run the fuller live validation pass against guide examples, the local failure
fixture, and webhook delivery/signature flow:

```bash
make api-cli-public-fixture-validate
```

Set `API_VALIDATE_SKIP_WEBHOOK=1` for a shorter pass that avoids the outbound
webhook worker.

Token helpers:

```bash
make api-cli-token-list
API_CLI_TOKEN_ID=<id> make api-cli-token-revoke
```

`/api/v1/monitor/stats` replaces direct filesystem reads of
`stats/sitespersec`, `stats/sitesqueue`, and `stats/totals`. JSON responses
include parsed counters plus exact legacy file bodies; `?file=totals` returns a
single legacy body as `text/plain`.

## Simulate A Failure

The Docker stack includes `api-fixture`, a deterministic local site fixture.
Containers reach it at `http://api-fixture:8091` and
`https://api-fixture:8443`; the host can inspect it at
`http://localhost:18091` and `https://localhost:18443`.

The fixture exposes response-code, redirect, keyword mismatch, slow-response,
TLS, and webhook-capture paths. Target-safety-enabled checks intentionally block
private Docker hostnames, so use `make api-cli-public-fixture-validate` or pass
a public-looking Docker-internal fixture URL for deterministic failure checks.

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

## Local Services

Database:

- normal local testing uses rendered JSON keys `DB_HOST`, `DB_PORT`,
  `DB_USER`, `DB_PASSWORD`, and `DB_NAME`
- the default Compose stack points those keys at `mysqldb`
- change local DB image/name/user/password through `docker/.env`
- use a Compose override for an external local DB
- set `JETMON_CONFIG_RENDER_MODE=never` only when mounting a hand-managed JSON
  config

Production-style `db-servers.php` testing is available by setting
`DB_SERVER_MAP_PATH` in JSON config. Keep it unset for normal local development
and do not set explicit `DB_HOST` / `DB_PORT` / `DB_USER` / `DB_PASSWORD` /
`DB_NAME` values at the same time. Use `GET /api/v1/monitor/db-config` or the
dashboard `db-config` dependency to confirm hot-reload state.

StatsD and email:

- Compose runs `graphiteapp/graphite-statsd` as `statsd`
- Monitor and Veriflier default to `"STATSD_ADDR": "statsd:8125"`
- set `STATSD_ADDR` in `docker/.env` to override or empty it to disable StatsD
- leave `JETMON_HOSTNAME` and `STATSD_HOST_PATH` unset locally unless testing a
  specific Graphite path
- Mailpit captures local alert-contact email at `http://localhost:8025` by
  default

API and WPCOM:

- local API binds to loopback by default
- set `API_BIND_ADDR=0.0.0.0` only on a trusted network when remote API access
  is intentional
- local Compose sets `WPCOM_NOTIFY_ENABLE=false`, so checks cannot contact WPCOM
- local Compose still renders `WPCOM_NOTIFY_MODE=legacy` so config shape
  matches rollout posture
- set `WPCOM_NOTIFY_MODE=modern` only for explicit WPCOM endpoint/auth contract
  testing

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
`jetpack_monitor_site_tenants`. Import the gateway or customer source of truth
before customer traffic depends on Jetmon-side tenant enforcement:

```bash
./bin/jetmon2 site-tenants import --file site-tenants.csv --dry-run
./bin/jetmon2 site-tenants import --file site-tenants.csv --source gateway
```

The CSV format is `tenant_id,blog_id` with an optional header row. Imports
upsert mappings and skip duplicate input rows; they do not delete missing
mappings.

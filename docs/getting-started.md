# Getting Started

This guide is for local development and smoke testing. Production rollout steps
live in [operations-guide.md](operations-guide.md).

## Requirements

- Go 1.22 or newer
- Docker and Docker Compose
- `make`

The Docker environment provides MySQL, StatsD/Graphite, Mailpit, the monitor,
the Go Veriflier, and the API failure fixture.

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

Mailpit captures local alert-contact email. Open it at
`http://localhost:8025` by default, or at the `BIND_ADDR` /
`MAILPIT_HOST_PORT` values from `docker/.env`.

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

Validation checks required keys, value ranges, MySQL connectivity, legacy
projection mode, email transport mode, and configured Verifliers. Veriflier
reachability is reported as operational context rather than a hard validation
failure.

## API CLI Smoke

Build the binary, create a local API key, and point the CLI at the exposed API:

```bash
make build
make api-cli-token-create

export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token

./bin/jetmon2 api health --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api commands --output table
./bin/jetmon2 api sites list --output table
```

Run the standard smoke sequence:

```bash
make api-cli-smoke
```

Run the fuller live validation pass against the guide examples, local failure
fixture, and webhook delivery/signature flow:

```bash
make api-cli-validate
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
`jetmon_site_tenants`. Import the gateway or customer source of truth before
customer traffic depends on Jetmon-side tenant enforcement:

```bash
./bin/jetmon2 site-tenants import --file site-tenants.csv --dry-run
./bin/jetmon2 site-tenants import --file site-tenants.csv --source gateway
```

The CSV format is `tenant_id,blog_id` with an optional header row. Imports
upsert mappings and skip duplicate input rows; they do not delete missing
mappings.

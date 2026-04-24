# Getting Started

Practical onboarding guide for running Jetmon 2 and Veriflier 2 locally.

## 1. Prerequisites

- macOS/Linux shell with `bash`
- Go 1.22+
- Docker + Docker Compose (for containerized setup)
- MySQL 8.0 (only needed for bare metal)
- `curl` and `jq` (recommended for API verification)

## 2. Quick Start (Docker)

From repo root (if you are already in `docker/`, skip the `cd docker` line):

```bash
cd docker
cp .env-sample .env
# edit .env and set at least:
# - WPCOM_JETMON_AUTH_TOKEN
# - VERIFLIER_AUTH_TOKEN
docker compose up --build -d
```

Check services:

```bash
docker compose ps
docker compose logs -f jetmon
```

Important: Docker entrypoint `docker/run-jetmon.sh` runs `./jetmon2 migrate` automatically before starting Jetmon.

Quick verification:

```bash
curl -s http://localhost:8080/api/health | jq .
curl -s http://localhost:8080/api/state | jq .
curl -s http://localhost:8080/ | head -n 5
```

Stop:

```bash
docker compose down
```

Reset DB volume (fresh start):

```bash
docker compose down -v
```

## 3. Bare Metal Setup

From repo root:

```bash
cp config/config-sample.json config/config.json
cp veriflier2/config/veriflier-sample.json veriflier2/config/veriflier.json
```

Set DB env vars (used by Jetmon for DB connection):

```bash
export DB_HOST=127.0.0.1
export DB_PORT=3306
export DB_USER=root
export DB_PASSWORD=123456
export DB_NAME=jetmon_db
```

Build binaries (repo convention):

```bash
make build
make build-veriflier
```

Equivalent direct build commands:

```bash
go build -o jetmon2 ./cmd/jetmon2/
go build -o veriflier2 ./veriflier2/cmd/
```

Run Veriflier (terminal 1):

```bash
VERIFLIER_CONFIG=veriflier2/config/veriflier.json ./veriflier2
```

Run Jetmon (terminal 2):

```bash
./jetmon2
```

Optional config validation:

```bash
./jetmon2 validate-config
```

## 4. Database Setup and Migrations

Create database (bare metal):

```bash
mysql -u root -p -e "CREATE DATABASE IF NOT EXISTS jetmon_db CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
```

Apply migrations (idempotent):

```bash
./jetmon2 migrate
```

Verify migrations ran:

```bash
mysql -u root -p -D jetmon_db -e "SELECT id, applied_at FROM jetmon_schema_migrations ORDER BY id;"
```

Seed a test site:

```bash
mysql -u root -p -D jetmon_db -e "INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval) VALUES (1,0,'https://wordpress.com',1,1,5);"
```

Note: `migrations/001_jetmon2.sql` is reference SQL; standard flow is `./jetmon2 migrate`.

## 5. API Configuration and Authentication

Jetmon serves dashboard + API on the same `DASHBOARD_PORT`.

Public routes (no auth):

- `/`
- `/events`
- `/api/state`
- `/api/health`

Authenticated API base:

- `/api/v1/...` requires `Authorization: Bearer <token>`

Token source behavior:

- `API_TOKENS` is the only source of allowed bearer tokens for `/api/v1`.
- If `API_TOKENS` is empty, `/api/v1` auth is disabled (local dev convenience).

Rate limit config (token bucket, per client IP):

- `API_RATE_LIMIT_RPS`
- `API_RATE_LIMIT_BURST`

`config/config-sample.json` may not include API keys yet. Add them manually in `config/config.json`:

```json
{
  "AUTH_TOKEN": "wpcom-token",
  "API_TOKENS": ["local-dev-token-1", "local-dev-token-2"],
  "API_RATE_LIMIT_RPS": 20,
  "API_RATE_LIMIT_BURST": 40,
  "DASHBOARD_PORT": 8080
}
```

## 6. Common API Calls

Set variables:

```bash
BASE_URL="http://localhost:8080"
TOKEN="local-dev-token-1"
```

List sites:

```bash
curl -s -H "Authorization: Bearer ${TOKEN}" \
  "${BASE_URL}/api/v1/sites?limit=20&offset=0" | jq .
```

Create site (POST):

```bash
curl -s -X POST "${BASE_URL}/api/v1/sites" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"blog_id":12345,"monitor_url":"https://example.com"}' | jq .
```

Duplicate behavior: POST is rejected (`409 duplicate_site`) for an existing `(blog_id, monitor_url)` pair.

Patch site (PATCH):

```bash
curl -s -X PATCH "${BASE_URL}/api/v1/sites/1" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{"monitor_active":false,"check_interval":10}' | jq .
```

PATCH rule: `monitor_url` cannot be changed via PATCH.

Delete site (DELETE):

```bash
curl -i -X DELETE "${BASE_URL}/api/v1/sites/1" \
  -H "Authorization: Bearer ${TOKEN}"
```

DELETE behavior: hard delete from `jetpack_monitor_sites`.

Site events:

```bash
curl -s -H "Authorization: Bearer ${TOKEN}" \
  "${BASE_URL}/api/v1/sites/1/events?limit=50&offset=0" | jq .
```

## 7. Troubleshooting

- `API` returns `401 unauthorized`: verify `Authorization: Bearer <token>` and ensure the token is in `API_TOKENS`.
- `/api/v1` unexpectedly allows requests without auth: this happens when `API_TOKENS` is empty.
- `429 rate_limited`: increase `API_RATE_LIMIT_RPS`/`API_RATE_LIMIT_BURST` for local testing.
- `db connect` errors: verify `DB_HOST/DB_PORT/DB_USER/DB_PASSWORD/DB_NAME` env vars and MySQL availability.
- `DB_UPDATES_ENABLE is true but JETMON_UNSAFE_DB_UPDATES=1 is not set`: either disable `DB_UPDATES_ENABLE` or export `JETMON_UNSAFE_DB_UPDATES=1` for local-only testing.
- Docker config not regenerating: remove `config/config.json` (or edit it directly) because Docker only auto-generates it when missing.

## 8. Next Steps

- Run tests: `go test ./...` and `go test -race ./...`
- Inspect live runtime state: `./jetmon2 status`
- Query audit trail: `./jetmon2 audit --blog-id 12345 --since 2h`
- Enable debug profiling if needed: set `DEBUG_PORT` and open `http://localhost:6060/debug/pprof/`

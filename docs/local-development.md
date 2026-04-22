# Local development

This guide is the fastest way to get Jetmon running locally for development.

## Prerequisites

- Go 1.22+
- Docker Desktop (or Docker Engine) with Compose
- Git

Optional but useful:
- `make`
- `curl`

## Quick start (Docker recommended)

Use this path for first run and integration testing.

```bash
git clone <repo-url>
cd jetmon
cp docker/.env-sample docker/.env
docker compose -f docker/docker-compose.yml up --build -d
docker compose -f docker/docker-compose.yml logs -f jetmon
```

Stop and clean up:

```bash
docker compose -f docker/docker-compose.yml down
docker compose -f docker/docker-compose.yml down -v
```

## Quick start (native Go)

Use this path for fast local iteration when you already have dependencies available.

```bash
git clone <repo-url>
cd jetmon
cp config/config-sample.json config/config.json
go build ./cmd/jetmon2/
go build ./veriflier2/
./jetmon2 validate-config
./jetmon2
```

## Config setup notes

- Main monitor config: `config/config.json` (copy from `config/config-sample.json`).
- Veriflier config: `veriflier2/config/veriflier.json` (copy from sample if needed in your flow).
- In Docker flows, config files can be generated from samples plus `docker/.env`.
- Send `SIGHUP` (or run `./jetmon2 reload`) to reload config without restart.
- `DB_UPDATES_ENABLE` is guarded; if enabled, set `JETMON_UNSAFE_DB_UPDATES=1` in environment.
- Current branch note: Monitor<->Veriflier transport is JSON-over-HTTP placeholder. Planned path is proto generation and gRPC transport swap.

## Canonical build/test/run commands

```bash
# Build
go build ./cmd/jetmon2/
go build ./veriflier2/

# Test
go test ./...
go test -race ./...

# Validate and run
./jetmon2 validate-config
./jetmon2 status
./jetmon2

# Useful runtime commands
./jetmon2 migrate
./jetmon2 reload
./jetmon2 drain
./jetmon2 audit --blog-id 12345 --since 2h
```

## Verification checklist

- `go test ./...` passes.
- `./jetmon2 validate-config` passes.
- Service starts and logs are written to `logs/jetmon.log`.
- `./jetmon2 status` reports expected health.
- Stats files update in `stats/` (`sitespersec`, `sitesqueue`, `totals`).
- If Docker is used, `docker compose -f docker/docker-compose.yml ps` shows healthy services.

## Troubleshooting

- Config load errors: re-copy sample config and re-apply minimal local edits.
- MySQL connection failures: verify DB host/port/credentials and container health.
- Veriflier unreachable: check configured endpoint and auth token; confirm `/status` responds.
- No site updates: check `DB_UPDATES_ENABLE` and `JETMON_UNSAFE_DB_UPDATES` guard pairing.
- Port conflicts (`DASHBOARD_PORT`, `DEBUG_PORT`): change local ports in config.
- High memory or stuck workers: inspect pprof endpoint at `http://localhost:<DEBUG_PORT>/debug/pprof/`.

## In-scope vs deferred

In scope now:
- Single-binary Jetmon monitor in Go.
- Go veriflier binary.
- Monitor<->Veriflier JSON-over-HTTP transport currently in use.

Deferred / planned:
- gRPC stub generation and transport swap after proto workflow is finalized.

## Further reading

- `README.md`
- `PROJECT.md`
- `ARCHITECTURE.md`
- `TAXONOMY.md`
- `EVENTS.md`
- `config/config.readme`

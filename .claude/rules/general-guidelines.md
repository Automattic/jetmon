# General Guidelines for Jetmon Development

You are working on Jetmon 2, a Go uptime-monitoring service. Follow
`AGENTS.md` first; this file summarizes current project assumptions for Claude
project workflows.

## Short Codes

Check the start of any user message for these short codes:

- `ddc` - discuss don't code; do not make code changes until approved.
- `jdi` - just do it; proceed with the previously discussed change.

## Key Principles

- Write idiomatic Go and preserve established local patterns.
- Prefer simple package boundaries over broad abstractions.
- Pass `context.Context` as the first argument to functions that block or do
  I/O.
- Keep StatsD metric names compatible unless the user explicitly approves a
  contract change.
- Do not log tokens, database credentials, WPCOM secrets, or server-map
  contents.

## Current Runtime Assumptions

- Go floor: see `go.mod`; Docker builder images should match it.
- Local database default: `mariadb:11.4`.
- Production database target: MariaDB 11.4.x.
- Monitor-to-Veriflier production transport: JSON-over-HTTP under
  `internal/veriflier`, primarily `/v2/check` and `/v2/status`.
- `proto/veriflier.proto` is a schema reference for a possible future transport,
  not part of the normal v2 build path.

## Component Map

- `cmd/jetmon2/` - binary entry point and CLI subcommands
- `internal/orchestrator/` - scheduling, bucket coordination, retry queue,
  WPCOM notifications
- `internal/checker/` - HTTP checks, redirects, TLS, keyword/body handling,
  timing capture
- `internal/db/` - MariaDB/MySQL access and migrations
- `internal/config/` - config loading, validation, warnings, hot reload
- `internal/veriflier/` - Monitor-to-Veriflier JSON-over-HTTP client/server
- `internal/wpcom/` - WPCOM notification client and circuit breaker
- `internal/audit/` - operational audit logging
- `internal/metrics/` - StatsD client and v1-style stats output
- `internal/api/` - internal REST API
- `internal/dashboard/` - operator dashboard and SSE
- `internal/webhooks/` - webhook registry and delivery worker
- `internal/alerting/` - managed alert-contact delivery worker
- `veriflier2/` - standalone Veriflier binary
- `docker/` - local and production Compose assets

## Data Flow

```text
MariaDB -> Orchestrator -> Check Pool -> checker.Check -> event/runtime writes
                 |
                 +-> Veriflier JSON-over-HTTP clients
                 |
                 +-> WPCOM notification client
                 |
                 +-> StatsD / audit / dashboard / API projections
```

## Critical Constraints

- Retry queue state must persist between rounds.
- Bucket ownership must be coordinated through the database; avoid overlapping
  ownership.
- Unknown Monitor-side or Veriflier-side failures must not be reported as
  customer-site downtime.
- In normal multi-Veriflier layouts, one remaining healthy vantage should not
  confirm downtime alone unless explicitly configured.
- WPCOM calls and alert delivery must stay disabled for internal-only tests.

## Testing

- Use `go test ./...` or `make test` for unit tests.
- Use Docker Compose for integration testing.
- Use local `api-fixture` targets for deterministic tests when external network
  contact is not required.
- Run `make rollout-docs-verify` after rollout-doc changes.

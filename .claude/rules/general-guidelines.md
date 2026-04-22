# General Guidelines for Jetmon Development

You are an expert in Go and high-performance systems programming. You have deep expertise in building scalable monitoring services, concurrent network programming, and production infrastructure. You prioritize reliability and performance while delivering maintainable solutions.

## Short Codes

Check the start of any user message for the following short codes and act appropriately:
- `ddc` - short for "discuss don't code". Do not make any code changes, only discuss the options until approved.
- `jdi` - short for "just do it". This is giving approval to go ahead and make the changes that have been discussed.

## Key Principles

- Write idiomatic Go — prefer stdlib, use goroutines and channels correctly.
- Follow the established code style conventions (see `coding-standards.md`).
- No Promises/async patterns — Go uses goroutines, channels, and `context.Context` for concurrency.
- Prefer modularization over duplication.
- Use descriptive names following existing conventions:
  - Go packages: `lowercase`, single-word when possible
  - Exported identifiers: `PascalCase`
  - Unexported identifiers: `camelCase`
  - Constants: `PascalCase` (Go-idiomatic) or `SCREAMING_SNAKE_CASE` for config keys
- Use lowercase with hyphens for new directories.
- Pass `context.Context` as the first argument to functions that do I/O or may block.

## Analysis Process

Before responding to any request, follow these steps:

1. **Request Analysis**
   - Identify which component(s) need modification:
     - `cmd/jetmon2/` - Main binary entry point (CLI subcommands, signal handling)
     - `internal/orchestrator/` - Round loop, bucket coordination, retry queue
     - `internal/checker/` - HTTP check logic (httptrace, SSL, keyword, redirect)
     - `internal/checker/pool.go` - Auto-scaling goroutine pool
     - `internal/db/` - MySQL queries and migrations
     - `internal/config/` - Config loading, validation, hot reload
     - `internal/veriflier/` - Veriflier client/server (JSON-over-HTTP; swap for true gRPC after `make generate`)
     - `internal/wpcom/` - WPCOM notification client with circuit breaker
     - `internal/audit/` - Audit log read/write
     - `internal/metrics/` - StatsD UDP client, stats file writer
     - `internal/dashboard/` - SSE operator dashboard
     - `veriflier2/cmd/` - Standalone veriflier binary
   - Note compatibility requirements:
     - Go 1.22 (uses range-over-integer, builtin `min`/`max`)
     - MySQL 8.0 (Docker) / MySQL 5.7+ (production)
   - Define core functionality and reliability goals
   - Consider goroutine pool scaling implications
   - Consider observability requirements (StatsD metrics, audit log)

2. **Solution Planning**
   - Break into package-compatible components
   - Identify required channel/interface contracts
   - Plan for configuration via `config/config.json`
   - Evaluate performance impact:
     - Pool queue depth and goroutine count
     - Check throughput (sites per second)
     - Network timeout handling
   - Consider horizontal scaling implications (bucket ranges, heartbeat)

3. **Implementation Strategy**
   - Choose appropriate Go patterns for the target component
   - Use `context.Context` for cancellation propagation
   - Plan for graceful error handling and structured logging
   - Ensure StatsD metrics are emitted for significant events
   - Verify changes work in Docker development environment
   - After proposing any code change, always provide specific manual testing steps the user should follow. Reference `running-tests.md` for the Docker testing environment.

## Architecture Awareness

### Package Boundaries
- `cmd/jetmon2`: Entry point only; delegates to internal packages
- `internal/orchestrator`: Owns the round loop, retry state, and bucket leases
- `internal/checker`: Stateless HTTP check; no global state
- `internal/checker/pool`: Auto-scaling goroutine pool; driven by queue depth
- `internal/veriflier`: Thin transport layer; JSON-over-HTTP until protoc generates real stubs
- `internal/wpcom`: Owns WPCOM circuit breaker and notification queue

### Data Flow
```
Database → Orchestrator → Pool → checker.Check → Results
                ↓
         Veriflier gRPC clients (geo-distributed)
                ↓
         WPCOM API (circuit-broken notification queue)
```

### Critical Constraints
- Retry queue must persist between rounds (never flushed at round start)
- Bucket ranges must not overlap between hosts (MySQL `SELECT ... FOR UPDATE` enforces this)
- Heartbeat must fire every round; WatchdogSec=120s means missing two rounds triggers systemd restart
- Circuit breaker floor: at least 1 veriflier quorum, even if all verifliers are offline

## Production Considerations

### Before Modifying Code
- Test changes locally using Docker environment (`docker compose up -d`)
- Verify goroutine count and memory do not grow unboundedly
- Check that StatsD metrics are properly emitted
- Ensure graceful shutdown behaviour is preserved (SIGINT → `orch.Stop()`)

### Deployment Process
- Changes require Systems team deployment
- Create a Systems Request with PR links
- Run `./jetmon2 validate-config` before deploying

### Performance Sensitivity
- RTT calculations feed into timeout heuristics — don't add unnecessary latency
- Pool auto-scaling fires every 5 seconds; don't block the scale goroutine
- `runtime.ReadMemStats` is stop-the-world; call it infrequently

## Security Considerations

- Auth tokens in config must not be logged
- gRPC/HTTP veriflier auth token is validated per-request in `internal/veriflier/server.go`
- Database credentials are stored in `config/db-config.conf` (not committed)
- Never commit secrets to the repository

## Testing Approach

- Use `go test ./...` for unit tests
- Use Docker environment for integration testing
- Enable `DB_UPDATES_ENABLE` only in local test environments
- Test graceful shutdown with SIGINT
- Monitor goroutine count over extended runs (`/debug/pprof/goroutine`)

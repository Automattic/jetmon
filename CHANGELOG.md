CHANGELOG
=========

Format: date (YYYY-MM-DD), change summary, PR or commit reference where available.
Breaking changes are marked **BREAKING**.

---

## Unreleased

### Jetmon 2 — initial Go rewrite

Complete rewrite of the Node.js + C++ uptime monitor as a single static Go binary.
Drop-in replacement for Jetmon 1; all existing MySQL schema columns are preserved.

**New:**
- Single binary (`jetmon2`) — no process tree, no node_modules
- Auto-scaling goroutine pool replaces worker process spawning
- `jetmon2 migrate` — schema migrations embedded in binary
- `jetmon2 validate-config` — config + DB connectivity check before deploy
- `jetmon2 drain` / `jetmon2 reload` — signal running process via PID file
- `jetmon2 audit` — query per-site audit log from CLI
- Operator dashboard on configurable port with SSE state stream
- pprof debug server on localhost-only `DEBUG_PORT` (default 6060)
- `DB_UPDATES_ENABLE` double-gate: requires both config flag and `JETMON_UNSAFE_DB_UPDATES=1` env var
- Graceful shutdown with 30-second hard-exit backstop
- Non-root Docker images (`jetmon` / `veriflier` system users)
- Healthcheck-gated MySQL dependency in docker-compose

**Changed:**
- Veriflier transport package renamed `internal/grpc` → `internal/veriflier`
- Auth token moved from JSON request body to `Authorization: Bearer` header
- MySQL DSN built via `mysql.Config.FormatDSN()` — password never in format strings
- `internal/db` functions accept `context.Context` for cancellation
- `DEBUG` config flag now controls log verbosity via `config.Debugf()`
- `AUTH_TOKEN` is now a required config field (validated at startup)
- `config-sample.json` ships with `DEBUG: false`

**Fixed:**
- `cmdDrain` / `cmdReload` now read PID path from `JETMON_PID_FILE` env var
  (previously hardcoded to wrong path `/var/run/jetmon2.pid`)
- Audit log failures are now logged rather than silently discarded
- DB write errors (`RecordCheckHistory`, `UpdateSSLExpiry`) are now logged

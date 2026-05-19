# Coding Standards

`AGENTS.md` is the source of truth for this repository. These notes are a
compact helper for Claude project workflows and should not override the main
agent instructions.

## Priority Order

Follow coding standards in this order:

1. Existing patterns in the codebase
2. `AGENTS.md`
3. Conventions documented in this file
4. Effective Go (https://go.dev/doc/effective_go)

## Go

### Formatting

- Run `gofmt` on all touched Go files.
- Use tabs for indentation, as enforced by `gofmt`.
- Do not impose a hard line length limit; prefer readable wrapping.

### Naming

- Packages: lowercase, short, and usually one word.
- Exported identifiers: `PascalCase`.
- Unexported identifiers: `camelCase`.
- Acronyms: `HTTPCode`, `RTTMs`, `URL`; lowercase when unexported.
- Sentinel errors: `ErrFoo`.
- Interfaces: nouns or `-er` suffixes when the behavior is clear.
- Config key strings: `SCREAMING_SNAKE_CASE` to match JSON/environment names.

### Errors

- Return errors from library code; do not panic for ordinary failures.
- Wrap errors with context using `%w`.
- Log and continue for non-fatal operational failures.
- Use fatal startup exits only when the process cannot run safely.

### Concurrency

- Pass `context.Context` as the first argument to functions that block or do
  I/O.
- Protect shared mutable state with `sync.Mutex`, `sync.RWMutex`, channels, or
  atomics as appropriate.
- Keep goroutine lifetimes tied to context cancellation or explicit stop
  channels.
- Prefer bounded queues and worker pools for request/check fan-out.

### Imports

- Standard library first, then external packages, then internal packages.
- Keep generated or future transport experiments out of normal build paths
  unless the project intentionally enables them.

### Comments

- Package comments and exported symbol comments should be present where they
  help public package documentation.
- Inline comments explain why a surprising choice is required, not what each
  line does.
- Avoid documenting temporary workarounds in long-lived docs unless the
  workaround is part of the production process.

## Shell Scripts

- Use `#!/usr/bin/env bash` and `set -euo pipefail` for non-trivial scripts.
- Quote variable expansions unless word splitting is intentional.
- Use `${VARIABLE_NAME}` for variables in scripts.
- Prefer explicit flags over positional magic for operator-facing scripts.

## Database Operations

- Use `database/sql` with context-aware calls.
- Use parameterized queries for dynamic values.
- Keep writes transactionally grouped when code updates related tables or
  projections.
- Release rows and close result sets promptly.
- Do not log DSNs, passwords, tokens, or server-map secrets.

## Configuration

- Keep config compatibility explicit: accepted aliases should be documented and
  should warn when their old meaning no longer applies.
- Validate config at startup and in `jetmon2 validate-config`.
- Hot-reloadable config must either apply atomically or keep the previous known
  good state.

## Logging

- Runtime logs go to stdout/stderr for service-manager or container collection.
- Keep routine scheduler/status chatter behind debug logging.
- Do not reintroduce v1 runtime log files unless a confirmed production
  consumer requires them.

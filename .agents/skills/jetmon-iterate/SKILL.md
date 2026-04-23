---
name: jetmon-iterate
description: End-to-end local iteration loop for Jetmon code changes
allowed-tools: Bash(scripts/agent/*), Bash(go test*), Bash(go build*), Bash(docker*), Read, Glob, Grep
---

# Jetmon iterate

Use this skill for repeatable local code-change loops.

## Usage

- `/jetmon-iterate`
- `/jetmon-iterate fast`
- `/jetmon-iterate rebuild`

## Fast loop

```bash
scripts/agent/up.sh
scripts/agent/seed.sh
scripts/agent/assert.sh
go test ./...
```

## Rebuild loop

```bash
scripts/agent/rebuild.sh
scripts/agent/seed.sh
scripts/agent/assert.sh
go test ./...
```

## Clean reset loop

```bash
scripts/agent/reset.sh
scripts/agent/seed.sh
scripts/agent/assert.sh
go test ./...
```

## Exit criteria

- Docker services are running.
- Seed scenarios are present with expected URLs.
- `assert.sh` returns success.
- Tests pass for touched areas (or full `go test ./...`).

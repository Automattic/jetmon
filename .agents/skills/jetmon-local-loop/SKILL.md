---
name: jetmon-local-loop
description: Run the standard local Docker loop for Jetmon with scripts/agent
allowed-tools: Bash(scripts/agent/*), Bash(docker*), Read, Glob
---

# Jetmon local loop

Use this skill to run the default local iteration loop with deterministic scripts.

## Usage

- `/jetmon-local-loop`
- `/jetmon-local-loop up`
- `/jetmon-local-loop seed`
- `/jetmon-local-loop assert`
- `/jetmon-local-loop logs`
- `/jetmon-local-loop rebuild`
- `/jetmon-local-loop down`
- `/jetmon-local-loop reset`

## Standard flow

From repository root:

```bash
scripts/agent/up.sh
scripts/agent/seed.sh
scripts/agent/assert.sh
```

If code changes require rebuilt images:

```bash
scripts/agent/rebuild.sh
scripts/agent/seed.sh
scripts/agent/assert.sh
```

## Notes

- Scripts always run compose from `docker/`.
- `assert.sh` is strict and exits non-zero on failures.
- Use `scripts/agent/logs.sh` (or `scripts/agent/logs.sh <service>`) for triage.

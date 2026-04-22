---
name: jetmon-db-seed
description: Seed stable local Jetmon test scenarios into MySQL idempotently
allowed-tools: Bash(scripts/agent/seed.sh), Bash(docker*), Read
---

# Jetmon DB seed

Use this skill to populate local scenario rows in `jetpack_monitor_sites`.

## Usage

- `/jetmon-db-seed`
- `/jetmon-db-seed run`

## Command

```bash
scripts/agent/seed.sh
```

## Behavior

- Idempotent by `blog_id` (updates existing, inserts missing).
- Probes table columns before write operations.
- Only writes optional columns when they exist.
- If `check_keyword` exists, sets `check_keyword='jetmon-keyword'` for `910005` and `NULL` for the other seeded rows.
- Uses `docker compose exec mysqldb mysql` from `docker/`.

## Scenarios

- `910001`: `https://httpstat.us/200`
- `910002`: `https://httpstat.us/500`
- `910003`: `https://httpstat.us/200?sleep=15000`
- `910004`: `https://httpstat.us/301`
- `910005`: `https://httpstat.us/200` (keyword-miss scenario; sets `check_keyword='jetmon-keyword'` when the column exists)

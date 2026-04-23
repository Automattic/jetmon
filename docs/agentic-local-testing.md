# Agentic local testing

This is a script-first local loop for Jetmon when iterating with an agent.

## Deterministic loop

From repo root:

```bash
# 1) bootstrap
scripts/agent/up.sh

# 2) health gate
scripts/agent/assert.sh

# 3) seed
scripts/agent/seed.sh

# 4) observe
scripts/agent/logs.sh

# 5) change
# edit code

# 6) rebuild
scripts/agent/rebuild.sh both

# 7) assert
scripts/agent/assert.sh

# 8) summarize
# report changed files + key outputs
```

## Safety guardrails

- Local-only workflow; do not target shared or production infra.
- Database writes are limited to the `jetpack_monitor_sites` seed rows (`blog_id` 910001..910005).
- Do not use destructive git commands (`reset --hard`, forced checkout, force push) in this loop.

## One-command examples

From repo root:

```bash
scripts/agent/up.sh
scripts/agent/down.sh
scripts/agent/reset.sh
scripts/agent/logs.sh
LOG_TAIL=250 scripts/agent/logs.sh jetmon
scripts/agent/rebuild.sh
scripts/agent/rebuild.sh jetmon
scripts/agent/rebuild.sh veriflier
scripts/agent/rebuild.sh both
scripts/agent/seed.sh
scripts/agent/assert.sh
```

## What each script does

- `scripts/agent/up.sh`: ensures `docker/.env` exists, then starts Docker services from `docker/` with build.
- `scripts/agent/down.sh`: stops Docker services.
- `scripts/agent/reset.sh`: drops stack and volumes, then rebuilds and starts.
- `scripts/agent/logs.sh`: follows compose logs with tail count (`LOG_TAIL`, default `120`) and defaults to `jetmon veriflier mysqldb`.
- `scripts/agent/rebuild.sh`: rebuilds and starts `jetmon`, `veriflier`, or both.
- `scripts/agent/seed.sh`: idempotently seeds scenario rows into `jetpack_monitor_sites`.
- `scripts/agent/assert.sh`: hard assertions for running/healthy services, startup logs, seeded rows, expected URLs, and stats files.

## Seed scenarios

The seed script keeps a stable set of local scenarios by `blog_id` and updates existing rows if they already exist.

- `910001`: HTTP 200 baseline
- `910002`: HTTP 500 server error
- `910003`: delayed endpoint for timeout testing
- `910004`: redirect endpoint
- `910005`: keyword-miss scenario (`https://httpstat.us/200` with `check_keyword='jetmon-keyword'` when the column exists)

The SQL is conservative:

- It inspects table columns first.
- It only writes columns that exist.
- It avoids schema assumptions about optional Jetmon 2 additive columns.

## Acceptance criteria

- `scripts/agent/up.sh` succeeds and stack is up.
- `scripts/agent/seed.sh` seeds exactly 5 target rows (`910001..910005`) idempotently.
- `scripts/agent/assert.sh` exits 0 with all checks passing.
- Expected URLs match all 5 scenarios, including `910005 -> https://httpstat.us/200`.
- Stats files exist at `stats/sitespersec`, `stats/sitesqueue`, and `stats/totals`.

## Notes

- Scripts rely on `docker/.env` for DB credentials.
- Compose commands run from `docker/` as required by this repository.
- Assertions exit non-zero on failure so CI and agents can stop early.

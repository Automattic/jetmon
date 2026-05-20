# 0011 — v2 tables use the `jetpack_monitor_` prefix

**Status:** Accepted (2026-05-19)

## Context

The v1 monitor's only persistent table is `jetpack_monitor_sites`, which lives
in the shared WPCOM "misc" dataset. Early v2 development created all new tables
with a `jetmon_` prefix (`jetmon_events`, `jetmon_audit_log`, …), reflecting the
"Jetmon 2" product name. That left two prefixes in one database: the frozen v1
table on `jetpack_monitor_` and ~28 v2 tables on `jetmon_`.

Two facts made the divergence a real risk rather than a cosmetic one:

- **v1's data layer routes queries to connection pools by table-name
  substring** (`lib/database.js`): a query containing `jetpack_` is sent to the
  `MISC_` pool, `languages` to `GLOBAL_`, everything else to the default
  `USER_` pool. The `jetpack_` prefix is a load-bearing convention in the
  surrounding WPCOM environment, not just a name.
- All v2 tables co-locate with `jetpack_monitor_sites` in one database (the
  scheduler JOINs across them), so prefix-based fleet tooling — backup,
  replication, sharding, schema management that keys off `jetpack_` — would
  treat the `jetmon_` tables as outside the family.

The project had already chosen v1 continuity for the StatsD metric path
(`com.jetpack.jetmon.…`, see AGENTS.md); the table prefix was the inconsistent
outlier, and the choice was undocumented.

## Decision

Rename every v2-owned table from `jetmon_<name>` to `jetpack_monitor_<name>`
(e.g. `jetmon_events` → `jetpack_monitor_events`). The legacy
`jetpack_monitor_sites` is unchanged. New v2 tables follow the
`jetpack_monitor_` prefix going forward.

Done while v2 is pre-production and no environment needs preserving, so the
migration `CREATE TABLE` definitions were edited in place — a fresh `migrate`
builds the renamed tables directly, with no `RENAME TABLE` step. Non-table
identifiers that happen to contain `jetmon_` (the `jetmon_db` database name,
`jetmon_dev_password`, `JETMON_*` env vars, the `jetmon_hosts_rows` operator
report label, binary names) are deliberately left unchanged.

## Consequences

**Wins:**
- One prefix for everything Jetmon stores in the shared dataset; `jetpack_`
  prefix-based WPCOM tooling and pool routing see the v2 tables as part of the
  family.
- Removes an undocumented naming split that reviewers kept rediscovering.

**Costs:**
- Longer table names (max `jetpack_monitor_rollout_confirmation_tokens`, 43
  chars — well under MySQL's 64-char limit).
- One-time churn across migrations, queries, tests, scripts, and docs. Cheap
  now; would have been a live `RENAME TABLE` migration after production data.

**Note:** this is only safe to do in place because no deployed environment had
applied the `jetmon_`-named migrations. After production exists, any further
table rename must go through `RENAME TABLE` migrations instead.

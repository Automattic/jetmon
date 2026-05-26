# Production Schema Package

Jetmon 2 production schema changes are applied by Systems, not by the service
at startup. The package should be boring to review: one additive SQL file, a
clear table inventory, and read-only validation commands.

## What To Apply

Use [`migrations/production-v2-baseline.sql`](../migrations/production-v2-baseline.sql)
as the production baseline DDL. It creates only Jetmon v2-owned side tables.

It does **not** create or alter `jetpack_monitor_sites`. V2 expects the legacy
table to keep its v1 shape:

- `jetpack_monitor_site_id`
- `blog_id`
- `bucket_no`
- `monitor_url`
- `monitor_active`
- `site_status`
- `last_status_change`
- `check_interval`

V2 also expects an index whose leading columns are `(bucket_no,
monitor_active)`. The current v1 index
`bucket_no_monitor_active_check_interval` satisfies that requirement, so no
legacy-table index rename is needed.

`jetpack_monitor_schema_migrations` is not part of the production contract. It
is legacy local/lab bookkeeping from the old embedded migration path. Current
production validation checks the actual schema shape through
`information_schema`, and the non-production reconciler uses the reviewed
baseline SQL directly.

## Validation Commands

Run these from the same network and credentials the Monitor will use:

```bash
./jetmon2 schema validate
./jetmon2 schema status
./jetmon2 schema diff
./jetmon2 doctor --skip-verifliers
```

`schema validate` is the deployment gate. It fails if any required table,
column, or index is missing and never applies DDL.

`schema diff` is read-only. It prints additive SQL that would make the connected
database satisfy the same structural contract. Treat it as diagnostic help, not
as a substitute for the reviewed Systems SQL package.

`schema status` prints the same structural contract and, if the legacy
local/lab migration ledger exists, prints that as extra context. Missing ledger
rows are not a production failure.

## Table Activity

Most v2 tables are low-churn operational state. The higher-volume tables are:

- `jetpack_monitor_check_history`: sampled probe timing history. Volume depends
  on `CHECK_HISTORY_MODE_DEFAULT` and sample rate.
- `jetpack_monitor_event_transitions`: one row per incident mutation.
- `jetpack_monitor_audit_log`: operational actions such as WPCOM attempts,
  Veriflier RPCs, config reloads, and suppression decisions.
- `jetpack_monitor_webhook_deliveries` and
  `jetpack_monitor_alert_deliveries`: one row per matched outbound delivery.
- `jetpack_monitor_process_health`, `jetpack_monitor_hosts`,
  `jetpack_monitor_veriflier_agents`: heartbeat-style upserts.

Per-site policy and runtime tables are keyed by
`jetpack_monitor_sites.jetpack_monitor_site_id`, not by `blog_id`, so multiple
active monitor URLs for one blog do not overwrite each other.

## Rollback

The v2 schema is additive. If rollout is stopped, roll the services back or
leave v2 Monitors in standby/API-controlled mode. Do not drop v2 tables during
an incident response window; leaving unused additive tables in place is safer
than adding a destructive database step to rollback.

## Future Schema Updates

Future production changes should follow the same pattern:

1. Additive SQL reviewed by Systems.
2. Code updated to validate the structural contract.
3. Production role uses `CONFIG_PROFILE=production` and
   `SCHEMA_MANAGEMENT_MODE=validate`.
4. `jetmon2 schema validate` confirms the applied schema before activation.

Do not require production operators to update `jetpack_monitor_schema_migrations`.
If a migration ledger exists in a lab database, it is useful diagnostic context
only and can be retired after local/dev tooling no longer depends on it.

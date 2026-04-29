# v1 to v2 Pinned Bucket Rollout

This document has moved.

Use [v1-to-v2-migration.md](v1-to-v2-migration.md) for the complete production
migration runbook, including pinned bucket mode, same-server replacement,
fresh-server takeover, monitoring, revert paths, dynamic ownership cutover, and
v1 teardown. That runbook is also the source of truth for static bucket plan,
post-cutover activity, rollback, and projection-drift safety checks.

Pinned mode still means:

- configure `PINNED_BUCKET_MIN` and `PINNED_BUCKET_MAX` to match the v1 host's
  static bucket range
- keep `LEGACY_STATUS_PROJECTION_ENABLE=true`
- keep `API_PORT=0` during initial production monitor replacement unless an API
  and delivery-owner plan has been approved
- run `./jetmon2 validate-config`
- run `./jetmon2 rollout pinned-check`
- after v2 starts, run `./jetmon2 rollout cutover-check --since=15m`, then
  rerun it with `--require-all` after one full expected check round

The old detailed checklist was consolidated into
[v1-to-v2-migration.md](v1-to-v2-migration.md) so migration guidance does not
drift across multiple docs.

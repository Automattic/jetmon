# v1 to v2 Pinned Bucket Rollout

**Status:** Production migration runbook for the first v1-to-v2 cutover.

This rollout replaces one v1 static-bucket host with one v2 host pinned to the
same inclusive bucket range. It avoids mixed ownership between v1 static config
and v2 `jetmon_hosts` dynamic ownership during the riskiest part of the
migration.

## Why Pinned Mode Exists

v1 and v2 do not share a bucket ownership protocol:

- v1 uses static `BUCKET_NO_MIN` / `BUCKET_NO_MAX` config per host.
- v2 normally uses the `jetmon_hosts` table with heartbeat and reclaim.

During a mixed fleet rollout, dynamic v2 ownership cannot know which buckets are
still covered by v1. Pinned mode keeps each replacement host on the exact range
its v1 predecessor owned and disables `jetmon_hosts` ownership for that v2 host.

## Configuration

Prefer explicit pinned keys in v2 config:

```json
{
  "PINNED_BUCKET_MIN": 0,
  "PINNED_BUCKET_MAX": 99,
  "LEGACY_STATUS_PROJECTION_ENABLE": true,
  "API_PORT": 0
}
```

The legacy v1 names `BUCKET_NO_MIN` and `BUCKET_NO_MAX` are accepted as aliases
for pinned mode. If both forms are present, they must describe the same range.

While pinned:

- the host checks only `PINNED_BUCKET_MIN <= bucket_no <= PINNED_BUCKET_MAX`
- the host does not claim or heartbeat `jetmon_hosts`
- shutdown does not release a `jetmon_hosts` row
- `BUCKET_TOTAL`, `BUCKET_TARGET`, and `BUCKET_HEARTBEAT_GRACE_SEC` still
  validate, but dynamic ownership does not use them on that host

## Preflight

1. Export the v1 fleet's static bucket plan to CSV and verify that the ranges
   are complete and non-overlapping before touching any hosts:

   ```csv
   host,bucket_min,bucket_max
   jetmon-v1-a,0,99
   jetmon-v1-b,100,199
   ```

   ```bash
   ./jetmon2 rollout static-plan-check --file rollout-buckets.csv
   ```

   The check reads `BUCKET_TOTAL` from `JETMON_CONFIG` by default. Use
   `--bucket-total=<n>` if the config file is not available yet. The legacy
   header names `BUCKET_NO_MIN` and `BUCKET_NO_MAX` are accepted so operators
   can paste directly from v1 config inventory.

   Before replacing an individual host, assert that the copied host/range still
   matches the approved plan:

   ```bash
   ./jetmon2 rollout static-plan-check --file rollout-buckets.csv \
     --host=jetmon-v1-a --bucket-min=0 --bucket-max=99
   ```
2. Build all v2 binaries and run `make test`, `make test-race`, and `make all`.
3. Apply additive migrations before the cutover:

   ```bash
   ./jetmon2 migrate
   ```

4. Keep `LEGACY_STATUS_PROJECTION_ENABLE=true` so legacy readers continue to see
   `jetpack_monitor_sites.site_status` and `last_status_change`.
5. Keep `API_PORT=0` on monitor hosts during initial replacement unless the API
   and delivery owner plan has been explicitly approved.
6. Run `./jetmon2 validate-config` with the prepared v2 config and confirm it
   prints the pinned rollout safety commands: static plan, pinned preflight,
   activity check, rollback check, and projection-drift report.
7. Verify Veriflier endpoints, WPCOM auth, StatsD, log paths, and config reload
   behavior in staging.

## Per-Host Cutover

For each v1 host:

1. Record the host name and v1 bucket range.
2. Confirm that host and range are present in the already-validated static
   bucket plan, then prepare the v2 config with the same pinned range.
3. Before stopping v1, run `./jetmon2 validate-config` and confirm it reports:
   - `legacy_status_projection=enabled`
   - `bucket_ownership=pinned range=<min>-<max>`
   - `rollout_static_plan=./jetmon2 rollout static-plan-check --file=<ranges.csv>`
   - `rollout_preflight=./jetmon2 rollout pinned-check`
   - `rollout_activity_check=./jetmon2 rollout activity-check --since=15m`
   - `rollout_rollback_check=./jetmon2 rollout rollback-check`
   - `rollout_drift_report=./jetmon2 rollout projection-drift`
4. Before stopping v1, run the pinned rollout preflight:

   ```bash
   ./jetmon2 rollout pinned-check
   ```

   This check fails if the host is not in pinned mode, legacy projection writes
   are disabled, the current host still has a `jetmon_hosts` ownership row, any
   dynamic `jetmon_hosts` row overlaps the pinned range, or the active sites in
   the pinned range have projection drift. It also prints the active site count
   for the range. If projection drift is reported, list the mismatched rows
   before continuing:

   ```bash
   ./jetmon2 rollout projection-drift
   ```

   If checking a config before running on the final hostname, pass the expected
   host id explicitly:

   ```bash
   ./jetmon2 rollout pinned-check --host=<v2-hostname>
   ```

5. Stop the v1 process for that host.
6. Start the v2 process.
7. Verify the process logs:
   - `legacy_status_projection=enabled`
   - `bucket_ownership=pinned range=<min>-<max>`
   - `orchestrator: using pinned buckets <min>-<max>`
8. If `DASHBOARD_PORT` is enabled, open the operator dashboard and confirm:
   - rollout ownership shows the pinned range
   - legacy projection is enabled
   - delivery workers are disabled unless the delivery owner plan explicitly
     enables them on this host
   - dependency health is green for MySQL, configured Verifliers, log/stats
     directory writes, and StatsD initialization; WPCOM must not show an open
     circuit
9. Watch one full check round for that bucket range.
10. Verify recent check activity for the copied bucket range:

   ```bash
   ./jetmon2 rollout activity-check --since=15m
   ```

   The check defaults to the pinned range from config. It fails if active sites
   exist in the range but none have `last_checked_at` at or after the cutoff.
   This proves that the copied range has fresh check writes; it does not prove
   which process wrote them, so keep v1 stopped and use logs or the dashboard to
   confirm v2 is checking only the pinned range.
   After enough time for a full expected round, use `--require-all` to fail
   unless every active site in the range has been checked recently:

   ```bash
   ./jetmon2 rollout activity-check --since=15m --require-all
   ```

11. Confirm:
   - checks are running only for the pinned range
   - Veriflier confirmation works
   - WPCOM notifications retain the v1 payload shape
   - `jetmon_events` and `jetmon_event_transitions` receive event mutations
   - `jetpack_monitor_sites.site_status` projection updates when enabled
   - no unexpected rows are claimed in `jetmon_hosts` by the pinned host

## Rollback

Rollback is host-local:

1. Stop the v2 process.
2. Run the rollback safety check:

   ```bash
   ./jetmon2 rollout rollback-check --host=<v2-hostname>
   ```

   This check defaults to the pinned range from config. It fails if the v2 host
   still owns a `jetmon_hosts` row, any dynamic `jetmon_hosts` row overlaps the
   rollback range, or the legacy projection has drifted. If running from an
   operator box instead of the v2 host, keep `--host` set to the v2 hostname
   that was just stopped. Pinned v2 hosts intentionally do not heartbeat
   `jetmon_hosts`, so this check cannot prove the pinned v2 process is stopped;
   confirm the service stop completed before restarting v1.
3. Restart the original v1 process with the same `BUCKET_NO_MIN` /
   `BUCKET_NO_MAX` config.
4. Verify v1 checks the range again.

The v2 migrations are additive, and legacy projection writes keep the old status
fields meaningful while `LEGACY_STATUS_PROJECTION_ENABLE=true`, so rollback does
not require schema rollback.

## Transition to Dynamic v2 Ownership

After every monitor host is on v2 and stable in pinned mode:

1. Confirm no v1 monitor hosts remain active.
2. Plan a coordinated dynamic-ownership cutover. Pinned hosts do not write
   `jetmon_hosts`, so avoid leaving a long-lived mixed fleet where some v2
   hosts are pinned and others use dynamic ownership.
3. Remove `PINNED_BUCKET_MIN` / `PINNED_BUCKET_MAX` (and any legacy
   `BUCKET_NO_MIN` / `BUCKET_NO_MAX` aliases) from the v2 monitor configs.
4. Restart the v2 monitor hosts in the approved deployment window.
5. Run `./jetmon2 validate-config` and confirm it reports:
   - `rollout_preflight=./jetmon2 rollout dynamic-check`
   - `rollout_activity_check=./jetmon2 rollout activity-check --since=15m`
   - `rollout_drift_report=./jetmon2 rollout projection-drift`
6. Run the dynamic ownership preflight:

   ```bash
   ./jetmon2 rollout dynamic-check
   ```

   This check fails if pinned mode is still configured, legacy projection writes
   are disabled, `jetmon_hosts` rows are missing, stale, inactive, overlapping,
   or gapped, or the legacy projection has drifted.

   To inspect projection drift details across the dynamic range:

   ```bash
   ./jetmon2 rollout projection-drift --limit=100
   ```

7. After one full expected round, verify all active sites have fresh activity:

   ```bash
   ./jetmon2 rollout activity-check --since=15m --require-all
   ```

8. Continue using the normal v2 rolling-update process from `README.md`.

Do not run a mixed configuration where some v1 hosts still own static ranges
while unpinned v2 hosts use dynamic `jetmon_hosts` ownership. Also avoid a
long-lived pinned-v2/dynamic-v2 mix: dynamic hosts cannot see pinned hosts in
`jetmon_hosts`, so the fleet can overlap checks even though it should not create
coverage gaps.

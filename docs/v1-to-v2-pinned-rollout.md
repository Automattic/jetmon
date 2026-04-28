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

1. Confirm the v1 fleet's static bucket ranges are complete and non-overlapping.
2. Build all v2 binaries and run `make test`, `make test-race`, and `make all`.
3. Apply additive migrations before the cutover:

   ```bash
   ./jetmon2 migrate
   ```

4. Keep `LEGACY_STATUS_PROJECTION_ENABLE=true` so legacy readers continue to see
   `jetpack_monitor_sites.site_status` and `last_status_change`.
5. Keep `API_PORT=0` on monitor hosts during initial replacement unless the API
   and delivery owner plan has been explicitly approved.
6. Verify Veriflier endpoints, WPCOM auth, StatsD, log paths, and config reload
   behavior in staging.

## Per-Host Cutover

For each v1 host:

1. Record the host name and v1 bucket range.
2. Prepare the v2 config with the same pinned range.
3. Stop the v1 process for that host.
4. Start the v2 process.
5. Run `./jetmon2 validate-config` and confirm it reports
   `bucket_ownership=pinned range=<min>-<max>`.
6. Run the pinned rollout preflight:

   ```bash
   ./jetmon2 rollout pinned-check
   ```

   This check fails if the host is not in pinned mode, legacy projection writes
   are disabled, the current host still has a `jetmon_hosts` ownership row, or
   the active sites in the pinned range have projection drift. It also prints the
   active site count for the range. If projection drift is reported, list the
   mismatched rows before continuing:

   ```bash
   ./jetmon2 rollout projection-drift
   ```

   If checking a config before running on the final hostname, pass the expected
   host id explicitly:

   ```bash
   ./jetmon2 rollout pinned-check --host=<v2-hostname>
   ```

7. Verify the process logs:
   - `legacy_status_projection=enabled`
   - `bucket_ownership=pinned range=<min>-<max>`
   - `orchestrator: using pinned buckets <min>-<max>`
8. Watch one full check round for that bucket range.
9. Confirm:
   - checks are running only for the pinned range
   - Veriflier confirmation works
   - WPCOM notifications retain the v1 payload shape
   - `jetmon_events` and `jetmon_event_transitions` receive event mutations
   - `jetpack_monitor_sites.site_status` projection updates when enabled
   - no unexpected rows are claimed in `jetmon_hosts` by the pinned host

## Rollback

Rollback is host-local:

1. Stop the v2 process.
2. Restart the original v1 process with the same `BUCKET_NO_MIN` /
   `BUCKET_NO_MAX` config.
3. Verify v1 checks the range again.

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
5. Run the dynamic ownership preflight:

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

6. Continue using the normal v2 rolling-update process from `README.md`.

Do not run a mixed configuration where some v1 hosts still own static ranges
while unpinned v2 hosts use dynamic `jetmon_hosts` ownership. Also avoid a
long-lived pinned-v2/dynamic-v2 mix: dynamic hosts cannot see pinned hosts in
`jetmon_hosts`, so the fleet can overlap checks even though it should not create
coverage gaps.

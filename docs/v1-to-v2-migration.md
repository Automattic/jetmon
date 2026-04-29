# v1 to v2 Migration Runbook

This is the source-of-truth runbook for the first production migration from
Jetmon 1 to Jetmon 2.

Use [rollout-quick-reference.md](rollout-quick-reference.md) as the condensed
command checklist during rehearsals and rollout windows. If it conflicts with
this runbook, this runbook wins.

Use this document for:

- preparing the fleet before any production change
- replacing v1 on the same server
- moving a v1 bucket range to a fresh v2 server
- monitoring the cutover
- reverting safely
- completing the move from pinned buckets to dynamic v2 ownership
- removing old v1 software after signoff

## What Changes For Customers

The important product fix is the probe method.

Jetmon 1 verified sites with `HEAD` requests. That caused real customer pain:
some production stacks block `HEAD`, route it differently, skip application
logic, or return a status that does not match a visitor's real page load.
Jetmon 2 uses `GET` requests for local monitor checks and Veriflier checks, so
it validates the same class of request a browser or customer-facing uptime
check normally makes.

This is why v2 can support keyword checks, richer redirect behavior, and better
VIP/Agency explanations. It is also why the rollout should be watched closely:
GET-based checks are more correct, but they can expose sites whose `HEAD`
behavior used to hide a real application issue.

## Success Criteria

The migration is complete only when:

- every active v1 bucket range is covered by exactly one v2 host
- no v1 monitor process is checking production buckets
- `./jetmon2 rollout dynamic-check` passes after pinned mode is removed
- legacy projection drift is zero while `LEGACY_STATUS_PROJECTION_ENABLE` is on
- WPCOM notifications retain the v1 payload shape
- check throughput, round timing, WPCOM delivery, Veriflier health, StatsD, and
  log/stats writes are stable for the agreed observation window
- old v1 software is retained until rollback signoff, then removed deliberately

## Rollout Invariants

Do not violate these during the migration:

- Do not run v1 and v2 against the same bucket range at the same time.
- Do not run unpinned dynamic v2 while any v1 host still owns static buckets.
- Keep `LEGACY_STATUS_PROJECTION_ENABLE=true` until legacy readers have moved to
  the v2 API or event tables.
- Keep `API_PORT=0` on production monitor hosts during initial replacement
  unless the API and delivery-owner plan has been explicitly approved.
- Do not remove v1 binaries, configs, service units, or dependencies until the
  rollback window is closed.
- Treat `./jetmon2 migrate` as forward-only. Migrations are additive, so revert
  by restarting v1, not by rolling the schema back.

## Phase 0: Prepare Before Production Changes

### Inventory The Current Fleet

Record, for every v1 host:

- hostname
- service manager name and start/stop commands
- v1 binary or checkout path
- v1 config path
- `BUCKET_NO_MIN` and `BUCKET_NO_MAX`
- log and stats paths
- WPCOM credentials source
- Veriflier list
- expected sites-per-round or sites-per-second baseline
- current alert volume and any known noisy sites

Confirm the bucket ranges are complete and non-overlapping:

```sql
SELECT bucket_no, COUNT(*) AS sites
FROM jetpack_monitor_sites
WHERE monitor_active = 1
GROUP BY bucket_no
ORDER BY bucket_no;
```

Export the approved host-to-bucket plan to CSV before touching any hosts:

```csv
host,bucket_min,bucket_max
jetmon-v1-a,0,99
jetmon-v1-b,100,199
```

Then verify that the copied v1 static plan covers the full configured bucket
range without gaps, overlaps, invalid ranges, or duplicate host rows:

```bash
./jetmon2 rollout static-plan-check --file rollout-buckets.csv
```

If checking the plan before the v2 config is available, pass the expected total:

```bash
./jetmon2 rollout static-plan-check --file rollout-buckets.csv --bucket-total=<n>
```

Before replacing a specific host, assert that the copied range still matches the
approved plan:

```bash
./jetmon2 rollout static-plan-check --file rollout-buckets.csv \
  --host=jetmon-v1-a --bucket-min=0 --bucket-max=99 --bucket-total=<total>
```

Generate the host-specific command sequence operators will rehearse and run:

```bash
./jetmon2 rollout rehearsal-plan \
  --file rollout-buckets.csv \
  --host=jetmon-v1-a \
  --bucket-min=0 \
  --bucket-max=99 \
  --bucket-total=<total> \
  --mode=same-server \
  --v1-stop-command='<exact v1 stop command>' \
  --v1-start-command='<exact v1 rollback start command>'
```

For a fresh-server takeover where the v2 hostname differs from the v1 host in
the static plan, add `--runtime-host=<new-v2-hostname>` and use
`--mode=fresh-server`. Add `--systemd-unit=<path>` if the staged service unit
is not `/etc/systemd/system/jetmon2.service`.

During the production window, prefer the guided command so operators do not
need to copy/paste each command manually:

```bash
./jetmon2 rollout guided \
  --file rollout-buckets.csv \
  --host=jetmon-v1-a \
  --runtime-host=jetmon-v1-a \
  --bucket-min=0 \
  --bucket-max=99 \
  --bucket-total=<total> \
  --mode=same-server \
  --v1-stop-command='<exact v1 stop command>' \
  --v1-start-command='<exact v1 rollback start command>' \
  --log-dir=logs/rollout
```

`rollout guided` checks that the log directory is writable before it starts,
writes a transcript plus `<runtime-host>-<min>-<max>.state.json` resume state,
explains each gate, asks before continuing, and stops on failed gates. It uses
typed confirmations before stopping v1, starting v2, stopping v2 during
rollback, or restarting v1. By default it prints service commands for the
operator to run and asks for `DONE`; add `--execute-operator-commands` only
when the operator intentionally wants the guided command to execute those
commands after confirmation.

If a rollout needs to return the range to v1, use the guided rollback path:

```bash
./jetmon2 rollout guided \
  --rollback \
  --file rollout-buckets.csv \
  --host=jetmon-v1-a \
  --runtime-host=jetmon-v1-a \
  --bucket-min=0 \
  --bucket-max=99 \
  --bucket-total=<total> \
  --v1-start-command='<exact v1 rollback start command>' \
  --log-dir=logs/rollout
```

### Prepare Database And Rollback Safety

1. Confirm a recent MySQL backup exists and restore has been tested according
   to normal production policy.
2. Review pending migrations with the release owner.
3. Apply additive migrations before the first host cutover:

   ```bash
   ./jetmon2 migrate
   ```

4. Confirm v1 continues to run normally after migrations are applied.
5. Do not plan a schema rollback. If v2 must be reverted, v1 can keep running
   with the additive v2 tables and columns present.

### Build And Stage Artifacts

Build and verify the release:

```bash
make all
make test
make test-race
make lint
make rollout-docs-verify
```

Stage these artifacts for each target host:

- `bin/jetmon2`, installed at the path expected by the service unit
  (`/opt/jetmon2/jetmon2` for the sample unit)
- `bin/veriflier2` when that host also owns a Veriflier deployment
- `systemd/jetmon2.service`
- `systemd/jetmon2-logrotate`
- `config/config.json`
- `/opt/jetmon2/config/jetmon2.env` from `config/db-config-sample.conf`

Keep v2 files in `/opt/jetmon2` or another v2-specific directory. Do not
overwrite the v1 install until rollback signoff.

Do not start `bin/jetmon-deliverer` during the initial monitor replacement
unless standalone delivery is part of the approved rollout plan. Use
[`jetmon-deliverer-rollout.md`](jetmon-deliverer-rollout.md) for that separate
process cutover.

After the binary and service files are staged, the pre-stop
`rollout host-preflight` gate verifies the installed service unit before v1 is
stopped. If you want an earlier packaging check from that staged host or
deployment root, run:

```bash
systemd-analyze verify /etc/systemd/system/jetmon2.service
```

If this check is run directly against the repository copy before installing the
binary to `/opt/jetmon2`, systemd can report missing `ExecStart` paths. Treat
that as a packaging reminder and re-run the check after the final paths exist.

### Prepare Pinned v2 Config

For each replacement host, configure the exact v1 bucket range:

```json
{
  "PINNED_BUCKET_MIN": 0,
  "PINNED_BUCKET_MAX": 99,
  "LEGACY_STATUS_PROJECTION_ENABLE": true,
  "API_PORT": 0
}
```

The legacy v1 names `BUCKET_NO_MIN` and `BUCKET_NO_MAX` are accepted as aliases,
but prefer `PINNED_BUCKET_MIN` and `PINNED_BUCKET_MAX` in v2 configs so the
deployment mode is explicit.

While pinned:

- the host checks only the configured inclusive bucket range
- the host does not claim or heartbeat `jetmon_hosts`
- shutdown does not release a `jetmon_hosts` row
- `BUCKET_TOTAL`, `BUCKET_TARGET`, and `BUCKET_HEARTBEAT_GRACE_SEC` still
  validate, but dynamic ownership does not use them on that host

### Validate Before Cutover

Run validation with the same `DB_*` environment the service will use:

```bash
./jetmon2 validate-config
```

Confirm it reports:

- `legacy_status_projection=enabled`
- `bucket_ownership=pinned range=<min>-<max>`
- `rollout_static_plan=./jetmon2 rollout static-plan-check --file=<ranges.csv>`
- `rollout_preflight=` points at `./jetmon2 rollout host-preflight` with the
  static plan file, v1 host, runtime v2 host, and pinned bucket range
- `rollout_activity_check=./jetmon2 rollout activity-check --since=15m`
- `rollout_cutover_check=./jetmon2 rollout cutover-check --since=15m`
- `rollout_rollback_check=./jetmon2 rollout rollback-check`
- `rollout_drift_report=./jetmon2 rollout projection-drift`

Run the host preflight when the host identity and config are final:

```bash
./jetmon2 rollout host-preflight \
  --file=rollout-buckets.csv \
  --host=<v1-hostname> \
  --runtime-host=<v2-hostname> \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --bucket-total=<total>
```

This gate fails if the copied static plan does not match the requested host
range, the staged config cannot load, DB connectivity fails, pinned config is
missing, the pinned config range does not match the requested range, legacy
projection writes are disabled, the runtime v2 host still owns a dynamic
`jetmon_hosts` row, any dynamic `jetmon_hosts` row overlaps the pinned range,
projection drift exists, or the staged systemd unit fails validation.

### Rehearse API CLI Workflows Outside Production

Use the API CLI in Docker, staging, or a dedicated rehearsal database with
disposable sites. Do not enable `API_PORT` on initial production monitor hosts
unless the delivery-owner plan has been approved.

```bash
./jetmon2 keys create --consumer api-cli-rehearsal --scope admin --created-by rollout-rehearsal

export JETMON_API_URL=http://<rehearsal-host>:8090
export JETMON_API_TOKEN=jm_replace_with_the_printed_token

./bin/jetmon2 api health --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api smoke --batch rollout-rehearsal --pretty
./bin/jetmon2 api sites simulate-failure \
  --batch rollout-rehearsal \
  --mode http-500 \
  --wait 30s \
  --expect-event-state 'Seems Down' \
  --expect-transition-reason opened \
  --pretty
./bin/jetmon2 api sites cleanup --batch rollout-rehearsal --count 3 --output table
```

When the Docker-local fixture and delivery workers are enabled, also exercise
the webhook path:

```bash
./bin/jetmon2 api smoke --batch rollout-webhook --exercise webhook --pretty
```

For a fuller Docker-local pass against the feature-guide examples, failure
fixture, webhook receiver, signature verification, and cleanup path, run:

```bash
make api-cli-validate
```

Set `API_VALIDATE_SKIP_WEBHOOK=1` when the environment does not have outbound
delivery workers enabled. Any API CLI write against a non-local API URL must
use `--allow-remote`, and remote smoke, bulk-add, cleanup, and failure
simulation must also use `--batch`.

## Phase 1A: Replace v1 On The Existing Server

Use this path when the same server currently running v1 will run v2 for the
same bucket range.

Preferred: run `./jetmon2 rollout guided ...` with the same host, range, stop,
and rollback commands from the generated rehearsal plan. The manual steps below
are the fallback/reference path and match what the guided command walks through.

1. Confirm v2 files and config are staged beside, not on top of, v1.
2. Confirm v1 service stop/start commands and config are documented for
   cutover and rollback.
3. Run `./jetmon2 validate-config`.
4. Run the pre-stop host gate:

   ```bash
   ./jetmon2 rollout host-preflight \
     --file=rollout-buckets.csv \
     --host=<v1-hostname> \
     --runtime-host=<v2-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max> \
     --bucket-total=<total>
   ```

5. Start a terminal watching v1 logs and a terminal ready to watch v2 logs.
6. Stop v1 cleanly with the existing production command.
7. Confirm the v1 process is no longer running.
8. Start v2:

   ```bash
   systemctl enable --now jetmon2
   ```

9. Confirm v2 logs show:

   - `legacy_status_projection=enabled`
   - `bucket_ownership=pinned range=<min>-<max>`
   - `orchestrator: using pinned buckets <min>-<max>`

10. Run:

    ```bash
    ./jetmon2 rollout cutover-check \
      --host=<v2-hostname> \
      --bucket-min=<min> \
      --bucket-max=<max> \
      --since=15m
    ```

    `cutover-check` runs the pinned preflight, recent activity check,
    dashboard status check, and projection-drift report. Its activity section
    proves the range has fresh `last_checked_at` writes, not which process
    wrote them. Keep v1 stopped and use logs or the dashboard to confirm v2 is
    checking only the pinned range.
11. After one full expected round, run:

    ```bash
    ./jetmon2 rollout cutover-check \
      --host=<v2-hostname> \
      --bucket-min=<min> \
      --bucket-max=<max> \
      --since=15m \
      --require-all
    ```

12. Watch one full check round before moving to the next host.

## Phase 1B: Move A v1 Range To A Fresh Server

Use this path when a new server will take over a bucket range from an existing
v1 server.

Preferred: run `./jetmon2 rollout guided --mode=fresh-server ...` from the new
v2 server, with `--host=<old-v1-hostname>` and
`--runtime-host=<new-v2-hostname>`. The manual steps below are the
fallback/reference path.

1. Provision the new server and install v2 artifacts.
2. Configure `PINNED_BUCKET_MIN` and `PINNED_BUCKET_MAX` to match the old v1
   host's `BUCKET_NO_MIN` and `BUCKET_NO_MAX`.
3. Keep the v2 service stopped.
4. Run `./jetmon2 validate-config` on the new server.
5. Run the pre-stop host gate from the new v2 server before stopping v1:

   ```bash
   ./jetmon2 rollout host-preflight \
     --file=rollout-buckets.csv \
     --host=<old-v1-hostname> \
     --runtime-host=<new-v2-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max> \
     --bucket-total=<total>
   ```

6. Confirm network access from the new server to MySQL, Verifliers, WPCOM,
   StatsD, and log/stats directories.
7. Stop v1 on the old server.
8. Confirm the old v1 process is no longer running.
9. Start v2 on the new server:

   ```bash
   systemctl enable --now jetmon2
   ```

10. Run the cutover smoke gate on the new server:

    ```bash
    ./jetmon2 rollout cutover-check \
      --host=<new-v2-hostname> \
      --bucket-min=<min> \
      --bucket-max=<max> \
      --since=15m
    ```

11. After one full expected v2 round, run the stronger gate:

    ```bash
    ./jetmon2 rollout cutover-check \
      --host=<new-v2-hostname> \
      --bucket-min=<min> \
      --bucket-max=<max> \
      --since=15m \
      --require-all
    ```
12. Watch one full check round before moving to the next host.

Do not leave the old v1 server running as a warm standby for the same range. A
standby is safe only when the monitor process is stopped.

## Phase 2: Monitor Each Cutover

For every replaced range, verify:

- checks run only for the pinned range
- round time and sites-per-second are within the expected envelope
- local checks use GET semantics against customer sites
- Veriflier confirmation works
- WPCOM notifications retain the v1 payload shape
- `jetmon_events` receives event rows
- `jetmon_event_transitions` receives transition rows for each mutation
- `jetpack_monitor_sites.site_status` and `last_status_change` update while
  legacy projection is enabled
- no unexpected row is claimed in `jetmon_hosts` by a pinned host
- no projection drift is reported:

  ```bash
  ./jetmon2 rollout projection-drift \
    --bucket-min=<min> \
    --bucket-max=<max> \
    --limit=100
  ```

- recent check activity exists for the pinned range:

  ```bash
  ./jetmon2 rollout activity-check \
    --bucket-min=<min> \
    --bucket-max=<max> \
    --since=15m
  ```

  After a full expected round, require every active site in the range to have a
  fresh `last_checked_at`:

  ```bash
  ./jetmon2 rollout activity-check \
    --bucket-min=<min> \
    --bucket-max=<max> \
    --since=15m \
    --require-all
  ```

  The bundled cutover check runs the pinned preflight, activity check,
  dashboard status check, and projection-drift report together:

  ```bash
  ./jetmon2 rollout cutover-check \
    --host=<v2-hostname> \
    --bucket-min=<min> \
    --bucket-max=<max> \
    --since=15m
  ./jetmon2 rollout cutover-check \
    --host=<v2-hostname> \
    --bucket-min=<min> \
    --bucket-max=<max> \
    --since=15m \
    --require-all
  ```

If `DASHBOARD_PORT` is enabled, confirm:

- bucket ownership mode is pinned
- dependency health is green for MySQL, configured Verifliers, WPCOM, StatsD,
  and log/stats directory writes
- WPCOM circuit breaker is closed
- retry queue depth is not growing unexpectedly
- RSS stays below the configured guardrail
- delivery workers are disabled unless explicitly approved

Useful direct checks:

```bash
./jetmon2 status
tail -f logs/jetmon.log
tail -f logs/status-change.log
cat stats/sitespersec
cat stats/sitesqueue
cat stats/totals
```

## Phase 3: Revert Safely

### Revert On The Existing Server

Use this when v2 replaced v1 on the same server.

Preferred: run `./jetmon2 rollout guided --rollback ...` with the original v1
start command. The manual steps below are the fallback/reference path.

1. Stop v2:

   ```bash
   systemctl stop jetmon2
   ```

2. Confirm the v2 process is stopped. Do not restart v1 until this is true.
3. Run the rollback safety check before restarting v1:

   ```bash
   ./jetmon2 rollout rollback-check \
     --host=<v2-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max>
   ```

   Pinned v2 hosts intentionally do not heartbeat `jetmon_hosts`, so this check
   cannot prove the pinned v2 process is stopped. It verifies the rollback range
   has no dynamic ownership overlap and no legacy projection drift; the process
   stop still needs explicit confirmation.
4. Restart the original v1 service with its original `BUCKET_NO_MIN` /
   `BUCKET_NO_MAX` config.
5. Verify v1 checks the range again.
6. Watch WPCOM notifications and legacy logs for one full v1 check round.
7. Leave v2 schema in place. Do not attempt schema rollback.

### Revert A Fresh-Server Takeover

Use this when v2 was started on a new server and the old v1 server was stopped.

Preferred: run `./jetmon2 rollout guided --rollback ...` from the new v2 server
with `--host=<old-v1-hostname>` and `--runtime-host=<new-v2-hostname>`. The
manual steps below are the fallback/reference path.

1. Stop v2 on the new server:

   ```bash
   systemctl stop jetmon2
   ```

2. Confirm the new v2 process is stopped. Do not restart v1 until this is true.
3. Run the rollback safety check from an operator shell with the stopped v2
   hostname:

   ```bash
   ./jetmon2 rollout rollback-check \
     --host=<new-v2-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max>
   ```

4. Restart v1 on the old server with its original bucket config.
5. Verify v1 checks the range again.
6. Keep the new v2 server disabled until the next approved attempt.

Never start the old v1 process until the new v2 process is stopped for that
range.

## Phase 4: Complete The Fleet Rollout

After every monitor host is on v2 and stable in pinned mode:

1. Confirm no v1 monitor process remains active.
2. Confirm every v2 host passes:

   ```bash
   ./jetmon2 rollout cutover-check --since=15m --require-all
   ```

3. Observe the fleet for the agreed stabilization window.
4. Plan a coordinated dynamic-ownership cutover. Pinned hosts do not write
   `jetmon_hosts`, so do not leave a long-lived mix of pinned and dynamic v2
   hosts.
5. Remove `PINNED_BUCKET_MIN` / `PINNED_BUCKET_MAX` and any legacy
   `BUCKET_NO_MIN` / `BUCKET_NO_MAX` aliases from every v2 monitor config.
6. Restart the v2 monitor fleet in the approved window.
7. Run:

   ```bash
   ./jetmon2 validate-config
   ./jetmon2 rollout dynamic-check
   ./jetmon2 rollout activity-check --since=15m --require-all
   ./jetmon2 rollout projection-drift --limit=100
   ```

8. Confirm `jetmon_hosts` coverage is active, fresh, gap-free, and
   overlap-free.
9. Continue with normal v2 rolling updates: stop one host, deploy, start it,
   verify `./jetmon2 status`, then move to the next host.

## Phase 5: Tear Down v1

Only remove v1 after rollout signoff.

1. Archive final v1 configs, service units, and deployment metadata according
   to normal retention policy.
2. Confirm no process manager references the v1 service.
3. Remove old v1 service units or disable them permanently.
4. Remove old Node.js application checkouts, `node_modules`, compiled native
   addons, Qt Veriflier artifacts, and v1-only logrotate files.
5. Remove v1-only deployment hooks from host automation.
6. Keep shared log and stats paths only if v2 still writes to them.
7. Keep v2 additive database schema. Do not remove compatibility columns while
   legacy consumers still read them.
8. Keep `LEGACY_STATUS_PROJECTION_ENABLE=true` until legacy readers have moved
   to v2 state surfaces. Retiring that projection is a separate project.

## Final Checklist

- [ ] v1 host inventory complete
- [ ] bucket ranges complete and non-overlapping
- [ ] `rollout static-plan-check` passes for the approved v1 bucket plan
- [ ] DB backup and restore path confirmed
- [ ] v2 binaries built and tested
- [ ] additive migrations applied
- [ ] pinned configs prepared for every range
- [ ] rollback commands documented for every host
- [ ] `rollout guided --dry-run` exercised for the first host
- [ ] `rollout host-preflight` passes before each v1 host is stopped
- [ ] first host cutover observed for one full round
- [ ] `rollout cutover-check --require-all` passes for replaced ranges
- [ ] `rollout rollback-check` exercised during rehearsal
- [ ] all hosts running v2 pinned
- [ ] dynamic ownership cutover completed
- [ ] `rollout dynamic-check` passes
- [ ] projection drift is zero
- [ ] v1 artifacts retained through rollback window
- [ ] v1 artifacts removed after signoff

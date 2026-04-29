# v1 to v2 Rollout Quick Reference

This is the short operator checklist for a production v1-to-v2 monitor rollout.
Use the full [migration runbook](v1-to-v2-migration.md) for preparation,
approval, troubleshooting, revert details, and final v1 teardown.

Unless a command explicitly targets the old v1 host, run it from the staged v2
host with the same `DB_*` environment the `jetmon2` service will use. Shell
commands do not automatically inherit systemd's `EnvironmentFile`.

## Guided Path

Prefer the guided command during the production window. It checks that the
rollout log directory is writable before it starts, writes a transcript and
resume state file, explains each step, asks before proceeding, uses typed
confirmations for v1/v2 stop/start transitions, and stops on failed gates.
If the command is interrupted after a stop/start transition, resuming with the
same options uses the saved service state to avoid repeating an already
completed transition. When resume state exists, the command has no default
choice; the operator must type `RESUME` or `START OVER`. Short `y` / `n`
answers are rejected for this prompt.

```bash
./jetmon2 rollout guided \
  --file=<ranges.csv> \
  --host=<v1-hostname> \
  --runtime-host=<v2-hostname> \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --bucket-total=<total> \
  --mode=same-server \
  --v1-stop-command='<exact v1 stop command>' \
  --v1-start-command='<exact v1 rollback start command>' \
  --log-dir=logs/rollout
```

By default, guided rollout prints v1/v2 stop/start commands and asks the
operator to confirm when they have been run. Add `--execute-operator-commands`
only when the operator wants the command to execute those stop/start commands
after typed confirmation. Use `--dry-run` to verify the selected path, log
paths, service commands, typed confirmation phrases, and manual `DONE`
checkpoints without running rollout checks or service commands.

To return a range to v1, run the guided rollback path:

```bash
./jetmon2 rollout guided \
  --rollback \
  --file=<ranges.csv> \
  --host=<v1-hostname> \
  --runtime-host=<v2-hostname> \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --bucket-total=<total> \
  --v1-start-command='<exact v1 rollback start command>'
```

If a forward gate fails after v2 has started and the operator chooses guided
rollback, the rollback path can complete successfully while the overall command
still exits non-zero. Treat that as "rollout did not complete; range returned
to v1" and keep the transcript with the incident record.

## Before The First Host

1. Confirm the approved static bucket plan exists as a reusable CSV:

   ```bash
   ./jetmon2 rollout static-plan-check \
     --file=<ranges.csv> \
     --host=<v1-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max> \
     --bucket-total=<total>
   ```

2. Generate the exact host command sequence:

   ```bash
   ./jetmon2 rollout rehearsal-plan \
     --file=<ranges.csv> \
     --host=<v1-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max> \
     --bucket-total=<total> \
     --mode=same-server \
     --v1-stop-command='<exact v1 stop command>' \
     --v1-start-command='<exact v1 rollback start command>'
   ```

   Use `--mode=fresh-server --runtime-host=<new-v2-hostname>` for a fresh v2
   server taking over from an existing v1 server. Add `--systemd-unit=<path>`
   when the staged service unit is not `/etc/systemd/system/jetmon2.service`.

3. Validate config, migrations, static plan match, pinned safety, and the
   staged systemd service:

   ```bash
   ./jetmon2 validate-config
   ./jetmon2 migrate
   ./jetmon2 rollout host-preflight \
     --file=<ranges.csv> \
     --host=<v1-hostname> \
     --runtime-host=<v2-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max> \
     --bucket-total=<total>
   ```

## Per-Host Cutover

1. Confirm the pre-stop host gate passes:

   ```bash
   ./jetmon2 rollout host-preflight \
     --file=<ranges.csv> \
     --host=<v1-hostname> \
     --runtime-host=<v2-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max> \
     --bucket-total=<total>
   ```

2. Stop the v1 monitor for that bucket range.
3. Confirm the v1 process is stopped, then start v2:

   ```bash
   systemctl enable --now jetmon2
   ```

4. Immediately run the smoke gate:

   ```bash
   ./jetmon2 rollout cutover-check \
     --host=<v2-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max> \
     --since=15m
   ```

   This confirms startup and recent activity, but recent writes can still
   include v1 because the cutoff reaches back before cutover.

5. After one full expected v2 check round, run the stronger gate:

   ```bash
   ./jetmon2 rollout cutover-check \
     --host=<v2-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max> \
     --since=15m \
     --require-all
   ```

6. Watch logs, dashboard health, WPCOM notification parity, event rows, and
   projection drift before moving to the next host.

## Rollback Gate

Before restarting v1 for a range, stop v2 and run:

```bash
./jetmon2 rollout rollback-check \
  --host=<v2-hostname> \
  --bucket-min=<min> \
  --bucket-max=<max>
```

Only restart v1 after the v2 process is stopped and the rollback check passes.
Do not roll back schema migrations.

## Fleet Completion

After every monitor host is stable on v2 pinned mode:

```bash
./jetmon2 rollout cutover-check --since=15m --require-all
```

Then remove `PINNED_BUCKET_MIN` / `PINNED_BUCKET_MAX` and legacy
`BUCKET_NO_MIN` / `BUCKET_NO_MAX` aliases from every v2 monitor config,
restart the fleet in the approved window, and run:

```bash
./jetmon2 validate-config
./jetmon2 rollout dynamic-check
./jetmon2 rollout activity-check --since=15m --require-all
./jetmon2 rollout projection-drift --limit=100
```

## Automation

Rollout gate commands support JSON output:

```bash
./jetmon2 rollout cutover-check --since=15m --require-all --output=json
```

Automation should gate on both the process exit code and the JSON `ok` field.
The human runbook remains the source of truth for what to do when a gate fails.

For a quick operator snapshot, run:

```bash
./jetmon2 rollout state-report --since=15m
```

This summarizes ownership mode, bucket coverage, activity freshness, projection
drift, delivery-owner state, and the suggested next action.

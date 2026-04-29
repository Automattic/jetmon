# v1 to v2 Rollout Quick Reference

This is the short operator checklist for a production v1-to-v2 monitor rollout.
Use the full [migration runbook](v1-to-v2-migration.md) for preparation,
approval, troubleshooting, revert details, and final v1 teardown.

## Before The First Host

1. Confirm the approved static bucket plan exists as a reusable CSV:

   ```bash
   ./jetmon2 rollout static-plan-check \
     --file=<ranges.csv> \
     --host=<v1-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max>
   ```

2. Generate the exact host command sequence:

   ```bash
   ./jetmon2 rollout rehearsal-plan \
     --file=<ranges.csv> \
     --host=<v1-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max> \
     --mode=same-server
   ```

   Use `--mode=fresh-server --runtime-host=<new-v2-hostname>` for a fresh v2
   server taking over from an existing v1 server.

3. Validate config, migrations, and the staged systemd service:

   ```bash
   ./jetmon2 validate-config
   ./jetmon2 migrate
   systemd-analyze verify /etc/systemd/system/jetmon2.service
   ```

## Per-Host Cutover

1. Confirm the v2 host is pinned to the v1 range and not participating in
   dynamic bucket ownership:

   ```bash
   ./jetmon2 rollout pinned-check --host=<v2-hostname>
   ```

2. Stop the v1 monitor for that bucket range.
3. Start v2:

   ```bash
   systemctl enable --now jetmon2
   ```

4. Immediately run:

   ```bash
   ./jetmon2 rollout cutover-check --host=<v2-hostname> --since=15m
   ```

5. After one full expected check round, run:

   ```bash
   ./jetmon2 rollout cutover-check --host=<v2-hostname> --since=15m --require-all
   ```

6. Watch logs, dashboard health, WPCOM notification parity, event rows, and
   projection drift before moving to the next host.

## Rollback Gate

Before restarting v1 for a range, stop v2 and run:

```bash
./jetmon2 rollout rollback-check --host=<v2-hostname>
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

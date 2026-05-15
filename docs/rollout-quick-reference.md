# v1 to v2 Rollout Quick Reference

This is the short operator checklist for a production v1-to-v2 monitor rollout.
Use the full [migration runbook](v1-to-v2-migration.md) for preparation,
approval, troubleshooting, revert details, and final v1 teardown.
Use the [prelaunch readiness tracker](jetmon-v2-prelaunch-readiness.md) as the
single launch-critical checklist. The first production activation should not
start until the launch posture, parity gates, observability thresholds,
failure drills, and synthetic canary tests have written evidence.

The preferred production rollout is now API-driven: deploy fresh v2 Veriflier
and Monitor containers beside the existing v1 fleet, keep Monitors in
`ROLLOUT_MODE=api-controlled` standby,
then activate bucket ranges through an authenticated Monitor API after Systems
stops the matching v1 range. A standalone `jetmon2` binary can run from an
operator workstation or bastion; direct shell access to the container hosts is
not required for the control-plane steps.

Configure the operator CLI with `~/.config/jetmon2.conf` or
`JETMON_API_CONFIG=/path/to/jetmon2.conf`:

```bash
./jetmon2 local-config init \
  --base-url=https://jetmon-v2-api.example.com \
  --token-file=jetmon2-api-token \
  --default-output=table
./jetmon2 local-config show
./jetmon2 local-config keys
```

```conf
base_url = https://jetmon-v2-api.example.com
token_file = jetmon2-api-token
auth_policy = same-origin
timeout = 30s
output = table
```

Keep the config and token file mode `0600`. Production writes to a remote API
still require `--allow-remote`.

## API-Driven Container Path

Preferred interactive wrapper:

```bash
./jetmon2 api rollout guided \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --change-ref=<ticket-or-change-id> \
  --allow-remote
```

Use `--dry-run` to print every API request and typed confirmation without
contacting the API. Use `--rollback` to walk the release path when an activated
range must return to v1 standby. After v2 is stable, add
`--include-comparison` and `--include-policy-migration` to include the
non-authoritative HEAD/GET comparison and staged policy planning steps.
Non-dry-run guided sessions write a transcript and local resume state under
`logs/api-rollout`; use `--resume` with the same range if the operator process
is interrupted.

The guided command wraps the API primitives below and stops at each gate until
the operator types the requested confirmation:

1. Systems applies the additive v2 schema.
2. Deploy the fresh v2 Veriflier fleet and validate `/v2/status`, stable
   `vantage.id` values, auth, capacity, and quorum.
3. Deploy v2 Monitors in `ROLLOUT_MODE=api-controlled` with `HEAD` + `legacy`
   defaults.
4. Run API preflight and read-only smoke checks from the operator CLI:

   ```bash
   ./jetmon2 api rollout preflight --bucket-min=<min> --bucket-max=<max> --allow-remote
   ./jetmon2 api rollout smoke --bucket-min=<min> --bucket-max=<max> --mode=head-legacy --sample-size=100 --read-only --allow-remote
   ```

   Preflight validates the configured v2 Veriflier contract and unique
   quorum-counted `vantage.id` coverage. Smoke runs sampled standby probes and
   remains read-only for incident state, runtime freshness, check history,
   WPCOM notifications, and the legacy projection.

5. Seed/adopt v2 side state without sending duplicate notifications:

   ```bash
   ./jetmon2 api rollout seed --bucket-min=<min> --bucket-max=<max> --dry-run --allow-remote
   ./jetmon2 api rollout seed --bucket-min=<min> --bucket-max=<max> --execute --confirm=<token> --allow-remote
   ```

6. After Systems stops the matching v1 bucket range, run final reconcile and
   explicitly activate that range in v2:

   ```bash
   ./jetmon2 api rollout final-reconcile --bucket-min=<min> --bucket-max=<max> --dry-run --allow-remote
   ./jetmon2 api rollout final-reconcile --bucket-min=<min> --bucket-max=<max> --execute --confirm=<token> --allow-remote
   ./jetmon2 api rollout activate-buckets --bucket-min=<min> --bucket-max=<max> --dry-run --allow-remote
   ./jetmon2 api rollout activate-buckets --bucket-min=<min> --bucket-max=<max> --execute --confirm=<token> --allow-remote
   ```

   A single Monitor owner may hold only one contiguous API-controlled range at
   a time. Use separate Monitor hosts for separate ranges, or release the
   current range before activating a different range for the same host.

7. Hold for health gates:

   ```bash
   ./jetmon2 api rollout status --allow-remote
   ./jetmon2 api rollout bucket-coverage --bucket-min=<min> --bucket-max=<max> --allow-remote
   ./jetmon2 api rollout activity-check --bucket-min=<min> --bucket-max=<max> --since=15m --allow-remote
   ./jetmon2 api rollout projection-drift --bucket-min=<min> --bucket-max=<max> --allow-remote
   ```

   Also run the approved synthetic canary sequence and attach the evidence to
   the rollout record. Until canary execution is built into the API rollout
   gate, this remains a separate required check covering known-up, controlled
   down, controlled recovery, WPCOM notification parity, Veriflier-confirmed
   down, and WAF/blocked-style behavior.

8. Roll back by releasing the v2 range before Systems restarts v1:

   ```bash
   ./jetmon2 api rollout release-buckets --bucket-min=<min> --bucket-max=<max> --dry-run --allow-remote
   ./jetmon2 api rollout release-buckets --bucket-min=<min> --bucket-max=<max> --execute --confirm=<token> --allow-remote
   ```

9. After all buckets are stable on v2, run non-authoritative comparison checks
   and staged policy transitions:

   ```bash
   ./jetmon2 api rollout compare-methods --bucket-min=<min> --bucket-max=<max> --from=head-legacy --to=get-simple --sample-size=100 --allow-remote
   ./jetmon2 api rollout stage-policy --bucket-min=<min> --bucket-max=<max> --method=GET --profile=simple_http --size=1000 --dry-run --allow-remote
   ./jetmon2 api rollout stage-policy --bucket-min=<min> --bucket-max=<max> --method=GET --profile=simple_http --size=1000 --execute --confirm=<token> --allow-remote
   ./jetmon2 api rollout stage-policy --bucket-min=<min> --bucket-max=<max> --method=GET --profile=full --size=1% --dry-run --allow-remote
   ./jetmon2 api rollout stage-policy --bucket-min=<min> --bucket-max=<max> --mode=rollback-last-stage --dry-run --allow-remote
   ./jetmon2 api rollout stage-policy --bucket-min=<min> --bucket-max=<max> --mode=rollback-all --dry-run --allow-remote
   ```

The API commands above are the target control-plane surface behind the guided
containerized rollout. They must remain idempotent, audited, admin-scoped, and
protected by dry-run plans plus generated confirmation tokens. The guided
wrapper also sends idempotency keys on execute steps, so a lost HTTP response
can be retried without re-running the mutation. Confirmation tokens are bound
to the authenticated API key identity, not just the typed phrase shown to the
operator.

## Host-Based Fallback Path

Run this runbook from the staged v2 runtime host for the bucket range. Do not
run it from a separate orchestration host unless that host is also the intended
v2 runtime host and has the same `DB_*` environment the `jetmon2` service will
use. Shell commands do not automatically inherit systemd's `EnvironmentFile`.

- Same-server rollout: `--host` and `--runtime-host` are normally the same
  hostname, and local service commands stop v1/start v2 on that host.
- Fresh-server rollout: run the guided command on the new v2
  `--runtime-host`, while `--host` names the old v1 host from the static plan.
  The v2 runtime host must have SSH access to the old v1 host when
  `--v1-stop-command` / `--v1-start-command` use `ssh` to stop or restart v1.

## Guided Path

Prefer the guided command during the production window. It checks that the
rollout log directory is writable before it starts, writes a transcript and
resume state file, explains each step, asks before proceeding, uses typed
confirmations for v1/v2 stop/start transitions, and stops on failed gates.
The guided command prints `guided_run_origin=runtime_host` and, in
fresh-server mode, warns when remote v1 access is required.
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
After the full-round v2 gate, the guided flow also captures a read-only WPCOM
parity telemetry report so the transcript includes notification and
operator-explanation evidence.

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

1. Verify that the documented operator flow still matches the CLI output:

   ```bash
   make rollout-rehearsal-verify
   ```

   This is a no-database dry-run gate for the generated same-server,
   fresh-server, and rollback flows. The broader `make rollout-docs-verify`
   target also runs it after build, test, lint, command-help, JSON, and staged
   systemd checks. It uses a disposable sample bucket plan and does not replace
   the real `host-preflight` gate or VM lab rehearsal.

2. Confirm the approved static bucket plan exists as a reusable CSV:

   ```bash
   ./jetmon2 rollout static-plan-check \
     --file=<ranges.csv> \
     --host=<v1-hostname> \
     --bucket-min=<min> \
     --bucket-max=<max> \
     --bucket-total=<total>
   ```

3. Generate the exact host command sequence:

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

4. Validate config, migrations, static plan match, pinned safety, and the
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

5. Deploy the new `veriflier2` fleet first and confirm it serves the v2
   contract from the v2 runtime host:

   ```bash
   ./jetmon2 validate-config
   curl -fsS http://<veriflier-host>:7803/v2/status
   ```

   The preferred migration uses fresh v2 Veriflier endpoints. Keep v1 Monitors
   pointed at the original v1 Verifliers until monitor cutover, and point v2
   Monitors only at the new `veriflier2` fleet. The original v1 Veriflier uses
   the old TLS/custom transport; the v2 Monitor's legacy `/check` fallback is
   only for `veriflier2`'s opt-in compatibility endpoint. Keep
   `VERIFLIER_ENABLE_LEGACY_HTTP=false` unless the endpoint is part of an
   explicit lab or emergency compatibility test. Roll one v2 endpoint at a time
   and leave database credentials unset on Veriflier hosts.

   `/v2/status` should advertise `v2-json-http`, a stable `vantage.id`, the
   serving `agent.id`, and non-zero capacity. Horizontally scaled replicas behind
   one endpoint must share the same `vantage.id`; do not add each replica as a
   separate monitor-side Veriflier unless it should count as an independent
   quorum vote. `validate-config` fails missing or duplicate v2 vantage IDs and
   warns on unreachable or legacy-only Verifliers.

   This is separate from the staged site check policy. The initial replacement
   phase can default all sites to `HEAD` + `legacy` probe behavior while remote
   confirmation still uses `POST /v2/check`; it does not require enabling
   `veriflier2`'s legacy-compatible `/check` endpoint.

   For auto-discovery, keep `VERIFLIER_DISCOVERY_MODE=shadow` until the
   registry matches the static `VERIFIERS` fleet. Seed
   `jetmon_veriflier_vantages` with one enabled row per trusted quorum vantage;
   do not rely on `jetmon_veriflier_agents` telemetry alone, because agent rows
   never create trusted votes. Move to `active` only after
   `validate-config` and the read-only discovery report show usable registry
   vantages and no shadow drift:

   ```bash
   ./jetmon2 verifliers discovery-report --output=text
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
   systemctl enable --now jetmon2 && systemctl is-active --quiet jetmon2
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

6. Capture WPCOM down/recovery parity and operator-explanation evidence:

   ```bash
   ./jetmon2 telemetry report --since=15m
   ```

   This is a read-only window-level report. Treat warnings as rollout hold
   points, and widen `--since` when the current range is too quiet to prove
   parity.

7. Watch logs, the host dashboard, `/fleet`, WPCOM notification parity, event
   rows, and projection drift before moving to the next host. In pinned rollout,
   `/fleet` should show pinned bucket mode as amber rather than dynamic green.

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

Run that pinned `cutover-check` from each v2 runtime host, or pass that host's
explicit `--host`, `--bucket-min`, and `--bucket-max`. It is a per-range
signoff, not the dynamic fleet-wide coverage check.

Then remove `PINNED_BUCKET_MIN` / `PINNED_BUCKET_MAX` and legacy
`BUCKET_NO_MIN` / `BUCKET_NO_MAX` aliases from every v2 monitor config,
restart the fleet in the approved window, and run:

```bash
./jetmon2 validate-config
./jetmon2 rollout dynamic-check
./jetmon2 rollout activity-check --since=15m --require-all
./jetmon2 rollout projection-drift --limit=100
./jetmon2 telemetry report --since=15m
```

When `projection-drift` fails, start with the summary and cause lines before
the row table. The command is read-only and gives repair guidance; it does not
change `site_status` automatically.

## Production Data Audit

Before the first host window, run the read-only production-data audit against
the approved bucket range:

```bash
./jetmon2 rollout production-data-audit --bucket-min=0 --bucket-max=<max>
```

Resolve hard blockers before rollout. In particular, active duplicate
`blog_id` rows are not safe for the current per-blog runtime identity model.
Existing active non-running v1 projections are expected in production, but they
must be represented in v2 events before `projection-drift` becomes a hard gate:

```bash
./jetmon2 rollout legacy-status-bootstrap --bucket-min=0 --bucket-max=<max>
./jetmon2 rollout legacy-status-bootstrap --bucket-min=0 --bucket-max=<max> --execute
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

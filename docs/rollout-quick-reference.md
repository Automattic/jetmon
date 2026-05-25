# v1 to v2 Rollout Quick Reference

This is the short operator checklist for the production v1-to-v2 Monitor
rollout. Use [v1-to-v2-migration.md](v1-to-v2-migration.md) for the full
runbook, rollback detail, troubleshooting, policy migration background, and v1
teardown. Use [jetmon-v2-prelaunch-readiness.md](jetmon-v2-prelaunch-readiness.md)
as the launch-critical readiness checklist.

The preferred production path is API-driven:

1. Deploy fresh v2 Verifliers.
2. Deploy v2 Monitors beside v1 in API-controlled standby.
3. Run read-only validation through a standalone `jetmon2` operator CLI.
4. Activate bucket ranges only after Systems stops the matching v1 ownership.
5. Observe each range before expanding.
6. Keep initial site checks at `HEAD` + `legacy`.
7. Later migrate cohorts to `GET` + `simple_http`, then `GET` + `full`.

## Operator CLI Config

Configure a standalone `jetmon2` binary with `~/.config/jetmon2.conf` or
`JETMON_API_CONFIG=/path/to/jetmon2.conf`:

```bash
./jetmon2 local-config init \
  --base-url=https://jetmon-v2-api.example.com \
  --token-file=jetmon2-api-token \
  --default-output=table

./jetmon2 local-config show
./jetmon2 local-config keys
```

Example config:

```conf
base_url = https://jetmon-v2-api.example.com
token_file = jetmon2-api-token
auth_policy = same-origin
timeout = 30s
output = table
```

Keep the config and token file mode `0600`. Production writes to a remote API
require `--allow-remote`.

## Before First Activation

Hard gates before any v2 range activation:

- production schema changes are applied and validated,
- prelaunch readiness checklist has owners and evidence,
- v2 Verifliers pass `/v2/status` checks with stable unique `vantage.id` values,
- v2 Monitors are deployed in standby/API-controlled mode,
- WPCOM notifications remain in legacy mode,
- canary sites or uptime-bench fixtures are approved for smoke checks,
- rollback ownership path is agreed with Systems.
- WPCOM provisioning has been validated against the planned bucket space.

Recommended checks:

```bash
./jetmon2 schema validate
./jetmon2 validate-config
./jetmon2 doctor --require-statsd
./jetmon2 verifliers discovery-report --output=text
./jetmon2 rollout state-report --since=15m
```

Run the read-only production data audit before the first window:

```bash
./jetmon2 rollout production-data-audit --bucket-min=0 --bucket-max=<max>
```

If the audit reports a WPCOM-style `0-511` bucket space while v2 uses a wider
`BUCKET_TOTAL`, do not activate empty higher buckets unless WPCOM bucket
assignment and rebalancing have been approved. For a compatibility-first
cutover, keep the first activation plan aligned to the WPCOM-populated bucket
space.

Also validate one WPCOM-style provisioning row before the window: insert or
activate a row in `jetpack_monitor_sites` with no v2 sidecars, then confirm v2
picks it up and creates runtime freshness after activation. This proves the
direct-table WPCOM path still works.

If existing active non-running v1 projections must be adopted into v2 events
before projection drift becomes a hard gate:

```bash
./jetmon2 rollout legacy-status-bootstrap --bucket-min=0 --bucket-max=<max>
./jetmon2 rollout legacy-status-bootstrap --bucket-min=0 --bucket-max=<max> --execute
```

## Guided Range Rollout

Use the API guided wrapper for normal range work:

```bash
cp docs/rollout-canaries.example.json rollout-canaries.json
# Edit rollout-canaries.json to approved controlled canaries or uptime-bench fixtures.

./jetmon2 api rollout guided \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --canary-file=rollout-canaries.json \
  --change-ref=<ticket-or-change-id> \
  --allow-remote
```

Useful flags:

- `--dry-run`: print planned API requests and confirmations without contacting
  the API.
- `--resume`: continue an interrupted guided run for the same range.
- `--rollback`: walk the release path for an activated range.
- `--include-comparison`: add HEAD/GET comparison after v2 is stable.
- `--include-policy-migration`: add staged policy migration planning after v2
  is stable.

Non-dry-run sessions write transcript and resume state under `logs/api-rollout`.

## API Primitive Flow

The guided command wraps these primitives.

### 1. Preflight And Read-Only Smoke

```bash
./jetmon2 api rollout preflight \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --allow-remote

./jetmon2 api rollout smoke \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --mode=head-legacy \
  --sample-size=100 \
  --read-only \
  --canary-file=rollout-canaries.json \
  --allow-remote
```

Smoke must not mutate incident state, runtime freshness, check history, WPCOM
notifications, or the legacy projection.

### 2. Seed v2 Side State

```bash
./jetmon2 api rollout seed \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --dry-run \
  --allow-remote

./jetmon2 api rollout seed \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --execute \
  --confirm=<token> \
  --allow-remote
```

### 3. Stop v1 For The Range

Systems stops the matching v1 bucket ownership. Do not activate v2 until that
is confirmed.

### 4. Final Reconcile And Activate

```bash
./jetmon2 api rollout final-reconcile \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --dry-run \
  --allow-remote

./jetmon2 api rollout final-reconcile \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --execute \
  --confirm=<token> \
  --allow-remote

./jetmon2 api rollout activate-buckets \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --dry-run \
  --allow-remote

./jetmon2 api rollout activate-buckets \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --execute \
  --confirm=<token> \
  --allow-remote
```

A single Monitor owner may hold only one contiguous API-controlled range at a
time.

### 5. Observe The Activated Range

```bash
./jetmon2 api rollout status --allow-remote

./jetmon2 api rollout bucket-coverage \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --allow-remote

./jetmon2 api rollout activity-check \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --since=15m \
  --allow-remote

./jetmon2 api rollout projection-drift \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --allow-remote

./jetmon2 api rollout smoke \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --mode=head-legacy \
  --sample-size=100 \
  --read-only \
  --canary-file=rollout-canaries.json \
  --allow-remote

./jetmon2 telemetry report --since=15m
```

Attach results to the rollout record. Treat these as hold points:

- missed or stale checks,
- projection drift,
- red host/fleet dashboard status,
- stale process heartbeat,
- Veriflier identity or discovery drift,
- WPCOM down/recovery parity gaps,
- delivery backlog or failed deliveries,
- canary mismatch.

## Rollback

Release the v2 range before Systems restarts v1:

```bash
./jetmon2 api rollout release-buckets \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --dry-run \
  --allow-remote

./jetmon2 api rollout release-buckets \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --execute \
  --confirm=<token> \
  --allow-remote
```

Do not roll back schema migrations as part of service rollback unless the
database-change process explicitly approves a reverse migration.

## Fleet Completion

After all ranges are stable on v2:

```bash
./jetmon2 rollout state-report --since=15m
./jetmon2 rollout dynamic-check
./jetmon2 rollout activity-check --since=15m --require-all
./jetmon2 rollout projection-drift --limit=100
./jetmon2 telemetry report --since=15m
```

When `projection-drift` fails, start with the summary and cause lines before
the row table. The command is read-only and does not repair `site_status`
automatically.

## Check-Policy Migration

Do this only after the v2 Monitor fleet is stable in `HEAD` + `legacy` mode.

Compare methods without changing authoritative alerting:

```bash
./jetmon2 api rollout compare-methods \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --from=head-legacy \
  --to=get-simple \
  --sample-size=100 \
  --allow-remote
```

Stage policy forward:

```bash
./jetmon2 api rollout stage-policy \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --method=GET \
  --profile=simple_http \
  --size=1000 \
  --dry-run \
  --allow-remote

./jetmon2 api rollout stage-policy \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --method=GET \
  --profile=simple_http \
  --size=1000 \
  --execute \
  --confirm=<token> \
  --allow-remote
```

Then stage `GET` + `full` in controlled cohorts:

```bash
./jetmon2 api rollout stage-policy \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --method=GET \
  --profile=full \
  --size=1% \
  --dry-run \
  --allow-remote
```

Rollback policy stages:

```bash
./jetmon2 api rollout stage-policy \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --mode=rollback-last-stage \
  --dry-run \
  --allow-remote

./jetmon2 api rollout stage-policy \
  --bucket-min=<min> \
  --bucket-max=<max> \
  --mode=rollback-all \
  --dry-run \
  --allow-remote
```

## Automation

Use JSON output for automation where available:

```bash
./jetmon2 rollout state-report --since=15m --output=json
./jetmon2 rollout projection-drift --limit=100 --output=json
```

Automation should gate on both process exit code and the JSON `ok` field. The
human runbook remains the source of truth for what to do when a gate fails.

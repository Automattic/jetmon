# v1 to v2 Migration Runbook

This is the source-of-truth runbook for the first production migration from
Jetmon 1 to Jetmon 2.

Use [rollout-quick-reference.md](rollout-quick-reference.md) as the condensed
command checklist during rehearsals and rollout windows. If it conflicts with
this runbook, this runbook wins.

Use [jetmon-v2-prelaunch-readiness.md](jetmon-v2-prelaunch-readiness.md) before
attempting the rollout to track launch posture, parity gates, support/WAF
readiness, rehearsal evidence, observability thresholds, consumer inventory, and
failure-mode drills.

Use this document for:

- preparing the fleet before any production change
- deploying a fresh containerized v2 Monitor and Veriflier fleet beside the
  existing v1 fleet
- replacing v1 on the same server
- moving a v1 bucket range to a fresh v2 server
- monitoring the cutover
- reverting safely
- completing the move from pinned buckets to dynamic v2 ownership
- removing old v1 software after signoff

## Preferred Production Shape: API-Driven Container Rollout

The preferred production rollout is a fresh v2 fleet deployed as containers on
servers unrelated to the v1 Monitor and Veriflier hosts. The operator should
not need shell access to those container hosts during the rollout window. A
standalone `jetmon2` binary can run from an operator workstation, bastion, or
other trusted host and call one API-enabled v2 Monitor. The Monitor fleet then
coordinates durable state through MySQL and records every control-plane action
in the audit trail.

The operator CLI should be configured with `~/.config/jetmon2.conf` or an
explicit `JETMON_API_CONFIG` path:

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

Keep this file and any `token_file` mode `0600`. `JETMON_API_URL`,
`JETMON_API_TOKEN`, and `JETMON_API_AUTH_POLICY` override the file, and command
flags override both. API writes to non-local URLs still require
`--allow-remote`, so production rollout commands should make remote mutation
intent explicit.

The container rollout has three operating states:

- **read-only standby:** v2 Monitors validate config, database schema,
  Veriflier reachability, and sampled probe behavior, but do not claim buckets,
  run scheduled checks, write events, update runtime/check-history rows, send
  WPCOM notifications, or run delivery workers.
- **armed standby:** v2 Monitors may publish process health and dependency
  health for dashboards, but still do not check customer sites or mutate site
  state.
- **active:** v2 owns an explicitly activated bucket range and is the
  authoritative checker for that range.

The API-driven rollout flow is:

1. Systems applies the additive v2 schema migrations.
2. Deploy the fresh v2 Veriflier fleet and validate the v2 JSON contract,
   stable `vantage.id` values, auth tokens, capacity, and quorum settings.
3. Deploy v2 Monitors in read-only standby with `HEAD` + `legacy` defaults,
   WPCOM notification disabled or explicitly guarded, and delivery workers
   disabled unless the delivery-owner plan has been approved.
4. Run API preflight from the operator CLI. This validates Monitor config,
   database access, schema version, Veriflier contract/quorum, delivery guards,
   bucket-control state, and standby mode.
5. Run read-only smoke checks against sampled sites and synthetic canaries.
   Smoke checks may issue HTTP and Veriflier probes, but they must not write
   incident state, runtime freshness, check history, WPCOM notifications, or
   legacy projection updates.
6. Seed/adopt v2 side state with a hybrid strategy: pre-seed scheduling/runtime
   rows and adopt existing v1 non-running projections into v2 event state before
   cutover, while allowing lazy creation for rows that are added or changed
   after the seed job. Adoption must not send duplicate down notifications.
7. After the sysadmin team stops the matching v1 bucket range, explicitly
   activate that range in v2 through the API. Do not rely on automatic v1
   shutdown detection; the v1 schema does not provide a reliable heartbeat or
   ownership record.
8. Run post-handoff gates for bucket coverage, recent check activity,
   projection drift, Veriflier health, canary down/recovery behavior, and
   delivery/WPCOM guard state.
9. If rollback is needed, explicitly release the activated bucket range through
   the API so v2 returns to standby for that range, then have the sysadmin team
   restart the matching v1 Monitor range.
10. After v2 owns all buckets and proves stable, run sampled non-authoritative
    `GET` + `simple_http` comparison checks while `HEAD` + `legacy` remains the
    alerting source of truth.
11. Transition cohorts from `HEAD` + `legacy` to `GET` + `simple_http` in
    explicit stages with dry-run, execute, pause, rollback-last-stage, and
    rollback-all support.
12. Transition stable cohorts from `GET` + `simple_http` to `GET` + `full`
    using the same staged controls.

The preferred operator command is the guided API wrapper:

```bash
./jetmon2 api rollout guided \
  --bucket-min=0 \
  --bucket-max=99 \
  --allow-remote
```

Use `--dry-run` to rehearse the exact API requests and typed confirmations
before the window. Use `--rollback` to walk the release path if a range must
return to v1 standby. After v2 owns all buckets and is stable, add
`--include-comparison` and `--include-policy-migration` to extend the guided
flow into sampled `HEAD`/`GET` comparison and staged check-policy planning.

The guided command wraps these control-plane API primitives:

```bash
./jetmon2 api rollout preflight --allow-remote
./jetmon2 api rollout smoke --mode=head-legacy --sample-size=1000 --read-only --allow-remote
./jetmon2 api rollout seed --dry-run --allow-remote
./jetmon2 api rollout seed --execute --confirm=<token> --allow-remote
./jetmon2 api rollout activate-buckets --bucket-min=0 --bucket-max=99 --dry-run --allow-remote
./jetmon2 api rollout activate-buckets --bucket-min=0 --bucket-max=99 --execute --confirm=<token> --allow-remote
./jetmon2 api rollout status --allow-remote
./jetmon2 api rollout release-buckets --bucket-min=0 --bucket-max=99 --execute --confirm=<token> --allow-remote
./jetmon2 api rollout compare-methods --from=head-legacy --to=get-simple --sample-size=10000 --allow-remote
./jetmon2 api rollout stage-policy --method=GET --profile=simple_http --size=1000 --dry-run --allow-remote
./jetmon2 api rollout stage-policy --method=GET --profile=full --size=1% --dry-run --allow-remote
```

Dangerous API rollout actions must be idempotent, admin-scoped, audited, and
protected by dry-run plans plus generated confirmation tokens. Bucket activation
and release must lock the requested range in durable database state so two
operators cannot activate overlapping ranges at the same time.

## What Changes For Customers

The important product fix is the probe method, but it should be rolled out in
stages.

Jetmon 1 verified sites with `HEAD` requests. That caused real customer pain:
some production stacks block `HEAD`, route it differently, skip application
logic, or return a status that does not match a visitor's real page load.
Jetmon 2 can use `GET` requests for local monitor checks and Veriflier checks,
so it can validate the same class of request a browser or customer-facing
uptime check normally makes.

The production rollout should not switch every variable at once. Use this
three-step check-policy migration:

1. Replace v1 processing with v2 while keeping `HEAD` plus the `legacy`
   detection profile. This validates the binary, bucket ownership, Veriflier
   transport, legacy projection, WPCOM payloads, and rollback process while the
   probe semantics stay as close to v1 as possible.
2. Move controlled batches to `GET` plus `simple_http`. This tests the visitor
   request path without enabling keyword, forbidden-content, redirect advisory,
   TLS advisory, or body-integrity detections.
3. Move stable batches to `GET` plus `full`. This enables the richer v2
   detections that provide better VIP/Agency explanations.

Set `DEFAULT_CHECK_METHOD=HEAD` and `DEFAULT_DETECTION_PROFILE=legacy` during
the initial replacement phase. Per-site overrides live in
`jetmon_site_check_config`; use that table or the API/CLI fields
`request_method` and `detection_profile` to move batches through the phases.
After migration, switch the process defaults to `GET` and `full`; keep
per-site `HEAD` overrides only for sites that truly require legacy semantics.

Terminology matters during this rollout. A site using `HEAD` plus the `legacy`
detection profile is only using v1-compatible **probe behavior**. It still runs
through the v2 Monitor and should still use the v2 Monitor-to-Veriflier
transport, `POST /v2/check`. That is separate from `veriflier2`'s optional
legacy-compatible HTTP endpoints, `POST /check` and `GET /status`, which are
disabled by default and only enabled with `VERIFLIER_ENABLE_LEGACY_HTTP=true`
for lab or emergency compatibility tests.

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
- Keep API-enabled Monitors in standby until bucket ownership is explicitly
  activated. API access is allowed for the container rollout control plane, but
  API availability must not imply scheduled checks, WPCOM notification, or
  delivery-worker ownership.
- Do not remove v1 binaries, configs, service units, or dependencies until the
  rollback window is closed.
- Treat `./jetmon2 migrate` as forward-only. Migrations are additive, so revert
  by restarting v1, not by rolling the schema back.
- V2 migrations intentionally add v2-owned tables and avoid requiring new
  columns or indexes on the live `jetpack_monitor_sites` compatibility table.

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

Run the production-data audit before approving the first host window. This
read-only gate summarizes the real legacy table shape without printing monitor
URLs, including active row count, observed bucket space, status distribution,
check-interval distribution, malformed URL counts, active duplicate `blog_id`
rows, and existing non-running v1 projections:

```bash
./jetmon2 rollout production-data-audit --bucket-min=0 --bucket-max=<max>
```

If the audit reports existing active non-running rows, bootstrap matching v2
events before treating `projection-drift` as a hard gate. The bootstrap is
read-only unless `--execute` is provided:

```bash
./jetmon2 rollout legacy-status-bootstrap --bucket-min=0 --bucket-max=<max>
./jetmon2 rollout legacy-status-bootstrap --bucket-min=0 --bucket-max=<max> --execute
```

Do not force the bootstrap past active duplicate `blog_id` blockers during the
initial rollout. Current v2 rollout state is still keyed by `blog_id`; duplicate
active rows need endpoint-identity support or explicit data cleanup before they
can be handled safely.

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

Run the generated runbook and `rollout guided` from the staged v2 runtime host,
not from a separate orchestration host. In same-server mode the v1 host and v2
runtime host are normally the same machine. In fresh-server mode,
`--host=<old-v1-hostname>` identifies the v1 host from the static plan and
`--runtime-host=<new-v2-hostname>` identifies the new v2 machine where the
guided command runs. If the v1 stop/start commands use `ssh`, the new v2
runtime host must be able to SSH to the old v1 host before the production
window starts.

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
is not `/etc/systemd/system/jetmon2.service`. Confirm SSH from the new v2
runtime host to the old v1 host before relying on SSH-based
`--v1-stop-command` or `--v1-start-command`.

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
prints the expected run origin, explains each gate, asks before continuing, and
stops on failed gates. It uses typed confirmations before stopping v1, starting
v2, stopping v2 during rollback, or restarting v1. By default it prints
service commands for the operator to run from the v2 runtime host and asks for
`DONE`; add `--execute-operator-commands` only when the operator intentionally
wants the guided command to execute those commands after confirmation.
If the command is interrupted after a stop/start transition, rerun it with the
same options and choose resume; saved service state prevents the command from
asking the operator to repeat a transition that already completed. When resume
state exists, there is no default choice; the operator must type `RESUME` or
`START OVER`. Short `y` / `n` answers are rejected for this prompt.
Dry-run mode prints the selected path, service commands, typed confirmation
phrases, and manual `DONE` checkpoints without running rollout checks or
service commands.

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

If a forward gate fails after v2 has started and the operator chooses rollback,
the rollback path can complete successfully while the overall command exits
non-zero. This is intentional: the host rollout did not complete, even though
the range was returned to v1. Keep the transcript with the rollout record.

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
   with the additive v2 tables present.

### Build And Stage Artifacts

Build and verify the release:

```bash
make test-race
make rollout-docs-verify
```

`make rollout-docs-verify` builds all binaries, runs the standard test suite
and `go vet`, checks rollout command help, verifies JSON output and staged
systemd units, and runs the operator rehearsal verifier. `make test-race` is
kept separate because it is slower. For a faster no-database check while
editing the runbook, run `make rollout-rehearsal-verify`; it verifies that
generated plans, guided output, runtime-host warnings, typed confirmations, and
rollback commands still match this runbook. That target uses a disposable
sample bucket plan and does not replace the real `host-preflight` gate or VM
lab rehearsal.

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

### Veriflier Contract Rollout

New `veriflier2` binaries serve the versioned v2 JSON contract by default:

- v2: `POST /v2/check`, `GET /v2/status`

They can optionally serve a legacy-compatible HTTP contract for lab or
emergency rollback testing by setting `VERIFLIER_ENABLE_LEGACY_HTTP=true`:

- legacy-compatible HTTP: `POST /check`, `GET /status`

This transport switch is independent of the site check method. Monitors can
send `HEAD` + `legacy` checks to Verifliers over `POST /v2/check`; enabling
legacy-compatible `/check` is not required for a Monitor rollout that starts
with all sites in legacy HEAD mode.

Deploy the new v2 Veriflier fleet before switching monitor hosts. The preferred
rollout uses fresh Veriflier servers, proves that fleet independently, then
points v2 Monitors only at those `veriflier2` endpoints. Keep the original v1
Verifliers serving the original v1 Monitors until monitor cutover is complete.

Do not depend on v2 Monitors talking to original v1 Verifliers. The original v1
Veriflier uses the old TLS/custom transport, while the v2 Monitor speaks the Go
JSON-over-HTTP Veriflier contract. The Monitor's legacy `/check` fallback is
for `veriflier2`'s legacy-compatible endpoint, not for the original v1
Veriflier process.

Deploy one new v2 Veriflier endpoint at a time:

1. Stage the `veriflier2` binary and its config on the new Veriflier host.
2. Set the listen port and monitor auth token that v2 Monitors will use.
3. Set `VERIFLIER_VANTAGE_ID` to a stable regional/provider identity. Leave
   database settings unset; Veriflier hosts do not need database credentials.
4. Leave `VERIFLIER_ENABLE_LEGACY_HTTP=false` unless this endpoint is part of an
   explicit lab or emergency compatibility test.
5. Start or restart the Veriflier service for that endpoint.
6. From a v2 monitor runtime host, verify the v2 status endpoint and then resume
   with the next Veriflier endpoint.

If the endpoint is a load-balanced pool, roll the backend replicas one at a
time. All replicas behind the same monitor-side endpoint must share the same
`VERIFLIER_VANTAGE_ID`, because that endpoint is one quorum vote. If a rollback
is needed before monitor cutover, remove that new v2 endpoint from the pending
v2 Monitor config or restart the previous `veriflier2` binary on the same new
endpoint. No Jetmon database rollback is required for a Veriflier-only
rollback.

For each Veriflier endpoint, set a stable `VERIFLIER_VANTAGE_ID` when the
endpoint represents a region/provider vantage. Multiple horizontally scaled
replicas behind the same load-balanced endpoint must share that `vantage_id`;
the monitor counts the configured endpoint as one quorum vote. `agent.id` in
`/v2/status` identifies the serving process for diagnostics only.

Monitor quorum counts unique v2 `vantage.id` values. If two configured
Veriflier entries report the same `vantage.id`, only one vote counts; the
duplicate reply is retained in audit metadata. In multi-Veriflier layouts,
Jetmon keeps a two-healthy-vantage floor unless `PEER_OFFLINE_LIMIT=1` was
intentionally configured.

Before advancing a monitor range that depends on the new v2 Veriflier fleet,
run `validate-config` and verify the v2 status endpoint from the v2 monitor
runtime host:

```bash
./jetmon2 validate-config
curl -fsS http://<veriflier-host>:7803/v2/status
```

`/v2/status` should report `protocols` containing `v2-json-http`,
`vantage.id`, `agent.id`, and non-zero `capacity.max_concurrency`. If a
Veriflier is saturated, `/v2/check` returns HTTP 503 and contributes no vote;
that is a rollout hold point for capacity, not evidence that customer sites are
down. `validate-config` warns for unreachable or legacy-only Verifliers and
fails for missing or duplicate v2 vantage IDs.

Veriflier auto-discovery is also staged. Leave
`VERIFLIER_DISCOVERY_MODE=static` for the first monitor cutover unless the
registry has already been rehearsed. To rehearse discovery, create one
pre-approved `jetmon_veriflier_vantages` row per trusted quorum vantage, enable
it only when the endpoint and token are correct, and run monitors in
`VERIFLIER_DISCOVERY_MODE=shadow`. Shadow mode queries
`jetmon_veriflier_vantages` and recent `jetmon_veriflier_agents` telemetry rows,
then reports missing/extra registry vantages without changing traffic. Switch
to `active` only after `validate-config` reports usable registry vantages and
no shadow drift. Active mode falls back to static `VERIFIERS` if discovery is
unavailable or empty during rollout.

Use the read-only discovery comparison report as the explicit shadow-mode gate:

```bash
./jetmon2 verifliers discovery-report --output=text
```

The report compares configured static Verifliers, trusted registry vantages,
and recent monitor-collected agent rows. Green means the static v2 vantage IDs
match the enabled registry and recent agents are present. Amber is a hold point
for drift, stale telemetry, incomplete registry rows, or endpoint mismatches.
Red is a hold point for active discovery, such as duplicate static vantages or
active mode without any usable enabled registry vantages. The report does not
print auth token values.

Agent telemetry is not trust. Monitors poll authenticated Veriflier
`/v2/status` endpoints and write `jetmon_veriflier_agents` rows showing process
liveness/capacity, so Veriflier hosts do not need database credentials. Those
rows do not create quorum votes unless an operator has created and enabled the
matching `jetmon_veriflier_vantages` row.

Keep `veriflier2`'s legacy-compatible `/check` fallback available as an
explicit opt-in compatibility guard, but keep it disabled on normal production
v2 endpoints and do not treat it as support for original v1 Verifliers. Remove
the fallback code only in a follow-up branch after all of these are true:

- every configured Veriflier endpoint reports `/v2/status` with
  `v2-json-http`, a stable `vantage.id`, `agent.id`, and non-zero capacity
- `./jetmon2 validate-config` has no legacy-only Veriflier warnings on the
  deployed fleet
- `make test-veriflier-soak` and the approved production-like Veriflier soak
  pass for high concurrency, overload, auth failure, timeout, duplicate-vantage
  misconfiguration, discovery drift, active-mode fallback, and long outage
  promotion/recovery
- `./jetmon2 telemetry report` shows stable verifier reply and vote evidence
  with no verifier metadata gaps over the agreed production window
- rollback plans no longer depend on any legacy-compatible `veriflier2`
  endpoint

Keep the historical `veriflier` / `veriflier2` names during v2 rollout. A v3
probe architecture can introduce a clearer `probe-agent` or `vantage-agent`
role without renaming the compatibility binary in place.

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
  "DEFAULT_CHECK_METHOD": "HEAD",
  "DEFAULT_DETECTION_PROFILE": "legacy",
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
- `default_check_policy=method:HEAD profile:legacy`
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
disposable sites. For the container rollout, the same CLI can run from any
trusted operator host and talk to an API-enabled standby Monitor.

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

For production-style operator use, prefer a local config file over repeatedly
copying tokens into shell history:

```bash
mkdir -p ~/.config
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

Store `token_file` relative to the config file directory or as an absolute
path, and keep it mode `0600`. Use `JETMON_API_CONFIG=/path/to/jetmon2.conf`
when an operator needs separate staging and production profiles.

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
   systemctl enable --now jetmon2 && systemctl is-active --quiet jetmon2
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
    proves the range has fresh `jetmon_site_runtime.last_checked_at` writes,
    not which process wrote them. Keep v1 stopped and use logs or the dashboard
    to confirm v2 is checking only the pinned range.
11. After one full expected round, run:

    ```bash
    ./jetmon2 rollout cutover-check \
      --host=<v2-hostname> \
      --bucket-min=<min> \
      --bucket-max=<max> \
      --since=15m \
      --require-all
    ```

12. Capture WPCOM parity and explanation evidence for this hold point:

    ```bash
    ./jetmon2 telemetry report --since=15m
    ```

    This report is read-only and window-level. Treat warnings as hold points,
    and widen `--since` when the range is too quiet to prove WPCOM down/recovery
    parity.

13. Watch one full check round before moving to the next host.

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
   systemctl enable --now jetmon2 && systemctl is-active --quiet jetmon2
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
- local checks use `HEAD` plus `legacy` detection during the first replacement
  phase
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

  If this fails, read the summary section first. It groups mismatches by bucket
  and likely cause, then lists sample rows. Do not restart v1 readers or apply
  ad hoc `site_status` updates until the matching `jetmon_events` rows and
  transition history confirm which projection value is authoritative.

- recent check activity exists for the pinned range:

  ```bash
  ./jetmon2 rollout activity-check \
    --bucket-min=<min> \
    --bucket-max=<max> \
    --since=15m
  ```

  After a full expected round, require every active site in the range to have a
  fresh `jetmon_site_runtime.last_checked_at`:

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
  ./jetmon2 telemetry report --since=15m
  ```

  The telemetry report is not a per-range hard gate like `cutover-check`; it is
  evidence that the rollout window still has WPCOM notification parity and
  enough metadata for support explanations. Widen `--since` if the current
  window has too few incidents to prove parity.

If `DASHBOARD_PORT` is enabled, confirm:

- the host dashboard at `/` shows bucket ownership mode as pinned
- the host dashboard dependency health is green for MySQL, configured
  Verifliers, WPCOM, StatsD, and log/stats directory writes
- the host dashboard shows the WPCOM circuit breaker closed
- retry queue depth is not growing unexpectedly
- Go runtime system memory stays below the configured guardrail and RSS stays
  within host-level expectations
- delivery workers are disabled unless explicitly approved
- the fleet dashboard at `/fleet` shows the replaced host as fresh, and pinned
  bucket mode as an expected amber rollout state

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
   systemctl stop jetmon2 && ! systemctl is-active --quiet jetmon2
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
   systemctl stop jetmon2 && ! systemctl is-active --quiet jetmon2
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
   ./jetmon2 telemetry report --since=15m
   ```

8. Confirm `jetmon_hosts` coverage is active, fresh, gap-free, and
   overlap-free. If `DASHBOARD_PORT` is enabled, `/fleet` should show
   `mode=dynamic`, green bucket coverage, no stale processes, no projection
   drift, and no failed or abandoned delivery rows.
   If the projection-drift check fails, use the bucket/cause summary to decide
   whether this is a stale legacy projection, a missing event-to-projection
   write, or an unexpected status value before making any manual repair.
9. Continue with normal v2 rolling updates: stop one host, deploy, start it,
   verify `./jetmon2 status`, then move to the next host.

## Phase 5: Migrate Probe Semantics

After v2 has replaced v1 and the fleet is stable, migrate probe semantics in
separate batches:

1. Select a small cohort and set `request_method='GET'`,
   `detection_profile='simple_http'` in `jetmon_site_check_config` or through
   the API/CLI. Watch for false-positive floods, verifier disagreement, WPCOM
   parity issues, and support reports.
2. Expand the `GET` + `simple_http` cohort only after the previous cohort is
   clean for the agreed observation window.
3. For stable GET cohorts, set `detection_profile='full'` to enable keyword,
   forbidden-content, redirect advisory/fail, TLS advisory, and body-integrity
   detections.
4. When all normal sites are stable on `GET` + `full`, change process defaults
   to:

   ```json
   {
     "DEFAULT_CHECK_METHOD": "GET",
     "DEFAULT_DETECTION_PROFILE": "full"
   }
   ```

5. Leave rows in `jetmon_site_check_config` only for sites that need an
   exception from the defaults, such as long-term `HEAD` compatibility.

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
7. Keep v2 additive database schema. Do not remove v2-owned tables while legacy
   consumers still need rollback coverage.
8. Keep `LEGACY_STATUS_PROJECTION_ENABLE=true` until legacy readers have moved
   to v2 state surfaces. Retiring that projection is a separate project.

## Final Checklist

- [ ] v1 host inventory complete
- [ ] bucket ranges complete and non-overlapping
- [ ] `rollout production-data-audit` reviewed for the production table
- [ ] existing non-running v1 rows bootstrapped with
      `rollout legacy-status-bootstrap --execute` if present
- [ ] active duplicate `blog_id` rows resolved or endpoint-identity support
      approved before rollout
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
- [ ] `telemetry report` captured for WPCOM parity and explanation evidence
- [ ] `rollout rollback-check` exercised during rehearsal
- [ ] all hosts running v2 pinned
- [ ] dynamic ownership cutover completed
- [ ] `rollout dynamic-check` passes
- [ ] projection drift is zero
- [ ] v1 artifacts retained through rollback window
- [ ] v1 artifacts removed after signoff

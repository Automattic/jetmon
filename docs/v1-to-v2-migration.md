# v1 to v2 Migration Runbook

This is the source-of-truth runbook for the first production migration from
Jetmon 1 to Jetmon 2.

The current production plan is a fresh v2 fleet deployed beside the v1 fleet:
new v2 Verifliers first, then containerized v2 Monitors in standby, then
explicit API-controlled bucket activation after the matching v1 range is
stopped. Operators should not need shell access to the container hosts during
the rollout window; a local `jetmon2` binary can drive the control plane through
one API-enabled Monitor.

Related docs:

- [`rollout-quick-reference.md`](rollout-quick-reference.md): condensed command
  checklist.
- [`jetmon-v2-prelaunch-readiness.md`](jetmon-v2-prelaunch-readiness.md):
  launch posture, approvals, canary evidence, and stop/go thresholds.
- [`production-teamcity-rollout.md`](production-teamcity-rollout.md): TeamCity,
  docker-deploy, config-sync sidecar, host-local StatsD, and secret handling.
- [`docker-images.md`](docker-images.md): image runtime inputs and entrypoint
  behavior.
- [`production-veriflier-compose.md`](production-veriflier-compose.md):
  production Veriflier VPS Compose stack.
- [`jetmon-deliverer-rollout.md`](jetmon-deliverer-rollout.md): standalone
  delivery-worker migration, if that is done separately.

## Invariants

Do not violate these during migration:

- Do not let v1 and v2 actively check the same bucket range at the same time.
- Do not start v2 scheduled checks by merely exposing the API. Monitors must
  remain standby until bucket ownership is explicitly activated.
- Keep `LEGACY_STATUS_PROJECTION_ENABLE=true` until legacy readers have moved
  to the v2 API or event tables.
- Keep WPCOM notifications in `WPCOM_NOTIFY_MODE=legacy` for the first rollout
  unless WPCOM explicitly approves `modern`.
- Do not run standalone delivery workers unless the delivery-owner plan is part
  of the approved rollout.
- Treat schema changes as forward-only. Revert by returning traffic to v1, not
  by rolling back additive v2 tables.
- Do not remove v1 software, configs, services, or dependencies until rollback
  signoff is complete.
- V2-owned site config and runtime state must stay out of
  `jetpack_monitor_sites`; during rollout v2 should only maintain the legacy
  projection fields needed for compatibility.

## Customer-Visible Change

Jetmon 1 checked sites with `HEAD` requests. Jetmon 2 can check with `GET`,
which better matches real visitor behavior and fixes a core v1 source of false
positives and false negatives.

Do not switch every variable at once. Use a staged check-policy migration:

1. **Replace v1 with v2 using `HEAD` + `legacy`.** This validates the new
   binary, Veriflier transport, schema, WPCOM payloads, bucket activation, and
   rollback path while keeping probe behavior as close to v1 as possible.
2. **Move controlled cohorts to `GET` + `simple_http`.** This exercises the
   visitor request path without enabling the richer v2 detections.
3. **Move stable cohorts to `GET` + `full`.** This enables keyword,
   forbidden-content, redirect, TLS, and body-integrity detections.

The check method/profile is per site in
`jetpack_monitor_site_check_config`. After migration, production defaults can
move to `GET` + `full`; keep per-site `HEAD` overrides only where needed.

## WPCOM Provisioning Compatibility

The first production rollout keeps WPCOM provisioning pointed at the legacy
table path. Current WPCOM Monitor activation creates or updates rows in
`jetpack_monitor_sites`; Jetpack plugin activation by itself does not create a
Monitor row. V2 must therefore be able to start checking a row that appears in
`jetpack_monitor_sites` without any pre-created v2 side tables.

That path is intentional. V2 reads active rows from `jetpack_monitor_sites` and
creates or adopts v2 runtime/config state as needed. The internal v2 Sites API
is not a drop-in replacement for WPCOM provisioning yet because WPCOM can manage
multiple monitor URLs for one blog while the current API is stricter about
duplicate `blog_id` creation.

Pre-window validation must include:

- a WPCOM-style direct insert or activation row with no v2 sidecars; v2 should
  pick it up on the next target reload and create runtime freshness
- a WPCOM URL-update flow, because current WPCOM behavior may delete and
  reinsert monitor rows for a blog
- a check that the rollout database includes `jetpack_monitor_sitemeta` if
  WPCOM URL settings or status-down webhooks are in scope

Bucket shape needs explicit signoff. Current WPCOM bucket assignment is a
512-bucket space (`0-511`), and v2 defaults `BUCKET_TOTAL` to `512` for that
reason. Do not override v2 to a wider `BUCKET_TOTAL` until WPCOM's bucket
assignment and any required rebalancing are updated; otherwise the extra buckets
will not receive newly provisioned WPCOM rows.

## Before The Window

Complete these before any production activation:

1. Approve the launch posture, canary cohort matrix, stop/go thresholds, and
   support/WPCOM parity expectations in
   [`jetmon-v2-prelaunch-readiness.md`](jetmon-v2-prelaunch-readiness.md).
2. Systems applies the additive v2 schema changes through the normal database
   change process. Production containers should run with
   `CONFIG_PROFILE=production` or `SCHEMA_MANAGEMENT_MODE=validate` so startup
   validates schema state and never applies DDL.
3. Validate the schema ledger from the same environment the Monitor will use:

   ```bash
   ./jetmon2 schema validate
   ```

4. Confirm v1 continues to run normally with the additive v2 tables present.
5. Run the read-only production data audit:

   ```bash
   ./jetmon2 rollout production-data-audit --bucket-min=0 --bucket-max=<max>
   ```

6. If the audit finds active non-running v1 projections, bootstrap matching v2
   event state before treating projection drift as a hard gate:

   ```bash
   ./jetmon2 rollout legacy-status-bootstrap --bucket-min=0 --bucket-max=<max>
   ./jetmon2 rollout legacy-status-bootstrap --bucket-min=0 --bucket-max=<max> --execute
   ```

7. Resolve or explicitly defer active duplicate `blog_id` blockers. Current
   rollout state is still keyed by `blog_id`; endpoint-identity work is needed
   for the small production cohort where one blog has multiple active monitor
   URLs.
8. Confirm the WPCOM provisioning shape for the rollout window:
   - WPCOM-created rows land in `jetpack_monitor_sites` with
     `monitor_active=1`.
   - The observed bucket space matches the range plan. Treat any v2
     `BUCKET_TOTAL` wider than the 512-bucket WPCOM source as a hold point
     until the cutover range or WPCOM rebalance plan is explicit.
   - A WPCOM-style direct row with no v2 sidecars is picked up by a standby lab
     or test Monitor after activation.
   - WPCOM URL updates have been tested against v2 sidecar/event state.
9. Prepare controlled canary targets and a canary file:

   ```bash
   cp docs/rollout-canaries.example.json rollout-canaries.json
   ```

10. Run local verification before shipping the rollout artifacts:

   ```bash
   make test-race
   make rollout-docs-verify
   ```

## Configure The Operator CLI

Create a local API CLI config on the operator workstation, bastion, or other
trusted host that will control the rollout:

```bash
./jetmon2 local-config init \
  --base-url=https://jetmon-v2-api.example.com \
  --token-file=jetmon2-api-token \
  --default-output=table
./jetmon2 local-config show
```

Example `~/.config/jetmon2.conf`:

```conf
base_url = https://jetmon-v2-api.example.com
token_file = jetmon2-api-token
auth_policy = same-origin
timeout = 30s
output = table
```

Keep the config and token file mode `0600`. Production mutations to non-local
URLs still require `--allow-remote`, so remote write intent remains explicit.

## Deploy And Validate v2 Verifliers

Deploy the fresh v2 Veriflier fleet before moving any Monitor buckets.

Rules:

- V2 Monitors should point only at `veriflier2` endpoints serving the v2 JSON
  contract: `POST /v2/check` and `GET /v2/status`.
- Do not depend on v2 Monitors talking to original v1 Verifliers. The original
  v1 transport is different.
- `VERIFLIER_ENABLE_LEGACY_HTTP=true` exposes compatibility `/check` and
  `/status` endpoints only for lab or emergency testing; it is not required for
  `HEAD` + `legacy` site checks.
- Each quorum vantage needs a stable `VERIFLIER_VANTAGE_ID`. Horizontally
  scaled replicas behind one endpoint share the same vantage ID; `agent.id` is
  process diagnostics, not an extra vote.
- Veriflier hosts do not need database credentials.

From a v2 Monitor runtime environment, verify each endpoint:

```bash
./jetmon2 validate-config
curl -fsS http://<veriflier-host>:7803/v2/status
./jetmon2 verifliers discovery-report --output=text
```

Hold the rollout if v2 status is missing, `vantage.id` is missing or duplicated,
capacity is zero, the endpoint is unreachable, or the discovery report is red.

## Deploy v2 Monitors In Standby

Deploy v2 Monitor containers with:

- `ROLLOUT_MODE=api-controlled`
- `VERIFLIER_DISCOVERY_MODE=shadow`
- initial defaults of `HEAD` + `legacy`
- `SCHEMA_MANAGEMENT_MODE=validate` or `CONFIG_PROFILE=production`
- `WPCOM_NOTIFY_MODE=legacy`
- WPCOM notifications disabled or explicitly guarded until parity gates are
  approved
- delivery workers disabled unless the delivery-owner plan is approved
- StatsD configured according to
  [`production-teamcity-rollout.md`](production-teamcity-rollout.md)

In standby, Monitors may validate config, database connectivity, schema state,
Veriflier reachability, and sampled probes. They must not claim buckets, run
scheduled checks, mutate incident state, write check history, update runtime
freshness, send WPCOM notifications, or run delivery workers.

## Per-Range Activation Flow

Run the guided API flow for each bucket range:

```bash
./jetmon2 api rollout guided \
  --bucket-min=0 \
  --bucket-max=99 \
  --canary-file=rollout-canaries.json \
  --change-ref=SYSREQ-12345 \
  --allow-remote
```

Use `--dry-run` before the window. Use `--resume` if the operator process is
interrupted. Non-dry-run sessions write a transcript and resume state under
`logs/api-rollout`.

The guided flow performs these gates:

1. **Preflight:** validate API mode, schema, DB access, rollout locks,
   Veriflier contract/quorum identity, delivery/WPCOM guard state, and canary
   definitions.
2. **Read-only smoke:** run sampled `HEAD` + `legacy` probes and controlled
   canaries without writing incident state, runtime freshness, check history,
   WPCOM notifications, or legacy projection updates.
3. **Seed/adopt:** pre-seed v2 side tables and adopt existing v1 non-running
   projections into v2 event state without duplicate down notifications.
4. **Stop v1 range:** Systems stops the matching v1 Monitor range.
5. **Final reconcile:** adopt sites added or changed after the first seed.
6. **Activate range:** explicitly activate the bucket range in v2. Do not rely
   on automatic v1 shutdown detection; v1 has no reliable ownership heartbeat.
7. **Post-handoff gates:** verify bucket coverage, recent activity, projection
   drift, Veriflier health, controlled canaries, and delivery/WPCOM guard state.

Useful primitive commands when the guided wrapper is not appropriate:

```bash
./jetmon2 api rollout preflight --bucket-min=0 --bucket-max=99 --canary-file=rollout-canaries.json --allow-remote
./jetmon2 api rollout smoke --bucket-min=0 --bucket-max=99 --mode=head-legacy --sample-size=100 --read-only --canary-file=rollout-canaries.json --allow-remote
./jetmon2 api rollout seed --bucket-min=0 --bucket-max=99 --dry-run --allow-remote
./jetmon2 api rollout seed --bucket-min=0 --bucket-max=99 --execute --confirm=<token> --allow-remote
./jetmon2 api rollout final-reconcile --bucket-min=0 --bucket-max=99 --execute --confirm=<token> --allow-remote
./jetmon2 api rollout activate-buckets --bucket-min=0 --bucket-max=99 --execute --confirm=<token> --allow-remote
./jetmon2 api rollout status --allow-remote
./jetmon2 api rollout bucket-coverage --bucket-min=0 --bucket-max=99 --allow-remote
./jetmon2 api rollout activity-check --bucket-min=0 --bucket-max=99 --since=15m --allow-remote
./jetmon2 api rollout projection-drift --bucket-min=0 --bucket-max=99 --allow-remote
```

Mutating commands are admin-scoped, audited, idempotent, dry-run first, and
protected by generated confirmation tokens. Tokens are bound to the
authenticated API key identity.

## Observe Each Activated Range

After activation, hold before moving to the next range until these are true for
the agreed window:

- bucket coverage is complete for the activated range
- recent activity shows v2 checks running on schedule
- `projection-drift` is zero while legacy projection is enabled
- Veriflier health is green and quorum metadata is present
- canary checks match expectations
- WPCOM parity evidence is captured or explicitly disabled by the approved test
  plan
- telemetry report has no unexplained notification, verifier, or event gaps
- dashboards show MySQL, Verifliers, WPCOM, StatsD, and stats directory writes
  healthy enough for the rollout stage

Useful commands:

```bash
./jetmon2 api rollout bucket-coverage --bucket-min=0 --bucket-max=99 --allow-remote
./jetmon2 api rollout activity-check --bucket-min=0 --bucket-max=99 --since=15m --allow-remote
./jetmon2 api rollout projection-drift --bucket-min=0 --bucket-max=99 --allow-remote
./jetmon2 telemetry report --since=15m
./jetmon2 verifliers discovery-report --output=text
```

## Roll Back A Range

Rollback returns a bucket range to v1; it does not remove v2 schema.

Preferred guided path:

```bash
./jetmon2 api rollout guided \
  --rollback \
  --bucket-min=0 \
  --bucket-max=99 \
  --change-ref=SYSREQ-12345 \
  --allow-remote
```

Manual API path:

```bash
./jetmon2 api rollout release-buckets --bucket-min=0 --bucket-max=99 --dry-run --allow-remote
./jetmon2 api rollout release-buckets --bucket-min=0 --bucket-max=99 --execute --confirm=<token> --allow-remote
```

After v2 releases the range, Systems restarts the matching v1 Monitor range.
Then verify v1 activity through the legacy operational path and keep the v2
rollout transcript with the incident record. If the guided forward path failed
and rollback succeeded, the command may still exit non-zero because the forward
rollout did not complete; that is expected.

## Complete Fleet Cutover

After every production bucket range is active on v2:

```bash
./jetmon2 api rollout status --allow-remote
./jetmon2 api rollout bucket-coverage --bucket-min=0 --bucket-max=<max> --allow-remote
./jetmon2 api rollout activity-check --bucket-min=0 --bucket-max=<max> --since=15m --allow-remote
./jetmon2 api rollout projection-drift --bucket-min=0 --bucket-max=<max> --allow-remote
./jetmon2 telemetry report --since=15m
```

Keep the fleet in the initial `HEAD` + `legacy` policy until the agreed
observation window has passed and WPCOM/support signoff is recorded.

If the fleet later moves from API-controlled pinned ownership to dynamic v2
ownership, use the normal rollout gates for that separate step and do not leave
gaps or overlaps in bucket ownership.

## Stage Check-Policy Migration

Run comparison before changing alerting semantics:

```bash
./jetmon2 api rollout compare-methods \
  --bucket-min=0 \
  --bucket-max=<max> \
  --from=head-legacy \
  --to=get-simple \
  --sample-size=100 \
  --allow-remote
```

Then stage cohorts explicitly:

```bash
./jetmon2 api rollout stage-policy --bucket-min=0 --bucket-max=<max> --method=GET --profile=simple_http --size=100 --dry-run --allow-remote
./jetmon2 api rollout stage-policy --bucket-min=0 --bucket-max=<max> --method=GET --profile=simple_http --size=100 --execute --confirm=<token> --allow-remote
./jetmon2 api rollout stage-policy --bucket-min=0 --bucket-max=<max> --method=GET --profile=simple_http --size=1000 --execute --confirm=<token> --allow-remote
./jetmon2 api rollout stage-policy --bucket-min=0 --bucket-max=<max> --method=GET --profile=full --size=1% --execute --confirm=<token> --allow-remote
```

Recommended cohort shape:

- 10 known-safe sites
- 100 mixed sites
- 1,000 mixed sites
- 1% of active sites
- 5%
- 10%
- 25%
- 50%
- 100%, excluding any intentionally retained `HEAD` sites

Hold between stages long enough to observe false-positive rate, missed recovery
rate, verifier disagreement, WPCOM parity, support reports, and resource
pressure.

Rollback policy changes if needed:

```bash
./jetmon2 api rollout stage-policy --bucket-min=0 --bucket-max=<max> --mode=rollback-last-stage --dry-run --allow-remote
./jetmon2 api rollout stage-policy --bucket-min=0 --bucket-max=<max> --mode=rollback-last-stage --execute --confirm=<token> --allow-remote
./jetmon2 api rollout stage-policy --bucket-min=0 --bucket-max=<max> --mode=rollback-all --execute --confirm=<token> --allow-remote
```

## Tear Down v1

Only remove v1 after rollout signoff.

1. Confirm all production bucket ranges are covered by v2.
2. Confirm no v1 Monitor process is checking production buckets.
3. Confirm rollback signoff has expired.
4. Archive v1 configs and rollout transcripts according to retention policy.
5. Remove old v1 service units, Node dependencies, native addons, Qt Veriflier
   artifacts, and v1-only logrotate files.
6. Remove or retire old Veriflier hosts only after v2 Veriflier capacity and
   quorum behavior are stable.

## Final Checklist

- [ ] production data audit reviewed
- [ ] duplicate active `blog_id` blockers resolved or deferred with signoff
- [ ] additive schema changes applied by Systems
- [ ] production configs use schema validation, not automatic migration
- [ ] v2 Veriflier fleet deployed and validated through `/v2/status`
- [ ] API CLI configured with a scoped admin token
- [ ] synthetic canaries approved and loaded
- [ ] v2 Monitors deployed in standby/API-controlled mode
- [ ] guided API rollout dry-run completed
- [ ] each range seeded, reconciled, activated, and observed
- [ ] projection drift is zero for activated ranges
- [ ] telemetry report captured for WPCOM parity and explanation evidence
- [ ] rollback path rehearsed and documented
- [ ] all ranges active on v2
- [ ] staged `HEAD` -> `GET/simple_http` -> `GET/full` migration completed or
      explicitly paused
- [ ] v1 teardown approved and completed

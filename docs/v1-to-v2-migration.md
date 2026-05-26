# v1 To v2 Rollout Guide

This is the canonical production rollout and rollback guide. It folds the old
quick reference, prelaunch readiness checklist, TeamCity notes, Docker image
notes, Veriflier compose notes, and deliverer rollout notes into one file.

Read [project.md](project.md) for architecture and
[operations-guide.md](operations-guide.md) for steady-state operation.

## Rollout Invariants

- Apply only approved additive schema changes.
- Keep v1 and v2 from owning the same bucket range at the same time.
- Start with `HEAD` + `legacy` check policy.
- Keep `LEGACY_STATUS_PROJECTION_ENABLE=true` until legacy readers no longer
  depend on `jetpack_monitor_sites.site_status`.
- Validate v2 Veriflier quorum before activating Monitor buckets.
- Use API-driven dry-run/execute gates for container rollout.
- Keep WPCOM notification mode v1-compatible during drop-in.
- Roll back by releasing v2 ownership and restarting the matching v1 owner.
- Do not use empty canary sets as proof that a range is healthy.

## Launch Gates

Before the first production activation, confirm:

| Gate | Evidence |
| --- | --- |
| Schema | Approved DB change applied; `jetmon2 validate-config` and API preflight pass. |
| Verifliers | `/v2/status` healthy from every required vantage; quorum report is green. |
| Images | Production image tags built by CI and promoted through the normal path. |
| Config | API-controlled standby mode configured; StatsD host path is v1-compatible. |
| WPCOM | Legacy notification path tested in the target environment. |
| Rollout CLI | Operator has a token with required scope and `--allow-remote` policy understood. |
| Canaries | Approved controlled canaries or uptime-bench fixtures are listed in a local canary file. |
| Rollback | v1 start command, v2 release path, and owner contact are known. |
| Dashboards | Host dashboard, fleet dashboard, API health, and metrics are visible. |

## Production Shape

Preferred sequence:

1. Deploy v2 Verifliers beside the existing Veriflier fleet.
2. Deploy v2 Monitor containers in standby/API-controlled mode.
3. Deploy `jetmon-deliverer` only if outbound delivery is being separated
   before full Monitor cutover; otherwise leave embedded delivery disabled or
   guarded.
4. Activate one v1 bucket range at a time after the matching v1 owner stops.
5. Observe, then expand.
6. Migrate check policy from `HEAD` + `legacy` to `GET` + `simple_http`, then
   `GET` + `full` after evidence supports it.
7. Remove v1 only after coverage, alerts, dashboards, and rollback gates are
   accepted.

## TeamCity And Container Inputs

Production deployment should be image-based and driven by the existing Systems
pipeline. The Monitor container needs:

- rendered `config/config.json` or equivalent environment-backed config;
- DB credentials or DB server-map config;
- API/dashboard/debug ports bound only as approved;
- StatsD endpoint and host path;
- WPCOM credential material for legacy notification mode;
- Veriflier endpoint list or trusted discovery config;
- mounted legacy stats directory only if old consumers still read files.

Safe deployment smoke for a fresh image:

```bash
./jetmon2 version
./jetmon2 validate-config
./jetmon2 status
curl -fsS http://127.0.0.1:${API_PORT}/api/v1/health
curl -fsS http://127.0.0.1:${API_PORT}/api/v1/ready || true
```

Readiness may be `starting` until the orchestrator publishes a fresh process
health row.

## Veriflier Deployment

V2 Monitors should use v2 Verifliers only. The production transport is
JSON/HTTP:

| Endpoint | Purpose |
| --- | --- |
| `/v2/status` | Health and capacity. |
| `/v2/check` | Batch check request. |

Deploy Verifliers before Monitor activation. Confirm:

- service is reachable only from trusted Monitor networks;
- auth token is present where configured;
- status reports enough usable/healthy vantages for the configured quorum;
- local firewall and host-level resource limits are appropriate;
- logs go to stdout/stderr.

Emergency lab/legacy endpoints are not part of normal production traffic.

## Operator CLI Config

Set up the API CLI on the operator workstation or bastion:

```bash
./bin/jetmon2 local-config init \
  --base-url=https://jetmon-api.example.internal \
  --token-file=/secure/path/jetmon-api-token \
  --auth-policy=same-origin
./bin/jetmon2 local-config show
```

For production writes to a non-local API, pass `--allow-remote` explicitly.

Useful checks:

```bash
./bin/jetmon2 api health --pretty
./bin/jetmon2 api ready --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api rollout capabilities --pretty
```

## Canary File

Copy and edit the fixture template:

```bash
cp docs/rollout-canaries.example.json rollout-canaries.json
```

Every URL must be an approved controlled canary or uptime-bench fixture. Do not
point canary probes at arbitrary customer sites. Canary probes are read-only and
non-authoritative; they do not write incident state, WPCOM notifications, or
check history.

## Range Rollout Checklist

For each bucket range:

1. Create or resume the rollout session.
2. Run preflight.
3. Run read-only smoke and canaries.
4. Seed v2 side tables in dry-run, then execute if clean.
5. Stop v1 for the exact range.
6. Run final reconcile.
7. Activate buckets in dry-run, then execute.
8. Observe activity, coverage, projection drift, WPCOM notifications, and
   Veriflier quorum.
9. Keep the rollback command ready until the observation window passes.

Guided flow:

```bash
./bin/jetmon2 api rollout guided \
  --bucket-min=0 \
  --bucket-max=99 \
  --canary-file=rollout-canaries.json \
  --allow-remote
```

Dry-run first:

```bash
./bin/jetmon2 api rollout guided \
  --bucket-min=0 \
  --bucket-max=99 \
  --canary-file=rollout-canaries.json \
  --dry-run
```

Rollback path:

```bash
./bin/jetmon2 api rollout guided \
  --bucket-min=0 \
  --bucket-max=99 \
  --rollback \
  --allow-remote
```

## API Primitive Flow

The guided CLI wraps these primitives. Use primitives directly only when the
guided path is insufficient.

```bash
./bin/jetmon2 api request POST /api/v1/rollout/sessions --json @session.json
./bin/jetmon2 api request POST /api/v1/rollout/preflight --json @range.json
./bin/jetmon2 api request POST /api/v1/rollout/smoke --json @range.json
./bin/jetmon2 api request POST /api/v1/rollout/seed --json @range-dry-run.json
./bin/jetmon2 api request POST /api/v1/rollout/final-reconcile --json @range.json
./bin/jetmon2 api request POST /api/v1/rollout/activate-buckets --json @range-execute.json
./bin/jetmon2 api request GET '/api/v1/rollout/bucket-coverage?bucket_min=0&bucket_max=99'
./bin/jetmon2 api request GET '/api/v1/rollout/activity-check?bucket_min=0&bucket_max=99'
./bin/jetmon2 api request GET '/api/v1/rollout/projection-drift?bucket_min=0&bucket_max=99'
```

Dry-run responses return a confirmation token. Execute requests must include
that token and an idempotency key.

## Observe After Activation

During the observation window, watch:

- `/api/v1/rollout/status`;
- bucket coverage and recent activity gates;
- projection drift;
- dashboard `/fleet`;
- StatsD check throughput, queue depth, errors, WPCOM sends/failures;
- Veriflier quorum report;
- event transitions and audit rows for the range;
- legacy status projection if legacy readers still depend on it.

Stop expansion if any range shows missing coverage, sustained queue growth,
unexpected WPCOM failures, projection drift, bad Veriflier quorum, or customer
impact that cannot be explained from event/audit evidence.

## Roll Back A Range

Rollback goal: stop v2 ownership before returning ownership to v1.

1. Run guided rollback or release buckets through the API.
2. Confirm v2 no longer owns the range.
3. Start the matching v1 owner.
4. Confirm v1 activity for the range.
5. Check projection drift and recent event transitions.
6. Record the reason and evidence packet.

Do not leave both owners active for the same range. If release fails, treat the
range as blocked and get Systems help before restarting v1.

## Check-Policy Migration

After the fleet is stable on v2, migrate policy in cohorts:

1. Compare `HEAD` and `GET` with non-authoritative sampled probes.
2. Stage `GET` + `simple_http` for a small cohort.
3. Observe false positives, missed alerts, WAF behavior, and support load.
4. Expand only after rollback criteria are understood.
5. Stage `GET` + `full` after body-rule evidence is acceptable.

Policy staging API:

```bash
./bin/jetmon2 api request POST /api/v1/rollout/compare-methods --json @range.json
./bin/jetmon2 api request POST /api/v1/rollout/stage-policy --json @stage-policy.json
```

Method comparison and smoke probes are non-authoritative. They must not write
incident state, WPCOM notifications, runtime freshness, or check history.

## Standalone Deliverer Rollout

Current single-binary deployments may run delivery workers inside `jetmon2`.
Standalone `jetmon-deliverer` is for separating outbound dispatch.

Conservative migration:

1. Start `jetmon-deliverer` with delivery disabled or `DELIVERY_OWNER_HOST`
   pointing at the current owner.
2. Validate config and DB access.
3. Confirm it observes queue state without claiming rows.
4. Move `DELIVERY_OWNER_HOST` to the standalone deliverer.
5. Disable embedded delivery or leave it guarded.
6. Watch pending, failed, abandoned, and per-worker health.

Active-active is supported by transactional row claims, but use single-owner
guarding during migration unless the rollout owner explicitly approves
active-active delivery.

## Final Fleet Cutover

Fleet completion requires:

- all v1 ranges have been replaced or intentionally left out of scope;
- dynamic bucket ownership is enabled where pinned migration ranges are no
  longer needed;
- `LEGACY_STATUS_PROJECTION_ENABLE` has an owner and retirement date;
- support and Systems know the evidence surfaces;
- WPCOM notification parity is accepted;
- dashboards and API health are green;
- rollback procedure has either expired or remains documented for the final
  transition window.

## Tear Down v1

Do not remove v1 until:

- v2 coverage is complete;
- no legacy reader needs v1-owned files/processes beyond the compatibility
  outputs v2 intentionally provides;
- WPCOM and support evidence is accepted;
- Systems has approved removal of old units/containers/host config;
- any retained fallback is explicit and tested.

## Rollout Verification Command

`make rollout-docs-verify` checks rollout CLI help, stale docs references,
guided dry-run output, JSON smoke, rehearsal flow, and staged systemd service
verification where available.

Run it before merging rollout-affecting docs or commands:

```bash
make rollout-docs-verify
```

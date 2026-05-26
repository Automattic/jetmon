# v1 To v2 Rollout Guide

Canonical production rollout and rollback guide. It folds the old quick
reference, prelaunch checklist, TeamCity/container notes, Veriflier notes, and
deliverer rollout notes into one runbook.

## Invariants

- Schema changes are approved and additive.
- v1 and v2 never own the same bucket range at the same time.
- Initial policy is `HEAD` + `legacy`.
- `LEGACY_STATUS_PROJECTION_ENABLE=true` until legacy readers move.
- V2 Veriflier quorum is healthy before Monitor activation.
- API rollout mutations use dry-run/execute confirmation.
- WPCOM stays v1-compatible during drop-in.
- Rollback releases v2 ownership before restarting v1.

## Launch Gates

| Gate | Evidence |
| --- | --- |
| Schema/config | Systems-applied DDL from [production-schema.md](production-schema.md), `schema validate`, `validate-config`, and API preflight pass. |
| Verifliers | `/v2/status` and quorum report are green. |
| Images | CI-built tags promoted by Systems. |
| WPCOM | Legacy notification path tested. |
| Operator CLI | Token, base URL, and `--allow-remote` policy are ready. |
| Canaries | Approved controlled canary file exists. |
| Rehearsal | Production-like Docker rehearsal covers schema validate, StatsD doctor, API preflight, guided dry-run, small-range execute, observation, and rollback. |
| Rollback | v1 start command, v2 release path, owner contact known. |
| Observability | API, dashboard, StatsD, logs visible. |

## Production Shape

1. Deploy v2 Verifliers beside the existing fleet.
2. Deploy v2 Monitors in standby/API-controlled mode.
3. Keep delivery embedded/guarded unless intentionally moving to
   `jetmon-deliverer`.
4. Activate one v1 bucket range at a time after Systems stops the matching v1
   owner.
5. Observe, then expand.
6. Migrate policy later: `HEAD` + `legacy` -> `GET` + `simple_http` ->
   `GET` + `full`.
7. Remove v1 only after coverage, alerts, dashboards, WPCOM, and rollback gates
   are accepted.

## Runtime Inputs

Monitor containers need rendered config, DB credentials or server-map config,
StatsD, WPCOM credential material, v2 Veriflier endpoints or discovery config,
and approved API/dashboard/debug bindings. Mount legacy stats only for consumers
that still read files.

Fresh image smoke:

```bash
./jetmon2 version
./jetmon2 schema validate
./jetmon2 validate-config
./jetmon2 status
curl -fsS http://127.0.0.1:${API_PORT}/api/v1/health
curl -fsS http://127.0.0.1:${API_PORT}/api/v1/ready || true
```

Readiness may report `starting` until process health is fresh.

## Verifliers

V2 Monitors use v2 Verifliers over JSON/HTTP(S) only:

| Endpoint | Purpose |
| --- | --- |
| `/v2/status` | Health and capacity. |
| `/v2/check` | Batch check request. |

Production Verifliers are public-web services. Do not run production
Monitor-to-Veriflier traffic over plain HTTP: bearer tokens and check payloads
must be protected by HTTPS. Use `VERIFLIER_TRANSPORT_SCHEME=https` or
per-Veriflier `scheme: "https"` in Monitor config. Terminate TLS either in
`veriflier2` with `VERIFLIER_TLS_CERT_PATH` / `VERIFLIER_TLS_KEY_PATH`, or at a
trusted proxy/load balancer in front of the Veriflier container.

Confirm HTTPS reachability, auth token presence, enough healthy vantages for
quorum, host resource limits, and stdout/stderr logging.

## Operator Setup

Rollout commands are normally run from an operator workstation or trusted jump
host, not by shelling into the Monitor container. The local `jetmon2` binary is
an API client: it reads `~/.config/jetmon2.conf` or `JETMON_API_CONFIG`, talks to
one approved running Monitor API, and that Monitor performs rollout actions
against the fleet/database. If the API is not directly reachable, use an
approved VPN, bastion, or SSH tunnel and point `--base-url` at that endpoint.
Any non-local Monitor API path must be HTTPS, either through native
`API_TLS_CERT_PATH` / `API_TLS_KEY_PATH` or through an approved TLS-terminating
proxy/load balancer.

```bash
./bin/jetmon2 local-config init \
  --base-url=https://jetmon-api.example.internal \
  --token-file=/secure/path/jetmon-api-token \
  --auth-policy=same-origin

./bin/jetmon2 api health --pretty
./bin/jetmon2 api ready --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api rollout capabilities --pretty
```

Production writes to a non-local API require `--allow-remote`. Read-only health,
ready, capability, and status checks can run without it.

Canary template:

```bash
cp docs/rollout-canaries.example.json rollout-canaries.json
```

Every canary URL must be controlled or an uptime-bench fixture.

## Per-Range Flow

1. Create/resume rollout session.
2. Run preflight.
3. Run read-only smoke and canaries.
4. Seed/adopt v2 side tables keyed by `jetpack_monitor_site_id`: dry-run, then
   execute.
5. Stop v1 for the exact range.
6. Run final reconcile.
7. Activate buckets: dry-run, then execute.
8. Observe coverage, activity, drift, WPCOM, and quorum.
9. Keep rollback ready until the observation window passes.

Guided commands:

```bash
./bin/jetmon2 api rollout guided \
  --bucket-min=0 \
  --bucket-max=99 \
  --canary-file=rollout-canaries.json \
  --dry-run

./bin/jetmon2 api rollout guided \
  --bucket-min=0 \
  --bucket-max=99 \
  --canary-file=rollout-canaries.json \
  --allow-remote

./bin/jetmon2 api rollout guided \
  --bucket-min=0 \
  --bucket-max=99 \
  --rollback \
  --allow-remote
```

## Observe

Watch:

- `/api/v1/rollout/status`;
- bucket coverage, activity, and projection-drift gates;
- dashboard `/fleet`;
- check throughput, queue depth, errors, WPCOM sends/failures;
- Veriflier quorum report;
- event transitions and audit rows for the range.

Stop expansion on missing coverage, sustained queue growth, unexpected WPCOM
failures, projection drift, bad quorum, or unexplained customer impact.

## Rollback

1. Run guided rollback or release buckets through the API.
2. Confirm v2 no longer owns the range.
3. Start the matching v1 owner.
4. Confirm v1 activity for the range.
5. Check projection drift and recent transitions.
6. Record reason and evidence.

If release fails, do not restart v1; get Systems help.

## Policy Migration

After v2 is stable:

1. Compare `HEAD` and `GET` with sampled non-authoritative probes.
2. Stage `GET` + `simple_http` for a small cohort.
3. Observe false positives, missed alerts, WAF behavior, and support load.
4. Expand only with rollback criteria.
5. Stage `GET` + `full` after body-rule evidence is acceptable.

```bash
./bin/jetmon2 api request POST /api/v1/rollout/compare-methods --json @range.json
./bin/jetmon2 api request POST /api/v1/rollout/stage-policy --json @stage-policy.json
```

Comparison and smoke probes are read-only and non-authoritative.

## Standalone Deliverer

Conservative migration:

1. Start `jetmon-deliverer` guarded or disabled.
2. Validate config and DB access.
3. Confirm it observes queues without claiming rows.
4. Move `DELIVERY_OWNER_HOST` to the standalone process.
5. Disable or guard embedded delivery.
6. Watch pending, failed, abandoned, and worker health.

Transactional row claims support active-active, but single-owner guarding is the
safer migration default.

## Finish And Tear Down

Fleet completion requires full v2 coverage or explicit exclusions, dynamic
ownership where pinned ranges are no longer needed, a retirement plan for legacy
projection, accepted support evidence, green dashboards/API, and an explicit
rollback window.

Do not remove v1 until Systems approves old unit/container/host cleanup and any
remaining fallback is tested.

Leave additive v2 tables in place during rollback. Production rollback is a
traffic/ownership change, not a destructive schema rollback.

## Verification

```bash
make rollout-docs-verify
```

This checks rollout CLI help, stale docs references, guided dry-run output, JSON
smoke, rehearsal flow, and systemd unit verification where available.

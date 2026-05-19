# Operations Guide

This guide collects production-facing details that used to live in the root
README: configuration, rollout, dashboard checks, delivery workers, metrics, and
debugging.

## Configuration

Jetmon configuration lives in `config/config.json`. Copy
`config/config-sample.json` to get started. Docker can generate this file from
`config-sample.json` and `docker/.env` when it is not present.

Use `SIGHUP` or `./jetmon2 reload` to reload configuration without restarting.

Key settings:

| Key | Default | Description |
|---|---:|---|
| `NUM_WORKERS` | 60 | Goroutine pool size/floor; 0 uses the default floor |
| `NUM_TO_PROCESS` | 40 | Legacy compatibility setting; does not cap Go scheduler throughput |
| `DATASET_SIZE` | 100 | Database fetch page size for scheduler work; not a total round cap; 0 uses the default |
| `NUM_OF_CHECKS` | 3 | Local failures before Veriflier escalation |
| `TIME_BETWEEN_CHECKS_SEC` | 30 | Legacy compatibility setting retained for copied v1-style configs |
| `MIN_TIME_BETWEEN_ROUNDS_SEC` | 300 | Fixed-cadence full-fleet pass interval when variable intervals are disabled |
| `NET_COMMS_TIMEOUT` | 10 | Default per-check HTTP timeout in seconds |
| `CHECK_DNS_RESOLVERS` | `[]` | Optional HTTP-check recursive resolver IPs, with optional ports; restart required after changes |
| `BODY_READ_MAX_BYTES` | 1048576 | Success-path body-read budget in bytes for unknown/large responses |
| `BODY_READ_MAX_MS` | 250 | Post-header body-phase budget in milliseconds for budgeted reads (unknown/large responses); 0 uses the default |
| `KEYWORD_READ_MAX_BYTES` | 1048576 | Max bytes scanned when keyword checks are enabled; 0 uses the default |
| `KEYWORD_READ_MAX_MS` | 0 | Keyword read budget in milliseconds, 0 inherits full request timeout envelope |
| `PEER_OFFLINE_LIMIT` | 3 | Veriflier agreements required to confirm downtime |
| `WORKER_MAX_MEM_MB` | 0 | Optional Go runtime memory threshold that triggers worker-pool drain; 0 disables the artificial cap |
| `BUCKET_TOTAL` | 1000 | Total bucket range across all hosts |
| `BUCKET_TARGET` | 500 | Maximum buckets this host should own; 0 means all buckets |
| `BUCKET_HEARTBEAT_GRACE_SEC` | 600 | Seconds before a silent host's buckets are reclaimed |
| `PINNED_BUCKET_MIN` / `PINNED_BUCKET_MAX` | unset | Static bucket range used by the [v1-to-v2 migration runbook](v1-to-v2-migration.md) |
| `ALERT_COOLDOWN_MINUTES` | 30 | Default cooldown between repeated alerts per site |
| `LEGACY_STATUS_PROJECTION_ENABLE` | true | Keep v1 status fields projected during the [v1-to-v2 migration](v1-to-v2-migration.md) |
| `LOG_FORMAT` | `text` | `text` or `json` |
| `DASHBOARD_PORT` | 8080 | Internal operator dashboard port, 0 disables it |
| `DASHBOARD_BIND_ADDR` | 127.0.0.1 | Dashboard listener address; keep localhost unless a trusted management network requires remote access |
| `API_PORT` | 0 | Internal REST API port, 0 disables it |
| `DELIVERY_OWNER_HOST` | empty | Optional host allowed to run embedded delivery workers |
| `DEBUG_PORT` | 6060 | localhost-only pprof port, 0 disables it |
| `WPCOM_NOTIFY_ENABLE` | true | Allow legacy WPCOM status-change notification calls; set false for internal-only tests |
| `WPCOM_NOTIFY_MODE` | `legacy` | `legacy` uses the v1-compatible `/jetmon/?data=...` client-certificate path; `modern` is retained for WPCOM contract testing only |
| `WPCOM_NOTIFY_LEGACY_CERT_PATH` / `WPCOM_NOTIFY_LEGACY_KEY_PATH` | `certs/jetmon.crt` / `certs/jetmon.key` | Client certificate/key used when notifications are enabled in legacy mode |
| `EMAIL_TRANSPORT` | `stub` | `stub`, `smtp`, or `wpcom` |
| `SCHEDULER_ENGINE` | `legacy` | `legacy` round/page scheduler or `streaming` v2-native scheduler |
| `STREAMING_LEGACY_PROJECTION_INTERVAL_MIN` | 15 | Coarse sidecar freshness rollback projection interval for streaming mode |
| `STREAMING_TARGET_RELOAD_SEC` | 300 | Active site config reload cadence for streaming mode |

Scheduler behavior:

- `DATASET_SIZE` limits one database page. Jetmon continues fetching pages until
  due work is drained, so a low value should not cause unchecked sites by itself.
  `DATASET_SIZE=0` uses the default page size.
- `NUM_WORKERS=0` uses the default worker floor instead of failing validation.
  In streaming mode this is not a throughput cap; the engine derives a higher
  worker target from active site rate and observed latency.
- `BUCKET_TARGET=0` expands to `BUCKET_TOTAL`, which is useful for a single
  monitor host in test fleets and removes one more manual capacity-tuning knob.
- A full worker queue applies backpressure; checks remain pending instead of
  being dropped.
- With `USE_VARIABLE_CHECK_INTERVALS=true`, Jetmon polls for newly due work on a
  short idle interval and uses each site's maintained
  `jetmon_site_runtime.next_check_at` timestamp to decide what to check.
  `next_check_at` is recalculated after every check: successful checks use
  `jetmon_site_runtime.last_checked_at + check_interval`, while failed checks
  are scheduled for a bounded one-minute follow-up when the normal interval is
  longer. `MIN_TIME_BETWEEN_ROUNDS_SEC` is only the fixed-cadence pass interval
  when variable intervals are disabled. Use this mode for production-like
  freshness and capacity tests.
- Watch the `scheduler.round.*` StatsD metrics during capacity tests. In
  particular, `due_start`, `selected`, `completed`, `outstanding`, and
  `due_remaining` show whether freshness pressure is clearing or building.
  Exact `due_start` / `due_remaining` and legacy projection-drift checks are
  sampled about once per minute in variable-interval mode so broad operator
  reporting queries do not run on every short scheduler poll. Use
  `scheduler.round.due_count_sampled.count` to distinguish sampled polls from
  intentionally skipped reporting polls.
- With `SCHEDULER_ENGINE=streaming`, Jetmon uses a v2-native time-wheel
  scheduler instead of database due-row polling. Active sites are spread over
  stable phases inside each site's interval, healthy probes avoid per-check
  history/freshness writes, and the checker pool target is derived from active
  site rate plus observed latency. Streaming mode keeps event, retry, verifier,
  SSL/TLS, recovery, and WPCOM behavior on the existing v2 incident path. It
  batches sidecar `last_checked_at`/`next_check_at` projection at
  `STREAMING_LEGACY_PROJECTION_INTERVAL_MIN` so rollback to the legacy scheduler
  has bounded freshness loss rather than exact per-check freshness. The
  projection interval is constrained to the accepted 5-15 minute rollback window
  and applies uniformly across sites. It intentionally does not shrink to match
  5-minute site cadence, because that makes rollback freshness writes scale with
  active fleet size in the hot path. Pending projection writes are also flushed
  in rate-sized batches so a backlog cannot turn one flush into a large
  lock-heavy update burst. Streaming mode intentionally uses larger in-memory
  due/result/work buffers than the legacy scheduler; low RSS in capacity tests is
  expected to be spent on those buffers before check dispatch is throttled.
- Treat the current single-host streaming capacity evidence as validated through
  2 million active internal-only targets on five-minute intervals, not as an
  unlimited ceiling. The 2026-05-12 2 million-target run had full target
  coverage, no stale or never-seen targets, p95 target age around 270 seconds,
  max target age below 285 seconds, process RSS around 6.3 GB peak, and host CPU
  around 36% average. A 4 million-target run exceeded the current stable
  envelope: timeout pressure grew, queue depth reached its cap, pending work
  climbed into the millions, and target coverage stopped at roughly 88%. During
  larger tests or rollout rehearsals, watch `scheduler.streaming.pending.count`,
  `queue_depth`, `result_depth`, `max_lag`, `dispatch_budget_limited`, timeout
  counters, process RSS, and host CPU together; backlog plus timeout growth is a
  hold point even when raw CPU still appears available.

See [../config/config.readme](../config/config.readme) for the full option
reference.

Copied v1 config files are accepted, but startup and `jetmon2 validate-config`
warn for deprecated aliases, ignored v1-only keys, and compatibility keys whose
meaning changed in v2. Treat these warnings as cleanup work before production
activation. The most important ignored v1-only keys are
`WORKER_MAX_CHECKS` and `TIMEOUT_FOR_REQUESTS_SEC`; common compatibility keys
such as `NUM_TO_PROCESS`, `BATCH_SIZE`, `VERIFLIER_BATCH_SIZE`,
`SQL_UPDATE_BATCH`, `TIME_BETWEEN_CHECKS_SEC`, and
`TIME_BETWEEN_NOTICES_MIN` also warn because they no longer tune the v2
scheduler or notification flow. `DB_UPDATES_ENABLE`, `BUCKET_NO_MIN/MAX`, and
`VERIFIERS[].grpc_port` remain aliases but should be replaced with their v2
names.

Production database server-map refresh is handled by a config-sync sidecar for
the first TeamCity/docker-deploy rollout, with host-side systemd sync kept as a
fallback. Use [production-teamcity-rollout.md](production-teamcity-rollout.md)
for the deployment plan, secret boundary, and DB server-map hot-reload details.
When `DB_SERVER_MAP_PATH` is set, v2 reads `db-servers.php` directly, separates
read/write endpoints from the `misc` dataset, and reloads changed connection
details on the `DB_CONFIG_UPDATES_MIN` cadence after ping validation. Check
`GET /api/v1/monitor/db-config` or the dashboard `db-config` dependency to
confirm the next scheduled check, last changed map observed, and last successful
hot reload.

Initial production rollout should keep `WPCOM_NOTIFY_MODE=legacy`. That mode
matches v1's client-certificate HTTPS `GET` to the legacy `/jetmon/` endpoint
and keeps the auth token inside the JSON payload. `WPCOM_NOTIFY_MODE=modern`
uses the bearer-token JSON `POST` endpoint and should be limited to local,
staging, or WPCOM contract tests until WPCOM explicitly approves it for
production. `jetmon2 validate-config` reports the selected mode; when
notifications are enabled it warns if modern mode is selected or if the legacy
certificate/key files are not readable.

Checker policy note: HTTP `>= 400` responses are classified immediately by status
code and do not depend on body drain completion. Strict EOF/truncation validation
applies only to eligible successful finite responses and is skipped for `101`,
upgrade handshakes, and `text/event-stream` when no keyword is configured. In
strict finite mode (known `Content-Length <= BODY_READ_MAX_BYTES`), body-phase
timeout is bounded by the request timeout envelope, not `BODY_READ_MAX_MS`.
Keyword read-budget exhaustion is classified as `ErrorTimeout`. Event metadata
keeps legacy `failure_class` for WPCOM-compatible status types and adds
operator-facing `detector_class` plus `body_read` evidence for partial/truncated
responses.

## Probe Safety Cleanup

Use `./jetmon2 site-safety unsafe-urls` to scan active legacy
`jetpack_monitor_sites.monitor_url` values with the same public-target guard
used by API admission and runtime probe execution. The default mode is a
dry-run: it prints bounded examples plus `scanned_active`, `unsafe`, `flagged`,
and `deactivated` counts without changing rows.

Run with `--execute` only after reviewing the dry-run output. Execution records
one `jetmon_site_safety_flags` row for each unsafe active monitor URL, then sets
that legacy row's `monitor_active` value to false. It does not delete site
rows, does not create downtime events, and does not send WPCOM down/recovery,
webhook, or alert-contact notifications. Runtime probe-safety blocks also write
open `jetmon_site_safety_flags` rows when the monitor row is known, so
operators can query one table for cleanup and recurring unsafe-target findings.

## Production Host Setup

1. Install `bin/jetmon2` as `/opt/jetmon2/jetmon2`, or update the service unit
   if your deployment system uses a different path.
2. Install `systemd/jetmon2.service` to `/etc/systemd/system/` and run
   `systemctl daemon-reload`.
3. Create `/opt/jetmon2/config` and `/opt/jetmon2/stats`, owned by the `jetmon`
   service user.
4. Create `/opt/jetmon2/config/jetmon2.env` with database credentials and auth
   tokens. See `config/db-config-sample.conf`. For production server-map use,
   set `DB_SERVER_MAP_PATH` and `DB_SERVER_MAP_DATACENTER` instead of baking
   DB passwords into the env file. For container rollout, keep real env/config
   files host-local and out of the image.
5. For TeamCity/docker-deploy rollout, provide `config-sync.env` from
   `config/jetmon-config-sync-sample.env` to the config-sync sidecar and share
   only the generated config-source path with the Monitor. If docker-deploy
   cannot support that sidecar shape, install
   `systemd/jetmon-config-sync.service` and
   `systemd/jetmon-config-sync.timer` as the host-side fallback.
6. Copy or generate `config/config.json`.
7. Set `BUCKET_TARGET` to the desired maximum bucket count for the host.
8. Run `./jetmon2 migrate`.
9. Run `systemd-analyze verify /etc/systemd/system/jetmon2.service` after the
   binary exists at the path used by `ExecStart`.
10. Start the service with
    `systemctl enable --now jetmon2 && systemctl is-active --quiet jetmon2`.

Runtime logs are collected from stdout/stderr by systemd or the container
runtime. V2 does not write v1 `jetmon.log` or `status-change.log` files by
default.

Manual commands such as `migrate`, `validate-config`, and `rollout` need the
same `DB_*` environment that systemd reads from
`/opt/jetmon2/config/jetmon2.env`; systemd's `EnvironmentFile` is not loaded for
commands run directly from a shell.

## v1 To v2 Migration

Use [v1-to-v2-migration.md](v1-to-v2-migration.md) for the full production
migration process. It covers preparation, additive migrations, pinned bucket
mode, replacing v1 on the same server, moving a range to a fresh v2 server,
monitoring, revert paths, dynamic ownership cutover, and v1 teardown.
Use [rollout-quick-reference.md](rollout-quick-reference.md) as the one-page
operator command checklist during rehearsals and rollout windows.

Use `./jetmon2 rollout guided --file=<ranges.csv> --host=<v1-host>
--runtime-host=<v2-host> --bucket-min=N --bucket-max=N --bucket-total=N
--v1-stop-command='<cmd>' --v1-start-command='<cmd>'` for the preferred
interactive rollout path. Run it from the staged v2 runtime host, not from a
separate orchestration host. For fresh-server rollouts, `--host` is the old v1
host and `--runtime-host` is the new v2 host where the guided command runs; if
the v1 stop/start commands use `ssh`, that runtime host must have SSH access to
the old v1 host. The command verifies that `--log-dir` is writable before it
starts, writes a transcript plus resume state, explains each gate, asks before
continuing, and requires typed confirmations before v1/v2 stop/start
transitions. By default it prints service commands for the operator to run from
the runtime host; add `--execute-operator-commands` only when the operator
intentionally wants the guided command to execute those commands after
confirmation. Use `--rollback` for the guided return-to-v1 path and
`--dry-run` for rehearsal.

Use `./jetmon2 rollout rehearsal-plan --file=<ranges.csv> --host=<host>
--bucket-min=N --bucket-max=N --mode=same-server` to print the ordered command
sequence for one host replacement. Use `--mode=fresh-server` plus
`--runtime-host=<new-v2-hostname>` when the new v2 hostname differs from the v1
host recorded in the static bucket plan. Confirm SSH from the new v2 runtime
host to the old v1 host before using SSH-based v1 commands. Add
`--v1-stop-command` and
`--v1-start-command` so the generated plan includes the exact cutover and
rollback commands instead of comments. Add `--bucket-total=N` when rehearsing
against an explicit bucket count, and `--systemd-unit=<path>` when the staged
unit is not `/etc/systemd/system/jetmon2.service`.

Before stopping v1 for a host, use `./jetmon2 rollout host-preflight
--file=<ranges.csv> --host=<v1-host> --runtime-host=<v2-host>
--bucket-min=N --bucket-max=N` to bundle the static plan match, config parse,
DB connectivity, pinned safety checks, and staged systemd validation. This is
the pre-stop gate; it runs the older pinned safety check internally.

After a pinned v2 host starts, use `./jetmon2 rollout cutover-check
--host=<v2-host> --bucket-min=N --bucket-max=N --since=15m` to run the
post-start pinned preflight, recent activity check, dashboard status check, and
projection-drift report together. Treat the immediate run as a smoke gate
because recent activity can still include v1 writes. After one full expected v2
check round, rerun it with `--require-all`, then run `./jetmon2 telemetry
report --since=15m` before moving to the next host. The telemetry report is
read-only window-level evidence for WPCOM down/recovery parity and explanation
coverage; warnings are rollout hold points, and quiet windows may need a wider
`--since` range.

Use `--output=json` on rollout gate commands when wiring them into Systems
automation. The command still exits non-zero on failed checks, and stdout
contains `ok`, the command name, parsed output lines, and failure messages.
Use `./jetmon2 rollout state-report --since=15m` for an operator snapshot of
ownership mode, bucket coverage, activity freshness, projection drift, delivery
owner state, and the suggested next action.

## v2 Rolling Updates

After all monitor hosts are on v2 dynamic bucket ownership, update one host at a
time. Surviving hosts absorb the draining host's buckets during the update
window.

```bash
systemctl stop jetmon2 && ! systemctl is-active --quiet jetmon2
./jetmon2 migrate
systemctl start jetmon2
./jetmon2 status
```

Repeat for the next host.

## Delivery Workers

In the embedded deployment, setting `API_PORT` to a non-zero value starts the
internal API and makes webhook and alert-contact delivery workers eligible to
run inside `jetmon2`.

Use `DELIVERY_OWNER_HOST` when only one API-enabled host should dispatch
outbound deliveries during rollout. If it is empty, delivery workers start on
any host with `API_PORT` enabled.

`bin/jetmon-deliverer` is the standalone process boundary for outbound delivery.
It starts the same webhook and alert-contact workers without starting the
monitor, API, dashboard, or bucket ownership loop. Delivery rows are claimed
transactionally, so multiple workers do not claim the same pending row.

For conservative single-owner rollout, validate the deliverer-specific config
before enabling the service:

```bash
JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
  /opt/jetmon2/bin/jetmon-deliverer validate-config \
    --require-owner-match \
    --require-api-disabled
```

Add `--require-email-delivery` when real alert-contact email delivery is
expected in that environment.

Run `systemd-analyze verify /etc/systemd/system/jetmon-deliverer.service` after
`/opt/jetmon2/bin/jetmon-deliverer` exists, or against an equivalent staged
deployment root where the service's `ExecStart` and `ExecStartPre` paths are
present.

During rollout, inspect the shared webhook and alert-contact delivery queues
from the same environment the service uses:

```bash
JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
  /opt/jetmon2/bin/jetmon-deliverer delivery-check --since=15m
```

Use thresholds for automated gates:

```bash
JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
  /opt/jetmon2/bin/jetmon-deliverer delivery-check \
    --since=15m \
    --max-due=0 \
    --max-abandoned=0 \
    --max-failed=0 \
    --output=json
```

`delivery-check` also reports `failed_since`, `oldest_pending_age_sec`, and
`oldest_due_age_sec`. Use `--require-recent-webhook-delivery` or
`--require-recent-alert-delivery` when a rollout gate needs each delivery family
to prove a successful send independently.

See [jetmon-deliverer-rollout.md](jetmon-deliverer-rollout.md) for the rollout
and rollback path.

## Runtime Checks

Status and reload commands:

```bash
./jetmon2 status
./jetmon2 reload
./jetmon2 drain
```

Monitor stats compatibility checks:

```bash
./jetmon2 api request --pretty GET /api/v1/monitor/stats
./jetmon2 api request GET '/api/v1/monitor/stats?file=totals'
./jetmon2 api request --pretty GET /api/v1/monitor/db-config
```

`/api/v1/monitor/stats` is the preferred production-compatible replacement for
external readers of `stats/sitespersec`, `stats/sitesqueue`, and `stats/totals`.
It renders from the Monitor's in-memory snapshot rather than reading the files
back from disk, so TeamCity/docker-deploy consumers do not need a host bind
mount just to inspect the legacy stats surface. Keep direct file reads only for
local debugging, host-side fallback installs, or an explicitly approved Systems
mount.

The operator dashboard is available on `DASHBOARD_BIND_ADDR:DASHBOARD_PORT`
when enabled. It defaults to `127.0.0.1`, because the host and fleet dashboards
are unauthenticated and expose internal dependency details, rollout commands,
host names, ports, bucket ownership, and delivery posture. Bind it to a remote
address only behind trusted operator-network controls.

The host dashboard shows a red/amber/green host summary with named issues, worker
count, active checks, queue depth, retry queue depth, throughput, round time,
owned buckets, rollout guard state, RSS memory, Go runtime system memory, WPCOM
circuit-breaker state, dependency health for MySQL, Verifliers, WPCOM, StatsD,
local stats writes, and the rollout commands an operator is most likely to
need from that host.

When `VERIFLIER_DISCOVERY_MODE` is `shadow` or `active`, host health also shows
Veriflier discovery status from the DB registry. Shadow mode is the rollout
gate: compare enabled `jetmon_veriflier_vantages` rows against the static
`VERIFIERS` list until there is no drift. Active mode uses enabled usable
registry rows and falls back to static config if discovery is unavailable or
empty. Monitor-collected rows in `jetmon_veriflier_agents` expose liveness and
capacity without giving Veriflier hosts DB credentials; they do not create
trusted quorum votes.

Use the read-only discovery report before switching from `shadow` to `active`:

```bash
./jetmon2 verifliers discovery-report --output=text
./jetmon2 verifliers discovery-report --output=json
```

The report probes configured static Verifliers for v2 `vantage.id` values,
compares them with enabled trusted registry rows, checks recent agent telemetry,
and reports green/amber/red status plus a suggested next action. It never prints
Veriflier auth tokens; text and JSON output only expose whether each static or
registry entry has a token present.

### Veriflier Discovery Warning Checklist

Use this checklist for warnings from the host dashboard, fleet dashboard, or
`jetmon2 verifliers discovery-report`.

**Green**

- Static configured Verifliers report v2 status with stable `vantage.id`
  values.
- Enabled trusted registry rows match the static quorum vantages.
- Recent active agent telemetry exists for each enabled usable registry vantage.
- Capacity and queue depth look normal for the current rollout window.

Action: continue the approved rollout step. In `shadow` mode, keep observing at
least one expected check/report interval before changing to `active`.

**Amber**

- `static_probe_failed`: verify monitor-to-Veriflier network access, auth token
  presence, and `/v2/status` response from the monitor runtime host.
- `static_legacy_only`: deploy or restart the Go `veriflier2` binary before
  relying on v2 identity or active discovery.
- `static_vantage_missing`: set a stable `VERIFLIER_VANTAGE_ID`; do not advance
  while a v2 Veriflier lacks a quorum identity.
- `static_missing_enabled_registry`: create or enable the matching
  `jetmon_veriflier_vantages` row after confirming the static endpoint is a
  trusted quorum vantage.
- `enabled_registry_missing_static`: confirm the registry row is intentional.
  If it is staged early, leave discovery in `shadow`; if it is stale, disable
  or correct the row.
- `registry_enabled_incomplete`: fill endpoint host, endpoint port, and auth
  token before active discovery can use the row.
- `static_registry_endpoint_mismatch` or `agent_registry_endpoint_mismatch`:
  decide whether the static config, registry row, or load-balanced endpoint is
  authoritative, then make them agree.
- `static_registry_auth_presence_mismatch`: fix missing token material on the
  side that should be active. The report will not print token values.
- `agent_without_registry`: treat the agent as untrusted telemetry. Add a
  registry row only if the vantage is intentionally approved for quorum.
- `enabled_registry_without_active_agent`: verify monitors can poll
  authenticated `/v2/status`, check the Veriflier process state, and widen
  `--stale-after` only if the report window is intentionally longer than the
  heartbeat interval.
- `duplicate_active_agent_endpoints`: confirm whether the agents are replicas
  behind one endpoint or an accidental split endpoint for the same vantage.
- Fleet/dashboard stale telemetry warnings: inspect `/api/fleet` or rerun
  `verifliers discovery-report`; stale rows are last-known state, not proof a
  Veriflier is still serving traffic.

Action: hold before switching from `shadow` to `active`, fix the named drift,
then rerun `validate-config` and `verifliers discovery-report`.

**Red**

- `static_vantage_duplicate`: two configured Verifliers report the same
  `vantage.id`; only one vote would count. Fix static config or endpoint
  identity before rollout.
- `active_without_usable_registry`: active discovery has no usable enabled
  trusted vantages and would fall back to static config. Treat this as a bad
  active-mode posture.
- Active discovery plus incomplete enabled registry rows: fill or disable those
  rows before relying on discovery traffic.
- Dashboard red Veriflier dependency health: the monitor cannot safely prove
  Veriflier contract, identity, or reachability from that host.

Action: do not advance rollout. Return to `static` or `shadow` if needed, fix
the registry/static/agent mismatch, and rerun the report from the monitor
runtime host.

The fleet dashboard is available at `/fleet` on the same listener. It summarizes
all rows in `jetmon_process_health` alongside `jetmon_hosts` dynamic bucket
coverage, delivery backlog, delivery-owner posture, dependency rollups,
Veriflier dependency health reported by monitor hosts, Veriflier discovery
registry state, and global legacy projection drift. It also shows per-table
delivery queue counts, per-host bucket-owner rows, trusted Veriflier vantages,
monitor-collected Veriflier agent telemetry, capacity, discovery modes, and
duplicate endpoint warnings. It uses stale heartbeat thresholds when deciding
whether a process, dynamic bucket owner, or Veriflier telemetry row is healthy.

When fleet projection drift is red, run `./jetmon2 rollout projection-drift
--limit=100` on an operator host. The command reports bucket/status summaries,
likely causes, and sample rows before listing individual mismatches, and it
does not repair the legacy projection automatically.

Capture the cause labels from rehearsal and early production incidents. A
future dry-run repair planner should be based on those observed patterns, not
on assumed failure modes, because the unsafe case is repairing `site_status`
while the event rows or transitions still need investigation.

Fleet snapshots are cached briefly by the dashboard process so multiple open
operator tabs do not run the full fleet query set on every refresh.

### Fleet Dashboard Operation

Enable the dashboards with:

```json
{
  "DASHBOARD_PORT": 8080,
  "DASHBOARD_BIND_ADDR": "127.0.0.1"
}
```

Open the host dashboard at `http://127.0.0.1:8080/` and the fleet dashboard at
`http://127.0.0.1:8080/fleet`. If an operator needs remote access, prefer an SSH
tunnel or a trusted management network instead of binding the dashboard to a
public interface:

```bash
ssh -L 8080:127.0.0.1:8080 <jetmon-host>
```

The fleet dashboard is read-only and unauthenticated. It does not discover or
scrape other hosts over HTTP; every `jetmon2` monitor dashboard reads the same
shared MySQL state and can serve the fleet view if `DASHBOARD_PORT` is enabled.
Standalone `jetmon-deliverer` processes do not serve a dashboard, but they do
publish their own rows to `jetmon_process_health`.

The dashboard accepts only `GET` and `HEAD` requests for static and JSON views,
and `/api/fleet` returns the same complete snapshot the HTML page renders for
local operator scripts:

```bash
curl -sS http://127.0.0.1:8080/api/fleet
```

Read the top summary first:

- **Red**: do not advance rollout. Typical causes are stale process heartbeats,
  broken dynamic bucket coverage, projection drift, failed/abandoned delivery
  rows, or red process dependency health.
- **Amber**: operator attention needed before the next change. Typical causes
  are pinned or mixed bucket ownership during rollout, due delivery rows,
  delivery workers without a clear owner, no process snapshots yet, or amber
  dependency health.
- **Green**: no fleet-level blocker is visible. Continue normal monitoring or
  the next approved rollout step.

During the v1-to-v2 rollout, pinned monitor hosts should make bucket coverage
show `mode=pinned` and amber. After the final dynamic-ownership cutover,
`mode=dynamic` should be green with fresh `jetmon_hosts` coverage and no gaps or
overlaps. A `mode=mixed` result means some monitor hosts still report pinned
ownership while others report dynamic ownership; treat that as a rollout state
to resolve intentionally.

For delivery ownership, green means the visible fresh delivery-capable process
set has a consistent owner posture. Amber means the fleet either has queued
delivery rows with no fresh worker, multiple owner values, enabled workers
without `DELIVERY_OWNER_HOST`, or a mix of explicit and unset ownership. Fix the
delivery-owner plan before moving outbound delivery responsibility.

The dashboard exposes these local JSON endpoints:

```text
GET /api/state   # raw host state snapshot
GET /api/health  # dependency health list
GET /api/host    # combined host state, dependency health, and summary
GET /api/fleet   # combined fleet rollup, process health, buckets, delivery, drift
```

Long-running `jetmon2` and `jetmon-deliverer` processes also publish compact
heartbeat snapshots to `jetmon_process_health`. That table is the durable data
source for the fleet dashboard. Treat stale `updated_at` values as
unknown/unhealthy; the row is the last reported process state, not proof that a
host is still alive. The dashboard listener remains unauthenticated for both
host and fleet views, so keep `DASHBOARD_BIND_ADDR` on loopback unless network
access is restricted to trusted operator hosts.

Bucket coverage can be inspected directly:

```sql
SELECT host_id, bucket_min, bucket_max, last_heartbeat, status
FROM jetmon_hosts
ORDER BY bucket_min;
```

Process health can be inspected directly:

```sql
SELECT process_id, host_id, process_type, state, updated_at
FROM jetmon_process_health
ORDER BY process_type, host_id;
```

For health rollups, memory, and runtime scheduler pressure:

```sql
SELECT process_id, state, health_status, rss_mem_mb, go_sys_mem_mb,
       runtime_goroutines, runtime_threads, updated_at
FROM jetmon_process_health
ORDER BY health_status DESC, updated_at;
```

Delivery queues can be inspected directly:

```sql
SELECT status, COUNT(*), MIN(COALESCE(next_attempt_at, created_at))
FROM jetmon_webhook_deliveries
GROUP BY status;

SELECT status, COUNT(*), MIN(COALESCE(next_attempt_at, created_at))
FROM jetmon_alert_deliveries
GROUP BY status;
```

A host whose heartbeat is older than `BUCKET_HEARTBEAT_GRACE_SEC` will have its
buckets reclaimed by peers on their next round.

## Metrics And Logs

StatsD metrics retain the v1 prefix:

```text
com.jetpack.jetmon.<hostname>
```

In production containers, set `HOSTNAME` in config to the v1-compatible
identity, normally `<datacenter>.<node>`, so process health and metric prefixes
stay stable even when the Docker runtime hostname is a container ID. The Docker
entrypoint accepts `JETMON_HOSTNAME` as the env input when rendering config. v1
derived this value by taking the first two labels of the production hostname and
reversing them: `<node>.<datacenter>.<domain>` became
`<datacenter>.<node>`. For example, `jetmon-prod-1.dfw1.example.com` should use
`JETMON_HOSTNAME=dfw1.jetmon-prod-1` during config rendering. That produces
`com.jetpack.jetmon.dfw1.jetmon-prod-1.<metric>`, matching the v1 dashboard path
shape. If both are present at runtime, `HOSTNAME` from config wins. Keep the
value stable and low-cardinality: do not include container IDs, release SHAs,
process IDs, ports, or random suffixes. Leave it unset or empty for local
development to use the process hostname fallback.

Important metric groups include:

- Worker pool capacity and active goroutines
- Sites processed per second
- Round completion time
- Scheduler page count, selected/dispatched/completed rows, outstanding checks,
  backpressure waits, stale/duplicate results, and sampled due backlog
- Streaming failure-pressure suppression via
  `scheduler.streaming.pressure_suppressed.count`, which shows local
  timeout/connect failures that were treated as monitor-side pressure instead
  of opening noisy incident side effects for otherwise running sites
- Scheduler phase timings for dispatch, wait, result processing,
  sidecar freshness writes, check-history inserts, SSL expiry
  writes, and event handling
- Scheduler write row/error counters for freshness, check history, and SSL
  expiry updates
- Staged-rollout check cohort counters under
  `scheduler.*.check.method.<method>.profile.<profile>.count`, using the
  effective runtime method/profile for `HEAD` / `GET` and `legacy` /
  `simple_http` / `full`
- In those metrics, `legacy` is a detection profile, not the Veriflier
  transport. `HEAD` + `legacy` cohorts can and should still be checked through
  the v2 `/v2/check` Veriflier contract.
- WPCOM API attempts, deliveries, retries, queued circuit-open responses,
  permanent 404/410 failures, errors, and final failures
- Veriflier response times and vote counters
- Detection flow timing from first failure to escalation, confirmation,
  recovery, or false alarm
- Detection outcome counters by local failure class
- Legacy projection drift
- RSS and Go Sys memory usage

StatsD is the primary metrics transport. Monitor and deliverer read
`STATSD_ADDR`; Jetmon binaries do not assume a production StatsD endpoint when
it is unset. Local Docker Compose and Veriflier production Compose set
`STATSD_ADDR=statsd:8125` explicitly for their bundled StatsD container.
Production Monitor containers should point `STATSD_ADDR` at the existing
host-local StatsD proxy through Docker bridge networking:
`--add-host=host.docker.internal:host-gateway` plus
`STATSD_ADDR=host.docker.internal:8125`. They should not use host networking
and should not start a StatsD/Graphite container in the Monitor stack.
Production Veriflier VPS Compose stacks include StatsD/Graphite locally so
central Grafana can query the Veriflier host's Graphite endpoint. Expose
Graphite/StatsD data through the approved metrics pipeline when external
systems need it.

StatsD uses UDP, so Monitor dashboard `statsd` health can confirm only that the
client was configured and created. Treat it as a local configuration signal,
not proof that the production StatsD/Graphite pipeline ingested the metric. The
Veriflier Compose stack owns its StatsD/Graphite containers and includes an
optional metrics smoke test that sends one test metric and queries Graphite for
it.

For repeatable capacity and scalability tests, use
[`jetmon-v2-scalability-test-plan.md`](jetmon-v2-scalability-test-plan.md).

For repeatable production summaries from durable Jetmon tables, use:

```bash
./jetmon2 telemetry report --since=24h
./jetmon2 telemetry report --since=2026-04-30T00:00:00Z --until=2026-05-01T00:00:00Z --output=json
./jetmon2 telemetry report --since=6h --query-timeout=45s
```

The report is read-only and runs with a bounded query timeout by default
(`--query-timeout` is capped at 5 minutes). The time window is half-open
(`since <= row time < until`) so adjacent scheduled reports do not double-count
boundary rows. It summarizes event lifecycle counts, first-failure timings,
verifier agreement, v2 verifier vote evidence, false-alarm classes, WPCOM
attempt parity, and metadata gaps that would make operator or customer
explanations weaker. Verifier vote evidence includes duplicate votes ignored
for quorum and transitions blocked by the minimum-healthy floor. WPCOM parity
is split between confirmed-down and recovery attempts, with maintenance/cooldown
suppressions separated the same way, so one side cannot mask a mismatch on the
other. During v1-to-v2 rollout, capture this report after each full-round
cutover gate and again at fleet completion. It reports aggregate counts and
classes rather than raw payloads or credentials.

The top line reports `telemetry_status`, `explanation_gap_types`, and
`explanation_gap_rows`. Treat `warn` or `fail` as a signal that the report found
missing or inconsistent telemetry, not as a site-availability rollup.
The `window_edge_lookback` line calls out transition rows at the end of the
window that can make WPCOM parity look temporarily incomplete; rerun with a
later `--until` before treating those edge deltas as missing audit data.

Use `LOG_FORMAT=json` for structured logs during investigations.

## Debugging

Enable debug logging:

```json
{ "DEBUG": true }
```

Attach pprof locally:

```bash
curl http://localhost:6060/debug/pprof/
curl http://localhost:6060/debug/pprof/heap > heap.prof
go tool pprof heap.prof
```

The debug listener binds to localhost only. Set `DEBUG_PORT` to 0 to disable it.

If `WORKER_MAX_MEM_MB` is greater than 0 and Go runtime memory exceeds that
threshold, the goroutine pool shrinks by 10 percent via graceful drain. The
default is 0 so Jetmon does not silently trade away check throughput because of
a legacy memory cap. Use the host/fleet dashboard RSS value to compare Jetmon's
resident memory with operating-system tools, and use the Go Sys value with
pprof when investigating sustained runtime memory pressure.

## Veriflier Health

Verifliers that fail to respond are excluded from confirmation requests. Quorum
counts unique v2 `vantage.id` values rather than raw agent replies. If the
healthy unique-vantage set drops below `PEER_OFFLINE_LIMIT`, Jetmon lowers the
effective quorum only to the multi-Veriflier safety floor: two healthy vantages
unless `PEER_OFFLINE_LIMIT=1` was explicitly configured. This prevents one
remaining healthy Veriflier from confirming downtime alone in normal
multi-Veriflier layouts.

New Verifliers expose the versioned contract at `/v2/status`. Use that endpoint
for operational detail: it reports supported protocols, the quorum-counted
`vantage.id`, serving `agent.id`, and current capacity. Horizontal replicas
behind one regional endpoint should share the same `vantage.id`; `agent.id`
changes per process and is diagnostic only.

Once a site is already projected as confirmed down, subsequent local failures do
not re-enter Veriflier confirmation. Jetmon keeps checking for recovery and
emits `detection.down.still_down.*` counters for the ongoing failed
observations without duplicating confirmed-down notifications.

For deployment, build the v2 Veriflier fleet before cutting monitors over.
The preferred rollout uses fresh `veriflier2` endpoints and points v2 Monitors
only at that fleet. Keep v1 Monitors pointed at the original v1 Verifliers
until monitor cutover is complete. `veriflier2` can serve `/check` and
`/status` for legacy-compatible HTTP clients only when
`VERIFLIER_ENABLE_LEGACY_HTTP=true`; leave that disabled for normal production
v2 endpoints. The original v1 Veriflier uses the old TLS/custom transport and
should not be treated as a supported v2 Monitor fallback target. Veriflier
hosts do not need database credentials; monitors collect agent telemetry and
write it to MySQL.

Manual check:

```bash
./jetmon2 validate-config
curl http://<veriflier-host>:7803/v2/status
```

`validate-config` reports each configured Veriflier's contract status and marks
duplicate or missing v2 `vantage.id` values as failures. The operator dashboard
uses the same status metadata and shows duplicate-vantage Verifliers as red.

If a v2 Veriflier is saturated, it returns HTTP 503 for `/v2/check`. Treat that
as a capacity or routing problem for that endpoint. It is not a site-down vote.

## Docker Cleanup

```bash
cd docker
docker compose down -v
rm -f ../config/config.json
rm -rf ../stats/*
```

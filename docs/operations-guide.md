# Operations Guide

Use this guide for day-to-day development, deployment care, production
debugging, support investigations, and runtime commands. Use
[v1-to-v2-migration.md](v1-to-v2-migration.md) for the production rollout
sequence and [project.md](project.md) for architecture.

## Local Development

Fast local loop:

```bash
cd docker
cp .env-sample .env
docker compose up --build -d
```

Build and test from the repository root:

```bash
make all
make test
make test-race
make lint
```

Useful local API smoke:

```bash
make build
make api-cli-token-create

export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token

./bin/jetmon2 api health --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api commands --output table
make api-cli-smoke
```

Manual run without Compose:

```bash
make build
cp config/config-sample.json config/config.json
./bin/jetmon2 validate-config
./bin/jetmon2 migrate
./bin/jetmon2
```

`make generate` is intentionally separate. It requires `protoc` and Go protobuf
plugins; production Veriflier traffic uses JSON/HTTP, not generated proto stubs.

## Main Binaries

| Binary | Purpose |
| --- | --- |
| `bin/jetmon2` | Monitor, orchestrator, REST API, dashboard, embedded delivery workers, CLI. |
| `bin/veriflier2` | Remote confirmation worker. |
| `bin/jetmon-deliverer` | Standalone webhook and alert-contact delivery worker. |

Core CLI commands:

```bash
./bin/jetmon2 version
./bin/jetmon2 validate-config
./bin/jetmon2 migrate
./bin/jetmon2 status
./bin/jetmon2 drain
./bin/jetmon2 reload
./bin/jetmon2 audit --blog-id 12345 --since 2h
./bin/jetmon2 site-tenants import --file site-tenants.csv --dry-run
```

## Configuration Posture

The sample config lives at `config/config-sample.json`; the full option
reference remains in `config/config.readme`.

Important rollout/runtime keys:

| Key | Purpose |
| --- | --- |
| `NUM_WORKERS` | Checker pool target size. |
| `NUM_TO_PROCESS` | Legacy compatibility setting; does not cap Go scheduler throughput. |
| `DATASET_SIZE` | Database fetch page size for active-target reloads. |
| `NET_COMMS_TIMEOUT` | Default per-check HTTP timeout in seconds. |
| `PEER_OFFLINE_LIMIT` | Veriflier agreements required to confirm downtime. |
| `BUCKET_TOTAL` | Total dynamic bucket range. |
| `BUCKET_TARGET` | Maximum buckets this host should own. |
| `BUCKET_HEARTBEAT_GRACE_SEC` | Stale host grace before bucket absorption. |
| `PINNED_BUCKET_MIN/MAX` | Migration-only static range; disables dynamic ownership for that host. |
| `LEGACY_STATUS_PROJECTION_ENABLE` | Keep v1 `site_status` / `last_status_change` projection updated from v2 events. |
| `STREAMING_TARGET_RELOAD_SEC` | Active target reload cadence. |
| `API_PORT` | Enables the API and makes embedded delivery workers eligible to run. |
| `DASHBOARD_PORT` | Operator dashboard port, `0` disables. |
| `DEBUG_PORT` | Localhost-only pprof port, `0` disables. |
| `DELIVERY_OWNER_HOST` | Optional rollout guard to keep delivery single-owner. |
| `EMAIL_TRANSPORT` | `wpcom`, `smtp`, or `stub`; empty means `stub`. |

Always run `validate-config` before starting a new production config. Startup
should validate schema state; production Monitor containers should not apply
automatic DDL.

## Docker Images

Production images are expected to be built and promoted by CI. Local Compose
uses the repository Dockerfiles.

Typical container contract:

- mount or render `config/config.json`;
- provide DB credentials or a server-map config path;
- expose API/dashboard only on trusted networks;
- mount legacy stats output only for consumers that still need file reads;
- use SIGINT for drain and SIGHUP for drain/re-exec reload.

Validate inside a container:

```bash
docker run --rm \
  -v "$PWD/config/config.json:/app/config/config.json:ro" \
  ghcr.io/automattic/jetmon2:<tag> \
  ./jetmon2 validate-config
```

Run a Veriflier container with its JSON/HTTP port exposed only to trusted
Monitor hosts:

```bash
docker run -d --name veriflier2 \
  -p 7803:7803 \
  ghcr.io/automattic/veriflier2:<tag>
```

## Restart And Drain

Preferred graceful shutdown:

```bash
./bin/jetmon2 drain
# or SIGINT through systemd/container runtime
```

Preferred reload:

```bash
./bin/jetmon2 reload
# or SIGHUP through systemd/container runtime
```

SIGHUP drains active work and re-execs through the configured restart target.
Use it after mounted JSON config changes, new binaries, or DB server-map changes
that require pools to be rebuilt.

Common playbook:

| Change | Action |
| --- | --- |
| Mounted JSON config changed | `validate-config`, then `reload`. |
| Compose environment changed | Restart container through Compose after validation. |
| New image or code deploy | SIGINT drain, replace image/binary, start, watch readiness and dashboard. |
| DB server map changed | Wait for hot reload if enabled; SIGHUP if credentials/endpoints need immediate pool replacement. |
| Remove host from service | SIGINT drain; confirm `/api/v1/monitor/drain-status` reports `done:true`. |

Full fleet deploy order:

1. Verifliers first.
2. Standalone deliverer if used.
3. Monitor hosts one at a time.
4. Dashboards/API health and bucket coverage after each host.

## Health Surfaces

| Surface | Use |
| --- | --- |
| `/api/v1/health` | API liveness and DB connectivity; unauthenticated. |
| `/api/v1/ready` | Host readiness; requires fresh green process-health snapshot. |
| `/api/v1/monitor/drain-status` | In-flight checks, queue depth, retry queue, WPCOM queue, drain completion. |
| `/api/v1/monitor/stats` | Current legacy stats snapshot and optional exact file body. |
| `/api/v1/monitor/db-config` | Sanitized DB config source and reload status. |
| `/api/v1/verifliers/quorum-report` | Per-vantage Veriflier health and quorum diagnostics. |
| Dashboard `/` | Single-host operations view. |
| Dashboard `/fleet` | Fleet state, bucket ownership, process health, delivery ownership. |
| pprof `/debug/pprof/` | Localhost-only runtime profiling when `DEBUG_PORT > 0`. |

Keep dashboard and pprof listeners on loopback unless a trusted operator-network
control protects them.

## Metrics And Logs

StatsD is the primary metrics transport. Metric names keep the v1 prefix:

```text
com.jetpack.jetmon.<hostname>.
```

V2 adds metrics for scheduler phases, queue depth, checker timing, freshness
writes, check-history inserts, SSL updates, event handling, API requests,
delivery workers, Veriflier health, and rollout gates.

Runtime logs go to stdout/stderr. Use service-manager or container runtime logs
for collection. Event state changes belong in `jetpack_monitor_events` and
`jetpack_monitor_event_transitions`; operational actions belong in
`jetpack_monitor_audit_log`.

## Delivery Workers

Embedded delivery workers are eligible when `API_PORT > 0`. The standalone
`jetmon-deliverer` can run the same webhook and alert-contact delivery loops
outside the Monitor process.

Correctness does not require a single delivery owner because rows are claimed
transactionally. `DELIVERY_OWNER_HOST` is a migration guard for deliberately
keeping one active owner while changing deployment shape.

Delivery retry ladder:

```text
immediate -> 1m -> 5m -> 30m -> 1h -> 6h -> abandoned
```

Watch pending/failed/abandoned rows and per-worker health before enabling a
broader delivery rollout.

## Support Workflow

When a customer-facing alert or missed alert needs explanation, collect evidence
in this order:

1. Site record from `./bin/jetmon2 api sites get <blog_id> --pretty`.
2. Active/recent events from `./bin/jetmon2 api events list --site <blog_id>`.
3. Event transitions for the incident.
4. Check history around the transition time.
5. Audit log rows for Veriflier RPC, WPCOM notification, maintenance, alert
   suppression, and API access.
6. Veriflier quorum report if the incident was or should have been confirmed.

Common interpretations:

| Signal | Meaning |
| --- | --- |
| `Seems Down` only, then `false_alarm` | Local failure did not meet verifier quorum. |
| `Down`, then `verifier_cleared` | Confirmed outage recovered. |
| `probe_cleared` | Local probe recovered before Veriflier confirmation. |
| Maintenance audit row | Checks continued, alerting was suppressed. |
| `blocked` failure class | 403/WAF-style denial. Validate allowlisting before treating as site outage. |
| Redirect failure | Site violated configured redirect policy. |
| TLS warning | SSL expiry/deprecated TLS signal, not necessarily downtime. |
| `Unknown` | Monitor/probe infrastructure issue; do not frame as customer-site downtime. |

Customer framing should describe what Jetmon observed, not overclaim root cause:
"Jetmon's local probe received HTTP 503 and two Verifliers confirmed the same
failure" is better than "your server was down."

## Probe Safety

The checker must avoid probing unsafe targets such as private IP ranges,
link-local addresses, and DNS rebinding results. Probe safety blocks are written
to audit and site safety state. Do not bypass target-safety checks for rollout
smoke or method-comparison probes; they are intentionally read-only but still
touch customer-controlled URLs.

## Retention

Keep retention policy explicit and conservative:

- transitions and events are product/audit evidence;
- check history may be sampled or capped by config;
- delivery rows are needed for retry and debugging;
- audit log volume depends on `AUDIT_LOG_MODE`.

Run retention cleanup only through the configured command/path for the
environment. Log cleanup decisions to audit.

## Debugging

Use pprof for sustained memory or goroutine growth:

```bash
curl http://localhost:${DEBUG_PORT:-6060}/debug/pprof/goroutine?debug=2
curl -o heap.pb.gz http://localhost:${DEBUG_PORT:-6060}/debug/pprof/heap
go tool pprof ./bin/jetmon2 heap.pb.gz
```

Useful DB checks:

```sql
SELECT * FROM jetpack_monitor_hosts ORDER BY bucket_min;
SELECT * FROM jetpack_monitor_process_health ORDER BY updated_at DESC LIMIT 20;
SELECT * FROM jetpack_monitor_event_transitions ORDER BY id DESC LIMIT 20;
SELECT * FROM jetpack_monitor_audit_log ORDER BY id DESC LIMIT 20;
```

Escalate before making production changes if uptime-bench or Jetmon capacity
tests are active. Local analysis, branch inspection, docs, and handoff
preparation are safe while tests are running.

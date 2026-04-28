jetmon2
=======

Overview
--------

Jetmon is the parallel HTTP health monitoring service for Jetpack-connected sites at scale. Jetmon 2 turns it from a binary up/down status flipper into a full event-sourced health platform — the same low-false-positive Veriflier-confirmed detection core, now with a five-layer severity model, an internal REST API, HMAC-signed webhooks, managed alert contacts (email, PagerDuty, Slack, Teams), and a complete operational audit trail.

The whole thing ships as a single static Go binary with embedded migrations. No `node_modules`, no native addons, no worker process tree. Every check, retry, Veriflier confirmation, and notification lands in `jetmon_audit_log`; every status transition lands in `jetmon_event_transitions`. An operator can replay any incident, end-to-end, from the database alone.

The Jetmon 1 detection pipeline is preserved verbatim — periodic check rounds, local retries before escalation, geo-distributed Veriflier confirmation before WPCOM is notified. v2 keeps WPCOM compatibility through a shadow-state migration: the v2 event tables are authoritative, and `jetpack_monitor_sites.site_status` / `last_status_change` continue to be projected transactionally for legacy consumers until they cut over (`LEGACY_STATUS_PROJECTION_ENABLE`).


What's new in v2
----------------

v2 keeps the Jetmon 1 detection pipeline (local retries → geo-distributed Veriflier confirmation → notify) and rebuilds everything around it.

| Capability | Jetmon 1 | Jetmon 2 |
|---|---|---|
| Status model | Binary `up` / `down` (`confirmed_down` for re-detections) | Five-layer severity ladder: `Up < Warning < Degraded < SeemsDown < Down`, paired with separate state vocabulary |
| State storage | Single mutable `site_status` column | Event-sourced — `jetmon_events` (current authoritative state) + append-only `jetmon_event_transitions` (every mutation) |
| Failure classifications | `down` | `server`, `client`, `blocked`, `https`, `intermittent`, `redirect`, `ssl_expiry`, `tls_deprecated`, `keyword_missing`, `success` |
| Notification channels | WPCOM only | WPCOM + HMAC-signed webhooks + managed alert contacts (email, PagerDuty, Slack, Teams) |
| API surface | None | Internal REST API at `/api/v1`: Bearer auth, three coarse scopes, per-key rate limit, Stripe-style idempotency, cursor pagination, full audit logging |
| Per-site config | Bucket + check interval | + custom headers, timeout override, redirect policy, alert cooldown, maintenance windows, keyword content check, SSL-expiry alerts at 30 / 14 / 7 days |
| Operational audit | Basic logging | Full audit trail (`jetmon_audit_log`) over every check, retry, Veriflier dispatch, alert suppression, API call, and config reload |
| Process model | Node master + Node workers + C++ native addon + Qt C++ Veriflier | Go monitor (`jetmon2`) + optional outbound deliverer (`jetmon-deliverer`) + Go Veriflier (`veriflier2`) |
| Worker scaling | Spawn / kill child processes | In-process goroutine pool that auto-scales by queue depth |
| Deployment friction | `npm` + `node-gyp` + Qt | Static binary + `./jetmon2 migrate` + `./jetmon2 validate-config` |
| Multi-host coordination | Manual `bucket_min` / `bucket_max` per host | MySQL-coordinated `jetmon_hosts` table with heartbeat-and-reclaim |
| Observability | StatsD | StatsD + structured logs + audit trail + operator dashboard (SSE) + localhost pprof |
| Hot reload | Restart | `SIGHUP` for config; `SIGINT` for graceful drain |

A few specifics worth bragging about:

- **Webhooks with Stripe-style HMAC signatures.** `t=<unix>,v1=<hex>` over `{ts}.{body}`, per-webhook in-flight cap, retry ladder 1m → 5m → 30m → 1h → 6h before abandon. Frozen-at-fire-time payload contract — consumers see the event as it was when the webhook fired, not as it is now.
- **Idempotent write endpoints.** POSTs accept `Idempotency-Key`; replays return the original response, so a retried "click to test" through a network blip won't double-page the destination.
- **Rotation grace windows on API keys.** `revoked_at` and `expires_at` are half-open cutoffs; setting `revoked_at` in the future keeps the old key valid until consumers deploy the replacement.
- **Migrations embedded in the binary.** `./jetmon2 migrate` walks the schema forward; `./jetmon2 validate-config` checks config + DB connectivity + email transport mode + verifier list before deploy, prints the matching rollout preflight command, and warns loudly when alert-contact email is set to the log-only stub.
- **MySQL 5.7+ compatible.** No window functions, no JSON-path expressions in SELECT — the v2 schema and queries land cleanly on the legacy production database.


Architecture
------------

```
┌──────────────────────────────────────────────────────────────┐
│                           jetmon2                            │
│                                                              │
│  ┌────────────┐  ┌────────────┐  ┌────────────────────┐      │
│  │Orchestrator│  │ Check pool │  │ Veriflier          │      │
│  │ goroutine  │  │(goroutines)│  │ transport          │      │
│  └─────┬──────┘  └─────┬──────┘  └────────┬───────────┘      │
│        │               │                  │                  │
│  ┌─────┴───────────────┴──────────────────┴────────────┐     │
│  │  Eventstore + Audit log                             │     │
│  └─────┬─────────────────┬──────────────────┬──────────┘     │
│        │                 │                  │                │
│  ┌─────┴──────┐  ┌───────┴────────┐  ┌──────┴──────────┐     │
│  │  REST API  │  │ Webhook worker │  │  Alert-contact  │     │
│  │  /api/v1/  │  │ embedded or    │  │ worker embedded │     │
│  │            │  │ deliverer      │  │ or deliverer    │     │
│  └────────────┘  └────────────────┘  └─────────────────┘     │
└────────┬─────────────────────────────────────────┬───────────┘
         │                                         │
       MySQL                              WPCOM · custom webhooks
       StatsD                             · email · PagerDuty
       Log files                          · Slack · Teams
```

The **Orchestrator goroutine** fetches site batches from MySQL, dispatches work to the check pool, manages the local retry queue, coordinates Veriflier confirmation, and sends WPCOM notifications. It owns all database access and all outbound WPCOM calls.

The **Check Pool** is a bounded goroutine pool that performs HTTP checks using Go's `net/http` and `net/http/httptrace`. It records DNS, TCP, TLS, and TTFB timings on every check and auto-scales against queue depth without spawning new processes.

The **Veriflier transport** sends confirmation batches to remote Veriflier instances. JSON-over-HTTP on the configured Veriflier port is the v2 production transport; the proto definition in `proto/` is retained only as a schema reference for a possible future transport.

The **Veriflier** is a standalone Go binary deployed at remote locations. It replaces the Qt C++ Veriflier and uses the same JSON-over-HTTP transport as the Monitor-side client.

The v2 platform layer sits below the detection pipeline:

- **Eventstore** is the sole writer for `jetmon_events` and `jetmon_event_transitions`. Every state change — open, escalate, close, recover, manual override — is an atomic transition with full history. Audit log writes share the same MySQL handle.
- **REST API** exposes the v2 surface at `/api/v1/` (enable with `API_PORT`). Bearer-token auth, three coarse scopes (`read` / `write` / `admin`), per-key token-bucket rate limiting, Stripe-style idempotency keys on POSTs. Every authenticated request lands in `jetmon_audit_log` with the consumer name, status, latency, and request id.
- **Webhook worker** delivers HMAC-signed `event.*` posts to registered consumers. Per-webhook in-flight cap, retry ladder 1m → 5m → 30m → 1h → 6h, frozen-at-fire-time payload.
- **Alert-contact worker** delivers Jetmon-rendered notifications through Jetmon-owned transports (email, PagerDuty Events API v2, Slack Block Kit, Teams Adaptive Cards). Per-contact `max_per_hour` rate cap as pager-storm insurance.

WPCOM notification flow (preserved from Jetmon 1, used during shadow-state migration):

| Previous Status | Current Status    | Action                                                |
|-----------------|-------------------|-------------------------------------------------------|
| UP              | DOWN              | Local retries → Veriflier confirmation → notify WPCOM |
| DOWN            | UP                | Notify WPCOM site recovered                           |
| DOWN            | DOWN (confirmed)  | Notify WPCOM confirmed down                           |

v2 emits richer events to webhook and alert-contact subscribers (full event lifecycle including escalations and severity transitions) — the WPCOM table above describes only the legacy notification path.


Installation
------------

1) Install [Docker](https://docs.docker.com/get-docker/) and [docker-compose](https://docs.docker.com/compose/install/)

2) Clone the repository

3) Copy the environment file:

		cd docker && cp .env-sample .env

4) Edit `docker/.env` for your local environment. The file is only for local
   host-side bind address / `*_HOST_PORT` overrides, credentials, and user ids.
   `BIND_ADDR` keeps non-API services local by default; `API_BIND_ADDR` controls
   whether the REST API is reachable by other systems. Container-side service
   ports are hardcoded in `docker-compose.yml`.
   `MYSQL_ROOT_PASSWORD` is used only for local container setup; Jetmon connects
   with the non-root `MYSQL_USER` / `MYSQL_PASSWORD` credentials.

5) Build and start all services:

		docker compose up --build -d


Configuration
-------------

Jetmon configuration lives in `config/config.json`. Copy `config/config-sample.json` to get started. The file is generated automatically from `config-sample.json` and `docker/.env` if not present.

Send `SIGHUP` to the running process to reload configuration without restarting.

Key settings:

| Key | Default | Description |
|-----|---------|-------------|
| `NUM_WORKERS` | 60 | Goroutine pool size |
| `NUM_TO_PROCESS` | 40 | Parallel checks per pool slot |
| `MIN_TIME_BETWEEN_ROUNDS_SEC` | 300 | Minimum seconds between check rounds |
| `NET_COMMS_TIMEOUT` | 10 | Default per-check HTTP timeout (seconds) |
| `PEER_OFFLINE_LIMIT` | 3 | Veriflier agreements required to confirm downtime |
| `WORKER_MAX_MEM_MB` | 53 | RSS threshold that triggers goroutine pool drain |
| `BUCKET_TOTAL` | 1000 | Total bucket range across all hosts |
| `BUCKET_TARGET` | 500 | Maximum buckets this host should own |
| `BUCKET_HEARTBEAT_GRACE_SEC` | 600 | Seconds before a silent host's buckets are reclaimed |
| `PINNED_BUCKET_MIN` / `PINNED_BUCKET_MAX` | unset | Migration-only static bucket range; disables `jetmon_hosts` ownership for v1-compatible host-by-host cutover |
| `ALERT_COOLDOWN_MINUTES` | 30 | Default cooldown between repeated alerts per site |
| `LEGACY_STATUS_PROJECTION_ENABLE` | true | Keep `jetpack_monitor_sites.site_status` / `last_status_change` updated for v1 consumers during migration |
| `LOG_FORMAT` | `text` | `text` for plain-text logs or `json` for structured logs |
| `DASHBOARD_PORT` | 8080 | Internal port for the operator dashboard (0 to disable) |
| `API_PORT` | 0 | Internal REST API port (0 to disable). Also makes webhook and alert-contact delivery workers eligible to run. |
| `DELIVERY_OWNER_HOST` | empty | Optional hostname allowed to run delivery workers when `API_PORT` is enabled; set this on shared production configs so only one API-enabled host dispatches outbound deliveries. |
| `DEBUG_PORT` | 6060 | localhost-only pprof port (`127.0.0.1:PORT`); 0 to disable |
| `EMAIL_TRANSPORT` | `stub` | Alert-contact email sender: `stub` (log only), `smtp`, or `wpcom` |

See `config/config.readme` for the full option reference.

The Veriflier configuration lives in `veriflier2/config/veriflier.json`, generated from `veriflier-sample.json` and `docker/.env`.


Running
-------

	cd docker && docker compose up -d

To rebuild the binary after code changes:

	docker compose up --build -d

To follow logs:

	docker compose logs -f jetmon

To stop:

	docker compose down


Database
--------

Main table (unchanged from Jetmon 1):

	CREATE TABLE `jetpack_monitor_sites` (
	    `jetpack_monitor_site_id` bigint(20) unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY,
	    `blog_id` bigint(20) unsigned NOT NULL,
	    `bucket_no` smallint(2) unsigned NOT NULL,
	    `monitor_url` varchar(300) NOT NULL,
	    `monitor_active` tinyint(1) unsigned NOT NULL DEFAULT 1,
	    `site_status` tinyint(1) unsigned NOT NULL DEFAULT 1,
	    `last_status_change` timestamp NULL DEFAULT current_timestamp(),
	    `check_interval` tinyint(1) unsigned NOT NULL DEFAULT 5,
	    INDEX `blog_id_monitor_url` (`blog_id`, `monitor_url`),
	    INDEX `bucket_no_monitor_active_check_interval` (`bucket_no`, `monitor_active`, `check_interval`)
	);

New columns added by Jetmon 2 (applied via `jetmon2 migrate`):

| Column | Type | Purpose |
|--------|------|---------|
| `ssl_expiry_date` | DATE NULL | Updated each HTTPS check |
| `check_keyword` | VARCHAR(500) NULL | String to verify in response body |
| `maintenance_start` | DATETIME NULL | Maintenance window start |
| `maintenance_end` | DATETIME NULL | Maintenance window end |
| `custom_headers` | JSON NULL | Per-site request headers |
| `timeout_seconds` | TINYINT NULL | Per-site timeout override |
| `redirect_policy` | ENUM NULL | `follow`, `alert`, or `fail` |
| `alert_cooldown_minutes` | SMALLINT NULL | Per-site cooldown override |

Jetmon 2 uses a shadow-v2-state migration model. Incident state is authoritative
in the v2 event tables, while `jetpack_monitor_sites` remains the legacy site
configuration table and compatibility projection during migration. With
`LEGACY_STATUS_PROJECTION_ENABLE: true`, every v2 incident mutation also updates
the v1 `site_status` / `last_status_change` fields in the same transaction. Once
legacy readers have moved to the v2 API/event tables, disable that projection.

New tables added by Jetmon 2:

| Table | Purpose |
|-------|---------|
| `jetmon_hosts` | MySQL-coordinated bucket ownership and heartbeat |
| `jetmon_events` | Authoritative current state of each v2 incident |
| `jetmon_event_transitions` | Append-only history of every mutation to `jetmon_events` |
| `jetmon_audit_log` | Operational trail for checks, retries, WPCOM calls, suppression, API access, and config reloads |
| `jetmon_check_history` | RTT and timing samples for trending |
| `jetmon_false_positives` | Veriflier non-confirmation events |
| `jetmon_api_keys` | Internal REST API Bearer-token registry |
| `jetmon_webhooks` | Webhook registrations and HMAC signing secrets |
| `jetmon_webhook_deliveries` | Outbound webhook delivery attempts and retry state |
| `jetmon_webhook_dispatch_progress` | Webhook worker high-water marks over event transitions |
| `jetmon_alert_contacts` | Managed alert destinations such as email, PagerDuty, Slack, and Teams |
| `jetmon_alert_deliveries` | Outbound alert-contact delivery attempts and retry state |
| `jetmon_alert_dispatch_progress` | Alert worker high-water marks over event transitions |

Apply migrations before starting for the first time:

	./jetmon2 migrate


For Developers
--------------

### Prerequisites

- Go 1.22 or later
- Docker and docker-compose (for integration testing)
- Access to a MySQL instance (provided by Docker Compose)

### Building

	make all              # Build bin/jetmon2, bin/jetmon-deliverer, and bin/veriflier2
	make build            # Build only bin/jetmon2
	make build-deliverer  # Build only bin/jetmon-deliverer
	make build-veriflier  # Build only bin/veriflier2

If `go` is not on `PATH`, the Makefile falls back to
`/usr/local/go/bin/go` when present. Override with `make GO=/path/to/go ...`
for other local layouts. Make targets use `GOCACHE=/tmp/jetmon-go-cache` by
default so builds do not depend on a writable home-directory cache; override
with `make GOCACHE=/path/to/cache ...` when needed.

`make generate` is intentionally separate from `make all`. It requires
`protoc` and the Go protobuf plugins, and is reserved for experimental proto
stub generation; generated stubs are not part of the v2 production transport.

### Running Tests

	make test
	make test-race
	make lint

The current `go test ./...` suite runs standalone. Use the Docker Compose environment for manual end-to-end checks against MySQL, StatsD, and Veriflier services.

### Docker Development Loop

	cd docker
	docker compose up --build -d          # Build binary and start all services
	docker compose logs -f jetmon          # Follow logs
	docker compose exec jetmon bash        # Shell into the container

### Adding Test Sites

Connect to the test database:

	docker compose exec mysqldb mysql -u jetmon -pjetmon_dev_password jetmon_db

Insert sites to check:

	INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status)
	VALUES
	    (1, 0, 'https://wordpress.com', 1, 1),
	    (2, 0, 'https://httpstat.us/200', 1, 1),
	    (3, 0, 'https://httpstat.us/500', 1, 1),
	    (4, 0, 'https://httpstat.us/200?sleep=15000', 1, 1);

### Legacy Status Projection

During migration, keep the legacy v1 status fields updated:

	{ "LEGACY_STATUS_PROJECTION_ENABLE": true }

This does not make the legacy row the source of truth. Jetmon v2 writes
`jetmon_events` and `jetmon_event_transitions` first, then projects
`site_status` and `last_status_change` back to `jetpack_monitor_sites` for
legacy consumers. After all consumers read from the v2 API/event tables, set
`LEGACY_STATUS_PROJECTION_ENABLE` to `false`.

### Simulated Site Server

The Docker Compose environment does not yet include the planned simulated site
server. Use external test endpoints or local ad-hoc services for response-code,
timeout, redirect, keyword, and TLS scenarios until that service is added.

### Config Validation

	./jetmon2 validate-config

Checks all required keys, validates value ranges, tests MySQL connectivity,
reports legacy projection and email transport modes, warns when alert-contact
email uses the log-only `stub` sender, and lists configured Verifliers.
Veriflier reachability is informational here rather than a validation failure.

### Tenant Mapping Backfill

Gateway-routed site reads and writes are scoped through
`jetmon_site_tenants`. Before customer traffic depends on Jetmon-side tenant
enforcement, import the gateway/customer source of truth as CSV:

	./jetmon2 site-tenants import --file site-tenants.csv --dry-run
	./jetmon2 site-tenants import --file site-tenants.csv --source gateway

The CSV format is `tenant_id,blog_id` with an optional header row. The import
upserts mappings and skips duplicate rows in the input; it does not delete
missing mappings, because pruning requires a source-specific reconciliation
policy.

### Debugging

Enable debug logging in `config/config.json`:

	{ "DEBUG": true }

Or switch to structured JSON logs for easier filtering:

	{ "LOG_FORMAT": "json" }

Attach the Go pprof profiler (localhost only, never exposed remotely):

	curl http://localhost:6060/debug/pprof/

The debug port is configurable via `DEBUG_PORT` (default 6060). Set to 0 to disable.

### Project Layout

| Path | Purpose |
|------|---------|
| `cmd/jetmon2/` | Binary entry point |
| `cmd/jetmon-deliverer/` | Standalone outbound delivery worker entry point |
| `internal/orchestrator/` | Round scheduling, DB fetch, WPCOM notifications |
| `internal/checker/` | HTTP check goroutine pool |
| `internal/veriflier/` | JSON-over-HTTP Veriflier transport |
| `internal/db/` | MySQL access, bucket heartbeat |
| `internal/config/` | Config loading and hot-reload |
| `internal/metrics/` | StatsD client, stats file writer |
| `internal/wpcom/` | WPCOM API client and circuit breaker |
| `internal/audit/` | Audit log |
| `internal/eventstore/` | Authoritative event and transition writer |
| `internal/api/` | Internal REST API server |
| `internal/deliverer/` | Shared outbound delivery worker wiring |
| `internal/webhooks/` | HMAC-signed webhook registry and delivery worker |
| `internal/alerting/` | Managed alert-contact registry and delivery worker |
| `internal/dashboard/` | Operator dashboard and SSE handler |
| `veriflier2/` | Go Veriflier binary |


For Testers
-----------

### Starting the Test Environment

	cd docker
	docker compose up --build -d
	docker compose logs -f jetmon

### Verifying Basic Operation

Check that sites are being processed:

	docker compose exec jetmon cat stats/sitespersec
	docker compose exec jetmon cat stats/sitesqueue
	docker compose exec jetmon ps aux

Check the StatsD dashboard at `http://localhost:8088` by default, or at the
`BIND_ADDR` / `GRAPHITE_HOST_PORT` values from `docker/.env`, under:
`Metrics > stats > com > jetpack > jetmon > docker > jetmon`

### Key Test Scenarios

**Downtime detection and confirmation:**
Insert a site pointing to `https://httpstat.us/500`. With `LEGACY_STATUS_PROJECTION_ENABLE: true`, Jetmon should detect the failure, retry locally, escalate to the Veriflier, confirm down, write the v2 event transition, and project `site_status` to `2`.

**SSL certificate expiry:**
Insert an HTTPS site. After a check round, verify `ssl_expiry_date` is populated in the database.

**Keyword check:**
Insert a site with `check_keyword` set to a string that appears (or does not appear) in the response body. Verify the check result matches the keyword presence.

**Maintenance window suppression:**
Set `maintenance_start` and `maintenance_end` to bracket the current time. Verify no alert fires even when the site returns errors. Verify the event appears in `jetmon_audit_log`.

**Timeout handling:**
Insert `https://httpstat.us/200?sleep=15000` (15-second delay). Verify it is classified as `intermittent` after the timeout expires.

**Redirect policy:**
Insert a site returning 301. Test `follow`, `alert`, and `fail` values in the `redirect_policy` column.

**Alert cooldown:**
Set `alert_cooldown_minutes` to a low value. Trigger repeated failures and verify subsequent alerts are suppressed and recorded in the audit log.

**Graceful shutdown:**
Drain in-flight checks and exit cleanly:

	docker compose exec jetmon ./jetmon2 drain

**Config reload:**
Modify `config/config.json` and reload without a restart:

	docker compose exec jetmon ./jetmon2 reload

**Worker pool auto-scaling:**
Set `NUM_WORKERS` to a low value with many sites in the database. Observe queue depth rising and the pool scaling up in the operator dashboard.

**Bucket ownership failover:**
Simulate a host failure by manually expiring a row in `jetmon_hosts`. Verify the surviving instance absorbs the abandoned buckets within one grace period.

### Operator Dashboard

The dashboard is available at http://localhost:8080 (configurable via
`DASHBOARD_PORT`). It shows worker count, active checks, queue depth, retry
queue depth, sites per second, round time, owned buckets, rollout guard state,
RSS, WPCOM circuit-breaker state, and live dependency health for MySQL,
configured Verifliers, WPCOM, StatsD, and log/stats directory writes.

### Internal API and Delivery Workers

The internal API is disabled by default. Set `API_PORT` to a non-zero port to enable `/api/v1/...`.

In the embedded v2 deployment, `API_PORT` also makes the webhook and alert-contact delivery workers eligible to run inside `jetmon2`. Set `DELIVERY_OWNER_HOST` to exactly one hostname per database cluster when you want additional API-enabled hosts to serve API traffic without owning delivery during a staged rollout. If `DELIVERY_OWNER_HOST` is empty, the host keeps the legacy behavior and starts delivery workers whenever `API_PORT` is enabled; startup and `validate-config` warn about that fallback.

`bin/jetmon-deliverer` is the first standalone process boundary for outbound delivery. It starts the same webhook and alert-contact workers without starting the monitor, API, dashboard, or bucket ownership loop. Delivery rows are claimed transactionally, so multiple active delivery workers do not claim the same pending row; use `DELIVERY_OWNER_HOST` when you want an explicit single-owner rollout during the transition from embedded to standalone delivery.

### Cleanup

	docker compose down -v
	rm -f config/config.json
	rm -rf logs/*.log stats/*


For Admins
----------

### Production Overview

Jetmon runs on multiple production hosts managed by the Systems team. Each host owns a non-overlapping range of site buckets. Bucket ownership is coordinated automatically via the `jetmon_hosts` MySQL table — no manual bucket range configuration is required.

### Deploying a New Host

1) Install the `jetmon2` binary to `/opt/jetmon2/`
2) Install `systemd/jetmon2.service` to `/etc/systemd/system/` and run `systemctl daemon-reload`
3) Install `systemd/jetmon2-logrotate` to `/etc/logrotate.d/jetmon2`
4) Create `/opt/jetmon2/logs` and `/opt/jetmon2/stats`, owned by the `jetmon` service user
5) Create `/opt/jetmon2/config/jetmon2.env` with the database credentials and auth tokens (see `config/db-config-sample.conf` for the required keys)
6) Copy `config/config.json` from an existing host (or generate from `config-sample.json`)
7) Set `BUCKET_TARGET` to the desired maximum bucket count for this host
8) Run `./jetmon2 migrate` to apply any pending schema migrations
9) Start the service: `systemctl enable --now jetmon2`

The new host will claim unclaimed buckets from the pool on first startup. No existing hosts need reconfiguration.

### v1 to v2 Pinned Rolling Migration

For the first production migration from v1, replace one v1 host at a time with
a v2 host pinned to that same inclusive bucket range. This avoids mixed v1/v2
bucket ownership and gives each host a simple rollback path.

1) Pre-apply additive migrations during a quiet period:

		./jetmon2 migrate

2) On the host being replaced, copy the existing v1 bucket range into v2 config:

		"PINNED_BUCKET_MIN": 0,
		"PINNED_BUCKET_MAX": 99,
		"LEGACY_STATUS_PROJECTION_ENABLE": true,
		"API_PORT": 0

   The v1 names `BUCKET_NO_MIN` / `BUCKET_NO_MAX` are accepted as aliases, but
   `PINNED_BUCKET_*` makes the migration mode explicit. In pinned mode, v2 does
   not claim or heartbeat `jetmon_hosts`; it checks only the configured range.

3) Before stopping v1, run config validation and confirm it prints the pinned
   preflight plus projection-drift commands:

		./jetmon2 validate-config

4) Before starting the cutover, run the pinned rollout preflight:

		./jetmon2 rollout pinned-check

   It verifies pinned mode, legacy projection writes, absence of a
   `jetmon_hosts` row for the host, active site count for the range, and zero
   legacy projection drift.

5) Stop the v1 process for that range, start v2, and verify checks,
   Veriflier confirmations, WPCOM notifications, audit rows, and legacy
   `site_status` projection for that bucket range. If the operator dashboard is
   enabled, also confirm rollout guard state and dependency health before
   moving to the next host.

6) If rollback is needed, stop v2 and restart the original v1 process with the
   same bucket config. Because the v2 migrations are additive and the legacy
   projection remains enabled, legacy readers continue to see familiar status
   fields.

7) Repeat for each v1 host. After the whole fleet is on v2 and stable, plan a
   coordinated dynamic-ownership cutover, remove `PINNED_BUCKET_*` from the v2
   monitor configs, restart the fleet in the approved window, then run:

		./jetmon2 rollout dynamic-check

   This verifies fresh, active, gap-free, overlap-free `jetmon_hosts` coverage
   before the fleet moves to normal v2 rolling updates.

If either rollout check reports legacy projection drift, list the mismatched
active site rows before continuing:

		./jetmon2 rollout projection-drift

For a specific range:

		./jetmon2 rollout projection-drift --bucket-min=0 --bucket-max=99 --limit=100

See [`docs/v1-to-v2-pinned-rollout.md`](docs/v1-to-v2-pinned-rollout.md) for
the detailed rollout checklist.

### v2 Rolling Updates (Zero Downtime)

After all monitor hosts are already on v2 dynamic bucket ownership, update one
host at a time. Surviving hosts absorb the draining host's buckets during the
update window:

1) On the host being updated, drain in-flight checks and release buckets:

		systemctl stop jetmon2

2) Deploy the new binary and run migrations:

		./jetmon2 migrate

3) Restart the service:

		systemctl start jetmon2

4) Verify the host has reclaimed its buckets:

		./jetmon2 status

5) Repeat for the next host.

### Decommissioning a Host

Send SIGINT to release buckets immediately:

	systemctl stop jetmon2

The service releases its buckets to the pool before exiting. Surviving hosts reclaim them at the start of their next round.

### Checking Service Health

	./jetmon2 status

Or check the operator dashboard at the configured `DASHBOARD_PORT` for
check-pool, throughput, bucket, rollout guard, memory, WPCOM circuit-breaker
state, and live dependency health. The rollout section shows bucket ownership
mode, legacy projection mode, delivery-worker ownership, and the matching
rollout preflight and projection-drift commands for the active config.

### Config Reload Without Restart

	./jetmon2 reload

Or directly:

	kill -HUP $(pgrep jetmon2)

### Monitoring Bucket Coverage

Query the `jetmon_hosts` table to verify all buckets are covered and heartbeats are current:

	SELECT host_id, bucket_min, bucket_max, last_heartbeat, status
	FROM jetmon_hosts
	ORDER BY bucket_min;

A host whose `last_heartbeat` is older than `BUCKET_HEARTBEAT_GRACE_SEC` will have its buckets reclaimed by peers on their next round.

### Memory Pressure

If RSS exceeds `WORKER_MAX_MEM_MB`, the goroutine pool shrinks by 10% via graceful drain. If memory pressure is sustained, use the pprof endpoint to investigate before raising the threshold:

	curl http://localhost:6060/debug/pprof/heap > heap.prof
	go tool pprof heap.prof

### StatsD Metrics

Metrics are emitted with prefix `com.jetpack.jetmon.<hostname>`. The Graphite/Grafana dashboard tracks:

- Free and active goroutines
- Sites processed per second
- Round completion time
- WPCOM API attempt, delivered, retry, error, and failed rates, including
  status-specific splits for `down`, `running`, and `confirmed_down`
- Veriflier response times
- Detection flow timing: first failure → Seems Down, first failure →
  Veriflier escalation, Seems Down → Down, Seems Down → false alarm, and
  Seems Down → probe-cleared recovery
- Detection outcome counters split by local failure class (`server`, `client`,
  `blocked`, `https`, `redirect`, `intermittent`) for false-alarm and
  confirmed-down rate comparisons
- Veriflier decision counters: escalations, RPC success/error, confirm/disagree
  votes, quorum-met confirmations, and false alarms
- Per-Veriflier-host RPC and vote counters under `verifier.host.<host>.*` so
  region/provider disagreement and latency can be compared during v2 production
- Legacy projection drift: per-bucket count of active sites whose
  `site_status` no longer matches the authoritative open HTTP event
- Memory usage

StatsD is the primary metrics transport. For integration with external systems, expose the Graphite/StatsD data via your existing metrics pipeline.

### Veriflier Health

Verifliers that fail to respond are automatically excluded from confirmation requests. If the number of healthy Verifliers drops below `PEER_OFFLINE_LIMIT`, no further downtime confirmations can be issued — monitor Veriflier health closely.

Verify Veriflier connectivity manually:

	curl http://<veriflier-host>:7803/status


For Happiness Engineers
-----------------------

### Looking Up a Site's Check History

Use the audit CLI to see a complete timeline for any site by `blog_id`:

	./jetmon2 audit --blog-id 12345 --since 24h

This outputs a human-readable timeline of every event: checks performed, local retries, Veriflier requests and results, WPCOM notifications sent, status transitions, and any maintenance windows that were active.

The same data is queryable in the operator dashboard under the site's detail view.

### Replaying an Alert Sequence

To reconstruct exactly what happened during a reported incident — useful for "why did I get an alert?" or "why didn't I get an alert?" investigations:

	./jetmon2 audit --blog-id 12345 --since 2026-04-01T10:00:00 --until 2026-04-01T11:00:00

The output shows the sequence of local checks, which Verifliers were asked to confirm and what they returned, and what was sent to WPCOM including the full payload.

### Understanding Alert Types

| Type | Meaning |
|------|---------|
| `server` | Site returned a 5xx response |
| `blocked` | Site returned 403 (monitoring blocked) |
| `client` | Site returned a 4xx other than 403 (auth or DNS issue) |
| `https` | SSL/TLS problem detected |
| `intermittent` | Request timed out (site may still be loading slowly) |
| `redirect` | Redirect policy failure (too many redirects or unexpected redirect) |
| `ssl_expiry` | SSL certificate expires within the configured threshold |
| `tls_deprecated` | Site is serving TLS 1.0 or 1.1 |
| `keyword_missing` | Response body did not contain the expected keyword |
| `success` | Site recovered (used in "site is back up" notifications) |

### Checking SSL Certificate Status

Query the database for a site's last recorded certificate expiry:

	SELECT blog_id, monitor_url, ssl_expiry_date
	FROM jetpack_monitor_sites
	WHERE blog_id = 12345;

`ssl_expiry_date` is updated on every HTTPS check. Alerts fire automatically at 30, 14, and 7 days before expiry via the same WPCOM notification path as downtime alerts.

### Checking for False Positives

If a customer reports an alert they believe was incorrect, check the false positives table:

	SELECT *
	FROM jetmon_false_positives
	WHERE blog_id = 12345
	ORDER BY created_at DESC
	LIMIT 20;

A false positive is recorded whenever Jetmon escalated a site to Veriflier confirmation but the Verifliers did not confirm it as down. High false positive rates for a site suggest its `NUM_OF_CHECKS` or `TIME_BETWEEN_CHECKS_SEC` per-site overrides should be tuned.

### Setting a Maintenance Window

To suppress alerts for a site during planned maintenance, set `maintenance_start` and `maintenance_end` directly in the database:

	UPDATE jetpack_monitor_sites
	SET maintenance_start = '2026-04-20 02:00:00',
	    maintenance_end   = '2026-04-20 04:00:00'
	WHERE blog_id = 12345;

Checks continue and results are recorded in the audit log during the window, but no WPCOM notifications are sent. Clear the window after maintenance is complete by setting both columns to NULL.

**Important:** An open-ended maintenance window (NULL `maintenance_end`) silently suppresses all alerts indefinitely. Always set an explicit end time.

### Tuning Alert Sensitivity Per Site

To reduce false positives for a site that flaps frequently:

	UPDATE jetpack_monitor_sites
	SET alert_cooldown_minutes = 60
	WHERE blog_id = 12345;

To require more local confirmations before escalating to Verifliers (increases confidence but slows detection):

	UPDATE jetpack_monitor_sites
	SET check_count_override = 5,
	    check_interval_override_sec = 30
	WHERE blog_id = 12345;

### WPCOM Notification Data

Every status change notification sent to WPCOM includes:

| Field | Description |
|-------|-------------|
| `blog_id` | The site's WPCOM ID |
| `monitor_url` | The URL that was checked |
| `status_id` | `0` = down, `1` = running, `2` = confirmed down |
| `last_check` | Datetime of the last check |
| `last_status_change` | Datetime of the last status change |
| `checks` | Array of check results from Jetmon and Verifliers |

Each entry in `checks` includes:

| Field | Description |
|-------|-------------|
| `type` | `1` = Jetmon local check, `2` = Veriflier check |
| `host` | Hostname of the checking server |
| `status` | `0` = down, `1` = running, `2` = confirmed down |
| `rtt` | Round-trip time in milliseconds |
| `code` | HTTP response code |

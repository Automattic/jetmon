jetmon2
=======

Overview
--------

Jetmon is a parallel HTTP uptime monitoring service that checks Jetpack websites at scale. Jetmon 2 is a complete rewrite of the original Node.js + C++ service as a single Go binary, delivering a large reduction in memory usage, a significant increase in concurrent checks per host, and a simpler deployment model with no native addon compilation.

Jetmon periodically loops over a list of Jetpack sites and performs HTTP checks. When a site appears down, local retries are attempted before geographically distributed Veriflier services are asked to confirm the outage. WPCOM is notified only after confirmation, keeping false positive rates low.

Jetmon 2 is a drop-in replacement: the MySQL schema, WPCOM notification payload, StatsD metric names, log file format, and config file keys are all backwards-compatible. See `PROJECT.md` for the full feature specification and performance estimates.


Architecture
------------

```
┌──────────────────────────────────────────────────────┐
│                  jetmon2 (single binary)             │
│                                                      │
│  ┌─────────────┐  ┌─────────────┐  ┌──────────────┐  │
│  │ Orchestrator│  │ Check Pool  │  │  gRPC Server │  │
│  │  goroutine  │  │ (goroutines)│  │  (Veriflier) │  │
│  └──────┬──────┘  └──────┬──────┘  └──────┬───────┘  │
│         │                │                │          │
│  ┌──────┴────────────────┴────────────────┴───────┐  │
│  │                 Internal channels              │  │
│  └────────────────────────────────────────────────┘  │
└────────────┬──────────────────────────┬──────────────┘
             │                          │
          MySQL                    WPCOM API
          StatsD                   (unchanged)
          Log files
          (all unchanged)
```

The **Orchestrator goroutine** fetches site batches from MySQL, dispatches work to the check pool, manages the local retry queue, coordinates Veriflier confirmation, and sends WPCOM notifications. It owns all database access and all outbound WPCOM calls.

The **Check Pool** is a bounded goroutine pool that performs HTTP checks using Go's `net/http` and `net/http/httptrace`. It records DNS, TCP, TLS, and TTFB timings on every check and auto-scales against queue depth without spawning new processes.

The **gRPC Server** receives confirmation results from remote Veriflier instances, replacing the previous custom HTTPS protocol.

The **Veriflier** is a standalone Go binary deployed at remote locations. It replaces the Qt C++ Veriflier, communicating with the Monitor via gRPC.

Status change flows:

| Previous Status | Current Status    | Action                                            |
|-----------------|-------------------|---------------------------------------------------|
| UP              | DOWN              | Local retries → Veriflier confirmation → notify WPCOM |
| DOWN            | UP                | Notify WPCOM site recovered                       |
| DOWN            | DOWN (confirmed)  | Notify WPCOM confirmed down                       |


Installation
------------

1) Install [Docker](https://docs.docker.com/get-docker/) and [docker-compose](https://docs.docker.com/compose/install/)

2) Clone the repository

3) Copy the environment file:

		cd docker && cp .env-sample .env

4) Edit `docker/.env` for your local environment

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
| `ALERT_COOLDOWN_MINUTES` | 30 | Default cooldown between repeated alerts per site |
| `LOG_FORMAT` | `text` | `text` for plain-text logs or `json` for structured logs |
| `DASHBOARD_PORT` | 8080 | Internal port for the operator dashboard (0 to disable) |
| `DEBUG_PORT` | 6060 | localhost-only pprof port (`127.0.0.1:PORT`); 0 to disable |

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

New tables added by Jetmon 2:

| Table | Purpose |
|-------|---------|
| `jetmon_hosts` | MySQL-coordinated bucket ownership and heartbeat |
| `jetmon_audit_log` | Full event history per site |
| `jetmon_check_history` | RTT and timing samples for trending |
| `jetmon_false_positives` | Veriflier non-confirmation events |

Apply migrations before starting for the first time:

	./jetmon2 migrate


For Developers
--------------

### Prerequisites

- Go 1.22 or later
- Docker and docker-compose (for integration testing)
- Access to a MySQL instance (provided by Docker Compose)

### Building

	go build ./cmd/jetmon2/
	go build ./veriflier2/

### Running Tests

	go test ./...
	go test -race ./...

Tests require the Docker Compose environment to be running for integration tests. Unit tests run standalone.

### Docker Development Loop

	cd docker
	docker compose up --build -d          # Build binary and start all services
	docker compose logs -f jetmon          # Follow logs
	docker compose exec jetmon bash        # Shell into the container

### Adding Test Sites

Connect to the test database:

	docker compose exec mysqldb mysql -u root -p123456 jetmon_db

Insert sites to check:

	INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status)
	VALUES
	    (1, 0, 'https://wordpress.com', 1, 1),
	    (2, 0, 'https://httpstat.us/200', 1, 1),
	    (3, 0, 'https://httpstat.us/500', 1, 1),
	    (4, 0, 'https://httpstat.us/200?sleep=15000', 1, 1);

### Enabling Database Updates

Edit `config/config.json`:

	{ "DB_UPDATES_ENABLE": true }

Then set the guard environment variable in `docker/.env`:

	JETMON_UNSAFE_DB_UPDATES=1

Both must be set together. The binary refuses to start with `DB_UPDATES_ENABLE: true` unless `JETMON_UNSAFE_DB_UPDATES=1` is also present in the environment.

**WARNING:** Never enable in production.

### Simulated Site Server

The Docker Compose environment includes a simulated site server. Toggle site states via its HTTP API to test specific scenarios without depending on external services:

- Static response codes (200, 404, 500, 503)
- Configurable response delay for timeout testing
- Flapping mode (alternates up/down on a schedule)
- SSL with a self-signed certificate
- Keyword presence and absence for content check testing
- Redirect chains
- Abrupt TCP close

### Config Validation

	./jetmon2 validate-config

Checks all required keys, validates value ranges, tests MySQL connectivity, tests Veriflier connectivity, and verifies the WPCOM API certificate.

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
| `internal/orchestrator/` | Round scheduling, DB fetch, WPCOM notifications |
| `internal/checker/` | HTTP check goroutine pool |
| `internal/veriflier/` | JSON-over-HTTP Veriflier transport (proto3 service defined in `proto/`) |
| `internal/db/` | MySQL access, bucket heartbeat |
| `internal/config/` | Config loading and hot-reload |
| `internal/metrics/` | StatsD client, stats file writer |
| `internal/wpcom/` | WPCOM API client and circuit breaker |
| `internal/audit/` | Audit log |
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

Check the StatsD dashboard at http://localhost:8088 under:
`Metrics > stats > com > jetpack > jetmon > docker > jetmon`

### Key Test Scenarios

**Downtime detection and confirmation:**
Insert a site pointing to `https://httpstat.us/500`. With `DB_UPDATES_ENABLE: true`, Jetmon should detect the failure, retry locally, escalate to the Veriflier, confirm down, and update `site_status` to `2`.

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

The dashboard is available at http://localhost:8080 (configurable via `DASHBOARD_PORT`). It shows goroutine counts, check queue depth, sites per second, Veriflier status, WPCOM API health, slowest sites, and most frequently down sites.

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
4) Create `/opt/jetmon2/config/jetmon2.env` with the database credentials and auth tokens (see `config/db-config-sample.conf` for the required keys)
5) Copy `config/config.json` from an existing host (or generate from `config-sample.json`)
6) Set `BUCKET_TARGET` to the desired maximum bucket count for this host
7) Run `./jetmon2 migrate` to apply any pending schema migrations
8) Start the service: `systemctl enable --now jetmon2`

The new host will claim unclaimed buckets from the pool on first startup. No existing hosts need reconfiguration.

### Rolling Updates (Zero Downtime)

Update one host at a time. Surviving hosts absorb the draining host's buckets during the update window:

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

Or check the operator dashboard at the configured `DASHBOARD_PORT`. The System Health Map view shows the status of MySQL, each Veriflier, WPCOM API, StatsD, and disk in a single grid.

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
- WPCOM API success and error rates
- Veriflier response times
- Memory usage

StatsD is the primary metrics transport. For integration with external systems, expose the Graphite/StatsD data via your existing metrics pipeline.

### Veriflier Health

Verifliers that fail to respond are automatically excluded from confirmation requests. The System Health Map shows each Veriflier's reachability and last response time. If the number of healthy Verifliers drops below `PEER_OFFLINE_LIMIT`, no further downtime confirmations can be issued — monitor Veriflier health closely.

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

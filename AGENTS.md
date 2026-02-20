# Jetmon Development Guidelines

You are an expert Node.js/C++ developer with extensive knowledge about WordPress and enterprise-level web services.

## Project Overview

Jetmon is a parallel HTTP health monitoring service that monitors Jetpack website uptime at scale. It performs HEAD requests against sites, uses geographically distributed Veriflier services to confirm downtime, and notifies WordPress.com of status changes.

## Architecture

```
Database → Master Process → Worker Pool → C++ HTTP Checks
                 ↓
         Veriflier Services (geo-distributed)
                 ↓
         WordPress.com API ← Status Notifications
```

**Master Process** (`lib/jetmon.js`): Spawns workers, fetches site batches from database every 5 seconds, distributes work, and notifies WordPress.com of status changes.

**Worker Processes** (`lib/httpcheck.js`): Forked child processes that perform HTTP checks via C++ native addon. Workers recycle when reaching memory limit (53MB) or check count (10,000).

**C++ Native Addon** (`src/http_checker.cpp`): High-performance HTTP checking with HEAD requests, 60-second timeout, OpenSSL support, and redirect handling.

**Veriflier Services** (`veriflier/`): C++/Qt applications deployed globally to verify downtime before status changes are reported.

## Build and Run Commands

```bash
# Docker development (recommended)
cd docker && docker compose up -d      # Start all services
docker compose down                     # Stop services

# Manual build and run
npm install
node-gyp rebuild
cp build/Release/jetmon.node lib/
node lib/jetmon.js

# Rebuild and run (npm script)
npm run rebuild-run
```

## Configuration

Copy `config/config-sample.json` to `config/config.json`. Key settings:

- `NUM_WORKERS`: Worker process count (default 60)
- `NUM_TO_PROCESS`: Parallel checks per worker (default 40)
- `BUCKET_NO_MIN/MAX`: Database bucket range for horizontal scaling (0-511 total)
- `MIN_TIME_BETWEEN_ROUNDS_SEC`: Check interval (300 seconds default)
- `PEER_OFFLINE_LIMIT`: Verifliers required to confirm downtime (3)

**Variable Check Intervals:** Sites can be configured for 1-5 minute check intervals via the `check_interval` database field. The default is 5 minutes. One-minute intervals require sufficient host capacity.

See `config/config.readme` for detailed documentation of all options.

## Key Files

| File | Purpose |
|------|---------|
| `lib/jetmon.js` | Master process orchestration |
| `lib/httpcheck.js` | Worker process HTTP checking |
| `lib/database.js` | MySQL queries and connection |
| `lib/comms.js` | HTTPS communication with Verifliers |
| `lib/wpcom.js` | WordPress.com API notifications |
| `src/http_checker.cpp` | C++ native addon for HTTP checks |
| `binding.gyp` | Node-gyp build configuration |

## Site Status Values

- `0` SITE_DOWN: Local checks failed
- `1` SITE_RUNNING: Confirmed online
- `2` SITE_CONFIRMED_DOWN: Verified down by Verifliers

## Monitoring Behavior

**Check Process:**
- Initial timeout: 10 seconds
- Verification timeout: 20 seconds (on retry from different locations)
- Max redirects: 3 (beyond this triggers "redirect" error)
- HTTP response code < 400 is considered success
- User Agent: `jetmon/1.0 (Jetpack Site Uptime Monitor by WordPress.com)`

**Downtime Verification:**
When a site appears down, Jetmon retries from the same location twice, then verifies from 2 other locations on different continents via Verifliers before confirming downtime.

**Status Change Email Types:**
- `server`: 5xx response (internal/fatal error)
- `blocked`: 403 response (monitoring blocked)
- `client`: 4xx response other than 403 (auth/DNS issues)
- `https`: SSL certificate problems
- `intermittent`: Request timeout (>10 seconds but site may load)
- `redirect`: Too many redirects (>3)
- `success`: Normal response (used in "site is back up" emails)

## Database Schema

Sites are stored in `jetpack_monitor_sites` with bucket-based sharding. The `bucket_no` field (0-511) enables horizontal scaling across multiple Jetmon instances.

## Metrics

StatsD metrics are sent with prefix `com.jetpack.jetmon.<hostname>`. Key metrics include worker lifecycle events, queue sizes, database timing, and memory usage.

**Grafana Dashboard:** Production metrics are visualized in the Jetmon Health Dashboard using Graphite as the StatsD backend. The dashboard tracks free/active workers, sites processed, round times, and memory usage.

**StatsD Configuration Notes:**
- Flush interval: 5 seconds (`STATS_UPDATE_INTERVAL_MS`)
- Graphite retention: 10s:6h, 1m:7d, 10m:5y
- Counter metrics use `sum` aggregation; gauges use `average`

## WPCOM Integration

**Jetmon Endpoint:** WPCOM receives status change notifications from Jetmon and triggers the `jetpack_monitor_site_status_change` hook for consumers (notifications, Activity Log, etc.).

**Email Notification Options (stored on WPCOM):**
- `jetpack_monitor_notifications_users_ids`: WPCOM user IDs to notify
- `jetpack_monitor_notify_email_addresses`: Additional email addresses

**REST API Endpoints:**
- `GET /sites/{site}/jetpack-monitor-status`: Current monitoring status
- `GET /sites/{site}/jetpack-monitor-incidents`: Historical incidents
- `GET/POST /sites/{site}/jetpack-monitor-settings`: Monitor configuration

## Production Deployment

Jetmon runs on 6 production hosts managed by the Systems team. To deploy changes:
1. Test changes locally using Docker environment
2. Create a Systems Request with PR links for review
3. Systems team deploys to production hosts

## Worker Lifecycle

Workers exit and are respawned when:
- Memory exceeds `WORKER_MAX_MEM_MB` (53MB default)
- Check count exceeds `WORKER_MAX_CHECKS` (10,000 default)
- Process receives termination signal

The master process tracks worker states and gracefully handles recycling.

## Known Pitfalls

**Retry Queue Persistence:** Retry queues must persist between rounds. Flushing queues at round start prevents sites from being confirmed as down, since the 1-minute recheck cannot complete before the next round.

**Bucket Configuration:** The `BUCKET_NO_MIN/MAX` configuration must not overlap between hosts. A past misconfiguration caused hosts to process only half their intended sites, masking performance issues.

**Node Version Sensitivity:** RTT (round-trip time) calculations can vary between Node.js versions. Version changes should be tested thoroughly as they can affect timeout behaviors.

**Memory Pressure:** When checking more sites (due to shorter intervals or configuration fixes), memory usage increases. Monitor memory metrics and consider scaling hosts horizontally if workers frequently hit memory limits.

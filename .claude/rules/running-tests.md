# Running Tests

Jetmon does not have a formal automated test suite. Testing is performed manually using the Docker development environment.

## Prerequisites

1. Install [Docker](https://docs.docker.com/get-docker/) and [docker-compose](https://docs.docker.com/compose/install/)
2. Clone the repository
3. Set up environment variables:
   ```bash
   cd docker
   cp .env-sample .env
   ```

## Docker Environment

### Start/Stop Services
```bash
cd docker
docker compose up -d                  # Start all services
docker compose down                   # Stop all services
docker compose down -v                # Stop and remove volumes (fresh start)
```

Services started: `mysqldb` (MySQL 5.7), `jetmon` (master + workers), `veriflier`, `statsd` (Graphite)

### View Logs
```bash
docker compose logs -f jetmon         # Follow Jetmon logs
docker compose logs -f veriflier      # Follow Veriflier logs
docker compose exec jetmon cat logs/jetmon.log
docker compose exec jetmon cat logs/status-change.log
```

### Monitor Activity
```bash
docker compose exec jetmon cat stats/sitespersec
docker compose exec jetmon cat stats/sitesqueue
docker compose exec jetmon ps auxf    # Process tree: master, workers, server
```

## Test Database Setup

### Create Table
```bash
docker compose exec mysqldb mysql -u root -p123456 jetmon_db
```

```sql
CREATE TABLE IF NOT EXISTS `jetpack_monitor_sites` (
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
```

### Insert Test Sites
```sql
INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status)
VALUES
    (1, 0, 'https://wordpress.com', 1, 1),
    (2, 0, 'https://jetpack.com', 1, 1),
    (3, 1, 'https://httpstat.us/500', 1, 1),   -- Returns 500 error
    (4, 1, 'https://httpstat.us/200', 1, 1),   -- Returns 200 OK
    (5, 0, 'https://httpstat.us/503', 1, 1),   -- Service unavailable
    (6, 0, 'https://httpstat.us/200?sleep=15000', 1, 1),  -- Slow response (timeout test)
    (7, 0, 'https://httpstat.us/301', 1, 1);   -- Redirect test
```

### Enable Database Updates
Edit `config/config.json`:
```json
{
    "DB_UPDATES_ENABLE": true
}
```

**WARNING**: Only enable `DB_UPDATES_ENABLE` in local test environments. Never in production.

## Testing Scenarios

### Configuration Reload
```bash
docker compose exec jetmon ps aux | grep jetmon-master  # Find PID
docker compose exec jetmon kill -HUP <pid>              # Reload config
```

### Graceful Shutdown
```bash
docker compose exec jetmon kill -INT <pid>    # Or: docker compose restart jetmon
```

### Veriflier Connectivity
```bash
docker compose exec jetmon curl -k https://veriflier:7801/get/status
# Should return: OK
```

### Native Addon Rebuild
```bash
docker compose exec jetmon npm run rebuild-run
# Or manually:
docker compose exec jetmon bash -c "node-gyp rebuild && cp build/Release/jetmon.node lib/ && node lib/jetmon.js"
```

### Test HTTP Checker Directly
Create `lib/test-addon.js`:
```javascript
var checker = require( './jetmon.node' );
checker.http_check( 'https://wordpress.com', 80, 0, function( index, rtt, http_code, error_code ) {
    console.log( 'RTT:', rtt, 'HTTP:', http_code, 'Error:', error_code );
    process.exit( 0 );
});
```
Run: `docker compose exec jetmon node lib/test-addon.js`

### Worker Recycling
Set low limits in `config/config.json`:
```json
{
    "WORKER_MAX_CHECKS": 100,
    "WORKER_MAX_MEM_MB": 30
}
```
Watch: `docker compose logs -f jetmon | grep -E "(spawn|die|recycle|limit)"`

### Memory Monitoring
```bash
docker compose exec jetmon bash -c 'while true; do ps aux --sort=-%mem | head -10; sleep 5; done'
```

### StatsD Metrics
- Dashboard: http://localhost:8088
- Path: `Metrics > stats > com > jetpack > jetmon > docker > jetmon`
- Test: `docker compose exec jetmon bash -c 'echo "test.metric:1|c" | nc -u -w1 statsd 8125'`

## Debugging

Enable debug mode in `config/config.json`:
```json
{
    "DEBUG": true
}
```

Attach to container: `docker compose exec jetmon bash`

Query database: `docker compose exec mysqldb mysql -u root -p123456 jetmon_db -e "SELECT COUNT(*) FROM jetpack_monitor_sites WHERE monitor_active = 1;"`

## Common Issues

| Problem | Check |
|---------|-------|
| Jetmon not starting | `docker compose ps mysqldb`, verify `config/db-config.conf` |
| No sites being checked | Verify `BUCKET_NO_MIN/MAX` matches data, `monitor_active = 1` |
| Veriflier connection fails | `docker compose ps veriflier`, check auth tokens match, SSL certs exist |
| StatsD not receiving | `docker compose exec jetmon ping statsd`, check for UDP errors |

## Cleanup

```bash
docker compose down -v                # Remove volumes
rm -f config/config.json config/db-config.conf
rm -rf logs/*.log stats/*
```

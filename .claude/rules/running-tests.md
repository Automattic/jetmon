# Running Tests

Jetmon does not have a formal automated test suite. Testing is performed manually using the Docker development environment. This guide covers how to test the various components of the system.

## Prerequisites

1. Install [Docker](https://docs.docker.com/get-docker/) and [docker-compose](https://docs.docker.com/compose/install/)
2. Clone the repository
3. Set up environment variables:
   ```bash
   cd docker
   cp .env-sample .env
   # Edit .env if needed
   ```

## Starting the Test Environment

### Start All Services
```bash
cd docker
docker compose up -d
```

This starts:
- `mysqldb` - MySQL 5.7 database
- `jetmon` - Main monitoring service (master + workers)
- `veriflier` - Geographic verification service
- `statsd` - Graphite/StatsD for metrics

### Start Individual Services
```bash
docker compose up -d mysqldb      # Database only
docker compose up -d jetmon       # Jetmon (requires mysqldb)
docker compose up -d veriflier    # Veriflier only
docker compose up -d statsd       # Metrics only
```

### View Service Logs
```bash
docker compose logs -f jetmon      # Follow Jetmon logs
docker compose logs -f veriflier   # Follow Veriflier logs
docker compose logs mysqldb        # View database logs
```

### Stop All Services
```bash
docker compose down
```

## Testing Jetmon

### Verify Jetmon Is Running
```bash
docker compose ps
# Should show jetmon container as "Up"
```

### Check Jetmon Logs
```bash
# Real-time logs from Docker
docker compose logs -f jetmon

# Log files inside container
docker compose exec jetmon cat logs/jetmon.log
docker compose exec jetmon cat logs/status-change.log
```

### Monitor Worker Activity
```bash
# View stats files
docker compose exec jetmon cat stats/sitespersec
docker compose exec jetmon cat stats/sitesqueue
docker compose exec jetmon cat stats/totals
```

### Test Configuration Reload
```bash
# Find the master process PID
docker compose exec jetmon ps aux | grep jetmon-master

# Send SIGHUP to reload config
docker compose exec jetmon kill -HUP <pid>
```

### Test Graceful Shutdown
```bash
# Send SIGINT to test graceful shutdown
docker compose exec jetmon kill -INT <pid>

# Or restart the container
docker compose restart jetmon
```

## Testing with Test Data

### Create Test Database Table
```bash
docker compose exec mysqldb mysql -u root -p123456 jetmon_db
```

Then run:
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
-- Insert test sites to monitor
INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status)
VALUES
    (1, 0, 'https://wordpress.com', 1, 1),
    (2, 0, 'https://jetpack.com', 1, 1),
    (3, 1, 'https://example.com', 1, 1),
    (4, 1, 'https://httpstat.us/500', 1, 1),  -- Returns 500 error
    (5, 2, 'https://httpstat.us/200', 1, 1);  -- Returns 200 OK
```

### Enable Database Updates for Testing
Edit `config/config.json` inside the container or on your host:
```json
{
    "DB_UPDATES_ENABLE": true
}
```

**WARNING**: Only enable `DB_UPDATES_ENABLE` in local test environments. Never enable in production.

### Verify Site Checks
```bash
# Watch for status changes
docker compose exec jetmon tail -f logs/status-change.log
```

## Testing the Veriflier

### Verify Veriflier Is Running
```bash
docker compose ps
# Should show veriflier container as "Up"

# Check veriflier logs
docker compose logs -f veriflier
```

### Test Veriflier Connectivity
```bash
# From the jetmon container, test connection to veriflier
docker compose exec jetmon curl -k https://veriflier:7801/get/status
# Should return: OK
```

### Veriflier Logs
```bash
docker compose exec veriflier cat /opt/veriflier/logs/veriflier.log
```

## Testing StatsD Metrics

### Access Graphite Dashboard
Open http://localhost:8088 in your browser to view the Graphite web interface.

### Verify Metrics Are Being Sent
Navigate to: `Metrics > stats > com > jetpack > jetmon > docker > jetmon`

Key metrics to check:
- `stats.workers.free.count` - Number of free workers
- `stats.workers.working.count` - Number of active workers
- `stats.sites.total.count` - Total sites processed
- `round.complete.time` - Time to complete a check round

### Test StatsD Manually
```bash
# Send a test metric from the jetmon container
docker compose exec jetmon bash -c 'echo "test.metric:1|c" | nc -u -w1 statsd 8125'
```

## Testing the Native Addon

### Rebuild After C++ Changes
```bash
docker compose exec jetmon npm run rebuild-run
```

Or manually:
```bash
docker compose exec jetmon bash
cd /jetmon
node-gyp rebuild
cp build/Release/jetmon.node lib/
node lib/jetmon.js
```

### Test HTTP Checker Directly
Create a test script `lib/test-addon.js`:
```javascript
var checker = require( './jetmon.node' );

checker.http_check( 'https://wordpress.com', 80, 0, function( index, rtt, http_code, error_code ) {
    console.log( 'Index:', index );
    console.log( 'RTT (microseconds):', rtt );
    console.log( 'HTTP Code:', http_code );
    console.log( 'Error Code:', error_code );
    process.exit( 0 );
});
```

Run it:
```bash
docker compose exec jetmon node lib/test-addon.js
```

## Testing Memory Behavior

### Monitor Memory Usage
```bash
# Watch process memory over time
docker compose exec jetmon bash -c 'while true; do ps aux --sort=-%mem | head -20; sleep 5; done'
```

### Test Worker Recycling
Set low limits in `config/config.json`:
```json
{
    "WORKER_MAX_CHECKS": 100,
    "WORKER_MAX_MEM_MB": 30
}
```

Watch workers recycle:
```bash
docker compose logs -f jetmon | grep -E "(spawn|die|recycle|limit)"
```

## Testing Specific Scenarios

### Test Site Down Detection
1. Add a test site that returns errors:
   ```sql
   INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active)
   VALUES (999, 0, 'https://httpstat.us/503', 1);
   ```

2. Watch for status changes:
   ```bash
   docker compose exec jetmon tail -f logs/status-change.log
   ```

### Test Timeout Handling
Add a slow-responding site:
```sql
INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active)
VALUES (998, 0, 'https://httpstat.us/200?sleep=15000', 1);
```

### Test Redirect Handling
```sql
INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active)
VALUES (997, 0, 'https://httpstat.us/301', 1);
```

## Debugging Tips

### Enable Debug Mode
Ensure `config/config.json` has:
```json
{
    "DEBUG": true
}
```

### Attach to Running Container
```bash
docker compose exec jetmon bash
```

### Check Process Tree
```bash
docker compose exec jetmon ps auxf
# Should show: jetmon-master, jetmon-worker (multiple), jetmon-server
```

### Database Query Testing
```bash
docker compose exec mysqldb mysql -u root -p123456 jetmon_db -e "SELECT COUNT(*) FROM jetpack_monitor_sites WHERE monitor_active = 1;"
```

## Common Issues

### Jetmon Not Starting
- Check database is running: `docker compose ps mysqldb`
- Verify database config: `docker compose exec jetmon cat config/db-config.conf`
- Check for port conflicts on 7800, 7801, 7802

### No Sites Being Checked
- Verify sites exist in database
- Check bucket range in config matches data: `BUCKET_NO_MIN`, `BUCKET_NO_MAX`
- Ensure `monitor_active = 1` for test sites

### Veriflier Connection Failures
- Check veriflier is running: `docker compose ps veriflier`
- Verify auth tokens match between jetmon and veriflier configs
- Check SSL certificates exist in `veriflier/certs/`

### StatsD Not Receiving Metrics
- Verify statsd container is running
- Check network connectivity: `docker compose exec jetmon ping statsd`
- Look for UDP errors in jetmon logs

## Cleanup

### Reset Test Environment
```bash
docker compose down -v  # Removes volumes (database data)
docker compose up -d    # Fresh start
```

### Remove Generated Files
```bash
rm -f config/config.json
rm -f config/db-config.conf
rm -rf logs/*.log
rm -rf stats/*
```

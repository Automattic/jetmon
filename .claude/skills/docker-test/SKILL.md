---
name: docker-test
description: Run, debug, and test Jetmon using the Docker development environment
allowed-tools: Bash(docker*), Bash(cd docker*), Read, Glob, Grep
---

# Docker Testing Skill

Use this skill for running, debugging, and testing Jetmon in the Docker development environment.

## Usage

- `/docker-test` - Interactive mode: I'll help you start and test the environment
- `/docker-test start` - Start all Docker services
- `/docker-test logs` - View Jetmon logs
- `/docker-test status` - Check service status
- `/docker-test stop` - Stop all services

## Docker Services

The docker-compose environment includes:

| Service | Port | Purpose |
|---------|------|---------|
| `mysqldb` | 3306 | MySQL 5.7 database |
| `jetmon` | 7800 | Main monitoring service |
| `veriflier` | 7801 | Geographic verification |
| `statsd` | 8125/8088 | Metrics (Graphite UI on 8088) |

## Common Commands

### Starting Services

```bash
cd docker && docker compose up -d           # Start all services
docker compose up -d mysqldb jetmon         # Start specific services
docker compose up -d --build jetmon         # Rebuild and start
```

### Viewing Logs

```bash
docker compose logs -f jetmon               # Follow Jetmon logs
docker compose logs -f veriflier            # Follow Veriflier logs
docker compose logs --tail=100 jetmon       # Last 100 lines
```

### Checking Status

```bash
docker compose ps                           # Service status
docker compose exec jetmon ps auxf          # Process tree inside container
```

### Stopping Services

```bash
docker compose down                         # Stop all services
docker compose down -v                      # Stop and remove volumes (reset DB)
```

## Testing Scenarios

### 1. Verify Jetmon Is Running

```bash
docker compose ps
docker compose exec jetmon cat stats/totals
docker compose exec jetmon cat stats/sitespersec
```

### 2. Check Worker Activity

```bash
# View worker stats
docker compose exec jetmon cat stats/sitesqueue

# Monitor worker memory
docker compose exec jetmon bash -c 'ps aux --sort=-%mem | head -10'
```

### 3. Test Configuration Reload

```bash
# Find master process PID
docker compose exec jetmon ps aux | grep jetmon-master

# Send SIGHUP to reload config
docker compose exec jetmon kill -HUP <pid>
```

### 4. Test Graceful Shutdown

```bash
# Send SIGINT for graceful shutdown
docker compose exec jetmon kill -INT <pid>

# Or restart the container
docker compose restart jetmon
```

### 5. View Status Changes

```bash
docker compose exec jetmon tail -f logs/status-change.log
```

### 6. Check Database

```bash
docker compose exec mysqldb mysql -u root -p123456 jetmon_db -e "SELECT COUNT(*) FROM jetpack_monitor_sites;"
```

## Adding Test Data

### Insert Test Sites

```bash
docker compose exec mysqldb mysql -u root -p123456 jetmon_db
```

```sql
INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status)
VALUES
    (1, 0, 'https://wordpress.com', 1, 1),
    (2, 0, 'https://jetpack.com', 1, 1),
    (3, 1, 'https://httpstat.us/500', 1, 1),  -- Returns 500 error
    (4, 1, 'https://httpstat.us/200', 1, 1);  -- Returns 200 OK
```

### Enable Database Updates

Edit `config/config.json`:
```json
{
    "DB_UPDATES_ENABLE": true
}
```

**WARNING**: Only enable `DB_UPDATES_ENABLE` in local test environments.

## Debugging Tips

### Enable Debug Mode

Ensure `config/config.json` has:
```json
{
    "DEBUG": true
}
```

### Attach to Container

```bash
docker compose exec jetmon bash
```

### Test Native Addon Directly

Create `lib/test-addon.js`:
```javascript
var checker = require( './jetmon.node' );

checker.http_check( 'https://wordpress.com', 80, 0, function( index, rtt, http_code, error_code ) {
    console.log( 'RTT:', rtt, 'HTTP:', http_code, 'Error:', error_code );
    process.exit( 0 );
});
```

Run it:
```bash
docker compose exec jetmon node lib/test-addon.js
```

### Check Metrics

Open http://localhost:8088 for Graphite UI. Navigate to:
`Metrics > stats > com > jetpack > jetmon > docker > jetmon`

## Common Issues

### Jetmon Not Starting

- Check database: `docker compose ps mysqldb`
- Verify config: `docker compose exec jetmon cat config/db-config.conf`
- Check for port conflicts on 7800, 7801, 7802

### No Sites Being Checked

- Verify sites exist in database
- Check bucket range matches data: `BUCKET_NO_MIN`, `BUCKET_NO_MAX`
- Ensure `monitor_active = 1` for test sites

### Veriflier Connection Failures

- Check veriflier is running: `docker compose ps veriflier`
- Test connectivity: `docker compose exec jetmon curl -k https://veriflier:7801/get/status`
- Verify SSL certificates exist in `veriflier/certs/`

### Memory Issues

```bash
# Monitor memory over time
docker compose exec jetmon bash -c 'while true; do ps aux --sort=-%mem | head -10; sleep 5; done'
```

## Cleanup

```bash
docker compose down -v                      # Remove volumes (database data)
docker compose up -d                        # Fresh start
```

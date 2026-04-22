---
name: docker-test
description: Run, debug, and test Jetmon 2 using the Docker development environment
allowed-tools: Bash(docker*), Bash(cd docker*), Read, Glob, Grep
---

# Docker Testing Skill

Use this skill for running, debugging, and testing Jetmon 2 in the Docker development environment.

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
| `mysqldb` | 3306 | MySQL 8.0 database |
| `jetmon` | 8080 | Jetmon 2 + operator dashboard |
| `veriflier` | 7803 | Geographic verification (gRPC) |
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
docker compose exec jetmon ps aux           # Single process inside container
docker compose exec jetmon ./jetmon2 status # Internal status via API
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

### 2. Open Operator Dashboard

Navigate to http://localhost:8080 in a browser. The dashboard shows:
- Worker/goroutine count
- Retry queue size
- WPCOM circuit breaker state
- Bucket range owned by this host

### 3. Test Configuration Reload

```bash
docker compose exec jetmon ./jetmon2 reload  # Sends SIGHUP via PID file
# Watch logs for "config reloaded"
docker compose logs -f jetmon
```

### 4. Test Graceful Drain/Shutdown

```bash
docker compose exec jetmon ./jetmon2 drain   # Sends SIGINT via PID file
# Or:
docker compose stop jetmon
```

### 5. View Audit Log

```bash
docker compose exec jetmon ./jetmon2 audit --blog-id 1 --since 1h
```

### 6. Test Veriflier Connectivity

```bash
docker compose exec jetmon curl http://veriflier:7803/status
# Should return: {"hostname":"...","version":"...","status":"ok"}
```

### 7. Check Database

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

### Validate Config Before Restart

```bash
docker compose exec jetmon ./jetmon2 validate-config
```

### Attach to Container

```bash
docker compose exec jetmon bash
```

### Profile Goroutines / Memory (pprof)

The dashboard exposes pprof at http://localhost:8080/debug/pprof/

```bash
# Goroutine dump
curl http://localhost:8080/debug/pprof/goroutine?debug=1

# Heap profile
curl http://localhost:8080/debug/pprof/heap > heap.prof
go tool pprof heap.prof
```

### Check Metrics

Open http://localhost:8088 for Graphite UI. Navigate to:
`Metrics > stats > com > jetpack > jetmon > docker > jetmon`

## Common Issues

### Jetmon Not Starting

- Check database: `docker compose ps mysqldb`
- Validate config: `docker compose exec jetmon ./jetmon2 validate-config`
- Check migration output: `docker compose logs jetmon | head -30`

### No Sites Being Checked

- Verify sites exist in database with `monitor_active = 1`
- Check bucket ownership: `docker compose exec jetmon ./jetmon2 status`

### Veriflier Connection Failures

- Check veriflier is running: `docker compose ps veriflier`
- Test connectivity: `docker compose exec jetmon curl http://veriflier:7803/status`
- Verify `VERIFLIER_AUTH_TOKEN` matches in both containers

### Memory Issues

```bash
# Monitor goroutine count and memory via pprof
curl http://localhost:8080/debug/pprof/goroutine?debug=1 | head -20
```

## Cleanup

```bash
docker compose down -v                      # Remove volumes (database data)
docker compose up -d                        # Fresh start
```

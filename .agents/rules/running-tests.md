# Running Tests

Jetmon 2 has a Go test suite (`go test ./...`) and a Docker development environment for integration testing.

## Automated Tests

```bash
make test          # go test ./...
make test-race     # go test -race ./...
make lint          # go vet ./...
```

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

Services started: `mysqldb` (MySQL 8.0), `jetmon` (single binary), `veriflier`, `statsd` (Graphite)

### View Logs
```bash
docker compose logs -f jetmon         # Follow Jetmon logs
docker compose logs -f veriflier      # Follow Veriflier logs
```

### Monitor Activity
```bash
docker compose exec jetmon cat stats/sitespersec
docker compose exec jetmon cat stats/sitesqueue
docker compose exec jetmon ps aux     # Single process — no worker tree
```

## Test Database Setup

The Docker entrypoint automatically runs `./jetmon2 migrate` on startup. For manual testing, connect to MySQL:

```bash
docker compose exec mysqldb mysql -u root -p123456 jetmon_db
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
docker compose exec jetmon ./jetmon2 reload   # Sends SIGHUP via PID file
# Or manually:
docker compose exec jetmon kill -HUP <pid>
```

### Graceful Shutdown / Drain
```bash
docker compose exec jetmon ./jetmon2 drain    # Sends SIGINT via PID file
# Or: docker compose stop jetmon
```

### Validate Config
```bash
docker compose exec jetmon ./jetmon2 validate-config
```

### Veriflier Connectivity
```bash
docker compose exec jetmon curl http://veriflier:7803/status
# Should return: {"hostname":"...","version":"...","status":"ok"}
```

### Operator Dashboard
- Open http://localhost:8080 in a browser after starting Docker services.

### Audit Log
```bash
docker compose exec jetmon ./jetmon2 audit --blog-id 1 --since 1h
```

### Memory Monitoring
```bash
docker compose exec jetmon bash -c 'while true; do ps aux --sort=-%mem | head -5; sleep 5; done'
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
| No sites being checked | Verify `BUCKET_TOTAL/TARGET` and that `monitor_active = 1` in DB |
| Veriflier connection fails | `docker compose ps veriflier`, check auth tokens match |
| StatsD not receiving | `docker compose exec jetmon ping statsd`, check for UDP errors |
| Migration fails | Check MySQL is up: `docker compose ps mysqldb` |

## Cleanup

```bash
docker compose down -v                # Remove volumes
rm -f config/config.json config/db-config.conf
rm -rf logs/*.log stats/*
```

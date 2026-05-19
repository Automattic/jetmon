# Running Tests

Jetmon 2 has a Go test suite and a Docker development environment.

## Automated Tests

```bash
make test          # go test ./...
make test-race     # go test -race ./...
make lint          # go vet ./...
```

## Docker Environment

```bash
cd docker
docker compose up -d
docker compose down
docker compose down -v
```

The local Compose stack includes MariaDB 11.4, `jetmon`, `veriflier`,
`api-fixture`, Mailpit, and StatsD/Graphite.

## Useful Checks

```bash
docker compose ps
docker compose exec jetmon ./jetmon2 validate-config
docker compose exec jetmon ./jetmon2 status
docker compose exec jetmon curl -fsS http://veriflier:7803/v2/status
docker compose exec jetmon cat stats/sitespersec
docker compose exec jetmon cat stats/sitesqueue
docker compose exec jetmon cat stats/totals
```

The operator dashboard is exposed on http://localhost:8080 by default. The
internal API is exposed on http://localhost:8090 by default.

## Local Test Sites

Prefer the Docker-local `api-fixture` service for deterministic internal-only
checks:

```sql
INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status)
VALUES
    (1, 0, 'http://api-fixture:8091/ok', 1, 1),
    (2, 0, 'http://api-fixture:8091/status/500', 1, 1),
    (3, 0, 'http://api-fixture:8091/slow?delay=5s', 1, 1),
    (4, 0, 'http://api-fixture:8091/redirect', 1, 1);
```

Only use public sites when the test explicitly requires external network
behavior.

## Runtime Scenarios

```bash
docker compose exec jetmon ./jetmon2 reload
docker compose exec jetmon ./jetmon2 drain
docker compose logs -f jetmon
docker compose logs -f veriflier
docker compose exec mysqldb mariadb -u jetmon -pjetmon_dev_password jetmon_db
```

Set `DB_UPDATES_ENABLE=true` only in local or explicitly approved test
environments.

## Profiling

The debug listener binds to localhost in the running process and defaults to
port 6060 when enabled. In Docker Compose, query it from inside the `jetmon`
container unless a local override explicitly publishes the debug port.

```bash
docker compose exec jetmon curl http://127.0.0.1:6060/debug/pprof/goroutine?debug=1
docker compose exec jetmon curl http://127.0.0.1:6060/debug/pprof/heap > heap.prof
go tool pprof heap.prof
```

Expose the debug port only in controlled development or lab environments.

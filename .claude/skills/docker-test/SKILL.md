---
name: docker-test
description: Run, debug, and test Jetmon 2 using the Docker development environment
allowed-tools: Bash(docker*), Bash(cd docker*), Read, Glob, Grep
---

# Docker Testing Skill

Use this skill for running, debugging, and testing Jetmon 2 in the Docker
development environment.

## Docker Services

| Service | Default Host Port | Purpose |
|---------|-------------------|---------|
| `mysqldb` | 3307 | MariaDB 11.4 database |
| `jetmon` | 8080 / 8090 | Operator dashboard / internal API |
| `veriflier` | 7803 | Veriflier JSON-over-HTTP service |
| `api-fixture` | 18091 / 18443 | Deterministic local test site and webhook receiver |
| `mailpit` | 8025 | Local email sink |
| `statsd` | 8125 / 8088 | StatsD and Graphite UI |

## Common Commands

```bash
cd docker
docker compose up -d
docker compose up -d --build jetmon veriflier
docker compose ps
docker compose logs -f jetmon
docker compose logs -f veriflier
docker compose down
docker compose down -v
```

## Health Checks

```bash
docker compose exec jetmon ./jetmon2 validate-config
docker compose exec jetmon ./jetmon2 status
docker compose exec jetmon curl -fsS http://veriflier:7803/v2/status
curl -fsS http://localhost:8080/
curl -fsS http://localhost:8090/api/v1/health
```

## Local Test Data

Use Docker-local fixtures unless a test explicitly needs external network
behavior:

```bash
docker compose exec mysqldb mariadb -u jetmon -pjetmon_dev_password jetmon_db
```

```sql
INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status)
VALUES
    (1, 0, 'http://api-fixture:8091/ok', 1, 1),
    (2, 0, 'http://api-fixture:8091/status/500', 1, 1),
    (3, 0, 'http://api-fixture:8091/slow?delay=5s', 1, 1),
    (4, 0, 'http://api-fixture:8091/redirect', 1, 1);
```

Set `DB_UPDATES_ENABLE=true` only in local or explicitly approved test
environments.

## Runtime Exercises

```bash
docker compose exec jetmon ./jetmon2 reload
docker compose exec jetmon ./jetmon2 drain
docker compose exec jetmon ./jetmon2 audit --blog-id 1 --since 1h
docker compose exec jetmon cat stats/sitespersec
docker compose exec jetmon cat stats/sitesqueue
docker compose exec jetmon cat stats/totals
```

## Metrics

Open http://localhost:8088 for Graphite. The default local path is under:

```text
Metrics > stats > com > jetpack > jetmon > docker > jetmon
```

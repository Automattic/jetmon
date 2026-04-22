# Docker Test Environment

Run, debug, and test Jetmon 2 using the Docker development environment.

## Instructions

Help the user test Jetmon 2 in the Docker environment. Follow these steps:

### 1. Check Docker Status
First, check if the Docker environment is already running:
```bash
cd docker && docker compose ps
```

### 2. Start Services (if needed)
If services aren't running, start them:
```bash
cd docker && docker compose up -d
```

Wait a few seconds for services to initialize, then verify:
```bash
docker compose ps
```

### 3. Ask User What They Want to Test

Present these options:
- **View logs** - Watch Jetmon or Veriflier logs in real-time
- **Operator dashboard** - Open http://localhost:8080 in a browser
- **Test with sample sites** - Insert test URLs into database
- **Test configuration reload** - Send SIGHUP to reload config
- **Test graceful drain** - Verify drain/shutdown behaviour
- **Test Veriflier connectivity** - Check Veriflier is responding
- **View audit log** - Query the audit log for a specific blog
- **View metrics** - Check StatsD/Graphite dashboard

### 4. Execute Based on Selection

**View logs:**
```bash
docker compose logs -f jetmon
```

**Check process and stats:**
```bash
docker compose exec jetmon ps aux
docker compose exec jetmon cat stats/sitespersec
docker compose exec jetmon cat stats/sitesqueue
```

**Test with sample sites:**
First check if table exists and has data:
```bash
docker compose exec mysqldb mysql -u root -p123456 jetmon_db -e "SELECT COUNT(*) as count FROM jetpack_monitor_sites;" 2>/dev/null
```

If empty or table doesn't exist, offer to create test data per `running-tests.md`.

**Test configuration reload:**
```bash
docker compose exec jetmon ./jetmon2 reload
```

**Test drain/graceful shutdown:**
```bash
docker compose exec jetmon ./jetmon2 drain
```

**Test Veriflier connectivity:**
```bash
docker compose exec jetmon curl http://veriflier:7803/status
```

**View audit log:**
```bash
docker compose exec jetmon ./jetmon2 audit --blog-id 1 --since 1h
```

**View metrics:**
Tell user to open http://localhost:8088 and navigate to `Metrics > stats > com > jetpack > jetmon > docker > jetmon`

### 5. Cleanup (if requested)
```bash
docker compose down
```

Or to fully reset with fresh database:
```bash
docker compose down -v
```

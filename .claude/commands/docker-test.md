# Docker Test Environment

Run, debug, and test Jetmon using the Docker development environment.

## Instructions

Help the user test Jetmon in the Docker environment. Follow these steps:

### 1. Check Docker Status
First, check if the Docker environment is already running:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose ps
```

### 2. Start Services (if needed)
If services aren't running, start them:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose up -d
```

Wait a few seconds for services to initialize, then verify:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose ps
```

### 3. Ask User What They Want to Test

Present these options:
- **View logs** - Watch Jetmon or Veriflier logs in real-time
- **Check worker status** - See worker activity and stats
- **Test with sample sites** - Insert test URLs into database
- **Test configuration reload** - Send SIGHUP to master process
- **Test graceful shutdown** - Verify shutdown behavior
- **Test Veriflier connectivity** - Check Veriflier is responding
- **View metrics** - Check StatsD/Graphite dashboard

### 4. Execute Based on Selection

**View logs:**
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose logs -f jetmon
```

**Check worker status:**
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon ps auxf
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon cat stats/sitespersec
```

**Test with sample sites:**
First check if table exists and has data:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec mysqldb mysql -u root -p123456 jetmon_db -e "SELECT COUNT(*) as count FROM jetpack_monitor_sites;" 2>/dev/null
```

If empty or table doesn't exist, offer to create test data per `running-tests.md`.

**Test configuration reload:**
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon sh -c 'kill -HUP $(pgrep -f "node lib/jetmon.js" | head -1)'
```

**Test Veriflier connectivity:**
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon curl -k https://veriflier:7801/get/status
```

**View metrics:**
Tell user to open http://localhost:8088 and navigate to `Metrics > stats > com > jetpack > jetmon > docker > jetmon`

### 5. Cleanup (if requested)
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose down
```

Or to fully reset with fresh database:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose down -v
```

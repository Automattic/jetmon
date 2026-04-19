# Debug Memory Issues

Debug memory growth and goroutine leaks in the Jetmon 2 Go binary.

## Instructions

Help the user diagnose memory problems in Jetmon 2. Unlike the old Node.js/worker architecture,
Jetmon 2 is a single Go binary. Memory pressure does not cause worker crashes — instead the
orchestrator drains the goroutine pool when RSS exceeds `WORKER_MAX_MEM_MB`.

### 1. Check Current Memory Status

```bash
cd docker && docker compose exec jetmon ps aux
```

Check memory config:
```bash
docker compose exec jetmon cat config/config.json | grep -E '(WORKER_MAX_MEM|NUM_WORKERS)'
```

### 2. Use pprof for Deep Analysis

The operator dashboard exposes pprof endpoints at http://localhost:8080/debug/pprof/

```bash
# Count goroutines
curl http://localhost:8080/debug/pprof/goroutine?debug=1 | grep -c "^goroutine"

# Heap profile
curl http://localhost:8080/debug/pprof/heap > heap.prof
go tool pprof heap.prof
```

### 3. Monitor Memory Over Time

```bash
docker compose exec jetmon bash -c 'while true; do ps -o pid,rss,vsz,comm -p $(pgrep jetmon2); sleep 10; done'
```

Enable detailed StatsD metrics by setting `STATSD_SEND_MEM_USAGE: true` in `config/config.json`,
then reload config: `docker compose exec jetmon ./jetmon2 reload`

### 4. Check Retry Queue Size

Large retry queues indicate many sites are down and being tracked. This is expected behaviour.

```bash
curl http://localhost:8080/api/state | python3 -m json.tool
```

Look at `RetryQueueSize`.

### 5. Common Issues

| Symptom | Likely Cause | Fix |
|---------|--------------|-----|
| Goroutine count grows | Context not cancelled on shutdown | Verify `orch.Stop()` called |
| Memory never drops | Pool drain not triggered | Check `WORKER_MAX_MEM_MB` value |
| Retry queue unbounded | Veriflier unreachable | Check veriflier connectivity |
| High allocations | Keyword-check body reads | Reduce `NUM_WORKERS` |

### 6. Restore Normal Settings

After testing, remind user to restore:
- `STATSD_SEND_MEM_USAGE`: false (avoid extra StatsD traffic in production)

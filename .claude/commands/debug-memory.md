# Debug Memory Issues

Debug memory issues in Jetmon workers and identify leaks.

## Instructions

Help the user diagnose memory problems in Jetmon workers. Memory leaks are a known pitfall because workers are long-running processes.

### 1. Check Current Memory Status

First, see current memory usage of all Jetmon processes:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon ps aux --sort=-%mem | grep -E '(node|PID)' | head -20
```

Check worker memory limits in config:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon cat config/config.json | grep -E '(WORKER_MAX_MEM|WORKER_MAX_CHECK)'
```

### 2. Monitor Memory Over Time

Watch memory growth in real-time:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon bash -c 'while true; do echo "=== $(date) ==="; ps aux --sort=-%mem | grep node | head -10; sleep 10; done'
```

Let this run for a few minutes to observe trends. Look for:
- Workers steadily increasing memory without recycling
- Workers approaching or exceeding `WORKER_MAX_MEM_MB` (default 53MB)
- Memory not dropping after worker recycle

### 3. Check Worker Recycling

Verify workers are being recycled when hitting limits:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose logs jetmon 2>&1 | grep -E '(memory|recycle|spawn|die|limit)' | tail -30
```

### 4. Force Aggressive Recycling (Testing)

To test worker recycling behavior, temporarily set low limits:

```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon bash -c 'cat > /tmp/test-config.json << EOF
{
    "WORKER_MAX_CHECKS": 50,
    "WORKER_MAX_MEM_MB": 20
}
EOF
cat /tmp/test-config.json'
```

Tell the user to manually update `config/config.json` with these values, then reload:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon sh -c 'kill -HUP $(pgrep -f "node lib/jetmon.js" | head -1)'
```

Watch for recycling:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose logs -f jetmon 2>&1 | grep -E '(spawn|die|recycle|memory|limit)'
```

### 5. Check for Known Memory Issues

**Retry queue growth:** If retry queues aren't being processed, they can grow unbounded:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose logs jetmon 2>&1 | grep -i retry | tail -20
```

**StatsD buffer:** Check if metrics buffer is growing:
```bash
cd /Users/rdcoll/Code/a8c/jetmon/docker && docker compose exec jetmon bash -c 'cat stats/* 2>/dev/null'
```

### 6. Analyze with Node.js Tools (Advanced)

If deeper analysis is needed, suggest:

1. **Heap snapshots:** Would require code changes to expose `v8.writeHeapSnapshot()`
2. **--inspect flag:** Could attach Chrome DevTools, but requires exposing debug port
3. **Process stats:** Check `/proc/<pid>/status` for detailed memory breakdown

### 7. Common Memory Issues in Jetmon

| Symptom | Likely Cause | Fix |
|---------|--------------|-----|
| Workers never recycle | `WORKER_MAX_MEM_MB` set to 0 or very high | Set reasonable limit (53MB default) |
| Memory spikes during rounds | Too many concurrent checks | Reduce `NUM_TO_PROCESS` |
| Gradual leak over hours | Retry queue not draining | Check Veriflier connectivity |
| Sudden OOM | Node.js version regression | Test with previous Node version |

### 8. Restore Normal Settings

Remind user to restore normal config values after testing:
- `WORKER_MAX_MEM_MB`: 53
- `WORKER_MAX_CHECKS`: 10000

---
name: debug-memory
description: Debug memory and goroutine issues in Jetmon 2
allowed-tools: Bash(docker*), Bash(ps*), Bash(curl*), Bash(go*), Read, Glob, Grep
---

# Debug Memory Issues

Use this skill to investigate memory growth and goroutine leaks in the Jetmon 2 Go binary.

## Usage

- `/debug-memory` - Interactive memory debugging session
- `/debug-memory monitor` - Start continuous memory monitoring
- `/debug-memory analyze` - Analyze current memory state

## Memory Architecture

Jetmon 2 is a single binary with an auto-scaling goroutine pool. Memory pressure does
not cause crashes; the orchestrator drains the pool when memory exceeds `WORKER_MAX_MEM_MB`.

Key memory consumers:
- Goroutine pool (each goroutine ~8KB stack, grows on demand)
- Retry queue (in-memory map, bounded by number of monitored sites)
- WPCOM notification queue (bounded at 1000 entries)
- HTTP response bodies (read up to 1MB for keyword checks)

## Monitoring Commands

### Docker Environment

```bash
# Real-time process memory (single Go process)
docker compose exec jetmon bash -c 'while true; do ps -o pid,rss,vsz,comm -p $(pgrep jetmon2); sleep 5; done'

# Goroutine count and heap via pprof
curl http://localhost:8080/debug/pprof/goroutine?debug=1 | head -30
```

### pprof Profiles (via Operator Dashboard)

The dashboard exposes `/debug/pprof/` endpoints:

```bash
# Heap profile — shows allocations
curl http://localhost:8080/debug/pprof/heap > heap.prof
go tool pprof heap.prof

# Goroutine profile — detect leaks
curl http://localhost:8080/debug/pprof/goroutine > goroutine.prof
go tool pprof goroutine.prof

# CPU profile (30s)
curl "http://localhost:8080/debug/pprof/profile?seconds=30" > cpu.prof
go tool pprof cpu.prof
```

### Metrics

Check Graphite (http://localhost:8088):
- `stats.goroutines.*` — goroutine count over time
- `stats.memory.*` — heap and RSS metrics (requires `STATSD_SEND_MEM_USAGE: true`)

## Common Memory Issues

### 1. Goroutine Leak

**Symptom:** Goroutine count grows unboundedly.

**Diagnosis:**
```bash
curl http://localhost:8080/debug/pprof/goroutine?debug=1 | grep -c "^goroutine"
```

**Cause:** A goroutine is blocked on a channel that is never read, or a context is never cancelled.

**Solution:** Check that all goroutines started in `orchestrator.go` and `pool.go` exit
when `ctx.Done()` fires. Ensure `orch.Stop()` is called on shutdown.

### 2. Retry Queue Growth

**Symptom:** Memory grows during extended site outages.

**Diagnosis:**
```bash
docker compose exec jetmon ./jetmon2 status
# Check RetryQueueSize in API response
curl http://localhost:8080/api/state | python3 -m json.tool
```

**Cause:** Retry queue entries accumulate when verifliers are unreachable.

**Solution:** Check veriflier connectivity. Retry queue is expected to hold state for down
sites — it is not a leak, but a design feature. If it grows without bound with no site
outages, check `retryQueue.clear()` is being called in `handleRecovery`.

### 3. HTTP Response Body Accumulation

**Symptom:** Memory spikes correlate with keyword-check sites.

**Cause:** Keyword checks read up to 1MB of response body per check. With many such sites
and a large pool, this can total significant memory.

**Solution:** Reduce `NUM_WORKERS` if memory is constrained. The 1MB cap is hard-coded in
`internal/checker/checker.go`.

## Configuration Tuning

```json
{
    "NUM_WORKERS": 40,
    "WORKER_MAX_MEM_MB": 200,
    "STATSD_SEND_MEM_USAGE": true
}
```

- `NUM_WORKERS`: Upper bound on pool goroutines
- `WORKER_MAX_MEM_MB`: Triggers pool drain when Go RSS exceeds this (MB)
- `STATSD_SEND_MEM_USAGE`: Emit `runtime.MemStats` to StatsD each interval

## Key Files for Investigation

| File | Memory-Related Code |
|------|---------------------|
| `internal/checker/pool.go` | Pool scaling, goroutine lifecycle |
| `internal/orchestrator/orchestrator.go` | Round loop, retry queue, pool drain |
| `internal/orchestrator/retry.go` | Retry queue implementation |
| `internal/wpcom/client.go` | Notification queue (bounded at 1000) |

## Checklist for Memory Issues

- [ ] Check goroutine count via pprof (is it growing?)
- [ ] Check retry queue size via `/api/state`
- [ ] Enable `STATSD_SEND_MEM_USAGE` and observe Graphite
- [ ] Capture heap profile before and after a round
- [ ] Verify `orch.Stop()` fully drains the pool on shutdown
- [ ] Check for unbounded channel accumulation in pool.go

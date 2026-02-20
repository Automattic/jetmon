---
name: debug-memory
description: Debug memory issues in Jetmon workers and identify leaks
allowed-tools: Bash(docker*), Bash(ps*), Bash(top*), Bash(node*), Read, Glob, Grep
---

# Debug Memory Issues

Use this skill to investigate memory problems in Jetmon workers, identify leaks, and optimize memory usage.

## Usage

- `/debug-memory` - Interactive memory debugging session
- `/debug-memory monitor` - Start continuous memory monitoring
- `/debug-memory analyze` - Analyze current memory state

## Memory Architecture

### Worker Memory Limits

| Setting | Default | Purpose |
|---------|---------|---------|
| `WORKER_MAX_MEM_MB` | 53 | Memory limit before worker recycles |
| `WORKER_MAX_CHECKS` | 10,000 | Check count before worker recycles |

Workers are designed to be disposable. When hitting limits, they stop accepting work and exit gracefully.

### Memory Flow

```
Worker Process
├── Node.js Heap (V8)
│   ├── HTTP check callbacks
│   ├── Retry queues (arrToRetry)
│   └── Active checks (arrCheck)
├── Native Addon (C++)
│   └── HTTP_Checker instances
└── Buffers (TCP/SSL)
```

## Monitoring Commands

### Docker Environment

```bash
# Real-time memory monitoring
docker compose exec jetmon bash -c 'while true; do ps aux --sort=-%mem | head -15; sleep 5; done'

# Memory usage by process
docker compose exec jetmon ps aux --sort=-%mem

# Specific worker memory
docker compose exec jetmon bash -c 'ps -o pid,rss,vsz,comm | grep jetmon'
```

### Process Details

```bash
# View process tree
docker compose exec jetmon ps auxf

# Memory maps (detailed)
docker compose exec jetmon bash -c 'cat /proc/$(pgrep -f jetmon-master)/status | grep -E "Vm|Rss"'
```

### StatsD Metrics

Check Graphite (http://localhost:8088) for:
- `stats.workers.*.memory` - Per-worker memory usage
- `stats.workers.recycle.count` - Worker recycling frequency
- `stats.workers.free.count` - Available workers

## Common Memory Issues

### 1. Retry Queue Growth

**Symptom:** Memory grows steadily, especially during site outages.

**Diagnosis:**
```bash
docker compose exec jetmon cat stats/sitesqueue
```

**Cause:** Large numbers of sites in retry queue (`arrToRetry`).

**Solution:** Check retry queue flush logic. Ensure retries are processed, not accumulated.

### 2. Native Addon Leak

**Symptom:** Memory grows even with low check counts.

**Diagnosis:**
```bash
# Enable debug mode in http_checker.cpp
#define DEBUG_MODE 1
```

Watch for:
- Unfreed buffers
- Socket descriptor leaks
- SSL context accumulation

**Solution:** Review C++ destructor cleanup in `HTTP_Checker::~HTTP_Checker()`.

### 3. Event Loop Blocking

**Symptom:** Workers become unresponsive, memory spikes.

**Diagnosis:**
```bash
docker compose exec jetmon node --trace-warnings lib/jetmon.js
```

**Solution:** Ensure async operations complete and callbacks fire.

### 4. DNS Resolution Caching

**Symptom:** Memory grows with unique domains checked.

**Diagnosis:** Check if `USE_GETADDRINFO` is enabled in http_checker.cpp.

**Solution:** `getaddrinfo` uses more memory than `gethostbyname`. Consider trade-offs.

## Memory Profiling

### Node.js Heap Snapshot

```javascript
// Add to lib/httpcheck.js for debugging
const v8 = require('v8');
const fs = require('fs');

// Trigger heap snapshot
function dumpHeap() {
    const filename = `/tmp/heap-${process.pid}-${Date.now()}.heapsnapshot`;
    const stream = fs.createWriteStream(filename);
    v8.writeHeapSnapshot(filename);
    console.log('Heap snapshot written to:', filename);
}

// Call when memory is high
if (process.memoryUsage().rss > 50 * 1024 * 1024) {
    dumpHeap();
}
```

### Memory Usage Logging

Add to worker process:

```javascript
setInterval(function() {
    const mem = process.memoryUsage();
    logger.debug('Memory: RSS=' + Math.round(mem.rss / 1024 / 1024) + 'MB, ' +
                 'Heap=' + Math.round(mem.heapUsed / 1024 / 1024) + 'MB');
}, 30000);
```

## Reducing Memory Usage

### Configuration Tuning

```json
{
    "NUM_WORKERS": 40,          // Reduce from 60 if memory constrained
    "NUM_TO_PROCESS": 30,       // Reduce parallel checks per worker
    "WORKER_MAX_MEM_MB": 40,    // Lower threshold for faster recycling
    "WORKER_MAX_CHECKS": 5000   // Recycle more frequently
}
```

### Code Patterns

**DO:**
```javascript
// Release references when done
arrCheck.splice(index, 1);  // Remove processed items

// Use callbacks, don't hold references
checker.http_check(url, port, index, function(result) {
    // Process result immediately
    sendResult(result);
    // Callback goes out of scope
});
```

**DON'T:**
```javascript
// Accumulate data without bounds
allResults.push(result);  // Unbounded growth

// Hold references longer than needed
var savedChecker = checker;  // Prevents GC
```

## Testing Memory Fixes

### Set Low Limits

```json
{
    "WORKER_MAX_MEM_MB": 30,
    "WORKER_MAX_CHECKS": 100
}
```

### Monitor Recycling

```bash
docker compose logs -f jetmon | grep -E "(spawn|die|recycle|memory)"
```

### Extended Run Test

```bash
# Run for extended period, monitor memory growth
docker compose up -d jetmon
watch -n 5 'docker compose exec jetmon ps aux --sort=-%mem | head -10'
```

## Key Files for Memory Investigation

| File | Memory-Related Code |
|------|---------------------|
| `lib/httpcheck.js` | Worker arrays: `arrCheck`, `arrToRetry` |
| `lib/jetmon.js` | Master arrays: `arrWorkers`, `gCountSuccess` |
| `src/http_checker.cpp` | Buffer allocation, SSL contexts |
| `lib/config.js` | Memory limit settings |

## Checklist for Memory Issues

- [ ] Check worker recycling frequency in metrics
- [ ] Monitor retry queue size (`stats/sitesqueue`)
- [ ] Review recent code changes affecting arrays
- [ ] Verify C++ cleanup in destructor
- [ ] Test with reduced memory limits
- [ ] Check for unclosed connections/sockets
- [ ] Review setTimeout/setInterval cleanup
- [ ] Confirm process.send() callbacks complete

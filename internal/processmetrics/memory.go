package processmetrics

import (
	"fmt"
	"math"
	"os"
	"runtime"
	runtimemetrics "runtime/metrics"
	"strconv"
	"strings"
)

const bytesPerMB = 1024 * 1024

// MemorySnapshot is a compact local process memory sample.
type MemorySnapshot struct {
	RSSMemMB       int
	GoSysMemMB     int
	HeapAllocMemMB int

	RuntimeGoroutines         int
	RuntimeGoroutinesRunnable int
	RuntimeGoroutinesRunning  int
	RuntimeGoroutinesWaiting  int
	RuntimeGoroutinesNotInGo  int
	RuntimeGoroutinesCreated  uint64
	RuntimeThreads            int
}

// CurrentMemory returns a single memory sample suitable for dashboards and
// metrics. RSS is best-effort because it depends on Linux procfs availability.
func CurrentMemory() MemorySnapshot {
	mem := currentRuntimeMemory()
	addRuntimeSchedulerMetrics(&mem)
	mem.RSSMemMB = rssMemMB()
	return mem
}

func currentRuntimeMemory() MemorySnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return MemorySnapshot{
		GoSysMemMB:     int(ms.Sys / bytesPerMB),
		HeapAllocMemMB: int(ms.HeapAlloc / bytesPerMB),
	}
}

func addRuntimeSchedulerMetrics(snapshot *MemorySnapshot) {
	if snapshot == nil {
		return
	}
	samples := []runtimemetrics.Sample{
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/sched/goroutines/runnable:goroutines"},
		{Name: "/sched/goroutines/running:goroutines"},
		{Name: "/sched/goroutines/waiting:goroutines"},
		{Name: "/sched/goroutines/not-in-go:goroutines"},
		{Name: "/sched/goroutines-created:goroutines"},
		{Name: "/sched/threads/total:threads"},
	}
	runtimemetrics.Read(samples)
	snapshot.RuntimeGoroutines = metricUint64ToInt(samples[0].Value.Uint64())
	snapshot.RuntimeGoroutinesRunnable = metricUint64ToInt(samples[1].Value.Uint64())
	snapshot.RuntimeGoroutinesRunning = metricUint64ToInt(samples[2].Value.Uint64())
	snapshot.RuntimeGoroutinesWaiting = metricUint64ToInt(samples[3].Value.Uint64())
	snapshot.RuntimeGoroutinesNotInGo = metricUint64ToInt(samples[4].Value.Uint64())
	snapshot.RuntimeGoroutinesCreated = samples[5].Value.Uint64()
	snapshot.RuntimeThreads = metricUint64ToInt(samples[6].Value.Uint64())
}

func metricUint64ToInt(v uint64) int {
	if v > uint64(math.MaxInt) {
		return math.MaxInt
	}
	return int(v)
}

// rssMemMB returns this process' resident set size in MiB when the operating
// system exposes it. A zero return means RSS could not be collected.
func rssMemMB() int {
	rssMB, err := rssMemMBFromStatm("/proc/self/statm", os.Getpagesize())
	if err != nil {
		return 0
	}
	return rssMB
}

// rssMemMBFromStatm parses a Linux procfs statm file and converts resident
// pages to MiB.
func rssMemMBFromStatm(path string, pageSize int) (int, error) {
	if pageSize <= 0 {
		return 0, fmt.Errorf("invalid page size %d", pageSize)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0, fmt.Errorf("statm %s has %d fields, want at least 2", path, len(fields))
	}
	residentPages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse resident pages: %w", err)
	}
	return int((residentPages * uint64(pageSize)) / bytesPerMB), nil
}

package processmetrics

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const bytesPerMB = 1024 * 1024

// MemorySnapshot is a compact local process memory sample.
type MemorySnapshot struct {
	RSSMemMB       int
	GoSysMemMB     int
	HeapAllocMemMB int
}

// CurrentMemory returns a single memory sample suitable for dashboards and
// metrics. RSS is best-effort because it depends on Linux procfs availability.
func CurrentMemory() MemorySnapshot {
	mem := currentRuntimeMemory()
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

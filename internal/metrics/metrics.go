package metrics

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Automattic/jetmon/internal/processmetrics"
)

// Client sends StatsD metrics via UDP and writes stats files.
type Client struct {
	prefix string
	conn   net.Conn
	mu     sync.Mutex
}

// StatsFilesSnapshot is the legacy text-file status surface written under
// stats/. Keep the field names aligned with v1's file labels.
type StatsFilesSnapshot struct {
	SitesPerSec int
	QueueSize   int
	Working     int
	Waiting     int
	Halting     int
	Error       int
	Offline     int
	Success     int
	Total       int
}

type LegacyStatsFiles struct {
	SitesPerSec string
	SitesQueue  string
	Totals      string
}

var global *Client

var statsFilesState = struct {
	sync.RWMutex
	snapshot  StatsFilesSnapshot
	updatedAt time.Time
	available bool
}{}

const (
	EnvStatsDAddr = "STATSD_ADDR"
)

// AddrFromEnv returns the configured StatsD address. An explicitly empty
// STATSD_ADDR disables StatsD; an unset STATSD_ADDR uses defaultAddr.
func AddrFromEnv(defaultAddr string) string {
	if addr, ok := os.LookupEnv(EnvStatsDAddr); ok {
		return strings.TrimSpace(addr)
	}
	return strings.TrimSpace(defaultAddr)
}

// InitFromEnv initializes the global StatsD client from STATSD_ADDR or a
// caller-provided default. It returns enabled=false when StatsD is disabled.
func InitFromEnv(hostPath, defaultAddr string) (addr string, enabled bool, err error) {
	addr = AddrFromEnv(defaultAddr)
	if addr == "" {
		return "", false, nil
	}
	return addr, true, Init(addr, hostPath)
}

// Init creates the global StatsD client.
// host:port is the StatsD server address (e.g. "statsd:8125").
// hostPath is used to build the metric prefix.
func Init(addr, hostPath string) error {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return fmt.Errorf("statsd dial %s: %w", addr, err)
	}
	global = &Client{
		prefix: "com.jetpack.jetmon." + MetricHostPath(hostPath),
		conn:   conn,
	}
	return nil
}

// MetricHostPath returns the host path segment used in the StatsD prefix.
func MetricHostPath(path string) string {
	if hostPath := sanitizeMetricPath(path); hostPath != "" {
		return hostPath
	}
	return "unknown"
}

// Client returns the global metrics client. Panics if Init was not called.
func Global() *Client {
	return global
}

// Increment sends a counter metric.
func (c *Client) Increment(stat string, value int) {
	c.send(fmt.Sprintf("%s.%s:%d|c", c.prefix, stat, value))
}

// Gauge sends a gauge metric.
func (c *Client) Gauge(stat string, value int) {
	c.send(fmt.Sprintf("%s.%s:%d|g", c.prefix, stat, value))
}

// Timing sends a timer metric in milliseconds.
func (c *Client) Timing(stat string, d time.Duration) {
	c.send(fmt.Sprintf("%s.%s:%d|ms", c.prefix, stat, d.Milliseconds()))
}

func (c *Client) send(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = fmt.Fprintln(c.conn, msg)
}

// EmitMemStats emits low-overhead local process resource gauges. The method
// keeps its historical name because it is controlled by the existing
// STATSD_SEND_MEM_USAGE config key.
func (c *Client) EmitMemStats() {
	mem := processmetrics.CurrentMemory()
	rssMB := mem.RSSMemMB
	goSysMB := mem.GoSysMemMB
	if rssMB <= 0 {
		rssMB = goSysMB
	}
	c.Gauge("process.rss_mb", rssMB)
	c.Gauge("process.go_sys_mem_mb", goSysMB)
	c.Gauge("process.heap_alloc_mb", mem.HeapAllocMemMB)
	c.Gauge("process.open_fds", mem.OpenFDs)
	if mem.MaxFDs > 0 {
		c.Gauge("process.max_fds", mem.MaxFDs)
		c.Gauge("process.fd_utilization_pct", int(float64(mem.OpenFDs)*100/float64(mem.MaxFDs)))
	}
	c.Gauge("runtime.goroutines.count", mem.RuntimeGoroutines)
	c.Gauge("runtime.goroutines.runnable", mem.RuntimeGoroutinesRunnable)
	c.Gauge("runtime.goroutines.running", mem.RuntimeGoroutinesRunning)
	c.Gauge("runtime.goroutines.waiting", mem.RuntimeGoroutinesWaiting)
	c.Gauge("runtime.goroutines.not_in_go", mem.RuntimeGoroutinesNotInGo)
	c.Gauge("runtime.goroutines.created_total", int(mem.RuntimeGoroutinesCreated))
	c.Gauge("runtime.threads.count", mem.RuntimeThreads)
}

// EmitDBStats emits a compact snapshot of one database pool. Current pool state
// is emitted as gauges. Cumulative sql.DB counters are also emitted as gauges
// with _total suffixes so downstream Graphite functions can derive rates
// without callers retaining previous values.
func (c *Client) EmitDBStats(prefix string, stats sql.DBStats) {
	prefix = strings.Trim(strings.TrimSpace(prefix), ".")
	if prefix == "" {
		prefix = "db.pool"
	}
	c.Gauge(prefix+".max_open_connections", stats.MaxOpenConnections)
	c.Gauge(prefix+".open_connections", stats.OpenConnections)
	c.Gauge(prefix+".in_use", stats.InUse)
	c.Gauge(prefix+".idle", stats.Idle)
	c.Gauge(prefix+".wait_count_total", int(stats.WaitCount))
	c.Gauge(prefix+".wait_duration_ms_total", int(stats.WaitDuration.Milliseconds()))
	c.Gauge(prefix+".max_idle_closed_total", int(stats.MaxIdleClosed))
	c.Gauge(prefix+".max_idle_time_closed_total", int(stats.MaxIdleTimeClosed))
	c.Gauge(prefix+".max_lifetime_closed_total", int(stats.MaxLifetimeClosed))
}

// WriteStatsFiles writes sitespersec, sitesqueue, and totals to the stats/
// directory so existing monitoring and the README examples continue to work.
func WriteStatsFiles(snapshot StatsFilesSnapshot) {
	StoreStatsFilesSnapshot(snapshot)
	files := RenderLegacyStatsFiles(snapshot)
	writeFile("stats/sitespersec", files.SitesPerSec)
	writeFile("stats/sitesqueue", files.SitesQueue)
	writeFile("stats/totals", files.Totals)
}

// StoreStatsFilesSnapshot updates the in-memory copy used by API consumers.
func StoreStatsFilesSnapshot(snapshot StatsFilesSnapshot) {
	statsFilesState.Lock()
	defer statsFilesState.Unlock()
	statsFilesState.snapshot = snapshot
	statsFilesState.updatedAt = time.Now().UTC()
	statsFilesState.available = true
}

// LastStatsFilesSnapshot returns the most recent stats/ snapshot.
func LastStatsFilesSnapshot() (StatsFilesSnapshot, time.Time, bool) {
	statsFilesState.RLock()
	defer statsFilesState.RUnlock()
	return statsFilesState.snapshot, statsFilesState.updatedAt, statsFilesState.available
}

// RenderLegacyStatsFiles returns the exact text written to v1-compatible stats/
// files. API handlers use this instead of reading the files back from disk.
func RenderLegacyStatsFiles(snapshot StatsFilesSnapshot) LegacyStatsFiles {
	return LegacyStatsFiles{
		SitesPerSec: fmt.Sprintf("sites per second: %d\n", snapshot.SitesPerSec),
		SitesQueue:  fmt.Sprintf("sites in queue: %d\n", snapshot.QueueSize),
		Totals: fmt.Sprintf(
			"working : %d\n"+
				"waiting : %d\n"+
				"halting : %d\n"+
				"error   : %d\n"+
				"offline : %d\n"+
				"success : %d\n"+
				"total   : %d\n",
			snapshot.Working,
			snapshot.Waiting,
			snapshot.Halting,
			snapshot.Error,
			snapshot.Offline,
			snapshot.Success,
			snapshot.Total,
		),
	}
}

func writeFile(path, content string) {
	_ = os.WriteFile(path, []byte(content), 0644)
}

func sanitize(s string) string {
	return strings.NewReplacer(".", "_", "-", "_").Replace(s)
}

func sanitizeMetricPath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, ".")
	if s == "" {
		return ""
	}

	var b strings.Builder
	lastDot := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_',
			r == '-':
			b.WriteRune(r)
			lastDot = false
		case r == '.':
			if b.Len() > 0 && !lastDot {
				b.WriteByte('.')
				lastDot = true
			}
		default:
			b.WriteByte('_')
			lastDot = false
		}
	}
	return strings.Trim(b.String(), ".")
}

package metrics

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Automattic/jetmon/internal/processmetrics"
)

// statsdSafePacketSize bounds the per-UDP-datagram payload the sender
// goroutine will pack into. Sized to stay under a typical 1500-byte MTU
// after IP+UDP overhead so the packet does not fragment on the network.
// StatsD line-protocol allows multiple metrics per packet separated by
// newlines; the sender opportunistically packs many metrics into one
// packet without ever delaying — see runSender.
const statsdSafePacketSize = 1400

// statsdChannelDepth is the producer→sender queue capacity. Sized large
// enough that bursts (e.g. a recovery wave emitting dozens of metrics in
// microseconds) never overflow. When the channel is full, dispatch falls
// back to a direct UDP send so the hot path never blocks.
const statsdChannelDepth = 1024

// Client sends StatsD metrics via UDP and writes stats files.
//
// Internally the client runs a dedicated sender goroutine that reads
// pre-formatted metric lines from a buffered channel and packs them into
// MTU-sized UDP datagrams. It flushes opportunistically — whenever the
// channel drains, regardless of how much is buffered — so individual
// metrics become visible to the StatsD server with the same effective
// latency as the original send-per-call implementation, while bursts of
// metrics get coalesced into far fewer syscalls.
type Client struct {
	prefix string
	conn   net.Conn
	mu     sync.Mutex // serializes conn writes (sender + fallback path)

	pending chan []byte
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
	c := &Client{
		prefix:  "com.jetpack.jetmon." + MetricHostPath(hostPath),
		conn:    conn,
		pending: make(chan []byte, statsdChannelDepth),
	}
	go c.runSender()
	global = c
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
	c.dispatch(buildMetricLine(c.prefix, stat, int64(value), 'c'))
}

// Gauge sends a gauge metric.
func (c *Client) Gauge(stat string, value int) {
	c.dispatch(buildMetricLine(c.prefix, stat, int64(value), 'g'))
}

// Timing sends a timer metric in milliseconds.
func (c *Client) Timing(stat string, d time.Duration) {
	c.dispatch(buildTimingLine(c.prefix, stat, d.Milliseconds()))
}

// dispatch pushes a pre-formatted metric line to the sender. If the channel
// is full (rare — would require a sustained backlog of statsdChannelDepth
// items), fall back to a direct UDP write so the hot path never blocks.
// Same "lose-metrics-rather-than-block" posture as the original
// implementation, just with a coalescer in front.
func (c *Client) dispatch(line []byte) {
	select {
	case c.pending <- line:
	default:
		c.writeUDP(line)
	}
}

// runSender packs metric lines from c.pending into MTU-sized UDP packets
// and flushes whenever the channel drains. The "flush on empty" semantics
// give the same observed per-metric latency as a direct-send-per-call
// implementation while reducing UDP syscalls by ~5–10x on bursts.
//
// The sender runs for the lifetime of the process. Jetmon has no clean
// metrics shutdown today — on process exit any unflushed buffer is lost,
// which matches the prior behavior.
func (c *Client) runSender() {
	buf := make([]byte, 0, statsdSafePacketSize+128)
	for {
		// Block waiting for the first metric in a new flush window.
		line, ok := <-c.pending
		if !ok {
			if len(buf) > 0 {
				c.writeUDP(buf)
			}
			return
		}
		buf = packMetricLine(buf, line, c.writeUDP)

		// Greedy drain: pack everything currently in the channel, then
		// flush whatever we've accumulated. The default branch is hit the
		// moment the channel empties, so a single late metric is sent in
		// the same packet as anything that arrived microseconds earlier
		// but cannot be delayed by anything that arrives later.
		draining := true
		for draining {
			select {
			case line, ok := <-c.pending:
				if !ok {
					if len(buf) > 0 {
						c.writeUDP(buf)
					}
					return
				}
				buf = packMetricLine(buf, line, c.writeUDP)
			default:
				if len(buf) > 0 {
					c.writeUDP(buf)
					buf = buf[:0]
				}
				draining = false
			}
		}
	}
}

// packMetricLine appends line to buf, flushing buf via flushFn first if
// adding line would exceed statsdSafePacketSize. line already includes its
// own trailing newline (see buildMetricLine / buildTimingLine) so multi-
// line packets are unambiguous and the dispatch fallback path's raw write
// produces the same wire format as the packed sender path.
func packMetricLine(buf, line []byte, flushFn func([]byte)) []byte {
	if len(buf)+len(line) > statsdSafePacketSize && len(buf) > 0 {
		flushFn(buf)
		buf = buf[:0]
	}
	buf = append(buf, line...)
	return buf
}

func (c *Client) writeUDP(p []byte) {
	c.mu.Lock()
	_, _ = c.conn.Write(p)
	c.mu.Unlock()
}

// buildMetricLine assembles "<prefix>.<stat>:<value>|<type>\n" without
// fmt.Sprintf so the per-metric hot path does not allocate a format
// scratch buffer or trigger reflection. The trailing newline makes each
// line a standalone valid StatsD packet, so the dispatch fallback can
// write it directly without extra framing.
func buildMetricLine(prefix, stat string, value int64, typ byte) []byte {
	b := make([]byte, 0, len(prefix)+1+len(stat)+1+22+1)
	b = append(b, prefix...)
	b = append(b, '.')
	b = append(b, stat...)
	b = append(b, ':')
	b = strconv.AppendInt(b, value, 10)
	b = append(b, '|', typ, '\n')
	return b
}

// buildTimingLine is the |ms variant of buildMetricLine.
func buildTimingLine(prefix, stat string, ms int64) []byte {
	b := make([]byte, 0, len(prefix)+1+len(stat)+1+24+1)
	b = append(b, prefix...)
	b = append(b, '.')
	b = append(b, stat...)
	b = append(b, ':')
	b = strconv.AppendInt(b, ms, 10)
	b = append(b, '|', 'm', 's', '\n')
	return b
}

// EmitMemStats emits low-overhead local process resource gauges. The method
// keeps its historical name for API compatibility even though it now includes
// file-descriptor and runtime scheduler pressure in addition to memory.
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

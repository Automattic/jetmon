package metrics

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client sends StatsD metrics via UDP and writes stats files.
type Client struct {
	prefix string
	conn   net.Conn
	mu     sync.Mutex
}

var global *Client

// Init creates the global StatsD client.
// host:port is the StatsD server address (e.g. "statsd:8125").
// hostname is used to build the metric prefix.
func Init(addr, hostname string) error {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return fmt.Errorf("statsd dial %s: %w", addr, err)
	}
	global = &Client{
		prefix: "com.jetpack.jetmon." + sanitize(hostname),
		conn:   conn,
	}
	return nil
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

// EmitMemStats emits legacy memory gauges. process.rss_mb is retained for
// StatsD compatibility, but the value is Go runtime Sys memory rather than OS
// resident set size.
func (c *Client) EmitMemStats() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	c.Gauge("process.rss_mb", int(ms.Sys/1024/1024))
	c.Gauge("process.heap_alloc_mb", int(ms.HeapAlloc/1024/1024))
}

// WriteStatsFiles writes sitespersec, sitesqueue, and totals to the stats/
// directory so existing monitoring and the README examples continue to work.
func WriteStatsFiles(sitesPerSec, queueSize, totalChecked int) {
	writeFile("stats/sitespersec", strconv.Itoa(sitesPerSec))
	writeFile("stats/sitesqueue", strconv.Itoa(queueSize))
	writeFile("stats/totals", strconv.Itoa(totalChecked))
}

func writeFile(path, content string) {
	_ = os.WriteFile(path, []byte(content+"\n"), 0644)
}

func sanitize(s string) string {
	return strings.NewReplacer(".", "_", "-", "_").Replace(s)
}

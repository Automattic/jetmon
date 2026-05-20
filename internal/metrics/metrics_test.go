package metrics

import (
	"bufio"
	"database/sql"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBuildMetricLineFormat(t *testing.T) {
	got := string(buildMetricLine("com.example", "checks.total", 42, 'c'))
	want := "com.example.checks.total:42|c\n"
	if got != want {
		t.Fatalf("buildMetricLine = %q, want %q", got, want)
	}
}

func TestBuildTimingLineFormat(t *testing.T) {
	got := string(buildTimingLine("com.example", "request.rtt", 1500))
	want := "com.example.request.rtt:1500|ms\n"
	if got != want {
		t.Fatalf("buildTimingLine = %q, want %q", got, want)
	}
}

func TestPackMetricLineUnderMTUAccumulates(t *testing.T) {
	var flushed [][]byte
	flushFn := func(b []byte) {
		flushed = append(flushed, append([]byte(nil), b...))
	}
	buf := make([]byte, 0, statsdSafePacketSize+128)
	for _, line := range []string{"a:1|c\n", "b:2|c\n", "c:3|c\n"} {
		buf = packMetricLine(buf, []byte(line), flushFn)
	}
	if len(flushed) != 0 {
		t.Fatalf("flushed %d times under MTU, want 0", len(flushed))
	}
	if string(buf) != "a:1|c\nb:2|c\nc:3|c\n" {
		t.Fatalf("buf = %q, want all three lines concatenated", string(buf))
	}
}

func TestPackMetricLineFlushesAtMTU(t *testing.T) {
	var flushed [][]byte
	flushFn := func(b []byte) {
		flushed = append(flushed, append([]byte(nil), b...))
	}
	buf := make([]byte, 0, statsdSafePacketSize+128)

	// One line under MTU.
	smallLine := []byte(strings.Repeat("a", 100) + "\n")
	// First call: fits, no flush.
	buf = packMetricLine(buf, smallLine, flushFn)
	// Repeat until just under MTU, then one more line should trigger flush.
	for len(buf)+len(smallLine) <= statsdSafePacketSize {
		buf = packMetricLine(buf, smallLine, flushFn)
	}
	if len(flushed) != 0 {
		t.Fatalf("flushed %d times before MTU, want 0", len(flushed))
	}
	// Next add must flush the accumulated buf and start fresh.
	buf = packMetricLine(buf, smallLine, flushFn)
	if len(flushed) != 1 {
		t.Fatalf("flushed %d times after crossing MTU, want 1", len(flushed))
	}
	if string(buf) != string(smallLine) {
		t.Fatalf("buf after flush = %q, want only the latest line", string(buf))
	}
}

// BenchmarkClientIncrement measures the per-call cost of Increment on the
// hot dispatch path (channel send). The number is dominated by the line
// build (alloc + AppendInt) plus the channel send; UDP write happens in a
// separate goroutine and does not block the producer.
func BenchmarkClientIncrement(b *testing.B) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("ListenPacket: %v", err)
	}
	b.Cleanup(func() { _ = pc.Close() })

	// Drain the listener so the sender doesn't block on a full kernel
	// buffer during the benchmark.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		buf := make([]byte, statsdSafePacketSize+256)
		for {
			_ = pc.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			_, _, _ = pc.ReadFrom(buf)
			select {
			case <-stop:
				return
			default:
			}
		}
	}()

	conn, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		b.Fatalf("Dial: %v", err)
	}
	b.Cleanup(func() { _ = conn.Close() })

	c := &Client{
		prefix:  "com.jetpack.jetmon.bench",
		conn:    conn,
		pending: make(chan []byte, statsdChannelDepth),
	}
	go c.runSender()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Increment("checks.total", 1)
	}
}

func TestClientWithSenderBatchesAndDelivers(t *testing.T) {
	// Spin up a real UDP listener so the full Init path (sender goroutine
	// included) is exercised.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	addr := pc.LocalAddr().String()
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	c := &Client{
		prefix:  "com.jetpack.jetmon.host_name",
		conn:    conn,
		pending: make(chan []byte, statsdChannelDepth),
	}
	go c.runSender()

	// Emit a burst — the sender should pack them into one or a few packets.
	c.Increment("a", 1)
	c.Increment("b", 2)
	c.Gauge("c", 3)
	c.Timing("d", 4*time.Millisecond)

	// Read packets until we've collected all four metrics or hit a timeout.
	collected := make(map[string]bool)
	deadline := time.Now().Add(time.Second)
	want := []string{
		"com.jetpack.jetmon.host_name.a:1|c",
		"com.jetpack.jetmon.host_name.b:2|c",
		"com.jetpack.jetmon.host_name.c:3|g",
		"com.jetpack.jetmon.host_name.d:4|ms",
	}
	for time.Now().Before(deadline) && len(collected) < len(want) {
		_ = pc.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		buf := make([]byte, statsdSafePacketSize+256)
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimRight(string(buf[:n]), "\n"), "\n") {
			collected[line] = true
		}
	}
	for _, w := range want {
		if !collected[w] {
			t.Errorf("missing metric %q (got %v)", w, collected)
		}
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"host.name", "host_name"},
		{"my-host", "my_host"},
		{"a.b-c.d", "a_b_c_d"},
	}
	for _, tt := range tests {
		if got := sanitize(tt.input); got != tt.want {
			t.Fatalf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGlobalNilBeforeInit(t *testing.T) {
	orig := global
	global = nil
	defer func() { global = orig }()

	if Global() != nil {
		t.Fatal("Global() = non-nil before Init, want nil")
	}
}

func TestAddrFromEnvDefaultWhenUnset(t *testing.T) {
	orig, hadOrig := os.LookupEnv(EnvStatsDAddr)
	t.Cleanup(func() {
		if hadOrig {
			_ = os.Setenv(EnvStatsDAddr, orig)
		} else {
			_ = os.Unsetenv(EnvStatsDAddr)
		}
	})
	_ = os.Unsetenv(EnvStatsDAddr)

	if got := AddrFromEnv("statsd:8125"); got != "statsd:8125" {
		t.Fatalf("AddrFromEnv(default) = %q, want statsd:8125", got)
	}
}

func TestAddrFromEnvOverride(t *testing.T) {
	t.Setenv(EnvStatsDAddr, " 127.0.0.1:8125 ")

	if got := AddrFromEnv("statsd:8125"); got != "127.0.0.1:8125" {
		t.Fatalf("AddrFromEnv(override) = %q, want 127.0.0.1:8125", got)
	}
}

func TestAddrFromEnvEmptyDisables(t *testing.T) {
	t.Setenv(EnvStatsDAddr, " ")

	if got := AddrFromEnv("statsd:8125"); got != "" {
		t.Fatalf("AddrFromEnv(empty) = %q, want empty", got)
	}
}

func TestMetricHostPathDefaultSanitizesRuntimeHostname(t *testing.T) {
	if got := MetricHostPath("my-host.example"); got != "my-host.example" {
		t.Fatalf("MetricHostPath(default) = %q, want my-host.example", got)
	}
}

func TestMetricHostPathPreservesGraphitePath(t *testing.T) {
	if got := MetricHostPath(" dfw1.jetmon-prod-1 "); got != "dfw1.jetmon-prod-1" {
		t.Fatalf("MetricHostPath(path) = %q, want dfw1.jetmon-prod-1", got)
	}
}

func TestMetricHostPathSanitizesUnsafeCharacters(t *testing.T) {
	if got := MetricHostPath(".dfw1.jetmon prod:1|blue."); got != "dfw1.jetmon_prod_1_blue" {
		t.Fatalf("MetricHostPath(sanitize) = %q, want dfw1.jetmon_prod_1_blue", got)
	}
}

func TestInitFromEnvDisabled(t *testing.T) {
	orig := global
	global = nil
	t.Cleanup(func() { global = orig })
	t.Setenv(EnvStatsDAddr, "")

	addr, enabled, err := InitFromEnv("host", "statsd:8125")
	if err != nil {
		t.Fatalf("InitFromEnv disabled error = %v, want nil", err)
	}
	if enabled || addr != "" {
		t.Fatalf("InitFromEnv disabled = addr %q enabled %v, want empty false", addr, enabled)
	}
	if Global() != nil {
		t.Fatal("Global() = non-nil after disabled InitFromEnv, want nil")
	}
}

func TestWriteStatsFilesDoesNotPanic(t *testing.T) {
	// stats/ directory may not exist in test context; errors are silently
	// ignored by design — just verify this does not panic.
	WriteStatsFiles(StatsFilesSnapshot{SitesPerSec: 10, QueueSize: 5, Total: 1000})
}

func TestWriteStatsFilesUsesLegacyTextFormat(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	if err := os.Mkdir("stats", 0755); err != nil {
		t.Fatalf("Mkdir stats: %v", err)
	}

	WriteStatsFiles(StatsFilesSnapshot{
		SitesPerSec: 12,
		QueueSize:   34,
		Working:     5,
		Waiting:     55,
		Halting:     0,
		Error:       3,
		Offline:     2,
		Success:     95,
		Total:       100,
	})

	assertFileContent(t, "stats/sitespersec", "sites per second: 12\n")
	assertFileContent(t, "stats/sitesqueue", "sites in queue: 34\n")
	assertFileContent(t, "stats/totals", ""+
		"working : 5\n"+
		"waiting : 55\n"+
		"halting : 0\n"+
		"error   : 3\n"+
		"offline : 2\n"+
		"success : 95\n"+
		"total   : 100\n")
}

func TestLastStatsFilesSnapshotTracksLatestWrite(t *testing.T) {
	want := StatsFilesSnapshot{
		SitesPerSec: 21,
		QueueSize:   43,
		Working:     6,
		Waiting:     7,
		Halting:     0,
		Error:       1,
		Offline:     2,
		Success:     97,
		Total:       100,
	}

	WriteStatsFiles(want)

	got, updatedAt, ok := LastStatsFilesSnapshot()
	if !ok {
		t.Fatal("LastStatsFilesSnapshot ok = false, want true")
	}
	if updatedAt.IsZero() {
		t.Fatal("LastStatsFilesSnapshot updatedAt is zero")
	}
	if got != want {
		t.Fatalf("LastStatsFilesSnapshot = %+v, want %+v", got, want)
	}
}

func TestRenderLegacyStatsFiles(t *testing.T) {
	files := RenderLegacyStatsFiles(StatsFilesSnapshot{
		SitesPerSec: 12,
		QueueSize:   34,
		Working:     5,
		Waiting:     55,
		Halting:     0,
		Error:       3,
		Offline:     2,
		Success:     95,
		Total:       100,
	})

	if files.SitesPerSec != "sites per second: 12\n" {
		t.Fatalf("SitesPerSec = %q", files.SitesPerSec)
	}
	if files.SitesQueue != "sites in queue: 34\n" {
		t.Fatalf("SitesQueue = %q", files.SitesQueue)
	}
	if files.Totals != ""+
		"working : 5\n"+
		"waiting : 55\n"+
		"halting : 0\n"+
		"error   : 3\n"+
		"offline : 2\n"+
		"success : 95\n"+
		"total   : 100\n" {
		t.Fatalf("Totals = %q", files.Totals)
	}
}

func TestClientSendsStatsDMessages(t *testing.T) {
	clientConn, serverConn := net.Pipe()

	lines := make(chan string, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(lines)
		r := bufio.NewReader(serverConn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			lines <- strings.TrimSpace(line)
		}
	}()

	c := &Client{
		prefix: "com.jetpack.jetmon.host_name",
		conn:   clientConn,
	}
	c.Increment("checks.total", 2)
	c.Gauge("queue.depth", 7)
	c.Timing("request.rtt", 1500*time.Millisecond)
	c.EmitMemStats()
	_ = clientConn.Close()

	got := make([]string, 0, 16)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				goto doneReading
			}
			got = append(got, line)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for metric lines; got %v", got)
		}
	}
doneReading:
	_ = serverConn.Close()
	<-done

	wantPrefix := "com.jetpack.jetmon.host_name."
	expected := map[string]bool{
		wantPrefix + "checks.total:2|c":                  false,
		wantPrefix + "queue.depth:7|g":                   false,
		wantPrefix + "request.rtt:1500|ms":               false,
		wantPrefix + "process.rss_mb:":                   false,
		wantPrefix + "process.go_sys_mem_mb:":            false,
		wantPrefix + "process.heap_alloc_mb:":            false,
		wantPrefix + "process.open_fds:":                 false,
		wantPrefix + "process.max_fds:":                  false,
		wantPrefix + "process.fd_utilization_pct:":       false,
		wantPrefix + "runtime.goroutines.count:":         false,
		wantPrefix + "runtime.goroutines.runnable:":      false,
		wantPrefix + "runtime.goroutines.running:":       false,
		wantPrefix + "runtime.goroutines.waiting:":       false,
		wantPrefix + "runtime.goroutines.not_in_go:":     false,
		wantPrefix + "runtime.goroutines.created_total:": false,
		wantPrefix + "runtime.threads.count:":            false,
	}
	for _, line := range got {
		if _, ok := expected[line]; ok {
			expected[line] = true
			continue
		}
		matchedDynamic := false
		for prefix := range expected {
			if strings.HasSuffix(prefix, ":") && strings.HasPrefix(line, prefix) {
				expected[prefix] = true
				matchedDynamic = true
				break
			}
		}
		if !matchedDynamic {
			t.Fatalf("unexpected metric line %q in %v", line, got)
		}
	}
	for line, seen := range expected {
		if !seen {
			t.Fatalf("missing metric line %q in %v", line, got)
		}
	}
}

func TestClientEmitsDBStats(t *testing.T) {
	clientConn, serverConn := net.Pipe()

	lines := make(chan string, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(lines)
		r := bufio.NewReader(serverConn)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			lines <- strings.TrimSpace(line)
		}
	}()

	c := &Client{
		prefix: "com.jetpack.jetmon.host_name",
		conn:   clientConn,
	}
	c.EmitDBStats("db.write_pool", sql.DBStats{
		MaxOpenConnections: 64,
		OpenConnections:    12,
		InUse:              7,
		Idle:               5,
		WaitCount:          9,
		WaitDuration:       1500 * time.Millisecond,
		MaxIdleClosed:      3,
		MaxIdleTimeClosed:  2,
		MaxLifetimeClosed:  1,
	})
	_ = clientConn.Close()

	got := make(map[string]bool)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				goto doneReading
			}
			got[line] = true
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for DB metric lines; got %v", got)
		}
	}
doneReading:
	_ = serverConn.Close()
	<-done

	for _, want := range []string{
		"com.jetpack.jetmon.host_name.db.write_pool.max_open_connections:64|g",
		"com.jetpack.jetmon.host_name.db.write_pool.open_connections:12|g",
		"com.jetpack.jetmon.host_name.db.write_pool.in_use:7|g",
		"com.jetpack.jetmon.host_name.db.write_pool.idle:5|g",
		"com.jetpack.jetmon.host_name.db.write_pool.wait_count_total:9|g",
		"com.jetpack.jetmon.host_name.db.write_pool.wait_duration_ms_total:1500|g",
		"com.jetpack.jetmon.host_name.db.write_pool.max_idle_closed_total:3|g",
		"com.jetpack.jetmon.host_name.db.write_pool.max_idle_time_closed_total:2|g",
		"com.jetpack.jetmon.host_name.db.write_pool.max_lifetime_closed_total:1|g",
	} {
		if !got[want] {
			t.Fatalf("missing DB metric line %q in %v", want, got)
		}
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestInitSetsGlobalClient(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("udp listener unavailable: %v", err)
	}
	defer pc.Close()

	orig := global
	t.Cleanup(func() {
		if global != nil && global.conn != nil {
			_ = global.conn.Close()
		}
		global = orig
	})

	if err := Init(pc.LocalAddr().String(), "my-host.example"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if Global() == nil {
		t.Fatal("Global() = nil after Init")
	}
	if Global().prefix != "com.jetpack.jetmon.my-host.example" {
		t.Fatalf("prefix = %q", Global().prefix)
	}
}

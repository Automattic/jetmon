package metrics

import (
	"bufio"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

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

func TestMetricHostnameDefaultSanitizesRuntimeHostname(t *testing.T) {
	if got := MetricHostname("my-host.example"); got != "my-host.example" {
		t.Fatalf("MetricHostname(default) = %q, want my-host.example", got)
	}
}

func TestMetricHostnamePreservesGraphitePath(t *testing.T) {
	if got := MetricHostname(" dfw1.jetmon-prod-1 "); got != "dfw1.jetmon-prod-1" {
		t.Fatalf("MetricHostname(path) = %q, want dfw1.jetmon-prod-1", got)
	}
}

func TestMetricHostnameSanitizesUnsafeCharacters(t *testing.T) {
	if got := MetricHostname(".dfw1.jetmon prod:1|blue."); got != "dfw1.jetmon_prod_1_blue" {
		t.Fatalf("MetricHostname(sanitize) = %q, want dfw1.jetmon_prod_1_blue", got)
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
	defer clientConn.Close()
	defer serverConn.Close()

	c := &Client{
		prefix: "com.jetpack.jetmon.host_name",
		conn:   clientConn,
	}

	lines := make(chan string, 6)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(serverConn)
		for i := 0; i < 6; i++ {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			lines <- strings.TrimSpace(line)
		}
	}()

	c.Increment("checks.total", 2)
	c.Gauge("queue.depth", 7)
	c.Timing("request.rtt", 1500*time.Millisecond)
	c.EmitMemStats()

	got := make([]string, 0, 6)
	for len(got) < 6 {
		select {
		case line := <-lines:
			got = append(got, line)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for metric lines; got %v", got)
		}
	}
	_ = serverConn.Close()
	<-done

	wantPrefix := "com.jetpack.jetmon.host_name."
	expected := map[string]bool{
		wantPrefix + "checks.total:2|c":       false,
		wantPrefix + "queue.depth:7|g":        false,
		wantPrefix + "request.rtt:1500|ms":    false,
		wantPrefix + "process.rss_mb:":        false,
		wantPrefix + "process.go_sys_mem_mb:": false,
		wantPrefix + "process.heap_alloc_mb:": false,
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

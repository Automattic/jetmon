package metrics

import (
	"bufio"
	"net"
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

func TestWriteStatsFilesDoesNotPanic(t *testing.T) {
	// stats/ directory may not exist in test context; errors are silently
	// ignored by design — just verify this does not panic.
	WriteStatsFiles(10, 5, 1000)
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
	if Global().prefix != "com.jetpack.jetmon.my_host_example" {
		t.Fatalf("prefix = %q", Global().prefix)
	}
}

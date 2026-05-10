package checker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResultStatusType(t *testing.T) {
	tests := []struct {
		name string
		res  Result
		want string
	}{
		{name: "success", res: Result{Success: true}, want: "success"},
		{name: "ssl error", res: Result{ErrorCode: ErrorSSL}, want: "https"},
		{name: "tls expired", res: Result{ErrorCode: ErrorTLSExpired}, want: "https"},
		{name: "timeout", res: Result{ErrorCode: ErrorTimeout}, want: "intermittent"},
		{name: "body read", res: Result{ErrorCode: ErrorBodyRead}, want: "intermittent"},
		{name: "redirect", res: Result{ErrorCode: ErrorRedirect}, want: "redirect"},
		{name: "403 blocked", res: Result{HTTPCode: 403}, want: "blocked"},
		{name: "500 server error", res: Result{HTTPCode: 500}, want: "server"},
		{name: "503 server error", res: Result{HTTPCode: 503}, want: "server"},
		{name: "400 client error", res: Result{HTTPCode: 400}, want: "client"},
		{name: "404 client error", res: Result{HTTPCode: 404}, want: "client"},
		{name: "connect error fallthrough", res: Result{ErrorCode: ErrorConnect}, want: "intermittent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.res.StatusType(); got != tt.want {
				t.Fatalf("StatusType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCustomHeaders(t *testing.T) {
	if got := ParseCustomHeaders(nil); got != nil {
		t.Fatalf("ParseCustomHeaders(nil) = %v, want nil", got)
	}

	empty := ""
	if got := ParseCustomHeaders(&empty); got != nil {
		t.Fatalf("ParseCustomHeaders(\"\") = %v, want nil", got)
	}

	invalid := "not json"
	if got := ParseCustomHeaders(&invalid); got != nil {
		t.Fatalf("ParseCustomHeaders(invalid) = %v, want nil", got)
	}

	valid := `{"X-Foo":"bar","X-Baz":"qux"}`
	got := ParseCustomHeaders(&valid)
	if len(got) != 2 {
		t.Fatalf("ParseCustomHeaders() len = %d, want 2", len(got))
	}
	if got["X-Foo"] != "bar" {
		t.Fatalf("ParseCustomHeaders()[\"X-Foo\"] = %q, want %q", got["X-Foo"], "bar")
	}
}

func TestResultIsFailure(t *testing.T) {
	tests := []struct {
		name string
		res  Result
		want bool
	}{
		{
			name: "plain success",
			res:  Result{Success: true, ErrorCode: ErrorNone},
			want: false,
		},
		{
			name: "deprecated tls is advisory",
			res:  Result{Success: true, ErrorCode: ErrorTLSDeprecated},
			want: false,
		},
		{
			name: "keyword failure is hard failure",
			res:  Result{Success: true, ErrorCode: ErrorKeyword},
			want: true,
		},
		{
			name: "body read failure is hard failure",
			res:  Result{Success: false, ErrorCode: ErrorBodyRead},
			want: true,
		},
		{
			name: "transport failure is hard failure",
			res:  Result{Success: false, ErrorCode: ErrorConnect},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.res.IsFailure(); got != tt.want {
				t.Fatalf("IsFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPoolDrainWorkers(t *testing.T) {
	p := NewPool(3, 1, 3)
	t.Cleanup(p.Drain)

	if drained := p.DrainWorkers(2); drained != 2 {
		t.Fatalf("DrainWorkers() = %d, want 2", drained)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.WorkerCount() == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("worker count = %d, want 1 after retirement", p.WorkerCount())
}

func TestPoolDrainWaitsForInflightCheck(t *testing.T) {
	orig := poolCheckFunc
	started := make(chan struct{})
	release := make(chan struct{})
	poolCheckFunc = func(_ context.Context, req Request) Result {
		close(started)
		<-release
		return Result{BlogID: req.BlogID}
	}
	t.Cleanup(func() { poolCheckFunc = orig })

	p := NewPool(1, 1, 1)
	if !p.Submit(Request{BlogID: 1}) {
		t.Fatal("Submit() returned false")
	}

	<-started

	drained := make(chan struct{})
	go func() {
		p.Drain()
		close(drained)
	}()

	select {
	case <-drained:
		t.Fatal("Drain returned before in-flight check completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("Drain did not return after in-flight check completed")
	}
}

func TestSubmitReturnsFalseAfterDrain(t *testing.T) {
	p := NewPool(1, 1, 1)
	p.Drain()
	if p.Submit(Request{BlogID: 1, URL: "https://example.com"}) {
		t.Fatal("Submit() returned true after Drain, want false")
	}
}

func TestSetMaxSizeRetireExcessWorkers(t *testing.T) {
	p := NewPool(5, 1, 5)
	t.Cleanup(p.Drain)

	p.SetMaxSize(2)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.WorkerCount() <= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("worker count = %d after SetMaxSize(2), want <= 2", p.WorkerCount())
}

func TestEnsureSizeStartsWorkersWithoutQueuePressure(t *testing.T) {
	p := NewPoolWithQueueCap(1, 1, 5, 10)
	t.Cleanup(p.Drain)

	if added := p.EnsureSize(4); added != 3 {
		t.Fatalf("EnsureSize(4) added = %d, want 3", added)
	}
	if got := p.WorkerCount(); got != 4 {
		t.Fatalf("WorkerCount() = %d, want 4", got)
	}
	if added := p.EnsureSize(10); added != 1 {
		t.Fatalf("EnsureSize(10) added = %d, want 1 capped by max", added)
	}
	if got := p.WorkerCount(); got != 5 {
		t.Fatalf("WorkerCount() = %d, want max 5", got)
	}
}

func TestSetSizeBoundsStartsAndRetiresWorkers(t *testing.T) {
	p := NewPoolWithQueueCap(1, 1, 5, 10)
	t.Cleanup(p.Drain)

	if added := p.SetSizeBounds(4, 4); added != 3 {
		t.Fatalf("SetSizeBounds(4, 4) added = %d, want 3", added)
	}
	if got := p.WorkerCount(); got != 4 {
		t.Fatalf("WorkerCount() = %d, want 4", got)
	}

	p.SetSizeBounds(1, 2)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.WorkerCount() <= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("WorkerCount() = %d after SetSizeBounds(1, 2), want <= 2", p.WorkerCount())
}

func TestDrainCalledTwice(t *testing.T) {
	p := NewPool(1, 1, 1)
	p.Drain()
	p.Drain() // second Drain must be a no-op, not block or panic
}

func TestSubmitDropsWhenQueueFull(t *testing.T) {
	// Zero workers means nothing drains the channel. Channel capacity = max*2 = 4.
	p := NewPool(0, 0, 2)
	t.Cleanup(p.Drain)

	const cap = 4 // max*2
	for i := range cap {
		if !p.Submit(Request{BlogID: int64(i), URL: "x"}) {
			t.Fatalf("Submit %d returned false on non-full queue", i)
		}
	}
	if p.Submit(Request{BlogID: 99, URL: "overflow"}) {
		t.Fatal("Submit returned true on full queue, want false")
	}
}

func TestDrainWorkersAtMinimum(t *testing.T) {
	p := NewPool(1, 1, 1) // size == minSize
	t.Cleanup(p.Drain)

	// Nothing above minSize to retire.
	if drained := p.DrainWorkers(5); drained != 0 {
		t.Fatalf("DrainWorkers(5) at minSize = %d, want 0", drained)
	}
}

func TestDrainWorkersExceedsAvailable(t *testing.T) {
	p := NewPool(3, 1, 3)
	t.Cleanup(p.Drain)

	// 2 workers above minSize (3-1=2), requesting 10 — should cap at 2.
	drained := p.DrainWorkers(10)
	if drained != 2 {
		t.Fatalf("DrainWorkers(10) = %d, want 2 (capped at available)", drained)
	}
}

// --- checker.Check() ---

func TestCheckHTTP200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5})
	if !res.Success {
		t.Fatalf("Success = false, want true")
	}
	if res.HTTPCode != 200 {
		t.Fatalf("HTTPCode = %d, want 200", res.HTTPCode)
	}
	if res.ErrorCode != ErrorNone {
		t.Fatalf("ErrorCode = %d, want ErrorNone", res.ErrorCode)
	}
	if res.Method != http.MethodGet {
		t.Fatalf("Method = %q, want GET", res.Method)
	}
}

func TestCheckUsesGETWhenHEADWouldFail(t *testing.T) {
	var sawGET bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			sawGET = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected method %q", r.Method)
		}
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5})
	if !sawGET {
		t.Fatal("server did not receive GET")
	}
	if !res.Success {
		t.Fatalf("Success = false when GET is healthy and HEAD would fail; result=%+v", res)
	}
}

func TestCheckUsesGETWhenHEADWouldTimeout(t *testing.T) {
	var sawGET bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			time.Sleep(5 * time.Second)
		case http.MethodGet:
			sawGET = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected method %q", r.Method)
		}
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 1})
	if !sawGET {
		t.Fatal("server did not receive GET")
	}
	if !res.Success {
		t.Fatalf("Success = false when GET is healthy and HEAD would timeout; result=%+v", res)
	}
}

func TestCheckHTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5})
	if res.Success {
		t.Fatal("Success = true for 500 response, want false")
	}
	if res.HTTPCode != 500 {
		t.Fatalf("HTTPCode = %d, want 500", res.HTTPCode)
	}
}

func TestCheckTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 1})
	if res.ErrorCode != ErrorTimeout {
		t.Fatalf("ErrorCode = %d, want ErrorTimeout", res.ErrorCode)
	}
}

func TestCheckKeywordMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello jetpack world"))
	}))
	defer srv.Close()

	kw := "jetpack"
	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5, Keyword: &kw})
	if !res.Success {
		t.Fatalf("Success = false for keyword match, want true")
	}
	if res.ErrorCode != ErrorNone {
		t.Fatalf("ErrorCode = %d for keyword match, want ErrorNone", res.ErrorCode)
	}
}

func TestCheckKeywordMiss(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	kw := "jetpack"
	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5, Keyword: &kw})
	if res.ErrorCode != ErrorKeyword {
		t.Fatalf("ErrorCode = %d, want ErrorKeyword", res.ErrorCode)
	}
	if res.KeywordRule != "required" {
		t.Fatalf("KeywordRule = %q, want required", res.KeywordRule)
	}
}

func TestCheckForbiddenKeywordAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	forbidden := "malware"
	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5, ForbiddenKeyword: &forbidden})
	if !res.Success {
		t.Fatalf("Success = false when forbidden keyword absent, want true; result=%+v", res)
	}
}

func TestCheckForbiddenKeywordPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello malware world"))
	}))
	defer srv.Close()

	forbidden := "malware"
	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5, ForbiddenKeyword: &forbidden})
	if res.Success {
		t.Fatal("Success = true for forbidden keyword present, want false")
	}
	if res.ErrorCode != ErrorKeyword {
		t.Fatalf("ErrorCode = %d, want ErrorKeyword", res.ErrorCode)
	}
	if res.KeywordRule != "forbidden" {
		t.Fatalf("KeywordRule = %q, want forbidden", res.KeywordRule)
	}
}

func TestCheckForbiddenKeywordsPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello <script src=\"https://metrics.evil-cdn.example/collect.js\"></script> world"))
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{
		BlogID:            1,
		URL:               srv.URL,
		TimeoutSeconds:    5,
		ForbiddenKeywords: []string{"buy cheap viagra", "metrics.evil-cdn.example/collect.js"},
	})
	if res.Success {
		t.Fatal("Success = true for forbidden keyword list match, want false")
	}
	if res.ErrorCode != ErrorKeyword {
		t.Fatalf("ErrorCode = %d, want ErrorKeyword", res.ErrorCode)
	}
	if res.KeywordRule != "forbidden" {
		t.Fatalf("KeywordRule = %q, want forbidden", res.KeywordRule)
	}
}

func TestCheckTruncatedBodyFailsWithoutKeyword(t *testing.T) {
	srv := truncatedBodyServer(t, "partial response")
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5})
	if res.Success {
		t.Fatalf("Success = true for truncated body, want false; result=%+v", res)
	}
	if res.HTTPCode != http.StatusOK {
		t.Fatalf("HTTPCode = %d, want %d", res.HTTPCode, http.StatusOK)
	}
	if res.ErrorCode != ErrorBodyRead {
		t.Fatalf("ErrorCode = %d, want ErrorBodyRead", res.ErrorCode)
	}
	if res.ErrorDetail == "" || res.BodyReadError == "" {
		t.Fatalf("body read diagnostic missing: error_detail=%q body_read_error=%q", res.ErrorDetail, res.BodyReadError)
	}
	if res.BodyReadMode != "strict_finite" {
		t.Fatalf("BodyReadMode = %q, want strict_finite", res.BodyReadMode)
	}
	if res.BodyExpectedBytes != 1024 {
		t.Fatalf("BodyExpectedBytes = %d, want 1024", res.BodyExpectedBytes)
	}
	if res.BodyBytesRead != int64(len("partial response")) {
		t.Fatalf("BodyBytesRead = %d, want %d", res.BodyBytesRead, len("partial response"))
	}
	if res.BodyReadLimitBytes == 0 {
		t.Fatal("BodyReadLimitBytes = 0, want configured/default limit")
	}
}

func TestCheckTruncatedBodyFailsEvenWhenKeywordIsPresent(t *testing.T) {
	srv := truncatedBodyServer(t, "needle but incomplete")
	defer srv.Close()

	kw := "needle"
	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5, Keyword: &kw})
	if res.Success {
		t.Fatalf("Success = true for truncated body, want false; result=%+v", res)
	}
	if res.ErrorCode != ErrorBodyRead {
		t.Fatalf("ErrorCode = %d, want ErrorBodyRead", res.ErrorCode)
	}
}

func TestCheckBodyReadMaxBytesLimitExactTruncatedFails(t *testing.T) {
	const bodyReadLimit = int64(1 << 20) // 1 MiB

	srv := truncatedBodyServerWithContentLength(t, bodyReadLimit, "partial body")
	defer srv.Close()

	res := Check(context.Background(), Request{
		BlogID:           1,
		URL:              srv.URL,
		TimeoutSeconds:   5,
		BodyReadMaxBytes: bodyReadLimit,
	})
	if res.Success {
		t.Fatalf("Success = true for truncated body at exact limit, want false; result=%+v", res)
	}
	if res.HTTPCode != http.StatusOK {
		t.Fatalf("HTTPCode = %d, want %d", res.HTTPCode, http.StatusOK)
	}
	if res.ErrorCode != ErrorBodyRead {
		t.Fatalf("ErrorCode = %d, want ErrorBodyRead", res.ErrorCode)
	}
}

func TestCheckBodyReadMaxBytesLimitPlusOneSucceedsWithBudgetedRead(t *testing.T) {
	const bodyReadLimit = int64(1 << 20) // 1 MiB
	const contentLength = bodyReadLimit + 1

	body := strings.Repeat("a", int(contentLength))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{
		BlogID:           1,
		URL:              srv.URL,
		TimeoutSeconds:   5,
		BodyReadMaxBytes: bodyReadLimit,
	})
	if !res.Success {
		t.Fatalf("Success = false for known Content-Length above finite limit, want true; result=%+v", res)
	}
	if res.HTTPCode != http.StatusOK {
		t.Fatalf("HTTPCode = %d, want %d", res.HTTPCode, http.StatusOK)
	}
	if res.ErrorCode != ErrorNone {
		t.Fatalf("ErrorCode = %d, want ErrorNone", res.ErrorCode)
	}
}

func TestCheckBodyReadMaxBytesUnknownLengthOverLimitSucceeds(t *testing.T) {
	const bodyReadLimit = int64(1 << 20) // 1 MiB

	body := strings.Repeat("a", int(bodyReadLimit)+1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{
		BlogID:           1,
		URL:              srv.URL,
		TimeoutSeconds:   5,
		BodyReadMaxBytes: bodyReadLimit,
	})
	if !res.Success {
		t.Fatalf("Success = false for unknown Content-Length above finite limit, want true; result=%+v", res)
	}
	if res.HTTPCode != http.StatusOK {
		t.Fatalf("HTTPCode = %d, want %d", res.HTTPCode, http.StatusOK)
	}
	if res.ErrorCode != ErrorNone {
		t.Fatalf("ErrorCode = %d, want ErrorNone", res.ErrorCode)
	}
}

func TestCheckRedirectFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/final", http.StatusMovedPermanently)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5, RedirectPolicy: RedirectFail})
	if res.ErrorCode != ErrorRedirect {
		t.Fatalf("ErrorCode = %d, want ErrorRedirect", res.ErrorCode)
	}
	if res.RedirectCount != 1 {
		t.Fatalf("RedirectCount = %d, want 1", res.RedirectCount)
	}
	if len(res.RedirectChain) != 1 || !strings.HasSuffix(res.RedirectChain[0], "/final") {
		t.Fatalf("RedirectChain = %#v, want one /final hop", res.RedirectChain)
	}
	if res.ErrorDetail == "" {
		t.Fatal("ErrorDetail is empty, want redirect diagnostic context")
	}
}

func TestCheckCustomHeadersForwarded(t *testing.T) {
	var receivedHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-Custom-Test")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{
		BlogID:         1,
		URL:            srv.URL,
		TimeoutSeconds: 5,
		CustomHeaders:  map[string]string{"X-Custom-Test": "hello"},
	})
	if !res.Success {
		t.Fatalf("Success = false, want true")
	}
	if receivedHeader != "hello" {
		t.Fatalf("X-Custom-Test = %q, want hello", receivedHeader)
	}
}

func TestCheckDisablesIdleConnectionReuseForFleetScans(t *testing.T) {
	oldTransport := defaultTransport
	defaultTransport = newCheckTransport()
	t.Cleanup(func() {
		defaultTransport.CloseIdleConnections()
		defaultTransport = oldTransport
	})
	if !defaultTransport.DisableKeepAlives {
		t.Fatal("checker transport should disable keep-alives for large unique-host fleet scans")
	}

	var newConns atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()

	kw := "ok"
	for i := 0; i < 2; i++ {
		res := Check(context.Background(), Request{BlogID: int64(i + 1), URL: srv.URL, TimeoutSeconds: 5, Keyword: &kw})
		if !res.Success {
			t.Fatalf("check %d Success = false, want true (error_code=%d)", i+1, res.ErrorCode)
		}
	}

	if got := newConns.Load(); got != 2 {
		t.Fatalf("new connections = %d, want one connection per check with keep-alives disabled", got)
	}
}

func TestParseResolverServersSkipsLocalStub(t *testing.T) {
	raw := `
nameserver 127.0.0.53
nameserver ::1
nameserver 10.0.0.1
nameserver 2600:1702:50c1:71bf:1298:36ff:fea4:d4ee
`
	got := parseResolverServers(raw)
	want := []string{"10.0.0.1:53", "[2600:1702:50c1:71bf:1298:36ff:fea4:d4ee]:53"}
	if len(got) != len(want) {
		t.Fatalf("resolver servers = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolver server %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNormalizeResolverServers(t *testing.T) {
	got, err := normalizeResolverServers([]string{"10.0.0.176", "10.0.0.176:5353", "[2001:db8::1]:5353"})
	if err != nil {
		t.Fatalf("normalizeResolverServers() error = %v", err)
	}
	want := []string{"10.0.0.176:53", "10.0.0.176:5353", "[2001:db8::1]:5353"}
	if len(got) != len(want) {
		t.Fatalf("normalizeResolverServers() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("resolver %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNormalizeResolverServersRejectsUnsafeValues(t *testing.T) {
	for _, raw := range []string{"", "resolver.internal:53", "10.0.0.176:0", "10.0.0.176:notaport"} {
		if _, err := normalizeResolverServers([]string{raw}); err == nil {
			t.Fatalf("normalizeResolverServers(%q) expected error", raw)
		}
	}
}

func TestConfigureResolverServersInstallsOverride(t *testing.T) {
	configuredResolverMu.RLock()
	oldServers := append([]string(nil), configuredResolverServers...)
	configuredResolverMu.RUnlock()
	oldCache := defaultDNSCache
	t.Cleanup(func() {
		configuredResolverMu.Lock()
		configuredResolverServers = oldServers
		configuredResolverMu.Unlock()

		restoredTransport := newCheckTransport()

		configuredResolverMu.Lock()
		currentTransport := defaultTransport
		defaultTransport = restoredTransport
		defaultDNSCache = oldCache
		configuredResolverMu.Unlock()
		if currentTransport != nil {
			currentTransport.CloseIdleConnections()
		}
	})

	if err := ConfigureResolverServers([]string{"10.0.0.176:5353"}); err != nil {
		t.Fatalf("ConfigureResolverServers() error = %v", err)
	}
	got := directResolverServers()
	if len(got) != 1 || got[0] != "10.0.0.176:5353" {
		t.Fatalf("directResolverServers() = %#v, want configured resolver", got)
	}
}

func TestOrderedResolverAddrsPrefersIPv4ButHonorsNetwork(t *testing.T) {
	addrs := []net.IPAddr{
		{IP: net.ParseIP("2001:db8::1")},
		{IP: net.ParseIP("192.0.2.10")},
		{IP: net.ParseIP("198.51.100.20")},
	}

	got := orderedResolverAddrs(addrs, "tcp")
	if len(got) != 3 || got[0].IP.String() != "192.0.2.10" || got[1].IP.String() != "198.51.100.20" || got[2].IP.String() != "2001:db8::1" {
		t.Fatalf("orderedResolverAddrs(tcp) = %#v, want IPv4 addresses first", got)
	}

	got = orderedResolverAddrs(addrs, "tcp4")
	if len(got) != 2 || got[0].IP.To4() == nil || got[1].IP.To4() == nil {
		t.Fatalf("orderedResolverAddrs(tcp4) = %#v, want IPv4 only", got)
	}

	got = orderedResolverAddrs(addrs, "tcp6")
	if len(got) != 1 || got[0].IP.To4() != nil {
		t.Fatalf("orderedResolverAddrs(tcp6) = %#v, want IPv6 only", got)
	}
}

func TestCheckDNSCacheReturnsClonedCachedAddresses(t *testing.T) {
	cache := newCheckDNSCache(time.Minute, 10)
	cache.entries["example.com|ip4"] = checkDNSCacheEntry{
		addrs:   []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}},
		expires: time.Now().Add(time.Minute),
	}

	got, err := cache.lookup(context.Background(), &net.Resolver{}, "Example.COM.", "tcp")
	if err != nil {
		t.Fatalf("lookup() error = %v", err)
	}
	if len(got) != 1 || got[0].IP.String() != "192.0.2.10" {
		t.Fatalf("lookup() = %#v, want cached IPv4 address", got)
	}

	got[0].IP[15] = 99
	cache.mu.RLock()
	cached := cache.entries["example.com|ip4"].addrs[0].IP.String()
	cache.mu.RUnlock()
	if cached != "192.0.2.10" {
		t.Fatalf("cached address mutated through returned slice: %s", cached)
	}
}

func TestPreferredLookupFamily(t *testing.T) {
	if got := preferredLookupFamily("tcp"); got != "ip4" {
		t.Fatalf("preferredLookupFamily(tcp) = %q, want ip4", got)
	}
	if got := preferredLookupFamily("tcp4"); got != "ip4" {
		t.Fatalf("preferredLookupFamily(tcp4) = %q, want ip4", got)
	}
	if got := preferredLookupFamily("tcp6"); got != "ip6" {
		t.Fatalf("preferredLookupFamily(tcp6) = %q, want ip6", got)
	}
}

func TestCheckRedirectAlert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/final", http.StatusMovedPermanently)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{
		BlogID:         1,
		URL:            srv.URL,
		TimeoutSeconds: 5,
		RedirectPolicy: RedirectAlert,
	})
	if !res.RedirectChanged {
		t.Fatal("RedirectChanged = false for redirect-alert policy, want true")
	}
	if res.RedirectCount != 1 {
		t.Fatalf("RedirectCount = %d, want 1", res.RedirectCount)
	}
	if !strings.HasSuffix(res.FinalURL, "/final") {
		t.Fatalf("FinalURL = %q, want /final", res.FinalURL)
	}
}

func TestCheckTLS11IsAdvisoryNotOutage(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{
		MinVersion: tls.VersionTLS11,
		MaxVersion: tls.VersionTLS11,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
		},
	}
	srv.StartTLS()
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	oldTransport := defaultTransport
	defaultTransport = newCheckTransport()
	defaultTransport.TLSClientConfig.RootCAs = roots
	t.Cleanup(func() {
		defaultTransport.CloseIdleConnections()
		defaultTransport = oldTransport
	})

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5})
	if !res.Success {
		t.Fatalf("Success = false for TLS 1.1 advisory, want true; result=%+v", res)
	}
	if res.ErrorCode != ErrorTLSDeprecated {
		t.Fatalf("ErrorCode = %d, want ErrorTLSDeprecated", res.ErrorCode)
	}
	if res.TLSVersion != tls.VersionTLS11 {
		t.Fatalf("TLSVersion = 0x%04x, want TLS 1.1", res.TLSVersion)
	}
}

func TestCheckInvalidURL(t *testing.T) {
	res := Check(context.Background(), Request{BlogID: 1, URL: "://invalid-url", TimeoutSeconds: 5})
	if res.ErrorCode != ErrorConnect {
		t.Fatalf("ErrorCode = %d, want ErrorConnect for invalid URL", res.ErrorCode)
	}
	if res.ErrorDetail == "" {
		t.Fatal("ErrorDetail is empty, want invalid-url diagnostic context")
	}
}

func TestCheckConnectionRefused(t *testing.T) {
	// Start a server to get a free port, then stop it so connections are refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: url, TimeoutSeconds: 5})
	if res.ErrorCode != ErrorConnect {
		t.Fatalf("ErrorCode = %d, want ErrorConnect", res.ErrorCode)
	}
	// Regression: a connection refused at the TCP layer fires
	// DNSStart/DNSDone successfully but ConnectStart without ConnectDone
	// (and never fires TLSHandshakeStart). The phase durations for any
	// half-fired phase must be zero, not negative — a negative duration
	// from `zero_time.Sub(real_time)` overflows the INT column in
	// jetmon_check_history.
	if res.TCP < 0 {
		t.Errorf("TCP duration is negative (%v); zero-time underflow", res.TCP)
	}
	if res.TLS < 0 {
		t.Errorf("TLS duration is negative (%v); zero-time underflow", res.TLS)
	}
	if res.DNS < 0 {
		t.Errorf("DNS duration is negative (%v); zero-time underflow", res.DNS)
	}
}

func TestBoundedErrorDetailTruncatesLongErrors(t *testing.T) {
	detail := boundedErrorDetail(errors.New(strings.Repeat("x", 600)))
	if len(detail) != 503 {
		t.Fatalf("len(ErrorDetail) = %d, want 503", len(detail))
	}
	if !strings.HasSuffix(detail, "...") {
		t.Fatalf("ErrorDetail suffix = %q, want ellipsis", detail[len(detail)-3:])
	}
}

func TestClassifyDNSError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nxdomain",
			err:  &net.DNSError{Name: "example.invalid", Err: "no such host", IsNotFound: true},
			want: "nxdomain",
		},
		{
			name: "timeout",
			err:  &net.DNSError{Name: "example.test", Err: "i/o timeout", IsTimeout: true},
			want: "timeout",
		},
		{
			name: "servfail",
			err:  &net.DNSError{Name: "example.test", Err: "server misbehaving", IsTemporary: true},
			want: "servfail",
		},
		{
			name: "other",
			err:  &net.DNSError{Name: "example.test", Err: "resolver refused"},
			want: "resolver_error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotName, _ := classifyDNSError(tt.err)
			if got != tt.want {
				t.Fatalf("kind = %q, want %q", got, tt.want)
			}
			if gotName == "" {
				t.Fatal("dns name is empty")
			}
		})
	}
}

func truncatedBodyServer(t *testing.T, body string) *httptest.Server {
	return truncatedBodyServerWithContentLength(t, 1024, body)
}

func truncatedBodyServerWithContentLength(t *testing.T, contentLength int64, body string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer does not support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
}

// --- Pool scale(), Results(), QueueDepth(), ActiveCount() ---

func TestScaleUpWhenQueueDeep(t *testing.T) {
	orig := poolCheckFunc
	block := make(chan struct{})
	poolCheckFunc = func(_ context.Context, req Request) Result {
		<-block
		return Result{BlogID: req.BlogID}
	}

	p := NewPool(1, 1, 5)
	// Single Cleanup so the order is explicit: unblock workers, drain the
	// pool to completion, then restore poolCheckFunc. The previous LIFO
	// ordering left a race where workers could still read poolCheckFunc as
	// it was reassigned.
	t.Cleanup(func() {
		close(block)
		p.Drain()
		poolCheckFunc = orig
	})

	// Submit enough work to ensure queue > current worker count.
	for range 4 {
		p.Submit(Request{BlogID: 1, URL: "x"})
	}
	time.Sleep(10 * time.Millisecond)

	p.scale()

	if p.WorkerCount() <= 1 {
		t.Fatalf("WorkerCount = %d after scale-up, want > 1", p.WorkerCount())
	}
}

func TestScaleDownGraduallyWhenIdle(t *testing.T) {
	p := NewPool(3, 1, 3)
	t.Cleanup(p.Drain)

	p.scale()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.WorkerCount() < 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("WorkerCount = %d after idle scale-down, want < 3", p.WorkerCount())
}

func TestScaleDownExcessAboveMax(t *testing.T) {
	p := NewPool(5, 1, 5)
	t.Cleanup(p.Drain)

	p.mu.Lock()
	p.maxSize = 3
	p.mu.Unlock()

	p.scale()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.WorkerCount() <= 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("WorkerCount = %d after maxSize reduction, want <= 3", p.WorkerCount())
}

func TestResults(t *testing.T) {
	orig := poolCheckFunc
	poolCheckFunc = func(_ context.Context, req Request) Result {
		return Result{BlogID: req.BlogID, Success: true, HTTPCode: 200}
	}
	t.Cleanup(func() { poolCheckFunc = orig })

	p := NewPool(1, 1, 1)
	t.Cleanup(p.Drain)

	p.Submit(Request{BlogID: 42, URL: "https://example.com"})

	select {
	case res := <-p.Results():
		if res.BlogID != 42 {
			t.Fatalf("result BlogID = %d, want 42", res.BlogID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for result")
	}
}

func TestPoolDoesNotDropResultWhenResultChannelIsFull(t *testing.T) {
	orig := poolCheckFunc
	secondReturned := make(chan struct{})
	var once sync.Once
	poolCheckFunc = func(_ context.Context, req Request) Result {
		if req.BlogID == 2 {
			once.Do(func() { close(secondReturned) })
		}
		return Result{BlogID: req.BlogID, Success: true, HTTPCode: 200}
	}
	t.Cleanup(func() { poolCheckFunc = orig })

	p := NewPoolWithQueueCap(1, 1, 1, 1)
	t.Cleanup(p.Drain)

	if !p.Submit(Request{BlogID: 1, URL: "https://example.com/1"}) {
		t.Fatal("first Submit() returned false")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.Results()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(p.Results()) != 1 {
		t.Fatal("first result did not fill result channel")
	}

	if !p.Submit(Request{BlogID: 2, URL: "https://example.com/2"}) {
		t.Fatal("second Submit() returned false")
	}
	select {
	case <-secondReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second check to complete")
	}
	time.Sleep(25 * time.Millisecond)

	first := <-p.Results()
	if first.BlogID != 1 {
		t.Fatalf("first result BlogID = %d, want 1", first.BlogID)
	}
	select {
	case second := <-p.Results():
		if second.BlogID != 2 {
			t.Fatalf("second result BlogID = %d, want 2", second.BlogID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second result; result was likely dropped")
	}
}

func TestQueueDepth(t *testing.T) {
	orig := poolCheckFunc
	release := make(chan struct{})
	poolCheckFunc = func(_ context.Context, req Request) Result {
		<-release
		return Result{BlogID: req.BlogID}
	}

	p := NewPool(1, 1, 1)
	// Cleanup order matters: close(release) unblocks workers so Drain can
	// complete, Drain ensures all worker goroutines have exited before we
	// restore poolCheckFunc. Doing this as one Cleanup keeps the ordering
	// explicit; LIFO ordering of multiple Cleanups previously left a race
	// where workers could still read poolCheckFunc as it was reassigned.
	t.Cleanup(func() {
		close(release)
		p.Drain()
		poolCheckFunc = orig
	})

	p.Submit(Request{BlogID: 1, URL: "a"})
	time.Sleep(10 * time.Millisecond) // let worker pick up first request
	p.Submit(Request{BlogID: 2, URL: "b"})

	if d := p.QueueDepth(); d != 1 {
		t.Fatalf("QueueDepth() = %d, want 1", d)
	}
}

func TestActiveCount(t *testing.T) {
	orig := poolCheckFunc
	started := make(chan struct{})
	release := make(chan struct{})
	poolCheckFunc = func(_ context.Context, req Request) Result {
		close(started)
		<-release
		return Result{BlogID: req.BlogID}
	}

	p := NewPool(1, 1, 1)
	// Same single-Cleanup ordering as TestQueueDepth — see comment there.
	t.Cleanup(func() {
		close(release)
		p.Drain()
		poolCheckFunc = orig
	})

	p.Submit(Request{BlogID: 1, URL: "x"})
	<-started

	if p.ActiveCount() != 1 {
		t.Fatalf("ActiveCount() = %d, want 1", p.ActiveCount())
	}
}

func BenchmarkCheckNoKeywordLargeBody(b *testing.B) {
	const bodyReadLimit = int64(1 << 20) // 1 MiB
	const contentLength = bodyReadLimit + 1024

	body := strings.Repeat("a", int(contentLength))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := Check(context.Background(), Request{
			BlogID:           1,
			URL:              srv.URL,
			TimeoutSeconds:   5,
			BodyReadMaxBytes: bodyReadLimit,
		})
		if !res.Success || res.ErrorCode != ErrorNone || res.HTTPCode != http.StatusOK {
			b.Fatalf("unexpected result: %+v", res)
		}
	}
}

func BenchmarkCheckKeywordLargeBody(b *testing.B) {
	const bodyReadLimit = int64(1 << 20) // 1 MiB
	const contentLength = bodyReadLimit + 1024
	const keyword = "required-token"
	keywordPtr := keyword

	body := keyword + strings.Repeat("a", int(contentLength)-len(keyword))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := Check(context.Background(), Request{
			BlogID:              1,
			URL:                 srv.URL,
			TimeoutSeconds:      5,
			BodyReadMaxBytes:    bodyReadLimit,
			KeywordReadMaxBytes: bodyReadLimit,
			Keyword:             &keywordPtr,
		})
		if !res.Success || res.ErrorCode != ErrorNone || res.HTTPCode != http.StatusOK {
			b.Fatalf("unexpected result: %+v", res)
		}
	}
}

package checker

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		{name: "probe safety", res: Result{ErrorCode: ErrorProbeSafety}, want: "probe_safety"},
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

	mixed := `{"X-Good":"ok","Connection":"close","Bad\r\nName":"x","X-Bad":"ok\r\nInjected: yes"}`
	got = ParseCustomHeaders(&mixed)
	if len(got) != 1 || got["X-Good"] != "ok" {
		t.Fatalf("ParseCustomHeaders(mixed) = %#v, want only X-Good", got)
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
		{
			name: "probe safety block is non-downtime",
			res:  Result{Success: false, ErrorCode: ErrorProbeSafety},
			want: false,
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

func TestCheckCanUseLegacyHEADMethod(t *testing.T) {
	var sawHEAD bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("method = %q, want HEAD", r.Method)
		}
		sawHEAD = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{
		BlogID:           1,
		URL:              srv.URL,
		Method:           http.MethodHead,
		DetectionProfile: "legacy",
		TimeoutSeconds:   5,
	})
	if !sawHEAD {
		t.Fatal("server did not receive HEAD")
	}
	if !res.Success || res.Method != http.MethodHead || res.DetectionProfile != "legacy" {
		t.Fatalf("HEAD result = %+v, want successful legacy HEAD", res)
	}
	if res.BodyReadMode != "" || res.BodyBytesRead != 0 {
		t.Fatalf("HEAD body read = mode:%q bytes:%d, want none", res.BodyReadMode, res.BodyBytesRead)
	}
}

func TestSimpleHTTPProfileSkipsKeywordDetection(t *testing.T) {
	kw := "must-be-present"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %q, want GET", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("plain healthy response"))
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{
		BlogID:           1,
		URL:              srv.URL,
		Method:           http.MethodGet,
		DetectionProfile: "simple_http",
		TimeoutSeconds:   5,
		Keyword:          &kw,
	})
	if !res.Success {
		t.Fatalf("simple_http result = %+v, want success despite missing keyword", res)
	}
	if res.ErrorCode != ErrorNone || res.KeywordRule != "" {
		t.Fatalf("simple_http keyword fields = code:%d rule:%q, want none", res.ErrorCode, res.KeywordRule)
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

func TestCheckContextCancelsBodyRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := Check(ctx, Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Check() took %s, want context cancellation to stop body read promptly", elapsed)
	}
	if res.ErrorCode != ErrorBodyRead {
		t.Fatalf("ErrorCode = %d, want ErrorBodyRead after response-body cancellation", res.ErrorCode)
	}
	if res.BodyReadError == "" {
		t.Fatal("BodyReadError is empty, want cancellation detail")
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

func TestCheckCompressedLargeBodyIsCappedAfterDecompression(t *testing.T) {
	const bodyReadLimit = int64(1024)

	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, _ = zw.Write([]byte(strings.Repeat("a", int(bodyReadLimit)*128)))
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(compressed.Len()))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(compressed.Bytes())
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{
		BlogID:           1,
		URL:              srv.URL,
		TimeoutSeconds:   5,
		BodyReadMaxBytes: bodyReadLimit,
	})
	if !res.Success {
		t.Fatalf("Success = false for compressed body over read cap, want true; result=%+v", res)
	}
	if res.ErrorCode != ErrorNone {
		t.Fatalf("ErrorCode = %d, want ErrorNone", res.ErrorCode)
	}
	if res.BodyBytesRead != bodyReadLimit {
		t.Fatalf("BodyBytesRead = %d, want cap %d", res.BodyBytesRead, bodyReadLimit)
	}
}

func TestCheckBodyReadMaxMSTimesOutBudgetedRead(t *testing.T) {
	srv := slowStreamingBodyServer(t, "x", 200*time.Millisecond)
	defer srv.Close()

	start := time.Now()
	res := Check(context.Background(), Request{
		BlogID:           1,
		URL:              srv.URL,
		TimeoutSeconds:   5,
		BodyReadMaxBytes: 1024,
		BodyReadMaxMS:    50,
	})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Check() took %s, want body-read budget to stop promptly", elapsed)
	}
	if res.Success {
		t.Fatalf("Success = true for body-read budget exhaustion, want false; result=%+v", res)
	}
	if res.ErrorCode != ErrorBodyRead {
		t.Fatalf("ErrorCode = %d, want ErrorBodyRead", res.ErrorCode)
	}
	if !strings.Contains(res.BodyReadError, "response body read budget exceeded") {
		t.Fatalf("BodyReadError = %q, want budget diagnostic", res.BodyReadError)
	}
}

func TestCheckBodyReadMaxMSDoesNotFailStrictFiniteBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(75 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{
		BlogID:           1,
		URL:              srv.URL,
		TimeoutSeconds:   5,
		BodyReadMaxBytes: 1024,
		BodyReadMaxMS:    10,
	})
	if !res.Success {
		t.Fatalf("Success = false for slow strict finite body, want true; result=%+v", res)
	}
	if res.ErrorCode != ErrorNone {
		t.Fatalf("ErrorCode = %d, want ErrorNone", res.ErrorCode)
	}
	if res.BodyReadMode != "strict_finite" {
		t.Fatalf("BodyReadMode = %q, want strict_finite", res.BodyReadMode)
	}
}

func TestCheckKeywordReadMaxMSTimesOutAsTimeout(t *testing.T) {
	srv := slowStreamingBodyServer(t, "x", 200*time.Millisecond)
	defer srv.Close()

	kw := "needle"
	start := time.Now()
	res := Check(context.Background(), Request{
		BlogID:              1,
		URL:                 srv.URL,
		TimeoutSeconds:      5,
		Keyword:             &kw,
		KeywordReadMaxBytes: 1024,
		KeywordReadMaxMS:    50,
	})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Check() took %s, want keyword-read budget to stop promptly", elapsed)
	}
	if res.Success {
		t.Fatalf("Success = true for keyword-read budget exhaustion, want false; result=%+v", res)
	}
	if res.ErrorCode != ErrorTimeout {
		t.Fatalf("ErrorCode = %d, want ErrorTimeout", res.ErrorCode)
	}
	if !strings.Contains(res.BodyReadError, "response body read budget exceeded") {
		t.Fatalf("BodyReadError = %q, want budget diagnostic", res.BodyReadError)
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

func TestCheckRedirectMetadataBoundsLargeLocation(t *testing.T) {
	longQuery := strings.Repeat("a", maxResultURLDetailBytes*2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/final?x="+longQuery, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5, RedirectPolicy: RedirectAlert})
	if !res.Success {
		t.Fatalf("Success = false after long redirect, want true; result=%+v", res)
	}
	if len(res.RedirectChain) != 1 {
		t.Fatalf("RedirectChain len = %d, want 1", len(res.RedirectChain))
	}
	if len(res.RedirectChain[0]) != maxResultURLDetailBytes+3 || !strings.HasSuffix(res.RedirectChain[0], "...") {
		t.Fatalf("RedirectChain[0] length/suffix = %d/%q, want bounded ellipsis", len(res.RedirectChain[0]), res.RedirectChain[0][len(res.RedirectChain[0])-3:])
	}
	if len(res.FinalURL) != maxResultURLDetailBytes+3 || !strings.HasSuffix(res.FinalURL, "...") {
		t.Fatalf("FinalURL length/suffix = %d/%q, want bounded ellipsis", len(res.FinalURL), res.FinalURL[len(res.FinalURL)-3:])
	}
}

func TestCheckRejectsCrossHostRedirectToUnsafeAddress(t *testing.T) {
	privateTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer privateTarget.Close()
	privateURL := strings.Replace(privateTarget.URL, "127.0.0.1", "localhost", 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, privateURL, http.StatusFound)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5, RedirectPolicy: RedirectFollow})
	if res.Success {
		t.Fatalf("Success = true for unsafe cross-host redirect, want false; result=%+v", res)
	}
	if res.ErrorCode != ErrorProbeSafety {
		t.Fatalf("ErrorCode = %d, want ErrorProbeSafety", res.ErrorCode)
	}
	if res.IsFailure() {
		t.Fatal("IsFailure = true for unsafe redirect, want false")
	}
	if !strings.Contains(res.ErrorDetail, "redirect safety check") {
		t.Fatalf("ErrorDetail = %q, want redirect safety diagnostic", res.ErrorDetail)
	}
	if res.RedirectCount != 1 {
		t.Fatalf("RedirectCount = %d, want 1", res.RedirectCount)
	}
}

func TestCheckRejectsSameHostRedirectWithUserinfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			u := *r.URL
			u.Scheme = "http"
			u.Host = r.Host
			u.Path = "/final"
			u.User = url.User("user")
			http.Redirect(w, r, u.String(), http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5, RedirectPolicy: RedirectFollow})
	if res.Success {
		t.Fatalf("Success = true for userinfo redirect, want false; result=%+v", res)
	}
	if res.ErrorCode != ErrorProbeSafety {
		t.Fatalf("ErrorCode = %d, want ErrorProbeSafety", res.ErrorCode)
	}
	if res.IsFailure() {
		t.Fatal("IsFailure = true for unsafe userinfo redirect, want false")
	}
	if !strings.Contains(res.ErrorDetail, "userinfo") {
		t.Fatalf("ErrorDetail = %q, want userinfo diagnostic", res.ErrorDetail)
	}
}

func TestTransportSafetyBlocksUnsafeIPBeforeDial(t *testing.T) {
	ctx := withTargetSafety(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1/", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	_, err = defaultHTTPIPTransport.RoundTrip(req)
	if !errors.Is(err, errProbeSafetyBlock) {
		t.Fatalf("RoundTrip err = %v, want errProbeSafetyBlock", err)
	}
}

func TestCheckDNSCacheExpiryUsesJitter(t *testing.T) {
	cache := newCheckDNSCache(15*time.Minute, 10)
	cache.jitter = 3 * time.Minute
	cache.salt = 1
	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)

	expires := cache.expiresAt("example.com|ip4", now)
	min := now.Add(12 * time.Minute)
	max := now.Add(15 * time.Minute)
	if expires.Before(min) || !expires.Before(max) {
		t.Fatalf("expiresAt = %s, want in [%s, %s)", expires, min, max)
	}
}

func TestCheckRejectsOversizedResponseHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Large-Header", strings.Repeat("a", int(maxResponseHeaderBytes)+1024))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5})
	if res.Success {
		t.Fatalf("Success = true for oversized response headers, want false; result=%+v", res)
	}
	if res.ErrorCode != ErrorConnect {
		t.Fatalf("ErrorCode = %d, want ErrorConnect", res.ErrorCode)
	}
	if !strings.Contains(res.ErrorDetail, "server response headers exceeded") {
		t.Fatalf("ErrorDetail = %q, want oversized-header diagnostic", res.ErrorDetail)
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

func TestCheckReusesCleartextIPConnectionsForFleetScans(t *testing.T) {
	oldTransport := defaultTransport
	oldHTTPIPTransport := defaultHTTPIPTransport
	defaultTransport = newCheckTransport()
	defaultHTTPIPTransport = newHTTPIPPoolTransportWithFallback(defaultTransport)
	t.Cleanup(func() {
		defaultTransport.CloseIdleConnections()
		defaultHTTPIPTransport.CloseIdleConnections()
		defaultTransport = oldTransport
		defaultHTTPIPTransport = oldHTTPIPTransport
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

	if got := newConns.Load(); got != 1 {
		t.Fatalf("new connections = %d, want cleartext IP pool to reuse the connection", got)
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
	oldHTTPIPTransport := defaultHTTPIPTransport
	t.Cleanup(func() {
		configuredResolverMu.Lock()
		configuredResolverServers = oldServers
		configuredResolverMu.Unlock()

		restoredTransport := newCheckTransport()

		configuredResolverMu.Lock()
		currentTransport := defaultTransport
		currentHTTPIPTransport := defaultHTTPIPTransport
		defaultTransport = restoredTransport
		defaultDNSCache = oldCache
		defaultHTTPIPTransport = oldHTTPIPTransport
		configuredResolverMu.Unlock()
		if currentTransport != nil {
			currentTransport.CloseIdleConnections()
		}
		if currentHTTPIPTransport != nil {
			currentHTTPIPTransport.CloseIdleConnections()
		}
	})

	if err := ConfigureResolverServers([]string{"10.0.0.176:5353"}); err != nil {
		t.Fatalf("ConfigureResolverServers() error = %v", err)
	}
	got := directResolverServers()
	if len(got) != 1 || got[0] != "10.0.0.176:5353" {
		t.Fatalf("directResolverServers() = %#v, want configured resolver", got)
	}
	configured := ConfiguredResolverServers()
	if len(configured) != 1 || configured[0] != "10.0.0.176:5353" {
		t.Fatalf("ConfiguredResolverServers() = %#v, want configured resolver", configured)
	}
}

func TestHTTPIPPoolTransportPreservesHostHeader(t *testing.T) {
	hostSeen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostSeen <- r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().(*net.TCPAddr)
	rawURL := "http://site-0000001.capacity.internal:" + strconv.Itoa(addr.Port) + "/"

	oldCache := defaultDNSCache
	oldHTTPIPTransport := defaultHTTPIPTransport
	defaultDNSCache = newCheckDNSCache(time.Minute, 10)
	defaultDNSCache.store(
		normalizeDNSCacheKey("site-0000001.capacity.internal", "ip4"),
		[]net.IPAddr{{IP: net.ParseIP("127.0.0.1")}},
		time.Now().Add(time.Minute),
	)
	defaultHTTPIPTransport = newHTTPIPPoolTransportWithFallback(defaultTransport)
	t.Cleanup(func() {
		if defaultHTTPIPTransport != nil {
			defaultHTTPIPTransport.CloseIdleConnections()
		}
		defaultDNSCache = oldCache
		defaultHTTPIPTransport = oldHTTPIPTransport
	})

	res := Check(context.Background(), Request{BlogID: 42, URL: rawURL, TimeoutSeconds: 2})
	if !res.Success {
		t.Fatalf("Check() success = false, result=%+v", res)
	}
	select {
	case got := <-hostSeen:
		want := "site-0000001.capacity.internal:" + strconv.Itoa(addr.Port)
		if got != want {
			t.Fatalf("Host header = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not receive request")
	}
}

func TestHTTPIPPoolTransportFallsBackAcrossResolvedAddresses(t *testing.T) {
	hostSeen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostSeen <- r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	addr := srv.Listener.Addr().(*net.TCPAddr)
	rawURL := "http://multi-a.capacity.internal:" + strconv.Itoa(addr.Port) + "/"

	oldCache := defaultDNSCache
	oldHTTPIPTransport := defaultHTTPIPTransport
	defaultDNSCache = newCheckDNSCache(time.Minute, 10)
	defaultDNSCache.store(
		normalizeDNSCacheKey("multi-a.capacity.internal", "ip4"),
		[]net.IPAddr{
			{IP: net.ParseIP("127.0.0.2")},
			{IP: net.ParseIP("127.0.0.1")},
		},
		time.Now().Add(time.Minute),
	)
	defaultHTTPIPTransport = newHTTPIPPoolTransportWithFallback(defaultTransport)
	t.Cleanup(func() {
		if defaultHTTPIPTransport != nil {
			defaultHTTPIPTransport.CloseIdleConnections()
		}
		defaultDNSCache = oldCache
		defaultHTTPIPTransport = oldHTTPIPTransport
	})

	res := Check(context.Background(), Request{BlogID: 42, URL: rawURL, TimeoutSeconds: 2})
	if !res.Success {
		t.Fatalf("Check() success = false after fallback, result=%+v", res)
	}
	select {
	case got := <-hostSeen:
		want := "multi-a.capacity.internal:" + strconv.Itoa(addr.Port)
		if got != want {
			t.Fatalf("Host header = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not receive request through fallback address")
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

func TestCheckDNSCacheReturnsCachedAddresses(t *testing.T) {
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
}

func TestCheckDNSCacheReturnsCachedNegativeEntry(t *testing.T) {
	cache := newCheckDNSCache(time.Minute, 10)
	wantErr := &net.DNSError{Err: "no such host", Name: "missing.test", IsNotFound: true}
	cache.entries["missing.test|ip4"] = checkDNSCacheEntry{
		err:      wantErr,
		expires:  time.Now().Add(time.Minute),
		negative: true,
	}

	addrs, err := cache.lookup(context.Background(), &net.Resolver{}, "missing.test", "tcp")
	if err == nil {
		t.Fatal("lookup() error = nil, want cached negative error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("lookup() err = %v, want cached %v", err, wantErr)
	}
	if len(addrs) != 0 {
		t.Fatalf("lookup() addrs = %#v, want empty", addrs)
	}
}

func TestIsDNSNotFoundErrDistinguishesNXDOMAIN(t *testing.T) {
	if !isDNSNotFoundErr(&net.DNSError{Err: "no such host", IsNotFound: true}) {
		t.Fatal("isDNSNotFoundErr did not recognize an NXDOMAIN-like DNSError")
	}
	if isDNSNotFoundErr(&net.DNSError{Err: "i/o timeout", IsTimeout: true}) {
		t.Fatal("isDNSNotFoundErr negative-cached a timeout (should not)")
	}
	if isDNSNotFoundErr(&net.DNSError{Err: "server misbehaving", IsTemporary: true}) {
		t.Fatal("isDNSNotFoundErr negative-cached a temporary failure (should not)")
	}
	if isDNSNotFoundErr(fmt.Errorf("plain error")) {
		t.Fatal("isDNSNotFoundErr returned true for a non-DNSError")
	}
}

func TestValidateResolvedTargetRejectsMixedPublicPrivateDNSAnswers(t *testing.T) {
	dnsAddr := startTestDNSServer(t, [][]net.IP{{
		net.ParseIP("93.184.216.34"),
		net.ParseIP("127.0.0.1"),
	}})
	withTestResolver(t, dnsAddr, 15*time.Minute)

	err := validateResolvedTarget(context.Background(), "probe safety check", "mixed.test")
	if err == nil {
		t.Fatal("validateResolvedTarget accepted mixed public/private DNS answers")
	}
	if !strings.Contains(err.Error(), "non-public address") {
		t.Fatalf("err = %v, want non-public address diagnostic", err)
	}
}

func TestValidateResolvedTargetRejectsDNSRebindAfterCacheExpiry(t *testing.T) {
	dnsAddr := startTestDNSServer(t, [][]net.IP{
		{net.ParseIP("93.184.216.34")},
		{net.ParseIP("127.0.0.1")},
	})
	withTestResolver(t, dnsAddr, time.Millisecond)

	if err := validateResolvedTarget(context.Background(), "probe safety check", "rebind.test"); err != nil {
		t.Fatalf("first validateResolvedTarget() error = %v, want nil", err)
	}
	time.Sleep(5 * time.Millisecond)

	err := validateResolvedTarget(context.Background(), "probe safety check", "rebind.test")
	if err == nil {
		t.Fatal("validateResolvedTarget accepted rebound private DNS answer")
	}
	if !strings.Contains(err.Error(), "non-public address") {
		t.Fatalf("err = %v, want non-public address diagnostic", err)
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

func TestCheckSelfSignedTLSFailsAsSSL(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := Check(context.Background(), Request{BlogID: 1, URL: srv.URL, TimeoutSeconds: 5})
	if res.Success {
		t.Fatalf("Success = true for untrusted TLS cert, want false; result=%+v", res)
	}
	if res.ErrorCode != ErrorSSL {
		t.Fatalf("ErrorCode = %d, want ErrorSSL; detail=%q", res.ErrorCode, res.ErrorDetail)
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

func TestCheckBlocksUnsafeDirectTargetWhenSafetyEnforced(t *testing.T) {
	for _, rawURL := range []string{
		"http://127.0.0.1/admin",
		"www.example.com",
		"file:///etc/passwd",
		"http://user@example.com",
	} {
		t.Run(rawURL, func(t *testing.T) {
			res := Check(context.Background(), Request{
				BlogID:              1,
				URL:                 rawURL,
				TimeoutSeconds:      5,
				EnforceTargetSafety: true,
			})
			if res.ErrorCode != ErrorProbeSafety {
				t.Fatalf("ErrorCode = %d, want ErrorProbeSafety; result=%+v", res.ErrorCode, res)
			}
			if !res.IsProbeSafetyBlock() {
				t.Fatal("IsProbeSafetyBlock = false, want true")
			}
			if res.IsFailure() {
				t.Fatal("IsFailure = true for probe safety block, want non-downtime result")
			}
			if !strings.Contains(res.ErrorDetail, "probe safety check") {
				t.Fatalf("ErrorDetail = %q, want probe safety diagnostic", res.ErrorDetail)
			}
		})
	}
}

func TestCheckTargetSafetyDNSFailureIsConnectFailure(t *testing.T) {
	dnsAddr := startTestNXDomainDNSServer(t)
	withTestResolver(t, dnsAddr, time.Millisecond)

	res := Check(context.Background(), Request{
		BlogID:              1,
		URL:                 "http://missing.example/security-check",
		TimeoutSeconds:      2,
		EnforceTargetSafety: true,
	})
	if res.ErrorCode != ErrorConnect {
		t.Fatalf("ErrorCode = %d, want ErrorConnect; result=%+v", res.ErrorCode, res)
	}
	if res.IsProbeSafetyBlock() {
		t.Fatal("IsProbeSafetyBlock = true for DNS failure, want false")
	}
	if res.DNSFailureKind != "nxdomain" {
		t.Fatalf("DNSFailureKind = %q, want nxdomain; detail=%q", res.DNSFailureKind, res.ErrorDetail)
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

func withTestResolver(t *testing.T, dnsAddr string, ttl time.Duration) {
	t.Helper()
	oldResolver := defaultResolver
	oldCache := defaultDNSCache
	defaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: time.Second}
			return d.DialContext(ctx, network, dnsAddr)
		},
	}
	defaultDNSCache = newCheckDNSCache(ttl, 100)
	defaultDNSCache.jitter = 0
	t.Cleanup(func() {
		defaultResolver = oldResolver
		defaultDNSCache = oldCache
	})
}

func startTestDNSServer(t *testing.T, answerBatches [][]net.IP) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	var queryCount atomic.Uint64
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			query := append([]byte(nil), buf[:n]...)
			idx := int(queryCount.Add(1)) - 1
			if idx >= len(answerBatches) {
				idx = len(answerBatches) - 1
			}
			resp := buildTestDNSAResponse(query, answerBatches[idx])
			if len(resp) > 0 {
				_, _ = pc.WriteTo(resp, addr)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func startTestNXDomainDNSServer(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			resp := buildTestDNSNXDomainResponse(buf[:n])
			if len(resp) > 0 {
				_, _ = pc.WriteTo(resp, addr)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func buildTestDNSAResponse(query []byte, ips []net.IP) []byte {
	if len(query) < 12 {
		return nil
	}
	qEnd := 12
	for qEnd < len(query) && query[qEnd] != 0 {
		qEnd += int(query[qEnd]) + 1
	}
	if qEnd+5 > len(query) {
		return nil
	}
	question := query[12 : qEnd+5]
	answers := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			answers = append(answers, ip4)
		}
	}

	resp := make([]byte, 0, 12+len(question)+len(answers)*16)
	resp = append(resp, query[0], query[1], 0x81, 0x80)
	resp = append(resp, 0x00, 0x01)
	resp = append(resp, byte(len(answers)>>8), byte(len(answers)))
	resp = append(resp, 0x00, 0x00, 0x00, 0x00)
	resp = append(resp, question...)
	for _, ip := range answers {
		resp = append(resp,
			0xc0, 0x0c,
			0x00, 0x01,
			0x00, 0x01,
			0x00, 0x00, 0x00, 0x01,
			0x00, 0x04,
			ip[0], ip[1], ip[2], ip[3],
		)
	}
	return resp
}

func buildTestDNSNXDomainResponse(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}
	qEnd := 12
	for qEnd < len(query) && query[qEnd] != 0 {
		qEnd += int(query[qEnd]) + 1
	}
	if qEnd+5 > len(query) {
		return nil
	}
	question := query[12 : qEnd+5]
	resp := make([]byte, 0, 12+len(question))
	resp = append(resp, query[0], query[1], 0x81, 0x83)
	resp = append(resp, 0x00, 0x01)
	resp = append(resp, 0x00, 0x00)
	resp = append(resp, 0x00, 0x00, 0x00, 0x00)
	resp = append(resp, question...)
	return resp
}

func slowStreamingBodyServer(t *testing.T, chunk string, delay time.Duration) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		ticker := time.NewTicker(delay)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := w.Write([]byte(chunk)); err != nil {
					return
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
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

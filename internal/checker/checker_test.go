package checker

import (
	"context"
	"net/http"
	"net/http/httptest"
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

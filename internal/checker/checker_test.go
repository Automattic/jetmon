package checker

import (
	"context"
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

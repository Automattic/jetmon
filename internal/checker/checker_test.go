package checker

import (
	"testing"
	"time"
)

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

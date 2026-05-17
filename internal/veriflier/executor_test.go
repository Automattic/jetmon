package veriflier

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
)

func TestCheckerProbeSafetyErrorCodeContract(t *testing.T) {
	if checkerErrorProbeSafety != checker.ErrorProbeSafety {
		t.Fatalf("checkerErrorProbeSafety = %d, want checker.ErrorProbeSafety %d", checkerErrorProbeSafety, checker.ErrorProbeSafety)
	}
	got := outcomeFromResult(CheckResult{Success: false, ErrorCode: int32(checkerErrorProbeSafety)})
	if got != OutcomeUnknown {
		t.Fatalf("outcomeFromResult(probe safety) = %q, want %q", got, OutcomeUnknown)
	}
}

func TestExecutorPreservesInputOrder(t *testing.T) {
	exec := NewExecutor(func(_ context.Context, req CheckRequest) ProbeResult {
		if req.BlogID == 1 {
			time.Sleep(20 * time.Millisecond)
		}
		return ProbeResult{CheckResult: CheckResult{
			BlogID:   req.BlogID,
			URL:      req.URL,
			Success:  true,
			HTTPCode: 200,
		}, Outcome: OutcomeUp}
	}, 2, 2)
	defer exec.Shutdown()

	results, err := exec.ExecuteBatch(context.Background(), []CheckRequest{
		{BlogID: 1, URL: "https://example.com/slow"},
		{BlogID: 2, URL: "https://example.com/fast"},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	if results[0].BlogID != 1 || results[1].BlogID != 2 {
		t.Fatalf("results order = [%d, %d], want [1, 2]", results[0].BlogID, results[1].BlogID)
	}
}

func TestExecutorPartiallyShedsOverCapacityBatch(t *testing.T) {
	var called atomic.Int64
	exec := NewExecutor(func(_ context.Context, req CheckRequest) ProbeResult {
		called.Add(1)
		return ProbeResult{CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL, Success: true}, Outcome: OutcomeUp}
	}, 1, 1)
	defer exec.Shutdown()

	results, err := exec.ExecuteBatch(context.Background(), []CheckRequest{
		{BlogID: 1, URL: "https://example.com/1"},
		{BlogID: 2, URL: "https://example.com/2"},
		{BlogID: 3, URL: "https://example.com/3"},
	})
	if err != nil {
		t.Fatalf("ExecuteBatch() error = %v", err)
	}
	if called.Load() != 2 {
		t.Fatalf("check function called %d times, want 2 admitted checks", called.Load())
	}
	if len(results) != 3 {
		t.Fatalf("results len = %d, want 3", len(results))
	}
	if !results[0].Success || !results[1].Success {
		t.Fatalf("admitted results = %+v, want successes", results[:2])
	}
	if results[2].Success || results[2].Outcome != OutcomeAgentOverloaded {
		t.Fatalf("shed result = %+v, want agent_overloaded", results[2])
	}
}

func TestExecutorContextCancellationReturnsTimeoutResult(t *testing.T) {
	started := make(chan struct{})
	exec := NewExecutor(func(ctx context.Context, req CheckRequest) ProbeResult {
		close(started)
		<-ctx.Done()
		return ProbeResult{CheckResult: CheckResult{
			BlogID:    req.BlogID,
			URL:       req.URL,
			Success:   false,
			ErrorCode: 1,
		}, Outcome: OutcomeTimeout}
	}, 1, 1)
	defer exec.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		results []ProbeResult
		err     error
	}, 1)
	go func() {
		results, err := exec.ExecuteBatch(ctx, []CheckRequest{{BlogID: 1, URL: "https://example.com", RequestID: "req-1"}})
		done <- struct {
			results []ProbeResult
			err     error
		}{results: results, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for check to start")
	}
	cancel()
	out := <-done
	results, err := out.results, out.err
	if err != nil {
		t.Fatalf("ExecuteBatch() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if results[0].Success || results[0].Outcome != OutcomeTimeout || results[0].ErrorCode != 1 || results[0].RequestID != "req-1" {
		t.Fatalf("timeout result = %+v, want timeout for req-1", results[0])
	}
}

func TestExecutorDeadlineCancellationReturnsAgentOverloaded(t *testing.T) {
	started := make(chan struct{})
	exec := NewExecutor(func(ctx context.Context, req CheckRequest) ProbeResult {
		close(started)
		<-ctx.Done()
		return ProbeResult{CheckResult: CheckResult{
			BlogID:    req.BlogID,
			URL:       req.URL,
			RequestID: req.RequestID,
			Success:   false,
			ErrorCode: 1,
		}, Outcome: OutcomeTimeout}
	}, 1, 1)
	defer exec.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	results, err := exec.ExecuteBatch(ctx, []CheckRequest{{BlogID: 1, URL: "https://example.com", RequestID: "req-1"}})
	if err != nil {
		t.Fatalf("ExecuteBatch() error = %v", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("check did not start")
	}
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if results[0].Success || results[0].Outcome != OutcomeAgentOverloaded || results[0].ErrorCode != 1 || results[0].RequestID != "req-1" {
		t.Fatalf("deadline result = %+v, want agent_overloaded for req-1", results[0])
	}
}

func TestExecutorDeadlineOverloadDoesNotPoisonAverageDuration(t *testing.T) {
	started := make(chan struct{})
	exec := NewExecutor(func(ctx context.Context, req CheckRequest) ProbeResult {
		if req.BlogID == 1 {
			close(started)
			<-ctx.Done()
			return ProbeResult{CheckResult: CheckResult{
				BlogID:    req.BlogID,
				URL:       req.URL,
				RequestID: req.RequestID,
				Success:   false,
				ErrorCode: 1,
			}, Outcome: OutcomeTimeout}
		}
		time.Sleep(2 * time.Millisecond)
		return ProbeResult{CheckResult: CheckResult{
			BlogID:    req.BlogID,
			URL:       req.URL,
			RequestID: req.RequestID,
			Success:   true,
			HTTPCode:  200,
		}, Outcome: OutcomeUp}
	}, 1, 1)
	defer exec.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	results, err := exec.ExecuteBatch(ctx, []CheckRequest{{BlogID: 1, URL: "https://example.com/slow", RequestID: "req-1"}})
	if err != nil {
		t.Fatalf("ExecuteBatch() deadline error = %v", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("deadline check did not start")
	}
	if len(results) != 1 || results[0].Outcome != OutcomeAgentOverloaded {
		t.Fatalf("deadline results = %+v, want agent_overloaded", results)
	}
	waitForExecutorCapacity(t, exec, func(c Capacity) bool {
		return c.Active == 0 && c.InFlight == 0
	})
	if got := exec.avgNanos.Load(); got != 0 {
		t.Fatalf("avg nanos after operational overload = %d, want 0", got)
	}

	results, err = exec.ExecuteBatch(context.Background(), []CheckRequest{{BlogID: 2, URL: "https://example.com/fast", RequestID: "req-2"}})
	if err != nil {
		t.Fatalf("ExecuteBatch() success error = %v", err)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("success results = %+v, want successful probe", results)
	}
	if got := exec.avgNanos.Load(); got <= 0 {
		t.Fatalf("avg nanos after successful probe = %d, want positive", got)
	}
}

func waitForExecutorCapacity(t *testing.T, exec *Executor, ok func(Capacity) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		capacity := exec.Capacity()
		if ok(capacity) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("executor capacity did not reach expected state: %+v", exec.Capacity())
}

func TestExecutorContextCancellationKeepsCompletedResults(t *testing.T) {
	slowStarted := make(chan struct{})
	fastReturned := make(chan struct{})
	exec := NewExecutor(func(ctx context.Context, req CheckRequest) ProbeResult {
		if req.BlogID == 2 {
			close(slowStarted)
			<-ctx.Done()
			return ProbeResult{CheckResult: CheckResult{
				BlogID:    req.BlogID,
				URL:       req.URL,
				RequestID: req.RequestID,
				Success:   false,
				ErrorCode: 1,
			}, Outcome: OutcomeTimeout}
		}
		close(fastReturned)
		return ProbeResult{CheckResult: CheckResult{
			BlogID:    req.BlogID,
			URL:       req.URL,
			RequestID: req.RequestID,
			Success:   true,
			HTTPCode:  200,
		}, Outcome: OutcomeUp}
	}, 2, 2)
	defer exec.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		results []ProbeResult
		err     error
	}, 1)
	go func() {
		results, err := exec.ExecuteBatch(ctx, []CheckRequest{
			{BlogID: 1, URL: "https://example.com/fast", RequestID: "fast"},
			{BlogID: 2, URL: "https://example.com/slow", RequestID: "slow"},
		})
		done <- struct {
			results []ProbeResult
			err     error
		}{results: results, err: err}
	}()
	select {
	case <-slowStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for slow check to start")
	}
	select {
	case <-fastReturned:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fast check to complete")
	}
	cancel()
	out := <-done
	results, err := out.results, out.err
	if err != nil {
		t.Fatalf("ExecuteBatch() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	if !results[0].Success || results[0].Outcome != OutcomeUp || results[0].RequestID != "fast" {
		t.Fatalf("fast result = %+v, want success for fast", results[0])
	}
	if results[1].Success || results[1].Outcome != OutcomeTimeout || results[1].ErrorCode != 1 || results[1].RequestID != "slow" {
		t.Fatalf("slow result = %+v, want timeout for slow", results[1])
	}
}

func TestExecutorCapacityReflectsInFlightWork(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{}, 1)
	exec := NewExecutor(func(_ context.Context, req CheckRequest) ProbeResult {
		started <- struct{}{}
		<-block
		return ProbeResult{CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL}}
	}, 1, 2)
	defer exec.Shutdown()

	done := make(chan error, 1)
	go func() {
		_, err := exec.ExecuteBatch(context.Background(), []CheckRequest{{BlogID: 1, URL: "https://example.com"}})
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for check to start")
	}
	capacity := exec.Capacity()
	if capacity.MaxConcurrency != 1 || capacity.QueueCapacity != 2 {
		t.Fatalf("capacity = %+v", capacity)
	}
	if capacity.Active != 1 || capacity.InFlight != 1 {
		t.Fatalf("capacity active/in-flight = %+v, want active=1 in_flight=1", capacity)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatalf("ExecuteBatch() error = %v", err)
	}
}

func TestDefaultMaxConcurrencyScalesWithCPUAndFDCap(t *testing.T) {
	tests := []struct {
		name  string
		procs int
		fdCap int
		want  int
	}{
		{name: "single cpu floor", procs: 1, want: 512},
		{name: "eight cpu host", procs: 8, want: 4096},
		{name: "fd cap wins", procs: 8, fdCap: 300, want: 300},
		{name: "global ceiling", procs: 256, want: 32768},
		{name: "bad gomaxprocs uses one cpu", procs: 0, want: 512},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultMaxConcurrencyFor(tt.procs, tt.fdCap); got != tt.want {
				t.Fatalf("defaultMaxConcurrencyFor(%d, %d) = %d, want %d", tt.procs, tt.fdCap, got, tt.want)
			}
		})
	}
}

func TestDefaultQueueCapacityProvidesBurstHeadroom(t *testing.T) {
	if got := defaultQueueCapacity(512); got != 4096 {
		t.Fatalf("defaultQueueCapacity(512) = %d, want 4096", got)
	}
	if got := defaultQueueCapacity(0); got != 0 {
		t.Fatalf("defaultQueueCapacity(0) = %d, want 0", got)
	}
}

func TestExecutorShutdownCancelsInFlightBatch(t *testing.T) {
	started := make(chan struct{}, 1)
	exec := NewExecutor(func(ctx context.Context, req CheckRequest) ProbeResult {
		started <- struct{}{}
		<-ctx.Done()
		return ProbeResult{CheckResult: CheckResult{
			BlogID:    req.BlogID,
			URL:       req.URL,
			ErrorCode: 1,
		}, Outcome: OutcomeTimeout}
	}, 1, 1)

	done := make(chan error, 1)
	go func() {
		_, err := exec.ExecuteBatch(context.Background(), []CheckRequest{{BlogID: 1, URL: "https://example.com"}})
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for check to start")
	}
	exec.Shutdown()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteBatch() error = %v, want context.Canceled", err)
	}
}

package veriflier

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

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

func TestExecutorRejectsOverCapacityBatch(t *testing.T) {
	var called atomic.Int64
	exec := NewExecutor(func(_ context.Context, req CheckRequest) ProbeResult {
		called.Add(1)
		return ProbeResult{CheckResult: CheckResult{BlogID: req.BlogID, URL: req.URL}}
	}, 1, 1)
	defer exec.Shutdown()

	_, err := exec.ExecuteBatch(context.Background(), []CheckRequest{
		{BlogID: 1, URL: "https://example.com/1"},
		{BlogID: 2, URL: "https://example.com/2"},
		{BlogID: 3, URL: "https://example.com/3"},
	})
	if !errors.Is(err, ErrOverloaded) {
		t.Fatalf("ExecuteBatch() error = %v, want ErrOverloaded", err)
	}
	if called.Load() != 0 {
		t.Fatalf("check function called %d times for rejected batch", called.Load())
	}
}

func TestExecutorContextCancellation(t *testing.T) {
	exec := NewExecutor(func(ctx context.Context, req CheckRequest) ProbeResult {
		<-ctx.Done()
		return ProbeResult{CheckResult: CheckResult{
			BlogID:    req.BlogID,
			URL:       req.URL,
			Success:   false,
			ErrorCode: 1,
		}, Outcome: OutcomeTimeout}
	}, 1, 1)
	defer exec.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := exec.ExecuteBatch(ctx, []CheckRequest{{BlogID: 1, URL: "https://example.com"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ExecuteBatch() error = %v, want DeadlineExceeded", err)
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

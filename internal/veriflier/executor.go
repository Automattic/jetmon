package veriflier

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
)

var ErrOverloaded = errors.New("veriflier overloaded")

const (
	defaultConcurrencyPerCPU = 256
	minDefaultConcurrency    = 256
	maxDefaultConcurrency    = 32768
	defaultQueueMultiplier   = 8
)

type CheckFunc func(context.Context, CheckRequest) ProbeResult

type Executor struct {
	checkFn CheckFunc

	jobs   chan execJob
	slots  chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	wg        sync.WaitGroup
	active    atomic.Int64
	completed atomic.Uint64
	rejected  atomic.Uint64

	maxConcurrency int
	queueCapacity  int
}

type execJob struct {
	ctx    context.Context
	index  int
	req    CheckRequest
	result chan execResult
}

type execResult struct {
	index  int
	result ProbeResult
}

func NewExecutor(checkFn CheckFunc, maxConcurrency, queueCapacity int) *Executor {
	if checkFn == nil {
		checkFn = func(_ context.Context, req CheckRequest) ProbeResult {
			return ProbeResult{CheckResult: CheckResult{
				BlogID:    req.BlogID,
				URL:       req.URL,
				Success:   false,
				ErrorCode: 1,
			}, Outcome: OutcomeUnknown}
		}
	}
	if maxConcurrency <= 0 {
		maxConcurrency = defaultMaxConcurrency()
	}
	if queueCapacity < 0 {
		queueCapacity = 0
	}
	if queueCapacity == 0 {
		queueCapacity = defaultQueueCapacity(maxConcurrency)
	}

	ctx, cancel := context.WithCancel(context.Background())
	e := &Executor{
		checkFn:        checkFn,
		jobs:           make(chan execJob, maxConcurrency+queueCapacity),
		slots:          make(chan struct{}, maxConcurrency+queueCapacity),
		ctx:            ctx,
		cancel:         cancel,
		maxConcurrency: maxConcurrency,
		queueCapacity:  queueCapacity,
	}
	for range maxConcurrency {
		e.wg.Add(1)
		go e.worker(ctx)
	}
	return e
}

func (e *Executor) ExecuteBatch(ctx context.Context, reqs []CheckRequest) ([]ProbeResult, error) {
	if len(reqs) == 0 {
		return nil, nil
	}

	acquired := 0
	for range reqs {
		select {
		case e.slots <- struct{}{}:
			acquired++
		case <-e.ctx.Done():
			e.releaseSlots(acquired)
			return nil, e.ctx.Err()
		case <-ctx.Done():
			e.releaseSlots(acquired)
			return nil, ctx.Err()
		default:
			e.rejected.Add(1)
			e.releaseSlots(acquired)
			return nil, ErrOverloaded
		}
	}

	results := make([]ProbeResult, len(reqs))
	resultCh := make(chan execResult, len(reqs))
	for i, req := range reqs {
		job := execJob{
			ctx:    ctx,
			index:  i,
			req:    req,
			result: resultCh,
		}
		select {
		case e.jobs <- job:
		case <-e.ctx.Done():
			e.releaseSlots(len(reqs) - i)
			return nil, e.ctx.Err()
		case <-ctx.Done():
			e.releaseSlots(len(reqs) - i)
			return nil, ctx.Err()
		}
		results[i].RequestID = req.RequestID
		results[i].BlogID = req.BlogID
		results[i].URL = req.URL
	}

	for range reqs {
		if err := e.ctx.Err(); err != nil {
			return nil, err
		}
		select {
		case <-e.ctx.Done():
			return nil, e.ctx.Err()
		case <-ctx.Done():
			return nil, ctx.Err()
		case res := <-resultCh:
			if err := e.ctx.Err(); err != nil {
				return nil, err
			}
			results[res.index] = res.result
		}
	}
	return results, nil
}

func (e *Executor) Capacity() Capacity {
	return Capacity{
		MaxConcurrency: e.maxConcurrency,
		QueueCapacity:  e.queueCapacity,
		QueueDepth:     len(e.jobs),
		Active:         int(e.active.Load()),
		InFlight:       len(e.slots),
	}
}

func (e *Executor) Shutdown() {
	e.cancel()
	e.wg.Wait()
}

func (e *Executor) worker(ctx context.Context) {
	defer e.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-e.jobs:
			e.active.Add(1)
			jobCtx, cancel := context.WithCancel(job.ctx)
			stopShutdownCancel := context.AfterFunc(ctx, cancel)
			res := e.checkFn(jobCtx, job.req)
			stopShutdownCancel()
			cancel()
			if res.RequestID == "" {
				res.RequestID = job.req.RequestID
			}
			if res.BlogID == 0 {
				res.BlogID = job.req.BlogID
			}
			if res.MonitorSiteID == 0 {
				res.MonitorSiteID = job.req.MonitorSiteID
			}
			if res.URL == "" {
				res.URL = job.req.URL
			}
			if res.Outcome == "" {
				res.Outcome = outcomeFromResult(res.CheckResult)
			}
			e.completed.Add(1)
			e.active.Add(-1)
			select {
			case job.result <- execResult{index: job.index, result: res}:
			default:
			}
			<-e.slots
		}
	}
}

func (e *Executor) releaseSlots(n int) {
	for range n {
		select {
		case <-e.slots:
		default:
			return
		}
	}
}

func defaultMaxConcurrency() int {
	return defaultMaxConcurrencyFor(runtime.GOMAXPROCS(0), fdConcurrencyCap())
}

func defaultMaxConcurrencyFor(procs, fdCap int) int {
	if procs < 1 {
		procs = 1
	}
	workers := procs * defaultConcurrencyPerCPU
	if workers < minDefaultConcurrency {
		workers = minDefaultConcurrency
	}
	if fdCap > 0 && workers > fdCap {
		workers = fdCap
	}
	if workers > maxDefaultConcurrency {
		workers = maxDefaultConcurrency
	}
	return workers
}

func defaultQueueCapacity(maxConcurrency int) int {
	if maxConcurrency <= 0 {
		return 0
	}
	queue := maxConcurrency * defaultQueueMultiplier
	if queue/maxConcurrency != defaultQueueMultiplier {
		return maxConcurrency
	}
	return queue
}

func fdConcurrencyCap() int {
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil || lim.Cur == 0 {
		return 0
	}
	// Leave descriptors for inbound sockets, logs, DNS resolver activity, and
	// short connection bursts. A single HTTP probe normally needs one outbound fd.
	cap := int(lim.Cur / 2)
	if cap < 16 {
		return 16
	}
	return cap
}

func outcomeFromResult(res CheckResult) string {
	if res.Success {
		return OutcomeUp
	}
	if res.ErrorCode == 1 {
		return OutcomeTimeout
	}
	if res.HTTPCode >= 400 {
		return OutcomeDown
	}
	if res.ErrorCode != 0 {
		return OutcomeProbeError
	}
	return OutcomeUnknown
}

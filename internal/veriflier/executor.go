package veriflier

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var ErrOverloaded = errors.New("veriflier overloaded")

const (
	defaultConcurrencyPerCPU = 512
	minDefaultConcurrency    = 512
	maxDefaultConcurrency    = 32768
	defaultQueueMultiplier   = 8
	defaultCheckEstimate     = 50 * time.Millisecond
	deadlineAdmissionReserve = 250 * time.Millisecond
	deadlineResultDrainGrace = 25 * time.Millisecond
	// Wire-compatible checker error code for probe safety blocks. Keep this
	// private to avoid a production dependency from veriflier back to checker;
	// executor tests assert it stays in sync with checker.ErrorProbeSafety.
	checkerErrorProbeSafety = 9
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
	avgNanos  atomic.Int64

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

	results := make([]ProbeResult, len(reqs))
	for i, req := range reqs {
		results[i] = agentOverloadedProbeResult(req)
	}

	resultCh := make(chan execResult, len(reqs))
	enqueued := 0
	for i, req := range reqs {
		if err := e.ctx.Err(); err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			drainReadyResults(results, resultCh)
			return results, nil
		}
		if e.shouldShedForDeadline(ctx) {
			e.rejected.Add(1)
			continue
		}
		select {
		case e.slots <- struct{}{}:
		case <-e.ctx.Done():
			return nil, e.ctx.Err()
		case <-ctx.Done():
			drainReadyResults(results, resultCh)
			return results, nil
		default:
			e.rejected.Add(1)
			continue
		}

		job := execJob{
			ctx:    ctx,
			index:  i,
			req:    req,
			result: resultCh,
		}
		select {
		case e.jobs <- job:
			enqueued++
		case <-e.ctx.Done():
			e.releaseSlots(1)
			return nil, e.ctx.Err()
		case <-ctx.Done():
			e.releaseSlots(1)
			drainReadyResults(results, resultCh)
			return results, nil
		}
	}

	for range enqueued {
		if err := e.ctx.Err(); err != nil {
			return nil, err
		}
		select {
		case <-e.ctx.Done():
			return nil, e.ctx.Err()
		case <-ctx.Done():
			drainReadyResultsWithGrace(results, resultCh, deadlineResultDrainGrace)
			return results, nil
		case res := <-resultCh:
			if err := e.ctx.Err(); err != nil {
				return nil, err
			}
			results[res.index] = res.result
		}
	}
	return results, nil
}

func (e *Executor) shouldShedForDeadline(ctx context.Context) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return false
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return true
	}
	avg := time.Duration(e.avgNanos.Load())
	if avg <= 0 {
		avg = defaultCheckEstimate
	}
	prospectivePosition := len(e.slots) + 1
	queuedAhead := prospectivePosition - e.maxConcurrency
	if queuedAhead < 0 {
		queuedAhead = 0
	}
	if queuedAhead == 0 {
		return false
	}
	if remaining <= deadlineAdmissionReserve {
		return true
	}
	wavesAhead := queuedAhead / e.maxConcurrency
	if queuedAhead%e.maxConcurrency != 0 {
		wavesAhead++
	}
	estimatedWait := time.Duration(wavesAhead) * avg
	return estimatedWait+deadlineAdmissionReserve >= remaining
}

func drainReadyResults(results []ProbeResult, resultCh <-chan execResult) {
	for {
		select {
		case res := <-resultCh:
			results[res.index] = res.result
		default:
			return
		}
	}
}

func drainReadyResultsWithGrace(results []ProbeResult, resultCh <-chan execResult, grace time.Duration) {
	drainReadyResults(results, resultCh)
	if grace <= 0 {
		return
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for {
		select {
		case res := <-resultCh:
			results[res.index] = res.result
		case <-timer.C:
			return
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func agentOverloadedProbeResult(req CheckRequest) ProbeResult {
	return ProbeResult{
		CheckResult: CheckResult{
			MonitorSiteID: req.MonitorSiteID,
			BlogID:        req.BlogID,
			URL:           req.URL,
			Outcome:       OutcomeAgentOverloaded,
			Success:       false,
			ErrorCode:     1,
			RequestID:     req.RequestID,
		},
		Outcome: OutcomeAgentOverloaded,
	}
}

func (e *Executor) Capacity() Capacity {
	avgMS := int(time.Duration(e.avgNanos.Load()).Milliseconds())
	return Capacity{
		MaxConcurrency: e.maxConcurrency,
		QueueCapacity:  e.queueCapacity,
		QueueDepth:     len(e.jobs),
		Active:         int(e.active.Load()),
		InFlight:       len(e.slots),
		Completed:      int(e.completed.Load()),
		Rejected:       int(e.rejected.Load()),
		AvgCheckMS:     avgMS,
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
			if err := job.ctx.Err(); err != nil {
				select {
				case job.result <- execResult{index: job.index, result: agentOverloadedProbeResult(job.req)}:
				default:
				}
				<-e.slots
				continue
			}
			e.active.Add(1)
			start := time.Now()
			jobCtx, cancel := context.WithCancel(job.ctx)
			stopShutdownCancel := context.AfterFunc(ctx, cancel)
			res := e.checkFn(jobCtx, job.req)
			stopShutdownCancel()
			cancel()
			operationalOverload := shouldTreatResultAsOperationalOverload(job.ctx, res)
			if operationalOverload {
				res = agentOverloadedProbeResult(job.req)
			}
			if !operationalOverload {
				e.observeDuration(time.Since(start))
			}
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

func shouldTreatResultAsOperationalOverload(ctx context.Context, res ProbeResult) bool {
	if ctx == nil || ctx.Err() == nil || res.Success {
		return false
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false
	}
	outcome := res.Outcome
	if outcome == "" {
		outcome = outcomeFromResult(res.CheckResult)
	}
	return outcome == OutcomeTimeout || res.ErrorCode == 1
}

func (e *Executor) observeDuration(d time.Duration) {
	if d <= 0 {
		return
	}
	nanos := int64(d)
	for {
		old := e.avgNanos.Load()
		next := nanos
		if old > 0 {
			next = old + (nanos-old)/16
		}
		if e.avgNanos.CompareAndSwap(old, next) {
			return
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
	if res.ErrorCode == checkerErrorProbeSafety {
		return OutcomeUnknown
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

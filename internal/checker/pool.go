package checker

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

var poolCheckFunc = Check

// Pool is an auto-scaling goroutine pool for HTTP checks.
type Pool struct {
	work    chan Request
	results chan Result
	retire  chan struct{}
	cancel  context.CancelFunc
	ctx     context.Context
	closed  atomic.Bool

	size   atomic.Int64
	active atomic.Int64

	mu      sync.Mutex
	workMu  sync.RWMutex
	wg      sync.WaitGroup
	minSize int
	maxSize int
	// scaleSignal wakes autoScale ahead of its 5s ticker when Submit observes
	// queue pressure. Capacity 1 so a burst of submits coalesces into a
	// single nudge: only the first signal matters, the autoscaler will pick
	// up whatever queue depth exists at the time it runs.
	scaleSignal chan struct{}
}

// NewPool creates a Pool with the given initial, min, and max worker counts.
func NewPool(initial, min, max int) *Pool {
	return NewPoolWithQueueCap(initial, min, max, max*2)
}

// NewPoolWithQueueCap creates a Pool with an explicit work/result channel
// capacity. It is used by streaming schedulers that need a large elastic queue
// without changing the legacy NewPool queue-size contract.
func NewPoolWithQueueCap(initial, min, max, queueCap int) *Pool {
	if queueCap < 1 {
		queueCap = 1
	}
	if max < 1 {
		max = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		work:        make(chan Request, queueCap),
		results:     make(chan Result, queueCap),
		retire:      make(chan struct{}, max),
		cancel:      cancel,
		ctx:         ctx,
		minSize:     min,
		maxSize:     max,
		scaleSignal: make(chan struct{}, 1),
	}
	for range initial {
		p.spawnWorker()
	}
	go p.autoScale()
	return p
}

// Submit enqueues a check request. Non-blocking; drops if queue is full.
//
// After enqueue, if the current queue depth exceeds the worker count and we
// have headroom below maxSize, signal autoScale to wake immediately instead
// of waiting up to 5s for its ticker. The signal channel has capacity 1 and
// a non-blocking send, so a burst of submits coalesces into a single nudge.
func (p *Pool) Submit(req Request) bool {
	p.workMu.RLock()
	defer p.workMu.RUnlock()
	if p.closed.Load() {
		return false
	}
	select {
	case p.work <- req:
		p.maybeSignalScale()
		return true
	default:
		return false
	}
}

// maybeSignalScale wakes autoScale early when the queue is growing faster
// than the current worker count can drain. Non-blocking; if a signal is
// already pending the existing one will serve this burst.
func (p *Pool) maybeSignalScale() {
	current := int(p.size.Load())
	if current >= p.maxSize {
		return
	}
	if len(p.work) <= current {
		return
	}
	select {
	case p.scaleSignal <- struct{}{}:
	default:
	}
}

// Results returns the channel on which completed results are delivered.
func (p *Pool) Results() <-chan Result {
	return p.results
}

// QueueDepth returns the number of pending requests.
func (p *Pool) QueueDepth() int {
	return len(p.work)
}

// ResultDepth returns the number of completed checks waiting for the
// orchestrator to process them.
func (p *Pool) ResultDepth() int {
	return len(p.results)
}

// ActiveCount returns the number of goroutines currently running a check.
func (p *Pool) ActiveCount() int {
	return int(p.active.Load())
}

// WorkerCount returns the total number of live worker goroutines.
func (p *Pool) WorkerCount() int {
	return int(p.size.Load())
}

// Drain stops accepting new work and waits for in-flight checks to complete.
//
// The closed flag is flipped under p.mu so it serializes with scale(), which
// checks closed and spawns workers (wg.Add) while holding p.mu. Without this,
// scale() could call wg.Add concurrently with the wg.Wait below — a data race
// and a WaitGroup misuse. Taking p.mu guarantees every wg.Add either completed
// before closed was set (scale ran first) or never happens (scale sees closed
// and returns), so no Add overlaps Wait.
func (p *Pool) Drain() {
	p.mu.Lock()
	swapped := p.closed.CompareAndSwap(false, true)
	p.mu.Unlock()
	if !swapped {
		return
	}
	p.workMu.Lock()
	close(p.work)
	p.workMu.Unlock()
	p.cancel()
	p.wg.Wait()
}

func (p *Pool) spawnWorker() {
	p.size.Add(1)
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer p.size.Add(-1)
		for {
			select {
			case <-p.ctx.Done():
				return
			case <-p.retire:
				return
			case req, ok := <-p.work:
				if !ok {
					return
				}
				p.active.Add(1)
				res := runPoolCheck(context.Background(), req)
				p.active.Add(-1)
				if p.closed.Load() {
					continue
				}
				select {
				case p.results <- res:
				case <-p.ctx.Done():
					return
				}
			}
		}
	}()
}

func runPoolCheck(ctx context.Context, req Request) (res Result) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err := fmt.Errorf("checker panic: %v", recovered)
			log.Printf("checker: recovered panic blog_id=%d url=%q: %v\n%s", req.BlogID, req.URL, recovered, debug.Stack())
			res = Result{
				MonitorSiteID:    req.MonitorSiteID,
				BlogID:           req.BlogID,
				URL:              req.URL,
				Method:           req.Method,
				DetectionProfile: req.DetectionProfile,
				Success:          false,
				ErrorCode:        ErrorInternal,
				ErrorDetail:      boundedErrorDetail(err),
				Timestamp:        time.Now().UTC(),
			}
		}
	}()
	return poolCheckFunc(ctx, req)
}

// autoScale adjusts the pool size every 5 seconds based on queue depth and
// process memory usage. It also wakes early when Submit signals that the
// queue is growing faster than the current worker count, so a flash backlog
// gets workers in microseconds rather than up to 5 seconds.
func (p *Pool) autoScale() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.scale()
		case <-p.scaleSignal:
			p.scale()
		}
	}
}

func (p *Pool) scale() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed.Load() {
		return
	}

	current := int(p.size.Load())
	queue := len(p.work)

	// Scale up: queue depth exceeds current worker count.
	if queue > current && current < p.maxSize {
		add := min(queue-current, p.maxSize-current)
		for range add {
			p.spawnWorker()
		}
		return
	}

	// Scale down gradually when demand is low or the max size has been lowered.
	if current > p.maxSize {
		p.retireWorkers(current - p.maxSize)
		return
	}
	if queue == 0 && current > p.minSize {
		p.retireWorkers(1)
	}
}

// SetMaxSize updates the autoscaler ceiling after config reload.
func (p *Pool) SetMaxSize(max int) {
	if max < 1 {
		max = 1
	}
	p.mu.Lock()
	p.maxSize = max
	current := int(p.size.Load())
	if current > p.maxSize {
		p.retireWorkers(current - p.maxSize)
	}
	p.mu.Unlock()
}

// SetSizeBounds updates the autoscaler floor and ceiling together. If the
// current worker count is below the new floor, workers are started immediately;
// if it is above the new ceiling, excess workers are retired gracefully.
func (p *Pool) SetSizeBounds(minSize, maxSize int) int {
	if maxSize < 1 {
		maxSize = 1
	}
	if minSize < 1 {
		minSize = 1
	}
	if minSize > maxSize {
		minSize = maxSize
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.minSize = minSize
	p.maxSize = maxSize

	current := int(p.size.Load())
	if current > p.maxSize {
		p.retireWorkers(current - p.maxSize)
		return 0
	}
	if current >= p.minSize || p.closed.Load() {
		return 0
	}
	added := p.minSize - current
	for range added {
		p.spawnWorker()
	}
	return added
}

// EnsureSize proactively starts workers up to target, bounded by maxSize.
// The queue-depth autoscaler will still adjust over time, but streaming
// schedulers use this to avoid a cold pool after a large target activation.
func (p *Pool) EnsureSize(target int) int {
	if target < 1 {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed.Load() {
		return 0
	}
	current := int(p.size.Load())
	if target > p.maxSize {
		target = p.maxSize
	}
	if target <= current {
		return 0
	}
	added := target - current
	for range added {
		p.spawnWorker()
	}
	return added
}

// DrainWorkers gracefully reduces the pool size by up to n idle workers.
func (p *Pool) DrainWorkers(n int) int {
	if n < 1 {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.retireWorkers(n)
}

func (p *Pool) retireWorkers(n int) int {
	if n < 1 {
		return 0
	}
	current := int(p.size.Load())
	available := current - p.minSize
	if available < 1 {
		return 0
	}
	if n > available {
		n = available
	}
	retired := 0
	for range n {
		select {
		case p.retire <- struct{}{}:
			retired++
		default:
			return retired
		}
	}
	return retired
}

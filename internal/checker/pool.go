package checker

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

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
}

// NewPool creates a Pool with the given initial, min, and max worker counts.
func NewPool(initial, min, max int) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		work:    make(chan Request, max*2),
		results: make(chan Result, max*2),
		retire:  make(chan struct{}, max),
		cancel:  cancel,
		ctx:     ctx,
		minSize: min,
		maxSize: max,
	}
	for range initial {
		p.spawnWorker()
	}
	go p.autoScale()
	return p
}

// Submit enqueues a check request. Non-blocking; drops if queue is full.
func (p *Pool) Submit(req Request) bool {
	p.workMu.RLock()
	defer p.workMu.RUnlock()
	if p.closed.Load() {
		return false
	}
	select {
	case p.work <- req:
		return true
	default:
		return false
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

// ActiveCount returns the number of goroutines currently running a check.
func (p *Pool) ActiveCount() int {
	return int(p.active.Load())
}

// WorkerCount returns the total number of live worker goroutines.
func (p *Pool) WorkerCount() int {
	return int(p.size.Load())
}

// Drain stops accepting new work and waits for in-flight checks to complete.
func (p *Pool) Drain() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	p.workMu.Lock()
	close(p.work)
	p.workMu.Unlock()
	p.wg.Wait()
	p.cancel()
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
				res := Check(context.Background(), req)
				p.active.Add(-1)
				if p.closed.Load() {
					continue
				}
				select {
				case p.results <- res:
				case <-p.ctx.Done():
					return
				default:
					// Avoid deadlocking shutdown if the result consumer has stopped.
				}
			}
		}
	}()
}

// autoScale adjusts the pool size every 5 seconds based on queue depth and
// process memory usage.
func (p *Pool) autoScale() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
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

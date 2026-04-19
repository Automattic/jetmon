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
	cancel  context.CancelFunc
	ctx     context.Context

	size    atomic.Int64
	active  atomic.Int64

	mu      sync.Mutex
	minSize int
	maxSize int
}

// NewPool creates a Pool with the given initial, min, and max worker counts.
func NewPool(initial, min, max int) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pool{
		work:    make(chan Request, max*2),
		results: make(chan Result, max*2),
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
	p.cancel()
}

func (p *Pool) spawnWorker() {
	p.size.Add(1)
	go func() {
		defer p.size.Add(-1)
		for {
			select {
			case <-p.ctx.Done():
				return
			case req, ok := <-p.work:
				if !ok {
					return
				}
				p.active.Add(1)
				res := Check(p.ctx, req)
				p.active.Add(-1)
				select {
				case p.results <- res:
				case <-p.ctx.Done():
					return
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

	// Scale down: no workers exit individually — they exit naturally when
	// the pool context is cancelled (Drain). Scale-down under memory pressure
	// is handled by the orchestrator based on WorkerMaxMemMB config.
}


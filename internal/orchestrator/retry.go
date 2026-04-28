package orchestrator

import (
	"sync"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
)

// retryEntry tracks local retry state for a site that has failed at least once.
type retryEntry struct {
	blogID      int64
	url         string
	failCount   int
	firstFailAt time.Time
	lastResult  checker.Result
	checks      []checker.Result // all check results since first failure
	eventID     int64            // jetmon_events.id for the open Seems Down event; 0 if not yet opened or eventstore unavailable
}

// retryQueue holds sites awaiting local retry or veriflier escalation.
// It persists between rounds — never flushed at round start.
type retryQueue struct {
	mu      sync.Mutex
	entries map[int64]*retryEntry
}

func newRetryQueue() *retryQueue {
	return &retryQueue{entries: make(map[int64]*retryEntry)}
}

// record adds a failed check result to the queue. Returns the updated entry.
func (q *retryQueue) record(res checker.Result) *retryEntry {
	q.mu.Lock()
	defer q.mu.Unlock()

	e, exists := q.entries[res.BlogID]
	if !exists {
		e = &retryEntry{
			blogID:      res.BlogID,
			url:         res.URL,
			firstFailAt: res.Timestamp,
		}
		q.entries[res.BlogID] = e
	}
	e.failCount++
	e.lastResult = res
	e.checks = append(e.checks, res)
	return e
}

// clear removes a site from the retry queue (site recovered or confirmed down).
func (q *retryQueue) clear(blogID int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.entries, blogID)
}

// get returns the entry for a site, or nil if not in the queue.
func (q *retryQueue) get(blogID int64) *retryEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.entries[blogID]
}

// allBlogIDs returns the blog IDs of all sites currently in retry.
func (q *retryQueue) allBlogIDs() []int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	ids := make([]int64, 0, len(q.entries))
	for id := range q.entries {
		ids = append(ids, id)
	}
	return ids
}

// size returns the number of sites in the queue.
func (q *retryQueue) size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries)
}

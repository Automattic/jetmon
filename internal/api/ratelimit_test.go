package api

import (
	"testing"
	"time"
)

// fakeClock returns a controllable time source for deterministic rate-limit
// tests. We can't use real time.Now() because elapsed-time math would make
// tests flaky on slow CI.
type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time { return c.t }

func newTestLimiter(c *fakeClock) *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[int64]*rateBucket),
		now:     c.now,
	}
	// Don't start the GC loop in tests.
	return rl
}

func TestRateLimiterAllowsUntilExhausted(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rl := newTestLimiter(clock)

	// Limit = 5/min. First five requests pass.
	for i := 0; i < 5; i++ {
		allowed, _, _ := rl.allow(42, 5)
		if !allowed {
			t.Fatalf("request %d should have been allowed", i+1)
		}
	}
	// Sixth is denied.
	allowed, remaining, _ := rl.allow(42, 5)
	if allowed {
		t.Fatal("sixth request should have been denied")
	}
	if remaining != 0 {
		t.Errorf("remaining = %d, want 0", remaining)
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rl := newTestLimiter(clock)

	// Exhaust a 60/min bucket → 60 tokens.
	for i := 0; i < 60; i++ {
		allowed, _, _ := rl.allow(7, 60)
		if !allowed {
			t.Fatalf("request %d should have been allowed in burst", i+1)
		}
	}
	// 61st denied.
	allowed, _, _ := rl.allow(7, 60)
	if allowed {
		t.Fatal("burst exhausted should deny")
	}

	// Advance 1 second — at 60/min that's 1 token refilled.
	clock.t = clock.t.Add(time.Second)
	allowed, _, _ = rl.allow(7, 60)
	if !allowed {
		t.Fatal("after 1s with 60/min limit, one token should have refilled")
	}
}

func TestRateLimiterIsolatesKeys(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rl := newTestLimiter(clock)

	// Exhaust key 1.
	for i := 0; i < 2; i++ {
		rl.allow(1, 2)
	}
	if allowed, _, _ := rl.allow(1, 2); allowed {
		t.Fatal("key 1 should be exhausted")
	}
	// Key 2 unaffected.
	if allowed, _, _ := rl.allow(2, 2); !allowed {
		t.Fatal("key 2 should not be affected by key 1's bucket")
	}
}

func TestRateLimiterResizeOnLimitChange(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rl := newTestLimiter(clock)

	// Use 5 of a 10-token bucket.
	for i := 0; i < 5; i++ {
		rl.allow(1, 10)
	}
	// Operator drops the limit to 3 (e.g. via key edit). Bucket should
	// shrink — caller can't have 5 tokens left under a 3-token cap.
	allowed, remaining, _ := rl.allow(1, 3)
	if !allowed {
		t.Fatal("first request after resize should still allow")
	}
	if remaining > 3 {
		t.Errorf("remaining = %d, want <= 3 after resize", remaining)
	}
}

func TestRateLimiterGCDropsStaleBuckets(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rl := newTestLimiter(clock)

	rl.allow(1, 60)
	rl.allow(2, 60)

	// Advance past the GC threshold.
	clock.t = clock.t.Add(20 * time.Minute)
	rl.gc(10 * time.Minute)

	if len(rl.buckets) != 0 {
		t.Errorf("expected GC to drop stale buckets, %d remain", len(rl.buckets))
	}
}

func TestRateLimiterResetTimeIsFuture(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	rl := newTestLimiter(clock)

	// Exhaust a 60/min bucket so deficit is real.
	for i := 0; i < 60; i++ {
		rl.allow(1, 60)
	}
	_, _, resetAt := rl.allow(1, 60)
	if !resetAt.After(clock.now()) {
		t.Errorf("reset time %v should be after now %v", resetAt, clock.now())
	}
}

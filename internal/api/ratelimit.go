package api

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// rateLimiter is an in-memory per-key token bucket. Each key gets its own
// bucket sized to the key's rate_limit_per_minute. Tokens refill continuously
// at limit/60 per second (so 60/min ≈ 1 token per second, smoothly).
//
// In-memory state is fine for this internal API — there's currently one
// jetmon2 instance per host, and the gateway in front handles cross-instance
// fairness. If we ever scale the API horizontally, this moves to Redis.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[int64]*rateBucket
	now     func() time.Time // injectable for tests
}

// rateBucket is a token bucket for a single key. tokens is fractional so
// short bursts above the per-minute average are possible (the bucket fills to
// `limit` tokens at full size).
type rateBucket struct {
	tokens     float64
	limit      float64
	lastRefill time.Time
}

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[int64]*rateBucket),
		now:     time.Now,
	}
	go rl.gcLoop()
	return rl
}

// allow consumes one token from the key's bucket. Returns whether the request
// is allowed, the remaining tokens (rounded down for the header), and the
// next refill instant for X-RateLimit-Reset and Retry-After.
func (rl *rateLimiter) allow(keyID int64, perMinute int) (allowed bool, remaining int, resetAt time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	b, ok := rl.buckets[keyID]
	if !ok {
		b = &rateBucket{
			tokens:     float64(perMinute),
			limit:      float64(perMinute),
			lastRefill: now,
		}
		rl.buckets[keyID] = b
	}

	// If the configured limit changed (key was rotated/edited), resize the
	// bucket. Don't shrink tokens past the new ceiling.
	if b.limit != float64(perMinute) {
		b.limit = float64(perMinute)
		if b.tokens > b.limit {
			b.tokens = b.limit
		}
	}

	// Refill based on elapsed time since last refill. Rate is limit/60 per second.
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.limit / 60.0
		if b.tokens > b.limit {
			b.tokens = b.limit
		}
		b.lastRefill = now
	}

	// Reset is "when the bucket would be full again from current state". We
	// expose this for X-RateLimit-Reset and Retry-After. For a non-empty
	// bucket the reset time is in the past (already at limit); we clamp to now
	// + 1s in that case so the header is meaningful.
	deficit := b.limit - b.tokens
	secondsToFull := deficit * 60.0 / b.limit
	resetAt = now.Add(time.Duration(secondsToFull * float64(time.Second)))
	if resetAt.Before(now) {
		resetAt = now.Add(time.Second)
	}

	if b.tokens < 1.0 {
		// Not enough tokens for this request.
		return false, int(b.tokens), resetAt
	}
	b.tokens -= 1.0
	return true, int(b.tokens), resetAt
}

// gcLoop drops buckets that haven't been touched in 10 minutes so the map
// doesn't grow unbounded as keys come and go.
func (rl *rateLimiter) gcLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.gc(10 * time.Minute)
	}
}

func (rl *rateLimiter) gc(maxIdle time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := rl.now().Add(-maxIdle)
	for id, b := range rl.buckets {
		if b.lastRefill.Before(cutoff) {
			delete(rl.buckets, id)
		}
	}
}

// writeRateLimitHeaders sets the standard X-RateLimit-{Limit,Remaining,Reset}
// headers on a response. Reset is unix seconds.
//
// Note: Go's net/http canonicalizes header names to "X-Ratelimit-Limit"
// (lowercase after the second segment) on the wire. This is RFC 7230 compliant
// — HTTP header names are case-insensitive — but the IETF draft for these
// headers uses the camelCase form. Bypassing canonicalization in stdlib
// requires direct map access (h[key] = value) and breaks http.Header.Get
// case-insensitive lookups for downstream Go consumers, so we accept the
// canonicalized form. Most clients (curl, fetch, requests) do
// case-insensitive header lookup and don't care.
func writeRateLimitHeaders(w http.ResponseWriter, limit, remaining int, resetAt time.Time) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt.Unix(), 10))
}

// writeRateLimited writes a 429 response with Retry-After and the standard
// rate limit headers. Used by the middleware when the limiter rejects.
func writeRateLimited(w http.ResponseWriter, r *http.Request, limit, remaining int, resetAt time.Time) {
	writeRateLimitHeaders(w, limit, remaining, resetAt)
	retryAfter := int(time.Until(resetAt).Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeError(w, r, http.StatusTooManyRequests, "rate_limited",
		fmt.Sprintf("rate limit exceeded; retry after %d seconds", retryAfter))
}

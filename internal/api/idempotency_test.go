package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/apikeys"
)

func TestIdempotencyHashStable(t *testing.T) {
	a := hashBytes([]byte(`{"foo":1}`))
	b := hashBytes([]byte(`{"foo":1}`))
	if a != b {
		t.Fatal("hashBytes is non-deterministic")
	}
	if len(a) != 64 {
		t.Fatalf("hashBytes length = %d, want 64 (sha256 hex)", len(a))
	}
}

func TestIdempotencyStoreLookupAndStore(t *testing.T) {
	store := newIdempotencyStore()
	store.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	k := idempotencyKey{keyID: 1, key: "abc"}
	if got := store.lookup(k); got != nil {
		t.Fatal("empty store should return nil")
	}
	entry := &idempotencyEntry{
		bodyHash:   "h1",
		status:     200,
		respHeader: http.Header{"Content-Type": []string{"application/json"}},
		respBody:   []byte(`{"ok":true}`),
		expiresAt:  store.now().Add(idempotencyTTL),
	}
	store.store(k, entry)
	got := store.lookup(k)
	if got == nil {
		t.Fatal("entry should be retrievable")
	}
	if got.status != 200 || got.bodyHash != "h1" {
		t.Errorf("retrieved entry mismatched: %+v", got)
	}
}

func TestIdempotencyStoreExpires(t *testing.T) {
	store := newIdempotencyStore()
	store.now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	k := idempotencyKey{keyID: 1, key: "abc"}
	store.store(k, &idempotencyEntry{
		expiresAt: store.now().Add(-time.Hour), // already expired
	})
	if got := store.lookup(k); got != nil {
		t.Fatal("expired entry should be invisible to lookup")
	}
}

func TestIdempotencyStoreGCRemovesExpiredEntries(t *testing.T) {
	store := newIdempotencyStore()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	expired := idempotencyKey{keyID: 1, key: "expired"}
	live := idempotencyKey{keyID: 1, key: "live"}
	store.store(expired, &idempotencyEntry{expiresAt: now.Add(-time.Second)})
	store.store(live, &idempotencyEntry{expiresAt: now.Add(time.Hour)})

	store.gc()

	if _, ok := store.entries[expired]; ok {
		t.Fatal("expired entry survived gc")
	}
	if _, ok := store.entries[live]; !ok {
		t.Fatal("live entry removed by gc")
	}
}

// bodyReader wraps a byte slice as an http.Request.Body.
func bodyReader(b []byte) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(b))
}

func TestIdempotencyMiddlewarePassthroughWhenNoHeader(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	called := 0
	wrapped := s.withIdempotency(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created":true}`))
	})

	req := requestWithKey("POST", "/", key)
	req.Body = bodyReader(nil)
	rec := httptest.NewRecorder()
	wrapped(rec, req)
	if called != 1 || rec.Code != http.StatusCreated {
		t.Fatalf("first call: called=%d code=%d", called, rec.Code)
	}

	// Second call without idempotency key should run again.
	req2 := requestWithKey("POST", "/", key)
	req2.Body = bodyReader(nil)
	rec2 := httptest.NewRecorder()
	wrapped(rec2, req2)
	if called != 2 {
		t.Fatalf("second call: handler called=%d, want 2", called)
	}
}

func TestIdempotencyMiddlewareCachesAndReplays(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	called := 0
	wrapped := s.withIdempotency(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42}`))
	})

	body := []byte(`{"foo":1}`)
	req := requestWithKey("POST", "/", key)
	req.Header.Set(idempotencyHeader, "key-1")
	req.Body = bodyReader(body)
	rec := httptest.NewRecorder()
	wrapped(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first call status = %d, want 201", rec.Code)
	}

	// Second call with same key + same body: handler must not run again.
	req2 := requestWithKey("POST", "/", key)
	req2.Header.Set(idempotencyHeader, "key-1")
	req2.Body = bodyReader(body)
	rec2 := httptest.NewRecorder()
	wrapped(rec2, req2)
	if called != 1 {
		t.Fatalf("handler called %d times, want 1 (replay)", called)
	}
	if rec2.Code != http.StatusCreated {
		t.Fatalf("replay status = %d, want 201", rec2.Code)
	}
	if got := rec2.Header().Get("Idempotency-Replayed"); got != "true" {
		t.Errorf("Idempotency-Replayed = %q, want true", got)
	}
	if got := rec2.Body.String(); got != `{"id":42}` {
		t.Errorf("replayed body = %q, want %q", got, `{"id":42}`)
	}
}

func TestIdempotencyMiddlewareConflictOnDifferentBody(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	called := 0
	wrapped := s.withIdempotency(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusCreated)
	})

	req := requestWithKey("POST", "/", key)
	req.Header.Set(idempotencyHeader, "key-1")
	req.Body = bodyReader([]byte(`{"foo":1}`))
	wrapped(httptest.NewRecorder(), req)
	if called != 1 {
		t.Fatalf("first call: handler called=%d, want 1", called)
	}

	req2 := requestWithKey("POST", "/", key)
	req2.Header.Set(idempotencyHeader, "key-1")
	req2.Body = bodyReader([]byte(`{"foo":2}`)) // different body, same key
	rec2 := httptest.NewRecorder()
	wrapped(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d (body=%s)", rec2.Code, rec2.Body.String())
	}
	if called != 1 {
		t.Fatalf("handler should not run on conflict; called=%d", called)
	}
	body := readErrorBody(t, rec2.Body)
	if body.Code != "idempotency_conflict" {
		t.Errorf("error code = %q, want idempotency_conflict", body.Code)
	}
}

func TestIdempotencyMiddlewareIsolatesByKeyID(t *testing.T) {
	// Two different API keys with the same idempotency string don't share
	// cached entries — the cache key includes the API key id.
	s, _, _, cleanup := newTestServer(t)
	defer cleanup()

	k1 := &apikeys.Key{ID: 1, ConsumerName: "consumer-a", Scope: apikeys.ScopeWrite, RateLimitPerMinute: 60}
	k2 := &apikeys.Key{ID: 2, ConsumerName: "consumer-b", Scope: apikeys.ScopeWrite, RateLimitPerMinute: 60}

	calledA := 0
	calledB := 0
	wrappedA := s.withIdempotency(func(w http.ResponseWriter, r *http.Request) {
		calledA++
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`A`))
	})
	wrappedB := s.withIdempotency(func(w http.ResponseWriter, r *http.Request) {
		calledB++
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`B`))
	})

	body := []byte(`{}`)
	rA := requestWithKey("POST", "/", k1)
	rA.Header.Set(idempotencyHeader, "shared")
	rA.Body = bodyReader(body)
	wrappedA(httptest.NewRecorder(), rA)

	rB := requestWithKey("POST", "/", k2)
	rB.Header.Set(idempotencyHeader, "shared")
	rB.Body = bodyReader(body)
	rec := httptest.NewRecorder()
	wrappedB(rec, rB)

	if calledA != 1 || calledB != 1 {
		t.Fatalf("each consumer's handler should run once; got A=%d B=%d", calledA, calledB)
	}
	if got := rec.Body.String(); got != "B" {
		t.Errorf("consumer B got %q, want B (cache should not bleed across keys)", got)
	}
}

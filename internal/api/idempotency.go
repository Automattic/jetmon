package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"sync"
	"time"
)

// idempotencyTTL is how long a cached response is replayable. Stripe uses
// 24h; we match that — long enough for a retry storm to settle, short enough
// that the in-memory store doesn't grow without bound.
const idempotencyTTL = 24 * time.Hour

// idempotencyHeader is the request header consumers send to make a request
// retry-safe. Stripe-style.
const idempotencyHeader = "Idempotency-Key"

// idempotencyKey identifies a stored response uniquely. Scoped by API key id
// so two consumers can't collide on the same opaque value.
type idempotencyKey struct {
	keyID int64
	key   string
}

// idempotencyEntry is the cached response. We replay status, headers, and
// body verbatim. bodyHash distinguishes "same key, same request" (replay) from
// "same key, different request" (409 conflict).
type idempotencyEntry struct {
	bodyHash   string
	status     int
	respHeader http.Header
	respBody   []byte
	expiresAt  time.Time
}

// idempotencyStore is an in-memory store with periodic GC. State is bound to
// this jetmon2 instance; a multi-instance deployment would need Redis or a
// dedicated table. For the current single-instance internal API that's
// adequate.
type idempotencyStore struct {
	mu      sync.Mutex
	entries map[idempotencyKey]*idempotencyEntry
	now     func() time.Time
}

func newIdempotencyStore() *idempotencyStore {
	s := &idempotencyStore{
		entries: make(map[idempotencyKey]*idempotencyEntry),
		now:     time.Now,
	}
	go s.gcLoop()
	return s
}

// lookup returns the cached entry if present and not expired. The caller is
// expected to compare the request body hash to entry.bodyHash to decide
// between replay and 409.
func (s *idempotencyStore) lookup(k idempotencyKey) *idempotencyEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[k]
	if !ok {
		return nil
	}
	if s.now().After(e.expiresAt) {
		delete(s.entries, k)
		return nil
	}
	return e
}

// store records a response under the idempotency key. Overwrites any existing
// entry for the key (which shouldn't happen in normal flow — entries are only
// stored after a successful handler run).
func (s *idempotencyStore) store(k idempotencyKey, e *idempotencyEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[k] = e
}

func (s *idempotencyStore) gcLoop() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		s.gc()
	}
}

func (s *idempotencyStore) gc() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, e := range s.entries {
		if now.After(e.expiresAt) {
			delete(s.entries, k)
		}
	}
}

// idempotencyResponseWriter buffers response writes so we can record the
// final status, headers, and body for replay. The wrapped writer is what
// the handler sees; the stored response is what we'll replay on a retry.
type idempotencyResponseWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
	wrote  bool
}

func (w *idempotencyResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.status = status
	w.wrote = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *idempotencyResponseWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

// withIdempotency wraps a handler so that if the request carries an
// Idempotency-Key header, the response is cached and replayed on retries
// with the same body. Stateless / unused if the header is absent.
//
// Usage: only wrap POST endpoints (and any other side-effecting verbs)
// where retries can otherwise duplicate work. GETs don't need it.
func (s *Server) withIdempotency(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idemKey := r.Header.Get(idempotencyHeader)
		if idemKey == "" {
			// No idempotency requested — pass through.
			h(w, r)
			return
		}
		key := keyFromRequest(r)
		if key == nil {
			// requireScope must have already run — this path is unreachable
			// in production. Defensive 500 rather than nil-deref.
			writeError(w, r, http.StatusInternalServerError, "auth_state_missing",
				"idempotency middleware: authenticated key not in context")
			return
		}

		// Read the body so we can both hash it (for conflict detection) and
		// re-supply it to the handler. Body size is bounded by the server's
		// ReadTimeout; a future MaxBytesReader would tighten this.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_body",
				"failed to read request body: "+err.Error())
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		bodyHash := hashBytes(body)

		ik := idempotencyKey{keyID: key.ID, key: idemKey}
		if cached := s.idempotency.lookup(ik); cached != nil {
			if cached.bodyHash != bodyHash {
				writeError(w, r, http.StatusConflict, "idempotency_conflict",
					"the idempotency key was previously used with a different request body")
				return
			}
			replayCached(w, cached)
			return
		}

		// Capture the response so we can store it.
		rec := &idempotencyResponseWriter{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)

		// Only cache successful and client-error responses (2xx and 4xx).
		// Server errors (5xx) shouldn't be replayed — the consumer should
		// retry and we should re-attempt the operation.
		if rec.status >= 200 && rec.status < 500 {
			headerCopy := http.Header{}
			for k, v := range w.Header() {
				headerCopy[k] = append([]string(nil), v...)
			}
			s.idempotency.store(ik, &idempotencyEntry{
				bodyHash:   bodyHash,
				status:     rec.status,
				respHeader: headerCopy,
				respBody:   append([]byte(nil), rec.body.Bytes()...),
				expiresAt:  time.Now().Add(idempotencyTTL),
			})
		}
	}
}

// replayCached writes a previously cached response verbatim. Adds an
// Idempotency-Replayed: true header so consumers can tell when a response
// is from the cache vs freshly computed (debugging aid).
func replayCached(w http.ResponseWriter, e *idempotencyEntry) {
	for k, v := range e.respHeader {
		w.Header()[k] = v
	}
	w.Header().Set("Idempotency-Replayed", "true")
	w.WriteHeader(e.status)
	_, _ = w.Write(e.respBody)
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

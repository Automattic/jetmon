package wpcom

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := &Client{
		authToken:  "test-token",
		notifyURL:  srv.URL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
	return c, srv.Close
}

func testNotification(blogID int64) Notification {
	return Notification{BlogID: blogID, MonitorURL: "https://example.com", StatusType: "success"}
}

func TestNotifySuccess(t *testing.T) {
	c, close := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization header = %q, want Bearer test-token", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	})
	defer close()

	if err := c.Notify(testNotification(1)); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if c.IsCircuitOpen() {
		t.Fatal("circuit should be closed after success")
	}
	if c.failures != 0 {
		t.Fatalf("failures = %d, want 0", c.failures)
	}
}

func TestNotifyResetsFailureCountOnSuccess(t *testing.T) {
	calls := 0
	c, close := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})
	defer close()

	_ = c.Notify(testNotification(1))
	_ = c.Notify(testNotification(2))
	if c.failures != 2 {
		t.Fatalf("failures = %d, want 2", c.failures)
	}

	_ = c.Notify(testNotification(3))
	if c.failures != 0 {
		t.Fatalf("failures after success = %d, want 0", c.failures)
	}
}

func TestNotifyOpensCircuitAfterMaxFailures(t *testing.T) {
	c, close := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	defer close()

	for range cbMaxFailures {
		_ = c.Notify(testNotification(1))
	}

	if !c.IsCircuitOpen() {
		t.Fatal("circuit should be open after max failures")
	}
}

func TestNotifyQueuesAndReturnsErrorWhenCircuitOpen(t *testing.T) {
	c, close := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer close()

	c.circuitOpen = true
	c.circuitOpenAt = time.Now()

	err := c.Notify(testNotification(42))
	if err == nil {
		t.Fatal("Notify() expected error when circuit is open")
	}
	if c.QueueDepth() != 1 {
		t.Fatalf("QueueDepth() = %d, want 1", c.QueueDepth())
	}
}

func TestNotifyResetsCircuitAfterTimeout(t *testing.T) {
	var flushed []int64
	c, close := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var n Notification
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		flushed = append(flushed, n.BlogID)
		w.WriteHeader(http.StatusOK)
	})
	defer close()

	// Open circuit and pre-load a queued notification.
	c.circuitOpen = true
	c.circuitOpenAt = time.Now().Add(-(cbResetTimeout + time.Second))
	c.failures = cbMaxFailures
	c.queue = []queuedNotification{{n: testNotification(99)}}
	_ = flushed

	// Next Notify call should reset the circuit and flush the queue.
	err := c.Notify(testNotification(1))
	if err != nil {
		t.Fatalf("Notify() after timeout error = %v", err)
	}
	if c.IsCircuitOpen() {
		t.Fatal("circuit should be closed after reset timeout")
	}
	if c.QueueDepth() != 0 {
		t.Fatalf("QueueDepth() = %d, want 0 after flush", c.QueueDepth())
	}
	if !slices.Equal(flushed, []int64{99, 1}) {
		t.Fatalf("flushed notifications = %v, want [99 1]", flushed)
	}
}

func TestNew(t *testing.T) {
	c := New("my-token", "my-host")
	if c == nil {
		t.Fatal("New() = nil")
	}
	if c.authToken != "my-token" {
		t.Fatalf("authToken = %q, want my-token", c.authToken)
	}
	if c.hostname != "my-host" {
		t.Fatalf("hostname = %q, want my-host", c.hostname)
	}
}

func TestSendFlushContinuesAfterError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := &Client{
		authToken:  "test-token",
		notifyURL:  srv.URL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}

	c.sendFlush([]queuedNotification{
		{n: testNotification(1)},
		{n: testNotification(2)},
	})

	if calls != 2 {
		t.Fatalf("send calls = %d, want 2 (flush should continue after first error)", calls)
	}
}

func TestSendFlushEmptyIsNoop(t *testing.T) {
	c := &Client{}
	c.sendFlush(nil)
	c.sendFlush([]queuedNotification{})
}

func TestNotifySendNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // close before sending — forces a connection error

	c := &Client{
		authToken:  "token",
		notifyURL:  url,
		httpClient: &http.Client{Timeout: time.Second},
	}

	err := c.Notify(testNotification(1))
	if err == nil {
		t.Fatal("Notify() expected error for closed server")
	}
	if c.failures != 1 {
		t.Fatalf("failures = %d after network error, want 1", c.failures)
	}
}

func TestEnqueueDropsOldestWhenFull(t *testing.T) {
	c := &Client{}
	for i := range queueMaxSize {
		c.enqueue(Notification{BlogID: int64(i)})
	}
	if c.QueueDepth() != queueMaxSize {
		t.Fatalf("QueueDepth() = %d, want %d", c.QueueDepth(), queueMaxSize)
	}

	c.enqueue(Notification{BlogID: queueMaxSize})

	if c.QueueDepth() != queueMaxSize {
		t.Fatalf("QueueDepth() after overflow = %d, want %d", c.QueueDepth(), queueMaxSize)
	}
	if c.queue[0].n.BlogID != 1 {
		t.Fatalf("oldest entry BlogID = %d, want 1 (entry 0 should have been dropped)", c.queue[0].n.BlogID)
	}
}

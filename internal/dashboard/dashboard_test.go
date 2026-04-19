package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleState(t *testing.T) {
	srv := New("test-host")
	srv.Update(State{WorkerCount: 5, QueueDepth: 3})

	r := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	w := httptest.NewRecorder()
	srv.handleState(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var st State
	if err := json.NewDecoder(w.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.WorkerCount != 5 {
		t.Fatalf("WorkerCount = %d, want 5", st.WorkerCount)
	}
	if st.Hostname != "test-host" {
		t.Fatalf("Hostname = %q, want test-host", st.Hostname)
	}
}

func TestHandleHealth(t *testing.T) {
	srv := New("test-host")
	srv.UpdateHealth([]HealthEntry{
		{Name: "db", Status: "green"},
		{Name: "wpcom", Status: "amber"},
	})

	r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var entries []HealthEntry
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries len = %d, want 2", len(entries))
	}
	if entries[0].Name != "db" || entries[0].Status != "green" {
		t.Fatalf("entries[0] = %+v, want {db green}", entries[0])
	}
}

func TestHandleIndex(t *testing.T) {
	srv := New("test-host")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.handleIndex(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/html; charset=utf-8", ct)
	}
	if !strings.Contains(w.Body.String(), "Jetmon") {
		t.Fatal("body does not contain expected HTML content")
	}
}

func TestUpdateSetsHostnameAndTimestamp(t *testing.T) {
	srv := New("my-host")
	srv.Update(State{WorkerCount: 7, QueueDepth: 2})

	srv.mu.RLock()
	st := srv.state
	srv.mu.RUnlock()

	if st.Hostname != "my-host" {
		t.Fatalf("Hostname = %q, want my-host", st.Hostname)
	}
	if st.WorkerCount != 7 {
		t.Fatalf("WorkerCount = %d, want 7", st.WorkerCount)
	}
	if st.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero after Update")
	}
}

func TestUpdateHealthStoresEntries(t *testing.T) {
	srv := New("test-host")
	srv.UpdateHealth([]HealthEntry{{Name: "redis", Status: "red"}})

	srv.mu.RLock()
	h := srv.health
	srv.mu.RUnlock()

	if len(h) != 1 || h[0].Name != "redis" {
		t.Fatalf("health = %v, want [{redis red}]", h)
	}
}

func TestBroadcastDeliverstToSSEClients(t *testing.T) {
	srv := New("test-host")

	ch := make(chan string, 1)
	id := "test-client"
	srv.sseMu.Lock()
	srv.sseClients[id] = ch
	srv.sseMu.Unlock()

	srv.broadcast(State{WorkerCount: 9})

	select {
	case msg := <-ch:
		var st State
		if err := json.Unmarshal([]byte(msg), &st); err != nil {
			t.Fatalf("unmarshal broadcast: %v", err)
		}
		if st.WorkerCount != 9 {
			t.Fatalf("WorkerCount = %d, want 9", st.WorkerCount)
		}
	default:
		t.Fatal("no message received by SSE client")
	}
}

func TestHandleSSESendsInitialStateAndCleanup(t *testing.T) {
	srv := New("test-host")
	srv.Update(State{WorkerCount: 7})

	mux := http.NewServeMux()
	mux.HandleFunc("/events", srv.handleSSE)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var event strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE line: %v", err)
		}
		event.WriteString(line)
		if line == "\n" {
			break
		}
	}
	data := event.String()
	if !strings.Contains(data, "data:") {
		t.Fatalf("initial SSE response = %q, want data: prefix", data)
	}

	// Disconnect the client — handleSSE should return via r.Context().Done().
	cancel()
}

func TestBroadcastDropsOnSlowClient(t *testing.T) {
	srv := New("test-host")

	// Channel capacity 0 — client is always "slow".
	ch := make(chan string)
	srv.sseMu.Lock()
	srv.sseClients["slow"] = ch
	srv.sseMu.Unlock()

	// Should not block.
	srv.broadcast(State{WorkerCount: 1})
}

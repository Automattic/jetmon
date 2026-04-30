package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHandleState(t *testing.T) {
	srv := New("test-host")
	srv.Update(State{
		WorkerCount:                   5,
		QueueDepth:                    3,
		BucketOwnership:               "pinned range=0-99",
		LegacyStatusProjectionEnabled: true,
		DeliveryWorkersEnabled:        true,
		DeliveryConfigEligible:        true,
		DeliveryOwnerHost:             "api-1",
		RolloutPreflightCommand:       "./jetmon2 rollout pinned-check",
		RolloutCutoverCommand:         "./jetmon2 rollout cutover-check --since=15m",
		RolloutActivityCommand:        "./jetmon2 rollout activity-check --since=15m",
		RolloutRollbackCommand:        "./jetmon2 rollout rollback-check",
		RolloutStateReportCommand:     "./jetmon2 rollout state-report --since=15m",
		ProjectionDriftCommand:        "./jetmon2 rollout projection-drift",
	})

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
	if st.BucketOwnership != "pinned range=0-99" {
		t.Fatalf("BucketOwnership = %q, want pinned range=0-99", st.BucketOwnership)
	}
	if !st.LegacyStatusProjectionEnabled {
		t.Fatal("LegacyStatusProjectionEnabled = false, want true")
	}
	if !st.DeliveryWorkersEnabled {
		t.Fatal("DeliveryWorkersEnabled = false, want true")
	}
	if !st.DeliveryConfigEligible {
		t.Fatal("DeliveryConfigEligible = false, want true")
	}
	if st.DeliveryOwnerHost != "api-1" {
		t.Fatalf("DeliveryOwnerHost = %q, want api-1", st.DeliveryOwnerHost)
	}
	if st.RolloutPreflightCommand != "./jetmon2 rollout pinned-check" {
		t.Fatalf("RolloutPreflightCommand = %q", st.RolloutPreflightCommand)
	}
	if st.RolloutCutoverCommand != "./jetmon2 rollout cutover-check --since=15m" {
		t.Fatalf("RolloutCutoverCommand = %q", st.RolloutCutoverCommand)
	}
	if st.RolloutActivityCommand != "./jetmon2 rollout activity-check --since=15m" {
		t.Fatalf("RolloutActivityCommand = %q", st.RolloutActivityCommand)
	}
	if st.RolloutRollbackCommand != "./jetmon2 rollout rollback-check" {
		t.Fatalf("RolloutRollbackCommand = %q", st.RolloutRollbackCommand)
	}
	if st.RolloutStateReportCommand != "./jetmon2 rollout state-report --since=15m" {
		t.Fatalf("RolloutStateReportCommand = %q", st.RolloutStateReportCommand)
	}
	if st.ProjectionDriftCommand != "./jetmon2 rollout projection-drift" {
		t.Fatalf("ProjectionDriftCommand = %q", st.ProjectionDriftCommand)
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

func TestHandleHostSnapshot(t *testing.T) {
	srv := New("test-host")
	srv.Update(State{
		WorkerCount:            5,
		WPCOMCircuitOpen:       true,
		DeliveryWorkersEnabled: true,
	})
	srv.UpdateHealth([]HealthEntry{
		{Name: "mysql", Status: "green"},
		{Name: "statsd", Status: "amber"},
	})

	r := httptest.NewRequest(http.MethodGet, "/api/host", nil)
	w := httptest.NewRecorder()
	srv.handleHost(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var snapshot HostSnapshot
	if err := json.NewDecoder(w.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snapshot.State.Hostname != "test-host" {
		t.Fatalf("Hostname = %q, want test-host", snapshot.State.Hostname)
	}
	if len(snapshot.Health) != 2 {
		t.Fatalf("health len = %d, want 2", len(snapshot.Health))
	}
	if snapshot.Summary.Status != "red" {
		t.Fatalf("summary status = %q, want red", snapshot.Summary.Status)
	}
	if snapshot.Summary.RedCount == 0 {
		t.Fatalf("summary red count = %d, want non-zero", snapshot.Summary.RedCount)
	}
	if len(snapshot.Summary.Issues) == 0 || !strings.Contains(snapshot.Summary.Issues[0], "wpcom") {
		t.Fatalf("summary issues = %#v, want wpcom issue first", snapshot.Summary.Issues)
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
	if !strings.Contains(w.Body.String(), "id=\"preflight\"") {
		t.Fatal("body does not contain rollout preflight card")
	}
	if !strings.Contains(w.Body.String(), "id=\"cutover\"") {
		t.Fatal("body does not contain rollout cutover command")
	}
	if !strings.Contains(w.Body.String(), "id=\"state-report\"") {
		t.Fatal("body does not contain rollout state report command")
	}
	if !strings.Contains(w.Body.String(), "id=\"activity\"") {
		t.Fatal("body does not contain rollout activity card")
	}
	if !strings.Contains(w.Body.String(), "id=\"rollback\"") {
		t.Fatal("body does not contain rollback card")
	}
	if !strings.Contains(w.Body.String(), "id=\"delivery-owner\"") {
		t.Fatal("body does not contain delivery owner card")
	}
	if !strings.Contains(w.Body.String(), "id=\"delivery-eligible\"") {
		t.Fatal("body does not contain delivery config eligibility card")
	}
	if !strings.Contains(w.Body.String(), "id=\"go-sys\"") {
		t.Fatal("body does not contain Go system memory card")
	}
	if !strings.Contains(w.Body.String(), "id=\"health\"") {
		t.Fatal("body does not contain dependency health grid")
	}
	if !strings.Contains(w.Body.String(), "/api/host") {
		t.Fatal("body does not fetch combined host snapshot")
	}
}

func TestSummarizeHost(t *testing.T) {
	waiting := SummarizeHost(State{UpdatedAt: time.Now()}, nil)
	if waiting.Status != "amber" {
		t.Fatalf("waiting summary status = %q, want amber", waiting.Status)
	}
	if waiting.AmberCount == 0 || len(waiting.Issues) == 0 || !strings.Contains(waiting.Issues[0], "dependency health") {
		t.Fatalf("waiting summary = %+v, want dependency health issue", waiting)
	}

	st := State{UpdatedAt: time.Now(), DeliveryWorkersEnabled: true, DeliveryConfigEligible: true}
	summary := SummarizeHost(st, []HealthEntry{{Name: "mysql", Status: "green"}})
	if summary.Status != "amber" {
		t.Fatalf("summary status = %q, want amber for unset delivery owner", summary.Status)
	}
	if len(summary.Issues) == 0 || !strings.Contains(summary.Issues[0], "DELIVERY_OWNER_HOST") {
		t.Fatalf("summary issues = %#v, want delivery owner issue", summary.Issues)
	}

	st.DeliveryOwnerHost = "host-a"
	summary = SummarizeHost(st, []HealthEntry{{Name: "statsd", Status: "amber", LastError: "not initialized"}, {Name: "mysql", Status: "red", LastError: "access denied"}})
	if summary.Status != "red" {
		t.Fatalf("summary status = %q, want red for dependency failure", summary.Status)
	}
	if len(summary.Issues) < 2 || !strings.HasPrefix(summary.Issues[0], "mysql red") || !strings.HasPrefix(summary.Issues[1], "statsd amber") {
		t.Fatalf("summary issues = %#v, want red issues before amber issues", summary.Issues)
	}

	summary = SummarizeHost(st, []HealthEntry{{Name: "mysql", Status: "green"}})
	if summary.Status != "green" {
		t.Fatalf("summary status = %q, want green", summary.Status)
	}
	if len(summary.Issues) != 0 {
		t.Fatalf("summary issues = %#v, want none", summary.Issues)
	}

	st.DeliveryConfigEligible = false
	summary = SummarizeHost(st, []HealthEntry{{Name: "mysql", Status: "green"}})
	if summary.Status != "amber" || len(summary.Issues) == 0 {
		t.Fatalf("summary = %+v, want amber config mismatch issue", summary)
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

func TestHandleSSERejectsExcessClients(t *testing.T) {
	srv := New("test-host")
	for i := 0; i < maxSSEClients; i++ {
		srv.sseClients[fmt.Sprintf("client-%d", i)] = make(chan string, 1)
	}

	r := httptest.NewRequest(http.MethodGet, "/events", nil)
	w := httptest.NewRecorder()
	srv.handleSSE(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
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

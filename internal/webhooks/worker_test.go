package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestNextRetryDelayFollowsSchedule(t *testing.T) {
	cases := []struct {
		current   int
		want      time.Duration
		abandoned bool
	}{
		{1, 1 * time.Minute, false},
		{2, 5 * time.Minute, false},
		{3, 30 * time.Minute, false},
		{4, 1 * time.Hour, false},
		{5, 6 * time.Hour, false},
		{6, 0, true}, // last attempt failed → abandon
		{7, 0, true}, // beyond max → still abandon (defensive)
	}
	for _, c := range cases {
		got, ab := nextRetryDelay(c.current)
		if ab != c.abandoned {
			t.Errorf("nextRetryDelay(%d).abandoned = %v, want %v", c.current, ab, c.abandoned)
		}
		if !c.abandoned && got != c.want {
			t.Errorf("nextRetryDelay(%d).delay = %v, want %v", c.current, got, c.want)
		}
	}
}

// TestSignatureRoundTrip verifies that consumers can recompute and verify
// the signature we send. This is the contract test — if it ever fails,
// every consumer's signature verification breaks.
func TestSignatureRoundTrip(t *testing.T) {
	secret := "whsec_TEST_SECRET_VALUE"
	body := []byte(`{"type":"event.opened","event_id":42}`)
	timestamp := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	signature := Sign(timestamp, body, secret)

	// Parse the signature: t=<unix>,v1=<hex>
	parts := strings.Split(signature, ",")
	if len(parts) != 2 {
		t.Fatalf("signature should have 2 parts, got %d: %s", len(parts), signature)
	}
	if !strings.HasPrefix(parts[0], "t=") {
		t.Fatalf("part 0 should start with t=, got %s", parts[0])
	}
	if !strings.HasPrefix(parts[1], "v1=") {
		t.Fatalf("part 1 should start with v1=, got %s", parts[1])
	}
	tsStr := strings.TrimPrefix(parts[0], "t=")
	sigHex := strings.TrimPrefix(parts[1], "v1=")

	// Recompute on the consumer side.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsStr))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sigHex), []byte(expected)) {
		t.Errorf("signature mismatch:\n  got %s\n want %s", sigHex, expected)
	}

	// Verify timestamp is parseable and matches what we sent.
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		t.Errorf("timestamp not parseable: %v", err)
	}
	if ts != timestamp.Unix() {
		t.Errorf("timestamp = %d, want %d", ts, timestamp.Unix())
	}
}

func TestApplyDefaults(t *testing.T) {
	c := WorkerConfig{}
	c.applyDefaults()
	if c.PollInterval != 1*time.Second {
		t.Errorf("PollInterval = %v, want 1s", c.PollInterval)
	}
	if c.MaxConcurrent != 50 {
		t.Errorf("MaxConcurrent = %d, want 50", c.MaxConcurrent)
	}
	if c.PerWebhookCap != 3 {
		t.Errorf("PerWebhookCap = %d, want 3", c.PerWebhookCap)
	}
	if c.HTTPTimeout != 30*time.Second {
		t.Errorf("HTTPTimeout = %v, want 30s", c.HTTPTimeout)
	}
	if c.BatchSize != 200 {
		t.Errorf("BatchSize = %d, want 200", c.BatchSize)
	}
	if c.InstanceID != "default" {
		t.Errorf("InstanceID = %q, want default", c.InstanceID)
	}
}

func TestApplyDefaultsPreservesExplicit(t *testing.T) {
	c := WorkerConfig{
		PollInterval:  5 * time.Second,
		MaxConcurrent: 10,
		InstanceID:    "host-a",
	}
	c.applyDefaults()
	if c.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s (explicit)", c.PollInterval)
	}
	if c.MaxConcurrent != 10 {
		t.Errorf("MaxConcurrent = %d, want 10 (explicit)", c.MaxConcurrent)
	}
	if c.InstanceID != "host-a" {
		t.Errorf("InstanceID = %q, want host-a (explicit)", c.InstanceID)
	}
	// Unset fields should still get defaults.
	if c.PerWebhookCap != 3 {
		t.Errorf("PerWebhookCap = %d, want 3 (default)", c.PerWebhookCap)
	}
}

func TestAcquireSlotRespectsCap(t *testing.T) {
	w := &Worker{
		cfg:      WorkerConfig{PerWebhookCap: 2},
		inFlight: make(map[int64]int),
	}
	if !w.acquireSlot(1) {
		t.Fatal("first acquire should succeed")
	}
	if !w.acquireSlot(1) {
		t.Fatal("second acquire should succeed (under cap)")
	}
	if w.acquireSlot(1) {
		t.Fatal("third acquire should fail (cap=2)")
	}
	w.releaseSlot(1)
	if !w.acquireSlot(1) {
		t.Fatal("acquire after release should succeed")
	}
}

func TestAcquireSlotIsolatesWebhooks(t *testing.T) {
	w := &Worker{
		cfg:      WorkerConfig{PerWebhookCap: 1},
		inFlight: make(map[int64]int),
	}
	if !w.acquireSlot(1) {
		t.Fatal("webhook 1 first acquire failed")
	}
	if w.acquireSlot(1) {
		t.Fatal("webhook 1 second acquire should fail (cap=1)")
	}
	// Different webhook should be unaffected.
	if !w.acquireSlot(2) {
		t.Fatal("webhook 2 acquire should succeed even though webhook 1 is at cap")
	}
}

func TestReleaseSlotCleansUpZeroCounts(t *testing.T) {
	w := &Worker{
		cfg:      WorkerConfig{PerWebhookCap: 5},
		inFlight: make(map[int64]int),
	}
	w.acquireSlot(1)
	w.releaseSlot(1)
	if _, ok := w.inFlight[1]; ok {
		t.Error("zero-count entry should be deleted from map")
	}
}

func TestNewWorkerInitializesRuntimeState(t *testing.T) {
	w := NewWorker(WorkerConfig{InstanceID: "host-a", HTTPTimeout: 2 * time.Second})
	if w.cfg.InstanceID != "host-a" {
		t.Fatalf("InstanceID = %q, want host-a", w.cfg.InstanceID)
	}
	if w.httpClient == nil || w.httpClient.Timeout != 2*time.Second {
		t.Fatalf("httpClient = %+v", w.httpClient)
	}
	if w.inFlight == nil || w.stop == nil || w.done == nil {
		t.Fatalf("worker runtime state not initialized: %+v", w)
	}
}

func TestDeliverTickNoReadyDeliveries(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(selectClaimReadySQL).WithArgs(50).
		WillReturnRows(sqlmock.NewRows(columnsClaimedDelivery))

	w := NewWorker(WorkerConfig{DB: db})
	if err := w.deliverTick(); err != nil {
		t.Fatalf("deliverTick: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestHandleResultSchedulesRetryAndForcedAbandon(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectExec("UPDATE jetmon_webhook_deliveries").
		WithArgs(503, "retry", sqlmock.AnyArg(), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetmon_webhook_deliveries").
		WithArgs(0, "gone", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	w := NewWorker(WorkerConfig{DB: db})
	w.handleResult(context.Background(), Delivery{ID: 1, Attempt: 0}, 503, "retry", false)
	w.handleResult(context.Background(), Delivery{ID: 2, Attempt: 0}, 0, "gone", true)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestBuildPayload(t *testing.T) {
	occurredAt := time.Date(2026, 4, 27, 12, 0, 0, 123, time.UTC)
	w := &Worker{}
	payload, err := w.buildPayload(EventOpened, 10, 20, 30, "opened", "Seems Down", occurredAt)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if body["type"] != EventOpened || body["reason"] != "opened" || body["state"] != "Seems Down" {
		t.Fatalf("payload = %s", payload)
	}
	if body["transition_id"].(float64) != 10 || body["event_id"].(float64) != 20 || body["site_id"].(float64) != 30 {
		t.Fatalf("payload ids = %s", payload)
	}
	if body["occurred_at"] != occurredAt.Format(time.RFC3339Nano) {
		t.Fatalf("occurred_at = %v", body["occurred_at"])
	}
}

func TestProgressLoadSave(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	w := &Worker{cfg: WorkerConfig{DB: db, InstanceID: "host-a"}}
	mock.ExpectQuery("SELECT last_transition_id FROM jetmon_webhook_dispatch_progress").
		WithArgs("host-a").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO jetmon_webhook_dispatch_progress").
		WithArgs("host-a", int64(55)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT last_transition_id FROM jetmon_webhook_dispatch_progress").
		WithArgs("host-a").
		WillReturnRows(sqlmock.NewRows([]string{"last_transition_id"}).AddRow(int64(55)))

	last, err := w.loadProgress(context.Background())
	if err != nil {
		t.Fatalf("loadProgress empty: %v", err)
	}
	if last != 0 {
		t.Fatalf("empty progress = %d, want 0", last)
	}
	if err := w.saveProgress(context.Background(), 55); err != nil {
		t.Fatalf("saveProgress: %v", err)
	}
	last, err = w.loadProgress(context.Background())
	if err != nil {
		t.Fatalf("loadProgress stored: %v", err)
	}
	if last != 55 {
		t.Fatalf("stored progress = %d, want 55", last)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

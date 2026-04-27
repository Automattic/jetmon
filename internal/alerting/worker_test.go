package alerting

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/eventstore"
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
		{6, 0, true},
		{7, 0, true},
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

func TestApplyDefaults(t *testing.T) {
	c := WorkerConfig{}
	c.applyDefaults()
	if c.PollInterval != 1*time.Second {
		t.Errorf("PollInterval = %v, want 1s", c.PollInterval)
	}
	if c.MaxConcurrent != 50 {
		t.Errorf("MaxConcurrent = %d, want 50", c.MaxConcurrent)
	}
	if c.PerContactCap != 3 {
		t.Errorf("PerContactCap = %d, want 3", c.PerContactCap)
	}
	if c.BatchSize != 200 {
		t.Errorf("BatchSize = %d, want 200", c.BatchSize)
	}
	if c.DispatchTimeout != 30*time.Second {
		t.Errorf("DispatchTimeout = %v, want 30s", c.DispatchTimeout)
	}
	if c.InstanceID != "default" {
		t.Errorf("InstanceID = %q, want default", c.InstanceID)
	}
}

func TestApplyDefaultsPreservesExplicit(t *testing.T) {
	c := WorkerConfig{
		PollInterval:  5 * time.Second,
		PerContactCap: 7,
		InstanceID:    "host-a",
	}
	c.applyDefaults()
	if c.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s (explicit)", c.PollInterval)
	}
	if c.PerContactCap != 7 {
		t.Errorf("PerContactCap = %d, want 7 (explicit)", c.PerContactCap)
	}
	if c.InstanceID != "host-a" {
		t.Errorf("InstanceID = %q, want host-a (explicit)", c.InstanceID)
	}
	// Unset fields still get defaults.
	if c.MaxConcurrent != 50 {
		t.Errorf("MaxConcurrent = %d, want 50 (default)", c.MaxConcurrent)
	}
}

func TestAcquireSlotRespectsCap(t *testing.T) {
	w := &Worker{
		cfg:      WorkerConfig{PerContactCap: 2},
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

func TestAcquireSlotIsolatesContacts(t *testing.T) {
	w := &Worker{
		cfg:      WorkerConfig{PerContactCap: 1},
		inFlight: make(map[int64]int),
	}
	if !w.acquireSlot(1) {
		t.Fatal("contact 1 first acquire failed")
	}
	if w.acquireSlot(1) {
		t.Fatal("contact 1 second acquire should fail (cap=1)")
	}
	if !w.acquireSlot(2) {
		t.Fatal("contact 2 should be unaffected by contact 1's cap")
	}
}

func TestReleaseSlotCleansUpZeroCounts(t *testing.T) {
	w := &Worker{
		cfg:      WorkerConfig{PerContactCap: 5},
		inFlight: make(map[int64]int),
	}
	w.acquireSlot(1)
	w.releaseSlot(1)
	if _, ok := w.inFlight[1]; ok {
		t.Error("zero-count entry should be deleted from map")
	}
}

func TestNewWorkerInitializesRuntimeState(t *testing.T) {
	dispatchers := map[Transport]Dispatcher{}
	w := NewWorker(WorkerConfig{InstanceID: "host-a", Dispatchers: dispatchers})
	if w.cfg.InstanceID != "host-a" {
		t.Fatalf("InstanceID = %q, want host-a", w.cfg.InstanceID)
	}
	if w.cfg.Dispatchers == nil || w.inFlight == nil || w.rateLimit == nil || w.stop == nil || w.done == nil {
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

	mock.ExpectExec("UPDATE jetmon_alert_deliveries").
		WithArgs(503, "retry", sqlmock.AnyArg(), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetmon_alert_deliveries").
		WithArgs(0, "gone", int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	w := NewWorker(WorkerConfig{DB: db})
	w.handleResult(context.Background(), Delivery{ID: 1, Attempt: 0}, 503, "retry", false)
	w.handleResult(context.Background(), Delivery{ID: 2, Attempt: 0}, 0, "gone", true)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestRateLimitWindowRespectsCapacity verifies the rate window admits up
// to capacity dispatches in an hour, then refuses, then admits again
// after a timestamp ages out.
func TestRateLimitWindowRespectsCapacity(t *testing.T) {
	r := newRateLimitWindow()
	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		if !r.tryConsume(42, 3, base.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("dispatch %d should be admitted (cap=3)", i)
		}
	}
	if r.tryConsume(42, 3, base.Add(4*time.Second)) {
		t.Fatal("4th dispatch should be refused (cap=3)")
	}
	// 1h+1s later, all three earlier timestamps age out.
	later := base.Add(1*time.Hour + 1*time.Second)
	if !r.tryConsume(42, 3, later) {
		t.Fatal("dispatch should be admitted after window pruning")
	}
}

func TestRateLimitWindowIsolatesContacts(t *testing.T) {
	r := newRateLimitWindow()
	now := time.Now()
	for i := 0; i < 2; i++ {
		_ = r.tryConsume(1, 2, now)
	}
	if !r.tryConsume(2, 2, now) {
		t.Error("contact 2 should not be affected by contact 1's rate")
	}
}

func TestEventTypeForReason(t *testing.T) {
	cases := map[string]string{
		"opened":                "alert.opened",
		"severity_escalation":   "alert.severity_changed",
		"severity_deescalation": "alert.severity_changed",
		"state_change":          "alert.state_changed",
		"verifier_confirmed":    "alert.state_changed",
		"verifier_cleared":      "alert.closed",
		"manual_override":       "alert.closed",
		"superseded":            "alert.closed",
		"unknown_reason":        "",
	}
	for reason, want := range cases {
		got := eventTypeForReason(reason)
		if got != want {
			t.Errorf("eventTypeForReason(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestBuildPayload(t *testing.T) {
	occurredAt := time.Date(2026, 4, 27, 12, 0, 0, 123, time.UTC)
	payload, err := buildPayload("alert.opened", 10, 20, 30, "opened", "Seems Down", 1, 4, occurredAt)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if body["type"] != "alert.opened" || body["reason"] != "opened" || body["state"] != "Seems Down" {
		t.Fatalf("payload = %s", payload)
	}
	if body["severity_before"].(float64) != 1 || body["severity_after"].(float64) != 4 {
		t.Fatalf("payload severities = %s", payload)
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
	mock.ExpectQuery("SELECT last_transition_id FROM jetmon_alert_dispatch_progress").
		WithArgs("host-a").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO jetmon_alert_dispatch_progress").
		WithArgs("host-a", int64(55)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT last_transition_id FROM jetmon_alert_dispatch_progress").
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

func TestBuildNotificationUsesSiteURLAndRecoveryFlag(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	occurredAt := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	payload, err := buildPayload(
		"alert.closed", 10, 20, 30, "verifier_cleared", "Resolved",
		eventstore.SeverityDown, eventstore.SeverityUp, occurredAt,
	)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	mock.ExpectQuery("SELECT monitor_url FROM jetpack_monitor_sites").
		WithArgs(int64(30)).
		WillReturnRows(sqlmock.NewRows([]string{"monitor_url"}).AddRow("https://site.example"))

	w := &Worker{cfg: WorkerConfig{DB: db}}
	notification, err := w.buildNotification(context.Background(), &AlertContact{
		ID:          1,
		MinSeverity: eventstore.SeverityDown,
	}, Delivery{
		ID:      99,
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("buildNotification: %v", err)
	}
	if !notification.Recovery || notification.Severity != eventstore.SeverityUp {
		t.Fatalf("notification recovery/severity = %+v", notification)
	}
	if notification.SiteURL != "https://site.example" || notification.DedupKey != "jetmon-event-20" {
		t.Fatalf("notification = %+v", notification)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestBuildNotificationFallsBackToSiteID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	payload, err := buildPayload(
		"alert.opened", 10, 20, 30, "opened", "Down",
		eventstore.SeverityUp, eventstore.SeverityDown, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("buildPayload: %v", err)
	}
	mock.ExpectQuery("SELECT monitor_url FROM jetpack_monitor_sites").
		WithArgs(int64(30)).
		WillReturnError(sql.ErrNoRows)

	w := &Worker{cfg: WorkerConfig{DB: db}}
	notification, err := w.buildNotification(context.Background(), &AlertContact{
		ID:          1,
		MinSeverity: eventstore.SeverityDown,
	}, Delivery{Payload: payload})
	if err != nil {
		t.Fatalf("buildNotification: %v", err)
	}
	if notification.SiteURL != "site:30" {
		t.Fatalf("SiteURL = %q, want site:30", notification.SiteURL)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

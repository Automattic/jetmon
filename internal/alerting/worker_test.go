package alerting

import (
	"testing"
	"time"
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

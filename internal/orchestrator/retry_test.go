package orchestrator

import (
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
)

func TestRetryQueueRecord(t *testing.T) {
	q := newRetryQueue()
	res := checker.Result{BlogID: 1, URL: "https://example.com", Timestamp: time.Now()}

	e := q.record(res)
	if e.failCount != 1 {
		t.Fatalf("failCount = %d, want 1", e.failCount)
	}
	if e.blogID != 1 {
		t.Fatalf("blogID = %d, want 1", e.blogID)
	}

	e = q.record(res)
	if e.failCount != 2 {
		t.Fatalf("failCount after second record = %d, want 2", e.failCount)
	}
}

func TestRetryQueueRecordAccumulatesChecks(t *testing.T) {
	q := newRetryQueue()
	res := checker.Result{BlogID: 1, Timestamp: time.Now()}

	for range 3 {
		q.record(res)
	}

	e := q.get(1)
	if len(e.checks) != 3 {
		t.Fatalf("checks length = %d, want 3", len(e.checks))
	}
}

func TestRetryQueueGet(t *testing.T) {
	q := newRetryQueue()

	if got := q.get(99); got != nil {
		t.Fatalf("get() = %v, want nil for unknown blog_id", got)
	}

	q.record(checker.Result{BlogID: 99, Timestamp: time.Now()})
	if got := q.get(99); got == nil {
		t.Fatalf("get() = nil after record, want entry")
	}
}

func TestRetryQueueClear(t *testing.T) {
	q := newRetryQueue()
	q.record(checker.Result{BlogID: 1, Timestamp: time.Now()})
	q.record(checker.Result{BlogID: 2, Timestamp: time.Now()})
	recoveredAt := time.Now().UTC()
	q.markRecovered(1, recoveredAt)

	q.clear(1)

	if q.get(1) != nil {
		t.Fatalf("get() after clear returned entry, want nil")
	}
	if !q.recentlyRecovered(1, recoveredAt.Add(time.Minute), 2*time.Minute) {
		t.Fatalf("recent recovery marker should survive retry clear")
	}
	if q.get(2) == nil {
		t.Fatalf("get() for uncleared entry returned nil")
	}
}

func TestRetryQueueRecentlyRecoveredExpires(t *testing.T) {
	q := newRetryQueue()
	recoveredAt := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	q.markRecovered(42, recoveredAt)

	if !q.recentlyRecovered(42, recoveredAt.Add(30*time.Second), time.Minute) {
		t.Fatal("recentlyRecovered returned false inside window")
	}
	if q.recentlyRecovered(42, recoveredAt.Add(2*time.Minute), time.Minute) {
		t.Fatal("recentlyRecovered returned true after window")
	}
	if q.recentlyRecovered(42, recoveredAt.Add(30*time.Second), time.Minute) {
		t.Fatal("expired marker should be removed")
	}
}

func TestRetryQueueRecentlyFalseAlarmedExpiresSeparately(t *testing.T) {
	q := newRetryQueue()
	falseAlarmAt := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	q.markFalseAlarm(42, falseAlarmAt)

	if !q.recentlyFalseAlarmed(42, falseAlarmAt.Add(4*time.Minute), 5*time.Minute) {
		t.Fatal("recentlyFalseAlarmed returned false inside window")
	}
	if q.recentlyRecovered(42, falseAlarmAt.Add(4*time.Minute), 5*time.Minute) {
		t.Fatal("false-alarm marker should not count as a normal recovery marker")
	}
	if q.recentlyFalseAlarmed(42, falseAlarmAt.Add(6*time.Minute), 5*time.Minute) {
		t.Fatal("recentlyFalseAlarmed returned true after window")
	}
}

func TestRetryQueueSize(t *testing.T) {
	q := newRetryQueue()
	if q.size() != 0 {
		t.Fatalf("size() = %d, want 0", q.size())
	}

	q.record(checker.Result{BlogID: 1, Timestamp: time.Now()})
	q.record(checker.Result{BlogID: 2, Timestamp: time.Now()})
	if q.size() != 2 {
		t.Fatalf("size() = %d, want 2", q.size())
	}

	q.clear(1)
	if q.size() != 1 {
		t.Fatalf("size() after clear = %d, want 1", q.size())
	}
}

func TestRetryQueuePersistsBetweenRounds(t *testing.T) {
	q := newRetryQueue()
	q.record(checker.Result{BlogID: 5, Timestamp: time.Now()})

	// Simulate a new round: record should accumulate, not reset.
	q.record(checker.Result{BlogID: 5, Timestamp: time.Now()})

	e := q.get(5)
	if e == nil {
		t.Fatal("entry missing after second round")
	}
	if e.failCount != 2 {
		t.Fatalf("failCount = %d, want 2 — queue was flushed between rounds", e.failCount)
	}
}

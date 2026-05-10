package orchestrator

import (
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

func TestStreamingPhaseStaysInsideInterval(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	site := db.Site{BlogID: 12345, CheckInterval: 5}

	due := initialStreamingDueAt(site, now)
	if due.Before(now) {
		t.Fatalf("initialStreamingDueAt() = %s before now %s", due, now)
	}
	if due.Sub(now) >= 5*time.Minute {
		t.Fatalf("initialStreamingDueAt() delay = %s, want < 5m", due.Sub(now))
	}
	if got := due.Unix() % int64(5*time.Minute/time.Second); got != streamingPhaseOffset(site.BlogID, 5*time.Minute) {
		t.Fatalf("due phase = %d, want stable phase", got)
	}
}

func TestStreamingPlannerPopDueSkipsQueuedAndInflightTargets(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	planner := &streamingPlanner{
		targets: make(map[int64]*streamingTarget),
		due:     make(map[int64][]int64),
	}
	ready := &streamingTarget{site: db.Site{BlogID: 1, CheckInterval: 1}, active: true}
	queued := &streamingTarget{site: db.Site{BlogID: 2, CheckInterval: 1}, active: true, queued: true}
	inFlight := &streamingTarget{site: db.Site{BlogID: 3, CheckInterval: 1}, active: true, inFlight: true}
	planner.targets[1] = ready
	planner.targets[2] = queued
	planner.targets[3] = inFlight
	planner.scheduleAt(ready, now.Add(-time.Second))
	planner.scheduleAt(queued, now.Add(-time.Second))
	planner.scheduleAt(inFlight, now.Add(-time.Second))

	due := planner.popDue(now)
	if len(due) != 1 || due[0].site.BlogID != 1 {
		t.Fatalf("popDue() = %+v, want only blog 1", due)
	}
	if !ready.queued {
		t.Fatal("ready target should be marked queued after popDue")
	}
}

func TestStreamingWorkerTargetScalesFromRequiredRate(t *testing.T) {
	planner := &streamingPlanner{targets: make(map[int64]*streamingTarget)}
	for i := int64(1); i <= 1200; i++ {
		planner.targets[i] = &streamingTarget{
			site:   db.Site{BlogID: i, CheckInterval: 1},
			active: true,
		}
	}
	planner.recalculateRequiredRate()
	cfg := &config.Config{NumWorkers: 10}

	got := streamingWorkerTarget(cfg, planner, time.Second)
	if got <= cfg.NumWorkers {
		t.Fatalf("streamingWorkerTarget() = %d, want above NumWorkers floor", got)
	}
	if got > planner.activeCount() {
		t.Fatalf("streamingWorkerTarget() = %d, want <= active target count", got)
	}
}

func TestQueueStreamingProjectionRespectsInterval(t *testing.T) {
	origNow := nowFunc
	defer func() { nowFunc = origNow }()

	o := &Orchestrator{}
	cfg := &config.Config{StreamingLegacyProjectionIntervalMin: 10}
	checkedAt := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	projectedAt := checkedAt.Add(10*time.Minute + 2*time.Second)
	nowFunc = func() time.Time { return projectedAt }
	target := &streamingTarget{
		site:            db.Site{BlogID: 42, CheckInterval: 1},
		dueAt:           checkedAt.Add(5 * time.Minute),
		lastProjectedAt: checkedAt.Add(-5 * time.Minute),
	}
	pending := map[int64]db.SiteCheck{}

	o.queueStreamingProjection(cfg, target, checker.Result{BlogID: 42, Timestamp: checkedAt}, pending)
	if len(pending) != 0 {
		t.Fatalf("pending projection rows = %d, want 0 before interval", len(pending))
	}

	later := checkedAt.Add(10 * time.Minute)
	o.queueStreamingProjection(cfg, target, checker.Result{BlogID: 42, Timestamp: later}, pending)
	if len(pending) != 1 {
		t.Fatalf("pending projection rows = %d, want 1 after interval", len(pending))
	}
	if got := pending[42].CheckedAt; !got.Equal(projectedAt) {
		t.Fatalf("projected CheckedAt = %s, want %s", got, projectedAt)
	}
}

func TestQueueStreamingProjectionProjectsEveryCheckWhenSiteIntervalMatchesProjection(t *testing.T) {
	origNow := nowFunc
	defer func() { nowFunc = origNow }()

	o := &Orchestrator{}
	cfg := &config.Config{StreamingLegacyProjectionIntervalMin: 10}
	checkedAt := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	projectedAt := checkedAt.Add(2 * time.Second)
	nowFunc = func() time.Time { return projectedAt }
	target := &streamingTarget{
		site:            db.Site{BlogID: 42, CheckInterval: 5},
		dueAt:           checkedAt.Add(5 * time.Minute),
		lastProjectedAt: checkedAt.Add(-299 * time.Second),
	}
	pending := map[int64]db.SiteCheck{}

	o.queueStreamingProjection(cfg, target, checker.Result{BlogID: 42, Timestamp: checkedAt}, pending)
	if len(pending) != 1 {
		t.Fatalf("pending projection rows = %d, want 1 for every 5m check", len(pending))
	}
	if got := pending[42].CheckedAt; !got.Equal(projectedAt) {
		t.Fatalf("projected CheckedAt = %s, want %s", got, projectedAt)
	}
}

func TestStreamingProjectionIntervalCapsToFiveMinuteSiteInterval(t *testing.T) {
	cfg := &config.Config{StreamingLegacyProjectionIntervalMin: 10}

	got := streamingProjectionInterval(cfg, db.Site{CheckInterval: 5})
	if got != 5*time.Minute {
		t.Fatalf("streamingProjectionInterval(5m site) = %s, want 5m", got)
	}

	got = streamingProjectionInterval(cfg, db.Site{CheckInterval: 1})
	if got != 10*time.Minute {
		t.Fatalf("streamingProjectionInterval(1m site) = %s, want configured 10m", got)
	}

	cfg.StreamingLegacyProjectionIntervalMin = 3
	got = streamingProjectionInterval(cfg, db.Site{CheckInterval: 1})
	if got != 5*time.Minute {
		t.Fatalf("streamingProjectionInterval(enforced floor) = %s, want 5m", got)
	}
}

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
	cadence := streamingCheckCadence(site)
	if got := due.Unix() % int64(cadence/time.Second); got != streamingPhaseOffset(site.BlogID, cadence) {
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

func TestStreamingWorkerTargetCapsScaleLatencyAtCheckTimeout(t *testing.T) {
	planner := &streamingPlanner{targets: make(map[int64]*streamingTarget)}
	for i := int64(1); i <= 100000; i++ {
		planner.targets[i] = &streamingTarget{
			site:   db.Site{BlogID: i, CheckInterval: 5},
			active: true,
		}
	}
	planner.recalculateRequiredRate()
	cfg := &config.Config{NumWorkers: 60, NetCommsTimeout: 2}

	got := streamingWorkerTarget(cfg, planner, 10*time.Second)
	want := int(planner.requiredChecksPerSecond() * 2 * streamingWorkerHeadroom)
	want++
	if got != want {
		t.Fatalf("streamingWorkerTarget() = %d, want capped target %d", got, want)
	}
}

func TestStreamingScaleLatencyCapUsesDefaultTimeout(t *testing.T) {
	if got := streamingScaleLatencyCap(&config.Config{}); got != 10*time.Second {
		t.Fatalf("streamingScaleLatencyCap(default) = %s, want 10s", got)
	}
	if got := streamingScaleLatencyCap(&config.Config{NetCommsTimeout: 1}); got != time.Second {
		t.Fatalf("streamingScaleLatencyCap(configured) = %s, want 1s", got)
	}
}

func TestStreamingQueueCapScalesWithActiveTargets(t *testing.T) {
	if got := streamingQueueCap(60, 0); got != streamingMinQueueCap {
		t.Fatalf("streamingQueueCap(empty) = %d, want %d", got, streamingMinQueueCap)
	}
	if got := streamingQueueCap(60, 100000); got != 100000 {
		t.Fatalf("streamingQueueCap(100k active) = %d, want 100000", got)
	}
	if got := streamingQueueCap(100000, 1000000); got != streamingMaxQueueCap {
		t.Fatalf("streamingQueueCap(capped) = %d, want %d", got, streamingMaxQueueCap)
	}
}

func TestStreamingSideEffectShardIsStable(t *testing.T) {
	const shards = 8
	first := streamingSideEffectShard(42, shards)
	for range 10 {
		if got := streamingSideEffectShard(42, shards); got != first {
			t.Fatalf("streamingSideEffectShard() = %d, want stable %d", got, first)
		}
	}
	if got := streamingSideEffectShard(-42, shards); got != first {
		t.Fatalf("streamingSideEffectShard(negative) = %d, want %d", got, first)
	}
}

func TestStreamingSideEffectShardCountIsBounded(t *testing.T) {
	got := streamingSideEffectShardCount(0)
	if got < streamingMinSideEffectShards || got > streamingMaxSideEffectShards {
		t.Fatalf("streamingSideEffectShardCount() = %d, want within [%d,%d]", got, streamingMinSideEffectShards, streamingMaxSideEffectShards)
	}

	large := streamingSideEffectShardCount(100000)
	if large <= got {
		t.Fatalf("streamingSideEffectShardCount(100k) = %d, want above empty target count %d", large, got)
	}
	if large > streamingMaxSideEffectShards {
		t.Fatalf("streamingSideEffectShardCount(100k) = %d, want <= %d", large, streamingMaxSideEffectShards)
	}
}

func TestStreamingSideEffectsNeededSkipsNoopSuccess(t *testing.T) {
	target := &streamingTarget{site: db.Site{BlogID: 42, SiteStatus: statusRunning}}
	res := checker.Result{BlogID: 42, Success: true, HTTPCode: 200}

	if streamingSideEffectsNeeded(target, res, nil, nil, nil) {
		t.Fatal("no-op success should not require side effects")
	}

	pending := map[int64]int{42: 1}
	if !streamingSideEffectsNeeded(target, res, pending, nil, nil) {
		t.Fatal("success behind a pending side effect should preserve ordering")
	}

	statusCache := map[int64]int{42: statusDown}
	if !streamingSideEffectsNeeded(target, res, nil, statusCache, nil) {
		t.Fatal("success for cached non-running status should run recovery side effects")
	}

	retries := newRetryQueue()
	retries.record(checker.Result{BlogID: 42, URL: "http://example.com", Timestamp: time.Now()})
	if !streamingSideEffectsNeeded(target, res, nil, nil, retries) {
		t.Fatal("success for retrying site should run recovery side effects")
	}
}

func TestStreamingSideEffectsNeededKeepsFailureAndTLS(t *testing.T) {
	target := &streamingTarget{site: db.Site{BlogID: 42, SiteStatus: statusRunning}}
	if !streamingSideEffectsNeeded(target, checker.Result{BlogID: 42, ErrorCode: checker.ErrorConnect}, nil, nil, nil) {
		t.Fatal("failure should require side effects")
	}
	if !streamingSideEffectsNeeded(target, checker.Result{BlogID: 42, Success: true, HTTPCode: 200, TLSVersion: 0x0304}, nil, nil, nil) {
		t.Fatal("TLS observations should require side effects")
	}
}

func TestStreamingCheckCadenceAddsBoundedHeadroom(t *testing.T) {
	if got := streamingCheckCadence(db.Site{CheckInterval: 5}); got != 285*time.Second {
		t.Fatalf("streamingCheckCadence(5m) = %s, want 285s", got)
	}
	if got := streamingCheckCadence(db.Site{CheckInterval: 1}); got != 57*time.Second {
		t.Fatalf("streamingCheckCadence(1m) = %s, want 57s", got)
	}
	if got := streamingCheckCadence(db.Site{CheckInterval: 60}); got != 3585*time.Second {
		t.Fatalf("streamingCheckCadence(60m) = %s, want 3585s", got)
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

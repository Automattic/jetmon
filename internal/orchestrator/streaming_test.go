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

func TestStreamingPlannerPopDueKeepsFutureBucketsPending(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	planner := &streamingPlanner{
		targets: make(map[int64]*streamingTarget),
		due:     make(map[int64][]int64),
	}
	ready := &streamingTarget{site: db.Site{BlogID: 1, CheckInterval: 1}, active: true}
	future := &streamingTarget{site: db.Site{BlogID: 2, CheckInterval: 1}, active: true}
	planner.targets[1] = ready
	planner.targets[2] = future
	planner.scheduleAt(future, now.Add(10*time.Minute))
	planner.scheduleAt(ready, now)

	due := planner.popDue(now)
	if len(due) != 1 || due[0].site.BlogID != 1 {
		t.Fatalf("popDue(now) = %+v, want only blog 1", due)
	}
	if got := planner.popDue(now.Add(5 * time.Minute)); len(got) != 0 {
		t.Fatalf("popDue(before future) = %+v, want no sites", got)
	}
	due = planner.popDue(now.Add(10 * time.Minute))
	if len(due) != 1 || due[0].site.BlogID != 2 {
		t.Fatalf("popDue(future) = %+v, want only blog 2", due)
	}
}

func TestStreamingPlannerPopDueSkipsStaleBucketEntries(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	planner := &streamingPlanner{
		targets: make(map[int64]*streamingTarget),
		due:     make(map[int64][]int64),
	}
	target := &streamingTarget{site: db.Site{BlogID: 1, CheckInterval: 1}, active: true}
	planner.targets[1] = target
	planner.scheduleAt(target, now.Add(-time.Second))
	planner.scheduleAt(target, now.Add(time.Minute))

	if got := planner.popDue(now); len(got) != 0 {
		t.Fatalf("popDue(now) = %+v, want stale bucket skipped", got)
	}
	if target.queued {
		t.Fatal("target should not be queued from a stale due bucket")
	}
	due := planner.popDue(now.Add(time.Minute))
	if len(due) != 1 || due[0].site.BlogID != 1 {
		t.Fatalf("popDue(rescheduled) = %+v, want blog 1", due)
	}
}

func TestStreamingScheduleAfterResultKeepsLocalRetryForSeemsDown(t *testing.T) {
	checkedAt := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	planner := &streamingPlanner{
		targets: make(map[int64]*streamingTarget),
		due:     make(map[int64][]int64),
	}
	target := &streamingTarget{
		site:   db.Site{BlogID: 42, CheckInterval: 5, SiteStatus: statusDown},
		dueAt:  checkedAt,
		active: true,
	}

	planner.scheduleAfterResult(target, checker.Result{
		BlogID:    42,
		ErrorCode: checker.ErrorConnect,
		Timestamp: checkedAt,
	}, true)

	if got, want := target.dueAt, checkedAt.Add(failedCheckRetryInterval); !got.Equal(want) {
		t.Fatalf("dueAt = %s, want retry at %s", got, want)
	}
}

func TestStreamingScheduleAfterResultUsesNormalCadenceForConfirmedDown(t *testing.T) {
	checkedAt := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	planner := &streamingPlanner{
		targets: make(map[int64]*streamingTarget),
		due:     make(map[int64][]int64),
	}
	target := &streamingTarget{
		site:   db.Site{BlogID: 42, CheckInterval: 5, SiteStatus: statusConfirmedDown},
		dueAt:  checkedAt,
		active: true,
	}

	planner.scheduleAfterResult(target, checker.Result{
		BlogID:    42,
		ErrorCode: checker.ErrorConnect,
		Timestamp: checkedAt,
	}, true)

	if got, want := target.dueAt, checkedAt.Add(streamingCheckCadence(target.site)); !got.Equal(want) {
		t.Fatalf("dueAt = %s, want normal cadence at %s", got, want)
	}
}

func TestStreamingScheduleAtNextPhaseAfterRestoresPhaseSpread(t *testing.T) {
	checkedAt := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	planner := &streamingPlanner{
		targets: make(map[int64]*streamingTarget),
		due:     make(map[int64][]int64),
	}
	target := &streamingTarget{
		site:   db.Site{BlogID: 42, CheckInterval: 5, SiteStatus: statusRunning},
		dueAt:  checkedAt.Add(failedCheckRetryInterval),
		active: true,
	}

	planner.scheduleAtNextPhaseAfter(target, checkedAt)

	cadence := streamingCheckCadence(target.site)
	expected := nextStreamingPhaseAt(checkedAt.Add(time.Second), cadence, streamingPhaseOffset(target.site.BlogID, cadence))
	if !target.dueAt.Equal(expected) {
		t.Fatalf("dueAt = %s, want phase-spread due at %s", target.dueAt, expected)
	}
	if target.dueAt.Equal(checkedAt.Add(failedCheckRetryInterval)) {
		t.Fatal("dueAt stayed on local retry cadence")
	}
}

func TestStreamingScheduleAfterResultSkipsImmediateRetryUnderPressure(t *testing.T) {
	checkedAt := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	planner := &streamingPlanner{
		targets: make(map[int64]*streamingTarget),
		due:     make(map[int64][]int64),
	}
	target := &streamingTarget{
		site:   db.Site{BlogID: 42, CheckInterval: 5, SiteStatus: statusDown},
		dueAt:  checkedAt,
		active: true,
	}

	planner.scheduleAfterResult(target, checker.Result{
		BlogID:    42,
		ErrorCode: checker.ErrorConnect,
		Timestamp: checkedAt,
	}, false)

	if target.dueAt.Equal(checkedAt.Add(failedCheckRetryInterval)) {
		t.Fatal("dueAt used immediate retry while failure pressure was active")
	}
	if got := target.dueAt.Sub(checkedAt); got < time.Minute {
		t.Fatalf("dueAt delay = %s, want normal cadence instead of retry cadence", got)
	}
}

func TestStreamingAllowImmediateRetryUnderPressure(t *testing.T) {
	target := &streamingTarget{site: db.Site{BlogID: 42, CheckInterval: 5, SiteStatus: statusRunning}}
	localTimeout := checker.Result{BlogID: 42, ErrorCode: checker.ErrorTimeout}
	if streamingAllowImmediateRetry(target, localTimeout, nil, true) {
		t.Fatal("new local timeout under pressure should not use immediate retry")
	}
	if !streamingAllowImmediateRetry(target, checker.Result{BlogID: 42, HTTPCode: 503}, nil, true) {
		t.Fatal("HTTP failure under pressure should keep immediate retry")
	}

	retries := newRetryQueue()
	retries.record(checker.Result{BlogID: 42, URL: "http://example.com", Timestamp: time.Now()})
	if !streamingAllowImmediateRetry(target, localTimeout, retries, true) {
		t.Fatal("existing retry state under pressure should keep immediate retry")
	}

	target.site.SiteStatus = statusDown
	if !streamingAllowImmediateRetry(target, localTimeout, nil, true) {
		t.Fatal("non-running site under pressure should keep immediate retry")
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

func TestStreamingPressureWorkerTargetUsesConservativeLatency(t *testing.T) {
	planner := &streamingPlanner{targets: make(map[int64]*streamingTarget)}
	for i := int64(1); i <= 100000; i++ {
		planner.targets[i] = &streamingTarget{
			site:   db.Site{BlogID: i, CheckInterval: 5},
			active: true,
		}
	}
	planner.recalculateRequiredRate()
	cfg := &config.Config{NumWorkers: 60, NetCommsTimeout: 10}

	got := streamingPressureWorkerTarget(cfg, planner)
	want := int(planner.requiredChecksPerSecond() * streamingFailurePressureLatency.Seconds() * streamingWorkerHeadroom)
	want++
	if got != want {
		t.Fatalf("streamingPressureWorkerTarget() = %d, want %d", got, want)
	}
}

func TestStreamingDesiredWorkerTargetUsesBacklogWithoutFailurePressure(t *testing.T) {
	planner := &streamingPlanner{targets: make(map[int64]*streamingTarget)}
	for i := int64(1); i <= 100000; i++ {
		planner.targets[i] = &streamingTarget{
			site:   db.Site{BlogID: i, CheckInterval: 5},
			active: true,
		}
	}
	planner.recalculateRequiredRate()
	cfg := &config.Config{NumWorkers: 60, NetCommsTimeout: 10}

	base := streamingWorkerTarget(cfg, planner, 40*time.Millisecond)
	got := streamingDesiredWorkerTarget(cfg, planner, 40*time.Millisecond, 2*time.Minute, 60000, 0, 0, 0, base, false)
	if got <= base {
		t.Fatalf("streamingDesiredWorkerTarget() = %d, want above base target %d for pending backlog", got, base)
	}
}

func TestStreamingDesiredWorkerTargetAvoidsLatencySurgeWhileOnTime(t *testing.T) {
	planner := &streamingPlanner{targets: make(map[int64]*streamingTarget)}
	for i := int64(1); i <= 100000; i++ {
		planner.targets[i] = &streamingTarget{
			site:   db.Site{BlogID: i, CheckInterval: 5},
			active: true,
		}
	}
	planner.recalculateRequiredRate()
	cfg := &config.Config{NumWorkers: 60, NetCommsTimeout: 10}

	base := streamingWorkerTarget(cfg, planner, streamingDefaultLatency)
	got := streamingDesiredWorkerTarget(cfg, planner, 3*time.Second, 30*time.Second, 60000, 0, 0, 0, base, false)
	if got != base {
		t.Fatalf("streamingDesiredWorkerTarget() = %d, want base target %d while freshness is on time", got, base)
	}
}

func TestStreamingDesiredWorkerTargetSkipsBacklogGrowthUnderResultPressure(t *testing.T) {
	planner := &streamingPlanner{targets: make(map[int64]*streamingTarget)}
	for i := int64(1); i <= 100000; i++ {
		planner.targets[i] = &streamingTarget{
			site:   db.Site{BlogID: i, CheckInterval: 5},
			active: true,
		}
	}
	planner.recalculateRequiredRate()
	cfg := &config.Config{NumWorkers: 60, NetCommsTimeout: 10}

	base := streamingWorkerTarget(cfg, planner, streamingDefaultLatency)
	resultDepth := streamingResultDispatchPauseDepth(base, planner.activeCount()) / 2
	got := streamingDesiredWorkerTarget(cfg, planner, streamingDefaultLatency, 2*time.Minute, 60000, 0, resultDepth, 0, base, false)
	if got != base {
		t.Fatalf("streamingDesiredWorkerTarget() = %d, want base target %d while result backlog is pressured", got, base)
	}
}

func TestStreamingDesiredWorkerTargetSkipsBacklogGrowthDuringFailurePressure(t *testing.T) {
	planner := &streamingPlanner{targets: make(map[int64]*streamingTarget)}
	for i := int64(1); i <= 100000; i++ {
		planner.targets[i] = &streamingTarget{
			site:   db.Site{BlogID: i, CheckInterval: 5},
			active: true,
		}
	}
	planner.recalculateRequiredRate()
	cfg := &config.Config{NumWorkers: 60, NetCommsTimeout: 10}

	base := streamingWorkerTarget(cfg, planner, 40*time.Millisecond)
	got := streamingDesiredWorkerTarget(cfg, planner, 40*time.Millisecond, 2*time.Minute, 60000, 0, 0, 0, base, true)
	if got != base {
		t.Fatalf("streamingDesiredWorkerTarget() = %d, want base target %d while failure pressure is active", got, base)
	}
}

func TestStreamingDampedWorkerTargetLimitsGrowthAndShrink(t *testing.T) {
	if got := streamingDampedWorkerTarget(400, 2000, false); got != 600 {
		t.Fatalf("growth damped target = %d, want 600", got)
	}
	if got := streamingDampedWorkerTarget(2000, 400, false); got != 1600 {
		t.Fatalf("shrink damped target = %d, want 1600", got)
	}
	if got := streamingDampedWorkerTarget(60, 80, false); got != 80 {
		t.Fatalf("small target change = %d, want 80", got)
	}
}

func TestStreamingDampedWorkerTargetShrinksFasterUnderFailurePressure(t *testing.T) {
	if got := streamingDampedWorkerTarget(2000, 400, true); got != 1000 {
		t.Fatalf("pressure shrink target = %d, want 1000", got)
	}
}

func TestStreamingFailurePressureRequiresVolumeAndRatio(t *testing.T) {
	if streamingFailurePressure(streamingStats{checkSuccesses: 9, checkFailures: 990}) {
		t.Fatal("failure pressure should wait for enough completed checks")
	}
	if streamingFailurePressure(streamingStats{checkSuccesses: 800, checkFailures: 200}) {
		t.Fatal("failure pressure should stay off below the failure ratio threshold")
	}
	if !streamingFailurePressure(streamingStats{checkSuccesses: 750, checkFailures: 250}) {
		t.Fatal("failure pressure should trip at the configured failure ratio")
	}
}

func TestStreamingStatsCountsErrorCodes(t *testing.T) {
	var stats streamingStats
	stats.addResult(checker.Result{ErrorCode: checker.ErrorTimeout}, 0)
	stats.addResult(checker.Result{ErrorCode: checker.ErrorConnect}, 0)
	stats.addResult(checker.Result{ErrorCode: checker.ErrorSSL}, 0)
	stats.addResult(checker.Result{ErrorCode: checker.ErrorRedirect}, 0)
	stats.addResult(checker.Result{ErrorCode: checker.ErrorKeyword}, 0)
	stats.addResult(checker.Result{ErrorCode: checker.ErrorBodyRead}, 0)
	stats.addResult(checker.Result{ErrorCode: checker.ErrorTLSExpired}, 0)
	stats.addResult(checker.Result{ErrorCode: checker.ErrorTLSDeprecated}, 0)

	if stats.errorTimeouts != 1 || stats.errorConnects != 1 || stats.errorSSL != 1 || stats.errorRedirects != 1 ||
		stats.errorKeywords != 1 || stats.errorBodyReads != 1 || stats.errorTLSExpired != 1 || stats.errorTLSDeprecated != 1 || stats.errorOther != 0 {
		t.Fatalf("error counters = timeout:%d connect:%d ssl:%d redirect:%d keyword:%d body:%d expired:%d deprecated:%d other:%d",
			stats.errorTimeouts,
			stats.errorConnects,
			stats.errorSSL,
			stats.errorRedirects,
			stats.errorKeywords,
			stats.errorBodyReads,
			stats.errorTLSExpired,
			stats.errorTLSDeprecated,
			stats.errorOther,
		)
	}
}

func TestStreamingBacklogWorkerTargetUsesSpareHeadroom(t *testing.T) {
	if got := streamingBacklogWorkerTarget(700, 100000, 42000); got != 875 {
		t.Fatalf("backlog worker target = %d, want base plus backlog catch-up", got)
	}
	if got := streamingBacklogWorkerTarget(700, 100000, 200000); got != 1400 {
		t.Fatalf("capped backlog worker target = %d, want 2x base cap", got)
	}
	if got := streamingBacklogWorkerTarget(700, 1000, 200000); got != 1000 {
		t.Fatalf("active-capped backlog worker target = %d, want active target count", got)
	}
}

func TestStreamingShouldDeferPeriodicReloadOnlyWhenHotPathIsBehind(t *testing.T) {
	planner := &streamingPlanner{targets: make(map[int64]*streamingTarget)}
	for i := int64(1); i <= 100000; i++ {
		planner.targets[i] = &streamingTarget{
			site:   db.Site{BlogID: i, CheckInterval: 5},
			active: true,
		}
	}

	if !streamingShouldDeferPeriodicReload(planner, 50000, 0, 0, 5000, streamingStats{}) {
		t.Fatal("large pending backlog should defer periodic reload")
	}
	resultBacklog := streamingResultDispatchPauseDepth(5000, planner.activeCount())/2 + 1
	if !streamingShouldDeferPeriodicReload(planner, 0, resultBacklog, 0, 5000, streamingStats{}) {
		t.Fatal("large result backlog should defer periodic reload")
	}
	if !streamingShouldDeferPeriodicReload(planner, 0, 0, 0, 5000, streamingStats{maxLag: 2 * time.Minute}) {
		t.Fatal("large scheduler lag should defer periodic reload")
	}
	if streamingShouldDeferPeriodicReload(planner, 10, 10, 0, 5000, streamingStats{}) {
		t.Fatal("healthy hot path should not defer periodic reload")
	}
}

func TestStreamingDeferredReloadRetriesSoonerThanFullInterval(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	lastReload := streamingDeferredReloadLastReload(now, 5*time.Minute)

	if next := lastReload.Add(5 * time.Minute); !next.Equal(now.Add(streamingReloadDeferInterval)) {
		t.Fatalf("next deferred reload = %s, want %s", next, now.Add(streamingReloadDeferInterval))
	}
}

func TestStreamingTargetReloadIntervalScalesForLargeFleets(t *testing.T) {
	cfg := &config.Config{StreamingTargetReloadSec: 300}
	planner := &streamingPlanner{targets: make(map[int64]*streamingTarget)}
	for i := int64(1); i <= 50000; i++ {
		planner.targets[i] = &streamingTarget{site: db.Site{BlogID: i}, active: true}
	}
	if got := streamingTargetReloadInterval(cfg, planner); got != 5*time.Minute {
		t.Fatalf("small fleet reload interval = %s, want configured 5m", got)
	}

	for i := int64(50001); i <= 500000; i++ {
		planner.targets[i] = &streamingTarget{site: db.Site{BlogID: i}, active: true}
	}
	if got := streamingTargetReloadInterval(cfg, planner); got != 10000*time.Second {
		t.Fatalf("500k fleet reload interval = %s, want scaled 10000s", got)
	}
}

func TestStreamingTargetReloadIntervalRespectsLongConfigAndCap(t *testing.T) {
	planner := &streamingPlanner{targets: make(map[int64]*streamingTarget)}
	for i := int64(1); i <= 500000; i++ {
		planner.targets[i] = &streamingTarget{site: db.Site{BlogID: i}, active: true}
	}
	if got := streamingTargetReloadInterval(&config.Config{StreamingTargetReloadSec: 4 * 60 * 60}, planner); got != 4*time.Hour {
		t.Fatalf("long configured reload interval = %s, want configured 4h", got)
	}
	for i := int64(500001); i <= 2000000; i++ {
		planner.targets[i] = &streamingTarget{site: db.Site{BlogID: i}, active: true}
	}
	if got := streamingTargetReloadInterval(&config.Config{StreamingTargetReloadSec: 300}, planner); got != streamingMaxTargetReloadInterval {
		t.Fatalf("capped reload interval = %s, want %s", got, streamingMaxTargetReloadInterval)
	}
}

func TestStreamingResultDrainLimitScalesWithBacklog(t *testing.T) {
	if got := streamingResultDrainLimitFor(10); got != streamingResultDrainLimit {
		t.Fatalf("small result drain limit = %d, want %d", got, streamingResultDrainLimit)
	}
	if got := streamingResultDrainLimitFor(40000); got != 20000 {
		t.Fatalf("medium result drain limit = %d, want 20000", got)
	}
	if got := streamingResultDrainLimitFor(200000); got != streamingMaxResultDrainLimit {
		t.Fatalf("large result drain limit = %d, want %d", got, streamingMaxResultDrainLimit)
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

func TestStreamingBackpressureDepthScalesWithWorkersAndTargets(t *testing.T) {
	if got := streamingResultBackpressureDepth(60, 0); got != streamingMinBackpressureDepth {
		t.Fatalf("empty result backpressure depth = %d, want %d", got, streamingMinBackpressureDepth)
	}
	if got := streamingResultDispatchPauseDepth(60, 0); got != streamingMinBackpressureDepth {
		t.Fatalf("empty result dispatch pause depth = %d, want %d", got, streamingMinBackpressureDepth)
	}
	if got := streamingSideEffectBackpressureDepth(2000, 100000); got != 5000 {
		t.Fatalf("100k side-effect backpressure depth = %d, want target-based 5000", got)
	}
	if got := streamingResultDispatchPauseDepth(5000, 500000); got != 50000 {
		t.Fatalf("500k result dispatch pause depth = %d, want target-based 50000", got)
	}
	if got := streamingResultBackpressureDepth(100000, 1000000); got != 131072 {
		t.Fatalf("capped result backpressure depth = %d, want 131072", got)
	}
	if got := streamingResultDispatchPauseDepth(100000, 1000000); got != 131072 {
		t.Fatalf("capped result dispatch pause depth = %d, want 131072", got)
	}
}

func TestStreamingDispatchBudgetPacesBacklogCatchup(t *testing.T) {
	if got := streamingDispatchBudget(350.88, 60000, 3500, time.Second, 0, 0, 100000); got != 702 {
		t.Fatalf("100k backlog dispatch budget = %d, want paced catch-up budget 702", got)
	}
	if got := streamingDispatchBudget(3508.8, 600000, 5000, time.Second, 0, 0, 1000000); got != 7018 {
		t.Fatalf("1M backlog dispatch budget = %d, want capped catch-up budget 7018", got)
	}
	if got := streamingDispatchBudget(350.88, 20, 3500, time.Second, 0, 0, 100000); got != 20 {
		t.Fatalf("small pending dispatch budget = %d, want pending count", got)
	}
}

func TestStreamingDispatchBudgetUsesFastCatchupWhenLagged(t *testing.T) {
	got := streamingDispatchBudget(350.88, 60000, 3500, time.Second, 2*time.Minute, 0, 100000)
	if got != 1404 {
		t.Fatalf("lagged dispatch budget = %d, want fast catch-up cap 1404", got)
	}
}

func TestStreamingDispatchBudgetAvoidsFastCatchupUnderResultPressure(t *testing.T) {
	resultDepth := streamingResultDispatchPauseDepth(3500, 100000) / 2
	got := streamingDispatchBudget(350.88, 60000, 3500, time.Second, 2*time.Minute, resultDepth, 100000)
	if got != 702 {
		t.Fatalf("pressured dispatch budget = %d, want normal catch-up budget 702", got)
	}
}

func TestStreamingDispatchBudgetScalesWithElapsedTime(t *testing.T) {
	got := streamingDispatchBudget(350.88, 60000, 3500, 10*time.Second, 0, 0, 100000)
	if got <= 702 {
		t.Fatalf("10s delayed dispatch budget = %d, want above one-second budget", got)
	}
	if got > 4211 {
		t.Fatalf("10s delayed dispatch budget = %d, want <= capped elapsed steady-state cap", got)
	}
}

func TestStreamingDispatchBudgetCapsLongPauseByWorkers(t *testing.T) {
	got := streamingDispatchBudget(350.88, 96077, 3693, 136*time.Second, 0, 0, 100000)
	if got != 3432 {
		t.Fatalf("long-pause dispatch budget = %d, want elapsed-capped budget 3432", got)
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

	if streamingSideEffectsNeeded(target, res, nil, nil, nil, false) {
		t.Fatal("no-op success should not require side effects")
	}

	pending := map[int64]int{42: 1}
	if !streamingSideEffectsNeeded(target, res, pending, nil, nil, false) {
		t.Fatal("success behind a pending side effect should preserve ordering")
	}

	statusCache := map[int64]int{42: statusDown}
	if !streamingSideEffectsNeeded(target, res, nil, statusCache, nil, false) {
		t.Fatal("success for cached non-running status should run recovery side effects")
	}

	retries := newRetryQueue()
	retries.record(checker.Result{BlogID: 42, URL: "http://example.com", Timestamp: time.Now()})
	if !streamingSideEffectsNeeded(target, res, nil, nil, retries, false) {
		t.Fatal("success for retrying site should run recovery side effects")
	}
}

func TestStreamingSideEffectsNeededKeepsFailureAndTLS(t *testing.T) {
	target := &streamingTarget{site: db.Site{BlogID: 42, SiteStatus: statusRunning}}
	if !streamingSideEffectsNeeded(target, checker.Result{BlogID: 42, ErrorCode: checker.ErrorConnect}, nil, nil, nil, false) {
		t.Fatal("failure should require side effects")
	}
	if !streamingSideEffectsNeeded(target, checker.Result{BlogID: 42, Success: true, HTTPCode: 200, TLSVersion: 0x0304}, nil, nil, nil, false) {
		t.Fatal("TLS observations should require side effects")
	}
}

func TestStreamingSideEffectsNeededSuppressesNewLocalFailuresUnderPressure(t *testing.T) {
	target := &streamingTarget{site: db.Site{BlogID: 42, SiteStatus: statusRunning}}
	timeout := checker.Result{BlogID: 42, ErrorCode: checker.ErrorTimeout}

	if streamingSideEffectsNeeded(target, timeout, nil, nil, nil, true) {
		t.Fatal("new local timeout under pressure should not open event side effects")
	}
	if !streamingSideEffectsNeeded(target, checker.Result{BlogID: 42, HTTPCode: 500}, nil, nil, nil, true) {
		t.Fatal("HTTP failures should still flow through side effects under pressure")
	}
	if !streamingSideEffectsNeeded(target, timeout, map[int64]int{42: 1}, nil, nil, true) {
		t.Fatal("pending side effects should preserve ordering under pressure")
	}

	retries := newRetryQueue()
	retries.record(checker.Result{BlogID: 42, URL: "http://example.com", Timestamp: time.Now()})
	if !streamingSideEffectsNeeded(target, timeout, nil, nil, retries, true) {
		t.Fatal("existing retry state should continue through side effects under pressure")
	}
}

func TestStreamingCheckRequestForTargetCachesParsedSiteFields(t *testing.T) {
	headers := `{"X-Test":"one"}`
	forbidden := `["blocked",""]`
	site := db.Site{
		BlogID:            42,
		MonitorURL:        "https://example.com",
		CheckInterval:     5,
		CustomHeaders:     &headers,
		ForbiddenKeywords: &forbidden,
	}
	cfg := &config.Config{
		NetCommsTimeout:     10,
		BodyReadMaxBytes:    64,
		BodyReadMaxMS:       20,
		KeywordReadMaxBytes: 128,
		KeywordReadMaxMS:    30,
	}
	target := &streamingTarget{site: site, checkRequestDirty: true}

	req := streamingCheckRequestForTarget(cfg, target)
	if !target.checkRequestReady {
		t.Fatal("check request was not marked ready")
	}
	if target.checkRequestDirty {
		t.Fatal("check request stayed dirty after refresh")
	}
	if got := req.CustomHeaders["X-Test"]; got != "one" {
		t.Fatalf("CustomHeaders[X-Test] = %q, want one", got)
	}
	if len(req.ForbiddenKeywords) != 1 || req.ForbiddenKeywords[0] != "blocked" {
		t.Fatalf("ForbiddenKeywords = %#v, want [blocked]", req.ForbiddenKeywords)
	}

	cfg.BodyReadMaxMS = 21
	req = streamingCheckRequestForTarget(cfg, target)
	if req.BodyReadMaxMS != 21 {
		t.Fatalf("BodyReadMaxMS = %d after config change, want 21", req.BodyReadMaxMS)
	}
}

func TestStreamingPlannerMergeMarksCheckRequestDirty(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	headers := `{"X-Test":"one"}`
	site := db.Site{BlogID: 42, MonitorURL: "https://example.com", CheckInterval: 5, CustomHeaders: &headers}
	cfg := &config.Config{NetCommsTimeout: 10}
	planner := newStreamingPlanner([]db.Site{site}, now)
	target := planner.targets[42]
	_ = streamingCheckRequestForTarget(cfg, target)

	updatedHeaders := `{"X-Test":"two"}`
	site.CustomHeaders = &updatedHeaders
	planner.merge([]db.Site{site}, now)
	if !target.checkRequestDirty {
		t.Fatal("planner merge did not mark cached check request dirty")
	}
	req := streamingCheckRequestForTarget(cfg, target)
	if got := req.CustomHeaders["X-Test"]; got != "two" {
		t.Fatalf("CustomHeaders[X-Test] = %q after merge, want two", got)
	}
}

func TestStreamingPlannerMergeKeepsCheckRequestCacheForStatusOnlyReload(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	site := db.Site{BlogID: 42, MonitorURL: "https://example.com", CheckInterval: 5, SiteStatus: statusRunning}
	cfg := &config.Config{NetCommsTimeout: 10}
	planner := newStreamingPlanner([]db.Site{site}, now)
	target := planner.targets[42]
	_ = streamingCheckRequestForTarget(cfg, target)

	site.SiteStatus = statusDown
	planner.merge([]db.Site{site}, now)
	if target.checkRequestDirty {
		t.Fatal("status-only reload marked cached check request dirty")
	}
	if target.site.SiteStatus != statusDown {
		t.Fatalf("SiteStatus = %d after merge, want %d", target.site.SiteStatus, statusDown)
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

func TestQueueStreamingProjectionUsesConfiguredRollbackWindowForFiveMinuteSites(t *testing.T) {
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
	if len(pending) != 0 {
		t.Fatalf("pending projection rows = %d, want 0 before configured interval", len(pending))
	}

	later := checkedAt.Add(10 * time.Minute)
	o.queueStreamingProjection(cfg, target, checker.Result{BlogID: 42, Timestamp: later}, pending)
	if len(pending) != 1 {
		t.Fatalf("pending projection rows = %d, want 1 after configured interval", len(pending))
	}
}

func TestStreamingProjectionIntervalUsesConfiguredRollbackWindow(t *testing.T) {
	cfg := &config.Config{StreamingLegacyProjectionIntervalMin: 10}

	got := streamingProjectionInterval(cfg)
	if got != 10*time.Minute {
		t.Fatalf("streamingProjectionInterval(configured) = %s, want 10m", got)
	}

	cfg.StreamingLegacyProjectionIntervalMin = 3
	got = streamingProjectionInterval(cfg)
	if got != 5*time.Minute {
		t.Fatalf("streamingProjectionInterval(enforced floor) = %s, want 5m", got)
	}
}

func TestStreamingProjectionFlushBatchCapsRows(t *testing.T) {
	pending := map[int64]db.SiteCheck{}
	for i := int64(1); i <= 5; i++ {
		pending[i] = db.SiteCheck{BlogID: i}
	}

	batch := streamingProjectionFlushBatch(pending, 2)
	if len(batch) != 2 {
		t.Fatalf("projection flush batch size = %d, want 2", len(batch))
	}
	if len(pending) != 3 {
		t.Fatalf("pending projection rows after capped batch = %d, want 3", len(pending))
	}

	batch = streamingProjectionFlushBatch(pending, 0)
	if len(batch) != 3 {
		t.Fatalf("projection flush final batch size = %d, want 3", len(batch))
	}
	if len(pending) != 0 {
		t.Fatalf("pending projection rows after final batch = %d, want 0", len(pending))
	}
}

func TestStreamingProjectionFlushRowLimitScalesWithRequiredRate(t *testing.T) {
	if got := streamingProjectionFlushRowLimit(10); got != streamingProjectionFlushMinRows {
		t.Fatalf("low-rate projection row limit = %d, want %d", got, streamingProjectionFlushMinRows)
	}
	if got := streamingProjectionFlushRowLimit(1754.39); got != 21930 {
		t.Fatalf("500k projection row limit = %d, want 21930", got)
	}
	if got := streamingProjectionFlushRowLimit(100000); got != streamingProjectionFlushMaxRows {
		t.Fatalf("high-rate projection row limit = %d, want %d", got, streamingProjectionFlushMaxRows)
	}
}

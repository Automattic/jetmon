package orchestrator

import (
	"context"
	"log"
	"math"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/metrics"
)

const (
	streamingTickInterval                    = time.Second
	streamingReportInterval                  = time.Minute
	streamingScaleInterval                   = 5 * time.Second
	streamingDispatchWakeInterval            = 100 * time.Millisecond
	streamingProjectionFlushInterval         = 10 * time.Second
	streamingDefaultProjectionInterval       = 15 * time.Minute
	streamingProjectionFlushMinRows          = 5000
	streamingProjectionFlushMaxRows          = 50000
	streamingReloadDeferInterval             = time.Minute
	streamingLargeFleetReloadFloor           = 100000
	streamingLargeFleetReloadSitesPerSecond  = 50
	streamingMaxTargetReloadInterval         = 6 * time.Hour
	streamingProjectionSlack                 = 2 * time.Second
	streamingEmptyTargetPollInterval         = 5 * time.Second
	streamingActiveCountPollInterval         = 30 * time.Second
	streamingDefaultLatency                  = 250 * time.Millisecond
	streamingBootstrapLatency                = time.Second
	streamingHistoryFlushInterval            = 250 * time.Millisecond
	streamingHistoryBatchSize                = 1000
	streamingMinLoadPageSize                 = 5000
	streamingMinQueueCap                     = 65536
	streamingMaxQueueCap                     = 1048576
	streamingMinScheduleHeadroom             = time.Second
	streamingMaxScheduleHeadroom             = 15 * time.Second
	streamingMinSideEffectShards             = 8
	streamingMaxSideEffectShards             = 256
	streamingWorkerHeadroom                  = 2.0
	streamingMinWorkerStep                   = 50
	streamingMinBackpressureDepth            = 1024
	streamingBacklogWorkerDivisor            = 240
	streamingBacklogWorkerMultiplier         = 2
	streamingResultDrainLimit                = 4096
	streamingMaxResultDrainLimit             = 262144
	streamingResultDispatchStride            = 1024
	streamingDispatchCatchupDivisor          = 120
	streamingDispatchFastCatchupDivisor      = 60
	streamingDispatchMaxElapsed              = 6 * time.Second
	streamingDispatchMaxMultiplier           = 2.0
	streamingDispatchCatchupMultiplier       = 4.0
	streamingDispatchWorkerMultiplier        = 3
	streamingDispatchCatchupWorkerMultiplier = 6
	streamingHotPathScaleLag                 = 10 * time.Second
	streamingFailurePressureMin              = 1000
	streamingFailurePressurePercent          = 25
	streamingFailurePressureHold             = 2 * time.Minute
	streamingFailurePressureLatency          = streamingDefaultLatency
)

type streamingTarget struct {
	site            db.Site
	dueAt           time.Time
	inFlight        bool
	queued          bool
	active          bool
	lastProjectedAt time.Time

	checkRequest       checker.Request
	checkRequestConfig streamingRequestConfig
	checkRequestReady  bool
	checkRequestDirty  bool
}

type streamingRequestConfig struct {
	timeoutSeconds      int
	bodyReadMaxBytes    int64
	bodyReadMaxMS       int
	keywordReadMaxBytes int64
	keywordReadMaxMS    int
	requestMethod       string
	detectionProfile    string
}

type streamingPlanner struct {
	targets      map[int64]*streamingTarget
	due          streamingDueWheel
	requiredRate float64
}

type streamingDueWheel struct {
	buckets     map[int64][]int64
	nextDueUnix int64
}

type streamingDueBucket struct {
	dueUnix   int64
	targetIDs []int64
}

func newStreamingDueWheel(capacity int) streamingDueWheel {
	if capacity < 1 {
		capacity = 1
	}
	return streamingDueWheel{buckets: make(map[int64][]int64, capacity)}
}

func (w *streamingDueWheel) schedule(dueUnix, targetID int64) {
	if w.buckets == nil {
		w.buckets = make(map[int64][]int64)
	}
	w.buckets[dueUnix] = append(w.buckets[dueUnix], targetID)
	if w.nextDueUnix == 0 || dueUnix < w.nextDueUnix {
		w.nextDueUnix = dueUnix
	}
}

func (w *streamingDueWheel) popReady(nowUnix int64) []streamingDueBucket {
	if w == nil || len(w.buckets) == 0 {
		return nil
	}
	if w.nextDueUnix == 0 {
		w.refreshNextDue()
	}
	if w.nextDueUnix > nowUnix {
		return nil
	}
	readyTimes := make([]int64, 0, min(len(w.buckets), 1024))
	for dueUnix := range w.buckets {
		if dueUnix <= nowUnix {
			readyTimes = append(readyTimes, dueUnix)
		}
	}
	if len(readyTimes) == 0 {
		return nil
	}
	sort.Slice(readyTimes, func(i, j int) bool {
		return readyTimes[i] < readyTimes[j]
	})
	ready := make([]streamingDueBucket, 0, len(readyTimes))
	for _, dueUnix := range readyTimes {
		targetIDs := w.buckets[dueUnix]
		delete(w.buckets, dueUnix)
		ready = append(ready, streamingDueBucket{
			dueUnix:   dueUnix,
			targetIDs: targetIDs,
		})
	}
	w.refreshNextDue()
	return ready
}

func (w *streamingDueWheel) refreshNextDue() {
	w.nextDueUnix = 0
	for dueUnix := range w.buckets {
		if w.nextDueUnix == 0 || dueUnix < w.nextDueUnix {
			w.nextDueUnix = dueUnix
		}
	}
}

type streamingReloadResult struct {
	sites     []db.Site
	bucketMin int
	bucketMax int
	err       error
}

type streamingProjectionFlushResult struct {
	checks   []db.SiteCheck
	duration time.Duration
	err      error
}

func newStreamingPlanner(sites []db.Site, now time.Time) *streamingPlanner {
	p := &streamingPlanner{
		targets: make(map[int64]*streamingTarget, len(sites)),
		due:     newStreamingDueWheel(streamingDueWheelInitialCapacity(sites)),
	}
	p.merge(sites, now)
	return p
}

func streamingDueWheelInitialCapacity(sites []db.Site) int {
	maxSeconds := 0
	for _, site := range sites {
		seconds := int(streamingCheckCadence(site) / time.Second)
		if seconds > maxSeconds {
			maxSeconds = seconds
		}
	}
	if maxSeconds < 1 {
		return 1
	}
	return maxSeconds
}

func (p *streamingPlanner) merge(sites []db.Site, now time.Time) (added, updated, removed int) {
	seen := make(map[int64]struct{}, len(sites))
	for _, site := range sites {
		targetID := monitorTargetID(site)
		seen[targetID] = struct{}{}
		if target, ok := p.targets[targetID]; ok {
			if !streamingSiteCheckConfigEqual(target.site, site) {
				target.checkRequestDirty = true
			}
			target.site = site
			target.active = true
			updated++
			continue
		}
		target := &streamingTarget{
			site:              site,
			active:            true,
			checkRequestDirty: true,
		}
		if site.LastCheckedAt != nil {
			target.lastProjectedAt = site.LastCheckedAt.UTC()
		}
		p.targets[targetID] = target
		p.scheduleAt(target, initialStreamingDueAt(site, now))
		added++
	}
	for blogID, target := range p.targets {
		if _, ok := seen[blogID]; ok {
			continue
		}
		target.active = false
		delete(p.targets, blogID)
		removed++
	}
	p.recalculateRequiredRate()
	return added, updated, removed
}

func streamingSiteCheckConfigEqual(a, b db.Site) bool {
	return a.MonitorURL == b.MonitorURL &&
		a.CheckInterval == b.CheckInterval &&
		stringPtrEqual(a.CheckKeyword, b.CheckKeyword) &&
		stringPtrEqual(a.ForbiddenKeyword, b.ForbiddenKeyword) &&
		stringPtrEqual(a.ForbiddenKeywords, b.ForbiddenKeywords) &&
		stringPtrEqual(a.CustomHeaders, b.CustomHeaders) &&
		a.TimeoutSeconds == b.TimeoutSeconds &&
		a.RedirectPolicy == b.RedirectPolicy &&
		a.RequestMethod == b.RequestMethod &&
		a.DetectionProfile == b.DetectionProfile
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (p *streamingPlanner) activeCount() int {
	return len(p.targets)
}

func (p *streamingPlanner) requiredChecksPerSecond() float64 {
	return p.requiredRate
}

func (p *streamingPlanner) recalculateRequiredRate() {
	var rate float64
	for _, target := range p.targets {
		interval := streamingCheckCadence(target.site)
		if interval <= 0 {
			continue
		}
		rate += 1 / interval.Seconds()
	}
	p.requiredRate = rate
}

func (p *streamingPlanner) scheduleAfterResult(target *streamingTarget, res checker.Result, checkedAt time.Time, allowImmediateRetry bool) {
	siteInterval := siteCheckInterval(target.site)
	if allowImmediateRetry && res.IsFailure() && target.site.SiteStatus != statusConfirmedDown && siteInterval > failedCheckRetryInterval {
		p.scheduleAt(target, checkedAt.Add(failedCheckRetryInterval))
		return
	}

	interval := streamingCheckCadence(target.site)
	next := target.dueAt.Add(interval)
	for !next.After(checkedAt) {
		next = next.Add(interval)
	}
	p.scheduleAt(target, next)
}

func (p *streamingPlanner) scheduleAtNextPhaseAfter(target *streamingTarget, after time.Time) {
	interval := streamingCheckCadence(target.site)
	phase := streamingPhaseOffset(monitorTargetID(target.site), interval)
	p.scheduleAt(target, nextStreamingPhaseAt(after.Add(time.Second), interval, phase))
}

func (p *streamingPlanner) scheduleAt(target *streamingTarget, dueAt time.Time) {
	dueAt = dueAt.UTC().Truncate(time.Second)
	target.dueAt = dueAt
	dueUnix := dueAt.Unix()
	p.due.schedule(dueUnix, monitorTargetID(target.site))
}

func (p *streamingPlanner) popDue(now time.Time) []*streamingTarget {
	nowUnix := now.UTC().Unix()
	var due []*streamingTarget
	for _, bucket := range p.due.popReady(nowUnix) {
		for _, targetID := range bucket.targetIDs {
			target, ok := p.targets[targetID]
			if !ok || !target.active || target.inFlight || target.queued || target.dueAt.Unix() != bucket.dueUnix {
				continue
			}
			target.queued = true
			due = append(due, target)
		}
	}
	return due
}

type streamingStats struct {
	selected           int
	dispatched         int
	completed          int
	backpressureWaits  int
	staleResults       int
	checkFailures      int
	checkOffline       int
	checkSuccesses     int
	historyRows        int
	historyErrors      int
	sslRows            int
	sslErrors          int
	eventDuration      time.Duration
	historyDuration    time.Duration
	sslDuration        time.Duration
	sideEffectRows     int
	sideEffectWaits    int
	sideEffectPaused   int
	resultPaused       int
	dispatchLimited    int
	pressureSuppressed int
	latencyTotal       time.Duration
	latencyCount       int
	successLatency     time.Duration
	successLatencyN    int
	maxLag             time.Duration
	errorTimeouts      int
	errorConnects      int
	errorSSL           int
	errorRedirects     int
	errorKeywords      int
	errorBodyReads     int
	errorTLSExpired    int
	errorTLSDeprecated int
	errorOther         int
	checkCohorts       map[checkCohortKey]int
}

func (s *streamingStats) addResult(res checker.Result, lag time.Duration, siteStatus int) {
	s.completed++
	s.checkCohorts = incrementCheckCohort(s.checkCohorts, res)
	if res.Success {
		s.checkSuccesses++
	} else {
		s.checkFailures++
		if siteStatus == statusConfirmedDown {
			s.checkOffline++
		}
	}
	if res.RTT > 0 {
		s.latencyTotal += res.RTT
		s.latencyCount++
		if !res.IsFailure() {
			s.successLatency += res.RTT
			s.successLatencyN++
		}
	}
	if lag > s.maxLag {
		s.maxLag = lag
	}
	if res.ErrorCode != checker.ErrorNone {
		s.addErrorCode(res.ErrorCode)
	}
}

func (s *streamingStats) addErrorCode(code int) {
	switch code {
	case checker.ErrorTimeout:
		s.errorTimeouts++
	case checker.ErrorConnect:
		s.errorConnects++
	case checker.ErrorSSL:
		s.errorSSL++
	case checker.ErrorRedirect:
		s.errorRedirects++
	case checker.ErrorKeyword:
		s.errorKeywords++
	case checker.ErrorBodyRead:
		s.errorBodyReads++
	case checker.ErrorTLSExpired:
		s.errorTLSExpired++
	case checker.ErrorTLSDeprecated:
		s.errorTLSDeprecated++
	default:
		s.errorOther++
	}
}

func (s *streamingStats) addSideEffects(summary resultProcessSummary) {
	s.sideEffectRows += summary.processed
	s.historyRows += summary.historyRows
	s.historyErrors += summary.historyErrors
	s.sslRows += summary.sslRows
	s.sslErrors += summary.sslErrors
	s.eventDuration += summary.eventDuration
	s.historyDuration += summary.historyDuration
	s.sslDuration += summary.sslDuration
}

func (s streamingStats) averageLatency() time.Duration {
	if s.latencyCount == 0 {
		return 0
	}
	return s.latencyTotal / time.Duration(s.latencyCount)
}

func (s streamingStats) scaleLatency() time.Duration {
	if s.successLatencyN > 0 {
		return s.successLatency / time.Duration(s.successLatencyN)
	}
	return 0
}

type streamingSideEffectJob struct {
	site db.Site
	res  checker.Result
}

type streamingSideEffectReport struct {
	targetID      int64
	blogID        int64
	status        int
	resultFailure bool
	checkedAt     time.Time
	summary       resultProcessSummary
}

type streamingSideEffectProcessor struct {
	ctx     <-chan struct{}
	shards  []chan streamingSideEffectJob
	reports chan streamingSideEffectReport
	wg      sync.WaitGroup
}

func (o *Orchestrator) newStreamingSideEffectProcessor(shards, queueCap int) *streamingSideEffectProcessor {
	if shards < 1 {
		shards = 1
	}
	if queueCap < shards {
		queueCap = shards
	}
	perShard := queueCap / shards
	if perShard < 1 {
		perShard = 1
	}
	p := &streamingSideEffectProcessor{
		ctx:     o.ctx.Done(),
		shards:  make([]chan streamingSideEffectJob, shards),
		reports: make(chan streamingSideEffectReport, queueCap),
	}
	for i := range shards {
		ch := make(chan streamingSideEffectJob, perShard)
		p.shards[i] = ch
		p.wg.Add(1)
		go p.runShard(o, ch)
	}
	return p
}

func (p *streamingSideEffectProcessor) runShard(o *Orchestrator, jobs <-chan streamingSideEffectJob) {
	defer p.wg.Done()
	statusByTarget := make(map[int64]int)
	sslExpiryByTarget := make(map[int64]*time.Time)
	historyRows := make([]db.CheckHistoryRow, 0, streamingHistoryBatchSize)
	historyTicker := time.NewTicker(streamingHistoryFlushInterval)
	defer historyTicker.Stop()
	flushHistory := func() bool {
		if len(historyRows) == 0 {
			return true
		}
		summary := o.recordStreamingHistoryRows(historyRows)
		historyRows = historyRows[:0]
		select {
		case p.reports <- streamingSideEffectReport{summary: summary}:
			return true
		case <-p.ctx:
			return false
		}
	}
	defer flushHistory()
	for {
		select {
		case <-p.ctx:
			flushHistory()
			return
		case <-historyTicker.C:
			if !flushHistory() {
				return
			}
		case job, ok := <-jobs:
			if !ok {
				flushHistory()
				return
			}
			site := job.site
			targetID := monitorTargetID(site)
			if status, ok := statusByTarget[targetID]; ok {
				site.SiteStatus = status
			}
			if expiry, ok := sslExpiryByTarget[targetID]; ok {
				site.SSLExpiryDate = expiry
			}
			summary, updated := o.processStreamingSideEffects(site, job.res)
			statusByTarget[targetID] = updated.SiteStatus
			if updated.SSLExpiryDate != nil {
				expiry := *updated.SSLExpiryDate
				sslExpiryByTarget[targetID] = &expiry
			} else {
				delete(sslExpiryByTarget, targetID)
			}
			if job.res.IsFailure() {
				historyRows = append(historyRows, checkHistoryRowForResult(site.BlogID, job.res))
				if len(historyRows) >= streamingHistoryBatchSize && !flushHistory() {
					return
				}
			}
			select {
			case p.reports <- streamingSideEffectReport{
				targetID:      targetID,
				blogID:        site.BlogID,
				status:        updated.SiteStatus,
				resultFailure: job.res.IsFailure(),
				checkedAt:     resultCheckedAt(job.res),
				summary:       summary,
			}:
			case <-p.ctx:
				return
			}
		}
	}
}

func (p *streamingSideEffectProcessor) enqueue(job streamingSideEffectJob) bool {
	if len(p.shards) == 0 {
		return false
	}
	ch := p.shards[streamingSideEffectShard(monitorTargetID(job.site), len(p.shards))]
	select {
	case ch <- job:
		return true
	case <-p.ctx:
		return false
	}
}

func (p *streamingSideEffectProcessor) tryEnqueue(job streamingSideEffectJob) bool {
	if len(p.shards) == 0 {
		return false
	}
	ch := p.shards[streamingSideEffectShard(monitorTargetID(job.site), len(p.shards))]
	select {
	case ch <- job:
		return true
	default:
		return false
	}
}

func (p *streamingSideEffectProcessor) reportsChannel() <-chan streamingSideEffectReport {
	return p.reports
}

func (p *streamingSideEffectProcessor) queueDepth() int {
	total := 0
	for _, ch := range p.shards {
		total += len(ch)
	}
	return total
}

func (p *streamingSideEffectProcessor) stop() {
	for _, ch := range p.shards {
		close(ch)
	}
	p.wg.Wait()
	close(p.reports)
}

func streamingSideEffectShard(blogID int64, shards int) int {
	if shards <= 1 {
		return 0
	}
	if blogID < 0 {
		blogID = -blogID
	}
	return int(blogID % int64(shards))
}

func streamingSideEffectShardCount(activeTargets int) int {
	shards := runtime.GOMAXPROCS(0) * 4
	if targetBased := activeTargets / 500; targetBased > shards {
		shards = targetBased
	}
	if shards < streamingMinSideEffectShards {
		return streamingMinSideEffectShards
	}
	if shards > streamingMaxSideEffectShards {
		return streamingMaxSideEffectShards
	}
	return shards
}

func (o *Orchestrator) runStreamingEngine() {
	cfg := config.Get()
	log.Printf("orchestrator: streaming scheduler starting, host=%s buckets=%d-%d", o.hostname, o.bucketMin, o.bucketMax)
	if _, err := o.refreshStreamingBuckets(cfg); err != nil {
		log.Printf("orchestrator: streaming bucket refresh failed: %v", err)
	}
	if cfg.RolloutMode == config.RolloutModeAPIControlled && o.bucketMax < o.bucketMin {
		log.Printf("orchestrator: streaming scheduler waiting; no active API-controlled bucket lock")
		return
	}
	sites, err := o.loadStreamingSites(cfg)
	if err != nil {
		log.Printf("orchestrator: streaming initial target load failed: %v", err)
		sites = nil
	}
	planner := newStreamingPlanner(sites, nowFunc().UTC())
	o.configureStreamingPool(cfg, planner, streamingBootstrapLatency)
	sideEffectShards := streamingSideEffectShardCount(planner.activeCount())
	sideEffects := o.newStreamingSideEffectProcessor(sideEffectShards, streamingQueueCap(streamingWorkerTarget(cfg, planner, streamingBootstrapLatency), planner.activeCount()))
	config.Debugf("orchestrator: streaming scheduler loaded targets=%d required_rate=%.2f/s workers=%d queue_cap=%d side_effect_shards=%d",
		planner.activeCount(),
		planner.requiredChecksPerSecond(),
		o.pool.WorkerCount(),
		streamingQueueCap(streamingWorkerTarget(cfg, planner, streamingBootstrapLatency), planner.activeCount()),
		sideEffectShards,
	)

	tick := time.NewTicker(streamingTickInterval)
	defer tick.Stop()

	var (
		pending             []*streamingTarget
		pendingProjection   = make(map[int64]db.SiteCheck)
		pendingSideEffects  = make(map[int64]int)
		sideEffectStatus    = make(map[int64]int)
		stats               streamingStats
		lastReport          = nowFunc().UTC()
		lastReload          = lastReport
		lastScale           = lastReport
		lastDispatch        = lastReport
		lastProjectionFlush = lastReport
		lastHeartbeat       = lastReport
		lastActiveCountPoll = lastReport
		lastRolloutPoll     = lastReport
		pressureUntil       time.Time
		hotPathPressure     bool
		reloadResults       = make(chan streamingReloadResult, 1)
		reloadInFlight      bool
		projectionResults   = make(chan streamingProjectionFlushResult, 1)
		projectionInFlight  bool
		telemetryResults    = make(chan struct{}, 1)
		telemetryInFlight   bool
	)

	startVeriflierTelemetrySync := func(syncCfg *config.Config) {
		if telemetryInFlight {
			return
		}
		telemetryInFlight = true
		go func() {
			o.syncVeriflierAgentTelemetry(syncCfg)
			select {
			case telemetryResults <- struct{}{}:
			case <-o.ctx.Done():
			}
		}()
	}
	startVeriflierTelemetrySync(cfg)

	applyReload := func(reload streamingReloadResult) {
		reloadInFlight = false
		if reload.err != nil {
			log.Printf("orchestrator: streaming target reload failed: %v", reload.err)
			return
		}
		if reload.bucketMin != o.bucketMin || reload.bucketMax != o.bucketMax {
			config.Debugf("orchestrator: streaming target reload discarded stale bucket snapshot loaded=%d-%d current=%d-%d",
				reload.bucketMin, reload.bucketMax, o.bucketMin, o.bucketMax)
			lastReload = time.Time{}
			return
		}

		wasEmpty := planner.activeCount() == 0
		wasActive := planner.activeCount() > 0
		added, updated, removed := planner.merge(reload.sites, nowFunc().UTC())
		if wasEmpty && planner.activeCount() > 0 {
			o.configureStreamingPool(cfg, planner, streamingBootstrapLatency)
			sideEffects.stop()
			sideEffectShards = streamingSideEffectShardCount(planner.activeCount())
			sideEffects = o.newStreamingSideEffectProcessor(sideEffectShards, streamingQueueCap(streamingWorkerTarget(cfg, planner, streamingBootstrapLatency), planner.activeCount()))
			pendingSideEffects = make(map[int64]int)
			sideEffectStatus = make(map[int64]int)
		} else if wasActive && planner.activeCount() == 0 {
			o.configureStreamingPool(cfg, planner, streamingBootstrapLatency)
			sideEffects.stop()
			sideEffectShards = streamingSideEffectShardCount(planner.activeCount())
			sideEffects = o.newStreamingSideEffectProcessor(sideEffectShards, streamingQueueCap(streamingWorkerTarget(cfg, planner, streamingBootstrapLatency), planner.activeCount()))
			pending = nil
			pendingSideEffects = make(map[int64]int)
			sideEffectStatus = make(map[int64]int)
		}
		config.Debugf("orchestrator: streaming target reload active=%d added=%d updated=%d removed=%d required_rate=%.2f/s",
			planner.activeCount(), added, updated, removed, planner.requiredChecksPerSecond())
	}

	startReload := func(reason string, now time.Time) {
		if reloadInFlight {
			return
		}
		reloadInFlight = true
		lastReload = now
		reloadCfg := cfg
		bucketMin, bucketMax := o.bucketMin, o.bucketMax
		config.Debugf("orchestrator: streaming target reload started reason=%s buckets=%d-%d", reason, bucketMin, bucketMax)
		go func() {
			sites, err := o.loadStreamingSitesForRange(o.ctx, reloadCfg, bucketMin, bucketMax)
			result := streamingReloadResult{
				sites:     sites,
				bucketMin: bucketMin,
				bucketMax: bucketMax,
				err:       err,
			}
			select {
			case reloadResults <- result:
			case <-o.ctx.Done():
			}
		}()
	}

	startProjectionFlush := func(checks []db.SiteCheck) {
		if len(checks) == 0 || projectionInFlight {
			return
		}
		projectionInFlight = true
		go func() {
			start := time.Now()
			err := dbMarkSitesChecked(o.ctx, checks)
			result := streamingProjectionFlushResult{
				checks:   checks,
				duration: time.Since(start),
				err:      err,
			}
			select {
			case projectionResults <- result:
			case <-o.ctx.Done():
			}
		}()
	}

	handleProjectionFlushResult := func(result streamingProjectionFlushResult) {
		projectionInFlight = false
		if result.err != nil {
			log.Printf("orchestrator: streaming legacy freshness projection rows=%d: %v", len(result.checks), result.err)
			for _, check := range result.checks {
				existing, ok := pendingProjection[check.BlogID]
				if !ok || existing.CheckedAt.Before(check.CheckedAt) {
					pendingProjection[check.BlogID] = check
				}
			}
			return
		}
		if m := metricsClientFunc(); m != nil {
			m.Increment("scheduler.streaming.legacy_projection.row.count", len(result.checks))
			m.Timing("scheduler.streaming.legacy_projection.time", result.duration)
		}
	}

	handleResult := func(res checker.Result, now time.Time) {
		targetID := checkResultTargetID(res)
		target, ok := planner.targets[targetID]
		if !ok || !target.inFlight {
			stats.staleResults++
			return
		}
		target.inFlight = false
		checkedAt := resultCheckedAt(res)
		lag := checkedAt.Sub(target.dueAt)
		if lag < 0 {
			lag = 0
		}
		stats.addResult(res, lag, target.site.SiteStatus)
		failurePressureActive := now.Before(pressureUntil)
		if streamingFailurePressure(stats) {
			pressureUntil = now.Add(streamingFailurePressureHold)
			failurePressureActive = true
		}
		pressureActive := failurePressureActive || hotPathPressure
		o.totalChecked++
		suppressedByPressure := streamingSideEffectsSuppressedByPressure(target, res, pendingSideEffects, sideEffectStatus, o.retries, pressureActive)
		if suppressedByPressure {
			stats.pressureSuppressed++
		}
		if !suppressedByPressure && streamingSideEffectsNeeded(target, res, pendingSideEffects, sideEffectStatus, o.retries, pressureActive) {
			job := streamingSideEffectJob{site: target.site, res: res}
			if !sideEffects.tryEnqueue(job) {
				stats.sideEffectWaits++
				if !sideEffects.enqueue(job) {
					return
				}
			}
			pendingSideEffects[targetID]++
		}
		planner.scheduleAfterResult(target, res, checkedAt, streamingAllowImmediateRetry(target, res, o.retries, pressureActive))
		o.queueStreamingProjection(cfg, target, checkedAt, now, pendingProjection)
	}

	dispatchPending := func(now time.Time, recordPause bool, minInterval time.Duration) {
		if len(pending) == 0 {
			return
		}
		if minInterval > 0 && now.Sub(lastDispatch) < minInterval {
			return
		}
		if streamingShouldPauseDispatchForResultBacklog(o.pool.ResultDepth(), o.pool.WorkerCount(), planner.activeCount(), o.pool.ActiveCount(), o.pool.QueueDepth()) {
			if recordPause {
				stats.resultPaused++
			}
			return
		}
		if sideEffectDepth := sideEffects.queueDepth(); sideEffectDepth >= streamingSideEffectBackpressureDepth(o.pool.WorkerCount(), planner.activeCount()) {
			if recordPause {
				stats.sideEffectPaused++
			}
			return
		}
		dispatchElapsed := now.Sub(lastDispatch)
		budget := streamingDispatchBudget(planner.requiredChecksPerSecond(), len(pending), o.pool.WorkerCount(), dispatchElapsed, stats.maxLag, o.pool.ResultDepth(), planner.activeCount())
		pending = o.dispatchStreamingPending(cfg, pending, budget, &stats)
		lastDispatch = now
	}

	drainResults := func(limit int, dispatchDuringDrain bool) {
		now := nowFunc().UTC()
		for processed := 0; processed < limit; processed++ {
			select {
			case res := <-o.pool.Results():
				handleResult(res, now)
				if dispatchDuringDrain && processed%streamingResultDispatchStride == streamingResultDispatchStride-1 {
					now = nowFunc().UTC()
					dispatchPending(now, false, streamingDispatchWakeInterval)
				}
			default:
				if dispatchDuringDrain {
					dispatchPending(now, false, streamingDispatchWakeInterval)
				}
				return
			}
		}
		if dispatchDuringDrain {
			dispatchPending(nowFunc().UTC(), false, streamingDispatchWakeInterval)
		}
	}

	stopStreaming := func() {
		o.flushStreamingProjection(pendingProjection)
		sideEffects.stop()
		if o.pool != nil {
			o.pool.Drain()
		}
	}

	handleTick := func() bool {
		now := nowFunc().UTC()
		cfg = config.Get()
		reloadReason := ""
		if cfg.RolloutMode == config.RolloutModeStandby {
			log.Printf("orchestrator: streaming scheduler stopping; rollout_mode=%s", cfg.RolloutMode)
			return false
		}
		if cfg.RolloutMode == config.RolloutModeAPIControlled && now.Sub(lastRolloutPoll) >= schedulerAPIControlledRangePollInterval {
			bucketsChanged, err := o.refreshStreamingBuckets(cfg)
			if err != nil {
				log.Printf("orchestrator: streaming API-controlled bucket refresh failed: %v", err)
				return false
			}
			if o.bucketMax < o.bucketMin {
				log.Printf("orchestrator: streaming scheduler stopping; API-controlled bucket lock released")
				return false
			}
			if bucketsChanged {
				lastReload = time.Time{}
				reloadReason = "api_controlled_bucket_change"
			}
			lastRolloutPoll = now
		}
		drainResults(streamingResultDrainLimitFor(o.pool.ResultDepth()), true)
		o.refreshVeriflierClients(cfg)
		failurePressureActive := now.Before(pressureUntil) || streamingFailurePressure(stats)
		hotPathPressure = streamingHotPathBehind(planner, len(pending), o.pool.ResultDepth(), sideEffects.queueDepth(), o.pool.WorkerCount(), stats)
		pressureActive := failurePressureActive || hotPathPressure
		if now.Sub(lastScale) >= streamingScaleInterval {
			o.applyStreamingWorkerTarget(cfg, planner, stats, len(pending), o.pool.ResultDepth(), sideEffects.queueDepth(), failurePressureActive, hotPathPressure)
			lastScale = now
		}

		if now.Sub(lastHeartbeat) >= schedulerBroadReportInterval {
			var bucketsChanged bool
			var err error
			if cfg.RolloutMode != config.RolloutModeAPIControlled {
				bucketsChanged, err = o.refreshStreamingBuckets(cfg)
				if err != nil {
					log.Printf("orchestrator: streaming bucket refresh failed: %v", err)
				}
			}
			startVeriflierTelemetrySync(cfg)
			if bucketsChanged {
				lastReload = time.Time{}
				reloadReason = "bucket_change"
			}
			lastHeartbeat = now
		}

		if now.Sub(lastActiveCountPoll) >= streamingActiveCountPollIntervalFor(planner) {
			if count, err := dbCountActiveSites(o.ctx, o.bucketMin, o.bucketMax); err != nil {
				log.Printf("orchestrator: streaming active target count check failed: %v", err)
			} else if count != planner.activeCount() {
				config.Debugf("orchestrator: streaming active target count changed db=%d memory=%d; reloading targets", count, planner.activeCount())
				lastReload = time.Time{}
				reloadReason = "active_count_changed"
			}
			lastActiveCountPoll = now
		}

		reloadInterval := streamingTargetReloadInterval(cfg, planner)
		if now.Sub(lastReload) >= reloadInterval {
			if reloadReason == "" {
				reloadReason = "periodic"
			}
			if reloadReason == "periodic" && streamingShouldDeferPeriodicReload(planner, len(pending), o.pool.ResultDepth(), sideEffects.queueDepth(), o.pool.WorkerCount(), stats) {
				lastReload = streamingDeferredReloadLastReload(now, reloadInterval)
				config.Debugf("orchestrator: streaming target reload deferred reason=periodic active=%d pending=%d result_depth=%d side_effect_depth=%d max_lag=%s",
					planner.activeCount(), len(pending), o.pool.ResultDepth(), sideEffects.queueDepth(), stats.maxLag.Round(time.Millisecond))
			} else {
				startReload(reloadReason, now)
			}
		}

		due := planner.popDue(now)
		stats.selected += len(due)
		pending = append(pending, due...)
		dispatchPending(now, true, 0)

		if now.Sub(lastProjectionFlush) >= streamingProjectionFlushInterval {
			if !projectionInFlight && len(pendingProjection) > 0 {
				checks := streamingProjectionFlushBatch(pendingProjection, streamingProjectionFlushRowLimit(planner.requiredChecksPerSecond()))
				startProjectionFlush(checks)
			}
			lastProjectionFlush = now
		}

		if now.Sub(lastReport) >= streamingReportInterval {
			reportElapsed := now.Sub(lastReport)
			o.reportStreamingStats(cfg, planner, stats, len(pending), sideEffects.queueDepth(), reportElapsed, pressureActive)
			stats = streamingStats{}
			lastReport = now
		}
		return true
	}

	drainReadyTick := func() bool {
		select {
		case <-tick.C:
			return handleTick()
		default:
			return true
		}
	}

	for {
		select {
		case <-o.ctx.Done():
			o.flushStreamingProjection(pendingProjection)
			sideEffects.stop()
			o.shutdown()
			return
		case result := <-projectionResults:
			handleProjectionFlushResult(result)
		case report := <-sideEffects.reportsChannel():
			stats.addSideEffects(report.summary)
			if report.targetID != 0 {
				if pendingSideEffects[report.targetID] <= 1 {
					delete(pendingSideEffects, report.targetID)
				} else {
					pendingSideEffects[report.targetID]--
				}
				sideEffectStatus[report.targetID] = report.status
				if target, ok := planner.targets[report.targetID]; ok {
					target.site.SiteStatus = report.status
					rescheduleStreamingAfterSideEffect(planner, target, report)
				}
			}
		case reload := <-reloadResults:
			applyReload(reload)
		case <-telemetryResults:
			telemetryInFlight = false
		case res := <-o.pool.Results():
			now := nowFunc().UTC()
			handleResult(res, now)
			drainResults(streamingResultDrainLimitFor(o.pool.ResultDepth()), true)
			dispatchPending(nowFunc().UTC(), false, streamingDispatchWakeInterval)
			if !drainReadyTick() {
				stopStreaming()
				return
			}
		case <-tick.C:
			if !handleTick() {
				stopStreaming()
				return
			}
		}
	}
}

func (o *Orchestrator) configureStreamingPool(cfg *config.Config, planner *streamingPlanner, latency time.Duration) {
	if o.pool != nil {
		o.pool.Drain()
	}
	workerTarget := streamingWorkerTarget(cfg, planner, latency)
	queueCap := streamingQueueCap(workerTarget, planner.activeCount())
	initial := cfg.NumWorkers
	if initial > workerTarget {
		initial = workerTarget
	}
	if initial < 1 {
		initial = 1
	}
	o.pool = checker.NewPoolWithQueueCap(initial, 1, workerTarget, queueCap)
	if planner.activeCount() > 0 {
		o.pool.EnsureSize(workerTarget)
	}
}

func (o *Orchestrator) refreshStreamingBuckets(cfg *config.Config) (bool, error) {
	oldMin, oldMax := o.bucketMin, o.bucketMax
	if cfg.RolloutMode == config.RolloutModeStandby {
		o.bucketMin = 0
		o.bucketMax = -1
		return oldMin != o.bucketMin || oldMax != o.bucketMax, nil
	}
	if cfg.RolloutMode == config.RolloutModeAPIControlled {
		ok, err := o.refreshAPIControlledRange()
		if err != nil {
			return false, err
		}
		if !ok {
			o.bucketMin = 0
			o.bucketMax = -1
		}
		return oldMin != o.bucketMin || oldMax != o.bucketMax, nil
	}
	if o.usesPinnedBuckets(cfg) {
		err := o.ClaimBuckets()
		return oldMin != o.bucketMin || oldMax != o.bucketMax, err
	}
	if err := dbHeartbeat(o.ctx, o.hostname); err != nil {
		log.Printf("orchestrator: streaming heartbeat failed: %v", err)
	}
	err := o.ClaimBuckets()
	return oldMin != o.bucketMin || oldMax != o.bucketMax, err
}

func (o *Orchestrator) loadStreamingSites(cfg *config.Config) ([]db.Site, error) {
	return o.loadStreamingSitesForRange(o.ctx, cfg, o.bucketMin, o.bucketMax)
}

func (o *Orchestrator) loadStreamingSitesForRange(ctx context.Context, cfg *config.Config, bucketMin, bucketMax int) ([]db.Site, error) {
	pageSize := streamingLoadPageSize(cfg)
	var (
		afterMonitorSiteID int64
		sites              []db.Site
	)
	for {
		page, err := dbListActiveSites(ctx, bucketMin, bucketMax, afterMonitorSiteID, pageSize)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return sites, nil
		}
		sites = append(sites, page...)
		afterMonitorSiteID = page[len(page)-1].ID
		if len(page) < pageSize {
			return sites, nil
		}
	}
}

func (o *Orchestrator) dispatchStreamingPending(cfg *config.Config, pending []*streamingTarget, budget int, stats *streamingStats) []*streamingTarget {
	if budget <= 0 {
		return pending
	}
	dispatched := 0
	for len(pending) > 0 && dispatched < budget {
		target := pending[0]
		if !target.active || target.inFlight || !target.queued {
			target.queued = false
			pending = pending[1:]
			continue
		}
		if !o.pool.Submit(streamingCheckRequestForTarget(cfg, target)) {
			stats.backpressureWaits++
			return pending
		}
		target.queued = false
		target.inFlight = true
		stats.dispatched++
		dispatched++
		pending = pending[1:]
	}
	if len(pending) > 0 && dispatched >= budget {
		stats.dispatchLimited++
	}
	return pending
}

func (o *Orchestrator) processStreamingSideEffects(site db.Site, res checker.Result) (resultProcessSummary, db.Site) {
	summary := resultProcessSummary{processed: 1}

	sslStart := time.Now()
	if fullDetectionsEnabled(config.Get(), site) {
		if res.TLSVersion != 0 {
			o.checkTLSDeprecated(site, res)
		}
		if res.SSLExpiry != nil {
			if shouldUpdateSSLExpiry(site.SSLExpiryDate, *res.SSLExpiry) {
				o.updateSSLExpiries([]db.SiteSSLExpiry{{
					BlogID: site.BlogID,
					Expiry: *res.SSLExpiry,
				}}, &summary)
				expiry := *res.SSLExpiry
				site.SSLExpiryDate = &expiry
			}
			o.checkSSLAlerts(site, *res.SSLExpiry)
		}
	}
	summary.sslDuration += time.Since(sslStart)

	eventStart := time.Now()
	if res.IsProbeSafetyBlock() {
		o.handleProbeSafetyBlock(site, res)
	} else if !res.IsFailure() {
		o.handleRecovery(site, res)
		site.SiteStatus = statusRunning
	} else {
		failureActive := o.handleFailure(site, res)
		if retry := o.retries.get(monitorTargetID(site)); retry != nil && (failureActive || retry.eventID > 0) {
			site.SiteStatus = statusDown
		} else if status, err := dbGetSiteStatus(o.ctx, site.ID, site.BlogID); err != nil {
			log.Printf("orchestrator: streaming refresh site status blog_id=%d: %v", site.BlogID, err)
		} else {
			site.SiteStatus = status
		}
	}
	summary.eventDuration += time.Since(eventStart)
	return summary, site
}

func streamingCheckRequestForTarget(cfg *config.Config, target *streamingTarget) checker.Request {
	if target == nil {
		return checker.Request{}
	}
	requestConfig := streamingRequestConfigForSite(cfg, target.site)
	if !target.checkRequestReady || target.checkRequestDirty || target.checkRequestConfig != requestConfig {
		target.checkRequest = checkRequestForSite(cfg, target.site)
		target.checkRequestConfig = requestConfig
		target.checkRequestReady = true
		target.checkRequestDirty = false
	}
	return target.checkRequest
}

func streamingRequestConfigForSite(cfg *config.Config, site db.Site) streamingRequestConfig {
	method := effectiveCheckMethod(cfg, site)
	profile := effectiveDetectionProfile(cfg, site, method)
	return streamingRequestConfig{
		timeoutSeconds:      timeoutForSite(cfg, site),
		bodyReadMaxBytes:    cfg.BodyReadMaxBytes,
		bodyReadMaxMS:       cfg.BodyReadMaxMS,
		keywordReadMaxBytes: cfg.KeywordReadMaxBytes,
		keywordReadMaxMS:    cfg.KeywordReadMaxMS,
		requestMethod:       method,
		detectionProfile:    profile,
	}
}

func streamingSideEffectsNeeded(target *streamingTarget, res checker.Result, pending map[int64]int, statusCache map[int64]int, retries *retryQueue, pressure bool) bool {
	if target == nil {
		return false
	}
	if streamingSideEffectsSuppressedByPressure(target, res, pending, statusCache, retries, pressure) {
		return false
	}
	targetID := monitorTargetID(target.site)
	if pending[targetID] > 0 {
		return true
	}
	status := target.site.SiteStatus
	if cached, ok := statusCache[targetID]; ok {
		status = cached
	}
	retrying := retries != nil && retries.get(targetID) != nil
	if res.IsFailure() || res.TLSVersion != 0 || res.SSLExpiry != nil {
		return true
	}
	if status != statusRunning {
		return true
	}
	return retrying
}

func streamingSideEffectsSuppressedByPressure(target *streamingTarget, res checker.Result, pending map[int64]int, statusCache map[int64]int, retries *retryQueue, pressure bool) bool {
	if !pressure || target == nil {
		return false
	}
	targetID := monitorTargetID(target.site)
	if pending[targetID] > 0 {
		return false
	}
	status := target.site.SiteStatus
	if cached, ok := statusCache[targetID]; ok {
		status = cached
	}
	retrying := retries != nil && retries.get(targetID) != nil
	return status == statusRunning && !retrying && streamingLocalPressureFailure(res)
}

func streamingLocalPressureFailure(res checker.Result) bool {
	if !res.IsFailure() || res.HTTPCode > 0 {
		return false
	}
	return res.ErrorCode == checker.ErrorTimeout || res.ErrorCode == checker.ErrorConnect
}

func streamingAllowImmediateRetry(target *streamingTarget, res checker.Result, retries *retryQueue, pressure bool) bool {
	if !pressure {
		if target == nil {
			return false
		}
		return !streamingSuppressPostRecoveryImmediateRetry(target, res, retries)
	}
	if target == nil {
		return false
	}
	if streamingSuppressPostRecoveryImmediateRetry(target, res, retries) {
		return false
	}
	if !streamingLocalPressureFailure(res) {
		return true
	}
	if target.site.SiteStatus != statusRunning {
		return true
	}
	return retries != nil && retries.get(monitorTargetID(target.site)) != nil
}

func streamingSuppressPostRecoveryImmediateRetry(target *streamingTarget, res checker.Result, retries *retryQueue) bool {
	if target == nil || retries == nil {
		return false
	}
	suppressed, _, _ := postRecoveryTransientSuppression(target.site, res, retries)
	return suppressed
}

func rescheduleStreamingAfterSideEffect(planner *streamingPlanner, target *streamingTarget, report streamingSideEffectReport) {
	if planner == nil || target == nil {
		return
	}
	if report.resultFailure && report.status != statusDown && !target.inFlight {
		target.queued = false
		planner.scheduleAtNextPhaseAfter(target, report.checkedAt)
	}
}

func (o *Orchestrator) queueStreamingProjection(cfg *config.Config, target *streamingTarget, resultAt, projectedAt time.Time, pending map[int64]db.SiteCheck) {
	interval := streamingProjectionInterval(cfg)
	if !streamingProjectionDue(target, resultAt, interval) {
		return
	}
	if projectedAt.Before(resultAt) {
		projectedAt = resultAt
	}
	pending[target.site.BlogID] = db.SiteCheck{
		BlogID:      target.site.BlogID,
		CheckedAt:   projectedAt,
		NextCheckAt: target.dueAt,
	}
	target.lastProjectedAt = projectedAt
}

func streamingProjectionDue(target *streamingTarget, checkedAt time.Time, interval time.Duration) bool {
	if target.lastProjectedAt.IsZero() || interval <= 0 {
		return true
	}
	siteInterval := siteCheckInterval(target.site)
	if siteInterval >= interval {
		return true
	}
	return !checkedAt.Add(streamingProjectionSlack).Before(target.lastProjectedAt.Add(interval))
}

func streamingProjectionInterval(cfg *config.Config) time.Duration {
	if cfg == nil {
		return streamingDefaultProjectionInterval
	}
	interval := time.Duration(cfg.StreamingLegacyProjectionIntervalMin) * time.Minute
	if interval <= 0 {
		interval = streamingDefaultProjectionInterval
	}
	minRollbackWindow := 5 * time.Minute
	if interval < minRollbackWindow {
		interval = minRollbackWindow
	}
	return interval
}

func (o *Orchestrator) flushStreamingProjection(pending map[int64]db.SiteCheck) bool {
	if len(pending) == 0 {
		return true
	}
	checks := streamingProjectionChecks(pending)
	start := time.Now()
	if err := dbMarkSitesChecked(o.ctx, checks); err != nil {
		log.Printf("orchestrator: streaming legacy freshness projection rows=%d: %v", len(checks), err)
		return false
	}
	if m := metricsClientFunc(); m != nil {
		m.Increment("scheduler.streaming.legacy_projection.row.count", len(checks))
		m.Timing("scheduler.streaming.legacy_projection.time", time.Since(start))
	}
	return true
}

func streamingProjectionChecks(pending map[int64]db.SiteCheck) []db.SiteCheck {
	checks := make([]db.SiteCheck, 0, len(pending))
	for _, check := range pending {
		checks = append(checks, check)
	}
	return checks
}

func streamingProjectionFlushBatch(pending map[int64]db.SiteCheck, limit int) []db.SiteCheck {
	if len(pending) == 0 {
		return nil
	}
	if limit <= 0 || limit >= len(pending) {
		checks := streamingProjectionChecks(pending)
		for blogID := range pending {
			delete(pending, blogID)
		}
		return checks
	}
	checks := make([]db.SiteCheck, 0, limit)
	for blogID, check := range pending {
		checks = append(checks, check)
		delete(pending, blogID)
		if len(checks) >= limit {
			break
		}
	}
	return checks
}

func streamingProjectionFlushRowLimit(requiredRate float64) int {
	limit := int(math.Ceil(requiredRate * streamingProjectionFlushInterval.Seconds() * 1.25))
	if limit < streamingProjectionFlushMinRows {
		return streamingProjectionFlushMinRows
	}
	if limit > streamingProjectionFlushMaxRows {
		return streamingProjectionFlushMaxRows
	}
	return limit
}

func (o *Orchestrator) reportStreamingStats(cfg *config.Config, planner *streamingPlanner, stats streamingStats, pending, sideEffectDepth int, elapsed time.Duration, pressureActive bool) {
	avgLatency := stats.averageLatency()
	if avgLatency == 0 {
		avgLatency = streamingDefaultLatency
	}
	scaleLatency := stats.scaleLatency()
	if scaleLatency == 0 {
		scaleLatency = streamingDefaultLatency
	}
	if elapsed <= 0 {
		elapsed = streamingReportInterval
	}
	workerTarget := o.pool.WorkerCount()

	activeChecks := o.pool.ActiveCount()
	queueDepth := o.pool.QueueDepth()
	resultDepth := o.pool.ResultDepth()
	workers := o.pool.WorkerCount()
	sps := 0
	if elapsed.Seconds() > 0 {
		sps = int(float64(stats.completed) / elapsed.Seconds())
	}
	o.statsMu.Lock()
	o.lastRoundSPS = sps
	o.lastRoundDur = elapsed
	o.statsMu.Unlock()

	if m := metricsClientFunc(); m != nil {
		m.Gauge("scheduler.streaming.targets.count", planner.activeCount())
		m.Gauge("scheduler.streaming.required_rate.count", int(planner.requiredChecksPerSecond()))
		m.Gauge("scheduler.streaming.worker_target.count", workerTarget)
		m.Gauge("scheduler.streaming.pending.count", pending)
		m.Gauge("scheduler.streaming.inflight.count", activeChecks)
		m.Gauge("scheduler.streaming.queue_depth.count", queueDepth)
		m.Gauge("scheduler.streaming.result_depth.count", resultDepth)
		m.Gauge("scheduler.streaming.side_effect_queue_depth.count", sideEffectDepth)
		m.Gauge("scheduler.streaming.worker.count", workers)
		m.Gauge("scheduler.streaming.sps.count", sps)
		m.Increment("scheduler.streaming.selected.count", stats.selected)
		m.Increment("scheduler.streaming.dispatched.count", stats.dispatched)
		m.Increment("scheduler.streaming.completed.count", stats.completed)
		m.Increment("scheduler.streaming.backpressure_wait.count", stats.backpressureWaits)
		m.Increment("scheduler.streaming.side_effect_backpressure_wait.count", stats.sideEffectWaits)
		m.Increment("scheduler.streaming.result_backpressure_pause.count", stats.resultPaused)
		m.Increment("scheduler.streaming.side_effect_backpressure_pause.count", stats.sideEffectPaused)
		m.Increment("scheduler.streaming.dispatch_budget_limited.count", stats.dispatchLimited)
		m.Increment("scheduler.streaming.pressure_suppressed.count", stats.pressureSuppressed)
		m.Increment("scheduler.streaming.stale_result.count", stats.staleResults)
		m.Increment("scheduler.streaming.check.success.count", stats.checkSuccesses)
		m.Increment("scheduler.streaming.check.failure.count", stats.checkFailures)
		m.Increment("scheduler.streaming.check.error.timeout.count", stats.errorTimeouts)
		m.Increment("scheduler.streaming.check.error.connect.count", stats.errorConnects)
		m.Increment("scheduler.streaming.check.error.ssl.count", stats.errorSSL)
		m.Increment("scheduler.streaming.check.error.redirect.count", stats.errorRedirects)
		m.Increment("scheduler.streaming.check.error.keyword.count", stats.errorKeywords)
		m.Increment("scheduler.streaming.check.error.body_read.count", stats.errorBodyReads)
		m.Increment("scheduler.streaming.check.error.tls_expired.count", stats.errorTLSExpired)
		m.Increment("scheduler.streaming.check.error.tls_deprecated.count", stats.errorTLSDeprecated)
		m.Increment("scheduler.streaming.check.error.other.count", stats.errorOther)
		emitCheckCohortCounters(m, "scheduler.streaming", stats.checkCohorts)
		m.Increment("scheduler.streaming.side_effect.processed.count", stats.sideEffectRows)
		m.Increment("scheduler.streaming.history.row.count", stats.historyRows)
		m.Increment("scheduler.streaming.history.error.count", stats.historyErrors)
		m.Increment("scheduler.streaming.ssl.row.count", stats.sslRows)
		m.Increment("scheduler.streaming.ssl.error.count", stats.sslErrors)
		m.Timing("scheduler.streaming.avg_latency.time", avgLatency)
		m.Timing("scheduler.streaming.scale_latency.time", scaleLatency)
		m.Timing("scheduler.streaming.max_lag.time", stats.maxLag)
		m.Timing("scheduler.streaming.history.time", stats.historyDuration)
		m.Timing("scheduler.streaming.ssl.time", stats.sslDuration)
		m.Timing("scheduler.streaming.events.time", stats.eventDuration)
	}
	metrics.WriteStatsFiles(metrics.StatsFilesSnapshot{
		SitesPerSec: sps,
		QueueSize:   queueDepth,
		Working:     activeChecks,
		Waiting:     nonNegative(workers - activeChecks),
		Halting:     0,
		Error:       nonNegative(stats.checkFailures - stats.checkOffline),
		Offline:     stats.checkOffline,
		Success:     stats.checkSuccesses,
		Total:       stats.completed,
	})

	config.Debugf("orchestrator: streaming summary active=%d required_rate=%.2f/s selected=%d dispatched=%d completed=%d side_effects=%d pending=%d active_checks=%d queue_depth=%d result_depth=%d side_effect_depth=%d workers=%d worker_target=%d sps=%d elapsed=%s max_lag=%s avg_latency=%s scale_latency=%s successes=%d failures=%d failure_pressure=%t pressure_suppressed=%d error_timeout=%d error_connect=%d error_ssl=%d error_redirect=%d error_keyword=%d error_body_read=%d error_tls_expired=%d error_tls_deprecated=%d error_other=%d history_rows=%d ssl_rows=%d stale_results=%d backpressure_waits=%d side_effect_waits=%d result_pauses=%d side_effect_pauses=%d dispatch_limited=%d",
		planner.activeCount(),
		planner.requiredChecksPerSecond(),
		stats.selected,
		stats.dispatched,
		stats.completed,
		stats.sideEffectRows,
		pending,
		activeChecks,
		queueDepth,
		resultDepth,
		sideEffectDepth,
		workers,
		workerTarget,
		sps,
		elapsed.Round(time.Millisecond),
		stats.maxLag.Round(time.Millisecond),
		avgLatency.Round(time.Millisecond),
		scaleLatency.Round(time.Millisecond),
		stats.checkSuccesses,
		stats.checkFailures,
		pressureActive || streamingFailurePressure(stats),
		stats.pressureSuppressed,
		stats.errorTimeouts,
		stats.errorConnects,
		stats.errorSSL,
		stats.errorRedirects,
		stats.errorKeywords,
		stats.errorBodyReads,
		stats.errorTLSExpired,
		stats.errorTLSDeprecated,
		stats.errorOther,
		stats.historyRows,
		stats.sslRows,
		stats.staleResults,
		stats.backpressureWaits,
		stats.sideEffectWaits,
		stats.resultPaused,
		stats.sideEffectPaused,
		stats.dispatchLimited,
	)
}

func (o *Orchestrator) applyStreamingWorkerTarget(cfg *config.Config, planner *streamingPlanner, stats streamingStats, pending, resultDepth, sideEffectDepth int, failurePressure, hotPathPressure bool) int {
	latency := stats.scaleLatency()
	desiredTarget := streamingDesiredWorkerTarget(cfg, planner, latency, stats.maxLag, pending, o.pool.QueueDepth(), resultDepth, sideEffectDepth, o.pool.WorkerCount(), failurePressure, hotPathPressure)
	workerTarget := streamingDampedWorkerTarget(o.pool.WorkerCount(), desiredTarget, failurePressure)
	if planner.activeCount() > 0 {
		if added := o.pool.SetSizeBounds(workerTarget, workerTarget); added > 0 {
			config.Debugf("orchestrator: streaming prewarmed check pool by %d workers (target=%d desired=%d active_targets=%d failure_pressure=%t hot_path_pressure=%t)",
				added, workerTarget, desiredTarget, planner.activeCount(), failurePressure, hotPathPressure)
		}
	} else {
		o.pool.SetSizeBounds(1, workerTarget)
	}
	return workerTarget
}

func streamingDesiredWorkerTarget(cfg *config.Config, planner *streamingPlanner, latency, maxLag time.Duration, pending, queueDepth, resultDepth, sideEffectDepth, currentWorkers int, failurePressure, hotPathPressure bool) int {
	resultPressured := resultDepth >= streamingResultDispatchPauseDepth(currentWorkers, planner.activeCount())/2
	scaleLatency := latency
	if !failurePressure && (!hotPathPressure || resultPressured) && scaleLatency > streamingDefaultLatency {
		scaleLatency = streamingDefaultLatency
	}
	desiredTarget := streamingWorkerTarget(cfg, planner, scaleLatency)
	if failurePressure {
		pressureTarget := streamingPressureWorkerTarget(cfg, planner)
		if desiredTarget > pressureTarget {
			desiredTarget = pressureTarget
		}
		return desiredTarget
	}
	scaleForBacklog := maxLag > streamingReportInterval
	if hotPathPressure && maxLag > streamingHotPathScaleLag {
		scaleForBacklog = true
	}
	if !scaleForBacklog {
		return desiredTarget
	}
	if resultPressured {
		return desiredTarget
	}
	if sideEffectDepth < streamingSideEffectBackpressureDepth(currentWorkers, planner.activeCount()) {
		desiredTarget = streamingBacklogWorkerTarget(desiredTarget, planner.activeCount(), pending+queueDepth)
	}
	return desiredTarget
}

func streamingPressureWorkerTarget(cfg *config.Config, planner *streamingPlanner) int {
	return streamingWorkerTarget(cfg, planner, streamingFailurePressureLatency)
}

func streamingDampedWorkerTarget(current, desired int, pressure bool) int {
	if current < 1 || desired < 1 {
		return desired
	}
	if desired > current {
		step := current / 2
		if step < streamingMinWorkerStep {
			step = streamingMinWorkerStep
		}
		if current+step < desired {
			return current + step
		}
		return desired
	}
	if desired < current {
		step := current / 5
		if pressure {
			step = current / 2
		}
		if step < streamingMinWorkerStep {
			step = streamingMinWorkerStep
		}
		if current-step > desired {
			return current - step
		}
		return desired
	}
	return desired
}

func streamingFailurePressure(stats streamingStats) bool {
	total := stats.checkSuccesses + stats.checkFailures
	if total < streamingFailurePressureMin {
		return false
	}
	return stats.checkFailures*100 >= total*streamingFailurePressurePercent
}

func streamingBacklogWorkerTarget(base, active, backlog int) int {
	if base < 1 || backlog <= 0 {
		return base
	}
	target := base + backlog/streamingBacklogWorkerDivisor
	if target <= base {
		target = base + 1
	}
	maxTarget := base * streamingBacklogWorkerMultiplier
	if maxTarget < base+streamingMinWorkerStep {
		maxTarget = base + streamingMinWorkerStep
	}
	if target > maxTarget {
		target = maxTarget
	}
	if active > 0 && target > active {
		target = active
	}
	return target
}

func streamingShouldDeferPeriodicReload(planner *streamingPlanner, pending, resultDepth, sideEffectDepth, workers int, stats streamingStats) bool {
	return streamingHotPathBehind(planner, pending, resultDepth, sideEffectDepth, workers, stats)
}

func streamingHotPathBehind(planner *streamingPlanner, pending, resultDepth, sideEffectDepth, workers int, stats streamingStats) bool {
	if planner == nil || planner.activeCount() == 0 {
		return false
	}
	active := planner.activeCount()
	if pending > streamingReloadPendingDeferDepth(active, workers) {
		return true
	}
	if resultDepth > streamingResultDispatchPauseDepth(workers, active)/2 {
		return true
	}
	if sideEffectDepth > streamingSideEffectBackpressureDepth(workers, active)/2 {
		return true
	}
	return stats.maxLag > streamingReportInterval
}

func streamingReloadPendingDeferDepth(active, workers int) int {
	if active < 1 {
		return 0
	}
	depth := active / 100
	if workerBased := workers * 2; workerBased > depth {
		depth = workerBased
	}
	if depth < 1000 {
		return 1000
	}
	return depth
}

func streamingDeferredReloadLastReload(now time.Time, reloadInterval time.Duration) time.Time {
	if reloadInterval <= streamingReloadDeferInterval {
		return now
	}
	return now.Add(-(reloadInterval - streamingReloadDeferInterval))
}

func streamingTargetReloadInterval(cfg *config.Config, planner *streamingPlanner) time.Duration {
	interval := time.Duration(cfg.StreamingTargetReloadSec) * time.Second
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	active := planner.activeCount()
	if active < streamingLargeFleetReloadFloor {
		return interval
	}
	scaled := time.Duration(active/streamingLargeFleetReloadSitesPerSecond) * time.Second
	if scaled < interval {
		return interval
	}
	if scaled > streamingMaxTargetReloadInterval {
		return streamingMaxTargetReloadInterval
	}
	return scaled
}

func streamingLoadPageSize(cfg *config.Config) int {
	if cfg == nil || cfg.DatasetSize < streamingMinLoadPageSize {
		return streamingMinLoadPageSize
	}
	return cfg.DatasetSize
}

func streamingActiveCountPollIntervalFor(planner *streamingPlanner) time.Duration {
	if planner.activeCount() == 0 {
		return streamingEmptyTargetPollInterval
	}
	return streamingActiveCountPollInterval
}

func streamingWorkerTarget(cfg *config.Config, planner *streamingPlanner, latency time.Duration) int {
	if cfg == nil {
		return 1
	}
	active := planner.activeCount()
	if active < 1 {
		if cfg.NumWorkers > 0 {
			return cfg.NumWorkers
		}
		return 1
	}
	if latency <= 0 {
		latency = streamingDefaultLatency
	}
	if latency < streamingDefaultLatency {
		latency = streamingDefaultLatency
	}
	if maxLatency := streamingScaleLatencyCap(cfg); latency > maxLatency {
		latency = maxLatency
	}
	// Little's Law with headroom: concurrency ~= throughput * latency. The
	// headroom absorbs normal latency variance, but it stays conservative enough
	// to avoid turning transient latency into a self-amplifying worker surge.
	target := int(planner.requiredChecksPerSecond()*latency.Seconds()*streamingWorkerHeadroom) + 1
	if target < cfg.NumWorkers {
		target = cfg.NumWorkers
	}
	if target > active {
		target = active
	}
	if target < 1 {
		target = 1
	}
	return target
}

func streamingScaleLatencyCap(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.NetCommsTimeout <= 0 {
		return 10 * time.Second
	}
	cap := time.Duration(cfg.NetCommsTimeout) * time.Second
	if cap < streamingDefaultLatency {
		return streamingDefaultLatency
	}
	return cap
}

func streamingQueueCap(workerTarget, activeCount int) int {
	capacity := workerTarget * 4
	if activeCount > 0 {
		activeCap := activeCount
		if activeCap > streamingMaxQueueCap {
			activeCap = streamingMaxQueueCap
		}
		if capacity < activeCap {
			capacity = activeCap
		}
	}
	if capacity < streamingMinQueueCap {
		return streamingMinQueueCap
	}
	if capacity > streamingMaxQueueCap {
		return streamingMaxQueueCap
	}
	return capacity
}

func streamingResultBackpressureDepth(workerTarget, activeCount int) int {
	return streamingBackpressureDepth(workerTarget, activeCount, 2)
}

func streamingResultDispatchPauseDepth(workerTarget, activeCount int) int {
	if workerTarget < 1 {
		workerTarget = 1
	}
	depth := workerTarget * 6
	if activeBased := activeCount / 3; activeBased > depth {
		depth = activeBased
	}
	if depth < streamingMinBackpressureDepth {
		return streamingMinBackpressureDepth
	}
	limit := streamingMaxQueueCap * 3 / 4
	if depth > limit {
		return limit
	}
	return depth
}

func streamingShouldPauseDispatchForResultBacklog(resultDepth, workerTarget, activeCount, activeChecks, queueDepth int) bool {
	if resultDepth < streamingResultDispatchPauseDepth(workerTarget, activeCount) {
		return false
	}
	return activeChecks+queueDepth > 0
}

func streamingResultDrainLimitFor(resultDepth int) int {
	if resultDepth <= streamingResultDrainLimit {
		return streamingResultDrainLimit
	}
	limit := resultDepth / 2
	if limit < streamingResultDrainLimit {
		return streamingResultDrainLimit
	}
	if limit > streamingMaxResultDrainLimit {
		return streamingMaxResultDrainLimit
	}
	return limit
}

func streamingSideEffectBackpressureDepth(workerTarget, activeCount int) int {
	return streamingBackpressureDepth(workerTarget, activeCount, 1)
}

func streamingBackpressureDepth(workerTarget, activeCount, workerMultiplier int) int {
	if workerTarget < 1 {
		workerTarget = 1
	}
	if workerMultiplier < 1 {
		workerMultiplier = 1
	}
	depth := workerTarget * workerMultiplier
	if activeBased := activeCount / 20; activeBased > depth {
		depth = activeBased
	}
	if depth < streamingMinBackpressureDepth {
		return streamingMinBackpressureDepth
	}
	limit := streamingMaxQueueCap / 2
	if depth > limit {
		return limit
	}
	return depth
}

func streamingDispatchBudget(requiredRate float64, pending, workerTarget int, elapsed, maxLag time.Duration, resultDepth, activeCount int) int {
	if pending <= 0 {
		return 0
	}
	if elapsed > streamingDispatchMaxElapsed {
		elapsed = streamingDispatchMaxElapsed
	}
	seconds := elapsed.Seconds()
	if seconds <= 0 {
		seconds = streamingTickInterval.Seconds()
	}
	base := int(math.Ceil(requiredRate * seconds * 1.25))
	if base < 1 {
		base = 1
	}
	catchupDivisor := streamingDispatchCatchupDivisor
	maxMultiplier := streamingDispatchMaxMultiplier
	workerMultiplier := streamingDispatchWorkerMultiplier
	if streamingDispatchFastCatchup(maxLag, resultDepth, workerTarget, activeCount) {
		catchupDivisor = streamingDispatchFastCatchupDivisor
		maxMultiplier = streamingDispatchCatchupMultiplier
		workerMultiplier = streamingDispatchCatchupWorkerMultiplier
	}
	catchup := pending / catchupDivisor
	budget := base + catchup
	maxBudget := int(math.Ceil(requiredRate * seconds * maxMultiplier))
	if maxBudget < base {
		maxBudget = base
	}
	workerCap := workerTarget * workerMultiplier
	if workerCap < workerTarget {
		workerCap = workerTarget
	}
	if workerCap > 0 && maxBudget > workerCap {
		maxBudget = workerCap
	}
	if maxBudget < 1 {
		maxBudget = 1
	}
	if budget > maxBudget {
		budget = maxBudget
	}
	if budget > pending {
		return pending
	}
	return budget
}

func streamingDispatchFastCatchup(maxLag time.Duration, resultDepth, workerTarget, activeCount int) bool {
	if maxLag <= streamingReportInterval {
		return false
	}
	return resultDepth < streamingResultDispatchPauseDepth(workerTarget, activeCount)/2
}

func initialStreamingDueAt(site db.Site, now time.Time) time.Time {
	interval := streamingCheckCadence(site)
	phase := streamingPhaseOffset(monitorTargetID(site), interval)
	return nextStreamingPhaseAt(now, interval, phase)
}

func streamingCheckCadence(site db.Site) time.Duration {
	interval := siteCheckInterval(site)
	headroom := interval / 20
	if headroom < streamingMinScheduleHeadroom {
		headroom = streamingMinScheduleHeadroom
	}
	if headroom > streamingMaxScheduleHeadroom {
		headroom = streamingMaxScheduleHeadroom
	}
	cadence := interval - headroom
	if cadence < time.Second {
		return time.Second
	}
	return cadence
}

func streamingPhaseOffset(blogID int64, interval time.Duration) int64 {
	seconds := int64(interval / time.Second)
	if seconds <= 1 {
		return 0
	}
	hash := uint64(blogID) * 11400714819323198485
	return int64(hash % uint64(seconds))
}

func nextStreamingPhaseAt(now time.Time, interval time.Duration, phase int64) time.Time {
	now = now.UTC().Truncate(time.Second)
	seconds := int64(interval / time.Second)
	if seconds <= 1 {
		return now
	}
	mod := now.Unix() % seconds
	delta := phase - mod
	if delta < 0 {
		delta += seconds
	}
	return now.Add(time.Duration(delta) * time.Second)
}

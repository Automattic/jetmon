package orchestrator

import (
	"log"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/metrics"
)

const (
	streamingTickInterval            = time.Second
	streamingReportInterval          = time.Minute
	streamingProjectionFlushInterval = 10 * time.Second
	streamingEmptyTargetPollInterval = 5 * time.Second
	streamingActiveCountPollInterval = 30 * time.Second
	streamingDefaultLatency          = 250 * time.Millisecond
	streamingMinLoadPageSize         = 5000
	streamingMinQueueCap             = 65536
)

type streamingTarget struct {
	site            db.Site
	dueAt           time.Time
	inFlight        bool
	queued          bool
	active          bool
	lastProjectedAt time.Time
}

type streamingPlanner struct {
	targets      map[int64]*streamingTarget
	due          map[int64][]int64
	requiredRate float64
}

func newStreamingPlanner(sites []db.Site, now time.Time) *streamingPlanner {
	p := &streamingPlanner{
		targets: make(map[int64]*streamingTarget, len(sites)),
		due:     make(map[int64][]int64, len(sites)),
	}
	p.merge(sites, now)
	return p
}

func (p *streamingPlanner) merge(sites []db.Site, now time.Time) (added, updated, removed int) {
	seen := make(map[int64]struct{}, len(sites))
	for _, site := range sites {
		seen[site.BlogID] = struct{}{}
		if target, ok := p.targets[site.BlogID]; ok {
			target.site = site
			target.active = true
			updated++
			continue
		}
		target := &streamingTarget{
			site:   site,
			active: true,
		}
		if site.LastCheckedAt != nil {
			target.lastProjectedAt = site.LastCheckedAt.UTC()
		}
		p.targets[site.BlogID] = target
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

func (p *streamingPlanner) activeCount() int {
	return len(p.targets)
}

func (p *streamingPlanner) requiredChecksPerSecond() float64 {
	return p.requiredRate
}

func (p *streamingPlanner) recalculateRequiredRate() {
	var rate float64
	for _, target := range p.targets {
		interval := siteCheckInterval(target.site)
		if interval <= 0 {
			continue
		}
		rate += 1 / interval.Seconds()
	}
	p.requiredRate = rate
}

func (p *streamingPlanner) scheduleAfterResult(target *streamingTarget, res checker.Result) {
	interval := siteCheckInterval(target.site)
	checkedAt := resultCheckedAt(res)
	if res.IsFailure() && interval > failedCheckRetryInterval {
		p.scheduleAt(target, checkedAt.Add(failedCheckRetryInterval))
		return
	}

	next := target.dueAt.Add(interval)
	for !next.After(checkedAt) {
		next = next.Add(interval)
	}
	p.scheduleAt(target, next)
}

func (p *streamingPlanner) scheduleAt(target *streamingTarget, dueAt time.Time) {
	dueAt = dueAt.UTC().Truncate(time.Second)
	target.dueAt = dueAt
	p.due[dueAt.Unix()] = append(p.due[dueAt.Unix()], target.site.BlogID)
}

func (p *streamingPlanner) popDue(now time.Time) []*streamingTarget {
	nowUnix := now.UTC().Unix()
	var due []*streamingTarget
	for dueUnix, blogIDs := range p.due {
		if dueUnix > nowUnix {
			continue
		}
		delete(p.due, dueUnix)
		for _, blogID := range blogIDs {
			target, ok := p.targets[blogID]
			if !ok || !target.active || target.inFlight || target.queued || target.dueAt.Unix() != dueUnix {
				continue
			}
			target.queued = true
			due = append(due, target)
		}
	}
	return due
}

type streamingStats struct {
	selected          int
	dispatched        int
	completed         int
	backpressureWaits int
	staleResults      int
	checkFailures     int
	checkSuccesses    int
	historyRows       int
	historyErrors     int
	sslRows           int
	sslErrors         int
	eventDuration     time.Duration
	historyDuration   time.Duration
	sslDuration       time.Duration
	latencyTotal      time.Duration
	latencyCount      int
	maxLag            time.Duration
}

func (s *streamingStats) addProcess(summary resultProcessSummary, res checker.Result, lag time.Duration) {
	s.completed += summary.processed
	s.historyRows += summary.historyRows
	s.historyErrors += summary.historyErrors
	s.sslRows += summary.sslRows
	s.sslErrors += summary.sslErrors
	s.eventDuration += summary.eventDuration
	s.historyDuration += summary.historyDuration
	s.sslDuration += summary.sslDuration
	s.checkFailures += summary.checkFailures
	s.checkSuccesses += summary.checkSuccesses
	if res.RTT > 0 {
		s.latencyTotal += res.RTT
		s.latencyCount++
	}
	if lag > s.maxLag {
		s.maxLag = lag
	}
}

func (s streamingStats) averageLatency() time.Duration {
	if s.latencyCount == 0 {
		return 0
	}
	return s.latencyTotal / time.Duration(s.latencyCount)
}

func (o *Orchestrator) runStreamingEngine() {
	cfg := config.Get()
	log.Printf("orchestrator: streaming scheduler starting, host=%s buckets=%d-%d", o.hostname, o.bucketMin, o.bucketMax)
	if _, err := o.refreshStreamingBuckets(cfg); err != nil {
		log.Printf("orchestrator: streaming bucket refresh failed: %v", err)
	}
	sites, err := o.loadStreamingSites(cfg)
	if err != nil {
		log.Printf("orchestrator: streaming initial target load failed: %v", err)
		sites = nil
	}
	planner := newStreamingPlanner(sites, nowFunc().UTC())
	o.configureStreamingPool(cfg, planner, streamingDefaultLatency)
	log.Printf("orchestrator: streaming scheduler loaded targets=%d required_rate=%.2f/s workers=%d queue_cap=%d",
		planner.activeCount(),
		planner.requiredChecksPerSecond(),
		o.pool.WorkerCount(),
		streamingQueueCap(streamingWorkerTarget(cfg, planner, streamingDefaultLatency)),
	)

	tick := time.NewTicker(streamingTickInterval)
	defer tick.Stop()

	var (
		pending             []*streamingTarget
		pendingProjection   = make(map[int64]db.SiteCheck)
		stats               streamingStats
		lastReport          = nowFunc().UTC()
		lastReload          = lastReport
		lastProjectionFlush = lastReport
		lastHeartbeat       = lastReport
		lastActiveCountPoll = lastReport
	)

	for {
		select {
		case <-o.ctx.Done():
			o.flushStreamingProjection(pendingProjection)
			o.shutdown()
			return
		case res := <-o.pool.Results():
			target, ok := planner.targets[res.BlogID]
			if !ok || !target.inFlight {
				stats.staleResults++
				continue
			}
			target.inFlight = false
			lag := resultCheckedAt(res).Sub(target.dueAt)
			if lag < 0 {
				lag = 0
			}
			processSummary := o.processStreamingResult(target, res)
			stats.addProcess(processSummary, res, lag)
			o.totalChecked += processSummary.processed
			planner.scheduleAfterResult(target, res)
			o.queueStreamingProjection(cfg, target, res, pendingProjection)
		case now := <-tick.C:
			cfg = config.Get()
			o.refreshVeriflierClients(cfg)
			o.pool.SetMaxSize(streamingWorkerTarget(cfg, planner, stats.averageLatency()))

			if now.Sub(lastHeartbeat) >= schedulerBroadReportInterval {
				bucketsChanged, err := o.refreshStreamingBuckets(cfg)
				if err != nil {
					log.Printf("orchestrator: streaming bucket refresh failed: %v", err)
				}
				if bucketsChanged {
					lastReload = time.Time{}
				}
				lastHeartbeat = now
			}

			if now.Sub(lastActiveCountPoll) >= streamingActiveCountPollIntervalFor(planner) {
				if count, err := dbCountActiveSites(o.ctx, o.bucketMin, o.bucketMax); err != nil {
					log.Printf("orchestrator: streaming active target count check failed: %v", err)
				} else if count != planner.activeCount() {
					log.Printf("orchestrator: streaming active target count changed db=%d memory=%d; reloading targets", count, planner.activeCount())
					lastReload = time.Time{}
				}
				lastActiveCountPoll = now
			}

			if now.Sub(lastReload) >= time.Duration(cfg.StreamingTargetReloadSec)*time.Second {
				if sites, err := o.loadStreamingSites(cfg); err != nil {
					log.Printf("orchestrator: streaming target reload failed: %v", err)
				} else {
					added, updated, removed := planner.merge(sites, now.UTC())
					log.Printf("orchestrator: streaming target reload active=%d added=%d updated=%d removed=%d required_rate=%.2f/s",
						planner.activeCount(), added, updated, removed, planner.requiredChecksPerSecond())
				}
				lastReload = now
			}

			due := planner.popDue(now)
			stats.selected += len(due)
			pending = append(pending, due...)
			pending = o.dispatchStreamingPending(cfg, pending, &stats)

			if now.Sub(lastProjectionFlush) >= streamingProjectionFlushInterval {
				if o.flushStreamingProjection(pendingProjection) {
					pendingProjection = make(map[int64]db.SiteCheck)
				}
				lastProjectionFlush = now
			}

			if now.Sub(lastReport) >= streamingReportInterval {
				o.reportStreamingStats(cfg, planner, stats, len(pending))
				stats = streamingStats{}
				lastReport = now
			}
		}
	}
}

func (o *Orchestrator) configureStreamingPool(cfg *config.Config, planner *streamingPlanner, latency time.Duration) {
	if o.pool != nil {
		o.pool.Drain()
	}
	workerTarget := streamingWorkerTarget(cfg, planner, latency)
	queueCap := streamingQueueCap(workerTarget)
	initial := cfg.NumWorkers
	if initial > workerTarget {
		initial = workerTarget
	}
	if initial < 1 {
		initial = 1
	}
	o.pool = checker.NewPoolWithQueueCap(initial, 1, workerTarget, queueCap)
}

func (o *Orchestrator) refreshStreamingBuckets(cfg *config.Config) (bool, error) {
	oldMin, oldMax := o.bucketMin, o.bucketMax
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
	pageSize := streamingLoadPageSize(cfg)
	var (
		afterBlogID int64
		sites       []db.Site
	)
	for {
		page, err := dbListActiveSites(o.ctx, o.bucketMin, o.bucketMax, afterBlogID, pageSize)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return sites, nil
		}
		sites = append(sites, page...)
		afterBlogID = page[len(page)-1].BlogID
		if len(page) < pageSize {
			return sites, nil
		}
	}
}

func (o *Orchestrator) dispatchStreamingPending(cfg *config.Config, pending []*streamingTarget, stats *streamingStats) []*streamingTarget {
	for len(pending) > 0 {
		target := pending[0]
		if !target.active || target.inFlight {
			target.queued = false
			pending = pending[1:]
			continue
		}
		if !o.pool.Submit(checkRequestForSite(cfg, target.site)) {
			stats.backpressureWaits++
			return pending
		}
		target.queued = false
		target.inFlight = true
		stats.dispatched++
		pending = pending[1:]
	}
	return pending
}

func (o *Orchestrator) processStreamingResult(target *streamingTarget, res checker.Result) resultProcessSummary {
	summary := resultProcessSummary{processed: 1}
	addCheckOutcome(&summary, res)

	record := siteCheckResult{blogID: target.site.BlogID, site: target.site, res: res}
	if res.IsFailure() {
		o.recordResultHistories([]siteCheckResult{record}, &summary)
	}

	sslStart := time.Now()
	if res.TLSVersion != 0 {
		o.checkTLSDeprecated(target.site, res)
	}
	if res.SSLExpiry != nil {
		if shouldUpdateSSLExpiry(target.site.SSLExpiryDate, *res.SSLExpiry) {
			o.updateSSLExpiries([]db.SiteSSLExpiry{{
				BlogID: target.site.BlogID,
				Expiry: *res.SSLExpiry,
			}}, &summary)
			expiry := *res.SSLExpiry
			target.site.SSLExpiryDate = &expiry
		}
		o.checkSSLAlerts(target.site, *res.SSLExpiry)
	}
	summary.sslDuration += time.Since(sslStart)

	eventStart := time.Now()
	if !res.IsFailure() {
		o.handleRecovery(target.site, res)
		target.site.SiteStatus = statusRunning
	} else {
		o.handleFailure(target.site, res)
		if o.retries.get(target.site.BlogID) != nil {
			target.site.SiteStatus = statusDown
		} else if status, err := dbGetSiteStatus(o.ctx, target.site.BlogID); err != nil {
			log.Printf("orchestrator: streaming refresh site status blog_id=%d: %v", target.site.BlogID, err)
		} else {
			target.site.SiteStatus = status
		}
	}
	summary.eventDuration += time.Since(eventStart)
	return summary
}

func (o *Orchestrator) queueStreamingProjection(cfg *config.Config, target *streamingTarget, res checker.Result, pending map[int64]db.SiteCheck) {
	interval := time.Duration(cfg.StreamingLegacyProjectionIntervalMin) * time.Minute
	checkedAt := resultCheckedAt(res)
	if !target.lastProjectedAt.IsZero() && checkedAt.Sub(target.lastProjectedAt) < interval {
		return
	}
	pending[target.site.BlogID] = db.SiteCheck{
		BlogID:      target.site.BlogID,
		CheckedAt:   checkedAt,
		NextCheckAt: target.dueAt,
	}
	target.lastProjectedAt = checkedAt
}

func (o *Orchestrator) flushStreamingProjection(pending map[int64]db.SiteCheck) bool {
	if len(pending) == 0 {
		return true
	}
	checks := make([]db.SiteCheck, 0, len(pending))
	for _, check := range pending {
		checks = append(checks, check)
	}
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

func (o *Orchestrator) reportStreamingStats(cfg *config.Config, planner *streamingPlanner, stats streamingStats, pending int) {
	avgLatency := stats.averageLatency()
	if avgLatency == 0 {
		avgLatency = streamingDefaultLatency
	}
	workerTarget := streamingWorkerTarget(cfg, planner, avgLatency)
	o.pool.SetMaxSize(workerTarget)

	activeChecks := o.pool.ActiveCount()
	queueDepth := o.pool.QueueDepth()
	workers := o.pool.WorkerCount()
	sps := 0
	if streamingReportInterval.Seconds() > 0 {
		sps = int(float64(stats.completed) / streamingReportInterval.Seconds())
	}
	o.statsMu.Lock()
	o.lastRoundSPS = sps
	o.lastRoundDur = streamingReportInterval
	o.statsMu.Unlock()

	if m := metricsClientFunc(); m != nil {
		m.Gauge("scheduler.streaming.targets.count", planner.activeCount())
		m.Gauge("scheduler.streaming.required_rate.count", int(planner.requiredChecksPerSecond()))
		m.Gauge("scheduler.streaming.worker_target.count", workerTarget)
		m.Gauge("scheduler.streaming.pending.count", pending)
		m.Gauge("scheduler.streaming.inflight.count", activeChecks)
		m.Gauge("scheduler.streaming.queue_depth.count", queueDepth)
		m.Gauge("scheduler.streaming.worker.count", workers)
		m.Gauge("scheduler.streaming.sps.count", sps)
		m.Increment("scheduler.streaming.selected.count", stats.selected)
		m.Increment("scheduler.streaming.dispatched.count", stats.dispatched)
		m.Increment("scheduler.streaming.completed.count", stats.completed)
		m.Increment("scheduler.streaming.backpressure_wait.count", stats.backpressureWaits)
		m.Increment("scheduler.streaming.stale_result.count", stats.staleResults)
		m.Increment("scheduler.streaming.check.success.count", stats.checkSuccesses)
		m.Increment("scheduler.streaming.check.failure.count", stats.checkFailures)
		m.Increment("scheduler.streaming.history.row.count", stats.historyRows)
		m.Increment("scheduler.streaming.history.error.count", stats.historyErrors)
		m.Increment("scheduler.streaming.ssl.row.count", stats.sslRows)
		m.Increment("scheduler.streaming.ssl.error.count", stats.sslErrors)
		m.Timing("scheduler.streaming.avg_latency.time", avgLatency)
		m.Timing("scheduler.streaming.max_lag.time", stats.maxLag)
		m.Timing("scheduler.streaming.history.time", stats.historyDuration)
		m.Timing("scheduler.streaming.ssl.time", stats.sslDuration)
		m.Timing("scheduler.streaming.events.time", stats.eventDuration)
		metrics.WriteStatsFiles(sps, queueDepth, o.totalChecked)
	}

	log.Printf("orchestrator: streaming summary active=%d required_rate=%.2f/s selected=%d dispatched=%d completed=%d pending=%d active_checks=%d queue_depth=%d workers=%d worker_target=%d sps=%d max_lag=%s avg_latency=%s successes=%d failures=%d history_rows=%d ssl_rows=%d stale_results=%d backpressure_waits=%d",
		planner.activeCount(),
		planner.requiredChecksPerSecond(),
		stats.selected,
		stats.dispatched,
		stats.completed,
		pending,
		activeChecks,
		queueDepth,
		workers,
		workerTarget,
		sps,
		stats.maxLag.Round(time.Millisecond),
		avgLatency.Round(time.Millisecond),
		stats.checkSuccesses,
		stats.checkFailures,
		stats.historyRows,
		stats.sslRows,
		stats.staleResults,
		stats.backpressureWaits,
	)
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
	// Little's Law with headroom: concurrency ~= throughput * latency.
	// Multiply by 3 so the pool can absorb normal latency variance without
	// requiring operators to hand-tune a static worker cap for each fleet size.
	target := int(planner.requiredChecksPerSecond()*latency.Seconds()*3) + 1
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

func streamingQueueCap(workerTarget int) int {
	capacity := workerTarget * 4
	if capacity < streamingMinQueueCap {
		return streamingMinQueueCap
	}
	return capacity
}

func initialStreamingDueAt(site db.Site, now time.Time) time.Time {
	interval := siteCheckInterval(site)
	phase := streamingPhaseOffset(site.BlogID, interval)
	return nextStreamingPhaseAt(now, interval, phase)
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

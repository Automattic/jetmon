package orchestrator

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	runtimemetrics "runtime/metrics"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/eventstore"
	"github.com/Automattic/jetmon/internal/metrics"
	"github.com/Automattic/jetmon/internal/veriflier"
	"github.com/Automattic/jetmon/internal/wpcom"
)

// v1 site_status values projected onto jetpack_monitor_sites.site_status from
// the event-sourced state. These remain unchanged for back-compat with v1
// consumers; the orchestrator writes them in the same transaction as every
// event mutation.
const (
	statusDown          = 0 // Seems Down event open (local failures, retry/verification in progress)
	statusRunning       = 1 // No active event
	statusConfirmedDown = 2 // Down event (verifier-confirmed)
)

// checkTypeHTTP is the canonical check_type for the v1 HTTP probe path. New
// check types (DNS, TLS expiry, keyword, redirect, etc.) get their own
// constants alongside.
const (
	checkTypeHTTP      = "http"
	checkTypeTLSExpiry = "tls_expiry"
)

// verifierRPCHeadroom is added to the per-site check timeout when computing
// the RPC deadline for a verifier call. The verifier needs enough budget to
// run its own HTTP check (matches site timeout) plus serialization, queueing,
// and network round-trip — 5s covers a comfortable steady-state and forces
// failure on a truly wedged verifier rather than letting the call hang.
const verifierRPCHeadroom = 5 * time.Second

const schedulerBackpressurePollInterval = 10 * time.Millisecond
const schedulerVariableIntervalPollInterval = 5 * time.Second
const schedulerBacklogPollInterval = 5 * time.Second

// VariableIntervalPollInterval returns the idle scheduler poll interval used
// when per-site check intervals are enabled. The SQL due predicate prevents
// early checks; this only controls how quickly newly due work is discovered.
func VariableIntervalPollInterval() time.Duration {
	return schedulerVariableIntervalPollInterval
}

var (
	nowFunc                = time.Now
	dbClaimBuckets         = db.ClaimBuckets
	dbHeartbeat            = db.Heartbeat
	dbReleaseHost          = db.ReleaseHost
	dbMarkHostDraining     = db.MarkHostDraining
	dbGetSitesForBucket    = db.GetSitesForBucket
	dbMarkSiteChecked      = db.MarkSiteChecked
	dbMarkSitesChecked     = db.MarkSitesChecked
	dbRecordCheckHistory   = db.RecordCheckHistory
	dbRecordCheckHistories = db.RecordCheckHistories
	dbUpdateSSLExpiry      = db.UpdateSSLExpiry
	dbUpdateSiteStatus     = db.UpdateSiteStatus
	dbRecordFalsePositive  = db.RecordFalsePositive
	dbUpdateLastAlertSent  = db.UpdateLastAlertSent
	dbCountDueSites        = db.CountDueSitesForBucketRange
	dbCountProjectionDrift = db.CountLegacyProjectionDrift
	veriflierCheckFunc     = func(c *veriflier.VeriflierClient, ctx stdctx.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return c.Check(ctx, req)
	}
	metricsClientFunc = func() metricsClient {
		if m := metrics.Global(); m != nil {
			return m
		}
		return nil
	}
	wpcomNotifyFunc     = func(c *wpcom.Client, n wpcom.Notification) error { return c.Notify(n) }
	currentMemoryMBFunc = currentMemoryMB
)

type metricsClient interface {
	Increment(stat string, value int)
	Gauge(stat string, value int)
	Timing(stat string, d time.Duration)
	EmitMemStats()
}

type roundSummary struct {
	pagesFetched      int
	selected          int
	dispatched        int
	completed         int
	outstanding       int
	backpressureWaits int
	staleResults      int
	duplicateResults  int
	neverChecked      int
	oldestSelectedAge time.Duration
	dueAtStart        int
	dueRemaining      int
	dueCountErrors    int
	fetchErrors       int
	interrupted       bool

	dispatchDuration    time.Duration
	waitDuration        time.Duration
	processDuration     time.Duration
	markCheckedDuration time.Duration
	historyDuration     time.Duration
	sslDuration         time.Duration
	eventDuration       time.Duration

	markCheckedRows   int
	historyRows       int
	markCheckedErrors int
	historyErrors     int
}

func (s *roundSummary) add(other roundSummary) {
	s.pagesFetched += other.pagesFetched
	s.selected += other.selected
	s.dispatched += other.dispatched
	s.completed += other.completed
	s.outstanding += other.outstanding
	s.backpressureWaits += other.backpressureWaits
	s.staleResults += other.staleResults
	s.duplicateResults += other.duplicateResults
	s.neverChecked += other.neverChecked
	s.dueCountErrors += other.dueCountErrors
	s.fetchErrors += other.fetchErrors
	s.dispatchDuration += other.dispatchDuration
	s.waitDuration += other.waitDuration
	s.processDuration += other.processDuration
	s.markCheckedDuration += other.markCheckedDuration
	s.historyDuration += other.historyDuration
	s.sslDuration += other.sslDuration
	s.eventDuration += other.eventDuration
	s.markCheckedRows += other.markCheckedRows
	s.historyRows += other.historyRows
	s.markCheckedErrors += other.markCheckedErrors
	s.historyErrors += other.historyErrors
	if other.oldestSelectedAge > s.oldestSelectedAge {
		s.oldestSelectedAge = other.oldestSelectedAge
	}
	if other.interrupted {
		s.interrupted = true
	}
}

type resultProcessSummary struct {
	processed           int
	markCheckedRows     int
	historyRows         int
	markCheckedErrors   int
	historyErrors       int
	markCheckedDuration time.Duration
	historyDuration     time.Duration
	sslDuration         time.Duration
	eventDuration       time.Duration
}

type siteCheckResult struct {
	blogID int64
	site   db.Site
	res    checker.Result
}

// Orchestrator drives the main check loop.
type Orchestrator struct {
	pool             *checker.Pool
	retries          *retryQueue
	wpcom            *wpcom.Client
	events           *eventstore.Store
	veriflierClients []*veriflier.VeriflierClient
	veriflierAddrs   []string // parallel slice of "addr|token" for change detection
	veriflierMu      sync.RWMutex
	hostname         string
	bucketMin        int
	bucketMax        int

	totalChecked int
	roundStart   time.Time
	statsMu      sync.RWMutex
	lastRoundSPS int
	lastRoundDur time.Duration

	ctx    stdctx.Context
	cancel stdctx.CancelFunc
}

// New creates an Orchestrator. Call Run to start the check loop.
func New(cfg *config.Config, wp *wpcom.Client) *Orchestrator {
	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	pool := checker.NewPool(cfg.NumWorkers/2, 1, cfg.NumWorkers)

	o := &Orchestrator{
		pool:     pool,
		retries:  newRetryQueue(),
		wpcom:    wp,
		events:   eventstore.New(db.DB()),
		hostname: db.Hostname(),
		ctx:      ctx,
		cancel:   cancel,
	}

	o.refreshVeriflierClients(cfg)
	if len(o.veriflierClients) == 0 {
		log.Println("orchestrator: warning: no verifliers configured — down confirmations rely on local checks only")
	}

	return o
}

// ev returns a non-nil event store. Tests that construct &Orchestrator{}
// directly without setting events get a no-op store backed by a nil DB so
// event-mutation paths run without panicking. Production always wires up a
// real Store in New().
func (o *Orchestrator) ev() *eventstore.Store {
	if o.events == nil {
		return eventstore.New(nil)
	}
	return o.events
}

// ClaimBuckets registers this host in jetmon_hosts and sets the bucket range.
func (o *Orchestrator) ClaimBuckets() error {
	cfg := config.Get()
	if min, max, ok := cfg.PinnedBucketRange(); ok {
		if o.bucketMin != min || o.bucketMax != max {
			log.Printf("orchestrator: using pinned buckets %d-%d (dynamic bucket ownership disabled)", min, max)
		}
		o.bucketMin = min
		o.bucketMax = max
		return nil
	}
	min, max, err := dbClaimBuckets(
		o.hostname,
		cfg.BucketTotal,
		cfg.BucketTarget,
		cfg.BucketHeartbeatGraceSec,
	)
	if err != nil {
		return err
	}
	o.bucketMin = min
	o.bucketMax = max
	log.Printf("orchestrator: claimed buckets %d-%d", min, max)
	return nil
}

// Run starts the main orchestration loop. Blocks until ctx is cancelled.
func (o *Orchestrator) Run() {
	log.Printf("orchestrator: starting, host=%s buckets=%d-%d", o.hostname, o.bucketMin, o.bucketMax)
	for {
		select {
		case <-o.ctx.Done():
			log.Println("orchestrator: shutting down")
			if !o.usesPinnedBuckets(config.Get()) {
				if err := dbMarkHostDraining(stdctx.Background(), o.hostname); err != nil {
					log.Printf("orchestrator: mark draining: %v", err)
				}
			}
			o.pool.Drain()
			if o.usesPinnedBuckets(config.Get()) {
				log.Println("orchestrator: pinned bucket mode active; no jetmon_hosts row to release")
			} else if err := dbReleaseHost(stdctx.Background(), o.hostname); err != nil {
				log.Printf("orchestrator: release host: %v", err)
			}
			return
		default:
		}

		cfg := config.Get()
		o.pool.SetMaxSize(cfg.NumWorkers)
		o.refreshVeriflierClients(cfg)

		o.roundStart = time.Now()
		summary := o.runRound()

		elapsed := time.Since(o.roundStart)
		sleepFor := schedulerSleepDuration(cfg, summary, elapsed)
		if sleepFor > 0 {
			select {
			case <-time.After(sleepFor):
			case <-o.ctx.Done():
			}
		}
	}
}

// Stop signals the orchestrator to shut down after the current round.
func (o *Orchestrator) Stop() {
	o.cancel()
}

func (o *Orchestrator) runRound() roundSummary {
	cfg := config.Get()
	summary := roundSummary{}
	if o.roundStart.IsZero() {
		o.roundStart = time.Now()
	}

	if o.usesPinnedBuckets(cfg) {
		if err := o.ClaimBuckets(); err != nil {
			log.Printf("orchestrator: pinned bucket claim failed: %v", err)
		}
	} else {
		// Update heartbeat.
		if err := dbHeartbeat(o.ctx, o.hostname); err != nil {
			log.Printf("orchestrator: heartbeat failed: %v", err)
		}
		// Re-claim every round so bucket ranges rebalance automatically when
		// hosts join or leave the cluster.
		if err := o.ClaimBuckets(); err != nil {
			log.Printf("orchestrator: bucket rebalance failed: %v", err)
		}
	}
	o.checkLegacyProjectionDrift(cfg)

	if due, err := dbCountDueSites(o.ctx, o.bucketMin, o.bucketMax, cfg.UseVariableCheckIntervals); err != nil {
		summary.dueCountErrors++
		log.Printf("orchestrator: count due sites failed: %v", err)
	} else {
		summary.dueAtStart = due
	}

	pageSize := cfg.DatasetSize
	if pageSize < 1 {
		pageSize = 1
	}
	seen := make(map[int64]struct{}, pageSize)
	for {
		select {
		case <-o.ctx.Done():
			summary.interrupted = true
			o.finishRound(cfg, summary)
			return summary
		default:
		}

		sites, err := dbGetSitesForBucket(o.ctx, o.bucketMin, o.bucketMax, pageSize, cfg.UseVariableCheckIntervals)
		if err != nil {
			summary.fetchErrors++
			log.Printf("orchestrator: fetch sites failed: %v", err)
			break
		}
		page := filterUnseenSites(sites, seen)
		if len(page) == 0 {
			break
		}

		summary.pagesFetched++
		summary.selected += len(page)
		summary.add(selectedSiteSummary(page))
		log.Printf("orchestrator: checking %d sites (scheduler page %d)", len(page), summary.pagesFetched)

		pageSummary := o.checkSitesPage(cfg, page, summary.pagesFetched)
		summary.add(pageSummary)
		if pageSummary.interrupted || pageSummary.outstanding > 0 {
			break
		}
		if len(sites) < pageSize {
			break
		}
	}

	if cfg.UseVariableCheckIntervals {
		if due, err := dbCountDueSites(o.ctx, o.bucketMin, o.bucketMax, true); err != nil {
			summary.dueCountErrors++
			log.Printf("orchestrator: count remaining due sites failed: %v", err)
		} else {
			summary.dueRemaining = due
		}
	} else {
		summary.dueRemaining = max(0, summary.dueAtStart-summary.completed)
	}

	o.finishRound(cfg, summary)
	o.applyMemoryPressure(cfg)
	return summary
}

func (o *Orchestrator) checkSitesPage(cfg *config.Config, sites []db.Site, pageNumber int) roundSummary {
	summary := roundSummary{}
	siteMap := make(map[int64]db.Site, len(sites))
	results := make(map[int64]checker.Result, len(sites))
	for _, s := range sites {
		siteMap[s.BlogID] = s
	}

	dispatchStart := time.Now()
	for _, site := range sites {
		req := checkRequestForSite(cfg, site)
		for {
			if o.pool.Submit(req) {
				summary.dispatched++
				break
			}
			summary.backpressureWaits++
			if !o.waitForPageResult(siteMap, results, &summary, schedulerBackpressurePollInterval) {
				summary.interrupted = true
				summary.dispatchDuration += time.Since(dispatchStart)
				return summary
			}
		}
	}
	summary.dispatchDuration += time.Since(dispatchStart)

	deadline := time.NewTimer(collectionDeadlineForSites(cfg, sites))
	defer deadline.Stop()
	waitStart := time.Now()
	for len(results) < summary.dispatched {
		select {
		case res := <-o.pool.Results():
			recordPageResult(siteMap, results, res, &summary)
		case <-deadline.C:
			summary.outstanding = summary.dispatched - len(results)
			log.Printf("orchestrator: round deadline reached, %d results outstanding", summary.outstanding)
			goto process
		case <-o.ctx.Done():
			summary.interrupted = true
			summary.waitDuration += time.Since(waitStart)
			return summary
		}
	}

process:
	summary.waitDuration += time.Since(waitStart)
	processStart := time.Now()
	processSummary := o.processResults(results, siteMap)
	summary.processDuration += time.Since(processStart)
	summary.completed += processSummary.processed
	summary.markCheckedRows += processSummary.markCheckedRows
	summary.historyRows += processSummary.historyRows
	summary.markCheckedErrors += processSummary.markCheckedErrors
	summary.historyErrors += processSummary.historyErrors
	summary.markCheckedDuration += processSummary.markCheckedDuration
	summary.historyDuration += processSummary.historyDuration
	summary.sslDuration += processSummary.sslDuration
	summary.eventDuration += processSummary.eventDuration
	o.totalChecked += processSummary.processed
	emitPageMetrics(summary)
	logPageSummary(pageNumber, len(sites), summary)
	return summary
}

func emitPageMetrics(summary roundSummary) {
	m := metricsClientFunc()
	if m == nil {
		return
	}
	m.Timing("scheduler.page.dispatch.time", summary.dispatchDuration)
	m.Timing("scheduler.page.wait.time", summary.waitDuration)
	m.Timing("scheduler.page.process.time", summary.processDuration)
	m.Timing("scheduler.page.mark_checked.time", summary.markCheckedDuration)
	m.Timing("scheduler.page.history.time", summary.historyDuration)
	m.Timing("scheduler.page.ssl.time", summary.sslDuration)
	m.Timing("scheduler.page.events.time", summary.eventDuration)
	m.Increment("scheduler.page.mark_checked.row.count", summary.markCheckedRows)
	m.Increment("scheduler.page.history.row.count", summary.historyRows)
	m.Increment("scheduler.page.mark_checked.error.count", summary.markCheckedErrors)
	m.Increment("scheduler.page.history.error.count", summary.historyErrors)
}

func logPageSummary(pageNumber, sites int, summary roundSummary) {
	log.Printf(
		"orchestrator: page summary page=%d sites=%d dispatched=%d completed=%d outstanding=%d dispatch=%s wait=%s process=%s mark_checked=%s history=%s ssl=%s events=%s mark_checked_rows=%d history_rows=%d mark_checked_errors=%d history_errors=%d",
		pageNumber,
		sites,
		summary.dispatched,
		summary.completed,
		summary.outstanding,
		summary.dispatchDuration.Round(time.Millisecond),
		summary.waitDuration.Round(time.Millisecond),
		summary.processDuration.Round(time.Millisecond),
		summary.markCheckedDuration.Round(time.Millisecond),
		summary.historyDuration.Round(time.Millisecond),
		summary.sslDuration.Round(time.Millisecond),
		summary.eventDuration.Round(time.Millisecond),
		summary.markCheckedRows,
		summary.historyRows,
		summary.markCheckedErrors,
		summary.historyErrors,
	)
}

func (o *Orchestrator) waitForPageResult(siteMap map[int64]db.Site, results map[int64]checker.Result, summary *roundSummary, maxWait time.Duration) bool {
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	select {
	case res := <-o.pool.Results():
		recordPageResult(siteMap, results, res, summary)
		return true
	case <-timer.C:
		return true
	case <-o.ctx.Done():
		return false
	}
}

func filterUnseenSites(sites []db.Site, seen map[int64]struct{}) []db.Site {
	filtered := make([]db.Site, 0, len(sites))
	for _, site := range sites {
		if _, ok := seen[site.BlogID]; ok {
			continue
		}
		seen[site.BlogID] = struct{}{}
		filtered = append(filtered, site)
	}
	return filtered
}

func selectedSiteSummary(sites []db.Site) roundSummary {
	summary := roundSummary{}
	now := nowFunc().UTC()
	for _, site := range sites {
		if site.LastCheckedAt == nil {
			summary.neverChecked++
			continue
		}
		age := now.Sub(site.LastCheckedAt.UTC())
		if age > summary.oldestSelectedAge {
			summary.oldestSelectedAge = age
		}
	}
	return summary
}

func checkRequestForSite(cfg *config.Config, site db.Site) checker.Request {
	req := checker.Request{
		BlogID:         site.BlogID,
		URL:            site.MonitorURL,
		TimeoutSeconds: timeoutForSite(cfg, site),
		Keyword:        site.CheckKeyword,
		CustomHeaders:  checker.ParseCustomHeaders(site.CustomHeaders),
		RedirectPolicy: checker.RedirectPolicy(site.RedirectPolicy),
	}
	if req.RedirectPolicy == "" {
		req.RedirectPolicy = checker.RedirectFollow
	}
	return req
}

func collectionDeadlineForSites(cfg *config.Config, sites []db.Site) time.Duration {
	timeout := cfg.NetCommsTimeout
	for _, site := range sites {
		if siteTimeout := timeoutForSite(cfg, site); siteTimeout > timeout {
			timeout = siteTimeout
		}
	}
	return time.Duration(timeout+5) * time.Second
}

func recordPageResult(siteMap map[int64]db.Site, results map[int64]checker.Result, res checker.Result, summary *roundSummary) {
	if _, ok := siteMap[res.BlogID]; !ok {
		summary.staleResults++
		log.Printf("orchestrator: ignored stale check result blog_id=%d", res.BlogID)
		return
	}
	if _, ok := results[res.BlogID]; ok {
		summary.duplicateResults++
		log.Printf("orchestrator: ignored duplicate check result blog_id=%d", res.BlogID)
		return
	}
	results[res.BlogID] = res
}

func schedulerSleepDuration(cfg *config.Config, summary roundSummary, elapsed time.Duration) time.Duration {
	if summary.interrupted {
		return 0
	}
	if summary.dueRemaining > 0 || summary.outstanding > 0 || summary.fetchErrors > 0 {
		return schedulerBacklogPollInterval
	}
	if cfg.UseVariableCheckIntervals {
		return schedulerVariableIntervalPollInterval
	}
	minInterval := time.Duration(cfg.MinTimeBetweenRoundsSec) * time.Second
	if elapsed >= minInterval {
		return 0
	}
	return minInterval - elapsed
}

func (o *Orchestrator) finishRound(cfg *config.Config, summary roundSummary) {
	// Emit metrics and update stats files.
	roundDuration := time.Since(o.roundStart)
	sps := 0
	if roundDuration.Seconds() > 0 {
		sps = int(float64(summary.completed) / roundDuration.Seconds())
	}
	o.statsMu.Lock()
	o.lastRoundSPS = sps
	o.lastRoundDur = roundDuration
	o.statsMu.Unlock()

	m := metricsClientFunc()
	if m != nil {
		activeChecks := 0
		queueDepth := 0
		if o.pool != nil {
			activeChecks = o.pool.ActiveCount()
			queueDepth = o.pool.QueueDepth()
		}
		retryQueueSize := 0
		if o.retries != nil {
			retryQueueSize = o.retries.size()
		}
		m.Timing("round.complete.time", roundDuration)
		m.Gauge("worker.queue.active", activeChecks)
		m.Gauge("worker.queue.queue_size", queueDepth)
		m.Gauge("retry.queue.size", retryQueueSize)
		m.Increment("round.sites.count", summary.completed)
		m.Gauge("round.sps.count", sps)
		m.Gauge("scheduler.round.pages.count", summary.pagesFetched)
		m.Gauge("scheduler.round.selected.count", summary.selected)
		m.Gauge("scheduler.round.dispatched.count", summary.dispatched)
		m.Gauge("scheduler.round.completed.count", summary.completed)
		m.Gauge("scheduler.round.outstanding.count", summary.outstanding)
		m.Gauge("scheduler.round.due_start.count", summary.dueAtStart)
		m.Gauge("scheduler.round.due_remaining.count", summary.dueRemaining)
		m.Gauge("scheduler.round.selected_never_checked.count", summary.neverChecked)
		m.Gauge("scheduler.round.selected_oldest_age_sec", int(summary.oldestSelectedAge.Seconds()))
		m.Increment("scheduler.dispatch.backpressure_wait.count", summary.backpressureWaits)
		m.Increment("scheduler.result.stale.count", summary.staleResults)
		m.Increment("scheduler.result.duplicate.count", summary.duplicateResults)
		m.Increment("scheduler.due_count.error.count", summary.dueCountErrors)
		m.Increment("scheduler.fetch.error.count", summary.fetchErrors)
		m.Timing("scheduler.round.dispatch.time", summary.dispatchDuration)
		m.Timing("scheduler.round.wait.time", summary.waitDuration)
		m.Timing("scheduler.round.process.time", summary.processDuration)
		m.Timing("scheduler.round.mark_checked.time", summary.markCheckedDuration)
		m.Timing("scheduler.round.history.time", summary.historyDuration)
		m.Timing("scheduler.round.ssl.time", summary.sslDuration)
		m.Timing("scheduler.round.events.time", summary.eventDuration)
		m.Increment("scheduler.round.mark_checked.row.count", summary.markCheckedRows)
		m.Increment("scheduler.round.history.row.count", summary.historyRows)
		m.Increment("scheduler.round.mark_checked.error.count", summary.markCheckedErrors)
		m.Increment("scheduler.round.history.error.count", summary.historyErrors)

		if cfg.StatsdSendMemUsage {
			m.EmitMemStats()
		}

		metrics.WriteStatsFiles(sps, queueDepth, o.totalChecked)
	}
	logRoundSummary(summary, roundDuration, sps)
}

func logRoundSummary(summary roundSummary, roundDuration time.Duration, sps int) {
	if summary.selected == 0 &&
		summary.dueRemaining == 0 &&
		summary.outstanding == 0 &&
		summary.backpressureWaits == 0 &&
		summary.fetchErrors == 0 &&
		summary.dueCountErrors == 0 {
		return
	}
	log.Printf(
		"orchestrator: round summary pages=%d due_start=%d selected=%d dispatched=%d completed=%d outstanding=%d due_remaining=%d backpressure_waits=%d stale_results=%d duplicate_results=%d never_checked=%d oldest_selected_age_sec=%d dispatch=%s wait=%s process=%s mark_checked=%s history=%s ssl=%s events=%s mark_checked_rows=%d history_rows=%d mark_checked_errors=%d history_errors=%d duration=%s sps=%d",
		summary.pagesFetched,
		summary.dueAtStart,
		summary.selected,
		summary.dispatched,
		summary.completed,
		summary.outstanding,
		summary.dueRemaining,
		summary.backpressureWaits,
		summary.staleResults,
		summary.duplicateResults,
		summary.neverChecked,
		int(summary.oldestSelectedAge.Seconds()),
		summary.dispatchDuration.Round(time.Millisecond),
		summary.waitDuration.Round(time.Millisecond),
		summary.processDuration.Round(time.Millisecond),
		summary.markCheckedDuration.Round(time.Millisecond),
		summary.historyDuration.Round(time.Millisecond),
		summary.sslDuration.Round(time.Millisecond),
		summary.eventDuration.Round(time.Millisecond),
		summary.markCheckedRows,
		summary.historyRows,
		summary.markCheckedErrors,
		summary.historyErrors,
		roundDuration.Round(time.Millisecond),
		sps,
	)
}

func (o *Orchestrator) processResults(results map[int64]checker.Result, sites map[int64]db.Site) resultProcessSummary {
	records := knownSiteResults(results, sites)
	summary := resultProcessSummary{processed: len(records)}
	if len(records) == 0 {
		return summary
	}

	o.markResultsChecked(records, &summary)
	o.recordResultHistories(records, &summary)

	sslStart := time.Now()
	for _, record := range records {
		// Update SSL expiry if available.
		if record.res.SSLExpiry != nil {
			if shouldUpdateSSLExpiry(record.site.SSLExpiryDate, *record.res.SSLExpiry) {
				if err := dbUpdateSSLExpiry(o.ctx, record.blogID, *record.res.SSLExpiry); err != nil {
					log.Printf("orchestrator: update ssl expiry blog_id=%d: %v", record.blogID, err)
				}
			}
			o.checkSSLAlerts(record.site, *record.res.SSLExpiry)
		}
	}
	summary.sslDuration += time.Since(sslStart)

	eventStart := time.Now()
	for _, record := range records {
		// Per-check data is recorded in jetmon_check_history (above); duplicating
		// it in jetmon_audit_log was retired with the operational/site-state split.
		if !record.res.IsFailure() {
			o.handleRecovery(record.site, record.res)
		} else {
			o.handleFailure(record.site, record.res)
		}
	}
	summary.eventDuration += time.Since(eventStart)
	return summary
}

func knownSiteResults(results map[int64]checker.Result, sites map[int64]db.Site) []siteCheckResult {
	blogIDs := make([]int64, 0, len(results))
	for blogID := range results {
		blogIDs = append(blogIDs, blogID)
	}
	sort.Slice(blogIDs, func(i, j int) bool {
		return blogIDs[i] < blogIDs[j]
	})

	records := make([]siteCheckResult, 0, len(results))
	for _, blogID := range blogIDs {
		site, ok := sites[blogID]
		if !ok {
			continue
		}
		records = append(records, siteCheckResult{
			blogID: blogID,
			site:   site,
			res:    results[blogID],
		})
	}
	return records
}

func (o *Orchestrator) markResultsChecked(records []siteCheckResult, summary *resultProcessSummary) {
	checks := make([]db.SiteCheck, 0, len(records))
	for _, record := range records {
		checks = append(checks, db.SiteCheck{
			BlogID:    record.blogID,
			CheckedAt: resultCheckedAt(record.res),
		})
	}

	start := time.Now()
	if err := dbMarkSitesChecked(o.ctx, checks); err != nil {
		summary.markCheckedErrors++
		log.Printf("orchestrator: batch mark checked sites=%d: %v", len(checks), err)
		for _, check := range checks {
			if err := dbMarkSiteChecked(o.ctx, check.BlogID, check.CheckedAt); err != nil {
				summary.markCheckedErrors++
				log.Printf("orchestrator: mark checked blog_id=%d: %v", check.BlogID, err)
				continue
			}
			summary.markCheckedRows++
		}
	} else {
		summary.markCheckedRows += len(checks)
	}
	summary.markCheckedDuration += time.Since(start)
}

func (o *Orchestrator) recordResultHistories(records []siteCheckResult, summary *resultProcessSummary) {
	histories := make([]db.CheckHistoryRow, 0, len(records))
	for _, record := range records {
		res := record.res
		histories = append(histories, db.CheckHistoryRow{
			BlogID:    record.blogID,
			HTTPCode:  res.HTTPCode,
			ErrorCode: res.ErrorCode,
			RTTMs:     res.RTT.Milliseconds(),
			DNSMs:     res.DNS.Milliseconds(),
			TCPMs:     res.TCP.Milliseconds(),
			TLSMs:     res.TLS.Milliseconds(),
			TTFBMs:    res.TTFB.Milliseconds(),
			CheckedAt: resultCheckedAt(res),
		})
	}

	start := time.Now()
	if err := dbRecordCheckHistories(o.ctx, histories); err != nil {
		summary.historyErrors++
		log.Printf("orchestrator: batch record check history rows=%d: %v", len(histories), err)
		for _, row := range histories {
			if err := dbRecordCheckHistory(
				row.BlogID,
				row.HTTPCode,
				row.ErrorCode,
				row.RTTMs,
				row.DNSMs,
				row.TCPMs,
				row.TLSMs,
				row.TTFBMs,
			); err != nil {
				summary.historyErrors++
				log.Printf("orchestrator: record history blog_id=%d: %v", row.BlogID, err)
				continue
			}
			summary.historyRows++
		}
	} else {
		summary.historyRows += len(histories)
	}
	summary.historyDuration += time.Since(start)
}

func resultCheckedAt(res checker.Result) time.Time {
	if res.Timestamp.IsZero() {
		return nowFunc().UTC()
	}
	return res.Timestamp.UTC()
}

func shouldUpdateSSLExpiry(stored *time.Time, observed time.Time) bool {
	if stored == nil {
		return true
	}
	storedYear, storedMonth, storedDay := stored.UTC().Date()
	observedYear, observedMonth, observedDay := observed.UTC().Date()
	return storedYear != observedYear || storedMonth != observedMonth || storedDay != observedDay
}

func (o *Orchestrator) handleRecovery(site db.Site, res checker.Result) {
	entry := o.retries.get(site.BlogID)
	if entry == nil && site.SiteStatus == statusRunning {
		return // was already up, nothing to do
	}

	knownEventID := int64(0)
	if entry != nil {
		knownEventID = entry.eventID
	}
	o.retries.clear(site.BlogID)

	if site.SiteStatus != statusRunning {
		changeTime := nowFunc().UTC()
		log.Printf("orchestrator: blog_id=%d recovered", site.BlogID)
		if entry != nil && site.SiteStatus == statusDown {
			emitCounter("detection.probe_cleared.count", 1)
			emitCounter("detection.probe_cleared."+failureClass(entry.lastResult)+".count", 1)
			emitTimingSince("detection.seems_down_to_probe_cleared.time", entry.firstFailAt, changeTime)
		}

		// Close the open event and project site_status back to running in the
		// same transaction. The resolution reason depends on whether the event
		// was already verifier-confirmed (Down) or still in the local-retry
		// phase (Seems Down).
		if err := o.closeRecoveredEvent(site.BlogID, knownEventID, changeTime); err != nil {
			log.Printf("orchestrator: close recovered event blog_id=%d: %v", site.BlogID, err)
		}

		if inMaintenance(site) {
			o.auditLog(audit.Entry{
				BlogID:    site.BlogID,
				EventType: audit.EventMaintenanceActive,
				Source:    "local",
				Detail:    "recovery suppressed during maintenance",
			})
		} else if !o.isAlertSuppressed(site) {
			o.sendNotification(site, res, statusRunning, changeTime, nil)
		}
	}
}

func (o *Orchestrator) handleFailure(site db.Site, res checker.Result) {
	entry := o.retries.record(res)
	class := failureClass(res)
	emitCounter("detection.failure."+class+".count", 1)

	// Open a Seems Down event on the first failure we don't already have an
	// id for. The schema's idempotent dedup_key means re-detecting the same
	// failure would update the same row, so this is also a self-healing retry
	// path if a previous Open failed to commit.
	if entry.eventID == 0 {
		id, err := o.openSeemsDown(site, res)
		if err != nil {
			log.Printf("orchestrator: open seems-down event blog_id=%d: %v", site.BlogID, err)
		} else {
			entry.eventID = id
			if entry.failCount == 1 {
				emitCounter("detection.seems_down.open.count", 1)
				emitCounter("detection.seems_down.open."+class+".count", 1)
				emitTimingSince("detection.first_failure_to_seems_down.time", entry.firstFailAt, nowFunc().UTC())
			}
		}
	}

	if entry.failCount < config.Get().NumOfChecks {
		meta, _ := json.Marshal(map[string]any{
			"http_code":  res.HTTPCode,
			"error_code": res.ErrorCode,
			"rtt_ms":     res.RTT.Milliseconds(),
			"attempt":    entry.failCount,
			"of":         config.Get().NumOfChecks,
			"event_id":   entry.eventID,
		})
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventID:   entry.eventID,
			EventType: audit.EventRetryDispatched,
			Source:    o.hostname,
			Detail:    fmt.Sprintf("retry %d of %d", entry.failCount, config.Get().NumOfChecks),
			Metadata:  meta,
		})
		return
	}

	// Escalate to verifliers.
	o.escalateToVerifliers(site, entry)
}

func (o *Orchestrator) escalateToVerifliers(site db.Site, entry *retryEntry) {
	clients := o.veriflierSnapshot()
	emitCounter("detection.verifier.escalation.count", 1)
	emitTimingSince("detection.first_failure_to_verification.time", entry.firstFailAt, nowFunc().UTC())
	if len(clients) == 0 {
		emitCounter("detection.verifier.no_clients.count", 1)
		o.confirmDown(site, entry, nil)
		return
	}

	req := veriflier.CheckRequest{
		BlogID:         site.BlogID,
		URL:            site.MonitorURL,
		TimeoutSeconds: int32(timeoutForSite(config.Get(), site)),
		Keyword:        stringPtrValue(site.CheckKeyword),
		CustomHeaders:  checker.ParseCustomHeaders(site.CustomHeaders),
		RedirectPolicy: site.RedirectPolicy,
		RequestID:      veriflier.NewRequestID(),
	}

	escalateMeta, _ := json.Marshal(map[string]any{
		"verifier_count": len(clients),
		"request_id":     req.RequestID,
	})
	o.auditLog(audit.Entry{
		BlogID:    site.BlogID,
		EventType: audit.EventVeriflierSent,
		Source:    o.hostname,
		Detail:    fmt.Sprintf("escalating to %d verifliers", len(clients)),
		Metadata:  escalateMeta,
	})

	// Per-RPC deadline: site's check budget plus headroom for the verifier's
	// own HTTP work, server queueing, and network. Without this the dial /
	// read can hang for o.ctx's lifetime (effectively forever) on a wedged
	// verifier — the old hardcoded 30s client.Timeout was the only bound and
	// has been removed in favor of this caller-controlled deadline.
	rpcDeadline := time.Duration(timeoutForSite(config.Get(), site))*time.Second + verifierRPCHeadroom
	rpcCtx, rpcCancel := stdctx.WithTimeout(o.ctx, rpcDeadline)
	defer rpcCancel()

	type vResult struct {
		host     string
		duration time.Duration
		res      *veriflier.CheckResult
		err      error
	}
	ch := make(chan vResult, len(clients))

	for _, client := range clients {
		c := client
		go func() {
			start := nowFunc()
			res, err := veriflierCheckFunc(c, rpcCtx, req)
			ch <- vResult{host: c.Addr(), duration: nowFunc().Sub(start), res: res, err: err}
		}()
	}

	var vResults []veriflier.CheckResult
	healthyVerifliers := 0
	confirmations := 0

	for range clients {
		vr := <-ch
		emitTiming("verifier.rpc.duration", vr.duration)
		hostSegment := metricSegment(vr.host)
		emitTiming("verifier.host."+hostSegment+".rpc.duration", vr.duration)
		if vr.err != nil {
			emitCounter("verifier.rpc.error.count", 1)
			emitCounter("verifier.host."+hostSegment+".rpc.error.count", 1)
			log.Printf("orchestrator: veriflier %s error: %v", vr.host, vr.err)
			continue
		}
		emitCounter("verifier.rpc.success.count", 1)
		emitCounter("verifier.host."+hostSegment+".rpc.success.count", 1)
		healthyVerifliers++
		// Verifier reply is operational telemetry — recorded under
		// EventVeriflierSent with the response in metadata. The site-state
		// outcome (confirm or false alarm) is captured separately, ultimately
		// as a transition row in jetmon_event_transitions.
		meta, _ := json.Marshal(map[string]any{
			"http_code":  vr.res.HTTPCode,
			"error_code": vr.res.ErrorCode,
			"rtt_ms":     vr.res.RTTMs,
			"success":    vr.res.Success,
			"request_id": vr.res.RequestID,
		})
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventType: audit.EventVeriflierSent,
			Source:    vr.host,
			Detail:    "veriflier reply",
			Metadata:  meta,
		})
		vResults = append(vResults, *vr.res)
		if !vr.res.Success {
			emitCounter("verifier.vote.confirm_down.count", 1)
			emitCounter("verifier.host."+hostSegment+".vote.confirm_down.count", 1)
			confirmations++
		} else {
			emitCounter("verifier.vote.disagree.count", 1)
			emitCounter("verifier.host."+hostSegment+".vote.disagree.count", 1)
		}
	}

	// Adjust quorum floor to healthy verifliers, but minimum 1.
	quorum := config.Get().PeerOfflineLimit
	if healthyVerifliers < quorum {
		quorum = healthyVerifliers
	}
	if quorum < 1 {
		quorum = 1
	}
	emitGauge("detection.verifier.healthy.count", healthyVerifliers)
	emitGauge("detection.verifier.confirmations.count", confirmations)
	emitGauge("detection.verifier.quorum.count", quorum)

	if confirmations >= quorum {
		emitCounter("detection.verifier.quorum_met.count", 1)
		o.confirmDown(site, entry, vResults)
	} else {
		// Verifliers did not confirm — false positive. Close the Seems Down
		// event with reason=false_alarm and reset site_status in the same tx.
		log.Printf("orchestrator: blog_id=%d verifliers did not confirm down (%d/%d)", site.BlogID, confirmations, quorum)
		emitCounter("detection.verifier.false_alarm.count", 1)
		emitCounter("detection.verifier.false_alarm."+failureClass(entry.lastResult)+".count", 1)
		emitTimingSince("detection.seems_down_to_false_alarm.time", entry.firstFailAt, nowFunc().UTC())
		_ = dbRecordFalsePositive(site.BlogID, entry.lastResult.HTTPCode, entry.lastResult.ErrorCode,
			entry.lastResult.RTT.Milliseconds())

		if entry.eventID > 0 {
			meta, _ := json.Marshal(map[string]any{
				"verifier_quorum":    quorum,
				"verifier_healthy":   healthyVerifliers,
				"verifier_disagreed": healthyVerifliers - confirmations,
				"verifier_confirmed": confirmations,
			})
			if err := o.closeEvent(site.BlogID, entry.eventID,
				eventstore.ReasonFalseAlarm, statusRunning, nowFunc().UTC(), meta); err != nil {
				log.Printf("orchestrator: close false-alarm event blog_id=%d event_id=%d: %v",
					site.BlogID, entry.eventID, err)
			}
		}
		o.retries.clear(site.BlogID)
	}
}

func (o *Orchestrator) confirmDown(site db.Site, entry *retryEntry, vResults []veriflier.CheckResult) {
	newStatus := statusConfirmedDown
	changeTime := nowFunc().UTC()
	emitCounter("detection.down.confirmed.count", 1)
	emitCounter("detection.down.confirmed."+failureClass(entry.lastResult)+".count", 1)
	emitTimingSince("detection.seems_down_to_down.time", entry.firstFailAt, changeTime)

	log.Printf("orchestrator: blog_id=%d confirmed down", site.BlogID)

	// Promote the open Seems Down event to Down with reason=verifier_confirmed
	// and project site_status=SITE_CONFIRMED_DOWN in the same tx. If we have no
	// event id (open failed earlier or eventstore unavailable), fall back to
	// the bare projection write.
	if entry.eventID > 0 {
		meta, _ := json.Marshal(map[string]any{
			"verifier_results":   summarizeVerifierResults(vResults),
			"verifier_confirmed": len(vResults),
		})
		if err := o.promoteToDown(site.BlogID, entry.eventID, changeTime, meta); err != nil {
			log.Printf("orchestrator: promote event blog_id=%d event_id=%d: %v", site.BlogID, entry.eventID, err)
		}
	} else if config.LegacyStatusProjectionEnabled() {
		_ = dbUpdateSiteStatus(o.ctx, site.BlogID, newStatus, changeTime)
	}

	if inMaintenance(site) {
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventType: audit.EventMaintenanceActive,
			Source:    "local",
			Detail:    "downtime suppressed during maintenance",
		})
	} else if !o.isAlertSuppressed(site) {
		o.sendNotification(site, entry.lastResult, newStatus, changeTime, vResults)
	} else {
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventType: audit.EventAlertSuppressed,
			Source:    "local",
			Detail:    "cooldown active",
		})
	}

	o.retries.clear(site.BlogID)
}

func (o *Orchestrator) sendNotification(site db.Site, res checker.Result, status int, changeTime time.Time, vResults []veriflier.CheckResult) {
	checks := []wpcom.CheckEntry{
		{
			Type:   1,
			Host:   o.hostname,
			Status: statusFromBool(res.Success),
			RTT:    res.RTT.Milliseconds(),
			Code:   res.HTTPCode,
		},
	}
	for _, vr := range vResults {
		checks = append(checks, wpcom.CheckEntry{
			Type:   2,
			Host:   vr.Host,
			Status: statusFromBool(vr.Success),
			RTT:    vr.RTTMs,
			Code:   int(vr.HTTPCode),
		})
	}

	n := wpcom.Notification{
		BlogID:           site.BlogID,
		MonitorURL:       site.MonitorURL,
		StatusID:         status,
		LastCheck:        res.Timestamp.UTC().Format(time.RFC3339),
		LastStatusChange: changeTime.UTC().Format(time.RFC3339),
		StatusType:       res.StatusType(),
		Checks:           checks,
	}

	o.auditLog(audit.Entry{
		BlogID:    site.BlogID,
		EventType: audit.EventWPCOMSent,
		Source:    "local",
		Detail:    fmt.Sprintf("status=%d type=%s", status, n.StatusType),
	})

	wpcomStatus := wpcomStatusMetricSegment(status)
	emitCounter("wpcom.notification.attempt.count", 1)
	emitCounter("wpcom.notification.status."+wpcomStatus+".attempt.count", 1)
	if err := wpcomNotifyFunc(o.wpcom, n); err != nil {
		emitCounter("wpcom.notification.error.count", 1)
		emitCounter("wpcom.notification.status."+wpcomStatus+".error.count", 1)
		emitCounter("wpcom.notification.retry.count", 1)
		log.Printf("orchestrator: wpcom notify failed for blog_id=%d: %v", site.BlogID, err)
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventType: audit.EventWPCOMRetry,
			Source:    "local",
			Detail:    err.Error(),
		})

		// Single retry.
		if retryErr := wpcomNotifyFunc(o.wpcom, n); retryErr != nil {
			emitCounter("wpcom.notification.error.count", 1)
			emitCounter("wpcom.notification.status."+wpcomStatus+".error.count", 1)
			emitCounter("wpcom.notification.failed.count", 1)
			emitCounter("wpcom.notification.status."+wpcomStatus+".failed.count", 1)
			log.Printf("orchestrator: wpcom notify retry failed for blog_id=%d: %v", site.BlogID, retryErr)
			return
		}
		emitCounter("wpcom.notification.retry.delivered.count", 1)
	}
	emitCounter("wpcom.notification.delivered.count", 1)
	emitCounter("wpcom.notification.status."+wpcomStatus+".delivered.count", 1)
	if err := dbUpdateLastAlertSent(o.ctx, site.BlogID, nowFunc().UTC()); err != nil {
		log.Printf("orchestrator: update last alert sent blog_id=%d: %v", site.BlogID, err)
	}
}

// checkSSLAlerts manages a site-level tls_expiry event that tracks the cert's
// remaining lifetime. The event is opened idempotently — once it's open, every
// HTTPS check is a no-op on the events table unless the threshold (and thus
// severity) changes. The event closes when the cert is renewed beyond the
// outermost threshold.
//
// Severity ladder:
//   - <= 7 days  → Degraded (severity 2)
//   - <= 14 days → Warning  (severity 1)
//   - <= 30 days → Warning  (severity 1)
//   - >  30 days → close any open event with reason=verifier_cleared
func (o *Orchestrator) checkSSLAlerts(site db.Site, expiry time.Time) {
	daysUntil := int(time.Until(expiry).Hours() / 24)

	const (
		warnDays     = 30
		degradedDays = 7
	)

	if daysUntil > warnDays {
		// Cert is healthy. Close any pre-existing tls_expiry event for this site.
		if err := o.closeSSLExpiryIfOpen(site.BlogID); err != nil {
			log.Printf("orchestrator: close tls_expiry event blog_id=%d: %v", site.BlogID, err)
		}
		return
	}

	severity := eventstore.SeverityWarning
	state := eventstore.StateWarning
	if daysUntil <= degradedDays {
		severity = eventstore.SeverityDegraded
		state = eventstore.StateDegraded
	}

	meta, _ := json.Marshal(map[string]any{
		"days_until": daysUntil,
		"expires_at": expiry.UTC().Format(time.RFC3339),
	})

	if err := o.openOrUpdateSSLExpiry(site.BlogID, severity, state, daysUntil, meta); err != nil {
		log.Printf("orchestrator: tls_expiry event blog_id=%d days=%d: %v", site.BlogID, daysUntil, err)
		return
	}
	log.Printf("orchestrator: blog_id=%d SSL cert expires in %d days (severity %d)", site.BlogID, daysUntil, severity)
}

// openOrUpdateSSLExpiry opens a tls_expiry event for the site if none exists,
// or escalates / de-escalates the existing event's severity if a threshold has
// been crossed. site_status is intentionally not projected — TLS expiry
// warnings don't affect the Up/Down state of the site (Layer 2 issue, not a
// Layer 4 outage).
func (o *Orchestrator) openOrUpdateSSLExpiry(blogID int64, severity uint8, state string, daysUntil int, meta json.RawMessage) error {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	out, err := tx.Open(o.ctx, eventstore.OpenInput{
		Identity: eventstore.Identity{BlogID: blogID, CheckType: checkTypeTLSExpiry},
		Severity: severity,
		State:    state,
		Source:   o.hostname,
		Metadata: meta,
	})
	if err != nil {
		return fmt.Errorf("open tls_expiry: %w", err)
	}

	// If the event already existed and its severity differs from the new
	// threshold, escalate (or de-escalate) with a transition row recording why.
	if !out.Opened && out.CurrentSeverity != severity {
		reason := eventstore.ReasonSeverityEscalation
		if severity < out.CurrentSeverity {
			reason = eventstore.ReasonSeverityDeescalation
		}
		if _, err := tx.Promote(o.ctx, out.EventID, severity, state, reason, o.hostname, meta); err != nil {
			return fmt.Errorf("escalate tls_expiry: %w", err)
		}
	}
	return tx.Commit()
}

// closeSSLExpiryIfOpen closes an open tls_expiry event for the site, if any.
// No-op if no event exists.
func (o *Orchestrator) closeSSLExpiryIfOpen(blogID int64) error {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if tx.Tx() == nil {
		return tx.Commit()
	}
	ae, err := tx.FindActiveByBlog(o.ctx, blogID, checkTypeTLSExpiry)
	if err != nil {
		if errors.Is(err, eventstore.ErrEventNotFound) {
			return tx.Commit()
		}
		return err
	}
	if err := tx.Close(o.ctx, ae.ID, eventstore.ReasonVerifierCleared, o.hostname, nil); err != nil {
		return fmt.Errorf("close tls_expiry: %w", err)
	}
	return tx.Commit()
}

func (o *Orchestrator) isAlertSuppressed(site db.Site) bool {
	cfg := config.Get()
	cooldown := cfg.AlertCooldownMinutes
	if site.AlertCooldownMinutes != nil {
		cooldown = *site.AlertCooldownMinutes
	}
	if cooldown <= 0 {
		return false
	}
	if site.LastAlertSentAt == nil || site.LastAlertSentAt.IsZero() {
		return false
	}
	return time.Since(*site.LastAlertSentAt) < time.Duration(cooldown)*time.Minute
}

func (o *Orchestrator) checkLegacyProjectionDrift(cfg *config.Config) {
	if !cfg.LegacyStatusProjectionEnable {
		return
	}
	count, err := dbCountProjectionDrift(o.ctx, o.bucketMin, o.bucketMax)
	if err != nil {
		log.Printf("orchestrator: legacy projection drift check failed: %v", err)
		emitCounter("projection.drift.check_error.count", 1)
		return
	}
	emitGauge("projection.drift.count", count)
	if count > 0 {
		log.Printf("orchestrator: WARN legacy projection drift detected count=%d buckets=%d-%d", count, o.bucketMin, o.bucketMax)
		emitCounter("projection.drift.detected.count", 1)
	}
}

// RetryQueueSize returns the number of sites currently in local retry.
func (o *Orchestrator) RetryQueueSize() int {
	return o.retries.size()
}

// BucketRange returns the current bucket min/max for this host.
func (o *Orchestrator) BucketRange() (int, int) {
	return o.bucketMin, o.bucketMax
}

func (o *Orchestrator) usesPinnedBuckets(cfg *config.Config) bool {
	_, _, ok := cfg.PinnedBucketRange()
	return ok
}

// WorkerCount returns the live worker count.
func (o *Orchestrator) WorkerCount() int {
	return o.pool.WorkerCount()
}

// ActiveChecks returns the active-check count.
func (o *Orchestrator) ActiveChecks() int {
	return o.pool.ActiveCount()
}

// QueueDepth returns the work queue depth.
func (o *Orchestrator) QueueDepth() int {
	return o.pool.QueueDepth()
}

// LastRoundStats returns the latest completed round's throughput and duration.
func (o *Orchestrator) LastRoundStats() (int, time.Duration) {
	o.statsMu.RLock()
	defer o.statsMu.RUnlock()
	return o.lastRoundSPS, o.lastRoundDur
}

func (o *Orchestrator) auditLog(e audit.Entry) {
	if err := audit.Log(o.ctx, e); err != nil {
		log.Printf("audit: blog_id=%d event=%s: %v", e.BlogID, e.EventType, err)
	}
}

func emitCounter(stat string, value int) {
	if m := metricsClientFunc(); m != nil {
		m.Increment(stat, value)
	}
}

func emitGauge(stat string, value int) {
	if m := metricsClientFunc(); m != nil {
		m.Gauge(stat, value)
	}
}

func emitTiming(stat string, d time.Duration) {
	if d < 0 {
		return
	}
	if m := metricsClientFunc(); m != nil {
		m.Timing(stat, d)
	}
}

func emitTimingSince(stat string, start, end time.Time) {
	if start.IsZero() || end.IsZero() {
		return
	}
	emitTiming(stat, end.Sub(start))
}

func failureClass(res checker.Result) string {
	return metricSegment((&res).StatusType())
}

func metricSegment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "unknown"
	}

	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}

	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unknown"
	}
	return out
}

// openSeemsDown opens (or re-detects) a Seems Down event for an HTTP-failing
// site and projects v1 site_status=SITE_DOWN in the same transaction. Returns
// the event id. Idempotent: a re-detection of the same identity returns the
// existing event's id with no transition row written and no projection update.
func (o *Orchestrator) openSeemsDown(site db.Site, res checker.Result) (int64, error) {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	meta, _ := json.Marshal(map[string]any{
		"http_code":  res.HTTPCode,
		"error_code": res.ErrorCode,
		"rtt_ms":     res.RTT.Milliseconds(),
		"url":        site.MonitorURL,
	})

	out, err := tx.Open(o.ctx, eventstore.OpenInput{
		Identity: eventstore.Identity{BlogID: site.BlogID, CheckType: checkTypeHTTP},
		Severity: eventstore.SeveritySeemsDown,
		State:    eventstore.StateSeemsDown,
		Source:   o.hostname,
		Metadata: meta,
	})
	if err != nil {
		return 0, err
	}

	// Project v1 site_status=SITE_DOWN only on the actual insert. A re-detection
	// (Opened=false) is by definition a row that already exists, so site_status
	// was already projected when the event first opened.
	if out.Opened && config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
		if err := db.UpdateSiteStatusTx(o.ctx, tx.Tx(), site.BlogID, statusDown, nowFunc().UTC()); err != nil {
			return 0, fmt.Errorf("project site_status: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return out.EventID, nil
}

// promoteToDown bumps an open Seems Down event to Down (severity 4) and
// projects site_status=SITE_CONFIRMED_DOWN in the same transaction.
func (o *Orchestrator) promoteToDown(blogID, eventID int64, changeTime time.Time, meta json.RawMessage) error {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Promote(o.ctx, eventID,
		eventstore.SeverityDown, eventstore.StateDown,
		eventstore.ReasonVerifierConfirmed, o.hostname, meta); err != nil {
		return fmt.Errorf("promote event: %w", err)
	}

	if config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
		if err := db.UpdateSiteStatusTx(o.ctx, tx.Tx(), blogID, statusConfirmedDown, changeTime); err != nil {
			return fmt.Errorf("project site_status: %w", err)
		}
	}
	return tx.Commit()
}

// closeEvent closes an open event with the given resolution reason and projects
// site_status to the given v1 value in the same transaction.
func (o *Orchestrator) closeEvent(blogID, eventID int64, reason string, projectedStatus int, changeTime time.Time, meta json.RawMessage) error {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.Close(o.ctx, eventID, reason, o.hostname, meta); err != nil {
		return fmt.Errorf("close event: %w", err)
	}

	if config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
		if err := db.UpdateSiteStatusTx(o.ctx, tx.Tx(), blogID, projectedStatus, changeTime); err != nil {
			return fmt.Errorf("project site_status: %w", err)
		}
	}
	return tx.Commit()
}

// closeRecoveredEvent closes the open event for a recovering site. Picks
// resolution reason from the event's current state — Seems Down → probe_cleared,
// Down → verifier_cleared. If the caller already knows the event id (from the
// retry entry) it is used directly; otherwise the active event is looked up
// inside the transaction. site_status is projected back to SITE_RUNNING in the
// same tx.
func (o *Orchestrator) closeRecoveredEvent(blogID, knownEventID int64, changeTime time.Time) error {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Determine event id and current state. If knownEventID is set, read state
	// directly; otherwise look up the active event for this blog.
	var eventID int64
	var state string
	switch {
	case knownEventID > 0 && tx.Tx() != nil:
		eventID = knownEventID
		if err := tx.Tx().QueryRowContext(o.ctx,
			`SELECT state FROM jetmon_events WHERE id = ?`, eventID,
		).Scan(&state); err != nil {
			return fmt.Errorf("read event state: %w", err)
		}
	case tx.Tx() != nil:
		ae, err := tx.FindActiveByBlog(o.ctx, blogID, checkTypeHTTP)
		if err != nil {
			if errors.Is(err, eventstore.ErrEventNotFound) {
				// site_status disagreed with the event store (no open event but
				// projection said non-running). Just project back to running.
				if config.LegacyStatusProjectionEnabled() {
					if err := db.UpdateSiteStatusTx(o.ctx, tx.Tx(), blogID, statusRunning, changeTime); err != nil {
						return fmt.Errorf("project site_status: %w", err)
					}
				}
				return tx.Commit()
			}
			return err
		}
		eventID = ae.ID
		state = ae.State
	default:
		// nil-mode (no DB): nothing to do.
		return tx.Commit()
	}

	reason := eventstore.ReasonProbeCleared
	if state == eventstore.StateDown {
		reason = eventstore.ReasonVerifierCleared
	}

	if err := tx.Close(o.ctx, eventID, reason, o.hostname, nil); err != nil {
		return fmt.Errorf("close event: %w", err)
	}
	if config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
		if err := db.UpdateSiteStatusTx(o.ctx, tx.Tx(), blogID, statusRunning, changeTime); err != nil {
			return fmt.Errorf("project site_status: %w", err)
		}
	}
	return tx.Commit()
}

// summarizeVerifierResults extracts a small JSON-friendly summary of verifier
// replies for storage in transition metadata. We don't store the full result
// list — the per-RPC details are already in jetmon_audit_log under
// EventVeriflierSent.
func summarizeVerifierResults(vResults []veriflier.CheckResult) []map[string]any {
	out := make([]map[string]any, 0, len(vResults))
	for _, vr := range vResults {
		out = append(out, map[string]any{
			"host":      vr.Host,
			"success":   vr.Success,
			"http_code": vr.HTTPCode,
			"rtt_ms":    vr.RTTMs,
		})
	}
	return out
}

func inMaintenance(site db.Site) bool {
	now := time.Now()
	if site.MaintenanceStart == nil || site.MaintenanceEnd == nil {
		return false
	}
	return now.After(*site.MaintenanceStart) && now.Before(*site.MaintenanceEnd)
}

func statusFromBool(success bool) int {
	if success {
		return statusRunning
	}
	return 0
}

func wpcomStatusMetricSegment(status int) string {
	switch status {
	case statusDown:
		return "down"
	case statusRunning:
		return "running"
	case statusConfirmedDown:
		return "confirmed_down"
	default:
		return "unknown"
	}
}

func (o *Orchestrator) refreshVeriflierClients(cfg *config.Config) {
	newAddrs := make([]string, 0, len(cfg.Verifiers))
	for _, v := range cfg.Verifiers {
		newAddrs = append(newAddrs, fmt.Sprintf("%s:%s|%s", v.Host, v.TransportPort(), v.AuthToken))
	}

	o.veriflierMu.RLock()
	unchanged := slicesEqual(o.veriflierAddrs, newAddrs)
	o.veriflierMu.RUnlock()
	if unchanged {
		return
	}

	clients := make([]*veriflier.VeriflierClient, 0, len(cfg.Verifiers))
	for _, v := range cfg.Verifiers {
		addr := fmt.Sprintf("%s:%s", v.Host, v.TransportPort())
		clients = append(clients, veriflier.NewVeriflierClient(addr, v.AuthToken))
	}
	o.veriflierMu.Lock()
	o.veriflierClients = clients
	o.veriflierAddrs = newAddrs
	o.veriflierMu.Unlock()
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (o *Orchestrator) veriflierSnapshot() []*veriflier.VeriflierClient {
	o.veriflierMu.RLock()
	defer o.veriflierMu.RUnlock()
	out := make([]*veriflier.VeriflierClient, len(o.veriflierClients))
	copy(out, o.veriflierClients)
	return out
}

func timeoutForSite(cfg *config.Config, site db.Site) int {
	timeout := cfg.NetCommsTimeout
	if site.TimeoutSeconds != nil {
		timeout = *site.TimeoutSeconds
	}
	return timeout
}

func stringPtrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (o *Orchestrator) applyMemoryPressure(cfg *config.Config) {
	if cfg.WorkerMaxMemMB <= 0 || o.pool == nil {
		return
	}

	rssMB := currentMemoryMBFunc()
	if rssMB <= 0 || rssMB <= cfg.WorkerMaxMemMB {
		return
	}

	current := o.pool.WorkerCount()
	toDrain := current / 10
	if toDrain < 1 {
		toDrain = 1
	}
	drained := o.pool.DrainWorkers(toDrain)
	if drained == 0 {
		return
	}

	// Lower the autoscaler ceiling for the rest of this round to avoid
	// immediately respawning the workers we just drained.
	o.pool.SetMaxSize(max(1, current-drained))
	log.Printf(
		"orchestrator: memory pressure %dMB > %dMB, draining %d workers",
		rssMB,
		cfg.WorkerMaxMemMB,
		drained,
	)
}

func currentMemoryMB() int {
	samples := []runtimemetrics.Sample{
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
	}
	runtimemetrics.Read(samples)

	total := samples[0].Value.Uint64()
	released := samples[1].Value.Uint64()
	if total <= released {
		return 0
	}
	return int((total - released) / 1024 / 1024)
}

package orchestrator

import (
	stdctx "context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	runtimemetrics "runtime/metrics"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/checkmode"
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
	checkTypeHTTP          = "http"
	checkTypeTLSExpiry     = "tls_expiry"
	checkTypeTLSDeprecated = "tls_deprecated"
)

// verifierRPCHeadroom is added to the per-site check timeout when computing
// the RPC deadline for a verifier call. The verifier needs enough budget to
// run its own HTTP check (matches site timeout) plus serialization, queueing,
// and network round-trip — 5s covers a comfortable steady-state and forces
// failure on a truly wedged verifier rather than letting the call hang.
const verifierRPCHeadroom = 5 * time.Second
const verifierTelemetryStatusTimeout = 2 * time.Second

const schedulerBackpressurePollInterval = 10 * time.Millisecond
const schedulerVariableIntervalPollInterval = 5 * time.Second
const schedulerBacklogPollInterval = 5 * time.Second
const schedulerAPIControlledRangePollInterval = 5 * time.Second
const schedulerBroadReportInterval = time.Minute
const eventMutationMaxAttempts = 3
const eventMutationRetryBaseDelay = 25 * time.Millisecond
const failedCheckRetryInterval = time.Minute
const maxPostRecoveryTransientFailureWindow = 5 * time.Minute
const minPostFalseAlarmTransientFailureWindow = 5 * time.Minute
const maxPostFalseAlarmTransientFailureWindow = 10 * time.Minute
const verifierOperationalBackoffBase = 15 * time.Second
const verifierOperationalBackoffMax = 2 * time.Minute
const verifierOperationalCooldownBase = 30 * time.Second
const verifierOperationalCooldownMax = 2 * time.Minute
const verifierOperationalCooldownMemory = 10 * time.Minute
const wpcomPermanentFailureLogInterval = 10 * time.Second

// VariableIntervalPollInterval returns the idle scheduler poll interval used
// when per-site check intervals are enabled. The SQL due predicate prevents
// early checks; this only controls how quickly newly due work is discovered.
func VariableIntervalPollInterval() time.Duration {
	return schedulerVariableIntervalPollInterval
}

var (
	nowFunc                 = time.Now
	dbClaimBuckets          = db.ClaimBuckets
	dbHeartbeat             = db.Heartbeat
	dbReleaseHost           = db.ReleaseHostAndRebalance
	dbMarkHostDraining      = db.MarkHostDraining
	dbGetSitesForBucket     = db.GetSitesForBucket
	dbListActiveSites       = db.ListActiveSitesForBucketRange
	dbCountActiveSites      = db.CountActiveSitesForBucketRange
	dbMarkSiteChecked       = db.MarkSiteChecked
	dbMarkSitesChecked      = db.MarkSitesChecked
	dbRecordCheckHistory    = db.RecordCheckHistory
	dbRecordCheckHistories  = db.RecordCheckHistories
	dbUpdateSSLExpiry       = db.UpdateSSLExpiry
	dbUpdateSSLExpiries     = db.UpdateSSLExpiries
	dbUpdateSiteStatus      = db.UpdateSiteStatus
	dbGetSiteStatus         = db.GetSiteStatusForMonitorSite
	dbRecordFalsePositive   = db.RecordFalsePositive
	dbUpdateLastAlertSent   = db.UpdateLastAlertSent
	dbCountDueSites         = db.CountDueSitesForBucketRange
	dbCountProjectionDrift  = db.CountLegacyProjectionDrift
	dbListVeriflierVantages = db.ListEnabledVeriflierVantages
	dbUpsertVeriflierAgent  = db.UpsertVeriflierAgent
	dbUpsertSiteSafetyFlag  = db.UpsertSiteSafetyFlag
	dbGetActiveRolloutRange = db.GetActiveRolloutRange
	veriflierStatusFunc     = func(c *veriflier.VeriflierClient, ctx stdctx.Context) (*veriflier.StatusV2Response, error) {
		return c.Status(ctx)
	}
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, ctx stdctx.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
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
	dueCountsSampled  bool
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
	sslRows           int
	markCheckedErrors int
	historyErrors     int
	sslErrors         int

	checkSuccesses     int
	checkFailures      int
	checkOffline       int
	checkHTTPFailures  int
	checkTimeouts      int
	checkConnectErrors int
	checkSSLErrors     int
	checkRedirects     int
	checkKeywords      int
	checkTLSDeprecated int
	checkCohorts       map[checkCohortKey]int
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
	if other.dueCountsSampled {
		s.dueCountsSampled = true
	}
	s.dispatchDuration += other.dispatchDuration
	s.waitDuration += other.waitDuration
	s.processDuration += other.processDuration
	s.markCheckedDuration += other.markCheckedDuration
	s.historyDuration += other.historyDuration
	s.sslDuration += other.sslDuration
	s.eventDuration += other.eventDuration
	s.markCheckedRows += other.markCheckedRows
	s.historyRows += other.historyRows
	s.sslRows += other.sslRows
	s.markCheckedErrors += other.markCheckedErrors
	s.historyErrors += other.historyErrors
	s.sslErrors += other.sslErrors
	s.checkSuccesses += other.checkSuccesses
	s.checkFailures += other.checkFailures
	s.checkOffline += other.checkOffline
	s.checkHTTPFailures += other.checkHTTPFailures
	s.checkTimeouts += other.checkTimeouts
	s.checkConnectErrors += other.checkConnectErrors
	s.checkSSLErrors += other.checkSSLErrors
	s.checkRedirects += other.checkRedirects
	s.checkKeywords += other.checkKeywords
	s.checkTLSDeprecated += other.checkTLSDeprecated
	mergeCheckCohorts(&s.checkCohorts, other.checkCohorts)
	if other.oldestSelectedAge > s.oldestSelectedAge {
		s.oldestSelectedAge = other.oldestSelectedAge
	}
	if other.interrupted {
		s.interrupted = true
	}
}

type resultProcessSummary struct {
	processed         int
	markCheckedRows   int
	historyRows       int
	sslRows           int
	markCheckedErrors int
	historyErrors     int
	sslErrors         int

	checkSuccesses     int
	checkFailures      int
	checkOffline       int
	checkHTTPFailures  int
	checkTimeouts      int
	checkConnectErrors int
	checkSSLErrors     int
	checkRedirects     int
	checkKeywords      int
	checkTLSDeprecated int
	checkCohorts       map[checkCohortKey]int

	markCheckedDuration time.Duration
	historyDuration     time.Duration
	sslDuration         time.Duration
	eventDuration       time.Duration
}

type checkCohortKey struct {
	method  string
	profile string
}

type siteCheckResult struct {
	blogID int64
	site   db.Site
	res    checker.Result
}

// Orchestrator drives the main check loop.
type Orchestrator struct {
	pool                *checker.Pool
	retries             *retryQueue
	wpcom               *wpcom.Client
	events              *eventstore.Store
	veriflierClients    []*veriflier.VeriflierClient
	veriflierAddrs      []string // parallel slice of "addr|token" for change detection
	veriflierMu         sync.RWMutex
	veriflierCooldownMu sync.Mutex
	veriflierCooldowns  map[string]verifierCooldown
	hostname            string
	bucketMin           int
	bucketMax           int

	totalChecked int
	roundStart   time.Time
	statsMu      sync.RWMutex
	lastRoundSPS int
	lastRoundDur time.Duration

	lastDueCountAt        time.Time
	lastProjectionDriftAt time.Time

	wpcomNotifyDisabledLogOnce sync.Once
	wpcomPermanentMu           sync.Mutex
	wpcomPermanentLastLog      time.Time
	wpcomPermanentSuppressed   int

	ctx    stdctx.Context
	cancel stdctx.CancelFunc
}

// New creates an Orchestrator. Call Run to start the check loop.
func New(cfg *config.Config, wp *wpcom.Client) *Orchestrator {
	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	pool := checker.NewPool(cfg.NumWorkers/2, 1, cfg.NumWorkers)

	o := &Orchestrator{
		pool:      pool,
		retries:   newRetryQueue(),
		wpcom:     wp,
		events:    eventstore.New(db.DB()),
		hostname:  db.Hostname(),
		bucketMax: -1,
		ctx:       ctx,
		cancel:    cancel,
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
	if cfg.RolloutMode == config.RolloutModeStandby || cfg.RolloutMode == config.RolloutModeAPIControlled {
		o.bucketMin = 0
		o.bucketMax = -1
		log.Printf("orchestrator: rollout_mode=%s; dynamic bucket claiming disabled", cfg.RolloutMode)
		return nil
	}
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
			o.shutdown()
			return
		default:
		}

		cfg := config.Get()
		if cfg.RolloutMode == config.RolloutModeStandby {
			o.waitInRolloutStandby(cfg.RolloutMode)
			continue
		}
		if cfg.RolloutMode == config.RolloutModeAPIControlled {
			ok, err := o.refreshAPIControlledRange()
			if err != nil {
				log.Printf("orchestrator: api-controlled rollout range lookup failed: %v", err)
				o.waitInRolloutStandby(cfg.RolloutMode)
				continue
			}
			if !ok {
				o.waitInRolloutStandby(cfg.RolloutMode)
				continue
			}
		}
		if cfg.SchedulerEngine == "streaming" {
			o.runStreamingEngine()
			if cfg.RolloutMode == config.RolloutModeAPIControlled {
				continue
			}
			return
		}
		o.pool.SetMaxSize(cfg.NumWorkers)
		o.refreshVeriflierClients(cfg)
		o.syncVeriflierAgentTelemetry(cfg)

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

func (o *Orchestrator) waitInRolloutStandby(mode string) {
	if o.bucketMax >= o.bucketMin {
		log.Printf("orchestrator: rollout_mode=%s entering standby; no active API bucket lock", mode)
	}
	o.bucketMin = 0
	o.bucketMax = -1
	select {
	case <-time.After(5 * time.Second):
	case <-o.ctx.Done():
	}
}

func (o *Orchestrator) refreshAPIControlledRange() (bool, error) {
	rng, ok, err := dbGetActiveRolloutRange(o.ctx, o.hostname)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if o.bucketMin != rng.BucketMin || o.bucketMax != rng.BucketMax {
		log.Printf("orchestrator: api-controlled rollout range active buckets=%d-%d count=%d", rng.BucketMin, rng.BucketMax, rng.Count)
		o.bucketMin = rng.BucketMin
		o.bucketMax = rng.BucketMax
	}
	return true, nil
}

func (o *Orchestrator) shutdown() {
	log.Println("orchestrator: shutting down")
	cfg := config.Get()
	if o.usesDynamicBuckets(cfg) {
		if err := dbMarkHostDraining(stdctx.Background(), o.hostname); err != nil {
			log.Printf("orchestrator: mark draining: %v", err)
		}
	}
	if o.pool != nil {
		o.pool.Drain()
	}
	if !o.usesDynamicBuckets(cfg) {
		log.Printf("orchestrator: rollout_mode=%s or pinned bucket mode active; no jetmon_hosts row to release", cfg.RolloutMode)
	} else if err := dbReleaseHost(stdctx.Background(), o.hostname, cfg.BucketTotal, cfg.BucketTarget); err != nil {
		log.Printf("orchestrator: release host: %v", err)
	}
}

// Stop signals the orchestrator to shut down after the current round.
func (o *Orchestrator) Stop() {
	o.cancel()
}

func (o *Orchestrator) runRound() roundSummary {
	cfg := config.Get()
	summary := roundSummary{}
	reportNow := nowFunc().UTC()
	if o.roundStart.IsZero() {
		o.roundStart = time.Now()
	}

	switch {
	case cfg.RolloutMode == config.RolloutModeAPIControlled:
		if o.bucketMax < o.bucketMin {
			log.Println("orchestrator: api-controlled rollout round skipped; no active bucket lock")
			o.finishRound(cfg, summary)
			return summary
		}
	case o.usesPinnedBuckets(cfg):
		if err := o.ClaimBuckets(); err != nil {
			log.Printf("orchestrator: pinned bucket claim failed: %v", err)
		}
	default:
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
	dueCountsSampled := !cfg.UseVariableCheckIntervals || o.shouldSampleDueCounts(reportNow)
	if o.shouldSampleProjectionDrift(cfg, reportNow) {
		o.checkLegacyProjectionDrift(cfg)
	}

	if dueCountsSampled {
		summary.dueCountsSampled = true
		if due, err := dbCountDueSites(o.ctx, o.bucketMin, o.bucketMax, cfg.UseVariableCheckIntervals); err != nil {
			summary.dueCountErrors++
			log.Printf("orchestrator: count due sites failed: %v", err)
		} else {
			summary.dueAtStart = due
		}
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
		config.Debugf("orchestrator: checking %d sites (scheduler page %d)", len(page), summary.pagesFetched)

		pageSummary := o.checkSitesPage(cfg, page, summary.pagesFetched)
		summary.add(pageSummary)
		if pageSummary.interrupted || pageSummary.outstanding > 0 {
			break
		}
		if len(sites) < pageSize {
			break
		}
	}

	if cfg.UseVariableCheckIntervals && dueCountsSampled {
		if due, err := dbCountDueSites(o.ctx, o.bucketMin, o.bucketMax, true); err != nil {
			summary.dueCountErrors++
			log.Printf("orchestrator: count remaining due sites failed: %v", err)
		} else {
			summary.dueRemaining = due
		}
	} else if !cfg.UseVariableCheckIntervals {
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
		siteMap[monitorTargetID(s)] = s
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
	summary.sslRows += processSummary.sslRows
	summary.markCheckedErrors += processSummary.markCheckedErrors
	summary.historyErrors += processSummary.historyErrors
	summary.sslErrors += processSummary.sslErrors
	summary.checkSuccesses += processSummary.checkSuccesses
	summary.checkFailures += processSummary.checkFailures
	summary.checkOffline += processSummary.checkOffline
	summary.checkHTTPFailures += processSummary.checkHTTPFailures
	summary.checkTimeouts += processSummary.checkTimeouts
	summary.checkConnectErrors += processSummary.checkConnectErrors
	summary.checkSSLErrors += processSummary.checkSSLErrors
	summary.checkRedirects += processSummary.checkRedirects
	summary.checkKeywords += processSummary.checkKeywords
	summary.checkTLSDeprecated += processSummary.checkTLSDeprecated
	mergeCheckCohorts(&summary.checkCohorts, processSummary.checkCohorts)
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
	m.Increment("scheduler.page.ssl.row.count", summary.sslRows)
	m.Increment("scheduler.page.mark_checked.error.count", summary.markCheckedErrors)
	m.Increment("scheduler.page.history.error.count", summary.historyErrors)
	m.Increment("scheduler.page.ssl.error.count", summary.sslErrors)
	m.Increment("scheduler.page.check.success.count", summary.checkSuccesses)
	m.Increment("scheduler.page.check.failure.count", summary.checkFailures)
	m.Increment("scheduler.page.check.http_failure.count", summary.checkHTTPFailures)
	m.Increment("scheduler.page.check.timeout.count", summary.checkTimeouts)
	m.Increment("scheduler.page.check.connect_error.count", summary.checkConnectErrors)
	m.Increment("scheduler.page.check.ssl_error.count", summary.checkSSLErrors)
	m.Increment("scheduler.page.check.redirect.count", summary.checkRedirects)
	m.Increment("scheduler.page.check.keyword.count", summary.checkKeywords)
	m.Increment("scheduler.page.check.tls_deprecated.count", summary.checkTLSDeprecated)
	emitCheckCohortCounters(m, "scheduler.page", summary.checkCohorts)
}

func logPageSummary(pageNumber, sites int, summary roundSummary) {
	config.Debugf(
		"orchestrator: page summary page=%d sites=%d dispatched=%d completed=%d outstanding=%d dispatch=%s wait=%s process=%s mark_checked=%s history=%s ssl=%s events=%s checks_success=%d checks_failure=%d checks_http_failure=%d checks_timeout=%d checks_connect_error=%d checks_ssl_error=%d checks_redirect=%d checks_keyword=%d checks_tls_deprecated=%d mark_checked_rows=%d history_rows=%d ssl_rows=%d mark_checked_errors=%d history_errors=%d ssl_errors=%d",
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
		summary.checkSuccesses,
		summary.checkFailures,
		summary.checkHTTPFailures,
		summary.checkTimeouts,
		summary.checkConnectErrors,
		summary.checkSSLErrors,
		summary.checkRedirects,
		summary.checkKeywords,
		summary.checkTLSDeprecated,
		summary.markCheckedRows,
		summary.historyRows,
		summary.sslRows,
		summary.markCheckedErrors,
		summary.historyErrors,
		summary.sslErrors,
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
		targetID := monitorTargetID(site)
		if _, ok := seen[targetID]; ok {
			continue
		}
		seen[targetID] = struct{}{}
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
	method := effectiveCheckMethod(cfg, site)
	profile := effectiveDetectionProfile(cfg, site, method)
	req := checker.Request{
		MonitorSiteID:       site.ID,
		BlogID:              site.BlogID,
		URL:                 site.MonitorURL,
		Method:              method,
		DetectionProfile:    profile,
		TimeoutSeconds:      timeoutForSite(cfg, site),
		BodyReadMaxBytes:    cfg.BodyReadMaxBytes,
		BodyReadMaxMS:       cfg.BodyReadMaxMS,
		KeywordReadMaxBytes: cfg.KeywordReadMaxBytes,
		KeywordReadMaxMS:    cfg.KeywordReadMaxMS,
		CustomHeaders:       checker.ParseCustomHeaders(site.CustomHeaders),
		RedirectPolicy:      checker.RedirectFollow,
		EnforceTargetSafety: true,
	}
	if profile == checkmode.ProfileFull {
		req.Keyword = site.CheckKeyword
		req.ForbiddenKeyword = site.ForbiddenKeyword
		req.ForbiddenKeywords = checker.ParseForbiddenKeywords(site.ForbiddenKeywords)
		req.RedirectPolicy = checker.RedirectPolicy(site.RedirectPolicy)
		if req.RedirectPolicy == "" {
			req.RedirectPolicy = checker.RedirectFollow
		}
	}
	return req
}

func effectiveCheckMethod(cfg *config.Config, site db.Site) string {
	def := checkmode.MethodGET
	if cfg != nil && cfg.DefaultCheckMethod != "" {
		def = cfg.DefaultCheckMethod
	}
	method, err := checkmode.NormalizeMethod(site.RequestMethod, def)
	if err != nil {
		return def
	}
	return method
}

func effectiveDetectionProfile(cfg *config.Config, site db.Site, method string) string {
	def := checkmode.ProfileFull
	if cfg != nil && cfg.DefaultDetectionProfile != "" {
		def = cfg.DefaultDetectionProfile
	}
	profile, err := checkmode.NormalizeProfile(site.DetectionProfile, def)
	if err != nil {
		return checkmode.EffectiveProfile(method, def)
	}
	return checkmode.EffectiveProfile(method, profile)
}

func fullDetectionsEnabled(cfg *config.Config, site db.Site) bool {
	method := effectiveCheckMethod(cfg, site)
	profile := effectiveDetectionProfile(cfg, site, method)
	return checkmode.FullDetectionsEnabled(method, profile)
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
	targetID := checkResultTargetID(res)
	if _, ok := siteMap[targetID]; !ok {
		summary.staleResults++
		config.Debugf("orchestrator: ignored stale check result target_id=%d blog_id=%d", targetID, res.BlogID)
		return
	}
	if _, ok := results[targetID]; ok {
		summary.duplicateResults++
		config.Debugf("orchestrator: ignored duplicate check result target_id=%d blog_id=%d", targetID, res.BlogID)
		return
	}
	results[targetID] = res
}

func (o *Orchestrator) shouldSampleDueCounts(now time.Time) bool {
	if o.lastDueCountAt.IsZero() || now.Before(o.lastDueCountAt) || now.Sub(o.lastDueCountAt) >= schedulerBroadReportInterval {
		o.lastDueCountAt = now
		return true
	}
	return false
}

func (o *Orchestrator) shouldSampleProjectionDrift(cfg *config.Config, now time.Time) bool {
	if !cfg.LegacyStatusProjectionEnable {
		return false
	}
	if o.lastProjectionDriftAt.IsZero() || now.Before(o.lastProjectionDriftAt) || now.Sub(o.lastProjectionDriftAt) >= schedulerBroadReportInterval {
		o.lastProjectionDriftAt = now
		return true
	}
	return false
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
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

	activeChecks := 0
	queueDepth := 0
	workerCount := 0
	if o.pool != nil {
		activeChecks = o.pool.ActiveCount()
		queueDepth = o.pool.QueueDepth()
		workerCount = o.pool.WorkerCount()
	}
	retryQueueSize := 0
	if o.retries != nil {
		retryQueueSize = o.retries.size()
	}

	m := metricsClientFunc()
	if m != nil {
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
		m.Gauge("scheduler.round.due_count_sampled.count", boolInt(summary.dueCountsSampled))
		if summary.dueCountsSampled {
			m.Gauge("scheduler.round.due_start.count", summary.dueAtStart)
			m.Gauge("scheduler.round.due_remaining.count", summary.dueRemaining)
		}
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
		m.Increment("scheduler.round.ssl.row.count", summary.sslRows)
		m.Increment("scheduler.round.mark_checked.error.count", summary.markCheckedErrors)
		m.Increment("scheduler.round.history.error.count", summary.historyErrors)
		m.Increment("scheduler.round.ssl.error.count", summary.sslErrors)
		m.Increment("scheduler.round.check.success.count", summary.checkSuccesses)
		m.Increment("scheduler.round.check.failure.count", summary.checkFailures)
		m.Increment("scheduler.round.check.http_failure.count", summary.checkHTTPFailures)
		m.Increment("scheduler.round.check.timeout.count", summary.checkTimeouts)
		m.Increment("scheduler.round.check.connect_error.count", summary.checkConnectErrors)
		m.Increment("scheduler.round.check.ssl_error.count", summary.checkSSLErrors)
		m.Increment("scheduler.round.check.redirect.count", summary.checkRedirects)
		m.Increment("scheduler.round.check.keyword.count", summary.checkKeywords)
		m.Increment("scheduler.round.check.tls_deprecated.count", summary.checkTLSDeprecated)
		emitCheckCohortCounters(m, "scheduler.round", summary.checkCohorts)

		if cfg.StatsdSendMemUsage {
			m.EmitMemStats()
		}
	}
	metrics.WriteStatsFiles(metrics.StatsFilesSnapshot{
		SitesPerSec: sps,
		QueueSize:   queueDepth,
		Working:     activeChecks,
		Waiting:     nonNegative(workerCount - activeChecks),
		Halting:     0,
		Error:       nonNegative(summary.checkFailures - summary.checkOffline),
		Offline:     summary.checkOffline,
		Success:     summary.checkSuccesses,
		Total:       summary.completed,
	})
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
	config.Debugf(
		"orchestrator: round summary pages=%d due_count_sampled=%t due_start=%d selected=%d dispatched=%d completed=%d outstanding=%d due_remaining=%d backpressure_waits=%d stale_results=%d duplicate_results=%d never_checked=%d oldest_selected_age_sec=%d dispatch=%s wait=%s process=%s mark_checked=%s history=%s ssl=%s events=%s checks_success=%d checks_failure=%d checks_http_failure=%d checks_timeout=%d checks_connect_error=%d checks_ssl_error=%d checks_redirect=%d checks_keyword=%d checks_tls_deprecated=%d mark_checked_rows=%d history_rows=%d ssl_rows=%d mark_checked_errors=%d history_errors=%d ssl_errors=%d duration=%s sps=%d",
		summary.pagesFetched,
		summary.dueCountsSampled,
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
		summary.checkSuccesses,
		summary.checkFailures,
		summary.checkHTTPFailures,
		summary.checkTimeouts,
		summary.checkConnectErrors,
		summary.checkSSLErrors,
		summary.checkRedirects,
		summary.checkKeywords,
		summary.checkTLSDeprecated,
		summary.markCheckedRows,
		summary.historyRows,
		summary.sslRows,
		summary.markCheckedErrors,
		summary.historyErrors,
		summary.sslErrors,
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
	for _, record := range records {
		addCheckOutcome(&summary, record.res, record.site.SiteStatus)
	}

	o.markResultsChecked(records, &summary)
	o.recordResultHistories(records, &summary)

	sslStart := time.Now()
	sslUpdates := make([]db.SiteSSLExpiry, 0)
	cfg := config.Get()
	for _, record := range records {
		if !fullDetectionsEnabled(cfg, record.site) {
			continue
		}
		if record.res.TLSVersion != 0 {
			o.checkTLSDeprecated(record.site, record.res)
		}
		// Update SSL expiry if available.
		if record.res.SSLExpiry != nil {
			if shouldUpdateSSLExpiry(record.site.SSLExpiryDate, *record.res.SSLExpiry) {
				sslUpdates = append(sslUpdates, db.SiteSSLExpiry{
					BlogID: record.blogID,
					Expiry: *record.res.SSLExpiry,
				})
			}
			o.checkSSLAlerts(record.site, *record.res.SSLExpiry)
		}
	}
	o.updateSSLExpiries(sslUpdates, &summary)
	summary.sslDuration += time.Since(sslStart)

	eventStart := time.Now()
	for _, record := range records {
		// Per-check data is recorded in jetmon_check_history (above); duplicating
		// it in jetmon_audit_log was retired with the operational/site-state split.
		if record.res.IsProbeSafetyBlock() {
			o.handleProbeSafetyBlock(record.site, record.res)
		} else if !record.res.IsFailure() {
			o.handleRecovery(record.site, record.res)
		} else {
			o.handleFailure(record.site, record.res)
		}
	}
	summary.eventDuration += time.Since(eventStart)
	return summary
}

func addCheckOutcome(summary *resultProcessSummary, res checker.Result, siteStatus int) {
	summary.checkCohorts = incrementCheckCohort(summary.checkCohorts, res)
	if res.Success {
		summary.checkSuccesses++
	} else {
		summary.checkFailures++
		if siteStatus == statusConfirmedDown {
			summary.checkOffline++
		}
	}

	if !res.Success && res.HTTPCode >= 400 {
		summary.checkHTTPFailures++
	}
	switch res.ErrorCode {
	case checker.ErrorTimeout:
		summary.checkTimeouts++
	case checker.ErrorConnect:
		summary.checkConnectErrors++
	case checker.ErrorSSL, checker.ErrorTLSExpired:
		summary.checkSSLErrors++
	case checker.ErrorRedirect:
		summary.checkRedirects++
	case checker.ErrorKeyword:
		summary.checkKeywords++
	case checker.ErrorTLSDeprecated:
		summary.checkTLSDeprecated++
	}
}

func incrementCheckCohort(cohorts map[checkCohortKey]int, res checker.Result) map[checkCohortKey]int {
	if cohorts == nil {
		cohorts = make(map[checkCohortKey]int)
	}
	cohorts[checkCohortForResult(res)]++
	return cohorts
}

func checkCohortForResult(res checker.Result) checkCohortKey {
	method := res.Method
	if method == "" {
		method = "unknown"
	}
	profile := res.DetectionProfile
	if profile == "" {
		profile = "unknown"
	}
	return checkCohortKey{method: method, profile: profile}
}

func mergeCheckCohorts(dst *map[checkCohortKey]int, src map[checkCohortKey]int) {
	if len(src) == 0 {
		return
	}
	if *dst == nil {
		*dst = make(map[checkCohortKey]int, len(src))
	}
	for key, count := range src {
		(*dst)[key] += count
	}
}

func emitCheckCohortCounters(m metricsClient, prefix string, cohorts map[checkCohortKey]int) {
	if m == nil || len(cohorts) == 0 {
		return
	}
	keys := make([]checkCohortKey, 0, len(cohorts))
	for key := range cohorts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].method == keys[j].method {
			return keys[i].profile < keys[j].profile
		}
		return keys[i].method < keys[j].method
	})
	for _, key := range keys {
		count := cohorts[key]
		if count <= 0 {
			continue
		}
		m.Increment(fmt.Sprintf(
			"%s.check.method.%s.profile.%s.count",
			prefix,
			metricSegment(key.method),
			metricSegment(key.profile),
		), count)
	}
}

func (o *Orchestrator) updateSSLExpiries(updates []db.SiteSSLExpiry, summary *resultProcessSummary) {
	if len(updates) == 0 {
		return
	}
	if err := dbUpdateSSLExpiries(o.ctx, updates); err != nil {
		summary.sslErrors++
		log.Printf("orchestrator: batch update ssl expiries rows=%d: %v", len(updates), err)
		for _, update := range updates {
			if err := dbUpdateSSLExpiry(o.ctx, update.BlogID, update.Expiry); err != nil {
				summary.sslErrors++
				log.Printf("orchestrator: update ssl expiry blog_id=%d: %v", update.BlogID, err)
				continue
			}
			summary.sslRows++
		}
		return
	}
	summary.sslRows += len(updates)
}

func knownSiteResults(results map[int64]checker.Result, sites map[int64]db.Site) []siteCheckResult {
	targetIDs := make([]int64, 0, len(results))
	for targetID := range results {
		targetIDs = append(targetIDs, targetID)
	}
	sort.Slice(targetIDs, func(i, j int) bool {
		return targetIDs[i] < targetIDs[j]
	})

	records := make([]siteCheckResult, 0, len(results))
	for _, targetID := range targetIDs {
		site, ok := sites[targetID]
		if !ok {
			continue
		}
		records = append(records, siteCheckResult{
			blogID: site.BlogID,
			site:   site,
			res:    results[targetID],
		})
	}
	return records
}

func (o *Orchestrator) markResultsChecked(records []siteCheckResult, summary *resultProcessSummary) {
	checks := make([]db.SiteCheck, 0, len(records))
	for _, record := range records {
		checks = append(checks, db.SiteCheck{
			BlogID:      record.blogID,
			CheckedAt:   resultCheckedAt(record.res),
			NextCheckAt: nextCheckAt(record.site, record.res),
		})
	}

	start := time.Now()
	if err := dbMarkSitesChecked(o.ctx, checks); err != nil {
		summary.markCheckedErrors++
		log.Printf("orchestrator: batch mark checked sites=%d: %v", len(checks), err)
		for _, check := range checks {
			if err := dbMarkSiteChecked(o.ctx, check.BlogID, check.CheckedAt, check.NextCheckAt); err != nil {
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
		histories = append(histories, checkHistoryRowForResult(record.blogID, record.res))
	}

	start := time.Now()
	if err := dbRecordCheckHistories(o.ctx, histories); err != nil {
		summary.historyErrors++
		log.Printf("orchestrator: batch record check history rows=%d: %v", len(histories), err)
		for _, row := range histories {
			if err := dbRecordCheckHistory(
				row.BlogID,
				row.RequestMethod,
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

func (o *Orchestrator) recordStreamingHistoryRows(rows []db.CheckHistoryRow) resultProcessSummary {
	summary := resultProcessSummary{}
	if len(rows) == 0 {
		return summary
	}

	rows = append([]db.CheckHistoryRow(nil), rows...)
	start := time.Now()
	if err := dbRecordCheckHistories(o.ctx, rows); err != nil {
		summary.historyErrors++
		log.Printf("orchestrator: streaming batch record check history rows=%d: %v", len(rows), err)
		for _, row := range rows {
			if err := dbRecordCheckHistory(
				row.BlogID,
				row.RequestMethod,
				row.HTTPCode,
				row.ErrorCode,
				row.RTTMs,
				row.DNSMs,
				row.TCPMs,
				row.TLSMs,
				row.TTFBMs,
			); err != nil {
				summary.historyErrors++
				log.Printf("orchestrator: streaming record check history blog_id=%d: %v", row.BlogID, err)
			}
		}
	}
	summary.historyDuration += time.Since(start)
	summary.historyRows += len(rows)
	return summary
}

func checkHistoryRowForResult(blogID int64, res checker.Result) db.CheckHistoryRow {
	return db.CheckHistoryRow{
		BlogID:        blogID,
		RequestMethod: res.Method,
		HTTPCode:      res.HTTPCode,
		ErrorCode:     res.ErrorCode,
		RTTMs:         res.RTT.Milliseconds(),
		DNSMs:         res.DNS.Milliseconds(),
		TCPMs:         res.TCP.Milliseconds(),
		TLSMs:         res.TLS.Milliseconds(),
		TTFBMs:        res.TTFB.Milliseconds(),
		CheckedAt:     resultCheckedAt(res),
	}
}

func resultCheckedAt(res checker.Result) time.Time {
	if res.Timestamp.IsZero() {
		return nowFunc().UTC()
	}
	return res.Timestamp.UTC()
}

func nextCheckAt(site db.Site, res checker.Result) time.Time {
	interval := siteCheckInterval(site)
	if res.IsFailure() && interval > failedCheckRetryInterval {
		interval = failedCheckRetryInterval
	}
	return resultCheckedAt(res).Add(interval)
}

func siteCheckInterval(site db.Site) time.Duration {
	interval := site.CheckInterval
	if interval < 1 {
		interval = 1
	}
	return time.Duration(interval) * time.Minute
}

func checkResultMetadata(site db.Site, res checker.Result, firstFailAt time.Time) map[string]any {
	method := res.Method
	if method == "" {
		method = effectiveCheckMethod(config.Get(), site)
	}
	profile := res.DetectionProfile
	if profile == "" {
		profile = effectiveDetectionProfile(config.Get(), site, method)
	}
	metadata := map[string]any{
		"detector_class":     detectorClass(res),
		"failure_class":      failureClass(res),
		"http_code":          res.HTTPCode,
		"error_code":         res.ErrorCode,
		"legacy_status_type": (&res).StatusType(),
		"keyword_rule":       res.KeywordRule,
		"method":             method,
		"detection_profile":  profile,
		"rtt_ms":             res.RTT.Milliseconds(),
		"url":                site.MonitorURL,
	}
	if res.ErrorDetail != "" {
		metadata["error_detail"] = res.ErrorDetail
	}
	if bodyReadMetadata := checkBodyReadMetadata(res); len(bodyReadMetadata) > 0 {
		metadata["body_read"] = bodyReadMetadata
	}
	if res.DNSFailureKind != "" {
		metadata["dns_error_kind"] = res.DNSFailureKind
		if servers := checker.ConfiguredResolverServers(); len(servers) > 0 {
			metadata["dns_resolver_source"] = "configured"
			metadata["check_dns_resolvers"] = servers
		} else {
			metadata["dns_resolver_source"] = "system"
		}
	}
	if res.DNSFailureName != "" {
		metadata["dns_error_name"] = res.DNSFailureName
	}
	if res.DNSFailureServer != "" {
		metadata["dns_error_server"] = res.DNSFailureServer
	}
	if site.RedirectPolicy != "" {
		metadata["redirect_policy"] = site.RedirectPolicy
	} else {
		metadata["redirect_policy"] = string(checker.RedirectFollow)
	}
	if res.RedirectCount > 0 {
		metadata["redirect_count"] = res.RedirectCount
	}
	if len(res.RedirectChain) > 0 {
		metadata["redirect_chain"] = append([]string(nil), res.RedirectChain...)
	}
	if res.FinalURL != "" {
		metadata["final_url"] = res.FinalURL
	}
	if res.TLSVersion != 0 {
		metadata["tls_version"] = tlsVersionName(res.TLSVersion)
		metadata["tls_version_code"] = fmt.Sprintf("0x%04x", res.TLSVersion)
	}
	if res.CipherSuite != 0 {
		metadata["cipher_suite"] = tls.CipherSuiteName(res.CipherSuite)
		metadata["cipher_suite_id"] = fmt.Sprintf("0x%04x", res.CipherSuite)
	}
	metadata["observation"] = failureObservationMetadata(site, res, firstFailAt)
	return metadata
}

func failureObservationMetadata(site db.Site, res checker.Result, firstFailAt time.Time) map[string]any {
	checkedAt := resultCheckedAt(res)
	if firstFailAt.IsZero() {
		firstFailAt = checkedAt
	}
	normalInterval := siteCheckInterval(site)
	nextInterval := nextCheckAt(site, res).Sub(checkedAt)
	obs := map[string]any{
		"checked_at":                    checkedAt.Format(time.RFC3339Nano),
		"first_failed_at":               firstFailAt.UTC().Format(time.RFC3339Nano),
		"normal_check_interval_seconds": int64(normalInterval / time.Second),
		"next_check_interval_seconds":   int64(nextInterval / time.Second),
	}
	if site.LastCheckedAt != nil {
		previousObservedAt := site.LastCheckedAt.UTC().Format(time.RFC3339Nano)
		obs["previous_observed_at"] = previousObservedAt
		if site.SiteStatus == statusRunning {
			obs["previous_known_good_at"] = previousObservedAt
		}
	}
	return obs
}

func recoveryResultMetadata(res checker.Result, changeTime time.Time) map[string]any {
	checkedAt := resultCheckedAt(res)
	method := res.Method
	if method == "" {
		method = "GET"
	}
	return map[string]any{
		"http_code":  res.HTTPCode,
		"error_code": res.ErrorCode,
		"method":     method,
		"rtt_ms":     res.RTT.Milliseconds(),
		"observation": map[string]any{
			"first_recovered_at": checkedAt.Format(time.RFC3339Nano),
			"closed_at":          changeTime.UTC().Format(time.RFC3339Nano),
		},
	}
}

func checkBodyReadMetadata(res checker.Result) map[string]any {
	if res.BodyReadMode == "" &&
		res.BodyBytesRead == 0 &&
		res.BodyReadLimitBytes == 0 &&
		res.BodyExpectedBytes <= 0 &&
		res.BodyReadError == "" {
		return nil
	}
	body := map[string]any{
		"bytes_read":  res.BodyBytesRead,
		"limit_bytes": res.BodyReadLimitBytes,
		"mode":        res.BodyReadMode,
	}
	if res.BodyExpectedBytes >= 0 {
		body["expected_bytes"] = res.BodyExpectedBytes
	}
	if res.BodyReadError != "" {
		body["error"] = res.BodyReadError
	}
	return body
}

func detectorClass(res checker.Result) string {
	switch {
	case res.Success:
		return "success"
	case res.ErrorCode == checker.ErrorBodyRead:
		return "partial_response"
	case res.ErrorCode == checker.ErrorKeyword:
		return "content_failure"
	case res.ErrorCode == checker.ErrorTimeout:
		return "timeout"
	case res.ErrorCode == checker.ErrorRedirect:
		return "redirect"
	case res.ErrorCode == checker.ErrorProbeSafety:
		return "probe_safety"
	case res.ErrorCode == checker.ErrorSSL || res.ErrorCode == checker.ErrorTLSExpired:
		return "tls_failure"
	case res.ErrorCode == checker.ErrorTLSDeprecated:
		return "tls_deprecated"
	case res.DNSFailureKind != "":
		return "dns_" + metricSegment(res.DNSFailureKind)
	case res.HTTPCode >= 400:
		return "http_failure"
	case res.ErrorCode == checker.ErrorConnect:
		return "connect_error"
	default:
		return "unknown"
	}
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
	targetID := monitorTargetID(site)
	entry := o.retries.get(targetID)
	if entry == nil && site.SiteStatus == statusRunning {
		return // was already up, nothing to do
	}

	knownEventID := int64(0)
	if entry != nil {
		knownEventID = entry.eventID
	}
	o.retries.clear(targetID)

	if site.SiteStatus != statusRunning || knownEventID > 0 {
		changeTime := nowFunc().UTC()
		config.Debugf("orchestrator: blog_id=%d recovered", site.BlogID)
		if entry != nil && site.SiteStatus == statusDown {
			emitCounter("detection.probe_cleared.count", 1)
			emitCounter("detection.probe_cleared."+failureClass(entry.lastResult)+".count", 1)
			emitTimingSince("detection.seems_down_to_probe_cleared.time", entry.firstFailAt, changeTime)
		}

		// Close the open event and project site_status back to running in the
		// same transaction. The resolution reason depends on whether the event
		// was already verifier-confirmed (Down) or still in the local-retry
		// phase (Seems Down).
		if err := o.closeRecoveredEvent(site, knownEventID, changeTime, res); err != nil {
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
		} else {
			o.auditLog(audit.Entry{
				BlogID:    site.BlogID,
				EventType: audit.EventAlertSuppressed,
				Source:    "local",
				Detail:    "recovery cooldown active",
			})
		}
		o.retries.markRecovered(targetID, changeTime)
	}
}

func (o *Orchestrator) handleFailure(site db.Site, res checker.Result) bool {
	if inMaintenance(site) {
		o.swallowMaintenanceFailure(site, res)
		return false
	}

	if suppressed, reason, window := o.postRecoveryTransientSuppression(site, res); suppressed {
		class := failureClass(res)
		emitCounter("detection.post_recovery_transient_suppressed.count", 1)
		emitCounter("detection.post_recovery_transient_suppressed."+class+".count", 1)
		metaMap := checkResultMetadata(site, res, resultCheckedAt(res))
		if reason == "false_alarm" {
			metaMap["suppressed_after_recent_false_alarm"] = true
		} else {
			metaMap["suppressed_after_recent_recovery"] = true
		}
		metaMap["suppressed_after"] = reason
		metaMap["post_recovery_window_seconds"] = int(window / time.Second)
		meta, _ := json.Marshal(metaMap)
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventType: audit.EventAlertSuppressed,
			Source:    o.hostname,
			Detail:    "post-recovery transient failure suppressed",
			Metadata:  meta,
		})
		return false
	}

	if site.SiteStatus == statusConfirmedDown {
		o.retries.clear(monitorTargetID(site))
		class := failureClass(res)
		emitCounter("detection.down.still_down.count", 1)
		emitCounter("detection.down.still_down."+class+".count", 1)
		return true
	}

	entry := o.retries.record(res)
	class := failureClass(res)
	emitCounter("detection.failure."+class+".count", 1)
	lowConfidenceDNS := lowConfidenceDNSFailure(res)

	// Open a Seems Down event on the first failure we don't already have an
	// id for. The schema's idempotent dedup_key means re-detecting the same
	// failure would update the same row, so this is also a self-healing retry
	// path if a previous Open failed to commit.
	if entry.eventID == 0 && lowConfidenceDNS {
		emitCounter("detection.low_confidence_dns.awaiting_verifier.count", 1)
		emitCounter("detection.low_confidence_dns.awaiting_verifier."+metricSegment(res.DNSFailureKind)+".count", 1)
	} else if entry.eventID == 0 {
		id, opened, err := o.openSeemsDown(site, res, entry.firstFailAt)
		if err != nil {
			log.Printf("orchestrator: open seems-down event blog_id=%d: %v", site.BlogID, err)
		} else {
			entry.eventID = id
			if opened || entry.failCount == 1 {
				emitCounter("detection.seems_down.open.count", 1)
				emitCounter("detection.seems_down.open."+class+".count", 1)
				emitTimingSince("detection.first_failure_to_seems_down.time", entry.firstFailAt, nowFunc().UTC())
			}
		}
	}

	if entry.failCount < config.Get().NumOfChecks {
		metaMap := checkResultMetadata(site, res, entry.firstFailAt)
		metaMap["attempt"] = entry.failCount
		metaMap["of"] = config.Get().NumOfChecks
		metaMap["event_id"] = entry.eventID
		if lowConfidenceDNS {
			metaMap["low_confidence_dns_failure"] = true
			metaMap["customer_visible_event_deferred_until_verifier_confirmation"] = true
		}
		meta, _ := json.Marshal(metaMap)
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventID:   entry.eventID,
			EventType: audit.EventRetryDispatched,
			Source:    o.hostname,
			Detail:    fmt.Sprintf("retry %d of %d", entry.failCount, config.Get().NumOfChecks),
			Metadata:  meta,
		})
		return entry.eventID > 0
	}

	if shouldDeferVerifierRetry(entry, resultCheckedAt(res)) {
		emitCounter("detection.verifier.deferred_retry_skipped.count", 1)
		metaMap := checkResultMetadata(site, res, entry.firstFailAt)
		metaMap["event_id"] = entry.eventID
		metaMap["attempt"] = entry.failCount
		metaMap["deferred_until"] = entry.verifierDeferredUntil.UTC().Format(time.RFC3339)
		metaMap["deferrals"] = entry.verifierDeferrals
		meta, _ := json.Marshal(metaMap)
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventID:   entry.eventID,
			EventType: audit.EventRetryDispatched,
			Source:    o.hostname,
			Detail:    "verifier retry deferred",
			Metadata:  meta,
		})
		return entry.eventID > 0
	}

	// Escalate to verifliers.
	o.escalateToVerifliers(site, entry)
	if lowConfidenceDNS {
		return entry.eventID > 0
	}
	return true
}

func (o *Orchestrator) handleProbeSafetyBlock(site db.Site, res checker.Result) {
	emitCounter("detection.probe_safety_blocked.count", 1)
	if site.ID > 0 {
		ctx := o.ctx
		if ctx == nil {
			ctx = stdctx.Background()
		}
		if err := dbUpsertSiteSafetyFlag(ctx, db.DB(), db.SiteSafetyFlag{
			BlogID:        site.BlogID,
			MonitorSiteID: site.ID,
			FlagType:      db.SiteSafetyFlagProbeSafetyBlock,
			Reason:        res.ErrorDetail,
			MonitorURL:    site.MonitorURL,
			Status:        db.SiteSafetyStatusOpen,
		}); err != nil {
			log.Printf("orchestrator: record probe safety flag blog_id=%d site_id=%d: %v", site.BlogID, site.ID, err)
		}
	}
	metaMap := checkResultMetadata(site, res, resultCheckedAt(res))
	metaMap["probe_safety_blocked"] = true
	meta, _ := json.Marshal(metaMap)
	o.auditLog(audit.Entry{
		BlogID:    site.BlogID,
		EventType: audit.EventProbeSafetyBlock,
		Source:    o.hostname,
		Detail:    "probe safety blocked outbound check",
		Metadata:  meta,
	})
}

func (o *Orchestrator) shouldSuppressPostRecoveryTransientFailure(site db.Site, res checker.Result) bool {
	suppressed, _, _ := o.postRecoveryTransientSuppression(site, res)
	return suppressed
}

func (o *Orchestrator) postRecoveryTransientSuppression(site db.Site, res checker.Result) (bool, string, time.Duration) {
	if o == nil || o.retries == nil || !postRecoveryTransientFailure(res) {
		return false, "", 0
	}
	if o.retries.get(monitorTargetID(site)) != nil {
		return false, "", 0
	}
	return postRecoveryTransientSuppression(site, res, o.retries)
}

func postRecoveryTransientSuppression(site db.Site, res checker.Result, retries *retryQueue) (bool, string, time.Duration) {
	if retries == nil || !postRecoveryTransientFailure(res) {
		return false, "", 0
	}
	targetID := monitorTargetID(site)
	if retries.get(targetID) != nil {
		return false, "", 0
	}
	checkedAt := resultCheckedAt(res)
	falseAlarmWindow := postFalseAlarmTransientFailureWindow(site)
	if retries.recentlyFalseAlarmed(targetID, checkedAt, falseAlarmWindow) {
		retries.markFalseAlarm(targetID, checkedAt)
		return true, "false_alarm", falseAlarmWindow
	}
	recoveryWindow := postRecoveryTransientFailureWindow(site)
	if retries.recentlyRecovered(targetID, checkedAt, recoveryWindow) {
		return true, "recovery", recoveryWindow
	}
	return false, "", 0
}

func postRecoveryTransientFailure(res checker.Result) bool {
	if !res.IsFailure() || res.HTTPCode > 0 {
		return false
	}
	return res.ErrorCode == checker.ErrorConnect || res.ErrorCode == checker.ErrorTimeout
}

func lowConfidenceDNSFailure(res checker.Result) bool {
	return postRecoveryTransientFailure(res) && res.DNSFailureKind != ""
}

func postRecoveryTransientFailureWindow(site db.Site) time.Duration {
	window := siteCheckInterval(site)
	if window < failedCheckRetryInterval {
		return failedCheckRetryInterval
	}
	if window > maxPostRecoveryTransientFailureWindow {
		return maxPostRecoveryTransientFailureWindow
	}
	return window
}

func postFalseAlarmTransientFailureWindow(site db.Site) time.Duration {
	window := siteCheckInterval(site) * 2
	if window < minPostFalseAlarmTransientFailureWindow {
		return minPostFalseAlarmTransientFailureWindow
	}
	if window > maxPostFalseAlarmTransientFailureWindow {
		return maxPostFalseAlarmTransientFailureWindow
	}
	return window
}

func (o *Orchestrator) escalateToVerifliers(site db.Site, entry *retryEntry) {
	allClients := o.veriflierSnapshot()
	emitCounter("detection.verifier.escalation.count", 1)
	emitTimingSince("detection.first_failure_to_verification.time", entry.firstFailAt, nowFunc().UTC())
	if len(allClients) == 0 {
		emitCounter("detection.verifier.no_clients.count", 1)
		o.confirmDown(site, entry, nil)
		return
	}
	clients, cooldownSkipped := o.availableVeriflierClients(allClients, nowFunc().UTC())
	if cooldownSkipped > 0 {
		emitCounter("detection.verifier.cooldown_skipped.count", cooldownSkipped)
		emitGauge("detection.verifier.cooldown_skipped_active.count", cooldownSkipped)
	}
	if len(clients) == 0 {
		minHealthy := verifierMinHealthyFloor(config.Get().PeerOfflineLimit, len(allClients))
		now := nowFunc().UTC()
		delay := verifierOperationalBackoff(site, entry.verifierDeferrals)
		entry.verifierDeferrals++
		entry.verifierDeferredUntil = now.Add(delay)
		emitCounter("detection.verifier.deferred.count", 1)
		emitCounter("detection.verifier.insufficient_healthy.count", 1)
		emitGauge("detection.verifier.healthy.count", 0)
		emitGauge("detection.verifier.confirmations.count", 0)
		emitGauge("detection.verifier.quorum.count", minHealthy)
		emitGauge("detection.verifier.min_healthy.count", minHealthy)
		meta, _ := json.Marshal(map[string]any{
			"event_id":                    entry.eventID,
			"verifier_configured":         len(allClients),
			"verifier_available":          0,
			"verifier_cooldown_skipped":   cooldownSkipped,
			"verifier_min_healthy":        minHealthy,
			"deferrals":                   entry.verifierDeferrals,
			"retry_after_seconds":         int(delay / time.Second),
			"deferred_until":              entry.verifierDeferredUntil.UTC().Format(time.RFC3339),
			"all_verifiers_in_cooldown":   true,
			"site_check_interval_seconds": int(siteCheckInterval(site) / time.Second),
		})
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventID:   entry.eventID,
			EventType: audit.EventRetryDispatched,
			Source:    o.hostname,
			Detail:    "verifier decision deferred",
			Metadata:  meta,
		})
		log.Printf(
			"orchestrator: blog_id=%d verifier decision pending; all configured verifliers are in operational cooldown retry_after=%s",
			site.BlogID,
			delay,
		)
		return
	}

	cfg := config.Get()
	method := effectiveCheckMethod(cfg, site)
	profile := effectiveDetectionProfile(cfg, site, method)
	req := veriflier.CheckRequest{
		MonitorSiteID:       site.ID,
		BlogID:              site.BlogID,
		URL:                 site.MonitorURL,
		Method:              method,
		DetectionProfile:    profile,
		TimeoutSeconds:      int32(timeoutForSite(cfg, site)),
		BodyReadMaxBytes:    cfg.BodyReadMaxBytes,
		BodyReadMaxMS:       int32(cfg.BodyReadMaxMS),
		KeywordReadMaxBytes: cfg.KeywordReadMaxBytes,
		KeywordReadMaxMS:    int32(cfg.KeywordReadMaxMS),
		CustomHeaders:       checker.ParseCustomHeaders(site.CustomHeaders),
		RedirectPolicy:      string(checker.RedirectFollow),
		RequestID:           veriflier.NewRequestID(),
	}
	if profile == checkmode.ProfileFull {
		req.Keyword = stringPtrValue(site.CheckKeyword)
		req.ForbiddenKeyword = stringPtrValue(site.ForbiddenKeyword)
		req.ForbiddenKeywords = checker.ParseForbiddenKeywords(site.ForbiddenKeywords)
		req.RedirectPolicy = site.RedirectPolicy
		if req.RedirectPolicy == "" {
			req.RedirectPolicy = string(checker.RedirectFollow)
		}
	}

	escalateMeta, _ := json.Marshal(map[string]any{
		"verifier_count":            len(clients),
		"verifier_configured_count": len(allClients),
		"verifier_cooldown_skipped": cooldownSkipped,
		"request_id":                req.RequestID,
		"method":                    method,
		"detection_profile":         profile,
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
	seenVoteIDs := make(map[string]struct{}, len(clients))
	healthyVerifliers := 0
	confirmations := 0
	duplicateVotes := 0

	for range clients {
		vr := <-ch
		emitTiming("verifier.rpc.duration", vr.duration)
		hostSegment := metricSegment(vr.host)
		emitTiming("verifier.host."+hostSegment+".rpc.duration", vr.duration)
		if vr.err != nil {
			emitCounter("verifier.rpc.error.count", 1)
			emitCounter("verifier.host."+hostSegment+".rpc.error.count", 1)
			delay := o.markVeriflierOperationalFailure(vr.host, nowFunc().UTC())
			if delay > 0 {
				emitCounter("verifier.host."+hostSegment+".cooldown.count", 1)
				emitTiming("verifier.host."+hostSegment+".cooldown.duration", delay)
			}
			log.Printf("orchestrator: veriflier %s error: %v", vr.host, vr.err)
			continue
		}
		if vr.res == nil {
			emitCounter("verifier.rpc.error.count", 1)
			emitCounter("verifier.host."+hostSegment+".rpc.error.count", 1)
			delay := o.markVeriflierOperationalFailure(vr.host, nowFunc().UTC())
			if delay > 0 {
				emitCounter("verifier.host."+hostSegment+".cooldown.count", 1)
				emitTiming("verifier.host."+hostSegment+".cooldown.duration", delay)
			}
			log.Printf("orchestrator: veriflier %s returned no result", vr.host)
			continue
		}

		emitCounter("verifier.rpc.success.count", 1)
		emitCounter("verifier.host."+hostSegment+".rpc.success.count", 1)
		voteID := verifierVoteID(vr.host, vr.res)
		_, duplicateVote := seenVoteIDs[voteID]
		if duplicateVote {
			duplicateVotes++
			emitCounter("verifier.vote.duplicate_identity.count", 1)
			emitCounter("verifier.host."+hostSegment+".vote.duplicate_identity.count", 1)
			log.Printf("orchestrator: veriflier %s returned duplicate vote identity %q; ignoring duplicate vote", vr.host, voteID)
		} else {
			seenVoteIDs[voteID] = struct{}{}
		}
		vr.res.Host = voteID

		// Verifier reply is operational telemetry — recorded under
		// EventVeriflierSent with the response in metadata. The site-state
		// outcome (confirm or false alarm) is captured separately, ultimately
		// as a transition row in jetmon_event_transitions.
		metaMap := map[string]any{
			"http_code":      vr.res.HTTPCode,
			"error_code":     vr.res.ErrorCode,
			"rtt_ms":         vr.res.RTTMs,
			"success":        vr.res.Success,
			"request_id":     vr.res.RequestID,
			"vote_id":        voteID,
			"duplicate_vote": duplicateVote,
		}
		if vr.res.VantageID != "" {
			metaMap["vantage_id"] = vr.res.VantageID
		}
		if vr.res.AgentID != "" {
			metaMap["agent_id"] = vr.res.AgentID
		}
		if vr.res.Outcome != "" {
			metaMap["outcome"] = vr.res.Outcome
		}
		meta, _ := json.Marshal(metaMap)
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventType: audit.EventVeriflierSent,
			Source:    vr.host,
			Detail:    "veriflier reply",
			Metadata:  meta,
		})
		if duplicateVote {
			continue
		}
		if verifierOperationalNonVote(vr.res) {
			emitCounter("verifier.vote.non_vote.count", 1)
			emitCounter("verifier.host."+hostSegment+".vote.non_vote.count", 1)
			delay := o.markVeriflierOperationalFailure(vr.host, nowFunc().UTC())
			if delay > 0 {
				emitCounter("verifier.host."+hostSegment+".cooldown.count", 1)
				emitTiming("verifier.host."+hostSegment+".cooldown.duration", delay)
			}
			log.Printf("orchestrator: veriflier %s returned operational non-vote outcome %q; leaving decision pending", vr.host, vr.res.Outcome)
			continue
		}

		o.markVeriflierHealthy(vr.host)
		healthyVerifliers++
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

	// Adjust quorum to healthy unique verifier vote identities. In a
	// multi-verifier fleet, avoid letting a degraded verifier set collapse
	// to one confirming vote unless operators intentionally configured a
	// one-vote quorum.
	quorum := config.Get().PeerOfflineLimit
	if healthyVerifliers < quorum {
		quorum = healthyVerifliers
	}
	if quorum < 1 {
		quorum = 1
	}
	minHealthy := verifierMinHealthyFloor(config.Get().PeerOfflineLimit, len(allClients))
	if quorum < minHealthy {
		quorum = minHealthy
	}
	emitGauge("detection.verifier.healthy.count", healthyVerifliers)
	emitGauge("detection.verifier.confirmations.count", confirmations)
	emitGauge("detection.verifier.quorum.count", quorum)
	emitGauge("detection.verifier.min_healthy.count", minHealthy)
	emitGauge("detection.verifier.duplicate_votes.count", duplicateVotes)
	decision := verifierDecision{
		Quorum:         quorum,
		MinHealthy:     minHealthy,
		Healthy:        healthyVerifliers,
		Confirmed:      confirmations,
		Disagreed:      healthyVerifliers - confirmations,
		DuplicateVotes: duplicateVotes,
	}

	if healthyVerifliers < minHealthy {
		now := nowFunc().UTC()
		delay := verifierOperationalBackoff(site, entry.verifierDeferrals)
		entry.verifierDeferrals++
		entry.verifierDeferredUntil = now.Add(delay)
		emitCounter("detection.verifier.deferred.count", 1)
		emitGauge("detection.verifier.deferrals.count", entry.verifierDeferrals)
		meta, _ := json.Marshal(map[string]any{
			"event_id":                 entry.eventID,
			"verifier_quorum":          quorum,
			"verifier_min_healthy":     minHealthy,
			"verifier_healthy":         healthyVerifliers,
			"verifier_confirmed":       confirmations,
			"verifier_disagreed":       healthyVerifliers - confirmations,
			"verifier_duplicate_votes": duplicateVotes,
			"deferrals":                entry.verifierDeferrals,
			"retry_after_seconds":      int(delay / time.Second),
			"deferred_until":           entry.verifierDeferredUntil.UTC().Format(time.RFC3339),
			"verifier_results":         summarizeVerifierResults(vResults),
		})
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventID:   entry.eventID,
			EventType: audit.EventRetryDispatched,
			Source:    o.hostname,
			Detail:    "verifier decision deferred",
			Metadata:  meta,
		})
		emitCounter("detection.verifier.insufficient_healthy.count", 1)
		log.Printf(
			"orchestrator: blog_id=%d verifier decision pending; healthy=%d min_healthy=%d confirmations=%d retry_after=%s",
			site.BlogID,
			healthyVerifliers,
			minHealthy,
			confirmations,
			delay,
		)
		return
	}

	if confirmations >= quorum {
		emitCounter("detection.verifier.quorum_met.count", 1)
		o.confirmDown(site, entry, vResults, decision)
	} else {
		// Verifliers did not confirm — false positive. Close the Seems Down
		// event with reason=false_alarm and reset site_status in the same tx.
		config.Debugf("orchestrator: blog_id=%d verifliers did not confirm down (%d/%d)", site.BlogID, confirmations, quorum)
		falseAlarmAt := nowFunc().UTC()
		emitCounter("detection.verifier.false_alarm.count", 1)
		emitCounter("detection.verifier.false_alarm."+failureClass(entry.lastResult)+".count", 1)
		emitTimingSince("detection.seems_down_to_false_alarm.time", entry.firstFailAt, falseAlarmAt)
		_ = dbRecordFalsePositive(site.BlogID, entry.lastResult.HTTPCode, entry.lastResult.ErrorCode,
			entry.lastResult.RTT.Milliseconds())

		if entry.eventID > 0 {
			meta, _ := json.Marshal(map[string]any{
				"verifier_quorum":          quorum,
				"verifier_min_healthy":     minHealthy,
				"verifier_healthy":         healthyVerifliers,
				"verifier_disagreed":       healthyVerifliers - confirmations,
				"verifier_confirmed":       confirmations,
				"verifier_duplicate_votes": duplicateVotes,
				"verifier_results":         summarizeVerifierResults(vResults),
			})
			if err := o.closeEvent(site, entry.eventID,
				eventstore.ReasonFalseAlarm, statusRunning, falseAlarmAt, meta); err != nil {
				log.Printf("orchestrator: close false-alarm event blog_id=%d event_id=%d: %v",
					site.BlogID, entry.eventID, err)
			}
		}
		targetID := monitorTargetID(site)
		o.retries.clear(targetID)
		o.retries.markFalseAlarm(targetID, falseAlarmAt)
	}
}

type verifierDecision struct {
	Quorum         int
	MinHealthy     int
	Healthy        int
	Confirmed      int
	Disagreed      int
	DuplicateVotes int
}

type verifierCooldown struct {
	until    time.Time
	failures int
}

func verifierVoteID(addr string, res *veriflier.CheckResult) string {
	if res != nil {
		if vantageID := strings.TrimSpace(res.VantageID); vantageID != "" {
			return vantageID
		}
		if host := strings.TrimSpace(res.Host); host != "" {
			return host
		}
	}
	return strings.TrimSpace(addr)
}

func verifierOperationalNonVote(res *veriflier.CheckResult) bool {
	if res == nil {
		return true
	}
	switch res.Outcome {
	case veriflier.OutcomeAgentOverloaded, veriflier.OutcomeUnknown:
		return true
	default:
		return false
	}
}

func shouldDeferVerifierRetry(entry *retryEntry, checkedAt time.Time) bool {
	if entry == nil || entry.verifierDeferredUntil.IsZero() {
		return false
	}
	if checkedAt.IsZero() {
		checkedAt = nowFunc().UTC()
	}
	return checkedAt.UTC().Before(entry.verifierDeferredUntil.UTC())
}

func verifierOperationalBackoff(site db.Site, deferrals int) time.Duration {
	if deferrals < 0 {
		deferrals = 0
	}
	delay := verifierOperationalBackoffBase
	for range deferrals {
		if delay >= verifierOperationalBackoffMax {
			break
		}
		delay *= 2
	}
	if delay > verifierOperationalBackoffMax {
		delay = verifierOperationalBackoffMax
	}
	interval := siteCheckInterval(site)
	if interval > 0 && delay > interval {
		return interval
	}
	return delay
}

func verifierMinHealthyFloor(peerOfflineLimit, configuredVerifiers int) int {
	if configuredVerifiers <= 0 {
		return 0
	}
	if configuredVerifiers == 1 || peerOfflineLimit <= 1 {
		return 1
	}
	return 2
}

func inferredVerifierDecision(vResults []veriflier.CheckResult) verifierDecision {
	decision := verifierDecision{
		Quorum:     len(vResults),
		MinHealthy: 1,
		Healthy:    len(vResults),
	}
	if len(vResults) == 0 {
		decision.MinHealthy = 0
	}
	for _, vr := range vResults {
		if vr.Success {
			decision.Disagreed++
			continue
		}
		decision.Confirmed++
	}
	return decision
}

func (o *Orchestrator) confirmDown(site db.Site, entry *retryEntry, vResults []veriflier.CheckResult, decisions ...verifierDecision) {
	if inMaintenance(site) {
		if entry != nil {
			o.swallowMaintenanceFailure(site, entry.lastResult)
		}
		return
	}
	if entry == nil {
		config.Debugf("orchestrator: confirmed down blog_id=%d without retry entry", site.BlogID)
		return
	}

	newStatus := statusConfirmedDown
	changeTime := nowFunc().UTC()
	emitCounter("detection.down.confirmed.count", 1)
	emitCounter("detection.down.confirmed."+failureClass(entry.lastResult)+".count", 1)
	emitTimingSince("detection.seems_down_to_down.time", entry.firstFailAt, changeTime)

	config.Debugf("orchestrator: blog_id=%d confirmed down", site.BlogID)

	meta := confirmedDownMetadata(site, entry, vResults, entry.eventID == 0, decisions...)

	// Promote the open Seems Down event to Down with reason=verifier_confirmed
	// and project site_status=SITE_CONFIRMED_DOWN in the same tx. Low-confidence
	// local DNS failures intentionally skip the Seems Down event, so verifier
	// confirmation opens the customer-visible incident directly as Down.
	if entry.eventID > 0 {
		if err := o.promoteToDown(site, entry.eventID, changeTime, meta); err != nil {
			log.Printf("orchestrator: promote event blog_id=%d event_id=%d: %v", site.BlogID, entry.eventID, err)
		}
	} else {
		eventID, opened, err := o.openConfirmedDown(site, changeTime, meta)
		if err != nil {
			log.Printf("orchestrator: open confirmed-down event blog_id=%d: %v", site.BlogID, err)
			if config.LegacyStatusProjectionEnabled() {
				_ = db.UpdateSiteStatusForMonitorSite(o.ctx, site.ID, site.BlogID, newStatus, changeTime)
			}
		} else {
			entry.eventID = eventID
			if opened {
				emitCounter("detection.down.open_after_verifier.count", 1)
			}
		}
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

	o.retries.clear(monitorTargetID(site))
}

func confirmedDownMetadata(site db.Site, entry *retryEntry, vResults []veriflier.CheckResult, directOpen bool, decisions ...verifierDecision) json.RawMessage {
	decision := inferredVerifierDecision(vResults)
	if len(decisions) > 0 {
		decision = decisions[0]
	}
	metaMap := checkResultMetadata(site, entry.lastResult, entry.firstFailAt)
	metaMap["verifier_results"] = summarizeVerifierResults(vResults)
	metaMap["verifier_quorum"] = decision.Quorum
	metaMap["verifier_min_healthy"] = decision.MinHealthy
	metaMap["verifier_healthy"] = decision.Healthy
	metaMap["verifier_disagreed"] = decision.Disagreed
	metaMap["verifier_confirmed"] = decision.Confirmed
	metaMap["verifier_duplicate_votes"] = decision.DuplicateVotes
	if directOpen {
		metaMap["opened_after_verifier_confirmation"] = true
	}
	if lowConfidenceDNSFailure(entry.lastResult) {
		metaMap["low_confidence_dns_failure"] = true
		metaMap["customer_visible_event_deferred_until_verifier_confirmation"] = true
	}
	meta, _ := json.Marshal(metaMap)
	return meta
}

func verifierConfirmationCount(vResults []veriflier.CheckResult) int {
	count := 0
	for _, res := range vResults {
		if !res.Success {
			count++
		}
	}
	return count
}

func (o *Orchestrator) swallowMaintenanceFailure(site db.Site, res checker.Result) {
	targetID := monitorTargetID(site)
	entry := o.retries.get(targetID)
	knownEventID := int64(0)
	if entry != nil {
		knownEventID = entry.eventID
	}

	class := failureClass(res)
	emitCounter("detection.maintenance.swallowed.count", 1)
	emitCounter("detection.maintenance.swallowed."+class+".count", 1)

	meta, _ := json.Marshal(map[string]any{
		"http_code":         res.HTTPCode,
		"error_code":        res.ErrorCode,
		"rtt_ms":            res.RTT.Milliseconds(),
		"maintenance_start": site.MaintenanceStart,
		"maintenance_end":   site.MaintenanceEnd,
		"event_id":          knownEventID,
	})

	if entry != nil || site.SiteStatus != statusRunning {
		if err := o.closeMaintenanceEvent(site, knownEventID, nowFunc().UTC(), meta); err != nil {
			log.Printf("orchestrator: close maintenance-swallowed event blog_id=%d event_id=%d: %v",
				site.BlogID, knownEventID, err)
		}
	}
	o.retries.clear(targetID)

	o.auditLog(audit.Entry{
		BlogID:    site.BlogID,
		EventID:   knownEventID,
		EventType: audit.EventMaintenanceActive,
		Source:    "local",
		Detail:    "failure swallowed during maintenance",
		Metadata:  meta,
	})
}

func (o *Orchestrator) sendNotification(site db.Site, res checker.Result, status int, changeTime time.Time, vResults []veriflier.CheckResult) {
	if !config.WPCOMNotifyEnabled() {
		emitCounter("wpcom.notification.disabled.count", 1)
		o.wpcomNotifyDisabledLogOnce.Do(func() {
			log.Print("orchestrator: wpcom notification disabled; skipping legacy status-change notifications")
		})
		return
	}

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
		log.Printf("orchestrator: wpcom notify failed for blog_id=%d: %v", site.BlogID, err)

		if errors.Is(err, wpcom.ErrCircuitOpen) {
			emitCounter("wpcom.notification.queued.count", 1)
			emitCounter("wpcom.notification.status."+wpcomStatus+".queued.count", 1)
			return
		}

		if wpcom.IsPermanentStatusError(err) {
			emitCounter("wpcom.notification.permanent_failure.count", 1)
			emitCounter("wpcom.notification.status."+wpcomStatus+".permanent_failure.count", 1)
			if statusCode, ok := wpcom.HTTPStatusCode(err); ok {
				emitCounter(fmt.Sprintf("wpcom.notification.http.%d.permanent_failure.count", statusCode), 1)
			}
			emitCounter("wpcom.notification.failed.count", 1)
			emitCounter("wpcom.notification.status."+wpcomStatus+".failed.count", 1)
			o.logWPCOMPermanentFailure(site.BlogID, err)
			o.auditLog(audit.Entry{
				BlogID:    site.BlogID,
				EventType: audit.EventWPCOMFailure,
				Source:    "local",
				Detail:    err.Error(),
			})
			return
		}

		emitCounter("wpcom.notification.retry.count", 1)
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
			o.auditLog(audit.Entry{
				BlogID:    site.BlogID,
				EventType: audit.EventWPCOMFailure,
				Source:    "local",
				Detail:    retryErr.Error(),
			})
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

func (o *Orchestrator) logWPCOMPermanentFailure(blogID int64, err error) {
	if o == nil {
		log.Printf("orchestrator: wpcom notify permanent failure for blog_id=%d: %v", blogID, err)
		return
	}
	o.wpcomPermanentMu.Lock()
	defer o.wpcomPermanentMu.Unlock()

	now := nowFunc()
	if o.wpcomPermanentLastLog.IsZero() || now.Sub(o.wpcomPermanentLastLog) >= wpcomPermanentFailureLogInterval {
		if o.wpcomPermanentSuppressed > 0 {
			log.Printf(
				"orchestrator: wpcom notify permanent failure for blog_id=%d: %v (suppressed %d similar permanent failures)",
				blogID,
				err,
				o.wpcomPermanentSuppressed,
			)
		} else {
			log.Printf("orchestrator: wpcom notify permanent failure for blog_id=%d: %v", blogID, err)
		}
		o.wpcomPermanentLastLog = now
		o.wpcomPermanentSuppressed = 0
		return
	}
	o.wpcomPermanentSuppressed++
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
//   - >  30 days → close any open event with reason=probe_cleared
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
	config.Debugf("orchestrator: blog_id=%d SSL cert expires in %d days (severity %d)", site.BlogID, daysUntil, severity)
}

// openOrUpdateSSLExpiry opens a tls_expiry event for the site if none exists,
// or escalates / de-escalates the existing event's severity if a threshold has
// been crossed. site_status is intentionally not projected — TLS expiry
// warnings don't affect the Up/Down state of the site (Layer 2 issue, not a
// Layer 4 outage).
func (o *Orchestrator) openOrUpdateSSLExpiry(blogID int64, severity uint8, state string, daysUntil int, meta json.RawMessage) error {
	return o.withEventMutationRetry(blogID, "open_update_ssl_expiry", func() error {
		return o.openOrUpdateSSLExpiryOnce(blogID, severity, state, daysUntil, meta)
	})
}

func (o *Orchestrator) openOrUpdateSSLExpiryOnce(blogID int64, severity uint8, state string, daysUntil int, meta json.RawMessage) error {
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
	return o.withEventMutationRetry(blogID, "close_ssl_expiry", func() error {
		return o.closeSSLExpiryIfOpenOnce(blogID)
	})
}

func (o *Orchestrator) closeSSLExpiryIfOpenOnce(blogID int64) error {
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
	if err := tx.Close(o.ctx, ae.ID, eventstore.ReasonProbeCleared, o.hostname, nil); err != nil {
		return fmt.Errorf("close tls_expiry: %w", err)
	}
	return tx.Commit()
}

func (o *Orchestrator) checkTLSDeprecated(site db.Site, res checker.Result) {
	if res.TLSVersion <= tls.VersionTLS11 {
		meta, _ := json.Marshal(map[string]any{
			"tls_version":      tlsVersionName(res.TLSVersion),
			"tls_version_code": fmt.Sprintf("0x%04x", res.TLSVersion),
			"cipher_suite":     tls.CipherSuiteName(res.CipherSuite),
			"cipher_suite_id":  fmt.Sprintf("0x%04x", res.CipherSuite),
		})
		if err := o.openTLSDeprecated(site.BlogID, meta); err != nil {
			log.Printf("orchestrator: tls_deprecated event blog_id=%d version=%s: %v",
				site.BlogID, tlsVersionName(res.TLSVersion), err)
		}
		return
	}

	if err := o.closeTLSDeprecatedIfOpen(site.BlogID); err != nil {
		log.Printf("orchestrator: close tls_deprecated event blog_id=%d: %v", site.BlogID, err)
	}
}

func (o *Orchestrator) openTLSDeprecated(blogID int64, meta json.RawMessage) error {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Open(o.ctx, eventstore.OpenInput{
		Identity: eventstore.Identity{BlogID: blogID, CheckType: checkTypeTLSDeprecated},
		Severity: eventstore.SeverityWarning,
		State:    eventstore.StateWarning,
		Source:   o.hostname,
		Metadata: meta,
	}); err != nil {
		return fmt.Errorf("open tls_deprecated: %w", err)
	}
	return tx.Commit()
}

func (o *Orchestrator) closeTLSDeprecatedIfOpen(blogID int64) error {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if tx.Tx() == nil {
		return tx.Commit()
	}
	ae, err := tx.FindActiveByBlog(o.ctx, blogID, checkTypeTLSDeprecated)
	if err != nil {
		if errors.Is(err, eventstore.ErrEventNotFound) {
			return tx.Commit()
		}
		return err
	}
	if err := tx.Close(o.ctx, ae.ID, eventstore.ReasonProbeCleared, o.hostname, nil); err != nil {
		return fmt.Errorf("close tls_deprecated: %w", err)
	}
	return tx.Commit()
}

func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("0x%04x", version)
	}
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

func (o *Orchestrator) usesDynamicBuckets(cfg *config.Config) bool {
	return cfg.RolloutMode != config.RolloutModeStandby &&
		cfg.RolloutMode != config.RolloutModeAPIControlled &&
		!o.usesPinnedBuckets(cfg)
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

func (o *Orchestrator) withEventMutationRetry(blogID int64, operation string, fn func() error) error {
	ctx := o.ctx
	if ctx == nil {
		ctx = stdctx.Background()
	}

	var lastErr error
	for attempt := 1; attempt <= eventMutationMaxAttempts; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryableMySQLError(err) || attempt == eventMutationMaxAttempts {
			return err
		}
		emitCounter("eventstore.mutation.retry.count", 1)
		emitCounter("eventstore.mutation."+metricSegment(operation)+".retry.count", 1)
		wait := time.Duration(attempt) * eventMutationRetryBaseDelay
		log.Printf("orchestrator: retrying event mutation blog_id=%d operation=%s attempt=%d/%d wait=%s err=%v",
			blogID, operation, attempt+1, eventMutationMaxAttempts, wait, err)
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return lastErr
}

func isRetryableMySQLError(err error) bool {
	mysqlErr, ok := errors.AsType[*mysql.MySQLError](err)
	if !ok {
		return false
	}
	switch mysqlErr.Number {
	case 1205, 1213:
		return true
	default:
		return false
	}
}

// openSeemsDown opens (or re-detects) a Seems Down event for an HTTP-failing
// site and projects v1 site_status=SITE_DOWN in the same transaction. Returns
// the event id. Idempotent: a re-detection of the same identity returns the
// existing event's id with no transition row written and no projection update.
func (o *Orchestrator) openSeemsDown(site db.Site, res checker.Result, firstFailAt time.Time) (int64, bool, error) {
	var eventID int64
	var opened bool
	err := o.withEventMutationRetry(site.BlogID, "open_seems_down", func() error {
		id, didOpen, err := o.openSeemsDownOnce(site, res, firstFailAt)
		if err != nil {
			return err
		}
		eventID = id
		opened = didOpen
		return nil
	})
	return eventID, opened, err
}

func (o *Orchestrator) openSeemsDownOnce(site db.Site, res checker.Result, firstFailAt time.Time) (int64, bool, error) {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()

	meta, _ := json.Marshal(checkResultMetadata(site, res, firstFailAt))

	out, err := tx.Open(o.ctx, eventstore.OpenInput{
		Identity: httpEventIdentity(site),
		Severity: eventstore.SeveritySeemsDown,
		State:    eventstore.StateSeemsDown,
		Source:   o.hostname,
		Metadata: meta,
	})
	if err != nil {
		return 0, false, err
	}

	// Project v1 site_status=SITE_DOWN only on the actual insert. A re-detection
	// (Opened=false) is by definition a row that already exists, so site_status
	// was already projected when the event first opened.
	if out.Opened && config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
		if err := db.UpdateSiteStatusTxForMonitorSite(o.ctx, tx.Tx(), site.ID, site.BlogID, statusDown, nowFunc().UTC()); err != nil {
			return 0, false, fmt.Errorf("project site_status: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit: %w", err)
	}
	return out.EventID, out.Opened, nil
}

// openConfirmedDown opens a Down event directly for failures that were kept
// out of the customer-visible Seems Down state until verifier confirmation.
func (o *Orchestrator) openConfirmedDown(site db.Site, changeTime time.Time, meta json.RawMessage) (int64, bool, error) {
	var eventID int64
	var opened bool
	err := o.withEventMutationRetry(site.BlogID, "open_confirmed_down", func() error {
		id, didOpen, err := o.openConfirmedDownOnce(site, changeTime, meta)
		if err != nil {
			return err
		}
		eventID = id
		opened = didOpen
		return nil
	})
	return eventID, opened, err
}

func (o *Orchestrator) openConfirmedDownOnce(site db.Site, changeTime time.Time, meta json.RawMessage) (int64, bool, error) {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()

	out, err := tx.Open(o.ctx, eventstore.OpenInput{
		Identity: httpEventIdentity(site),
		Severity: eventstore.SeverityDown,
		State:    eventstore.StateDown,
		Source:   o.hostname,
		Metadata: meta,
	})
	if err != nil {
		return 0, false, err
	}

	projectConfirmedDown := out.Opened
	if !out.Opened && (out.CurrentSeverity != eventstore.SeverityDown || out.CurrentState != eventstore.StateDown) {
		if _, err := tx.Promote(o.ctx, out.EventID,
			eventstore.SeverityDown, eventstore.StateDown,
			eventstore.ReasonVerifierConfirmed, o.hostname, meta); err != nil {
			return 0, false, fmt.Errorf("promote existing event: %w", err)
		}
		projectConfirmedDown = true
	}

	if projectConfirmedDown && config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
		if err := db.UpdateSiteStatusTxForMonitorSite(o.ctx, tx.Tx(), site.ID, site.BlogID, statusConfirmedDown, changeTime); err != nil {
			return 0, false, fmt.Errorf("project site_status: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit: %w", err)
	}
	return out.EventID, out.Opened, nil
}

// promoteToDown bumps an open Seems Down event to Down (severity 4) and
// projects site_status=SITE_CONFIRMED_DOWN in the same transaction.
func (o *Orchestrator) promoteToDown(site db.Site, eventID int64, changeTime time.Time, meta json.RawMessage) error {
	return o.withEventMutationRetry(site.BlogID, "promote_to_down", func() error {
		return o.promoteToDownOnce(site, eventID, changeTime, meta)
	})
}

func (o *Orchestrator) promoteToDownOnce(site db.Site, eventID int64, changeTime time.Time, meta json.RawMessage) error {
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
		if err := db.UpdateSiteStatusTxForMonitorSite(o.ctx, tx.Tx(), site.ID, site.BlogID, statusConfirmedDown, changeTime); err != nil {
			return fmt.Errorf("project site_status: %w", err)
		}
	}
	return tx.Commit()
}

// closeEvent closes an open event with the given resolution reason and projects
// site_status to the given v1 value in the same transaction.
func (o *Orchestrator) closeEvent(site db.Site, eventID int64, reason string, projectedStatus int, changeTime time.Time, meta json.RawMessage) error {
	return o.withEventMutationRetry(site.BlogID, "close_event", func() error {
		return o.closeEventOnce(site, eventID, reason, projectedStatus, changeTime, meta)
	})
}

func (o *Orchestrator) closeEventOnce(site db.Site, eventID int64, reason string, projectedStatus int, changeTime time.Time, meta json.RawMessage) error {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.Close(o.ctx, eventID, reason, o.hostname, meta); err != nil {
		return fmt.Errorf("close event: %w", err)
	}

	if config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
		if err := db.UpdateSiteStatusTxForMonitorSite(o.ctx, tx.Tx(), site.ID, site.BlogID, projectedStatus, changeTime); err != nil {
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
func (o *Orchestrator) closeRecoveredEvent(site db.Site, knownEventID int64, changeTime time.Time, res checker.Result) error {
	return o.withEventMutationRetry(site.BlogID, "close_recovered_event", func() error {
		return o.closeRecoveredEventOnce(site, knownEventID, changeTime, res)
	})
}

func (o *Orchestrator) closeRecoveredEventOnce(site db.Site, knownEventID int64, changeTime time.Time, res checker.Result) error {
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
		ae, err := tx.FindActive(o.ctx, httpEventIdentity(site))
		if err != nil {
			if errors.Is(err, eventstore.ErrEventNotFound) {
				// site_status disagreed with the event store (no open event but
				// projection said non-running). Just project back to running.
				if config.LegacyStatusProjectionEnabled() {
					if err := db.UpdateSiteStatusTxForMonitorSite(o.ctx, tx.Tx(), site.ID, site.BlogID, statusRunning, changeTime); err != nil {
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

	meta, _ := json.Marshal(recoveryResultMetadata(res, changeTime))
	if err := tx.Close(o.ctx, eventID, reason, o.hostname, meta); err != nil {
		return fmt.Errorf("close event: %w", err)
	}
	if config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
		if err := db.UpdateSiteStatusTxForMonitorSite(o.ctx, tx.Tx(), site.ID, site.BlogID, statusRunning, changeTime); err != nil {
			return fmt.Errorf("project site_status: %w", err)
		}
	}
	return tx.Commit()
}

func (o *Orchestrator) closeMaintenanceEvent(site db.Site, knownEventID int64, changeTime time.Time, meta json.RawMessage) error {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var eventID int64
	switch {
	case knownEventID > 0 && tx.Tx() != nil:
		eventID = knownEventID
	case tx.Tx() != nil:
		ae, err := tx.FindActive(o.ctx, httpEventIdentity(site))
		if err != nil {
			if errors.Is(err, eventstore.ErrEventNotFound) {
				if config.LegacyStatusProjectionEnabled() {
					if err := db.UpdateSiteStatusTxForMonitorSite(o.ctx, tx.Tx(), site.ID, site.BlogID, statusRunning, changeTime); err != nil {
						return fmt.Errorf("project site_status: %w", err)
					}
				}
				return tx.Commit()
			}
			return err
		}
		eventID = ae.ID
	default:
		return tx.Commit()
	}

	if err := tx.Close(o.ctx, eventID, eventstore.ReasonMaintenanceSwallowed, o.hostname, meta); err != nil {
		return fmt.Errorf("close event: %w", err)
	}
	if config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
		if err := db.UpdateSiteStatusTxForMonitorSite(o.ctx, tx.Tx(), site.ID, site.BlogID, statusRunning, changeTime); err != nil {
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
		item := map[string]any{
			"host":       vr.Host,
			"success":    vr.Success,
			"http_code":  vr.HTTPCode,
			"error_code": vr.ErrorCode,
			"rtt_ms":     vr.RTTMs,
			"request_id": vr.RequestID,
		}
		if vr.VantageID != "" {
			item["vantage_id"] = vr.VantageID
		}
		if vr.AgentID != "" {
			item["agent_id"] = vr.AgentID
		}
		if vr.Outcome != "" {
			item["outcome"] = vr.Outcome
		}
		out = append(out, item)
	}
	return out
}

func inMaintenance(site db.Site) bool {
	now := nowFunc()
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
	verifiers := o.veriflierConfigs(cfg)
	newAddrs := make([]string, 0, len(verifiers))
	for _, v := range verifiers {
		newAddrs = append(newAddrs, fmt.Sprintf("%s:%s|%s", v.Host, v.TransportPort(), v.AuthToken))
	}

	o.veriflierMu.RLock()
	unchanged := slicesEqual(o.veriflierAddrs, newAddrs)
	o.veriflierMu.RUnlock()
	if unchanged {
		return
	}

	clients := make([]*veriflier.VeriflierClient, 0, len(verifiers))
	for _, v := range verifiers {
		addr := fmt.Sprintf("%s:%s", v.Host, v.TransportPort())
		clients = append(clients, veriflier.NewVeriflierClient(addr, v.AuthToken))
	}
	o.veriflierMu.Lock()
	o.veriflierClients = clients
	o.veriflierAddrs = newAddrs
	o.veriflierMu.Unlock()
}

func (o *Orchestrator) veriflierConfigs(cfg *config.Config) []config.VerifierConfig {
	if cfg == nil {
		return nil
	}
	static := cfg.Verifiers
	if cfg.VeriflierDiscoveryModeOrDefault() != config.VeriflierDiscoveryModeActive {
		return static
	}

	ctx := o.ctx
	if ctx == nil {
		ctx = stdctx.Background()
	}
	queryCtx, cancel := stdctx.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	vantages, err := dbListVeriflierVantages(queryCtx, db.VeriflierDiscoveryDefaultStaleAfter)
	if err != nil {
		log.Printf("orchestrator: veriflier discovery failed, using static config: %v", err)
		return static
	}
	discovered := verifierConfigsFromVantages(vantages)
	if len(discovered) == 0 {
		log.Println("orchestrator: veriflier discovery returned no usable enabled vantages, using static config")
		return static
	}
	return discovered
}

func verifierConfigsFromVantages(vantages []db.VeriflierVantage) []config.VerifierConfig {
	out := make([]config.VerifierConfig, 0, len(vantages))
	for _, vantage := range vantages {
		if !vantage.Usable() {
			continue
		}
		out = append(out, config.VerifierConfig{
			Name:      vantage.VantageID,
			Host:      strings.TrimSpace(vantage.EndpointHost),
			Port:      strings.TrimSpace(vantage.EndpointPort),
			AuthToken: strings.TrimSpace(vantage.AuthToken),
		})
	}
	return out
}

func (o *Orchestrator) syncVeriflierAgentTelemetry(cfg *config.Config) {
	verifiers := o.veriflierConfigs(cfg)
	if len(verifiers) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, verifierConfig := range verifiers {
		v := verifierConfig
		if v.Host == "" || v.TransportPort() == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := o.ctx
			if ctx == nil {
				ctx = stdctx.Background()
			}
			statusCtx, cancel := stdctx.WithTimeout(ctx, verifierTelemetryStatusTimeout)
			defer cancel()

			addr := fmt.Sprintf("%s:%s", v.Host, v.TransportPort())
			status, err := veriflierStatusFunc(veriflier.NewVeriflierClient(addr, v.AuthToken), statusCtx)
			if err != nil || status == nil || !verifierStatusSupportsProtocol(status, veriflier.ProtocolV2) {
				return
			}
			hb := veriflierAgentHeartbeatFromStatus(v, status)
			if hb.AgentID == "" || hb.VantageID == "" {
				return
			}
			writeCtx, writeCancel := stdctx.WithTimeout(ctx, verifierTelemetryStatusTimeout)
			defer writeCancel()
			if err := dbUpsertVeriflierAgent(writeCtx, hb); err != nil {
				log.Printf("orchestrator: veriflier agent telemetry failed addr=%s: %v", addr, err)
			}
		}()
	}
	wg.Wait()
}

func veriflierAgentHeartbeatFromStatus(cfg config.VerifierConfig, status *veriflier.StatusV2Response) db.VeriflierAgentHeartbeat {
	if status == nil {
		return db.VeriflierAgentHeartbeat{}
	}
	return db.VeriflierAgentHeartbeat{
		AgentID:        strings.TrimSpace(status.Agent.ID),
		VantageID:      strings.TrimSpace(status.Vantage.ID),
		Hostname:       strings.TrimSpace(status.Agent.Host),
		EndpointHost:   strings.TrimSpace(cfg.Host),
		EndpointPort:   strings.TrimSpace(cfg.TransportPort()),
		Version:        strings.TrimSpace(status.Version),
		Protocols:      append([]string(nil), status.Protocols...),
		MaxConcurrency: status.Capacity.MaxConcurrency,
		QueueCapacity:  status.Capacity.QueueCapacity,
		QueueDepth:     status.Capacity.QueueDepth,
		Active:         status.Capacity.Active,
		InFlight:       status.Capacity.InFlight,
		Status:         "active",
	}
}

func verifierStatusSupportsProtocol(status *veriflier.StatusV2Response, protocol string) bool {
	if status == nil {
		return false
	}
	for _, p := range status.Protocols {
		if p == protocol {
			return true
		}
	}
	return false
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

func (o *Orchestrator) availableVeriflierClients(clients []*veriflier.VeriflierClient, now time.Time) ([]*veriflier.VeriflierClient, int) {
	if len(clients) == 0 {
		return nil, 0
	}
	if now.IsZero() {
		now = nowFunc().UTC()
	}
	now = now.UTC()
	o.veriflierCooldownMu.Lock()
	defer o.veriflierCooldownMu.Unlock()
	if len(o.veriflierCooldowns) == 0 {
		return clients, 0
	}
	out := make([]*veriflier.VeriflierClient, 0, len(clients))
	skipped := 0
	for _, client := range clients {
		addr := client.Addr()
		cooldown, ok := o.veriflierCooldowns[addr]
		if !ok {
			out = append(out, client)
			continue
		}
		until := cooldown.until.UTC()
		if now.Before(until) {
			skipped++
			continue
		}
		if verifierOperationalCooldownMemory > 0 && now.Sub(until) > verifierOperationalCooldownMemory {
			delete(o.veriflierCooldowns, addr)
		}
		out = append(out, client)
	}
	return out, skipped
}

func (o *Orchestrator) markVeriflierOperationalFailure(addr string, now time.Time) time.Duration {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 0
	}
	if now.IsZero() {
		now = nowFunc().UTC()
	}
	now = now.UTC()
	o.veriflierCooldownMu.Lock()
	defer o.veriflierCooldownMu.Unlock()
	if o.veriflierCooldowns == nil {
		o.veriflierCooldowns = make(map[string]verifierCooldown)
	}
	current := o.veriflierCooldowns[addr]
	failures := current.failures + 1
	delay := verifierOperationalCooldown(failures - 1)
	o.veriflierCooldowns[addr] = verifierCooldown{
		failures: failures,
		until:    now.Add(delay),
	}
	return delay
}

func (o *Orchestrator) markVeriflierHealthy(addr string) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return
	}
	o.veriflierCooldownMu.Lock()
	defer o.veriflierCooldownMu.Unlock()
	delete(o.veriflierCooldowns, addr)
}

func verifierOperationalCooldown(failures int) time.Duration {
	if failures < 0 {
		failures = 0
	}
	delay := verifierOperationalCooldownBase
	for range failures {
		if delay >= verifierOperationalCooldownMax {
			break
		}
		delay *= 2
	}
	if delay > verifierOperationalCooldownMax {
		return verifierOperationalCooldownMax
	}
	return delay
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

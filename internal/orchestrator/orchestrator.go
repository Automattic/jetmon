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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"

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
const schedulerBroadReportInterval = time.Minute
const schedulerBatchSitesPerWorker = 100
const schedulerBaseMaxBatchSites = 25000
const schedulerBatchWorkersMultiplier = 2
const schedulerPoolQueueBufferMultiplier = 2
const schedulerResultProcessChunkSites = 5000
const schedulerAdaptiveWorkerSafetyNumerator = 6
const schedulerAdaptiveWorkerSafetyDenominator = 5
const schedulerWorkerFDReserve = 256
const schedulerWorkerFDUseNumerator = 8
const schedulerWorkerFDUseDenominator = 10
const schedulerSlowWriteLogThreshold = 10 * time.Second
const eventWorkerMinCount = 1
const eventWorkerMaxCount = 16
const eventWorkerScaleSites = 60
const eventQueueBatches = 4
const eventMutationMaxAttempts = 3
const eventMutationRetryBaseDelay = 25 * time.Millisecond
const failureStormMinFailures = 1000
const failureStormMinPercent = 25
const failureStormTransportPercent = 80
const failureStormVerifierSamples = 5
const failureStormVerifierSuccessPercent = 80
const failureStormVerifierSampleTimeout = 2 * time.Second
const failureStormVerifierCacheTTL = 30 * time.Second
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
	dbReleaseHost           = db.ReleaseHost
	dbMarkHostDraining      = db.MarkHostDraining
	dbGetSitesForBucketPage = db.GetSitesForBucketPage
	dbMarkSiteChecked       = db.MarkSiteChecked
	dbMarkSitesCheckedAt    = db.MarkSitesCheckedAt
	dbMarkSitesChecked      = db.MarkSitesChecked
	dbRecordCheckHistory    = db.RecordCheckHistory
	dbRecordCheckHistories  = db.RecordCheckHistories
	dbSiteMonitorActive     = db.SiteMonitorActive
	dbUpdateSSLExpiry       = db.UpdateSSLExpiry
	dbUpdateSSLExpiries     = db.UpdateSSLExpiries
	dbUpdateSiteStatus      = db.UpdateSiteStatus
	dbRecordFalsePositive   = db.RecordFalsePositive
	dbUpdateLastAlertSent   = db.UpdateLastAlertSent
	dbCountDueSites         = db.CountDueSitesForBucketRange
	dbCountProjectionDrift  = db.CountLegacyProjectionDrift
	veriflierCheckFunc      = func(c *veriflier.VeriflierClient, ctx stdctx.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return c.Check(ctx, req)
	}
	metricsClientFunc = func() metricsClient {
		if m := metrics.Global(); m != nil {
			return m
		}
		return nil
	}
	wpcomNotifyFunc       = func(c *wpcom.Client, n wpcom.Notification) error { return c.Notify(n) }
	currentMemoryMBFunc   = currentMemoryMB
	workerResourceCapFunc = schedulerWorkerResourceCap
)

type metricsClient interface {
	Increment(stat string, value int)
	Gauge(stat string, value int)
	Timing(stat string, d time.Duration)
	EmitMemStats()
}

type roundSummary struct {
	pagesFetched      int
	batchesProcessed  int
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

	batchTarget          int
	poolWorkerCountMax   int
	poolActiveChecksMax  int
	poolQueueDepthMax    int
	poolQueueCapacityMax int
	eventJobsQueued      int
	eventQueueDepthMax   int
	eventQueueCapacity   int

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

	processChunks int

	checkSuccesses     int
	checkFailures      int
	checkHTTPFailures  int
	checkTimeouts      int
	checkConnectErrors int
	checkSSLErrors     int
	checkRedirects     int
	checkKeywords      int
	checkTLSDeprecated int
}

func (s *roundSummary) add(other roundSummary) {
	s.pagesFetched += other.pagesFetched
	s.batchesProcessed += other.batchesProcessed
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
	s.processChunks += other.processChunks
	s.checkSuccesses += other.checkSuccesses
	s.checkFailures += other.checkFailures
	s.checkHTTPFailures += other.checkHTTPFailures
	s.checkTimeouts += other.checkTimeouts
	s.checkConnectErrors += other.checkConnectErrors
	s.checkSSLErrors += other.checkSSLErrors
	s.checkRedirects += other.checkRedirects
	s.checkKeywords += other.checkKeywords
	s.checkTLSDeprecated += other.checkTLSDeprecated
	if other.oldestSelectedAge > s.oldestSelectedAge {
		s.oldestSelectedAge = other.oldestSelectedAge
	}
	if other.batchTarget > s.batchTarget {
		s.batchTarget = other.batchTarget
	}
	if other.poolWorkerCountMax > s.poolWorkerCountMax {
		s.poolWorkerCountMax = other.poolWorkerCountMax
	}
	if other.poolActiveChecksMax > s.poolActiveChecksMax {
		s.poolActiveChecksMax = other.poolActiveChecksMax
	}
	if other.poolQueueDepthMax > s.poolQueueDepthMax {
		s.poolQueueDepthMax = other.poolQueueDepthMax
	}
	if other.poolQueueCapacityMax > s.poolQueueCapacityMax {
		s.poolQueueCapacityMax = other.poolQueueCapacityMax
	}
	s.eventJobsQueued += other.eventJobsQueued
	if other.eventQueueDepthMax > s.eventQueueDepthMax {
		s.eventQueueDepthMax = other.eventQueueDepthMax
	}
	if other.eventQueueCapacity > s.eventQueueCapacity {
		s.eventQueueCapacity = other.eventQueueCapacity
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
	checkHTTPFailures  int
	checkTimeouts      int
	checkConnectErrors int
	checkSSLErrors     int
	checkRedirects     int
	checkKeywords      int
	checkTLSDeprecated int
	failureClassCounts map[string]int

	eventJobsQueued        int
	eventQueueDepthMax     int
	eventQueueCapacity     int
	failureStormSuppressed int

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

type failureStormAssessment struct {
	suppress bool
	cached   bool

	failures          int
	transportFailures int
	samples           int
	verifierResponses int
	verifierSuccesses int
	verifierFailures  int
	verifierErrors    int
}

type failureStormVerifierCache struct {
	expiresAt         time.Time
	verifierResponses int
	verifierSuccesses int
	verifierFailures  int
	verifierErrors    int
}

type pageResultBuffer struct {
	siteMap  map[int64]db.Site
	seen     map[int64]struct{}
	pending  map[int64]checker.Result
	received int
}

func newPageResultBuffer(sites []db.Site) pageResultBuffer {
	buf := pageResultBuffer{
		siteMap: make(map[int64]db.Site, len(sites)),
		seen:    make(map[int64]struct{}, len(sites)),
		pending: make(map[int64]checker.Result, min(len(sites), schedulerResultProcessChunkSites)),
	}
	for _, site := range sites {
		buf.siteMap[site.BlogID] = site
	}
	return buf
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
	eventWork        []chan siteCheckResult
	eventWG          sync.WaitGroup
	eventStopOnce    sync.Once
	schedulerDBWrite atomic.Bool
	hostname         string
	bucketMin        int
	bucketMax        int

	totalChecked int
	roundStart   time.Time
	statsMu      sync.RWMutex
	lastRoundSPS int
	lastRoundDur time.Duration

	lastDueCountAt        time.Time
	lastProjectionDriftAt time.Time

	wpcomPermanentMu            sync.Mutex
	wpcomPermanentLastLog       time.Time
	wpcomPermanentLogSuppressed int

	failureStormMu    sync.Mutex
	failureStormCache failureStormVerifierCache

	ctx    stdctx.Context
	cancel stdctx.CancelFunc
}

// New creates an Orchestrator. Call Run to start the check loop.
func New(cfg *config.Config, wp *wpcom.Client) *Orchestrator {
	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	poolMax := schedulerPoolMax(cfg)
	initialWorkers := cfg.NumWorkers
	if initialWorkers < 1 {
		initialWorkers = 1
	}
	if initialWorkers > poolMax {
		initialWorkers = poolMax
	}
	pool := checker.NewPoolWithQueueCapacity(initialWorkers, 1, poolMax, schedulerPoolQueueCapacity(cfg))

	o := &Orchestrator{
		pool:     pool,
		retries:  newRetryQueue(),
		wpcom:    wp,
		events:   eventstore.New(db.DB()),
		hostname: db.Hostname(),
		ctx:      ctx,
		cancel:   cancel,
	}

	o.startEventWorkers(eventWorkerCountForConfig(cfg), eventQueueCapacityForConfig(cfg))
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
			o.stopEventWorkers()
			if o.usesPinnedBuckets(config.Get()) {
				log.Println("orchestrator: pinned bucket mode active; no jetmon_hosts row to release")
			} else if err := dbReleaseHost(stdctx.Background(), o.hostname); err != nil {
				log.Printf("orchestrator: release host: %v", err)
			}
			return
		default:
		}

		cfg := config.Get()
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
	reportNow := nowFunc().UTC()
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
	dueCountsSampled := !cfg.UseVariableCheckIntervals || o.shouldSampleDueCounts(reportNow)
	if o.shouldSampleProjectionDrift(cfg, reportNow) {
		o.checkLegacyProjectionDrift(cfg)
	}

	workerMax := cfg.NumWorkers
	if o.pool != nil {
		workerMax = max(workerMax, o.pool.MaxSize())
	}
	if dueCountsSampled {
		if due, ok := o.sampleDueSites(cfg, &summary); ok {
			workerMax = o.applyAdaptiveWorkerCeiling(cfg, due)
		}
	}

	pageSize := cfg.DatasetSize
	if pageSize < 1 {
		pageSize = 1
	}
	batchTarget := schedulerBatchTargetSites(cfg, pageSize, workerMax)
	summary.batchTarget = batchTarget
	seen := make(map[int64]struct{}, batchTarget)
	cursor := db.SitePageCursor{}
	doneFetching := false
	schedulerBatch := 0
	o.capturePoolStats(&summary)
	for {
		select {
		case <-o.ctx.Done():
			summary.interrupted = true
			o.finishRound(cfg, summary)
			return summary
		default:
		}

		batch := make([]db.Site, 0, batchTarget)
		for len(batch) < batchTarget && !doneFetching {
			sites, err := dbGetSitesForBucketPage(o.ctx, o.bucketMin, o.bucketMax, pageSize, cfg.UseVariableCheckIntervals, cursor)
			if err != nil {
				summary.fetchErrors++
				log.Printf("orchestrator: fetch sites failed: %v", err)
				doneFetching = true
				break
			}
			if len(sites) == 0 {
				doneFetching = true
				break
			}
			if cfg.UseVariableCheckIntervals && !summary.dueCountsSampled && len(sites) == pageSize {
				if due, ok := o.sampleDueSites(cfg, &summary); ok {
					workerMax = o.applyAdaptiveWorkerCeiling(cfg, due)
					if target := schedulerBatchTargetSites(cfg, pageSize, workerMax); target > batchTarget {
						batchTarget = target
						summary.batchTarget = batchTarget
					}
				}
			}

			cursor = schedulerCursorFromSite(sites[len(sites)-1], cfg.UseVariableCheckIntervals)
			page := filterUnseenSites(sites, seen)
			if len(page) > 0 {
				summary.pagesFetched++
				summary.selected += len(page)
				summary.add(selectedSiteSummary(page))
				batch = append(batch, page...)
			}
			if len(sites) < pageSize {
				doneFetching = true
				break
			}
		}

		if len(batch) == 0 {
			break
		}

		schedulerBatch++
		summary.batchesProcessed++
		log.Printf(
			"orchestrator: checking %d sites (scheduler batch %d, db pages fetched=%d)",
			len(batch),
			schedulerBatch,
			summary.pagesFetched,
		)

		pageSummary := o.checkSitesPage(cfg, batch, schedulerBatch)
		summary.add(pageSummary)
		if pageSummary.interrupted {
			break
		}
		if doneFetching {
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

func (o *Orchestrator) sampleDueSites(cfg *config.Config, summary *roundSummary) (int, bool) {
	if summary == nil {
		return 0, false
	}
	summary.dueCountsSampled = true
	if cfg == nil {
		return 0, false
	}
	ctx := stdctx.Background()
	bucketMin, bucketMax := 0, 0
	if o != nil && o.ctx != nil {
		ctx = o.ctx
	}
	if o != nil {
		bucketMin = o.bucketMin
		bucketMax = o.bucketMax
	}
	due, err := dbCountDueSites(ctx, bucketMin, bucketMax, cfg.UseVariableCheckIntervals)
	if err != nil {
		summary.dueCountErrors++
		log.Printf("orchestrator: count due sites failed: %v", err)
		return 0, false
	}
	summary.dueAtStart = due
	return due, true
}

func (o *Orchestrator) applyAdaptiveWorkerCeiling(cfg *config.Config, dueSites int) int {
	workerMax := schedulerAdaptiveWorkerMax(cfg, dueSites)
	if o != nil && o.pool != nil {
		o.pool.SetMaxSize(workerMax)
	}
	return workerMax
}

func schedulerBatchTargetSites(cfg *config.Config, pageSize, workerMax int) int {
	if pageSize < 1 {
		pageSize = 1
	}
	if workerMax < 1 && cfg != nil {
		workerMax = cfg.NumWorkers
	}
	if workerMax < 1 {
		workerMax = 1
	}
	target := workerMax * schedulerBatchSitesPerWorker
	if cfg != nil && cfg.MinTimeBetweenRoundsSec > 0 && cfg.NetCommsTimeout > 0 {
		timeoutBound := workerMax * cfg.MinTimeBetweenRoundsSec / cfg.NetCommsTimeout
		if timeoutBound > 0 && timeoutBound < target {
			target = timeoutBound
		}
	}
	if target < pageSize {
		target = pageSize
	}
	capacityCap := schedulerBatchCapacityCap(workerMax)
	if target > capacityCap {
		target = capacityCap
	}
	return target
}

func schedulerBatchCapacityCap(workerMax int) int {
	if workerMax < 1 {
		workerMax = 1
	}
	capacityCap := workerMax * schedulerBatchWorkersMultiplier
	if capacityCap < schedulerBaseMaxBatchSites {
		return schedulerBaseMaxBatchSites
	}
	return capacityCap
}

func schedulerAdaptiveWorkerMax(cfg *config.Config, dueSites int) int {
	base := 1
	if cfg != nil && cfg.NumWorkers > 0 {
		base = cfg.NumWorkers
	}
	desired := base
	if cfg != nil &&
		cfg.UseVariableCheckIntervals &&
		dueSites > 0 &&
		cfg.MinTimeBetweenRoundsSec > 0 &&
		cfg.NetCommsTimeout > 0 {
		numerator := int64(dueSites) *
			int64(cfg.NetCommsTimeout) *
			int64(schedulerAdaptiveWorkerSafetyNumerator)
		denominator := int64(cfg.MinTimeBetweenRoundsSec) *
			int64(schedulerAdaptiveWorkerSafetyDenominator)
		if denominator > 0 {
			needed := ceilDivInt64(numerator, denominator)
			if needed > int64(maxInt()) {
				desired = maxInt()
			} else if int(needed) > desired {
				desired = int(needed)
			}
		}
	}
	if resourceCap := workerResourceCapFunc(); resourceCap > 0 && desired > resourceCap {
		desired = resourceCap
	}
	if poolMax := schedulerPoolMax(cfg); poolMax > 0 && desired > poolMax {
		desired = poolMax
	}
	if desired < 1 {
		return 1
	}
	return desired
}

func schedulerPoolMax(cfg *config.Config) int {
	base := 1
	if cfg != nil && cfg.NumWorkers > 0 {
		base = cfg.NumWorkers
	}
	poolMax := base
	if resourceCap := workerResourceCapFunc(); resourceCap > 0 {
		poolMax = resourceCap
	}
	if poolMax < 1 {
		return 1
	}
	return poolMax
}

func schedulerPoolQueueCapacity(cfg *config.Config) int {
	base := 1
	if cfg != nil && cfg.NumWorkers > 0 {
		base = cfg.NumWorkers
	}
	poolMax := schedulerPoolMax(cfg)
	if poolMax > 0 && base > poolMax {
		base = poolMax
	}
	capacity := base * schedulerPoolQueueBufferMultiplier
	if poolMax > capacity {
		capacity = poolMax
	}
	if capacity < 1 {
		return 1
	}
	return capacity
}

func schedulerWorkerResourceCap() int {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return 0
	}
	maximum := maxInt()
	softLimit := maximum
	if limit.Cur < uint64(maximum) {
		softLimit = int(limit.Cur)
	}
	if softLimit <= schedulerWorkerFDReserve {
		return 0
	}
	return (softLimit - schedulerWorkerFDReserve) *
		schedulerWorkerFDUseNumerator /
		schedulerWorkerFDUseDenominator
}

func ceilDivInt64(numerator, denominator int64) int64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	return (numerator + denominator - 1) / denominator
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func eventWorkerCountForConfig(cfg *config.Config) int {
	if cfg == nil || cfg.NumWorkers <= 0 {
		return eventWorkerMinCount
	}
	workers := cfg.NumWorkers / eventWorkerScaleSites
	if workers < eventWorkerMinCount {
		workers = eventWorkerMinCount
	}
	if workers > eventWorkerMaxCount {
		workers = eventWorkerMaxCount
	}
	return workers
}

func eventQueueCapacityForConfig(cfg *config.Config) int {
	if cfg == nil {
		return schedulerBaseMaxBatchSites
	}
	pageSize := 1
	if cfg.DatasetSize > 0 {
		pageSize = cfg.DatasetSize
	}
	return schedulerBatchTargetSites(cfg, pageSize, cfg.NumWorkers) * eventQueueBatches
}

func (o *Orchestrator) startEventWorkers(count, queueCapacity int) {
	if o == nil || count <= 0 {
		return
	}
	if queueCapacity < count {
		queueCapacity = count
	}
	o.eventWork = make([]chan siteCheckResult, count)
	for i := range count {
		ch := make(chan siteCheckResult, queueCapacity)
		o.eventWork[i] = ch
		o.eventWG.Add(1)
		go o.eventWorker(i, ch)
	}
	log.Printf("orchestrator: event workers started workers=%d queue_capacity_per_worker=%d", count, queueCapacity)
}

func (o *Orchestrator) stopEventWorkers() {
	if o == nil {
		return
	}
	o.eventStopOnce.Do(func() {
		for _, ch := range o.eventWork {
			close(ch)
		}
		o.eventWG.Wait()
	})
}

func (o *Orchestrator) eventWorker(workerID int, ch <-chan siteCheckResult) {
	defer o.eventWG.Done()
	for record := range ch {
		if waited := o.waitForSchedulerDBWrites(); waited > 0 {
			emitTiming("scheduler.event_worker.scheduler_write_wait.time", waited)
			emitCounter("scheduler.event_worker.scheduler_write_wait.count", 1)
		}
		start := time.Now()
		o.processResultEvent(record)
		emitTiming("scheduler.event_worker.process.time", time.Since(start))
		emitCounter("scheduler.event_worker.process.count", 1)
		emitCounter(fmt.Sprintf("scheduler.event_worker.%d.process.count", workerID+1), 1)
	}
}

func (o *Orchestrator) waitForSchedulerDBWrites() time.Duration {
	if o == nil || !o.schedulerDBWrite.Load() {
		return 0
	}
	ctx := o.ctx
	if ctx == nil {
		ctx = stdctx.Background()
	}
	start := time.Now()
	ticker := time.NewTicker(schedulerBackpressurePollInterval)
	defer ticker.Stop()
	for o.schedulerDBWrite.Load() {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return time.Since(start)
		}
	}
	return time.Since(start)
}

func (o *Orchestrator) withSchedulerDBWrite(fn func()) {
	if o == nil {
		fn()
		return
	}
	o.schedulerDBWrite.Store(true)
	defer o.schedulerDBWrite.Store(false)
	fn()
}

func schedulerCursorFromSite(site db.Site, useVariableIntervals bool) db.SitePageCursor {
	cursor := db.SitePageCursor{
		HasCursor: true,
		BlogID:    site.BlogID,
	}
	if useVariableIntervals {
		cursor.NextCheckAt = site.NextCheckAt
	} else {
		cursor.LastCheckedAt = site.LastCheckedAt
	}
	return cursor
}

func (o *Orchestrator) capturePoolStats(summary *roundSummary) {
	if o == nil || o.pool == nil || summary == nil {
		return
	}
	workers := o.pool.WorkerCount()
	active := o.pool.ActiveCount()
	queueDepth := o.pool.QueueDepth()
	queueCapacity := o.pool.QueueCapacity()
	if workers > summary.poolWorkerCountMax {
		summary.poolWorkerCountMax = workers
	}
	if active > summary.poolActiveChecksMax {
		summary.poolActiveChecksMax = active
	}
	if queueDepth > summary.poolQueueDepthMax {
		summary.poolQueueDepthMax = queueDepth
	}
	if queueCapacity > summary.poolQueueCapacityMax {
		summary.poolQueueCapacityMax = queueCapacity
	}
}

func (o *Orchestrator) checkSitesPage(cfg *config.Config, sites []db.Site, pageNumber int) roundSummary {
	summary := roundSummary{}
	results := newPageResultBuffer(sites)

	o.capturePoolStats(&summary)
	dispatchStart := time.Now()
	for _, site := range sites {
		req := checkRequestForSite(cfg, site)
		for {
			if o.pool.Submit(req) {
				summary.dispatched++
				o.capturePoolStats(&summary)
				break
			}
			summary.backpressureWaits++
			if !o.waitForPageResult(&results, &summary, &dispatchStart, &summary.dispatchDuration, schedulerBackpressurePollInterval) {
				summary.interrupted = true
				summary.dispatchDuration += time.Since(dispatchStart)
				o.capturePoolStats(&summary)
				return summary
			}
			o.capturePoolStats(&summary)
		}
	}
	summary.dispatchDuration += time.Since(dispatchStart)
	o.capturePoolStats(&summary)

	collectionDeadlineAt := time.Now().Add(collectionDeadlineForSites(cfg, sites, o.pool.MaxSize()))
	deadline := time.NewTimer(time.Until(collectionDeadlineAt))
	defer deadline.Stop()
	waitStart := time.Now()
	for results.received < summary.dispatched {
		select {
		case res := <-o.pool.Results():
			if !o.recordCollectionResult(&results, &summary, deadline, &collectionDeadlineAt, &waitStart, res) {
				summary.outstanding = summary.dispatched - results.received
				log.Printf("orchestrator: round deadline reached, %d results outstanding", summary.outstanding)
				goto process
			}
			if !o.drainAvailableCollectionResults(&results, &summary, deadline, &collectionDeadlineAt, &waitStart) {
				summary.outstanding = summary.dispatched - results.received
				log.Printf("orchestrator: round deadline reached, %d results outstanding", summary.outstanding)
				goto process
			}
			o.capturePoolStats(&summary)
		case <-deadline.C:
			summary.outstanding = summary.dispatched - results.received
			log.Printf("orchestrator: round deadline reached, %d results outstanding", summary.outstanding)
			goto process
		case <-o.ctx.Done():
			summary.interrupted = true
			summary.waitDuration += time.Since(waitStart)
			o.capturePoolStats(&summary)
			return summary
		}
	}

process:
	summary.waitDuration += time.Since(waitStart)
	o.capturePoolStats(&summary)
	o.flushPageResults(&results, &summary)
	emitPageMetrics(summary)
	logPageSummary(pageNumber, len(sites), summary)
	return summary
}

func (o *Orchestrator) recordCollectionResult(results *pageResultBuffer, summary *roundSummary, deadline *time.Timer, collectionDeadlineAt *time.Time, waitStart *time.Time, res checker.Result) bool {
	results.record(res, summary)
	if len(results.pending) < schedulerResultProcessChunkSites {
		return true
	}
	summary.waitDuration += time.Since(*waitStart)
	processStart := time.Now()
	stopAndDrainTimer(deadline)
	o.flushPageResults(results, summary)
	*collectionDeadlineAt = collectionDeadlineAt.Add(time.Since(processStart))
	*waitStart = time.Now()
	return resetTimerUntil(deadline, *collectionDeadlineAt)
}

func (o *Orchestrator) drainAvailableCollectionResults(results *pageResultBuffer, summary *roundSummary, deadline *time.Timer, collectionDeadlineAt *time.Time, waitStart *time.Time) bool {
	for results.received < summary.dispatched {
		select {
		case res := <-o.pool.Results():
			if !o.recordCollectionResult(results, summary, deadline, collectionDeadlineAt, waitStart, res) {
				return false
			}
		default:
			return true
		}
	}
	return true
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
	m.Gauge("scheduler.page.process.chunk.count", summary.processChunks)
	m.Gauge("scheduler.page.pool.workers.max", summary.poolWorkerCountMax)
	m.Gauge("scheduler.page.pool.active.max", summary.poolActiveChecksMax)
	m.Gauge("scheduler.page.pool.queue_depth.max", summary.poolQueueDepthMax)
	m.Gauge("scheduler.page.pool.queue_capacity.max", summary.poolQueueCapacityMax)
	m.Gauge("scheduler.page.event_queue.depth.max", summary.eventQueueDepthMax)
	m.Gauge("scheduler.page.event_queue.capacity", summary.eventQueueCapacity)
	m.Increment("scheduler.page.mark_checked.row.count", summary.markCheckedRows)
	m.Increment("scheduler.page.history.row.count", summary.historyRows)
	m.Increment("scheduler.page.ssl.row.count", summary.sslRows)
	m.Increment("scheduler.page.event_queue.job.count", summary.eventJobsQueued)
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
}

func logPageSummary(pageNumber, sites int, summary roundSummary) {
	log.Printf(
		"orchestrator: batch summary batch=%d sites=%d dispatched=%d completed=%d outstanding=%d process_chunks=%d pool_workers_max=%d pool_active_max=%d pool_queue_depth_max=%d pool_queue_capacity=%d event_jobs_queued=%d event_queue_depth_max=%d event_queue_capacity=%d dispatch=%s wait=%s process=%s mark_checked=%s history=%s ssl=%s events=%s checks_success=%d checks_failure=%d checks_http_failure=%d checks_timeout=%d checks_connect_error=%d checks_ssl_error=%d checks_redirect=%d checks_keyword=%d checks_tls_deprecated=%d mark_checked_rows=%d history_rows=%d ssl_rows=%d mark_checked_errors=%d history_errors=%d ssl_errors=%d",
		pageNumber,
		sites,
		summary.dispatched,
		summary.completed,
		summary.outstanding,
		summary.processChunks,
		summary.poolWorkerCountMax,
		summary.poolActiveChecksMax,
		summary.poolQueueDepthMax,
		summary.poolQueueCapacityMax,
		summary.eventJobsQueued,
		summary.eventQueueDepthMax,
		summary.eventQueueCapacity,
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

func (o *Orchestrator) waitForPageResult(results *pageResultBuffer, summary *roundSummary, phaseStart *time.Time, phaseDuration *time.Duration, maxWait time.Duration) bool {
	if o.drainAvailablePageResults(results, summary, phaseStart, phaseDuration) > 0 {
		return true
	}
	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	select {
	case res := <-o.pool.Results():
		o.recordPageResultForPhase(results, summary, phaseStart, phaseDuration, res)
		return true
	case <-timer.C:
		return true
	case <-o.ctx.Done():
		return false
	}
}

func (o *Orchestrator) drainAvailablePageResults(results *pageResultBuffer, summary *roundSummary, phaseStart *time.Time, phaseDuration *time.Duration) int {
	drained := 0
	for {
		select {
		case res := <-o.pool.Results():
			o.recordPageResultForPhase(results, summary, phaseStart, phaseDuration, res)
			drained++
		default:
			return drained
		}
	}
}

func (o *Orchestrator) recordPageResultForPhase(results *pageResultBuffer, summary *roundSummary, phaseStart *time.Time, phaseDuration *time.Duration, res checker.Result) {
	results.record(res, summary)
	if len(results.pending) < schedulerResultProcessChunkSites {
		return
	}
	*phaseDuration += time.Since(*phaseStart)
	o.flushPageResults(results, summary)
	*phaseStart = time.Now()
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

func collectionDeadlineForSites(cfg *config.Config, sites []db.Site, workers int) time.Duration {
	timeout := cfg.NetCommsTimeout
	for _, site := range sites {
		if siteTimeout := timeoutForSite(cfg, site); siteTimeout > timeout {
			timeout = siteTimeout
		}
	}
	if timeout < 1 {
		timeout = 1
	}
	if workers < 1 {
		workers = 1
	}
	waves := ceilDivInt64(int64(len(sites)), int64(workers))
	if waves < 1 {
		waves = 1
	}
	return time.Duration(int64(timeout)*waves+5) * time.Second
}

func stopAndDrainTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func resetTimerUntil(timer *time.Timer, deadline time.Time) bool {
	if timer == nil {
		return false
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	timer.Reset(remaining)
	return true
}

func (r *pageResultBuffer) record(res checker.Result, summary *roundSummary) {
	if r == nil {
		return
	}
	if _, ok := r.siteMap[res.BlogID]; !ok {
		summary.staleResults++
		return
	}
	if _, ok := r.seen[res.BlogID]; ok {
		summary.duplicateResults++
		return
	}
	r.seen[res.BlogID] = struct{}{}
	r.pending[res.BlogID] = res
	r.received++
}

func (o *Orchestrator) flushPageResults(results *pageResultBuffer, summary *roundSummary) {
	if results == nil || len(results.pending) == 0 {
		return
	}
	processStart := time.Now()
	processSummary := o.processResults(results.pending, results.siteMap)
	summary.processDuration += time.Since(processStart)
	addResultProcessSummary(summary, processSummary)
	summary.processChunks++
	o.totalChecked += processSummary.processed
	clear(results.pending)
}

func addResultProcessSummary(summary *roundSummary, processSummary resultProcessSummary) {
	if summary == nil {
		return
	}
	summary.completed += processSummary.processed
	summary.markCheckedRows += processSummary.markCheckedRows
	summary.historyRows += processSummary.historyRows
	summary.sslRows += processSummary.sslRows
	summary.markCheckedErrors += processSummary.markCheckedErrors
	summary.historyErrors += processSummary.historyErrors
	summary.sslErrors += processSummary.sslErrors
	summary.checkSuccesses += processSummary.checkSuccesses
	summary.checkFailures += processSummary.checkFailures
	summary.checkHTTPFailures += processSummary.checkHTTPFailures
	summary.checkTimeouts += processSummary.checkTimeouts
	summary.checkConnectErrors += processSummary.checkConnectErrors
	summary.checkSSLErrors += processSummary.checkSSLErrors
	summary.checkRedirects += processSummary.checkRedirects
	summary.checkKeywords += processSummary.checkKeywords
	summary.checkTLSDeprecated += processSummary.checkTLSDeprecated
	summary.markCheckedDuration += processSummary.markCheckedDuration
	summary.historyDuration += processSummary.historyDuration
	summary.sslDuration += processSummary.sslDuration
	summary.eventDuration += processSummary.eventDuration
	summary.eventJobsQueued += processSummary.eventJobsQueued
	if processSummary.eventQueueDepthMax > summary.eventQueueDepthMax {
		summary.eventQueueDepthMax = processSummary.eventQueueDepthMax
	}
	if processSummary.eventQueueCapacity > summary.eventQueueCapacity {
		summary.eventQueueCapacity = processSummary.eventQueueCapacity
	}
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
		m.Gauge("scheduler.round.batches.count", summary.batchesProcessed)
		m.Gauge("scheduler.round.batch_target.count", summary.batchTarget)
		m.Gauge("scheduler.round.selected.count", summary.selected)
		m.Gauge("scheduler.round.dispatched.count", summary.dispatched)
		m.Gauge("scheduler.round.completed.count", summary.completed)
		m.Gauge("scheduler.round.outstanding.count", summary.outstanding)
		m.Gauge("scheduler.round.process.chunk.count", summary.processChunks)
		m.Gauge("scheduler.round.pool.workers.max", summary.poolWorkerCountMax)
		m.Gauge("scheduler.round.pool.active.max", summary.poolActiveChecksMax)
		m.Gauge("scheduler.round.pool.queue_depth.max", summary.poolQueueDepthMax)
		m.Gauge("scheduler.round.pool.queue_capacity.max", summary.poolQueueCapacityMax)
		m.Gauge("scheduler.round.event_queue.depth.max", summary.eventQueueDepthMax)
		m.Gauge("scheduler.round.event_queue.capacity", summary.eventQueueCapacity)
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
		m.Increment("scheduler.round.event_queue.job.count", summary.eventJobsQueued)
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
		"orchestrator: round summary pages=%d batches=%d batch_target=%d due_count_sampled=%t due_start=%d selected=%d dispatched=%d completed=%d outstanding=%d due_remaining=%d backpressure_waits=%d process_chunks=%d pool_workers_max=%d pool_active_max=%d pool_queue_depth_max=%d pool_queue_capacity=%d event_jobs_queued=%d event_queue_depth_max=%d event_queue_capacity=%d stale_results=%d duplicate_results=%d never_checked=%d oldest_selected_age_sec=%d dispatch=%s wait=%s process=%s mark_checked=%s history=%s ssl=%s events=%s checks_success=%d checks_failure=%d checks_http_failure=%d checks_timeout=%d checks_connect_error=%d checks_ssl_error=%d checks_redirect=%d checks_keyword=%d checks_tls_deprecated=%d mark_checked_rows=%d history_rows=%d ssl_rows=%d mark_checked_errors=%d history_errors=%d ssl_errors=%d duration=%s sps=%d",
		summary.pagesFetched,
		summary.batchesProcessed,
		summary.batchTarget,
		summary.dueCountsSampled,
		summary.dueAtStart,
		summary.selected,
		summary.dispatched,
		summary.completed,
		summary.outstanding,
		summary.dueRemaining,
		summary.backpressureWaits,
		summary.processChunks,
		summary.poolWorkerCountMax,
		summary.poolActiveChecksMax,
		summary.poolQueueDepthMax,
		summary.poolQueueCapacityMax,
		summary.eventJobsQueued,
		summary.eventQueueDepthMax,
		summary.eventQueueCapacity,
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
		addCheckOutcome(&summary, record.res)
	}
	emitDetectionFailureCounters(summary.failureClassCounts)

	o.withSchedulerDBWrite(func() {
		o.markResultsChecked(records, &summary)
	})

	storm := o.assessFailureStorm(records)

	o.withSchedulerDBWrite(func() {
		o.recordResultHistories(records, &summary, storm)

		sslStart := time.Now()
		sslUpdates := make([]db.SiteSSLExpiry, 0)
		for _, record := range records {
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
	})

	eventStart := time.Now()
	o.processResultEvents(records, &summary, storm)
	summary.eventDuration += time.Since(eventStart)
	return summary
}

func (o *Orchestrator) processResultEvents(records []siteCheckResult, summary *resultProcessSummary, storm failureStormAssessment) {
	if storm.suppress {
		summary.failureStormSuppressed += storm.transportFailures
		emitCounter("detection.failure_storm.suppressed.count", storm.transportFailures)
		if storm.cached {
			emitCounter("detection.failure_storm.cache_hit.count", 1)
		} else {
			emitCounter("detection.failure_storm.sample.count", storm.samples)
			emitCounter("detection.failure_storm.verifier_response.count", storm.verifierResponses)
			emitCounter("detection.failure_storm.verifier_success.count", storm.verifierSuccesses)
			emitCounter("detection.failure_storm.verifier_failure.count", storm.verifierFailures)
			emitCounter("detection.failure_storm.verifier_error.count", storm.verifierErrors)
		}
		log.Printf(
			"orchestrator: suppressing broad local failure storm records=%d failures=%d transport_failures=%d verifier_cache_hit=%t verifier_samples=%d verifier_responses=%d verifier_successes=%d verifier_failures=%d verifier_errors=%d",
			len(records),
			storm.failures,
			storm.transportFailures,
			storm.cached,
			storm.samples,
			storm.verifierResponses,
			storm.verifierSuccesses,
			storm.verifierFailures,
			storm.verifierErrors,
		)
		o.clearSuppressedFailureRetries(records)
	}

	for _, record := range records {
		// Per-check data is recorded in jetmon_check_history (above); duplicating
		// it in jetmon_audit_log was retired with the operational/site-state split.
		if storm.suppress && isTransportFailure(record.res) {
			continue
		}
		if !o.shouldProcessResultEvent(record) {
			continue
		}
		if o.enqueueResultEvent(record, summary) {
			continue
		}
		o.processResultEvent(record)
	}
}

func (o *Orchestrator) shouldProcessResultEvent(record siteCheckResult) bool {
	if record.res.IsFailure() {
		return true
	}
	if record.site.SiteStatus != statusRunning {
		return true
	}
	return o != nil && o.retries != nil && o.retries.get(record.blogID) != nil
}

func (o *Orchestrator) enqueueResultEvent(record siteCheckResult, summary *resultProcessSummary) bool {
	if o == nil || len(o.eventWork) == 0 {
		return false
	}
	worker := int(record.blogID % int64(len(o.eventWork)))
	if worker < 0 {
		worker = -worker
	}
	ch := o.eventWork[worker]
	select {
	case ch <- record:
		summary.eventJobsQueued++
		depth := len(ch)
		if depth > summary.eventQueueDepthMax {
			summary.eventQueueDepthMax = depth
		}
		if cap(ch) > summary.eventQueueCapacity {
			summary.eventQueueCapacity = cap(ch)
		}
		return true
	case <-o.ctx.Done():
		return true
	}
}

func (o *Orchestrator) processResultEvent(record siteCheckResult) {
	if !record.res.IsFailure() {
		o.handleRecovery(record.site, record.res)
		return
	}
	o.handleFailure(record.site, record.res)
}

func addCheckOutcome(summary *resultProcessSummary, res checker.Result) {
	if res.Success {
		summary.checkSuccesses++
	} else {
		summary.checkFailures++
		if summary.failureClassCounts == nil {
			summary.failureClassCounts = make(map[string]int)
		}
		summary.failureClassCounts[failureClass(res)]++
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

func emitDetectionFailureCounters(counts map[string]int) {
	for class, count := range counts {
		if count > 0 {
			emitCounter("detection.failure."+class+".count", count)
		}
	}
}

func (o *Orchestrator) assessFailureStorm(records []siteCheckResult) failureStormAssessment {
	var assessment failureStormAssessment
	if len(records) == 0 {
		return assessment
	}

	transportRecords := make([]siteCheckResult, 0, min(len(records), failureStormVerifierSamples))
	for _, record := range records {
		if !record.res.IsFailure() {
			continue
		}
		assessment.failures++
		if !isTransportFailure(record.res) {
			continue
		}
		assessment.transportFailures++
		if len(transportRecords) < failureStormVerifierSamples {
			transportRecords = append(transportRecords, record)
		}
	}

	if assessment.transportFailures < failureStormMinFailures {
		return assessment
	}
	if assessment.failures*100 < len(records)*failureStormMinPercent {
		return assessment
	}
	if assessment.transportFailures*100 < assessment.failures*failureStormTransportPercent {
		return assessment
	}

	clients := o.veriflierSnapshot()
	if len(clients) == 0 || len(transportRecords) == 0 {
		return assessment
	}

	if cached, ok := o.cachedFailureStormSuppression(nowFunc()); ok {
		assessment.suppress = true
		assessment.cached = true
		assessment.verifierResponses = cached.verifierResponses
		assessment.verifierSuccesses = cached.verifierSuccesses
		assessment.verifierFailures = cached.verifierFailures
		assessment.verifierErrors = cached.verifierErrors
		return assessment
	}

	assessment.samples = len(transportRecords)
	assessment.verifierResponses, assessment.verifierSuccesses, assessment.verifierFailures, assessment.verifierErrors =
		o.sampleVerifiersForFailureStorm(transportRecords, clients)
	if assessment.verifierResponses == 0 {
		return assessment
	}
	if assessment.verifierSuccesses*100 >= assessment.verifierResponses*failureStormVerifierSuccessPercent {
		assessment.suppress = true
		o.rememberFailureStormSuppression(nowFunc(), assessment)
	}
	return assessment
}

func (o *Orchestrator) cachedFailureStormSuppression(now time.Time) (failureStormVerifierCache, bool) {
	if o == nil {
		return failureStormVerifierCache{}, false
	}
	o.failureStormMu.Lock()
	defer o.failureStormMu.Unlock()
	if o.failureStormCache.expiresAt.IsZero() || !now.Before(o.failureStormCache.expiresAt) {
		return failureStormVerifierCache{}, false
	}
	return o.failureStormCache, true
}

func (o *Orchestrator) rememberFailureStormSuppression(now time.Time, assessment failureStormAssessment) {
	if o == nil || !assessment.suppress || assessment.cached {
		return
	}
	o.failureStormMu.Lock()
	defer o.failureStormMu.Unlock()
	o.failureStormCache = failureStormVerifierCache{
		expiresAt:         now.Add(failureStormVerifierCacheTTL),
		verifierResponses: assessment.verifierResponses,
		verifierSuccesses: assessment.verifierSuccesses,
		verifierFailures:  assessment.verifierFailures,
		verifierErrors:    assessment.verifierErrors,
	}
}

func isTransportFailure(res checker.Result) bool {
	if !res.IsFailure() {
		return false
	}
	switch res.ErrorCode {
	case checker.ErrorTimeout, checker.ErrorConnect:
		return true
	default:
		return false
	}
}

func (o *Orchestrator) sampleVerifiersForFailureStorm(records []siteCheckResult, clients []*veriflier.VeriflierClient) (responses, successes, failures, errorsCount int) {
	if len(records) == 0 || len(clients) == 0 {
		return 0, 0, 0, 0
	}

	parent := o.ctx
	if parent == nil {
		parent = stdctx.Background()
	}
	ctx, cancel := stdctx.WithTimeout(parent, failureStormVerifierSampleTimeout)
	defer cancel()

	type sampleResult struct {
		success bool
		err     error
	}
	total := len(records) * len(clients)
	ch := make(chan sampleResult, total)
	for _, record := range records {
		req := veriflier.CheckRequest{
			BlogID:         record.blogID,
			URL:            record.site.MonitorURL,
			TimeoutSeconds: int32(timeoutForSite(config.Get(), record.site)),
			Keyword:        stringPtrValue(record.site.CheckKeyword),
			CustomHeaders:  checker.ParseCustomHeaders(record.site.CustomHeaders),
			RedirectPolicy: record.site.RedirectPolicy,
			RequestID:      veriflier.NewRequestID(),
		}
		for _, client := range clients {
			c := client
			go func() {
				res, err := veriflierCheckFunc(c, ctx, req)
				if err != nil {
					ch <- sampleResult{err: err}
					return
				}
				ch <- sampleResult{success: res != nil && res.Success}
			}()
		}
	}

	for range total {
		select {
		case result := <-ch:
			if result.err != nil {
				errorsCount++
				continue
			}
			responses++
			if result.success {
				successes++
			} else {
				failures++
			}
		case <-ctx.Done():
			errorsCount += total - responses - errorsCount
			return responses, successes, failures, errorsCount
		}
	}
	return responses, successes, failures, errorsCount
}

func (o *Orchestrator) clearSuppressedFailureRetries(records []siteCheckResult) {
	if o == nil || o.retries == nil {
		return
	}
	o.retries.mu.Lock()
	defer o.retries.mu.Unlock()
	if len(o.retries.entries) == 0 {
		return
	}
	for _, record := range records {
		if !isTransportFailure(record.res) {
			continue
		}
		entry := o.retries.entries[record.blogID]
		if entry == nil || entry.eventID == 0 {
			delete(o.retries.entries, record.blogID)
		}
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
	blogIDs := make([]int64, 0, len(records))
	checks := make([]db.SiteCheck, 0, len(records))
	var chunkCheckedAt time.Time
	for _, record := range records {
		checkedAt := resultCheckedAt(record.res)
		if checkedAt.After(chunkCheckedAt) {
			chunkCheckedAt = checkedAt
		}
		blogIDs = append(blogIDs, record.blogID)
		checks = append(checks, db.SiteCheck{
			BlogID:      record.blogID,
			CheckedAt:   checkedAt,
			NextCheckAt: nextCheckAt(record.site, record.res),
		})
	}

	start := time.Now()
	if err := dbMarkSitesCheckedAt(o.ctx, blogIDs, chunkCheckedAt); err != nil {
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
	elapsed := time.Since(start)
	summary.markCheckedDuration += elapsed
	if elapsed >= schedulerSlowWriteLogThreshold && len(checks) > 0 {
		emitCounter("scheduler.mark_checked.slow.count", 1)
		log.Printf(
			"orchestrator: slow batch mark checked sites=%d duration=%s",
			len(checks),
			elapsed.Round(time.Millisecond),
		)
	}
}

func (o *Orchestrator) recordResultHistories(records []siteCheckResult, summary *resultProcessSummary, storm failureStormAssessment) {
	histories := make([]db.CheckHistoryRow, 0, len(records))
	suppressedTransportFailures := 0
	for _, record := range records {
		if storm.suppress && isTransportFailure(record.res) {
			suppressedTransportFailures++
			continue
		}
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
	if suppressedTransportFailures > 0 {
		emitCounter("scheduler.check_history.suppressed_transport_storm.count", suppressedTransportFailures)
	}
	if len(histories) == 0 {
		return
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
	elapsed := time.Since(start)
	summary.historyDuration += elapsed
	if elapsed >= schedulerSlowWriteLogThreshold && len(histories) > 0 {
		emitCounter("scheduler.history.slow.count", 1)
		log.Printf(
			"orchestrator: slow batch record check history rows=%d duration=%s",
			len(histories),
			elapsed.Round(time.Millisecond),
		)
	}
}

func resultCheckedAt(res checker.Result) time.Time {
	if res.Timestamp.IsZero() {
		return nowFunc().UTC()
	}
	return res.Timestamp.UTC()
}

func nextCheckAt(site db.Site, res checker.Result) time.Time {
	interval := site.CheckInterval
	if interval < 1 {
		interval = 1
	}
	return resultCheckedAt(res).Add(time.Duration(interval) * time.Minute)
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

	if entry != nil || site.SiteStatus != statusRunning {
		changeTime := nowFunc().UTC()
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

		if site.SiteStatus == statusRunning {
			return
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
	// The scheduler fetches only active rows. Re-querying monitor_active for
	// every failed probe turns broad outages into tens of thousands of extra
	// SELECTs that contend with freshness writes. If a caller passes a concrete
	// inactive row, still honor it; otherwise trust the selected scheduler
	// snapshot and let normal deactivation cleanup close any rare race.
	if site.ID != 0 && !site.MonitorActive {
		o.retries.clear(site.BlogID)
		emitCounter("detection.inactive_site_failure.skipped.count", 1)
		return
	}

	entry := o.retries.record(res)
	class := failureClass(res)

	// Open a Seems Down event on the first failure we don't already have an
	// id for. The schema's idempotent dedup_key means re-detecting the same
	// failure would update the same row, so this is also a self-healing retry
	// path if a previous Open failed to commit.
	if entry.eventID == 0 {
		id, opened, err := o.openSeemsDown(site, res)
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
		// The open event, transition metadata, retry queue, and check-history
		// sample preserve the local retry context. Avoid writing one audit row
		// per failed probe; broad outages turn that into the dominant DB load.
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
				"verifier_results":   summarizeVerifierResults(vResults),
			})
			if err := o.closeEvent(site.BlogID, entry.eventID,
				eventstore.ReasonFalseAlarm, statusRunning, nowFunc().UTC(), meta); err != nil {
				if errors.Is(err, eventstore.ErrEventClosed) {
					o.retries.clear(site.BlogID)
					return
				}
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
	wpcomStatus := wpcomStatusMetricSegment(status)
	if cfg := config.Get(); cfg != nil && !cfg.WPCOMNotifyEnable {
		emitCounter("wpcom.notification.skipped.count", 1)
		emitCounter("wpcom.notification.status."+wpcomStatus+".skipped.count", 1)
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

	emitCounter("wpcom.notification.attempt.count", 1)
	emitCounter("wpcom.notification.status."+wpcomStatus+".attempt.count", 1)
	if err := wpcomNotifyFunc(o.wpcom, n); err != nil {
		emitCounter("wpcom.notification.error.count", 1)
		emitCounter("wpcom.notification.status."+wpcomStatus+".error.count", 1)
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
			return
		}

		// Single retry.
		emitCounter("wpcom.notification.retry.count", 1)
		log.Printf("orchestrator: wpcom notify failed for blog_id=%d: %v", site.BlogID, err)
		o.auditLog(audit.Entry{
			BlogID:    site.BlogID,
			EventType: audit.EventWPCOMRetry,
			Source:    "local",
			Detail:    err.Error(),
		})
		if retryErr := wpcomNotifyFunc(o.wpcom, n); retryErr != nil {
			emitCounter("wpcom.notification.error.count", 1)
			emitCounter("wpcom.notification.status."+wpcomStatus+".error.count", 1)
			if errors.Is(retryErr, wpcom.ErrCircuitOpen) {
				emitCounter("wpcom.notification.queued.count", 1)
				emitCounter("wpcom.notification.status."+wpcomStatus+".queued.count", 1)
				return
			}
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

func (o *Orchestrator) logWPCOMPermanentFailure(blogID int64, err error) {
	if o == nil {
		log.Printf("orchestrator: wpcom notify permanent failure for blog_id=%d: %v", blogID, err)
		return
	}
	o.wpcomPermanentMu.Lock()
	defer o.wpcomPermanentMu.Unlock()

	now := nowFunc()
	if o.wpcomPermanentLastLog.IsZero() || now.Sub(o.wpcomPermanentLastLog) >= wpcomPermanentFailureLogInterval {
		if o.wpcomPermanentLogSuppressed > 0 {
			log.Printf(
				"orchestrator: wpcom notify permanent failure for blog_id=%d: %v (suppressed %d similar permanent failures)",
				blogID,
				err,
				o.wpcomPermanentLogSuppressed,
			)
		} else {
			log.Printf("orchestrator: wpcom notify permanent failure for blog_id=%d: %v", blogID, err)
		}
		o.wpcomPermanentLastLog = now
		o.wpcomPermanentLogSuppressed = 0
		return
	}
	o.wpcomPermanentLogSuppressed++
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
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
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
func (o *Orchestrator) openSeemsDown(site db.Site, res checker.Result) (int64, bool, error) {
	var eventID int64
	var opened bool
	err := o.withEventMutationRetry(site.BlogID, "open_seems_down", func() error {
		id, didOpen, err := o.openSeemsDownOnce(site, res)
		if err != nil {
			return err
		}
		eventID = id
		opened = didOpen
		return nil
	})
	return eventID, opened, err
}

func (o *Orchestrator) openSeemsDownOnce(site db.Site, res checker.Result) (int64, bool, error) {
	tx, err := o.ev().Begin(o.ctx)
	if err != nil {
		return 0, false, err
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
		return 0, false, err
	}

	// Project v1 site_status=SITE_DOWN only on the actual insert. A re-detection
	// (Opened=false) is by definition a row that already exists, so site_status
	// was already projected when the event first opened.
	if out.Opened && config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
		if err := db.UpdateSiteStatusTx(o.ctx, tx.Tx(), site.BlogID, statusDown, nowFunc().UTC()); err != nil {
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
func (o *Orchestrator) promoteToDown(blogID, eventID int64, changeTime time.Time, meta json.RawMessage) error {
	return o.withEventMutationRetry(blogID, "promote_to_down", func() error {
		return o.promoteToDownOnce(blogID, eventID, changeTime, meta)
	})
}

func (o *Orchestrator) promoteToDownOnce(blogID, eventID int64, changeTime time.Time, meta json.RawMessage) error {
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
	return o.withEventMutationRetry(blogID, "close_event", func() error {
		return o.closeEventOnce(blogID, eventID, reason, projectedStatus, changeTime, meta)
	})
}

func (o *Orchestrator) closeEventOnce(blogID, eventID int64, reason string, projectedStatus int, changeTime time.Time, meta json.RawMessage) error {
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
	return o.withEventMutationRetry(blogID, "close_recovered_event", func() error {
		return o.closeRecoveredEventOnce(blogID, knownEventID, changeTime)
	})
}

func (o *Orchestrator) closeRecoveredEventOnce(blogID, knownEventID int64, changeTime time.Time) error {
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
		if errors.Is(err, eventstore.ErrEventClosed) {
			if config.LegacyStatusProjectionEnabled() && tx.Tx() != nil {
				if err := db.UpdateSiteStatusTx(o.ctx, tx.Tx(), blogID, statusRunning, changeTime); err != nil {
					return fmt.Errorf("project site_status: %w", err)
				}
			}
			return tx.Commit()
		}
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

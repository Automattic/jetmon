package orchestrator

import (
	stdctx "context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	runtimemetrics "runtime/metrics"
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

var (
	nowFunc                = time.Now
	dbClaimBuckets         = db.ClaimBuckets
	dbHeartbeat            = db.Heartbeat
	dbReleaseHost          = db.ReleaseHost
	dbMarkHostDraining     = db.MarkHostDraining
	dbGetSitesForBucket    = db.GetSitesForBucket
	dbMarkSiteChecked      = db.MarkSiteChecked
	dbRecordCheckHistory   = db.RecordCheckHistory
	dbUpdateSSLExpiry      = db.UpdateSSLExpiry
	dbUpdateSiteStatus     = db.UpdateSiteStatus
	dbRecordFalsePositive  = db.RecordFalsePositive
	dbUpdateLastAlertSent  = db.UpdateLastAlertSent
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
		o.runRound()

		elapsed := time.Since(o.roundStart)
		minInterval := time.Duration(cfg.MinTimeBetweenRoundsSec) * time.Second
		if elapsed < minInterval {
			select {
			case <-time.After(minInterval - elapsed):
			case <-o.ctx.Done():
			}
		}
	}
}

// Stop signals the orchestrator to shut down after the current round.
func (o *Orchestrator) Stop() {
	o.cancel()
}

func (o *Orchestrator) runRound() {
	cfg := config.Get()

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

	// Fetch sites.
	sites, err := dbGetSitesForBucket(o.ctx, o.bucketMin, o.bucketMax, cfg.DatasetSize, cfg.UseVariableCheckIntervals)
	if err != nil {
		log.Printf("orchestrator: fetch sites failed: %v", err)
		return
	}

	if len(sites) == 0 {
		return
	}

	log.Printf("orchestrator: checking %d sites", len(sites))

	// Dispatch checks.
	dispatched := 0
	for _, site := range sites {
		timeout := cfg.NetCommsTimeout
		if site.TimeoutSeconds != nil {
			timeout = *site.TimeoutSeconds
		}

		req := checker.Request{
			BlogID:         site.BlogID,
			URL:            site.MonitorURL,
			TimeoutSeconds: timeout,
			Keyword:        site.CheckKeyword,
			CustomHeaders:  checker.ParseCustomHeaders(site.CustomHeaders),
			RedirectPolicy: checker.RedirectPolicy(site.RedirectPolicy),
		}
		if req.RedirectPolicy == "" {
			req.RedirectPolicy = checker.RedirectFollow
		}

		if o.pool.Submit(req) {
			dispatched++
		} else {
			log.Printf("orchestrator: dropped check blog_id=%d queue_depth=%d", site.BlogID, o.pool.QueueDepth())
		}
	}

	// Collect results with a deadline.
	deadline := time.NewTimer(time.Duration(cfg.NetCommsTimeout+5) * time.Second)
	defer deadline.Stop()

	results := make(map[int64]checker.Result, dispatched)
	for len(results) < dispatched {
		select {
		case res := <-o.pool.Results():
			results[res.BlogID] = res
		case <-deadline.C:
			log.Printf("orchestrator: round deadline reached, %d results outstanding", dispatched-len(results))
			goto process
		case <-o.ctx.Done():
			return
		}
	}

process:
	siteMap := make(map[int64]db.Site, len(sites))
	for _, s := range sites {
		siteMap[s.BlogID] = s
	}

	o.processResults(results, siteMap)
	o.totalChecked += len(results)

	// Emit metrics and update stats files.
	roundDuration := time.Since(o.roundStart)
	m := metricsClientFunc()
	if m != nil {
		m.Timing("round.complete.time", roundDuration)
		m.Gauge("worker.queue.active", o.pool.ActiveCount())
		m.Gauge("worker.queue.queue_size", o.pool.QueueDepth())
		m.Gauge("retry.queue.size", o.retries.size())
		m.Increment("round.sites.count", len(results))

		sps := 0
		if roundDuration.Seconds() > 0 {
			sps = int(float64(len(results)) / roundDuration.Seconds())
		}
		m.Gauge("round.sps.count", sps)

		if cfg.StatsdSendMemUsage {
			m.EmitMemStats()
		}

		metrics.WriteStatsFiles(sps, o.pool.QueueDepth(), o.totalChecked)
	}

	o.applyMemoryPressure(cfg)
}

func (o *Orchestrator) processResults(results map[int64]checker.Result, sites map[int64]db.Site) {
	for blogID, res := range results {
		site, ok := sites[blogID]
		if !ok {
			continue
		}
		if err := dbMarkSiteChecked(o.ctx, blogID, res.Timestamp); err != nil {
			log.Printf("orchestrator: mark checked blog_id=%d: %v", blogID, err)
		}

		// Log timing data.
		if err := dbRecordCheckHistory(
			blogID,
			res.HTTPCode, res.ErrorCode,
			res.RTT.Milliseconds(),
			res.DNS.Milliseconds(),
			res.TCP.Milliseconds(),
			res.TLS.Milliseconds(),
			res.TTFB.Milliseconds(),
		); err != nil {
			log.Printf("orchestrator: record history blog_id=%d: %v", blogID, err)
		}

		// Update SSL expiry if available.
		if res.SSLExpiry != nil {
			if err := dbUpdateSSLExpiry(o.ctx, blogID, *res.SSLExpiry); err != nil {
				log.Printf("orchestrator: update ssl expiry blog_id=%d: %v", blogID, err)
			}
			o.checkSSLAlerts(site, *res.SSLExpiry)
		}

		// Per-check data is recorded in jetmon_check_history (above); duplicating
		// it in jetmon_audit_log was retired with the operational/site-state split.

		if !res.IsFailure() {
			o.handleRecovery(site, res)
		} else {
			o.handleFailure(site, res)
		}
	}
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
	if cfg.WorkerMaxMemMB <= 0 {
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

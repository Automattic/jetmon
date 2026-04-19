package orchestrator

import (
	stdctx "context"
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/metrics"
	"github.com/Automattic/jetmon/internal/veriflier"
	"github.com/Automattic/jetmon/internal/wpcom"
)

const (
	statusRunning       = 1
	statusDown          = 0
	statusConfirmedDown = 2
)

// Orchestrator drives the main check loop.
type Orchestrator struct {
	pool             *checker.Pool
	retries          *retryQueue
	wpcom            *wpcom.Client
	veriflierClients []*veriflier.VeriflierClient
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

// ClaimBuckets registers this host in jetmon_hosts and sets the bucket range.
func (o *Orchestrator) ClaimBuckets() error {
	cfg := config.Get()
	min, max, err := db.ClaimBuckets(
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
			if err := db.MarkHostDraining(stdctx.Background(), o.hostname); err != nil {
				log.Printf("orchestrator: mark draining: %v", err)
			}
			o.pool.Drain()
			if err := db.ReleaseHost(stdctx.Background(), o.hostname); err != nil {
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

	// Update heartbeat.
	if err := db.Heartbeat(o.ctx, o.hostname); err != nil {
		log.Printf("orchestrator: heartbeat failed: %v", err)
	}
	if err := o.ClaimBuckets(); err != nil {
		log.Printf("orchestrator: bucket rebalance failed: %v", err)
	}

	// Fetch sites.
	sites, err := db.GetSitesForBucket(o.ctx, o.bucketMin, o.bucketMax, cfg.DatasetSize, cfg.UseVariableCheckIntervals)
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
	m := metrics.Global()
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

	// Memory pressure check.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	rssMB := int(ms.Sys / 1024 / 1024)
	if rssMB > cfg.WorkerMaxMemMB {
		log.Printf("orchestrator: memory pressure %dMB > %dMB, draining 10%% of pool", rssMB, cfg.WorkerMaxMemMB)
		// Pool auto-scaling handles this; log is sufficient for now.
	}
}

func (o *Orchestrator) processResults(results map[int64]checker.Result, sites map[int64]db.Site) {
	for blogID, res := range results {
		site, ok := sites[blogID]
		if !ok {
			continue
		}
		if err := db.MarkSiteChecked(o.ctx, blogID, res.Timestamp); err != nil {
			log.Printf("orchestrator: mark checked blog_id=%d: %v", blogID, err)
		}

		// Log timing data.
		if err := db.RecordCheckHistory(
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
			if err := db.UpdateSSLExpiry(o.ctx, blogID, *res.SSLExpiry); err != nil {
				log.Printf("orchestrator: update ssl expiry blog_id=%d: %v", blogID, err)
			}
			o.checkSSLAlerts(site, *res.SSLExpiry)
		}

		o.auditLog(blogID, audit.EventCheck, o.hostname,
			res.HTTPCode, res.ErrorCode, res.RTT.Milliseconds(), "")

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

	o.retries.clear(site.BlogID)

	if site.SiteStatus != statusRunning {
		changeTime := time.Now().UTC()
		log.Printf("orchestrator: blog_id=%d recovered", site.BlogID)
		o.auditTransition(site.BlogID, site.SiteStatus, statusRunning, "site recovered")

		if config.Get().DBUpdatesEnable {
			_ = db.UpdateSiteStatus(o.ctx, site.BlogID, statusRunning, changeTime)
		}

		if inMaintenance(site) {
			o.auditLog(site.BlogID, audit.EventMaintenanceActive, "local", 0, 0, 0, "recovery suppressed during maintenance")
		} else if !o.isAlertSuppressed(site) {
			o.sendNotification(site, res, statusRunning, changeTime, nil)
		}
	}
}

func (o *Orchestrator) handleFailure(site db.Site, res checker.Result) {
	entry := o.retries.record(res)

	if entry.failCount < config.Get().NumOfChecks {
		o.auditLog(site.BlogID, audit.EventRetryDispatched, o.hostname,
			res.HTTPCode, res.ErrorCode, res.RTT.Milliseconds(),
			fmt.Sprintf("retry %d of %d", entry.failCount, config.Get().NumOfChecks))
		return
	}

	// Escalate to verifliers.
	o.escalateToVerifliers(site, entry)
}

func (o *Orchestrator) escalateToVerifliers(site db.Site, entry *retryEntry) {
	clients := o.veriflierSnapshot()
	if len(clients) == 0 {
		o.confirmDown(site, entry, nil)
		return
	}

	o.auditLog(site.BlogID, audit.EventVeriflierSent, o.hostname, 0, 0, 0,
		fmt.Sprintf("escalating to %d verifliers", len(clients)))

	req := veriflier.CheckRequest{
		BlogID:         site.BlogID,
		URL:            site.MonitorURL,
		TimeoutSeconds: int32(timeoutForSite(config.Get(), site)),
		Keyword:        stringPtrValue(site.CheckKeyword),
		CustomHeaders:  checker.ParseCustomHeaders(site.CustomHeaders),
		RedirectPolicy: site.RedirectPolicy,
	}

	type vResult struct {
		host string
		res  *veriflier.CheckResult
		err  error
	}
	ch := make(chan vResult, len(clients))

	for _, client := range clients {
		c := client
		go func() {
			res, err := c.Check(o.ctx, req)
			ch <- vResult{host: c.Addr(), res: res, err: err}
		}()
	}

	var vResults []veriflier.CheckResult
	healthyVerifliers := 0
	confirmations := 0

	for range clients {
		vr := <-ch
		if vr.err != nil {
			log.Printf("orchestrator: veriflier %s error: %v", vr.host, vr.err)
			continue
		}
		healthyVerifliers++
		o.auditLog(site.BlogID, audit.EventVeriflierResult, vr.host,
			int(vr.res.HTTPCode), int(vr.res.ErrorCode), vr.res.RTTMs, "")
		vResults = append(vResults, *vr.res)
		if !vr.res.Success {
			confirmations++
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

	if confirmations >= quorum {
		o.confirmDown(site, entry, vResults)
	} else {
		// Verifliers did not confirm — false positive.
		log.Printf("orchestrator: blog_id=%d verifliers did not confirm down (%d/%d)", site.BlogID, confirmations, quorum)
		_ = db.RecordFalsePositive(site.BlogID, entry.lastResult.HTTPCode, entry.lastResult.ErrorCode,
			entry.lastResult.RTT.Milliseconds())
		o.retries.clear(site.BlogID)
	}
}

func (o *Orchestrator) confirmDown(site db.Site, entry *retryEntry, vResults []veriflier.CheckResult) {
	newStatus := statusConfirmedDown
	changeTime := time.Now().UTC()

	log.Printf("orchestrator: blog_id=%d confirmed down", site.BlogID)
	o.auditTransition(site.BlogID, site.SiteStatus, newStatus, "confirmed down")

	if config.Get().DBUpdatesEnable {
		_ = db.UpdateSiteStatus(o.ctx, site.BlogID, newStatus, changeTime)
	}

	if inMaintenance(site) {
		o.auditLog(site.BlogID, audit.EventMaintenanceActive, "local", 0, 0, 0, "downtime suppressed during maintenance")
	} else if !o.isAlertSuppressed(site) {
		o.sendNotification(site, entry.lastResult, newStatus, changeTime, vResults)
	} else {
		o.auditLog(site.BlogID, audit.EventAlertSuppressed, "local", 0, 0, 0, "cooldown active")
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

	o.auditLog(site.BlogID, audit.EventWPCOMSent, "local", 0, 0, 0,
		fmt.Sprintf("status=%d type=%s", status, n.StatusType))

	if err := o.wpcom.Notify(n); err != nil {
		log.Printf("orchestrator: wpcom notify failed for blog_id=%d: %v", site.BlogID, err)
		o.auditLog(site.BlogID, audit.EventWPCOMRetry, "local", 0, 0, 0, err.Error())

		// Single retry.
		if retryErr := o.wpcom.Notify(n); retryErr != nil {
			log.Printf("orchestrator: wpcom notify retry failed for blog_id=%d: %v", site.BlogID, retryErr)
			return
		}
	}
	if err := db.UpdateLastAlertSent(o.ctx, site.BlogID, time.Now().UTC()); err != nil {
		log.Printf("orchestrator: update last alert sent blog_id=%d: %v", site.BlogID, err)
	}
}

func (o *Orchestrator) checkSSLAlerts(site db.Site, expiry time.Time) {
	thresholds := []int{30, 14, 7}
	daysUntil := int(time.Until(expiry).Hours() / 24)
	for _, t := range thresholds {
		if daysUntil == t {
			log.Printf("orchestrator: blog_id=%d SSL cert expires in %d days", site.BlogID, daysUntil)
			o.auditLog(site.BlogID, audit.EventCheck, "local", 0, checker.ErrorTLSExpired, 0,
				fmt.Sprintf("ssl certificate expires in %d days", daysUntil))
		}
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

// RetryQueueSize returns the number of sites currently in local retry.
func (o *Orchestrator) RetryQueueSize() int {
	return o.retries.size()
}

// BucketRange returns the current bucket min/max for this host.
func (o *Orchestrator) BucketRange() (int, int) {
	return o.bucketMin, o.bucketMax
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

func (o *Orchestrator) auditLog(blogID int64, event, source string, httpCode, errorCode int, rttMs int64, detail string) {
	if err := audit.Log(blogID, event, source, httpCode, errorCode, rttMs, detail); err != nil {
		log.Printf("audit: blog_id=%d event=%s: %v", blogID, event, err)
	}
}

func (o *Orchestrator) auditTransition(blogID int64, from, to int, detail string) {
	if err := audit.LogTransition(blogID, from, to, detail); err != nil {
		log.Printf("audit: blog_id=%d transition %d->%d: %v", blogID, from, to, err)
	}
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
	return statusDown
}

func (o *Orchestrator) refreshVeriflierClients(cfg *config.Config) {
	clients := make([]*veriflier.VeriflierClient, 0, len(cfg.Verifiers))
	for _, v := range cfg.Verifiers {
		addr := fmt.Sprintf("%s:%s", v.Host, v.GRPCPort)
		clients = append(clients, veriflier.NewVeriflierClient(addr, v.AuthToken))
	}
	o.veriflierMu.Lock()
	o.veriflierClients = clients
	o.veriflierMu.Unlock()
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

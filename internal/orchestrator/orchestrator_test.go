package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/veriflier"
	"github.com/Automattic/jetmon/internal/wpcom"
)

var orchestratorConfigTestMu sync.Mutex

func TestIsAlertSuppressedUsesLastAlertSent(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-5 * time.Minute)
	old := now.Add(-31 * time.Minute)

	setTestConfig(t)

	o := &Orchestrator{}

	if o.isAlertSuppressed(db.Site{}) {
		t.Fatalf("zero site should not be suppressed")
	}
	if o.isAlertSuppressed(db.Site{LastAlertSentAt: &old}) {
		t.Fatalf("old alert should not be suppressed")
	}
	if !o.isAlertSuppressed(db.Site{LastAlertSentAt: &recent}) {
		t.Fatalf("recent alert should be suppressed")
	}
}

func TestTimeoutForSite(t *testing.T) {
	cfg := &config.Config{NetCommsTimeout: 10}

	if got := timeoutForSite(cfg, db.Site{}); got != 10 {
		t.Fatalf("timeoutForSite() = %d, want 10", got)
	}

	override := 3
	if got := timeoutForSite(cfg, db.Site{TimeoutSeconds: &override}); got != 3 {
		t.Fatalf("timeoutForSite() with override = %d, want 3", got)
	}
}

func TestInMaintenance(t *testing.T) {
	now := time.Now()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	if inMaintenance(db.Site{}) {
		t.Fatal("nil window should not be in maintenance")
	}
	if inMaintenance(db.Site{MaintenanceStart: &past}) {
		t.Fatal("nil end should not be in maintenance")
	}
	if inMaintenance(db.Site{MaintenanceEnd: &future}) {
		t.Fatal("nil start should not be in maintenance")
	}
	if !inMaintenance(db.Site{MaintenanceStart: &past, MaintenanceEnd: &future}) {
		t.Fatal("active window should be in maintenance")
	}
	if inMaintenance(db.Site{MaintenanceStart: &past, MaintenanceEnd: &past}) {
		t.Fatal("expired window should not be in maintenance")
	}
	if inMaintenance(db.Site{MaintenanceStart: &future, MaintenanceEnd: &future}) {
		t.Fatal("future window should not be in maintenance")
	}
}

func TestSummarizeVerifierResults(t *testing.T) {
	got := summarizeVerifierResults([]veriflier.CheckResult{
		{Host: "us-west", Success: false, HTTPCode: 500, RTTMs: 123},
		{Host: "eu", Success: true, HTTPCode: 200, RTTMs: 45},
	})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0]["host"] != "us-west" || got[0]["success"] != false ||
		got[0]["http_code"] != int32(500) || got[0]["rtt_ms"] != int64(123) {
		t.Fatalf("first summary = %+v", got[0])
	}
	if got[1]["host"] != "eu" || got[1]["success"] != true {
		t.Fatalf("second summary = %+v", got[1])
	}
}

func TestSlicesEqual(t *testing.T) {
	if !slicesEqual(nil, nil) {
		t.Fatal("nil slices should be equal")
	}
	if !slicesEqual([]string{"a", "b"}, []string{"a", "b"}) {
		t.Fatal("identical slices should be equal")
	}
	if slicesEqual([]string{"a"}, []string{"b"}) {
		t.Fatal("different content should not be equal")
	}
	if slicesEqual([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("different lengths should not be equal")
	}
}

func TestRefreshVeriflierClientsReusesUnchangedClients(t *testing.T) {
	cfg := &config.Config{
		Verifiers: []config.VerifierConfig{
			{Name: "a", Host: "host1", Port: "7803", AuthToken: "token1"},
			{Name: "b", Host: "host2", Port: "7804", AuthToken: "token2"},
		},
	}

	o := New(cfg, nil)
	before := append([]*veriflier.VeriflierClient(nil), o.veriflierClients...)

	o.refreshVeriflierClients(cfg)

	for i := range before {
		if before[i] != o.veriflierClients[i] {
			t.Fatalf("client %d was rebuilt for unchanged config", i)
		}
	}
}

func TestRefreshVeriflierClientsRebuildsChangedClients(t *testing.T) {
	cfg := &config.Config{
		Verifiers: []config.VerifierConfig{
			{Name: "a", Host: "host1", Port: "7803", AuthToken: "token1"},
		},
	}

	o := New(cfg, nil)
	before := o.veriflierClients[0]

	updated := &config.Config{
		Verifiers: []config.VerifierConfig{
			{Name: "a", Host: "host1", Port: "7803", AuthToken: "token2"},
		},
	}

	o.refreshVeriflierClients(updated)

	if before == o.veriflierClients[0] {
		t.Fatalf("client was reused after config changed")
	}
}

func TestSendNotificationRetriesAndUpdatesAlertTimestamp(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	setTestConfig(t)

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	var notifyCalls int
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		notifyCalls++
		if notifyCalls == 1 {
			return fmt.Errorf("first failure")
		}
		return nil
	}

	var updatedBlogID int64
	dbUpdateLastAlertSent = func(_ context.Context, blogID int64, _ time.Time) error {
		updatedBlogID = blogID
		return nil
	}

	o := &Orchestrator{
		wpcom:    &wpcom.Client{},
		hostname: "local-host",
		ctx:      context.Background(),
	}

	res := checkerResultSuccess(123)
	o.sendNotification(db.Site{BlogID: 123, MonitorURL: "https://example.com"}, res, statusRunning, res.Timestamp, nil)

	if notifyCalls != 2 {
		t.Fatalf("notify calls = %d, want 2", notifyCalls)
	}
	if updatedBlogID != 123 {
		t.Fatalf("updated blog_id = %d, want 123", updatedBlogID)
	}
	for stat, want := range map[string]int{
		"wpcom.notification.attempt.count":                  1,
		"wpcom.notification.status.running.attempt.count":   1,
		"wpcom.notification.error.count":                    1,
		"wpcom.notification.status.running.error.count":     1,
		"wpcom.notification.retry.count":                    1,
		"wpcom.notification.retry.delivered.count":          1,
		"wpcom.notification.delivered.count":                1,
		"wpcom.notification.status.running.delivered.count": 1,
	} {
		if got := rec.counter(stat); got != want {
			t.Fatalf("%s = %d, want %d", stat, got, want)
		}
	}
}

func TestConfirmDownSuppressedDuringCooldown(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	setTestConfig(t)

	recent := time.Now().UTC().Add(-5 * time.Minute)
	var notifyCalls int
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		notifyCalls++
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local-host",
		ctx:      context.Background(),
	}
	o.retries.record(checkerResultFailure(123))

	entry := o.retries.get(123)
	o.confirmDown(db.Site{
		BlogID:          123,
		SiteStatus:      statusRunning,
		LastAlertSentAt: &recent,
	}, entry, nil)

	if notifyCalls != 0 {
		t.Fatalf("notify calls = %d, want 0", notifyCalls)
	}
	if o.retries.get(123) != nil {
		t.Fatal("retry entry should be cleared after confirmDown")
	}
}

func TestEscalateToVerifliersConfirmsWhenQuorumReached(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.PeerOfflineLimit = 2

	var notifyCalls int
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		notifyCalls++
		return nil
	}
	dbUpdateLastAlertSent = func(context.Context, int64, time.Time) error { return nil }

	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return &veriflier.CheckResult{
			BlogID:   req.BlogID,
			Host:     c.Addr(),
			Success:  false,
			HTTPCode: 500,
		}, nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local-host",
		veriflierClients: []*veriflier.VeriflierClient{
			veriflier.NewVeriflierClient("v1", ""),
			veriflier.NewVeriflierClient("v2", ""),
		},
	}

	fail := checkerResultFailure(321)
	o.retries.record(fail)
	entry := o.retries.get(321)
	o.escalateToVerifliers(db.Site{BlogID: 321, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	if notifyCalls != 1 {
		t.Fatalf("notify calls = %d, want 1", notifyCalls)
	}
	if o.retries.get(321) != nil {
		t.Fatal("retry entry should be cleared after confirmed down")
	}
}

func TestEscalateToVerifliersRecordsFalsePositiveWhenQuorumMissed(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.PeerOfflineLimit = 2

	var falsePositiveBlogID int64
	dbRecordFalsePositive = func(blogID int64, _ int, _ int, _ int64) error {
		falsePositiveBlogID = blogID
		return nil
	}
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("notification should not be sent for false positive")
		return nil
	}

	// escalateToVerifliers fans the verifier RPC out across goroutines, so
	// `call` is read+written concurrently. Use atomic so `go test -race`
	// stays clean. The semantics — first verifier returns Success=false,
	// subsequent ones return true — are unchanged.
	var call atomic.Int64
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		n := call.Add(1)
		return &veriflier.CheckResult{
			BlogID:   req.BlogID,
			Host:     c.Addr(),
			Success:  n != 1,
			HTTPCode: 200,
		}, nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local-host",
		veriflierClients: []*veriflier.VeriflierClient{
			veriflier.NewVeriflierClient("v1", ""),
			veriflier.NewVeriflierClient("v2", ""),
		},
	}

	fail := checkerResultFailure(654)
	o.retries.record(fail)
	entry := o.retries.get(654)
	o.escalateToVerifliers(db.Site{BlogID: 654, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	if falsePositiveBlogID != 654 {
		t.Fatalf("false positive blog_id = %d, want 654", falsePositiveBlogID)
	}
	if o.retries.get(654) != nil {
		t.Fatal("retry entry should be cleared after false positive")
	}
}

func stubOrchestratorDeps() func() {
	origNow := nowFunc
	origDBClaimBuckets := dbClaimBuckets
	origDBHeartbeat := dbHeartbeat
	origDBReleaseHost := dbReleaseHost
	origDBMarkHostDraining := dbMarkHostDraining
	origDBGetSites := dbGetSitesForBucket
	origDBUpdateStatus := dbUpdateSiteStatus
	origDBUpdateLastAlert := dbUpdateLastAlertSent
	origDBRecordFalsePositive := dbRecordFalsePositive
	origDBMarkSiteChecked := dbMarkSiteChecked
	origDBMarkSitesChecked := dbMarkSitesChecked
	origDBRecordCheckHistory := dbRecordCheckHistory
	origDBRecordCheckHistories := dbRecordCheckHistories
	origDBUpdateSSLExpiry := dbUpdateSSLExpiry
	origDBCountDueSites := dbCountDueSites
	origDBCountProjectionDrift := dbCountProjectionDrift
	origNotify := wpcomNotifyFunc
	origVeriflierCheck := veriflierCheckFunc
	origMetricsClient := metricsClientFunc

	nowFunc = time.Now
	dbClaimBuckets = func(string, int, int, int) (int, int, error) { return 0, 0, nil }
	dbHeartbeat = func(context.Context, string) error { return nil }
	dbReleaseHost = func(context.Context, string) error { return nil }
	dbMarkHostDraining = func(context.Context, string) error { return nil }
	dbGetSitesForBucket = func(context.Context, int, int, int, bool) ([]db.Site, error) { return nil, nil }
	dbUpdateSiteStatus = func(context.Context, int64, int, time.Time) error { return nil }
	dbUpdateLastAlertSent = func(context.Context, int64, time.Time) error { return nil }
	dbRecordFalsePositive = func(int64, int, int, int64) error { return nil }
	dbMarkSiteChecked = func(context.Context, int64, time.Time) error { return nil }
	dbMarkSitesChecked = func(context.Context, []db.SiteCheck) error { return nil }
	dbRecordCheckHistory = func(int64, int, int, int64, int64, int64, int64, int64) error { return nil }
	dbRecordCheckHistories = func(context.Context, []db.CheckHistoryRow) error { return nil }
	dbUpdateSSLExpiry = func(context.Context, int64, time.Time) error { return nil }
	dbCountDueSites = func(context.Context, int, int, bool) (int, error) { return 0, nil }
	dbCountProjectionDrift = func(context.Context, int, int) (int, error) { return 0, nil }
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error { return nil }
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, ctx context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return c.Check(ctx, req)
	}

	return func() {
		nowFunc = origNow
		dbClaimBuckets = origDBClaimBuckets
		dbHeartbeat = origDBHeartbeat
		dbReleaseHost = origDBReleaseHost
		dbMarkHostDraining = origDBMarkHostDraining
		dbGetSitesForBucket = origDBGetSites
		dbUpdateSiteStatus = origDBUpdateStatus
		dbUpdateLastAlertSent = origDBUpdateLastAlert
		dbRecordFalsePositive = origDBRecordFalsePositive
		dbMarkSiteChecked = origDBMarkSiteChecked
		dbMarkSitesChecked = origDBMarkSitesChecked
		dbRecordCheckHistory = origDBRecordCheckHistory
		dbRecordCheckHistories = origDBRecordCheckHistories
		dbUpdateSSLExpiry = origDBUpdateSSLExpiry
		dbCountDueSites = origDBCountDueSites
		dbCountProjectionDrift = origDBCountProjectionDrift
		wpcomNotifyFunc = origNotify
		veriflierCheckFunc = origVeriflierCheck
		metricsClientFunc = origMetricsClient
	}
}

func setTestConfig(t *testing.T) *config.Config {
	t.Helper()
	orchestratorConfigTestMu.Lock()
	t.Cleanup(func() {
		_ = config.Load("../../config/config-sample.json")
		orchestratorConfigTestMu.Unlock()
	})
	if err := config.Load("../../config/config-sample.json"); err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	cfg := config.Get()
	cfg.AlertCooldownMinutes = 30
	cfg.NumOfChecks = 3
	cfg.PeerOfflineLimit = 2
	cfg.LegacyStatusProjectionEnable = false
	return cfg
}

func checkerResultSuccess(blogID int64) checker.Result {
	return checker.Result{
		BlogID:    blogID,
		Success:   true,
		Timestamp: time.Now().UTC(),
	}
}

func checkerResultFailure(blogID int64) checker.Result {
	return checker.Result{
		BlogID:    blogID,
		Success:   false,
		HTTPCode:  500,
		ErrorCode: checker.ErrorConnect,
		RTT:       100 * time.Millisecond,
		Timestamp: time.Now().UTC(),
	}
}

func TestHandleRecoverySendsNotificationWhenSiteWasDown(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var notifiedStatus int
	wpcomNotifyFunc = func(_ *wpcom.Client, n wpcom.Notification) error {
		notifiedStatus = n.StatusID
		return nil
	}
	dbUpdateLastAlertSent = func(context.Context, int64, time.Time) error { return nil }

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	o.handleRecovery(db.Site{BlogID: 1, SiteStatus: statusConfirmedDown}, checkerResultSuccess(1))

	if notifiedStatus != statusRunning {
		t.Fatalf("notification StatusID = %d, want %d (statusRunning)", notifiedStatus, statusRunning)
	}
	if o.retries.get(1) != nil {
		t.Fatal("retry entry should be cleared after recovery")
	}
}

func TestHandleRecoveryIsNoopWhenSiteAlreadyRunning(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var notifyCalls int
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		notifyCalls++
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	// No retry entry, site already running — should be a complete no-op.
	o.handleRecovery(db.Site{BlogID: 1, SiteStatus: statusRunning}, checkerResultSuccess(1))

	if notifyCalls != 0 {
		t.Fatalf("notify calls = %d, want 0", notifyCalls)
	}
}

func TestHandleRecoveryClearsRetryEntryEvenWhenAlreadyRunning(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	// Site has a stale retry entry (e.g. from a previous partial failure) but
	// is now reported as running. The entry must be cleared.
	o.retries.record(checkerResultFailure(1))
	o.handleRecovery(db.Site{BlogID: 1, SiteStatus: statusRunning}, checkerResultSuccess(1))

	if o.retries.get(1) != nil {
		t.Fatal("stale retry entry should be cleared on recovery even when status was already running")
	}
}

func TestHandleRecoveryEmitsProbeClearedClassMetric(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}
	o.retries.record(checkerResultFailure(42))

	o.handleRecovery(db.Site{BlogID: 42, SiteStatus: statusDown}, checkerResultSuccess(42))

	if got := rec.counter("detection.probe_cleared.count"); got != 1 {
		t.Fatalf("probe-cleared counter = %d, want 1", got)
	}
	if got := rec.counter("detection.probe_cleared.server.count"); got != 1 {
		t.Fatalf("probe-cleared server counter = %d, want 1", got)
	}
	if got := rec.timingCount("detection.seems_down_to_probe_cleared.time"); got != 1 {
		t.Fatalf("probe-cleared timing count = %d, want 1", got)
	}
}

func TestHandleFailureBelowThresholdDoesNotEscalate(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)
	config.Get().NumOfChecks = 3

	var escalated bool
	veriflierCheckFunc = func(_ *veriflier.VeriflierClient, _ context.Context, _ veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		escalated = true
		return &veriflier.CheckResult{Success: false}, nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	// First failure only — failCount (1) < NumOfChecks (3).
	o.handleFailure(db.Site{BlogID: 1}, checkerResultFailure(1))

	if escalated {
		t.Fatal("escalated to verifliers after only 1 failure, want NumOfChecks (3) failures first")
	}
	if o.retries.get(1) == nil {
		t.Fatal("retry entry should exist after first failure")
	}
}

func TestProcessResultsMarksChecked(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var markedBlogID int64
	dbMarkSitesChecked = func(_ context.Context, checks []db.SiteCheck) error {
		if len(checks) != 1 {
			t.Fatalf("batch checks = %d, want 1", len(checks))
		}
		markedBlogID = checks[0].BlogID
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	res := checkerResultSuccess(42)
	sites := map[int64]db.Site{42: {BlogID: 42, SiteStatus: statusRunning}}
	o.processResults(map[int64]checker.Result{42: res}, sites)

	if markedBlogID != 42 {
		t.Fatalf("MarkSitesChecked blog_id = %d, want 42", markedBlogID)
	}
}

func TestProcessResultsFallsBackWhenBatchWritesFail(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	dbMarkSitesChecked = func(context.Context, []db.SiteCheck) error {
		return fmt.Errorf("batch mark failed")
	}
	dbRecordCheckHistories = func(context.Context, []db.CheckHistoryRow) error {
		return fmt.Errorf("batch history failed")
	}

	var fallbackMarked int64
	dbMarkSiteChecked = func(_ context.Context, blogID int64, _ time.Time) error {
		fallbackMarked = blogID
		return nil
	}
	var fallbackHistory int64
	dbRecordCheckHistory = func(blogID int64, _ int, _ int, _ int64, _ int64, _ int64, _ int64, _ int64) error {
		fallbackHistory = blogID
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	summary := o.processResults(
		map[int64]checker.Result{42: checkerResultSuccess(42)},
		map[int64]db.Site{42: {BlogID: 42, SiteStatus: statusRunning}},
	)

	if fallbackMarked != 42 || fallbackHistory != 42 {
		t.Fatalf("fallback marked/history = %d/%d, want 42/42", fallbackMarked, fallbackHistory)
	}
	if summary.markCheckedRows != 1 || summary.historyRows != 1 {
		t.Fatalf("fallback rows = %d/%d, want 1/1", summary.markCheckedRows, summary.historyRows)
	}
	if summary.markCheckedErrors != 1 || summary.historyErrors != 1 {
		t.Fatalf("batch errors = %d/%d, want 1/1", summary.markCheckedErrors, summary.historyErrors)
	}
}

func TestProcessResultsSkipsUnknownSite(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var markCalled bool
	dbMarkSitesChecked = func(_ context.Context, _ []db.SiteCheck) error {
		markCalled = true
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	res := checkerResultSuccess(99)
	o.processResults(map[int64]checker.Result{99: res}, map[int64]db.Site{})

	if markCalled {
		t.Fatal("MarkSitesChecked called for unknown blog_id, want skipped")
	}
}

func TestProcessResultsUpdatesSSLExpiry(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var updatedExpiry time.Time
	dbUpdateSSLExpiry = func(_ context.Context, _ int64, expiry time.Time) error {
		updatedExpiry = expiry
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	expiry := time.Now().Add(30 * 24 * time.Hour)
	res := checkerResultSuccess(1)
	res.SSLExpiry = &expiry

	sites := map[int64]db.Site{1: {BlogID: 1, SiteStatus: statusRunning}}
	o.processResults(map[int64]checker.Result{1: res}, sites)

	if updatedExpiry.IsZero() {
		t.Fatal("UpdateSSLExpiry not called")
	}
}

func TestShouldUpdateSSLExpiryComparesStoredDate(t *testing.T) {
	stored := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	sameDate := time.Date(2026, 5, 2, 23, 59, 0, 0, time.UTC)
	nextDate := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)

	if !shouldUpdateSSLExpiry(nil, sameDate) {
		t.Fatal("nil stored expiry should update")
	}
	if shouldUpdateSSLExpiry(&stored, sameDate) {
		t.Fatal("same stored expiry date should not update")
	}
	if !shouldUpdateSSLExpiry(&stored, nextDate) {
		t.Fatal("different stored expiry date should update")
	}
}

func TestCheckSSLAlertsAtThresholds(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	o := &Orchestrator{hostname: "local"}

	// Test each threshold and a non-threshold day; verify no panic.
	for _, days := range []int{30, 14, 7, 31, 15} {
		expiry := time.Now().Add(time.Duration(days)*24*time.Hour + 30*time.Minute)
		o.checkSSLAlerts(db.Site{BlogID: 1}, expiry)
	}
}

func TestApplyMemoryPressureNoActionBelowLimit(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	origFn := currentMemoryMBFunc
	currentMemoryMBFunc = func() int { return 10 }
	defer func() { currentMemoryMBFunc = origFn }()

	p := checker.NewPool(5, 1, 5)
	t.Cleanup(p.Drain)

	o := &Orchestrator{pool: p, ctx: context.Background()}
	cfg := config.Get()
	cfg.WorkerMaxMemMB = 100

	o.applyMemoryPressure(cfg)

	if p.WorkerCount() != 5 {
		t.Fatalf("WorkerCount = %d under limit, want 5", p.WorkerCount())
	}
}

func TestApplyMemoryPressureNoActionWhenDisabled(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	origFn := currentMemoryMBFunc
	currentMemoryMBFunc = func() int { return 9999 }
	defer func() { currentMemoryMBFunc = origFn }()

	p := checker.NewPool(5, 1, 5)
	t.Cleanup(p.Drain)

	o := &Orchestrator{pool: p, ctx: context.Background()}
	cfg := config.Get()
	cfg.WorkerMaxMemMB = 0

	o.applyMemoryPressure(cfg)

	if p.WorkerCount() != 5 {
		t.Fatalf("WorkerCount = %d when disabled, want 5", p.WorkerCount())
	}
}

func TestApplyMemoryPressureDrainsWorkersOverLimit(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	origFn := currentMemoryMBFunc
	currentMemoryMBFunc = func() int { return 500 }
	defer func() { currentMemoryMBFunc = origFn }()

	p := checker.NewPool(10, 1, 10)
	t.Cleanup(p.Drain)

	o := &Orchestrator{pool: p, ctx: context.Background()}
	cfg := config.Get()
	cfg.WorkerMaxMemMB = 50

	initial := p.WorkerCount()
	o.applyMemoryPressure(cfg)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.WorkerCount() < initial {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("WorkerCount = %d after memory pressure, want < %d", p.WorkerCount(), initial)
}

func TestOrchestratorAccessors(t *testing.T) {
	p := checker.NewPool(3, 1, 3)
	defer p.Drain()

	o := &Orchestrator{
		retries:   newRetryQueue(),
		bucketMin: 10,
		bucketMax: 99,
		pool:      p,
	}
	o.retries.record(checkerResultFailure(1))

	if o.RetryQueueSize() != 1 {
		t.Fatalf("RetryQueueSize() = %d, want 1", o.RetryQueueSize())
	}
	min, max := o.BucketRange()
	if min != 10 || max != 99 {
		t.Fatalf("BucketRange() = %d-%d, want 10-99", min, max)
	}
	if o.WorkerCount() != 3 {
		t.Fatalf("WorkerCount() = %d, want 3", o.WorkerCount())
	}
	if o.ActiveChecks() != 0 {
		t.Fatalf("ActiveChecks() = %d, want 0", o.ActiveChecks())
	}
	if o.QueueDepth() != 0 {
		t.Fatalf("QueueDepth() = %d, want 0", o.QueueDepth())
	}
}

func TestClaimBucketsUsesPinnedRangeWithoutHostTable(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	min, max := 12, 34
	cfg.PinnedBucketMin = &min
	cfg.PinnedBucketMax = &max

	var dynamicClaimCalled bool
	dbClaimBuckets = func(string, int, int, int) (int, int, error) {
		dynamicClaimCalled = true
		return 0, 0, nil
	}

	o := &Orchestrator{hostname: "host-a"}
	if err := o.ClaimBuckets(); err != nil {
		t.Fatalf("ClaimBuckets: %v", err)
	}
	if dynamicClaimCalled {
		t.Fatal("ClaimBuckets called dynamic jetmon_hosts claim in pinned mode")
	}
	if o.bucketMin != 12 || o.bucketMax != 34 {
		t.Fatalf("bucket range = %d-%d, want 12-34", o.bucketMin, o.bucketMax)
	}
}

func TestRunRoundSkipsHeartbeatWhenPinned(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	min, max := 12, 34
	cfg.PinnedBucketMin = &min
	cfg.PinnedBucketMax = &max

	var heartbeatCalled bool
	dbHeartbeat = func(context.Context, string) error {
		heartbeatCalled = true
		return nil
	}
	dbGetSitesForBucket = func(_ context.Context, gotMin, gotMax, _ int, _ bool) ([]db.Site, error) {
		if gotMin != 12 || gotMax != 34 {
			t.Fatalf("fetch buckets = %d-%d, want 12-34", gotMin, gotMax)
		}
		return nil, nil
	}

	o := &Orchestrator{ctx: context.Background(), hostname: "host-a"}
	o.runRound()

	if heartbeatCalled {
		t.Fatal("runRound updated jetmon_hosts heartbeat in pinned mode")
	}
}

func TestRunRoundDrainsAllPagesUntilWorkWraps(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.DatasetSize = 2
	cfg.NetCommsTimeout = 1
	cfg.MinTimeBetweenRoundsSec = 0
	cfg.WorkerMaxMemMB = 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sites := []db.Site{
		{BlogID: 1, MonitorURL: srv.URL},
		{BlogID: 2, MonitorURL: srv.URL},
		{BlogID: 3, MonitorURL: srv.URL},
		{BlogID: 4, MonitorURL: srv.URL},
		{BlogID: 5, MonitorURL: srv.URL},
	}

	checked := make(map[int64]bool)
	var marked []int64
	var queries int
	dbGetSitesForBucket = func(_ context.Context, _, _ int, batchSize int, useVariableIntervals bool) ([]db.Site, error) {
		if batchSize != 2 {
			t.Fatalf("batch size = %d, want 2", batchSize)
		}
		if useVariableIntervals {
			t.Fatal("useVariableIntervals = true, want false")
		}
		queries++
		return nextSchedulerTestPage(sites, checked, batchSize, false), nil
	}
	dbMarkSitesChecked = func(_ context.Context, checks []db.SiteCheck) error {
		for _, check := range checks {
			checked[check.BlogID] = true
			marked = append(marked, check.BlogID)
		}
		return nil
	}
	dbCountDueSites = func(_ context.Context, _, _ int, useVariableIntervals bool) (int, error) {
		if useVariableIntervals {
			t.Fatal("count due useVariableIntervals = true, want false")
		}
		return len(sites), nil
	}

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	p := checker.NewPool(2, 1, 2)
	defer p.Drain()
	o := &Orchestrator{
		pool:       p,
		retries:    newRetryQueue(),
		ctx:        context.Background(),
		hostname:   "host-a",
		roundStart: time.Now(),
	}

	summary := o.runRound()
	if summary.selected != 5 || summary.dispatched != 5 || summary.completed != 5 {
		t.Fatalf("summary selected/dispatched/completed = %d/%d/%d, want 5/5/5", summary.selected, summary.dispatched, summary.completed)
	}
	if summary.pagesFetched != 3 {
		t.Fatalf("pages fetched = %d, want 3", summary.pagesFetched)
	}
	if len(marked) != 5 {
		t.Fatalf("marked checked = %d, want 5", len(marked))
	}
	if queries < 4 {
		t.Fatalf("queries = %d, want at least 4 including wrap-stop query", queries)
	}
	if got := rec.gauge("scheduler.round.selected.count"); got != 5 {
		t.Fatalf("selected metric = %d, want 5", got)
	}
	if got := rec.gauge("scheduler.round.completed.count"); got != 5 {
		t.Fatalf("completed metric = %d, want 5", got)
	}
	if got := rec.gauge("scheduler.round.due_remaining.count"); got != 0 {
		t.Fatalf("due remaining metric = %d, want 0", got)
	}
}

func TestRunRoundWaitsUnderPoolBackpressureInsteadOfDropping(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.DatasetSize = 5
	cfg.NetCommsTimeout = 1
	cfg.MinTimeBetweenRoundsSec = 0
	cfg.UseVariableCheckIntervals = true
	cfg.WorkerMaxMemMB = 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(25 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sites := []db.Site{
		{BlogID: 1, MonitorURL: srv.URL},
		{BlogID: 2, MonitorURL: srv.URL},
		{BlogID: 3, MonitorURL: srv.URL},
		{BlogID: 4, MonitorURL: srv.URL},
		{BlogID: 5, MonitorURL: srv.URL},
	}

	checked := make(map[int64]bool)
	dbGetSitesForBucket = func(_ context.Context, _, _ int, batchSize int, useVariableIntervals bool) ([]db.Site, error) {
		if batchSize != 5 {
			t.Fatalf("batch size = %d, want 5", batchSize)
		}
		if !useVariableIntervals {
			t.Fatal("useVariableIntervals = false, want true")
		}
		return nextSchedulerTestPage(sites, checked, batchSize, true), nil
	}
	dbMarkSitesChecked = func(_ context.Context, checks []db.SiteCheck) error {
		for _, check := range checks {
			checked[check.BlogID] = true
		}
		return nil
	}
	dbCountDueSites = func(_ context.Context, _, _ int, useVariableIntervals bool) (int, error) {
		if !useVariableIntervals {
			t.Fatal("count due useVariableIntervals = false, want true")
		}
		count := 0
		for _, site := range sites {
			if !checked[site.BlogID] {
				count++
			}
		}
		return count, nil
	}

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	p := checker.NewPool(1, 1, 1)
	defer p.Drain()
	o := &Orchestrator{
		pool:       p,
		retries:    newRetryQueue(),
		ctx:        context.Background(),
		hostname:   "host-a",
		roundStart: time.Now(),
	}

	summary := o.runRound()
	if summary.selected != 5 || summary.dispatched != 5 || summary.completed != 5 {
		t.Fatalf("summary selected/dispatched/completed = %d/%d/%d, want 5/5/5", summary.selected, summary.dispatched, summary.completed)
	}
	if summary.backpressureWaits == 0 {
		t.Fatal("backpressure waits = 0, want > 0")
	}
	if got := rec.counter("scheduler.dispatch.backpressure_wait.count"); got == 0 {
		t.Fatal("backpressure metric = 0, want > 0")
	}
	if got := rec.gauge("scheduler.round.outstanding.count"); got != 0 {
		t.Fatalf("outstanding metric = %d, want 0", got)
	}
	if got := rec.gauge("scheduler.round.due_remaining.count"); got != 0 {
		t.Fatalf("due remaining metric = %d, want 0", got)
	}
}

func TestSchedulerSleepDurationUsesShortPollForVariableIntervals(t *testing.T) {
	cfg := &config.Config{
		MinTimeBetweenRoundsSec:   300,
		UseVariableCheckIntervals: true,
	}
	if got := schedulerSleepDuration(cfg, roundSummary{}, time.Second); got != schedulerVariableIntervalPollInterval {
		t.Fatalf("schedulerSleepDuration(variable) = %v, want %v", got, schedulerVariableIntervalPollInterval)
	}

	cfg.UseVariableCheckIntervals = false
	if got := schedulerSleepDuration(cfg, roundSummary{}, time.Second); got != 299*time.Second {
		t.Fatalf("schedulerSleepDuration(fixed) = %v, want 299s", got)
	}

	if got := schedulerSleepDuration(cfg, roundSummary{dueRemaining: 1}, time.Second); got != schedulerBacklogPollInterval {
		t.Fatalf("schedulerSleepDuration(backlog) = %v, want %v", got, schedulerBacklogPollInterval)
	}

	if got := schedulerSleepDuration(cfg, roundSummary{}, 301*time.Second); got != 0 {
		t.Fatalf("schedulerSleepDuration(elapsed) = %v, want 0", got)
	}
}

func nextSchedulerTestPage(sites []db.Site, checked map[int64]bool, batchSize int, dueOnly bool) []db.Site {
	out := make([]db.Site, 0, batchSize)
	for _, site := range sites {
		if checked[site.BlogID] {
			continue
		}
		out = append(out, site)
		if len(out) == batchSize {
			return out
		}
	}
	if dueOnly {
		return out
	}
	for _, site := range sites {
		if !checked[site.BlogID] {
			continue
		}
		out = append(out, site)
		if len(out) == batchSize {
			return out
		}
	}
	return out
}

func TestRetryQueueAllBlogIDs(t *testing.T) {
	q := newRetryQueue()
	q.record(checkerResultFailure(1))
	q.record(checkerResultFailure(2))
	q.record(checkerResultFailure(3))

	ids := q.allBlogIDs()
	if len(ids) != 3 {
		t.Fatalf("allBlogIDs() len = %d, want 3", len(ids))
	}
}

func TestStringPtrValue(t *testing.T) {
	if got := stringPtrValue(nil); got != "" {
		t.Fatalf("stringPtrValue(nil) = %q, want empty", got)
	}
	s := "hello"
	if got := stringPtrValue(&s); got != "hello" {
		t.Fatalf("stringPtrValue(&\"hello\") = %q, want hello", got)
	}
}

func TestStatusFromBool(t *testing.T) {
	if got := statusFromBool(true); got != statusRunning {
		t.Fatalf("statusFromBool(true) = %d, want %d", got, statusRunning)
	}
	if got := statusFromBool(false); got != 0 {
		t.Fatalf("statusFromBool(false) = %d, want 0", got)
	}
}

func TestIsAlertSuppressedCustomCooldown(t *testing.T) {
	setTestConfig(t)

	recent := time.Now().UTC().Add(-2 * time.Minute)
	customCooldown := 60

	o := &Orchestrator{}
	// Custom per-site cooldown of 60 min, last alert 2 min ago → suppressed.
	if !o.isAlertSuppressed(db.Site{LastAlertSentAt: &recent, AlertCooldownMinutes: &customCooldown}) {
		t.Fatal("expected suppressed with custom 60-min cooldown and 2-min-old alert")
	}
	// Custom cooldown of 0 → never suppressed.
	zeroCooldown := 0
	if o.isAlertSuppressed(db.Site{LastAlertSentAt: &recent, AlertCooldownMinutes: &zeroCooldown}) {
		t.Fatal("expected not suppressed when custom cooldown = 0")
	}
}

func TestCheckLegacyProjectionDriftEmitsGaugeAndWarningCounter(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.LegacyStatusProjectionEnable = true

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }
	dbCountProjectionDrift = func(_ context.Context, bucketMin, bucketMax int) (int, error) {
		if bucketMin != 10 || bucketMax != 20 {
			t.Fatalf("drift check buckets = %d-%d, want 10-20", bucketMin, bucketMax)
		}
		return 3, nil
	}

	o := &Orchestrator{ctx: context.Background(), bucketMin: 10, bucketMax: 20}
	o.checkLegacyProjectionDrift(cfg)

	if got := rec.gauge("projection.drift.count"); got != 3 {
		t.Fatalf("projection.drift.count = %d, want 3", got)
	}
	if got := rec.counter("projection.drift.detected.count"); got != 1 {
		t.Fatalf("projection.drift.detected.count = %d, want 1", got)
	}
}

func TestCheckLegacyProjectionDriftSkipsWhenProjectionDisabled(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.LegacyStatusProjectionEnable = false

	var called bool
	dbCountProjectionDrift = func(context.Context, int, int) (int, error) {
		called = true
		return 0, nil
	}

	o := &Orchestrator{ctx: context.Background()}
	o.checkLegacyProjectionDrift(cfg)
	if called {
		t.Fatal("drift check should be skipped when legacy projection is disabled")
	}
}

func TestCheckLegacyProjectionDriftEmitsErrorCounter(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.LegacyStatusProjectionEnable = true

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }
	dbCountProjectionDrift = func(context.Context, int, int) (int, error) {
		return 0, fmt.Errorf("db failed")
	}

	o := &Orchestrator{ctx: context.Background()}
	o.checkLegacyProjectionDrift(cfg)
	if got := rec.counter("projection.drift.check_error.count"); got != 1 {
		t.Fatalf("projection.drift.check_error.count = %d, want 1", got)
	}
}

func TestSendNotificationBothRetriesFail(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	calls := 0
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		calls++
		return fmt.Errorf("always fails")
	}

	var updateAlertCalled bool
	dbUpdateLastAlertSent = func(context.Context, int64, time.Time) error {
		updateAlertCalled = true
		return nil
	}

	o := &Orchestrator{
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}
	o.sendNotification(db.Site{BlogID: 1, MonitorURL: "https://example.com"}, checkerResultFailure(1), statusConfirmedDown, time.Now(), nil)

	if calls != 2 {
		t.Fatalf("notify calls = %d, want 2 (initial + retry)", calls)
	}
	if updateAlertCalled {
		t.Fatal("dbUpdateLastAlertSent should not be called when both retries fail")
	}
	for stat, want := range map[string]int{
		"wpcom.notification.attempt.count":                         1,
		"wpcom.notification.status.confirmed_down.attempt.count":   1,
		"wpcom.notification.error.count":                           2,
		"wpcom.notification.status.confirmed_down.error.count":     2,
		"wpcom.notification.retry.count":                           1,
		"wpcom.notification.failed.count":                          1,
		"wpcom.notification.status.confirmed_down.failed.count":    1,
		"wpcom.notification.delivered.count":                       0,
		"wpcom.notification.status.confirmed_down.delivered.count": 0,
	} {
		if got := rec.counter(stat); got != want {
			t.Fatalf("%s = %d, want %d", stat, got, want)
		}
	}
}

func TestEscalateToVerifliersNoClients(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var confirmed bool
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		confirmed = true
		return nil
	}
	dbUpdateLastAlertSent = func(context.Context, int64, time.Time) error { return nil }

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local",
		// veriflierClients is empty
	}
	fail := checkerResultFailure(55)
	o.retries.record(fail)
	entry := o.retries.get(55)
	o.escalateToVerifliers(db.Site{BlogID: 55, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	if !confirmed {
		t.Fatal("expected confirmDown (and notification) when no verifliers are configured")
	}
}

func TestConfirmDownInMaintenance(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("notification should not be sent during maintenance")
		return nil
	}

	now := time.Now()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local",
	}
	fail := checkerResultFailure(77)
	o.retries.record(fail)
	entry := o.retries.get(77)

	o.confirmDown(db.Site{
		BlogID:           77,
		SiteStatus:       statusRunning,
		MaintenanceStart: &past,
		MaintenanceEnd:   &future,
	}, entry, nil)

	if o.retries.get(77) != nil {
		t.Fatal("retry entry should be cleared after confirmDown in maintenance")
	}
}

func TestHandleRecoveryInMaintenance(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("notification should not be sent during maintenance")
		return nil
	}

	now := time.Now()
	past := now.Add(-1 * time.Hour)
	future := now.Add(1 * time.Hour)

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local",
	}

	o.handleRecovery(db.Site{
		BlogID:           1,
		SiteStatus:       statusConfirmedDown,
		MaintenanceStart: &past,
		MaintenanceEnd:   &future,
	}, checkerResultSuccess(1))
}

func TestProcessResultsLogsErrorsFromDB(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	// Make all DB calls return errors to exercise the log.Printf branches in processResults.
	dbMarkSitesChecked = func(context.Context, []db.SiteCheck) error {
		return fmt.Errorf("batch mark checked error")
	}
	dbMarkSiteChecked = func(context.Context, int64, time.Time) error {
		return fmt.Errorf("mark checked error")
	}
	dbRecordCheckHistories = func(context.Context, []db.CheckHistoryRow) error {
		return fmt.Errorf("batch history error")
	}
	dbRecordCheckHistory = func(int64, int, int, int64, int64, int64, int64, int64) error {
		return fmt.Errorf("history error")
	}
	dbUpdateSSLExpiry = func(context.Context, int64, time.Time) error {
		return fmt.Errorf("ssl expiry error")
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	expiry := time.Now().Add(30 * 24 * time.Hour)
	res := checkerResultSuccess(1)
	res.SSLExpiry = &expiry
	sites := map[int64]db.Site{1: {BlogID: 1, SiteStatus: statusRunning}}

	// Should not panic despite all DB calls failing.
	o.processResults(map[int64]checker.Result{1: res}, sites)
}

func TestHandleFailureEscalatesAfterThreshold(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)
	cfg := config.Get()
	cfg.NumOfChecks = 2
	cfg.PeerOfflineLimit = 1

	var escalated bool
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error { return nil }
	dbUpdateLastAlertSent = func(context.Context, int64, time.Time) error { return nil }
	veriflierCheckFunc = func(_ *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		escalated = true
		return &veriflier.CheckResult{BlogID: req.BlogID, Success: false, HTTPCode: 500}, nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
		veriflierClients: []*veriflier.VeriflierClient{
			veriflier.NewVeriflierClient("v1", ""),
		},
	}

	// Two failures reaches NumOfChecks (2) and triggers escalation.
	for range cfg.NumOfChecks {
		o.handleFailure(db.Site{BlogID: 1, SiteStatus: statusRunning}, checkerResultFailure(1))
	}

	if !escalated {
		t.Fatal("expected escalation to verifliers after NumOfChecks failures")
	}
}

func TestHandleFailureEmitsSeemsDownMetrics(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	firstFailureAt := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return firstFailureAt.Add(2 * time.Second) }

	res := checkerResultFailure(42)
	res.Timestamp = firstFailureAt

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local-host",
		ctx:      context.Background(),
	}
	o.handleFailure(db.Site{BlogID: 42, MonitorURL: "https://example.com", SiteStatus: statusRunning}, res)

	if got := rec.counter("detection.seems_down.open.count"); got != 1 {
		t.Fatalf("seems-down open counter = %d, want 1", got)
	}
	if got := rec.counter("detection.failure.server.count"); got != 1 {
		t.Fatalf("failure class counter = %d, want 1", got)
	}
	if got := rec.counter("detection.seems_down.open.server.count"); got != 1 {
		t.Fatalf("seems-down class counter = %d, want 1", got)
	}
	if got := rec.timingCount("detection.first_failure_to_seems_down.time"); got != 1 {
		t.Fatalf("first failure timing count = %d, want 1", got)
	}
}

func TestEscalateToVerifliersEmitsConfirmedMetrics(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.PeerOfflineLimit = 1

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error { return nil }
	dbUpdateLastAlertSent = func(context.Context, int64, time.Time) error { return nil }
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return &veriflier.CheckResult{
			BlogID:    req.BlogID,
			Host:      c.Addr(),
			Success:   false,
			HTTPCode:  500,
			RequestID: req.RequestID,
		}, nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local-host",
		veriflierClients: []*veriflier.VeriflierClient{
			veriflier.NewVeriflierClient("v1", ""),
		},
	}

	fail := checkerResultFailure(321)
	o.retries.record(fail)
	entry := o.retries.get(321)
	o.escalateToVerifliers(db.Site{BlogID: 321, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	for stat, want := range map[string]int{
		"detection.verifier.escalation.count":      1,
		"verifier.rpc.success.count":               1,
		"verifier.host.v1.rpc.success.count":       1,
		"verifier.vote.confirm_down.count":         1,
		"verifier.host.v1.vote.confirm_down.count": 1,
		"detection.verifier.quorum_met.count":      1,
		"detection.down.confirmed.count":           1,
		"detection.down.confirmed.server.count":    1,
	} {
		if got := rec.counter(stat); got != want {
			t.Fatalf("%s = %d, want %d", stat, got, want)
		}
	}
	for _, stat := range []string{
		"detection.first_failure_to_verification.time",
		"verifier.rpc.duration",
		"verifier.host.v1.rpc.duration",
		"detection.seems_down_to_down.time",
	} {
		if got := rec.timingCount(stat); got != 1 {
			t.Fatalf("%s timing count = %d, want 1", stat, got)
		}
	}
}

func TestEscalateToVerifliersEmitsFalseAlarmMetrics(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.PeerOfflineLimit = 1

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	dbRecordFalsePositive = func(int64, int, int, int64) error { return nil }
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("notification should not be sent for false alarm")
		return nil
	}
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return &veriflier.CheckResult{
			BlogID:    req.BlogID,
			Host:      c.Addr(),
			Success:   true,
			HTTPCode:  200,
			RequestID: req.RequestID,
		}, nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local-host",
		veriflierClients: []*veriflier.VeriflierClient{
			veriflier.NewVeriflierClient("v1", ""),
		},
	}

	fail := checkerResultFailure(654)
	o.retries.record(fail)
	entry := o.retries.get(654)
	o.escalateToVerifliers(db.Site{BlogID: 654, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	for stat, want := range map[string]int{
		"detection.verifier.escalation.count":         1,
		"verifier.rpc.success.count":                  1,
		"verifier.host.v1.rpc.success.count":          1,
		"verifier.vote.disagree.count":                1,
		"verifier.host.v1.vote.disagree.count":        1,
		"detection.verifier.false_alarm.count":        1,
		"detection.verifier.false_alarm.server.count": 1,
	} {
		if got := rec.counter(stat); got != want {
			t.Fatalf("%s = %d, want %d", stat, got, want)
		}
	}
	if got := rec.timingCount("detection.seems_down_to_false_alarm.time"); got != 1 {
		t.Fatalf("false alarm timing count = %d, want 1", got)
	}
}

func TestMetricSegment(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "", want: "unknown"},
		{in: "server", want: "server"},
		{in: "US-West:7803", want: "us_west_7803"},
		{in: "  eu.central-1  ", want: "eu_central_1"},
		{in: "://", want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := metricSegment(tt.in); got != tt.want {
				t.Fatalf("metricSegment(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestWPCOMStatusMetricSegment(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{status: statusDown, want: "down"},
		{status: statusRunning, want: "running"},
		{status: statusConfirmedDown, want: "confirmed_down"},
		{status: 99, want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := wpcomStatusMetricSegment(tt.status); got != tt.want {
				t.Fatalf("wpcomStatusMetricSegment(%d) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

type recordingMetrics struct {
	mu       sync.Mutex
	counters map[string]int
	gauges   map[string]int
	timings  map[string][]time.Duration
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{
		counters: make(map[string]int),
		gauges:   make(map[string]int),
		timings:  make(map[string][]time.Duration),
	}
}

func (r *recordingMetrics) Increment(stat string, value int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[stat] += value
}

func (r *recordingMetrics) Gauge(stat string, value int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[stat] = value
}

func (r *recordingMetrics) Timing(stat string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timings[stat] = append(r.timings[stat], d)
}

func (r *recordingMetrics) EmitMemStats() {}

func (r *recordingMetrics) counter(stat string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters[stat]
}

func (r *recordingMetrics) gauge(stat string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gauges[stat]
}

func (r *recordingMetrics) timingCount(stat string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.timings[stat])
}

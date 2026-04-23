package orchestrator

import (
	"context"
	"fmt"
	"sync"
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
			{Name: "a", Host: "host1", GRPCPort: "7803", AuthToken: "token1"},
			{Name: "b", Host: "host2", GRPCPort: "7804", AuthToken: "token2"},
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
			{Name: "a", Host: "host1", GRPCPort: "7803", AuthToken: "token1"},
		},
	}

	o := New(cfg, nil)
	before := o.veriflierClients[0]

	updated := &config.Config{
		Verifiers: []config.VerifierConfig{
			{Name: "a", Host: "host1", GRPCPort: "7803", AuthToken: "token2"},
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

	call := 0
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		call++
		return &veriflier.CheckResult{
			BlogID:   req.BlogID,
			Host:     c.Addr(),
			Success:  call != 1,
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
	origDBUpdateStatus := dbUpdateSiteStatus
	origDBUpdateLastAlert := dbUpdateLastAlertSent
	origDBRecordFalsePositive := dbRecordFalsePositive
	origDBMarkSiteChecked := dbMarkSiteChecked
	origDBRecordCheckHistory := dbRecordCheckHistory
	origDBUpdateSSLExpiry := dbUpdateSSLExpiry
	origDBOpenSiteEvent := dbOpenSiteEvent
	origDBUpgradeOpenSiteEvent := dbUpgradeOpenSiteEvent
	origDBCloseOpenSiteEvent := dbCloseOpenSiteEvent
	origNotify := wpcomNotifyFunc
	origVeriflierCheck := veriflierCheckFunc

	nowFunc = time.Now
	dbUpdateSiteStatus = func(context.Context, int64, int, time.Time) error { return nil }
	dbUpdateLastAlertSent = func(context.Context, int64, time.Time) error { return nil }
	dbRecordFalsePositive = func(int64, int, int, int64) error { return nil }
	dbMarkSiteChecked = func(context.Context, int64, time.Time) error { return nil }
	dbRecordCheckHistory = func(int64, int, int, int64, int64, int64, int64, int64) error { return nil }
	dbUpdateSSLExpiry = func(context.Context, int64, time.Time) error { return nil }
	dbOpenSiteEvent = func(context.Context, int64, uint8, uint8, time.Time) error { return nil }
	dbUpgradeOpenSiteEvent = func(context.Context, int64, uint8, uint8) error { return nil }
	dbCloseOpenSiteEvent = func(context.Context, int64, time.Time) error { return nil }
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error { return nil }
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, ctx context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return c.Check(ctx, req)
	}

	return func() {
		nowFunc = origNow
		dbUpdateSiteStatus = origDBUpdateStatus
		dbUpdateLastAlertSent = origDBUpdateLastAlert
		dbRecordFalsePositive = origDBRecordFalsePositive
		dbMarkSiteChecked = origDBMarkSiteChecked
		dbRecordCheckHistory = origDBRecordCheckHistory
		dbUpdateSSLExpiry = origDBUpdateSSLExpiry
		dbOpenSiteEvent = origDBOpenSiteEvent
		dbUpgradeOpenSiteEvent = origDBUpgradeOpenSiteEvent
		dbCloseOpenSiteEvent = origDBCloseOpenSiteEvent
		wpcomNotifyFunc = origNotify
		veriflierCheckFunc = origVeriflierCheck
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
	cfg.DBUpdatesEnable = false
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

func TestHandleFailureFirstFailureOpensSeemsDownEvent(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)
	config.Get().NumOfChecks = 3

	failAt := time.Date(2026, time.April, 23, 12, 0, 0, 0, time.UTC)

	var gotSiteID int64
	var gotEventType uint8
	var gotSeverity uint8
	var gotStartedAt time.Time
	var openCalls int
	dbOpenSiteEvent = func(_ context.Context, siteID int64, eventType, severity uint8, startedAt time.Time) error {
		openCalls++
		gotSiteID = siteID
		gotEventType = eventType
		gotSeverity = severity
		gotStartedAt = startedAt
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	o.handleFailure(db.Site{ID: 99, BlogID: 1}, checker.Result{
		BlogID:    1,
		Success:   false,
		HTTPCode:  500,
		ErrorCode: checker.ErrorConnect,
		RTT:       100 * time.Millisecond,
		Timestamp: failAt,
	})

	if openCalls != 1 {
		t.Fatalf("OpenSiteEvent calls = %d, want 1", openCalls)
	}
	if gotSiteID != 99 {
		t.Fatalf("OpenSiteEvent site_id = %d, want 99", gotSiteID)
	}
	if gotEventType != db.EventTypeSeemsDown {
		t.Fatalf("OpenSiteEvent event_type = %d, want %d", gotEventType, db.EventTypeSeemsDown)
	}
	if gotSeverity != db.EventSeverityLow {
		t.Fatalf("OpenSiteEvent severity = %d, want %d", gotSeverity, db.EventSeverityLow)
	}
	if !gotStartedAt.Equal(failAt) {
		t.Fatalf("OpenSiteEvent started_at = %v, want %v", gotStartedAt, failAt)
	}
}

func TestConfirmDownUpgradesOpenEvent(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var gotSiteID int64
	var gotEventType uint8
	var gotSeverity uint8
	var upgradeCalls int
	dbUpgradeOpenSiteEvent = func(_ context.Context, siteID int64, eventType, severity uint8) error {
		upgradeCalls++
		gotSiteID = siteID
		gotEventType = eventType
		gotSeverity = severity
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}
	fail := checkerResultFailure(123)
	o.retries.record(fail)
	entry := o.retries.get(123)

	o.confirmDown(db.Site{ID: 77, BlogID: 123, SiteStatus: statusRunning}, entry, nil)

	if upgradeCalls != 1 {
		t.Fatalf("UpgradeOpenSiteEvent calls = %d, want 1", upgradeCalls)
	}
	if gotSiteID != 77 {
		t.Fatalf("UpgradeOpenSiteEvent site_id = %d, want 77", gotSiteID)
	}
	if gotEventType != db.EventTypeConfirmedDown {
		t.Fatalf("UpgradeOpenSiteEvent event_type = %d, want %d", gotEventType, db.EventTypeConfirmedDown)
	}
	if gotSeverity != db.EventSeverityHigh {
		t.Fatalf("UpgradeOpenSiteEvent severity = %d, want %d", gotSeverity, db.EventSeverityHigh)
	}
}

func TestEscalateToVerifliersFalsePositiveClosesOpenEvent(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.PeerOfflineLimit = 2

	fixedNow := time.Date(2026, time.April, 23, 12, 34, 56, 0, time.UTC)
	nowFunc = func() time.Time { return fixedNow }

	var gotSiteID int64
	var gotEndedAt time.Time
	var closeCalls int
	dbCloseOpenSiteEvent = func(_ context.Context, siteID int64, endedAt time.Time) error {
		closeCalls++
		gotSiteID = siteID
		gotEndedAt = endedAt
		return nil
	}

	call := 0
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		call++
		return &veriflier.CheckResult{
			BlogID:   req.BlogID,
			Host:     c.Addr(),
			Success:  call != 1,
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
	o.escalateToVerifliers(db.Site{ID: 44, BlogID: 654, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	if closeCalls != 1 {
		t.Fatalf("CloseOpenSiteEvent calls = %d, want 1", closeCalls)
	}
	if gotSiteID != 44 {
		t.Fatalf("CloseOpenSiteEvent site_id = %d, want 44", gotSiteID)
	}
	if !gotEndedAt.Equal(fixedNow) {
		t.Fatalf("CloseOpenSiteEvent ended_at = %v, want %v", gotEndedAt, fixedNow)
	}
}

func TestHandleRecoveryClosesOpenEvent(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	fixedNow := time.Date(2026, time.April, 23, 14, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return fixedNow }

	var gotSiteID int64
	var gotEndedAt time.Time
	var closeCalls int
	dbCloseOpenSiteEvent = func(_ context.Context, siteID int64, endedAt time.Time) error {
		closeCalls++
		gotSiteID = siteID
		gotEndedAt = endedAt
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	o.handleRecovery(db.Site{ID: 12, BlogID: 1, SiteStatus: statusConfirmedDown}, checkerResultSuccess(1))

	if closeCalls != 1 {
		t.Fatalf("CloseOpenSiteEvent calls = %d, want 1", closeCalls)
	}
	if gotSiteID != 12 {
		t.Fatalf("CloseOpenSiteEvent site_id = %d, want 12", gotSiteID)
	}
	if !gotEndedAt.Equal(fixedNow) {
		t.Fatalf("CloseOpenSiteEvent ended_at = %v, want %v", gotEndedAt, fixedNow)
	}
}

func TestProcessResultsMarksChecked(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var markedBlogID int64
	dbMarkSiteChecked = func(_ context.Context, blogID int64, _ time.Time) error {
		markedBlogID = blogID
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
		t.Fatalf("MarkSiteChecked blog_id = %d, want 42", markedBlogID)
	}
}

func TestProcessResultsSkipsUnknownSite(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var markCalled bool
	dbMarkSiteChecked = func(_ context.Context, _ int64, _ time.Time) error {
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
		t.Fatal("MarkSiteChecked called for unknown blog_id, want skipped")
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

func TestSendNotificationBothRetriesFail(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

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
	dbMarkSiteChecked = func(context.Context, int64, time.Time) error {
		return fmt.Errorf("mark checked error")
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

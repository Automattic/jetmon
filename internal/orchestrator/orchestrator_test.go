package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/veriflier"
	"github.com/Automattic/jetmon/internal/wpcom"
)

func TestIsAlertSuppressedUsesLastAlertSent(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-5 * time.Minute)
	old := now.Add(-31 * time.Minute)

	if err := config.Load("../../config/config-sample.json"); err != nil {
		t.Fatalf("config load: %v", err)
	}
	cfg := config.Get()
	cfg.AlertCooldownMinutes = 30

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

	setTestConfig()

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

	setTestConfig()

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

	setTestConfig()
	cfg := config.Get()
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

	setTestConfig()
	cfg := config.Get()
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
	origNotify := wpcomNotifyFunc
	origVeriflierCheck := veriflierCheckFunc

	nowFunc = time.Now
	dbUpdateSiteStatus = func(context.Context, int64, int, time.Time) error { return nil }
	dbUpdateLastAlertSent = func(context.Context, int64, time.Time) error { return nil }
	dbRecordFalsePositive = func(int64, int, int, int64) error { return nil }
	dbMarkSiteChecked = func(context.Context, int64, time.Time) error { return nil }
	dbRecordCheckHistory = func(int64, int, int, int64, int64, int64, int64, int64) error { return nil }
	dbUpdateSSLExpiry = func(context.Context, int64, time.Time) error { return nil }
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
		wpcomNotifyFunc = origNotify
		veriflierCheckFunc = origVeriflierCheck
	}
}

func setTestConfig() {
	_ = config.Load("../../config/config-sample.json")
	cfg := config.Get()
	cfg.AlertCooldownMinutes = 30
	cfg.NumOfChecks = 3
	cfg.PeerOfflineLimit = 2
	cfg.DBUpdatesEnable = false
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

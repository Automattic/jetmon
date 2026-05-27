package orchestrator

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/eventstore"
	"github.com/Automattic/jetmon/internal/veriflier"
	"github.com/Automattic/jetmon/internal/wpcom"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
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

func TestDuplicateBlogMonitorRowsUseMonitorSiteIdentity(t *testing.T) {
	sites := []db.Site{
		{ID: 10, BlogID: 42, MonitorURL: "https://example.com/"},
		{ID: 11, BlogID: 42, MonitorURL: "https://example.com/path"},
	}
	if monitorTargetID(sites[0]) == monitorTargetID(sites[1]) {
		t.Fatal("duplicate blog rows should have distinct monitor target identities")
	}

	req := checkRequestForSite(&config.Config{}, sites[0])
	if req.MonitorSiteID != 10 || req.BlogID != 42 {
		t.Fatalf("request identity = monitor_site_id:%d blog_id:%d, want 10/42", req.MonitorSiteID, req.BlogID)
	}

	siteMap := map[int64]db.Site{
		monitorTargetID(sites[0]): sites[0],
		monitorTargetID(sites[1]): sites[1],
	}
	results := map[int64]checker.Result{
		10: {MonitorSiteID: 10, BlogID: 42, Success: true},
		11: {MonitorSiteID: 11, BlogID: 42, Success: true},
	}
	records := knownSiteResults(results, siteMap)
	if len(records) != 2 {
		t.Fatalf("knownSiteResults returned %d records, want 2", len(records))
	}

	identity := httpEventIdentity(sites[0])
	if identity.EndpointID == nil || *identity.EndpointID != 10 {
		t.Fatalf("httpEventIdentity endpoint = %v, want 10", identity.EndpointID)
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

func TestCheckRequestForSiteAppliesRolloutCheckPolicy(t *testing.T) {
	keyword := "needle"
	forbidden := `["blocked"]`
	cfg := &config.Config{
		NetCommsTimeout:         10,
		DefaultCheckMethod:      "HEAD",
		DefaultDetectionProfile: "legacy",
		BodyReadMaxBytes:        64,
		KeywordReadMaxBytes:     128,
	}

	req := checkRequestForSite(cfg, db.Site{
		BlogID:            42,
		MonitorURL:        "https://example.com",
		CheckKeyword:      &keyword,
		ForbiddenKeywords: &forbidden,
		RedirectPolicy:    "fail",
	})
	if req.Method != "HEAD" || req.DetectionProfile != "legacy" {
		t.Fatalf("request policy = %s/%s, want HEAD/legacy", req.Method, req.DetectionProfile)
	}
	if req.Keyword != nil || len(req.ForbiddenKeywords) != 0 || req.RedirectPolicy != checker.RedirectFollow {
		t.Fatalf("legacy request kept full detections: %+v", req)
	}

	req = checkRequestForSite(cfg, db.Site{
		BlogID:            43,
		MonitorURL:        "https://example.com",
		RequestMethod:     "GET",
		DetectionProfile:  "full",
		CheckKeyword:      &keyword,
		ForbiddenKeywords: &forbidden,
		RedirectPolicy:    "fail",
	})
	if req.Method != "GET" || req.DetectionProfile != "full" {
		t.Fatalf("request policy = %s/%s, want GET/full", req.Method, req.DetectionProfile)
	}
	if req.Keyword == nil || len(req.ForbiddenKeywords) != 1 || req.RedirectPolicy != checker.RedirectFail {
		t.Fatalf("full request did not keep rich detections: %+v", req)
	}
}

func TestCheckRequestForSiteCanDisableTargetSafetyForTests(t *testing.T) {
	cfg := &config.Config{
		NetCommsTimeout:         10,
		DefaultCheckMethod:      "GET",
		DefaultDetectionProfile: "full",
		CheckTargetSafetyMode:   config.CheckTargetSafetyModePublicOnly,
	}
	req := checkRequestForSite(cfg, db.Site{BlogID: 42, MonitorURL: "http://example.com"})
	if !req.EnforceTargetSafety {
		t.Fatal("public_only mode disabled target safety")
	}

	cfg.CheckTargetSafetyMode = config.CheckTargetSafetyModeAllowPrivateForTests
	req = checkRequestForSite(cfg, db.Site{BlogID: 42, MonitorURL: "http://site-0000001.capacity.internal"})
	if req.EnforceTargetSafety {
		t.Fatal("allow_private_for_tests mode kept target safety enabled")
	}
}

func TestInMaintenance(t *testing.T) {
	origNow := nowFunc
	defer func() { nowFunc = origNow }()

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return now }
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
		{Host: "us-west", VantageID: "us-west", AgentID: "agent-a", Outcome: veriflier.OutcomeDown, Success: false, HTTPCode: 500, RTTMs: 123},
		{Host: "eu", Success: true, HTTPCode: 200, RTTMs: 45},
	})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0]["host"] != "us-west" || got[0]["success"] != false ||
		got[0]["http_code"] != int32(500) || got[0]["rtt_ms"] != int64(123) {
		t.Fatalf("first summary = %+v", got[0])
	}
	if got[0]["vantage_id"] != "us-west" || got[0]["agent_id"] != "agent-a" || got[0]["outcome"] != veriflier.OutcomeDown {
		t.Fatalf("first v2 identity summary = %+v", got[0])
	}
	if got[1]["host"] != "eu" || got[1]["success"] != true {
		t.Fatalf("second summary = %+v", got[1])
	}
	if _, ok := got[1]["vantage_id"]; ok {
		t.Fatalf("legacy summary included synthetic vantage_id: %+v", got[1])
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

func TestRefreshVeriflierClientsActiveDiscoveryUsesEnabledVantages(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	dbListVeriflierVantages = func(context.Context, time.Duration) ([]db.VeriflierVantage, error) {
		return []db.VeriflierVantage{
			{VantageID: "us-east", EndpointHost: "east.example", EndpointPort: "7803", AuthToken: "east-token"},
			{VantageID: "incomplete", EndpointHost: "missing-token", EndpointPort: "7803"},
			{VantageID: "us-west", EndpointHost: "west.example", EndpointPort: "7804", AuthToken: "west-token"},
		}, nil
	}

	cfg := &config.Config{
		NumWorkers:             2,
		VeriflierDiscoveryMode: config.VeriflierDiscoveryModeActive,
		Verifiers:              []config.VerifierConfig{{Name: "static", Host: "static.example", Port: "7803", AuthToken: "static-token"}},
	}
	o := New(cfg, nil)

	want := []string{"http://east.example:7803|east-token", "http://west.example:7804|west-token"}
	if !slicesEqual(o.veriflierAddrs, want) {
		t.Fatalf("veriflierAddrs = %#v, want %#v", o.veriflierAddrs, want)
	}
}

func TestRefreshVeriflierClientsActiveDiscoveryFallsBackToStatic(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	dbListVeriflierVantages = func(context.Context, time.Duration) ([]db.VeriflierVantage, error) {
		return nil, fmt.Errorf("db unavailable")
	}

	cfg := &config.Config{
		NumWorkers:             2,
		VeriflierDiscoveryMode: config.VeriflierDiscoveryModeActive,
		Verifiers:              []config.VerifierConfig{{Name: "static", Host: "static.example", Port: "7803", AuthToken: "static-token"}},
	}
	o := New(cfg, nil)

	want := []string{"http://static.example:7803|static-token"}
	if !slicesEqual(o.veriflierAddrs, want) {
		t.Fatalf("veriflierAddrs = %#v, want %#v", o.veriflierAddrs, want)
	}
}

func TestVerifierConfigsFromVantagesFiltersIncompleteRows(t *testing.T) {
	got := verifierConfigsFromVantages([]db.VeriflierVantage{
		{VantageID: "ok", EndpointHost: " host.example ", EndpointPort: " 7803 ", AuthToken: " token "},
		{VantageID: "no-host", EndpointPort: "7803", AuthToken: "token"},
		{VantageID: "no-port", EndpointHost: "host.example", AuthToken: "token"},
		{VantageID: "no-token", EndpointHost: "host.example", EndpointPort: "7803"},
	})
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Name != "ok" || got[0].Host != "host.example" || got[0].Port != "7803" || got[0].AuthToken != "token" {
		t.Fatalf("config = %+v", got[0])
	}
}

func TestSyncVeriflierAgentTelemetryWritesMonitorCollectedStatus(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	var mu sync.Mutex
	var heartbeats []db.VeriflierAgentHeartbeat
	veriflierStatusFunc = func(c *veriflier.VeriflierClient, _ context.Context) (*veriflier.StatusV2Response, error) {
		return &veriflier.StatusV2Response{
			Version:   "test-version",
			Protocols: []string{veriflier.ProtocolV2, veriflier.ProtocolLegacy},
			Vantage:   veriflier.Vantage{ID: "us-east", Region: "iad", Provider: "test"},
			Agent:     veriflier.Agent{ID: "agent-a", Host: "host-a", Version: "test-version", Protocol: veriflier.ProtocolV2},
			Capacity:  veriflier.Capacity{MaxConcurrency: 64, QueueCapacity: 256, QueueDepth: 3, Active: 2, InFlight: 1},
		}, nil
	}
	dbUpsertVeriflierAgent = func(_ context.Context, hb db.VeriflierAgentHeartbeat) error {
		mu.Lock()
		defer mu.Unlock()
		heartbeats = append(heartbeats, hb)
		return nil
	}

	o := &Orchestrator{ctx: context.Background()}
	o.syncVeriflierAgentTelemetry(&config.Config{
		Verifiers: []config.VerifierConfig{{Name: "east", Host: "east.example", Port: "7803", AuthToken: "token"}},
	})

	mu.Lock()
	defer mu.Unlock()
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats len = %d, want 1", len(heartbeats))
	}
	got := heartbeats[0]
	if got.AgentID != "agent-a" || got.VantageID != "us-east" || got.EndpointHost != "east.example" || got.EndpointPort != "7803" {
		t.Fatalf("heartbeat identity = %+v", got)
	}
	if got.MaxConcurrency != 64 || got.QueueCapacity != 256 || got.QueueDepth != 3 || got.Active != 2 || got.InFlight != 1 {
		t.Fatalf("heartbeat capacity = %+v", got)
	}
}

func TestSyncVeriflierAgentTelemetrySkipsLegacyStatus(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	veriflierStatusFunc = func(_ *veriflier.VeriflierClient, _ context.Context) (*veriflier.StatusV2Response, error) {
		return &veriflier.StatusV2Response{Version: "legacy", Protocols: []string{veriflier.ProtocolLegacy}}, nil
	}
	dbUpsertVeriflierAgent = func(_ context.Context, hb db.VeriflierAgentHeartbeat) error {
		t.Fatalf("unexpected heartbeat: %+v", hb)
		return nil
	}

	o := &Orchestrator{ctx: context.Background()}
	o.syncVeriflierAgentTelemetry(&config.Config{
		Verifiers: []config.VerifierConfig{{Name: "legacy", Host: "legacy.example", Port: "7803", AuthToken: "token"}},
	})
}

func TestVeriflierAgentHeartbeatFromStatus(t *testing.T) {
	got := veriflierAgentHeartbeatFromStatus(
		config.VerifierConfig{Host: " endpoint.example ", Port: " 7803 "},
		&veriflier.StatusV2Response{
			Version:   " test-version ",
			Protocols: []string{veriflier.ProtocolV2},
			Vantage:   veriflier.Vantage{ID: " us-west "},
			Agent:     veriflier.Agent{ID: " agent-west ", Host: " host-west "},
			Capacity:  veriflier.Capacity{MaxConcurrency: 8, QueueCapacity: 16},
		},
	)
	if got.AgentID != "agent-west" || got.VantageID != "us-west" || got.EndpointHost != "endpoint.example" ||
		got.EndpointPort != "7803" || got.Version != "test-version" || got.Status != "active" {
		t.Fatalf("heartbeat = %+v", got)
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
	dbUpdateLastAlertSent = func(_ context.Context, _ int64, blogID int64, _ time.Time) error {
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

func TestSendNotificationDoesNotRetryWhenWPCOMCircuitOpen(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	setTestConfig(t)

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	var notifyCalls int
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		notifyCalls++
		return fmt.Errorf("%w, notification queued", wpcom.ErrCircuitOpen)
	}

	var updateAlertCalled bool
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error {
		updateAlertCalled = true
		return nil
	}

	o := &Orchestrator{
		wpcom:    &wpcom.Client{},
		hostname: "local-host",
		ctx:      context.Background(),
	}

	res := checkerResultFailure(123)
	o.sendNotification(db.Site{BlogID: 123, MonitorURL: "https://example.com"}, res, statusConfirmedDown, res.Timestamp, nil)

	if notifyCalls != 1 {
		t.Fatalf("notify calls = %d, want 1 for circuit-open queue response", notifyCalls)
	}
	if updateAlertCalled {
		t.Fatal("dbUpdateLastAlertSent should not be called while WPCOM circuit is open")
	}
	for stat, want := range map[string]int{
		"wpcom.notification.attempt.count":                         1,
		"wpcom.notification.status.confirmed_down.attempt.count":   1,
		"wpcom.notification.error.count":                           1,
		"wpcom.notification.status.confirmed_down.error.count":     1,
		"wpcom.notification.queued.count":                          1,
		"wpcom.notification.status.confirmed_down.queued.count":    1,
		"wpcom.notification.retry.count":                           0,
		"wpcom.notification.failed.count":                          0,
		"wpcom.notification.delivered.count":                       0,
		"wpcom.notification.status.confirmed_down.delivered.count": 0,
	} {
		if got := rec.counter(stat); got != want {
			t.Fatalf("%s = %d, want %d", stat, got, want)
		}
	}
}

func TestSendNotificationDoesNotRetryPermanentWPCOMFailure(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	setTestConfig(t)

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	var notifyCalls int
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		notifyCalls++
		return wpcom.StatusError{StatusCode: http.StatusNotFound}
	}

	var updateAlertCalled bool
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error {
		updateAlertCalled = true
		return nil
	}

	o := &Orchestrator{
		wpcom:    &wpcom.Client{},
		hostname: "local-host",
		ctx:      context.Background(),
	}

	res := checkerResultFailure(123)
	o.sendNotification(db.Site{BlogID: 123, MonitorURL: "https://example.com"}, res, statusConfirmedDown, res.Timestamp, nil)

	if notifyCalls != 1 {
		t.Fatalf("notify calls = %d, want 1 for permanent failure", notifyCalls)
	}
	if updateAlertCalled {
		t.Fatal("dbUpdateLastAlertSent should not be called for permanent failure")
	}
	for stat, want := range map[string]int{
		"wpcom.notification.attempt.count":                                 1,
		"wpcom.notification.status.confirmed_down.attempt.count":           1,
		"wpcom.notification.error.count":                                   1,
		"wpcom.notification.status.confirmed_down.error.count":             1,
		"wpcom.notification.permanent_failure.count":                       1,
		"wpcom.notification.status.confirmed_down.permanent_failure.count": 1,
		"wpcom.notification.http.404.permanent_failure.count":              1,
		"wpcom.notification.failed.count":                                  1,
		"wpcom.notification.status.confirmed_down.failed.count":            1,
		"wpcom.notification.retry.count":                                   0,
		"wpcom.notification.delivered.count":                               0,
		"wpcom.notification.status.confirmed_down.delivered.count":         0,
	} {
		if got := rec.counter(stat); got != want {
			t.Fatalf("%s = %d, want %d", stat, got, want)
		}
	}
}

func TestSendNotificationSkipsWhenWPCOMDisabled(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.WPCOMNotifyEnable = false

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	var notifyCalls int
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		notifyCalls++
		return nil
	}

	var updatedBlogID int64
	dbUpdateLastAlertSent = func(_ context.Context, _ int64, blogID int64, _ time.Time) error {
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

	if notifyCalls != 0 {
		t.Fatalf("notify calls = %d, want 0", notifyCalls)
	}
	if updatedBlogID != 0 {
		t.Fatalf("updated blog_id = %d, want 0", updatedBlogID)
	}
	if got := rec.counter("wpcom.notification.disabled.count"); got != 1 {
		t.Fatalf("wpcom.notification.disabled.count = %d, want 1", got)
	}
}

func TestSendNotificationBuildsLegacyWPCOMPayload(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	setTestConfig(t)

	checkTime := time.Date(2026, 5, 3, 3, 0, 0, 0, time.UTC)
	changeTime := time.Date(2026, 5, 3, 3, 1, 0, 0, time.UTC)
	alertUpdateTime := time.Date(2026, 5, 3, 3, 1, 5, 0, time.UTC)
	nowFunc = func() time.Time { return alertUpdateTime }

	var got wpcom.Notification
	wpcomNotifyFunc = func(_ *wpcom.Client, n wpcom.Notification) error {
		got = n
		return nil
	}
	var updatedAt time.Time
	dbUpdateLastAlertSent = func(_ context.Context, _ int64, blogID int64, ts time.Time) error {
		if blogID != 123 {
			t.Fatalf("updated alert blog_id = %d, want 123", blogID)
		}
		updatedAt = ts
		return nil
	}

	o := &Orchestrator{
		wpcom:    &wpcom.Client{},
		hostname: "monitor-a",
		ctx:      context.Background(),
	}
	res := checker.Result{
		BlogID:    123,
		Success:   false,
		HTTPCode:  500,
		ErrorCode: checker.ErrorConnect,
		RTT:       123 * time.Millisecond,
		Timestamp: checkTime,
	}
	vResults := []veriflier.CheckResult{
		{Host: "verifier-us", Success: false, HTTPCode: 500, RTTMs: 456},
		{Host: "verifier-eu", Success: true, HTTPCode: 200, RTTMs: 78},
	}

	o.sendNotification(
		db.Site{BlogID: 123, MonitorURL: "https://example.com/"},
		res,
		statusConfirmedDown,
		changeTime,
		vResults,
	)

	want := wpcom.Notification{
		BlogID:           123,
		MonitorURL:       "https://example.com/",
		StatusID:         statusConfirmedDown,
		LastCheck:        "2026-05-03T03:00:00Z",
		LastStatusChange: "2026-05-03T03:01:00Z",
		StatusType:       "server",
		Checks: []wpcom.CheckEntry{
			{Type: 1, Host: "monitor-a", Status: statusDown, RTT: 123, Code: 500},
			{Type: 2, Host: "verifier-us", Status: statusDown, RTT: 456, Code: 500},
			{Type: 2, Host: "verifier-eu", Status: statusRunning, RTT: 78, Code: 200},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("notification = %+v, want %+v", got, want)
	}
	if !updatedAt.Equal(alertUpdateTime) {
		t.Fatalf("last alert update = %s, want %s", updatedAt, alertUpdateTime)
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
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error { return nil }

	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		if req.BodyReadMaxBytes != cfg.BodyReadMaxBytes {
			t.Fatalf("verifier req BodyReadMaxBytes = %d, want %d", req.BodyReadMaxBytes, cfg.BodyReadMaxBytes)
		}
		if req.BodyReadMaxMS != int32(cfg.BodyReadMaxMS) {
			t.Fatalf("verifier req BodyReadMaxMS = %d, want %d", req.BodyReadMaxMS, cfg.BodyReadMaxMS)
		}
		if req.KeywordReadMaxBytes != cfg.KeywordReadMaxBytes {
			t.Fatalf("verifier req KeywordReadMaxBytes = %d, want %d", req.KeywordReadMaxBytes, cfg.KeywordReadMaxBytes)
		}
		if req.KeywordReadMaxMS != int32(cfg.KeywordReadMaxMS) {
			t.Fatalf("verifier req KeywordReadMaxMS = %d, want %d", req.KeywordReadMaxMS, cfg.KeywordReadMaxMS)
		}
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

func TestEscalateToVerifliersIgnoresDuplicateVoteIdentities(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.PeerOfflineLimit = 2

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	var falsePositiveBlogID int64
	dbRecordFalsePositive = func(blogID int64, _ int, _ int, _ int64) error {
		falsePositiveBlogID = blogID
		return nil
	}
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("duplicate verifier identity should not satisfy quorum")
		return nil
	}
	veriflierCheckFunc = func(_ *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return &veriflier.CheckResult{
			BlogID:   req.BlogID,
			Host:     "shared-vantage",
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

	fail := checkerResultFailure(655)
	o.retries.record(fail)
	entry := o.retries.get(655)
	o.escalateToVerifliers(db.Site{BlogID: 655, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	if falsePositiveBlogID != 0 {
		t.Fatalf("false positive blog_id = %d, want none while duplicate votes leave health below floor", falsePositiveBlogID)
	}
	if entry := o.retries.get(655); entry == nil {
		t.Fatal("retry entry was cleared despite duplicate votes leaving verifier health below floor")
	}
	if got := rec.counter("verifier.vote.duplicate_identity.count"); got != 1 {
		t.Fatalf("duplicate identity counter = %d, want 1", got)
	}
	if got := rec.gauge("detection.verifier.healthy.count"); got != 1 {
		t.Fatalf("healthy verifier gauge = %d, want 1 unique vote", got)
	}
	if got := rec.gauge("detection.verifier.duplicate_votes.count"); got != 1 {
		t.Fatalf("duplicate vote gauge = %d, want 1", got)
	}
}

func TestEscalateToVerifliersRequiresTwoHealthyVotesForMultiVerifierFleet(t *testing.T) {
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
		t.Fatal("one healthy verifier should not confirm downtime in a multi-verifier fleet")
		return nil
	}
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		if c.Addr() != "v1" {
			return nil, fmt.Errorf("veriflier offline")
		}
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
			veriflier.NewVeriflierClient("v3", ""),
		},
	}

	fail := checkerResultFailure(656)
	o.retries.record(fail)
	entry := o.retries.get(656)
	o.escalateToVerifliers(db.Site{BlogID: 656, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	if falsePositiveBlogID != 0 {
		t.Fatalf("false positive blog_id = %d, want none while verifier health is below floor", falsePositiveBlogID)
	}
	if entry := o.retries.get(656); entry == nil {
		t.Fatal("retry entry was cleared despite insufficient healthy verifier votes")
	}
}

func TestEscalateToVerifliersAllowsSingleConfiguredVerifier(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.PeerOfflineLimit = 2

	var notifyCalls int
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		notifyCalls++
		return nil
	}
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error { return nil }
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
		},
	}

	fail := checkerResultFailure(657)
	o.retries.record(fail)
	entry := o.retries.get(657)
	o.escalateToVerifliers(db.Site{BlogID: 657, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	if notifyCalls != 1 {
		t.Fatalf("notify calls = %d, want 1", notifyCalls)
	}
}

func TestEscalateToVerifliersKeepsRetryOnOperationalNonVote(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.PeerOfflineLimit = 1

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }
	dbRecordFalsePositive = func(blogID int64, _ int, _ int, _ int64) error {
		t.Fatalf("false positive recorded for operational non-vote blog_id=%d", blogID)
		return nil
	}
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("operational non-vote should not confirm downtime")
		return nil
	}
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return &veriflier.CheckResult{
			BlogID:    req.BlogID,
			Host:      c.Addr(),
			Success:   false,
			ErrorCode: 1,
			Outcome:   veriflier.OutcomeAgentOverloaded,
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

	fail := checkerResultFailure(658)
	o.retries.record(fail)
	entry := o.retries.get(658)
	o.escalateToVerifliers(db.Site{BlogID: 658, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	if entry := o.retries.get(658); entry == nil {
		t.Fatal("retry entry was cleared after operational non-vote")
	}
	if got := rec.counter("verifier.vote.non_vote.count"); got != 1 {
		t.Fatalf("non-vote counter = %d, want 1", got)
	}
	if got := rec.counter("detection.verifier.insufficient_healthy.count"); got != 1 {
		t.Fatalf("insufficient healthy counter = %d, want 1", got)
	}
	if got := rec.counter("detection.verifier.deferred.count"); got != 1 {
		t.Fatalf("deferred counter = %d, want 1", got)
	}
	if got := rec.counter("detection.verifier.false_alarm.count"); got != 0 {
		t.Fatalf("false alarm counter = %d, want 0", got)
	}
	if entry := o.retries.get(658); entry == nil || entry.verifierDeferrals != 1 || entry.verifierDeferredUntil.IsZero() {
		t.Fatalf("retry entry deferral = %+v, want one deferred verification", entry)
	}
}

func TestEscalateToVerifliersDoesNotCooldownForSiteScopedNonVote(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.PeerOfflineLimit = 1

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }
	dbRecordFalsePositive = func(blogID int64, _ int, _ int, _ int64) error {
		t.Fatalf("false positive recorded for site-scoped non-vote blog_id=%d", blogID)
		return nil
	}
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("site-scoped non-vote should not confirm downtime")
		return nil
	}
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return &veriflier.CheckResult{
			BlogID:    req.BlogID,
			Host:      c.Addr(),
			Success:   false,
			ErrorCode: checker.ErrorProbeSafety,
			Outcome:   veriflier.OutcomeUnknown,
			RequestID: req.RequestID,
		}, nil
	}

	client := veriflier.NewVeriflierClient("v1", "")
	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local-host",
		veriflierClients: []*veriflier.VeriflierClient{
			client,
		},
	}

	fail := checkerResultFailure(668)
	o.retries.record(fail)
	entry := o.retries.get(668)
	o.escalateToVerifliers(db.Site{BlogID: 668, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	if entry := o.retries.get(668); entry == nil {
		t.Fatal("retry entry was cleared after site-scoped non-vote")
	}
	if got := rec.counter("verifier.vote.non_vote.count"); got != 1 {
		t.Fatalf("non-vote counter = %d, want 1", got)
	}
	available, skipped := o.availableVeriflierClients([]*veriflier.VeriflierClient{client}, nowFunc().UTC().Add(time.Second))
	if len(available) != 1 || skipped != 0 {
		t.Fatalf("available verifliers after site-scoped non-vote = len %d skipped %d, want still available", len(available), skipped)
	}
}

func TestHandleFailureBacksOffVerifierAfterOperationalNonVotes(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.NumOfChecks = 1
	cfg.PeerOfflineLimit = 1

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return now }

	var calls atomic.Int64
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		calls.Add(1)
		return &veriflier.CheckResult{
			BlogID:    req.BlogID,
			Host:      c.Addr(),
			Success:   false,
			ErrorCode: 1,
			Outcome:   veriflier.OutcomeAgentOverloaded,
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
	site := db.Site{BlogID: 659, MonitorURL: "https://example.com", CheckInterval: 1, SiteStatus: statusRunning}
	failureAt := func() checker.Result {
		res := checkerResultFailure(659)
		res.Timestamp = now
		return res
	}

	o.handleFailure(site, failureAt())
	if got := calls.Load(); got != 1 {
		t.Fatalf("verifier calls after first failure = %d, want 1", got)
	}
	entry := o.retries.get(659)
	if entry == nil || entry.verifierDeferrals != 1 {
		t.Fatalf("retry entry after first operational non-vote = %+v, want one deferral", entry)
	}
	firstDeferredUntil := entry.verifierDeferredUntil

	now = now.Add(5 * time.Second)
	o.handleFailure(site, failureAt())
	if got := calls.Load(); got != 1 {
		t.Fatalf("verifier calls during deferral = %d, want still 1", got)
	}
	if got := rec.counter("detection.verifier.deferred_retry_skipped.count"); got != 1 {
		t.Fatalf("deferred retry skipped counter = %d, want 1", got)
	}
	if !o.retries.get(659).verifierDeferredUntil.Equal(firstDeferredUntil) {
		t.Fatalf("deferred retry should not extend deferral window on skipped attempt")
	}

	now = firstDeferredUntil.Add(time.Second)
	o.handleFailure(site, failureAt())
	if got := calls.Load(); got != 1 {
		t.Fatalf("verifier calls while verifier cooldown is still active = %d, want still 1", got)
	}
	entry = o.retries.get(659)
	if entry == nil || entry.verifierDeferrals != 2 {
		t.Fatalf("retry entry after cooldown skip = %+v, want two deferrals", entry)
	}

	now = entry.verifierDeferredUntil.Add(time.Second)
	o.handleFailure(site, failureAt())
	if got := calls.Load(); got != 2 {
		t.Fatalf("verifier calls after deferral and cooldown expire = %d, want 2", got)
	}
	if got := o.retries.get(659).verifierDeferrals; got != 3 {
		t.Fatalf("verifier deferrals = %d, want 3 after second operational non-vote", got)
	}
}

func TestHandleFailureSkipsVerifierDuringOperationalCooldown(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.NumOfChecks = 1
	cfg.PeerOfflineLimit = 1

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	now := time.Date(2026, 5, 16, 12, 30, 0, 0, time.UTC)
	nowFunc = func() time.Time { return now }

	var calls atomic.Int64
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		calls.Add(1)
		return &veriflier.CheckResult{
			BlogID:    req.BlogID,
			Host:      c.Addr(),
			Success:   false,
			ErrorCode: 1,
			Outcome:   veriflier.OutcomeAgentOverloaded,
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
	failureAt := func(blogID int64) checker.Result {
		res := checkerResultFailure(blogID)
		res.Timestamp = now
		return res
	}

	o.handleFailure(db.Site{BlogID: 660, MonitorURL: "https://example.com/a", CheckInterval: 1, SiteStatus: statusRunning}, failureAt(660))
	if got := calls.Load(); got != 1 {
		t.Fatalf("verifier calls after first operational non-vote = %d, want 1", got)
	}

	o.handleFailure(db.Site{BlogID: 661, MonitorURL: "https://example.com/b", CheckInterval: 1, SiteStatus: statusRunning}, failureAt(661))
	if got := calls.Load(); got != 1 {
		t.Fatalf("verifier calls while verifier is in cooldown = %d, want still 1", got)
	}
	if got := rec.counter("detection.verifier.cooldown_skipped.count"); got != 1 {
		t.Fatalf("cooldown skipped counter = %d, want 1", got)
	}
	if got := rec.counter("detection.verifier.deferred.count"); got != 2 {
		t.Fatalf("deferred counter = %d, want 2", got)
	}
	if entry := o.retries.get(661); entry == nil || entry.verifierDeferrals != 1 || entry.verifierDeferredUntil.IsZero() {
		t.Fatalf("second retry entry = %+v, want verifier deferral without an RPC", entry)
	}

	now = now.Add(verifierOperationalCooldownBase + time.Second)
	o.handleFailure(db.Site{BlogID: 662, MonitorURL: "https://example.com/c", CheckInterval: 1, SiteStatus: statusRunning}, failureAt(662))
	if got := calls.Load(); got != 2 {
		t.Fatalf("verifier calls after cooldown expires = %d, want 2", got)
	}
}

func TestVeriflierOperationalCooldownRetainsFailureHistoryUntilHealthy(t *testing.T) {
	o := &Orchestrator{}
	now := time.Date(2026, 5, 16, 14, 0, 0, 0, time.UTC)
	client := veriflier.NewVeriflierClient("v1", "")

	if got := o.markVeriflierOperationalFailure("v1", now); got != verifierOperationalCooldownBase {
		t.Fatalf("first cooldown = %s, want %s", got, verifierOperationalCooldownBase)
	}
	clients, skipped := o.availableVeriflierClients([]*veriflier.VeriflierClient{client}, now.Add(verifierOperationalCooldownBase+time.Second))
	if skipped != 0 || len(clients) != 1 {
		t.Fatalf("available after cooldown = len %d skipped %d, want one available", len(clients), skipped)
	}
	if got := o.markVeriflierOperationalFailure("v1", now.Add(verifierOperationalCooldownBase+2*time.Second)); got != 2*verifierOperationalCooldownBase {
		t.Fatalf("second cooldown = %s, want %s", got, 2*verifierOperationalCooldownBase)
	}

	o.markVeriflierHealthy("v1")
	if got := o.markVeriflierOperationalFailure("v1", now.Add(time.Hour)); got != verifierOperationalCooldownBase {
		t.Fatalf("cooldown after healthy vote = %s, want reset to %s", got, verifierOperationalCooldownBase)
	}
}

func TestAvailableVeriflierClientsForgetsStaleCooldownHistory(t *testing.T) {
	o := &Orchestrator{}
	now := time.Date(2026, 5, 16, 14, 30, 0, 0, time.UTC)
	client := veriflier.NewVeriflierClient("v1", "")

	if got := o.markVeriflierOperationalFailure("v1", now); got != verifierOperationalCooldownBase {
		t.Fatalf("first cooldown = %s, want %s", got, verifierOperationalCooldownBase)
	}
	clients, skipped := o.availableVeriflierClients([]*veriflier.VeriflierClient{client}, now.Add(verifierOperationalCooldownBase+verifierOperationalCooldownMemory+time.Second))
	if skipped != 0 || len(clients) != 1 {
		t.Fatalf("available after stale cooldown = len %d skipped %d, want one available", len(clients), skipped)
	}
	if got := o.markVeriflierOperationalFailure("v1", now.Add(time.Hour)); got != verifierOperationalCooldownBase {
		t.Fatalf("cooldown after stale memory = %s, want %s", got, verifierOperationalCooldownBase)
	}
}

// TestVeriflierCooldownConcurrentAccessRace drives every veriflierCooldowns
// access path (mark-failure, mark-healthy, availability filtering) plus the
// disjoint veriflierMu snapshot path from many goroutines at once. It exists to
// catch lock-discipline regressions on the cooldown map under `go test -race`:
// the map must stay exclusively guarded by veriflierCooldownMu, and the
// client-list snapshot by veriflierMu, with neither held across the other. A
// clean run is only meaningful because this actually exercises concurrency —
// the other cooldown tests are sequential.
func TestVeriflierCooldownConcurrentAccessRace(t *testing.T) {
	o := &Orchestrator{}
	addrs := []string{"v0", "v1", "v2", "v3"}
	clients := make([]*veriflier.VeriflierClient, len(addrs))
	for i, a := range addrs {
		clients[i] = veriflier.NewVeriflierClient(a, "")
	}
	// Set the client list once before fan-out so veriflierSnapshot's RLock has
	// real data to copy concurrently with cooldown-map mutation.
	o.veriflierClients = clients

	base := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	const (
		workers = 8
		iters   = 500
	)
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				addr := addrs[(w+i)%len(addrs)]
				now := base.Add(time.Duration(i) * time.Second)
				switch i % 4 {
				case 0:
					o.markVeriflierOperationalFailure(addr, now)
				case 1:
					o.markVeriflierHealthy(addr)
				case 2:
					o.availableVeriflierClients(o.veriflierSnapshot(), now)
				default:
					o.availableVeriflierClients(clients, now)
				}
			}
		}(w)
	}
	wg.Wait()
}

func TestVerifierOperationalBackoffBoundsToSiteIntervalAndCap(t *testing.T) {
	setTestConfig(t)

	if got := verifierOperationalBackoff(db.Site{CheckInterval: 1}, 0); got != 15*time.Second {
		t.Fatalf("initial backoff = %s, want 15s", got)
	}
	if got := verifierOperationalBackoff(db.Site{CheckInterval: 1}, 2); got != time.Minute {
		t.Fatalf("interval-bounded backoff = %s, want 1m", got)
	}
	if got := verifierOperationalBackoff(db.Site{CheckInterval: 10}, 20); got != verifierOperationalBackoffMax {
		t.Fatalf("max backoff = %s, want %s", got, verifierOperationalBackoffMax)
	}
}

func TestVerifierMinHealthyFloor(t *testing.T) {
	tests := []struct {
		name               string
		peerOfflineLimit   int
		configuredVerifier int
		want               int
	}{
		{name: "none", peerOfflineLimit: 2, configuredVerifier: 0, want: 0},
		{name: "single verifier", peerOfflineLimit: 2, configuredVerifier: 1, want: 1},
		{name: "intentional one vote quorum", peerOfflineLimit: 1, configuredVerifier: 3, want: 1},
		{name: "multi verifier floor", peerOfflineLimit: 2, configuredVerifier: 3, want: 2},
		{name: "higher configured quorum still floor two", peerOfflineLimit: 4, configuredVerifier: 5, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verifierMinHealthyFloor(tt.peerOfflineLimit, tt.configuredVerifier); got != tt.want {
				t.Fatalf("verifierMinHealthyFloor() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestEscalateToVerifliersConfirmsDownOnPartialResponseFromLocalAndVerifier(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.PeerOfflineLimit = 1

	var got wpcom.Notification
	wpcomNotifyFunc = func(_ *wpcom.Client, n wpcom.Notification) error {
		got = n
		return nil
	}
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error { return nil }

	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		if req.BodyReadMaxBytes != cfg.BodyReadMaxBytes {
			t.Fatalf("verifier req BodyReadMaxBytes = %d, want %d", req.BodyReadMaxBytes, cfg.BodyReadMaxBytes)
		}
		if req.BodyReadMaxMS != int32(cfg.BodyReadMaxMS) {
			t.Fatalf("verifier req BodyReadMaxMS = %d, want %d", req.BodyReadMaxMS, cfg.BodyReadMaxMS)
		}
		if req.KeywordReadMaxBytes != cfg.KeywordReadMaxBytes {
			t.Fatalf("verifier req KeywordReadMaxBytes = %d, want %d", req.KeywordReadMaxBytes, cfg.KeywordReadMaxBytes)
		}
		if req.KeywordReadMaxMS != int32(cfg.KeywordReadMaxMS) {
			t.Fatalf("verifier req KeywordReadMaxMS = %d, want %d", req.KeywordReadMaxMS, cfg.KeywordReadMaxMS)
		}
		return &veriflier.CheckResult{
			BlogID:    req.BlogID,
			Host:      c.Addr(),
			Success:   false,
			HTTPCode:  200,
			ErrorCode: checker.ErrorBodyTruncated,
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

	fail := checker.Result{BlogID: 777, Success: false, HTTPCode: 200, ErrorCode: checker.ErrorBodyTruncated, RTT: 120 * time.Millisecond, Timestamp: time.Now().UTC()}
	entry := o.retries.record(fail)
	entry.failCount = cfg.NumOfChecks
	entry.firstFailAt = time.Now().Add(-2 * time.Minute)

	o.escalateToVerifliers(db.Site{BlogID: 777, MonitorURL: "https://example.com", SiteStatus: statusRunning}, entry)

	if got.BlogID != 777 {
		t.Fatalf("notified blog_id = %d, want 777", got.BlogID)
	}
	if got.StatusID != statusConfirmedDown {
		t.Fatalf("notification status = %d, want %d", got.StatusID, statusConfirmedDown)
	}
	if len(got.Checks) != 2 {
		t.Fatalf("checks len = %d, want 2 (local + verifier)", len(got.Checks))
	}
	if got.Checks[0].Code != 200 || got.Checks[1].Code != 200 {
		t.Fatalf("check HTTP codes = [%d, %d], want [200, 200]", got.Checks[0].Code, got.Checks[1].Code)
	}
}

func stubOrchestratorDeps() func() {
	origNow := nowFunc
	origDBClaimBuckets := dbClaimBuckets
	origDBHeartbeat := dbHeartbeat
	origDBReleaseHost := dbReleaseHost
	origDBMarkHostDraining := dbMarkHostDraining
	origDBUpdateStatus := dbUpdateSiteStatus
	origDBGetSiteStatus := dbGetSiteStatus
	origDBUpdateLastAlert := dbUpdateLastAlertSent
	origDBRecordFalsePositive := dbRecordFalsePositive
	origDBMarkSiteChecked := dbMarkSiteChecked
	origDBMarkSitesChecked := dbMarkSitesChecked
	origDBRecordCheckHistory := dbRecordCheckHistory
	origDBRecordCheckHistories := dbRecordCheckHistories
	origDBUpdateSSLExpiry := dbUpdateSSLExpiry
	origDBUpdateSSLExpiries := dbUpdateSSLExpiries
	origDBCountProjectionDrift := dbCountProjectionDrift
	origDBListVeriflierVantages := dbListVeriflierVantages
	origDBUpsertVeriflierAgent := dbUpsertVeriflierAgent
	origDBUpsertSiteSafetyFlag := dbUpsertSiteSafetyFlag
	origDBGetActiveRolloutRange := dbGetActiveRolloutRange
	origNotify := wpcomNotifyFunc
	origVeriflierStatus := veriflierStatusFunc
	origVeriflierCheck := veriflierCheckFunc
	origMetricsClient := metricsClientFunc

	nowFunc = time.Now
	dbClaimBuckets = func(string, int, int, int) (int, int, error) { return 0, 0, nil }
	dbHeartbeat = func(context.Context, string) error { return nil }
	dbReleaseHost = func(context.Context, string, int, int) error { return nil }
	dbMarkHostDraining = func(context.Context, string) error { return nil }
	dbUpdateSiteStatus = func(context.Context, int64, int, time.Time) error { return nil }
	dbGetSiteStatus = func(context.Context, int64, int64) (int, error) { return statusRunning, nil }
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error { return nil }
	dbRecordFalsePositive = func(int64, int, int, int64) error { return nil }
	dbMarkSiteChecked = func(context.Context, int64, int64, time.Time, time.Time) error { return nil }
	dbMarkSitesChecked = func(context.Context, []db.SiteCheck) error { return nil }
	dbRecordCheckHistory = func(int64, int64, string, int, int, int64, int64, int64, int64, int64) error { return nil }
	dbRecordCheckHistories = func(context.Context, []db.CheckHistoryRow) error { return nil }
	dbUpdateSSLExpiry = func(context.Context, int64, int64, time.Time) error { return nil }
	dbUpdateSSLExpiries = func(context.Context, []db.SiteSSLExpiry) error { return nil }
	dbCountProjectionDrift = func(context.Context, int, int) (int, error) { return 0, nil }
	dbListVeriflierVantages = func(context.Context, time.Duration) ([]db.VeriflierVantage, error) { return nil, nil }
	dbUpsertVeriflierAgent = func(context.Context, db.VeriflierAgentHeartbeat) error { return nil }
	dbUpsertSiteSafetyFlag = func(context.Context, db.SiteSafetyFlagExecer, db.SiteSafetyFlag) error {
		return nil
	}
	dbGetActiveRolloutRange = func(context.Context, string) (db.RolloutActiveRange, bool, error) {
		return db.RolloutActiveRange{}, false, nil
	}
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error { return nil }
	veriflierStatusFunc = func(c *veriflier.VeriflierClient, ctx context.Context) (*veriflier.StatusV2Response, error) {
		return c.Status(ctx)
	}
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, ctx context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return c.Check(ctx, req)
	}

	return func() {
		nowFunc = origNow
		dbClaimBuckets = origDBClaimBuckets
		dbHeartbeat = origDBHeartbeat
		dbReleaseHost = origDBReleaseHost
		dbMarkHostDraining = origDBMarkHostDraining
		dbUpdateSiteStatus = origDBUpdateStatus
		dbGetSiteStatus = origDBGetSiteStatus
		dbUpdateLastAlertSent = origDBUpdateLastAlert
		dbRecordFalsePositive = origDBRecordFalsePositive
		dbMarkSiteChecked = origDBMarkSiteChecked
		dbMarkSitesChecked = origDBMarkSitesChecked
		dbRecordCheckHistory = origDBRecordCheckHistory
		dbRecordCheckHistories = origDBRecordCheckHistories
		dbUpdateSSLExpiry = origDBUpdateSSLExpiry
		dbUpdateSSLExpiries = origDBUpdateSSLExpiries
		dbCountProjectionDrift = origDBCountProjectionDrift
		dbListVeriflierVantages = origDBListVeriflierVantages
		dbUpsertVeriflierAgent = origDBUpsertVeriflierAgent
		dbUpsertSiteSafetyFlag = origDBUpsertSiteSafetyFlag
		dbGetActiveRolloutRange = origDBGetActiveRolloutRange
		wpcomNotifyFunc = origNotify
		veriflierStatusFunc = origVeriflierStatus
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
	// Most orchestrator tests exercise legacy WPCOM notification behavior. Keep
	// that opt-in explicit now that the dev profile disables notifications by
	// default for local safety.
	cfg.WPCOMNotifyEnable = true
	cfg.LegacyStatusProjectionEnable = false
	// Existing orchestrator tests assert that every processed result writes a
	// check-history row; keep them on mode=all. The shouldRecordCheckHistory
	// filter has its own dedicated tests for the other modes.
	cfg.CheckHistoryModeDefault = config.CheckHistoryModeAll
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

func checkerResultTransportFailure(blogID int64, at time.Time) checker.Result {
	return checker.Result{
		BlogID:      blogID,
		URL:         "https://example.com",
		Success:     false,
		HTTPCode:    0,
		ErrorCode:   checker.ErrorConnect,
		ErrorDetail: "dial tcp: connection refused",
		RTT:         10 * time.Millisecond,
		Timestamp:   at.UTC(),
	}
}

func checkerResultDNSFailure(blogID int64, at time.Time) checker.Result {
	res := checkerResultTransportFailure(blogID, at)
	res.ErrorDetail = "lookup example.test on 127.0.0.53:53: no such host"
	res.DNSFailureKind = "nxdomain"
	res.DNSFailureName = "example.test"
	res.DNSFailureServer = "127.0.0.53:53"
	return res
}

func TestCheckResultMetadataIncludesObservationAndDiagnostics(t *testing.T) {
	previous := time.Date(2026, 5, 3, 11, 57, 0, 0, time.UTC)
	firstFail := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	res := checkerResultFailure(42)
	res.HTTPCode = 0
	res.Timestamp = firstFail.Add(5 * time.Second)
	res.Method = "GET"
	res.ErrorDetail = "dial tcp: connection refused"
	res.DNSFailureKind = "nxdomain"
	res.DNSFailureName = "example.invalid"
	res.DNSFailureServer = "127.0.0.53:53"
	res.RedirectCount = 1
	res.RedirectChain = []string{"https://example.com/final"}
	res.FinalURL = "https://example.com/final"
	res.TLSVersion = tls.VersionTLS12
	res.CipherSuite = tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256

	meta := checkResultMetadata(db.Site{
		BlogID:         42,
		MonitorURL:     "https://example.com",
		SiteStatus:     statusRunning,
		CheckInterval:  7,
		LastCheckedAt:  &previous,
		RedirectPolicy: "alert",
	}, res, firstFail)

	if meta["error_detail"] != res.ErrorDetail {
		t.Fatalf("error_detail = %v, want %q", meta["error_detail"], res.ErrorDetail)
	}
	if meta["detector_class"] != "dns_nxdomain" {
		t.Fatalf("detector_class = %v, want dns_nxdomain", meta["detector_class"])
	}
	if meta["legacy_status_type"] != "intermittent" {
		t.Fatalf("legacy_status_type = %v, want intermittent", meta["legacy_status_type"])
	}
	if meta["dns_error_kind"] != "nxdomain" || meta["dns_error_name"] != "example.invalid" {
		t.Fatalf("dns metadata = kind:%v name:%v, want nxdomain/example.invalid", meta["dns_error_kind"], meta["dns_error_name"])
	}
	if meta["dns_resolver_source"] != "system" {
		t.Fatalf("dns_resolver_source = %v, want system", meta["dns_resolver_source"])
	}
	if meta["redirect_policy"] != "alert" || meta["redirect_count"] != 1 {
		t.Fatalf("redirect metadata = policy:%v count:%v, want alert/1", meta["redirect_policy"], meta["redirect_count"])
	}
	if meta["final_url"] != res.FinalURL {
		t.Fatalf("final_url = %v, want %q", meta["final_url"], res.FinalURL)
	}
	if meta["tls_version"] == "" || meta["cipher_suite"] == "" {
		t.Fatalf("TLS metadata missing: %+v", meta)
	}

	obs, ok := meta["observation"].(map[string]any)
	if !ok {
		t.Fatalf("observation = %T, want map[string]any", meta["observation"])
	}
	if obs["first_failed_at"] != firstFail.Format(time.RFC3339Nano) {
		t.Fatalf("first_failed_at = %v, want %s", obs["first_failed_at"], firstFail.Format(time.RFC3339Nano))
	}
	if obs["previous_known_good_at"] != previous.Format(time.RFC3339Nano) {
		t.Fatalf("previous_known_good_at = %v, want %s", obs["previous_known_good_at"], previous.Format(time.RFC3339Nano))
	}
	if obs["normal_check_interval_seconds"] != int64(420) {
		t.Fatalf("normal_check_interval_seconds = %v, want 420", obs["normal_check_interval_seconds"])
	}
	if obs["next_check_interval_seconds"] != int64(60) {
		t.Fatalf("next_check_interval_seconds = %v, want 60", obs["next_check_interval_seconds"])
	}
}

func TestCheckResultMetadataIncludesBodyReadEvidence(t *testing.T) {
	res := checkerResultFailure(42)
	res.HTTPCode = http.StatusOK
	res.ErrorCode = checker.ErrorBodyRead
	res.ErrorDetail = "unexpected EOF"
	res.BodyReadMode = "strict_finite"
	res.BodyBytesRead = 100
	res.BodyExpectedBytes = 1024
	res.BodyReadLimitBytes = 1048576
	res.BodyReadError = "unexpected EOF"

	meta := checkResultMetadata(db.Site{
		BlogID:        42,
		MonitorURL:    "https://example.com",
		CheckInterval: 1,
	}, res, res.Timestamp)

	if meta["detector_class"] != "partial_response" {
		t.Fatalf("detector_class = %v, want partial_response", meta["detector_class"])
	}
	if meta["failure_class"] != "intermittent" {
		t.Fatalf("failure_class = %v, want legacy intermittent", meta["failure_class"])
	}
	body, ok := meta["body_read"].(map[string]any)
	if !ok {
		t.Fatalf("body_read = %T, want map[string]any", meta["body_read"])
	}
	if body["mode"] != "strict_finite" ||
		body["bytes_read"] != int64(100) ||
		body["expected_bytes"] != int64(1024) ||
		body["limit_bytes"] != int64(1048576) ||
		body["error"] != "unexpected EOF" {
		t.Fatalf("body_read metadata = %+v", body)
	}
}

func TestRecoveryResultMetadataMarshalsObservation(t *testing.T) {
	res := checkerResultSuccess(42)
	res.Timestamp = time.Date(2026, 5, 3, 12, 4, 0, 0, time.UTC)
	changeTime := time.Date(2026, 5, 3, 12, 4, 2, 0, time.UTC)

	raw, err := json.Marshal(recoveryResultMetadata(res, changeTime))
	if err != nil {
		t.Fatalf("marshal recovery metadata: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal recovery metadata: %v", err)
	}
	obs, ok := meta["observation"].(map[string]any)
	if !ok {
		t.Fatalf("observation = %T, want map[string]any", meta["observation"])
	}
	if obs["first_recovered_at"] != res.Timestamp.Format(time.RFC3339Nano) {
		t.Fatalf("first_recovered_at = %v, want %s", obs["first_recovered_at"], res.Timestamp.Format(time.RFC3339Nano))
	}
	if obs["closed_at"] != changeTime.Format(time.RFC3339Nano) {
		t.Fatalf("closed_at = %v, want %s", obs["closed_at"], changeTime.Format(time.RFC3339Nano))
	}
}

func maintenanceSite(blogID int64, now time.Time) db.Site {
	start := now.Add(-1 * time.Hour)
	end := now.Add(1 * time.Hour)
	return db.Site{
		BlogID:           blogID,
		MonitorURL:       "https://example.com",
		SiteStatus:       statusRunning,
		MaintenanceStart: &start,
		MaintenanceEnd:   &end,
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
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error { return nil }

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
	if !o.retries.recentlyRecovered(1, time.Now().UTC(), postRecoveryTransientFailureWindow(db.Site{BlogID: 1, CheckInterval: 5})) {
		t.Fatal("recovery should mark site for post-recovery transient dampening")
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

func TestHandleFailureDefersLowConfidenceDNSFailureEventUntilVerifierConfirmation(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.NumOfChecks = 3

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	active := o.handleFailure(
		db.Site{BlogID: 1, MonitorURL: "https://example.com", SiteStatus: statusRunning},
		checkerResultDNSFailure(1, time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)),
	)

	if active {
		t.Fatal("low-confidence local DNS failure below verifier threshold should not make the site non-running")
	}
	if entry := o.retries.get(1); entry == nil || entry.eventID != 0 {
		t.Fatalf("retry entry = %+v, want retry without customer-visible event", entry)
	}
	if got := rec.counter("detection.seems_down.open.count"); got != 0 {
		t.Fatalf("seems-down open counter = %d, want 0", got)
	}
	if got := rec.counter("detection.low_confidence_dns.awaiting_verifier.count"); got != 1 {
		t.Fatalf("low-confidence DNS counter = %d, want 1", got)
	}
}

func TestHandleFailureOpensConfirmedDownAfterVerifierConfirmsDeferredDNSFailure(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.NumOfChecks = 1
	cfg.PeerOfflineLimit = 1

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO jetpack_monitor_events").
		WithArgs(int64(1), nil, checkTypeHTTP, nil, eventstore.SeverityDown, eventstore.StateDown, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(501, 1))
	mock.ExpectExec("INSERT INTO jetpack_monitor_event_transitions").
		WithArgs(int64(501), int64(1), nil, nil, eventstore.SeverityDown, nil, eventstore.StateDown, eventstore.ReasonOpened, "local-host", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		return &veriflier.CheckResult{
			BlogID:    req.BlogID,
			Host:      c.Addr(),
			Success:   false,
			RequestID: req.RequestID,
		}, nil
	}

	o := &Orchestrator{
		events:   eventstore.New(sqlDB),
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local-host",
		ctx:      context.Background(),
		veriflierClients: []*veriflier.VeriflierClient{
			veriflier.NewVeriflierClient("v1", ""),
		},
	}

	active := o.handleFailure(
		db.Site{BlogID: 1, MonitorURL: "https://example.com", SiteStatus: statusRunning},
		checkerResultDNSFailure(1, time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)),
	)

	if !active {
		t.Fatal("verifier-confirmed DNS failure should become active after the Down event opens")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestHandleFailureSuppressesFirstPostRecoveryTransportFailure(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.NumOfChecks = 1

	var escalated bool
	veriflierCheckFunc = func(_ *veriflier.VeriflierClient, _ context.Context, _ veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		escalated = true
		return &veriflier.CheckResult{Success: false}, nil
	}

	recoveredAt := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}
	o.retries.markRecovered(42, recoveredAt)

	active := o.handleFailure(
		db.Site{BlogID: 42, MonitorURL: "https://example.com", CheckInterval: 3, SiteStatus: statusRunning},
		checkerResultTransportFailure(42, recoveredAt.Add(2*time.Minute)),
	)

	if active {
		t.Fatal("first post-recovery transport failure should not make the site non-running")
	}
	if escalated {
		t.Fatal("first post-recovery transport failure escalated despite dampening")
	}
	if entry := o.retries.get(42); entry != nil {
		t.Fatalf("suppressed failure created retry state: %+v", entry)
	}
}

func TestHandleFailureEscalatesPostRecoveryTransportFailureAfterWindow(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.NumOfChecks = 1
	cfg.PeerOfflineLimit = 1

	var escalated bool
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		escalated = true
		return &veriflier.CheckResult{
			BlogID:   req.BlogID,
			Host:     c.Addr(),
			Success:  false,
			HTTPCode: 0,
		}, nil
	}

	recoveredAt := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
		veriflierClients: []*veriflier.VeriflierClient{
			veriflier.NewVeriflierClient("v1", ""),
		},
	}
	o.retries.markRecovered(42, recoveredAt)
	site := db.Site{BlogID: 42, MonitorURL: "https://example.com", CheckInterval: 3, SiteStatus: statusRunning}

	o.handleFailure(site, checkerResultTransportFailure(42, recoveredAt.Add(postRecoveryTransientFailureWindow(site)+time.Second)))

	if !escalated {
		t.Fatal("transport failure after post-recovery suppression window should continue the normal retry pipeline")
	}
}

func TestHandleFailureSuppressesPostFalseAlarmTransportFailurePastRecoveryWindow(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.NumOfChecks = 1

	var escalated bool
	veriflierCheckFunc = func(_ *veriflier.VeriflierClient, _ context.Context, _ veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		escalated = true
		return &veriflier.CheckResult{Success: false}, nil
	}

	falseAlarmAt := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}
	o.retries.markFalseAlarm(42, falseAlarmAt)
	site := db.Site{BlogID: 42, MonitorURL: "https://example.com", CheckInterval: 3, SiteStatus: statusRunning}

	active := o.handleFailure(site, checkerResultTransportFailure(42, falseAlarmAt.Add(postRecoveryTransientFailureWindow(site)+time.Second)))

	if active {
		t.Fatal("post-false-alarm transport failure should stay suppressed beyond the normal recovery window")
	}
	if escalated {
		t.Fatal("post-false-alarm transport failure escalated despite false-alarm dampening")
	}
	if entry := o.retries.get(42); entry != nil {
		t.Fatalf("suppressed post-false-alarm failure created retry state: %+v", entry)
	}
}

func TestHandleFailureRefreshesPostFalseAlarmSuppressionWindow(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.NumOfChecks = 1

	var escalated bool
	veriflierCheckFunc = func(_ *veriflier.VeriflierClient, _ context.Context, _ veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		escalated = true
		return &veriflier.CheckResult{Success: false}, nil
	}

	falseAlarmAt := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}
	o.retries.markFalseAlarm(42, falseAlarmAt)
	site := db.Site{BlogID: 42, MonitorURL: "https://example.com", CheckInterval: 3, SiteStatus: statusRunning}
	falseAlarmWindow := postFalseAlarmTransientFailureWindow(site)

	firstSuppressedAt := falseAlarmAt.Add(5 * time.Minute)
	if active := o.handleFailure(site, checkerResultTransportFailure(42, firstSuppressedAt)); active {
		t.Fatal("first post-false-alarm transient failure should stay suppressed")
	}

	secondSuppressedAt := falseAlarmAt.Add(falseAlarmWindow + 30*time.Second)
	if active := o.handleFailure(site, checkerResultTransportFailure(42, secondSuppressedAt)); active {
		t.Fatal("suppression window should roll forward while transient failures continue")
	}
	if escalated {
		t.Fatal("rolling false-alarm dampening escalated despite refreshed suppression window")
	}
	if entry := o.retries.get(42); entry != nil {
		t.Fatalf("suppressed rolling post-false-alarm failure created retry state: %+v", entry)
	}
}

func TestHandleFailureEscalatesPostFalseAlarmTransportFailureAfterFalseAlarmWindow(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	cfg := setTestConfig(t)
	cfg.NumOfChecks = 1
	cfg.PeerOfflineLimit = 1

	var escalated bool
	veriflierCheckFunc = func(c *veriflier.VeriflierClient, _ context.Context, req veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		escalated = true
		return &veriflier.CheckResult{
			BlogID:   req.BlogID,
			Host:     c.Addr(),
			Success:  false,
			HTTPCode: 0,
		}, nil
	}

	falseAlarmAt := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
		veriflierClients: []*veriflier.VeriflierClient{
			veriflier.NewVeriflierClient("v1", ""),
		},
	}
	o.retries.markFalseAlarm(42, falseAlarmAt)
	site := db.Site{BlogID: 42, MonitorURL: "https://example.com", CheckInterval: 3, SiteStatus: statusRunning}

	o.handleFailure(site, checkerResultTransportFailure(42, falseAlarmAt.Add(postFalseAlarmTransientFailureWindow(site)+time.Second)))

	if !escalated {
		t.Fatal("transport failure after post-false-alarm suppression window should continue the normal retry pipeline")
	}
}

func TestProcessResultsMarksChecked(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var markedBlogID int64
	var markedAt time.Time
	var markedNext time.Time
	dbMarkSitesChecked = func(_ context.Context, checks []db.SiteCheck) error {
		if len(checks) != 1 {
			t.Fatalf("batch checks = %d, want 1", len(checks))
		}
		markedBlogID = checks[0].BlogID
		markedAt = checks[0].CheckedAt
		markedNext = checks[0].NextCheckAt
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	res := checkerResultSuccess(42)
	res.Timestamp = time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	sites := map[int64]db.Site{42: {BlogID: 42, SiteStatus: statusRunning, CheckInterval: 7}}
	o.processResults(map[int64]checker.Result{42: res}, sites)

	if markedBlogID != 42 {
		t.Fatalf("MarkSitesChecked blog_id = %d, want 42", markedBlogID)
	}
	if !markedAt.Equal(res.Timestamp) {
		t.Fatalf("MarkSitesChecked checked_at = %s, want %s", markedAt, res.Timestamp)
	}
	if want := res.Timestamp.Add(7 * time.Minute); !markedNext.Equal(want) {
		t.Fatalf("MarkSitesChecked next_check_at = %s, want %s", markedNext, want)
	}
}

func TestProcessResultsProbeSafetyBlockAuditsWithoutStateChange(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()
	audit.Init(sqlDB)
	t.Cleanup(func() { audit.Init(nil) })

	dbUpdateSiteStatus = func(context.Context, int64, int, time.Time) error {
		t.Fatal("probe safety block must not update site status")
		return nil
	}
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("probe safety block must not send notifications")
		return nil
	}
	var safetyFlag db.SiteSafetyFlag
	dbUpsertSiteSafetyFlag = func(_ context.Context, _ db.SiteSafetyFlagExecer, flag db.SiteSafetyFlag) error {
		safetyFlag = flag
		return nil
	}

	mock.ExpectExec(`INSERT INTO jetpack_monitor_audit_log`).
		WithArgs(int64(42), nil, audit.EventProbeSafetyBlock, "local", "probe safety blocked outbound check", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	res := checker.Result{
		BlogID:      42,
		URL:         "http://127.0.0.1",
		Success:     false,
		ErrorCode:   checker.ErrorProbeSafety,
		ErrorDetail: "probe safety check: target host \"127.0.0.1\" is not public",
		Timestamp:   time.Now().UTC(),
	}
	sites := map[int64]db.Site{42: {ID: 123, BlogID: 42, MonitorURL: "http://127.0.0.1", SiteStatus: statusRunning, CheckInterval: 5}}
	o.processResults(map[int64]checker.Result{42: res}, sites)

	if retry := o.retries.get(42); retry != nil {
		t.Fatalf("retry entry = %+v, want nil for probe safety block", retry)
	}
	if safetyFlag.BlogID != 42 || safetyFlag.MonitorSiteID != 123 || safetyFlag.FlagType != db.SiteSafetyFlagProbeSafetyBlock || safetyFlag.Status != db.SiteSafetyStatusOpen {
		t.Fatalf("safety flag = %+v", safetyFlag)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("audit expectations: %v", err)
	}
}

func TestProcessResultsOperationalUnknownAuditsWithoutStateChange(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()
	audit.Init(sqlDB)
	t.Cleanup(func() { audit.Init(nil) })

	dbUpdateSiteStatus = func(context.Context, int64, int, time.Time) error {
		t.Fatal("operational unknown must not update site status")
		return nil
	}
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("operational unknown must not send notifications")
		return nil
	}

	mock.ExpectExec(`INSERT INTO jetpack_monitor_audit_log`).
		WithArgs(int64(42), nil, audit.EventCheckInternal, "local", "checker internal error", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	res := checker.Result{
		BlogID:      42,
		URL:         "https://example.com",
		Success:     false,
		ErrorCode:   checker.ErrorInternal,
		ErrorDetail: "checker panic: synthetic failure",
		Timestamp:   time.Now().UTC(),
	}
	sites := map[int64]db.Site{42: {ID: 123, BlogID: 42, MonitorURL: "https://example.com", SiteStatus: statusRunning, CheckInterval: 5}}
	o.processResults(map[int64]checker.Result{42: res}, sites)

	if retry := o.retries.get(42); retry != nil {
		t.Fatalf("retry entry = %+v, want nil for operational unknown", retry)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("audit expectations: %v", err)
	}
}

func TestProcessResultsSchedulesFailedChecksSoonerThanNormalInterval(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	var markedNext time.Time
	dbMarkSitesChecked = func(_ context.Context, checks []db.SiteCheck) error {
		if len(checks) != 1 {
			t.Fatalf("batch checks = %d, want 1", len(checks))
		}
		markedNext = checks[0].NextCheckAt
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	res := checkerResultFailure(42)
	res.Timestamp = time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	sites := map[int64]db.Site{42: {BlogID: 42, SiteStatus: statusRunning, CheckInterval: 7}}
	o.processResults(map[int64]checker.Result{42: res}, sites)

	if want := res.Timestamp.Add(time.Minute); !markedNext.Equal(want) {
		t.Fatalf("failed check next_check_at = %s, want %s", markedNext, want)
	}
}

func TestProcessResultsReportsCheckOutcomes(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	success := checkerResultSuccess(1)
	timeout := checkerResultFailure(2)
	timeout.HTTPCode = 0
	timeout.ErrorCode = checker.ErrorTimeout
	connect := checkerResultFailure(3)
	connect.HTTPCode = 0
	connect.ErrorCode = checker.ErrorConnect
	server := checkerResultFailure(4)
	server.HTTPCode = 500
	server.ErrorCode = checker.ErrorNone
	deprecatedTLS := checkerResultSuccess(5)
	deprecatedTLS.ErrorCode = checker.ErrorTLSDeprecated

	summary := o.processResults(
		map[int64]checker.Result{
			1: success,
			2: timeout,
			3: connect,
			4: server,
			5: deprecatedTLS,
		},
		map[int64]db.Site{
			1: {BlogID: 1, SiteStatus: statusRunning},
			2: {BlogID: 2, SiteStatus: statusRunning},
			3: {BlogID: 3, SiteStatus: statusRunning},
			4: {BlogID: 4, SiteStatus: statusRunning},
			5: {BlogID: 5, SiteStatus: statusRunning},
		},
	)

	if summary.checkSuccesses != 2 || summary.checkFailures != 3 {
		t.Fatalf("success/failure counts = %d/%d, want 2/3", summary.checkSuccesses, summary.checkFailures)
	}
	if summary.checkTimeouts != 1 || summary.checkConnectErrors != 1 || summary.checkHTTPFailures != 1 || summary.checkTLSDeprecated != 1 {
		t.Fatalf("outcome counts timeout/connect/http/tls = %d/%d/%d/%d, want 1/1/1/1",
			summary.checkTimeouts,
			summary.checkConnectErrors,
			summary.checkHTTPFailures,
			summary.checkTLSDeprecated,
		)
	}
}

func TestProcessResultsReportsCheckCohorts(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	headLegacy := checkerResultSuccess(1)
	headLegacy.Method = http.MethodHead
	headLegacy.DetectionProfile = "legacy"
	getSimple := checkerResultFailure(2)
	getSimple.Method = http.MethodGet
	getSimple.DetectionProfile = "simple_http"
	getFull := checkerResultSuccess(3)
	getFull.Method = http.MethodGet
	getFull.DetectionProfile = "full"

	summary := o.processResults(
		map[int64]checker.Result{
			1: headLegacy,
			2: getSimple,
			3: getFull,
		},
		map[int64]db.Site{
			1: {BlogID: 1, SiteStatus: statusRunning},
			2: {BlogID: 2, SiteStatus: statusRunning},
			3: {BlogID: 3, SiteStatus: statusRunning},
		},
	)

	assertCheckCohortCount(t, summary.checkCohorts, http.MethodHead, "legacy", 1)
	assertCheckCohortCount(t, summary.checkCohorts, http.MethodGet, "simple_http", 1)
	assertCheckCohortCount(t, summary.checkCohorts, http.MethodGet, "full", 1)
}

func TestEmitCheckCohortCounters(t *testing.T) {
	rec := newRecordingMetrics()
	emitCheckCohortCounters(rec, "scheduler.streaming", map[checkCohortKey]int{
		{method: http.MethodGet, profile: "full"}:         2,
		{method: http.MethodHead, profile: "simple_http"}: 1,
	})

	if got := rec.counter("scheduler.streaming.check.method.get.profile.full.count"); got != 2 {
		t.Fatalf("GET/full cohort counter = %d, want 2", got)
	}
	if got := rec.counter("scheduler.streaming.check.method.head.profile.simple_http.count"); got != 1 {
		t.Fatalf("HEAD/simple_http cohort counter = %d, want 1", got)
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
	dbUpdateSSLExpiries = func(context.Context, []db.SiteSSLExpiry) error {
		return fmt.Errorf("batch ssl failed")
	}

	var fallbackMarked int64
	dbMarkSiteChecked = func(_ context.Context, _ int64, blogID int64, _, _ time.Time) error {
		fallbackMarked = blogID
		return nil
	}
	var fallbackHistory int64
	dbRecordCheckHistory = func(_ int64, blogID int64, _ string, _ int, _ int, _ int64, _ int64, _ int64, _ int64, _ int64) error {
		fallbackHistory = blogID
		return nil
	}
	var fallbackSSL int64
	dbUpdateSSLExpiry = func(_ context.Context, _ int64, blogID int64, _ time.Time) error {
		fallbackSSL = blogID
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	res := checkerResultSuccess(42)
	expiry := time.Now().UTC().AddDate(0, 1, 0)
	res.SSLExpiry = &expiry
	summary := o.processResults(
		map[int64]checker.Result{42: res},
		map[int64]db.Site{42: {BlogID: 42, SiteStatus: statusRunning}},
	)

	if fallbackMarked != 42 || fallbackHistory != 42 || fallbackSSL != 42 {
		t.Fatalf("fallback marked/history/ssl = %d/%d/%d, want 42/42/42", fallbackMarked, fallbackHistory, fallbackSSL)
	}
	if summary.markCheckedRows != 1 || summary.historyRows != 1 || summary.sslRows != 1 {
		t.Fatalf("fallback rows = %d/%d/%d, want 1/1/1", summary.markCheckedRows, summary.historyRows, summary.sslRows)
	}
	if summary.markCheckedErrors != 1 || summary.historyErrors != 1 || summary.sslErrors != 1 {
		t.Fatalf("batch errors = %d/%d/%d, want 1/1/1", summary.markCheckedErrors, summary.historyErrors, summary.sslErrors)
	}
}

func TestRecordStreamingHistoryRowsCountsFallbackSuccesses(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	dbRecordCheckHistories = func(context.Context, []db.CheckHistoryRow) error {
		return fmt.Errorf("batch history failed")
	}
	dbRecordCheckHistory = func(_ int64, blogID int64, _ string, _ int, _ int, _ int64, _ int64, _ int64, _ int64, _ int64) error {
		if blogID == 2 {
			return fmt.Errorf("poisoned history row")
		}
		return nil
	}

	o := &Orchestrator{ctx: context.Background()}
	summary := o.recordStreamingHistoryRows([]db.CheckHistoryRow{
		{BlogID: 1, RequestMethod: http.MethodGet, HTTPCode: 200},
		{BlogID: 2, RequestMethod: http.MethodGet, HTTPCode: 200},
	})

	if summary.historyRows != 1 {
		t.Fatalf("history rows = %d, want one successful fallback row", summary.historyRows)
	}
	if summary.historyErrors != 2 {
		t.Fatalf("history errors = %d, want batch error plus poisoned-row error", summary.historyErrors)
	}
}

func TestEventMutationRetryRetriesDeadlocks(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	o := &Orchestrator{ctx: context.Background()}
	attempts := 0
	err := o.withEventMutationRetry(42, "open_seems_down", func() error {
		attempts++
		if attempts == 1 {
			return fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1213, Message: "deadlock"})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withEventMutationRetry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if got := rec.counter("eventstore.mutation.retry.count"); got != 1 {
		t.Fatalf("retry metric = %d, want 1", got)
	}
}

func TestIsRetryableMySQLError(t *testing.T) {
	if !isRetryableMySQLError(fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1205, Message: "lock wait"})) {
		t.Fatal("lock wait timeout should be retryable")
	}
	if !isRetryableMySQLError(&mysql.MySQLError{Number: 1213, Message: "deadlock"}) {
		t.Fatal("deadlock should be retryable")
	}
	if isRetryableMySQLError(&mysql.MySQLError{Number: 1062, Message: "duplicate"}) {
		t.Fatal("duplicate key should not be retryable")
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
	dbUpdateSSLExpiries = func(_ context.Context, updates []db.SiteSSLExpiry) error {
		if len(updates) != 1 {
			t.Fatalf("ssl expiry updates = %d, want 1", len(updates))
		}
		updatedExpiry = updates[0].Expiry
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

func TestProcessResultsTLSDeprecatedIsAdvisory(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("deprecated TLS advisory should not send a downtime notification")
		return nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local",
		ctx:      context.Background(),
	}

	res := checkerResultSuccess(72)
	res.HTTPCode = 200
	res.ErrorCode = checker.ErrorTLSDeprecated
	res.TLSVersion = tls.VersionTLS11

	sites := map[int64]db.Site{72: {BlogID: 72, SiteStatus: statusRunning}}
	o.processResults(map[int64]checker.Result{72: res}, sites)

	if o.retries.get(72) != nil {
		t.Fatal("deprecated TLS advisory should not enter the downtime retry queue")
	}
}

func TestCheckTLSDeprecatedOpensWarningEvent(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO jetpack_monitor_events").
		WithArgs(int64(72), nil, checkTypeTLSDeprecated, nil, eventstore.SeverityWarning, eventstore.StateWarning, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(101, 1))
	mock.ExpectExec("INSERT INTO jetpack_monitor_event_transitions").
		WithArgs(int64(101), int64(72), nil, nil, eventstore.SeverityWarning, nil, eventstore.StateWarning, eventstore.ReasonOpened, "local-host", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	o := &Orchestrator{
		events:   eventstore.New(sqlDB),
		hostname: "local-host",
		ctx:      context.Background(),
	}
	o.checkTLSDeprecated(db.Site{BlogID: 72}, checker.Result{
		TLSVersion:  tls.VersionTLS11,
		CipherSuite: tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCheckTLSDeprecatedClosesWarningOnModernTLS(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, severity, state FROM jetpack_monitor_events").
		WithArgs(int64(73), checkTypeTLSDeprecated).
		WillReturnRows(sqlmock.NewRows([]string{"id", "severity", "state"}).
			AddRow(int64(202), eventstore.SeverityWarning, eventstore.StateWarning))
	mock.ExpectQuery("SELECT blog_id, endpoint_id, severity, state, ended_at, cause_event_id").
		WithArgs(int64(202)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id", "endpoint_id", "severity", "state", "ended_at", "cause_event_id"}).
			AddRow(int64(73), nil, eventstore.SeverityWarning, eventstore.StateWarning, nil, nil))
	mock.ExpectExec("UPDATE jetpack_monitor_events").
		WithArgs(eventstore.ReasonProbeCleared, int64(202)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO jetpack_monitor_event_transitions").
		WithArgs(int64(202), int64(73), nil, eventstore.SeverityWarning, nil, eventstore.StateWarning, eventstore.StateResolved, eventstore.ReasonProbeCleared, "local-host", nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	o := &Orchestrator{
		events:   eventstore.New(sqlDB),
		hostname: "local-host",
		ctx:      context.Background(),
	}
	o.checkTLSDeprecated(db.Site{BlogID: 73}, checker.Result{TLSVersion: tls.VersionTLS12})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
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

func TestCloseSSLExpiryUsesProbeCleared(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, severity, state FROM jetpack_monitor_events").
		WithArgs(int64(74), checkTypeTLSExpiry).
		WillReturnRows(sqlmock.NewRows([]string{"id", "severity", "state"}).
			AddRow(int64(303), eventstore.SeverityWarning, eventstore.StateWarning))
	mock.ExpectQuery("SELECT blog_id, endpoint_id, severity, state, ended_at, cause_event_id").
		WithArgs(int64(303)).
		WillReturnRows(sqlmock.NewRows([]string{"blog_id", "endpoint_id", "severity", "state", "ended_at", "cause_event_id"}).
			AddRow(int64(74), nil, eventstore.SeverityWarning, eventstore.StateWarning, nil, nil))
	mock.ExpectExec("UPDATE jetpack_monitor_events").
		WithArgs(eventstore.ReasonProbeCleared, int64(303)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO jetpack_monitor_event_transitions").
		WithArgs(int64(303), int64(74), nil, eventstore.SeverityWarning, nil, eventstore.StateWarning, eventstore.StateResolved, eventstore.ReasonProbeCleared, "local-host", nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	o := &Orchestrator{
		events:   eventstore.New(sqlDB),
		hostname: "local-host",
		ctx:      context.Background(),
	}
	if err := o.closeSSLExpiryIfOpen(74); err != nil {
		t.Fatalf("closeSSLExpiryIfOpen: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
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
		t.Fatal("ClaimBuckets called dynamic jetpack_monitor_hosts claim in pinned mode")
	}
	if o.bucketMin != 12 || o.bucketMax != 34 {
		t.Fatalf("bucket range = %d-%d, want 12-34", o.bucketMin, o.bucketMax)
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
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error {
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
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error { return nil }

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

func TestHandleFailureSwallowsFailureDuringMaintenance(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.NumOfChecks = 1

	fixedNow := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return fixedNow }

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }

	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("notification should not be sent during maintenance")
		return nil
	}
	veriflierCheckFunc = func(_ *veriflier.VeriflierClient, _ context.Context, _ veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		t.Fatal("failure during maintenance should not escalate to verifliers")
		return nil, nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local",
		veriflierClients: []*veriflier.VeriflierClient{
			veriflier.NewVeriflierClient("v1", ""),
		},
	}

	o.handleFailure(maintenanceSite(88, fixedNow), checkerResultFailure(88))

	if o.retries.get(88) != nil {
		t.Fatal("retry entry should not be retained for maintenance-swallowed failure")
	}
	if got := rec.counter("detection.maintenance.swallowed.count"); got != 1 {
		t.Fatalf("maintenance swallowed counter = %d, want 1", got)
	}
	if got := rec.counter("detection.maintenance.swallowed.server.count"); got != 1 {
		t.Fatalf("maintenance swallowed server counter = %d, want 1", got)
	}
	if got := rec.counter("detection.failure.server.count"); got != 0 {
		t.Fatalf("failure server counter = %d, want 0 for maintenance-swallowed failure", got)
	}
}

func TestHandleFailureClearsExistingRetryWhenMaintenanceStarts(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	fixedNow := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return fixedNow }

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local",
	}

	fail := checkerResultFailure(89)
	o.retries.record(fail)
	entry := o.retries.get(89)
	entry.eventID = 123

	o.handleFailure(maintenanceSite(89, fixedNow), fail)

	if o.retries.get(89) != nil {
		t.Fatal("retry entry should be cleared when maintenance swallows an existing failure")
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

func TestHandleRecoveryCooldownSuppressionIsAudited(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()
	audit.Init(sqlDB)
	t.Cleanup(func() { audit.Init(nil) })

	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("notification should not be sent during cooldown")
		return nil
	}

	recent := time.Now().UTC().Add(-5 * time.Minute)
	mock.ExpectExec(`INSERT INTO jetpack_monitor_audit_log`).
		WithArgs(int64(1), nil, audit.EventAlertSuppressed, "local", "recovery cooldown active", nil).
		WillReturnResult(sqlmock.NewResult(1, 1))

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		ctx:      context.Background(),
		hostname: "local",
	}

	o.handleRecovery(db.Site{
		BlogID:          1,
		SiteStatus:      statusConfirmedDown,
		LastAlertSentAt: &recent,
	}, checkerResultSuccess(1))

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestProcessResultsLogsErrorsFromDB(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()
	setTestConfig(t)

	// Make all DB calls return errors to exercise the log.Printf branches in processResults.
	dbMarkSitesChecked = func(context.Context, []db.SiteCheck) error {
		return fmt.Errorf("batch mark checked error")
	}
	dbMarkSiteChecked = func(context.Context, int64, int64, time.Time, time.Time) error {
		return fmt.Errorf("mark checked error")
	}
	dbRecordCheckHistories = func(context.Context, []db.CheckHistoryRow) error {
		return fmt.Errorf("batch history error")
	}
	dbRecordCheckHistory = func(int64, int64, string, int, int, int64, int64, int64, int64, int64) error {
		return fmt.Errorf("history error")
	}
	dbUpdateSSLExpiries = func(context.Context, []db.SiteSSLExpiry) error {
		return fmt.Errorf("batch ssl expiry error")
	}
	dbUpdateSSLExpiry = func(context.Context, int64, int64, time.Time) error {
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
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error { return nil }
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

func TestHandleFailureDoesNotNotifyWPCOMBeforeConfirmation(t *testing.T) {
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
		hostname: "local-host",
		ctx:      context.Background(),
	}
	o.handleFailure(db.Site{BlogID: 42, MonitorURL: "https://example.com", SiteStatus: statusRunning}, checkerResultFailure(42))

	if notifyCalls != 0 {
		t.Fatalf("notify calls = %d, want 0 before verifier confirmation", notifyCalls)
	}
}

func TestHandleFailureDoesNotReverifyAlreadyConfirmedDownSite(t *testing.T) {
	restore := stubOrchestratorDeps()
	defer restore()

	cfg := setTestConfig(t)
	cfg.NumOfChecks = 1

	rec := newRecordingMetrics()
	metricsClientFunc = func() metricsClient { return rec }
	wpcomNotifyFunc = func(_ *wpcom.Client, _ wpcom.Notification) error {
		t.Fatal("already-confirmed down failure should not send another notification")
		return nil
	}
	veriflierCheckFunc = func(_ *veriflier.VeriflierClient, _ context.Context, _ veriflier.CheckRequest) (*veriflier.CheckResult, error) {
		t.Fatal("already-confirmed down failure should not re-enter Veriflier confirmation")
		return nil, nil
	}

	o := &Orchestrator{
		retries:  newRetryQueue(),
		wpcom:    &wpcom.Client{},
		hostname: "local-host",
		ctx:      context.Background(),
		veriflierClients: []*veriflier.VeriflierClient{
			veriflier.NewVeriflierClient("v1", ""),
		},
	}
	o.retries.record(checkerResultFailure(42))

	active := o.handleFailure(db.Site{
		BlogID:     42,
		MonitorURL: "https://example.com",
		SiteStatus: statusConfirmedDown,
	}, checkerResultFailure(42))

	if !active {
		t.Fatal("already-confirmed down failure should report an active failure")
	}
	if entry := o.retries.get(42); entry != nil {
		t.Fatalf("stale retry entry should be cleared for already-confirmed down site: %+v", entry)
	}
	if got := rec.counter("detection.down.still_down.count"); got != 1 {
		t.Fatalf("still-down counter = %d, want 1", got)
	}
	if got := rec.counter("detection.down.still_down.server.count"); got != 1 {
		t.Fatalf("still-down server counter = %d, want 1", got)
	}
	if got := rec.counter("detection.verifier.escalation.count"); got != 0 {
		t.Fatalf("verifier escalation counter = %d, want 0", got)
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
	dbUpdateLastAlertSent = func(context.Context, int64, int64, time.Time) error { return nil }
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
	if entry := o.retries.get(654); entry != nil {
		t.Fatalf("retry entry after false alarm = %+v, want nil", entry)
	}
	if !o.retries.recentlyFalseAlarmed(654, nowFunc().UTC(), postFalseAlarmTransientFailureWindow(db.Site{BlogID: 654, CheckInterval: 5})) {
		t.Fatal("false alarm should mark the site for false-alarm transient suppression")
	}
	if o.retries.recentlyRecovered(654, nowFunc().UTC(), postRecoveryTransientFailureWindow(db.Site{BlogID: 654, CheckInterval: 5})) {
		t.Fatal("false alarm should not mark the site as a normal recovery")
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

func (r *recordingMetrics) EmitDBStats(prefix string, stats sql.DBStats) {}

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

func assertCheckCohortCount(t *testing.T, cohorts map[checkCohortKey]int, method, profile string, want int) {
	t.Helper()
	if got := cohorts[checkCohortKey{method: method, profile: profile}]; got != want {
		t.Fatalf("cohort %s/%s count = %d, want %d", method, profile, got, want)
	}
}

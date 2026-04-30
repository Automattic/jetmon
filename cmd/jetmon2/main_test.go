package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/alerting"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/dashboard"
	"github.com/Automattic/jetmon/internal/deliverer"
	"github.com/Automattic/jetmon/internal/fleethealth"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestHTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	body, err := httpGet(srv.URL)
	if err != nil {
		t.Fatalf("httpGet() error = %v", err)
	}
	if strings.TrimSpace(body) != "ok" {
		t.Fatalf("httpGet() body = %q, want %q", body, "ok")
	}
}

func TestHTTPGetErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := httpGet(srv.URL)
	if err == nil {
		t.Fatalf("httpGet() expected error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("httpGet() error = %v, want status code", err)
	}
}

func TestEnvOrDefault(t *testing.T) {
	const key = "JETMON_TEST_ENV_OR_DEFAULT"
	t.Setenv(key, "")

	if got := envOrDefault(key, "fallback"); got != "fallback" {
		t.Fatalf("envOrDefault() = %q, want fallback", got)
	}

	t.Setenv(key, "set-value")
	if got := envOrDefault(key, "fallback"); got != "set-value" {
		t.Fatalf("envOrDefault() = %q, want set-value", got)
	}
}

func TestReadPIDFile(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "test.pid")
	if err := os.WriteFile(pidPath, []byte("12345\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("JETMON_PID_FILE", pidPath)

	pid := readPIDFile()
	if pid != 12345 {
		t.Fatalf("readPIDFile() = %d, want 12345", pid)
	}
}

func TestWriteAndRemovePIDFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pid")
	if err := writePIDFile(path); err != nil {
		t.Fatalf("writePIDFile() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var pid int
	if _, err := fmt.Sscan(string(data), &pid); err != nil || pid <= 0 {
		t.Fatalf("invalid PID in file: %q", string(data))
	}

	removePIDFile(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("PID file still exists after removePIDFile()")
	}
}

func TestResolveSince(t *testing.T) {
	if got := resolveSince(""); got != "" {
		t.Fatalf("resolveSince(\"\") = %q, want empty", got)
	}

	// Duration input: result should be a timestamp just before now.
	before := time.Now()
	got := resolveSince("1h")
	after := time.Now()

	ts, err := time.ParseInLocation("2006-01-02 15:04:05", got, time.Local)
	if err != nil {
		t.Fatalf("resolveSince(\"1h\") = %q, not a valid timestamp: %v", got, err)
	}
	if ts.Before(before.Add(-time.Hour-time.Second)) || ts.After(after.Add(-time.Hour+time.Second)) {
		t.Fatalf("resolveSince(\"1h\") = %q, out of expected range", got)
	}

	// Non-duration string passes through unchanged.
	const literal = "2024-01-15 10:00:00"
	if got := resolveSince(literal); got != literal {
		t.Fatalf("resolveSince(%q) = %q, want passthrough", literal, got)
	}
}

func TestEmailTransportLabelAndDelivery(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.Config
		label    string
		delivers bool
	}{
		{
			name:     "empty is stub alias",
			cfg:      config.Config{EmailTransport: ""},
			label:    "stub",
			delivers: false,
		},
		{
			name:     "stub logs only",
			cfg:      config.Config{EmailTransport: "stub"},
			label:    "stub",
			delivers: false,
		},
		{
			name:     "smtp delivers",
			cfg:      config.Config{EmailTransport: "smtp"},
			label:    "smtp",
			delivers: true,
		},
		{
			name:     "wpcom delivers",
			cfg:      config.Config{EmailTransport: "wpcom"},
			label:    "wpcom",
			delivers: true,
		},
		{
			name:     "invalid transport does not deliver",
			cfg:      config.Config{EmailTransport: "sendmail"},
			label:    "sendmail",
			delivers: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := emailTransportLabel(&tt.cfg); got != tt.label {
				t.Fatalf("emailTransportLabel() = %q, want %q", got, tt.label)
			}
			if got := emailTransportDelivers(&tt.cfg); got != tt.delivers {
				t.Fatalf("emailTransportDelivers() = %v, want %v", got, tt.delivers)
			}
		})
	}
}

func TestDeliveryWorkersShouldStart(t *testing.T) {
	tests := []struct {
		name      string
		cfg       config.Config
		hostname  string
		wantStart bool
		wantLevel string
		wantMsg   string
	}{
		{
			name:      "api disabled",
			cfg:       config.Config{},
			hostname:  "host-a",
			wantLevel: "INFO",
			wantMsg:   "delivery_workers=disabled",
		},
		{
			name:      "legacy api port behavior starts workers",
			cfg:       config.Config{APIPort: 8090},
			hostname:  "host-a",
			wantStart: true,
			wantLevel: "WARN",
			wantMsg:   "delivery_owner_host is unset",
		},
		{
			name: "matching owner starts workers",
			cfg: config.Config{
				APIPort:           8090,
				DeliveryOwnerHost: "host-a",
			},
			hostname:  "host-a",
			wantStart: true,
			wantLevel: "INFO",
			wantMsg:   "matched",
		},
		{
			name: "non-owner skips workers",
			cfg: config.Config{
				APIPort:           8090,
				DeliveryOwnerHost: "host-a",
			},
			hostname:  "host-b",
			wantLevel: "INFO",
			wantMsg:   "disabled on host",
		},
		{
			name: "owner ignored when api disabled",
			cfg: config.Config{
				DeliveryOwnerHost: "host-a",
			},
			hostname:  "host-a",
			wantLevel: "INFO",
			wantMsg:   "ignored because API_PORT is disabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deliveryWorkersShouldStart(&tt.cfg, tt.hostname); got != tt.wantStart {
				t.Fatalf("deliveryWorkersShouldStart() = %v, want %v", got, tt.wantStart)
			}
			level, msg := deliveryOwnerStatus(&tt.cfg, tt.hostname)
			if level != tt.wantLevel {
				t.Fatalf("deliveryOwnerStatus() level = %q, want %q", level, tt.wantLevel)
			}
			if !strings.Contains(msg, tt.wantMsg) {
				t.Fatalf("deliveryOwnerStatus() message = %q, want substring %q", msg, tt.wantMsg)
			}
		})
	}
}

func TestEnabledLabel(t *testing.T) {
	if got := enabledLabel(true); got != "enabled" {
		t.Fatalf("enabledLabel(true) = %q, want enabled", got)
	}
	if got := enabledLabel(false); got != "disabled" {
		t.Fatalf("enabledLabel(false) = %q, want disabled", got)
	}
}

func TestBucketOwnershipLabel(t *testing.T) {
	if got := bucketOwnershipLabel(&config.Config{}); got != "dynamic jetmon_hosts" {
		t.Fatalf("bucketOwnershipLabel(dynamic) = %q", got)
	}
	min, max := 12, 34
	got := bucketOwnershipLabel(&config.Config{PinnedBucketMin: &min, PinnedBucketMax: &max})
	if got != "pinned range=12-34" {
		t.Fatalf("bucketOwnershipLabel(pinned) = %q", got)
	}
}

func TestRolloutAdviceLines(t *testing.T) {
	dynamic := rolloutAdviceLines(&config.Config{})
	if len(dynamic) != 4 {
		t.Fatalf("dynamic advice len = %d, want 4", len(dynamic))
	}
	if !strings.Contains(dynamic[0], "rollout dynamic-check") {
		t.Fatalf("dynamic preflight advice = %q", dynamic[0])
	}
	if !strings.Contains(dynamic[1], "rollout activity-check") {
		t.Fatalf("dynamic activity advice = %q", dynamic[1])
	}
	if !strings.Contains(dynamic[2], "rollout state-report") {
		t.Fatalf("dynamic state report advice = %q", dynamic[2])
	}
	if !strings.Contains(dynamic[3], "rollout projection-drift") {
		t.Fatalf("dynamic drift advice = %q", dynamic[3])
	}

	min, max := 12, 34
	pinned := rolloutAdviceLines(&config.Config{PinnedBucketMin: &min, PinnedBucketMax: &max})
	if len(pinned) != 7 {
		t.Fatalf("pinned advice len = %d, want 7", len(pinned))
	}
	if !strings.Contains(pinned[0], "rollout static-plan-check") {
		t.Fatalf("pinned static-plan advice = %q", pinned[0])
	}
	if !strings.Contains(pinned[1], "rollout host-preflight") {
		t.Fatalf("pinned preflight advice = %q", pinned[1])
	}
	if !strings.Contains(pinned[2], "rollout activity-check") {
		t.Fatalf("pinned activity advice = %q", pinned[2])
	}
	if !strings.Contains(pinned[3], "rollout cutover-check") {
		t.Fatalf("pinned cutover advice = %q", pinned[3])
	}
	if !strings.Contains(pinned[4], "rollout rollback-check") {
		t.Fatalf("pinned rollback advice = %q", pinned[4])
	}
	if !strings.Contains(pinned[5], "rollout state-report") {
		t.Fatalf("pinned state report advice = %q", pinned[5])
	}
	if !strings.Contains(pinned[6], "rollout projection-drift") {
		t.Fatalf("pinned drift advice = %q", pinned[6])
	}
}

func TestRolloutCommandHelpers(t *testing.T) {
	if got := staticPlanCheckCommand(); got != "./jetmon2 rollout static-plan-check --file=<ranges.csv>" {
		t.Fatalf("staticPlanCheckCommand() = %q", got)
	}
	if got := rolloutPreflightCommand(&config.Config{}); got != "./jetmon2 rollout dynamic-check" {
		t.Fatalf("rolloutPreflightCommand(dynamic) = %q", got)
	}
	min, max := 12, 34
	cfg := &config.Config{PinnedBucketMin: &min, PinnedBucketMax: &max, BucketTotal: 100}
	want := "./jetmon2 rollout host-preflight --file=<ranges.csv> --host=<v1-hostname> --runtime-host=<v2-hostname> --bucket-min=12 --bucket-max=34 --bucket-total=100"
	if got := rolloutPreflightCommand(cfg); got != want {
		t.Fatalf("rolloutPreflightCommand(pinned) = %q", got)
	}
	if got := rolloutActivityCommand(); got != "./jetmon2 rollout activity-check --since=15m" {
		t.Fatalf("rolloutActivityCommand() = %q", got)
	}
	if got := cutoverCheckCommand(&config.Config{}); got != "" {
		t.Fatalf("cutoverCheckCommand(dynamic) = %q, want empty", got)
	}
	if got := cutoverCheckCommand(cfg); got != "./jetmon2 rollout cutover-check --since=15m" {
		t.Fatalf("cutoverCheckCommand(pinned) = %q", got)
	}
	if got := rollbackCheckCommand(&config.Config{}); got != "" {
		t.Fatalf("rollbackCheckCommand(dynamic) = %q, want empty", got)
	}
	if got := rollbackCheckCommand(cfg); got != "./jetmon2 rollout rollback-check" {
		t.Fatalf("rollbackCheckCommand(pinned) = %q", got)
	}
	if got := projectionDriftCommand(); got != "./jetmon2 rollout projection-drift" {
		t.Fatalf("projectionDriftCommand() = %q", got)
	}
	if got := stateReportCommand(); got != "./jetmon2 rollout state-report --since=15m" {
		t.Fatalf("stateReportCommand() = %q", got)
	}
}

func TestDashboardHealthEntriesReportsCoreDependencies(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "logs"), 0755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "stats"), 0755); err != nil {
		t.Fatalf("mkdir stats: %v", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()

	sqlDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()
	mock.ExpectPing()

	checkedAt := time.Date(2026, 4, 28, 3, 0, 0, 0, time.UTC)
	entries := dashboardHealthEntries(context.Background(), &config.Config{}, sqlDB, nil, false, checkedAt)
	byName := make(map[string]string, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry.Status
		if !entry.CheckedAt.Equal(checkedAt) {
			t.Fatalf("%s CheckedAt = %s, want %s", entry.Name, entry.CheckedAt, checkedAt)
		}
	}

	want := map[string]string{
		"mysql":      "green",
		"wpcom":      "red",
		"statsd":     "amber",
		"disk:logs":  "green",
		"disk:stats": "green",
		"verifliers": "amber",
	}
	for name, status := range want {
		if byName[name] != status {
			t.Fatalf("health[%s] = %q, want %q (entries=%v)", name, byName[name], status, entries)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMonitorProcessHealthSnapshot(t *testing.T) {
	started := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{APIPort: 8090, DashboardPort: 8080, DeliveryOwnerHost: "host-a"}
	st := dashboard.State{
		WorkerCount:            12,
		ActiveChecks:           3,
		QueueDepth:             4,
		RetryQueueSize:         5,
		BucketMin:              0,
		BucketMax:              99,
		BucketOwnership:        "pinned range=0-99",
		DeliveryWorkersEnabled: true,
		DeliveryConfigEligible: true,
		DeliveryOwnerHost:      "host-a",
		WPCOMQueueDepth:        2,
		GoSysMemMB:             88,
		RSSMemMB:               99,
	}
	health := []dashboard.HealthEntry{{
		Name:      "mysql",
		Status:    "green",
		CheckedAt: started,
	}}

	snapshot := monitorProcessHealthSnapshot("host-a", started, fleethealth.StateRunning, cfg, st, health)
	if snapshot.HostID != "host-a" {
		t.Fatalf("HostID = %q, want host-a", snapshot.HostID)
	}
	if snapshot.ProcessType != fleethealth.ProcessMonitor {
		t.Fatalf("ProcessType = %q, want monitor", snapshot.ProcessType)
	}
	if snapshot.BucketMin == nil || *snapshot.BucketMin != 0 {
		t.Fatalf("BucketMin = %v, want 0", snapshot.BucketMin)
	}
	if snapshot.APIPort == nil || *snapshot.APIPort != 8090 {
		t.Fatalf("APIPort = %v, want 8090", snapshot.APIPort)
	}
	if snapshot.HealthStatus != fleethealth.HealthGreen {
		t.Fatalf("HealthStatus = %q, want green", snapshot.HealthStatus)
	}
	if snapshot.GoSysMemMB != 88 || snapshot.RSSMemMB != 99 {
		t.Fatalf("memory fields = go=%d rss=%d, want go=88 rss=99", snapshot.GoSysMemMB, snapshot.RSSMemMB)
	}
	if len(snapshot.DependencyHealth) != 1 || snapshot.DependencyHealth[0].Name != "mysql" {
		t.Fatalf("DependencyHealth = %+v, want mysql entry", snapshot.DependencyHealth)
	}
}

func TestDashboardListenAddrDefaultsLocalhost(t *testing.T) {
	cfg := &config.Config{DashboardPort: 8080}
	if got := dashboardListenAddr(cfg); got != "127.0.0.1:8080" {
		t.Fatalf("dashboardListenAddr() = %q, want 127.0.0.1:8080", got)
	}

	cfg.DashboardBindAddr = "0.0.0.0"
	if got := dashboardListenAddr(cfg); got != "0.0.0.0:8080" {
		t.Fatalf("dashboardListenAddr() = %q, want 0.0.0.0:8080", got)
	}
}

func TestDashboardBindWarning(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{name: "empty defaults local", addr: "", want: false},
		{name: "ipv4 loopback", addr: "127.0.0.1", want: false},
		{name: "ipv6 loopback", addr: "::1", want: false},
		{name: "localhost", addr: "localhost", want: false},
		{name: "wildcard", addr: "0.0.0.0", want: true},
		{name: "private address", addr: "10.0.0.5", want: true},
		{name: "hostname", addr: "dashboard.internal", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dashboardBindWarning(tt.addr)
			if tt.want && got == "" {
				t.Fatalf("dashboardBindWarning(%q) = empty, want warning", tt.addr)
			}
			if !tt.want && got != "" {
				t.Fatalf("dashboardBindWarning(%q) = %q, want empty", tt.addr, got)
			}
		})
	}
}

func TestCheckWritableDirReportsMissingDirectory(t *testing.T) {
	err := checkWritableDir(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("checkWritableDir() returned nil for missing directory")
	}
}

func TestParseInt64(t *testing.T) {
	got, err := parseInt64("12345")
	if err != nil {
		t.Fatalf("parseInt64(valid) error = %v", err)
	}
	if got != 12345 {
		t.Fatalf("parseInt64(valid) = %d, want 12345", got)
	}
	if _, err := parseInt64("not-an-id"); err == nil {
		t.Fatal("parseInt64(invalid) returned nil error")
	}
}

func TestCurrentOperatorPrefersUserThenLogname(t *testing.T) {
	t.Setenv("USER", "alice")
	t.Setenv("LOGNAME", "bob")
	if got := currentOperator(); got != "alice" {
		t.Fatalf("currentOperator() = %q, want USER", got)
	}

	t.Setenv("USER", "")
	if got := currentOperator(); got != "bob" {
		t.Fatalf("currentOperator() = %q, want LOGNAME", got)
	}

	t.Setenv("LOGNAME", "")
	if got := currentOperator(); got != "cli" {
		t.Fatalf("currentOperator() = %q, want cli", got)
	}
}

func TestReadPIDFileRejectsInvalidContent(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "test.pid")
	if err := os.WriteFile(pidPath, []byte("0\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("JETMON_PID_FILE", pidPath)

	if os.Getenv("JETMON_TEST_READ_PID_INVALID") == "1" {
		_ = readPIDFile()
		return
	}

	cmd := os.Args[0]
	proc, err := os.StartProcess(cmd, []string{cmd, "-test.run=TestReadPIDFileRejectsInvalidContent"}, &os.ProcAttr{
		Env: append(os.Environ(),
			"JETMON_TEST_READ_PID_INVALID=1",
			"JETMON_PID_FILE="+pidPath,
		),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	state, err := proc.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if state.Success() {
		t.Fatal("readPIDFile accepted invalid PID content")
	}
}

func TestBuildAlertDispatchersIncludesStubEmail(t *testing.T) {
	dispatchers := deliverer.BuildAlertDispatchers(&config.Config{
		EmailTransport: "stub",
		EmailFrom:      "jetmon@example.com",
	})

	for _, transport := range []alerting.Transport{
		alerting.TransportEmail,
		alerting.TransportPagerDuty,
		alerting.TransportSlack,
		alerting.TransportTeams,
	} {
		if dispatchers[transport] == nil {
			t.Fatalf("dispatcher for %s is nil", transport)
		}
	}

	destination, err := json.Marshal(map[string]string{"address": "ops@example.com"})
	if err != nil {
		t.Fatalf("Marshal destination: %v", err)
	}

	status, response, err := dispatchers[alerting.TransportEmail].Send(
		context.Background(),
		destination,
		alerting.Notification{
			SiteID:       123,
			SiteURL:      "https://example.com",
			EventID:      456,
			EventType:    "alert.opened",
			SeverityName: "Down",
			Timestamp:    time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("stub email dispatcher Send() error = %v", err)
	}
	// 250 mirrors the SMTP "Requested mail action okay, completed" reply
	// code so the audit row reads the same shape regardless of which email
	// transport actually fired.
	if status != 250 {
		t.Fatalf("stub email dispatcher status = %d, want 250", status)
	}
	if response != "delivered" {
		t.Fatalf("stub email dispatcher response = %q, want delivered", response)
	}
}

func TestBuildAlertDispatchersSelectsConfiguredEmailSenders(t *testing.T) {
	tests := []struct {
		name      string
		transport string
		wantType  string
	}{
		{name: "smtp", transport: "smtp", wantType: "*alerting.emailDispatcher"},
		{name: "wpcom", transport: "wpcom", wantType: "*alerting.emailDispatcher"},
		{name: "unknown falls back", transport: "sendmail", wantType: "*alerting.emailDispatcher"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatchers := deliverer.BuildAlertDispatchers(&config.Config{
				EmailTransport:     tt.transport,
				EmailFrom:          "jetmon@example.com",
				WPCOMEmailEndpoint: "https://wpcom.example/send",
				SMTPHost:           "smtp.example",
				SMTPPort:           25,
			})
			got := fmt.Sprintf("%T", dispatchers[alerting.TransportEmail])
			if got != tt.wantType {
				t.Fatalf("email dispatcher type = %s, want %s", got, tt.wantType)
			}
		})
	}
}

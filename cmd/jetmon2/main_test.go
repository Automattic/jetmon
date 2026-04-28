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
	"github.com/Automattic/jetmon/internal/deliverer"
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
	if len(dynamic) != 2 {
		t.Fatalf("dynamic advice len = %d, want 2", len(dynamic))
	}
	if !strings.Contains(dynamic[0], "rollout dynamic-check") {
		t.Fatalf("dynamic preflight advice = %q", dynamic[0])
	}
	if !strings.Contains(dynamic[1], "rollout projection-drift") {
		t.Fatalf("dynamic drift advice = %q", dynamic[1])
	}

	min, max := 12, 34
	pinned := rolloutAdviceLines(&config.Config{PinnedBucketMin: &min, PinnedBucketMax: &max})
	if len(pinned) != 2 {
		t.Fatalf("pinned advice len = %d, want 2", len(pinned))
	}
	if !strings.Contains(pinned[0], "rollout pinned-check") {
		t.Fatalf("pinned preflight advice = %q", pinned[0])
	}
	if !strings.Contains(pinned[1], "rollout projection-drift") {
		t.Fatalf("pinned drift advice = %q", pinned[1])
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

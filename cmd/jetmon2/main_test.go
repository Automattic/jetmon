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

func TestBuildAlertDispatchersIncludesStubEmail(t *testing.T) {
	dispatchers := buildAlertDispatchers(&config.Config{
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

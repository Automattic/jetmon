package opsalerts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNotifySendsSlackPayload(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want application/json", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := New(Config{
		Enabled:         true,
		SlackWebhookURL: srv.URL,
		MinSeverity:     SeverityInfo,
		Service:         "monitor",
		Host:            "monitor-1",
		Version:         "v2",
		HTTPClient:      srv.Client(),
	})
	err := client.Notify(context.Background(), Alert{
		Severity: SeverityCritical,
		Code:     "verifier_quorum_below_floor",
		Summary:  "0 healthy Verifliers",
		Impact:   "Downtime confirmation is deferred.",
		Details:  map[string]any{"configured": 3, "healthy": 0},
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if payload["text"] == "" {
		t.Fatalf("payload missing text: %#v", payload)
	}
	if _, ok := payload["blocks"].([]any); !ok {
		t.Fatalf("payload missing blocks: %#v", payload)
	}
}

func TestNotifyHonorsSeverityAndDedupe(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := New(Config{
		Enabled:         true,
		SlackWebhookURL: srv.URL,
		MinSeverity:     SeverityError,
		RepeatInterval:  time.Hour,
		HTTPClient:      srv.Client(),
	})
	if err := client.Notify(context.Background(), Alert{Severity: SeverityWarning, Code: "below_threshold", Summary: "warn"}); err != nil {
		t.Fatalf("Notify warning: %v", err)
	}
	if calls != 0 {
		t.Fatalf("calls after warning = %d, want 0", calls)
	}
	alert := Alert{Severity: SeverityError, Code: "same_code", Summary: "error"}
	if err := client.Notify(context.Background(), alert); err != nil {
		t.Fatalf("Notify error: %v", err)
	}
	if err := client.Notify(context.Background(), alert); err != nil {
		t.Fatalf("Notify duplicate: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls after duplicate = %d, want 1", calls)
	}
}

func TestNormalizeSeverity(t *testing.T) {
	tests := map[string]string{
		"info":     SeverityInfo,
		"warn":     SeverityWarning,
		"warning":  SeverityWarning,
		"err":      SeverityError,
		"error":    SeverityError,
		"crit":     SeverityCritical,
		"critical": SeverityCritical,
		"bogus":    "",
	}
	for in, want := range tests {
		if got := NormalizeSeverity(in); got != want {
			t.Fatalf("NormalizeSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

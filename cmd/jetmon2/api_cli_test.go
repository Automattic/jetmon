package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPIRequestURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		target  string
		want    string
		wantErr bool
	}{
		{
			name:    "absolute path",
			baseURL: "http://localhost:8090",
			target:  "/api/v1/health",
			want:    "http://localhost:8090/api/v1/health",
		},
		{
			name:    "relative path",
			baseURL: "http://localhost:8090/",
			target:  "api/v1/me",
			want:    "http://localhost:8090/api/v1/me",
		},
		{
			name:    "absolute url",
			baseURL: "http://localhost:8090",
			target:  "http://127.0.0.1:9000/api/v1/health",
			want:    "http://127.0.0.1:9000/api/v1/health",
		},
		{
			name:    "base requires host",
			baseURL: "localhost:8090",
			target:  "/api/v1/health",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := apiRequestURL(tt.baseURL, tt.target)
			if tt.wantErr {
				if err == nil {
					t.Fatal("apiRequestURL() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("apiRequestURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("apiRequestURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecuteAPIRequestSendsAuthAndVerboseHeaders(t *testing.T) {
	var sawAuth, sawIDKey bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got == "Bearer token-123" {
			sawAuth = true
		}
		if got := r.Header.Get("Idempotency-Key"); got == "idem-1" {
			sawIDKey = true
		}
		w.Header().Set("X-Test-Response", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	opts := apiCLIOptions{
		baseURL:        srv.URL,
		token:          "token-123",
		idempotencyKey: "idem-1",
		verbose:        true,
		pretty:         true,
		timeout:        time.Second,
		out:            &stdout,
		errOut:         &stderr,
	}
	if err := executeAPIRequest(context.Background(), srv.Client(), opts, http.MethodPost, "/api/v1/sites/42/trigger-now", []byte(`{}`)); err != nil {
		t.Fatalf("executeAPIRequest() error = %v", err)
	}
	if !sawAuth {
		t.Fatal("Authorization header was not sent")
	}
	if !sawIDKey {
		t.Fatal("Idempotency-Key header was not sent")
	}
	if got := stdout.String(); !strings.Contains(got, "{\n  \"ok\": true\n}") {
		t.Fatalf("stdout = %q, want pretty JSON body", got)
	}
	errOut := stderr.String()
	for _, want := range []string{
		"> POST /api/v1/sites/42/trigger-now HTTP/1.1",
		"> Authorization: Bearer token-123",
		"> Idempotency-Key: idem-1",
		"< HTTP/1.1 201 Created",
		"< X-Test-Response: yes",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

func TestExecuteAPIRequestReturnsErrorForHTTPFailureAfterWritingBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"missing token"}`))
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	opts := apiCLIOptions{
		baseURL: srv.URL,
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
	}
	err := executeAPIRequest(context.Background(), srv.Client(), opts, http.MethodGet, "/api/v1/me", nil)
	if err == nil {
		t.Fatal("executeAPIRequest() error = nil, want error")
	}
	if got := stdout.String(); !strings.Contains(got, `"missing token"`) {
		t.Fatalf("stdout = %q, want error body", got)
	}
}

func TestAPIFlagUsageUsesLongDashesAndHidesTokenDefault(t *testing.T) {
	var stderr bytes.Buffer
	opts := apiCLIOptions{
		baseURL: "http://localhost:8090",
		token:   "token-should-not-print",
		timeout: 10 * time.Second,
		errOut:  &stderr,
	}
	fs := newAPIFlagSet("api health", &opts)
	fs.Usage()

	got := stderr.String()
	for _, want := range []string{
		"Usage of api health:",
		"--base-url string",
		"--header value",
		"--output string",
		"--pretty",
		"--timeout duration",
		"--token string",
		"-v",
		"--verbose",
		`API base URL (default "http://localhost:8090")`,
		`request timeout (default 10s)`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("usage missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"  -base-url",
		"  -header",
		"  -output",
		"  -pretty",
		"  -timeout",
		"  -token",
		"  -verbose",
		"token-should-not-print",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("usage contains %q:\n%s", unwanted, got)
		}
	}
}

func TestAPIHelpReturnsFlagErrHelp(t *testing.T) {
	var stderr bytes.Buffer
	opts := apiCLIOptions{baseURL: "http://localhost:8090", timeout: 10 * time.Second, errOut: &stderr}
	fs := newAPIFlagSet("api health", &opts)
	err := parseAPIFlags(fs, []string{"--help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Parse(--help) error = %v, want flag.ErrHelp", err)
	}
	if got := stderr.String(); !strings.Contains(got, "--base-url string") {
		t.Fatalf("usage = %q, want long-dash flag output", got)
	}
}

func TestParseAPIFlagsAllowsFlagsAfterPositionals(t *testing.T) {
	var stderr bytes.Buffer
	opts := apiCLIOptions{baseURL: "http://localhost:8090", timeout: 10 * time.Second, errOut: &stderr}
	fs := newAPIFlagSet("api sites get", &opts)

	err := parseAPIFlags(fs, []string{"12345", "--pretty", "--output", "table", "--header", "X-Test: yes"})
	if err != nil {
		t.Fatalf("parseAPIFlags() error = %v", err)
	}
	if !opts.pretty {
		t.Fatal("pretty = false, want true")
	}
	if opts.output != "table" {
		t.Fatalf("output = %q, want table", opts.output)
	}
	if got := opts.headers; len(got) != 1 || got[0] != "X-Test: yes" {
		t.Fatalf("headers = %#v, want X-Test header", got)
	}
	if got := fs.Args(); len(got) != 1 || got[0] != "12345" {
		t.Fatalf("args = %#v, want [12345]", got)
	}
}

func TestParseAPIFlagsPreservesPositionalsAfterDoubleDash(t *testing.T) {
	var stderr bytes.Buffer
	opts := apiCLIOptions{baseURL: "http://localhost:8090", timeout: 10 * time.Second, errOut: &stderr}
	fs := newAPIFlagSet("api request", &opts)

	err := parseAPIFlags(fs, []string{"GET", "--", "--not-a-flag"})
	if err != nil {
		t.Fatalf("parseAPIFlags() error = %v", err)
	}
	if got := fs.Args(); len(got) != 2 || got[0] != "GET" || got[1] != "--not-a-flag" {
		t.Fatalf("args = %#v, want GET and literal --not-a-flag", got)
	}
}

func TestWriteAPIResponseTableForSiteList(t *testing.T) {
	body := []byte(`{
		"data": [
			{"id": 42, "monitor_url": "https://example.com", "monitor_active": true, "current_state": "Up", "current_severity": 0},
			{"id": 43, "monitor_url": "https://wordpress.com", "monitor_active": false, "current_state": "Paused", "current_severity": 0}
		],
		"page": {"limit": 50}
	}`)
	var out bytes.Buffer
	if err := writeAPIResponseTable(&out, body); err != nil {
		t.Fatalf("writeAPIResponseTable() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"id  monitor_url            monitor_active  current_state  current_severity",
		"42  https://example.com    true            Up             0",
		"43  https://wordpress.com  false           Paused         0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q:\n%s", want, got)
		}
	}
}

func TestWriteAPIResponseTableUsesNestedWorkflowRows(t *testing.T) {
	body := []byte(`{
		"mode": "http-500",
		"sites": [
			{"site_id": 42, "action": "updated", "note": "no active events returned"},
			{"site_id": 43, "action": "created", "error": "trigger failed"}
		]
	}`)
	var out bytes.Buffer
	if err := writeAPIResponseTable(&out, body); err != nil {
		t.Fatalf("writeAPIResponseTable() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"site_id  action   note                       error",
		"42       updated  no active events returned",
		"43       created                             trigger failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q:\n%s", want, got)
		}
	}
}

func TestWriteAPIResponseTableIncludesSimulationSummaryColumns(t *testing.T) {
	body := []byte(`{
		"mode": "http-500",
		"sites": [
			{
				"site_id": 42,
				"action": "updated",
				"trigger_status": "failed_http_500",
				"event_ids": [99],
				"event_states": ["Seems Down"],
				"event_severities": [3],
				"transition_count": 1
			}
		]
	}`)
	var out bytes.Buffer
	if err := writeAPIResponseTable(&out, body); err != nil {
		t.Fatalf("writeAPIResponseTable() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"site_id  action   trigger_status   event_ids  event_states  event_severities  transition_count",
		"42       updated  failed_http_500  99         Seems Down    3                 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q:\n%s", want, got)
		}
	}
}

func TestWriteAPIResponseTableIncludesSmokeCleanupRows(t *testing.T) {
	body := []byte(`{
		"steps": [
			{"name": "health", "status": "ok"},
			{"name": "me", "status": "ok"}
		],
		"cleanup_results": [
			{"resource": "alert_contact", "id": 77, "status": "deleted"},
			{"resource": "site", "id": 910, "status": "failed", "error": "not found"}
		]
	}`)
	var out bytes.Buffer
	if err := writeAPIResponseTable(&out, body); err != nil {
		t.Fatalf("writeAPIResponseTable() error = %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"kind     name           id   status   detail",
		"step     health              ok",
		"cleanup  alert_contact  77   deleted",
		"cleanup  site           910  failed   not found",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("table missing %q:\n%s", want, got)
		}
	}
}

func TestWriteAPIResponseTableFallsBackToSortedColumns(t *testing.T) {
	body := []byte(`{"zeta":"last","alpha":"first"}`)
	var out bytes.Buffer
	if err := writeAPIResponseTable(&out, body); err != nil {
		t.Fatalf("writeAPIResponseTable() error = %v", err)
	}
	if got := out.String(); !strings.HasPrefix(got, "alpha  zeta\n") {
		t.Fatalf("table = %q, want sorted fallback columns", got)
	}
}

func TestWriteAPIOutputRejectsUnknownFormat(t *testing.T) {
	err := writeAPIOutput(ioDiscard{}, []byte(`{"ok":true}`), apiCLIOptions{output: "yaml"})
	if err == nil {
		t.Fatal("writeAPIOutput() error = nil, want bad output format")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunAPISitesSimulateFailureUpdatesAndReportsEvents(t *testing.T) {
	var severity apiOptionalIntFlag
	setTestFlag(t, &severity, "3")
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.String())
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/sites/42":
			var body map[string]any
			decodeTestJSON(t, r, &body)
			if body["monitor_url"] != "https://httpbin.org/status/500" {
				t.Fatalf("monitor_url = %#v, want http-500 URL", body["monitor_url"])
			}
			writeTestJSON(t, w, map[string]any{"id": 42, "monitor_url": body["monitor_url"]})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sites/42/trigger-now":
			writeTestJSON(t, w, map[string]any{"result": map[string]any{"success": false, "http_code": 500}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/42/events":
			writeTestJSON(t, w, map[string]any{
				"data": []any{map[string]any{"id": 99, "state": "Seems Down", "severity": 3}},
				"page": map[string]any{"limit": 10},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/42/events/99/transitions":
			writeTestJSON(t, w, map[string]any{
				"data": []any{map[string]any{
					"id":             1,
					"event_id":       99,
					"severity_after": 3,
					"state_after":    "Seems Down",
					"reason":         "opened",
				}},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	err := runAPISitesSimulateFailure(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		token:   "token-123",
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
	}, apiSitesSimulateFailureOptions{
		mode:                   "http-500",
		siteIDs:                mustSiteIDs(t, "42"),
		trigger:                true,
		pollInterval:           time.Millisecond,
		expectEventState:       "Seems Down",
		expectEventSeverity:    severity,
		requireTransition:      true,
		expectTransitionReason: "opened",
	})
	if err != nil {
		t.Fatalf("runAPISitesSimulateFailure() error = %v\nstdout=%s", err, stdout.String())
	}
	var summary apiSimulateFailureSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v\n%s", err, stdout.String())
	}
	if summary.Mode != "http-500" || len(summary.Sites) != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Sites[0].Action != "updated" {
		t.Fatalf("action = %q, want updated", summary.Sites[0].Action)
	}
	if summary.Sites[0].TriggerStatus != "failed_http_500" {
		t.Fatalf("trigger status = %q, want failed_http_500", summary.Sites[0].TriggerStatus)
	}
	if got := summary.Sites[0].EventIDs; len(got) != 1 || got[0] != 99 {
		t.Fatalf("event ids = %#v, want [99]", got)
	}
	if got := summary.Sites[0].EventStates; len(got) != 1 || got[0] != "Seems Down" {
		t.Fatalf("event states = %#v, want [Seems Down]", got)
	}
	if got := summary.Sites[0].EventSeverities; len(got) != 1 || got[0] != 3 {
		t.Fatalf("event severities = %#v, want [3]", got)
	}
	if summary.Sites[0].TransitionCount != 1 {
		t.Fatalf("transition count = %d, want 1", summary.Sites[0].TransitionCount)
	}
	if len(summary.Sites[0].Transitions) != 1 || summary.Sites[0].Transitions[0].EventID != 99 {
		t.Fatalf("transitions = %#v, want event 99", summary.Sites[0].Transitions)
	}
	wantCalls := []string{
		"PATCH /api/v1/sites/42",
		"POST /api/v1/sites/42/trigger-now",
		"GET /api/v1/sites/42/events?active=true&limit=10",
		"GET /api/v1/sites/42/events/99/transitions",
	}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(wantCalls, "\n"))
	}
}

func TestRunAPISitesSimulateFailurePollsUntilAssertionsMatch(t *testing.T) {
	var eventPolls int
	var transitionPolls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/sites/42":
			writeTestJSON(t, w, map[string]any{"id": 42})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/42/events":
			eventPolls++
			state := "Seems Down"
			severity := 3
			if eventPolls > 1 {
				state = "Down"
				severity = 4
			}
			writeTestJSON(t, w, map[string]any{
				"data": []any{map[string]any{"id": 99, "state": state, "severity": severity}},
				"page": map[string]any{"limit": 10},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/42/events/99/transitions":
			transitionPolls++
			reason := "opened"
			if transitionPolls > 1 {
				reason = "verifier_confirmed"
			}
			writeTestJSON(t, w, map[string]any{
				"data": []any{map[string]any{"id": transitionPolls, "event_id": 99, "reason": reason}},
			})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	err := runAPISitesSimulateFailure(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
	}, apiSitesSimulateFailureOptions{
		mode:                   "http-500",
		siteIDs:                mustSiteIDs(t, "42"),
		trigger:                false,
		wait:                   100 * time.Millisecond,
		pollInterval:           time.Millisecond,
		expectEventState:       "Down",
		expectTransitionReason: "verifier_confirmed",
	})
	if err != nil {
		t.Fatalf("runAPISitesSimulateFailure() error = %v\nstdout=%s", err, stdout.String())
	}
	if eventPolls < 2 || transitionPolls < 2 {
		t.Fatalf("eventPolls=%d transitionPolls=%d, want at least 2 each", eventPolls, transitionPolls)
	}
}

func TestRunAPISitesSimulateFailureFailsWhenAssertionsDoNotMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/sites/42":
			writeTestJSON(t, w, map[string]any{"id": 42})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/42/events":
			writeTestJSON(t, w, map[string]any{"data": []any{}, "page": map[string]any{"limit": 10}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	err := runAPISitesSimulateFailure(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
	}, apiSitesSimulateFailureOptions{
		mode:              "http-500",
		siteIDs:           mustSiteIDs(t, "42"),
		trigger:           false,
		pollInterval:      time.Millisecond,
		expectEventState:  "Seems Down",
		requireTransition: true,
	})
	if err == nil {
		t.Fatalf("runAPISitesSimulateFailure() error = nil\nstdout=%s", stdout.String())
	}
	if !strings.Contains(err.Error(), `expected active event state "Seems Down"`) {
		t.Fatalf("error = %v, want event-state assertion failure", err)
	}
	if !strings.Contains(stdout.String(), "expected at least one transition") {
		t.Fatalf("stdout = %s, want transition assertion failure", stdout.String())
	}
}

func TestRunAPISitesSimulateFailureCanCreateMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/sites/42":
			writeTestStatusJSON(t, w, http.StatusNotFound, map[string]string{"code": "site_not_found"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sites":
			var body map[string]any
			decodeTestJSON(t, r, &body)
			if body["blog_id"] != float64(42) {
				t.Fatalf("blog_id = %#v, want 42", body["blog_id"])
			}
			writeTestStatusJSON(t, w, http.StatusCreated, map[string]any{"id": 42})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/42/events":
			writeTestJSON(t, w, map[string]any{"data": []any{}, "page": map[string]any{"limit": 10}})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	err := runAPISitesSimulateFailure(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
	}, apiSitesSimulateFailureOptions{
		mode:          "keyword",
		siteIDs:       mustSiteIDs(t, "42"),
		createMissing: true,
		trigger:       false,
		pollInterval:  time.Millisecond,
	})
	if err != nil {
		t.Fatalf("runAPISitesSimulateFailure() error = %v\nstdout=%s", err, stdout.String())
	}
	var summary apiSimulateFailureSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v\n%s", err, stdout.String())
	}
	if summary.Sites[0].Action != "created" {
		t.Fatalf("action = %q, want created", summary.Sites[0].Action)
	}
}

func TestRunAPISitesSimulateFailureRejectsUnmatchedBatchMarker(t *testing.T) {
	start := apiCLIBatchBlogIDStart("simulation-batch")
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/"+strconvInt64(start):
			writeTestJSON(t, w, map[string]any{"id": start, "cli_batch": "other-batch"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	err := runAPISitesSimulateFailure(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
	}, apiSitesSimulateFailureOptions{
		mode:         "http-500",
		batch:        "simulation-batch",
		count:        1,
		trigger:      false,
		pollInterval: time.Millisecond,
	})
	if err == nil {
		t.Fatal("runAPISitesSimulateFailure() error = nil, want batch mismatch")
	}
	if !strings.Contains(err.Error(), `does not belong to CLI batch "simulation-batch"`) {
		t.Fatalf("error = %v, want batch mismatch", err)
	}
	if strings.Join(calls, "\n") != "GET /api/v1/sites/"+strconvInt64(start) {
		t.Fatalf("calls:\n%s\nwant only GET", strings.Join(calls, "\n"))
	}
}

func TestAPISimulationSiteIDsFromBatch(t *testing.T) {
	ids, err := apiSimulationSiteIDs(apiSitesSimulateFailureOptions{batch: "batch-a", count: 3})
	if err != nil {
		t.Fatalf("apiSimulationSiteIDs() error = %v", err)
	}
	start := apiCLIBatchBlogIDStart("batch-a")
	want := []int64{start, start + 1, start + 2}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids[%d] = %d, want %d", i, ids[i], want[i])
		}
	}
}

func TestAPIFailureModesCoverRoadmapTargets(t *testing.T) {
	for _, mode := range []string{"unreachable", "http-500", "http-403", "redirect", "keyword", "timeout", "tls"} {
		t.Run(mode, func(t *testing.T) {
			def, err := apiFailureMode(mode, "")
			if err != nil {
				t.Fatalf("apiFailureMode(%q) error = %v", mode, err)
			}
			if def.MonitorURL == "" || def.RedirectPolicy == "" {
				t.Fatalf("definition = %#v, want URL and redirect policy", def)
			}
		})
	}
}

func TestAPIFailureModesPreferFixtureWhenConfigured(t *testing.T) {
	tests := []struct {
		mode string
		url  string
	}{
		{mode: "http-500", url: "http://api-fixture:8091/status/500"},
		{mode: "http-403", url: "http://api-fixture:8091/status/403"},
		{mode: "redirect", url: "http://api-fixture:8091/redirect"},
		{mode: "keyword", url: "http://api-fixture:8091/keyword"},
		{mode: "timeout", url: "http://api-fixture:8091/slow?delay=5s"},
		{mode: "tls", url: "https://api-fixture:8443/tls"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			def, err := apiFailureMode(tt.mode, "http://api-fixture:8091")
			if err != nil {
				t.Fatalf("apiFailureMode(%q) error = %v", tt.mode, err)
			}
			if def.MonitorURL != tt.url {
				t.Fatalf("MonitorURL = %q, want %q", def.MonitorURL, tt.url)
			}
		})
	}
}

func TestAPISimulationFixtureURLAutoDetection(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("probe path = %q, want /health", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer fixture.Close()

	got := apiSimulationFixtureURL(context.Background(), apiSitesSimulateFailureOptions{
		fixtureURL:      apiFixtureAuto,
		fixtureProbeURL: fixture.URL + "/health",
	})
	if got != defaultAPIFixtureMonitorURL {
		t.Fatalf("fixture URL = %q, want default Docker monitor URL", got)
	}

	got = apiSimulationFixtureURL(context.Background(), apiSitesSimulateFailureOptions{
		fixtureURL:      apiFixtureAuto,
		fixtureProbeURL: "http://127.0.0.1:1/health",
	})
	if got != "" {
		t.Fatalf("fixture URL = %q, want fallback to public endpoints", got)
	}
}

func mustSiteIDs(t *testing.T, raw string) apiInt64SliceFlags {
	t.Helper()
	var ids apiInt64SliceFlags
	if err := ids.Set(raw); err != nil {
		t.Fatalf("set site ids: %v", err)
	}
	return ids
}

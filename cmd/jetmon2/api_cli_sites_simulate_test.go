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
				"data": []any{map[string]any{"id": 99, "state": "Seems Down"}},
				"page": map[string]any{"limit": 10},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/42/events/99/transitions":
			writeTestJSON(t, w, map[string]any{"data": []any{map[string]any{"id": 1, "event_id": 99}}})
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
		mode:         "http-500",
		siteIDs:      mustSiteIDs(t, "42"),
		trigger:      true,
		pollInterval: time.Millisecond,
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
			def, err := apiFailureMode(mode)
			if err != nil {
				t.Fatalf("apiFailureMode(%q) error = %v", mode, err)
			}
			if def.MonitorURL == "" || def.RedirectPolicy == "" {
				t.Fatalf("definition = %#v, want URL and redirect policy", def)
			}
		})
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

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

func TestRunAPISmokeHappyPath(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Path != "/api/v1/health" && r.Header.Get("Authorization") != "Bearer token-123" {
			t.Fatalf("missing auth for %s %s", r.Method, r.URL.Path)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/health":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			writeTestJSON(t, w, map[string]any{"consumer_name": "api-cli-test", "scope": "admin"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sites":
			var body map[string]any
			decodeTestJSON(t, r, &body)
			if body["blog_id"] != float64(910) {
				t.Fatalf("blog_id = %#v, want 910", body["blog_id"])
			}
			headers := body["custom_headers"].(map[string]any)
			if headers[apiCLIBatchHeader] != "smoke-test" {
				t.Fatalf("batch header = %#v, want smoke-test", headers[apiCLIBatchHeader])
			}
			writeTestStatusJSON(t, w, http.StatusCreated, map[string]any{"id": 910, "blog_id": 910})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sites/910/trigger-now":
			writeTestJSON(t, w, map[string]any{"result": map[string]any{"success": true}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/910/events":
			writeTestJSON(t, w, map[string]any{"data": []any{}, "page": map[string]any{"limit": 5}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/alert-contacts":
			writeTestStatusJSON(t, w, http.StatusCreated, map[string]any{"id": 77, "label": "api-cli-smoke-smoke-test"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/alert-contacts/77/test":
			writeTestJSON(t, w, map[string]any{"contact_id": 77, "delivered": true})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/alert-contacts/77":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/sites/910":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	err := runAPISmoke(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		token:   "token-123",
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
	}, apiSmokeOptions{
		batch:    "smoke-test",
		blogID:   910,
		url:      "https://example.com/",
		cleanup:  true,
		exercise: "alert-contact",
	})
	if err != nil {
		t.Fatalf("runAPISmoke() error = %v\nstdout=%s", err, stdout.String())
	}

	var summary apiSmokeSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v\n%s", err, stdout.String())
	}
	if summary.Batch != "smoke-test" || summary.BlogID != 910 {
		t.Fatalf("summary batch/id = %q/%d", summary.Batch, summary.BlogID)
	}
	if len(summary.Steps) != 7 {
		t.Fatalf("steps = %#v, want 7 steps", summary.Steps)
	}
	for _, step := range summary.Steps {
		if step.Status != "ok" {
			t.Fatalf("step %#v, want ok", step)
		}
	}
	if len(summary.CleanupResults) != 2 {
		t.Fatalf("cleanup results = %#v, want contact and site cleanup", summary.CleanupResults)
	}
	wantCalls := []string{
		"GET /api/v1/health",
		"GET /api/v1/me",
		"POST /api/v1/sites",
		"POST /api/v1/sites/910/trigger-now",
		"GET /api/v1/sites/910/events",
		"POST /api/v1/alert-contacts",
		"POST /api/v1/alert-contacts/77/test",
		"DELETE /api/v1/alert-contacts/77",
		"DELETE /api/v1/sites/910",
	}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(wantCalls, "\n"))
	}
}

func TestRunAPISmokeWritesFailureSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case "/api/v1/me":
			writeTestStatusJSON(t, w, http.StatusUnauthorized, map[string]string{"error": "missing token"})
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	err := runAPISmoke(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
	}, apiSmokeOptions{
		batch:    "smoke-failure",
		blogID:   911,
		cleanup:  true,
		exercise: "none",
	})
	if err == nil {
		t.Fatal("runAPISmoke() error = nil, want auth failure")
	}
	var summary apiSmokeSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v\n%s", err, stdout.String())
	}
	if len(summary.Steps) != 2 {
		t.Fatalf("steps = %#v, want health + failed me", summary.Steps)
	}
	if summary.Steps[1].Name != "me" || summary.Steps[1].Status != "failed" {
		t.Fatalf("failed step = %#v, want me failed", summary.Steps[1])
	}
}

func TestAPICLIBatchBlogIDStartStable(t *testing.T) {
	first := apiCLIBatchBlogIDStart("batch-a")
	second := apiCLIBatchBlogIDStart("batch-a")
	if first != second {
		t.Fatalf("batch id start not stable: %d != %d", first, second)
	}
	if first < 910000000 || first >= 1000000000 {
		t.Fatalf("batch id start = %d, want high local-test range", first)
	}
}

func decodeTestJSON(t *testing.T, r *http.Request, v any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	writeTestStatusJSON(t, w, http.StatusOK, v)
}

func writeTestStatusJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunAPIRolloutGuidedDryRun(t *testing.T) {
	var stdout bytes.Buffer
	err := runAPIRolloutGuided(context.Background(), nil, apiCLIOptions{
		baseURL: "https://jetmon-api.example.test",
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
		in:      strings.NewReader(""),
	}, apiRolloutGuidedOptions{
		bucketMin:              0,
		bucketMax:              9,
		sampleSize:             25,
		compareSampleSize:      50,
		since:                  "30m",
		dryRun:                 true,
		includeComparison:      true,
		includePolicyMigration: true,
	})
	if err != nil {
		t.Fatalf("runAPIRolloutGuided() error = %v", err)
	}
	got := stdout.String()
	for _, want := range []string{
		"Jetmon API rollout guided flow",
		"bucket_range: 0-9",
		"mode: dry-run",
		"DRY-RUN request: POST /api/v1/rollout/preflight",
		`"mode":"standby"`,
		"DRY-RUN manual confirmation required: Confirm Systems has stopped v1 for this range. Type V1 STOPPED 0-9 to continue.",
		"DRY-RUN request: POST /api/v1/rollout/compare-methods",
		"DRY-RUN request: POST /api/v1/rollout/stage-policy",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, got)
		}
	}
}

func TestRunAPIRolloutGuidedHappyPath(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer token-123" {
			t.Fatalf("missing auth for %s %s", r.Method, r.URL.Path)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/health":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			writeTestJSON(t, w, map[string]any{"consumer_name": "rollout-test", "scope": "admin"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/rollout/preflight":
			body := decodeRolloutTestBody(t, r)
			requireRolloutTestNumber(t, body, "bucket_min", 0)
			requireRolloutTestNumber(t, body, "bucket_max", 99)
			if body["mode"] != "standby" {
				t.Fatalf("preflight mode = %#v, want standby", body["mode"])
			}
			writeTestJSON(t, w, map[string]any{"status": "ok", "summary": "preflight clean"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/rollout/smoke":
			body := decodeRolloutTestBody(t, r)
			if body["mode"] != apiRolloutModeHeadLegacy {
				t.Fatalf("smoke mode = %#v, want %s", body["mode"], apiRolloutModeHeadLegacy)
			}
			requireRolloutTestNumber(t, body, "sample_size", 5)
			if body["read_only"] != true {
				t.Fatalf("smoke read_only = %#v, want true", body["read_only"])
			}
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/rollout/seed":
			body := decodeRolloutTestBody(t, r)
			if body["dry_run"] == true {
				writeTestJSON(t, w, map[string]string{"status": "ok", "confirmation_token": "seed-token"})
				return
			}
			if body["execute"] != true || body["confirm"] != "seed-token" {
				t.Fatalf("seed execute body = %#v, want execute with seed-token", body)
			}
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/rollout/activate-buckets":
			body := decodeRolloutTestBody(t, r)
			if body["dry_run"] == true {
				writeTestJSON(t, w, map[string]string{"status": "ok", "confirmation_token": "activate-token"})
				return
			}
			if body["execute"] != true || body["confirm"] != "activate-token" {
				t.Fatalf("activate execute body = %#v, want execute with activate-token", body)
			}
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/rollout/status":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/rollout/bucket-coverage":
			if r.URL.Query().Get("bucket_min") != "0" || r.URL.Query().Get("bucket_max") != "99" {
				t.Fatalf("bucket coverage query = %q", r.URL.RawQuery)
			}
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/rollout/activity-check":
			if r.URL.Query().Get("since") != "15m" {
				t.Fatalf("activity since = %q, want 15m", r.URL.Query().Get("since"))
			}
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/rollout/projection-drift":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	input := strings.Join([]string{
		"YES",
		"YES",
		"YES",
		"EXECUTE SEED",
		"V1 STOPPED 0-99",
		"YES",
		"ACTIVATE 0-99",
		"",
	}, "\n")
	var stdout bytes.Buffer
	err := runAPIRolloutGuided(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		token:   "token-123",
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
		in:      strings.NewReader(input),
	}, apiRolloutGuidedOptions{
		bucketMin:         0,
		bucketMax:         99,
		sampleSize:        5,
		compareSampleSize: 50,
		since:             "15m",
	})
	if err != nil {
		t.Fatalf("runAPIRolloutGuided() error = %v\nstdout=%s", err, stdout.String())
	}
	wantCalls := []string{
		"GET /api/v1/health",
		"GET /api/v1/me",
		"POST /api/v1/rollout/preflight",
		"POST /api/v1/rollout/smoke",
		"POST /api/v1/rollout/seed",
		"POST /api/v1/rollout/seed",
		"POST /api/v1/rollout/activate-buckets",
		"POST /api/v1/rollout/activate-buckets",
		"GET /api/v1/rollout/status",
		"GET /api/v1/rollout/bucket-coverage",
		"GET /api/v1/rollout/activity-check",
		"GET /api/v1/rollout/projection-drift",
	}
	if strings.Join(calls, "\n") != strings.Join(wantCalls, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(wantCalls, "\n"))
	}
	if got := stdout.String(); !strings.Contains(got, "PASS activate_execute status=ok") {
		t.Fatalf("stdout missing activation pass:\n%s", got)
	}
}

func TestRunAPIRolloutGuidedMissingEndpointHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/health":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/rollout/preflight":
			writeTestStatusJSON(t, w, http.StatusNotFound, map[string]string{"error": "not found"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	err := runAPIRolloutGuided(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		token:   "token-123",
		timeout: time.Second,
		out:     ioDiscard{},
		errOut:  ioDiscard{},
		in:      strings.NewReader("YES\n"),
	}, apiRolloutGuidedOptions{
		bucketMin:         0,
		bucketMax:         99,
		sampleSize:        5,
		compareSampleSize: 50,
		since:             "15m",
	})
	if err == nil {
		t.Fatal("runAPIRolloutGuided() error = nil, want missing endpoint error")
	}
	if !strings.Contains(err.Error(), "rollout API endpoint is not available") {
		t.Fatalf("error = %v, want missing endpoint hint", err)
	}
}

func TestRunAPIRolloutGuidedRequiresDryRunConfirmationToken(t *testing.T) {
	var seedExecuteCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/health":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/rollout/preflight":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/rollout/smoke":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/rollout/seed":
			body := decodeRolloutTestBody(t, r)
			if body["dry_run"] == true {
				writeTestJSON(t, w, map[string]string{"status": "ok"})
				return
			}
			seedExecuteCalled = true
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	err := runAPIRolloutGuided(context.Background(), srv.Client(), apiCLIOptions{
		baseURL: srv.URL,
		token:   "token-123",
		timeout: time.Second,
		out:     ioDiscard{},
		errOut:  ioDiscard{},
		in:      strings.NewReader("YES\nYES\nYES\n"),
	}, apiRolloutGuidedOptions{
		bucketMin:         0,
		bucketMax:         99,
		sampleSize:        5,
		compareSampleSize: 50,
		since:             "15m",
	})
	if err == nil || !strings.Contains(err.Error(), "missing confirmation token") {
		t.Fatalf("runAPIRolloutGuided() error = %v, want missing token error", err)
	}
	if seedExecuteCalled {
		t.Fatal("seed execute was called without a dry-run confirmation token")
	}
}

func decodeRolloutTestBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		t.Fatalf("decode request body: %v", err)
	}
	return body
}

func requireRolloutTestNumber(t *testing.T, body map[string]any, key string, want float64) {
	t.Helper()
	if body[key] != want {
		t.Fatalf("%s = %#v, want %v", key, body[key], want)
	}
}

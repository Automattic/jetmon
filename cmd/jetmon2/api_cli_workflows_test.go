package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

func TestRunAPISmokeWebhookExercise(t *testing.T) {
	const webhookSecret = "whsec_TESTSMOKESECRET"

	fixture := newSmokeWebhookFixture(t)
	defer fixture.Close()

	var (
		calls         []string
		triggerCalls  int
		registeredURL string
	)
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
			writeTestStatusJSON(t, w, http.StatusCreated, map[string]any{"id": 910, "blog_id": 910})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sites/910/trigger-now":
			triggerCalls++
			if triggerCalls == 2 {
				postSignedSmokeWebhook(t, registeredURL, webhookSecret, []byte(`{"type":"event.opened","site_id":910}`))
				writeTestJSON(t, w, map[string]any{"result": map[string]any{"success": false, "http_code": 500}})
				return
			}
			writeTestJSON(t, w, map[string]any{"result": map[string]any{"success": true}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/910/events" && r.URL.RawQuery == "limit=5":
			writeTestJSON(t, w, map[string]any{"data": []any{}, "page": map[string]any{"limit": 5}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/910" && r.URL.Query().Get("include_cli_metadata") == "true":
			writeTestJSON(t, w, map[string]any{"id": 910, "blog_id": 910, "cli_batch": "smoke-webhook"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/webhooks":
			var body map[string]any
			decodeTestJSON(t, r, &body)
			if body["url"] != fixture.URL+"/webhook" {
				t.Fatalf("webhook url = %#v", body["url"])
			}
			if body["active"] != false {
				t.Fatalf("webhook active = %#v, want false until secret is registered", body["active"])
			}
			writeTestStatusJSON(t, w, http.StatusCreated, map[string]any{
				"id":             88,
				"url":            fixture.URL + "/webhook",
				"active":         false,
				"events":         []string{apiSmokeWebhookEvent},
				"secret_preview": "whsec_TEST...",
				"secret":         webhookSecret,
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/webhooks/88":
			var body map[string]any
			decodeTestJSON(t, r, &body)
			registeredURL = body["url"].(string)
			if !strings.Contains(registeredURL, "secret="+webhookSecret) {
				t.Fatalf("registered URL did not include fixture secret: %q", registeredURL)
			}
			if body["active"] != true {
				t.Fatalf("webhook active = %#v, want true", body["active"])
			}
			writeTestJSON(t, w, map[string]any{
				"id":             88,
				"url":            registeredURL,
				"active":         true,
				"events":         []string{apiSmokeWebhookEvent},
				"secret_preview": "whsec_TEST...",
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/sites/910":
			var body map[string]any
			decodeTestJSON(t, r, &body)
			if !strings.Contains(fmt.Sprint(body["monitor_url"]), "/status/500") {
				t.Fatalf("monitor_url = %#v, want fixture failure URL", body["monitor_url"])
			}
			writeTestJSON(t, w, map[string]any{"id": 910, "blog_id": 910})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/910/events" && r.URL.RawQuery == "active=true&limit=10":
			writeTestJSON(t, w, map[string]any{
				"data": []any{
					map[string]any{"id": 321, "state": apiSmokeWebhookState, "severity": 3},
				},
				"page": map[string]any{"limit": 10},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sites/910/events/321/transitions":
			writeTestJSON(t, w, map[string]any{
				"data": []any{
					map[string]any{"id": 654, "event_id": 321, "reason": "opened", "state_after": apiSmokeWebhookState, "severity_after": 3},
				},
				"page": map[string]any{"limit": 50},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/webhooks/88/deliveries":
			writeTestJSON(t, w, map[string]any{
				"data": []any{
					map[string]any{
						"id":         776,
						"status":     "delivered",
						"event_id":   321,
						"event_type": apiSmokeWebhookEvent,
						"payload":    map[string]any{"type": apiSmokeWebhookEvent, "site_id": 910},
					},
					map[string]any{
						"id":         777,
						"status":     "delivered",
						"event_id":   321,
						"event_type": apiSmokeWebhookEvent,
						"payload":    map[string]any{"type": apiSmokeWebhookEvent, "site_id": 910},
					},
				},
				"page": map[string]any{"limit": 10},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/webhooks/88":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/sites/910":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
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
		batch:               "smoke-webhook",
		blogID:              910,
		url:                 "https://example.com/",
		cleanup:             true,
		exercise:            "webhook",
		webhookURL:          fixture.URL + "/webhook",
		webhookRequestsURL:  fixture.URL + "/webhook/requests",
		webhookWait:         2 * time.Second,
		webhookPollInterval: 10 * time.Millisecond,
		fixtureURL:          fixture.URL,
		fixtureProbeURL:     fixture.URL + "/health",
	})
	if err != nil {
		t.Fatalf("runAPISmoke() error = %v\nstdout=%s", err, stdout.String())
	}

	var summary apiSmokeSummary
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal summary: %v\n%s", err, stdout.String())
	}
	if summary.Webhook == nil || summary.Webhook.ID != 88 {
		t.Fatalf("webhook summary = %#v, want webhook id 88", summary.Webhook)
	}
	if strings.Contains(summary.Webhook.URL, webhookSecret) {
		t.Fatalf("webhook summary URL leaked raw secret: %q", summary.Webhook.URL)
	}
	if summary.WebhookFixture == nil || !summary.WebhookFixture.SignatureVerified {
		t.Fatalf("fixture summary = %#v, want verified signature", summary.WebhookFixture)
	}
	if summary.FailureSimulation == nil || summary.FailureSimulation.TransitionCount != 1 {
		t.Fatalf("failure simulation = %#v, want one transition", summary.FailureSimulation)
	}
	if len(summary.CleanupResults) != 2 {
		t.Fatalf("cleanup results = %#v, want webhook and site cleanup", summary.CleanupResults)
	}

	wantCalls := []string{
		"GET /api/v1/health",
		"GET /api/v1/me",
		"POST /api/v1/sites",
		"POST /api/v1/sites/910/trigger-now",
		"GET /api/v1/sites/910/events",
		"POST /api/v1/webhooks",
		"PATCH /api/v1/webhooks/88",
		"GET /api/v1/sites/910",
		"PATCH /api/v1/sites/910",
		"POST /api/v1/sites/910/trigger-now",
		"GET /api/v1/sites/910/events",
		"GET /api/v1/sites/910/events/321/transitions",
		"GET /api/v1/webhooks/88/deliveries",
		"DELETE /api/v1/webhooks/88",
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

func TestRedactAPISecretError(t *testing.T) {
	err := redactAPISecretError(
		fmt.Errorf(`PATCH /api/v1/webhooks/88 returned 400 Bad Request: {"url":"http://api-fixture:8091/webhook?secret=whsec_TEST"}`),
		"whsec_TEST",
	)
	if err == nil {
		t.Fatal("redactAPISecretError() = nil, want error")
	}
	if strings.Contains(err.Error(), "whsec_TEST") {
		t.Fatalf("redactAPISecretError() leaked secret: %v", err)
	}
	if !strings.Contains(err.Error(), "secret=redacted") {
		t.Fatalf("redactAPISecretError() = %v, want redacted query value", err)
	}
}

func TestAPIDeliveredWebhookRowsIncludeSiteRequiresExpectedDeliveryID(t *testing.T) {
	body := json.RawMessage(`{
		"data": [
			{"id": 776, "status": "delivered", "payload": {"type": "event.opened", "site_id": 910}},
			{"id": 778, "status": "delivered", "payload": {"type": "event.opened", "site_id": 911}}
		]
	}`)
	if apiDeliveredWebhookRowsIncludeSite(body, 910, "777") {
		t.Fatal("apiDeliveredWebhookRowsIncludeSite() = true for wrong delivery id")
	}
	if !apiDeliveredWebhookRowsIncludeSite(body, 910, "776") {
		t.Fatal("apiDeliveredWebhookRowsIncludeSite() = false for expected delivery id")
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

type smokeWebhookFixture struct {
	*httptest.Server
	mu       sync.Mutex
	requests []apiSmokeFixtureWebhookHit
}

func newSmokeWebhookFixture(t *testing.T) *smokeWebhookFixture {
	t.Helper()
	fixture := &smokeWebhookFixture{}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			writeTestJSON(t, w, map[string]string{"status": "ok"})
		case r.Method == http.MethodDelete && r.URL.Path == "/webhook/requests":
			fixture.mu.Lock()
			fixture.requests = nil
			fixture.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/webhook/requests":
			fixture.mu.Lock()
			requests := append([]apiSmokeFixtureWebhookHit(nil), fixture.requests...)
			fixture.mu.Unlock()
			writeTestJSON(t, w, map[string]any{"count": len(requests), "requests": requests})
		case r.Method == http.MethodPost && r.URL.Path == "/webhook":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read webhook body: %v", err)
			}
			valid := smokeTestSignatureValid(r.Header.Get("X-Jetmon-Signature"), body, r.URL.Query().Get("secret"))
			fixture.mu.Lock()
			fixture.requests = append(fixture.requests, apiSmokeFixtureWebhookHit{
				ID:             len(fixture.requests) + 1,
				Event:          r.Header.Get("X-Jetmon-Event"),
				Delivery:       r.Header.Get("X-Jetmon-Delivery"),
				Signature:      r.Header.Get("X-Jetmon-Signature"),
				SignatureValid: &valid,
				Body:           string(body),
			})
			fixture.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected fixture request: %s %s", r.Method, r.URL.Path)
		}
	}))
	return fixture
}

func postSignedSmokeWebhook(t *testing.T, target, secret string, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build webhook request: %v", err)
	}
	req.Header.Set("X-Jetmon-Event", apiSmokeWebhookEvent)
	req.Header.Set("X-Jetmon-Delivery", "777")
	req.Header.Set("X-Jetmon-Signature", smokeTestSignature(1700000000, body, secret))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post webhook: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("post webhook status = %s", resp.Status)
	}
}

func smokeTestSignature(ts int64, body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.", ts)))
	_, _ = mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func smokeTestSignatureValid(signature string, body []byte, secret string) bool {
	return signature == smokeTestSignature(1700000000, body, secret)
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

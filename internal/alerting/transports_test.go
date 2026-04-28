package alerting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Automattic/jetmon/internal/eventstore"
)

// captureServer is a tiny httptest.Server wrapper that records the
// most recent request body and headers, returning a configurable
// response status and body.
type captureServer struct {
	srv        *httptest.Server
	gotBody    []byte
	gotHeaders http.Header
	gotMethod  string
	hits       int
	respStatus int
	respBody   string
}

func newCaptureServer() *captureServer {
	c := &captureServer{respStatus: http.StatusOK, respBody: `{"ok":true}`}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.hits++
		c.gotMethod = r.Method
		c.gotHeaders = r.Header.Clone()
		c.gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(c.respStatus)
		_, _ = w.Write([]byte(c.respBody))
	}))
	return c
}

func (c *captureServer) URL() string { return c.srv.URL }
func (c *captureServer) Close()      { c.srv.Close() }

// ─── PagerDuty ────────────────────────────────────────────────────────

func TestPagerDutyTriggerHappyPath(t *testing.T) {
	cap := newCaptureServer()
	defer cap.Close()

	d := &PagerDutyDispatcher{Endpoint: cap.URL()}
	dest := json.RawMessage(`{"integration_key":"PDKEY"}`)
	n := makeTestNotification()

	status, _, err := d.Send(context.Background(), dest, n)
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}

	var got pagerDutyEvent
	if err := json.Unmarshal(cap.gotBody, &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.RoutingKey != "PDKEY" {
		t.Errorf("RoutingKey = %q", got.RoutingKey)
	}
	if got.EventAction != "trigger" {
		t.Errorf("EventAction = %q, want trigger", got.EventAction)
	}
	if got.Payload.Severity != "critical" {
		t.Errorf("Severity = %q, want critical (Down)", got.Payload.Severity)
	}
	if !strings.Contains(got.Payload.Summary, "https://example.com") {
		t.Errorf("Summary missing site URL: %q", got.Payload.Summary)
	}
	if got.DedupKey != "jetmon-event-777" {
		t.Errorf("DedupKey = %q", got.DedupKey)
	}
}

func TestPagerDutyResolveOnRecovery(t *testing.T) {
	cap := newCaptureServer()
	defer cap.Close()

	d := &PagerDutyDispatcher{Endpoint: cap.URL()}
	n := makeTestNotification()
	n.Recovery = true
	n.Severity = eventstore.SeverityUp
	n.SeverityName = "Up"

	if _, _, err := d.Send(context.Background(), json.RawMessage(`{"integration_key":"K"}`), n); err != nil {
		t.Fatalf("Send error: %v", err)
	}

	var got pagerDutyEvent
	_ = json.Unmarshal(cap.gotBody, &got)
	if got.EventAction != "resolve" {
		t.Errorf("EventAction = %q, want resolve", got.EventAction)
	}
	if got.DedupKey != "jetmon-event-777" {
		t.Errorf("DedupKey for resolve must match trigger: %q", got.DedupKey)
	}
}

func TestPagerDutyTestUsesDistinctDedupKey(t *testing.T) {
	cap := newCaptureServer()
	defer cap.Close()

	d := &PagerDutyDispatcher{Endpoint: cap.URL()}
	n := makeTestNotification()
	n.IsTest = true

	if _, _, err := d.Send(context.Background(), json.RawMessage(`{"integration_key":"K"}`), n); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	var got pagerDutyEvent
	_ = json.Unmarshal(cap.gotBody, &got)
	if !strings.HasPrefix(got.DedupKey, "jetmon-test-") {
		t.Errorf("test send should use jetmon-test- dedup_key, got %q", got.DedupKey)
	}
}

func TestPagerDutySeverityMapping(t *testing.T) {
	cases := map[uint8]string{
		eventstore.SeverityDown:      "critical",
		eventstore.SeveritySeemsDown: "critical",
		eventstore.SeverityDegraded:  "warning",
		eventstore.SeverityWarning:   "info",
	}
	for sev, want := range cases {
		if got := pagerDutySeverity(sev); got != want {
			t.Errorf("pagerDutySeverity(%d) = %q, want %q", sev, got, want)
		}
	}
}

func TestPagerDutyRejectsBadDestination(t *testing.T) {
	d := &PagerDutyDispatcher{Endpoint: "https://nowhere.invalid"}
	cases := []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"integration_key":""}`),
		json.RawMessage(`not json`),
	}
	for i, dest := range cases {
		_, _, err := d.Send(context.Background(), dest, makeTestNotification())
		if err == nil {
			t.Errorf("case %d: expected error for %s", i, dest)
		}
	}
}

func TestPagerDutySurfacesUpstreamError(t *testing.T) {
	cap := newCaptureServer()
	cap.respStatus = http.StatusBadRequest
	cap.respBody = `{"error":"missing routing_key"}`
	defer cap.Close()

	d := &PagerDutyDispatcher{Endpoint: cap.URL()}
	status, body, err := d.Send(context.Background(), json.RawMessage(`{"integration_key":"K"}`), makeTestNotification())
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if status != 400 {
		t.Errorf("status = %d", status)
	}
	if !strings.Contains(body, "missing routing_key") {
		t.Errorf("body should include upstream error: %q", body)
	}
}

// ─── Slack ────────────────────────────────────────────────────────────

func TestSlackHappyPath(t *testing.T) {
	cap := newCaptureServer()
	defer cap.Close()

	d := &SlackDispatcher{}
	dest, _ := json.Marshal(slackDestination{WebhookURL: cap.URL()})

	status, _, err := d.Send(context.Background(), dest, makeTestNotification())
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d", status)
	}

	var got slackMessage
	if err := json.Unmarshal(cap.gotBody, &got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got.Text == "" {
		t.Error("Slack body must include fallback text")
	}
	if len(got.Blocks) < 2 {
		t.Errorf("Slack body should have at least 2 blocks, got %d", len(got.Blocks))
	}
	headerText := got.Blocks[0].Text.Text
	if !strings.Contains(headerText, "Down") {
		t.Errorf("header should include severity: %q", headerText)
	}
	if !strings.Contains(headerText, "https://example.com") {
		t.Errorf("header should include site URL: %q", headerText)
	}
}

func TestSlackRecoveryHeader(t *testing.T) {
	cap := newCaptureServer()
	defer cap.Close()

	d := &SlackDispatcher{}
	dest, _ := json.Marshal(slackDestination{WebhookURL: cap.URL()})
	n := makeTestNotification()
	n.Recovery = true

	if _, _, err := d.Send(context.Background(), dest, n); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	var got slackMessage
	_ = json.Unmarshal(cap.gotBody, &got)
	if !strings.Contains(got.Blocks[0].Text.Text, "Recovered") {
		t.Errorf("recovery header expected, got %q", got.Blocks[0].Text.Text)
	}
}

func TestSlackTestHeader(t *testing.T) {
	cap := newCaptureServer()
	defer cap.Close()

	d := &SlackDispatcher{}
	dest, _ := json.Marshal(slackDestination{WebhookURL: cap.URL()})
	n := makeTestNotification()
	n.IsTest = true

	if _, _, err := d.Send(context.Background(), dest, n); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	var got slackMessage
	_ = json.Unmarshal(cap.gotBody, &got)
	if !strings.Contains(got.Blocks[0].Text.Text, "Jetmon test") {
		t.Errorf("test header expected, got %q", got.Blocks[0].Text.Text)
	}
}

func TestSlackRejectsBadDestination(t *testing.T) {
	d := &SlackDispatcher{}
	for _, dest := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"webhook_url":""}`),
		json.RawMessage(`not json`),
	} {
		if _, _, err := d.Send(context.Background(), dest, makeTestNotification()); err == nil {
			t.Errorf("expected error for %s", dest)
		}
	}
}

// ─── Teams ────────────────────────────────────────────────────────────

func TestTeamsHappyPath(t *testing.T) {
	cap := newCaptureServer()
	defer cap.Close()

	d := &TeamsDispatcher{}
	dest, _ := json.Marshal(teamsDestination{WebhookURL: cap.URL()})

	status, _, err := d.Send(context.Background(), dest, makeTestNotification())
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d", status)
	}

	// Decode loosely; the Adaptive Card JSON has nested polymorphic
	// content that's painful to model fully — we check the key fields.
	var generic map[string]any
	if err := json.Unmarshal(cap.gotBody, &generic); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if generic["type"] != "message" {
		t.Errorf("type = %v", generic["type"])
	}
	atts, _ := generic["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments len = %d", len(atts))
	}
	att := atts[0].(map[string]any)
	if att["contentType"] != "application/vnd.microsoft.card.adaptive" {
		t.Errorf("contentType = %v", att["contentType"])
	}
	// Spot check: serialize the attachment back and verify the
	// header text contains the severity.
	raw, _ := json.Marshal(att)
	if !strings.Contains(string(raw), "Down") {
		t.Errorf("Teams card missing severity in body: %s", raw)
	}
	if !strings.Contains(string(raw), "https://example.com") {
		t.Errorf("Teams card missing site URL: %s", raw)
	}
}

func TestTeamsRecoveryHeader(t *testing.T) {
	cap := newCaptureServer()
	defer cap.Close()

	d := &TeamsDispatcher{}
	dest, _ := json.Marshal(teamsDestination{WebhookURL: cap.URL()})
	n := makeTestNotification()
	n.Recovery = true

	if _, _, err := d.Send(context.Background(), dest, n); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if !strings.Contains(string(cap.gotBody), "Recovered") {
		t.Errorf("recovery body should mention Recovered: %s", cap.gotBody)
	}
}

func TestTeamsRejectsBadDestination(t *testing.T) {
	d := &TeamsDispatcher{}
	for _, dest := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"webhook_url":""}`),
		json.RawMessage(`not json`),
	} {
		if _, _, err := d.Send(context.Background(), dest, makeTestNotification()); err == nil {
			t.Errorf("expected error for %s", dest)
		}
	}
}

// ─── Shared helpers ──────────────────────────────────────────────────

func TestTruncateResponseBody(t *testing.T) {
	short := strings.Repeat("a", 100)
	if got := truncateResponseBody(short); got != short {
		t.Error("short body should pass through unchanged")
	}
	long := strings.Repeat("b", 3000)
	got := truncateResponseBody(long)
	if len(got) != 2048 {
		t.Errorf("long body length = %d, want 2048", len(got))
	}
}

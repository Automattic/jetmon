package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/eventstore"
)

// defaultTransportTimeout bounds every outbound HTTP transport call.
// Short enough that a hung receiver doesn't wedge the worker for long;
// long enough to absorb normal third-party API latency.
const defaultTransportTimeout = 10 * time.Second

// httpClientOrDefault returns c if non-nil, otherwise a fresh client
// with defaultTransportTimeout. Tests inject their own client to
// point at httptest servers.
func httpClientOrDefault(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: defaultTransportTimeout}
}

// truncateResponseBody caps a transport response at the
// jetmon_alert_deliveries.last_response column width. Keeps the
// most recent bytes since failure messages tend to be at the start
// but trailing context (e.g. "rate-limit reset at ...") is also
// useful.
func truncateResponseBody(s string) string {
	const cap = 2048
	if len(s) <= cap {
		return s
	}
	return s[:cap]
}

// readResponseBody reads up to 4 KB so a misbehaving server can't
// fill memory on a 200 OK with a giant body.
func readResponseBody(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	return string(b)
}

// ─── PagerDuty ────────────────────────────────────────────────────────

// PagerDutyDispatcher implements Dispatcher for the PagerDuty Events
// API v2. Each notification becomes an event of action "trigger"
// (or "resolve" for recoveries) with a stable dedup_key derived from
// the Jetmon event id, so PagerDuty groups all transitions of the
// same incident under one alert.
type PagerDutyDispatcher struct {
	Endpoint   string       // override for tests; defaults to events.pagerduty.com/v2/enqueue
	HTTPClient *http.Client // override for tests
}

// pagerDutyDestination is the contact's destination JSON shape for
// the pagerduty transport.
type pagerDutyDestination struct {
	IntegrationKey string `json:"integration_key"`
}

// pagerDutyEvent is the Events API v2 request body. See
// https://developer.pagerduty.com/docs/events-api-v2-overview.
type pagerDutyEvent struct {
	RoutingKey  string             `json:"routing_key"`
	EventAction string             `json:"event_action"` // trigger | resolve
	DedupKey    string             `json:"dedup_key,omitempty"`
	Payload     pagerDutyEventBody `json:"payload"`
}

type pagerDutyEventBody struct {
	Summary       string                 `json:"summary"`
	Source        string                 `json:"source"`
	Severity      string                 `json:"severity"` // critical | error | warning | info
	CustomDetails map[string]interface{} `json:"custom_details,omitempty"`
}

// Send delivers n to the PagerDuty Events API v2.
func (d *PagerDutyDispatcher) Send(ctx context.Context, destination json.RawMessage, n Notification) (int, string, error) {
	var dest pagerDutyDestination
	if err := json.Unmarshal(destination, &dest); err != nil {
		return 0, "invalid destination JSON", fmt.Errorf("alerting/pagerduty: parse destination: %w", err)
	}
	if dest.IntegrationKey == "" {
		return 0, "destination missing integration_key", errors.New("alerting/pagerduty: destination missing integration_key")
	}

	endpoint := d.Endpoint
	if endpoint == "" {
		endpoint = "https://events.pagerduty.com/v2/enqueue"
	}

	action := "trigger"
	if n.Recovery {
		action = "resolve"
	}

	dedup := fmt.Sprintf("jetmon-event-%d", n.EventID)
	if n.IsTest {
		// Test sends use a dedicated dedup key so they don't accidentally
		// resolve a real alert when a test follows a real trigger.
		dedup = fmt.Sprintf("jetmon-test-%d-%d", n.SiteID, n.Timestamp.Unix())
	}

	body := pagerDutyEvent{
		RoutingKey:  dest.IntegrationKey,
		EventAction: action,
		DedupKey:    dedup,
		Payload: pagerDutyEventBody{
			Summary:  pagerDutySummary(n),
			Source:   n.SiteURL,
			Severity: pagerDutySeverity(n.Severity),
			CustomDetails: map[string]interface{}{
				"site_id":    n.SiteID,
				"event_id":   n.EventID,
				"event_type": n.EventType,
				"state":      n.State,
				"reason":     n.Reason,
				"is_test":    n.IsTest,
			},
		},
	}

	return postJSON(ctx, httpClientOrDefault(d.HTTPClient), endpoint, body, nil)
}

// pagerDutySummary is the short string PagerDuty shows in its UI and
// pager notifications. Subject-line equivalent.
func pagerDutySummary(n Notification) string {
	switch {
	case n.IsTest:
		return fmt.Sprintf("[Jetmon test] %s", n.SiteURL)
	case n.Recovery:
		return fmt.Sprintf("Recovered: %s", n.SiteURL)
	default:
		return fmt.Sprintf("%s: %s", n.SeverityName, n.SiteURL)
	}
}

// pagerDutySeverity maps Jetmon's severity uint8 to PagerDuty's
// severity string. Up never fires here (it routes through resolve).
func pagerDutySeverity(s uint8) string {
	switch s {
	case eventstore.SeverityDown, eventstore.SeveritySeemsDown:
		return "critical"
	case eventstore.SeverityDegraded:
		return "warning"
	case eventstore.SeverityWarning:
		return "info"
	default:
		// Up still gets a value because the events-v2 schema requires it
		// even on resolve actions; PagerDuty ignores it on resolve.
		return "info"
	}
}

// ─── Slack ────────────────────────────────────────────────────────────

// SlackDispatcher implements Dispatcher for Slack incoming-webhook URLs.
// Each notification becomes a Block Kit message with site, severity,
// state, time, and (for recoveries) a green-highlighted recovery banner.
type SlackDispatcher struct {
	HTTPClient *http.Client
}

type slackDestination struct {
	WebhookURL string `json:"webhook_url"`
}

// slackMessage is the request body for an incoming-webhook POST. We
// use blocks (the modern format) rather than text+attachments.
type slackMessage struct {
	Text   string       `json:"text"` // fallback for old clients / mobile previews
	Blocks []slackBlock `json:"blocks"`
}

type slackBlock struct {
	Type string                 `json:"type"`
	Text *slackText             `json:"text,omitempty"`
	Fields []slackText          `json:"fields,omitempty"`
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Send POSTs a Block Kit message to the destination's webhook URL.
func (d *SlackDispatcher) Send(ctx context.Context, destination json.RawMessage, n Notification) (int, string, error) {
	var dest slackDestination
	if err := json.Unmarshal(destination, &dest); err != nil {
		return 0, "invalid destination JSON", fmt.Errorf("alerting/slack: parse destination: %w", err)
	}
	if dest.WebhookURL == "" {
		return 0, "destination missing webhook_url", errors.New("alerting/slack: destination missing webhook_url")
	}

	body := slackMessage{
		Text:   slackFallbackText(n),
		Blocks: slackBlocks(n),
	}
	return postJSON(ctx, httpClientOrDefault(d.HTTPClient), dest.WebhookURL, body, nil)
}

func slackFallbackText(n Notification) string {
	switch {
	case n.IsTest:
		return fmt.Sprintf("Jetmon test notification for %s", n.SiteURL)
	case n.Recovery:
		return fmt.Sprintf("Jetmon recovery: %s", n.SiteURL)
	default:
		return fmt.Sprintf("Jetmon %s alert: %s", n.SeverityName, n.SiteURL)
	}
}

func slackBlocks(n Notification) []slackBlock {
	var headerEmoji string
	switch {
	case n.IsTest:
		headerEmoji = ":mag:"
	case n.Recovery:
		headerEmoji = ":white_check_mark:"
	case n.Severity >= eventstore.SeveritySeemsDown:
		headerEmoji = ":rotating_light:"
	default:
		headerEmoji = ":warning:"
	}
	header := fmt.Sprintf("%s *%s* — %s", headerEmoji, n.SeverityName, n.SiteURL)
	if n.Recovery {
		header = fmt.Sprintf("%s *Recovered* — %s", headerEmoji, n.SiteURL)
	}
	if n.IsTest {
		header = fmt.Sprintf("%s *Jetmon test* — %s", headerEmoji, n.SiteURL)
	}

	fields := []slackText{
		{Type: "mrkdwn", Text: fmt.Sprintf("*Site ID*\n%d", n.SiteID)},
		{Type: "mrkdwn", Text: fmt.Sprintf("*Event*\n#%d", n.EventID)},
	}
	if n.State != "" {
		fields = append(fields, slackText{Type: "mrkdwn", Text: fmt.Sprintf("*State*\n%s", n.State)})
	}
	if n.Reason != "" {
		fields = append(fields, slackText{Type: "mrkdwn", Text: fmt.Sprintf("*Reason*\n%s", n.Reason)})
	}
	fields = append(fields, slackText{Type: "mrkdwn", Text: fmt.Sprintf("*Time*\n%s", n.Timestamp.UTC().Format(time.RFC3339))})

	return []slackBlock{
		{Type: "section", Text: &slackText{Type: "mrkdwn", Text: header}},
		{Type: "section", Fields: fields},
	}
}

// ─── Microsoft Teams ──────────────────────────────────────────────────

// TeamsDispatcher implements Dispatcher for Microsoft Teams incoming-
// webhook URLs. Each notification becomes an Adaptive Card sent via
// a "message" envelope — same shape as Slack but Teams-specific JSON.
type TeamsDispatcher struct {
	HTTPClient *http.Client
}

type teamsDestination struct {
	WebhookURL string `json:"webhook_url"`
}

type teamsMessage struct {
	Type        string             `json:"type"`        // always "message"
	Attachments []teamsAttachment  `json:"attachments"`
}

type teamsAttachment struct {
	ContentType string         `json:"contentType"`
	Content     teamsCardBody  `json:"content"`
}

type teamsCardBody struct {
	Schema  string                   `json:"$schema"`
	Type    string                   `json:"type"`
	Version string                   `json:"version"`
	Body    []map[string]interface{} `json:"body"`
}

// Send POSTs an Adaptive Card to the destination's webhook URL.
func (d *TeamsDispatcher) Send(ctx context.Context, destination json.RawMessage, n Notification) (int, string, error) {
	var dest teamsDestination
	if err := json.Unmarshal(destination, &dest); err != nil {
		return 0, "invalid destination JSON", fmt.Errorf("alerting/teams: parse destination: %w", err)
	}
	if dest.WebhookURL == "" {
		return 0, "destination missing webhook_url", errors.New("alerting/teams: destination missing webhook_url")
	}

	header := fmt.Sprintf("**%s** — %s", n.SeverityName, n.SiteURL)
	switch {
	case n.IsTest:
		header = fmt.Sprintf("**Jetmon test** — %s", n.SiteURL)
	case n.Recovery:
		header = fmt.Sprintf("**Recovered** — %s", n.SiteURL)
	}

	facts := []map[string]string{
		{"title": "Site ID", "value": fmt.Sprintf("%d", n.SiteID)},
		{"title": "Event", "value": fmt.Sprintf("#%d (%s)", n.EventID, n.EventType)},
	}
	if n.State != "" {
		facts = append(facts, map[string]string{"title": "State", "value": n.State})
	}
	if n.Reason != "" {
		facts = append(facts, map[string]string{"title": "Reason", "value": n.Reason})
	}
	facts = append(facts, map[string]string{"title": "Time", "value": n.Timestamp.UTC().Format(time.RFC3339)})

	body := teamsMessage{
		Type: "message",
		Attachments: []teamsAttachment{
			{
				ContentType: "application/vnd.microsoft.card.adaptive",
				Content: teamsCardBody{
					Schema:  "http://adaptivecards.io/schemas/adaptive-card.json",
					Type:    "AdaptiveCard",
					Version: "1.4",
					Body: []map[string]interface{}{
						{
							"type":   "TextBlock",
							"text":   header,
							"wrap":   true,
							"size":   "Large",
							"weight": "Bolder",
						},
						{
							"type":  "FactSet",
							"facts": facts,
						},
					},
				},
			},
		},
	}

	return postJSON(ctx, httpClientOrDefault(d.HTTPClient), dest.WebhookURL, body, nil)
}

// ─── Shared helpers ──────────────────────────────────────────────────

// postJSON serializes body and POSTs it to url with optional extra
// headers. Returns (statusCode, truncatedResponseBody, err) shaped for
// the Dispatcher interface. err is non-nil when the HTTP call failed
// at the transport layer (DNS, TCP, TLS, timeout) OR when the response
// status indicates a permanent or retryable failure (>=400).
func postJSON(ctx context.Context, client *http.Client, url string, body any, extraHeaders map[string]string) (int, string, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, "", fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return 0, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()

	respBody := truncateResponseBody(strings.TrimSpace(readResponseBody(resp.Body)))

	if resp.StatusCode >= 400 {
		return resp.StatusCode, respBody, fmt.Errorf("status %d", resp.StatusCode)
	}
	return resp.StatusCode, respBody, nil
}

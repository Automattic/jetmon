// Package opsalerts sends low-volume operational alerts about Jetmon itself.
//
// These alerts are intentionally separate from customer site alerts. They are
// for Monitor, Veriflier, delivery, database, WPCOM, rollout, and security
// posture issues that operators need to see quickly.
package opsalerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityError    = "error"
	SeverityCritical = "critical"

	defaultTimeout = 3 * time.Second
)

// Config controls the operational alert sender.
type Config struct {
	Enabled           bool
	SlackWebhookURL   string
	MinSeverity       string
	RepeatInterval    time.Duration
	ServiceOnline     bool
	Service           string
	Host              string
	Version           string
	Commit            string
	BuildDate         string
	RunbookBaseURL    string
	HTTPClient        *http.Client
	LogSendFailures   bool
	AdditionalContext map[string]string
}

// Alert is one operational alert.
type Alert struct {
	Severity string
	Code     string
	Summary  string
	Impact   string
	Runbook  string
	Details  map[string]any
}

// Client sends operational alerts with basic local dedupe.
type Client struct {
	cfg      Config
	http     *http.Client
	mu       sync.Mutex
	lastSent map[string]time.Time
}

// New creates an operational alert client. A nil client is not needed; disabled
// configs produce a client whose Notify methods are no-ops.
func New(cfg Config) *Client {
	cfg.MinSeverity = NormalizeSeverity(cfg.MinSeverity)
	if cfg.MinSeverity == "" {
		cfg.MinSeverity = SeverityWarning
	}
	cfg.Service = strings.TrimSpace(cfg.Service)
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.Version = strings.TrimSpace(cfg.Version)
	cfg.Commit = strings.TrimSpace(cfg.Commit)
	cfg.BuildDate = strings.TrimSpace(cfg.BuildDate)
	cfg.SlackWebhookURL = strings.TrimSpace(cfg.SlackWebhookURL)
	cfg.RunbookBaseURL = strings.TrimRight(strings.TrimSpace(cfg.RunbookBaseURL), "/")
	if cfg.RepeatInterval < 0 {
		cfg.RepeatInterval = 0
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		cfg:      cfg,
		http:     httpClient,
		lastSent: map[string]time.Time{},
	}
}

// Enabled reports whether this client has an enabled transport.
func (c *Client) Enabled() bool {
	return c != nil && c.cfg.Enabled && c.cfg.SlackWebhookURL != ""
}

// NotifyAsync sends an alert in the background. It is appropriate for hot paths
// where alert delivery must not block monitoring work.
func (c *Client) NotifyAsync(alert Alert) {
	if c == nil || !c.shouldSend(alert) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		if err := c.Notify(ctx, alert); err != nil && c.cfg.LogSendFailures {
			log.Printf("ops-alerts: send failed code=%s severity=%s: %v", alert.Code, NormalizeSeverity(alert.Severity), err)
		}
	}()
}

// Notify sends one alert synchronously.
func (c *Client) Notify(ctx context.Context, alert Alert) error {
	if c == nil || !c.shouldSend(alert) {
		return nil
	}
	alert = c.normalizeAlert(alert)
	if !c.reserveDedupe(alert) {
		return nil
	}
	payload, err := json.Marshal(c.slackPayload(alert))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.SlackWebhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// ServiceOnline emits a low-severity startup notification when configured.
func (c *Client) ServiceOnline(details map[string]any) {
	if c == nil || !c.cfg.ServiceOnline {
		return
	}
	c.NotifyAsync(Alert{
		Severity: SeverityInfo,
		Code:     "service_online",
		Summary:  "Jetmon service came online",
		Impact:   "A Jetmon process is now running and reporting its startup posture.",
		Details:  details,
	})
}

func (c *Client) shouldSend(alert Alert) bool {
	if !c.Enabled() {
		return false
	}
	severity := NormalizeSeverity(alert.Severity)
	if severity == "" {
		severity = SeverityWarning
	}
	return severityRank(severity) >= severityRank(c.cfg.MinSeverity)
}

func (c *Client) normalizeAlert(alert Alert) Alert {
	alert.Severity = NormalizeSeverity(alert.Severity)
	if alert.Severity == "" {
		alert.Severity = SeverityWarning
	}
	alert.Code = metricSafe(strings.TrimSpace(alert.Code))
	if alert.Code == "" {
		alert.Code = "operational_alert"
	}
	alert.Summary = strings.TrimSpace(alert.Summary)
	if alert.Summary == "" {
		alert.Summary = alert.Code
	}
	alert.Impact = strings.TrimSpace(alert.Impact)
	alert.Runbook = strings.TrimSpace(alert.Runbook)
	if alert.Runbook == "" && c.cfg.RunbookBaseURL != "" {
		alert.Runbook = c.cfg.RunbookBaseURL + "#" + strings.ReplaceAll(alert.Code, "_", "-")
	}
	return alert
}

func (c *Client) reserveDedupe(alert Alert) bool {
	if c.cfg.RepeatInterval <= 0 {
		return true
	}
	key := alert.Severity + ":" + alert.Code
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if last, ok := c.lastSent[key]; ok && now.Sub(last) < c.cfg.RepeatInterval {
		return false
	}
	c.lastSent[key] = now
	return true
}

func (c *Client) slackPayload(alert Alert) map[string]any {
	text := fmt.Sprintf("[Jetmon v2] %s %s", strings.ToUpper(alert.Severity), alert.Code)
	if c.cfg.Host != "" {
		text += " host=" + c.cfg.Host
	}
	fields := []map[string]any{
		{"type": "mrkdwn", "text": "*Severity*\n" + alert.Severity},
		{"type": "mrkdwn", "text": "*Code*\n" + alert.Code},
	}
	if c.cfg.Service != "" {
		fields = append(fields, map[string]any{"type": "mrkdwn", "text": "*Service*\n" + c.cfg.Service})
	}
	if c.cfg.Host != "" {
		fields = append(fields, map[string]any{"type": "mrkdwn", "text": "*Host*\n" + c.cfg.Host})
	}
	if c.cfg.Version != "" {
		fields = append(fields, map[string]any{"type": "mrkdwn", "text": "*Version*\n" + c.cfg.Version})
	}
	if c.cfg.Commit != "" {
		fields = append(fields, map[string]any{"type": "mrkdwn", "text": "*Commit*\n" + c.cfg.Commit})
	}
	for _, field := range mapFields(c.cfg.AdditionalContext) {
		fields = append(fields, field)
	}
	for _, field := range detailFields(alert.Details) {
		fields = append(fields, field)
	}
	blocks := []map[string]any{
		{"type": "header", "text": map[string]any{"type": "plain_text", "text": text}},
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": alert.Summary}},
	}
	if alert.Impact != "" {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "*Impact*\n" + alert.Impact}})
	}
	blocks = append(blocks, map[string]any{"type": "section", "fields": fields})
	if alert.Runbook != "" {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "*Runbook*\n" + alert.Runbook}})
	}
	return map[string]any{
		"text":   text + " - " + alert.Summary,
		"blocks": blocks,
	}
}

func mapFields(values map[string]string) []map[string]any {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(values[key])
		if value == "" {
			continue
		}
		out = append(out, map[string]any{"type": "mrkdwn", "text": "*" + slackEscape(key) + "*\n" + slackEscape(value)})
	}
	return out
}

func detailFields(values map[string]any) []map[string]any {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{"type": "mrkdwn", "text": "*" + slackEscape(key) + "*\n" + slackEscape(fmt.Sprint(values[key]))})
	}
	return out
}

// NormalizeSeverity returns the canonical severity name.
func NormalizeSeverity(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "", "warn":
		if strings.TrimSpace(severity) == "" {
			return ""
		}
		return SeverityWarning
	case SeverityInfo:
		return SeverityInfo
	case SeverityWarning:
		return SeverityWarning
	case "err", SeverityError:
		return SeverityError
	case "crit", SeverityCritical:
		return SeverityCritical
	default:
		return ""
	}
}

// ValidSeverity reports whether severity is a known ops alert severity.
func ValidSeverity(severity string) bool {
	return NormalizeSeverity(severity) != ""
}

func severityRank(severity string) int {
	switch NormalizeSeverity(severity) {
	case SeverityInfo:
		return 1
	case SeverityWarning:
		return 2
	case SeverityError:
		return 3
	case SeverityCritical:
		return 4
	default:
		return 0
	}
}

func metricSafe(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func slackEscape(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}

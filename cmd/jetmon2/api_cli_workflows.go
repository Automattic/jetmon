package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	apiCLIBatchHeader       = "X-Jetmon-CLI-Batch"
	apiSmokeDefaultURL      = "https://example.com/"
	apiSmokeDefaultKeyword  = "Example Domain"
	apiSmokeAlertTestEmail  = "jetmon-api-cli@example.invalid"
	apiSmokeDefaultExercise = "alert-contact"
	apiSmokeWebhookEvent    = "event.opened"
	apiSmokeWebhookState    = "Seems Down"
	apiSmokeWebhookMode     = "http-500"

	defaultAPIFixtureWebhookURL         = "http://api-fixture:8091/webhook"
	defaultAPIFixtureWebhookRequestsURL = "http://localhost:18091/webhook/requests"
)

type apiSmokeOptions struct {
	batch                string
	blogID               int64
	url                  string
	cleanup              bool
	exercise             string
	idempotencyKeyPrefix string
	webhookURL           string
	webhookRequestsURL   string
	webhookWait          time.Duration
	webhookPollInterval  time.Duration
	fixtureURL           string
	fixtureProbeURL      string
}

type apiSmokeSummary struct {
	Batch             string                         `json:"batch"`
	BlogID            int64                          `json:"blog_id"`
	BaseURL           string                         `json:"base_url"`
	Cleanup           bool                           `json:"cleanup"`
	Steps             []apiSmokeStep                 `json:"steps"`
	Site              json.RawMessage                `json:"site,omitempty"`
	TriggerNow        json.RawMessage                `json:"trigger_now,omitempty"`
	Events            json.RawMessage                `json:"events,omitempty"`
	AlertContact      json.RawMessage                `json:"alert_contact,omitempty"`
	AlertTest         json.RawMessage                `json:"alert_test,omitempty"`
	Webhook           *apiSmokeWebhookSummary        `json:"webhook,omitempty"`
	WebhookDelivery   json.RawMessage                `json:"webhook_delivery,omitempty"`
	WebhookFixture    *apiSmokeWebhookFixtureSummary `json:"webhook_fixture,omitempty"`
	FailureSimulation *apiSimulatedSiteResult        `json:"failure_simulation,omitempty"`
	CleanupResults    []apiSmokeCleanupResult        `json:"cleanup_results,omitempty"`
}

type apiSmokeStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type apiSmokeCleanupResult struct {
	Resource string `json:"resource"`
	ID       int64  `json:"id"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

type apiSmokeWebhookSummary struct {
	ID            int64    `json:"id"`
	URL           string   `json:"url"`
	Active        bool     `json:"active"`
	Events        []string `json:"events,omitempty"`
	SecretPreview string   `json:"secret_preview,omitempty"`
}

type apiSmokeWebhookFixtureSummary struct {
	Requests          int    `json:"requests"`
	MatchedDeliveryID string `json:"matched_delivery_id,omitempty"`
	MatchedEvent      string `json:"matched_event,omitempty"`
	SignatureVerified bool   `json:"signature_verified"`
}

type apiSmokeFixtureResponse struct {
	Count    int                         `json:"count"`
	Requests []apiSmokeFixtureWebhookHit `json:"requests"`
}

type apiSmokeFixtureWebhookHit struct {
	ID             int    `json:"id"`
	Event          string `json:"event,omitempty"`
	Delivery       string `json:"delivery,omitempty"`
	Signature      string `json:"signature,omitempty"`
	SignatureValid *bool  `json:"signature_valid,omitempty"`
	Body           string `json:"body"`
}

type apiWorkflowHTTPError struct {
	Method string
	Target string
	Status string
	Body   []byte
}

func (e apiWorkflowHTTPError) Error() string {
	body := strings.TrimSpace(string(e.Body))
	if len(body) > 300 {
		body = body[:300] + "..."
	}
	if body == "" {
		return fmt.Sprintf("%s %s returned %s", e.Method, e.Target, e.Status)
	}
	return fmt.Sprintf("%s %s returned %s: %s", e.Method, e.Target, e.Status, body)
}

func cmdAPISmoke(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api smoke", &opts)
	smoke := apiSmokeOptions{
		url:      apiSmokeDefaultURL,
		cleanup:  true,
		exercise: apiSmokeDefaultExercise,
		webhookURL: envOrDefault(
			"JETMON_API_WEBHOOK_FIXTURE_URL",
			defaultAPIFixtureWebhookURL,
		),
		webhookRequestsURL: envOrDefault(
			"JETMON_API_WEBHOOK_FIXTURE_REQUESTS_URL",
			defaultAPIFixtureWebhookRequestsURL,
		),
		webhookWait:         60 * time.Second,
		webhookPollInterval: 2 * time.Second,
		fixtureURL:          envOrDefault("JETMON_API_FIXTURE_URL", apiFixtureAuto),
		fixtureProbeURL: envOrDefault(
			"JETMON_API_FIXTURE_PROBE_URL",
			defaultAPIFixtureProbeURL,
		),
	}
	fs.StringVar(&smoke.batch, "batch", "", "stable batch label for generated test resources")
	fs.Int64Var(&smoke.blogID, "blog-id", 0, "specific blog_id to create; default derives from --batch")
	fs.StringVar(&smoke.url, "url", smoke.url, "site monitor URL to create")
	fs.BoolVar(&smoke.cleanup, "cleanup", smoke.cleanup, "delete smoke-created resources before exit")
	fs.StringVar(&smoke.exercise, "exercise", smoke.exercise, "extra path to exercise: alert-contact, webhook, or none")
	fs.StringVar(&smoke.idempotencyKeyPrefix, "idempotency-key-prefix", "", "prefix for smoke POST Idempotency-Key headers")
	fs.StringVar(&smoke.webhookURL, "webhook-url", smoke.webhookURL, "receiver URL to register when --exercise=webhook")
	fs.StringVar(&smoke.webhookRequestsURL, "webhook-requests-url", smoke.webhookRequestsURL, "fixture requests URL to poll when --exercise=webhook")
	fs.DurationVar(&smoke.webhookWait, "webhook-wait", smoke.webhookWait, "maximum wait for webhook delivery when --exercise=webhook")
	fs.DurationVar(&smoke.webhookPollInterval, "webhook-poll-interval", smoke.webhookPollInterval, "poll interval for webhook delivery checks")
	fs.StringVar(&smoke.fixtureURL, "fixture-url", smoke.fixtureURL, "Docker fixture monitor URL, auto, or off when --exercise=webhook")
	fs.StringVar(&smoke.fixtureProbeURL, "fixture-probe-url", smoke.fixtureProbeURL, "URL used when --fixture-url=auto")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api smoke [flags]")
	}
	return runAPISmoke(context.Background(), nil, opts, smoke)
}

func runAPISmoke(ctx context.Context, client *http.Client, opts apiCLIOptions, smoke apiSmokeOptions) error {
	if opts.out == nil {
		opts.out = io.Discard
	}
	remote, err := requireAPILocalOrAllowRemote(opts, opts.allowRemote, "api smoke")
	if err != nil {
		return err
	}
	if remote && strings.TrimSpace(smoke.batch) == "" {
		return errors.New("api smoke requires --batch when --allow-remote targets a non-local API")
	}
	if smoke.batch == "" {
		smoke.batch = apiCLINewBatchID("smoke")
	}
	if smoke.blogID == 0 {
		smoke.blogID = apiCLIBatchBlogIDStart(smoke.batch)
	}
	if smoke.url == "" {
		smoke.url = apiSmokeDefaultURL
	}
	if smoke.exercise == "" {
		smoke.exercise = apiSmokeDefaultExercise
	}
	if smoke.webhookURL == "" {
		smoke.webhookURL = defaultAPIFixtureWebhookURL
	}
	if smoke.webhookRequestsURL == "" {
		smoke.webhookRequestsURL = defaultAPIFixtureWebhookRequestsURL
	}
	if smoke.webhookWait == 0 {
		smoke.webhookWait = 60 * time.Second
	}
	if smoke.webhookPollInterval == 0 {
		smoke.webhookPollInterval = 2 * time.Second
	}
	if smoke.fixtureURL == "" {
		smoke.fixtureURL = apiFixtureAuto
	}
	if smoke.fixtureProbeURL == "" {
		smoke.fixtureProbeURL = defaultAPIFixtureProbeURL
	}
	if smoke.exercise != "alert-contact" && smoke.exercise != "webhook" && smoke.exercise != "none" {
		return errors.New("exercise must be one of: alert-contact, webhook, none")
	}
	if smoke.webhookWait <= 0 {
		return errors.New("webhook-wait must be positive")
	}
	if smoke.webhookPollInterval <= 0 {
		return errors.New("webhook-poll-interval must be positive")
	}

	summary := apiSmokeSummary{
		Batch:   smoke.batch,
		BlogID:  smoke.blogID,
		BaseURL: opts.baseURL,
		Cleanup: smoke.cleanup,
	}
	var createdContactID int64
	var createdWebhookID int64
	siteCreated := false

	cleanup := func() {
		if !smoke.cleanup {
			return
		}
		if createdWebhookID > 0 {
			target := "/api/v1/webhooks/" + strconv.FormatInt(createdWebhookID, 10)
			err := apiWorkflowDelete(ctx, client, opts, target)
			result := apiSmokeCleanupResult{Resource: "webhook", ID: createdWebhookID, Status: "deleted"}
			if err != nil {
				result.Status = "failed"
				result.Error = err.Error()
			}
			summary.CleanupResults = append(summary.CleanupResults, result)
		}
		if createdContactID > 0 {
			target := "/api/v1/alert-contacts/" + strconv.FormatInt(createdContactID, 10)
			err := apiWorkflowDelete(ctx, client, opts, target)
			result := apiSmokeCleanupResult{Resource: "alert_contact", ID: createdContactID, Status: "deleted"}
			if err != nil {
				result.Status = "failed"
				result.Error = err.Error()
			}
			summary.CleanupResults = append(summary.CleanupResults, result)
		}
		if siteCreated {
			target := "/api/v1/sites/" + strconv.FormatInt(smoke.blogID, 10)
			err := apiWorkflowDelete(ctx, client, opts, target)
			result := apiSmokeCleanupResult{Resource: "site", ID: smoke.blogID, Status: "deleted"}
			if err != nil {
				result.Status = "failed"
				result.Error = err.Error()
			}
			summary.CleanupResults = append(summary.CleanupResults, result)
		}
	}

	step := func(name string, fn func() error) error {
		if err := fn(); err != nil {
			summary.Steps = append(summary.Steps, apiSmokeStep{Name: name, Status: "failed", Detail: err.Error()})
			cleanup()
			_ = writeAPIValueOutput(opts.out, summary, opts)
			return fmt.Errorf("smoke %s failed: %w", name, err)
		}
		summary.Steps = append(summary.Steps, apiSmokeStep{Name: name, Status: "ok"})
		return nil
	}

	if err := step("health", func() error {
		_, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodGet, "/api/v1/health", nil, "")
		return err
	}); err != nil {
		return err
	}
	if err := step("me", func() error {
		_, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodGet, "/api/v1/me", nil, "")
		return err
	}); err != nil {
		return err
	}
	if err := step("create_site", func() error {
		keyword := apiSmokeDefaultKeyword
		redirectPolicy := "follow"
		checkInterval := 5
		headers := map[string]string{apiCLIBatchHeader: smoke.batch}
		site, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodPost, "/api/v1/sites", apiSiteCreateRequest{
			BlogID:         smoke.blogID,
			MonitorURL:     smoke.url,
			CheckKeyword:   &keyword,
			RedirectPolicy: &redirectPolicy,
			CheckInterval:  &checkInterval,
			CustomHeaders:  &headers,
		}, apiSmokeIDKey(smoke, "create-site"))
		if err != nil {
			return err
		}
		siteCreated = true
		summary.Site = site
		return nil
	}); err != nil {
		return err
	}
	if err := step("trigger_now", func() error {
		body, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodPost, fmt.Sprintf("/api/v1/sites/%d/trigger-now", smoke.blogID), nil, apiSmokeIDKey(smoke, "trigger-now"))
		if err != nil {
			return err
		}
		summary.TriggerNow = body
		return nil
	}); err != nil {
		return err
	}
	if err := step("events", func() error {
		body, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodGet, fmt.Sprintf("/api/v1/sites/%d/events?limit=5", smoke.blogID), nil, "")
		if err != nil {
			return err
		}
		summary.Events = body
		return nil
	}); err != nil {
		return err
	}
	if smoke.exercise == "alert-contact" {
		if err := step("create_alert_contact", func() error {
			contact, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodPost, "/api/v1/alert-contacts", apiAlertContactCreateRequest{
				Label:       "api-cli-smoke-" + smoke.batch,
				Transport:   "email",
				Destination: json.RawMessage(`{"address":"` + apiSmokeAlertTestEmail + `"}`),
				SiteFilter:  apiAlertContactSiteFilter{SiteIDs: []int64{smoke.blogID}},
			}, apiSmokeIDKey(smoke, "create-alert-contact"))
			if err != nil {
				return err
			}
			id, err := apiJSONInt64(contact, "id")
			if err != nil {
				return err
			}
			createdContactID = id
			summary.AlertContact = contact
			return nil
		}); err != nil {
			return err
		}
		if err := step("alert_contact_test", func() error {
			body, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodPost, fmt.Sprintf("/api/v1/alert-contacts/%d/test", createdContactID), nil, apiSmokeIDKey(smoke, "alert-contact-test"))
			if err != nil {
				return err
			}
			summary.AlertTest = body
			return nil
		}); err != nil {
			return err
		}
	}
	if smoke.exercise == "webhook" {
		var webhookSecret string
		if err := step("webhook_clear_fixture", func() error {
			return clearAPIWebhookFixtureRequests(ctx, client, opts, smoke.webhookRequestsURL)
		}); err != nil {
			return err
		}
		if err := step("create_webhook", func() error {
			active := false
			hook, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodPost, "/api/v1/webhooks", apiWebhookCreateRequest{
				URL:    strings.TrimSpace(smoke.webhookURL),
				Active: &active,
				Events: []string{apiSmokeWebhookEvent},
				SiteFilter: apiWebhookSiteFilter{
					SiteIDs: []int64{smoke.blogID},
				},
				StateFilter: apiWebhookStateFilter{
					States: []string{apiSmokeWebhookState},
				},
			}, apiSmokeIDKey(smoke, "create-webhook"))
			if err != nil {
				return err
			}
			id, err := apiJSONInt64(hook, "id")
			if err != nil {
				return err
			}
			secret, err := apiJSONString(hook, "secret")
			if err != nil {
				return err
			}
			createdWebhookID = id
			webhookSecret = secret
			summary.Webhook = redactedAPIWebhookSummary(hook)
			return nil
		}); err != nil {
			return err
		}
		if err := step("activate_webhook_signature_fixture", func() error {
			signedURL, err := apiWebhookFixtureURLWithSecret(smoke.webhookURL, webhookSecret)
			if err != nil {
				return err
			}
			active := true
			body, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodPatch, fmt.Sprintf("/api/v1/webhooks/%d", createdWebhookID), apiWebhookUpdateRequest{
				URL:    &signedURL,
				Active: &active,
			}, "")
			if err != nil {
				return err
			}
			summary.Webhook = redactedAPIWebhookSummary(body)
			return nil
		}); err != nil {
			return err
		}
		if err := step("simulate_failure_for_webhook", func() error {
			result, err := runAPISmokeWebhookFailureSimulation(ctx, client, opts, smoke)
			if err != nil {
				return err
			}
			summary.FailureSimulation = &result
			return nil
		}); err != nil {
			return err
		}
		if err := step("webhook_fixture_delivery", func() error {
			fixture, err := waitForAPIWebhookFixtureDelivery(ctx, client, opts, smoke)
			if err != nil {
				return err
			}
			summary.WebhookFixture = fixture
			return nil
		}); err != nil {
			return err
		}
		if err := step("webhook_delivery_row", func() error {
			body, err := waitForAPIWebhookDeliveredRow(ctx, client, opts, createdWebhookID, smoke)
			if err != nil {
				return err
			}
			summary.WebhookDelivery = body
			return nil
		}); err != nil {
			return err
		}
	}

	cleanup()
	return writeAPIValueOutput(opts.out, summary, opts)
}

func apiWorkflowRequestJSON(ctx context.Context, client *http.Client, opts apiCLIOptions, method, target string, body any, idempotencyKey string) (json.RawMessage, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	requestOpts := opts
	requestOpts.idempotencyKey = idempotencyKey
	resp, err := doAPIRequest(ctx, client, requestOpts, method, target, payload)
	if err != nil {
		return nil, err
	}
	trimmed := json.RawMessage(strings.TrimSpace(string(resp.Body)))
	if len(trimmed) == 0 {
		trimmed = json.RawMessage(`null`)
	}
	if resp.StatusCode >= 400 {
		return trimmed, apiWorkflowHTTPError{Method: method, Target: target, Status: resp.Status, Body: resp.Body}
	}
	return trimmed, nil
}

func apiWorkflowDelete(ctx context.Context, client *http.Client, opts apiCLIOptions, target string) error {
	resp, err := doAPIRequest(ctx, client, opts, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return apiWorkflowHTTPError{Method: http.MethodDelete, Target: target, Status: resp.Status, Body: resp.Body}
	}
	return nil
}

func runAPISmokeWebhookFailureSimulation(ctx context.Context, client *http.Client, opts apiCLIOptions, smoke apiSmokeOptions) (apiSimulatedSiteResult, error) {
	sim := apiSitesSimulateFailureOptions{
		mode:                   apiSmokeWebhookMode,
		batch:                  smoke.batch,
		count:                  1,
		blogIDStart:            smoke.blogID,
		createMissing:          false,
		trigger:                true,
		wait:                   smoke.webhookWait,
		pollInterval:           smoke.webhookPollInterval,
		idempotencyKeyPrefix:   smoke.idempotencyKeyPrefix,
		fixtureURL:             smoke.fixtureURL,
		fixtureProbeURL:        smoke.fixtureProbeURL,
		expectEventState:       apiSmokeWebhookState,
		requireTransition:      true,
		expectTransitionReason: "opened",
	}
	sim.expectEventSeverity.set = true
	sim.expectEventSeverity.value = 3
	fixtureURL := apiSimulationFixtureURL(ctx, sim)
	if fixtureURL == "" {
		return apiSimulatedSiteResult{}, errors.New("Docker API fixture is required for --exercise=webhook; start api-fixture or pass --fixture-url")
	}
	def, err := apiFailureMode(sim.mode, fixtureURL)
	if err != nil {
		return apiSimulatedSiteResult{}, err
	}
	return runAPISiteSimulation(ctx, client, opts, sim, def, smoke.blogID, 0)
}

func clearAPIWebhookFixtureRequests(ctx context.Context, client *http.Client, opts apiCLIOptions, requestsURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimSpace(requestsURL), nil)
	if err != nil {
		return err
	}
	resp, err := apiExternalHTTPClient(client, opts).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return apiWorkflowHTTPError{Method: http.MethodDelete, Target: requestsURL, Status: resp.Status, Body: body}
	}
	return nil
}

func waitForAPIWebhookFixtureDelivery(ctx context.Context, client *http.Client, opts apiCLIOptions, smoke apiSmokeOptions) (*apiSmokeWebhookFixtureSummary, error) {
	deadline := time.Now().Add(smoke.webhookWait)
	for {
		fixture, err := getAPIWebhookFixtureRequests(ctx, client, opts, smoke.webhookRequestsURL)
		if err != nil {
			return nil, err
		}
		if summary := matchingAPIWebhookFixtureDelivery(fixture, smoke.blogID); summary != nil {
			return summary, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for verified webhook fixture delivery for site %d", smoke.blogID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(smoke.webhookPollInterval):
		}
	}
}

func getAPIWebhookFixtureRequests(ctx context.Context, client *http.Client, opts apiCLIOptions, requestsURL string) (apiSmokeFixtureResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(requestsURL), nil)
	if err != nil {
		return apiSmokeFixtureResponse{}, err
	}
	resp, err := apiExternalHTTPClient(client, opts).Do(req)
	if err != nil {
		return apiSmokeFixtureResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return apiSmokeFixtureResponse{}, err
	}
	if resp.StatusCode >= 400 {
		return apiSmokeFixtureResponse{}, apiWorkflowHTTPError{Method: http.MethodGet, Target: requestsURL, Status: resp.Status, Body: body}
	}
	var fixture apiSmokeFixtureResponse
	if err := json.Unmarshal(body, &fixture); err != nil {
		return apiSmokeFixtureResponse{}, err
	}
	return fixture, nil
}

func matchingAPIWebhookFixtureDelivery(fixture apiSmokeFixtureResponse, siteID int64) *apiSmokeWebhookFixtureSummary {
	for _, req := range fixture.Requests {
		if req.SignatureValid == nil || !*req.SignatureValid {
			continue
		}
		var body struct {
			Type   string `json:"type"`
			SiteID int64  `json:"site_id"`
		}
		if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
			continue
		}
		if body.Type != apiSmokeWebhookEvent || body.SiteID != siteID {
			continue
		}
		return &apiSmokeWebhookFixtureSummary{
			Requests:          fixture.Count,
			MatchedDeliveryID: req.Delivery,
			MatchedEvent:      req.Event,
			SignatureVerified: true,
		}
	}
	return nil
}

func waitForAPIWebhookDeliveredRow(ctx context.Context, client *http.Client, opts apiCLIOptions, webhookID int64, smoke apiSmokeOptions) (json.RawMessage, error) {
	deadline := time.Now().Add(smoke.webhookWait)
	target := fmt.Sprintf("/api/v1/webhooks/%d/deliveries?status=delivered&limit=10", webhookID)
	for {
		body, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodGet, target, nil, "")
		if err != nil {
			return nil, err
		}
		if apiDeliveredWebhookRowsIncludeSite(body, smoke.blogID) {
			return body, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for delivered webhook row for webhook %d and site %d", webhookID, smoke.blogID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(smoke.webhookPollInterval):
		}
	}
}

func apiDeliveredWebhookRowsIncludeSite(body json.RawMessage, siteID int64) bool {
	var envelope struct {
		Data []struct {
			Status  string          `json:"status"`
			Payload json.RawMessage `json:"payload"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	for _, row := range envelope.Data {
		if row.Status != "delivered" {
			continue
		}
		var payload struct {
			Type   string `json:"type"`
			SiteID int64  `json:"site_id"`
		}
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			continue
		}
		if payload.Type == apiSmokeWebhookEvent && payload.SiteID == siteID {
			return true
		}
	}
	return false
}

func apiWebhookFixtureURLWithSecret(rawURL, secret string) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("webhook secret is empty")
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", err
	}
	if !u.IsAbs() || u.Host == "" {
		return "", errors.New("webhook-url must be absolute")
	}
	q := u.Query()
	q.Set("secret", secret)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func apiExternalHTTPClient(client *http.Client, opts apiCLIOptions) *http.Client {
	if client != nil {
		return client
	}
	timeout := opts.timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

func apiJSONInt64(body json.RawMessage, field string) (int64, error) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return 0, err
	}
	raw, ok := obj[field]
	if !ok {
		return 0, fmt.Errorf("response missing %q", field)
	}
	switch v := raw.(type) {
	case float64:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("response field %q is %T, want number", field, raw)
	}
}

func apiJSONString(body json.RawMessage, field string) (string, error) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return "", err
	}
	raw, ok := obj[field]
	if !ok {
		return "", fmt.Errorf("response missing %q", field)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("response field %q is %T, want string", field, raw)
	}
	return value, nil
}

func redactedAPIWebhookSummary(body json.RawMessage) *apiSmokeWebhookSummary {
	var hook struct {
		ID            int64    `json:"id"`
		URL           string   `json:"url"`
		Active        bool     `json:"active"`
		Events        []string `json:"events"`
		SecretPreview string   `json:"secret_preview"`
	}
	if err := json.Unmarshal(body, &hook); err != nil {
		return nil
	}
	return &apiSmokeWebhookSummary{
		ID:            hook.ID,
		URL:           redactedWebhookFixtureURL(hook.URL),
		Active:        hook.Active,
		Events:        hook.Events,
		SecretPreview: hook.SecretPreview,
	}
}

func redactedWebhookFixtureURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.Query().Has("secret") {
		q := u.Query()
		q.Set("secret", "redacted")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func apiSmokeIDKey(smoke apiSmokeOptions, suffix string) string {
	if smoke.idempotencyKeyPrefix == "" {
		return ""
	}
	return smoke.idempotencyKeyPrefix + "-" + suffix
}

func apiCLINewBatchID(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, time.Now().UTC().Format("20060102T150405Z"))
}

func apiCLIBatchBlogIDStart(batch string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(batch))
	// Reserve a deterministic 1,000-id slot in the high local-test range.
	return 910000000 + int64(h.Sum32()%90000)*1000
}

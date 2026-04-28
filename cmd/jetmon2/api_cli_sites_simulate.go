package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	apiFixtureAuto              = "auto"
	apiFixtureOff               = "off"
	defaultAPIFixtureMonitorURL = "http://api-fixture:8091"
	defaultAPIFixtureProbeURL   = "http://localhost:18091/health"
)

type apiSitesSimulateFailureOptions struct {
	mode                   string
	batch                  string
	siteIDs                apiInt64SliceFlags
	count                  int
	blogIDStart            int64
	createMissing          bool
	trigger                bool
	wait                   time.Duration
	pollInterval           time.Duration
	idempotencyKeyPrefix   string
	fixtureURL             string
	fixtureProbeURL        string
	expectEventState       string
	expectEventSeverity    apiOptionalIntFlag
	requireTransition      bool
	expectTransitionReason string
}

type apiFailureModeDefinition struct {
	Mode           string
	Description    string
	MonitorURL     string
	CheckKeyword   *string
	RedirectPolicy string
	TimeoutSeconds *int
	CustomHeaders  map[string]string
}

type apiSimulateFailureSummary struct {
	Mode          string                   `json:"mode"`
	Batch         string                   `json:"batch,omitempty"`
	Wait          string                   `json:"wait"`
	Trigger       bool                     `json:"trigger"`
	CreateMissing bool                     `json:"create_missing"`
	FixtureURL    string                   `json:"fixture_url,omitempty"`
	Sites         []apiSimulatedSiteResult `json:"sites"`
}

type apiSimulatedSiteResult struct {
	SiteID      int64                    `json:"site_id"`
	Action      string                   `json:"action"`
	Site        json.RawMessage          `json:"site,omitempty"`
	TriggerNow  json.RawMessage          `json:"trigger_now,omitempty"`
	Events      json.RawMessage          `json:"events,omitempty"`
	Transitions []apiSimulatedTransition `json:"transitions,omitempty"`
	Note        string                   `json:"note,omitempty"`
	Error       string                   `json:"error,omitempty"`
}

type apiSimulatedTransition struct {
	EventID     int64           `json:"event_id"`
	Transitions json.RawMessage `json:"transitions"`
}

func cmdAPISitesSimulateFailure(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api sites simulate-failure", &opts)
	sim := apiSitesSimulateFailureOptions{
		mode:         "http-500",
		count:        1,
		trigger:      true,
		pollInterval: 2 * time.Second,
		fixtureURL:   envOrDefault("JETMON_API_FIXTURE_URL", apiFixtureAuto),
		fixtureProbeURL: envOrDefault(
			"JETMON_API_FIXTURE_PROBE_URL",
			defaultAPIFixtureProbeURL,
		),
	}
	fs.StringVar(&sim.mode, "mode", sim.mode, "failure mode: unreachable, http-500, http-403, redirect, keyword, timeout, or tls")
	fs.StringVar(&sim.batch, "batch", "", "batch label whose deterministic site ids should be mutated")
	fs.Var(&sim.siteIDs, "site-id", "explicit site id to mutate (repeatable or comma-separated)")
	fs.IntVar(&sim.count, "count", sim.count, "number of batch-derived site ids to mutate")
	fs.Int64Var(&sim.blogIDStart, "blog-id-start", 0, "first batch blog_id; default derives from --batch")
	fs.BoolVar(&sim.createMissing, "create-missing", false, "create a site if the target id does not exist")
	fs.BoolVar(&sim.trigger, "trigger", sim.trigger, "call trigger-now after mutation")
	fs.DurationVar(&sim.wait, "wait", 0, "poll duration for active events after mutation")
	fs.DurationVar(&sim.pollInterval, "poll-interval", sim.pollInterval, "active-event poll interval when --wait is set")
	fs.StringVar(&sim.idempotencyKeyPrefix, "idempotency-key-prefix", "", "prefix for per-site POST Idempotency-Key headers")
	fs.StringVar(&sim.fixtureURL, "fixture-url", sim.fixtureURL, "Docker fixture monitor URL, auto, or off")
	fs.StringVar(&sim.fixtureProbeURL, "fixture-probe-url", sim.fixtureProbeURL, "URL used when --fixture-url=auto")
	fs.StringVar(&sim.expectEventState, "expect-event-state", "", "require at least one active event with this state after polling")
	fs.Var(&sim.expectEventSeverity, "expect-event-severity", "require at least one active event with this severity after polling")
	fs.BoolVar(&sim.requireTransition, "require-transition", false, "require at least one event transition after polling")
	fs.StringVar(&sim.expectTransitionReason, "expect-transition-reason", "", "require at least one transition with this reason after polling")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api sites simulate-failure [flags]")
	}
	return runAPISitesSimulateFailure(context.Background(), nil, opts, sim)
}

func runAPISitesSimulateFailure(ctx context.Context, client *http.Client, opts apiCLIOptions, sim apiSitesSimulateFailureOptions) error {
	if opts.out == nil {
		opts.out = io.Discard
	}
	fixtureURL := apiSimulationFixtureURL(ctx, sim)
	def, err := apiFailureMode(sim.mode, fixtureURL)
	if err != nil {
		return err
	}
	siteIDs, err := apiSimulationSiteIDs(sim)
	if err != nil {
		return err
	}
	if sim.pollInterval <= 0 {
		return errors.New("poll-interval must be positive")
	}

	summary := apiSimulateFailureSummary{
		Mode:          def.Mode,
		Batch:         sim.batch,
		Wait:          sim.wait.String(),
		Trigger:       sim.trigger,
		CreateMissing: sim.createMissing,
		FixtureURL:    fixtureURL,
		Sites:         make([]apiSimulatedSiteResult, 0, len(siteIDs)),
	}
	for i, siteID := range siteIDs {
		result, err := runAPISiteSimulation(ctx, client, opts, sim, def, siteID, i)
		summary.Sites = append(summary.Sites, result)
		if err != nil {
			summary.Sites[len(summary.Sites)-1].Error = err.Error()
			_ = writeAPIValueOutput(opts.out, summary, opts)
			return fmt.Errorf("simulate failure for site %d: %w", siteID, err)
		}
	}
	return writeAPIValueOutput(opts.out, summary, opts)
}

func runAPISiteSimulation(ctx context.Context, client *http.Client, opts apiCLIOptions, sim apiSitesSimulateFailureOptions, def apiFailureModeDefinition, siteID int64, index int) (apiSimulatedSiteResult, error) {
	result := apiSimulatedSiteResult{SiteID: siteID}
	update := apiSiteUpdateRequest{
		MonitorURL:     &def.MonitorURL,
		CheckKeyword:   def.CheckKeyword,
		RedirectPolicy: &def.RedirectPolicy,
		TimeoutSeconds: def.TimeoutSeconds,
	}
	if len(def.CustomHeaders) > 0 || sim.batch != "" {
		headers := make(map[string]string, len(def.CustomHeaders)+1)
		for k, v := range def.CustomHeaders {
			headers[k] = v
		}
		if sim.batch != "" {
			headers[apiCLIBatchHeader] = sim.batch
		}
		update.CustomHeaders = &headers
	}

	site, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodPatch, fmt.Sprintf("/api/v1/sites/%d", siteID), update, "")
	if err != nil {
		var httpErr apiWorkflowHTTPError
		if errors.As(err, &httpErr) && strings.Contains(httpErr.Status, "404") && sim.createMissing {
			site, err = createMissingSimulationSite(ctx, client, opts, sim, def, siteID, index)
			if err != nil {
				return result, err
			}
			result.Action = "created"
		} else {
			return result, err
		}
	} else {
		result.Action = "updated"
	}
	result.Site = site

	if sim.trigger {
		body, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodPost, fmt.Sprintf("/api/v1/sites/%d/trigger-now", siteID), nil, apiSimulationIDKey(sim, index, "trigger-now"))
		if err != nil {
			return result, err
		}
		result.TriggerNow = body
	}

	events, transitions, err := waitForSimulationEvents(ctx, client, opts, siteID, sim)
	if err != nil {
		return result, err
	}
	result.Events = events
	result.Transitions = transitions
	if len(transitions) == 0 {
		result.Note = "no active events returned; trigger-now reports check results but regular orchestrator rounds create failure events"
	}
	if err := validateSimulationExpectations(result, sim); err != nil {
		return result, err
	}
	return result, nil
}

func createMissingSimulationSite(ctx context.Context, client *http.Client, opts apiCLIOptions, sim apiSitesSimulateFailureOptions, def apiFailureModeDefinition, siteID int64, index int) (json.RawMessage, error) {
	headers := map[string]string{}
	for k, v := range def.CustomHeaders {
		headers[k] = v
	}
	if sim.batch != "" {
		headers[apiCLIBatchHeader] = sim.batch
	}
	req := apiSiteCreateRequest{
		BlogID:         siteID,
		MonitorURL:     def.MonitorURL,
		CheckKeyword:   def.CheckKeyword,
		RedirectPolicy: &def.RedirectPolicy,
		TimeoutSeconds: def.TimeoutSeconds,
	}
	if len(headers) > 0 {
		req.CustomHeaders = &headers
	}
	return apiWorkflowRequestJSON(ctx, client, opts, http.MethodPost, "/api/v1/sites", req, apiSimulationIDKey(sim, index, "create-site"))
}

func waitForSimulationEvents(ctx context.Context, client *http.Client, opts apiCLIOptions, siteID int64, sim apiSitesSimulateFailureOptions) (json.RawMessage, []apiSimulatedTransition, error) {
	deadline := time.Now().Add(sim.wait)
	for {
		body, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodGet, fmt.Sprintf("/api/v1/sites/%d/events?active=true&limit=10", siteID), nil, "")
		if err != nil {
			return nil, nil, err
		}
		ids := eventIDsFromList(body)
		transitions, err := querySimulationTransitions(ctx, client, opts, siteID, ids)
		if err != nil {
			return nil, nil, err
		}
		if simulationHasExpectations(sim) && sim.wait > 0 {
			result := apiSimulatedSiteResult{SiteID: siteID, Events: body, Transitions: transitions}
			if validateSimulationExpectations(result, sim) == nil {
				return body, transitions, nil
			}
		} else if len(ids) > 0 || sim.wait <= 0 || time.Now().After(deadline) {
			return body, transitions, nil
		}
		if sim.wait <= 0 || time.Now().After(deadline) {
			return body, transitions, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(sim.pollInterval):
		}
	}
}

func querySimulationTransitions(ctx context.Context, client *http.Client, opts apiCLIOptions, siteID int64, eventIDs []int64) ([]apiSimulatedTransition, error) {
	out := make([]apiSimulatedTransition, 0, len(eventIDs))
	for _, eventID := range eventIDs {
		body, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodGet, fmt.Sprintf("/api/v1/sites/%d/events/%d/transitions", siteID, eventID), nil, "")
		if err != nil {
			return nil, err
		}
		out = append(out, apiSimulatedTransition{EventID: eventID, Transitions: body})
	}
	return out, nil
}

func eventIDsFromList(body json.RawMessage) []int64 {
	events, err := simulationEventsFromList(body)
	if err != nil {
		return nil
	}
	ids := make([]int64, 0, len(events))
	for _, event := range events {
		if event.ID > 0 {
			ids = append(ids, event.ID)
		}
	}
	return ids
}

type apiSimulationListedEvent struct {
	ID       int64  `json:"id"`
	State    string `json:"state"`
	Severity int    `json:"severity"`
}

type apiSimulationListedTransition struct {
	ID            int64   `json:"id"`
	EventID       int64   `json:"event_id"`
	Reason        string  `json:"reason"`
	StateAfter    *string `json:"state_after"`
	SeverityAfter *int    `json:"severity_after"`
}

func simulationEventsFromList(body json.RawMessage) ([]apiSimulationListedEvent, error) {
	var envelope struct {
		Data []apiSimulationListedEvent `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	return envelope.Data, nil
}

func simulationTransitionsFromResults(results []apiSimulatedTransition) ([]apiSimulationListedTransition, error) {
	rows := []apiSimulationListedTransition{}
	for _, result := range results {
		var envelope struct {
			Data []apiSimulationListedTransition `json:"data"`
		}
		if err := json.Unmarshal(result.Transitions, &envelope); err != nil {
			return nil, err
		}
		rows = append(rows, envelope.Data...)
	}
	return rows, nil
}

func validateSimulationExpectations(result apiSimulatedSiteResult, sim apiSitesSimulateFailureOptions) error {
	if !simulationHasExpectations(sim) {
		return nil
	}
	events, err := simulationEventsFromList(result.Events)
	if err != nil {
		return fmt.Errorf("decode active events response: %w", err)
	}
	var failures []string
	if sim.expectEventState != "" && !simulationHasEventState(events, sim.expectEventState) {
		failures = append(failures, fmt.Sprintf("expected active event state %q, got %s", sim.expectEventState, formatSimulationEvents(events)))
	}
	if sim.expectEventSeverity.set && !simulationHasEventSeverity(events, sim.expectEventSeverity.value) {
		failures = append(failures, fmt.Sprintf("expected active event severity %d, got %s", sim.expectEventSeverity.value, formatSimulationEvents(events)))
	}
	transitions, err := simulationTransitionsFromResults(result.Transitions)
	if err != nil {
		return fmt.Errorf("decode transition response: %w", err)
	}
	if sim.requireTransition && len(transitions) == 0 {
		failures = append(failures, "expected at least one transition, got none")
	}
	if sim.expectTransitionReason != "" && !simulationHasTransitionReason(transitions, sim.expectTransitionReason) {
		failures = append(failures, fmt.Sprintf("expected transition reason %q, got %s", sim.expectTransitionReason, formatSimulationTransitions(transitions)))
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func simulationHasExpectations(sim apiSitesSimulateFailureOptions) bool {
	return sim.expectEventState != "" ||
		sim.expectEventSeverity.set ||
		sim.requireTransition ||
		sim.expectTransitionReason != ""
}

func simulationHasEventState(events []apiSimulationListedEvent, state string) bool {
	for _, event := range events {
		if event.State == state {
			return true
		}
	}
	return false
}

func simulationHasEventSeverity(events []apiSimulationListedEvent, severity int) bool {
	for _, event := range events {
		if event.Severity == severity {
			return true
		}
	}
	return false
}

func simulationHasTransitionReason(transitions []apiSimulationListedTransition, reason string) bool {
	for _, transition := range transitions {
		if transition.Reason == reason {
			return true
		}
	}
	return false
}

func formatSimulationEvents(events []apiSimulationListedEvent) string {
	if len(events) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(events))
	for _, event := range events {
		parts = append(parts, fmt.Sprintf("#%d state=%q severity=%d", event.ID, event.State, event.Severity))
	}
	return strings.Join(parts, ", ")
}

func formatSimulationTransitions(transitions []apiSimulationListedTransition) string {
	if len(transitions) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(transitions))
	for _, transition := range transitions {
		parts = append(parts, fmt.Sprintf("#%d event=%d reason=%q", transition.ID, transition.EventID, transition.Reason))
	}
	return strings.Join(parts, ", ")
}

func apiSimulationSiteIDs(sim apiSitesSimulateFailureOptions) ([]int64, error) {
	if sim.siteIDs.set {
		return sim.siteIDs.valuesOrEmpty(), nil
	}
	if sim.batch == "" && sim.blogIDStart == 0 {
		return nil, errors.New("use --batch, --blog-id-start, or --site-id")
	}
	if sim.count <= 0 {
		return nil, errors.New("count must be positive")
	}
	start := sim.blogIDStart
	if start == 0 {
		start = apiCLIBatchBlogIDStart(sim.batch)
	}
	if start <= 0 {
		return nil, errors.New("blog-id-start must be positive")
	}
	ids := make([]int64, 0, sim.count)
	for i := 0; i < sim.count; i++ {
		ids = append(ids, start+int64(i))
	}
	return ids, nil
}

func apiSimulationIDKey(sim apiSitesSimulateFailureOptions, index int, suffix string) string {
	if sim.idempotencyKeyPrefix == "" {
		return ""
	}
	return fmt.Sprintf("%s-%03d-%s", sim.idempotencyKeyPrefix, index+1, suffix)
}

func apiSimulationFixtureURL(ctx context.Context, sim apiSitesSimulateFailureOptions) string {
	fixtureURL := strings.TrimSpace(sim.fixtureURL)
	switch strings.ToLower(fixtureURL) {
	case "", apiFixtureOff, "none", "false":
		return ""
	case apiFixtureAuto:
		if apiFixtureAvailable(ctx, sim.fixtureProbeURL) {
			return defaultAPIFixtureMonitorURL
		}
		return ""
	default:
		return strings.TrimRight(fixtureURL, "/")
	}
}

func apiFixtureAvailable(ctx context.Context, probeURL string) bool {
	probeURL = strings.TrimSpace(probeURL)
	if probeURL == "" {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, probeURL, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func apiFailureMode(mode, fixtureBase string) (apiFailureModeDefinition, error) {
	if strings.TrimSpace(fixtureBase) != "" {
		return apiFixtureFailureMode(mode, strings.TrimRight(fixtureBase, "/"))
	}

	policyFollow := "follow"
	policyFail := "fail"
	missingKeyword := "jetmon-api-cli-keyword-that-should-not-exist"
	timeoutShort := 2
	switch mode {
	case "unreachable":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "reserved TEST-NET-1 address expected to be unreachable",
			MonitorURL:     "http://192.0.2.1/",
			RedirectPolicy: policyFollow,
			TimeoutSeconds: &timeoutShort,
		}, nil
	case "http-500":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "HTTP 500 response",
			MonitorURL:     "https://httpbin.org/status/500",
			RedirectPolicy: policyFollow,
		}, nil
	case "http-403":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "HTTP 403 response",
			MonitorURL:     "https://httpbin.org/status/403",
			RedirectPolicy: policyFollow,
		}, nil
	case "redirect":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "redirect response with fail policy",
			MonitorURL:     "https://httpbin.org/redirect-to?url=https%3A%2F%2Fexample.com%2F",
			RedirectPolicy: policyFail,
		}, nil
	case "keyword":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "keyword mismatch against example.com",
			MonitorURL:     "https://example.com/",
			CheckKeyword:   &missingKeyword,
			RedirectPolicy: policyFollow,
		}, nil
	case "timeout":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "slow response with short timeout",
			MonitorURL:     "https://httpbin.org/delay/10",
			RedirectPolicy: policyFollow,
			TimeoutSeconds: &timeoutShort,
		}, nil
	case "tls":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "expired TLS certificate",
			MonitorURL:     "https://expired.badssl.com/",
			RedirectPolicy: policyFollow,
		}, nil
	default:
		return apiFailureModeDefinition{}, errors.New("mode must be one of: unreachable, http-500, http-403, redirect, keyword, timeout, tls")
	}
}

func apiFixtureFailureMode(mode, fixtureBase string) (apiFailureModeDefinition, error) {
	policyFollow := "follow"
	policyFail := "fail"
	missingKeyword := "jetmon-api-cli-keyword-that-should-not-exist"
	timeoutShort := 1
	switch mode {
	case "unreachable":
		return apiFailureMode(mode, "")
	case "http-500":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "Docker fixture HTTP 500 response",
			MonitorURL:     fixtureBase + "/status/500",
			RedirectPolicy: policyFollow,
		}, nil
	case "http-403":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "Docker fixture HTTP 403 response",
			MonitorURL:     fixtureBase + "/status/403",
			RedirectPolicy: policyFollow,
		}, nil
	case "redirect":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "Docker fixture redirect response with fail policy",
			MonitorURL:     fixtureBase + "/redirect",
			RedirectPolicy: policyFail,
		}, nil
	case "keyword":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "Docker fixture keyword mismatch",
			MonitorURL:     fixtureBase + "/keyword",
			CheckKeyword:   &missingKeyword,
			RedirectPolicy: policyFollow,
		}, nil
	case "timeout":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "Docker fixture slow response with short timeout",
			MonitorURL:     fixtureBase + "/slow?delay=5s",
			RedirectPolicy: policyFollow,
			TimeoutSeconds: &timeoutShort,
		}, nil
	case "tls":
		return apiFailureModeDefinition{
			Mode:           mode,
			Description:    "Docker fixture self-signed TLS certificate",
			MonitorURL:     apiFixtureTLSBase(fixtureBase) + "/tls",
			RedirectPolicy: policyFollow,
		}, nil
	default:
		return apiFailureModeDefinition{}, errors.New("mode must be one of: unreachable, http-500, http-403, redirect, keyword, timeout, tls")
	}
}

func apiFixtureTLSBase(fixtureBase string) string {
	u, err := url.Parse(fixtureBase)
	if err != nil || u.Host == "" {
		return strings.TrimRight(fixtureBase, "/")
	}
	u.Scheme = "https"
	host, port, err := net.SplitHostPort(u.Host)
	if err == nil && port == "8091" {
		u.Host = net.JoinHostPort(host, "8443")
	}
	return strings.TrimRight(u.String(), "/")
}

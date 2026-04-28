package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type apiSitesSimulateFailureOptions struct {
	mode                 string
	batch                string
	siteIDs              apiInt64SliceFlags
	count                int
	blogIDStart          int64
	createMissing        bool
	trigger              bool
	wait                 time.Duration
	pollInterval         time.Duration
	idempotencyKeyPrefix string
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
	def, err := apiFailureMode(sim.mode)
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
		Sites:         make([]apiSimulatedSiteResult, 0, len(siteIDs)),
	}
	for i, siteID := range siteIDs {
		result, err := runAPISiteSimulation(ctx, client, opts, sim, def, siteID, i)
		summary.Sites = append(summary.Sites, result)
		if err != nil {
			summary.Sites[len(summary.Sites)-1].Error = err.Error()
			_ = writeAPIJSON(opts.out, summary, opts.pretty)
			return fmt.Errorf("simulate failure for site %d: %w", siteID, err)
		}
	}
	return writeAPIJSON(opts.out, summary, opts.pretty)
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

	events, transitions, err := waitForSimulationEvents(ctx, client, opts, siteID, sim.wait, sim.pollInterval)
	if err != nil {
		return result, err
	}
	result.Events = events
	result.Transitions = transitions
	if len(transitions) == 0 {
		result.Note = "no active events returned; trigger-now reports check results but regular orchestrator rounds create failure events"
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

func waitForSimulationEvents(ctx context.Context, client *http.Client, opts apiCLIOptions, siteID int64, wait, pollInterval time.Duration) (json.RawMessage, []apiSimulatedTransition, error) {
	deadline := time.Now().Add(wait)
	for {
		body, err := apiWorkflowRequestJSON(ctx, client, opts, http.MethodGet, fmt.Sprintf("/api/v1/sites/%d/events?active=true&limit=10", siteID), nil, "")
		if err != nil {
			return nil, nil, err
		}
		ids := eventIDsFromList(body)
		if len(ids) > 0 || wait <= 0 || time.Now().After(deadline) {
			transitions, err := querySimulationTransitions(ctx, client, opts, siteID, ids)
			return body, transitions, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(pollInterval):
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
	var envelope struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	ids := make([]int64, 0, len(envelope.Data))
	for _, event := range envelope.Data {
		if event.ID > 0 {
			ids = append(ids, event.ID)
		}
	}
	return ids
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

func apiFailureMode(mode string) (apiFailureModeDefinition, error) {
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

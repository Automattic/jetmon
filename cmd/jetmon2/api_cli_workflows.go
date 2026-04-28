package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
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
)

type apiSmokeOptions struct {
	batch                string
	blogID               int64
	url                  string
	cleanup              bool
	allowRemote          bool
	exercise             string
	idempotencyKeyPrefix string
}

type apiSmokeSummary struct {
	Batch          string                  `json:"batch"`
	BlogID         int64                   `json:"blog_id"`
	BaseURL        string                  `json:"base_url"`
	Cleanup        bool                    `json:"cleanup"`
	Steps          []apiSmokeStep          `json:"steps"`
	Site           json.RawMessage         `json:"site,omitempty"`
	TriggerNow     json.RawMessage         `json:"trigger_now,omitempty"`
	Events         json.RawMessage         `json:"events,omitempty"`
	AlertContact   json.RawMessage         `json:"alert_contact,omitempty"`
	AlertTest      json.RawMessage         `json:"alert_test,omitempty"`
	CleanupResults []apiSmokeCleanupResult `json:"cleanup_results,omitempty"`
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
	}
	fs.StringVar(&smoke.batch, "batch", "", "stable batch label for generated test resources")
	fs.Int64Var(&smoke.blogID, "blog-id", 0, "specific blog_id to create; default derives from --batch")
	fs.StringVar(&smoke.url, "url", smoke.url, "site monitor URL to create")
	fs.BoolVar(&smoke.cleanup, "cleanup", smoke.cleanup, "delete smoke-created resources before exit")
	fs.BoolVar(&smoke.allowRemote, "allow-remote", false, "allow smoke-created resources on a non-local API base URL")
	fs.StringVar(&smoke.exercise, "exercise", smoke.exercise, "extra path to exercise: alert-contact or none")
	fs.StringVar(&smoke.idempotencyKeyPrefix, "idempotency-key-prefix", "", "prefix for smoke POST Idempotency-Key headers")
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
	remote, err := requireAPILocalOrAllowRemote(opts, smoke.allowRemote, "api smoke")
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
	if smoke.exercise != "alert-contact" && smoke.exercise != "none" {
		return errors.New("exercise must be one of: alert-contact, none")
	}

	summary := apiSmokeSummary{
		Batch:   smoke.batch,
		BlogID:  smoke.blogID,
		BaseURL: opts.baseURL,
		Cleanup: smoke.cleanup,
	}
	var createdContactID int64
	siteCreated := false

	cleanup := func() {
		if !smoke.cleanup {
			return
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

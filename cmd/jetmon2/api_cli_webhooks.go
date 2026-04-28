package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type apiWebhookCreateOptions struct {
	url     string
	active  apiOptionalBoolFlag
	events  apiStringSliceFlags
	siteIDs apiInt64SliceFlags
	states  apiStringSliceFlags
}

type apiWebhookUpdateOptions struct {
	url         apiOptionalStringFlag
	active      apiOptionalBoolFlag
	events      apiStringSliceFlags
	clearEvents bool
	siteIDs     apiInt64SliceFlags
	clearSites  bool
	states      apiStringSliceFlags
	clearStates bool
}

type apiWebhookDeliveriesFilters struct {
	cursor string
	limit  int
	status string
}

type apiWebhookSiteFilter struct {
	SiteIDs []int64 `json:"site_ids,omitempty"`
}

type apiWebhookStateFilter struct {
	States []string `json:"states,omitempty"`
}

type apiWebhookCreateRequest struct {
	URL         string                `json:"url"`
	Active      *bool                 `json:"active,omitempty"`
	Events      []string              `json:"events"`
	SiteFilter  apiWebhookSiteFilter  `json:"site_filter"`
	StateFilter apiWebhookStateFilter `json:"state_filter"`
}

type apiWebhookUpdateRequest struct {
	URL         *string                `json:"url,omitempty"`
	Active      *bool                  `json:"active,omitempty"`
	Events      *[]string              `json:"events,omitempty"`
	SiteFilter  *apiWebhookSiteFilter  `json:"site_filter,omitempty"`
	StateFilter *apiWebhookStateFilter `json:"state_filter,omitempty"`
}

func cmdAPIWebhooks(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jetmon2 api webhooks <list|get|create|update|delete|rotate-secret|deliveries|retry> [flags]")
	}

	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return cmdAPIWebhooksList(rest)
	case "get":
		return cmdAPIWebhooksGet(rest)
	case "create":
		return cmdAPIWebhooksCreate(rest)
	case "update":
		return cmdAPIWebhooksUpdate(rest)
	case "delete":
		return cmdAPIWebhooksDelete(rest)
	case "rotate-secret":
		return cmdAPIWebhooksRotateSecret(rest)
	case "deliveries":
		return cmdAPIWebhooksDeliveries(rest)
	case "retry":
		return cmdAPIWebhooksRetry(rest)
	default:
		return fmt.Errorf("unknown api webhooks subcommand %q (want: list, get, create, update, delete, rotate-secret, deliveries, retry)", sub)
	}
}

func cmdAPIWebhooksList(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api webhooks list", &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api webhooks list [flags]")
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, "/api/v1/webhooks", nil)
}

func cmdAPIWebhooksGet(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api webhooks get", &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api webhooks get [flags] <webhook-id>")
	}
	target, err := apiWebhookPath(fs.Arg(0), "")
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, target, nil)
}

func cmdAPIWebhooksCreate(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api webhooks create", &opts)
	addAPIIdempotencyFlag(fs, &opts)
	create := apiWebhookCreateOptions{}
	fs.StringVar(&create.url, "url", "", "webhook destination URL")
	fs.Var(&create.active, "active", "webhook enabled: true or false")
	fs.Var(&create.events, "event", "event type filter (repeatable or comma-separated)")
	fs.Var(&create.siteIDs, "site-id", "site id filter (repeatable or comma-separated)")
	fs.Var(&create.states, "state", "state filter (repeatable or comma-separated)")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api webhooks create [flags]")
	}
	body, err := marshalAPIWebhookCreateBody(create)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPost, "/api/v1/webhooks", body)
}

func cmdAPIWebhooksUpdate(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api webhooks update", &opts)
	update := apiWebhookUpdateOptions{}
	fs.Var(&update.url, "url", "webhook destination URL")
	fs.Var(&update.active, "active", "webhook enabled: true or false")
	fs.Var(&update.events, "event", "event type filter (repeatable or comma-separated)")
	fs.BoolVar(&update.clearEvents, "clear-events", false, "clear event filters")
	fs.Var(&update.siteIDs, "site-id", "site id filter (repeatable or comma-separated)")
	fs.BoolVar(&update.clearSites, "clear-sites", false, "clear site filters")
	fs.Var(&update.states, "state", "state filter (repeatable or comma-separated)")
	fs.BoolVar(&update.clearStates, "clear-states", false, "clear state filters")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api webhooks update [flags] <webhook-id>")
	}
	target, err := apiWebhookPath(fs.Arg(0), "")
	if err != nil {
		return err
	}
	body, err := marshalAPIWebhookUpdateBody(update)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPatch, target, body)
}

func cmdAPIWebhooksDelete(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api webhooks delete", &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api webhooks delete [flags] <webhook-id>")
	}
	target, err := apiWebhookPath(fs.Arg(0), "")
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodDelete, target, nil)
}

func cmdAPIWebhooksRotateSecret(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api webhooks rotate-secret", &opts)
	addAPIIdempotencyFlag(fs, &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api webhooks rotate-secret [flags] <webhook-id>")
	}
	target, err := apiWebhookPath(fs.Arg(0), "rotate-secret")
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPost, target, nil)
}

func cmdAPIWebhooksDeliveries(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api webhooks deliveries", &opts)
	filters := apiWebhookDeliveriesFilters{}
	fs.StringVar(&filters.cursor, "cursor", "", "pagination cursor")
	fs.IntVar(&filters.limit, "limit", 0, "page size (1-200)")
	fs.StringVar(&filters.status, "status", "", "delivery status: pending, delivered, failed, or abandoned")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api webhooks deliveries [flags] <webhook-id>")
	}
	target, err := apiWebhookDeliveriesPath(fs.Arg(0), filters)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, target, nil)
}

func cmdAPIWebhooksRetry(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api webhooks retry", &opts)
	addAPIIdempotencyFlag(fs, &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: jetmon2 api webhooks retry [flags] <webhook-id> <delivery-id>")
	}
	target, err := apiWebhookRetryPath(fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPost, target, nil)
}

func apiWebhookPath(rawID, suffix string) (string, error) {
	id, err := apiPositiveID(rawID, "webhook")
	if err != nil {
		return "", err
	}
	path := "/api/v1/webhooks/" + strconv.FormatInt(id, 10)
	if suffix != "" {
		path += "/" + strings.TrimPrefix(suffix, "/")
	}
	return path, nil
}

func apiWebhookDeliveriesPath(rawID string, filters apiWebhookDeliveriesFilters) (string, error) {
	path, err := apiWebhookPath(rawID, "deliveries")
	if err != nil {
		return "", err
	}
	if filters.limit < 0 {
		return "", errors.New("limit must be positive")
	}

	values := url.Values{}
	if filters.cursor != "" {
		values.Set("cursor", filters.cursor)
	}
	if filters.limit > 0 {
		values.Set("limit", strconv.Itoa(filters.limit))
	}
	if filters.status != "" {
		switch filters.status {
		case "pending", "delivered", "failed", "abandoned":
			values.Set("status", filters.status)
		default:
			return "", errors.New("status must be one of: pending, delivered, failed, abandoned")
		}
	}
	if len(values) == 0 {
		return path, nil
	}
	return path + "?" + values.Encode(), nil
}

func apiWebhookRetryPath(rawWebhookID, rawDeliveryID string) (string, error) {
	webhookID, err := apiPositiveID(rawWebhookID, "webhook")
	if err != nil {
		return "", err
	}
	deliveryID, err := apiPositiveID(rawDeliveryID, "delivery")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/api/v1/webhooks/%d/deliveries/%d/retry", webhookID, deliveryID), nil
}

func marshalAPIWebhookCreateBody(opts apiWebhookCreateOptions) ([]byte, error) {
	if strings.TrimSpace(opts.url) == "" {
		return nil, errors.New("url is required")
	}
	req := apiWebhookCreateRequest{
		URL:         opts.url,
		Active:      opts.active.ptr(),
		Events:      opts.events.valuesOrEmpty(),
		SiteFilter:  apiWebhookSiteFilter{SiteIDs: opts.siteIDs.valuesOrEmpty()},
		StateFilter: apiWebhookStateFilter{States: opts.states.valuesOrEmpty()},
	}
	return json.Marshal(req)
}

func marshalAPIWebhookUpdateBody(opts apiWebhookUpdateOptions) ([]byte, error) {
	if opts.clearEvents && opts.events.set {
		return nil, errors.New("use --event or --clear-events, not both")
	}
	if opts.clearSites && opts.siteIDs.set {
		return nil, errors.New("use --site-id or --clear-sites, not both")
	}
	if opts.clearStates && opts.states.set {
		return nil, errors.New("use --state or --clear-states, not both")
	}

	req := apiWebhookUpdateRequest{
		URL:    opts.url.ptr(),
		Active: opts.active.ptr(),
	}
	if opts.events.set || opts.clearEvents {
		events := opts.events.valuesOrEmpty()
		req.Events = &events
	}
	if opts.siteIDs.set || opts.clearSites {
		req.SiteFilter = &apiWebhookSiteFilter{SiteIDs: opts.siteIDs.valuesOrEmpty()}
	}
	if opts.states.set || opts.clearStates {
		req.StateFilter = &apiWebhookStateFilter{States: opts.states.valuesOrEmpty()}
	}
	return json.Marshal(req)
}

type apiStringSliceFlags struct {
	values []string
	set    bool
}

func (f *apiStringSliceFlags) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		f.values = append(f.values, part)
		f.set = true
	}
	return nil
}

func (f *apiStringSliceFlags) String() string {
	return strings.Join(f.values, ",")
}

func (f apiStringSliceFlags) valuesOrEmpty() []string {
	if !f.set {
		return []string{}
	}
	out := make([]string, len(f.values))
	copy(out, f.values)
	return out
}

type apiInt64SliceFlags struct {
	values []int64
	set    bool
}

func (f *apiInt64SliceFlags) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := apiPositiveID(part, "site")
		if err != nil {
			return err
		}
		f.values = append(f.values, id)
		f.set = true
	}
	return nil
}

func (f *apiInt64SliceFlags) String() string {
	parts := make([]string, len(f.values))
	for i, v := range f.values {
		parts[i] = strconv.FormatInt(v, 10)
	}
	return strings.Join(parts, ",")
}

func (f apiInt64SliceFlags) valuesOrEmpty() []int64 {
	if !f.set {
		return []int64{}
	}
	out := make([]int64, len(f.values))
	copy(out, f.values)
	return out
}

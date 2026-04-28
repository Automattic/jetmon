package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type apiAlertContactCreateOptions struct {
	label       string
	active      apiOptionalBoolFlag
	transport   string
	destination apiAlertDestinationOptions
	siteIDs     apiInt64SliceFlags
	minSeverity apiOptionalStringFlag
	maxPerHour  apiOptionalIntFlag
}

type apiAlertContactUpdateOptions struct {
	label       apiOptionalStringFlag
	active      apiOptionalBoolFlag
	destination apiAlertDestinationOptions
	siteIDs     apiInt64SliceFlags
	clearSites  bool
	minSeverity apiOptionalStringFlag
	maxPerHour  apiOptionalIntFlag
}

type apiAlertDestinationOptions struct {
	raw            string
	address        string
	integrationKey string
	webhookURL     string
}

type apiAlertDeliveriesFilters struct {
	cursor string
	limit  int
	status string
}

type apiAlertContactSiteFilter struct {
	SiteIDs []int64 `json:"site_ids,omitempty"`
}

type apiAlertContactCreateRequest struct {
	Label       string                    `json:"label"`
	Active      *bool                     `json:"active,omitempty"`
	Transport   string                    `json:"transport"`
	Destination json.RawMessage           `json:"destination"`
	SiteFilter  apiAlertContactSiteFilter `json:"site_filter"`
	MinSeverity *string                   `json:"min_severity,omitempty"`
	MaxPerHour  *int                      `json:"max_per_hour,omitempty"`
}

type apiAlertContactUpdateRequest struct {
	Label       *string                    `json:"label,omitempty"`
	Active      *bool                      `json:"active,omitempty"`
	Destination json.RawMessage            `json:"destination,omitempty"`
	SiteFilter  *apiAlertContactSiteFilter `json:"site_filter,omitempty"`
	MinSeverity *string                    `json:"min_severity,omitempty"`
	MaxPerHour  *int                       `json:"max_per_hour,omitempty"`
}

func cmdAPIAlertContacts(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jetmon2 api alert-contacts <list|get|create|update|delete|test|deliveries|retry> [flags]")
	}

	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return cmdAPIAlertContactsList(rest)
	case "get":
		return cmdAPIAlertContactsGet(rest)
	case "create":
		return cmdAPIAlertContactsCreate(rest)
	case "update":
		return cmdAPIAlertContactsUpdate(rest)
	case "delete":
		return cmdAPIAlertContactsDelete(rest)
	case "test":
		return cmdAPIAlertContactsTest(rest)
	case "deliveries":
		return cmdAPIAlertContactsDeliveries(rest)
	case "retry":
		return cmdAPIAlertContactsRetry(rest)
	default:
		return fmt.Errorf("unknown api alert-contacts subcommand %q (want: list, get, create, update, delete, test, deliveries, retry)", sub)
	}
}

func cmdAPIAlertContactsList(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api alert-contacts list", &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api alert-contacts list [flags]")
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, "/api/v1/alert-contacts", nil)
}

func cmdAPIAlertContactsGet(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api alert-contacts get", &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api alert-contacts get [flags] <contact-id>")
	}
	target, err := apiAlertContactPath(fs.Arg(0), "")
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, target, nil)
}

func cmdAPIAlertContactsCreate(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api alert-contacts create", &opts)
	addAPIIdempotencyFlag(fs, &opts)
	create := apiAlertContactCreateOptions{}
	fs.StringVar(&create.label, "label", "", "alert contact label")
	fs.Var(&create.active, "active", "alert contact enabled: true or false")
	fs.StringVar(&create.transport, "transport", "", "transport: email, pagerduty, slack, or teams")
	addAPIAlertDestinationFlags(fs, &create.destination)
	fs.Var(&create.siteIDs, "site-id", "site id filter (repeatable or comma-separated)")
	fs.Var(&create.minSeverity, "min-severity", "minimum severity: Up, Warning, Degraded, SeemsDown, or Down")
	fs.Var(&create.maxPerHour, "max-per-hour", "maximum notifications per hour, 0 for unlimited")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api alert-contacts create [flags]")
	}
	body, err := marshalAPIAlertContactCreateBody(create)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPost, "/api/v1/alert-contacts", body)
}

func cmdAPIAlertContactsUpdate(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api alert-contacts update", &opts)
	update := apiAlertContactUpdateOptions{}
	fs.Var(&update.label, "label", "alert contact label")
	fs.Var(&update.active, "active", "alert contact enabled: true or false")
	addAPIAlertDestinationFlags(fs, &update.destination)
	fs.Var(&update.siteIDs, "site-id", "site id filter (repeatable or comma-separated)")
	fs.BoolVar(&update.clearSites, "clear-sites", false, "clear site filters")
	fs.Var(&update.minSeverity, "min-severity", "minimum severity: Up, Warning, Degraded, SeemsDown, or Down")
	fs.Var(&update.maxPerHour, "max-per-hour", "maximum notifications per hour, 0 for unlimited")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api alert-contacts update [flags] <contact-id>")
	}
	target, err := apiAlertContactPath(fs.Arg(0), "")
	if err != nil {
		return err
	}
	body, err := marshalAPIAlertContactUpdateBody(update)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPatch, target, body)
}

func cmdAPIAlertContactsDelete(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api alert-contacts delete", &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api alert-contacts delete [flags] <contact-id>")
	}
	target, err := apiAlertContactPath(fs.Arg(0), "")
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodDelete, target, nil)
}

func cmdAPIAlertContactsTest(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api alert-contacts test", &opts)
	addAPIIdempotencyFlag(fs, &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api alert-contacts test [flags] <contact-id>")
	}
	target, err := apiAlertContactPath(fs.Arg(0), "test")
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPost, target, nil)
}

func cmdAPIAlertContactsDeliveries(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api alert-contacts deliveries", &opts)
	filters := apiAlertDeliveriesFilters{}
	fs.StringVar(&filters.cursor, "cursor", "", "pagination cursor")
	fs.IntVar(&filters.limit, "limit", 0, "page size (1-200)")
	fs.StringVar(&filters.status, "status", "", "delivery status: pending, delivered, failed, or abandoned")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api alert-contacts deliveries [flags] <contact-id>")
	}
	target, err := apiAlertContactDeliveriesPath(fs.Arg(0), filters)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, target, nil)
}

func cmdAPIAlertContactsRetry(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api alert-contacts retry", &opts)
	addAPIIdempotencyFlag(fs, &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: jetmon2 api alert-contacts retry [flags] <contact-id> <delivery-id>")
	}
	target, err := apiAlertContactRetryPath(fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPost, target, nil)
}

func addAPIAlertDestinationFlags(fs *flag.FlagSet, dest *apiAlertDestinationOptions) {
	fs.StringVar(&dest.raw, "destination", "", "raw destination JSON")
	fs.StringVar(&dest.address, "address", "", "email destination address")
	fs.StringVar(&dest.integrationKey, "integration-key", "", "PagerDuty Events API v2 integration key")
	fs.StringVar(&dest.webhookURL, "webhook-url", "", "Slack or Teams incoming webhook URL")
}

func apiAlertContactPath(rawID, suffix string) (string, error) {
	id, err := apiPositiveID(rawID, "alert contact")
	if err != nil {
		return "", err
	}
	path := "/api/v1/alert-contacts/" + strconv.FormatInt(id, 10)
	if suffix != "" {
		path += "/" + strings.TrimPrefix(suffix, "/")
	}
	return path, nil
}

func apiAlertContactDeliveriesPath(rawID string, filters apiAlertDeliveriesFilters) (string, error) {
	path, err := apiAlertContactPath(rawID, "deliveries")
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

func apiAlertContactRetryPath(rawContactID, rawDeliveryID string) (string, error) {
	contactID, err := apiPositiveID(rawContactID, "alert contact")
	if err != nil {
		return "", err
	}
	deliveryID, err := apiPositiveID(rawDeliveryID, "delivery")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/api/v1/alert-contacts/%d/deliveries/%d/retry", contactID, deliveryID), nil
}

func marshalAPIAlertContactCreateBody(opts apiAlertContactCreateOptions) ([]byte, error) {
	if strings.TrimSpace(opts.label) == "" {
		return nil, errors.New("label is required")
	}
	if strings.TrimSpace(opts.transport) == "" {
		return nil, errors.New("transport is required")
	}
	destination, err := opts.destination.rawForTransport(opts.transport, true)
	if err != nil {
		return nil, err
	}
	req := apiAlertContactCreateRequest{
		Label:       opts.label,
		Active:      opts.active.ptr(),
		Transport:   opts.transport,
		Destination: destination,
		SiteFilter:  apiAlertContactSiteFilter{SiteIDs: opts.siteIDs.valuesOrEmpty()},
		MinSeverity: opts.minSeverity.ptr(),
		MaxPerHour:  opts.maxPerHour.ptr(),
	}
	return json.Marshal(req)
}

func marshalAPIAlertContactUpdateBody(opts apiAlertContactUpdateOptions) ([]byte, error) {
	if opts.clearSites && opts.siteIDs.set {
		return nil, errors.New("use --site-id or --clear-sites, not both")
	}
	destination, err := opts.destination.rawForTransport("", false)
	if err != nil {
		return nil, err
	}
	req := apiAlertContactUpdateRequest{
		Label:       opts.label.ptr(),
		Active:      opts.active.ptr(),
		Destination: destination,
		MinSeverity: opts.minSeverity.ptr(),
		MaxPerHour:  opts.maxPerHour.ptr(),
	}
	if opts.siteIDs.set || opts.clearSites {
		req.SiteFilter = &apiAlertContactSiteFilter{SiteIDs: opts.siteIDs.valuesOrEmpty()}
	}
	return json.Marshal(req)
}

func (opts apiAlertDestinationOptions) rawForTransport(transport string, required bool) (json.RawMessage, error) {
	set := 0
	for _, v := range []string{opts.raw, opts.address, opts.integrationKey, opts.webhookURL} {
		if strings.TrimSpace(v) != "" {
			set++
		}
	}
	if set == 0 {
		if required {
			return nil, errors.New("destination is required")
		}
		return nil, nil
	}
	if set > 1 {
		return nil, errors.New("use only one destination flag")
	}
	if opts.raw != "" {
		if !json.Valid([]byte(opts.raw)) {
			return nil, errors.New("destination must be valid JSON")
		}
		return json.RawMessage(opts.raw), nil
	}

	var value any
	switch {
	case opts.address != "":
		if transport != "" && transport != "email" {
			return nil, errors.New("--address requires --transport email")
		}
		value = map[string]string{"address": opts.address}
	case opts.integrationKey != "":
		if transport != "" && transport != "pagerduty" {
			return nil, errors.New("--integration-key requires --transport pagerduty")
		}
		value = map[string]string{"integration_key": opts.integrationKey}
	case opts.webhookURL != "":
		if transport != "" && transport != "slack" && transport != "teams" {
			return nil, errors.New("--webhook-url requires --transport slack or teams")
		}
		value = map[string]string{"webhook_url": opts.webhookURL}
	default:
		return nil, errors.New("destination is required")
	}

	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

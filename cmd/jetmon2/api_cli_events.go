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

type apiEventsListFilters struct {
	cursor       string
	limit        int
	state        string
	stateIn      string
	checkType    string
	checkTypeIn  string
	startedAtGTE string
	startedAtLT  string
	active       string
}

type apiTransitionsListFilters struct {
	cursor string
	limit  int
}

type apiEventCloseOptions struct {
	reason string
	note   string
}

type apiEventCloseRequest struct {
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
}

func cmdAPIEvents(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jetmon2 api events <list|get|transitions|close> [flags]")
	}

	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return cmdAPIEventsList(rest)
	case "get":
		return cmdAPIEventsGet(rest)
	case "transitions":
		return cmdAPIEventsTransitions(rest)
	case "close":
		return cmdAPIEventsClose(rest)
	default:
		return fmt.Errorf("unknown api events subcommand %q (want: list, get, transitions, close)", sub)
	}
}

func cmdAPIEventsList(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api events list", &opts)
	filters := apiEventsListFilters{}
	fs.StringVar(&filters.cursor, "cursor", "", "pagination cursor")
	fs.IntVar(&filters.limit, "limit", 0, "page size (1-200)")
	fs.StringVar(&filters.state, "state", "", "filter by event state")
	fs.StringVar(&filters.stateIn, "state-in", "", "comma-separated event states")
	fs.StringVar(&filters.checkType, "check-type", "", "filter by check type")
	fs.StringVar(&filters.checkTypeIn, "check-type-in", "", "comma-separated check types")
	fs.StringVar(&filters.startedAtGTE, "started-at-gte", "", "filter events started at or after this RFC3339 timestamp")
	fs.StringVar(&filters.startedAtLT, "started-at-lt", "", "filter events started before this RFC3339 timestamp")
	fs.StringVar(&filters.active, "active", "", "filter open events: true or false")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api events list [flags] <site-id>")
	}
	target, err := apiEventsListPath(fs.Arg(0), filters)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, target, nil)
}

func cmdAPIEventsGet(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api events get", &opts)
	var siteID string
	fs.StringVar(&siteID, "site-id", "", "optional site id for site-scoped event lookup")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api events get [flags] <event-id>")
	}
	target, err := apiEventDetailPath(siteID, fs.Arg(0))
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, target, nil)
}

func cmdAPIEventsTransitions(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api events transitions", &opts)
	filters := apiTransitionsListFilters{}
	fs.StringVar(&filters.cursor, "cursor", "", "pagination cursor")
	fs.IntVar(&filters.limit, "limit", 0, "page size (1-200)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: jetmon2 api events transitions [flags] <site-id> <event-id>")
	}
	target, err := apiEventTransitionsPath(fs.Arg(0), fs.Arg(1), filters)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, target, nil)
}

func cmdAPIEventsClose(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api events close", &opts)
	addAPIIdempotencyFlag(fs, &opts)
	closeOpts := apiEventCloseOptions{}
	fs.StringVar(&closeOpts.reason, "reason", "", "resolution reason (default: manual_override)")
	fs.StringVar(&closeOpts.note, "note", "", "operator note recorded in transition metadata")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("usage: jetmon2 api events close [flags] <site-id> <event-id>")
	}
	target, err := apiEventClosePath(fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	body, err := marshalAPIEventCloseBody(closeOpts)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPost, target, body)
}

func apiEventsListPath(rawSiteID string, filters apiEventsListFilters) (string, error) {
	siteID, err := apiPositiveID(rawSiteID, "site")
	if err != nil {
		return "", err
	}
	if filters.limit < 0 {
		return "", errors.New("limit must be positive")
	}
	if filters.state != "" && filters.stateIn != "" {
		return "", errors.New("use --state or --state-in, not both")
	}
	if filters.checkType != "" && filters.checkTypeIn != "" {
		return "", errors.New("use --check-type or --check-type-in, not both")
	}

	values := url.Values{}
	if filters.cursor != "" {
		values.Set("cursor", filters.cursor)
	}
	if filters.limit > 0 {
		values.Set("limit", strconv.Itoa(filters.limit))
	}
	if filters.state != "" {
		values.Set("state", filters.state)
	}
	if filters.stateIn != "" {
		values.Set("state__in", filters.stateIn)
	}
	if filters.checkType != "" {
		values.Set("check_type", filters.checkType)
	}
	if filters.checkTypeIn != "" {
		values.Set("check_type__in", filters.checkTypeIn)
	}
	if filters.startedAtGTE != "" {
		values.Set("started_at__gte", filters.startedAtGTE)
	}
	if filters.startedAtLT != "" {
		values.Set("started_at__lt", filters.startedAtLT)
	}
	if strings.TrimSpace(filters.active) != "" {
		active, err := strconv.ParseBool(filters.active)
		if err != nil {
			return "", errors.New("active must be true or false")
		}
		values.Set("active", strconv.FormatBool(active))
	}

	path := "/api/v1/sites/" + strconv.FormatInt(siteID, 10) + "/events"
	if len(values) == 0 {
		return path, nil
	}
	return path + "?" + values.Encode(), nil
}

func apiEventDetailPath(rawSiteID, rawEventID string) (string, error) {
	eventID, err := apiPositiveID(rawEventID, "event")
	if err != nil {
		return "", err
	}
	if rawSiteID == "" {
		return "/api/v1/events/" + strconv.FormatInt(eventID, 10), nil
	}
	siteID, err := apiPositiveID(rawSiteID, "site")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/api/v1/sites/%d/events/%d", siteID, eventID), nil
}

func apiEventTransitionsPath(rawSiteID, rawEventID string, filters apiTransitionsListFilters) (string, error) {
	path, err := apiEventDetailPath(rawSiteID, rawEventID)
	if err != nil {
		return "", err
	}
	if rawSiteID == "" {
		return "", errors.New("site id is required for transitions")
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
	path += "/transitions"
	if len(values) == 0 {
		return path, nil
	}
	return path + "?" + values.Encode(), nil
}

func apiEventClosePath(rawSiteID, rawEventID string) (string, error) {
	path, err := apiEventDetailPath(rawSiteID, rawEventID)
	if err != nil {
		return "", err
	}
	if rawSiteID == "" {
		return "", errors.New("site id is required for close")
	}
	return path + "/close", nil
}

func marshalAPIEventCloseBody(opts apiEventCloseOptions) ([]byte, error) {
	req := apiEventCloseRequest{
		Reason: opts.reason,
		Note:   opts.note,
	}
	return json.Marshal(req)
}

func apiPositiveID(raw, label string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s id must be a positive integer", label)
	}
	return id, nil
}

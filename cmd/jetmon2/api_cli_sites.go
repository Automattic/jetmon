package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type apiSitesListFilters struct {
	cursor        string
	limit         int
	state         string
	stateIn       string
	severityGTE   int
	monitorActive string
	q             string
}

type apiSiteCreateOptions struct {
	blogID               int64
	monitorURL           string
	monitorActive        apiOptionalBoolFlag
	bucketNo             apiOptionalIntFlag
	checkKeyword         apiOptionalStringFlag
	redirectPolicy       apiOptionalStringFlag
	timeoutSeconds       apiOptionalIntFlag
	customHeaders        apiStringMapFlags
	alertCooldownMinutes apiOptionalIntFlag
	checkInterval        apiOptionalIntFlag
}

type apiSiteUpdateOptions struct {
	monitorURL           apiOptionalStringFlag
	monitorActive        apiOptionalBoolFlag
	bucketNo             apiOptionalIntFlag
	checkKeyword         apiOptionalStringFlag
	redirectPolicy       apiOptionalStringFlag
	timeoutSeconds       apiOptionalIntFlag
	customHeaders        apiStringMapFlags
	clearCustomHeaders   bool
	alertCooldownMinutes apiOptionalIntFlag
	checkInterval        apiOptionalIntFlag
	maintenanceStart     apiOptionalStringFlag
	maintenanceEnd       apiOptionalStringFlag
}

type apiSiteCreateRequest struct {
	BlogID               int64              `json:"blog_id"`
	MonitorURL           string             `json:"monitor_url"`
	MonitorActive        *bool              `json:"monitor_active,omitempty"`
	BucketNo             *int               `json:"bucket_no,omitempty"`
	CheckKeyword         *string            `json:"check_keyword,omitempty"`
	RedirectPolicy       *string            `json:"redirect_policy,omitempty"`
	TimeoutSeconds       *int               `json:"timeout_seconds,omitempty"`
	CustomHeaders        *map[string]string `json:"custom_headers,omitempty"`
	AlertCooldownMinutes *int               `json:"alert_cooldown_minutes,omitempty"`
	CheckInterval        *int               `json:"check_interval,omitempty"`
}

type apiSiteUpdateRequest struct {
	MonitorURL           *string            `json:"monitor_url,omitempty"`
	MonitorActive        *bool              `json:"monitor_active,omitempty"`
	BucketNo             *int               `json:"bucket_no,omitempty"`
	CheckKeyword         *string            `json:"check_keyword,omitempty"`
	RedirectPolicy       *string            `json:"redirect_policy,omitempty"`
	TimeoutSeconds       *int               `json:"timeout_seconds,omitempty"`
	CustomHeaders        *map[string]string `json:"custom_headers,omitempty"`
	AlertCooldownMinutes *int               `json:"alert_cooldown_minutes,omitempty"`
	CheckInterval        *int               `json:"check_interval,omitempty"`
	MaintenanceStart     *string            `json:"maintenance_start,omitempty"`
	MaintenanceEnd       *string            `json:"maintenance_end,omitempty"`
}

func cmdAPISites(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jetmon2 api sites <list|get|create|update|delete|pause|resume|trigger-now|bulk-add> [flags]")
	}

	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return cmdAPISitesList(rest)
	case "get":
		return cmdAPISitesGet(rest)
	case "create":
		return cmdAPISitesCreate(rest)
	case "update":
		return cmdAPISitesUpdate(rest)
	case "delete":
		return cmdAPISitesDelete(rest)
	case "pause":
		return cmdAPISitesPostAction(rest, "pause", "pause")
	case "resume":
		return cmdAPISitesPostAction(rest, "resume", "resume")
	case "trigger-now":
		return cmdAPISitesPostAction(rest, "trigger-now", "trigger-now")
	case "bulk-add":
		return cmdAPISitesBulkAdd(rest)
	default:
		return fmt.Errorf("unknown api sites subcommand %q (want: list, get, create, update, delete, pause, resume, trigger-now, bulk-add)", sub)
	}
}

func printAPISitesUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: jetmon2 api sites <list|get|create|update|delete|pause|resume|trigger-now|bulk-add> [flags]")
}

func cmdAPISitesList(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api sites list", &opts)
	filters := apiSitesListFilters{severityGTE: -1}
	fs.StringVar(&filters.cursor, "cursor", "", "pagination cursor")
	fs.IntVar(&filters.limit, "limit", 0, "page size (1-200)")
	fs.StringVar(&filters.state, "state", "", "filter by current state")
	fs.StringVar(&filters.stateIn, "state-in", "", "comma-separated current states")
	fs.IntVar(&filters.severityGTE, "severity-gte", -1, "minimum current severity")
	fs.StringVar(&filters.monitorActive, "monitor-active", "", "filter active sites: true or false")
	fs.StringVar(&filters.q, "q", "", "monitor URL substring search")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api sites list [flags]")
	}
	target, err := apiSitesListPath(filters)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, target, nil)
}

func cmdAPISitesGet(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api sites get", &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api sites get [flags] <site-id>")
	}
	target, err := apiSiteResourcePath(fs.Arg(0), "")
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, target, nil)
}

func cmdAPISitesCreate(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api sites create", &opts)
	addAPIIdempotencyFlag(fs, &opts)
	create := apiSiteCreateOptions{}
	fs.Int64Var(&create.blogID, "blog-id", 0, "site blog_id")
	fs.StringVar(&create.monitorURL, "url", "", "site monitor URL")
	fs.Var(&create.monitorActive, "monitor-active", "monitoring enabled: true or false")
	fs.Var(&create.bucketNo, "bucket-no", "bucket number")
	fs.Var(&create.checkKeyword, "check-keyword", "keyword required in response body")
	fs.Var(&create.redirectPolicy, "redirect-policy", "redirect policy: follow, alert, or fail")
	fs.Var(&create.timeoutSeconds, "timeout-seconds", "per-site timeout in seconds")
	fs.Var(&create.customHeaders, "custom-header", "site custom header in Name: Value form (repeatable)")
	fs.Var(&create.alertCooldownMinutes, "alert-cooldown-minutes", "per-site alert cooldown in minutes")
	fs.Var(&create.checkInterval, "check-interval", "check interval in minutes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api sites create [flags]")
	}
	body, err := marshalAPISiteCreateBody(create)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPost, "/api/v1/sites", body)
}

func cmdAPISitesUpdate(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api sites update", &opts)
	update := apiSiteUpdateOptions{}
	fs.Var(&update.monitorURL, "url", "site monitor URL")
	fs.Var(&update.monitorActive, "monitor-active", "monitoring enabled: true or false")
	fs.Var(&update.bucketNo, "bucket-no", "bucket number")
	fs.Var(&update.checkKeyword, "check-keyword", "keyword required in response body; empty clears it")
	fs.Var(&update.redirectPolicy, "redirect-policy", "redirect policy: follow, alert, or fail")
	fs.Var(&update.timeoutSeconds, "timeout-seconds", "per-site timeout in seconds")
	fs.Var(&update.customHeaders, "custom-header", "site custom header in Name: Value form (repeatable)")
	fs.BoolVar(&update.clearCustomHeaders, "clear-custom-headers", false, "clear all site custom headers")
	fs.Var(&update.alertCooldownMinutes, "alert-cooldown-minutes", "per-site alert cooldown in minutes")
	fs.Var(&update.checkInterval, "check-interval", "check interval in minutes")
	fs.Var(&update.maintenanceStart, "maintenance-start", "maintenance start RFC3339 timestamp; empty clears it")
	fs.Var(&update.maintenanceEnd, "maintenance-end", "maintenance end RFC3339 timestamp; empty clears it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api sites update [flags] <site-id>")
	}
	target, err := apiSiteResourcePath(fs.Arg(0), "")
	if err != nil {
		return err
	}
	body, err := marshalAPISiteUpdateBody(update)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPatch, target, body)
}

func cmdAPISitesDelete(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api sites delete", &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: jetmon2 api sites delete [flags] <site-id>")
	}
	target, err := apiSiteResourcePath(fs.Arg(0), "")
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodDelete, target, nil)
}

func cmdAPISitesPostAction(args []string, usageName, suffix string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api sites "+usageName, &opts)
	addAPIIdempotencyFlag(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: jetmon2 api sites %s [flags] <site-id>", usageName)
	}
	target, err := apiSiteResourcePath(fs.Arg(0), suffix)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPost, target, nil)
}

func addAPIIdempotencyFlag(fs *flag.FlagSet, opts *apiCLIOptions) {
	fs.StringVar(&opts.idempotencyKey, "idempotency-key", "", "Idempotency-Key header for POST retries")
}

func apiSitesListPath(filters apiSitesListFilters) (string, error) {
	if filters.limit < 0 {
		return "", errors.New("limit must be positive")
	}
	if filters.severityGTE < -1 {
		return "", errors.New("severity-gte must be zero or greater")
	}
	if filters.state != "" && filters.stateIn != "" {
		return "", errors.New("use --state or --state-in, not both")
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
	if filters.severityGTE >= 0 {
		values.Set("severity__gte", strconv.Itoa(filters.severityGTE))
	}
	if strings.TrimSpace(filters.monitorActive) != "" {
		active, err := strconv.ParseBool(filters.monitorActive)
		if err != nil {
			return "", errors.New("monitor-active must be true or false")
		}
		values.Set("monitor_active", strconv.FormatBool(active))
	}
	if filters.q != "" {
		values.Set("q", filters.q)
	}

	if len(values) == 0 {
		return "/api/v1/sites", nil
	}
	return "/api/v1/sites?" + values.Encode(), nil
}

func apiSiteResourcePath(rawID, suffix string) (string, error) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		return "", errors.New("site id must be a positive integer")
	}
	path := "/api/v1/sites/" + strconv.FormatInt(id, 10)
	if suffix != "" {
		path += "/" + strings.TrimPrefix(suffix, "/")
	}
	return path, nil
}

func marshalAPISiteCreateBody(opts apiSiteCreateOptions) ([]byte, error) {
	if opts.blogID <= 0 {
		return nil, errors.New("blog-id is required and must be a positive integer")
	}
	if strings.TrimSpace(opts.monitorURL) == "" {
		return nil, errors.New("url is required")
	}

	req := apiSiteCreateRequest{
		BlogID:               opts.blogID,
		MonitorURL:           opts.monitorURL,
		MonitorActive:        opts.monitorActive.ptr(),
		BucketNo:             opts.bucketNo.ptr(),
		CheckKeyword:         opts.checkKeyword.ptr(),
		RedirectPolicy:       opts.redirectPolicy.ptr(),
		TimeoutSeconds:       opts.timeoutSeconds.ptr(),
		CustomHeaders:        opts.customHeaders.ptr(),
		AlertCooldownMinutes: opts.alertCooldownMinutes.ptr(),
		CheckInterval:        opts.checkInterval.ptr(),
	}
	return json.Marshal(req)
}

func marshalAPISiteUpdateBody(opts apiSiteUpdateOptions) ([]byte, error) {
	if opts.clearCustomHeaders && opts.customHeaders.set {
		return nil, errors.New("use --custom-header or --clear-custom-headers, not both")
	}

	req := apiSiteUpdateRequest{
		MonitorURL:           opts.monitorURL.ptr(),
		MonitorActive:        opts.monitorActive.ptr(),
		BucketNo:             opts.bucketNo.ptr(),
		CheckKeyword:         opts.checkKeyword.ptr(),
		RedirectPolicy:       opts.redirectPolicy.ptr(),
		TimeoutSeconds:       opts.timeoutSeconds.ptr(),
		CustomHeaders:        opts.customHeaders.ptr(),
		AlertCooldownMinutes: opts.alertCooldownMinutes.ptr(),
		CheckInterval:        opts.checkInterval.ptr(),
		MaintenanceStart:     opts.maintenanceStart.ptr(),
		MaintenanceEnd:       opts.maintenanceEnd.ptr(),
	}
	if opts.clearCustomHeaders {
		empty := map[string]string{}
		req.CustomHeaders = &empty
	}
	return json.Marshal(req)
}

type apiOptionalBoolFlag struct {
	value bool
	set   bool
}

func (f *apiOptionalBoolFlag) Set(v string) error {
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return err
	}
	f.value = parsed
	f.set = true
	return nil
}

func (f *apiOptionalBoolFlag) String() string {
	if !f.set {
		return ""
	}
	return strconv.FormatBool(f.value)
}

func (f *apiOptionalBoolFlag) IsBoolFlag() bool {
	return true
}

func (f apiOptionalBoolFlag) ptr() *bool {
	if !f.set {
		return nil
	}
	v := f.value
	return &v
}

type apiOptionalIntFlag struct {
	value int
	set   bool
}

func (f *apiOptionalIntFlag) Set(v string) error {
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return err
	}
	f.value = parsed
	f.set = true
	return nil
}

func (f *apiOptionalIntFlag) String() string {
	if !f.set {
		return ""
	}
	return strconv.Itoa(f.value)
}

func (f apiOptionalIntFlag) ptr() *int {
	if !f.set {
		return nil
	}
	v := f.value
	return &v
}

type apiOptionalStringFlag struct {
	value string
	set   bool
}

func (f *apiOptionalStringFlag) Set(v string) error {
	f.value = v
	f.set = true
	return nil
}

func (f *apiOptionalStringFlag) String() string {
	return f.value
}

func (f apiOptionalStringFlag) ptr() *string {
	if !f.set {
		return nil
	}
	v := f.value
	return &v
}

type apiStringMapFlags struct {
	values map[string]string
	set    bool
}

func (f *apiStringMapFlags) Set(v string) error {
	name, value, ok := strings.Cut(v, ":")
	if !ok {
		return fmt.Errorf("custom header %q must be in Name: Value form", v)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("custom header name must not be empty")
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[name] = strings.TrimSpace(value)
	f.set = true
	return nil
}

func (f *apiStringMapFlags) String() string {
	if !f.set {
		return ""
	}
	keys := make([]string, 0, len(f.values))
	for k := range f.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+f.values[k])
	}
	return strings.Join(parts, ", ")
}

func (f apiStringMapFlags) ptr() *map[string]string {
	if !f.set {
		return nil
	}
	values := make(map[string]string, len(f.values))
	for k, v := range f.values {
		values[k] = v
	}
	return &values
}

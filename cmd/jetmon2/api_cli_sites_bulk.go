package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const (
	apiSitesBulkAddMaxCount      = 200
	defaultAPIBulkAddBlogIDStart = int64(900000000)
)

type apiSitesBulkAddOptions struct {
	count                int
	batch                string
	source               string
	file                 string
	blogIDStart          int64
	dryRun               bool
	idempotencyKeyPrefix string
	monitorActive        apiOptionalBoolFlag
}

type apiBulkSiteEntry struct {
	MonitorURL           string            `json:"monitor_url"`
	CheckKeyword         *string           `json:"check_keyword,omitempty"`
	RedirectPolicy       *string           `json:"redirect_policy,omitempty"`
	TimeoutSeconds       *int              `json:"timeout_seconds,omitempty"`
	CustomHeaders        map[string]string `json:"custom_headers,omitempty"`
	AlertCooldownMinutes *int              `json:"alert_cooldown_minutes,omitempty"`
	CheckInterval        *int              `json:"check_interval,omitempty"`
}

type apiSitesBulkAddOutput struct {
	DryRun  bool              `json:"dry_run,omitempty"`
	Count   int               `json:"count"`
	Sites   []json.RawMessage `json:"sites,omitempty"`
	Created []json.RawMessage `json:"created,omitempty"`
}

func cmdAPISitesBulkAdd(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api sites bulk-add", &opts)
	bulk := apiSitesBulkAddOptions{
		source:      "fixture",
		blogIDStart: defaultAPIBulkAddBlogIDStart,
	}
	fs.IntVar(&bulk.count, "count", 0, "number of sites to create, max 200")
	fs.StringVar(&bulk.batch, "batch", "", "stable batch label; derives blog ids and stores a custom header marker")
	fs.StringVar(&bulk.source, "source", bulk.source, "site source: fixture, file, or stdin")
	fs.StringVar(&bulk.file, "file", "", "source file for --source file")
	fs.Int64Var(&bulk.blogIDStart, "blog-id-start", bulk.blogIDStart, "first blog_id to assign")
	fs.BoolVar(&bulk.dryRun, "dry-run", false, "print planned create payloads without sending requests")
	fs.StringVar(&bulk.idempotencyKeyPrefix, "idempotency-key-prefix", "", "prefix for per-site Idempotency-Key headers")
	fs.Var(&bulk.monitorActive, "monitor-active", "override monitor_active for every generated site")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api sites bulk-add [flags]")
	}
	return runAPISitesBulkAdd(context.Background(), nil, opts, bulk)
}

func runAPISitesBulkAdd(ctx context.Context, client *http.Client, opts apiCLIOptions, bulk apiSitesBulkAddOptions) error {
	if opts.out == nil {
		opts.out = io.Discard
	}
	entries, err := loadAPIBulkSiteEntries(bulk, opts.in)
	if err != nil {
		return err
	}
	planned, err := planAPIBulkSiteCreates(entries, bulk)
	if err != nil {
		return err
	}

	if bulk.dryRun {
		sites, err := marshalAPIBulkSiteRequests(planned)
		if err != nil {
			return err
		}
		return writeAPIValueOutput(opts.out, apiSitesBulkAddOutput{
			DryRun: true,
			Count:  len(sites),
			Sites:  sites,
		}, opts)
	}

	created := make([]json.RawMessage, 0, len(planned))
	for i, req := range planned {
		body, err := json.Marshal(req)
		if err != nil {
			return err
		}
		requestOpts := opts
		var response bytes.Buffer
		requestOpts.out = &response
		if bulk.idempotencyKeyPrefix != "" {
			requestOpts.idempotencyKey = fmt.Sprintf("%s-%03d", bulk.idempotencyKeyPrefix, i+1)
		}
		if err := executeAPIRequest(ctx, client, requestOpts, http.MethodPost, "/api/v1/sites", body); err != nil {
			if response.Len() > 0 {
				_, _ = opts.out.Write(response.Bytes())
			}
			return fmt.Errorf("create site %d (%s): %w", req.BlogID, req.MonitorURL, err)
		}
		created = append(created, json.RawMessage(bytes.TrimSpace(response.Bytes())))
	}

	return writeAPIValueOutput(opts.out, apiSitesBulkAddOutput{
		Count:   len(created),
		Created: created,
	}, opts)
}

func loadAPIBulkSiteEntries(opts apiSitesBulkAddOptions, in io.Reader) ([]apiBulkSiteEntry, error) {
	var data []byte
	var err error
	switch opts.source {
	case "fixture":
		if opts.file != "" {
			return nil, errors.New("--file is only valid with --source file")
		}
		data = apiCLISiteFixture
	case "file":
		if opts.file == "" {
			return nil, errors.New("--file is required with --source file")
		}
		data, err = os.ReadFile(opts.file)
		if err != nil {
			return nil, err
		}
	case "stdin":
		if in == nil {
			return nil, errors.New("stdin source requires an input reader")
		}
		data, err = io.ReadAll(in)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("source must be one of: fixture, file, stdin")
	}
	return parseAPIBulkSiteEntries(data)
}

func parseAPIBulkSiteEntries(data []byte) ([]apiBulkSiteEntry, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("site source is empty")
	}
	if trimmed[0] == '[' || trimmed[0] == '{' || trimmed[0] == '"' {
		return parseAPIBulkJSONSiteEntries(trimmed)
	}
	return parseAPIBulkCSVSiteEntries(trimmed)
}

func parseAPIBulkJSONSiteEntries(data []byte) ([]apiBulkSiteEntry, error) {
	var raw []json.RawMessage
	if data[0] == '[' {
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
	} else {
		raw = []json.RawMessage{data}
	}
	entries := make([]apiBulkSiteEntry, 0, len(raw))
	for _, item := range raw {
		var entry apiBulkSiteEntry
		if err := json.Unmarshal(item, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return validateAPIBulkSiteEntries(entries)
}

func parseAPIBulkCSVSiteEntries(data []byte) ([]apiBulkSiteEntry, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, errors.New("site source is empty")
	}

	header := apiBulkCSVHeader(records[0])
	start := 0
	if len(header) > 0 {
		start = 1
	}

	entries := make([]apiBulkSiteEntry, 0, len(records)-start)
	for _, record := range records[start:] {
		if len(record) == 0 || strings.TrimSpace(record[0]) == "" {
			continue
		}
		if len(header) == 0 {
			entries = append(entries, apiBulkSiteEntry{MonitorURL: strings.TrimSpace(record[0])})
			continue
		}
		entry, err := apiBulkSiteEntryFromCSVRecord(header, record)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return validateAPIBulkSiteEntries(entries)
}

func apiBulkCSVHeader(record []string) map[string]int {
	header := map[string]int{}
	hasURL := false
	for i, col := range record {
		name := strings.ToLower(strings.TrimSpace(col))
		header[name] = i
		if name == "monitor_url" || name == "url" {
			hasURL = true
		}
	}
	if !hasURL {
		return nil
	}
	return header
}

func apiBulkSiteEntryFromCSVRecord(header map[string]int, record []string) (apiBulkSiteEntry, error) {
	entry := apiBulkSiteEntry{}
	entry.MonitorURL = csvField(header, record, "monitor_url")
	if entry.MonitorURL == "" {
		entry.MonitorURL = csvField(header, record, "url")
	}
	if v := csvField(header, record, "check_keyword"); v != "" {
		entry.CheckKeyword = &v
	}
	if v := csvField(header, record, "redirect_policy"); v != "" {
		entry.RedirectPolicy = &v
	}
	if v := csvField(header, record, "timeout_seconds"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return entry, fmt.Errorf("timeout_seconds must be an integer: %w", err)
		}
		entry.TimeoutSeconds = &parsed
	}
	if v := csvField(header, record, "check_interval"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return entry, fmt.Errorf("check_interval must be an integer: %w", err)
		}
		entry.CheckInterval = &parsed
	}
	return entry, nil
}

func csvField(header map[string]int, record []string, name string) string {
	idx, ok := header[name]
	if !ok || idx >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[idx])
}

func validateAPIBulkSiteEntries(entries []apiBulkSiteEntry) ([]apiBulkSiteEntry, error) {
	if len(entries) == 0 {
		return nil, errors.New("no sites found in source")
	}
	for i := range entries {
		entries[i].MonitorURL = strings.TrimSpace(entries[i].MonitorURL)
		if entries[i].MonitorURL == "" {
			return nil, fmt.Errorf("site source entry %d is missing monitor_url", i+1)
		}
	}
	return entries, nil
}

func planAPIBulkSiteCreates(entries []apiBulkSiteEntry, opts apiSitesBulkAddOptions) ([]apiSiteCreateRequest, error) {
	if opts.count <= 0 {
		return nil, errors.New("count is required and must be positive")
	}
	if opts.count > apiSitesBulkAddMaxCount {
		return nil, fmt.Errorf("count must be <= %d", apiSitesBulkAddMaxCount)
	}
	if opts.blogIDStart <= 0 {
		return nil, errors.New("blog-id-start must be a positive integer")
	}
	if opts.batch != "" && opts.blogIDStart == defaultAPIBulkAddBlogIDStart {
		opts.blogIDStart = apiCLIBatchBlogIDStart(opts.batch)
	}
	if len(entries) == 0 {
		return nil, errors.New("no sites found in source")
	}

	out := make([]apiSiteCreateRequest, 0, opts.count)
	for i := 0; i < opts.count; i++ {
		entry := entries[i%len(entries)]
		req := apiSiteCreateRequest{
			BlogID:               opts.blogIDStart + int64(i),
			MonitorURL:           entry.MonitorURL,
			MonitorActive:        opts.monitorActive.ptr(),
			CheckKeyword:         entry.CheckKeyword,
			RedirectPolicy:       entry.RedirectPolicy,
			TimeoutSeconds:       entry.TimeoutSeconds,
			AlertCooldownMinutes: entry.AlertCooldownMinutes,
			CheckInterval:        entry.CheckInterval,
		}
		if len(entry.CustomHeaders) > 0 || opts.batch != "" {
			headers := make(map[string]string, len(entry.CustomHeaders)+1)
			for k, v := range entry.CustomHeaders {
				headers[k] = v
			}
			if opts.batch != "" {
				headers[apiCLIBatchHeader] = opts.batch
			}
			req.CustomHeaders = &headers
		}
		out = append(out, req)
	}
	return out, nil
}

func marshalAPIBulkSiteRequests(requests []apiSiteCreateRequest) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(requests))
	for _, req := range requests {
		b, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(b))
	}
	return out, nil
}

func (e *apiBulkSiteEntry) UnmarshalJSON(data []byte) error {
	var urlOnly string
	if err := json.Unmarshal(data, &urlOnly); err == nil {
		e.MonitorURL = urlOnly
		return nil
	}

	type bulkSiteEntry apiBulkSiteEntry
	var aux struct {
		bulkSiteEntry
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*e = apiBulkSiteEntry(aux.bulkSiteEntry)
	if e.MonitorURL == "" {
		e.MonitorURL = aux.URL
	}
	return nil
}

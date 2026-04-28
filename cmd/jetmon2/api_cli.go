package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

const defaultAPIBaseURL = "http://localhost:8090"

type apiCLIOptions struct {
	baseURL        string
	token          string
	verbose        bool
	pretty         bool
	output         string
	timeout        time.Duration
	body           string
	bodyFile       string
	idempotencyKey string
	headers        apiHeaderFlags
	out            io.Writer
	errOut         io.Writer
	in             io.Reader
}

type apiHeaderFlags []string

type apiHTTPResponse struct {
	StatusCode int
	Status     string
	Body       []byte
}

func (h *apiHeaderFlags) String() string {
	return strings.Join(*h, ",")
}

func (h *apiHeaderFlags) Set(v string) error {
	if !strings.Contains(v, ":") {
		return fmt.Errorf("header %q must be in Name: Value form", v)
	}
	*h = append(*h, v)
	return nil
}

func cmdAPI(args []string) {
	if len(args) == 0 {
		printAPIUsage(os.Stderr)
		os.Exit(1)
	}

	sub := args[0]
	rest := args[1:]
	var err error
	switch sub {
	case "health":
		err = cmdAPIHealth(rest)
	case "me":
		err = cmdAPIMe(rest)
	case "request":
		err = cmdAPIRequest(rest)
	case "sites":
		err = cmdAPISites(rest)
	case "events":
		err = cmdAPIEvents(rest)
	case "webhooks":
		err = cmdAPIWebhooks(rest)
	case "alert-contacts":
		err = cmdAPIAlertContacts(rest)
	case "smoke":
		err = cmdAPISmoke(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown api subcommand %q (want: health, me, request, sites, events, webhooks, alert-contacts, smoke)\n", sub)
		printAPIUsage(os.Stderr)
		os.Exit(1)
	}
	if err != nil {
		logAPIErrorAndExit(err)
	}
}

func printAPIUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: jetmon2 api <health|me|request|sites|events|webhooks|alert-contacts|smoke> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Environment:")
	fmt.Fprintln(w, "  JETMON_API_URL     API base URL (default: http://localhost:8090)")
	fmt.Fprintln(w, "  JETMON_API_TOKEN   Bearer token for authenticated routes")
}

func cmdAPIHealth(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api health", &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: jetmon2 api health [flags]")
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, "/api/v1/health", nil)
}

func cmdAPIMe(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api me", &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: jetmon2 api me [flags]")
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, "/api/v1/me", nil)
}

func cmdAPIRequest(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api request", &opts)
	fs.StringVar(&opts.body, "body", "", "literal request body")
	fs.StringVar(&opts.bodyFile, "body-file", "", "file containing request body (- reads stdin)")
	fs.StringVar(&opts.idempotencyKey, "idempotency-key", "", "Idempotency-Key header for POST retries")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: jetmon2 api request [flags] <method> <path-or-url>")
	}

	body, err := readAPIRequestBody(opts)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, fs.Arg(0), fs.Arg(1), body)
}

func defaultAPIOptions() apiCLIOptions {
	return apiCLIOptions{
		baseURL: envOrDefault("JETMON_API_URL", defaultAPIBaseURL),
		token:   os.Getenv("JETMON_API_TOKEN"),
		timeout: 10 * time.Second,
		out:     os.Stdout,
		errOut:  os.Stderr,
		in:      os.Stdin,
	}
}

func newAPIFlagSet(name string, opts *apiCLIOptions) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(opts.errOut)
	fs.StringVar(&opts.baseURL, "base-url", opts.baseURL, "API base URL")
	fs.StringVar(&opts.token, "token", opts.token, "Bearer token")
	fs.BoolVar(&opts.verbose, "v", false, "print request and response headers to stderr")
	fs.BoolVar(&opts.verbose, "verbose", false, "print request and response headers to stderr")
	fs.BoolVar(&opts.pretty, "pretty", false, "pretty-print JSON response bodies")
	fs.StringVar(&opts.output, "output", "json", "response output format: json or table")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "request timeout")
	fs.Var(&opts.headers, "header", "additional request header in Name: Value form (repeatable)")
	return fs
}

func readAPIRequestBody(opts apiCLIOptions) ([]byte, error) {
	if opts.body != "" && opts.bodyFile != "" {
		return nil, errors.New("use --body or --body-file, not both")
	}
	if opts.body != "" {
		return []byte(opts.body), nil
	}
	if opts.bodyFile == "" {
		return nil, nil
	}
	if opts.bodyFile == "-" {
		return io.ReadAll(opts.in)
	}
	return os.ReadFile(opts.bodyFile)
}

func executeAPIRequest(ctx context.Context, client *http.Client, opts apiCLIOptions, method, target string, body []byte) error {
	if opts.out == nil {
		opts.out = io.Discard
	}
	if err := validateAPIOutputFormat(opts.output); err != nil {
		return err
	}
	resp, err := doAPIRequest(ctx, client, opts, method, target, body)
	if err != nil {
		return err
	}
	if err := writeAPIOutput(opts.out, resp.Body, opts); err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("api returned %s", resp.Status)
	}
	return nil
}

func doAPIRequest(ctx context.Context, client *http.Client, opts apiCLIOptions, method, target string, body []byte) (apiHTTPResponse, error) {
	if opts.errOut == nil {
		opts.errOut = io.Discard
	}
	if opts.timeout <= 0 {
		opts.timeout = 10 * time.Second
	}
	if client == nil {
		client = &http.Client{Timeout: opts.timeout}
	}

	requestURL, err := apiRequestURL(opts.baseURL, target)
	if err != nil {
		return apiHTTPResponse{}, err
	}

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), requestURL, bodyReader)
	if err != nil {
		return apiHTTPResponse{}, err
	}
	applyAPIRequestHeaders(req, opts, len(body) > 0)

	if opts.verbose {
		writeAPIRequestHeaders(opts.errOut, req)
	}

	resp, err := client.Do(req)
	if err != nil {
		return apiHTTPResponse{}, err
	}
	defer resp.Body.Close()

	if opts.verbose {
		writeAPIResponseHeaders(opts.errOut, resp)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return apiHTTPResponse{}, err
	}
	return apiHTTPResponse{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       respBody,
	}, nil
}

func apiRequestURL(baseURL, target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", errors.New("request path is required")
	}
	if u, err := url.Parse(target); err == nil && u.IsAbs() {
		return u.String(), nil
	}

	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid API base URL %q: %w", baseURL, err)
	}
	if !base.IsAbs() || base.Host == "" {
		return "", fmt.Errorf("invalid API base URL %q: must include scheme and host", baseURL)
	}
	rel, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("invalid API path %q: %w", target, err)
	}
	if !strings.HasPrefix(rel.Path, "/") {
		rel.Path = "/" + rel.Path
	}
	return base.ResolveReference(rel).String(), nil
}

func applyAPIRequestHeaders(req *http.Request, opts apiCLIOptions, hasBody bool) {
	req.Header.Set("Accept", "application/json")
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(opts.token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(opts.token))
	}
	if strings.TrimSpace(opts.idempotencyKey) != "" {
		req.Header.Set("Idempotency-Key", strings.TrimSpace(opts.idempotencyKey))
	}
	for _, raw := range opts.headers {
		name, value, ok := strings.Cut(raw, ":")
		if !ok {
			continue
		}
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}
}

func writeAPIRequestHeaders(w io.Writer, req *http.Request) {
	path := req.URL.RequestURI()
	if path == "" {
		path = "/"
	}
	fmt.Fprintf(w, "> %s %s %s\n", req.Method, path, req.Proto)
	fmt.Fprintf(w, "> Host: %s\n", req.URL.Host)
	writeSortedHeaders(w, "> ", req.Header)
	fmt.Fprintln(w, ">")
}

func writeAPIResponseHeaders(w io.Writer, resp *http.Response) {
	fmt.Fprintf(w, "< %s %s\n", resp.Proto, resp.Status)
	writeSortedHeaders(w, "< ", resp.Header)
	fmt.Fprintln(w, "<")
}

func writeSortedHeaders(w io.Writer, prefix string, h http.Header) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range h.Values(k) {
			fmt.Fprintf(w, "%s%s: %s\n", prefix, k, v)
		}
	}
}

func writeAPIResponseBody(w io.Writer, body []byte, pretty bool) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	if pretty && json.Valid(body) {
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, body, "", "  "); err != nil {
			return err
		}
		body = formatted.Bytes()
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if !bytes.HasSuffix(body, []byte("\n")) {
		_, err := fmt.Fprintln(w)
		return err
	}
	return nil
}

func writeAPIValueOutput(w io.Writer, value any, opts apiCLIOptions) error {
	if w == nil {
		w = io.Discard
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeAPIOutput(w, body, opts)
}

func writeAPIOutput(w io.Writer, body []byte, opts apiCLIOptions) error {
	if err := validateAPIOutputFormat(opts.output); err != nil {
		return err
	}
	switch opts.output {
	case "", "json":
		return writeAPIResponseBody(w, body, opts.pretty)
	case "table":
		return writeAPIResponseTable(w, body)
	}
	return nil
}

func validateAPIOutputFormat(output string) error {
	switch output {
	case "", "json", "table":
		return nil
	default:
		return fmt.Errorf("output must be one of: json, table")
	}
}

func writeAPIResponseTable(w io.Writer, body []byte) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	rows := apiTableRows(value)
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "no rows")
		return err
	}
	columns := apiTableColumns(rows)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i, col := range columns {
		if i > 0 {
			fmt.Fprint(tw, "\t")
		}
		fmt.Fprint(tw, col)
	}
	fmt.Fprintln(tw)
	for _, row := range rows {
		for i, col := range columns {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, apiTableValue(row[col]))
		}
		fmt.Fprintln(tw)
	}
	return tw.Flush()
}

func apiTableRows(value any) []map[string]any {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range []string{"data", "created", "sites", "steps"} {
			if data, ok := v[key].([]any); ok {
				return apiRowsFromArray(data)
			}
		}
		return []map[string]any{v}
	case []any:
		return apiRowsFromArray(v)
	default:
		return nil
	}
}

func apiRowsFromArray(data []any) []map[string]any {
	rows := make([]map[string]any, 0, len(data))
	for _, item := range data {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	return rows
}

func apiTableColumns(rows []map[string]any) []string {
	best := []string{}
	for _, cols := range [][]string{
		{"id", "blog_id", "monitor_url", "monitor_active", "current_state", "current_severity", "active_event_id"},
		{"blog_id", "monitor_url", "monitor_active", "check_keyword", "redirect_policy", "timeout_seconds"},
		{"id", "site_id", "check_type", "state", "severity", "started_at", "ended_at"},
		{"id", "url", "active", "events", "secret_preview", "created_at"},
		{"id", "label", "active", "transport", "min_severity", "max_per_hour", "destination_preview"},
		{"id", "status", "attempt", "event_id", "event_type", "last_status_code", "created_at"},
		{"site_id", "action", "note", "error"},
		{"name", "status", "detail"},
	} {
		present := apiColumnsPresent(rows, cols)
		if len(present) > len(best) {
			best = present
		}
	}
	if len(best) > 0 {
		return best
	}
	seen := map[string]struct{}{}
	for _, row := range rows {
		for k := range row {
			seen[k] = struct{}{}
		}
	}
	cols := make([]string, 0, len(seen))
	for k := range seen {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return cols
}

func apiColumnsPresent(rows []map[string]any, cols []string) []string {
	out := []string{}
	for _, col := range cols {
		for _, row := range rows {
			if _, ok := row[col]; ok {
				out = append(out, col)
				break
			}
		}
	}
	return out
}

func apiTableValue(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		return fmt.Sprintf("%t", value)
	case float64:
		if value == float64(int64(value)) {
			return fmt.Sprintf("%d", int64(value))
		}
		return fmt.Sprintf("%g", value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, apiTableValue(item))
		}
		return strings.Join(parts, ",")
	default:
		b, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(b)
	}
}

func logAPIErrorAndExit(err error) {
	fmt.Fprintf(os.Stderr, "api: %v\n", err)
	os.Exit(1)
}

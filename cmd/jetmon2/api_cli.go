package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

const defaultAPIBaseURL = "http://localhost:8090"
const defaultAPIAuthPolicy = "same-origin"

type apiCLIOptions struct {
	baseURL        string
	token          string
	authPolicy     string
	allowRemote    bool
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
	commandName    string
}

type apiHeaderFlags []string

type apiHTTPResponse struct {
	StatusCode int
	Status     string
	Body       []byte
}

type apiCommandInfo struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Example     string `json:"example"`
}

var apiCommandCatalog = []apiCommandInfo{
	{Command: "health", Description: "check API and database health", Example: "jetmon2 api health --pretty"},
	{Command: "me", Description: "show the authenticated API key identity", Example: "jetmon2 api me --pretty"},
	{Command: "request", Description: "send an arbitrary request to an API path", Example: "jetmon2 api request --output table GET /api/v1/sites"},
	{Command: "sites list", Description: "list monitored sites with filters", Example: "jetmon2 api sites list --limit 20 --output table"},
	{Command: "sites get", Description: "show one monitored site", Example: "jetmon2 api sites get 12345 --pretty"},
	{Command: "sites create", Description: "create a monitored site", Example: "jetmon2 api sites create --blog-id 12345 --url https://example.com --pretty"},
	{Command: "sites update", Description: "update check settings for a site", Example: "jetmon2 api sites update 12345 --url https://example.com/health --pretty"},
	{Command: "sites delete", Description: "delete a monitored site", Example: "jetmon2 api sites delete 12345"},
	{Command: "sites pause", Description: "pause monitoring for a site", Example: "jetmon2 api sites pause 12345 --idempotency-key site-12345-pause"},
	{Command: "sites resume", Description: "resume monitoring for a site", Example: "jetmon2 api sites resume 12345 --idempotency-key site-12345-resume"},
	{Command: "sites trigger-now", Description: "run an immediate check", Example: "jetmon2 api sites trigger-now 12345 --pretty"},
	{Command: "sites bulk-add", Description: "create bounded local test-site batches", Example: "jetmon2 api sites bulk-add --count 3 --batch local-smoke --dry-run --pretty"},
	{Command: "sites cleanup", Description: "delete deterministic CLI-created site batches", Example: "jetmon2 api sites cleanup --batch local-smoke --count 3 --output table"},
	{Command: "sites simulate-failure", Description: "mutate test sites into known failure modes", Example: "jetmon2 api sites simulate-failure --batch local-smoke --mode http-500 --wait 30s --output table"},
	{Command: "events list", Description: "list events for a site", Example: "jetmon2 api events list 12345 --active=true --output table"},
	{Command: "events get", Description: "show one event", Example: "jetmon2 api events get --site-id 12345 98765 --pretty"},
	{Command: "events transitions", Description: "list event transition history", Example: "jetmon2 api events transitions 12345 98765 --output table"},
	{Command: "events close", Description: "manually close an event", Example: "jetmon2 api events close 12345 98765 --reason manual_override --pretty"},
	{Command: "webhooks list", Description: "list webhook registrations", Example: "jetmon2 api webhooks list --output table"},
	{Command: "webhooks create", Description: "create a webhook registration", Example: "jetmon2 api webhooks create --url https://receiver.example.test/jetmon --event event.opened --pretty"},
	{Command: "webhooks deliveries", Description: "list webhook delivery rows", Example: "jetmon2 api webhooks deliveries 77 --status failed --output table"},
	{Command: "webhooks retry", Description: "retry an abandoned webhook delivery", Example: "jetmon2 api webhooks retry 77 555 --idempotency-key webhook-77-555-retry --pretty"},
	{Command: "alert-contacts list", Description: "list managed alert contacts", Example: "jetmon2 api alert-contacts list --output table"},
	{Command: "alert-contacts create", Description: "create an email, PagerDuty, Slack, or Teams contact", Example: "jetmon2 api alert-contacts create --label Local --transport email --address alerts@example.test --pretty"},
	{Command: "alert-contacts test", Description: "send a managed alert-contact test", Example: "jetmon2 api alert-contacts test 12 --idempotency-key alert-12-test --pretty"},
	{Command: "alert-contacts deliveries", Description: "list managed alert delivery rows", Example: "jetmon2 api alert-contacts deliveries 12 --status failed --output table"},
	{Command: "smoke", Description: "run the Docker-local API smoke workflow", Example: "jetmon2 api smoke --batch local-smoke --exercise webhook --pretty"},
	{Command: "commands", Description: "list API CLI commands and examples", Example: "jetmon2 api commands --output table"},
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
	case "commands":
		err = cmdAPICommands(rest)
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
		fmt.Fprintf(os.Stderr, "unknown api subcommand %q (want: health, me, request, commands, sites, events, webhooks, alert-contacts, smoke)\n", sub)
		printAPIUsage(os.Stderr)
		os.Exit(1)
	}
	if err != nil {
		logAPIErrorAndExit(err)
	}
}

func printAPIUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: jetmon2 api <health|me|request|commands|sites|events|webhooks|alert-contacts|smoke> [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run `jetmon2 api commands --output table` for the command catalog.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Environment:")
	fmt.Fprintln(w, "  JETMON_API_URL          API base URL (default: http://localhost:8090)")
	fmt.Fprintln(w, "  JETMON_API_TOKEN        Bearer token for authenticated routes")
	fmt.Fprintln(w, "  JETMON_API_AUTH_POLICY  automatic auth policy: same-origin or any-origin (default: same-origin)")
}

func cmdAPIHealth(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api health", &opts)
	if err := parseAPIFlags(fs, args); err != nil {
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
	if err := parseAPIFlags(fs, args); err != nil {
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
	if err := parseAPIFlags(fs, args); err != nil {
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

func cmdAPICommands(args []string) error {
	opts := defaultAPIOptions()
	opts.output = "table"
	fs := newAPIFlagSet("api commands", &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: jetmon2 api commands [flags]")
	}
	return writeAPICommands(opts)
}

func writeAPICommands(opts apiCLIOptions) error {
	return writeAPIValueOutput(opts.out, map[string]any{"commands": apiCommandCatalog}, opts)
}

func defaultAPIOptions() apiCLIOptions {
	return apiCLIOptions{
		baseURL:    envOrDefault("JETMON_API_URL", defaultAPIBaseURL),
		token:      os.Getenv("JETMON_API_TOKEN"),
		authPolicy: envOrDefault("JETMON_API_AUTH_POLICY", defaultAPIAuthPolicy),
		timeout:    10 * time.Second,
		out:        os.Stdout,
		errOut:     os.Stderr,
		in:         os.Stdin,
	}
}

func newAPIFlagSet(name string, opts *apiCLIOptions) *flag.FlagSet {
	opts.commandName = name
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(opts.errOut)
	fs.StringVar(&opts.baseURL, "base-url", opts.baseURL, "API base URL")
	fs.StringVar(&opts.token, "token", opts.token, "Bearer token")
	if tokenFlag := fs.Lookup("token"); tokenFlag != nil {
		tokenFlag.DefValue = ""
	}
	fs.StringVar(&opts.authPolicy, "auth-policy", opts.authPolicy, "automatic auth policy: same-origin or any-origin")
	fs.BoolVar(&opts.allowRemote, "allow-remote", opts.allowRemote, "allow writes to a non-local API base URL")
	fs.BoolVar(&opts.verbose, "v", false, "print request and response headers to stderr")
	fs.BoolVar(&opts.verbose, "verbose", false, "print request and response headers to stderr")
	fs.BoolVar(&opts.pretty, "pretty", false, "pretty-print JSON response bodies")
	defaultOutput := opts.output
	if defaultOutput == "" {
		defaultOutput = "json"
	}
	fs.StringVar(&opts.output, "output", defaultOutput, "response output format: json or table")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "request timeout")
	fs.Var(&opts.headers, "header", "additional request header in Name: Value form (repeatable)")
	fs.Usage = func() {
		printAPIFlagUsage(fs.Output(), fs)
	}
	return fs
}

type apiBoolFlag interface {
	IsBoolFlag() bool
}

func parseAPIFlags(fs *flag.FlagSet, args []string) error {
	normalized := normalizeAPIFlagArgs(fs, args)
	return fs.Parse(normalized)
}

func normalizeAPIFlagArgs(fs *flag.FlagSet, args []string) []string {
	flags := []string{}
	positionals := []string{}
	onlyPositionals := false
	hasTerminator := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if onlyPositionals || arg == "-" || !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "--" {
			onlyPositionals = true
			hasTerminator = true
			continue
		}

		name, hasValue := apiFlagName(arg)
		f := fs.Lookup(name)
		if f == nil {
			flags = append(flags, arg)
			continue
		}
		flags = append(flags, arg)
		if hasValue || apiFlagIsBool(f) {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if hasTerminator {
		flags = append(flags, "--")
	}
	return append(flags, positionals...)
}

func apiFlagName(arg string) (string, bool) {
	name := strings.TrimLeft(arg, "-")
	if idx := strings.IndexByte(name, '='); idx >= 0 {
		return name[:idx], true
	}
	return name, false
}

func apiFlagIsBool(f *flag.Flag) bool {
	bf, ok := f.Value.(apiBoolFlag)
	return ok && bf.IsBoolFlag()
}

func printAPIFlagUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintf(w, "Usage of %s:\n", fs.Name())
	printAPIFlagDefaults(w, fs)
}

func printAPIFlagDefaults(w io.Writer, fs *flag.FlagSet) {
	flags := []*flag.Flag{}
	fs.VisitAll(func(f *flag.Flag) {
		flags = append(flags, f)
	})
	sort.Slice(flags, func(i, j int) bool {
		return flags[i].Name < flags[j].Name
	})

	for _, f := range flags {
		valueName, usage := flag.UnquoteUsage(f)
		prefix := "--"
		if len(f.Name) == 1 {
			prefix = "-"
		}
		fmt.Fprintf(w, "  %s%s", prefix, f.Name)
		if valueName != "" {
			fmt.Fprintf(w, " %s", valueName)
		}
		fmt.Fprintf(w, "\n    \t%s", usage)
		if defaultValue := apiFlagDefaultValue(f, valueName); defaultValue != "" {
			fmt.Fprintf(w, " (default %s)", defaultValue)
		}
		fmt.Fprintln(w)
	}
}

func apiFlagDefaultValue(f *flag.Flag, valueName string) string {
	if f.DefValue == "" || f.DefValue == "0" || f.DefValue == "0s" || f.DefValue == "false" {
		return ""
	}
	if valueName == "string" {
		return strconv.Quote(f.DefValue)
	}
	return f.DefValue
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
	if apiMethodRequiresRemoteWriteGuard(method) {
		if _, err := requireAPILocalURLOrAllowRemote(requestURL, opts.allowRemote, apiRemoteGuardCommand(opts)); err != nil {
			return apiHTTPResponse{}, err
		}
	}

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), requestURL, bodyReader)
	if err != nil {
		return apiHTTPResponse{}, err
	}
	sendAutoAuth, err := shouldSendAPIAutoAuth(opts.baseURL, requestURL, opts.authPolicy)
	if err != nil {
		return apiHTTPResponse{}, err
	}
	applyAPIRequestHeaders(req, opts, len(body) > 0, sendAutoAuth)

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

func shouldSendAPIAutoAuth(baseURL, requestURL, policy string) (bool, error) {
	policy = strings.ToLower(strings.TrimSpace(policy))
	if policy == "" {
		policy = defaultAPIAuthPolicy
	}
	switch policy {
	case "any-origin":
		return true, nil
	case "same-origin":
		base, err := url.Parse(strings.TrimRight(baseURL, "/"))
		if err != nil {
			return false, fmt.Errorf("invalid API base URL %q: %w", baseURL, err)
		}
		target, err := url.Parse(requestURL)
		if err != nil {
			return false, fmt.Errorf("invalid request URL %q: %w", requestURL, err)
		}
		return sameAPIOrigin(base, target), nil
	default:
		return false, fmt.Errorf("invalid auth policy %q (want: same-origin or any-origin)", policy)
	}
}

func sameAPIOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func apiMethodRequiresRemoteWriteGuard(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func apiRemoteGuardCommand(opts apiCLIOptions) string {
	if strings.TrimSpace(opts.commandName) != "" {
		return strings.TrimSpace(opts.commandName)
	}
	return "api"
}

func requireAPILocalOrAllowRemote(opts apiCLIOptions, allowRemote bool, command string) (bool, error) {
	return requireAPILocalURLOrAllowRemote(opts.baseURL, allowRemote, command)
}

func requireAPILocalURLOrAllowRemote(rawURL string, allowRemote bool, command string) (bool, error) {
	local, err := isLocalAPIURL(rawURL)
	if err != nil {
		return false, err
	}
	if local {
		return false, nil
	}
	if allowRemote {
		return true, nil
	}
	return true, fmt.Errorf("%s refuses to modify non-local API URL %q without --allow-remote (local means localhost or loopback IP)", command, rawURL)
}

func isLocalAPIURL(rawURL string) (bool, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false, fmt.Errorf("invalid API URL %q: %w", rawURL, err)
	}
	if !u.IsAbs() || u.Host == "" {
		return false, fmt.Errorf("invalid API URL %q: must include scheme and host", rawURL)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true, nil
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback(), nil
}

func applyAPIRequestHeaders(req *http.Request, opts apiCLIOptions, hasBody bool, sendAutoAuth bool) {
	req.Header.Set("Accept", "application/json")
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if sendAutoAuth && strings.TrimSpace(opts.token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(opts.token))
	}
	if sendAutoAuth && strings.TrimSpace(opts.idempotencyKey) != "" {
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
			if isSensitiveAPIHeader(k) {
				v = "[redacted]"
			}
			fmt.Fprintf(w, "%s%s: %s\n", prefix, k, v)
		}
	}
}

func isSensitiveAPIHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "proxy-authorization", "idempotency-key", "cookie", "set-cookie", "x-api-key":
		return true
	default:
		return false
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
		if rows := apiWorkflowTableRows(v); len(rows) > 0 {
			return rows
		}
		for _, key := range []string{"data", "created", "sites", "steps", "commands"} {
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

func apiWorkflowTableRows(value map[string]any) []map[string]any {
	steps, ok := value["steps"].([]any)
	if !ok {
		return nil
	}
	rows := make([]map[string]any, 0, len(steps))
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if !ok {
			continue
		}
		row := map[string]any{"kind": "step"}
		for k, v := range step {
			row[k] = v
		}
		rows = append(rows, row)
	}
	cleanupResults, _ := value["cleanup_results"].([]any)
	for _, item := range cleanupResults {
		cleanup, ok := item.(map[string]any)
		if !ok {
			continue
		}
		row := map[string]any{
			"kind":   "cleanup",
			"name":   cleanup["resource"],
			"id":     cleanup["id"],
			"status": cleanup["status"],
		}
		if errText, ok := cleanup["error"]; ok {
			row["detail"] = errText
		}
		rows = append(rows, row)
	}
	return rows
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
		{"site_id", "status", "error"},
		{"site_id", "action", "trigger_status", "event_ids", "event_states", "event_severities", "transition_count", "note", "error"},
		{"site_id", "action", "note", "error"},
		{"kind", "name", "id", "status", "detail"},
		{"command", "description", "example"},
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
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "api: %v\n", err)
	os.Exit(1)
}

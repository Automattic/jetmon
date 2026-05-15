package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const (
	apiRolloutModeHeadLegacy = "head-legacy"
	apiRolloutGetSimple      = "get-simple"
)

type apiRolloutGuidedOptions struct {
	bucketMin              int
	bucketMax              int
	sampleSize             int
	compareSampleSize      int
	since                  string
	dryRun                 bool
	rollback               bool
	skipSeed               bool
	includeComparison      bool
	includePolicyMigration bool
}

type apiRolloutStep struct {
	Name       string
	Title      string
	Details    string
	Method     string
	Target     string
	Body       any
	IDKey      string
	Prompt     string
	Confirm    string
	Danger     bool
	ManualOnly bool
}

func cmdAPIRollout(args []string) error {
	if len(args) == 0 {
		printAPIRolloutUsage(os.Stderr)
		return errors.New("usage: jetmon2 api rollout <guided> [flags]")
	}
	switch args[0] {
	case "guided":
		return cmdAPIRolloutGuided(args[1:])
	case "--help", "-h", "help":
		printAPIRolloutUsage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown api rollout subcommand %q (want: guided)", args[0])
	}
}

func printAPIRolloutUsage(w io.Writer) {
	if w == nil {
		w = io.Discard
	}
	fmt.Fprintln(w, "usage: jetmon2 api rollout <guided> [flags]")
}

func cmdAPIRolloutGuided(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api rollout guided", &opts)
	guided := apiRolloutGuidedOptions{
		bucketMin:         -1,
		bucketMax:         -1,
		sampleSize:        1000,
		compareSampleSize: 10000,
		since:             "15m",
	}
	fs.IntVar(&guided.bucketMin, "bucket-min", guided.bucketMin, "inclusive bucket range minimum")
	fs.IntVar(&guided.bucketMax, "bucket-max", guided.bucketMax, "inclusive bucket range maximum")
	fs.IntVar(&guided.sampleSize, "sample-size", guided.sampleSize, "read-only smoke sample size")
	fs.IntVar(&guided.compareSampleSize, "compare-sample-size", guided.compareSampleSize, "HEAD/GET comparison sample size")
	fs.StringVar(&guided.since, "since", guided.since, "recent activity window for post-activation gates")
	fs.BoolVar(&guided.dryRun, "dry-run", false, "print the guided plan without contacting the API")
	fs.BoolVar(&guided.rollback, "rollback", false, "release an activated v2 bucket range back to standby")
	fs.BoolVar(&guided.skipSeed, "skip-seed", false, "skip v2 side-state seed/adopt steps")
	fs.BoolVar(&guided.includeComparison, "include-comparison", false, "run non-authoritative HEAD/GET comparison after activation gates")
	fs.BoolVar(&guided.includePolicyMigration, "include-policy-migration", false, "include staged policy migration dry-run steps after comparison")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api rollout guided [flags]")
	}
	return runAPIRolloutGuided(context.Background(), nil, opts, guided)
}

func runAPIRolloutGuided(ctx context.Context, client *http.Client, opts apiCLIOptions, guided apiRolloutGuidedOptions) error {
	if opts.out == nil {
		opts.out = io.Discard
	}
	if opts.errOut == nil {
		opts.errOut = io.Discard
	}
	if opts.in == nil {
		opts.in = strings.NewReader("")
	}
	if guided.bucketMin < 0 || guided.bucketMax < 0 || guided.bucketMin > guided.bucketMax {
		return errors.New("api rollout guided requires --bucket-min and --bucket-max with min <= max")
	}
	if guided.sampleSize <= 0 {
		return errors.New("sample-size must be positive")
	}
	if guided.compareSampleSize <= 0 {
		return errors.New("compare-sample-size must be positive")
	}
	if strings.TrimSpace(guided.since) == "" {
		return errors.New("since must be non-empty")
	}
	if !guided.dryRun {
		if _, err := requireAPILocalOrAllowRemote(opts, opts.allowRemote, "api rollout guided"); err != nil {
			return err
		}
	}

	steps := buildAPIRolloutGuidedSteps(guided)
	fmt.Fprintf(opts.out, "Jetmon API rollout guided flow\n")
	fmt.Fprintf(opts.out, "base_url: %s\n", opts.baseURL)
	fmt.Fprintf(opts.out, "bucket_range: %d-%d\n", guided.bucketMin, guided.bucketMax)
	if guided.dryRun {
		fmt.Fprintln(opts.out, "mode: dry-run")
	}
	if guided.rollback {
		fmt.Fprintln(opts.out, "path: rollback")
	} else {
		fmt.Fprintln(opts.out, "path: forward")
	}
	fmt.Fprintln(opts.out)

	state := map[string]string{}
	promptReader := bufio.NewReader(opts.in)
	for i, step := range steps {
		fmt.Fprintf(opts.out, "Step %d/%d: %s\n", i+1, len(steps), step.Title)
		if strings.TrimSpace(step.Details) != "" {
			fmt.Fprintf(opts.out, "%s\n", step.Details)
		}
		if step.Danger {
			fmt.Fprintln(opts.out, "This step can change rollout state or depends on an external handoff. Read the result before continuing.")
		}
		if step.ManualOnly {
			if guided.dryRun {
				fmt.Fprintf(opts.out, "DRY-RUN manual confirmation required: %s\n\n", apiRolloutPromptText(step))
				continue
			}
			if err := promptAPIRolloutPhrase(promptReader, opts.out, apiRolloutPromptText(step), step.Confirm); err != nil {
				return fmt.Errorf("%s: %w", step.Name, err)
			}
			fmt.Fprintln(opts.out, "PASS manual checkpoint")
			fmt.Fprintln(opts.out)
			continue
		}
		target := expandAPIRolloutTarget(step.Target, state)
		body := expandAPIRolloutBody(step.Body, state)
		if !guided.dryRun && (apiRolloutHasUnresolvedPlaceholder(target) || apiRolloutBodyHasUnresolvedPlaceholder(body)) {
			return fmt.Errorf("%s: missing confirmation token from previous dry-run plan", step.Name)
		}
		if step.Confirm != "" {
			if guided.dryRun {
				fmt.Fprintf(opts.out, "DRY-RUN confirmation required: %s\n", apiRolloutPromptText(step))
			} else if err := promptAPIRolloutPhrase(promptReader, opts.out, apiRolloutPromptText(step), step.Confirm); err != nil {
				return fmt.Errorf("%s: %w", step.Name, err)
			}
		}
		if guided.dryRun {
			fmt.Fprintf(opts.out, "DRY-RUN request: %s %s\n", step.Method, target)
			if body != nil {
				if rendered, err := json.Marshal(body); err == nil {
					fmt.Fprintf(opts.out, "DRY-RUN body: %s\n", rendered)
				}
			}
			fmt.Fprintln(opts.out)
			continue
		}
		resp, err := apiWorkflowRequestJSON(ctx, client, opts, step.Method, target, body, step.IDKey)
		if err != nil {
			return fmt.Errorf("%s failed: %w%s", step.Name, err, apiRolloutEndpointHint(err))
		}
		if token := apiRolloutConfirmationToken(resp); token != "" {
			switch step.Name {
			case "seed_dry_run":
				state["seed_confirmation_token"] = token
			case "activate_dry_run":
				state["activate_confirmation_token"] = token
			case "release_dry_run":
				state["release_confirmation_token"] = token
			}
		}
		writeAPIRolloutStepResult(opts.out, step.Name, resp)
		fmt.Fprintln(opts.out)
	}
	return nil
}

func buildAPIRolloutGuidedSteps(g apiRolloutGuidedOptions) []apiRolloutStep {
	if g.rollback {
		return []apiRolloutStep{
			apiRolloutRequestStep("health", "Check API health", "Confirm the selected Monitor API is reachable before attempting rollback.", http.MethodGet, "/api/v1/health", nil),
			apiRolloutRequestStep("identity", "Check API identity", "Confirm the configured token can identify itself.", http.MethodGet, "/api/v1/me", nil),
			{
				Name:    "release_dry_run",
				Title:   "Plan v2 bucket release",
				Details: "Ask the Monitor API to plan releasing this bucket range back to standby. This should not mutate state.",
				Method:  http.MethodPost,
				Target:  "/api/v1/rollout/release-buckets",
				Body: map[string]any{
					"bucket_min": g.bucketMin,
					"bucket_max": g.bucketMax,
					"dry_run":    true,
				},
				Prompt:  "Plan releasing this v2 bucket range.",
				Confirm: "YES",
			},
			{
				Name:    "release_execute",
				Title:   "Release v2 bucket range",
				Details: "This returns the range to v2 standby. Systems should restart the matching v1 range only after this succeeds.",
				Method:  http.MethodPost,
				Target:  "/api/v1/rollout/release-buckets",
				Body: map[string]any{
					"bucket_min": g.bucketMin,
					"bucket_max": g.bucketMax,
					"execute":    true,
					"confirm":    "{{release_confirmation_token}}",
				},
				Prompt:  "Release this v2 bucket range.",
				Confirm: fmt.Sprintf("RELEASE %d-%d", g.bucketMin, g.bucketMax),
				Danger:  true,
			},
			apiRolloutRequestStep("status", "Check rollout status", "Confirm the Monitor reports the range as no longer active in v2.", http.MethodGet, "/api/v1/rollout/status", nil),
		}
	}

	steps := []apiRolloutStep{
		apiRolloutRequestStep("health", "Check API health", "Confirm the selected standby Monitor API is reachable.", http.MethodGet, "/api/v1/health", nil),
		apiRolloutRequestStep("identity", "Check API identity", "Confirm the configured token can identify itself before rollout operations.", http.MethodGet, "/api/v1/me", nil),
		{
			Name:    "preflight",
			Title:   "Run standby preflight",
			Details: "Validate Monitor config, DB connectivity, schema version, Veriflier contract/quorum, delivery guards, bucket-control state, and standby mode.",
			Method:  http.MethodPost,
			Target:  "/api/v1/rollout/preflight",
			Body: apiRolloutRangeBody(g, map[string]any{
				"mode": "standby",
			}),
			Prompt:  "Run rollout preflight.",
			Confirm: "YES",
		},
		{
			Name:    "smoke",
			Title:   "Run read-only HEAD/legacy smoke",
			Details: "Sample sites and synthetic canaries through HEAD/legacy probes without writing incident state, runtime freshness, check history, WPCOM notifications, or legacy projection rows.",
			Method:  http.MethodPost,
			Target:  "/api/v1/rollout/smoke",
			Body: apiRolloutRangeBody(g, map[string]any{
				"mode":        apiRolloutModeHeadLegacy,
				"sample_size": g.sampleSize,
				"read_only":   true,
			}),
			Prompt:  "Run read-only smoke checks.",
			Confirm: "YES",
		},
	}
	if !g.skipSeed {
		steps = append(steps,
			apiRolloutStep{
				Name:    "seed_dry_run",
				Title:   "Plan side-state seed/adoption",
				Details: "Plan v2 side-table seed/adopt work, including existing v1 non-running projections, without sending duplicate down notifications.",
				Method:  http.MethodPost,
				Target:  "/api/v1/rollout/seed",
				Body: apiRolloutRangeBody(g, map[string]any{
					"dry_run": true,
				}),
				Prompt:  "Plan v2 side-state seed/adoption.",
				Confirm: "YES",
			},
			apiRolloutStep{
				Name:    "seed_execute",
				Title:   "Execute side-state seed/adoption",
				Details: "Apply the seed/adopt plan. This is a database mutation but should not make v2 authoritative for the range.",
				Method:  http.MethodPost,
				Target:  "/api/v1/rollout/seed",
				Body: apiRolloutRangeBody(g, map[string]any{
					"execute": true,
					"confirm": "{{seed_confirmation_token}}",
				}),
				Prompt:  "Apply v2 side-state seed/adoption.",
				Confirm: "EXECUTE SEED",
				Danger:  true,
			},
		)
	}
	steps = append(steps,
		apiRolloutStep{
			Name:       "v1_stopped_checkpoint",
			Title:      "Confirm Systems stopped v1 for this range",
			Details:    "Do not activate v2 while v1 may still be checking the same buckets.",
			Prompt:     "Confirm Systems has stopped v1 for this range.",
			Confirm:    fmt.Sprintf("V1 STOPPED %d-%d", g.bucketMin, g.bucketMax),
			ManualOnly: true,
			Danger:     true,
		},
		apiRolloutStep{
			Name:    "activate_dry_run",
			Title:   "Plan v2 bucket activation",
			Details: "Ask the Monitor API to validate and plan activating this bucket range in v2. This should not mutate state.",
			Method:  http.MethodPost,
			Target:  "/api/v1/rollout/activate-buckets",
			Body: apiRolloutRangeBody(g, map[string]any{
				"dry_run": true,
			}),
			Prompt:  "Plan v2 activation for this bucket range.",
			Confirm: "YES",
		},
		apiRolloutStep{
			Name:    "activate_execute",
			Title:   "Activate v2 bucket range",
			Details: "This makes v2 authoritative for this bucket range. Roll back with `jetmon2 api rollout guided --rollback` if post-handoff gates fail.",
			Method:  http.MethodPost,
			Target:  "/api/v1/rollout/activate-buckets",
			Body: apiRolloutRangeBody(g, map[string]any{
				"execute": true,
				"confirm": "{{activate_confirmation_token}}",
			}),
			Prompt:  "Make v2 authoritative for this range.",
			Confirm: fmt.Sprintf("ACTIVATE %d-%d", g.bucketMin, g.bucketMax),
			Danger:  true,
		},
		apiRolloutRequestStep("status", "Check rollout status", "Confirm v2 reports the expected active rollout state.", http.MethodGet, "/api/v1/rollout/status", nil),
		apiRolloutRequestStep("bucket_coverage", "Check bucket coverage", "Confirm the activated range is covered by v2 with no gaps or overlaps.", http.MethodGet, apiRolloutRangeQuery("/api/v1/rollout/bucket-coverage", g), nil),
		apiRolloutRequestStep("activity", "Check recent activity", "Confirm recent checks are visible for the activated range.", http.MethodGet, apiRolloutRangeQuery("/api/v1/rollout/activity-check", g)+"&since="+url.QueryEscape(g.since), nil),
		apiRolloutRequestStep("projection_drift", "Check projection drift", "Confirm legacy projection drift is acceptable while LEGACY_STATUS_PROJECTION_ENABLE is active.", http.MethodGet, apiRolloutRangeQuery("/api/v1/rollout/projection-drift", g), nil),
	)
	if g.includeComparison {
		steps = append(steps, apiRolloutStep{
			Name:    "compare_methods",
			Title:   "Run non-authoritative HEAD/GET comparison",
			Details: "Sample GET/simple_http checks while HEAD/legacy remains the alerting source of truth.",
			Method:  http.MethodPost,
			Target:  "/api/v1/rollout/compare-methods",
			Body: apiRolloutRangeBody(g, map[string]any{
				"from":        apiRolloutModeHeadLegacy,
				"to":          apiRolloutGetSimple,
				"sample_size": g.compareSampleSize,
			}),
			Prompt:  "Run non-authoritative HEAD/GET comparison.",
			Confirm: "YES",
		})
	}
	if g.includePolicyMigration {
		steps = append(steps,
			apiRolloutStep{
				Name:    "stage_get_simple_dry_run",
				Title:   "Plan HEAD/legacy to GET/simple_http stage",
				Details: "Plan the first staged policy cohort; execute the returned plan only after reviewing comparison evidence.",
				Method:  http.MethodPost,
				Target:  "/api/v1/rollout/stage-policy",
				Body: apiRolloutRangeBody(g, map[string]any{
					"method":  "GET",
					"profile": "simple_http",
					"size":    10,
					"dry_run": true,
				}),
				Prompt:  "Plan the first GET/simple_http policy stage.",
				Confirm: "YES",
			},
			apiRolloutStep{
				Name:    "stage_get_full_dry_run",
				Title:   "Plan GET/simple_http to GET/full stage",
				Details: "Plan full-profile migration only; execute later after simple_http cohorts prove stable.",
				Method:  http.MethodPost,
				Target:  "/api/v1/rollout/stage-policy",
				Body: apiRolloutRangeBody(g, map[string]any{
					"method":  "GET",
					"profile": "full",
					"size":    10,
					"dry_run": true,
				}),
				Prompt:  "Plan the first GET/full policy stage.",
				Confirm: "YES",
			},
		)
	}
	return steps
}

func apiRolloutRequestStep(name, title, details, method, target string, body any) apiRolloutStep {
	return apiRolloutStep{
		Name:    name,
		Title:   title,
		Details: details,
		Method:  method,
		Target:  target,
		Body:    body,
	}
}

func apiRolloutRangeBody(g apiRolloutGuidedOptions, extra map[string]any) map[string]any {
	body := map[string]any{
		"bucket_min": g.bucketMin,
		"bucket_max": g.bucketMax,
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func apiRolloutRangeQuery(path string, g apiRolloutGuidedOptions) string {
	values := url.Values{}
	values.Set("bucket_min", strconv.Itoa(g.bucketMin))
	values.Set("bucket_max", strconv.Itoa(g.bucketMax))
	return path + "?" + values.Encode()
}

func apiRolloutPromptText(step apiRolloutStep) string {
	prompt := strings.TrimSpace(step.Prompt)
	confirm := strings.TrimSpace(step.Confirm)
	if confirm == "" {
		return prompt
	}
	if prompt == "" {
		return fmt.Sprintf("Type %s to continue.", confirm)
	}
	return fmt.Sprintf("%s Type %s to continue.", prompt, confirm)
}

func promptAPIRolloutPhrase(reader *bufio.Reader, out io.Writer, prompt, want string) error {
	if reader == nil {
		reader = bufio.NewReader(strings.NewReader(""))
	}
	if out == nil {
		out = io.Discard
	}
	want = strings.TrimSpace(want)
	if want == "" {
		return errors.New("confirmation phrase is not configured")
	}
	fmt.Fprintf(out, "%s\n> ", prompt)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(line) != want {
		return errors.New("confirmation did not match")
	}
	return nil
}

func expandAPIRolloutTarget(target string, state map[string]string) string {
	return expandAPIRolloutString(target, state)
}

func expandAPIRolloutBody(body any, state map[string]string) any {
	switch v := body.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, value := range v {
			out[k] = expandAPIRolloutBody(value, state)
		}
		return out
	case string:
		return expandAPIRolloutString(v, state)
	default:
		return body
	}
}

func expandAPIRolloutString(s string, state map[string]string) string {
	for k, v := range state {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

func apiRolloutBodyHasUnresolvedPlaceholder(body any) bool {
	switch v := body.(type) {
	case nil:
		return false
	case map[string]any:
		for _, value := range v {
			if apiRolloutBodyHasUnresolvedPlaceholder(value) {
				return true
			}
		}
	case []any:
		for _, value := range v {
			if apiRolloutBodyHasUnresolvedPlaceholder(value) {
				return true
			}
		}
	case string:
		return apiRolloutHasUnresolvedPlaceholder(v)
	}
	return false
}

func apiRolloutHasUnresolvedPlaceholder(s string) bool {
	return strings.Contains(s, "{{") || strings.Contains(s, "}}")
}

func apiRolloutConfirmationToken(body json.RawMessage) string {
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		return ""
	}
	for _, key := range []string{"confirmation_token", "confirm_token", "token"} {
		if token, ok := value[key].(string); ok && strings.TrimSpace(token) != "" {
			return strings.TrimSpace(token)
		}
	}
	if confirmation, ok := value["confirmation"].(map[string]any); ok {
		for _, key := range []string{"token", "confirmation_token"} {
			if token, ok := confirmation[key].(string); ok && strings.TrimSpace(token) != "" {
				return strings.TrimSpace(token)
			}
		}
	}
	return ""
}

func writeAPIRolloutStepResult(out io.Writer, name string, body json.RawMessage) {
	if out == nil {
		out = io.Discard
	}
	status := apiRolloutStringField(body, "status")
	if status == "" {
		status = "ok"
	}
	fmt.Fprintf(out, "PASS %s status=%s", name, status)
	if token := apiRolloutConfirmationToken(body); token != "" {
		fmt.Fprintf(out, " confirmation_token=received")
	}
	if summary := apiRolloutStringField(body, "summary"); summary != "" {
		fmt.Fprintf(out, " summary=%q", summary)
	}
	fmt.Fprintln(out)
}

func apiRolloutStringField(body json.RawMessage, field string) string {
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		return ""
	}
	if got, ok := value[field].(string); ok {
		return got
	}
	return ""
}

func apiRolloutEndpointHint(err error) string {
	var httpErr apiWorkflowHTTPError
	if errors.As(err, &httpErr) && strings.Contains(httpErr.Status, "404") {
		return " (rollout API endpoint is not available on this Monitor yet)"
	}
	return ""
}

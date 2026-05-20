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
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	runID                  string
	changeRef              string
	logDir                 string
	dryRun                 bool
	resume                 bool
	rollback               bool
	skipSeed               bool
	includeComparison      bool
	includePolicyMigration bool
	canaryFile             string
	canaries               []any
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

type apiRolloutGuidedState struct {
	Version        int               `json:"version"`
	RunID          string            `json:"run_id,omitempty"`
	BucketMin      int               `json:"bucket_min"`
	BucketMax      int               `json:"bucket_max"`
	Rollback       bool              `json:"rollback"`
	ChangeRef      string            `json:"change_ref,omitempty"`
	State          map[string]string `json:"state,omitempty"`
	CompletedSteps map[string]bool   `json:"completed_steps,omitempty"`
	UpdatedAt      string            `json:"updated_at"`
}

func cmdAPIRollout(args []string) error {
	if len(args) == 0 {
		printAPIRolloutUsage(os.Stderr)
		return errors.New("usage: jetmon2 api rollout <guided> [flags]")
	}
	switch args[0] {
	case "guided":
		return cmdAPIRolloutGuided(args[1:])
	case "capabilities":
		return cmdAPIRolloutCapabilities(args[1:])
	case "preflight", "smoke", "seed", "final-reconcile", "activate-buckets", "release-buckets", "compare-methods", "stage-policy":
		return cmdAPIRolloutPost(args[0], args[1:])
	case "status":
		return cmdAPIRolloutStatus(args[1:])
	case "bucket-coverage", "activity-check", "projection-drift":
		return cmdAPIRolloutGate(args[0], args[1:])
	case "jobs":
		return cmdAPIRolloutJobs(args[1:])
	case "--help", "-h", "help":
		printAPIRolloutUsage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown api rollout subcommand %q", args[0])
	}
}

func printAPIRolloutUsage(w io.Writer) {
	if w == nil {
		w = io.Discard
	}
	fmt.Fprintln(w, "usage: jetmon2 api rollout <guided|capabilities|preflight|smoke|seed|final-reconcile|activate-buckets|release-buckets|status|bucket-coverage|activity-check|projection-drift|compare-methods|stage-policy|jobs> [flags]")
}

func cmdAPIRolloutGuided(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api rollout guided", &opts)
	guided := apiRolloutGuidedOptions{
		bucketMin:         -1,
		bucketMax:         -1,
		sampleSize:        100,
		compareSampleSize: 100,
		since:             "15m",
	}
	fs.IntVar(&guided.bucketMin, "bucket-min", guided.bucketMin, "inclusive bucket range minimum")
	fs.IntVar(&guided.bucketMax, "bucket-max", guided.bucketMax, "inclusive bucket range maximum")
	fs.IntVar(&guided.sampleSize, "sample-size", guided.sampleSize, "read-only smoke sample size (max 1000)")
	fs.IntVar(&guided.compareSampleSize, "compare-sample-size", guided.compareSampleSize, "HEAD/GET comparison sample size (max 1000)")
	fs.StringVar(&guided.since, "since", guided.since, "recent activity window for post-activation gates")
	fs.StringVar(&guided.runID, "run-id", "", "existing rollout run id to use")
	fs.StringVar(&guided.changeRef, "change-ref", "", "change ticket/reference recorded with rollout API actions")
	fs.StringVar(&guided.logDir, "log-dir", "logs/api-rollout", "directory for guided API rollout transcript and resume state")
	fs.BoolVar(&guided.dryRun, "dry-run", false, "print the guided plan without contacting the API")
	fs.BoolVar(&guided.resume, "resume", false, "resume a guided API rollout from --log-dir state")
	fs.BoolVar(&guided.rollback, "rollback", false, "release an activated v2 bucket range back to standby")
	fs.BoolVar(&guided.skipSeed, "skip-seed", false, "skip v2 side-state seed/adopt steps")
	fs.BoolVar(&guided.includeComparison, "include-comparison", false, "run non-authoritative HEAD/GET comparison after activation gates")
	fs.BoolVar(&guided.includePolicyMigration, "include-policy-migration", false, "include staged policy migration dry-run steps after comparison")
	fs.StringVar(&guided.canaryFile, "canary-file", "", "JSON file containing rollout synthetic canaries for preflight and smoke")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api rollout guided [flags]")
	}
	return runAPIRolloutGuided(context.Background(), nil, opts, guided)
}

func cmdAPIRolloutCapabilities(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api rollout capabilities", &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api rollout capabilities [flags]")
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, "/api/v1/rollout/capabilities", nil)
}

type apiRolloutPrimitiveOptions struct {
	bucketMin  int
	bucketMax  int
	runID      string
	changeRef  string
	ownerHost  string
	mode       string
	sampleSize int
	readOnly   bool
	dryRun     bool
	execute    bool
	confirm    string
	since      string
	from       string
	to         string
	method     string
	profile    string
	size       string
	canaryFile string
}

func cmdAPIRolloutPost(command string, args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api rollout "+command, &opts)
	prim := apiRolloutPrimitiveOptions{bucketMin: -1, bucketMax: -1, since: "15m"}
	fs.IntVar(&prim.bucketMin, "bucket-min", prim.bucketMin, "inclusive bucket range minimum")
	fs.IntVar(&prim.bucketMax, "bucket-max", prim.bucketMax, "inclusive bucket range maximum")
	fs.StringVar(&prim.runID, "run-id", "", "rollout run id")
	fs.StringVar(&prim.changeRef, "change-ref", "", "change ticket/reference recorded with this rollout action")
	fs.StringVar(&prim.ownerHost, "owner-host", "", "monitor host that should own activated buckets (default selected API host)")
	fs.StringVar(&prim.mode, "mode", "", "rollout check mode or stage-policy mode")
	fs.IntVar(&prim.sampleSize, "sample-size", 0, "sample size for smoke or comparison operations (default 100, max 1000)")
	fs.BoolVar(&prim.readOnly, "read-only", false, "require read-only behavior")
	fs.BoolVar(&prim.dryRun, "dry-run", false, "plan without mutating state")
	fs.BoolVar(&prim.execute, "execute", false, "execute a previously planned operation")
	fs.StringVar(&prim.confirm, "confirm", "", "confirmation token returned by a dry-run plan")
	fs.StringVar(&prim.from, "from", "", "source check method/profile cohort")
	fs.StringVar(&prim.to, "to", "", "target check method/profile cohort")
	fs.StringVar(&prim.method, "method", "", "target HTTP method for staged policy")
	fs.StringVar(&prim.profile, "profile", "", "target detection profile for staged policy")
	fs.StringVar(&prim.size, "size", "", "cohort size for staged policy")
	fs.StringVar(&prim.canaryFile, "canary-file", "", "JSON file containing rollout synthetic canaries for preflight or smoke")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: jetmon2 api rollout %s [flags]", command)
	}
	if prim.bucketMin < 0 || prim.bucketMax < 0 || prim.bucketMin > prim.bucketMax {
		return errors.New("--bucket-min and --bucket-max are required with min <= max")
	}
	body := map[string]any{
		"bucket_min": prim.bucketMin,
		"bucket_max": prim.bucketMax,
	}
	addNonEmpty(body, "run_id", prim.runID)
	addNonEmpty(body, "change_ref", prim.changeRef)
	addNonEmpty(body, "owner_host", prim.ownerHost)
	addNonEmpty(body, "mode", prim.mode)
	addNonEmpty(body, "confirm", prim.confirm)
	addNonEmpty(body, "from", prim.from)
	addNonEmpty(body, "to", prim.to)
	addNonEmpty(body, "method", prim.method)
	addNonEmpty(body, "profile", prim.profile)
	if prim.sampleSize > 0 {
		body["sample_size"] = prim.sampleSize
	}
	if prim.readOnly {
		body["read_only"] = true
	}
	if prim.dryRun {
		body["dry_run"] = true
	}
	if prim.execute {
		body["execute"] = true
	}
	if prim.size != "" {
		body["size"] = prim.size
	}
	if strings.TrimSpace(prim.canaryFile) != "" {
		if command != "preflight" && command != "smoke" {
			return errors.New("--canary-file is only supported with preflight and smoke")
		}
		canaries, err := loadAPIRolloutCanaries(prim.canaryFile)
		if err != nil {
			return err
		}
		body["canaries"] = canaries
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodPost, "/api/v1/rollout/"+command, payload)
}

func cmdAPIRolloutStatus(args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api rollout status", &opts)
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api rollout status [flags]")
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, "/api/v1/rollout/status", nil)
}

func cmdAPIRolloutGate(command string, args []string) error {
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api rollout "+command, &opts)
	bucketMin := fs.Int("bucket-min", -1, "inclusive bucket range minimum")
	bucketMax := fs.Int("bucket-max", -1, "inclusive bucket range maximum")
	since := fs.String("since", "15m", "recent activity window")
	if err := parseAPIFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: jetmon2 api rollout %s [flags]", command)
	}
	if *bucketMin < 0 || *bucketMax < 0 || *bucketMin > *bucketMax {
		return errors.New("--bucket-min and --bucket-max are required with min <= max")
	}
	values := url.Values{}
	values.Set("bucket_min", strconv.Itoa(*bucketMin))
	values.Set("bucket_max", strconv.Itoa(*bucketMax))
	if command == "activity-check" {
		values.Set("since", *since)
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, "/api/v1/rollout/"+command+"?"+values.Encode(), nil)
}

func cmdAPIRolloutJobs(args []string) error {
	if len(args) < 2 || args[0] != "get" {
		return errors.New("usage: jetmon2 api rollout jobs get <job-id> [flags]")
	}
	opts := defaultAPIOptions()
	fs := newAPIFlagSet("api rollout jobs get", &opts)
	if err := parseAPIFlags(fs, args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: jetmon2 api rollout jobs get <job-id> [flags]")
	}
	return executeAPIRequest(context.Background(), nil, opts, http.MethodGet, "/api/v1/rollout/jobs/"+url.PathEscape(args[1]), nil)
}

func addNonEmpty(body map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		body[key] = value
	}
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
	if strings.TrimSpace(guided.canaryFile) != "" {
		canaries, err := loadAPIRolloutCanaries(guided.canaryFile)
		if err != nil {
			return err
		}
		guided.canaries = canaries
	}
	if strings.TrimSpace(guided.since) == "" {
		return errors.New("since must be non-empty")
	}
	guided.runID = strings.TrimSpace(guided.runID)
	guided.changeRef = strings.TrimSpace(guided.changeRef)
	var stateFile string
	if strings.TrimSpace(guided.logDir) != "" && !guided.dryRun {
		if err := os.MkdirAll(guided.logDir, 0700); err != nil {
			return fmt.Errorf("create rollout log directory: %w", err)
		}
		stateFile = apiRolloutGuidedStatePath(guided)
		if guided.resume {
			saved, err := readAPIRolloutGuidedState(stateFile)
			if err != nil {
				return err
			}
			if err := validateAPIRolloutGuidedState(saved, guided); err != nil {
				return err
			}
			if guided.runID == "" {
				guided.runID = saved.RunID
			}
		}
		logPath := apiRolloutGuidedLogPath(guided)
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("open guided API rollout log: %w", err)
		}
		defer logFile.Close()
		opts.out = io.MultiWriter(opts.out, logFile)
		fmt.Fprintf(opts.out, "INFO api_rollout_log=%s\n", logPath)
		fmt.Fprintf(opts.out, "INFO api_rollout_state=%s\n", stateFile)
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
	if guided.runID != "" {
		fmt.Fprintf(opts.out, "run_id: %s\n", guided.runID)
	}
	if guided.changeRef != "" {
		fmt.Fprintf(opts.out, "change_ref: %s\n", guided.changeRef)
	}
	if strings.TrimSpace(guided.canaryFile) != "" {
		fmt.Fprintf(opts.out, "canary_file: %s\n", guided.canaryFile)
		fmt.Fprintf(opts.out, "canaries: %d\n", len(guided.canaries))
	}
	fmt.Fprintln(opts.out)

	state := map[string]string{}
	if guided.runID != "" {
		state["run_id"] = guided.runID
	}
	if guided.changeRef != "" {
		state["change_ref"] = guided.changeRef
	}
	completed := map[string]bool{}
	if stateFile != "" && guided.resume {
		if saved, err := readAPIRolloutGuidedState(stateFile); err == nil {
			for k, v := range saved.State {
				state[k] = v
			}
			for k, v := range saved.CompletedSteps {
				completed[k] = v
			}
		}
	}
	promptReader := bufio.NewReader(opts.in)
	for i, step := range steps {
		fmt.Fprintf(opts.out, "Step %d/%d: %s\n", i+1, len(steps), step.Title)
		if completed[step.Name] {
			fmt.Fprintf(opts.out, "SKIP %s already completed in resume state\n\n", step.Name)
			continue
		}
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
			completed[step.Name] = true
			if stateFile != "" {
				if err := writeAPIRolloutGuidedState(stateFile, guided, state, completed); err != nil {
					return err
				}
			}
			continue
		}
		target := expandAPIRolloutTarget(step.Target, state)
		body := expandAPIRolloutBody(step.Body, state)
		if !guided.dryRun && (apiRolloutHasUnresolvedPlaceholder(target) || apiRolloutBodyHasUnresolvedPlaceholder(body)) {
			return fmt.Errorf("%s: missing confirmation token from previous dry-run plan", step.Name)
		}
		idempotencyKey := expandAPIRolloutString(step.IDKey, state)
		if !guided.dryRun && apiRolloutHasUnresolvedPlaceholder(idempotencyKey) {
			return fmt.Errorf("%s: missing rollout state for idempotency key", step.Name)
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
		resp, err := apiWorkflowRequestJSON(ctx, client, opts, step.Method, target, body, idempotencyKey)
		if err != nil {
			return fmt.Errorf("%s failed: %w%s", step.Name, err, apiRolloutEndpointHint(err))
		}
		if token := apiRolloutConfirmationToken(resp); token != "" {
			switch step.Name {
			case "seed_dry_run":
				state["seed_confirmation_token"] = token
			case "final_reconcile_dry_run":
				state["final_reconcile_confirmation_token"] = token
			case "activate_dry_run":
				state["activate_confirmation_token"] = token
			case "release_dry_run":
				state["release_confirmation_token"] = token
			}
		}
		if runID := apiRolloutStringField(resp, "run_id"); runID != "" {
			state["run_id"] = runID
			guided.runID = runID
		}
		writeAPIRolloutStepResult(opts.out, step.Name, resp)
		fmt.Fprintln(opts.out)
		completed[step.Name] = true
		if stateFile != "" {
			if err := writeAPIRolloutGuidedState(stateFile, guided, state, completed); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildAPIRolloutGuidedSteps(g apiRolloutGuidedOptions) []apiRolloutStep {
	if g.rollback {
		steps := []apiRolloutStep{
			apiRolloutRequestStep("health", "Check API health", "Confirm the selected Monitor API is reachable before attempting rollback.", http.MethodGet, "/api/v1/health", nil),
			apiRolloutRequestStep("identity", "Check API identity", "Confirm the configured token can identify itself.", http.MethodGet, "/api/v1/me", nil),
			apiRolloutRequestStep("capabilities", "Check rollout API capabilities", "Confirm this Monitor supports the API-driven rollout contract expected by this CLI.", http.MethodGet, "/api/v1/rollout/capabilities", nil),
			{
				Name:    "create_session",
				Title:   "Create or resume rollout session",
				Details: "Create a durable server-side session for audit and resume tracking.",
				Method:  http.MethodPost,
				Target:  "/api/v1/rollout/sessions",
				Body:    apiRolloutSessionBody(g),
			},
			{
				Name:    "release_dry_run",
				Title:   "Plan v2 bucket release",
				Details: "Ask the Monitor API to plan releasing this bucket range back to standby. This should not mutate state.",
				Method:  http.MethodPost,
				Target:  "/api/v1/rollout/release-buckets",
				Body: apiRolloutRangeBody(g, map[string]any{
					"dry_run": true,
				}),
				Prompt:  "Plan releasing this v2 bucket range.",
				Confirm: "YES",
			},
			{
				Name:    "release_execute",
				Title:   "Release v2 bucket range",
				Details: "This returns the range to v2 standby. Systems should restart the matching v1 range only after this succeeds.",
				Method:  http.MethodPost,
				Target:  "/api/v1/rollout/release-buckets",
				Body: apiRolloutRangeBody(g, map[string]any{
					"execute": true,
					"confirm": "{{release_confirmation_token}}",
				}),
				Prompt:  "Release this v2 bucket range.",
				Confirm: fmt.Sprintf("RELEASE %d-%d", g.bucketMin, g.bucketMax),
				Danger:  true,
				IDKey:   "api-rollout:{{run_id}}:release-execute",
			},
			apiRolloutRequestStep("status", "Check rollout status", "Confirm the Monitor reports the range as no longer active in v2.", http.MethodGet, "/api/v1/rollout/status", nil),
		}
		return filterAPIRolloutSessionStep(steps, g)
	}

	steps := []apiRolloutStep{
		apiRolloutRequestStep("health", "Check API health", "Confirm the selected standby Monitor API is reachable.", http.MethodGet, "/api/v1/health", nil),
		apiRolloutRequestStep("identity", "Check API identity", "Confirm the configured token can identify itself before rollout operations.", http.MethodGet, "/api/v1/me", nil),
		apiRolloutRequestStep("capabilities", "Check rollout API capabilities", "Confirm this Monitor supports sessions, confirmation tokens, range locks, and API-controlled rollout mode.", http.MethodGet, "/api/v1/rollout/capabilities", nil),
		{
			Name:    "create_session",
			Title:   "Create or resume rollout session",
			Details: "Create a durable server-side session for audit and resume tracking.",
			Method:  http.MethodPost,
			Target:  "/api/v1/rollout/sessions",
			Body:    apiRolloutSessionBody(g),
		},
		{
			Name:    "preflight",
			Title:   "Run API-controlled preflight",
			Details: "Validate Monitor config, DB connectivity, schema version, API-controlled rollout mode, delivery guards, v2 Veriflier contract/quorum identity, and bucket-control state.",
			Method:  http.MethodPost,
			Target:  "/api/v1/rollout/preflight",
			Body: apiRolloutRangeBodyWithCanaries(g, map[string]any{
				"mode": "api-controlled",
			}),
			Prompt:  "Run rollout preflight.",
			Confirm: "YES",
		},
		{
			Name:    "smoke",
			Title:   "Run read-only HEAD/legacy smoke",
			Details: "Execute sampled HEAD/legacy read-only smoke probes without writing incident state, runtime freshness, check history, WPCOM notifications, or legacy projection rows.",
			Method:  http.MethodPost,
			Target:  "/api/v1/rollout/smoke",
			Body: apiRolloutRangeBodyWithCanaries(g, map[string]any{
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
				IDKey:   "api-rollout:{{run_id}}:seed-execute",
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
			Name:    "final_reconcile_dry_run",
			Title:   "Plan final side-state reconcile",
			Details: "After v1 is stopped, re-check rows added or changed since the first seed/adopt plan before v2 activation.",
			Method:  http.MethodPost,
			Target:  "/api/v1/rollout/final-reconcile",
			Body: apiRolloutRangeBody(g, map[string]any{
				"dry_run": true,
			}),
			Prompt:  "Plan the final side-state reconcile.",
			Confirm: "YES",
		},
		apiRolloutStep{
			Name:    "final_reconcile_execute",
			Title:   "Execute final side-state reconcile",
			Details: "Apply the final seed/adopt pass while v1 is stopped and before v2 becomes authoritative.",
			Method:  http.MethodPost,
			Target:  "/api/v1/rollout/final-reconcile",
			Body: apiRolloutRangeBody(g, map[string]any{
				"execute": true,
				"confirm": "{{final_reconcile_confirmation_token}}",
			}),
			Prompt:  "Apply the final side-state reconcile.",
			Confirm: "EXECUTE RECONCILE",
			Danger:  true,
			IDKey:   "api-rollout:{{run_id}}:final-reconcile-execute",
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
			IDKey:   "api-rollout:{{run_id}}:activate-execute",
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
	return filterAPIRolloutSessionStep(steps, g)
}

func filterAPIRolloutSessionStep(steps []apiRolloutStep, g apiRolloutGuidedOptions) []apiRolloutStep {
	if strings.TrimSpace(g.runID) == "" {
		return steps
	}
	out := steps[:0]
	for _, step := range steps {
		if step.Name == "create_session" {
			continue
		}
		out = append(out, step)
	}
	return out
}

func apiRolloutGuidedStatePath(g apiRolloutGuidedOptions) string {
	name := fmt.Sprintf("api-rollout-%d-%d", g.bucketMin, g.bucketMax)
	if g.rollback {
		name += "-rollback"
	}
	return filepath.Join(g.logDir, name+".state.json")
}

func apiRolloutGuidedLogPath(g apiRolloutGuidedOptions) string {
	ts := time.Now().UTC().Format("20060102T150405Z")
	name := fmt.Sprintf("api-rollout-%d-%d-%s", g.bucketMin, g.bucketMax, ts)
	if g.rollback {
		name += "-rollback"
	}
	return filepath.Join(g.logDir, name+".log")
}

func readAPIRolloutGuidedState(path string) (apiRolloutGuidedState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return apiRolloutGuidedState{}, fmt.Errorf("read guided API rollout state: %w", err)
	}
	var state apiRolloutGuidedState
	if err := json.Unmarshal(raw, &state); err != nil {
		return apiRolloutGuidedState{}, fmt.Errorf("decode guided API rollout state: %w", err)
	}
	if state.Version != 1 {
		return apiRolloutGuidedState{}, fmt.Errorf("guided API rollout state version %d is not supported", state.Version)
	}
	return state, nil
}

func validateAPIRolloutGuidedState(state apiRolloutGuidedState, g apiRolloutGuidedOptions) error {
	if state.BucketMin != g.bucketMin || state.BucketMax != g.bucketMax || state.Rollback != g.rollback {
		return fmt.Errorf("guided API rollout state does not match requested range/path")
	}
	if g.runID != "" && state.RunID != "" && g.runID != state.RunID {
		return fmt.Errorf("guided API rollout state run_id=%q does not match requested run_id=%q", state.RunID, g.runID)
	}
	return nil
}

func writeAPIRolloutGuidedState(path string, g apiRolloutGuidedOptions, state map[string]string, completed map[string]bool) error {
	runID := strings.TrimSpace(state["run_id"])
	if runID == "" {
		runID = g.runID
	}
	payload := apiRolloutGuidedState{
		Version:        1,
		RunID:          runID,
		BucketMin:      g.bucketMin,
		BucketMax:      g.bucketMax,
		Rollback:       g.rollback,
		ChangeRef:      g.changeRef,
		State:          state,
		CompletedSteps: completed,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return fmt.Errorf("write guided API rollout state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace guided API rollout state: %w", err)
	}
	return nil
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
	if g.runID != "" {
		body["run_id"] = g.runID
	} else {
		body["run_id"] = "{{run_id}}"
	}
	if g.changeRef != "" {
		body["change_ref"] = g.changeRef
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func apiRolloutRangeBodyWithCanaries(g apiRolloutGuidedOptions, extra map[string]any) map[string]any {
	body := apiRolloutRangeBody(g, extra)
	if len(g.canaries) > 0 {
		body["canaries"] = g.canaries
	}
	return body
}

func apiRolloutSessionBody(g apiRolloutGuidedOptions) map[string]any {
	body := map[string]any{
		"bucket_min": g.bucketMin,
		"bucket_max": g.bucketMax,
	}
	if g.changeRef != "" {
		body["change_ref"] = g.changeRef
	}
	return body
}

func apiRolloutRangeQuery(path string, g apiRolloutGuidedOptions) string {
	values := url.Values{}
	values.Set("bucket_min", strconv.Itoa(g.bucketMin))
	values.Set("bucket_max", strconv.Itoa(g.bucketMax))
	return path + "?" + values.Encode()
}

func loadAPIRolloutCanaries(path string) ([]any, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rollout canary file: %w", err)
	}
	var canaries []any
	if err := json.Unmarshal(raw, &canaries); err == nil {
		return validateAPIRolloutCanaries(canaries)
	}
	var wrapped struct {
		Canaries []any `json:"canaries"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("decode rollout canary file: %w", err)
	}
	return validateAPIRolloutCanaries(wrapped.Canaries)
}

func validateAPIRolloutCanaries(canaries []any) ([]any, error) {
	if len(canaries) == 0 {
		return nil, errors.New("rollout canary file must contain at least one canary")
	}
	for i, canary := range canaries {
		if _, ok := canary.(map[string]any); !ok {
			return nil, fmt.Errorf("rollout canary %d must be a JSON object", i+1)
		}
	}
	return canaries, nil
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
		if v == "" {
			continue
		}
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

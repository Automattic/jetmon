package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Automattic/jetmon/internal/config"
)

const (
	rolloutAPIVersion          = "2026-05-15"
	rolloutConfirmationTTL     = 15 * time.Minute
	rolloutDefaultPollInterval = 2 * time.Second

	rolloutOpSeed            = "seed"
	rolloutOpFinalReconcile  = "final_reconcile"
	rolloutOpActivateBuckets = "activate_buckets"
	rolloutOpReleaseBuckets  = "release_buckets"
	rolloutOpStagePolicy     = "stage_policy"
)

type rolloutCapabilitiesResponse struct {
	Status       string   `json:"status"`
	APIVersion   string   `json:"api_version"`
	ServerHost   string   `json:"server_host"`
	RolloutMode  string   `json:"rollout_mode"`
	Features     []string `json:"features"`
	Requirements []string `json:"requirements"`
}

type rolloutSessionRequest struct {
	BucketMin int    `json:"bucket_min"`
	BucketMax int    `json:"bucket_max"`
	OwnerHost string `json:"owner_host,omitempty"`
	ChangeRef string `json:"change_ref,omitempty"`
	Metadata  any    `json:"metadata,omitempty"`
}

type rolloutSessionResponse struct {
	Status    string `json:"status"`
	RunID     string `json:"run_id"`
	BucketMin int    `json:"bucket_min"`
	BucketMax int    `json:"bucket_max"`
	OwnerHost string `json:"owner_host"`
	ChangeRef string `json:"change_ref,omitempty"`
}

type rolloutRangeRequest struct {
	RunID       string `json:"run_id,omitempty"`
	BucketMin   int    `json:"bucket_min"`
	BucketMax   int    `json:"bucket_max"`
	OwnerHost   string `json:"owner_host,omitempty"`
	ChangeRef   string `json:"change_ref,omitempty"`
	DryRun      bool   `json:"dry_run,omitempty"`
	Execute     bool   `json:"execute,omitempty"`
	Confirm     string `json:"confirm,omitempty"`
	Mode        string `json:"mode,omitempty"`
	SampleSize  int    `json:"sample_size,omitempty"`
	ReadOnly    bool   `json:"read_only,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	Method      string `json:"method,omitempty"`
	Profile     string `json:"profile,omitempty"`
	Size        any    `json:"size,omitempty"`
	Description string `json:"description,omitempty"`
}

type rolloutJobResponse struct {
	JobID        string          `json:"job_id"`
	RunID        string          `json:"run_id,omitempty"`
	Operation    string          `json:"operation"`
	Status       string          `json:"status"`
	Progress     int             `json:"progress"`
	Summary      string          `json:"summary,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	CreatedAt    string          `json:"created_at,omitempty"`
	UpdatedAt    string          `json:"updated_at,omitempty"`
}

type rolloutOperationResponse struct {
	Status            string             `json:"status"`
	Operation         string             `json:"operation"`
	RunID             string             `json:"run_id,omitempty"`
	BucketMin         int                `json:"bucket_min,omitempty"`
	BucketMax         int                `json:"bucket_max,omitempty"`
	OwnerHost         string             `json:"owner_host,omitempty"`
	ChangeRef         string             `json:"change_ref,omitempty"`
	ConfirmationToken string             `json:"confirmation_token,omitempty"`
	TokenExpiresAt    string             `json:"token_expires_at,omitempty"`
	Job               rolloutJobResponse `json:"job"`
	JobID             string             `json:"job_id"`
	StatusURL         string             `json:"status_url"`
	Summary           string             `json:"summary,omitempty"`
	Warnings          []string           `json:"warnings,omitempty"`
	Blockers          []string           `json:"blockers,omitempty"`
	NextAction        string             `json:"next_action,omitempty"`
	Result            any                `json:"result,omitempty"`
}

type rolloutStatusResponse struct {
	Status       string                   `json:"status"`
	ServerHost   string                   `json:"server_host"`
	RolloutMode  string                   `json:"rollout_mode"`
	ActiveRanges []rolloutActiveRangeJSON `json:"active_ranges"`
	OpenSessions int                      `json:"open_sessions"`
}

type rolloutActiveRangeJSON struct {
	RunID       string `json:"run_id"`
	BucketMin   int    `json:"bucket_min"`
	BucketMax   int    `json:"bucket_max"`
	OwnerHost   string `json:"owner_host"`
	ChangeRef   string `json:"change_ref,omitempty"`
	ActivatedAt string `json:"activated_at"`
}

type rolloutGateResponse struct {
	Status string `json:"status"`
}

func (s *Server) handleRolloutCapabilities(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	mode := config.RolloutModeActive
	if cfg != nil {
		mode = cfg.RolloutMode
	}
	writeJSON(w, http.StatusOK, rolloutCapabilitiesResponse{
		Status:      "ok",
		APIVersion:  rolloutAPIVersion,
		ServerHost:  s.hostname,
		RolloutMode: mode,
		Features: []string{
			"sessions",
			"synchronous_jobs",
			"confirmation_tokens",
			"bucket_range_locks",
			"api_controlled_monitor_mode",
			"preflight",
			"read_only_smoke_plan",
			"seed_adopt",
			"final_reconcile",
			"activate_release",
			"post_handoff_gates",
			"method_comparison",
			"policy_stage_planning",
		},
		Requirements: []string{
			"admin token for rollout mutations",
			"ROLLOUT_MODE=api-controlled for API activation",
			"dry-run confirmation token for execute operations",
			"operator confirmation that matching v1 buckets are stopped before activation",
		},
	})
}

func (s *Server) handleCreateRolloutSession(w http.ResponseWriter, r *http.Request) {
	var body rolloutSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be valid JSON: "+err.Error())
		return
	}
	if err := validateRolloutRange(body.BucketMin, body.BucketMax); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_bucket_range", err.Error())
		return
	}
	owner := rolloutOwnerHost(body.OwnerHost, s.hostname)
	operator := rolloutOperator(r)
	runID := "rol_" + newRequestID()
	metadata, err := json.Marshal(body.Metadata)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_metadata", "metadata must be JSON-serializable")
		return
	}
	if string(metadata) == "null" {
		metadata = nil
	}
	if _, err := s.db.ExecContext(r.Context(), `
		INSERT INTO jetmon_rollout_sessions
			(run_id, bucket_min, bucket_max, owner_host, change_ref, operator, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		runID, body.BucketMin, body.BucketMax, owner, strings.TrimSpace(body.ChangeRef), operator, nullableRawJSON(metadata),
	); err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "create rollout session failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rolloutSessionResponse{
		Status:    "open",
		RunID:     runID,
		BucketMin: body.BucketMin,
		BucketMax: body.BucketMax,
		OwnerHost: owner,
		ChangeRef: strings.TrimSpace(body.ChangeRef),
	})
}

func (s *Server) handleGetRolloutJob(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("job_id"))
	if jobID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_job_id", "job_id is required")
		return
	}
	job, err := s.readRolloutJob(r.Context(), jobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "job_not_found", "rollout job not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error", "read rollout job failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleRolloutPreflight(w http.ResponseWriter, r *http.Request) {
	body, ok := s.decodeRolloutRangeBody(w, r)
	if !ok {
		return
	}
	cfg := config.Get()
	var blockers []string
	var warnings []string
	if cfg == nil {
		blockers = append(blockers, "runtime config is not loaded")
	} else {
		if cfg.RolloutMode != config.RolloutModeAPIControlled {
			blockers = append(blockers, "ROLLOUT_MODE must be api-controlled before API-driven container rollout")
		}
		if cfg.APIPort <= 0 {
			blockers = append(blockers, "API_PORT is disabled")
		}
		if cfg.WPCOMNotifyEnable {
			warnings = append(warnings, "WPCOM_NOTIFY_ENABLE is true; confirm this is intended for the rollout stage")
		}
		if cfg.RolloutMode == config.RolloutModeActive && cfg.DeliveryOwnerHost == "" && cfg.APIPort > 0 {
			warnings = append(warnings, "DELIVERY_OWNER_HOST is unset; embedded delivery workers may be eligible")
		}
	}
	if err := s.db.PingContext(r.Context()); err != nil {
		blockers = append(blockers, "database ping failed: "+err.Error())
	}
	maxMigration, err := s.maxSchemaMigration(r.Context())
	if err != nil {
		blockers = append(blockers, "schema migration lookup failed: "+err.Error())
	} else if maxMigration < 46 {
		blockers = append(blockers, fmt.Sprintf("schema migration %d is older than required rollout migration 46", maxMigration))
	}
	summary := "preflight passed"
	status := "ok"
	if len(blockers) > 0 {
		summary = "preflight blocked"
		status = "blocked"
	}
	resp := s.rolloutOperation(r, rolloutOpName("preflight"), body, status, summary, map[string]any{
		"schema_migration": maxMigration,
		"rollout_mode":     rolloutModeString(cfg),
	}, warnings, blockers)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRolloutSmoke(w http.ResponseWriter, r *http.Request) {
	body, ok := s.decodeRolloutRangeBody(w, r)
	if !ok {
		return
	}
	if !body.ReadOnly {
		writeError(w, r, http.StatusUnprocessableEntity, "read_only_required", "rollout smoke must set read_only=true")
		return
	}
	active, err := s.countActiveSites(r.Context(), body.BucketMin, body.BucketMax)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "count active sites failed: "+err.Error())
		return
	}
	sample := body.SampleSize
	if sample <= 0 {
		sample = 1000
	}
	if sample > active {
		sample = active
	}
	resp := s.rolloutOperation(r, "smoke", body, "ok", "read-only smoke plan completed", map[string]any{
		"mode":         emptyDefault(body.Mode, "head-legacy"),
		"active_sites": active,
		"sample_size":  sample,
		"read_only":    true,
	}, nil, nil)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRolloutSeed(w http.ResponseWriter, r *http.Request) {
	s.handleRolloutSeedLike(w, r, rolloutOpSeed)
}

func (s *Server) handleRolloutFinalReconcile(w http.ResponseWriter, r *http.Request) {
	s.handleRolloutSeedLike(w, r, rolloutOpFinalReconcile)
}

func (s *Server) handleRolloutSeedLike(w http.ResponseWriter, r *http.Request, operation string) {
	body, ok := s.decodeRolloutRangeBody(w, r)
	if !ok {
		return
	}
	counts, err := s.rolloutSeedCounts(r.Context(), body.BucketMin, body.BucketMax)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", operation+" plan failed: "+err.Error())
		return
	}
	if body.DryRun || !body.Execute {
		resp := s.rolloutPlanResponse(r, operation, body, "seed/adopt plan is ready", counts)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !s.consumeRolloutConfirmation(w, r, operation, body) {
		return
	}
	if err := s.executeRolloutSeed(r.Context(), body.BucketMin, body.BucketMax); err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", operation+" execute failed: "+err.Error())
		return
	}
	after, err := s.rolloutSeedCounts(r.Context(), body.BucketMin, body.BucketMax)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", operation+" readback failed: "+err.Error())
		return
	}
	resp := s.rolloutOperation(r, operation, body, "ok", "seed/adopt executed", after, nil, nil)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRolloutActivateBuckets(w http.ResponseWriter, r *http.Request) {
	body, ok := s.decodeRolloutRangeBody(w, r)
	if !ok {
		return
	}
	cfg := config.Get()
	if cfg == nil || cfg.RolloutMode != config.RolloutModeAPIControlled {
		blocker := "ROLLOUT_MODE must be api-controlled before bucket activation"
		if body.DryRun || !body.Execute {
			resp := s.rolloutPlanResponseWithStatus(r, rolloutOpActivateBuckets, body, "blocked", "activation blocked by monitor rollout mode", map[string]any{
				"rollout_mode": rolloutModeString(cfg),
			}, []string{blocker})
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeError(w, r, http.StatusUnprocessableEntity, "rollout_mode_blocked", blocker)
		return
	}
	overlaps, err := s.countRolloutBucketOverlaps(r.Context(), body.BucketMin, body.BucketMax)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "activation overlap check failed: "+err.Error())
		return
	}
	ownerOutside, err := s.countRolloutOwnerBucketsOutsideRange(r.Context(), body.OwnerHost, body.BucketMin, body.BucketMax)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "activation owner lock check failed: "+err.Error())
		return
	}
	result := map[string]any{"overlapping_active_buckets": overlaps, "owner_active_buckets_outside_range": ownerOutside}
	if body.DryRun || !body.Execute {
		var blockers []string
		status := "ok"
		summary := "activation plan is ready"
		if overlaps > 0 {
			status = "blocked"
			summary = "activation has overlapping active buckets"
			blockers = append(blockers, "requested bucket range overlaps an active v2 rollout lock")
		}
		if ownerOutside > 0 {
			status = "blocked"
			summary = "activation would create non-contiguous owner locks"
			blockers = append(blockers, "owner host already has active rollout buckets outside the requested range")
		}
		resp := s.rolloutPlanResponseWithStatus(r, rolloutOpActivateBuckets, body, status, summary, result, blockers)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !s.consumeRolloutConfirmation(w, r, rolloutOpActivateBuckets, body) {
		return
	}
	if ownerOutside > 0 {
		writeError(w, r, http.StatusConflict, "owner_range_conflict", "owner host already has active rollout buckets outside the requested range")
		return
	}
	lockID, err := s.activateRolloutBuckets(r.Context(), body)
	if err != nil {
		if isDuplicateKey(err) {
			writeError(w, r, http.StatusConflict, "bucket_range_conflict", "bucket range overlaps an active rollout lock")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error", "activate bucket range failed: "+err.Error())
		return
	}
	result["range_lock_id"] = lockID
	resp := s.rolloutOperation(r, rolloutOpActivateBuckets, body, "ok", "bucket range activated", result, nil, nil)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRolloutReleaseBuckets(w http.ResponseWriter, r *http.Request) {
	body, ok := s.decodeRolloutRangeBody(w, r)
	if !ok {
		return
	}
	active, err := s.countRolloutBucketOverlaps(r.Context(), body.BucketMin, body.BucketMax)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "release lookup failed: "+err.Error())
		return
	}
	result := map[string]any{"active_buckets_in_range": active}
	if body.DryRun || !body.Execute {
		resp := s.rolloutPlanResponse(r, rolloutOpReleaseBuckets, body, "release plan is ready", result)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !s.consumeRolloutConfirmation(w, r, rolloutOpReleaseBuckets, body) {
		return
	}
	released, err := s.releaseRolloutBuckets(r.Context(), body)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "release bucket range failed: "+err.Error())
		return
	}
	result["released_buckets"] = released
	resp := s.rolloutOperation(r, rolloutOpReleaseBuckets, body, "ok", "bucket range released", result, nil, nil)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRolloutStatus(w http.ResponseWriter, r *http.Request) {
	active, err := s.listRolloutActiveRanges(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "list active rollout ranges failed: "+err.Error())
		return
	}
	openSessions, err := s.countOpenRolloutSessions(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "count open rollout sessions failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rolloutStatusResponse{
		Status:       "ok",
		ServerHost:   s.hostname,
		RolloutMode:  rolloutModeString(config.Get()),
		ActiveRanges: active,
		OpenSessions: openSessions,
	})
}

func (s *Server) handleRolloutBucketCoverage(w http.ResponseWriter, r *http.Request) {
	min, max, ok := parseRolloutRangeQuery(w, r)
	if !ok {
		return
	}
	active, err := s.countRolloutBucketOverlaps(r.Context(), min, max)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "bucket coverage lookup failed: "+err.Error())
		return
	}
	expected := max - min + 1
	status := "ok"
	var blockers []string
	if active != expected {
		status = "blocked"
		blockers = append(blockers, fmt.Sprintf("active bucket lock count %d does not match expected %d", active, expected))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         status,
		"bucket_min":     min,
		"bucket_max":     max,
		"expected_count": expected,
		"active_count":   active,
		"blockers":       blockers,
	})
}

func (s *Server) handleRolloutActivityCheck(w http.ResponseWriter, r *http.Request) {
	min, max, ok := parseRolloutRangeQuery(w, r)
	if !ok {
		return
	}
	cutoff, err := parseSinceCutoff(r.URL.Query().Get("since"), 15*time.Minute)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_since", err.Error())
		return
	}
	active, err := s.countActiveSites(r.Context(), min, max)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "count active sites failed: "+err.Error())
		return
	}
	recent, err := s.countRecentlyCheckedSites(r.Context(), min, max, cutoff)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "count recent checks failed: "+err.Error())
		return
	}
	status := "ok"
	if active > 0 && recent == 0 {
		status = "blocked"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":               status,
		"bucket_min":           min,
		"bucket_max":           max,
		"active_sites":         active,
		"recently_checked":     recent,
		"cutoff":               cutoff.UTC().Format(time.RFC3339),
		"freshness_percentage": percentage(recent, active),
	})
}

func (s *Server) handleRolloutProjectionDrift(w http.ResponseWriter, r *http.Request) {
	min, max, ok := parseRolloutRangeQuery(w, r)
	if !ok {
		return
	}
	drift, err := s.countProjectionDrift(r.Context(), min, max)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "count projection drift failed: "+err.Error())
		return
	}
	status := "ok"
	if drift > 0 {
		status = "blocked"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           status,
		"bucket_min":       min,
		"bucket_max":       max,
		"projection_drift": drift,
	})
}

func (s *Server) handleRolloutCompareMethods(w http.ResponseWriter, r *http.Request) {
	body, ok := s.decodeRolloutRangeBody(w, r)
	if !ok {
		return
	}
	sample := body.SampleSize
	if sample <= 0 {
		sample = 10000
	}
	resp := s.rolloutOperation(r, "compare_methods", body, "ok", "method comparison job recorded", map[string]any{
		"from":        emptyDefault(body.From, "head-legacy"),
		"to":          emptyDefault(body.To, "get-simple"),
		"sample_size": sample,
	}, nil, nil)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRolloutStagePolicy(w http.ResponseWriter, r *http.Request) {
	body, ok := s.decodeRolloutRangeBody(w, r)
	if !ok {
		return
	}
	result := map[string]any{
		"method":  strings.ToUpper(strings.TrimSpace(body.Method)),
		"profile": strings.TrimSpace(body.Profile),
		"size":    body.Size,
	}
	if body.DryRun || !body.Execute {
		resp := s.rolloutPlanResponse(r, rolloutOpStagePolicy, body, "policy stage plan is ready", result)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !s.consumeRolloutConfirmation(w, r, rolloutOpStagePolicy, body) {
		return
	}
	resp := s.rolloutOperation(r, rolloutOpStagePolicy, body, "ok", "policy stage recorded", result, nil, nil)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) decodeRolloutRangeBody(w http.ResponseWriter, r *http.Request) (rolloutRangeRequest, bool) {
	var body rolloutRangeRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body", "request body must be valid JSON: "+err.Error())
		return body, false
	}
	if err := validateRolloutRange(body.BucketMin, body.BucketMax); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_bucket_range", err.Error())
		return body, false
	}
	body.OwnerHost = rolloutOwnerHost(body.OwnerHost, s.hostname)
	body.ChangeRef = strings.TrimSpace(body.ChangeRef)
	body.RunID = strings.TrimSpace(body.RunID)
	return body, true
}

func validateRolloutRange(min, max int) error {
	if min < 0 || max < 0 || max < min {
		return fmt.Errorf("bucket_min and bucket_max must be non-negative with min <= max")
	}
	cfg := config.Get()
	if cfg != nil && max >= cfg.BucketTotal {
		return fmt.Errorf("bucket_max must be < BUCKET_TOTAL (%d)", cfg.BucketTotal)
	}
	return nil
}

func parseRolloutRangeQuery(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	min, err := strconv.Atoi(r.URL.Query().Get("bucket_min"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_bucket_min", "bucket_min query parameter is required")
		return 0, 0, false
	}
	max, err := strconv.Atoi(r.URL.Query().Get("bucket_max"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_bucket_max", "bucket_max query parameter is required")
		return 0, 0, false
	}
	if err := validateRolloutRange(min, max); err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_bucket_range", err.Error())
		return 0, 0, false
	}
	return min, max, true
}

func (s *Server) rolloutPlanResponse(r *http.Request, operation string, body rolloutRangeRequest, summary string, result any) rolloutOperationResponse {
	return s.rolloutPlanResponseWithStatus(r, operation, body, "ok", summary, result, nil)
}

func (s *Server) rolloutPlanResponseWithStatus(r *http.Request, operation string, body rolloutRangeRequest, status, summary string, result any, blockers []string) rolloutOperationResponse {
	if status == "blocked" || len(blockers) > 0 {
		return s.rolloutOperation(r, operation, body, "blocked", summary, result, nil, blockers)
	}
	token, expiresAt, err := s.createRolloutConfirmation(r.Context(), operation, body, rolloutOperator(r))
	if err != nil {
		return s.rolloutOperation(r, operation, body, "blocked", "confirmation token creation failed", result, nil, []string{err.Error()})
	}
	resp := s.rolloutOperation(r, operation, body, status, summary, result, nil, blockers)
	resp.ConfirmationToken = token
	resp.TokenExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	return resp
}

func (s *Server) rolloutOperation(r *http.Request, operation string, body rolloutRangeRequest, status, summary string, result any, warnings, blockers []string) rolloutOperationResponse {
	if status == "" {
		status = "ok"
	}
	resultJSON, _ := json.Marshal(result)
	jobStatus := "completed"
	progress := 100
	if status == "blocked" {
		jobStatus = "blocked"
		progress = 0
	}
	jobID := "rjob_" + newRequestID()
	job := rolloutJobResponse{
		JobID:     jobID,
		RunID:     body.RunID,
		Operation: operation,
		Status:    jobStatus,
		Progress:  progress,
		Summary:   summary,
		Result:    resultJSON,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.insertRolloutJob(r.Context(), job); err != nil {
		blockers = append(blockers, "record rollout job failed: "+err.Error())
		status = "blocked"
		job.Status = "blocked"
		job.Progress = 0
		job.ErrorCode = "job_record_failed"
		job.ErrorMessage = err.Error()
	}
	next := "Review the result and continue to the next rollout gate."
	if len(blockers) > 0 {
		next = "Stop and resolve blockers before continuing."
	}
	return rolloutOperationResponse{
		Status:     status,
		Operation:  operation,
		RunID:      body.RunID,
		BucketMin:  body.BucketMin,
		BucketMax:  body.BucketMax,
		OwnerHost:  body.OwnerHost,
		ChangeRef:  body.ChangeRef,
		Job:        job,
		JobID:      jobID,
		StatusURL:  "/api/v1/rollout/jobs/" + jobID,
		Summary:    summary,
		Warnings:   warnings,
		Blockers:   blockers,
		NextAction: next,
		Result:     result,
	}
}

func (s *Server) createRolloutConfirmation(ctx context.Context, operation string, body rolloutRangeRequest, operator string) (string, time.Time, error) {
	token, err := randomRolloutToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(rolloutConfirmationTTL)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO jetmon_rollout_confirmation_tokens
			(token_hash, run_id, operation, request_hash, bucket_min, bucket_max, operator, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sha256Hex(token), body.RunID, operation, rolloutRequestHash(operation, body), body.BucketMin, body.BucketMax, operator, expiresAt,
	)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func (s *Server) consumeRolloutConfirmation(w http.ResponseWriter, r *http.Request, operation string, body rolloutRangeRequest) bool {
	confirm := strings.TrimSpace(body.Confirm)
	if confirm == "" {
		writeError(w, r, http.StatusUnprocessableEntity, "missing_confirmation", "confirm token is required for execute")
		return false
	}
	res, err := s.db.ExecContext(r.Context(), `
		UPDATE jetmon_rollout_confirmation_tokens
		   SET used_at = CURRENT_TIMESTAMP
		 WHERE token_hash = ?
		   AND run_id = ?
		   AND operation = ?
		   AND request_hash = ?
		   AND bucket_min = ?
		   AND bucket_max = ?
		   AND operator = ?
		   AND used_at IS NULL
		   AND expires_at > CURRENT_TIMESTAMP`,
		sha256Hex(confirm), body.RunID, operation, rolloutRequestHash(operation, body), body.BucketMin, body.BucketMax, rolloutOperator(r),
	)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "confirmation token validation failed: "+err.Error())
		return false
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_confirmation", "confirmation token is invalid, expired, used, or does not match this request")
		return false
	}
	return true
}

func rolloutRequestHash(operation string, body rolloutRangeRequest) string {
	canonical := map[string]any{
		"operation":  operation,
		"run_id":     body.RunID,
		"bucket_min": body.BucketMin,
		"bucket_max": body.BucketMax,
		"owner_host": body.OwnerHost,
		"change_ref": body.ChangeRef,
		"mode":       body.Mode,
		"from":       body.From,
		"to":         body.To,
		"method":     strings.ToUpper(strings.TrimSpace(body.Method)),
		"profile":    strings.TrimSpace(body.Profile),
		"size":       body.Size,
	}
	raw, _ := json.Marshal(canonical)
	return sha256Hex(string(raw))
}

func (s *Server) rolloutSeedCounts(ctx context.Context, min, max int) (map[string]any, error) {
	active, err := s.countActiveSites(ctx, min, max)
	if err != nil {
		return nil, err
	}
	var missingTargets int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites s
		  LEFT JOIN jetmon_check_targets t ON t.source_site_id = s.jetpack_monitor_site_id
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN ? AND ?
		   AND t.source_site_id IS NULL`,
		min, max,
	).Scan(&missingTargets); err != nil {
		return nil, err
	}
	var missingRuntime int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites s
		  LEFT JOIN jetmon_site_runtime r ON r.blog_id = s.blog_id
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN ? AND ?
		   AND r.blog_id IS NULL`,
		min, max,
	).Scan(&missingRuntime); err != nil {
		return nil, err
	}
	var nonRunning int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites
		 WHERE monitor_active = 1
		   AND bucket_no BETWEEN ? AND ?
		   AND site_status <> 1`,
		min, max,
	).Scan(&nonRunning); err != nil {
		return nil, err
	}
	return map[string]any{
		"active_sites":            active,
		"missing_check_targets":   missingTargets,
		"missing_runtime_rows":    missingRuntime,
		"non_running_projections": nonRunning,
	}, nil
}

func (s *Server) executeRolloutSeed(ctx context.Context, min, max int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO jetmon_check_targets
			(blog_id, source_site_id, bucket_no, monitor_url, monitor_active, check_interval_sec, phase_slot_sec, config_hash)
		SELECT s.blog_id,
		       s.jetpack_monitor_site_id,
		       s.bucket_no,
		       s.monitor_url,
		       s.monitor_active,
		       GREATEST(s.check_interval, 1) * 60,
		       MOD(s.jetpack_monitor_site_id, GREATEST(s.check_interval, 1) * 60),
		       SHA2(CONCAT_WS('|', s.blog_id, s.jetpack_monitor_site_id, s.monitor_url, s.monitor_active, s.check_interval), 256)
		  FROM jetpack_monitor_sites s
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN ? AND ?
		ON DUPLICATE KEY UPDATE
		       blog_id = VALUES(blog_id),
		       bucket_no = VALUES(bucket_no),
		       monitor_url = VALUES(monitor_url),
		       monitor_active = VALUES(monitor_active),
		       check_interval_sec = VALUES(check_interval_sec),
		       phase_slot_sec = VALUES(phase_slot_sec),
		       config_hash = VALUES(config_hash),
		       last_config_sync_at = CURRENT_TIMESTAMP(3)`,
		min, max,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO jetmon_site_runtime (blog_id)
		SELECT DISTINCT blog_id
		  FROM jetpack_monitor_sites
		 WHERE monitor_active = 1
		   AND bucket_no BETWEEN ? AND ?`,
		min, max,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) activateRolloutBuckets(ctx context.Context, body rolloutRangeRequest) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO jetmon_rollout_range_locks
			(run_id, bucket_min, bucket_max, owner_host, change_ref)
		VALUES (?, ?, ?, ?, ?)`,
		body.RunID, body.BucketMin, body.BucketMax, body.OwnerHost, body.ChangeRef,
	)
	if err != nil {
		return 0, err
	}
	lockID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for bucket := body.BucketMin; bucket <= body.BucketMax; bucket++ {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO jetmon_rollout_bucket_locks
				(bucket_no, run_id, range_lock_id, owner_host)
			VALUES (?, ?, ?, ?)`,
			bucket, body.RunID, lockID, body.OwnerHost,
		); err != nil {
			return 0, err
		}
	}
	return lockID, tx.Commit()
}

func (s *Server) releaseRolloutBuckets(ctx context.Context, body rolloutRangeRequest) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		DELETE FROM jetmon_rollout_bucket_locks
		 WHERE bucket_no BETWEEN ? AND ?
		   AND (? = '' OR run_id = ?)`,
		body.BucketMin, body.BucketMax, body.RunID, body.RunID,
	)
	if err != nil {
		return 0, err
	}
	released, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, `
		UPDATE jetmon_rollout_range_locks
		   SET status = 'released', released_at = CURRENT_TIMESTAMP
		 WHERE status = 'active'
		   AND bucket_min <= ?
		   AND bucket_max >= ?
		   AND (? = '' OR run_id = ?)`,
		body.BucketMax, body.BucketMin, body.RunID, body.RunID,
	); err != nil {
		return 0, err
	}
	return released, tx.Commit()
}

func (s *Server) countRolloutBucketOverlaps(ctx context.Context, min, max int) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetmon_rollout_bucket_locks
		 WHERE bucket_no BETWEEN ? AND ?`,
		min, max,
	).Scan(&count)
	return count, err
}

func (s *Server) countRolloutOwnerBucketsOutsideRange(ctx context.Context, owner string, min, max int) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetmon_rollout_bucket_locks
		 WHERE owner_host = ?
		   AND (bucket_no < ? OR bucket_no > ?)`,
		owner, min, max,
	).Scan(&count)
	return count, err
}

func (s *Server) countActiveSites(ctx context.Context, min, max int) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites
		 WHERE monitor_active = 1
		   AND bucket_no BETWEEN ? AND ?`,
		min, max,
	).Scan(&count)
	return count, err
}

func (s *Server) countRecentlyCheckedSites(ctx context.Context, min, max int, cutoff time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites s
		  JOIN jetmon_site_runtime r ON r.blog_id = s.blog_id
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN ? AND ?
		   AND r.last_checked_at >= ?`,
		min, max, cutoff.UTC(),
	).Scan(&count)
	return count, err
}

func (s *Server) countProjectionDrift(ctx context.Context, min, max int) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM (
			SELECT s.jetpack_monitor_site_id,
			       s.blog_id,
			       s.site_status,
			       CASE
			         WHEN SUM(CASE WHEN e.state = 'Down' THEN 1 ELSE 0 END) > 0 THEN 2
			         WHEN SUM(CASE WHEN e.state = 'Seems Down' THEN 1 ELSE 0 END) > 0 THEN 0
			         ELSE 1
			       END AS expected_status
			  FROM jetpack_monitor_sites s
			  LEFT JOIN jetmon_events e
			    ON e.blog_id = s.blog_id
			   AND (e.endpoint_id = s.jetpack_monitor_site_id OR e.endpoint_id IS NULL)
			   AND e.check_type = 'http'
			   AND e.ended_at IS NULL
			 WHERE s.monitor_active = 1
			   AND s.bucket_no BETWEEN ? AND ?
			 GROUP BY s.jetpack_monitor_site_id, s.blog_id, s.site_status
		  ) drift
		 WHERE drift.site_status <> drift.expected_status`,
		min, max,
	).Scan(&count)
	return count, err
}

func (s *Server) maxSchemaMigration(ctx context.Context) (int, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(id) FROM jetmon_schema_migrations`).Scan(&id)
	if err != nil {
		return 0, err
	}
	if !id.Valid {
		return 0, nil
	}
	return int(id.Int64), nil
}

func (s *Server) countOpenRolloutSessions(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jetmon_rollout_sessions WHERE status = 'open'`).Scan(&count)
	return count, err
}

func (s *Server) listRolloutActiveRanges(ctx context.Context) ([]rolloutActiveRangeJSON, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, bucket_min, bucket_max, owner_host, change_ref, activated_at
		  FROM jetmon_rollout_range_locks
		 WHERE status = 'active'
		 ORDER BY bucket_min, bucket_max`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rolloutActiveRangeJSON
	for rows.Next() {
		var row rolloutActiveRangeJSON
		var activated time.Time
		if err := rows.Scan(&row.RunID, &row.BucketMin, &row.BucketMax, &row.OwnerHost, &row.ChangeRef, &activated); err != nil {
			return nil, err
		}
		row.ActivatedAt = activated.UTC().Format(time.RFC3339)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Server) insertRolloutJob(ctx context.Context, job rolloutJobResponse) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO jetmon_rollout_jobs
			(job_id, run_id, operation, status, progress, summary, result, error_code, error_message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.JobID, job.RunID, job.Operation, job.Status, job.Progress, job.Summary, nullableRawJSON(job.Result), job.ErrorCode, job.ErrorMessage,
	)
	return err
}

func (s *Server) readRolloutJob(ctx context.Context, jobID string) (rolloutJobResponse, error) {
	var job rolloutJobResponse
	var result sql.NullString
	var created, updated time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT job_id, run_id, operation, status, progress, summary, result, error_code, error_message, created_at, updated_at
		  FROM jetmon_rollout_jobs
		 WHERE job_id = ?`,
		jobID,
	).Scan(&job.JobID, &job.RunID, &job.Operation, &job.Status, &job.Progress, &job.Summary, &result, &job.ErrorCode, &job.ErrorMessage, &created, &updated)
	if err != nil {
		return rolloutJobResponse{}, err
	}
	if result.Valid {
		job.Result = json.RawMessage(result.String)
	}
	job.CreatedAt = created.UTC().Format(time.RFC3339)
	job.UpdatedAt = updated.UTC().Format(time.RFC3339)
	return job, nil
}

func randomRolloutToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "jmr_" + hex.EncodeToString(b[:]), nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func rolloutOperator(r *http.Request) string {
	if key := keyFromRequest(r); key != nil {
		return key.ConsumerName
	}
	return ""
}

func rolloutOwnerHost(owner, fallback string) string {
	owner = strings.TrimSpace(owner)
	if owner != "" {
		return owner
	}
	return fallback
}

func rolloutModeString(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.RolloutMode
}

func nullableRawJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func emptyDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func rolloutOpName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

func parseSinceCutoff(raw string, def time.Duration) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().UTC().Add(-def), nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return time.Now().UTC().Add(-d), nil
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("since must be a duration like 15m or an RFC3339 timestamp")
}

func percentage(part, total int) float64 {
	if total <= 0 {
		return 100
	}
	return float64(part) * 100 / float64(total)
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

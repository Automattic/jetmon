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
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/checkmode"
	"github.com/Automattic/jetmon/internal/config"
	jetdb "github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/eventstore"
	"github.com/Automattic/jetmon/internal/veriflier"
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

	rolloutDefaultProbeSample     = 100
	rolloutMaxSynchronousSample   = 1000
	rolloutDefaultProbeConcurrent = 16
)

type rolloutCapabilitiesResponse struct {
	Status                      string   `json:"status"`
	APIVersion                  string   `json:"api_version"`
	ServerHost                  string   `json:"server_host"`
	RolloutMode                 string   `json:"rollout_mode"`
	ConfirmationTokenTTLSeconds int      `json:"confirmation_token_ttl_seconds"`
	Features                    []string `json:"features"`
	Requirements                []string `json:"requirements"`
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

type rolloutModeSpec struct {
	Label   string `json:"label"`
	Method  string `json:"method"`
	Profile string `json:"profile"`
}

type rolloutProbeFailure struct {
	BlogID       int64  `json:"blog_id"`
	SourceSiteID int64  `json:"source_site_id"`
	BucketNo     int    `json:"bucket_no"`
	MonitorURL   string `json:"monitor_url"`
	Method       string `json:"method"`
	Profile      string `json:"profile"`
	HTTPCode     int    `json:"http_code"`
	ErrorCode    int    `json:"error_code"`
	ErrorDetail  string `json:"error_detail,omitempty"`
	RTTMs        int64  `json:"rtt_ms"`
}

type rolloutProbeSummary struct {
	Mode          rolloutModeSpec       `json:"mode"`
	ActiveSites   int                   `json:"active_sites"`
	SampleSize    int                   `json:"sample_size"`
	Checked       int                   `json:"checked"`
	Successes     int                   `json:"successes"`
	Failures      int                   `json:"failures"`
	ReadOnly      bool                  `json:"read_only"`
	FailureSample []rolloutProbeFailure `json:"failure_sample,omitempty"`
}

type rolloutComparisonResult struct {
	BlogID        int64  `json:"blog_id"`
	SourceSiteID  int64  `json:"source_site_id"`
	BucketNo      int    `json:"bucket_no"`
	MonitorURL    string `json:"monitor_url"`
	FromMethod    string `json:"from_method"`
	FromProfile   string `json:"from_profile"`
	ToMethod      string `json:"to_method"`
	ToProfile     string `json:"to_profile"`
	FromSuccess   bool   `json:"from_success"`
	ToSuccess     bool   `json:"to_success"`
	FromHTTPCode  int    `json:"from_http_code"`
	ToHTTPCode    int    `json:"to_http_code"`
	FromErrorCode int    `json:"from_error_code"`
	ToErrorCode   int    `json:"to_error_code"`
	FromRTTMs     int64  `json:"from_rtt_ms"`
	ToRTTMs       int64  `json:"to_rtt_ms"`
	DeltaClass    string `json:"delta_class"`
}

type rolloutVeriflierPreflightResult struct {
	Name         string   `json:"name,omitempty"`
	Address      string   `json:"address"`
	Status       string   `json:"status"`
	Version      string   `json:"version,omitempty"`
	Protocols    []string `json:"protocols,omitempty"`
	VantageID    string   `json:"vantage_id,omitempty"`
	AgentID      string   `json:"agent_id,omitempty"`
	Healthy      bool     `json:"healthy"`
	V2Compatible bool     `json:"v2_compatible"`
	Error        string   `json:"error,omitempty"`
}

func (s *Server) handleRolloutCapabilities(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	mode := config.RolloutModeActive
	if cfg != nil {
		mode = cfg.RolloutMode
	}
	writeJSON(w, http.StatusOK, rolloutCapabilitiesResponse{
		Status:                      "ok",
		APIVersion:                  rolloutAPIVersion,
		ServerHost:                  s.hostname,
		RolloutMode:                 mode,
		ConfirmationTokenTTLSeconds: int(rolloutConfirmationTTL.Seconds()),
		Features: []string{
			"sessions",
			"synchronous_jobs",
			"confirmation_tokens",
			"bucket_range_locks",
			"api_controlled_monitor_mode",
			"preflight",
			"read_only_smoke_checks",
			"seed_adopt",
			"final_reconcile",
			"activate_release",
			"post_handoff_gates",
			"method_comparison",
			"policy_stage_execute",
			"policy_stage_rollback",
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
	var veriflierResults []rolloutVeriflierPreflightResult
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
		results, verifierWarnings, verifierBlockers := s.rolloutVeriflierPreflight(r.Context(), cfg)
		veriflierResults = results
		warnings = append(warnings, verifierWarnings...)
		blockers = append(blockers, verifierBlockers...)
	}
	if err := s.db.PingContext(r.Context()); err != nil {
		blockers = append(blockers, "database ping failed: "+err.Error())
	}
	maxMigration, err := s.maxSchemaMigration(r.Context())
	if err != nil {
		blockers = append(blockers, "schema migration lookup failed: "+err.Error())
	} else if maxMigration < 48 {
		blockers = append(blockers, fmt.Sprintf("schema migration %d is older than required rollout migration 48", maxMigration))
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
		"verifliers":       veriflierResults,
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
	sample, err := rolloutSynchronousSampleSize(body.SampleSize, active)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "sample_too_large", err.Error())
		return
	}
	mode, err := rolloutModeFromString(body.Mode, rolloutModeSpec{Label: "head-legacy", Method: checkmode.MethodHEAD, Profile: checkmode.ProfileLegacy})
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_mode", err.Error())
		return
	}
	sites, err := s.rolloutSampleSites(r.Context(), body.BucketMin, body.BucketMax, sample)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "sample sites failed: "+err.Error())
		return
	}
	result := s.rolloutRunChecks(r.Context(), sites, mode, true)
	result.ActiveSites = active
	var warnings []string
	if active == 0 {
		warnings = append(warnings, "requested bucket range has no active sites")
	}
	if result.Failures > 0 {
		resp := s.rolloutOperation(r, "smoke", body, "blocked", "read-only smoke checks found failures", result, warnings, []string{"one or more sampled checks failed"})
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp := s.rolloutOperation(r, "smoke", body, "ok", "read-only smoke checks completed", result, warnings, nil)
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
	adopted, err := s.executeRolloutSeed(r.Context(), body.BucketMin, body.BucketMax)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", operation+" execute failed: "+err.Error())
		return
	}
	after, err := s.rolloutSeedCounts(r.Context(), body.BucketMin, body.BucketMax)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", operation+" readback failed: "+err.Error())
		return
	}
	after["adopted_non_running_events"] = adopted
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
	active, err := s.countActiveSites(r.Context(), body.BucketMin, body.BucketMax)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "count active sites failed: "+err.Error())
		return
	}
	sample, err := rolloutSynchronousSampleSize(body.SampleSize, active)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "sample_too_large", err.Error())
		return
	}
	from, err := rolloutModeFromString(body.From, rolloutModeSpec{Label: "head-legacy", Method: checkmode.MethodHEAD, Profile: checkmode.ProfileLegacy})
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_from", err.Error())
		return
	}
	to, err := rolloutModeFromString(body.To, rolloutModeSpec{Label: "get-simple", Method: checkmode.MethodGET, Profile: checkmode.ProfileSimpleHTTP})
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_to", err.Error())
		return
	}
	sites, err := s.rolloutSampleSites(r.Context(), body.BucketMin, body.BucketMax, sample)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error", "sample sites failed: "+err.Error())
		return
	}
	comparison := s.rolloutCompareChecks(r.Context(), sites, from, to)
	var warnings []string
	if active == 0 {
		warnings = append(warnings, "requested bucket range has no active sites")
	}
	result := map[string]any{
		"from":         from,
		"to":           to,
		"active_sites": active,
		"sample_size":  sample,
		"checked":      len(comparison),
		"read_only":    true,
		"deltas":       rolloutComparisonDeltaCounts(comparison),
		"sample":       rolloutComparisonSample(comparison, 20),
	}
	resp := s.rolloutOperation(r, "compare_methods", body, "ok", "method comparison completed", result, warnings, nil)
	if err := s.insertRolloutComparisonResults(r.Context(), resp.JobID, body.RunID, comparison); err != nil {
		resp.Status = "blocked"
		resp.Job.Status = "blocked"
		resp.Job.Progress = 0
		resp.Job.ErrorCode = "comparison_persist_failed"
		resp.Job.ErrorMessage = err.Error()
		resp.Summary = "method comparison completed but result persistence failed"
		resp.Blockers = append(resp.Blockers, "record method comparison results failed: "+err.Error())
		resp.NextAction = "Stop and resolve blockers before continuing."
		resp.Result = map[string]any{
			"from":          from,
			"to":            to,
			"active_sites":  active,
			"sample_size":   sample,
			"checked":       len(comparison),
			"deltas":        rolloutComparisonDeltaCounts(comparison),
			"persist_error": err.Error(),
		}
		_ = s.updateRolloutJobBlocked(r.Context(), resp.JobID, resp.Summary, resp.Job.ErrorCode, resp.Job.ErrorMessage)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRolloutStagePolicy(w http.ResponseWriter, r *http.Request) {
	body, ok := s.decodeRolloutRangeBody(w, r)
	if !ok {
		return
	}
	result, blockers, err := s.rolloutStagePolicyPlan(r.Context(), body)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_stage_policy", err.Error())
		return
	}
	if body.DryRun || !body.Execute {
		status := "ok"
		summary := "policy stage plan is ready"
		if len(blockers) > 0 {
			status = "blocked"
			summary = "policy stage plan is blocked"
		}
		resp := s.rolloutPlanResponseWithStatus(r, rolloutOpStagePolicy, body, status, summary, result, blockers)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if len(blockers) > 0 {
		resp := s.rolloutOperation(r, rolloutOpStagePolicy, body, "blocked", "policy stage plan is blocked", result, nil, blockers)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if !s.consumeRolloutConfirmation(w, r, rolloutOpStagePolicy, body) {
		return
	}
	resp := s.rolloutOperation(r, rolloutOpStagePolicy, body, "ok", "policy stage executed", result, nil, nil)
	executed, err := s.executeRolloutStagePolicy(r.Context(), body, resp.JobID)
	if err != nil {
		_ = s.updateRolloutJobBlocked(r.Context(), resp.JobID, "policy stage execution failed", "stage_policy_failed", err.Error())
		writeError(w, r, http.StatusInternalServerError, "db_error", "policy stage execute failed: "+err.Error())
		return
	}
	resp.Result = executed
	if raw, err := json.Marshal(executed); err == nil {
		resp.Job.Result = raw
	}
	_ = s.updateRolloutJobResult(r.Context(), resp.JobID, executed)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) rolloutStagePolicyPlan(ctx context.Context, body rolloutRangeRequest) (map[string]any, []string, error) {
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if mode == "" {
		mode = "stage"
	}
	switch mode {
	case "pause":
		return map[string]any{
			"mode":   mode,
			"action": "no policy changes will be made; use this as an operator checkpoint",
		}, nil, nil
	case "rollback-last-stage", "rollback-all":
		count, err := s.countRolloutStageRollbackRows(ctx, body, mode)
		if err != nil {
			return nil, nil, err
		}
		var blockers []string
		if count == 0 {
			blockers = append(blockers, "no unapplied policy stage rows are available for rollback")
		}
		return map[string]any{
			"mode":          mode,
			"rollback_rows": count,
		}, blockers, nil
	case "stage":
	default:
		return nil, nil, fmt.Errorf("mode must be stage, pause, rollback-last-stage, or rollback-all")
	}

	method, err := checkmode.NormalizeMethod(body.Method, "")
	if err != nil {
		return nil, nil, err
	}
	profile, err := checkmode.NormalizeProfile(body.Profile, "")
	if err != nil {
		return nil, nil, err
	}
	if body.Size == nil {
		return nil, nil, fmt.Errorf("size is required for staged policy changes")
	}
	profile = checkmode.EffectiveProfile(method, profile)
	active, err := s.countActiveSites(ctx, body.BucketMin, body.BucketMax)
	if err != nil {
		return nil, nil, err
	}
	eligible, err := s.countPolicyStageEligible(ctx, body.BucketMin, body.BucketMax, method, profile)
	if err != nil {
		return nil, nil, err
	}
	size, err := rolloutStageSize(body.Size, eligible)
	if err != nil {
		return nil, nil, err
	}
	var blockers []string
	if eligible == 0 {
		blockers = append(blockers, "no active sites need this policy change")
	}
	if size == 0 && eligible > 0 {
		blockers = append(blockers, "stage size resolved to zero")
	}
	return map[string]any{
		"mode":         mode,
		"method":       method,
		"profile":      profile,
		"size":         body.Size,
		"active_sites": active,
		"eligible":     eligible,
		"selected":     size,
	}, blockers, nil
}

func (s *Server) updateRolloutJobBlocked(ctx context.Context, jobID, summary, code, message string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE jetmon_rollout_jobs
		   SET status = 'blocked',
		       progress = 0,
		       summary = ?,
		       error_code = ?,
		       error_message = ?
		 WHERE job_id = ?`,
		summary, code, message, jobID,
	)
	return err
}

func (s *Server) updateRolloutJobResult(ctx context.Context, jobID string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE jetmon_rollout_jobs
		   SET result = ?,
		       updated_at = CURRENT_TIMESTAMP
		 WHERE job_id = ?`,
		nullableRawJSON(raw), jobID,
	)
	return err
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

func (s *Server) executeRolloutSeed(ctx context.Context, min, max int) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
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
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT IGNORE INTO jetmon_site_runtime (blog_id)
		SELECT DISTINCT blog_id
		  FROM jetpack_monitor_sites
		 WHERE monitor_active = 1
		   AND bucket_no BETWEEN ? AND ?`,
		min, max,
	); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	adopted, err := s.adoptRolloutNonRunningProjections(ctx, min, max)
	return adopted, err
}

func (s *Server) adoptRolloutNonRunningProjections(ctx context.Context, min, max int) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT jetpack_monitor_site_id, blog_id, bucket_no, monitor_url, site_status, last_status_change
		  FROM jetpack_monitor_sites
		 WHERE monitor_active = 1
		   AND bucket_no BETWEEN ? AND ?
		   AND site_status <> 1
		 ORDER BY jetpack_monitor_site_id`,
		min, max,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	store := eventstore.New(s.db)
	adopted := 0
	for rows.Next() {
		var sourceSiteID, blogID int64
		var bucketNo, status int
		var url string
		var lastChange sql.NullTime
		if err := rows.Scan(&sourceSiteID, &blogID, &bucketNo, &url, &status, &lastChange); err != nil {
			return adopted, err
		}
		state := eventstore.StateSeemsDown
		severity := eventstore.SeveritySeemsDown
		if status == 2 {
			state = eventstore.StateDown
			severity = eventstore.SeverityDown
		}
		started := time.Now().UTC()
		if lastChange.Valid {
			started = lastChange.Time.UTC()
		}
		endpointID := sourceSiteID
		meta, _ := json.Marshal(map[string]any{
			"source":             "legacy_projection_adoption",
			"legacy_site_status": status,
			"source_site_id":     sourceSiteID,
			"bucket_no":          bucketNo,
			"monitor_url":        url,
		})
		res, err := store.Open(ctx, eventstore.OpenInput{
			Identity: eventstore.Identity{
				BlogID:     blogID,
				EndpointID: &endpointID,
				CheckType:  "http",
			},
			Severity:  severity,
			State:     state,
			Source:    "rollout:seed",
			Metadata:  meta,
			StartedAt: &started,
		})
		if err != nil {
			return adopted, err
		}
		if res.Opened {
			adopted++
		} else if status == 2 && (res.CurrentSeverity < severity || res.CurrentState != state) {
			changed, err := store.Promote(ctx, res.EventID, severity, state, eventstore.ReasonVerifierConfirmed, "rollout:seed", meta)
			if err != nil {
				return adopted, err
			}
			if changed {
				adopted++
			}
		}
	}
	return adopted, rows.Err()
}

func (s *Server) rolloutSampleSites(ctx context.Context, min, max, limit int) ([]jetdb.Site, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			s.jetpack_monitor_site_id, s.blog_id, s.bucket_no, s.monitor_url,
			s.monitor_active, s.site_status, s.last_status_change, s.check_interval, r.last_checked_at, r.next_check_at,
			r.ssl_expiry_date, c.check_keyword, c.forbidden_keyword, c.forbidden_keywords, c.maintenance_start, c.maintenance_end,
			c.custom_headers, c.timeout_seconds, c.redirect_policy, c.alert_cooldown_minutes, r.last_alert_sent_at,
			c.request_method, c.detection_profile
		FROM jetpack_monitor_sites s
		LEFT JOIN jetmon_site_check_config c ON c.blog_id = s.blog_id
		LEFT JOIN jetmon_site_runtime r ON r.blog_id = s.blog_id
		WHERE s.monitor_active = 1
		  AND s.bucket_no BETWEEN ? AND ?
		ORDER BY s.jetpack_monitor_site_id ASC
		LIMIT ?`,
		min, max, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRolloutSiteRows(rows)
}

func scanRolloutSiteRows(rows *sql.Rows) ([]jetdb.Site, error) {
	var sites []jetdb.Site
	for rows.Next() {
		var site jetdb.Site
		var redirectPolicy sql.NullString
		var requestMethod sql.NullString
		var detectionProfile sql.NullString
		if err := rows.Scan(
			&site.ID, &site.BlogID, &site.BucketNo, &site.MonitorURL,
			&site.MonitorActive, &site.SiteStatus, &site.LastStatusChange, &site.CheckInterval, &site.LastCheckedAt, &site.NextCheckAt,
			&site.SSLExpiryDate, &site.CheckKeyword, &site.ForbiddenKeyword, &site.ForbiddenKeywords, &site.MaintenanceStart, &site.MaintenanceEnd,
			&site.CustomHeaders, &site.TimeoutSeconds, &redirectPolicy, &site.AlertCooldownMinutes, &site.LastAlertSentAt,
			&requestMethod, &detectionProfile,
		); err != nil {
			return nil, err
		}
		if redirectPolicy.Valid {
			site.RedirectPolicy = redirectPolicy.String
		} else {
			site.RedirectPolicy = "follow"
		}
		if requestMethod.Valid {
			site.RequestMethod = requestMethod.String
		}
		if detectionProfile.Valid {
			site.DetectionProfile = detectionProfile.String
		}
		sites = append(sites, site)
	}
	return sites, rows.Err()
}

func (s *Server) rolloutRunChecks(ctx context.Context, sites []jetdb.Site, mode rolloutModeSpec, readOnly bool) rolloutProbeSummary {
	results := rolloutCheckSites(ctx, sites, mode)
	out := rolloutProbeSummary{
		Mode:       mode,
		SampleSize: len(sites),
		Checked:    len(results),
		ReadOnly:   readOnly,
	}
	for i, res := range results {
		if res.Success {
			out.Successes++
			continue
		}
		out.Failures++
		if len(out.FailureSample) < 20 {
			out.FailureSample = append(out.FailureSample, rolloutProbeFailure{
				BlogID:       sites[i].BlogID,
				SourceSiteID: sites[i].ID,
				BucketNo:     sites[i].BucketNo,
				MonitorURL:   sites[i].MonitorURL,
				Method:       res.Method,
				Profile:      res.DetectionProfile,
				HTTPCode:     res.HTTPCode,
				ErrorCode:    res.ErrorCode,
				ErrorDetail:  res.ErrorDetail,
				RTTMs:        res.RTT.Milliseconds(),
			})
		}
	}
	return out
}

func rolloutCheckSites(ctx context.Context, sites []jetdb.Site, mode rolloutModeSpec) []checker.Result {
	results := make([]checker.Result, len(sites))
	if len(sites) == 0 {
		return results
	}
	cfg := config.Get()
	workers := rolloutDefaultProbeConcurrent
	if workers > len(sites) {
		workers = len(sites)
	}
	type job struct {
		i    int
		site jetdb.Site
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				select {
				case <-ctx.Done():
					results[job.i] = checker.Result{
						MonitorSiteID:    job.site.ID,
						BlogID:           job.site.BlogID,
						URL:              job.site.MonitorURL,
						Method:           mode.Method,
						DetectionProfile: mode.Profile,
						ErrorCode:        checker.ErrorTimeout,
						ErrorDetail:      ctx.Err().Error(),
						Success:          false,
						Timestamp:        time.Now().UTC(),
					}
				default:
					results[job.i] = checker.Check(ctx, rolloutCheckRequest(cfg, job.site, mode))
				}
			}
		}()
	}
	for i, site := range sites {
		jobs <- job{i: i, site: site}
	}
	close(jobs)
	wg.Wait()
	return results
}

func rolloutCheckRequest(cfg *config.Config, site jetdb.Site, mode rolloutModeSpec) checker.Request {
	timeout := 10
	if cfg != nil && cfg.NetCommsTimeout > 0 {
		timeout = cfg.NetCommsTimeout
	}
	if site.TimeoutSeconds != nil && *site.TimeoutSeconds > 0 {
		timeout = *site.TimeoutSeconds
	}
	req := checker.Request{
		MonitorSiteID:       site.ID,
		BlogID:              site.BlogID,
		URL:                 site.MonitorURL,
		Method:              mode.Method,
		DetectionProfile:    mode.Profile,
		TimeoutSeconds:      timeout,
		CustomHeaders:       checker.ParseCustomHeaders(site.CustomHeaders),
		RedirectPolicy:      checker.RedirectFollow,
		BodyReadMaxBytes:    1048576,
		BodyReadMaxMS:       250,
		KeywordReadMaxBytes: 1048576,
		EnforceTargetSafety: true,
	}
	if cfg != nil {
		req.BodyReadMaxBytes = cfg.BodyReadMaxBytes
		req.BodyReadMaxMS = cfg.BodyReadMaxMS
		req.KeywordReadMaxBytes = cfg.KeywordReadMaxBytes
		req.KeywordReadMaxMS = cfg.KeywordReadMaxMS
	}
	if mode.Profile == checkmode.ProfileFull {
		req.Keyword = site.CheckKeyword
		req.ForbiddenKeyword = site.ForbiddenKeyword
		req.ForbiddenKeywords = checker.ParseForbiddenKeywords(site.ForbiddenKeywords)
		req.RedirectPolicy = checker.RedirectPolicy(site.RedirectPolicy)
		if req.RedirectPolicy == "" {
			req.RedirectPolicy = checker.RedirectFollow
		}
	}
	return req
}

func (s *Server) rolloutCompareChecks(ctx context.Context, sites []jetdb.Site, from, to rolloutModeSpec) []rolloutComparisonResult {
	fromResults := rolloutCheckSites(ctx, sites, from)
	toResults := rolloutCheckSites(ctx, sites, to)
	out := make([]rolloutComparisonResult, 0, len(sites))
	for i, site := range sites {
		a := fromResults[i]
		b := toResults[i]
		out = append(out, rolloutComparisonResult{
			BlogID:        site.BlogID,
			SourceSiteID:  site.ID,
			BucketNo:      site.BucketNo,
			MonitorURL:    site.MonitorURL,
			FromMethod:    a.Method,
			FromProfile:   a.DetectionProfile,
			ToMethod:      b.Method,
			ToProfile:     b.DetectionProfile,
			FromSuccess:   a.Success,
			ToSuccess:     b.Success,
			FromHTTPCode:  a.HTTPCode,
			ToHTTPCode:    b.HTTPCode,
			FromErrorCode: a.ErrorCode,
			ToErrorCode:   b.ErrorCode,
			FromRTTMs:     a.RTT.Milliseconds(),
			ToRTTMs:       b.RTT.Milliseconds(),
			DeltaClass:    rolloutComparisonDelta(a, b),
		})
	}
	return out
}

func rolloutComparisonDelta(from, to checker.Result) string {
	switch {
	case from.Success == to.Success && from.HTTPCode == to.HTTPCode && from.ErrorCode == to.ErrorCode:
		return "same"
	case !from.Success && to.Success:
		return "get_better"
	case from.Success && !to.Success:
		return "get_worse"
	default:
		return "different_failure"
	}
}

func rolloutComparisonDeltaCounts(rows []rolloutComparisonResult) map[string]int {
	out := map[string]int{
		"same":              0,
		"get_better":        0,
		"get_worse":         0,
		"different_failure": 0,
	}
	for _, row := range rows {
		out[row.DeltaClass]++
	}
	return out
}

func rolloutComparisonSample(rows []rolloutComparisonResult, limit int) []rolloutComparisonResult {
	if len(rows) <= limit {
		return rows
	}
	sample := make([]rolloutComparisonResult, 0, limit)
	for _, row := range rows {
		if row.DeltaClass != "same" {
			sample = append(sample, row)
			if len(sample) == limit {
				return sample
			}
		}
	}
	return rows[:limit]
}

func (s *Server) insertRolloutComparisonResults(ctx context.Context, jobID, runID string, rows []rolloutComparisonResult) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO jetmon_rollout_comparison_results
			(job_id, run_id, blog_id, source_site_id, bucket_no, monitor_url,
			 from_method, from_profile, to_method, to_profile,
			 from_success, to_success, from_http_code, to_http_code,
			 from_error_code, to_error_code, from_rtt_ms, to_rtt_ms, delta_class)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err := stmt.ExecContext(ctx,
			jobID, runID, row.BlogID, row.SourceSiteID, row.BucketNo, row.MonitorURL,
			row.FromMethod, row.FromProfile, row.ToMethod, row.ToProfile,
			boolInt(row.FromSuccess), boolInt(row.ToSuccess), row.FromHTTPCode, row.ToHTTPCode,
			row.FromErrorCode, row.ToErrorCode, row.FromRTTMs, row.ToRTTMs, row.DeltaClass,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) rolloutVeriflierPreflight(ctx context.Context, cfg *config.Config) ([]rolloutVeriflierPreflightResult, []string, []string) {
	var results []rolloutVeriflierPreflightResult
	var warnings []string
	var blockers []string
	verifiers, discoveryWarnings, discoveryBlockers := s.rolloutVerifierConfigsForPreflight(ctx, cfg)
	warnings = append(warnings, discoveryWarnings...)
	blockers = append(blockers, discoveryBlockers...)
	if len(verifiers) == 0 {
		blockers = append(blockers, "no Veriflier endpoints are available; rollout preflight cannot prove remote confirmation coverage")
		return results, warnings, blockers
	}
	healthyVantages := make(map[string]struct{})
	for i, verifierCfg := range verifiers {
		name := strings.TrimSpace(verifierCfg.Name)
		if name == "" {
			name = fmt.Sprintf("veriflier-%d", i+1)
		}
		port := strings.TrimSpace(verifierCfg.TransportPort())
		host := strings.TrimSpace(verifierCfg.Host)
		addr := net.JoinHostPort(host, port)
		result := rolloutVeriflierPreflightResult{Name: name, Address: addr}
		if host == "" || port == "" {
			result.Error = "host and port are required"
			results = append(results, result)
			blockers = append(blockers, fmt.Sprintf("%s has incomplete host/port config", name))
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		status, err := veriflier.NewVeriflierClient(addr, verifierCfg.AuthToken).Status(checkCtx)
		cancel()
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			blockers = append(blockers, fmt.Sprintf("%s status check failed: %v", name, err))
			continue
		}
		result.Status = status.Status
		result.Version = status.Version
		result.Protocols = status.Protocols
		result.VantageID = status.Vantage.ID
		result.AgentID = status.Agent.ID
		result.V2Compatible = stringSliceContains(status.Protocols, veriflier.ProtocolV2)
		result.Healthy = strings.EqualFold(status.Status, "ok") && result.V2Compatible && result.VantageID != ""
		if !result.V2Compatible {
			blockers = append(blockers, fmt.Sprintf("%s does not advertise %s", name, veriflier.ProtocolV2))
		}
		if result.VantageID == "" {
			blockers = append(blockers, fmt.Sprintf("%s did not report a quorum-counted vantage id", name))
		}
		if status.Capacity.MaxConcurrency <= 0 || status.Capacity.QueueCapacity <= 0 {
			warnings = append(warnings, fmt.Sprintf("%s did not report usable capacity hints", name))
		}
		if result.Healthy {
			if _, exists := healthyVantages[result.VantageID]; exists {
				blockers = append(blockers, fmt.Sprintf("duplicate healthy Veriflier vantage id %q reported by %s", result.VantageID, name))
			}
			healthyVantages[result.VantageID] = struct{}{}
		}
		results = append(results, result)
	}
	required := cfg.PeerOfflineLimit
	if required < 1 {
		required = 1
	}
	if len(healthyVantages) < required {
		blockers = append(blockers, fmt.Sprintf("healthy v2 Veriflier vantage count %d is below PEER_OFFLINE_LIMIT %d", len(healthyVantages), required))
	}
	return results, warnings, blockers
}

func (s *Server) rolloutVerifierConfigsForPreflight(ctx context.Context, cfg *config.Config) ([]config.VerifierConfig, []string, []string) {
	mode := cfg.VeriflierDiscoveryModeOrDefault()
	if mode != config.VeriflierDiscoveryModeActive {
		return cfg.Verifiers, nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT vantage_id, endpoint_host, endpoint_port, auth_token
		  FROM jetmon_veriflier_vantages
		 WHERE enabled = 1
		 ORDER BY vantage_id`)
	if err != nil {
		if len(cfg.Verifiers) > 0 {
			return cfg.Verifiers, []string{"active Veriflier discovery registry lookup failed; checking static VERIFIERS fallback: " + err.Error()}, nil
		}
		return nil, nil, []string{"active Veriflier discovery registry lookup failed and static VERIFIERS is empty: " + err.Error()}
	}
	defer rows.Close()
	var out []config.VerifierConfig
	for rows.Next() {
		var v config.VerifierConfig
		if err := rows.Scan(&v.Name, &v.Host, &v.Port, &v.AuthToken); err != nil {
			return nil, nil, []string{"active Veriflier discovery registry scan failed: " + err.Error()}
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, []string{"active Veriflier discovery registry scan failed: " + err.Error()}
	}
	agentHints, err := s.rolloutVeriflierAgentEndpointHints(ctx)
	if err != nil {
		return nil, nil, []string{"active Veriflier discovery agent hint lookup failed: " + err.Error()}
	}
	for i := range out {
		if hint, ok := agentHints[out[i].Name]; ok {
			if strings.TrimSpace(out[i].Host) == "" {
				out[i].Host = hint.Host
			}
			if strings.TrimSpace(out[i].Port) == "" {
				out[i].Port = hint.Port
			}
		}
	}
	out = completeVerifierConfigs(out)
	if len(out) == 0 && len(cfg.Verifiers) > 0 {
		return cfg.Verifiers, []string{"active Veriflier discovery has no complete enabled vantages; checking static VERIFIERS fallback"}, nil
	}
	if len(out) == 0 {
		return nil, nil, []string{"active Veriflier discovery has no complete enabled vantages and static VERIFIERS is empty"}
	}
	return out, nil, nil
}

type rolloutVerifierEndpointHint struct {
	Host string
	Port string
}

func (s *Server) rolloutVeriflierAgentEndpointHints(ctx context.Context) (map[string]rolloutVerifierEndpointHint, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT vantage_id, endpoint_host, endpoint_port
		  FROM jetmon_veriflier_agents
		 WHERE status = 'active'
		   AND last_seen >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL 90 SECOND)
		 ORDER BY vantage_id, last_seen DESC, agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]rolloutVerifierEndpointHint)
	for rows.Next() {
		var vantageID, host, port string
		if err := rows.Scan(&vantageID, &host, &port); err != nil {
			return nil, err
		}
		if _, exists := out[vantageID]; exists {
			continue
		}
		out[vantageID] = rolloutVerifierEndpointHint{Host: host, Port: port}
	}
	return out, rows.Err()
}

func completeVerifierConfigs(values []config.VerifierConfig) []config.VerifierConfig {
	out := values[:0]
	for _, value := range values {
		if strings.TrimSpace(value.Host) == "" || strings.TrimSpace(value.TransportPort()) == "" || strings.TrimSpace(value.AuthToken) == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (s *Server) countPolicyStageEligible(ctx context.Context, min, max int, method, profile string) (int, error) {
	defaultMethod, defaultProfile := rolloutDefaultPolicy()
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT s.blog_id)
		  FROM jetpack_monitor_sites s
		  LEFT JOIN jetmon_site_check_config c ON c.blog_id = s.blog_id
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN ? AND ?
		   AND (
			 COALESCE(c.request_method, ?) <> ?
			 OR CASE
			      WHEN COALESCE(c.request_method, ?) = 'HEAD' AND COALESCE(c.detection_profile, ?) = 'full' THEN 'simple_http'
			      ELSE COALESCE(c.detection_profile, ?)
			    END <> ?
		   )`,
		min, max,
		defaultMethod, method,
		defaultMethod, defaultProfile, defaultProfile, profile,
	).Scan(&count)
	return count, err
}

type rolloutPolicyStageCandidate struct {
	BlogID      int64
	BucketNo    int
	PrevMethod  sql.NullString
	PrevProfile sql.NullString
}

func (s *Server) selectPolicyStageCandidates(ctx context.Context, body rolloutRangeRequest, method, profile string, limit int) ([]rolloutPolicyStageCandidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	defaultMethod, defaultProfile := rolloutDefaultPolicy()
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.blog_id, MIN(s.bucket_no), c.request_method, c.detection_profile
		  FROM jetpack_monitor_sites s
		  LEFT JOIN jetmon_site_check_config c ON c.blog_id = s.blog_id
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN ? AND ?
		   AND (
			 COALESCE(c.request_method, ?) <> ?
			 OR CASE
			      WHEN COALESCE(c.request_method, ?) = 'HEAD' AND COALESCE(c.detection_profile, ?) = 'full' THEN 'simple_http'
			      ELSE COALESCE(c.detection_profile, ?)
			    END <> ?
		   )
		 GROUP BY s.blog_id, c.request_method, c.detection_profile
		 ORDER BY MIN(s.jetpack_monitor_site_id) ASC
		 LIMIT ?`,
		body.BucketMin, body.BucketMax,
		defaultMethod, method,
		defaultMethod, defaultProfile, defaultProfile, profile,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rolloutPolicyStageCandidate
	for rows.Next() {
		var candidate rolloutPolicyStageCandidate
		if err := rows.Scan(&candidate.BlogID, &candidate.BucketNo, &candidate.PrevMethod, &candidate.PrevProfile); err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func (s *Server) executeRolloutStagePolicy(ctx context.Context, body rolloutRangeRequest, jobID string) (map[string]any, error) {
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if mode == "" {
		mode = "stage"
	}
	switch mode {
	case "pause":
		return map[string]any{"mode": mode, "changed": 0}, nil
	case "rollback-last-stage", "rollback-all":
		return s.rollbackRolloutStagePolicy(ctx, body, jobID, mode)
	}
	method, err := checkmode.NormalizeMethod(body.Method, "")
	if err != nil {
		return nil, err
	}
	profile, err := checkmode.NormalizeProfile(body.Profile, "")
	if err != nil {
		return nil, err
	}
	profile = checkmode.EffectiveProfile(method, profile)
	eligible, err := s.countPolicyStageEligible(ctx, body.BucketMin, body.BucketMax, method, profile)
	if err != nil {
		return nil, err
	}
	size, err := rolloutStageSize(body.Size, eligible)
	if err != nil {
		return nil, err
	}
	candidates, err := s.selectPolicyStageCandidates(ctx, body, method, profile, size)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, candidate := range candidates {
		if err := upsertRolloutPolicyTx(ctx, tx, candidate.BlogID, method, profile); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO jetmon_rollout_policy_stage_rows
				(job_id, run_id, blog_id, bucket_no, previous_request_method, previous_detection_profile, new_request_method, new_detection_profile)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			jobID, body.RunID, candidate.BlogID, candidate.BucketNo, nullableString(candidate.PrevMethod), nullableString(candidate.PrevProfile), method, profile,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{
		"mode":     mode,
		"method":   method,
		"profile":  profile,
		"eligible": eligible,
		"changed":  len(candidates),
	}, nil
}

type rolloutPolicyRollbackRow struct {
	ID          int64
	BlogID      int64
	PrevMethod  sql.NullString
	PrevProfile sql.NullString
}

func (s *Server) countRolloutStageRollbackRows(ctx context.Context, body rolloutRangeRequest, mode string) (int, error) {
	if mode == "rollback-last-stage" {
		jobID, err := s.latestRolloutStageJob(ctx, body)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, nil
			}
			return 0, err
		}
		var count int
		err = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*)
			  FROM jetmon_rollout_policy_stage_rows
			 WHERE job_id = ?
			   AND rolled_back_at IS NULL
			   AND bucket_no BETWEEN ? AND ?`,
			jobID, body.BucketMin, body.BucketMax,
		).Scan(&count)
		return count, err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetmon_rollout_policy_stage_rows
		 WHERE rolled_back_at IS NULL
		   AND bucket_no BETWEEN ? AND ?
		   AND (? = '' OR run_id = ?)`,
		body.BucketMin, body.BucketMax, body.RunID, body.RunID,
	).Scan(&count)
	return count, err
}

func (s *Server) rollbackRolloutStagePolicy(ctx context.Context, body rolloutRangeRequest, jobID, mode string) (map[string]any, error) {
	rows, err := s.rolloutRollbackRows(ctx, body, mode)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range rows {
		if err := upsertRolloutPolicyTx(ctx, tx, row.BlogID, nullableString(row.PrevMethod), nullableString(row.PrevProfile)); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE jetmon_rollout_policy_stage_rows
			   SET rolled_back_at = CURRENT_TIMESTAMP(3)
			 WHERE id = ?`,
			row.ID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{
		"mode":         mode,
		"rollback_job": jobID,
		"rolled_back":  len(rows),
	}, nil
}

func (s *Server) rolloutRollbackRows(ctx context.Context, body rolloutRangeRequest, mode string) ([]rolloutPolicyRollbackRow, error) {
	query := `
		SELECT id, blog_id, previous_request_method, previous_detection_profile
		  FROM jetmon_rollout_policy_stage_rows
		 WHERE rolled_back_at IS NULL
		   AND bucket_no BETWEEN ? AND ?
		   AND (? = '' OR run_id = ?)`
	args := []any{body.BucketMin, body.BucketMax, body.RunID, body.RunID}
	if mode == "rollback-last-stage" {
		jobID, err := s.latestRolloutStageJob(ctx, body)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		query += ` AND job_id = ?`
		args = append(args, jobID)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rolloutPolicyRollbackRow
	for rows.Next() {
		var row rolloutPolicyRollbackRow
		if err := rows.Scan(&row.ID, &row.BlogID, &row.PrevMethod, &row.PrevProfile); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Server) latestRolloutStageJob(ctx context.Context, body rolloutRangeRequest) (string, error) {
	var jobID string
	err := s.db.QueryRowContext(ctx, `
		SELECT job_id
		  FROM jetmon_rollout_policy_stage_rows
		 WHERE rolled_back_at IS NULL
		   AND bucket_no BETWEEN ? AND ?
		   AND (? = '' OR run_id = ?)
		 GROUP BY job_id
		 ORDER BY MAX(created_at) DESC, MAX(id) DESC
		 LIMIT 1`,
		body.BucketMin, body.BucketMax, body.RunID, body.RunID,
	).Scan(&jobID)
	return jobID, err
}

func upsertRolloutPolicyTx(ctx context.Context, tx *sql.Tx, blogID int64, method, profile any) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO jetmon_site_check_config (blog_id, request_method, detection_profile)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE
			request_method = VALUES(request_method),
			detection_profile = VALUES(detection_profile)`,
		blogID, method, profile,
	)
	return err
}

func nullableString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
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
		return fmt.Sprintf("%s#%d", key.ConsumerName, key.ID)
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

func rolloutModeFromString(value string, fallback rolloutModeSpec) (rolloutModeSpec, error) {
	raw := strings.ToLower(strings.TrimSpace(value))
	if raw == "" {
		return normalizeRolloutModeSpec(fallback)
	}
	raw = strings.ReplaceAll(raw, "_", "-")
	raw = strings.ReplaceAll(raw, "/", "-")
	raw = strings.ReplaceAll(raw, ":", "-")
	switch raw {
	case "head", "head-legacy":
		return normalizeRolloutModeSpec(rolloutModeSpec{Label: "head-legacy", Method: checkmode.MethodHEAD, Profile: checkmode.ProfileLegacy})
	case "get", "get-simple", "get-simple-http":
		return normalizeRolloutModeSpec(rolloutModeSpec{Label: "get-simple", Method: checkmode.MethodGET, Profile: checkmode.ProfileSimpleHTTP})
	case "get-full":
		return normalizeRolloutModeSpec(rolloutModeSpec{Label: "get-full", Method: checkmode.MethodGET, Profile: checkmode.ProfileFull})
	default:
		return rolloutModeSpec{}, fmt.Errorf("mode must be one of head-legacy, get-simple, or get-full")
	}
}

func normalizeRolloutModeSpec(spec rolloutModeSpec) (rolloutModeSpec, error) {
	method, err := checkmode.NormalizeMethod(spec.Method, "")
	if err != nil {
		return rolloutModeSpec{}, err
	}
	profile, err := checkmode.NormalizeProfile(spec.Profile, "")
	if err != nil {
		return rolloutModeSpec{}, err
	}
	profile = checkmode.EffectiveProfile(method, profile)
	label := strings.TrimSpace(spec.Label)
	if label == "" {
		label = strings.ToLower(method + "-" + profile)
		label = strings.ReplaceAll(label, "_", "-")
	}
	return rolloutModeSpec{Label: label, Method: method, Profile: profile}, nil
}

func rolloutSynchronousSampleSize(requested, active int) (int, error) {
	if active <= 0 {
		return 0, nil
	}
	if requested <= 0 {
		requested = rolloutDefaultProbeSample
	}
	if requested > rolloutMaxSynchronousSample {
		return 0, fmt.Errorf("sample_size %d exceeds synchronous limit %d", requested, rolloutMaxSynchronousSample)
	}
	if requested > active {
		return active, nil
	}
	return requested, nil
}

func rolloutStageSize(value any, eligible int) (int, error) {
	if eligible <= 0 {
		return 0, nil
	}
	if value == nil {
		return eligible, nil
	}
	switch v := value.(type) {
	case float64:
		if v <= 0 || v != float64(int(v)) {
			return 0, fmt.Errorf("size must be a positive integer or percentage")
		}
		if v > float64(eligible) {
			return eligible, nil
		}
		return int(v), nil
	case int:
		if v <= 0 {
			return 0, fmt.Errorf("size must be positive")
		}
		if v > eligible {
			return eligible, nil
		}
		return v, nil
	case string:
		raw := strings.TrimSpace(v)
		if raw == "" {
			return eligible, nil
		}
		if strings.HasSuffix(raw, "%") {
			percent, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(raw, "%")), 64)
			if err != nil || percent <= 0 {
				return 0, fmt.Errorf("percentage size must be positive")
			}
			n := int(float64(eligible) * percent / 100)
			if n == 0 {
				n = 1
			}
			if n > eligible {
				n = eligible
			}
			return n, nil
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("size must be a positive integer or percentage")
		}
		if n > eligible {
			return eligible, nil
		}
		return n, nil
	default:
		return 0, fmt.Errorf("size must be a positive integer or percentage")
	}
}

func rolloutDefaultPolicy() (string, string) {
	cfg := config.Get()
	method := checkmode.MethodGET
	profile := checkmode.ProfileFull
	if cfg != nil {
		if normalized, err := checkmode.NormalizeMethod(cfg.DefaultCheckMethod, method); err == nil {
			method = normalized
		}
		if normalized, err := checkmode.NormalizeProfile(cfg.DefaultDetectionProfile, profile); err == nil {
			profile = normalized
		}
	}
	return method, checkmode.EffectiveProfile(method, profile)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

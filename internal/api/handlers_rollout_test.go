package api

import (
	"net/http"
	"testing"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/checkmode"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestRolloutCapabilities(t *testing.T) {
	s, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := requestWithKey(http.MethodGet, "/api/v1/rollout/capabilities", key)
	rec := invokeAuthed(s, req, s.handleRolloutCapabilities)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp rolloutCapabilitiesResponse
	readJSON(t, rec.Body, &resp)
	if resp.APIVersion != rolloutAPIVersion {
		t.Fatalf("api_version = %q, want %q", resp.APIVersion, rolloutAPIVersion)
	}
	if resp.ServerHost != "test-host" {
		t.Fatalf("server_host = %q, want test-host", resp.ServerHost)
	}
	if resp.ConfirmationTokenTTLSeconds != int(rolloutConfirmationTTL.Seconds()) {
		t.Fatalf("confirmation ttl = %d, want %d", resp.ConfirmationTokenTTLSeconds, int(rolloutConfirmationTTL.Seconds()))
	}
	if !containsString(resp.Features, "confirmation_tokens") {
		t.Fatalf("features = %#v, want confirmation_tokens", resp.Features)
	}
	if !containsString(resp.Features, "synthetic_canary_checks") {
		t.Fatalf("features = %#v, want synthetic_canary_checks", resp.Features)
	}
	if !containsString(resp.Requirements, "ROLLOUT_MODE=api-controlled for API activation") {
		t.Fatalf("requirements = %#v, want api-controlled requirement", resp.Requirements)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestRolloutModeFromString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		method  string
		profile string
	}{
		{name: "empty fallback", input: "", method: checkmode.MethodHEAD, profile: checkmode.ProfileLegacy},
		{name: "head legacy", input: "head-legacy", method: checkmode.MethodHEAD, profile: checkmode.ProfileLegacy},
		{name: "get simple alias", input: "get-simple_http", method: checkmode.MethodGET, profile: checkmode.ProfileSimpleHTTP},
		{name: "get full", input: "get-full", method: checkmode.MethodGET, profile: checkmode.ProfileFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rolloutModeFromString(tt.input, rolloutModeSpec{Label: "head-legacy", Method: checkmode.MethodHEAD, Profile: checkmode.ProfileLegacy})
			if err != nil {
				t.Fatalf("rolloutModeFromString(%q): %v", tt.input, err)
			}
			if got.Method != tt.method || got.Profile != tt.profile {
				t.Fatalf("mode = %#v, want method=%s profile=%s", got, tt.method, tt.profile)
			}
		})
	}
	if _, err := rolloutModeFromString("post-full", rolloutModeSpec{}); err == nil {
		t.Fatal("rolloutModeFromString accepted unsupported mode")
	}
}

func TestRolloutStageSize(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		eligible int
		want     int
	}{
		{name: "nil selects all", value: nil, eligible: 100, want: 100},
		{name: "integer caps at eligible", value: float64(150), eligible: 100, want: 100},
		{name: "string integer", value: "25", eligible: 100, want: 25},
		{name: "percentage rounds up to one", value: "1%", eligible: 10, want: 1},
		{name: "percentage", value: "12.5%", eligible: 200, want: 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rolloutStageSize(tt.value, tt.eligible)
			if err != nil {
				t.Fatalf("rolloutStageSize(%v, %d): %v", tt.value, tt.eligible, err)
			}
			if got != tt.want {
				t.Fatalf("rolloutStageSize(%v, %d) = %d, want %d", tt.value, tt.eligible, got, tt.want)
			}
		})
	}
	if _, err := rolloutStageSize("0%", 100); err == nil {
		t.Fatal("rolloutStageSize accepted zero percentage")
	}
}

func TestRolloutOperatorBindsToAPIKeyID(t *testing.T) {
	_, _, key, cleanup := newTestServer(t)
	defer cleanup()

	req := requestWithKey(http.MethodPost, "/api/v1/rollout/seed", key)
	if got := rolloutOperator(req); got != "test-consumer#1" {
		t.Fatalf("rolloutOperator = %q, want test-consumer#1", got)
	}
}

func TestRolloutActivityCheckRequireAllBlocksPartialFreshness(t *testing.T) {
	s, mock, key, cleanup := newTestServer(t)
	defer cleanup()

	mock.ExpectQuery(`
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites
		 WHERE monitor_active = 1
		   AND bucket_no BETWEEN ? AND ?`).
		WithArgs(0, 9).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(10))
	mock.ExpectQuery(`
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites s
		  JOIN jetpack_monitor_site_runtime r ON r.source_site_id = s.jetpack_monitor_site_id
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN ? AND ?
		   AND r.last_checked_at >= ?`).
		WithArgs(0, 9, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(8))

	req := requestWithKey(http.MethodGet, "/api/v1/rollout/activity-check?bucket_min=0&bucket_max=9&since=15m&require_all=true", key)
	rec := invokeAuthed(s, req, s.handleRolloutActivityCheck)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Status          string   `json:"status"`
		ActiveSites     int      `json:"active_sites"`
		RecentlyChecked int      `json:"recently_checked"`
		RequireAll      bool     `json:"require_all"`
		Blockers        []string `json:"blockers"`
	}
	readJSON(t, rec.Body, &resp)
	if resp.Status != "blocked" || resp.ActiveSites != 10 || resp.RecentlyChecked != 8 || !resp.RequireAll {
		t.Fatalf("activity response = %+v, want blocked 8/10 require_all", resp)
	}
	if len(resp.Blockers) != 1 {
		t.Fatalf("blockers = %#v, want one blocker", resp.Blockers)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestRolloutCanaryMode(t *testing.T) {
	fallback := rolloutModeSpec{Label: "head-legacy", Method: checkmode.MethodHEAD, Profile: checkmode.ProfileLegacy}
	got, err := rolloutCanaryMode(rolloutCanarySpec{Method: "GET", Profile: "full"}, fallback)
	if err != nil {
		t.Fatalf("rolloutCanaryMode() error = %v", err)
	}
	if got.Method != checkmode.MethodGET || got.Profile != checkmode.ProfileFull {
		t.Fatalf("mode = %#v, want GET/full", got)
	}
	got, err = rolloutCanaryMode(rolloutCanarySpec{Mode: "get-simple"}, fallback)
	if err != nil {
		t.Fatalf("rolloutCanaryMode(mode) error = %v", err)
	}
	if got.Method != checkmode.MethodGET || got.Profile != checkmode.ProfileSimpleHTTP {
		t.Fatalf("mode = %#v, want GET/simple_http", got)
	}
	if _, err := rolloutCanaryMode(rolloutCanarySpec{Mode: "get-full", Method: "GET"}, fallback); err == nil {
		t.Fatal("rolloutCanaryMode accepted mode with method override")
	}
}

func TestRolloutEvaluateCanary(t *testing.T) {
	success := false
	httpCode := 503
	errorCode := 2
	res := checker.Result{
		URL:              "https://canary.example.test/down",
		Method:           checkmode.MethodGET,
		DetectionProfile: checkmode.ProfileSimpleHTTP,
		Success:          false,
		HTTPCode:         503,
		ErrorCode:        2,
	}
	result := rolloutEvaluateCanary(rolloutCanarySpec{
		Name:            "controlled-down",
		ExpectSuccess:   &success,
		ExpectHTTPCode:  &httpCode,
		ExpectErrorCode: &errorCode,
	}, rolloutModeSpec{Label: "get-simple", Method: checkmode.MethodGET, Profile: checkmode.ProfileSimpleHTTP}, res, 1)
	if !result.Passed {
		t.Fatalf("canary result failed unexpectedly: %#v", result)
	}

	expectedOK := true
	result = rolloutEvaluateCanary(rolloutCanarySpec{ExpectSuccess: &expectedOK}, rolloutModeSpec{Label: "get-simple", Method: checkmode.MethodGET, Profile: checkmode.ProfileSimpleHTTP}, res, 2)
	if result.Passed || len(result.Mismatches) == 0 {
		t.Fatalf("canary result = %#v, want mismatch", result)
	}
}

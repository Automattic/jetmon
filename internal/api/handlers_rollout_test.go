package api

import (
	"net/http"
	"testing"

	"github.com/Automattic/jetmon/internal/checkmode"
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

package api

import (
	"net/http"
	"testing"
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

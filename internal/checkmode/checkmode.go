package checkmode

import (
	"fmt"
	"net/http"
	"strings"
)

const (
	MethodHEAD = http.MethodHead
	MethodGET  = http.MethodGet

	ProfileLegacy     = "legacy"
	ProfileSimpleHTTP = "simple_http"
	ProfileFull       = "full"
)

// NormalizeMethod returns a canonical request method. Empty values use def.
func NormalizeMethod(value, def string) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(value))
	if method == "" {
		method = strings.ToUpper(strings.TrimSpace(def))
	}
	switch method {
	case MethodHEAD, MethodGET:
		return method, nil
	default:
		return "", fmt.Errorf("request_method must be HEAD or GET")
	}
}

// NormalizeProfile returns a canonical detection profile. Empty values use def.
func NormalizeProfile(value, def string) (string, error) {
	profile := strings.ToLower(strings.TrimSpace(value))
	if profile == "" {
		profile = strings.ToLower(strings.TrimSpace(def))
	}
	switch profile {
	case ProfileLegacy, ProfileSimpleHTTP, ProfileFull:
		return profile, nil
	default:
		return "", fmt.Errorf("detection_profile must be one of: legacy, simple_http, full")
	}
}

// EffectiveProfile gates detections that cannot run with the selected request
// method. A HEAD request can still prove basic reachability, but it cannot
// support body-based checks, so it never executes the full detection profile.
func EffectiveProfile(method, profile string) string {
	if method == MethodHEAD && profile == ProfileFull {
		return ProfileSimpleHTTP
	}
	return profile
}

// FullDetectionsEnabled reports whether rich v2 detections should run.
func FullDetectionsEnabled(method, profile string) bool {
	return EffectiveProfile(method, profile) == ProfileFull
}

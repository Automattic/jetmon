package main

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestAPISitesListPath(t *testing.T) {
	got, err := apiSitesListPath(apiSitesListFilters{
		cursor:        "cur-1",
		limit:         25,
		stateIn:       "Down,Seems Down",
		severityGTE:   3,
		monitorActive: "1",
		q:             "example.com",
	})
	if err != nil {
		t.Fatalf("apiSitesListPath() error = %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	if u.Path != "/api/v1/sites" {
		t.Fatalf("path = %q, want /api/v1/sites", u.Path)
	}
	q := u.Query()
	for key, want := range map[string]string{
		"cursor":         "cur-1",
		"limit":          "25",
		"state__in":      "Down,Seems Down",
		"severity__gte":  "3",
		"monitor_active": "true",
		"q":              "example.com",
	} {
		if got := q.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q in %s", key, got, want, got)
		}
	}
}

func TestAPISitesListPathRejectsAmbiguousStateFilter(t *testing.T) {
	_, err := apiSitesListPath(apiSitesListFilters{state: "Down", stateIn: "Up,Down"})
	if err == nil {
		t.Fatal("apiSitesListPath() error = nil, want error")
	}
}

func TestAPISiteResourcePath(t *testing.T) {
	got, err := apiSiteResourcePath("42", "trigger-now")
	if err != nil {
		t.Fatalf("apiSiteResourcePath() error = %v", err)
	}
	if got != "/api/v1/sites/42/trigger-now" {
		t.Fatalf("path = %q, want trigger-now path", got)
	}
	if _, err := apiSiteResourcePath("0", ""); err == nil {
		t.Fatal("apiSiteResourcePath() error = nil, want invalid id error")
	}
}

func TestMarshalAPISiteCreateBody(t *testing.T) {
	var active apiOptionalBoolFlag
	setTestFlag(t, &active, "false")
	var bucket apiOptionalIntFlag
	setTestFlag(t, &bucket, "7")
	var redirect apiOptionalStringFlag
	setTestFlag(t, &redirect, "alert")
	var headers apiStringMapFlags
	setTestFlag(t, &headers, "X-Jetmon-Test: yes")
	var forbiddenKeywords apiStringSliceFlags
	setTestFlag(t, &forbiddenKeywords, "metrics.evil-cdn.example/collect.js")
	setTestFlag(t, &forbiddenKeywords, "buy cheap viagra")

	body, err := marshalAPISiteCreateBody(apiSiteCreateOptions{
		blogID:            12345,
		monitorURL:        "https://example.com",
		monitorActive:     active,
		bucketNo:          bucket,
		forbiddenKeywords: forbiddenKeywords,
		redirectPolicy:    redirect,
		customHeaders:     headers,
	})
	if err != nil {
		t.Fatalf("marshalAPISiteCreateBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got["blog_id"] != float64(12345) {
		t.Fatalf("blog_id = %#v, want 12345", got["blog_id"])
	}
	if got["monitor_url"] != "https://example.com" {
		t.Fatalf("monitor_url = %#v", got["monitor_url"])
	}
	if got["monitor_active"] != false {
		t.Fatalf("monitor_active = %#v, want false", got["monitor_active"])
	}
	if got["bucket_no"] != float64(7) {
		t.Fatalf("bucket_no = %#v, want 7", got["bucket_no"])
	}
	if got["redirect_policy"] != "alert" {
		t.Fatalf("redirect_policy = %#v, want alert", got["redirect_policy"])
	}
	assertStringArray(t, got["forbidden_keywords"], []string{"metrics.evil-cdn.example/collect.js", "buy cheap viagra"})
	custom, ok := got["custom_headers"].(map[string]any)
	if !ok {
		t.Fatalf("custom_headers = %#v, want object", got["custom_headers"])
	}
	if custom["X-Jetmon-Test"] != "yes" {
		t.Fatalf("custom header = %#v, want yes", custom["X-Jetmon-Test"])
	}
}

func TestMarshalAPISiteUpdateBodySupportsClears(t *testing.T) {
	var keyword apiOptionalStringFlag
	setTestFlag(t, &keyword, "")
	var maintenanceEnd apiOptionalStringFlag
	setTestFlag(t, &maintenanceEnd, "")

	body, err := marshalAPISiteUpdateBody(apiSiteUpdateOptions{
		checkKeyword:           keyword,
		clearCustomHeaders:     true,
		clearForbiddenKeywords: true,
		maintenanceEnd:         maintenanceEnd,
	})
	if err != nil {
		t.Fatalf("marshalAPISiteUpdateBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got["check_keyword"] != "" {
		t.Fatalf("check_keyword = %#v, want empty string", got["check_keyword"])
	}
	if got["maintenance_end"] != "" {
		t.Fatalf("maintenance_end = %#v, want empty string", got["maintenance_end"])
	}
	custom, ok := got["custom_headers"].(map[string]any)
	if !ok {
		t.Fatalf("custom_headers = %#v, want object", got["custom_headers"])
	}
	if len(custom) != 0 {
		t.Fatalf("custom_headers = %#v, want empty object", custom)
	}
	assertStringArray(t, got["forbidden_keywords"], []string{})
}

func TestMarshalAPISiteUpdateBodyRejectsCustomHeaderConflict(t *testing.T) {
	var headers apiStringMapFlags
	setTestFlag(t, &headers, "X-Test: yes")
	_, err := marshalAPISiteUpdateBody(apiSiteUpdateOptions{
		customHeaders:      headers,
		clearCustomHeaders: true,
	})
	if err == nil {
		t.Fatal("marshalAPISiteUpdateBody() error = nil, want conflict error")
	}
}

func TestMarshalAPISiteUpdateBodyRejectsForbiddenKeywordConflict(t *testing.T) {
	var forbiddenKeywords apiStringSliceFlags
	setTestFlag(t, &forbiddenKeywords, "bad")
	_, err := marshalAPISiteUpdateBody(apiSiteUpdateOptions{
		forbiddenKeywords:      forbiddenKeywords,
		clearForbiddenKeywords: true,
	})
	if err == nil {
		t.Fatal("marshalAPISiteUpdateBody() error = nil, want conflict error")
	}
}

func setTestFlag(t *testing.T, v interface{ Set(string) error }, raw string) {
	t.Helper()
	if err := v.Set(raw); err != nil {
		t.Fatalf("Set(%q) error = %v", raw, err)
	}
}

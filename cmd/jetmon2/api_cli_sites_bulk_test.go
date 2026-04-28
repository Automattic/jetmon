package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseAPIBulkJSONSiteEntries(t *testing.T) {
	entries, err := parseAPIBulkSiteEntries([]byte(`[
		"https://example.com/",
		{"url":"https://wordpress.com/","check_keyword":"WordPress","redirect_policy":"follow","timeout_seconds":5}
	]`))
	if err != nil {
		t.Fatalf("parseAPIBulkSiteEntries() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].MonitorURL != "https://example.com/" {
		t.Fatalf("first URL = %q", entries[0].MonitorURL)
	}
	if entries[1].MonitorURL != "https://wordpress.com/" {
		t.Fatalf("second URL = %q", entries[1].MonitorURL)
	}
	if entries[1].CheckKeyword == nil || *entries[1].CheckKeyword != "WordPress" {
		t.Fatalf("check_keyword = %#v, want WordPress", entries[1].CheckKeyword)
	}
	if entries[1].TimeoutSeconds == nil || *entries[1].TimeoutSeconds != 5 {
		t.Fatalf("timeout_seconds = %#v, want 5", entries[1].TimeoutSeconds)
	}
}

func TestParseAPIBulkCSVSiteEntries(t *testing.T) {
	source := strings.NewReader("monitor_url,check_keyword,redirect_policy,check_interval\nhttps://example.com/,Example Domain,follow,5\n")
	entries, err := loadAPIBulkSiteEntries(apiSitesBulkAddOptions{source: "stdin"}, source)
	if err != nil {
		t.Fatalf("loadAPIBulkSiteEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].MonitorURL != "https://example.com/" {
		t.Fatalf("monitor_url = %q", entries[0].MonitorURL)
	}
	if entries[0].CheckKeyword == nil || *entries[0].CheckKeyword != "Example Domain" {
		t.Fatalf("check_keyword = %#v, want Example Domain", entries[0].CheckKeyword)
	}
	if entries[0].CheckInterval == nil || *entries[0].CheckInterval != 5 {
		t.Fatalf("check_interval = %#v, want 5", entries[0].CheckInterval)
	}
}

func TestParseAPIBulkNewlineSiteEntries(t *testing.T) {
	entries, err := parseAPIBulkSiteEntries([]byte("https://example.com/\nhttps://wordpress.com/\n"))
	if err != nil {
		t.Fatalf("parseAPIBulkSiteEntries() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[1].MonitorURL != "https://wordpress.com/" {
		t.Fatalf("second URL = %q", entries[1].MonitorURL)
	}
}

func TestPlanAPIBulkSiteCreatesCyclesFixtureEntries(t *testing.T) {
	var active apiOptionalBoolFlag
	setTestFlag(t, &active, "false")
	entries := []apiBulkSiteEntry{
		{MonitorURL: "https://example.com/"},
		{MonitorURL: "https://wordpress.com/"},
	}
	planned, err := planAPIBulkSiteCreates(entries, apiSitesBulkAddOptions{
		count:         3,
		blogIDStart:   900,
		monitorActive: active,
	})
	if err != nil {
		t.Fatalf("planAPIBulkSiteCreates() error = %v", err)
	}
	if len(planned) != 3 {
		t.Fatalf("len(planned) = %d, want 3", len(planned))
	}
	if planned[0].BlogID != 900 || planned[2].BlogID != 902 {
		t.Fatalf("blog ids = %d, %d; want 900, 902", planned[0].BlogID, planned[2].BlogID)
	}
	if planned[2].MonitorURL != "https://example.com/" {
		t.Fatalf("cycled URL = %q, want first source URL", planned[2].MonitorURL)
	}
	if planned[0].MonitorActive == nil || *planned[0].MonitorActive {
		t.Fatalf("monitor_active = %#v, want false", planned[0].MonitorActive)
	}
}

func TestPlanAPIBulkSiteCreatesUsesBatchMarker(t *testing.T) {
	entries := []apiBulkSiteEntry{{MonitorURL: "https://example.com/"}}
	planned, err := planAPIBulkSiteCreates(entries, apiSitesBulkAddOptions{
		count:       1,
		batch:       "batch-a",
		blogIDStart: defaultAPIBulkAddBlogIDStart,
	})
	if err != nil {
		t.Fatalf("planAPIBulkSiteCreates() error = %v", err)
	}
	if planned[0].BlogID != apiCLIBatchBlogIDStart("batch-a") {
		t.Fatalf("blog_id = %d, want batch-derived id", planned[0].BlogID)
	}
	if planned[0].CustomHeaders == nil || (*planned[0].CustomHeaders)[apiCLIBatchHeader] != "batch-a" {
		t.Fatalf("custom headers = %#v, want batch marker", planned[0].CustomHeaders)
	}
}

func TestPlanAPIBulkSiteCreatesRejectsUnboundedCount(t *testing.T) {
	_, err := planAPIBulkSiteCreates([]apiBulkSiteEntry{{MonitorURL: "https://example.com/"}}, apiSitesBulkAddOptions{
		count:       apiSitesBulkAddMaxCount + 1,
		blogIDStart: 900,
	})
	if err == nil {
		t.Fatal("planAPIBulkSiteCreates() error = nil, want max count error")
	}
}

func TestLoadAPIBulkFixture(t *testing.T) {
	entries, err := loadAPIBulkSiteEntries(apiSitesBulkAddOptions{source: "fixture"}, nil)
	if err != nil {
		t.Fatalf("load fixture error = %v", err)
	}
	if len(entries) < 8 {
		t.Fatalf("fixture entries = %d, want at least 8", len(entries))
	}
}

func TestMarshalAPIBulkSiteRequests(t *testing.T) {
	keyword := "Example Domain"
	requests := []apiSiteCreateRequest{{
		BlogID:       900,
		MonitorURL:   "https://example.com/",
		CheckKeyword: &keyword,
	}}
	raw, err := marshalAPIBulkSiteRequests(requests)
	if err != nil {
		t.Fatalf("marshalAPIBulkSiteRequests() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw[0], &got); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if got["blog_id"] != float64(900) {
		t.Fatalf("blog_id = %#v, want 900", got["blog_id"])
	}
	if got["check_keyword"] != "Example Domain" {
		t.Fatalf("check_keyword = %#v, want Example Domain", got["check_keyword"])
	}
}

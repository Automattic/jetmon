package main

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestMarshalAPIWebhookCreateBody(t *testing.T) {
	var active apiOptionalBoolFlag
	setTestFlag(t, &active, "false")
	var events apiStringSliceFlags
	setTestFlag(t, &events, "event.opened,event.closed")
	var siteIDs apiInt64SliceFlags
	setTestFlag(t, &siteIDs, "42,99")
	var states apiStringSliceFlags
	setTestFlag(t, &states, "Down")

	body, err := marshalAPIWebhookCreateBody(apiWebhookCreateOptions{
		url:     "https://example.com/hook",
		active:  active,
		events:  events,
		siteIDs: siteIDs,
		states:  states,
	})
	if err != nil {
		t.Fatalf("marshalAPIWebhookCreateBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got["url"] != "https://example.com/hook" {
		t.Fatalf("url = %#v", got["url"])
	}
	if got["active"] != false {
		t.Fatalf("active = %#v, want false", got["active"])
	}
	assertStringArray(t, got["events"], []string{"event.opened", "event.closed"})
	siteFilter := got["site_filter"].(map[string]any)
	assertNumberArray(t, siteFilter["site_ids"], []int64{42, 99})
	stateFilter := got["state_filter"].(map[string]any)
	assertStringArray(t, stateFilter["states"], []string{"Down"})
}

func TestMarshalAPIWebhookCreateBodyDefaultsFiltersToMatchAll(t *testing.T) {
	body, err := marshalAPIWebhookCreateBody(apiWebhookCreateOptions{
		url: "https://example.com/hook",
	})
	if err != nil {
		t.Fatalf("marshalAPIWebhookCreateBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	assertStringArray(t, got["events"], []string{})
	if _, ok := got["site_filter"].(map[string]any)["site_ids"]; ok {
		t.Fatalf("site_ids present in empty site_filter: %#v", got["site_filter"])
	}
	if _, ok := got["state_filter"].(map[string]any)["states"]; ok {
		t.Fatalf("states present in empty state_filter: %#v", got["state_filter"])
	}
}

func TestMarshalAPIWebhookUpdateBodySupportsClears(t *testing.T) {
	body, err := marshalAPIWebhookUpdateBody(apiWebhookUpdateOptions{
		clearEvents: true,
		clearSites:  true,
		clearStates: true,
	})
	if err != nil {
		t.Fatalf("marshalAPIWebhookUpdateBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	assertStringArray(t, got["events"], []string{})
	if _, ok := got["site_filter"].(map[string]any)["site_ids"]; ok {
		t.Fatalf("site_ids present in cleared site_filter: %#v", got["site_filter"])
	}
	if _, ok := got["state_filter"].(map[string]any)["states"]; ok {
		t.Fatalf("states present in cleared state_filter: %#v", got["state_filter"])
	}
}

func TestMarshalAPIWebhookUpdateBodyRejectsClearConflicts(t *testing.T) {
	var events apiStringSliceFlags
	setTestFlag(t, &events, "event.opened")
	if _, err := marshalAPIWebhookUpdateBody(apiWebhookUpdateOptions{events: events, clearEvents: true}); err == nil {
		t.Fatal("events conflict error = nil, want error")
	}

	var siteIDs apiInt64SliceFlags
	setTestFlag(t, &siteIDs, "42")
	if _, err := marshalAPIWebhookUpdateBody(apiWebhookUpdateOptions{siteIDs: siteIDs, clearSites: true}); err == nil {
		t.Fatal("sites conflict error = nil, want error")
	}

	var states apiStringSliceFlags
	setTestFlag(t, &states, "Down")
	if _, err := marshalAPIWebhookUpdateBody(apiWebhookUpdateOptions{states: states, clearStates: true}); err == nil {
		t.Fatal("states conflict error = nil, want error")
	}
}

func TestAPIWebhookPaths(t *testing.T) {
	got, err := apiWebhookPath("7", "rotate-secret")
	if err != nil {
		t.Fatalf("apiWebhookPath() error = %v", err)
	}
	if got != "/api/v1/webhooks/7/rotate-secret" {
		t.Fatalf("path = %q, want rotate-secret path", got)
	}

	got, err = apiWebhookRetryPath("7", "44")
	if err != nil {
		t.Fatalf("apiWebhookRetryPath() error = %v", err)
	}
	if got != "/api/v1/webhooks/7/deliveries/44/retry" {
		t.Fatalf("retry path = %q, want delivery retry path", got)
	}
}

func TestAPIWebhookDeliveriesPath(t *testing.T) {
	got, err := apiWebhookDeliveriesPath("7", apiWebhookDeliveriesFilters{
		cursor: "cur-4",
		limit:  25,
		status: "abandoned",
	})
	if err != nil {
		t.Fatalf("apiWebhookDeliveriesPath() error = %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	if u.Path != "/api/v1/webhooks/7/deliveries" {
		t.Fatalf("path = %q, want deliveries path", u.Path)
	}
	for key, want := range map[string]string{
		"cursor": "cur-4",
		"limit":  "25",
		"status": "abandoned",
	} {
		if got := u.Query().Get(key); got != want {
			t.Fatalf("query %s = %q, want %q", key, got, want)
		}
	}
}

func TestAPIWebhookDeliveriesPathRejectsBadStatus(t *testing.T) {
	_, err := apiWebhookDeliveriesPath("7", apiWebhookDeliveriesFilters{status: "waiting"})
	if err == nil {
		t.Fatal("apiWebhookDeliveriesPath() error = nil, want bad status error")
	}
}

func assertStringArray(t *testing.T, got any, want []string) {
	t.Helper()
	items, ok := got.([]any)
	if !ok {
		t.Fatalf("value = %#v, want JSON array", got)
	}
	if len(items) != len(want) {
		t.Fatalf("array len = %d, want %d: %#v", len(items), len(want), items)
	}
	for i, wantItem := range want {
		if items[i] != wantItem {
			t.Fatalf("array[%d] = %#v, want %q", i, items[i], wantItem)
		}
	}
}

func assertNumberArray(t *testing.T, got any, want []int64) {
	t.Helper()
	items, ok := got.([]any)
	if !ok {
		t.Fatalf("value = %#v, want JSON array", got)
	}
	if len(items) != len(want) {
		t.Fatalf("array len = %d, want %d: %#v", len(items), len(want), items)
	}
	for i, wantItem := range want {
		if items[i] != float64(wantItem) {
			t.Fatalf("array[%d] = %#v, want %d", i, items[i], wantItem)
		}
	}
}

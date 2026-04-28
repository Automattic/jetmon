package main

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestMarshalAPIAlertContactCreateBody(t *testing.T) {
	var active apiOptionalBoolFlag
	setTestFlag(t, &active, "false")
	var siteIDs apiInt64SliceFlags
	setTestFlag(t, &siteIDs, "42,99")
	var minSeverity apiOptionalStringFlag
	setTestFlag(t, &minSeverity, "Warning")
	var maxPerHour apiOptionalIntFlag
	setTestFlag(t, &maxPerHour, "0")

	body, err := marshalAPIAlertContactCreateBody(apiAlertContactCreateOptions{
		label:       "ops-email",
		active:      active,
		transport:   "email",
		destination: apiAlertDestinationOptions{address: "ops@example.com"},
		siteIDs:     siteIDs,
		minSeverity: minSeverity,
		maxPerHour:  maxPerHour,
	})
	if err != nil {
		t.Fatalf("marshalAPIAlertContactCreateBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got["label"] != "ops-email" {
		t.Fatalf("label = %#v", got["label"])
	}
	if got["active"] != false {
		t.Fatalf("active = %#v, want false", got["active"])
	}
	if got["transport"] != "email" {
		t.Fatalf("transport = %#v, want email", got["transport"])
	}
	dest := got["destination"].(map[string]any)
	if dest["address"] != "ops@example.com" {
		t.Fatalf("destination.address = %#v", dest["address"])
	}
	siteFilter := got["site_filter"].(map[string]any)
	assertNumberArray(t, siteFilter["site_ids"], []int64{42, 99})
	if got["min_severity"] != "Warning" {
		t.Fatalf("min_severity = %#v, want Warning", got["min_severity"])
	}
	if got["max_per_hour"] != float64(0) {
		t.Fatalf("max_per_hour = %#v, want 0", got["max_per_hour"])
	}
}

func TestMarshalAPIAlertContactCreateBodyBuildsTransportDestinations(t *testing.T) {
	tests := []struct {
		name        string
		transport   string
		destination apiAlertDestinationOptions
		wantKey     string
		wantValue   string
	}{
		{
			name:        "pagerduty",
			transport:   "pagerduty",
			destination: apiAlertDestinationOptions{integrationKey: "pd-key"},
			wantKey:     "integration_key",
			wantValue:   "pd-key",
		},
		{
			name:        "slack",
			transport:   "slack",
			destination: apiAlertDestinationOptions{webhookURL: "https://hooks.slack.com/services/test"},
			wantKey:     "webhook_url",
			wantValue:   "https://hooks.slack.com/services/test",
		},
		{
			name:        "teams",
			transport:   "teams",
			destination: apiAlertDestinationOptions{webhookURL: "https://outlook.office.com/webhook/test"},
			wantKey:     "webhook_url",
			wantValue:   "https://outlook.office.com/webhook/test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := marshalAPIAlertContactCreateBody(apiAlertContactCreateOptions{
				label:       tt.name,
				transport:   tt.transport,
				destination: tt.destination,
			})
			if err != nil {
				t.Fatalf("marshalAPIAlertContactCreateBody() error = %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			dest := got["destination"].(map[string]any)
			if dest[tt.wantKey] != tt.wantValue {
				t.Fatalf("destination[%s] = %#v, want %q", tt.wantKey, dest[tt.wantKey], tt.wantValue)
			}
		})
	}
}

func TestMarshalAPIAlertContactUpdateBodySupportsDestinationAndClearSites(t *testing.T) {
	var label apiOptionalStringFlag
	setTestFlag(t, &label, "platform-oncall")

	body, err := marshalAPIAlertContactUpdateBody(apiAlertContactUpdateOptions{
		label:       label,
		destination: apiAlertDestinationOptions{raw: `{"webhook_url":"https://example.com/hook"}`},
		clearSites:  true,
	})
	if err != nil {
		t.Fatalf("marshalAPIAlertContactUpdateBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got["label"] != "platform-oncall" {
		t.Fatalf("label = %#v", got["label"])
	}
	dest := got["destination"].(map[string]any)
	if dest["webhook_url"] != "https://example.com/hook" {
		t.Fatalf("destination.webhook_url = %#v", dest["webhook_url"])
	}
	if _, ok := got["site_filter"].(map[string]any)["site_ids"]; ok {
		t.Fatalf("site_ids present in cleared site_filter: %#v", got["site_filter"])
	}
}

func TestMarshalAPIAlertContactUpdateBodyRejectsConflicts(t *testing.T) {
	var siteIDs apiInt64SliceFlags
	setTestFlag(t, &siteIDs, "42")
	if _, err := marshalAPIAlertContactUpdateBody(apiAlertContactUpdateOptions{siteIDs: siteIDs, clearSites: true}); err == nil {
		t.Fatal("site filter conflict error = nil, want error")
	}
	if _, err := (apiAlertDestinationOptions{raw: `{}`, address: "ops@example.com"}).rawForTransport("", false); err == nil {
		t.Fatal("destination conflict error = nil, want error")
	}
	if _, err := (apiAlertDestinationOptions{raw: `{not-json}`}).rawForTransport("", false); err == nil {
		t.Fatal("invalid raw destination error = nil, want error")
	}
}

func TestAPIAlertContactPaths(t *testing.T) {
	got, err := apiAlertContactPath("17", "test")
	if err != nil {
		t.Fatalf("apiAlertContactPath() error = %v", err)
	}
	if got != "/api/v1/alert-contacts/17/test" {
		t.Fatalf("path = %q, want test path", got)
	}

	got, err = apiAlertContactRetryPath("17", "88")
	if err != nil {
		t.Fatalf("apiAlertContactRetryPath() error = %v", err)
	}
	if got != "/api/v1/alert-contacts/17/deliveries/88/retry" {
		t.Fatalf("retry path = %q, want delivery retry path", got)
	}
}

func TestAPIAlertContactDeliveriesPath(t *testing.T) {
	got, err := apiAlertContactDeliveriesPath("17", apiAlertDeliveriesFilters{
		cursor: "cur-5",
		limit:  50,
		status: "failed",
	})
	if err != nil {
		t.Fatalf("apiAlertContactDeliveriesPath() error = %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	if u.Path != "/api/v1/alert-contacts/17/deliveries" {
		t.Fatalf("path = %q, want deliveries path", u.Path)
	}
	for key, want := range map[string]string{
		"cursor": "cur-5",
		"limit":  "50",
		"status": "failed",
	} {
		if got := u.Query().Get(key); got != want {
			t.Fatalf("query %s = %q, want %q", key, got, want)
		}
	}
}

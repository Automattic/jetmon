package main

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestAPIEventsListPath(t *testing.T) {
	got, err := apiEventsListPath("42", apiEventsListFilters{
		cursor:       "cur-2",
		limit:        20,
		state:        "Down",
		checkTypeIn:  "http,tls_expiry",
		startedAtGTE: "2026-04-28T10:00:00Z",
		startedAtLT:  "2026-04-29T10:00:00Z",
		active:       "true",
	})
	if err != nil {
		t.Fatalf("apiEventsListPath() error = %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	if u.Path != "/api/v1/sites/42/events" {
		t.Fatalf("path = %q, want site events path", u.Path)
	}
	q := u.Query()
	for key, want := range map[string]string{
		"cursor":          "cur-2",
		"limit":           "20",
		"state":           "Down",
		"check_type__in":  "http,tls_expiry",
		"started_at__gte": "2026-04-28T10:00:00Z",
		"started_at__lt":  "2026-04-29T10:00:00Z",
		"active":          "true",
	} {
		if got := q.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q", key, got, want)
		}
	}
}

func TestAPIEventsListPathRejectsAmbiguousFilters(t *testing.T) {
	if _, err := apiEventsListPath("42", apiEventsListFilters{state: "Down", stateIn: "Up,Down"}); err == nil {
		t.Fatal("apiEventsListPath() state error = nil, want error")
	}
	if _, err := apiEventsListPath("42", apiEventsListFilters{checkType: "http", checkTypeIn: "http,tls"}); err == nil {
		t.Fatal("apiEventsListPath() check type error = nil, want error")
	}
}

func TestAPIEventDetailPath(t *testing.T) {
	got, err := apiEventDetailPath("", "99")
	if err != nil {
		t.Fatalf("apiEventDetailPath() direct error = %v", err)
	}
	if got != "/api/v1/events/99" {
		t.Fatalf("direct path = %q, want /api/v1/events/99", got)
	}

	got, err = apiEventDetailPath("42", "99")
	if err != nil {
		t.Fatalf("apiEventDetailPath() scoped error = %v", err)
	}
	if got != "/api/v1/sites/42/events/99" {
		t.Fatalf("scoped path = %q, want site-scoped event path", got)
	}
}

func TestAPIEventTransitionsPath(t *testing.T) {
	got, err := apiEventTransitionsPath("42", "99", apiTransitionsListFilters{
		cursor: "cur-3",
		limit:  100,
	})
	if err != nil {
		t.Fatalf("apiEventTransitionsPath() error = %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse path: %v", err)
	}
	if u.Path != "/api/v1/sites/42/events/99/transitions" {
		t.Fatalf("path = %q, want transitions path", u.Path)
	}
	if got := u.Query().Get("cursor"); got != "cur-3" {
		t.Fatalf("cursor = %q, want cur-3", got)
	}
	if got := u.Query().Get("limit"); got != "100" {
		t.Fatalf("limit = %q, want 100", got)
	}
}

func TestAPIEventClosePath(t *testing.T) {
	got, err := apiEventClosePath("42", "99")
	if err != nil {
		t.Fatalf("apiEventClosePath() error = %v", err)
	}
	if got != "/api/v1/sites/42/events/99/close" {
		t.Fatalf("path = %q, want close path", got)
	}
}

func TestMarshalAPIEventCloseBody(t *testing.T) {
	body, err := marshalAPIEventCloseBody(apiEventCloseOptions{
		reason: "false_alarm",
		note:   "verified from dashboard",
	})
	if err != nil {
		t.Fatalf("marshalAPIEventCloseBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got["reason"] != "false_alarm" {
		t.Fatalf("reason = %#v, want false_alarm", got["reason"])
	}
	if got["note"] != "verified from dashboard" {
		t.Fatalf("note = %#v, want dashboard note", got["note"])
	}

	body, err = marshalAPIEventCloseBody(apiEventCloseOptions{})
	if err != nil {
		t.Fatalf("marshalAPIEventCloseBody(empty) error = %v", err)
	}
	if string(body) != "{}" {
		t.Fatalf("empty body = %s, want {}", body)
	}
}

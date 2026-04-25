package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGenerateSecretShape(t *testing.T) {
	raw, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if !strings.HasPrefix(raw, SecretPrefix) {
		t.Fatalf("missing prefix: %q", raw)
	}
	// 32 random bytes → 52 base32 chars (no padding) + len(SecretPrefix).
	if len(raw) != len(SecretPrefix)+52 {
		t.Errorf("raw length = %d, want %d", len(raw), len(SecretPrefix)+52)
	}
}

func TestGenerateSecretUnique(t *testing.T) {
	a, _ := GenerateSecret()
	b, _ := GenerateSecret()
	if a == b {
		t.Fatal("two generated secrets collided")
	}
}

func TestSignDeterministicWithSameInputs(t *testing.T) {
	ts := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"event":"event.opened","id":42}`)
	a := Sign(ts, body, "whsec_TESTSECRET")
	b := Sign(ts, body, "whsec_TESTSECRET")
	if a != b {
		t.Errorf("Sign should be deterministic; got %q vs %q", a, b)
	}
}

func TestSignFormat(t *testing.T) {
	ts := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"hello":"world"}`)
	secret := "whsec_TESTSECRET"
	got := Sign(ts, body, secret)
	if !strings.HasPrefix(got, "t=") {
		t.Errorf("signature = %q, want prefix t=", got)
	}
	if !strings.Contains(got, ",v1=") {
		t.Errorf("signature = %q, want ,v1=", got)
	}
	// Compute the expected signature independently — same algorithm but with
	// the timestamp pulled from ts so the test stays correct under any clock.
	tsStr := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(tsStr))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := "t=" + tsStr + ",v1=" + hex.EncodeToString(mac.Sum(nil))
	if got != expected {
		t.Errorf("Sign computed unexpectedly\n got: %s\nwant: %s", got, expected)
	}
}

func TestSignDiffersOnTimestamp(t *testing.T) {
	t1 := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(1 * time.Second)
	body := []byte(`{}`)
	a := Sign(t1, body, "whsec_x")
	b := Sign(t2, body, "whsec_x")
	if a == b {
		t.Errorf("signature should change with timestamp; both = %q", a)
	}
}

func TestSignDiffersOnSecret(t *testing.T) {
	ts := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	body := []byte(`{}`)
	if Sign(ts, body, "whsec_a") == Sign(ts, body, "whsec_b") {
		t.Error("signature should differ between secrets")
	}
}

func TestEventTypeForReason(t *testing.T) {
	cases := map[string]string{
		"opened":                EventOpened,
		"severity_escalation":   EventSeverityChanged,
		"severity_deescalation": EventSeverityChanged,
		"state_change":          EventStateChanged,
		"verifier_confirmed":    EventStateChanged,
		"cause_linked":          EventCauseLinked,
		"cause_unlinked":        EventCauseUnlinked,
		"verifier_cleared":      EventClosed,
		"probe_cleared":         EventClosed,
		"false_alarm":           EventClosed,
		"manual_override":       EventClosed,
		"maintenance_swallowed": EventClosed,
		"superseded":            EventClosed,
		"auto_timeout":          EventClosed,
		"unknown_reason":        "",
		"":                      "",
	}
	for reason, want := range cases {
		got := EventTypeForReason(reason)
		if got != want {
			t.Errorf("EventTypeForReason(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestWebhookMatchesAllFiltersEmpty(t *testing.T) {
	// No filters set — webhook should match everything.
	w := &Webhook{Active: true}
	if !w.Matches(EventOpened, 12345, "Down") {
		t.Error("empty filters should match all events")
	}
	if !w.Matches(EventClosed, 99999, "Up") {
		t.Error("empty filters should match unrelated event/state")
	}
}

func TestWebhookMatchesInactive(t *testing.T) {
	w := &Webhook{Active: false}
	if w.Matches(EventOpened, 1, "Down") {
		t.Error("inactive webhook should never match")
	}
}

func TestWebhookMatchesEventFilter(t *testing.T) {
	w := &Webhook{
		Active: true,
		Events: []string{EventOpened, EventClosed},
	}
	if !w.Matches(EventOpened, 1, "Down") {
		t.Error("event in filter should match")
	}
	if w.Matches(EventSeverityChanged, 1, "Down") {
		t.Error("event not in filter should not match")
	}
}

func TestWebhookMatchesSiteFilter(t *testing.T) {
	w := &Webhook{
		Active:     true,
		SiteFilter: SiteFilter{SiteIDs: []int64{101, 102}},
	}
	if !w.Matches(EventOpened, 101, "Down") {
		t.Error("site in filter should match")
	}
	if w.Matches(EventOpened, 999, "Down") {
		t.Error("site not in filter should not match")
	}
}

func TestWebhookMatchesStateFilter(t *testing.T) {
	w := &Webhook{
		Active:      true,
		StateFilter: StateFilter{States: []string{"Down", "Seems Down"}},
	}
	if !w.Matches(EventOpened, 1, "Down") {
		t.Error("state in filter should match")
	}
	if w.Matches(EventOpened, 1, "Warning") {
		t.Error("state not in filter should not match")
	}
}

func TestWebhookMatchesAllDimensions(t *testing.T) {
	// All three filters set — must AND across dimensions.
	w := &Webhook{
		Active:      true,
		Events:      []string{EventOpened},
		SiteFilter:  SiteFilter{SiteIDs: []int64{42}},
		StateFilter: StateFilter{States: []string{"Down"}},
	}
	if !w.Matches(EventOpened, 42, "Down") {
		t.Error("all three dimensions match → should fire")
	}
	if w.Matches(EventClosed, 42, "Down") {
		t.Error("event mismatch → should not fire (AND semantics)")
	}
	if w.Matches(EventOpened, 99, "Down") {
		t.Error("site mismatch → should not fire (AND semantics)")
	}
	if w.Matches(EventOpened, 42, "Up") {
		t.Error("state mismatch → should not fire (AND semantics)")
	}
}

func TestPreviewOf(t *testing.T) {
	if got := previewOf("whsec_LONG_SECRET_VALUE_XYZ"); got != "_XYZ" {
		t.Errorf("previewOf long = %q, want _XYZ", got)
	}
	if got := previewOf("ab"); got != "ab" {
		t.Errorf("previewOf short = %q, want ab", got)
	}
}

func TestValidateEventsRejectsUnknown(t *testing.T) {
	if err := validateEvents([]string{EventOpened, "event.bogus"}); err == nil {
		t.Error("unknown event type should be rejected")
	}
	if err := validateEvents([]string{EventOpened, EventClosed}); err != nil {
		t.Errorf("known events rejected: %v", err)
	}
	if err := validateEvents(nil); err != nil {
		t.Errorf("empty events list rejected: %v", err)
	}
}

func TestAllEventTypesIsCanonical(t *testing.T) {
	all := AllEventTypes()
	expected := []string{
		EventOpened, EventSeverityChanged, EventStateChanged,
		EventCauseLinked, EventCauseUnlinked, EventClosed,
	}
	if len(all) != len(expected) {
		t.Fatalf("AllEventTypes() len = %d, want %d", len(all), len(expected))
	}
	for i, e := range expected {
		if all[i] != e {
			t.Errorf("AllEventTypes()[%d] = %q, want %q", i, all[i], e)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate(short) = %q", got)
	}
	if got := truncate("hello world", 5); got != "hello" {
		t.Errorf("truncate(long) = %q", got)
	}
}

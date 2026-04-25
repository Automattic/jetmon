package alerting

import (
	"testing"

	"github.com/Automattic/jetmon/internal/eventstore"
)

func TestSeverityNameRoundTrip(t *testing.T) {
	for _, name := range AllSeverityNames() {
		s, err := SeverityFromName(name)
		if err != nil {
			t.Errorf("SeverityFromName(%q) returned error: %v", name, err)
			continue
		}
		if got := SeverityName(s); got != name {
			t.Errorf("round-trip %q → %d → %q failed", name, s, got)
		}
	}
}

func TestSeverityNameUnknown(t *testing.T) {
	if got := SeverityName(99); got != "" {
		t.Errorf("SeverityName(99) = %q, want empty string", got)
	}
	if _, err := SeverityFromName("Bogus"); err == nil {
		t.Error("SeverityFromName(\"Bogus\") should error")
	}
}

func TestIsValidTransport(t *testing.T) {
	for _, valid := range []string{"email", "pagerduty", "slack", "teams"} {
		if !IsValidTransport(valid) {
			t.Errorf("IsValidTransport(%q) = false, want true", valid)
		}
	}
	for _, bad := range []string{"", "Email", "sms", "opsgenie", "EMAIL"} {
		if IsValidTransport(bad) {
			t.Errorf("IsValidTransport(%q) = true, want false", bad)
		}
	}
}

// TestMatchesInactive verifies an inactive contact never fires regardless
// of severity — a deactivated contact should be invisible to the worker.
func TestMatchesInactive(t *testing.T) {
	c := &AlertContact{
		Active:      false,
		MinSeverity: eventstore.SeverityWarning,
	}
	if c.Matches(eventstore.SeverityUp, eventstore.SeverityDown, 1) {
		t.Error("inactive contact should not match")
	}
}

// TestMatchesEmptySiteFilter verifies an empty site filter matches all sites
// — the documented "empty = match all" semantic.
func TestMatchesEmptySiteFilter(t *testing.T) {
	c := &AlertContact{
		Active:      true,
		MinSeverity: eventstore.SeverityDown,
		// SiteFilter is zero value → empty SiteIDs → match all.
	}
	for _, siteID := range []int64{1, 42, 99999} {
		if !c.Matches(eventstore.SeverityUp, eventstore.SeverityDown, siteID) {
			t.Errorf("empty site filter should match site %d", siteID)
		}
	}
}

func TestMatchesSiteFilterWhitelist(t *testing.T) {
	c := &AlertContact{
		Active:      true,
		SiteFilter:  SiteFilter{SiteIDs: []int64{42, 99}},
		MinSeverity: eventstore.SeverityDown,
	}
	if !c.Matches(eventstore.SeverityUp, eventstore.SeverityDown, 42) {
		t.Error("site 42 should match")
	}
	if !c.Matches(eventstore.SeverityUp, eventstore.SeverityDown, 99) {
		t.Error("site 99 should match")
	}
	if c.Matches(eventstore.SeverityUp, eventstore.SeverityDown, 7) {
		t.Error("site 7 should not match (not in whitelist)")
	}
}

// TestMatchesSeverityGate covers the escalation half of the gate:
// new_severity >= min_severity fires, regardless of prev_severity.
func TestMatchesSeverityGate(t *testing.T) {
	c := &AlertContact{
		Active:      true,
		MinSeverity: eventstore.SeverityDegraded, // 2
	}
	cases := []struct {
		prev, next uint8
		want       bool
		desc       string
	}{
		{eventstore.SeverityUp, eventstore.SeverityWarning, false, "Up→Warning, both below gate"},
		{eventstore.SeverityUp, eventstore.SeverityDegraded, true, "Up→Degraded, crosses gate"},
		{eventstore.SeverityWarning, eventstore.SeverityDegraded, true, "Warning→Degraded, crosses gate"},
		{eventstore.SeverityDegraded, eventstore.SeveritySeemsDown, true, "Degraded→SeemsDown, within gated band"},
		{eventstore.SeveritySeemsDown, eventstore.SeverityDown, true, "SeemsDown→Down, within gated band"},
	}
	for _, tc := range cases {
		got := c.Matches(tc.prev, tc.next, 0)
		if got != tc.want {
			t.Errorf("%s: Matches(%d,%d) = %v, want %v", tc.desc, tc.prev, tc.next, got, tc.want)
		}
	}
}

// TestMatchesRecovery covers the recovery half: a transition back to Up
// fires only if prev_severity was at or above the gate.
func TestMatchesRecovery(t *testing.T) {
	c := &AlertContact{
		Active:      true,
		MinSeverity: eventstore.SeverityDegraded, // 2
	}
	cases := []struct {
		prev, next uint8
		want       bool
		desc       string
	}{
		{eventstore.SeverityDown, eventstore.SeverityUp, true, "Down→Up: previously paged, now recovered"},
		{eventstore.SeverityDegraded, eventstore.SeverityUp, true, "Degraded→Up: at-gate recovery fires"},
		{eventstore.SeverityWarning, eventstore.SeverityUp, false, "Warning→Up: never paged, no recovery to send"},
		{eventstore.SeverityUp, eventstore.SeverityUp, false, "Up→Up: no transition meaning"},
	}
	for _, tc := range cases {
		got := c.Matches(tc.prev, tc.next, 0)
		if got != tc.want {
			t.Errorf("%s: Matches(%d,%d) = %v, want %v", tc.desc, tc.prev, tc.next, got, tc.want)
		}
	}
}

// TestMatchesAllDimensions verifies the AND across all dimensions:
// a contact must satisfy active, site_filter, and severity gate.
func TestMatchesAllDimensions(t *testing.T) {
	c := &AlertContact{
		Active:      true,
		SiteFilter:  SiteFilter{SiteIDs: []int64{42}},
		MinSeverity: eventstore.SeverityDown, // 4
	}
	// All dimensions match.
	if !c.Matches(eventstore.SeverityUp, eventstore.SeverityDown, 42) {
		t.Error("all dimensions matching should fire")
	}
	// Wrong site, severity matches.
	if c.Matches(eventstore.SeverityUp, eventstore.SeverityDown, 7) {
		t.Error("wrong site should not fire")
	}
	// Right site, severity below gate (and no recovery: prev was below gate too).
	if c.Matches(eventstore.SeverityUp, eventstore.SeverityWarning, 42) {
		t.Error("severity below gate should not fire when prev also below")
	}
}

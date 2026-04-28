// Package alerting manages outbound alert contact subscriptions and the
// delivery worker that fans transitions out to managed transports.
//
// An alert contact is a registration that says "send a Jetmon-rendered
// notification through this transport when matching transitions fire."
// A delivery is one alert contact firing — created when an event
// transition matches the contact's site_filter and severity gate, then
// dispatched by the background worker through the configured transport.
//
// Where webhooks (internal/webhooks) deliver a raw signed event stream
// for the consumer to render, alert contacts deliver a Jetmon-rendered
// notification through a transport Jetmon owns end-to-end (subject lines,
// PagerDuty severity mapping, Slack Block Kit rendering, etc.).
//
// See API.md "Family 5" for the public design and ROADMAP.md for deferred
// items (SMS, OpsGenie, alert grouping, WPCOM-flow migration).
package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Automattic/jetmon/internal/eventstore"
)

// Storage note: destination credentials are stored in plaintext in
// jetmon_alert_contacts.destination. Same rationale as
// jetmon_webhooks.secret — outbound dispatch needs the raw value at
// every send. A hash is useless because we'd have to recover the
// original to call the transport. Encryption at rest with a master
// key is on ROADMAP.md as a future hardening step.

// Status enumerates the lifecycle states of a delivery row.
type Status string

const (
	StatusPending   Status = "pending"
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
	StatusAbandoned Status = "abandoned"
)

// Transport identifies which managed channel a contact delivers through.
// New transports are added (never renamed) so existing contact configs
// don't break — the ENUM in the migration mirrors this set.
type Transport string

const (
	TransportEmail     Transport = "email"
	TransportPagerDuty Transport = "pagerduty"
	TransportSlack     Transport = "slack"
	TransportTeams     Transport = "teams"
)

// AllTransports returns the canonical set of transport identifiers.
// Used by validators (a contact's transport must be one of these) and
// by docs/listings.
func AllTransports() []Transport {
	return []Transport{TransportEmail, TransportPagerDuty, TransportSlack, TransportTeams}
}

// IsValidTransport reports whether s is one of the known transports.
func IsValidTransport(s string) bool {
	for _, t := range AllTransports() {
		if string(t) == s {
			return true
		}
	}
	return false
}

// Sentinel errors returned by package functions.
var (
	ErrContactNotFound  = errors.New("alerting: alert contact not found")
	ErrDeliveryNotFound = errors.New("alerting: alert delivery not found")
	ErrInvalidTransport = errors.New("alerting: unknown transport")
	ErrInvalidSeverity  = errors.New("alerting: unknown severity")
)

// AlertContact is the in-memory shape of a jetmon_alert_contacts row.
// The raw destination credential is never stored here — it's loaded
// separately by the worker via LoadDestination so it can't leak through
// serialization of the AlertContact struct.
type AlertContact struct {
	ID                 int64
	Label              string
	Active             bool
	OwnerTenantID      *string
	Transport          Transport
	DestinationPreview string     // last 4 chars of the credential, for display
	SiteFilter         SiteFilter // empty = match all sites
	MinSeverity        uint8      // matches eventstore.Severity* (0=Up..4=Down)
	MaxPerHour         int        // 0 = unlimited
	CreatedBy          string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// SiteFilter restricts deliveries to a fixed list of sites. Empty
// SiteIDs (or a nil filter) means "match all sites." Same shape as
// webhooks.SiteFilter — kept as a separate type so alerting can evolve
// independently of the webhooks package.
type SiteFilter struct {
	SiteIDs []int64 `json:"site_ids,omitempty"`
}

// Matches reports whether this contact should fire for a given
// transition. The filter rule is:
//
//	site_id ∈ site_filter.site_ids   (or site_filter empty → all sites)
//	AND (
//	    new_severity >= min_severity              // escalation / sustained
//	    OR (prev_severity >= min_severity         // recovery from a
//	        AND new_severity == SeverityUp)       //   previously-paging state
//	)
//
// Within-band changes (e.g. Down → SeemsDown when min_severity=Warning)
// fire as flickers. The per-contact max_per_hour cap absorbs the noise.
//
// Recovery firing requires both prev and new severity because Matches
// doesn't see the transition reason — it can't distinguish "resolved"
// from "transitioned through Up by accident." Practically, transitions
// to Up only happen on real recoveries.
func (c *AlertContact) Matches(prevSeverity, newSeverity uint8, siteID int64) bool {
	if !c.Active {
		return false
	}
	if len(c.SiteFilter.SiteIDs) > 0 && !containsInt64(c.SiteFilter.SiteIDs, siteID) {
		return false
	}
	if newSeverity >= c.MinSeverity {
		return true
	}
	if prevSeverity >= c.MinSeverity && newSeverity == eventstore.SeverityUp {
		return true
	}
	return false
}

// CreateInput is the data needed to insert a new alert contact.
// Label, Transport, and Destination are required; everything else has
// sensible defaults (Active=true, SiteFilter empty=match-all,
// MinSeverity=SeverityDown, MaxPerHour=60).
type CreateInput struct {
	Label         string
	Active        *bool // nil → true
	OwnerTenantID *string
	Transport     Transport
	Destination   json.RawMessage // transport-specific shape; validated per transport
	SiteFilter    SiteFilter
	MinSeverity   *uint8 // nil → SeverityDown
	MaxPerHour    *int   // nil → 60
	CreatedBy     string
}

// UpdateInput is a sparse patch. nil fields are unchanged. An explicit
// empty SiteFilter clears the filter (restores match-all). Transport
// and Destination cannot be updated together via PATCH — change of
// transport requires creating a new contact (the destination shape
// is transport-specific and validating cross-transport changes is
// more brittle than just deleting+recreating).
type UpdateInput struct {
	Label       *string
	Active      *bool
	Destination json.RawMessage // transport-specific; nil = unchanged
	SiteFilter  *SiteFilter
	MinSeverity *uint8
	MaxPerHour  *int
}

// Notification is the rendered shape passed to a Transport.Send
// implementation. The worker builds this once per delivery from the
// frozen-at-fire-time payload; transports translate it into their
// channel-specific representation.
//
// IsTest=true is used by the send-test endpoint to flag synthetic
// notifications. Transports may use this to add a banner ("This is a
// Jetmon test notification") or to choose dedup keys that won't
// collide with real alerts.
type Notification struct {
	SiteID       int64
	SiteURL      string
	EventID      int64
	EventType    string
	Severity     uint8
	SeverityName string
	State        string
	Reason       string
	Timestamp    time.Time
	DedupKey     string
	Recovery     bool
	IsTest       bool
}

// Dispatcher defines the contract every concrete transport
// (email/pagerduty/slack/teams) implements. Send is responsible for
// translating Notification into the channel-specific request and
// reporting the outcome.
//
// statusCode is the channel's idiomatic status (HTTP code for
// HTTP-based transports, SMTP reply class for email — e.g. 250
// becomes 250). responseBody is a truncated summary suitable for
// storing in jetmon_alert_deliveries.last_response (max 2048 chars;
// the worker truncates if needed).
//
// Returning err != nil means the dispatch failed in a way the worker
// should retry on the standard ladder. Returning err == nil with a
// non-2xx-equivalent status also schedules a retry; the worker
// treats both as failures for retry purposes but distinguishes them
// for diagnostics.
type Dispatcher interface {
	Send(ctx context.Context, destination json.RawMessage, n Notification) (statusCode int, responseBody string, err error)
}

// SeverityName returns the canonical string form of a severity uint8,
// matching the constants in internal/eventstore. Used by the API
// layer (which returns severity names in JSON) and by transport
// renderers (PagerDuty severity field, email subjects, Slack message
// bodies).
//
// Returns "" for unknown values rather than panicking — some callers
// pass user-supplied input that hasn't been validated yet.
func SeverityName(s uint8) string {
	switch s {
	case eventstore.SeverityUp:
		return "Up"
	case eventstore.SeverityWarning:
		return "Warning"
	case eventstore.SeverityDegraded:
		return "Degraded"
	case eventstore.SeveritySeemsDown:
		return "SeemsDown"
	case eventstore.SeverityDown:
		return "Down"
	default:
		return ""
	}
}

// SeverityFromName parses a severity string back into the eventstore
// uint8 constant. Used by the API layer to validate min_severity
// inputs from JSON. Returns ErrInvalidSeverity on unknown names.
func SeverityFromName(s string) (uint8, error) {
	switch s {
	case "Up":
		return eventstore.SeverityUp, nil
	case "Warning":
		return eventstore.SeverityWarning, nil
	case "Degraded":
		return eventstore.SeverityDegraded, nil
	case "SeemsDown":
		return eventstore.SeveritySeemsDown, nil
	case "Down":
		return eventstore.SeverityDown, nil
	default:
		return 0, ErrInvalidSeverity
	}
}

// AllSeverityNames returns the full ordered list of severity names,
// least-to-most severe. Used by docs and validators.
func AllSeverityNames() []string {
	return []string{"Up", "Warning", "Degraded", "SeemsDown", "Down"}
}

func containsInt64(haystack []int64, needle int64) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

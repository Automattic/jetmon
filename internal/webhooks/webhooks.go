// Package webhooks manages outbound webhook subscriptions and HMAC-signed
// deliveries. Sole writer for jetmon_webhooks and jetmon_webhook_deliveries.
//
// A webhook is a registration that says "POST to this URL when matching
// events fire." A delivery is one webhook firing — created when an event
// transition matches the webhook's filters, then dispatched by the
// background delivery worker.
//
// See docs/internal-api-reference.md "Family 4" for the public design and docs/roadmap.md for deferred
// items (site.state_changed events, grace-period secret rotation).
package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Storage note: the raw secret is stored in plaintext in jetmon_webhooks.
// Webhooks are outbound-only — the server signs every delivery, so the HMAC
// key has to be available in plaintext at signing time. Hashing the secret
// at rest (the API-key pattern) would make signing impossible. Encryption
// at rest with a master key is on docs/roadmap.md as a future hardening step.

// Status enumerates the lifecycle states of a delivery row.
type Status string

const (
	StatusPending   Status = "pending"
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
	StatusAbandoned Status = "abandoned"
)

// Webhook event type strings — what consumers see in the X-Jetmon-Event
// header and the events filter array. Stable identifiers; new types are
// added (never renamed) so existing webhook configs don't break.
const (
	EventOpened          = "event.opened"
	EventSeverityChanged = "event.severity_changed"
	EventStateChanged    = "event.state_changed"
	EventCauseLinked     = "event.cause_linked"
	EventCauseUnlinked   = "event.cause_unlinked"
	EventClosed          = "event.closed"
)

// AllEventTypes returns the canonical set of webhook event types. Used by
// validators (a webhook's events filter must use values from this set) and
// by docs/listings.
func AllEventTypes() []string {
	return []string{
		EventOpened,
		EventSeverityChanged,
		EventStateChanged,
		EventCauseLinked,
		EventCauseUnlinked,
		EventClosed,
	}
}

// SecretPrefix is the leak-detection hint on every raw secret. Stripe
// convention: a string that starts with this is unmistakably a webhook
// signing secret if it shows up in logs or git diffs.
const SecretPrefix = "whsec_"

// Sentinel errors returned by package functions.
var (
	ErrWebhookNotFound = errors.New("webhooks: webhook not found")
	ErrInvalidEvent    = errors.New("webhooks: unknown event type")
)

// Webhook is the in-memory shape of a jetmon_webhooks row. The raw secret
// is never stored here — it's hashed at create/rotate time and discarded.
type Webhook struct {
	ID            int64
	URL           string
	Active        bool
	OwnerTenantID *string
	Events        []string    // empty slice = match all
	SiteFilter    SiteFilter  // empty = match all
	StateFilter   StateFilter // empty = match all
	SecretPreview string      // last 4 chars of the raw secret, for display
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// SiteFilter restricts deliveries to a fixed list of sites. Empty SiteIDs
// (or a nil filter) means "match all sites."
type SiteFilter struct {
	SiteIDs []int64 `json:"site_ids,omitempty"`
}

// StateFilter restricts deliveries to events with one of the given states.
// Empty States means "match all states."
type StateFilter struct {
	States []string `json:"states,omitempty"`
}

// Matches reports whether the filter set as a whole accepts a given
// (event_type, site_id, state) combination. Filters AND together; empty
// dimensions are unrestricted.
func (w *Webhook) Matches(eventType string, siteID int64, state string) bool {
	if !w.Active {
		return false
	}
	if len(w.Events) > 0 && !contains(w.Events, eventType) {
		return false
	}
	if len(w.SiteFilter.SiteIDs) > 0 && !containsInt64(w.SiteFilter.SiteIDs, siteID) {
		return false
	}
	if len(w.StateFilter.States) > 0 && !contains(w.StateFilter.States, state) {
		return false
	}
	return true
}

// CreateInput is the data needed to insert a new webhook. URL is required;
// everything else has sensible defaults (Active=true, all filters empty =
// match-all).
type CreateInput struct {
	URL           string
	Active        *bool // nil → true
	OwnerTenantID *string
	Events        []string
	SiteFilter    SiteFilter
	StateFilter   StateFilter
	CreatedBy     string
}

// UpdateInput is a sparse patch. nil fields are unchanged. Empty slices
// (vs. nil slices) are meaningful: an explicit empty slice clears the
// filter, restoring "match all" semantics.
type UpdateInput struct {
	URL         *string
	Active      *bool
	Events      *[]string
	SiteFilter  *SiteFilter
	StateFilter *StateFilter
}

// Create inserts a webhook and returns the one-time raw secret plus the
// persisted record. The raw secret is also stored in the DB (see Storage
// note above) so the delivery worker can sign with it.
func Create(ctx context.Context, db *sql.DB, in CreateInput) (rawSecret string, w *Webhook, err error) {
	if in.URL == "" {
		return "", nil, errors.New("webhooks: URL is required")
	}
	if err := validateEvents(in.Events); err != nil {
		return "", nil, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}

	rawSecret, err = GenerateSecret()
	if err != nil {
		return "", nil, err
	}
	preview := previewOf(rawSecret)

	eventsJSON, _ := json.Marshal(in.Events)
	siteFilterJSON, _ := json.Marshal(in.SiteFilter)
	stateFilterJSON, _ := json.Marshal(in.StateFilter)

	res, err := db.ExecContext(ctx, `
		INSERT INTO jetmon_webhooks
			(url, active, owner_tenant_id, events, site_filter, state_filter,
			 secret, secret_preview, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.URL, boolToTinyint(active), nullableString(in.OwnerTenantID), eventsJSON, siteFilterJSON, stateFilterJSON,
		rawSecret, preview, in.CreatedBy,
	)
	if err != nil {
		return "", nil, fmt.Errorf("webhooks: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, fmt.Errorf("webhooks: last insert id: %w", err)
	}

	w, err = Get(ctx, db, id)
	if err != nil {
		return "", nil, err
	}
	return rawSecret, w, nil
}

// Get returns a single webhook by id, or ErrWebhookNotFound.
func Get(ctx context.Context, db *sql.DB, id int64) (*Webhook, error) {
	return get(ctx, db, id, "")
}

// GetForTenant returns a single webhook owned by ownerTenantID. It hides
// cross-tenant rows behind ErrWebhookNotFound so future public callers don't
// learn whether another tenant's webhook exists.
func GetForTenant(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) (*Webhook, error) {
	if ownerTenantID == "" {
		return nil, errors.New("webhooks: owner tenant id is required")
	}
	return get(ctx, db, id, ownerTenantID)
}

func get(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) (*Webhook, error) {
	q := selectWebhookSQL + " WHERE id = ?"
	args := []any{id}
	if ownerTenantID != "" {
		q += " AND owner_tenant_id = ?"
		args = append(args, ownerTenantID)
	}
	row := db.QueryRowContext(ctx, q, args...)
	w, err := scanWebhookRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrWebhookNotFound
		}
		return nil, err
	}
	return w, nil
}

// List returns all webhooks ordered by id ASC. Webhook count is bounded by
// the number of registered consumers; we don't paginate today. If a future
// deployment grows past hundreds of webhooks, add cursor pagination here.
func List(ctx context.Context, db *sql.DB) ([]Webhook, error) {
	return list(ctx, db, "")
}

// ListForTenant returns only webhooks owned by ownerTenantID.
func ListForTenant(ctx context.Context, db *sql.DB, ownerTenantID string) ([]Webhook, error) {
	if ownerTenantID == "" {
		return nil, errors.New("webhooks: owner tenant id is required")
	}
	return list(ctx, db, ownerTenantID)
}

func list(ctx context.Context, db *sql.DB, ownerTenantID string) ([]Webhook, error) {
	q := selectWebhookSQL
	args := []any{}
	if ownerTenantID != "" {
		q += " WHERE owner_tenant_id = ?"
		args = append(args, ownerTenantID)
	}
	q += " ORDER BY id ASC"
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("webhooks: list: %w", err)
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhookRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// ListActive returns only webhooks with active=1. Used by the delivery
// dispatcher; inactive webhooks don't get matched against new transitions.
func ListActive(ctx context.Context, db *sql.DB) ([]Webhook, error) {
	rows, err := db.QueryContext(ctx, selectWebhookSQL+" WHERE active = 1 ORDER BY id ASC")
	if err != nil {
		return nil, fmt.Errorf("webhooks: list active: %w", err)
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		w, err := scanWebhookRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// Update applies a partial patch and returns the updated webhook. Fields
// left nil in UpdateInput are unchanged; an explicitly empty slice clears
// the corresponding filter to "match all" semantics.
func Update(ctx context.Context, db *sql.DB, id int64, in UpdateInput) (*Webhook, error) {
	return update(ctx, db, id, "", in)
}

// UpdateForTenant updates a webhook only when it is owned by ownerTenantID.
func UpdateForTenant(ctx context.Context, db *sql.DB, id int64, ownerTenantID string, in UpdateInput) (*Webhook, error) {
	if ownerTenantID == "" {
		return nil, errors.New("webhooks: owner tenant id is required")
	}
	return update(ctx, db, id, ownerTenantID, in)
}

func update(ctx context.Context, db *sql.DB, id int64, ownerTenantID string, in UpdateInput) (*Webhook, error) {
	if in.Events != nil {
		if err := validateEvents(*in.Events); err != nil {
			return nil, err
		}
	}

	clauses := []string{}
	args := []any{}
	if in.URL != nil {
		clauses = append(clauses, "url = ?")
		args = append(args, *in.URL)
	}
	if in.Active != nil {
		clauses = append(clauses, "active = ?")
		args = append(args, boolToTinyint(*in.Active))
	}
	if in.Events != nil {
		b, _ := json.Marshal(*in.Events)
		clauses = append(clauses, "events = ?")
		args = append(args, b)
	}
	if in.SiteFilter != nil {
		b, _ := json.Marshal(*in.SiteFilter)
		clauses = append(clauses, "site_filter = ?")
		args = append(args, b)
	}
	if in.StateFilter != nil {
		b, _ := json.Marshal(*in.StateFilter)
		clauses = append(clauses, "state_filter = ?")
		args = append(args, b)
	}

	if len(clauses) == 0 {
		// No-op patch — return current state.
		return get(ctx, db, id, ownerTenantID)
	}

	args = append(args, id)
	q := "UPDATE jetmon_webhooks SET "
	for i, c := range clauses {
		if i > 0 {
			q += ", "
		}
		q += c
	}
	q += " WHERE id = ?"
	if ownerTenantID != "" {
		q += " AND owner_tenant_id = ?"
		args = append(args, ownerTenantID)
	}
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		return nil, fmt.Errorf("webhooks: update: %w", err)
	}
	return get(ctx, db, id, ownerTenantID)
}

// Delete removes a webhook from jetmon_webhooks. Existing rows in
// jetmon_webhook_deliveries are intentionally NOT cascaded — they remain
// for audit and manual retry. The dispatcher won't create new deliveries
// for a deleted webhook because ListActive filters it out.
func Delete(ctx context.Context, db *sql.DB, id int64) error {
	return deleteWebhook(ctx, db, id, "")
}

// DeleteForTenant removes a webhook only when it is owned by ownerTenantID.
func DeleteForTenant(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) error {
	if ownerTenantID == "" {
		return errors.New("webhooks: owner tenant id is required")
	}
	return deleteWebhook(ctx, db, id, ownerTenantID)
}

func deleteWebhook(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) error {
	q := "DELETE FROM jetmon_webhooks WHERE id = ?"
	args := []any{id}
	if ownerTenantID != "" {
		q += " AND owner_tenant_id = ?"
		args = append(args, ownerTenantID)
	}
	res, err := db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("webhooks: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrWebhookNotFound
	}
	return nil
}

// RotateSecret generates a new secret, replaces the stored value, and
// returns the new raw secret (one-time view in API responses). The old
// secret stops working immediately — see docs/internal-api-reference.md "Signing and secret
// rotation" for why this is the v1 behavior and how grace-period rotation
// will be added later.
func RotateSecret(ctx context.Context, db *sql.DB, id int64) (string, *Webhook, error) {
	return rotateSecret(ctx, db, id, "")
}

// RotateSecretForTenant rotates a webhook secret only when it is owned by
// ownerTenantID.
func RotateSecretForTenant(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) (string, *Webhook, error) {
	if ownerTenantID == "" {
		return "", nil, errors.New("webhooks: owner tenant id is required")
	}
	return rotateSecret(ctx, db, id, ownerTenantID)
}

func rotateSecret(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) (string, *Webhook, error) {
	rawSecret, err := GenerateSecret()
	if err != nil {
		return "", nil, err
	}
	preview := previewOf(rawSecret)
	q := `UPDATE jetmon_webhooks SET secret = ?, secret_preview = ? WHERE id = ?`
	args := []any{rawSecret, preview, id}
	if ownerTenantID != "" {
		q += " AND owner_tenant_id = ?"
		args = append(args, ownerTenantID)
	}
	res, err := db.ExecContext(ctx,
		q, args...)
	if err != nil {
		return "", nil, fmt.Errorf("webhooks: rotate-secret: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", nil, ErrWebhookNotFound
	}
	w, err := get(ctx, db, id, ownerTenantID)
	if err != nil {
		return "", nil, err
	}
	return rawSecret, w, nil
}

// LoadSecret returns the raw signing secret for a webhook. Used by the
// delivery worker only — every public-facing handler returns SecretPreview
// instead. Kept as a separate function (not a field on Webhook) so the
// raw value can't leak through serialization of the Webhook struct.
func LoadSecret(ctx context.Context, db *sql.DB, id int64) (string, error) {
	var s string
	err := db.QueryRowContext(ctx,
		`SELECT secret FROM jetmon_webhooks WHERE id = ?`, id,
	).Scan(&s)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrWebhookNotFound
		}
		return "", fmt.Errorf("webhooks: load secret: %w", err)
	}
	return s, nil
}

// GenerateSecret returns a fresh raw secret. 32 random bytes encoded as
// base32 with the "whsec_" prefix. Same shape as apikeys — high-entropy
// random; the leak-detection prefix is the only thing that distinguishes
// it from a generic random string.
func GenerateSecret() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("webhooks: read entropy: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:])
	return SecretPrefix + encoded, nil
}

// Sign produces the X-Jetmon-Signature header value for a delivery.
// Format: "t=<unix>,v1=<hex_hmac_sha256(t.body)>" — see docs/internal-api-reference.md.
//
// The timestamp is part of the signature input so consumers can reject
// stale (replayed) deliveries by checking the t= value against their
// own clock and refusing anything older than ~5 minutes.
func Sign(timestamp time.Time, body []byte, secret string) string {
	ts := strconv.FormatInt(timestamp.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))
	return "t=" + ts + ",v1=" + sig
}

// EventTypeForReason maps a jetmon_event_transitions.reason value to the
// webhook event type that should fire. Returns "" if the reason should
// produce no webhook (used for cause-link reasons that are stored as
// transitions but not surfaced as separate webhook events in v1).
//
// The mapping is fixed in code — adding new transition reasons requires
// extending this function so consumers see the right webhook event type.
func EventTypeForReason(reason string) string {
	switch reason {
	case "opened":
		return EventOpened
	case "severity_escalation", "severity_deescalation":
		return EventSeverityChanged
	case "state_change", "verifier_confirmed":
		return EventStateChanged
	case "cause_linked":
		return EventCauseLinked
	case "cause_unlinked":
		return EventCauseUnlinked
	case "verifier_cleared", "probe_cleared", "false_alarm",
		"manual_override", "maintenance_swallowed", "superseded", "auto_timeout":
		return EventClosed
	default:
		return ""
	}
}

// validateEvents rejects an events list that includes an unknown event
// type. Empty list is fine — that's the "match all" sentinel.
func validateEvents(events []string) error {
	all := AllEventTypes()
	for _, e := range events {
		if !contains(all, e) {
			return fmt.Errorf("%w: %q (allowed: %v)", ErrInvalidEvent, e, all)
		}
	}
	return nil
}

// previewOf returns the last 4 characters of a raw secret for display.
// Short enough to fit on a one-line listing; long enough to disambiguate
// among a handful of webhooks.
func previewOf(s string) string {
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}

// selectWebhookSQL is shared by Get / List / ListActive so the column
// order matches scanWebhookRow.
const selectWebhookSQL = `
	SELECT id, url, active, owner_tenant_id, events, site_filter, state_filter,
	       secret_preview, created_by, created_at, updated_at
	  FROM jetmon_webhooks`

type rowScanner interface {
	Scan(...any) error
}

func scanWebhookRow(s rowScanner) (*Webhook, error) {
	var (
		w               Webhook
		active          uint8
		ownerTenantID   sql.NullString
		eventsJSON      sql.NullString
		siteFilterJSON  sql.NullString
		stateFilterJSON sql.NullString
	)
	if err := s.Scan(
		&w.ID, &w.URL, &active, &ownerTenantID, &eventsJSON, &siteFilterJSON, &stateFilterJSON,
		&w.SecretPreview, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt,
	); err != nil {
		return nil, err
	}
	w.Active = active == 1
	if ownerTenantID.Valid {
		w.OwnerTenantID = &ownerTenantID.String
	}
	if eventsJSON.Valid && eventsJSON.String != "" {
		_ = json.Unmarshal([]byte(eventsJSON.String), &w.Events)
	}
	if siteFilterJSON.Valid && siteFilterJSON.String != "" {
		_ = json.Unmarshal([]byte(siteFilterJSON.String), &w.SiteFilter)
	}
	if stateFilterJSON.Valid && stateFilterJSON.String != "" {
		_ = json.Unmarshal([]byte(stateFilterJSON.String), &w.StateFilter)
	}
	return &w, nil
}

func boolToTinyint(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func containsInt64(haystack []int64, needle int64) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

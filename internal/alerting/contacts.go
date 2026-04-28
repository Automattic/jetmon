package alerting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Create inserts a new alert contact and returns the persisted record.
// Unlike webhooks.Create (which returns the one-time raw secret), the
// destination is supplied by the caller — they already know the
// credential, so there's nothing to return-once. Subsequent reads
// expose only DestinationPreview.
func Create(ctx context.Context, db *sql.DB, in CreateInput) (*AlertContact, error) {
	if err := validateCreateInput(in); err != nil {
		return nil, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	minSev := uint8(4) // SeverityDown
	if in.MinSeverity != nil {
		minSev = *in.MinSeverity
	}
	maxPerHour := 60
	if in.MaxPerHour != nil {
		maxPerHour = *in.MaxPerHour
	}
	preview := destinationPreview(in.Transport, in.Destination)
	siteFilterJSON, _ := json.Marshal(in.SiteFilter)

	res, err := db.ExecContext(ctx, `
		INSERT INTO jetmon_alert_contacts
			(label, active, owner_tenant_id, transport, destination, destination_preview,
			 site_filter, min_severity, max_per_hour, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Label, boolToTinyint(active), nullableString(in.OwnerTenantID), string(in.Transport), []byte(in.Destination), preview,
		siteFilterJSON, minSev, maxPerHour, in.CreatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("alerting: insert contact: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("alerting: last insert id: %w", err)
	}
	return Get(ctx, db, id)
}

// Get returns a single contact by id, or ErrContactNotFound. Does not
// load the destination credential — use LoadDestination for that.
func Get(ctx context.Context, db *sql.DB, id int64) (*AlertContact, error) {
	return get(ctx, db, id, "")
}

// GetForTenant returns a single contact owned by ownerTenantID. It hides
// cross-tenant rows behind ErrContactNotFound.
func GetForTenant(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) (*AlertContact, error) {
	if ownerTenantID == "" {
		return nil, errors.New("alerting: owner tenant id is required")
	}
	return get(ctx, db, id, ownerTenantID)
}

func get(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) (*AlertContact, error) {
	q := selectContactSQL + " WHERE id = ?"
	args := []any{id}
	if ownerTenantID != "" {
		q += " AND owner_tenant_id = ?"
		args = append(args, ownerTenantID)
	}
	row := db.QueryRowContext(ctx, q, args...)
	c, err := scanContactRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrContactNotFound
		}
		return nil, err
	}
	return c, nil
}

// List returns all contacts ordered by id ASC.
func List(ctx context.Context, db *sql.DB) ([]AlertContact, error) {
	return list(ctx, db, "")
}

// ListForTenant returns only contacts owned by ownerTenantID.
func ListForTenant(ctx context.Context, db *sql.DB, ownerTenantID string) ([]AlertContact, error) {
	if ownerTenantID == "" {
		return nil, errors.New("alerting: owner tenant id is required")
	}
	return list(ctx, db, ownerTenantID)
}

func list(ctx context.Context, db *sql.DB, ownerTenantID string) ([]AlertContact, error) {
	q := selectContactSQL
	args := []any{}
	if ownerTenantID != "" {
		q += " WHERE owner_tenant_id = ?"
		args = append(args, ownerTenantID)
	}
	q += " ORDER BY id ASC"
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("alerting: list contacts: %w", err)
	}
	defer rows.Close()
	var out []AlertContact
	for rows.Next() {
		c, err := scanContactRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ListActive returns only contacts with active=1. Used by the delivery
// dispatcher; inactive contacts don't get matched against new
// transitions.
func ListActive(ctx context.Context, db *sql.DB) ([]AlertContact, error) {
	rows, err := db.QueryContext(ctx, selectContactSQL+" WHERE active = 1 ORDER BY id ASC")
	if err != nil {
		return nil, fmt.Errorf("alerting: list active contacts: %w", err)
	}
	defer rows.Close()
	var out []AlertContact
	for rows.Next() {
		c, err := scanContactRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// Update applies a partial patch and returns the updated contact. The
// transport itself cannot be changed via PATCH (the destination shape
// is transport-specific and validating cross-transport changes is
// brittle); callers who want to switch transport delete and re-create.
func Update(ctx context.Context, db *sql.DB, id int64, in UpdateInput) (*AlertContact, error) {
	return update(ctx, db, id, "", in)
}

// UpdateForTenant updates a contact only when it is owned by ownerTenantID.
func UpdateForTenant(ctx context.Context, db *sql.DB, id int64, ownerTenantID string, in UpdateInput) (*AlertContact, error) {
	if ownerTenantID == "" {
		return nil, errors.New("alerting: owner tenant id is required")
	}
	return update(ctx, db, id, ownerTenantID, in)
}

func update(ctx context.Context, db *sql.DB, id int64, ownerTenantID string, in UpdateInput) (*AlertContact, error) {
	// Validate input fields that don't depend on the existing row first
	// (fail fast — no DB hit on obviously bad PATCH bodies).
	if in.Label != nil && *in.Label == "" {
		return nil, errors.New("alerting: label must not be empty")
	}
	if in.MinSeverity != nil {
		if err := validateSeverity(*in.MinSeverity); err != nil {
			return nil, err
		}
	}
	if in.MaxPerHour != nil && *in.MaxPerHour < 0 {
		return nil, errors.New("alerting: max_per_hour must be >= 0")
	}

	// The destination shape is transport-specific, so we need the
	// existing row to know what to validate against.
	current, err := get(ctx, db, id, ownerTenantID)
	if err != nil {
		return nil, err
	}
	if in.Destination != nil {
		if err := validateDestination(current.Transport, in.Destination); err != nil {
			return nil, err
		}
	}

	clauses := []string{}
	args := []any{}
	if in.Label != nil {
		clauses = append(clauses, "label = ?")
		args = append(args, *in.Label)
	}
	if in.Active != nil {
		clauses = append(clauses, "active = ?")
		args = append(args, boolToTinyint(*in.Active))
	}
	if in.Destination != nil {
		clauses = append(clauses, "destination = ?", "destination_preview = ?")
		args = append(args, []byte(in.Destination), destinationPreview(current.Transport, in.Destination))
	}
	if in.SiteFilter != nil {
		b, _ := json.Marshal(*in.SiteFilter)
		clauses = append(clauses, "site_filter = ?")
		args = append(args, b)
	}
	if in.MinSeverity != nil {
		clauses = append(clauses, "min_severity = ?")
		args = append(args, *in.MinSeverity)
	}
	if in.MaxPerHour != nil {
		clauses = append(clauses, "max_per_hour = ?")
		args = append(args, *in.MaxPerHour)
	}

	if len(clauses) == 0 {
		return current, nil
	}

	args = append(args, id)
	q := "UPDATE jetmon_alert_contacts SET " + strings.Join(clauses, ", ") + " WHERE id = ?"
	if ownerTenantID != "" {
		q += " AND owner_tenant_id = ?"
		args = append(args, ownerTenantID)
	}
	if _, err := db.ExecContext(ctx, q, args...); err != nil {
		return nil, fmt.Errorf("alerting: update contact: %w", err)
	}
	return get(ctx, db, id, ownerTenantID)
}

// Delete removes an alert contact. Existing rows in
// jetmon_alert_deliveries are intentionally NOT cascaded — they
// remain for audit and manual retry, mirroring webhooks.Delete.
func Delete(ctx context.Context, db *sql.DB, id int64) error {
	return deleteContact(ctx, db, id, "")
}

// DeleteForTenant removes a contact only when it is owned by ownerTenantID.
func DeleteForTenant(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) error {
	if ownerTenantID == "" {
		return errors.New("alerting: owner tenant id is required")
	}
	return deleteContact(ctx, db, id, ownerTenantID)
}

func deleteContact(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) error {
	q := "DELETE FROM jetmon_alert_contacts WHERE id = ?"
	args := []any{id}
	if ownerTenantID != "" {
		q += " AND owner_tenant_id = ?"
		args = append(args, ownerTenantID)
	}
	res, err := db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("alerting: delete contact: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrContactNotFound
	}
	return nil
}

// LoadDestination returns the raw destination JSON for a contact,
// used by the worker to call the configured Dispatcher. Kept as a
// separate function (not a field on AlertContact) so the credential
// can't leak through serialization of the AlertContact struct.
func LoadDestination(ctx context.Context, db *sql.DB, id int64) (json.RawMessage, error) {
	return loadDestination(ctx, db, id, "")
}

// LoadDestinationForTenant loads a contact credential only when it is owned
// by ownerTenantID.
func LoadDestinationForTenant(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) (json.RawMessage, error) {
	if ownerTenantID == "" {
		return nil, errors.New("alerting: owner tenant id is required")
	}
	return loadDestination(ctx, db, id, ownerTenantID)
}

func loadDestination(ctx context.Context, db *sql.DB, id int64, ownerTenantID string) (json.RawMessage, error) {
	var raw []byte
	q := `SELECT destination FROM jetmon_alert_contacts WHERE id = ?`
	args := []any{id}
	if ownerTenantID != "" {
		q += " AND owner_tenant_id = ?"
		args = append(args, ownerTenantID)
	}
	err := db.QueryRowContext(ctx,
		q, args...,
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrContactNotFound
		}
		return nil, fmt.Errorf("alerting: load destination: %w", err)
	}
	return raw, nil
}

// validateCreateInput enforces the required-fields contract for Create.
func validateCreateInput(in CreateInput) error {
	if in.Label == "" {
		return errors.New("alerting: label is required")
	}
	if !IsValidTransport(string(in.Transport)) {
		return fmt.Errorf("%w: %q", ErrInvalidTransport, in.Transport)
	}
	if err := validateDestination(in.Transport, in.Destination); err != nil {
		return err
	}
	if in.MinSeverity != nil {
		if err := validateSeverity(*in.MinSeverity); err != nil {
			return err
		}
	}
	if in.MaxPerHour != nil && *in.MaxPerHour < 0 {
		return errors.New("alerting: max_per_hour must be >= 0")
	}
	return nil
}

// validateDestination checks that the destination JSON has the shape
// the transport requires. Validates field presence, not field
// well-formedness — a malformed Slack webhook URL surfaces as a
// transport error at delivery time, which is fine because operators
// can use the send-test endpoint to catch it before real alerts fire.
func validateDestination(t Transport, dest json.RawMessage) error {
	if len(dest) == 0 {
		return errors.New("alerting: destination is required")
	}
	switch t {
	case TransportEmail:
		var d emailDestination
		if err := json.Unmarshal(dest, &d); err != nil {
			return fmt.Errorf("alerting: destination not valid JSON: %w", err)
		}
		if d.Address == "" {
			return errors.New("alerting: email destination requires an address")
		}
	case TransportPagerDuty:
		var d pagerDutyDestination
		if err := json.Unmarshal(dest, &d); err != nil {
			return fmt.Errorf("alerting: destination not valid JSON: %w", err)
		}
		if d.IntegrationKey == "" {
			return errors.New("alerting: pagerduty destination requires an integration_key")
		}
	case TransportSlack:
		var d slackDestination
		if err := json.Unmarshal(dest, &d); err != nil {
			return fmt.Errorf("alerting: destination not valid JSON: %w", err)
		}
		if d.WebhookURL == "" {
			return errors.New("alerting: slack destination requires a webhook_url")
		}
	case TransportTeams:
		var d teamsDestination
		if err := json.Unmarshal(dest, &d); err != nil {
			return fmt.Errorf("alerting: destination not valid JSON: %w", err)
		}
		if d.WebhookURL == "" {
			return errors.New("alerting: teams destination requires a webhook_url")
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidTransport, t)
	}
	return nil
}

// validateSeverity rejects severity values outside the eventstore range.
// Anything 0..4 is accepted; 5+ is reserved per the eventstore comment
// for future "worse than down" signals but isn't usable as a gate yet.
func validateSeverity(s uint8) error {
	if s > 4 {
		return fmt.Errorf("%w: %d (allowed 0-4)", ErrInvalidSeverity, s)
	}
	return nil
}

// destinationPreview returns the last 4 chars of the credential field
// for the given transport. Used as a UI hint so operators can identify
// a contact without exposing the full credential.
func destinationPreview(t Transport, dest json.RawMessage) string {
	var s string
	switch t {
	case TransportEmail:
		var d emailDestination
		_ = json.Unmarshal(dest, &d)
		s = d.Address
	case TransportPagerDuty:
		var d pagerDutyDestination
		_ = json.Unmarshal(dest, &d)
		s = d.IntegrationKey
	case TransportSlack:
		var d slackDestination
		_ = json.Unmarshal(dest, &d)
		s = d.WebhookURL
	case TransportTeams:
		var d teamsDestination
		_ = json.Unmarshal(dest, &d)
		s = d.WebhookURL
	}
	if len(s) <= 4 {
		return s
	}
	return s[len(s)-4:]
}

// boolToTinyint mirrors the helper in internal/webhooks/webhooks.go.
func boolToTinyint(b bool) int {
	if b {
		return 1
	}
	return 0
}

const selectContactSQL = `
	SELECT id, label, active, owner_tenant_id, transport, destination_preview,
	       site_filter, min_severity, max_per_hour,
	       created_by, created_at, updated_at
	  FROM jetmon_alert_contacts`

type rowScanner interface {
	Scan(...any) error
}

func scanContactRow(s rowScanner) (*AlertContact, error) {
	var (
		c              AlertContact
		active         uint8
		ownerTenantID  sql.NullString
		transport      string
		siteFilterJSON sql.NullString
	)
	if err := s.Scan(
		&c.ID, &c.Label, &active, &ownerTenantID, &transport, &c.DestinationPreview,
		&siteFilterJSON, &c.MinSeverity, &c.MaxPerHour,
		&c.CreatedBy, &c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return nil, err
	}
	c.Active = active == 1
	if ownerTenantID.Valid {
		c.OwnerTenantID = &ownerTenantID.String
	}
	c.Transport = Transport(transport)
	if siteFilterJSON.Valid && siteFilterJSON.String != "" {
		_ = json.Unmarshal([]byte(siteFilterJSON.String), &c.SiteFilter)
	}
	return &c, nil
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

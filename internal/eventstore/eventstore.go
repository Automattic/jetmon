// Package eventstore is the sole writer for jetmon_events and jetmon_event_transitions.
//
// Site state in Jetmon is event-sourced across two tables:
//
//   - jetmon_events holds the current state of every incident — one row per
//     (blog_id, endpoint_id, check_type, discriminator) tuple while open, mutable
//     until ended_at is set, then frozen.
//   - jetmon_event_transitions is the append-only history of every mutation made
//     to a jetmon_events row. One row per change. Never updated, never deleted.
//
// The load-bearing invariant is: every mutation to jetmon_events writes exactly
// one row into jetmon_event_transitions, in the same database transaction. This
// package enforces that by being the only writer for both tables. External
// callers go through Open, UpdateSeverity, UpdateState, LinkCause, and Close.
//
// Two API surfaces:
//
//   - Store.Open / Store.Promote / Store.Close (etc.) — each opens its own
//     transaction, performs the event mutation + transition write, and commits.
//     Use these when the event mutation is the only DB write.
//
//   - Store.Begin → *Tx → Tx.Open / Tx.Promote / Tx.Close (etc.) → Tx.Commit —
//     caller controls transaction boundaries, can run additional SQL on the
//     same transaction (e.g. updating jetpack_monitor_sites.site_status as a
//     v1 projection alongside the event write).
//
// See EVENTS.md for the full design rationale and TAXONOMY.md for the data model.
package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// State labels written to jetmon_events.state and jetmon_event_transitions.state_*.
// The state column is VARCHAR(32) rather than ENUM so new states can be added in
// code without a schema migration.
const (
	StateUp          = "Up"
	StateWarning     = "Warning"
	StateDegraded    = "Degraded"
	StateSeemsDown   = "Seems Down"
	StateDown        = "Down"
	StatePaused      = "Paused"
	StateMaintenance = "Maintenance"
	StateUnknown     = "Unknown"
	StateResolved    = "Resolved"
)

// Severity is the numeric, ordered companion to State. Higher = worse. Stored
// as TINYINT UNSIGNED so values 0–255 are valid; the canonical scale below
// covers the lifecycle states. Severity moves independently of state — a
// degradation worsening bumps severity without changing state, and severity
// values above SeverityDown can be reserved for future "worse than down"
// signals (e.g. data loss, security compromise) without breaking rollup.
const (
	SeverityUp        uint8 = 0
	SeverityWarning   uint8 = 1
	SeverityDegraded  uint8 = 2
	SeveritySeemsDown uint8 = 3
	SeverityDown      uint8 = 4
)

// Transition reasons written to jetmon_event_transitions.reason. The closed-event
// reasons are also written to jetmon_events.resolution_reason on Close.
const (
	ReasonOpened               = "opened"
	ReasonSeverityEscalation   = "severity_escalation"
	ReasonSeverityDeescalation = "severity_deescalation"
	ReasonStateChange          = "state_change"
	ReasonVerifierConfirmed    = "verifier_confirmed"
	ReasonVerifierCleared      = "verifier_cleared"
	ReasonProbeCleared         = "probe_cleared"
	ReasonFalseAlarm           = "false_alarm"
	ReasonManualOverride       = "manual_override"
	ReasonMaintenanceSwallowed = "maintenance_swallowed"
	ReasonSuperseded           = "superseded"
	ReasonAutoTimeout          = "auto_timeout"
	ReasonCauseLinked          = "cause_linked"
	ReasonCauseUnlinked        = "cause_unlinked"
)

// ErrEventClosed is returned when a caller attempts to mutate an event that is
// already closed (ended_at IS NOT NULL). Closed events are immutable.
var ErrEventClosed = errors.New("eventstore: event is closed")

// ErrEventNotFound is returned when a caller references an event id that does
// not exist.
var ErrEventNotFound = errors.New("eventstore: event not found")

// Identity is the dedup tuple for an event. Two open events cannot share the
// same Identity — the schema's dedup_key + UNIQUE INDEX enforces this.
type Identity struct {
	BlogID        int64
	EndpointID    *int64 // nil for site-level checks (DNS, TLS expiry, domain)
	CheckType     string
	Discriminator string // empty when the (blog, endpoint, check_type) is single-failure
}

// OpenInput carries the fields needed to open (or reopen) an event.
type OpenInput struct {
	Identity Identity
	Severity uint8
	State    string
	Source   string          // who detected the failure: "local", "veriflier:us-west", …
	Metadata json.RawMessage // optional check-type-specific payload
}

// OpenResult describes the outcome of an Open call.
type OpenResult struct {
	EventID         int64
	Opened          bool   // true if a new event was inserted; false if an existing open event matched the identity
	CurrentSeverity uint8  // severity on the event row after the call
	CurrentState    string // state on the event row after the call
}

// Store is the sole writer for jetmon_events and jetmon_event_transitions.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by the given database handle. A nil db is allowed
// (writes become no-ops) so packages that depend on Store can still construct
// in tests where the database isn't available.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Tx wraps a single database transaction and exposes the same event-mutation
// API as Store, but without committing. Callers who need to coordinate event
// writes with other SQL (e.g. updating a v1 projection like
// jetpack_monitor_sites.site_status) start a Tx, perform the event mutation,
// run their other writes via Tx.Tx().Exec(...), then Commit.
//
// A Tx returned from a nil-db Store is itself a no-op shell; all methods
// short-circuit and Commit/Rollback are safe to call.
type Tx struct {
	tx *sql.Tx // nil when Store had no db
}

// Begin starts a new transaction. Caller must Commit or Rollback. Calling on a
// nil-db Store returns an empty Tx whose methods are no-ops.
func (s *Store) Begin(ctx context.Context) (*Tx, error) {
	if s.db == nil {
		return &Tx{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	return &Tx{tx: tx}, nil
}

// Tx returns the underlying *sql.Tx so the caller can run additional SQL on
// the same transaction. Returns nil when the Tx is in nil-db mode.
func (t *Tx) Tx() *sql.Tx { return t.tx }

// Commit commits the transaction. No-op in nil-db mode.
func (t *Tx) Commit() error {
	if t.tx == nil {
		return nil
	}
	return t.tx.Commit()
}

// Rollback rolls back the transaction. No-op in nil-db mode. Safe to call
// after Commit (the underlying sql.ErrTxDone is swallowed) so it composes
// with `defer tx.Rollback()`.
func (t *Tx) Rollback() error {
	if t.tx == nil {
		return nil
	}
	if err := t.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return err
	}
	return nil
}

// Open opens a new event for the given identity, or returns the existing open
// event's id if one already exists. Idempotent — repeated calls with the same
// identity return the same event id and only write one "opened" transition
// row (the one for the actual insert).
//
// Severity escalation on a re-detection should go through UpdateSeverity, not
// through repeated Opens.
func (t *Tx) Open(ctx context.Context, in OpenInput) (OpenResult, error) {
	if t.tx == nil {
		return OpenResult{}, nil
	}
	if in.Identity.CheckType == "" {
		return OpenResult{}, errors.New("eventstore: Open requires CheckType")
	}
	if in.State == "" {
		return OpenResult{}, errors.New("eventstore: Open requires State")
	}

	// LAST_INSERT_ID(id) on the UPDATE branch makes the driver return the
	// existing row's id. RowsAffected is 1 on insert, 2 on update (per the
	// MySQL driver convention). We only write an "opened" transition on insert.
	res, err := t.tx.ExecContext(ctx, `
		INSERT INTO jetmon_events
			(blog_id, endpoint_id, check_type, discriminator, severity, state, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
		in.Identity.BlogID,
		nullableEndpoint(in.Identity.EndpointID),
		in.Identity.CheckType,
		nullableDiscriminator(in.Identity.Discriminator),
		in.Severity,
		in.State,
		nullableJSON(in.Metadata),
	)
	if err != nil {
		return OpenResult{}, fmt.Errorf("insert event: %w", err)
	}
	eventID, err := res.LastInsertId()
	if err != nil {
		return OpenResult{}, fmt.Errorf("last insert id: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return OpenResult{}, fmt.Errorf("rows affected: %w", err)
	}
	opened := rowsAffected == 1

	var currentSeverity uint8
	var currentState string
	if opened {
		currentSeverity = in.Severity
		currentState = in.State
		sev := in.Severity
		if err := writeTransition(ctx, t.tx, transitionInput{
			eventID:        eventID,
			blogID:         in.Identity.BlogID,
			severityBefore: nil,
			severityAfter:  &sev,
			stateBefore:    "",
			stateAfter:     in.State,
			reason:         ReasonOpened,
			source:         in.Source,
			metadata:       in.Metadata,
		}); err != nil {
			return OpenResult{}, err
		}
	} else {
		// Existing open event matched. Read its current severity/state so the
		// caller can decide whether to follow up with UpdateSeverity/UpdateState.
		if err := t.tx.QueryRowContext(ctx,
			`SELECT severity, state FROM jetmon_events WHERE id = ?`, eventID,
		).Scan(&currentSeverity, &currentState); err != nil {
			return OpenResult{}, fmt.Errorf("read existing event: %w", err)
		}
	}

	return OpenResult{
		EventID:         eventID,
		Opened:          opened,
		CurrentSeverity: currentSeverity,
		CurrentState:    currentState,
	}, nil
}

// UpdateSeverity changes the severity of an open event. If the new severity
// equals the current one, no row is written and (false, nil) is returned.
func (t *Tx) UpdateSeverity(ctx context.Context, eventID int64, newSeverity uint8, reason, source string, metadata json.RawMessage) (bool, error) {
	if t.tx == nil {
		return false, nil
	}
	return t.mutate(ctx, eventID, mutation{
		severityAfter: &newSeverity,
		reason:        reason,
		source:        source,
		metadata:      metadata,
	})
}

// UpdateState changes the lifecycle state of an open event (e.g.,
// Seems Down → Down on verifier confirmation). If the new state equals the
// current one, no row is written.
func (t *Tx) UpdateState(ctx context.Context, eventID int64, newState, reason, source string, metadata json.RawMessage) (bool, error) {
	if t.tx == nil {
		return false, nil
	}
	return t.mutate(ctx, eventID, mutation{
		stateAfter: &newState,
		reason:     reason,
		source:     source,
		metadata:   metadata,
	})
}

// Promote bumps state and severity together with one transition row. Used for
// the common "verifier confirms a Seems Down event as Down" path.
func (t *Tx) Promote(ctx context.Context, eventID int64, newSeverity uint8, newState, reason, source string, metadata json.RawMessage) (bool, error) {
	if t.tx == nil {
		return false, nil
	}
	return t.mutate(ctx, eventID, mutation{
		severityAfter: &newSeverity,
		stateAfter:    &newState,
		reason:        reason,
		source:        source,
		metadata:      metadata,
	})
}

// LinkCause sets or clears the cause_event_id on an open event. Passing 0 (or
// a negative value) clears the existing link.
func (t *Tx) LinkCause(ctx context.Context, eventID, causeEventID int64, source string) (bool, error) {
	if t.tx == nil {
		return false, nil
	}
	cur, err := readEventForUpdate(ctx, t.tx, eventID)
	if err != nil {
		return false, err
	}
	if cur.endedAt.Valid {
		return false, ErrEventClosed
	}

	var newCause sql.NullInt64
	if causeEventID > 0 {
		newCause = sql.NullInt64{Int64: causeEventID, Valid: true}
	}
	if cur.causeEventID == newCause {
		return false, nil
	}

	if _, err := t.tx.ExecContext(ctx,
		`UPDATE jetmon_events SET cause_event_id = ? WHERE id = ?`,
		nullableInt64(newCause), eventID,
	); err != nil {
		return false, fmt.Errorf("update cause: %w", err)
	}

	reason := ReasonCauseLinked
	if !newCause.Valid {
		reason = ReasonCauseUnlinked
	}
	meta, err := json.Marshal(map[string]any{
		"cause_event_id_before": nullableInt64ToAny(cur.causeEventID),
		"cause_event_id_after":  nullableInt64ToAny(newCause),
	})
	if err != nil {
		return false, fmt.Errorf("marshal cause metadata: %w", err)
	}
	if err := writeTransition(ctx, t.tx, transitionInput{
		eventID:        eventID,
		blogID:         cur.blogID,
		severityBefore: &cur.severity,
		severityAfter:  &cur.severity,
		stateBefore:    cur.state,
		stateAfter:     cur.state,
		reason:         reason,
		source:         source,
		metadata:       meta,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// Close marks an open event as resolved. resolutionReason is recorded on the
// event row and used as the transition reason. Closing an already-closed event
// returns ErrEventClosed; closing a missing event returns ErrEventNotFound.
func (t *Tx) Close(ctx context.Context, eventID int64, resolutionReason, source string, metadata json.RawMessage) error {
	if t.tx == nil {
		return nil
	}
	if resolutionReason == "" {
		return errors.New("eventstore: Close requires resolutionReason")
	}
	cur, err := readEventForUpdate(ctx, t.tx, eventID)
	if err != nil {
		return err
	}
	if cur.endedAt.Valid {
		return ErrEventClosed
	}

	if _, err := t.tx.ExecContext(ctx, `
		UPDATE jetmon_events
		   SET ended_at = CURRENT_TIMESTAMP(3),
		       resolution_reason = ?
		 WHERE id = ?`,
		resolutionReason, eventID,
	); err != nil {
		return fmt.Errorf("close event: %w", err)
	}

	resolved := StateResolved
	return writeTransition(ctx, t.tx, transitionInput{
		eventID:        eventID,
		blogID:         cur.blogID,
		severityBefore: &cur.severity,
		severityAfter:  nil,
		stateBefore:    cur.state,
		stateAfter:     resolved,
		reason:         resolutionReason,
		source:         source,
		metadata:       metadata,
	})
}

// ActiveEvent is the minimal snapshot of an open event needed by callers that
// found it via FindActiveByBlog and now want to close, promote, or otherwise
// mutate it without a second round-trip to read its state.
type ActiveEvent struct {
	ID       int64
	Severity uint8
	State    string
}

// FindActiveByBlog returns the open event for (blog_id, check_type) — the
// most common lookup the orchestrator needs on recovery. Returns
// ErrEventNotFound if no open event exists. Used when the caller doesn't have
// the event id cached (e.g. a recovery in a round after the open was forgotten
// across a process restart).
func (t *Tx) FindActiveByBlog(ctx context.Context, blogID int64, checkType string) (ActiveEvent, error) {
	if t.tx == nil {
		return ActiveEvent{}, nil
	}
	var ae ActiveEvent
	err := t.tx.QueryRowContext(ctx, `
		SELECT id, severity, state FROM jetmon_events
		 WHERE blog_id = ? AND check_type = ? AND ended_at IS NULL
		 ORDER BY started_at ASC
		 LIMIT 1`, blogID, checkType,
	).Scan(&ae.ID, &ae.Severity, &ae.State)
	if errors.Is(err, sql.ErrNoRows) {
		return ActiveEvent{}, ErrEventNotFound
	}
	if err != nil {
		return ActiveEvent{}, fmt.Errorf("find active event: %w", err)
	}
	return ae, nil
}

// Standalone Store methods are thin wrappers that begin/commit a transaction
// around a single Tx call. Use these when no other writes need to land in the
// same transaction.

// Open is the standalone (auto-commit) form of Tx.Open.
func (s *Store) Open(ctx context.Context, in OpenInput) (OpenResult, error) {
	if s.db == nil {
		return OpenResult{}, nil
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Open(ctx, in)
	if err != nil {
		return OpenResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return OpenResult{}, fmt.Errorf("commit: %w", err)
	}
	return res, nil
}

// UpdateSeverity is the standalone form of Tx.UpdateSeverity.
func (s *Store) UpdateSeverity(ctx context.Context, eventID int64, newSeverity uint8, reason, source string, metadata json.RawMessage) (bool, error) {
	return s.runTx(ctx, func(tx *Tx) (bool, error) {
		return tx.UpdateSeverity(ctx, eventID, newSeverity, reason, source, metadata)
	})
}

// UpdateState is the standalone form of Tx.UpdateState.
func (s *Store) UpdateState(ctx context.Context, eventID int64, newState, reason, source string, metadata json.RawMessage) (bool, error) {
	return s.runTx(ctx, func(tx *Tx) (bool, error) {
		return tx.UpdateState(ctx, eventID, newState, reason, source, metadata)
	})
}

// Promote is the standalone form of Tx.Promote.
func (s *Store) Promote(ctx context.Context, eventID int64, newSeverity uint8, newState, reason, source string, metadata json.RawMessage) (bool, error) {
	return s.runTx(ctx, func(tx *Tx) (bool, error) {
		return tx.Promote(ctx, eventID, newSeverity, newState, reason, source, metadata)
	})
}

// LinkCause is the standalone form of Tx.LinkCause.
func (s *Store) LinkCause(ctx context.Context, eventID, causeEventID int64, source string) (bool, error) {
	return s.runTx(ctx, func(tx *Tx) (bool, error) {
		return tx.LinkCause(ctx, eventID, causeEventID, source)
	})
}

// Close is the standalone form of Tx.Close.
func (s *Store) Close(ctx context.Context, eventID int64, resolutionReason, source string, metadata json.RawMessage) error {
	if s.db == nil {
		return nil
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Close(ctx, eventID, resolutionReason, source, metadata); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *Store) runTx(ctx context.Context, fn func(*Tx) (bool, error)) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	changed, err := fn(tx)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return changed, nil
}

// mutation captures the pieces of a single severity/state change. severityAfter
// or stateAfter (or both) must be non-nil for a mutation to be written.
type mutation struct {
	severityAfter *uint8
	stateAfter    *string
	reason        string
	source        string
	metadata      json.RawMessage
}

func (t *Tx) mutate(ctx context.Context, eventID int64, m mutation) (bool, error) {
	if m.severityAfter == nil && m.stateAfter == nil {
		return false, errors.New("eventstore: mutate requires severityAfter or stateAfter")
	}
	if m.reason == "" {
		return false, errors.New("eventstore: mutate requires reason")
	}

	cur, err := readEventForUpdate(ctx, t.tx, eventID)
	if err != nil {
		return false, err
	}
	if cur.endedAt.Valid {
		return false, ErrEventClosed
	}

	severityChanged := m.severityAfter != nil && *m.severityAfter != cur.severity
	stateChanged := m.stateAfter != nil && *m.stateAfter != cur.state
	if !severityChanged && !stateChanged {
		// No-op — do not write a transition row.
		return false, nil
	}

	switch {
	case severityChanged && stateChanged:
		_, err = t.tx.ExecContext(ctx,
			`UPDATE jetmon_events SET severity = ?, state = ? WHERE id = ?`,
			*m.severityAfter, *m.stateAfter, eventID)
	case severityChanged:
		_, err = t.tx.ExecContext(ctx,
			`UPDATE jetmon_events SET severity = ? WHERE id = ?`,
			*m.severityAfter, eventID)
	case stateChanged:
		_, err = t.tx.ExecContext(ctx,
			`UPDATE jetmon_events SET state = ? WHERE id = ?`,
			*m.stateAfter, eventID)
	}
	if err != nil {
		return false, fmt.Errorf("update event: %w", err)
	}

	severityBefore := cur.severity
	severityAfter := cur.severity
	if m.severityAfter != nil {
		severityAfter = *m.severityAfter
	}
	stateAfter := cur.state
	if m.stateAfter != nil {
		stateAfter = *m.stateAfter
	}
	if err := writeTransition(ctx, t.tx, transitionInput{
		eventID:        eventID,
		blogID:         cur.blogID,
		severityBefore: &severityBefore,
		severityAfter:  &severityAfter,
		stateBefore:    cur.state,
		stateAfter:     stateAfter,
		reason:         m.reason,
		source:         m.source,
		metadata:       m.metadata,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// eventSnapshot is what readEventForUpdate returns: the columns we need to
// validate the mutation and to populate the *_before fields on the transition.
type eventSnapshot struct {
	blogID       int64
	severity     uint8
	state        string
	endedAt      sql.NullTime
	causeEventID sql.NullInt64
}

func readEventForUpdate(ctx context.Context, tx *sql.Tx, eventID int64) (eventSnapshot, error) {
	var snap eventSnapshot
	err := tx.QueryRowContext(ctx, `
		SELECT blog_id, severity, state, ended_at, cause_event_id
		  FROM jetmon_events
		 WHERE id = ?
		   FOR UPDATE`, eventID,
	).Scan(&snap.blogID, &snap.severity, &snap.state, &snap.endedAt, &snap.causeEventID)
	if errors.Is(err, sql.ErrNoRows) {
		return snap, ErrEventNotFound
	}
	if err != nil {
		return snap, fmt.Errorf("read event %d: %w", eventID, err)
	}
	return snap, nil
}

type transitionInput struct {
	eventID        int64
	blogID         int64
	severityBefore *uint8
	severityAfter  *uint8
	stateBefore    string
	stateAfter     string
	reason         string
	source         string
	metadata       json.RawMessage
}

func writeTransition(ctx context.Context, tx *sql.Tx, t transitionInput) error {
	source := t.source
	if source == "" {
		source = "local"
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO jetmon_event_transitions
			(event_id, blog_id, severity_before, severity_after,
			 state_before, state_after, reason, source, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.eventID, t.blogID,
		nullableUint8(t.severityBefore), nullableUint8(t.severityAfter),
		nullableString(t.stateBefore), nullableString(t.stateAfter),
		t.reason, source, nullableJSON(t.metadata),
	)
	if err != nil {
		return fmt.Errorf("insert transition: %w", err)
	}
	return nil
}

func nullableEndpoint(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableDiscriminator(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

func nullableUint8(p *uint8) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt64(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

func nullableInt64ToAny(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

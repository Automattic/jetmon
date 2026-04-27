package alerting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Automattic/jetmon/internal/eventstore"
)

// retrySchedule mirrors the webhooks worker schedule. Same trade-offs:
// six attempts over ~7h36m total elapsed, then abandon.
var retrySchedule = []time.Duration{
	0,
	1 * time.Minute,
	5 * time.Minute,
	30 * time.Minute,
	1 * time.Hour,
	6 * time.Hour,
}

const maxAttempts = 6

func nextRetryDelay(currentAttempt int) (delay time.Duration, abandoned bool) {
	next := currentAttempt + 1
	if next > maxAttempts {
		return 0, true
	}
	return retrySchedule[next-1], false
}

// WorkerConfig configures the delivery worker.
type WorkerConfig struct {
	DB              *sql.DB
	InstanceID      string
	Dispatchers     map[Transport]Dispatcher
	PollInterval    time.Duration
	MaxConcurrent   int           // shared deliverer pool size
	PerContactCap   int           // per-contact in-flight cap
	BatchSize       int           // dispatch + claim batch size
	DispatchTimeout time.Duration // per-delivery wall-clock limit
}

func (c *WorkerConfig) applyDefaults() {
	if c.PollInterval == 0 {
		c.PollInterval = 1 * time.Second
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 50
	}
	if c.PerContactCap == 0 {
		c.PerContactCap = 3
	}
	if c.BatchSize == 0 {
		c.BatchSize = 200
	}
	if c.DispatchTimeout == 0 {
		c.DispatchTimeout = 30 * time.Second
	}
	if c.InstanceID == "" {
		c.InstanceID = "default"
	}
}

// Worker drives alert contact delivery. Two background goroutines:
//
//   - dispatcher: every PollInterval, polls jetmon_event_transitions for
//     new rows since last_seen, matches each against active contacts
//     (site_filter + min_severity gate), and enqueues a delivery per
//     match.
//   - deliverer: every PollInterval, claims pending deliveries, picks
//     the right Dispatcher per contact, builds a Notification, and
//     calls Send. Successes mark delivered; failures schedule retries
//     on the standard ladder. Per-contact rate cap drops dispatches
//     when a contact's per-hour budget is exhausted.
type Worker struct {
	cfg WorkerConfig

	inFlightMu sync.Mutex
	inFlight   map[int64]int // contactID → current in-flight count

	rateLimit *rateLimitWindow

	stop chan struct{}
	done chan struct{}
}

// NewWorker constructs a Worker. Call Start to launch goroutines.
// Dispatchers map is required — without it, all dispatches fail with
// "transport not configured."
func NewWorker(cfg WorkerConfig) *Worker {
	cfg.applyDefaults()
	return &Worker{
		cfg:       cfg,
		inFlight:  make(map[int64]int),
		rateLimit: newRateLimitWindow(),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
}

// Start launches the background goroutines. Non-blocking.
func (w *Worker) Start() {
	go w.run()
}

// Stop signals shutdown and blocks until both goroutines exit.
func (w *Worker) Stop() {
	close(w.stop)
	<-w.done
}

func (w *Worker) run() {
	defer close(w.done)

	dispatcherDone := make(chan struct{})
	delivererDone := make(chan struct{})

	go func() {
		defer close(dispatcherDone)
		w.dispatchLoop()
	}()
	go func() {
		defer close(delivererDone)
		w.deliverLoop()
	}()

	<-dispatcherDone
	<-delivererDone
}

// ─── Dispatch loop ────────────────────────────────────────────────────

func (w *Worker) dispatchLoop() {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			if err := w.dispatchTick(); err != nil {
				log.Printf("alerting: dispatcher tick error: %v", err)
			}
		}
	}
}

// dispatchTick polls jetmon_event_transitions for new rows and
// enqueues per-contact deliveries for each match.
func (w *Worker) dispatchTick() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lastID, err := w.loadProgress(ctx)
	if err != nil {
		return fmt.Errorf("load progress: %w", err)
	}

	type transitionRow struct {
		id             int64
		eventID        int64
		blogID         int64
		severityBefore sql.NullInt64
		severityAfter  sql.NullInt64
		stateAfter     sql.NullString
		reason         string
		changedAt      time.Time
	}

	rows, err := w.cfg.DB.QueryContext(ctx, `
		SELECT id, event_id, blog_id, severity_before, severity_after, state_after, reason, changed_at
		  FROM jetmon_event_transitions
		 WHERE id > ?
		 ORDER BY id ASC
		 LIMIT ?`, lastID, w.cfg.BatchSize)
	if err != nil {
		return fmt.Errorf("query transitions: %w", err)
	}
	defer rows.Close()

	var transitions []transitionRow
	for rows.Next() {
		var t transitionRow
		if err := rows.Scan(&t.id, &t.eventID, &t.blogID, &t.severityBefore, &t.severityAfter, &t.stateAfter, &t.reason, &t.changedAt); err != nil {
			return fmt.Errorf("scan transition: %w", err)
		}
		transitions = append(transitions, t)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("transitions iterate: %w", err)
	}
	if len(transitions) == 0 {
		return nil
	}

	contacts, err := ListActive(ctx, w.cfg.DB)
	if err != nil {
		return fmt.Errorf("list active contacts: %w", err)
	}

	for _, t := range transitions {
		// Use SeverityUp as the conservative default when severity is
		// unknown. severity_after may be NULL on certain non-severity
		// transitions; the gate then evaluates as "not above any cap"
		// and Matches returns false unless prev_severity caused a
		// recovery.
		prev := uint8(eventstore.SeverityUp)
		if t.severityBefore.Valid {
			prev = uint8(t.severityBefore.Int64)
		}
		next := uint8(eventstore.SeverityUp)
		if t.severityAfter.Valid {
			next = uint8(t.severityAfter.Int64)
		}
		eventType := eventTypeForReason(t.reason)
		if eventType == "" {
			continue
		}
		state := ""
		if t.stateAfter.Valid {
			state = t.stateAfter.String
		}

		for i := range contacts {
			c := &contacts[i]
			if !c.Matches(prev, next, t.blogID) {
				continue
			}
			payload, err := buildPayload(eventType, t.id, t.eventID, t.blogID, t.reason, state, prev, next, t.changedAt)
			if err != nil {
				log.Printf("alerting: build payload event_id=%d transition_id=%d: %v", t.eventID, t.id, err)
				continue
			}
			if _, err := Enqueue(ctx, w.cfg.DB, EnqueueInput{
				AlertContactID: c.ID,
				TransitionID:   t.id,
				EventID:        t.eventID,
				EventType:      eventType,
				Severity:       next,
				Payload:        payload,
			}); err != nil {
				log.Printf("alerting: enqueue contact_id=%d transition_id=%d: %v", c.ID, t.id, err)
				continue
			}
		}
	}

	if err := w.saveProgress(ctx, transitions[len(transitions)-1].id); err != nil {
		return fmt.Errorf("save progress: %w", err)
	}
	return nil
}

// eventTypeForReason maps a transition reason to a coarse alerting
// event type. Less granular than the webhook event-type set because
// alert contacts care primarily about "did something happen" and let
// the severity gate drive what gets sent.
func eventTypeForReason(reason string) string {
	switch reason {
	case "opened":
		return "alert.opened"
	case "severity_escalation", "severity_deescalation":
		return "alert.severity_changed"
	case "state_change", "verifier_confirmed":
		return "alert.state_changed"
	case "verifier_cleared", "probe_cleared", "false_alarm",
		"manual_override", "maintenance_swallowed", "superseded", "auto_timeout":
		return "alert.closed"
	default:
		return ""
	}
}

// buildPayload returns the JSON body stored on the delivery row. Frozen
// at enqueue time. Includes both severity values so the renderer at
// dispatch time can correctly distinguish escalation from recovery.
func buildPayload(eventType string, transitionID, eventID, blogID int64, reason, state string, prev, next uint8, occurredAt time.Time) (json.RawMessage, error) {
	body := map[string]any{
		"type":            eventType,
		"occurred_at":     occurredAt.UTC().Format(time.RFC3339Nano),
		"transition_id":   transitionID,
		"event_id":        eventID,
		"site_id":         blogID,
		"reason":          reason,
		"state":           state,
		"severity_before": prev,
		"severity_after":  next,
	}
	return json.Marshal(body)
}

// loadProgress / saveProgress mirror the webhooks worker on the
// jetmon_alert_dispatch_progress table.
func (w *Worker) loadProgress(ctx context.Context) (int64, error) {
	var lastID int64
	err := w.cfg.DB.QueryRowContext(ctx,
		`SELECT last_transition_id FROM jetmon_alert_dispatch_progress WHERE instance_id = ?`,
		w.cfg.InstanceID,
	).Scan(&lastID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return lastID, nil
}

func (w *Worker) saveProgress(ctx context.Context, lastID int64) error {
	_, err := w.cfg.DB.ExecContext(ctx, `
		INSERT INTO jetmon_alert_dispatch_progress (instance_id, last_transition_id)
		VALUES (?, ?)
		ON DUPLICATE KEY UPDATE last_transition_id = VALUES(last_transition_id)`,
		w.cfg.InstanceID, lastID)
	return err
}

// ─── Deliver loop ─────────────────────────────────────────────────────

func (w *Worker) deliverLoop() {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			if err := w.deliverTick(); err != nil {
				log.Printf("alerting: deliverer tick error: %v", err)
			}
		}
	}
}

func (w *Worker) deliverTick() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	deliveries, err := ClaimReady(ctx, w.cfg.DB, w.cfg.MaxConcurrent)
	if err != nil {
		return err
	}
	for i := range deliveries {
		d := deliveries[i]
		if !w.acquireSlot(d.AlertContactID) {
			continue
		}
		go func(d Delivery) {
			defer w.releaseSlot(d.AlertContactID)
			w.deliver(d)
		}(d)
	}
	return nil
}

func (w *Worker) acquireSlot(contactID int64) bool {
	w.inFlightMu.Lock()
	defer w.inFlightMu.Unlock()
	if w.inFlight[contactID] >= w.cfg.PerContactCap {
		return false
	}
	w.inFlight[contactID]++
	return true
}

func (w *Worker) releaseSlot(contactID int64) {
	w.inFlightMu.Lock()
	defer w.inFlightMu.Unlock()
	w.inFlight[contactID]--
	if w.inFlight[contactID] <= 0 {
		delete(w.inFlight, contactID)
	}
}

// deliver runs one dispatch attempt for d. Loads the contact +
// destination, applies the rate cap, builds a Notification, and calls
// the configured Dispatcher. Updates the delivery row with the result.
func (w *Worker) deliver(d Delivery) {
	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.DispatchTimeout+5*time.Second)
	defer cancel()

	contact, err := Get(ctx, w.cfg.DB, d.AlertContactID)
	if err != nil {
		w.handleResult(ctx, d, 0, fmt.Sprintf("contact lookup: %v", err), true)
		return
	}
	if !contact.Active {
		w.handleResult(ctx, d, 0, "contact is inactive", true)
		return
	}

	// Per-contact rate cap. 0 = unlimited.
	if contact.MaxPerHour > 0 && !w.rateLimit.tryConsume(contact.ID, contact.MaxPerHour, time.Now()) {
		if err := MarkSuppressed(ctx, w.cfg.DB, d.ID,
			fmt.Sprintf("rate-limited: contact %d exceeded max_per_hour=%d", contact.ID, contact.MaxPerHour),
		); err != nil {
			log.Printf("alerting: mark suppressed id=%d: %v", d.ID, err)
		}
		return
	}

	dispatcher, ok := w.cfg.Dispatchers[contact.Transport]
	if !ok {
		w.handleResult(ctx, d, 0,
			fmt.Sprintf("transport %q not configured on this instance", contact.Transport), true)
		return
	}
	dest, err := LoadDestination(ctx, w.cfg.DB, contact.ID)
	if err != nil {
		w.handleResult(ctx, d, 0, fmt.Sprintf("destination lookup: %v", err), true)
		return
	}

	n, err := w.buildNotification(ctx, contact, d)
	if err != nil {
		w.handleResult(ctx, d, 0, fmt.Sprintf("build notification: %v", err), true)
		return
	}

	sendCtx, sendCancel := context.WithTimeout(ctx, w.cfg.DispatchTimeout)
	defer sendCancel()
	statusCode, respBody, sendErr := dispatcher.Send(sendCtx, dest, n)
	if sendErr != nil {
		w.handleResult(ctx, d, statusCode, "transport error: "+sendErr.Error(), false)
		return
	}
	if err := MarkDelivered(ctx, w.cfg.DB, d.ID, statusCode, respBody); err != nil {
		log.Printf("alerting: mark delivered id=%d: %v", d.ID, err)
	}
}

// buildNotification reconstructs the rendered Notification from the
// delivery row's frozen payload. Looks up the site URL from
// jetpack_monitor_sites if available so renderers have a useful
// display string; falls back to "site:<id>" if the lookup fails.
func (w *Worker) buildNotification(ctx context.Context, contact *AlertContact, d Delivery) (Notification, error) {
	var p struct {
		SiteID         int64     `json:"site_id"`
		EventID        int64     `json:"event_id"`
		EventType      string    `json:"type"`
		Reason         string    `json:"reason"`
		State          string    `json:"state"`
		SeverityBefore uint8     `json:"severity_before"`
		SeverityAfter  uint8     `json:"severity_after"`
		OccurredAt     time.Time `json:"occurred_at"`
	}
	if err := json.Unmarshal(d.Payload, &p); err != nil {
		return Notification{}, fmt.Errorf("decode payload: %w", err)
	}

	siteURL := lookupSiteURL(ctx, w.cfg.DB, p.SiteID)
	if siteURL == "" {
		siteURL = fmt.Sprintf("site:%d", p.SiteID)
	}

	recovery := p.SeverityBefore >= contact.MinSeverity && p.SeverityAfter == eventstore.SeverityUp
	severity := p.SeverityAfter

	return Notification{
		SiteID:       p.SiteID,
		SiteURL:      siteURL,
		EventID:      p.EventID,
		EventType:    p.EventType,
		Severity:     severity,
		SeverityName: SeverityName(severity),
		State:        p.State,
		Reason:       p.Reason,
		Timestamp:    p.OccurredAt,
		DedupKey:     fmt.Sprintf("jetmon-event-%d", p.EventID),
		Recovery:     recovery,
	}, nil
}

func lookupSiteURL(ctx context.Context, db *sql.DB, blogID int64) string {
	var url sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT monitor_url FROM jetpack_monitor_sites WHERE blog_id = ? LIMIT 1`,
		blogID,
	).Scan(&url)
	if err != nil || !url.Valid {
		return ""
	}
	return url.String
}

func (w *Worker) handleResult(ctx context.Context, d Delivery, statusCode int, responseBody string, forceAbandon bool) {
	currentAttempt := d.Attempt + 1
	var (
		next      time.Time
		abandoned bool
	)
	if forceAbandon {
		abandoned = true
	} else {
		delay, ab := nextRetryDelay(currentAttempt)
		abandoned = ab
		if !abandoned {
			next = time.Now().Add(delay)
		}
	}
	if err := ScheduleRetry(ctx, w.cfg.DB, d.ID, statusCode, responseBody, next, abandoned); err != nil {
		log.Printf("alerting: schedule retry id=%d: %v", d.ID, err)
	}
}

// ─── Rate limit window ────────────────────────────────────────────────

// rateLimitWindow tracks recent dispatch timestamps per contact for
// the per-hour rate cap. Sliding window via timestamp pruning.
//
// In-memory only; multi-instance deployments share state via the DB
// today. For a single-instance deployment this is correct; for
// multi-instance, each instance enforces its own slice of the cap and
// the actual delivered rate per contact may exceed the configured
// max_per_hour by the number of instances. Tracked alongside the
// "multi-instance row claim" caveat in deliveries.go.
type rateLimitWindow struct {
	mu         sync.Mutex
	perContact map[int64][]time.Time
}

func newRateLimitWindow() *rateLimitWindow {
	return &rateLimitWindow{perContact: make(map[int64][]time.Time)}
}

// tryConsume attempts to allocate a delivery for the given contact at
// the given timestamp. Returns true if the window is under capacity
// (and records the timestamp); false if the window is at capacity.
func (r *rateLimitWindow) tryConsume(contactID int64, capacity int, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := now.Add(-1 * time.Hour)
	stamps := r.perContact[contactID]
	pruned := stamps[:0]
	for _, t := range stamps {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	if len(pruned) >= capacity {
		r.perContact[contactID] = pruned
		return false
	}
	r.perContact[contactID] = append(pruned, now)
	return true
}

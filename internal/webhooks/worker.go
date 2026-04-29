package webhooks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// retrySchedule maps the *next* attempt number to its delay from the
// previous attempt. attempt 1 is the initial enqueue (immediate); attempts
// 2–6 retry at the documented intervals.
//
// After attempt 6 fails, the delivery is abandoned. Total elapsed time
// from first attempt to abandonment: ~7h36m. See docs/internal-api-reference.md for rationale.
var retrySchedule = []time.Duration{
	0,                // attempt 1 — initial enqueue, no retry delay
	1 * time.Minute,  // attempt 2
	5 * time.Minute,  // attempt 3
	30 * time.Minute, // attempt 4
	1 * time.Hour,    // attempt 5
	6 * time.Hour,    // attempt 6
}

// maxAttempts is the highest attempt number we'll try. After attempt 6
// fails, the row is marked abandoned.
const maxAttempts = 6

// nextRetryDelay returns the delay until the next attempt given the
// current attempt count (1-indexed: 1 is the first POST, 6 is the last).
// abandoned=true means there is no next attempt — the delivery should
// be marked abandoned.
func nextRetryDelay(currentAttempt int) (delay time.Duration, abandoned bool) {
	next := currentAttempt + 1
	if next > maxAttempts {
		return 0, true
	}
	return retrySchedule[next-1], false
}

// WorkerConfig configures the delivery worker. Defaults are sensible for
// a single jetmon2 instance; multi-instance deployments should set
// InstanceID to a unique value per instance so each tracks its own
// dispatch progress.
type WorkerConfig struct {
	DB            *sql.DB
	InstanceID    string        // key into jetmon_webhook_dispatch_progress
	PollInterval  time.Duration // default 1s
	MaxConcurrent int           // shared deliverer pool size; default 50
	PerWebhookCap int           // per-webhook in-flight cap; default 3
	HTTPTimeout   time.Duration // per-delivery HTTP timeout; default 30s
	BatchSize     int           // dispatcher's transition fetch + deliverer's claim batch; default 200
}

func (c *WorkerConfig) applyDefaults() {
	if c.PollInterval == 0 {
		c.PollInterval = 1 * time.Second
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = 50
	}
	if c.PerWebhookCap == 0 {
		c.PerWebhookCap = 3
	}
	if c.HTTPTimeout == 0 {
		c.HTTPTimeout = 30 * time.Second
	}
	if c.BatchSize == 0 {
		c.BatchSize = 200
	}
	if c.InstanceID == "" {
		c.InstanceID = "default"
	}
}

// Worker drives webhook delivery. Two background goroutines:
//
//   - dispatcher: every PollInterval, polls jetmon_event_transitions for
//     new rows since last_seen, matches each against active webhooks,
//     and enqueues a delivery per match.
//   - deliverer: every PollInterval, claims pending deliveries whose
//     next_attempt_at has passed and POSTs them with HMAC signing.
//     Successes mark delivered; failures schedule retries on the
//     exponential backoff schedule until attempt 6, then abandon.
//
// Both goroutines run continuously until Stop is called. Stop blocks
// until both have exited cleanly.
type Worker struct {
	cfg        WorkerConfig
	httpClient *http.Client

	inFlightMu sync.Mutex
	inFlight   map[int64]int // webhook_id → current in-flight count

	stop chan struct{}
	done chan struct{}
}

// NewWorker constructs a Worker. Call Start to launch the goroutines.
func NewWorker(cfg WorkerConfig) *Worker {
	cfg.applyDefaults()
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Worker{
		cfg:        cfg,
		httpClient: &http.Client{Transport: transport, Timeout: cfg.HTTPTimeout},
		inFlight:   make(map[int64]int),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// Start launches the dispatcher and deliverer goroutines. Call Stop to
// signal shutdown. Start is non-blocking.
func (w *Worker) Start() {
	go w.run()
}

// Stop signals the goroutines to exit and waits for them.
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

// dispatchLoop is the polling loop for the dispatcher.
func (w *Worker) dispatchLoop() {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			if err := w.dispatchTick(); err != nil {
				log.Printf("webhooks: dispatcher tick error: %v", err)
			}
		}
	}
}

// dispatchTick polls jetmon_event_transitions for new rows and creates
// deliveries for each match against an active webhook.
func (w *Worker) dispatchTick() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	lastID, err := w.loadProgress(ctx)
	if err != nil {
		return fmt.Errorf("load progress: %w", err)
	}

	type transitionRow struct {
		id         int64
		eventID    int64
		blogID     int64
		stateAfter sql.NullString
		reason     string
		changedAt  time.Time
	}
	rows, err := w.cfg.DB.QueryContext(ctx, `
		SELECT id, event_id, blog_id, state_after, reason, changed_at
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
		if err := rows.Scan(&t.id, &t.eventID, &t.blogID, &t.stateAfter, &t.reason, &t.changedAt); err != nil {
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

	hooks, err := ListActive(ctx, w.cfg.DB)
	if err != nil {
		return fmt.Errorf("list active webhooks: %w", err)
	}

	for _, t := range transitions {
		eventType := EventTypeForReason(t.reason)
		if eventType == "" {
			continue
		}
		state := ""
		if t.stateAfter.Valid {
			state = t.stateAfter.String
		}
		for i := range hooks {
			h := &hooks[i]
			if !h.Matches(eventType, t.blogID, state) {
				continue
			}
			payload, err := w.buildPayload(eventType, t.id, t.eventID, t.blogID, t.reason, state, t.changedAt)
			if err != nil {
				log.Printf("webhooks: build payload event_id=%d transition_id=%d: %v",
					t.eventID, t.id, err)
				continue
			}
			if _, err := Enqueue(ctx, w.cfg.DB, EnqueueInput{
				WebhookID:    h.ID,
				TransitionID: t.id,
				EventID:      t.eventID,
				EventType:    eventType,
				Payload:      payload,
			}); err != nil {
				log.Printf("webhooks: enqueue webhook_id=%d transition_id=%d: %v",
					h.ID, t.id, err)
				continue
			}
		}
	}

	if err := w.saveProgress(ctx, transitions[len(transitions)-1].id); err != nil {
		return fmt.Errorf("save progress: %w", err)
	}
	return nil
}

// buildPayload returns the JSON body that the consumer receives. Frozen at
// enqueue time — see docs/internal-api-reference.md "frozen-at-fire-time" contract.
//
// Shape is flat: type, occurred_at, ids, and the relevant event/transition
// fields. Consumers who want full event detail call GET /events/{id}.
func (w *Worker) buildPayload(eventType string, transitionID, eventID, blogID int64, reason, state string, occurredAt time.Time) (json.RawMessage, error) {
	body := map[string]any{
		"type":          eventType,
		"occurred_at":   occurredAt.UTC().Format(time.RFC3339Nano),
		"transition_id": transitionID,
		"event_id":      eventID,
		"site_id":       blogID,
		"reason":        reason,
		"state":         state,
	}
	return json.Marshal(body)
}

// loadProgress reads the last_transition_id high-water mark for this
// instance from jetmon_webhook_dispatch_progress. Returns 0 if no row
// exists yet (first tick).
func (w *Worker) loadProgress(ctx context.Context) (int64, error) {
	var lastID int64
	err := w.cfg.DB.QueryRowContext(ctx,
		`SELECT last_transition_id FROM jetmon_webhook_dispatch_progress WHERE instance_id = ?`,
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

// saveProgress upserts the last_transition_id high-water mark for this
// instance. Multi-instance: each instance has its own row keyed on
// instance_id, so they don't trample each other's progress.
func (w *Worker) saveProgress(ctx context.Context, lastID int64) error {
	_, err := w.cfg.DB.ExecContext(ctx, `
		INSERT INTO jetmon_webhook_dispatch_progress (instance_id, last_transition_id)
		VALUES (?, ?)
		ON DUPLICATE KEY UPDATE last_transition_id = VALUES(last_transition_id)`,
		w.cfg.InstanceID, lastID)
	return err
}

// deliverLoop is the polling loop for the deliverer. It pulls ready
// deliveries from the queue and dispatches each as a goroutine, subject
// to the per-webhook in-flight cap.
func (w *Worker) deliverLoop() {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			if err := w.deliverTick(); err != nil {
				log.Printf("webhooks: deliverer tick error: %v", err)
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
		if !w.acquireSlot(d.WebhookID) {
			// Per-webhook cap reached; row stays pending and we'll pick
			// it up next tick.
			continue
		}
		go func(d Delivery) {
			defer w.releaseSlot(d.WebhookID)
			w.deliver(d)
		}(d)
	}
	return nil
}

// acquireSlot tries to reserve a per-webhook in-flight slot. Returns true
// if reserved, false if the webhook is already at its cap.
func (w *Worker) acquireSlot(webhookID int64) bool {
	w.inFlightMu.Lock()
	defer w.inFlightMu.Unlock()
	if w.inFlight[webhookID] >= w.cfg.PerWebhookCap {
		return false
	}
	w.inFlight[webhookID]++
	return true
}

func (w *Worker) releaseSlot(webhookID int64) {
	w.inFlightMu.Lock()
	defer w.inFlightMu.Unlock()
	w.inFlight[webhookID]--
	if w.inFlight[webhookID] <= 0 {
		delete(w.inFlight, webhookID)
	}
}

// deliver runs one POST attempt against the consumer URL. Updates the
// delivery row with success/retry/abandon based on the response.
func (w *Worker) deliver(d Delivery) {
	ctx, cancel := context.WithTimeout(context.Background(), w.cfg.HTTPTimeout+5*time.Second)
	defer cancel()

	// Look up the URL and signing secret from the webhook row. Either may
	// be missing if the webhook was deleted between dispatch and deliver,
	// in which case we abandon the row (the delivery target is gone).
	hook, err := Get(ctx, w.cfg.DB, d.WebhookID)
	if err != nil {
		w.handleResult(ctx, d, 0, fmt.Sprintf("webhook lookup: %v", err), true)
		return
	}
	secret, err := LoadSecret(ctx, w.cfg.DB, d.WebhookID)
	if err != nil {
		w.handleResult(ctx, d, 0, fmt.Sprintf("secret lookup: %v", err), true)
		return
	}
	if !hook.Active {
		// Webhook was paused between dispatch and deliver. Abandon: the
		// caller doesn't want this delivery anymore.
		w.handleResult(ctx, d, 0, "webhook is inactive", true)
		return
	}

	timestamp := time.Now()
	signature := Sign(timestamp, d.Payload, secret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.URL, bytes.NewReader(d.Payload))
	if err != nil {
		w.handleResult(ctx, d, 0, fmt.Sprintf("build request: %v", err), false)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Jetmon-Event", d.EventType)
	req.Header.Set("X-Jetmon-Delivery", strconv.FormatInt(d.ID, 10))
	req.Header.Set("X-Jetmon-Signature", signature)

	resp, err := w.httpClient.Do(req)
	if err != nil {
		// Network-level failure: connection refused, DNS, timeout, TLS.
		// Record the error message as last_response and schedule retry.
		w.handleResult(ctx, d, 0, "transport: "+err.Error(), false)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := MarkDelivered(ctx, w.cfg.DB, d.ID, resp.StatusCode, string(body)); err != nil {
			log.Printf("webhooks: mark delivered id=%d: %v", d.ID, err)
		}
		return
	}
	// Any non-2xx is retried. Some 4xx (404, 410) might warrant immediate
	// abandonment, but for v1 we treat all non-2xx alike — consumers
	// occasionally return 4xx during deploys, and a single 4xx shouldn't
	// permanently fail an otherwise-recoverable webhook.
	w.handleResult(ctx, d, resp.StatusCode, string(body), false)
}

// handleResult writes the delivery outcome to the database. forceAbandon
// is true for non-retryable failures (webhook deleted/inactive, request
// build error); otherwise the retry schedule decides whether to retry or
// abandon based on the attempt count.
func (w *Worker) handleResult(ctx context.Context, d Delivery, statusCode int, responseBody string, forceAbandon bool) {
	currentAttempt := d.Attempt + 1 // we just completed this attempt
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
		log.Printf("webhooks: schedule retry id=%d: %v", d.ID, err)
	}
}

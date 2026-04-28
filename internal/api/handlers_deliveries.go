package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Automattic/jetmon/internal/webhooks"
)

// deliveryResponse is the JSON shape for a webhook delivery row. Fields are
// flat (not nested) so consumers can easily filter and sort. payload is
// pass-through json.RawMessage — whatever the dispatcher froze at fire time.
type deliveryResponse struct {
	ID             int64           `json:"id"`
	WebhookID      int64           `json:"webhook_id"`
	TransitionID   int64           `json:"transition_id"`
	EventID        int64           `json:"event_id"`
	EventType      string          `json:"event_type"`
	Payload        json.RawMessage `json:"payload"`
	Status         string          `json:"status"`
	Attempt        int             `json:"attempt"`
	NextAttemptAt  *string         `json:"next_attempt_at"`
	LastStatusCode *int            `json:"last_status_code"`
	LastResponse   *string         `json:"last_response"`
	LastAttemptAt  *string         `json:"last_attempt_at"`
	DeliveredAt    *string         `json:"delivered_at"`
	CreatedAt      string          `json:"created_at"`
}

func toDeliveryResponse(d *webhooks.Delivery) deliveryResponse {
	out := deliveryResponse{
		ID:             d.ID,
		WebhookID:      d.WebhookID,
		TransitionID:   d.TransitionID,
		EventID:        d.EventID,
		EventType:      d.EventType,
		Payload:        d.Payload,
		Status:         string(d.Status),
		Attempt:        d.Attempt,
		LastStatusCode: d.LastStatusCode,
		LastResponse:   d.LastResponse,
		CreatedAt:      d.CreatedAt.UTC().Format(time.RFC3339),
	}
	if d.NextAttemptAt != nil {
		v := d.NextAttemptAt.UTC().Format(time.RFC3339)
		out.NextAttemptAt = &v
	}
	if d.LastAttemptAt != nil {
		v := d.LastAttemptAt.UTC().Format(time.RFC3339)
		out.LastAttemptAt = &v
	}
	if d.DeliveredAt != nil {
		v := d.DeliveredAt.UTC().Format(time.RFC3339)
		out.DeliveredAt = &v
	}
	return out
}

// handleListDeliveries implements GET /api/v1/webhooks/{id}/deliveries.
//
// Filters:
//   - status: pending | delivered | failed | abandoned (single value)
//
// Cursor pagination on id (descending — most recent first).
func (s *Server) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	webhookID, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_webhook_id",
			"webhook id must be a positive integer")
		return
	}
	if !s.ensureWebhookOwnedForRequest(w, r, webhookID) {
		return
	}

	q := r.URL.Query()
	limit, err := parseLimit(q.Get("limit"), 50, 200)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	cursor, err := decodeIDCursor(q.Get("cursor"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}

	var status webhooks.Status
	if v := q.Get("status"); v != "" {
		switch webhooks.Status(v) {
		case webhooks.StatusPending, webhooks.StatusDelivered,
			webhooks.StatusFailed, webhooks.StatusAbandoned:
			status = webhooks.Status(v)
		default:
			writeError(w, r, http.StatusBadRequest, "invalid_status",
				"status must be one of: pending, delivered, failed, abandoned")
			return
		}
	}

	// Fetch limit+1 to detect a next-page boundary without an extra count.
	rows, err := webhooks.ListDeliveries(r.Context(), s.db, webhookID, status, cursor, limit+1)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"deliveries list failed: "+err.Error())
		return
	}

	out := make([]deliveryResponse, 0, len(rows))
	for i := range rows {
		out = append(out, toDeliveryResponse(&rows[i]))
	}

	var nextCursor *string
	if len(out) > limit {
		out = out[:limit]
		c := encodeIDCursor(out[len(out)-1].ID)
		nextCursor = &c
	}

	writeJSON(w, http.StatusOK, ListEnvelope{
		Data: out,
		Page: Page{Next: nextCursor, Limit: limit},
	})
}

// handleRetryDelivery implements POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/retry.
//
// Resets an abandoned delivery row to pending so the worker picks it up
// on the next tick. Used by operators after fixing a previously-broken
// consumer endpoint. Only abandoned deliveries can be retried — pending
// ones are already in the queue, delivered ones don't need to fire again.
func (s *Server) handleRetryDelivery(w http.ResponseWriter, r *http.Request) {
	webhookID, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_webhook_id",
			"webhook id must be a positive integer")
		return
	}
	deliveryID, err := parseIDPath(r, "delivery_id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_delivery_id",
			"delivery id must be a positive integer")
		return
	}
	if !s.ensureWebhookOwnedForRequest(w, r, webhookID) {
		return
	}

	// Cross-check: the delivery must belong to the named webhook. This
	// matches the cross-site protection we use elsewhere — an explicit
	// 404 if the consumer asks under the wrong webhook.
	d, err := webhooks.GetDelivery(r.Context(), s.db, deliveryID)
	if err != nil {
		if errors.Is(err, webhooks.ErrDeliveryNotFound) {
			writeError(w, r, http.StatusNotFound, "delivery_not_found",
				fmt.Sprintf("Delivery %d does not exist", deliveryID))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"delivery lookup failed: "+err.Error())
		return
	}
	if d.WebhookID != webhookID {
		writeError(w, r, http.StatusNotFound, "delivery_not_found",
			fmt.Sprintf("Delivery %d does not belong to webhook %d", deliveryID, webhookID))
		return
	}

	if err := webhooks.RetryDelivery(r.Context(), s.db, deliveryID); err != nil {
		// Distinguish "not abandoned" (currently pending or delivered) from
		// other DB errors so the caller gets a useful message.
		writeError(w, r, http.StatusConflict, "delivery_not_retryable", err.Error())
		return
	}

	// Read back the updated row so the caller sees the new pending state.
	d, err = webhooks.GetDelivery(r.Context(), s.db, deliveryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"read-back failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toDeliveryResponse(d))
}

func (s *Server) ensureWebhookOwnedForRequest(w http.ResponseWriter, r *http.Request, id int64) bool {
	tenantID, ok := ownerTenantIDFromRequest(r)
	if !ok {
		return true
	}
	if _, err := webhooks.GetForTenant(r.Context(), s.db, id, tenantID); err != nil {
		if errors.Is(err, webhooks.ErrWebhookNotFound) {
			writeError(w, r, http.StatusNotFound, "webhook_not_found",
				fmt.Sprintf("Webhook %d does not exist", id))
			return false
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"webhook lookup failed: "+err.Error())
		return false
	}
	return true
}

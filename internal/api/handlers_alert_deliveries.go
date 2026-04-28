package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Automattic/jetmon/internal/alerting"
)

// alertDeliveryResponse is the JSON shape for an alert delivery row.
// Same flat shape as deliveryResponse for webhooks — flat fields are
// easier to filter and sort than a nested envelope.
type alertDeliveryResponse struct {
	ID             int64           `json:"id"`
	AlertContactID int64           `json:"alert_contact_id"`
	TransitionID   int64           `json:"transition_id"`
	EventID        int64           `json:"event_id"`
	EventType      string          `json:"event_type"`
	Severity       string          `json:"severity"`
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

func toAlertDeliveryResponse(d *alerting.Delivery) alertDeliveryResponse {
	out := alertDeliveryResponse{
		ID:             d.ID,
		AlertContactID: d.AlertContactID,
		TransitionID:   d.TransitionID,
		EventID:        d.EventID,
		EventType:      d.EventType,
		Severity:       alerting.SeverityName(d.Severity),
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

// handleListAlertDeliveries implements
// GET /api/v1/alert-contacts/{id}/deliveries.
func (s *Server) handleListAlertDeliveries(w http.ResponseWriter, r *http.Request) {
	contactID, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_alert_contact_id",
			"alert contact id must be a positive integer")
		return
	}
	if !s.ensureAlertContactOwnedForRequest(w, r, contactID) {
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

	var status alerting.Status
	if v := q.Get("status"); v != "" {
		switch alerting.Status(v) {
		case alerting.StatusPending, alerting.StatusDelivered,
			alerting.StatusFailed, alerting.StatusAbandoned:
			status = alerting.Status(v)
		default:
			writeError(w, r, http.StatusBadRequest, "invalid_status",
				"status must be one of: pending, delivered, failed, abandoned")
			return
		}
	}

	rows, err := alerting.ListDeliveries(r.Context(), s.db, contactID, status, cursor, limit+1)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"alert deliveries list failed: "+err.Error())
		return
	}

	out := make([]alertDeliveryResponse, 0, len(rows))
	for i := range rows {
		out = append(out, toAlertDeliveryResponse(&rows[i]))
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

// handleRetryAlertDelivery implements
// POST /api/v1/alert-contacts/{id}/deliveries/{delivery_id}/retry.
//
// Resets an abandoned delivery row to pending so the worker picks it
// up on the next tick. Same semantics as the webhook retry endpoint —
// only abandoned deliveries can be retried; pending ones are already
// queued and delivered ones don't need to fire again.
func (s *Server) handleRetryAlertDelivery(w http.ResponseWriter, r *http.Request) {
	contactID, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_alert_contact_id",
			"alert contact id must be a positive integer")
		return
	}
	deliveryID, err := parseIDPath(r, "delivery_id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_delivery_id",
			"delivery id must be a positive integer")
		return
	}
	if !s.ensureAlertContactOwnedForRequest(w, r, contactID) {
		return
	}

	d, err := alerting.GetDelivery(r.Context(), s.db, deliveryID)
	if err != nil {
		if errors.Is(err, alerting.ErrDeliveryNotFound) {
			writeError(w, r, http.StatusNotFound, "delivery_not_found",
				fmt.Sprintf("Delivery %d does not exist", deliveryID))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"delivery lookup failed: "+err.Error())
		return
	}
	if d.AlertContactID != contactID {
		writeError(w, r, http.StatusNotFound, "delivery_not_found",
			fmt.Sprintf("Delivery %d does not belong to alert contact %d", deliveryID, contactID))
		return
	}

	if err := alerting.RetryDelivery(r.Context(), s.db, deliveryID); err != nil {
		writeError(w, r, http.StatusConflict, "delivery_not_retryable", err.Error())
		return
	}

	d, err = alerting.GetDelivery(r.Context(), s.db, deliveryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"read-back failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAlertDeliveryResponse(d))
}

func (s *Server) ensureAlertContactOwnedForRequest(w http.ResponseWriter, r *http.Request, id int64) bool {
	tenantID, ok := ownerTenantIDFromRequest(r)
	if !ok {
		return true
	}
	if _, err := alerting.GetForTenant(r.Context(), s.db, id, tenantID); err != nil {
		if errors.Is(err, alerting.ErrContactNotFound) {
			writeError(w, r, http.StatusNotFound, "alert_contact_not_found",
				fmt.Sprintf("Alert contact %d does not exist", id))
			return false
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"alert contact lookup failed: "+err.Error())
		return false
	}
	return true
}

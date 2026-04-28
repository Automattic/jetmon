package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Automattic/jetmon/internal/alerting"
)

// alertContactResponse is the JSON shape for an alert contact in
// list/single responses. The destination credential is never returned;
// only DestinationPreview (last 4 chars) is exposed.
type alertContactResponse struct {
	ID                 int64               `json:"id"`
	Label              string              `json:"label"`
	Active             bool                `json:"active"`
	Transport          string              `json:"transport"`
	DestinationPreview string              `json:"destination_preview"`
	SiteFilter         alerting.SiteFilter `json:"site_filter"`
	MinSeverity        string              `json:"min_severity"`
	MaxPerHour         int                 `json:"max_per_hour"`
	CreatedBy          string              `json:"created_by"`
	CreatedAt          string              `json:"created_at"`
	UpdatedAt          string              `json:"updated_at"`
}

func toAlertContactResponse(c *alerting.AlertContact) alertContactResponse {
	return alertContactResponse{
		ID:                 c.ID,
		Label:              c.Label,
		Active:             c.Active,
		Transport:          string(c.Transport),
		DestinationPreview: c.DestinationPreview,
		SiteFilter:         c.SiteFilter,
		MinSeverity:        alerting.SeverityName(c.MinSeverity),
		MaxPerHour:         c.MaxPerHour,
		CreatedBy:          c.CreatedBy,
		CreatedAt:          c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// createAlertContactRequest is the body shape for POST /api/v1/alert-contacts.
// MinSeverity is a string ("Down", "Warning", etc.) on the wire — it's
// translated to the internal uint8 before passing to the alerting
// package.
type createAlertContactRequest struct {
	Label       string              `json:"label"`
	Active      *bool               `json:"active"`
	Transport   string              `json:"transport"`
	Destination json.RawMessage     `json:"destination"`
	SiteFilter  alerting.SiteFilter `json:"site_filter"`
	MinSeverity *string             `json:"min_severity"`
	MaxPerHour  *int                `json:"max_per_hour"`
}

// updateAlertContactRequest is the body shape for PATCH
// /api/v1/alert-contacts/{id}. Pointer fields distinguish absent from
// explicitly empty. Transport itself cannot be changed via PATCH —
// see API.md "Family 5".
type updateAlertContactRequest struct {
	Label       *string              `json:"label"`
	Active      *bool                `json:"active"`
	Destination json.RawMessage      `json:"destination"`
	SiteFilter  *alerting.SiteFilter `json:"site_filter"`
	MinSeverity *string              `json:"min_severity"`
	MaxPerHour  *int                 `json:"max_per_hour"`
}

// alertContactTestResponse is returned by POST /alert-contacts/{id}/test.
type alertContactTestResponse struct {
	ContactID    int64  `json:"contact_id"`
	Transport    string `json:"transport"`
	StatusCode   int    `json:"status_code"`
	ResponseBody string `json:"response_body"`
	Error        string `json:"error,omitempty"`
	Delivered    bool   `json:"delivered"`
}

// handleCreateAlertContact implements POST /api/v1/alert-contacts.
func (s *Server) handleCreateAlertContact(w http.ResponseWriter, r *http.Request) {
	var body createAlertContactRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body",
			"request body must be valid JSON: "+err.Error())
		return
	}
	if !alerting.IsValidTransport(body.Transport) {
		writeError(w, r, http.StatusBadRequest, "invalid_transport",
			fmt.Sprintf("transport must be one of: email, pagerduty, slack, teams (got %q)", body.Transport))
		return
	}

	in := alerting.CreateInput{
		Label:       body.Label,
		Active:      body.Active,
		Transport:   alerting.Transport(body.Transport),
		Destination: body.Destination,
		SiteFilter:  body.SiteFilter,
		MaxPerHour:  body.MaxPerHour,
		CreatedBy:   consumerName(r),
	}
	if body.MinSeverity != nil {
		sev, err := alerting.SeverityFromName(*body.MinSeverity)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_severity",
				fmt.Sprintf("min_severity must be one of: %v", alerting.AllSeverityNames()))
			return
		}
		in.MinSeverity = &sev
	}

	contact, err := alerting.Create(r.Context(), s.db, in)
	if err != nil {
		writeAlertingValidationError(w, r, err, "alert contact create failed")
		return
	}
	writeJSON(w, http.StatusCreated, toAlertContactResponse(contact))
}

// handleListAlertContacts implements GET /api/v1/alert-contacts.
func (s *Server) handleListAlertContacts(w http.ResponseWriter, r *http.Request) {
	contacts, err := alerting.List(r.Context(), s.db)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"alert contact list failed: "+err.Error())
		return
	}
	out := make([]alertContactResponse, 0, len(contacts))
	for i := range contacts {
		out = append(out, toAlertContactResponse(&contacts[i]))
	}
	writeJSON(w, http.StatusOK, ListEnvelope{
		Data: out,
		Page: Page{Next: nil, Limit: len(out)},
	})
}

// handleGetAlertContact implements GET /api/v1/alert-contacts/{id}.
func (s *Server) handleGetAlertContact(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_alert_contact_id",
			"alert contact id must be a positive integer")
		return
	}
	contact, err := alerting.Get(r.Context(), s.db, id)
	if err != nil {
		if errors.Is(err, alerting.ErrContactNotFound) {
			writeError(w, r, http.StatusNotFound, "alert_contact_not_found",
				fmt.Sprintf("Alert contact %d does not exist", id))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"alert contact lookup failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAlertContactResponse(contact))
}

// handleUpdateAlertContact implements PATCH /api/v1/alert-contacts/{id}.
func (s *Server) handleUpdateAlertContact(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_alert_contact_id",
			"alert contact id must be a positive integer")
		return
	}
	var body updateAlertContactRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body",
			"request body must be valid JSON: "+err.Error())
		return
	}

	in := alerting.UpdateInput{
		Label:       body.Label,
		Active:      body.Active,
		Destination: body.Destination,
		SiteFilter:  body.SiteFilter,
		MaxPerHour:  body.MaxPerHour,
	}
	if body.MinSeverity != nil {
		sev, err := alerting.SeverityFromName(*body.MinSeverity)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_severity",
				fmt.Sprintf("min_severity must be one of: %v", alerting.AllSeverityNames()))
			return
		}
		in.MinSeverity = &sev
	}

	contact, err := alerting.Update(r.Context(), s.db, id, in)
	if err != nil {
		if errors.Is(err, alerting.ErrContactNotFound) {
			writeError(w, r, http.StatusNotFound, "alert_contact_not_found",
				fmt.Sprintf("Alert contact %d does not exist", id))
			return
		}
		writeAlertingValidationError(w, r, err, "alert contact update failed")
		return
	}
	writeJSON(w, http.StatusOK, toAlertContactResponse(contact))
}

// handleDeleteAlertContact implements DELETE /api/v1/alert-contacts/{id}.
// Hard delete — see comment on handleDeleteWebhook for rationale.
func (s *Server) handleDeleteAlertContact(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_alert_contact_id",
			"alert contact id must be a positive integer")
		return
	}
	if err := alerting.Delete(r.Context(), s.db, id); err != nil {
		if errors.Is(err, alerting.ErrContactNotFound) {
			writeError(w, r, http.StatusNotFound, "alert_contact_not_found",
				fmt.Sprintf("Alert contact %d does not exist", id))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"alert contact delete failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAlertContactTest implements POST /api/v1/alert-contacts/{id}/test.
//
// Sends a synthetic notification through the contact's transport — same
// rendering, same dispatch path, but with a test-flagged Notification.
// Bypasses the severity gate and the per-hour rate cap; logged in
// jetmon_audit_log via the API auth middleware.
//
// Returns the transport's status_code + truncated response body so
// operators can verify connectivity. Transport-level errors are
// surfaced as 502 (we successfully called the transport, but the
// transport reported a failure).
func (s *Server) handleAlertContactTest(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_alert_contact_id",
			"alert contact id must be a positive integer")
		return
	}
	contact, err := alerting.Get(r.Context(), s.db, id)
	if err != nil {
		if errors.Is(err, alerting.ErrContactNotFound) {
			writeError(w, r, http.StatusNotFound, "alert_contact_not_found",
				fmt.Sprintf("Alert contact %d does not exist", id))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"alert contact lookup failed: "+err.Error())
		return
	}

	dispatcher, ok := s.alertDispatchers[contact.Transport]
	if !ok {
		writeError(w, r, http.StatusServiceUnavailable, "transport_not_configured",
			fmt.Sprintf("transport %q is not configured on this server", contact.Transport))
		return
	}
	dest, err := alerting.LoadDestination(r.Context(), s.db, id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"alert contact destination load failed: "+err.Error())
		return
	}

	now := time.Now().UTC()
	n := alerting.Notification{
		SiteID:       0,
		SiteURL:      "https://test.invalid/" + contact.Label,
		EventID:      0,
		EventType:    "event.test",
		Severity:     contact.MinSeverity,
		SeverityName: alerting.SeverityName(contact.MinSeverity),
		State:        "Test",
		Reason:       "test_send",
		Timestamp:    now,
		IsTest:       true,
	}

	// Bound the test send tightly so a wedged transport doesn't
	// hang the API request indefinitely.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	statusCode, respBody, sendErr := dispatcher.Send(ctx, dest, n)

	resp := alertContactTestResponse{
		ContactID:    contact.ID,
		Transport:    string(contact.Transport),
		StatusCode:   statusCode,
		ResponseBody: respBody,
	}
	if sendErr != nil {
		resp.Error = sendErr.Error()
		writeJSON(w, http.StatusBadGateway, resp)
		return
	}
	resp.Delivered = true
	writeJSON(w, http.StatusOK, resp)
}

// writeAlertingValidationError translates package-level validation
// errors into the appropriate HTTP status. ErrInvalidTransport and
// ErrInvalidSeverity are operator/client mistakes (4xx); everything
// else is treated as a 500 db_error.
func writeAlertingValidationError(w http.ResponseWriter, r *http.Request, err error, prefix string) {
	switch {
	case errors.Is(err, alerting.ErrInvalidTransport):
		writeError(w, r, http.StatusBadRequest, "invalid_transport", err.Error())
	case errors.Is(err, alerting.ErrInvalidSeverity):
		writeError(w, r, http.StatusBadRequest, "invalid_severity", err.Error())
	default:
		// Treat all other errors as either client validation (string
		// errors from validateCreateInput / validateDestination) or
		// server-side DB errors. Validation strings start with
		// "alerting:" — match that prefix to choose 422 vs 500.
		msg := err.Error()
		if len(msg) >= 9 && msg[:9] == "alerting:" {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_alert_contact", msg)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			prefix+": "+msg)
	}
}

// consumerName returns the API key consumer name from the request
// context, or "" if no key is attached. Used to populate created_by
// fields when a write endpoint creates a new resource.
func consumerName(r *http.Request) string {
	if k := keyFromRequest(r); k != nil {
		return k.ConsumerName
	}
	return ""
}

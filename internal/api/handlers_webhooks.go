package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Automattic/jetmon/internal/webhooks"
)

// webhookResponse is the JSON shape for a webhook in list/single responses.
// secret is omitted by default — only the create and rotate-secret endpoints
// return it (one-time view). secret_preview is the safe permanent view.
type webhookResponse struct {
	ID            int64                `json:"id"`
	URL           string               `json:"url"`
	Active        bool                 `json:"active"`
	Events        []string             `json:"events"`
	SiteFilter    webhooks.SiteFilter  `json:"site_filter"`
	StateFilter   webhooks.StateFilter `json:"state_filter"`
	SecretPreview string               `json:"secret_preview"`
	CreatedBy     string               `json:"created_by"`
	CreatedAt     string               `json:"created_at"`
	UpdatedAt     string               `json:"updated_at"`
}

// createWebhookResponse extends webhookResponse with the raw secret. Used
// once at create + rotate time; afterwards the caller stores the secret.
type createWebhookResponse struct {
	webhookResponse
	Secret string `json:"secret"`
}

func toWebhookResponse(w *webhooks.Webhook) webhookResponse {
	events := w.Events
	if events == nil {
		events = []string{}
	}
	return webhookResponse{
		ID:            w.ID,
		URL:           w.URL,
		Active:        w.Active,
		Events:        events,
		SiteFilter:    w.SiteFilter,
		StateFilter:   w.StateFilter,
		SecretPreview: w.SecretPreview,
		CreatedBy:     w.CreatedBy,
		CreatedAt:     w.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     w.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// createWebhookRequest is the body shape for POST /api/v1/webhooks.
type createWebhookRequest struct {
	URL         string               `json:"url"`
	Active      *bool                `json:"active"`
	Events      []string             `json:"events"`
	SiteFilter  webhooks.SiteFilter  `json:"site_filter"`
	StateFilter webhooks.StateFilter `json:"state_filter"`
}

// updateWebhookRequest is the body shape for PATCH /api/v1/webhooks/{id}.
// Pointer fields distinguish "absent" from "explicitly empty"; an explicit
// empty list/object clears the filter to "match all" semantics.
type updateWebhookRequest struct {
	URL         *string               `json:"url"`
	Active      *bool                 `json:"active"`
	Events      *[]string             `json:"events"`
	SiteFilter  *webhooks.SiteFilter  `json:"site_filter"`
	StateFilter *webhooks.StateFilter `json:"state_filter"`
}

// handleCreateWebhook implements POST /api/v1/webhooks. Returns 201 with
// the full webhook + the one-time raw secret. The secret is shown ONCE —
// after this response, only secret_preview is returned.
func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	var body createWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body",
			"request body must be valid JSON: "+err.Error())
		return
	}
	if err := validateMonitorURL(body.URL); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_url",
			"webhook url: "+err.Error())
		return
	}

	createdBy := ""
	if k := keyFromRequest(r); k != nil {
		createdBy = k.ConsumerName
	}

	rawSecret, hook, err := webhooks.Create(r.Context(), s.db, webhooks.CreateInput{
		URL:           body.URL,
		Active:        body.Active,
		OwnerTenantID: ownerTenantIDPtr(r),
		Events:        body.Events,
		SiteFilter:    body.SiteFilter,
		StateFilter:   body.StateFilter,
		CreatedBy:     createdBy,
	})
	if err != nil {
		if errors.Is(err, webhooks.ErrInvalidEvent) {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_event_type",
				err.Error())
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"webhook create failed: "+err.Error())
		return
	}

	resp := createWebhookResponse{
		webhookResponse: toWebhookResponse(hook),
		Secret:          rawSecret,
	}
	writeJSON(w, http.StatusCreated, resp)
}

// handleListWebhooks implements GET /api/v1/webhooks. No pagination yet —
// webhook count is bounded by registered consumers. List endpoint returns
// the full set; if a deployment ever grows past hundreds, add cursor
// pagination here mirroring the sites endpoint.
func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	var (
		hooks []webhooks.Webhook
		err   error
	)
	if tenantID, ok := ownerTenantIDFromRequest(r); ok {
		hooks, err = webhooks.ListForTenant(r.Context(), s.db, tenantID)
	} else {
		hooks, err = webhooks.List(r.Context(), s.db)
	}
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"webhook list failed: "+err.Error())
		return
	}
	out := make([]webhookResponse, 0, len(hooks))
	for i := range hooks {
		out = append(out, toWebhookResponse(&hooks[i]))
	}
	writeJSON(w, http.StatusOK, ListEnvelope{
		Data: out,
		Page: Page{Next: nil, Limit: len(out)},
	})
}

// handleGetWebhook implements GET /api/v1/webhooks/{id}.
func (s *Server) handleGetWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_webhook_id",
			"webhook id must be a positive integer")
		return
	}
	hook, err := getWebhookForRequest(r, s.db, id)
	if err != nil {
		if errors.Is(err, webhooks.ErrWebhookNotFound) {
			writeError(w, r, http.StatusNotFound, "webhook_not_found",
				fmt.Sprintf("Webhook %d does not exist", id))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"webhook lookup failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toWebhookResponse(hook))
}

// handleUpdateWebhook implements PATCH /api/v1/webhooks/{id}.
func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_webhook_id",
			"webhook id must be a positive integer")
		return
	}

	var body updateWebhookRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_body",
			"request body must be valid JSON: "+err.Error())
		return
	}
	if body.URL != nil {
		if err := validateMonitorURL(*body.URL); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_url",
				"webhook url: "+err.Error())
			return
		}
	}

	in := webhooks.UpdateInput{
		URL:         body.URL,
		Active:      body.Active,
		Events:      body.Events,
		SiteFilter:  body.SiteFilter,
		StateFilter: body.StateFilter,
	}
	var hook *webhooks.Webhook
	if tenantID, ok := ownerTenantIDFromRequest(r); ok {
		hook, err = webhooks.UpdateForTenant(r.Context(), s.db, id, tenantID, in)
	} else {
		hook, err = webhooks.Update(r.Context(), s.db, id, in)
	}
	if err != nil {
		if errors.Is(err, webhooks.ErrInvalidEvent) {
			writeError(w, r, http.StatusUnprocessableEntity, "invalid_event_type",
				err.Error())
			return
		}
		if errors.Is(err, webhooks.ErrWebhookNotFound) {
			writeError(w, r, http.StatusNotFound, "webhook_not_found",
				fmt.Sprintf("Webhook %d does not exist", id))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"webhook update failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toWebhookResponse(hook))
}

// handleDeleteWebhook implements DELETE /api/v1/webhooks/{id}.
//
// Delete is hard, not soft. The dispatcher's ListActive filter would also
// stop a soft-deleted webhook from receiving new deliveries, but a real
// DELETE keeps the registry clean and matches consumer expectations
// ("I revoked my webhook subscription"). Existing rows in
// jetmon_webhook_deliveries are preserved for audit and manual retry.
func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_webhook_id",
			"webhook id must be a positive integer")
		return
	}
	err = nil
	if tenantID, ok := ownerTenantIDFromRequest(r); ok {
		err = webhooks.DeleteForTenant(r.Context(), s.db, id, tenantID)
	} else {
		err = webhooks.Delete(r.Context(), s.db, id)
	}
	if err != nil {
		if errors.Is(err, webhooks.ErrWebhookNotFound) {
			writeError(w, r, http.StatusNotFound, "webhook_not_found",
				fmt.Sprintf("Webhook %d does not exist", id))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"webhook delete failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRotateWebhookSecret implements POST /api/v1/webhooks/{id}/rotate-secret.
//
// v1 behaviour: immediate revocation. The new secret is returned ONCE in
// the response; the old secret stops working immediately. Failed deliveries
// during the consumer's deploy window go into the retry queue and clear
// when the consumer rolls. Grace-period rotation is in docs/roadmap.md as a
// non-breaking future addition.
func (s *Server) handleRotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDPath(r, "id")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_webhook_id",
			"webhook id must be a positive integer")
		return
	}
	var (
		rawSecret string
		hook      *webhooks.Webhook
	)
	if tenantID, ok := ownerTenantIDFromRequest(r); ok {
		rawSecret, hook, err = webhooks.RotateSecretForTenant(r.Context(), s.db, id, tenantID)
	} else {
		rawSecret, hook, err = webhooks.RotateSecret(r.Context(), s.db, id)
	}
	if err != nil {
		if errors.Is(err, webhooks.ErrWebhookNotFound) {
			writeError(w, r, http.StatusNotFound, "webhook_not_found",
				fmt.Sprintf("Webhook %d does not exist", id))
			return
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"webhook rotate-secret failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, createWebhookResponse{
		webhookResponse: toWebhookResponse(hook),
		Secret:          rawSecret,
	})
}

// parseIDPath extracts a positive int64 from the named path parameter.
// Returns 0 + error for anything malformed; handlers translate to
// invalid_<resource>_id 400.
func parseIDPath(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("must be a positive integer")
	}
	return id, nil
}

func getWebhookForRequest(r *http.Request, db *sql.DB, id int64) (*webhooks.Webhook, error) {
	if tenantID, ok := ownerTenantIDFromRequest(r); ok {
		return webhooks.GetForTenant(r.Context(), db, id, tenantID)
	}
	return webhooks.Get(r.Context(), db, id)
}

func ownerTenantIDPtr(r *http.Request) *string {
	tenantID, ok := ownerTenantIDFromRequest(r)
	if !ok {
		return nil
	}
	return &tenantID
}

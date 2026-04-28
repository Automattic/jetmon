package api

import (
	"net/http"

	"github.com/Automattic/jetmon/internal/apikeys"
)

type serverHandler func(*Server, http.ResponseWriter, *http.Request)

func (h serverHandler) bind(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h(s, w, r)
	}
}

type routeDef struct {
	Method        string
	Path          string
	OperationID   string
	Summary       string
	Tags          []string
	Scope         apikeys.Scope
	SuccessStatus int
	JSONBody      bool
	BodyRequired  bool
	Idempotency   bool
	Handler       serverHandler
}

func (r routeDef) pattern() string {
	return r.Method + " " + r.Path
}

func (r routeDef) authenticated() bool {
	return r.Scope != ""
}

func (r routeDef) register(s *Server, mux *http.ServeMux) {
	handler := r.Handler.bind(s)
	if r.Idempotency {
		handler = s.withIdempotency(handler)
	}
	if r.authenticated() {
		handler = s.requireScope(r.Scope, handler)
	}
	mux.HandleFunc(r.pattern(), handler)
}

func apiRoutes() []routeDef {
	return []routeDef{
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/health",
			OperationID:   "getHealth",
			Summary:       "Check API and database health",
			Tags:          []string{"Utility"},
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleHealth,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/openapi.json",
			OperationID:   "getOpenAPI",
			Summary:       "Return the OpenAPI 3.1 route contract",
			Tags:          []string{"Utility"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleOpenAPIJSON,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/me",
			OperationID:   "getCurrentAPIKey",
			Summary:       "Return the authenticated API key identity",
			Tags:          []string{"Identity"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleMe,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/sites",
			OperationID:   "listSites",
			Summary:       "List monitored sites",
			Tags:          []string{"Sites"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleListSites,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/sites/{id}",
			OperationID:   "getSite",
			Summary:       "Get one monitored site",
			Tags:          []string{"Sites"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleGetSite,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/sites",
			OperationID:   "createSite",
			Summary:       "Create a monitored site",
			Tags:          []string{"Sites"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusCreated,
			JSONBody:      true,
			BodyRequired:  true,
			Idempotency:   true,
			Handler:       (*Server).handleCreateSite,
		},
		{
			Method:        http.MethodPatch,
			Path:          "/api/v1/sites/{id}",
			OperationID:   "updateSite",
			Summary:       "Update a monitored site",
			Tags:          []string{"Sites"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			JSONBody:      true,
			BodyRequired:  true,
			Handler:       (*Server).handleUpdateSite,
		},
		{
			Method:        http.MethodDelete,
			Path:          "/api/v1/sites/{id}",
			OperationID:   "deleteSite",
			Summary:       "Deactivate a monitored site",
			Tags:          []string{"Sites"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusNoContent,
			Handler:       (*Server).handleDeleteSite,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/sites/{id}/pause",
			OperationID:   "pauseSite",
			Summary:       "Pause monitoring for a site",
			Tags:          []string{"Sites"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			Idempotency:   true,
			Handler:       (*Server).handlePauseSite,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/sites/{id}/resume",
			OperationID:   "resumeSite",
			Summary:       "Resume monitoring for a site",
			Tags:          []string{"Sites"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			Idempotency:   true,
			Handler:       (*Server).handleResumeSite,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/sites/{id}/trigger-now",
			OperationID:   "triggerSiteCheck",
			Summary:       "Run an immediate site check",
			Tags:          []string{"Sites"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			Idempotency:   true,
			Handler:       (*Server).handleTriggerNow,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/sites/{id}/events",
			OperationID:   "listSiteEvents",
			Summary:       "List events for a site",
			Tags:          []string{"Events"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleListSiteEvents,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/sites/{id}/events/{event_id}",
			OperationID:   "getSiteEvent",
			Summary:       "Get a site-scoped event",
			Tags:          []string{"Events"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleGetEventBySite,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/sites/{id}/events/{event_id}/transitions",
			OperationID:   "listEventTransitions",
			Summary:       "List transitions for a site event",
			Tags:          []string{"Events"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleListTransitions,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/events/{event_id}",
			OperationID:   "getEvent",
			Summary:       "Get an event by ID",
			Tags:          []string{"Events"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleGetEvent,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/sites/{id}/events/{event_id}/close",
			OperationID:   "closeEvent",
			Summary:       "Manually close a site event",
			Tags:          []string{"Events"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			JSONBody:      true,
			Idempotency:   true,
			Handler:       (*Server).handleCloseEvent,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/sites/{id}/uptime",
			OperationID:   "getSiteUptime",
			Summary:       "Get site uptime statistics",
			Tags:          []string{"Statistics"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleSiteUptime,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/sites/{id}/response-time",
			OperationID:   "getSiteResponseTime",
			Summary:       "Get site response-time statistics",
			Tags:          []string{"Statistics"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleSiteResponseTime,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/sites/{id}/timing-breakdown",
			OperationID:   "getSiteTimingBreakdown",
			Summary:       "Get site timing breakdown statistics",
			Tags:          []string{"Statistics"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleSiteTimingBreakdown,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/webhooks",
			OperationID:   "listWebhooks",
			Summary:       "List webhooks",
			Tags:          []string{"Webhooks"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleListWebhooks,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/webhooks/{id}",
			OperationID:   "getWebhook",
			Summary:       "Get one webhook",
			Tags:          []string{"Webhooks"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleGetWebhook,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/webhooks",
			OperationID:   "createWebhook",
			Summary:       "Create a webhook",
			Tags:          []string{"Webhooks"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusCreated,
			JSONBody:      true,
			BodyRequired:  true,
			Idempotency:   true,
			Handler:       (*Server).handleCreateWebhook,
		},
		{
			Method:        http.MethodPatch,
			Path:          "/api/v1/webhooks/{id}",
			OperationID:   "updateWebhook",
			Summary:       "Update a webhook",
			Tags:          []string{"Webhooks"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			JSONBody:      true,
			BodyRequired:  true,
			Handler:       (*Server).handleUpdateWebhook,
		},
		{
			Method:        http.MethodDelete,
			Path:          "/api/v1/webhooks/{id}",
			OperationID:   "deleteWebhook",
			Summary:       "Delete a webhook",
			Tags:          []string{"Webhooks"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusNoContent,
			Handler:       (*Server).handleDeleteWebhook,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/webhooks/{id}/rotate-secret",
			OperationID:   "rotateWebhookSecret",
			Summary:       "Rotate a webhook signing secret",
			Tags:          []string{"Webhooks"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			Idempotency:   true,
			Handler:       (*Server).handleRotateWebhookSecret,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/webhooks/{id}/deliveries",
			OperationID:   "listWebhookDeliveries",
			Summary:       "List webhook deliveries",
			Tags:          []string{"Webhook deliveries"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleListDeliveries,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/webhooks/{id}/deliveries/{delivery_id}/retry",
			OperationID:   "retryWebhookDelivery",
			Summary:       "Retry an abandoned webhook delivery",
			Tags:          []string{"Webhook deliveries"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			Idempotency:   true,
			Handler:       (*Server).handleRetryDelivery,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/alert-contacts",
			OperationID:   "listAlertContacts",
			Summary:       "List alert contacts",
			Tags:          []string{"Alert contacts"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleListAlertContacts,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/alert-contacts/{id}",
			OperationID:   "getAlertContact",
			Summary:       "Get one alert contact",
			Tags:          []string{"Alert contacts"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleGetAlertContact,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/alert-contacts",
			OperationID:   "createAlertContact",
			Summary:       "Create an alert contact",
			Tags:          []string{"Alert contacts"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusCreated,
			JSONBody:      true,
			BodyRequired:  true,
			Idempotency:   true,
			Handler:       (*Server).handleCreateAlertContact,
		},
		{
			Method:        http.MethodPatch,
			Path:          "/api/v1/alert-contacts/{id}",
			OperationID:   "updateAlertContact",
			Summary:       "Update an alert contact",
			Tags:          []string{"Alert contacts"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			JSONBody:      true,
			BodyRequired:  true,
			Handler:       (*Server).handleUpdateAlertContact,
		},
		{
			Method:        http.MethodDelete,
			Path:          "/api/v1/alert-contacts/{id}",
			OperationID:   "deleteAlertContact",
			Summary:       "Delete an alert contact",
			Tags:          []string{"Alert contacts"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusNoContent,
			Handler:       (*Server).handleDeleteAlertContact,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/alert-contacts/{id}/test",
			OperationID:   "testAlertContact",
			Summary:       "Send a test alert through a contact",
			Tags:          []string{"Alert contacts"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			Idempotency:   true,
			Handler:       (*Server).handleAlertContactTest,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/v1/alert-contacts/{id}/deliveries",
			OperationID:   "listAlertContactDeliveries",
			Summary:       "List alert-contact deliveries",
			Tags:          []string{"Alert deliveries"},
			Scope:         scopeRead,
			SuccessStatus: http.StatusOK,
			Handler:       (*Server).handleListAlertDeliveries,
		},
		{
			Method:        http.MethodPost,
			Path:          "/api/v1/alert-contacts/{id}/deliveries/{delivery_id}/retry",
			OperationID:   "retryAlertContactDelivery",
			Summary:       "Retry an abandoned alert-contact delivery",
			Tags:          []string{"Alert deliveries"},
			Scope:         scopeWrite,
			SuccessStatus: http.StatusOK,
			Idempotency:   true,
			Handler:       (*Server).handleRetryAlertDelivery,
		},
	}
}

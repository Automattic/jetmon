package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

func (s *Server) handleOpenAPIJSON(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildOpenAPIDocument())
}

func buildOpenAPIDocument() map[string]any {
	paths := map[string]any{}
	for _, route := range apiRoutes() {
		pathItem, ok := paths[route.Path].(map[string]any)
		if !ok {
			pathItem = map[string]any{}
			paths[route.Path] = pathItem
		}
		pathItem[strings.ToLower(route.Method)] = openAPIOperation(route)
	}

	return map[string]any{
		"openapi":           "3.1.0",
		"jsonSchemaDialect": "https://json-schema.org/draft/2020-12/schema",
		"info": map[string]any{
			"title":   "Jetmon Internal API",
			"version": "v1",
		},
		"servers": []map[string]any{
			{"url": "/"},
		},
		"security": []map[string][]string{
			{"bearerAuth": {}},
		},
		"paths": paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]any{
					"type":         "http",
					"scheme":       "bearer",
					"bearerFormat": "Jetmon API key",
				},
			},
			"schemas": openAPISchemas(),
		},
	}
}

func openAPIOperation(route routeDef) map[string]any {
	op := map[string]any{
		"operationId": route.OperationID,
		"summary":     route.Summary,
		"tags":        route.Tags,
		"responses":   openAPIResponses(route),
	}

	if !route.authenticated() {
		op["security"] = []map[string][]string{}
	} else {
		op["x-jetmon-required-scope"] = string(route.Scope)
	}
	if route.Idempotency {
		op["x-jetmon-idempotency-key"] = "optional"
	}
	if params := openAPIParameters(route); len(params) > 0 {
		op["parameters"] = params
	}
	if route.JSONBody {
		schema := map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		}
		if route.RequestSchema != "" {
			schema = openAPIRef(route.RequestSchema)
		}
		op["requestBody"] = map[string]any{
			"required": route.BodyRequired,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": schema,
				},
			},
		}
	}

	return op
}

func openAPIParameters(route routeDef) []map[string]any {
	params := make([]map[string]any, 0)
	for _, name := range pathParamNames(route.Path) {
		params = append(params, map[string]any{
			"name":        name,
			"in":          "path",
			"required":    true,
			"description": "Path identifier.",
			"schema": map[string]any{
				"type":   "integer",
				"format": "int64",
			},
		})
	}
	if route.Idempotency {
		params = append(params, map[string]any{
			"name":        idempotencyHeader,
			"in":          "header",
			"required":    false,
			"description": "Optional key used to safely replay POST requests.",
			"schema": map[string]any{
				"type": "string",
			},
		})
	}
	return params
}

func pathParamNames(path string) []string {
	var out []string
	remaining := path
	for {
		start := strings.IndexByte(remaining, '{')
		if start < 0 {
			return out
		}
		end := strings.IndexByte(remaining[start+1:], '}')
		if end < 0 {
			return out
		}
		name := remaining[start+1 : start+1+end]
		out = append(out, name)
		remaining = remaining[start+1+end+1:]
	}
}

func openAPISuccessResponse(status int) map[string]any {
	description := http.StatusText(status)
	if description == "" {
		description = "Success"
	}
	resp := map[string]any{"description": description}
	if status != http.StatusNoContent {
		resp["content"] = map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{},
			},
		}
	}
	return resp
}

func openAPIResponses(route routeDef) map[string]any {
	status := strconv.Itoa(route.SuccessStatus)
	responses := map[string]any{
		status: openAPISuccessResponseForRoute(route),
		"default": map[string]any{
			"description": "Error response",
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": openAPIRef("ErrorEnvelope"),
				},
			},
		},
	}
	return responses
}

func openAPISuccessResponseForRoute(route routeDef) map[string]any {
	resp := openAPISuccessResponse(route.SuccessStatus)
	if route.SuccessStatus == http.StatusNoContent || route.ResponseSchema == "" {
		return resp
	}
	resp["content"] = map[string]any{
		"application/json": map[string]any{
			"schema": openAPIRef(route.ResponseSchema),
		},
	}
	return resp
}

func openAPIRef(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func openAPISchemas() map[string]any {
	schemas := map[string]any{
		"ErrorEnvelope": errorEnvelopeSchema(),
		"HealthResponse": map[string]any{
			"type":     "object",
			"required": []string{"status"},
			"properties": map[string]any{
				"status": map[string]any{"type": "string"},
			},
		},
		"OpenAPIDocument": map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		},
		"Page": schemaFromType(reflect.TypeOf(Page{})),
	}

	for name, typ := range openAPIComponentTypes() {
		schemas[name] = schemaFromType(typ)
	}
	for name, item := range map[string]string{
		"SiteListEnvelope":            "Site",
		"EventListEnvelope":           "Event",
		"TransitionListEnvelope":      "Transition",
		"WebhookListEnvelope":         "Webhook",
		"WebhookDeliveryListEnvelope": "WebhookDelivery",
		"AlertContactListEnvelope":    "AlertContact",
		"AlertDeliveryListEnvelope":   "AlertDelivery",
	} {
		schemas[name] = listEnvelopeSchema(item)
	}
	return schemas
}

func openAPIComponentTypes() map[string]reflect.Type {
	return map[string]reflect.Type{
		"MeResponse":                reflect.TypeOf(meResponse{}),
		"Site":                      reflect.TypeOf(siteResponse{}),
		"ActiveEventSummary":        reflect.TypeOf(activeEventSummary{}),
		"SiteDetail":                reflect.TypeOf(singleSiteResponse{}),
		"CreateSiteRequest":         reflect.TypeOf(createSiteRequest{}),
		"UpdateSiteRequest":         reflect.TypeOf(updateSiteRequest{}),
		"Event":                     reflect.TypeOf(eventResponse{}),
		"Transition":                reflect.TypeOf(transitionResponse{}),
		"EventDetail":               reflect.TypeOf(eventDetailResponse{}),
		"CloseEventRequest":         reflect.TypeOf(closeEventRequest{}),
		"TriggerNowResponse":        reflect.TypeOf(triggerNowResponse{}),
		"CheckResultPayload":        reflect.TypeOf(checkResultPayload{}),
		"UptimeResponse":            reflect.TypeOf(uptimeResponse{}),
		"ResponseTimeResponse":      reflect.TypeOf(responseTimeResponse{}),
		"TimingBreakdownResponse":   reflect.TypeOf(timingBreakdownResponse{}),
		"Window":                    reflect.TypeOf(windowResponse{}),
		"LatencyComponent":          reflect.TypeOf(latencyComponent{}),
		"Webhook":                   reflect.TypeOf(webhookResponse{}),
		"WebhookWithSecret":         reflect.TypeOf(createWebhookResponse{}),
		"CreateWebhookRequest":      reflect.TypeOf(createWebhookRequest{}),
		"UpdateWebhookRequest":      reflect.TypeOf(updateWebhookRequest{}),
		"WebhookDelivery":           reflect.TypeOf(deliveryResponse{}),
		"AlertContact":              reflect.TypeOf(alertContactResponse{}),
		"CreateAlertContactRequest": reflect.TypeOf(createAlertContactRequest{}),
		"UpdateAlertContactRequest": reflect.TypeOf(updateAlertContactRequest{}),
		"AlertContactTestResponse":  reflect.TypeOf(alertContactTestResponse{}),
		"AlertDelivery":             reflect.TypeOf(alertDeliveryResponse{}),
	}
}

func listEnvelopeSchema(itemSchema string) map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"data", "page"},
		"properties": map[string]any{
			"data": map[string]any{
				"type":  "array",
				"items": openAPIRef(itemSchema),
			},
			"page": openAPIRef("Page"),
		},
	}
}

func schemaFromType(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{
			"description": "Arbitrary JSON value.",
		}
	}

	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		return map[string]any{"type": "integer", "format": "int32"}
	case reflect.Int64:
		return map[string]any{"type": "integer", "format": "int64"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return map[string]any{"type": "integer", "format": "int32", "minimum": 0}
	case reflect.Uint64:
		return map[string]any{"type": "integer", "format": "int64", "minimum": 0}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number", "format": "double"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice, reflect.Array:
		return map[string]any{
			"type":  "array",
			"items": schemaForType(t.Elem()),
		}
	case reflect.Map:
		return map[string]any{
			"type":                 "object",
			"additionalProperties": schemaForType(t.Elem()),
		}
	case reflect.Struct:
		return structSchema(t)
	case reflect.Interface:
		return map[string]any{"description": "Arbitrary JSON value."}
	default:
		return map[string]any{}
	}
}

func schemaForType(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		return nullableSchema(schemaFromType(t.Elem()))
	}
	return schemaFromType(t)
}

func nullableSchema(schema map[string]any) map[string]any {
	if typ, ok := schema["type"].(string); ok {
		copy := cloneSchema(schema)
		copy["type"] = []string{typ, "null"}
		return copy
	}
	return map[string]any{"anyOf": []map[string]any{schema, map[string]any{"type": "null"}}}
}

func cloneSchema(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		out[k] = v
	}
	return out
}

func structSchema(t reflect.Type) map[string]any {
	properties := map[string]any{}
	var required []string

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			embedded := structSchema(field.Type)
			if embeddedProps, ok := embedded["properties"].(map[string]any); ok {
				for name, schema := range embeddedProps {
					properties[name] = schema
				}
			}
			if embeddedReq, ok := embedded["required"].([]string); ok {
				required = append(required, embeddedReq...)
			}
			continue
		}

		name, omitEmpty, ok := jsonFieldName(field)
		if !ok {
			continue
		}
		properties[name] = schemaForType(field.Type)
		if field.Type.Kind() != reflect.Pointer && !omitEmpty {
			required = append(required, name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func jsonFieldName(field reflect.StructField) (name string, omitEmpty, ok bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] != "" {
		name = parts[0]
	} else {
		name = field.Name
	}
	for _, part := range parts[1:] {
		if part == "omitempty" {
			omitEmpty = true
			break
		}
	}
	return name, omitEmpty, true
}

func errorEnvelopeSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"error"},
		"properties": map[string]any{
			"error": map[string]any{
				"type":     "object",
				"required": []string{"code", "message"},
				"properties": map[string]any{
					"code": map[string]any{
						"type": "string",
					},
					"message": map[string]any{
						"type": "string",
					},
					"request_id": map[string]any{
						"type": "string",
					},
				},
			},
		},
	}
}

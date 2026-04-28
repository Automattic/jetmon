package api

import (
	"net/http"
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
			"schemas": map[string]any{
				"ErrorEnvelope": errorEnvelopeSchema(),
			},
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
		op["requestBody"] = map[string]any{
			"required": route.BodyRequired,
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"type":                 "object",
						"additionalProperties": true,
					},
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

func openAPIResponses(route routeDef) map[string]any {
	status := strconv.Itoa(route.SuccessStatus)
	responses := map[string]any{
		status: openAPISuccessResponse(route.SuccessStatus),
		"default": map[string]any{
			"description": "Error response",
			"content": map[string]any{
				"application/json": map[string]any{
					"schema": map[string]any{
						"$ref": "#/components/schemas/ErrorEnvelope",
					},
				},
			},
		},
	}
	return responses
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

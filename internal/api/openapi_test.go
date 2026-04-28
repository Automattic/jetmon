package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestOpenAPIDocumentIncludesAPIRoutes(t *testing.T) {
	doc := buildOpenAPIDocument()
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("paths missing or wrong type")
	}

	for _, route := range apiRoutes() {
		pathItem, ok := paths[route.Path].(map[string]any)
		if !ok {
			t.Fatalf("OpenAPI path %s missing", route.Path)
		}
		op, ok := pathItem[strings.ToLower(route.Method)].(map[string]any)
		if !ok {
			t.Fatalf("OpenAPI operation %s %s missing", route.Method, route.Path)
		}
		if got := op["operationId"]; got != route.OperationID {
			t.Errorf("%s %s operationId = %v, want %s",
				route.Method, route.Path, got, route.OperationID)
		}

		responses, ok := op["responses"].(map[string]any)
		if !ok {
			t.Fatalf("%s %s responses missing", route.Method, route.Path)
		}
		status := strconv.Itoa(route.SuccessStatus)
		if _, ok := responses[status]; !ok {
			t.Errorf("%s %s missing success response %s", route.Method, route.Path, status)
		}
	}
}

func TestAPIRouteMetadataIsComplete(t *testing.T) {
	seenPatterns := map[string]struct{}{}
	seenOperationIDs := map[string]struct{}{}

	for _, route := range apiRoutes() {
		if route.Method == "" {
			t.Fatalf("route with path %q has empty method", route.Path)
		}
		if route.Path == "" {
			t.Fatalf("route %q has empty path", route.OperationID)
		}
		pattern := route.pattern()
		if _, ok := seenPatterns[pattern]; ok {
			t.Fatalf("duplicate route pattern %s", pattern)
		}
		seenPatterns[pattern] = struct{}{}

		if route.OperationID == "" {
			t.Fatalf("%s has empty operation id", pattern)
		}
		if _, ok := seenOperationIDs[route.OperationID]; ok {
			t.Fatalf("duplicate operation id %s", route.OperationID)
		}
		seenOperationIDs[route.OperationID] = struct{}{}

		if route.Summary == "" {
			t.Fatalf("%s has empty summary", pattern)
		}
		if len(route.Tags) == 0 {
			t.Fatalf("%s has no tags", pattern)
		}
		if route.SuccessStatus == 0 {
			t.Fatalf("%s has no success status", pattern)
		}
		if route.SuccessStatus != http.StatusNoContent && route.ResponseSchema == "" {
			t.Fatalf("%s has no response schema", pattern)
		}
		if route.JSONBody && route.RequestSchema == "" {
			t.Fatalf("%s has JSON body but no request schema", pattern)
		}
		if route.authenticated() && !route.Scope.Valid() {
			t.Fatalf("%s has invalid scope %q", pattern, route.Scope)
		}
		if route.Handler == nil {
			t.Fatalf("%s has nil handler", pattern)
		}
	}
}

func TestOpenAPIDocumentUsesRouteSchemas(t *testing.T) {
	doc := buildOpenAPIDocument()
	schemas := openAPISchemasFromDocument(t, doc)
	for _, route := range apiRoutes() {
		if route.RequestSchema != "" {
			if _, ok := schemas[route.RequestSchema]; !ok {
				t.Fatalf("%s request schema %q is not in components", route.pattern(), route.RequestSchema)
			}
		}
		if route.ResponseSchema != "" {
			if _, ok := schemas[route.ResponseSchema]; !ok {
				t.Fatalf("%s response schema %q is not in components", route.pattern(), route.ResponseSchema)
			}
		}
	}

	me := openAPIOperationAt(t, doc, http.MethodGet, "/api/v1/me")
	assertOpenAPIResponseRef(t, me, "200", "MeResponse")

	createSite := openAPIOperationAt(t, doc, http.MethodPost, "/api/v1/sites")
	assertOpenAPIRequestRef(t, createSite, "CreateSiteRequest")
	assertOpenAPIResponseRef(t, createSite, "201", "Site")

	listSites := openAPIOperationAt(t, doc, http.MethodGet, "/api/v1/sites")
	assertOpenAPIResponseRef(t, listSites, "200", "SiteListEnvelope")

	deleteSite := openAPIOperationAt(t, doc, http.MethodDelete, "/api/v1/sites/{id}")
	responses := deleteSite["responses"].(map[string]any)
	noContent := responses["204"].(map[string]any)
	if _, ok := noContent["content"]; ok {
		t.Fatal("204 response should not declare response content")
	}
}

func TestOpenAPISchemasIncludeHandlerShapes(t *testing.T) {
	doc := buildOpenAPIDocument()
	schemas := openAPISchemasFromDocument(t, doc)

	site, ok := schemas["Site"].(map[string]any)
	if !ok {
		t.Fatal("Site schema missing")
	}
	siteProps := site["properties"].(map[string]any)
	if _, ok := siteProps["monitor_url"]; !ok {
		t.Fatal("Site.monitor_url missing")
	}
	if _, ok := siteProps["active_event_id"]; !ok {
		t.Fatal("Site.active_event_id missing")
	}
	if _, ok := siteProps["bucket_no"]; !ok {
		t.Fatal("Site.bucket_no missing")
	}
	if _, ok := siteProps["check_interval"]; !ok {
		t.Fatal("Site.check_interval missing")
	}

	list, ok := schemas["SiteListEnvelope"].(map[string]any)
	if !ok {
		t.Fatal("SiteListEnvelope schema missing")
	}
	data := list["properties"].(map[string]any)["data"].(map[string]any)
	items := data["items"].(map[string]any)
	if got := items["$ref"]; got != "#/components/schemas/Site" {
		t.Fatalf("SiteListEnvelope data ref = %v, want Site ref", got)
	}

	webhookWithSecret, ok := schemas["WebhookWithSecret"].(map[string]any)
	if !ok {
		t.Fatal("WebhookWithSecret schema missing")
	}
	webhookProps := webhookWithSecret["properties"].(map[string]any)
	if _, ok := webhookProps["secret"]; !ok {
		t.Fatal("WebhookWithSecret.secret missing")
	}
}

func TestOpenAPIDocumentMarksAuthAndIdempotency(t *testing.T) {
	doc := buildOpenAPIDocument()

	health := openAPIOperationAt(t, doc, http.MethodGet, "/api/v1/health")
	if security, ok := health["security"].([]map[string][]string); !ok || len(security) != 0 {
		t.Fatalf("health security = %#v, want unauthenticated override", health["security"])
	}

	openapi := openAPIOperationAt(t, doc, http.MethodGet, "/api/v1/openapi.json")
	if got := openapi["x-jetmon-required-scope"]; got != "read" {
		t.Fatalf("openapi required scope = %v, want read", got)
	}

	closeEvent := openAPIOperationAt(t, doc, http.MethodPost, "/api/v1/sites/{id}/events/{event_id}/close")
	if got := closeEvent["x-jetmon-required-scope"]; got != "write" {
		t.Fatalf("close-event required scope = %v, want write", got)
	}
	if got := closeEvent["x-jetmon-idempotency-key"]; got != "optional" {
		t.Fatalf("close-event idempotency marker = %v, want optional", got)
	}

	params, ok := closeEvent["parameters"].([]map[string]any)
	if !ok {
		t.Fatalf("close-event parameters missing or wrong type: %#v", closeEvent["parameters"])
	}
	assertOpenAPIParam(t, params, "id", "path")
	assertOpenAPIParam(t, params, "event_id", "path")
	assertOpenAPIParam(t, params, idempotencyHeader, "header")

	body, ok := closeEvent["requestBody"].(map[string]any)
	if !ok {
		t.Fatal("close-event requestBody missing")
	}
	if got := body["required"]; got != false {
		t.Fatalf("close-event requestBody required = %v, want false", got)
	}
}

func TestOpenAPIEndpointRequiresReadScope(t *testing.T) {
	s := New(":0", nil, "test")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	rec := httptest.NewRecorder()

	s.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	if got := readErrorBody(t, rec.Body).Code; got != "missing_token" {
		t.Fatalf("error code = %q, want missing_token", got)
	}
}

func TestHandleOpenAPIJSON(t *testing.T) {
	s := New(":0", nil, "test")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	rec := httptest.NewRecorder()

	s.handleOpenAPIJSON(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var doc map[string]any
	readJSON(t, rec.Body, &doc)
	if got := doc["openapi"]; got != "3.1.0" {
		t.Fatalf("openapi = %v, want 3.1.0", got)
	}
	if _, ok := doc["components"].(map[string]any); !ok {
		t.Fatal("components missing")
	}
}

func openAPIOperationAt(t *testing.T, doc map[string]any, method, path string) map[string]any {
	t.Helper()
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("paths missing or wrong type")
	}
	pathItem, ok := paths[path].(map[string]any)
	if !ok {
		t.Fatalf("path %s missing", path)
	}
	op, ok := pathItem[strings.ToLower(method)].(map[string]any)
	if !ok {
		t.Fatalf("operation %s %s missing", method, path)
	}
	return op
}

func assertOpenAPIParam(t *testing.T, params []map[string]any, name, location string) {
	t.Helper()
	for _, param := range params {
		if param["name"] == name && param["in"] == location {
			return
		}
	}
	t.Fatalf("parameter %s in %s missing from %#v", name, location, params)
}

func assertOpenAPIRequestRef(t *testing.T, op map[string]any, want string) {
	t.Helper()
	body, ok := op["requestBody"].(map[string]any)
	if !ok {
		t.Fatal("requestBody missing")
	}
	content := body["content"].(map[string]any)
	jsonContent := content["application/json"].(map[string]any)
	schema := jsonContent["schema"].(map[string]any)
	if got := schema["$ref"]; got != "#/components/schemas/"+want {
		t.Fatalf("request schema ref = %v, want %s", got, want)
	}
}

func assertOpenAPIResponseRef(t *testing.T, op map[string]any, status, want string) {
	t.Helper()
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		t.Fatal("responses missing")
	}
	response, ok := responses[status].(map[string]any)
	if !ok {
		t.Fatalf("response %s missing", status)
	}
	content := response["content"].(map[string]any)
	jsonContent := content["application/json"].(map[string]any)
	schema := jsonContent["schema"].(map[string]any)
	if got := schema["$ref"]; got != "#/components/schemas/"+want {
		t.Fatalf("response schema ref = %v, want %s", got, want)
	}
}

func openAPISchemasFromDocument(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	components, ok := doc["components"].(map[string]any)
	if !ok {
		t.Fatal("components missing")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("components.schemas missing")
	}
	return schemas
}

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
		if route.authenticated() && !route.Scope.Valid() {
			t.Fatalf("%s has invalid scope %q", pattern, route.Scope)
		}
		if route.Handler == nil {
			t.Fatalf("%s has nil handler", pattern)
		}
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

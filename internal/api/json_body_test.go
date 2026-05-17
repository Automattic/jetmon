package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONBodyRejectsTrailingJSONValue(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(`{"name":"ok"} {"name":"extra"}`))
	rec := httptest.NewRecorder()

	var body struct {
		Name string `json:"name"`
	}
	if decodeJSONBody(rec, req, &body) {
		t.Fatal("decodeJSONBody accepted a second JSON value")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "single JSON value") {
		t.Fatalf("body = %q, want single-value diagnostic", rec.Body.String())
	}
}

func TestDecodeOptionalJSONBodyAllowsEmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(""))
	rec := httptest.NewRecorder()

	var body struct {
		Name string `json:"name"`
	}
	if !decodeOptionalJSONBody(rec, req, &body) {
		t.Fatalf("decodeOptionalJSONBody rejected empty body: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

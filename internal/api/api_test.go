package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRequestIDLength(t *testing.T) {
	id := newRequestID()
	if len(id) != 32 {
		t.Fatalf("newRequestID len = %d, want 32 (16-byte hex)", len(id))
	}
	other := newRequestID()
	if id == other {
		t.Fatal("newRequestID collided across two calls")
	}
}

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"Bearer jm_abc123", "jm_abc123"},
		{"Bearer  jm_abc123  ", "jm_abc123"},
		{"bearer jm_abc123", ""}, // wrong case
		{"jm_abc123", ""},        // missing "Bearer " prefix
		{"", ""},
	}
	for _, c := range cases {
		req, _ := http.NewRequest("GET", "/", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		got := bearerToken(req)
		if got != c.want {
			t.Errorf("bearerToken(%q) = %q, want %q", c.header, got, c.want)
		}
	}
}

func TestEncodeDecodeIDCursor(t *testing.T) {
	encoded := encodeIDCursor(98765)
	if encoded == "" {
		t.Fatal("empty cursor")
	}
	got, err := decodeIDCursor(encoded)
	if err != nil {
		t.Fatalf("decodeIDCursor: %v", err)
	}
	if got != 98765 {
		t.Fatalf("decoded id = %d, want 98765", got)
	}
}

func TestDecodeIDCursorInvalid(t *testing.T) {
	if _, err := decodeIDCursor("not-base64!!"); err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestDeriveStateFromSiteStatus(t *testing.T) {
	cases := []struct {
		siteStatus int
		state      string
		severity   uint8
	}{
		{0, "Seems Down", 3},
		{1, "Up", 0},
		{2, "Down", 4},
		{99, "Unknown", 0},
	}
	for _, c := range cases {
		gotState, gotSev := deriveStateFromSiteStatus(c.siteStatus)
		if gotState != c.state || gotSev != c.severity {
			t.Errorf("deriveStateFromSiteStatus(%d) = (%q, %d), want (%q, %d)",
				c.siteStatus, gotState, gotSev, c.state, c.severity)
		}
	}
}

func TestParseLimit(t *testing.T) {
	cases := []struct {
		s, want any
	}{
		{"", 50},
		{"100", 100},
		{"500", 200}, // clamped to maxLimit
	}
	for _, c := range cases {
		got, err := parseLimit(c.s.(string), 50, 200)
		if err != nil {
			t.Errorf("parseLimit(%q): %v", c.s, err)
			continue
		}
		if got != c.want.(int) {
			t.Errorf("parseLimit(%q) = %d, want %d", c.s, got, c.want)
		}
	}
	if _, err := parseLimit("abc", 50, 200); err == nil {
		t.Error("parseLimit('abc') should error")
	}
	if _, err := parseLimit("0", 50, 200); err == nil {
		t.Error("parseLimit('0') should error")
	}
}

func TestHandleHealthWithoutDB(t *testing.T) {
	s := New(":0", nil, "test")
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	body := readErrorBody(t, rec.Body)
	if body.Code != "db_unavailable" {
		t.Errorf("error code = %q, want db_unavailable", body.Code)
	}
}

func TestRoutesRegisterAllPaths(t *testing.T) {
	// Sanity: every route in the routes() table is wired and doesn't panic
	// when constructed. We don't exercise the handlers (those need a DB).
	s := New(":0", nil, "test")
	mux := s.routes()
	if mux == nil {
		t.Fatal("routes() returned nil")
	}
	// 404 catch-all should fire for unknown paths (and gives us a free
	// signal that the mux was constructed).
	req := httptest.NewRequest("GET", "/totally-not-a-route", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404", rec.Code)
	}
}

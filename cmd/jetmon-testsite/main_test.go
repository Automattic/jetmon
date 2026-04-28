package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFixtureHandlerEndpoints(t *testing.T) {
	srv := httptest.NewServer(newFixtureHandler())
	defer srv.Close()

	tests := []struct {
		path string
		code int
		body string
	}{
		{path: "/health", code: http.StatusOK, body: "ok"},
		{path: "/ok", code: http.StatusOK, body: "fixture ok"},
		{path: "/keyword", code: http.StatusOK, body: "keyword present"},
		{path: "/status/403", code: http.StatusForbidden, body: "status 403"},
		{path: "/status/500", code: http.StatusInternalServerError, body: "status 500"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tt.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tt.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.code {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.code)
			}
			buf := make([]byte, 256)
			n, _ := resp.Body.Read(buf)
			if !strings.Contains(string(buf[:n]), tt.body) {
				t.Fatalf("body = %q, want substring %q", string(buf[:n]), tt.body)
			}
		})
	}
}

func TestFixtureRedirectAndDelay(t *testing.T) {
	srv := httptest.NewServer(newFixtureHandler())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(srv.URL + "/redirect")
	if err != nil {
		t.Fatalf("GET redirect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ok" {
		t.Fatalf("redirect status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}

	start := time.Now()
	resp, err = http.Get(srv.URL + "/slow?delay=10ms")
	if err != nil {
		t.Fatalf("GET slow: %v", err)
	}
	resp.Body.Close()
	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Fatalf("slow endpoint returned too quickly: %s", elapsed)
	}
}

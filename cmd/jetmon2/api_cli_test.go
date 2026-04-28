package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPIRequestURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		target  string
		want    string
		wantErr bool
	}{
		{
			name:    "absolute path",
			baseURL: "http://localhost:8090",
			target:  "/api/v1/health",
			want:    "http://localhost:8090/api/v1/health",
		},
		{
			name:    "relative path",
			baseURL: "http://localhost:8090/",
			target:  "api/v1/me",
			want:    "http://localhost:8090/api/v1/me",
		},
		{
			name:    "absolute url",
			baseURL: "http://localhost:8090",
			target:  "http://127.0.0.1:9000/api/v1/health",
			want:    "http://127.0.0.1:9000/api/v1/health",
		},
		{
			name:    "base requires host",
			baseURL: "localhost:8090",
			target:  "/api/v1/health",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := apiRequestURL(tt.baseURL, tt.target)
			if tt.wantErr {
				if err == nil {
					t.Fatal("apiRequestURL() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("apiRequestURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("apiRequestURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecuteAPIRequestSendsAuthAndVerboseHeaders(t *testing.T) {
	var sawAuth, sawIDKey bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got == "Bearer token-123" {
			sawAuth = true
		}
		if got := r.Header.Get("Idempotency-Key"); got == "idem-1" {
			sawIDKey = true
		}
		w.Header().Set("X-Test-Response", "yes")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	opts := apiCLIOptions{
		baseURL:        srv.URL,
		token:          "token-123",
		idempotencyKey: "idem-1",
		verbose:        true,
		pretty:         true,
		timeout:        time.Second,
		out:            &stdout,
		errOut:         &stderr,
	}
	if err := executeAPIRequest(context.Background(), srv.Client(), opts, http.MethodPost, "/api/v1/sites/42/trigger-now", []byte(`{}`)); err != nil {
		t.Fatalf("executeAPIRequest() error = %v", err)
	}
	if !sawAuth {
		t.Fatal("Authorization header was not sent")
	}
	if !sawIDKey {
		t.Fatal("Idempotency-Key header was not sent")
	}
	if got := stdout.String(); !strings.Contains(got, "{\n  \"ok\": true\n}") {
		t.Fatalf("stdout = %q, want pretty JSON body", got)
	}
	errOut := stderr.String()
	for _, want := range []string{
		"> POST /api/v1/sites/42/trigger-now HTTP/1.1",
		"> Authorization: Bearer token-123",
		"> Idempotency-Key: idem-1",
		"< HTTP/1.1 201 Created",
		"< X-Test-Response: yes",
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

func TestExecuteAPIRequestReturnsErrorForHTTPFailureAfterWritingBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"missing token"}`))
	}))
	defer srv.Close()

	var stdout bytes.Buffer
	opts := apiCLIOptions{
		baseURL: srv.URL,
		timeout: time.Second,
		out:     &stdout,
		errOut:  ioDiscard{},
	}
	err := executeAPIRequest(context.Background(), srv.Client(), opts, http.MethodGet, "/api/v1/me", nil)
	if err == nil {
		t.Fatal("executeAPIRequest() error = nil, want error")
	}
	if got := stdout.String(); !strings.Contains(got, `"missing token"`) {
		t.Fatalf("stdout = %q, want error body", got)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

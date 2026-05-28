package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPIDashboardSubcommands(t *testing.T) {
	tests := []struct {
		sub  string
		path string
	}{
		{sub: "state", path: "/api/v1/dashboard/state"},
		{sub: "health", path: "/api/v1/dashboard/health"},
		{sub: "host", path: "/api/v1/dashboard/host"},
		{sub: "fleet", path: "/api/v1/dashboard/fleet"},
	}

	for _, tt := range tests {
		t.Run(tt.sub, func(t *testing.T) {
			var sawPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawPath = r.URL.Path
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer srv.Close()

			var out bytes.Buffer
			opts := apiCLIOptions{
				baseURL: srv.URL,
				timeout: time.Second,
				output:  "json",
				out:     &out,
			}
			if err := executeAPIDashboardRequest(tt.sub, srv.Client(), opts); err != nil {
				t.Fatalf("executeAPIDashboardRequest() error = %v", err)
			}
			if sawPath != tt.path {
				t.Fatalf("path = %q, want %q", sawPath, tt.path)
			}
			if !strings.Contains(out.String(), `"ok":true`) {
				t.Fatalf("stdout = %q, want JSON body", out.String())
			}
		})
	}
}

func TestAPIDashboardRejectsUnknownSubcommand(t *testing.T) {
	if err := cmdAPIDashboard([]string{"bogus"}); err == nil {
		t.Fatal("cmdAPIDashboard() error = nil, want unknown subcommand error")
	}
}

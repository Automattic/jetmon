package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestIsLocalAPIBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    bool
	}{
		{name: "localhost", baseURL: "http://localhost:8090", want: true},
		{name: "localhost subdomain", baseURL: "http://jetmon.localhost:8090", want: true},
		{name: "ipv4 loopback", baseURL: "http://127.0.0.1:8090", want: true},
		{name: "ipv6 loopback", baseURL: "http://[::1]:8090", want: true},
		{name: "private lan is remote", baseURL: "http://10.0.0.171:8090", want: false},
		{name: "public hostname", baseURL: "https://jetmon-api.example.test", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isLocalAPIURL(tt.baseURL)
			if err != nil {
				t.Fatalf("isLocalAPIURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("isLocalAPIURL(%q) = %v, want %v", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestExecuteAPIRequestRejectsRemoteWrite(t *testing.T) {
	err := executeAPIRequest(context.Background(), nil, apiCLIOptions{
		baseURL: "https://jetmon-api.example.test",
		out:     ioDiscard{},
		errOut:  ioDiscard{},
	}, "POST", "/api/v1/sites", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "--allow-remote") {
		t.Fatalf("executeAPIRequest() error = %v, want --allow-remote refusal", err)
	}
}

func TestExecuteAPIRequestRejectsAbsoluteRemoteWriteWithLocalBase(t *testing.T) {
	err := executeAPIRequest(context.Background(), nil, apiCLIOptions{
		baseURL: "http://localhost:8090",
		out:     ioDiscard{},
		errOut:  ioDiscard{},
	}, "DELETE", "https://jetmon-api.example.test/api/v1/sites/42", nil)
	if err == nil || !strings.Contains(err.Error(), "--allow-remote") {
		t.Fatalf("executeAPIRequest() error = %v, want --allow-remote refusal", err)
	}
}

func TestRemoteWorkflowGuardRequiresAllowRemote(t *testing.T) {
	opts := apiCLIOptions{baseURL: "https://jetmon-api.example.test"}
	remote, err := requireAPILocalOrAllowRemote(opts, false, "api smoke")
	if err == nil {
		t.Fatal("requireAPILocalOrAllowRemote() error = nil, want refusal")
	}
	if !remote {
		t.Fatal("remote = false, want true")
	}
	if !strings.Contains(err.Error(), "--allow-remote") {
		t.Fatalf("error = %v, want --allow-remote hint", err)
	}

	remote, err = requireAPILocalOrAllowRemote(opts, true, "api smoke")
	if err != nil {
		t.Fatalf("requireAPILocalOrAllowRemote(... allow) error = %v", err)
	}
	if !remote {
		t.Fatal("remote = false with remote URL and allow flag, want true")
	}
}

func TestRunAPISitesBulkAddRemoteGuard(t *testing.T) {
	opts := apiCLIOptions{baseURL: "https://jetmon-api.example.test", out: ioDiscard{}, errOut: ioDiscard{}}
	bulk := apiSitesBulkAddOptions{
		count:       1,
		batch:       "remote-batch",
		source:      "fixture",
		blogIDStart: defaultAPIBulkAddBlogIDStart,
	}
	err := runAPISitesBulkAdd(context.Background(), nil, opts, bulk)
	if err == nil || !strings.Contains(err.Error(), "--allow-remote") {
		t.Fatalf("runAPISitesBulkAdd() error = %v, want --allow-remote refusal", err)
	}

	opts.allowRemote = true
	bulk.batch = ""
	err = runAPISitesBulkAdd(context.Background(), nil, opts, bulk)
	if err == nil || !strings.Contains(err.Error(), "requires --batch") {
		t.Fatalf("runAPISitesBulkAdd() error = %v, want remote batch requirement", err)
	}
}

func TestRunAPISitesBulkAddDryRunAllowsRemotePlanning(t *testing.T) {
	var stdout bytes.Buffer
	err := runAPISitesBulkAdd(context.Background(), nil, apiCLIOptions{
		baseURL: "https://jetmon-api.example.test",
		out:     &stdout,
		errOut:  ioDiscard{},
	}, apiSitesBulkAddOptions{
		count:       1,
		source:      "fixture",
		blogIDStart: defaultAPIBulkAddBlogIDStart,
		dryRun:      true,
	})
	if err != nil {
		t.Fatalf("runAPISitesBulkAdd() dry-run error = %v", err)
	}
	if !strings.Contains(stdout.String(), `"dry_run":true`) {
		t.Fatalf("stdout = %s, want dry-run output", stdout.String())
	}
}

func TestRunAPISitesCleanupRemoteGuard(t *testing.T) {
	opts := apiCLIOptions{baseURL: "https://jetmon-api.example.test", out: ioDiscard{}, errOut: ioDiscard{}}
	cleanup := apiSitesCleanupOptions{batch: "remote-batch", count: 1, ignoreNotFound: true}
	err := runAPISitesCleanup(context.Background(), nil, opts, cleanup)
	if err == nil || !strings.Contains(err.Error(), "--allow-remote") {
		t.Fatalf("runAPISitesCleanup() error = %v, want --allow-remote refusal", err)
	}

	opts.allowRemote = true
	cleanup.allowUnmarked = true
	err = runAPISitesCleanup(context.Background(), nil, opts, cleanup)
	if err == nil || !strings.Contains(err.Error(), "cannot use --allow-unmarked") {
		t.Fatalf("runAPISitesCleanup() error = %v, want allow-unmarked refusal", err)
	}
}

func TestRunAPISitesSimulateFailureRemoteGuard(t *testing.T) {
	opts := apiCLIOptions{baseURL: "https://jetmon-api.example.test", out: ioDiscard{}, errOut: ioDiscard{}}
	sim := apiSitesSimulateFailureOptions{
		mode:         "http-500",
		batch:        "remote-batch",
		count:        1,
		trigger:      false,
		pollInterval: 1,
	}
	err := runAPISitesSimulateFailure(context.Background(), nil, opts, sim)
	if err == nil || !strings.Contains(err.Error(), "--allow-remote") {
		t.Fatalf("runAPISitesSimulateFailure() error = %v, want --allow-remote refusal", err)
	}

	opts.allowRemote = true
	sim.batch = ""
	err = runAPISitesSimulateFailure(context.Background(), nil, opts, sim)
	if err == nil || !strings.Contains(err.Error(), "requires --batch") {
		t.Fatalf("runAPISitesSimulateFailure() error = %v, want remote batch requirement", err)
	}
}

func TestRunAPISmokeRemoteGuard(t *testing.T) {
	err := runAPISmoke(context.Background(), nil, apiCLIOptions{
		baseURL: "https://jetmon-api.example.test",
		out:     ioDiscard{},
		errOut:  ioDiscard{},
	}, apiSmokeOptions{batch: "remote-smoke", exercise: "none"})
	if err == nil || !strings.Contains(err.Error(), "--allow-remote") {
		t.Fatalf("runAPISmoke() error = %v, want --allow-remote refusal", err)
	}

	err = runAPISmoke(context.Background(), nil, apiCLIOptions{
		baseURL:     "https://jetmon-api.example.test",
		allowRemote: true,
		out:         ioDiscard{},
		errOut:      ioDiscard{},
	}, apiSmokeOptions{exercise: "none"})
	if err == nil || !strings.Contains(err.Error(), "requires --batch") {
		t.Fatalf("runAPISmoke() error = %v, want remote batch requirement", err)
	}
}

func TestRunAPISmokeWebhookExerciseRemoteGuard(t *testing.T) {
	err := runAPISmoke(context.Background(), nil, apiCLIOptions{
		baseURL:     "https://jetmon-api.example.test",
		allowRemote: true,
		out:         ioDiscard{},
		errOut:      ioDiscard{},
	}, apiSmokeOptions{
		batch:    "remote-smoke",
		exercise: "webhook",
	})
	if err == nil || !strings.Contains(err.Error(), "Docker-local only") {
		t.Fatalf("runAPISmoke() error = %v, want Docker-local webhook refusal", err)
	}
}

func TestRunAPISmokeWebhookRequiresLocalRequestsURL(t *testing.T) {
	err := runAPISmoke(context.Background(), nil, apiCLIOptions{
		baseURL: "http://localhost:8090",
		out:     ioDiscard{},
		errOut:  ioDiscard{},
	}, apiSmokeOptions{
		batch:              "local-smoke",
		exercise:           "webhook",
		webhookRequestsURL: "https://fixture.example.test/webhook/requests",
	})
	if err == nil || !strings.Contains(err.Error(), "webhook-requests-url must be local") {
		t.Fatalf("runAPISmoke() error = %v, want local webhook requests URL refusal", err)
	}
}

func TestRunAPISmokeWebhookRejectsExternalWebhookURL(t *testing.T) {
	err := runAPISmoke(context.Background(), nil, apiCLIOptions{
		baseURL: "http://localhost:8090",
		out:     ioDiscard{},
		errOut:  ioDiscard{},
	}, apiSmokeOptions{
		batch:              "local-smoke",
		exercise:           "webhook",
		webhookURL:         "https://receiver.example.test/webhook",
		webhookRequestsURL: "http://localhost:18091/webhook/requests",
	})
	if err == nil || !strings.Contains(err.Error(), "allow-external-webhook-url") {
		t.Fatalf("runAPISmoke() error = %v, want external webhook URL refusal", err)
	}
}

func TestRequireAPIWebhookFixtureURLAllowed(t *testing.T) {
	tests := []struct {
		name          string
		rawURL        string
		allowExternal bool
		wantErr       bool
	}{
		{name: "api fixture", rawURL: "http://api-fixture:8091/webhook"},
		{name: "localhost", rawURL: "http://localhost:18091/webhook"},
		{name: "loopback", rawURL: "http://127.0.0.1:18091/webhook"},
		{name: "external blocked", rawURL: "https://receiver.example.test/webhook", wantErr: true},
		{name: "external explicit", rawURL: "https://receiver.example.test/webhook", allowExternal: true},
		{name: "relative rejected", rawURL: "/webhook", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireAPIWebhookFixtureURLAllowed(tt.rawURL, tt.allowExternal)
			if tt.wantErr && err == nil {
				t.Fatal("requireAPIWebhookFixtureURLAllowed() error = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("requireAPIWebhookFixtureURLAllowed() error = %v", err)
			}
		})
	}
}

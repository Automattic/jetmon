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
			got, err := isLocalAPIBaseURL(tt.baseURL)
			if err != nil {
				t.Fatalf("isLocalAPIBaseURL() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("isLocalAPIBaseURL(%q) = %v, want %v", tt.baseURL, got, tt.want)
			}
		})
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

	bulk.allowRemote = true
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

	cleanup.allowRemote = true
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

	sim.allowRemote = true
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
		baseURL: "https://jetmon-api.example.test",
		out:     ioDiscard{},
		errOut:  ioDiscard{},
	}, apiSmokeOptions{allowRemote: true, exercise: "none"})
	if err == nil || !strings.Contains(err.Error(), "requires --batch") {
		t.Fatalf("runAPISmoke() error = %v, want remote batch requirement", err)
	}
}

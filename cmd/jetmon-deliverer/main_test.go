package main

import (
	"strings"
	"testing"

	"github.com/Automattic/jetmon/internal/config"
)

func TestDeliveryWorkersShouldStart(t *testing.T) {
	tests := []struct {
		name      string
		cfg       config.Config
		hostname  string
		wantStart bool
		wantLevel string
		wantMsg   string
	}{
		{
			name:      "empty owner starts with warning",
			cfg:       config.Config{},
			hostname:  "host-a",
			wantStart: true,
			wantLevel: "WARN",
			wantMsg:   "delivery_owner_host is unset",
		},
		{
			name: "matching owner starts",
			cfg: config.Config{
				DeliveryOwnerHost: "host-a",
			},
			hostname:  "host-a",
			wantStart: true,
			wantLevel: "INFO",
			wantMsg:   "matched",
		},
		{
			name: "non-owner idles",
			cfg: config.Config{
				DeliveryOwnerHost: "host-a",
			},
			hostname:  "host-b",
			wantLevel: "INFO",
			wantMsg:   "idle on host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deliveryWorkersShouldStart(&tt.cfg, tt.hostname); got != tt.wantStart {
				t.Fatalf("deliveryWorkersShouldStart() = %v, want %v", got, tt.wantStart)
			}
			level, msg := deliveryOwnerStatus(&tt.cfg, tt.hostname)
			if level != tt.wantLevel {
				t.Fatalf("deliveryOwnerStatus() level = %q, want %q", level, tt.wantLevel)
			}
			if !strings.Contains(msg, tt.wantMsg) {
				t.Fatalf("deliveryOwnerStatus() message = %q, want substring %q", msg, tt.wantMsg)
			}
		})
	}
}

func TestParseValidateConfigOptions(t *testing.T) {
	opts, err := parseValidateConfigOptions([]string{
		"--host=deliverer-1",
		"--require-owner-match",
		"--require-email-delivery",
		"--require-api-disabled",
	})
	if err != nil {
		t.Fatalf("parseValidateConfigOptions: %v", err)
	}
	if opts.HostOverride != "deliverer-1" {
		t.Fatalf("HostOverride = %q, want deliverer-1", opts.HostOverride)
	}
	if !opts.RequireOwnerMatch || !opts.RequireEmailDelivery || !opts.RequireAPIDisabled {
		t.Fatalf("parsed options = %+v, want all requirements enabled", opts)
	}

	if _, err := parseValidateConfigOptions([]string{"extra"}); err == nil {
		t.Fatal("parseValidateConfigOptions accepted unexpected positional argument")
	}
}

func TestValidateDelivererConfigRequirements(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.Config
		hostname string
		opts     delivererValidationOptions
		want     []string
	}{
		{
			name: "single owner production config passes",
			cfg: config.Config{
				DeliveryOwnerHost: "deliverer-1",
				EmailTransport:    "smtp",
			},
			hostname: "deliverer-1",
			opts: delivererValidationOptions{
				RequireOwnerMatch:    true,
				RequireEmailDelivery: true,
				RequireAPIDisabled:   true,
			},
		},
		{
			name:     "owner required but empty",
			cfg:      config.Config{EmailTransport: "smtp"},
			hostname: "deliverer-1",
			opts:     delivererValidationOptions{RequireOwnerMatch: true},
			want:     []string{"DELIVERY_OWNER_HOST must be set"},
		},
		{
			name: "owner mismatch",
			cfg: config.Config{
				DeliveryOwnerHost: "deliverer-2",
				EmailTransport:    "smtp",
			},
			hostname: "deliverer-1",
			opts:     delivererValidationOptions{RequireOwnerMatch: true},
			want:     []string{"does not match"},
		},
		{
			name: "stub email rejected",
			cfg: config.Config{
				DeliveryOwnerHost: "deliverer-1",
				EmailTransport:    "stub",
			},
			hostname: "deliverer-1",
			opts:     delivererValidationOptions{RequireEmailDelivery: true},
			want:     []string{"does not deliver email"},
		},
		{
			name: "api port rejected",
			cfg: config.Config{
				DeliveryOwnerHost: "deliverer-1",
				EmailTransport:    "smtp",
				APIPort:           8090,
			},
			hostname: "deliverer-1",
			opts:     delivererValidationOptions{RequireAPIDisabled: true},
			want:     []string{"API_PORT=8090"},
		},
		{
			name:     "empty host rejected when owner must match",
			cfg:      config.Config{DeliveryOwnerHost: "deliverer-1"},
			hostname: " ",
			opts:     delivererValidationOptions{RequireOwnerMatch: true},
			want:     []string{"host id is empty"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			failures := validateDelivererConfigRequirements(&tt.cfg, tt.hostname, tt.opts)
			if len(tt.want) == 0 {
				if len(failures) != 0 {
					t.Fatalf("failures = %v, want none", failures)
				}
				return
			}
			if len(failures) != len(tt.want) {
				t.Fatalf("failures = %v, want %d failures", failures, len(tt.want))
			}
			for i, want := range tt.want {
				if !strings.Contains(failures[i], want) {
					t.Fatalf("failure[%d] = %q, want substring %q", i, failures[i], want)
				}
			}
		})
	}
}

func TestEmailTransportLabelAndDelivery(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.Config
		label    string
		delivers bool
	}{
		{name: "empty is stub alias", cfg: config.Config{}, label: "stub"},
		{name: "stub logs only", cfg: config.Config{EmailTransport: "stub"}, label: "stub"},
		{name: "smtp delivers", cfg: config.Config{EmailTransport: "smtp"}, label: "smtp", delivers: true},
		{name: "wpcom delivers", cfg: config.Config{EmailTransport: "wpcom"}, label: "wpcom", delivers: true},
		{name: "unknown does not deliver", cfg: config.Config{EmailTransport: "sendmail"}, label: "sendmail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := emailTransportLabel(&tt.cfg); got != tt.label {
				t.Fatalf("emailTransportLabel() = %q, want %q", got, tt.label)
			}
			if got := emailTransportDelivers(&tt.cfg); got != tt.delivers {
				t.Fatalf("emailTransportDelivers() = %v, want %v", got, tt.delivers)
			}
		})
	}
}

func TestEnvOrDefault(t *testing.T) {
	const key = "JETMON_DELIVERER_TEST_ENV_OR_DEFAULT"
	t.Setenv(key, "")
	if got := envOrDefault(key, "fallback"); got != "fallback" {
		t.Fatalf("envOrDefault() = %q, want fallback", got)
	}

	t.Setenv(key, "set-value")
	if got := envOrDefault(key, "fallback"); got != "set-value" {
		t.Fatalf("envOrDefault() = %q, want set-value", got)
	}
}

package netguard

import (
	"net"
	"strings"
	"testing"
)

func TestUnsafeHost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"app.localhost", true},
		{"example.local", true},
		{"host.docker.internal", true},
		{"metadata.google.internal", true},
		{"router.lan", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"10.0.0.1", true},
		{"0177.0.0.1", true},
		{"0x7f.0.0.1", true},
		{"2130706433", true},
		{"127.1", true},
		{"169.254.169.254", true},
		{"100.64.0.1", true},
		{"192.0.2.1", true},
		{"198.18.0.1", true},
		{"198.51.100.1", true},
		{"203.0.113.1", true},
		{"example.com", false},
		{"02.example.com", false},
		{"0x0fff.com", false},
		{"03062013.jackchan.hk", false},
		{"93.184.216.34", false},
		{"2606:2800:220:1:248:1893:25c8:1946", false},
	}
	for _, tt := range tests {
		if got := UnsafeHost(tt.host); got != tt.want {
			t.Fatalf("UnsafeHost(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestParsePublicHTTPURL(t *testing.T) {
	valid, err := ParsePublicHTTPURL("https://example.com/path", "monitor_url")
	if err != nil {
		t.Fatalf("ParsePublicHTTPURL(valid) err = %v", err)
	}
	if valid.Hostname() != "example.com" {
		t.Fatalf("hostname = %q, want example.com", valid.Hostname())
	}

	for _, raw := range []string{
		"",
		"example.com",
		"file:///etc/passwd",
		"http://localhost",
		"http://127.0.0.1",
		"http://0177.0.0.1",
		"http://2130706433",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://host.docker.internal:99",
		"http://user@example.com",
		"https://example.com/" + strings.Repeat("a", MaxPublicHTTPURLBytes),
	} {
		if _, err := ParsePublicHTTPURL(raw, "monitor_url"); err == nil {
			t.Fatalf("ParsePublicHTTPURL(%q) err = nil, want rejection", raw)
		}
	}
}

func TestUnsafeIP(t *testing.T) {
	if !UnsafeIP(net.ParseIP("0.0.0.0")) {
		t.Fatal("UnsafeIP(0.0.0.0) = false, want true")
	}
	if UnsafeIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("UnsafeIP(8.8.8.8) = true, want false")
	}
}

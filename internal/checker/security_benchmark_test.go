package checker

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/netguard"
)

func BenchmarkProbeTargetSafetyCached(b *testing.B) {
	oldCache := defaultDNSCache
	defaultDNSCache = newCheckDNSCache(time.Hour, 16)
	defaultDNSCache.store(
		normalizeDNSCacheKey("example.com", "ip4"),
		[]net.IPAddr{{IP: net.ParseIP("93.184.216.34")}},
		time.Now().Add(time.Hour),
	)
	b.Cleanup(func() { defaultDNSCache = oldCache })

	target, err := url.Parse("https://example.com/path")
	if err != nil {
		b.Fatalf("parse target: %v", err)
	}
	const rawURL = "https://example.com/path"
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := validateInitialTarget(ctx, target); err != nil {
			b.Fatalf("validateInitialTarget: %v", err)
		}
	}
}

func BenchmarkParsePublicHTTPURL(b *testing.B) {
	const rawURL = "https://example.com/path"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := netguard.ParsePublicHTTPURL(rawURL, "monitor_url"); err != nil {
			b.Fatalf("ParsePublicHTTPURL: %v", err)
		}
	}
}

func BenchmarkProbeTargetSafetyBlockedLiteral(b *testing.B) {
	const rawURL = "http://127.0.0.1/admin"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := netguard.ParsePublicHTTPURL(rawURL, "probe safety check: target URL"); err == nil {
			b.Fatal("ParsePublicHTTPURL accepted unsafe target")
		}
	}
}

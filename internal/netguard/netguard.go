// Package netguard contains small SSRF guard helpers for outbound probes.
package netguard

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

var unsafeCIDRs = mustParseCIDRs([]string{
	"100.64.0.0/10",   // carrier-grade NAT
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1 documentation network
	"198.51.100.0/24", // TEST-NET-2 documentation network
	"198.18.0.0/15",   // benchmarking networks
	"203.0.113.0/24",  // TEST-NET-3 documentation network
	"240.0.0.0/4",     // reserved IPv4
	"2001:db8::/32",   // documentation IPv6
	"2001:2::/48",     // benchmarking IPv6
	"2001:10::/28",    // deprecated ORCHID
	"2002::/16",       // 6to4
	"3fff::/20",       // documentation IPv6
})

const MaxPublicHTTPURLBytes = 2083

var unsafeHostSuffixes = []string{
	".localhost",
	".local",
	".lan",
	".internal",
	".intranet",
	".home",
	".corp",
}

func mustParseCIDRs(rawCIDRs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(rawCIDRs))
	for _, raw := range rawCIDRs {
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			panic(err)
		}
		out = append(out, network)
	}
	return out
}

// UnsafeHost reports whether host is an obvious non-public probe target.
//
// Hostnames are treated as safe unless they are localhost aliases. Callers
// that resolve arbitrary hostnames should also apply UnsafeIP to every
// resolved address before connecting.
func UnsafeHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || host == "localhost" || host == "local" {
		return true
	}
	for _, suffix := range unsafeHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		return UnsafeIP(ip)
	}
	if looksLikeNonCanonicalIPv4(host) {
		return true
	}
	return false
}

func looksLikeNonCanonicalIPv4(host string) bool {
	if host == "" || strings.Contains(host, ":") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return false
		}
		if allDigits(label) {
			continue
		}
		if len(label) > 2 && label[0] == '0' && (label[1] == 'x' || label[1] == 'X') && allHexDigits(label[2:]) {
			continue
		}
		return false
	}
	return true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func allHexDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// ParsePublicHTTPURL parses rawURL and rejects obvious unsafe untrusted probe
// targets. It intentionally does not resolve DNS; callers that will connect to
// the URL should also validate resolved addresses with UnsafeIP.
func ParsePublicHTTPURL(rawURL, field string) (*url.URL, error) {
	if field == "" {
		field = "url"
	}
	if rawURL == "" {
		return nil, fmt.Errorf("%s is required", field)
	}
	if len([]byte(rawURL)) > MaxPublicHTTPURLBytes {
		return nil, fmt.Errorf("%s must be %d bytes or fewer", field, MaxPublicHTTPURLBytes)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%s is not a valid URL: %v", field, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%s must use http or https", field)
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("%s must include a host", field)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%s must not include userinfo", field)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if UnsafeHost(host) {
		return nil, fmt.Errorf("%s host %q is not a public target", field, host)
	}
	return u, nil
}

// UnsafeIP reports whether ip is unsuitable for an untrusted outbound probe.
func UnsafeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	for _, network := range unsafeCIDRs {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

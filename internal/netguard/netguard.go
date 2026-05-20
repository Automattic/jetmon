// Package netguard contains small SSRF guard helpers for outbound probes.
package netguard

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
)

var unsafeCIDRs = parseStaticCIDRs([]string{
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

// parseStaticCIDRs parses the package-level static CIDR list. The input is a
// constant developer-controlled list, so any failure here is a code bug; we
// log.Fatalf instead of panicking so the operator sees a clean message and
// the failing CIDR. The TestUnsafeCIDRListParses test guards against this
// reaching production.
func parseStaticCIDRs(rawCIDRs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(rawCIDRs))
	for _, raw := range rawCIDRs {
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			log.Fatalf("netguard: invalid static CIDR %q: %v", raw, err)
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
	host = NormalizeHost(host)
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

func NormalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func looksLikeNonCanonicalIPv4(host string) bool {
	if host == "" || strings.Contains(host, ":") {
		return false
	}
	labelStart := 0
	for i := 0; i <= len(host); i++ {
		if i < len(host) && host[i] != '.' {
			continue
		}
		if !looksLikeNumericIPv4Label(host[labelStart:i]) {
			return false
		}
		labelStart = i + 1
	}
	return true
}

func looksLikeNumericIPv4Label(label string) bool {
	if label == "" {
		return false
	}
	if allDigits(label) {
		return true
	}
	return len(label) > 2 && label[0] == '0' &&
		(label[1] == 'x' || label[1] == 'X') &&
		allHexDigits(label[2:])
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

func ValidOutboundHeader(name, value string) bool {
	return ValidHTTPHeaderName(name) && !ForbiddenOutboundHeaderName(name) && ValidHTTPHeaderValue(value)
}

func ValidHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isHTTPTokenChar(name[i]) {
			return false
		}
	}
	return true
}

func isHTTPTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

func ForbiddenOutboundHeaderName(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "content-length", "host", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func ValidHTTPHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
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
	if len(rawURL) > MaxPublicHTTPURLBytes {
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
	host := NormalizeHost(u.Hostname())
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

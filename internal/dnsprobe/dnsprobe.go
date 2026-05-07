package dnsprobe

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"sort"
	"strings"
	"time"
)

const (
	StatusOK            = "ok"
	StatusNXDomain      = "nxdomain"
	StatusTimeout       = "timeout"
	StatusNoRecords     = "no_records"
	StatusSERVFAIL      = "servfail"
	StatusResolverError = "resolver_error"
	StatusInvalidHost   = "invalid_host"
)

// Resolver captures the net.Resolver methods used by DNS checks. Tests can
// provide a fake resolver without changing production behavior.
type Resolver interface {
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Request describes one recursive DNS probe.
type Request struct {
	BlogID        int64
	Hostname      string
	Timeout       time.Duration
	ResolverAddrs []string
}

// Result contains the latest DNS evidence for a monitored hostname.
type Result struct {
	BlogID     int64
	Hostname   string
	Success    bool
	Status     string
	Error      string
	Addresses  []string
	CNAMEChain []string
	Resolver   string
	Duration   time.Duration
	Timestamp  time.Time
}

var defaultResolver Resolver = net.DefaultResolver

// Check performs a recursive DNS lookup. By default it uses the system resolver;
// when Request.ResolverAddrs is set it uses a stable per-hostname resolver from
// that list so tests and operators can point DNS monitoring at a known recursive
// path without creating synchronized resolver load.
func Check(ctx context.Context, req Request) Result {
	resolver, label := resolverForRequest(req)
	return checkWithResolver(ctx, resolver, req, label)
}

// CheckWithResolver performs a recursive DNS lookup with a supplied resolver.
func CheckWithResolver(ctx context.Context, resolver Resolver, req Request) Result {
	return checkWithResolver(ctx, resolver, req, "injected")
}

func checkWithResolver(ctx context.Context, resolver Resolver, req Request, resolverLabel string) Result {
	hostname := NormalizeHostname(req.Hostname)
	start := time.Now()
	res := Result{
		BlogID:    req.BlogID,
		Hostname:  hostname,
		Status:    StatusResolverError,
		Resolver:  resolverLabel,
		Timestamp: start.UTC(),
	}
	defer func() {
		res.Duration = time.Since(start)
	}()

	if hostname == "" {
		res.Status = StatusInvalidHost
		res.Error = "hostname is empty"
		return res
	}
	if resolver == nil {
		res.Status = StatusResolverError
		res.Error = "resolver is nil"
		return res
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if canonical, err := resolver.LookupCNAME(probeCtx, hostname); err == nil {
		canonical = NormalizeHostname(canonical)
		if canonical != "" && canonical != hostname {
			res.CNAMEChain = []string{canonical}
		}
	}

	addrs, err := resolver.LookupIPAddr(probeCtx, hostname)
	if err != nil {
		res.Status, res.Error = classifyError(err)
		return res
	}
	if len(addrs) == 0 {
		res.Status = StatusNoRecords
		res.Error = "no A or AAAA records returned"
		return res
	}
	res.Addresses = normalizeAddresses(addrs)
	res.Success = true
	res.Status = StatusOK
	return res
}

func resolverForRequest(req Request) (Resolver, string) {
	addrs := normalizeResolverAddrs(req.ResolverAddrs)
	if len(addrs) == 0 {
		return defaultResolver, "system"
	}
	addr := addrs[resolverIndex(req, len(addrs))]
	dialer := &net.Dialer{}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
	}, addr
}

// NormalizeHostname returns a lower-case hostname without a trailing root dot.
func NormalizeHostname(hostname string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(hostname)), ".")
}

func normalizeResolverAddrs(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	seen := make(map[string]struct{}, len(addrs))
	for _, raw := range addrs {
		addr := strings.TrimSpace(raw)
		if addr == "" {
			continue
		}
		normalized := normalizeResolverAddr(addr)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeResolverAddr(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	if strings.Contains(addr, ":") {
		return net.JoinHostPort(strings.Trim(addr, "[]"), "53")
	}
	return net.JoinHostPort(addr, "53")
}

func resolverIndex(req Request, n int) int {
	if n <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%d/%s", req.BlogID, NormalizeHostname(req.Hostname))
	return int(h.Sum32() % uint32(n))
}

func normalizeAddresses(addrs []net.IPAddr) []string {
	out := make([]string, 0, len(addrs))
	seen := make(map[string]struct{}, len(addrs))
	for _, addr := range addrs {
		if addr.IP == nil {
			continue
		}
		value := addr.IP.String()
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func classifyError(err error) (string, string) {
	if err == nil {
		return StatusOK, ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return StatusTimeout, err.Error()
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsTimeout:
			return StatusTimeout, dnsErr.Error()
		case dnsErr.IsNotFound:
			return StatusNXDomain, dnsErr.Error()
		case dnsErr.IsTemporary:
			return StatusSERVFAIL, dnsErr.Error()
		default:
			return StatusResolverError, dnsErr.Error()
		}
	}
	return StatusResolverError, err.Error()
}

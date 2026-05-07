package dnsprobe

import (
	"context"
	"errors"
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
	BlogID   int64
	Hostname string
	Timeout  time.Duration
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
	Duration   time.Duration
	Timestamp  time.Time
}

var defaultResolver Resolver = net.DefaultResolver

// Check performs a recursive DNS lookup with the default resolver.
func Check(ctx context.Context, req Request) Result {
	return CheckWithResolver(ctx, defaultResolver, req)
}

// CheckWithResolver performs a recursive DNS lookup with a supplied resolver.
func CheckWithResolver(ctx context.Context, resolver Resolver, req Request) Result {
	hostname := NormalizeHostname(req.Hostname)
	start := time.Now()
	res := Result{
		BlogID:    req.BlogID,
		Hostname:  hostname,
		Status:    StatusResolverError,
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
	if canonical, err := resolver.LookupCNAME(probeCtx, hostname); err == nil {
		canonical = NormalizeHostname(canonical)
		if canonical != "" && canonical != hostname {
			res.CNAMEChain = []string{canonical}
		}
	}
	res.Success = true
	res.Status = StatusOK
	return res
}

// NormalizeHostname returns a lower-case hostname without a trailing root dot.
func NormalizeHostname(hostname string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(hostname)), ".")
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

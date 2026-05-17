package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const ProtectedMaxResponseHeaderBytes int64 = 64 << 10

var ErrUnsafeTarget = errors.New("unsafe outbound target")

// NewProtectedHTTPClient returns an HTTP client for untrusted, user-supplied
// destinations. It validates redirect targets and refuses to dial non-public
// resolved addresses.
func NewProtectedHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: NewProtectedHTTPTransport(nil),
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req == nil || req.URL == nil {
				return fmt.Errorf("%w: redirect target URL is required", ErrUnsafeTarget)
			}
			_, err := ParsePublicHTTPURL(req.URL.String(), "redirect url")
			return err
		},
	}
}

func NewProtectedHTTPTransport(resolver *net.Resolver) *http.Transport {
	return &http.Transport{
		DialContext:            ProtectedDialContext(resolver),
		MaxResponseHeaderBytes: ProtectedMaxResponseHeaderBytes,
		TLSHandshakeTimeout:    5 * time.Second,
		ExpectContinueTimeout:  1 * time.Second,
		ForceAttemptHTTP2:      true,
	}
}

func ProtectedDialContext(resolver *net.Resolver) func(context.Context, string, string) (net.Conn, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addrs, err := resolvePublicIPAddrs(ctx, resolver, host, "target")
		if err != nil {
			return nil, err
		}
		var firstErr error
		for _, addr := range addrs {
			if !networkAllowsIP(network, addr.IP) {
				continue
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = err
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, fmt.Errorf("target %q has no addresses usable on %s", host, network)
	}
}

func ValidateResolvedHost(ctx context.Context, resolver *net.Resolver, host, field string) error {
	_, err := resolvePublicIPAddrs(ctx, resolver, host, field)
	return err
}

func resolvePublicIPAddrs(ctx context.Context, resolver *net.Resolver, host, field string) ([]net.IPAddr, error) {
	if field == "" {
		field = "target"
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if ip := net.ParseIP(host); ip != nil {
		if UnsafeIP(ip) {
			return nil, fmt.Errorf("%w: %s address %s is not public", ErrUnsafeTarget, field, ip)
		}
		return []net.IPAddr{{IP: ip}}, nil
	}
	if UnsafeHost(host) {
		return nil, fmt.Errorf("%w: %s host %q is not public", ErrUnsafeTarget, field, host)
	}
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%s resolve %q: %w", field, host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s resolve %q: no addresses", field, host)
	}
	for _, addr := range addrs {
		if UnsafeIP(addr.IP) {
			return nil, fmt.Errorf("%w: %s host %q resolves to non-public address %s", ErrUnsafeTarget, field, host, addr.IP)
		}
	}
	return addrs, nil
}

func networkAllowsIP(network string, ip net.IP) bool {
	if ip == nil {
		return false
	}
	switch network {
	case "tcp4":
		return ip.To4() != nil
	case "tcp6":
		return ip.To4() == nil
	default:
		return true
	}
}

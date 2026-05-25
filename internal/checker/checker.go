package checker

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Automattic/jetmon/internal/checkmode"
	"github.com/Automattic/jetmon/internal/netguard"
)

// ErrorCode mirrors the status change email types from the original Jetmon.
const (
	ErrorNone          = 0
	ErrorTimeout       = 1
	ErrorConnect       = 2
	ErrorSSL           = 3
	ErrorRedirect      = 4
	ErrorKeyword       = 5
	ErrorTLSExpired    = 6
	ErrorTLSDeprecated = 7
	ErrorBodyRead      = 8
	ErrorProbeSafety   = 9
	ErrorBodyTruncated = ErrorBodyRead
)

const (
	maxBodyIntegrityBytes      int64 = 64 << 10
	maxKeywordBodyBytes        int64 = 1 << 20
	maxSemanticBodyPrefixBytes       = 4 << 10
	maxResultURLDetailBytes          = 2048
	maxResponseHeaderBytes     int64 = 64 << 10
	checkDNSCacheTTL                 = 15 * time.Minute
	checkDNSCacheTTLJitter           = 3 * time.Minute
	checkDNSCacheMaxEntries          = 2000000
	checkDNSCachePurgeInterval       = 10000
	// checkDNSCacheNegativeTTL bounds how long NXDOMAIN answers are cached.
	// Kept short so a freshly-registered domain becomes resolvable quickly,
	// long enough to absorb a burst of checks for a clearly-missing host.
	checkDNSCacheNegativeTTL = 5 * time.Second
)

var errBodyReadBudgetExceeded = errors.New("response body read budget exceeded")
var errProbeSafetyBlock = errors.New("probe safety block")

var semanticFailurePatterns = []struct {
	needle string
	rule   string
	detail string
}{
	{
		needle: "error establishing a database connection",
		rule:   "semantic_wp_db_error",
		detail: "WordPress database error page returned with HTTP 200",
	},
	{
		needle: "there has been a critical error on this website",
		rule:   "semantic_wp_critical_error",
		detail: "WordPress critical error page returned with HTTP 200",
	},
	{
		needle: "there has been a critical error on your website",
		rule:   "semantic_wp_critical_error",
		detail: "WordPress critical error page returned with HTTP 200",
	},
	{
		needle: "this site is experiencing technical difficulties",
		rule:   "semantic_wp_technical_difficulties",
		detail: "WordPress technical difficulties page returned with HTTP 200",
	},
	{
		needle: "the site is experiencing technical difficulties",
		rule:   "semantic_wp_technical_difficulties",
		detail: "WordPress technical difficulties page returned with HTTP 200",
	},
	{
		needle: "briefly unavailable for scheduled maintenance",
		rule:   "semantic_wp_maintenance",
		detail: "WordPress maintenance page returned with HTTP 200",
	},
	{
		needle: "xml-rpc server accepts post requests only",
		rule:   "semantic_wp_xmlrpc_echo",
		detail: "WordPress XML-RPC endpoint response was served as the homepage with HTTP 200",
	},
	{
		needle: "your php installation appears to be missing the mysql extension which is required by wordpress",
		rule:   "semantic_wp_missing_mysql_extension",
		detail: "WordPress missing MySQL extension page returned with HTTP 200",
	},
	{
		needle: "welcome to wordpress. before getting started",
		rule:   "semantic_wp_setup_config",
		detail: "WordPress setup/configuration page returned with HTTP 200",
	},
	{
		needle: "there doesn't seem to be a wp-config.php file",
		rule:   "semantic_wp_setup_config",
		detail: "WordPress setup/configuration page returned with HTTP 200",
	},
	{
		needle: "one or more database tables are unavailable. the database may need to be repaired",
		rule:   "semantic_wp_db_repair",
		detail: "WordPress database repair page returned with HTTP 200",
	},
	{
		needle: "database tables are missing",
		rule:   "semantic_wp_db_missing_tables",
		detail: "WordPress missing database tables page returned with HTTP 200",
	},
	{
		needle: "database update required",
		rule:   "semantic_wp_db_update_required",
		detail: "WordPress database update page returned with HTTP 200",
	},
	{
		needle: "your server is running php version",
		rule:   "semantic_wp_unsupported_php",
		detail: "WordPress unsupported PHP version page returned with HTTP 200",
	},
}

// RedirectPolicy controls how redirect responses are handled.
type RedirectPolicy string

const (
	RedirectFollow RedirectPolicy = "follow"
	RedirectAlert  RedirectPolicy = "alert"
	RedirectFail   RedirectPolicy = "fail"
)

// defaultTransport is shared across checks so the checker does not allocate a
// fresh connection pool for every probe. The http.Client stays per request so
// redirect policy remains isolated to that site check. Timeouts are carried by
// the request context instead of http.Client.Timeout so each probe does not pay
// for a second timer on the hot path.
var defaultResolver = newCheckResolver()
var defaultTransport = newCheckTransportWithResolver(defaultResolver)
var defaultHTTPIPTransport = newHTTPIPPoolTransportWithFallbackAndResolver(defaultTransport, defaultResolver)
var defaultDNSCache = newCheckDNSCache(checkDNSCacheTTL, checkDNSCacheMaxEntries)
var defaultDNSLookupLimiter = newCheckDNSLookupLimiter()
var configuredResolverMu sync.RWMutex
var configuredResolverServers []string

// targetSafetyKey is the context key marking a check as requiring SSRF
// safety re-validation at dial time. The empty-struct type follows the
// stdlib convention: the key's uniqueness comes from the type itself, so no
// other package can collide with it even by accident.
type targetSafetyKey struct{}

func withTargetSafety(ctx context.Context) context.Context {
	return context.WithValue(ctx, targetSafetyKey{}, true)
}

func targetSafetyEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(targetSafetyKey{}).(bool)
	return enabled
}

type checkDNSLookupLimiter struct {
	slots chan struct{}
}

func newCheckDNSLookupLimiter() *checkDNSLookupLimiter {
	limit := runtime.GOMAXPROCS(0) * 128
	if limit < 128 {
		limit = 128
	}
	if limit > 1024 {
		limit = 1024
	}
	return &checkDNSLookupLimiter{slots: make(chan struct{}, limit)}
}

func (l *checkDNSLookupLimiter) acquire(ctx context.Context) (func(), error) {
	if l == nil || cap(l.slots) == 0 {
		return func() {}, nil
	}
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ConfigureResolverServers replaces the system resolver list used by the HTTP
// checker. It is intended for process startup before checks begin; runtime
// resolver changes should restart the service so in-flight checks continue to
// use a stable transport.
func ConfigureResolverServers(rawServers []string) error {
	servers, err := normalizeResolverServers(rawServers)
	if err != nil {
		return err
	}

	configuredResolverMu.Lock()
	configuredResolverServers = servers
	configuredResolverMu.Unlock()

	newResolver := newCheckResolver()
	newTransport := newCheckTransportWithResolver(newResolver)
	newHTTPIPTransport := newHTTPIPPoolTransportWithFallbackAndResolver(newTransport, newResolver)

	configuredResolverMu.Lock()
	oldTransport := defaultTransport
	oldHTTPIPTransport := defaultHTTPIPTransport
	defaultResolver = newResolver
	defaultTransport = newTransport
	defaultHTTPIPTransport = newHTTPIPTransport
	defaultDNSCache = newCheckDNSCache(checkDNSCacheTTL, checkDNSCacheMaxEntries)
	configuredResolverMu.Unlock()

	if oldTransport != nil {
		oldTransport.CloseIdleConnections()
	}
	if oldHTTPIPTransport != nil {
		oldHTTPIPTransport.CloseIdleConnections()
	}
	return nil
}

// ConfiguredResolverServers returns the normalized resolver override currently
// installed for checks. An empty slice means checks are using the host resolver
// configuration.
func ConfiguredResolverServers() []string {
	configuredResolverMu.RLock()
	defer configuredResolverMu.RUnlock()
	return append([]string(nil), configuredResolverServers...)
}

type checkDNSCache struct {
	mu         sync.RWMutex
	ttl        time.Duration
	jitter     time.Duration
	salt       uint64
	maxEntries int
	writes     int
	entries    map[string]checkDNSCacheEntry
}

type checkDNSCacheEntry struct {
	addrs   []net.IPAddr
	err     error
	expires time.Time
	// negative marks the entry as a cached NXDOMAIN-like lookup failure. The
	// addrs slice is nil for negative entries; err carries the original
	// resolver error so callers see the same diagnostic as an uncached miss.
	negative bool
}

func newCheckDNSCache(ttl time.Duration, maxEntries int) *checkDNSCache {
	return &checkDNSCache{
		ttl:        ttl,
		jitter:     checkDNSCacheTTLJitter,
		salt:       newDNSCacheSalt(),
		maxEntries: maxEntries,
		entries:    make(map[string]checkDNSCacheEntry),
	}
}

func (c *checkDNSCache) lookup(ctx context.Context, resolver *net.Resolver, host, network string) ([]net.IPAddr, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if c == nil || c.ttl <= 0 {
		return lookupResolverIPAddrs(ctx, resolver, host, network)
	}
	key := normalizeDNSCacheKey(host, preferredLookupFamily(network))
	now := time.Now()
	c.mu.RLock()
	entry, ok := c.entries[key]
	if ok && now.Before(entry.expires) {
		// Cache entries are immutable after store; callers only iterate them.
		// Returning the stored slice avoids an allocation on every repeated
		// check for long-lived monitored sites.
		addrs := entry.addrs
		negative := entry.negative
		cachedErr := entry.err
		c.mu.RUnlock()
		if negative {
			return nil, cachedErr
		}
		return addrs, nil
	}
	c.mu.RUnlock()

	release, err := defaultDNSLookupLimiter.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	now = time.Now()
	c.mu.RLock()
	entry, ok = c.entries[key]
	if ok && now.Before(entry.expires) {
		// Cache entries are immutable after store; callers only iterate them.
		// Returning the stored slice avoids an allocation on every repeated
		// check for long-lived monitored sites.
		addrs := entry.addrs
		negative := entry.negative
		cachedErr := entry.err
		c.mu.RUnlock()
		if negative {
			return nil, cachedErr
		}
		return addrs, nil
	}
	c.mu.RUnlock()

	addrs, err := lookupResolverIPAddrs(ctx, resolver, host, network)
	if err != nil {
		if isDNSNotFoundErr(err) {
			c.storeNegative(key, err, now.Add(checkDNSCacheNegativeTTL))
		}
		return addrs, err
	}
	if len(addrs) == 0 {
		return addrs, err
	}
	c.store(key, addrs, c.expiresAt(key, now))
	return addrs, nil
}

// isDNSNotFoundErr returns true when err is an NXDOMAIN-like response. Only
// such errors are negative-cached; transient failures (timeouts, SERVFAIL) are
// left to retry so a flaky resolver does not poison the cache.
func isDNSNotFoundErr(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}
	return false
}

func (c *checkDNSCache) expiresAt(key string, now time.Time) time.Time {
	if c == nil || c.ttl <= 0 {
		return now
	}
	jitter := c.jitter
	if jitter <= 0 || jitter >= c.ttl {
		return now.Add(c.ttl)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	var salt [8]byte
	binary.LittleEndian.PutUint64(salt[:], c.salt)
	_, _ = h.Write(salt[:])
	offset := time.Duration(h.Sum64() % uint64(jitter))
	return now.Add(c.ttl - jitter + offset)
}

func newDNSCacheSalt() uint64 {
	h := fnv.New64a()
	if hostname, err := os.Hostname(); err == nil {
		_, _ = h.Write([]byte(hostname))
	}
	var now [8]byte
	binary.LittleEndian.PutUint64(now[:], uint64(time.Now().UnixNano()))
	_, _ = h.Write(now[:])
	return h.Sum64()
}

func (c *checkDNSCache) store(key string, addrs []net.IPAddr, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxEntries > 0 && len(c.entries) >= c.maxEntries {
		c.purgeExpiredLocked(time.Now())
		if len(c.entries) >= c.maxEntries {
			return
		}
	}
	c.entries[key] = checkDNSCacheEntry{addrs: cloneIPAddrs(addrs), expires: expires}
	c.writes++
	if c.writes%checkDNSCachePurgeInterval == 0 {
		c.purgeExpiredLocked(time.Now())
	}
}

// storeNegative records an NXDOMAIN-like miss so a burst of checks for a
// clearly-missing host short-circuits without re-hitting the resolver.
func (c *checkDNSCache) storeNegative(key string, lookupErr error, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.maxEntries > 0 && len(c.entries) >= c.maxEntries {
		c.purgeExpiredLocked(time.Now())
		if len(c.entries) >= c.maxEntries {
			return
		}
	}
	c.entries[key] = checkDNSCacheEntry{err: lookupErr, expires: expires, negative: true}
	c.writes++
	if c.writes%checkDNSCachePurgeInterval == 0 {
		c.purgeExpiredLocked(time.Now())
	}
}

func (c *checkDNSCache) purgeExpiredLocked(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, key)
		}
	}
}

func normalizeDNSCacheHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func normalizeDNSCacheKey(host, family string) string {
	return normalizeDNSCacheHost(host) + "|" + family
}

func preferredLookupFamily(network string) string {
	if strings.HasSuffix(network, "6") {
		return "ip6"
	}
	return "ip4"
}

func lookupResolverIPAddrs(ctx context.Context, resolver *net.Resolver, host, network string) ([]net.IPAddr, error) {
	family := preferredLookupFamily(network)
	addrs, err := lookupResolverIPFamily(ctx, resolver, host, family)
	if (err == nil && len(addrs) > 0) || family == "ip6" || strings.HasSuffix(network, "4") {
		return addrs, err
	}

	fallback, fallbackErr := lookupResolverIPFamily(ctx, resolver, host, "ip6")
	if fallbackErr == nil && len(fallback) > 0 {
		return fallback, nil
	}
	if err != nil {
		return nil, err
	}
	return fallback, fallbackErr
}

func lookupResolverIPFamily(ctx context.Context, resolver *net.Resolver, host, family string) ([]net.IPAddr, error) {
	ips, err := resolver.LookupIP(ctx, family, host)
	if err != nil {
		return nil, err
	}
	addrs := make([]net.IPAddr, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		addrs = append(addrs, net.IPAddr{IP: ip})
	}
	return addrs, nil
}

func cloneIPAddrs(addrs []net.IPAddr) []net.IPAddr {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]net.IPAddr, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IP == nil {
			continue
		}
		ip := make(net.IP, len(addr.IP))
		copy(ip, addr.IP)
		out = append(out, net.IPAddr{IP: ip, Zone: addr.Zone})
	}
	return out
}

func newCheckTransport() *http.Transport {
	return newCheckTransportWithResolver(newCheckResolver())
}

func newCheckTransportWithResolver(resolver *net.Resolver) *http.Transport {
	return &http.Transport{
		DialContext: newCheckDialContext(resolver),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
			// Deprecated TLS versions are still site-reachability signals.
			// Complete the handshake so the orchestrator can open an advisory
			// tls_deprecated event instead of reporting customer downtime.
			MinVersion: tls.VersionTLS10,
		},
		TLSHandshakeTimeout:    10 * time.Second,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
		// Jetmon checks huge fleets of mostly-unique hostnames on minute-scale
		// cadences. Connections would usually expire before reuse, while the
		// shared idle pool becomes a global lock and goroutine-pressure point at
		// high concurrency.
		DisableKeepAlives: true,
	}
}

type httpIPPoolTransport struct {
	inner    *http.Transport
	fallback *http.Transport
	resolver *net.Resolver
}

func newHTTPIPPoolTransport() *httpIPPoolTransport {
	return newHTTPIPPoolTransportWithFallback(defaultTransport)
}

func newHTTPIPPoolTransportWithFallback(fallback *http.Transport) *httpIPPoolTransport {
	return newHTTPIPPoolTransportWithFallbackAndResolver(fallback, newCheckResolver())
}

func newHTTPIPPoolTransportWithFallbackAndResolver(fallback *http.Transport, resolver *net.Resolver) *httpIPPoolTransport {
	inner := newCheckTransportWithResolver(resolver)
	inner.DisableKeepAlives = false
	inner.MaxIdleConns = 8192
	inner.MaxIdleConnsPerHost = 2048
	inner.IdleConnTimeout = 30 * time.Second
	return &httpIPPoolTransport{
		inner:    inner,
		fallback: fallback,
		resolver: resolver,
	}
}

func (t *httpIPPoolTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil || t.inner == nil {
		return defaultTransport.RoundTrip(req)
	}
	if req == nil || req.URL == nil || req.URL.Scheme != "http" {
		return t.roundTripFallback(req)
	}
	host := req.URL.Hostname()
	if host == "" || net.ParseIP(host) != nil {
		return t.inner.RoundTrip(req)
	}
	port := req.URL.Port()
	if port == "" {
		port = "80"
	}

	trace := httptrace.ContextClientTrace(req.Context())
	if trace != nil && trace.DNSStart != nil {
		trace.DNSStart(httptrace.DNSStartInfo{Host: host})
	}
	addrs, err := defaultDNSCache.lookup(req.Context(), t.resolver, host, "tcp")
	if trace != nil && trace.DNSDone != nil {
		trace.DNSDone(httptrace.DNSDoneInfo{Addrs: addrs, Err: err})
	}
	if err != nil {
		return nil, err
	}
	ordered := orderedResolverAddrs(addrs, "tcp")
	if len(ordered) == 0 {
		return nil, &net.DNSError{Name: host, Err: "no addresses"}
	}

	var firstErr error
	for _, addr := range ordered {
		if targetSafetyEnabled(req.Context()) && netguard.UnsafeIP(addr.IP) {
			err := fmt.Errorf("%w: target %q resolves to non-public address %s", errProbeSafetyBlock, host, addr.IP)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		resp, err := t.roundTripResolvedIP(req, addr, port)
		if err == nil || resp != nil {
			return resp, err
		}
		if firstErr == nil {
			firstErr = err
		}
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("lookup %s: no usable addresses", host)
}

func (t *httpIPPoolTransport) roundTripResolvedIP(req *http.Request, addr net.IPAddr, port string) (*http.Response, error) {
	clone := new(http.Request)
	*clone = *req
	cloneURL := *req.URL
	cloneURL.Host = net.JoinHostPort(addr.IP.String(), port)
	clone.URL = &cloneURL
	if clone.Host == "" {
		clone.Host = req.URL.Host
	}
	resp, err := t.inner.RoundTrip(clone)
	if resp != nil && resp.Request == clone {
		resp.Request = req
	}
	return resp, err
}

func (t *httpIPPoolTransport) roundTripFallback(req *http.Request) (*http.Response, error) {
	if t != nil && t.fallback != nil {
		return t.fallback.RoundTrip(req)
	}
	return defaultTransport.RoundTrip(req)
}

func (t *httpIPPoolTransport) CloseIdleConnections() {
	if t != nil && t.inner != nil {
		t.inner.CloseIdleConnections()
	}
}

func transportForRequestURL(rawURL string) http.RoundTripper {
	if strings.HasPrefix(strings.ToLower(rawURL), "http://") && defaultHTTPIPTransport != nil {
		return defaultHTTPIPTransport
	}
	return defaultTransport
}

// targetSafetyDialControl is the net.Dialer.ControlContext gate that
// re-validates the resolved IP one last time, after DNS resolution and
// before the kernel issues the connect syscall. This closes the TOCTOU
// window between the in-Go hostname-based validation and the actual dial:
// even if a path bypassed the higher-level wrapper (nil custom resolver,
// future code change), a target-safety-enabled request cannot connect to
// an RFC1918 / loopback / link-local / etc address.
func targetSafetyDialControl(ctx context.Context, _, address string, _ syscall.RawConn) error {
	if !targetSafetyEnabled(ctx) {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: dial address %q is not host:port", errProbeSafetyBlock, address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// At ControlContext time the address has already been resolved to an
		// IP literal; a non-IP host here would indicate a Go stdlib change we
		// want to fail closed on.
		return fmt.Errorf("%w: dial address %q is not an IP literal", errProbeSafetyBlock, host)
	}
	if netguard.UnsafeIP(ip) {
		return fmt.Errorf("%w: dial address %s is not public", errProbeSafetyBlock, ip)
	}
	return nil
}

func newCheckDialContext(resolver *net.Resolver) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:        30 * time.Second,
		KeepAlive:      30 * time.Second,
		ControlContext: targetSafetyDialControl,
	}
	if resolver == nil {
		// No custom resolver: rely on Go's default. ControlContext above will
		// still gate every connect against netguard.UnsafeIP, so a DNS answer
		// that arrives later (or differs from any earlier in-Go check) cannot
		// drive the kernel to dial an unsafe address.
		return dialer.DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if ip := net.ParseIP(host); ip != nil {
			if targetSafetyEnabled(ctx) && netguard.UnsafeIP(ip) {
				return nil, fmt.Errorf("%w: target address %s is not public", errProbeSafetyBlock, ip)
			}
			return dialer.DialContext(ctx, network, address)
		}

		trace := httptrace.ContextClientTrace(ctx)
		if trace != nil && trace.DNSStart != nil {
			trace.DNSStart(httptrace.DNSStartInfo{Host: host})
		}
		addrs, err := defaultDNSCache.lookup(ctx, resolver, host, network)
		if trace != nil && trace.DNSDone != nil {
			trace.DNSDone(httptrace.DNSDoneInfo{Addrs: addrs, Err: err})
		}
		if err != nil {
			return nil, err
		}

		var firstErr error
		for _, addr := range orderedResolverAddrs(addrs, network) {
			if targetSafetyEnabled(ctx) && netguard.UnsafeIP(addr.IP) {
				err := fmt.Errorf("%w: target %q resolves to non-public address %s", errProbeSafetyBlock, host, addr.IP)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			target := net.JoinHostPort(addr.IP.String(), port)
			conn, err := dialer.DialContext(ctx, network, target)
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
		return nil, fmt.Errorf("lookup %s: no usable addresses", host)
	}
}

func newCheckResolver() *net.Resolver {
	servers := directResolverServers()
	if len(servers) == 0 {
		return nil
	}
	var next atomic.Uint64
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			idx := next.Add(1)
			server := servers[int(idx-1)%len(servers)]
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, server)
		},
	}
}

func orderedResolverAddrs(addrs []net.IPAddr, network string) []net.IPAddr {
	if len(addrs) == 1 {
		addr := addrs[0]
		if addr.IP == nil {
			return nil
		}
		if strings.HasSuffix(network, "6") && addr.IP.To4() != nil {
			return nil
		}
		if strings.HasSuffix(network, "4") && addr.IP.To4() == nil {
			return nil
		}
		return addrs
	}
	ordered := make([]net.IPAddr, 0, len(addrs))
	wants4 := strings.HasSuffix(network, "4")
	wants6 := strings.HasSuffix(network, "6")
	for _, addr := range addrs {
		if addr.IP == nil {
			continue
		}
		if addr.IP.To4() == nil {
			continue
		}
		if wants6 {
			continue
		}
		ordered = append(ordered, addr)
	}
	for _, addr := range addrs {
		if addr.IP == nil {
			continue
		}
		if addr.IP.To4() != nil {
			continue
		}
		if wants4 {
			continue
		}
		ordered = append(ordered, addr)
	}
	return ordered
}

func directResolverServers() []string {
	configuredResolverMu.RLock()
	if len(configuredResolverServers) > 0 {
		servers := append([]string(nil), configuredResolverServers...)
		configuredResolverMu.RUnlock()
		return servers
	}
	configuredResolverMu.RUnlock()

	if servers := parseResolverServers(readResolverConfig("/run/systemd/resolve/resolv.conf")); len(servers) > 0 {
		return servers
	}
	return parseResolverServers(readResolverConfig("/etc/resolv.conf"))
}

func readResolverConfig(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func parseResolverServers(raw string) []string {
	var servers []string
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		host := fields[1]
		if isLocalResolverHost(host) {
			continue
		}
		servers = append(servers, net.JoinHostPort(host, "53"))
	}
	return servers
}

func normalizeResolverServers(rawServers []string) ([]string, error) {
	if len(rawServers) == 0 {
		return nil, nil
	}
	servers := make([]string, 0, len(rawServers))
	for i, raw := range rawServers {
		server, err := normalizeResolverServer(raw)
		if err != nil {
			return nil, fmt.Errorf("resolver %d: %w", i, err)
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func normalizeResolverServer(raw string) (string, error) {
	server := strings.TrimSpace(raw)
	if server == "" {
		return "", fmt.Errorf("empty resolver")
	}
	host := server
	port := "53"
	if splitHost, splitPort, err := net.SplitHostPort(server); err == nil {
		host = strings.Trim(splitHost, "[]")
		port = splitPort
	} else if strings.Contains(server, ":") {
		if ip := net.ParseIP(strings.Trim(server, "[]")); ip == nil || ip.To4() != nil {
			return "", fmt.Errorf("resolver must be an IP literal with optional port")
		}
		host = strings.Trim(server, "[]")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("resolver must be an IP literal with optional port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return "", fmt.Errorf("resolver port must be between 1 and 65535")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(n)), nil
}

func isLocalResolverHost(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// Request holds the parameters for a single HTTP check.
type Request struct {
	MonitorSiteID       int64
	BlogID              int64
	URL                 string
	Method              string
	DetectionProfile    string
	TimeoutSeconds      int
	BodyReadMaxBytes    int64
	BodyReadMaxMS       int
	KeywordReadMaxBytes int64
	KeywordReadMaxMS    int
	Keyword             *string
	ForbiddenKeyword    *string
	ForbiddenKeywords   []string
	CustomHeaders       map[string]string
	RedirectPolicy      RedirectPolicy
	EnforceTargetSafety bool
}

// Result holds the outcome of a single HTTP check.
type Result struct {
	MonitorSiteID    int64
	BlogID           int64
	URL              string
	Method           string
	DetectionProfile string
	Success          bool
	HTTPCode         int
	ErrorCode        int
	// ErrorDetail is bounded diagnostic context from the checker. It is meant
	// for operator-facing event metadata, not matching logic.
	ErrorDetail string

	RTT  time.Duration
	DNS  time.Duration
	TCP  time.Duration
	TLS  time.Duration
	TTFB time.Duration

	SSLExpiry          *time.Time
	TLSVersion         uint16
	CipherSuite        uint16
	DNSFailureKind     string
	DNSFailureName     string
	DNSFailureServer   string
	RedirectChanged    bool
	RedirectCount      int
	RedirectChain      []string
	FinalURL           string
	KeywordRule        string
	BodyReadMode       string
	BodyBytesRead      int64
	BodyExpectedBytes  int64
	BodyReadLimitBytes int64
	BodyReadError      string

	Timestamp time.Time
}

// StatusType maps the result to a WPCOM status change email type.
func (r *Result) StatusType() string {
	switch {
	case r.Success:
		return "success"
	case r.ErrorCode == ErrorProbeSafety:
		return "probe_safety"
	case r.ErrorCode == ErrorSSL || r.ErrorCode == ErrorTLSExpired:
		return "https"
	case r.ErrorCode == ErrorTimeout || r.ErrorCode == ErrorBodyRead:
		return "intermittent"
	case r.ErrorCode == ErrorRedirect:
		return "redirect"
	case r.HTTPCode == 403:
		return "blocked"
	case r.HTTPCode >= 500:
		return "server"
	case r.HTTPCode >= 400:
		return "client"
	default:
		return "intermittent"
	}
}

// IsFailure reports whether the result should enter the downtime pipeline.
func (r *Result) IsFailure() bool {
	if r.ErrorCode == ErrorProbeSafety {
		return false
	}
	if !r.Success {
		return true
	}
	switch r.ErrorCode {
	case ErrorNone, ErrorTLSDeprecated:
		return false
	default:
		return true
	}
}

// IsProbeSafetyBlock reports that Jetmon intentionally skipped the outbound
// probe because the target would be unsafe for an untrusted remote site check.
func (r *Result) IsProbeSafetyBlock() bool {
	return r != nil && r.ErrorCode == ErrorProbeSafety
}

// Check performs an HTTP check and returns the result.
func Check(ctx context.Context, req Request) Result {
	method, err := checkmode.NormalizeMethod(req.Method, checkmode.MethodGET)
	if err != nil {
		method = checkmode.MethodGET
	}
	profile, err := checkmode.NormalizeProfile(req.DetectionProfile, checkmode.ProfileFull)
	if err != nil {
		profile = checkmode.ProfileFull
	}
	profile = checkmode.EffectiveProfile(method, profile)

	res := Result{
		MonitorSiteID:    req.MonitorSiteID,
		BlogID:           req.BlogID,
		URL:              req.URL,
		Method:           method,
		DetectionProfile: profile,
		Timestamp:        time.Now(),
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if req.EnforceTargetSafety {
		ctx = withTargetSafety(ctx)
	}

	var (
		dnsStart, tcpStart, tlsStart, reqStart time.Time
		dnsEnd, tcpEnd, tlsEnd                 time.Time
	)

	trace := &httptrace.ClientTrace{
		DNSStart:             func(_ httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:              func(_ httptrace.DNSDoneInfo) { dnsEnd = time.Now() },
		ConnectStart:         func(_, _ string) { tcpStart = time.Now() },
		ConnectDone:          func(_, _ string, _ error) { tcpEnd = time.Now() },
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(_ tls.ConnectionState, _ error) { tlsEnd = time.Now() },
		WroteRequest:         func(_ httptrace.WroteRequestInfo) { reqStart = time.Now() },
		GotFirstResponseByte: func() { res.TTFB = time.Since(reqStart) },
	}
	ctx = httptrace.WithClientTrace(ctx, trace)

	headers := req.CustomHeaders

	var redirectChain []string
	var initialRedirectHost string
	redirectPolicyStr := string(req.RedirectPolicy)
	if redirectPolicyStr == "" {
		redirectPolicyStr = string(RedirectFollow)
	}
	if profile != checkmode.ProfileFull {
		redirectPolicyStr = string(RedirectFollow)
	}

	client := &http.Client{
		Transport: transportForRequestURL(req.URL),
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			redirectChain = append(redirectChain, boundedURLDetail(r.URL.String()))
			if redirectPolicyStr == string(RedirectFail) {
				return fmt.Errorf("redirect policy: fail")
			}
			if len(redirectChain) > 10 {
				return fmt.Errorf("too many redirects")
			}
			if err := validateRedirectTarget(r.Context(), initialRedirectHost, r.URL); err != nil {
				return err
			}
			return nil
		},
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, nil)
	if err != nil {
		if req.EnforceTargetSafety {
			res.ErrorCode = ErrorProbeSafety
		} else {
			res.ErrorCode = ErrorConnect
		}
		res.ErrorDetail = boundedErrorDetail(err)
		return res
	}
	initialRedirectHost = normalizedURLHost(httpReq.URL)
	if req.EnforceTargetSafety {
		if err := validateInitialTarget(ctx, httpReq.URL); err != nil {
			if errors.Is(err, errProbeSafetyBlock) {
				res.ErrorCode = ErrorProbeSafety
			} else {
				res.ErrorCode = ErrorConnect
				res.DNSFailureKind, res.DNSFailureName, res.DNSFailureServer = classifyDNSError(err)
			}
			res.ErrorDetail = boundedErrorDetail(err)
			return res
		}
	}

	httpReq.Header.Set("User-Agent", "jetmon/2.0 (Jetpack Site Uptime Monitor by WordPress.com)")
	for k, v := range headers {
		if !netguard.ValidOutboundHeader(k, v) {
			continue
		}
		httpReq.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	res.RTT = time.Since(start)
	res.RedirectCount = len(redirectChain)
	if len(redirectChain) > 0 {
		res.RedirectChain = append([]string(nil), redirectChain...)
	}
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		res.FinalURL = boundedURLDetail(resp.Request.URL.String())
	}

	// Only record a phase duration when BOTH start and end fired. If a
	// connection errors mid-handshake the DNSStart / ConnectStart / TLS
	// HandshakeStart hook fires without its matching Done — in that case
	// the *End is the zero time.Time and *End.Sub(*Start) returns a huge
	// negative duration (roughly -unix-nanos), which then overflows the
	// jetpack_monitor_check_history INT columns and surfaces as
	// "Out of range value for column 'dns_ms'". A failed phase is
	// reported as zero rather than a misleading negative.
	if !dnsStart.IsZero() && !dnsEnd.IsZero() {
		res.DNS = dnsEnd.Sub(dnsStart)
	}
	if !tcpStart.IsZero() && !tcpEnd.IsZero() {
		res.TCP = tcpEnd.Sub(tcpStart)
	}
	if !tlsStart.IsZero() && !tlsEnd.IsZero() {
		res.TLS = tlsEnd.Sub(tlsStart)
	}

	if err != nil {
		res.ErrorDetail = boundedErrorDetail(err)
		res.DNSFailureKind, res.DNSFailureName, res.DNSFailureServer = classifyDNSError(err)
		if ctx.Err() != nil {
			res.ErrorCode = ErrorTimeout
		} else if errors.Is(err, errProbeSafetyBlock) {
			res.ErrorCode = ErrorProbeSafety
		} else if strings.Contains(err.Error(), "redirect") {
			res.ErrorCode = ErrorRedirect
		} else if strings.Contains(err.Error(), "tls") || strings.Contains(err.Error(), "certificate") {
			res.ErrorCode = ErrorSSL
		} else {
			res.ErrorCode = ErrorConnect
		}
		return res
	}
	defer resp.Body.Close()

	res.HTTPCode = resp.StatusCode

	// Inspect TLS state if available.
	if resp.TLS != nil {
		res.TLSVersion = resp.TLS.Version
		res.CipherSuite = resp.TLS.CipherSuite
		if len(resp.TLS.PeerCertificates) > 0 {
			cert := resp.TLS.PeerCertificates[0]
			expiry := cert.NotAfter
			res.SSLExpiry = &expiry
			if time.Now().After(expiry) {
				res.ErrorCode = ErrorTLSExpired
				return res
			}
		}
		// Flag deprecated TLS versions (TLS 1.0 = 0x0301, TLS 1.1 = 0x0302)
		// only when rich detections are enabled. Legacy/simple rollout modes
		// keep TLS version as telemetry but avoid new advisory events.
		if profile == checkmode.ProfileFull && resp.TLS.Version <= tls.VersionTLS11 {
			res.ErrorCode = ErrorTLSDeprecated
		}
	}

	if profile == checkmode.ProfileFull && redirectPolicyStr == string(RedirectAlert) && res.RedirectCount > 0 {
		res.RedirectChanged = true
	}

	forbiddenKeywords := collectForbiddenKeywords(req.ForbiddenKeyword, req.ForbiddenKeywords)
	needsBody := profile == checkmode.ProfileFull && method != http.MethodHead &&
		((req.Keyword != nil && *req.Keyword != "") || len(forbiddenKeywords) > 0)
	shouldReadBody := profile == checkmode.ProfileFull && method != http.MethodHead
	if shouldReadBody {
		bodyRead := readResponseBody(resp, needsBody, req)
		body := bodyRead.Body
		res.BodyReadMode = bodyRead.Mode
		res.BodyBytesRead = bodyRead.BytesRead
		res.BodyExpectedBytes = bodyRead.ExpectedBytes
		res.BodyReadLimitBytes = bodyRead.LimitBytes
		if bodyRead.Err != nil {
			res.BodyReadError = boundedErrorDetail(bodyRead.Err)
		}
		if bodyRead.Err != nil && res.HTTPCode < http.StatusBadRequest {
			if needsBody && errors.Is(bodyRead.Err, errBodyReadBudgetExceeded) {
				res.ErrorCode = ErrorTimeout
			} else {
				res.ErrorCode = ErrorBodyRead
			}
			res.ErrorDetail = res.BodyReadError
			return res
		}
		if rule, detail := semanticBodyFailure(resp, body, bodyRead.BytesRead); rule != "" {
			res.KeywordRule = rule
			res.ErrorCode = ErrorKeyword
			res.ErrorDetail = detail
			return res
		}

		// Keyword check uses the same bounded body read as integrity checks.
		if needsBody {
			bodyText := string(body)
			if req.Keyword != nil && *req.Keyword != "" {
				if !strings.Contains(bodyText, *req.Keyword) {
					res.KeywordRule = "required"
					res.ErrorCode = ErrorKeyword
					return res
				}
			}
			for _, keyword := range forbiddenKeywords {
				if strings.Contains(bodyText, keyword) {
					res.KeywordRule = "forbidden"
					res.ErrorCode = ErrorKeyword
					return res
				}
			}
		}
	}

	res.Success = res.HTTPCode > 0 && res.HTTPCode < 400
	return res
}

func boundedErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	const maxErrorDetail = 500
	detail := err.Error()
	if len(detail) <= maxErrorDetail {
		return detail
	}
	return detail[:maxErrorDetail] + "..."
}

func boundedURLDetail(rawURL string) string {
	if len(rawURL) <= maxResultURLDetailBytes {
		return rawURL
	}
	return rawURL[:maxResultURLDetailBytes] + "..."
}

func normalizedURLHost(u *url.URL) string {
	if u == nil {
		return ""
	}
	return netguard.NormalizeHost(u.Hostname())
}

func validateRedirectTarget(ctx context.Context, initialHost string, target *url.URL) error {
	if target != nil && target.Scheme != "" && target.Scheme != "http" && target.Scheme != "https" {
		return fmt.Errorf("%w: redirect safety check: target must use http or https", errProbeSafetyBlock)
	}
	if target != nil && target.User != nil {
		return fmt.Errorf("%w: redirect safety check: target must not include userinfo", errProbeSafetyBlock)
	}
	targetHost := normalizedURLHost(target)
	if targetHost == "" || targetHost == initialHost {
		return nil
	}
	if netguard.UnsafeHost(targetHost) {
		return fmt.Errorf("%w: redirect safety check: target host %q is not public", errProbeSafetyBlock, targetHost)
	}
	return validateResolvedTarget(ctx, "redirect safety check", targetHost)
}

func validateInitialTarget(ctx context.Context, target *url.URL) error {
	if target == nil {
		return fmt.Errorf("%w: probe safety check: target URL is required", errProbeSafetyBlock)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return fmt.Errorf("%w: probe safety check: target URL must use http or https", errProbeSafetyBlock)
	}
	targetHost := normalizedURLHost(target)
	if targetHost == "" {
		return fmt.Errorf("%w: probe safety check: target URL must include a host", errProbeSafetyBlock)
	}
	if target.User != nil {
		return fmt.Errorf("%w: probe safety check: target URL must not include userinfo", errProbeSafetyBlock)
	}
	if netguard.UnsafeHost(targetHost) {
		return fmt.Errorf("%w: probe safety check: target host %q is not public", errProbeSafetyBlock, targetHost)
	}
	return validateResolvedTarget(ctx, "probe safety check", targetHost)
}

func validateResolvedTarget(ctx context.Context, detailPrefix, targetHost string) error {
	resolver := defaultResolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := defaultDNSCache.lookup(ctx, resolver, targetHost, "tcp")
	if err != nil {
		return fmt.Errorf("%s: resolve target %q: %w", detailPrefix, targetHost, err)
	}
	for _, addr := range addrs {
		if netguard.UnsafeIP(addr.IP) {
			return fmt.Errorf("%w: %s: target %q resolves to non-public address %s", errProbeSafetyBlock, detailPrefix, targetHost, addr.IP)
		}
	}
	return nil
}

func classifyDNSError(err error) (kind, name, server string) {
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		return "", "", ""
	}
	name = dnsErr.Name
	server = dnsErr.Server
	switch {
	case dnsErr.IsNotFound:
		kind = "nxdomain"
	case dnsErr.IsTimeout:
		kind = "timeout"
	case dnsErr.IsTemporary || strings.Contains(strings.ToLower(dnsErr.Err), "server misbehaving"):
		kind = "servfail"
	default:
		kind = "resolver_error"
	}
	return kind, name, server
}

type bodyReadResult struct {
	Body          []byte
	Err           error
	Mode          string
	BytesRead     int64
	ExpectedBytes int64
	LimitBytes    int64
}

func readResponseBody(resp *http.Response, needKeyword bool, req Request) bodyReadResult {
	limit := maxBodyIntegrityBytes
	mode := "success_budgeted"
	if req.BodyReadMaxBytes > 0 {
		limit = req.BodyReadMaxBytes
	}
	if needKeyword {
		mode = "keyword"
		limit = maxKeywordBodyBytes
		if req.KeywordReadMaxBytes > 0 {
			limit = req.KeywordReadMaxBytes
		}
	} else if resp.ContentLength > limit && resp.ContentLength <= maxKeywordBodyBytes {
		limit = resp.ContentLength
	}
	if !needKeyword && resp.ContentLength >= 0 && resp.ContentLength <= limit {
		mode = "strict_finite"
	}

	stopBudget := startBodyReadBudget(resp.Body, responseBodyReadBudget(mode, needKeyword, req))

	if !needKeyword {
		prefix := &bodyPrefixCapture{limit: maxSemanticBodyPrefixBytes}
		n, err := io.Copy(io.MultiWriter(io.Discard, prefix), io.LimitReader(resp.Body, limit+1))
		if stopBodyReadBudget(stopBudget) {
			err = bodyReadBudgetError(err)
		}
		result := bodyReadResult{
			Body:          prefix.Bytes(),
			Err:           err,
			Mode:          mode,
			BytesRead:     n,
			ExpectedBytes: resp.ContentLength,
			LimitBytes:    limit,
		}
		if n > limit {
			result.BytesRead = limit
			return result
		}
		if err == nil && resp.ContentLength >= 0 && resp.ContentLength <= limit && n != resp.ContentLength {
			result.Err = io.ErrUnexpectedEOF
		}
		return result
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if stopBodyReadBudget(stopBudget) {
		err = bodyReadBudgetError(err)
	}
	result := bodyReadResult{
		Body:          body,
		Err:           err,
		Mode:          mode,
		BytesRead:     int64(len(body)),
		ExpectedBytes: resp.ContentLength,
		LimitBytes:    limit,
	}
	if err != nil {
		return result
	}
	if int64(len(body)) > limit {
		result.Body = body[:limit]
		result.BytesRead = int64(len(result.Body))
		return result
	}
	if resp.ContentLength >= 0 && resp.ContentLength <= limit && int64(len(body)) != resp.ContentLength {
		result.Err = io.ErrUnexpectedEOF
		return result
	}
	return result
}

type bodyPrefixCapture struct {
	buf   []byte
	limit int
}

func (c *bodyPrefixCapture) Write(p []byte) (int, error) {
	if c == nil || c.limit <= 0 {
		return len(p), nil
	}
	if len(c.buf) < c.limit {
		remaining := c.limit - len(c.buf)
		if len(p) < remaining {
			remaining = len(p)
		}
		c.buf = append(c.buf, p[:remaining]...)
	}
	return len(p), nil
}

func (c *bodyPrefixCapture) Bytes() []byte {
	if c == nil || len(c.buf) == 0 {
		return nil
	}
	out := make([]byte, len(c.buf))
	copy(out, c.buf)
	return out
}

func semanticBodyFailure(resp *http.Response, body []byte, bytesRead int64) (string, string) {
	if resp == nil || resp.StatusCode != http.StatusOK {
		return "", ""
	}

	bodyText := string(body)
	lowerBody := strings.ToLower(bodyText)
	strippedLowerBody := strings.ToLower(stripHTMLTags(bodyText))
	for _, pattern := range semanticFailurePatterns {
		if strings.Contains(lowerBody, pattern.needle) || strings.Contains(strippedLowerBody, pattern.needle) {
			return pattern.rule, pattern.detail
		}
	}
	if rule, detail := compoundSemanticBodyFailure(lowerBody); rule != "" {
		return rule, detail
	}

	if looksLikeHTML(resp.Header.Get("Content-Type"), lowerBody) && bodyLooksNearEmptyHTML(bodyText, bytesRead) {
		return "semantic_empty_html", "empty or near-empty HTML response body returned with HTTP 200"
	}

	return "", ""
}

func compoundSemanticBodyFailure(lowerBody string) (string, string) {
	if looksLikeWordPressPHPError(lowerBody) || looksLikeWordPressPHPError(strings.ToLower(stripHTMLTags(lowerBody))) {
		return "semantic_wp_php_error", "WordPress PHP error output returned with HTTP 200"
	}
	if strings.Contains(lowerBody, "hi jetpack!") && strings.Contains(lowerBody, "all systems go") {
		return "semantic_jetpack_probe_echo", "Jetpack probe response was served as the homepage with HTTP 200"
	}
	if looksLikeWordPressDatabaseCrash(lowerBody) {
		return "semantic_wp_db_table_crashed", "WordPress database table crash output returned with HTTP 200"
	}
	if looksLikeWordPressUnsupportedDatabase(lowerBody) {
		return "semantic_wp_unsupported_database", "WordPress unsupported database version page returned with HTTP 200"
	}
	if looksLikeWordPressRedisConnectionError(lowerBody) {
		return "semantic_wp_redis_connection", "WordPress Redis object-cache connection error returned with HTTP 200"
	}
	if strings.Contains(lowerBody, "apache2 ubuntu default page") && strings.Contains(lowerBody, "it works") {
		return "semantic_default_vhost_apache", "Apache default virtual-host page returned with HTTP 200"
	}
	if strings.Contains(lowerBody, "welcome to nginx!") && strings.Contains(lowerBody, "nginx web server is successfully installed") {
		return "semantic_default_vhost_nginx", "nginx default virtual-host page returned with HTTP 200"
	}
	if looksLikeAccountSuspendedPage(lowerBody) {
		return "semantic_host_account_suspended", "hosting account suspension page returned with HTTP 200"
	}
	if looksLikeWordPressDirectoryListing(lowerBody) {
		return "semantic_wp_directory_listing", "WordPress directory listing returned with HTTP 200"
	}
	return "", ""
}

func looksLikeWordPressPHPError(lowerBody string) bool {
	if !strings.Contains(lowerBody, " on line ") {
		return false
	}
	if !strings.Contains(lowerBody, "fatal error") &&
		!strings.Contains(lowerBody, "allowed memory size") &&
		!strings.Contains(lowerBody, "maximum execution time") &&
		!strings.Contains(lowerBody, "call to undefined function") &&
		!(strings.Contains(lowerBody, "parse error:") && strings.Contains(lowerBody, "syntax error")) {
		return false
	}
	return containsWordPressPathHint(lowerBody)
}

func looksLikeWordPressDatabaseCrash(lowerBody string) bool {
	return strings.Contains(lowerBody, "wordpress database error") &&
		strings.Contains(lowerBody, "marked as crashed") &&
		(strings.Contains(lowerBody, "should be repaired") || strings.Contains(lowerBody, "repair failed"))
}

func looksLikeWordPressUnsupportedDatabase(lowerBody string) bool {
	if !strings.Contains(lowerBody, "wordpress") || !strings.Contains(lowerBody, "requires") {
		return false
	}
	if !strings.Contains(lowerBody, "mysql") && !strings.Contains(lowerBody, "mariadb") {
		return false
	}
	return strings.Contains(lowerBody, "error:") ||
		strings.Contains(lowerBody, "you are running") ||
		strings.Contains(lowerBody, "your server is running")
}

func looksLikeWordPressRedisConnectionError(lowerBody string) bool {
	const phrase = "error establishing a redis connection"
	if !strings.Contains(lowerBody, phrase) &&
		!strings.Contains(lowerBody, "error establishing connection. to disable redis") {
		return false
	}
	stripped := strings.TrimSpace(strings.ToLower(stripHTMLTags(lowerBody)))
	if !strings.Contains(lowerBody, "<title>"+phrase) &&
		!strings.Contains(lowerBody, "<h1>"+phrase) &&
		!strings.HasPrefix(stripped, phrase) {
		return false
	}
	return strings.Contains(lowerBody, "wordpress is unable to establish a connection to redis") ||
		strings.Contains(lowerBody, "object-cache.php") ||
		strings.Contains(lowerBody, "/wp-content/") ||
		strings.Contains(lowerBody, "redis object cache") ||
		strings.Contains(lowerBody, "wp_redis")
}

func containsWordPressPathHint(lowerBody string) bool {
	for _, hint := range []string{
		"/wp-content/",
		"/wp-includes/",
		"/wp-admin/",
		"wp-config.php",
		"wp-settings.php",
		"wp-load.php",
		"wp-blog-header.php",
	} {
		if strings.Contains(lowerBody, hint) {
			return true
		}
	}
	return false
}

func looksLikeAccountSuspendedPage(lowerBody string) bool {
	if strings.Contains(lowerBody, "cgi-sys/suspendedpage.cgi") {
		return true
	}
	if !strings.Contains(lowerBody, "account has been suspended") {
		return false
	}
	return strings.Contains(lowerBody, "contact your hosting provider") ||
		strings.Contains(lowerBody, "contact your web host") ||
		strings.Contains(lowerBody, "<title>account suspended</title>") ||
		strings.Contains(lowerBody, "<title>this account has been suspended</title>")
}

func looksLikeWordPressDirectoryListing(lowerBody string) bool {
	if !strings.Contains(lowerBody, "index of /") {
		return false
	}
	return strings.Contains(lowerBody, "wp-admin") ||
		strings.Contains(lowerBody, "wp-content") ||
		strings.Contains(lowerBody, "wp-includes")
}

func looksLikeHTML(contentType, lowerBody string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if contentType == "text/html" || contentType == "application/xhtml+xml" {
		return true
	}
	trimmed := strings.TrimSpace(lowerBody)
	return strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html")
}

func bodyLooksNearEmptyHTML(bodyText string, bytesRead int64) bool {
	if bytesRead == 0 {
		return true
	}
	if bytesRead > 256 {
		return false
	}
	return stripHTMLTags(bodyText) == ""
}

func stripHTMLTags(bodyText string) string {
	var b strings.Builder
	inTag := false
	for _, r := range bodyText {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				b.WriteRune(r)
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func responseBodyReadBudget(mode string, needKeyword bool, req Request) time.Duration {
	if needKeyword {
		if req.KeywordReadMaxMS <= 0 {
			return 0
		}
		return time.Duration(req.KeywordReadMaxMS) * time.Millisecond
	}
	if mode == "strict_finite" || req.BodyReadMaxMS <= 0 {
		return 0
	}
	return time.Duration(req.BodyReadMaxMS) * time.Millisecond
}

func startBodyReadBudget(body io.Closer, budget time.Duration) func() bool {
	if body == nil || budget <= 0 {
		return nil
	}
	var timedOut atomic.Bool
	timer := time.AfterFunc(budget, func() {
		timedOut.Store(true)
		_ = body.Close()
	})
	return func() bool {
		timer.Stop()
		return timedOut.Load()
	}
}

func stopBodyReadBudget(stop func() bool) bool {
	if stop == nil {
		return false
	}
	return stop()
}

func bodyReadBudgetError(err error) error {
	if err == nil {
		return errBodyReadBudgetExceeded
	}
	return fmt.Errorf("%w: %v", errBodyReadBudgetExceeded, err)
}

func collectForbiddenKeywords(single *string, many []string) []string {
	out := make([]string, 0, 1+len(many))
	if single != nil && *single != "" {
		out = append(out, *single)
	}
	for _, keyword := range many {
		if keyword != "" {
			out = append(out, keyword)
		}
	}
	return out
}

// ParseCustomHeaders deserialises a JSON custom headers string into a map.
func ParseCustomHeaders(raw *string) map[string]string {
	if raw == nil || *raw == "" {
		return nil
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(*raw), &m)
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if netguard.ValidOutboundHeader(k, v) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ParseForbiddenKeywords deserialises a JSON array of body strings that must
// not appear in the response.
func ParseForbiddenKeywords(raw *string) []string {
	if raw == nil || *raw == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(*raw), &values); err != nil {
		return nil
	}
	out := values[:0]
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

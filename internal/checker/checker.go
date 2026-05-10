package checker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
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
	ErrorBodyTruncated = ErrorBodyRead
)

const (
	maxBodyIntegrityBytes      int64 = 64 << 10
	maxKeywordBodyBytes        int64 = 1 << 20
	checkDNSCacheTTL                 = 15 * time.Minute
	checkDNSCacheMaxEntries          = 2000000
	checkDNSCachePurgeInterval       = 10000
)

// RedirectPolicy controls how redirect responses are handled.
type RedirectPolicy string

const (
	RedirectFollow RedirectPolicy = "follow"
	RedirectAlert  RedirectPolicy = "alert"
	RedirectFail   RedirectPolicy = "fail"
)

// defaultTransport is shared across checks so the checker does not allocate a
// fresh connection pool for every probe. The http.Client stays per request so
// timeout and redirect policy remain isolated to that site check.
var defaultTransport = newCheckTransport()
var defaultDNSCache = newCheckDNSCache(checkDNSCacheTTL, checkDNSCacheMaxEntries)
var defaultDNSLookupLimiter = newCheckDNSLookupLimiter()
var configuredResolverMu sync.RWMutex
var configuredResolverServers []string

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

	newTransport := newCheckTransport()

	configuredResolverMu.Lock()
	oldTransport := defaultTransport
	defaultTransport = newTransport
	defaultDNSCache = newCheckDNSCache(checkDNSCacheTTL, checkDNSCacheMaxEntries)
	configuredResolverMu.Unlock()

	if oldTransport != nil {
		oldTransport.CloseIdleConnections()
	}
	return nil
}

type checkDNSCache struct {
	mu         sync.RWMutex
	ttl        time.Duration
	maxEntries int
	writes     int
	entries    map[string]checkDNSCacheEntry
}

type checkDNSCacheEntry struct {
	addrs   []net.IPAddr
	expires time.Time
}

func newCheckDNSCache(ttl time.Duration, maxEntries int) *checkDNSCache {
	return &checkDNSCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    make(map[string]checkDNSCacheEntry),
	}
}

func (c *checkDNSCache) lookup(ctx context.Context, resolver *net.Resolver, host, network string) ([]net.IPAddr, error) {
	if resolver == nil {
		return nil, fmt.Errorf("lookup %s: resolver unavailable", host)
	}
	if c == nil || c.ttl <= 0 {
		return lookupResolverIPAddrs(ctx, resolver, host, network)
	}
	key := normalizeDNSCacheKey(host, preferredLookupFamily(network))
	now := time.Now()
	c.mu.RLock()
	entry, ok := c.entries[key]
	if ok && now.Before(entry.expires) {
		addrs := cloneIPAddrs(entry.addrs)
		c.mu.RUnlock()
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
		addrs := cloneIPAddrs(entry.addrs)
		c.mu.RUnlock()
		return addrs, nil
	}
	c.mu.RUnlock()

	addrs, err := lookupResolverIPAddrs(ctx, resolver, host, network)
	if err != nil || len(addrs) == 0 {
		return addrs, err
	}
	c.store(key, addrs, now.Add(c.ttl))
	return addrs, nil
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
	return &http.Transport{
		DialContext: newCheckDialContext(newCheckResolver()),
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
			// Deprecated TLS versions are still site-reachability signals.
			// Complete the handshake so the orchestrator can open an advisory
			// tls_deprecated event instead of reporting customer downtime.
			MinVersion: tls.VersionTLS10,
		},
		TLSHandshakeTimeout: 10 * time.Second,
		// Jetmon checks huge fleets of mostly-unique hostnames on minute-scale
		// cadences. Connections would usually expire before reuse, while the
		// shared idle pool becomes a global lock and goroutine-pressure point at
		// high concurrency.
		DisableKeepAlives: true,
	}
}

func newCheckDialContext(resolver *net.Resolver) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	if resolver == nil {
		return dialer.DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if ip := net.ParseIP(host); ip != nil {
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
	BlogID              int64
	URL                 string
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
}

// Result holds the outcome of a single HTTP check.
type Result struct {
	BlogID    int64
	URL       string
	Method    string
	Success   bool
	HTTPCode  int
	ErrorCode int
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

// Check performs an HTTP check and returns the result.
func Check(ctx context.Context, req Request) Result {
	res := Result{
		BlogID:    req.BlogID,
		URL:       req.URL,
		Method:    http.MethodGet,
		Timestamp: time.Now(),
	}

	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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
	redirectPolicyStr := string(req.RedirectPolicy)
	if redirectPolicyStr == "" {
		redirectPolicyStr = string(RedirectFollow)
	}

	client := &http.Client{
		Transport: defaultTransport,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			redirectChain = append(redirectChain, r.URL.String())
			if redirectPolicyStr == string(RedirectFail) {
				return fmt.Errorf("redirect policy: fail")
			}
			if len(redirectChain) > 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
		Timeout: timeout,
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		res.ErrorCode = ErrorConnect
		res.ErrorDetail = boundedErrorDetail(err)
		return res
	}

	httpReq.Header.Set("User-Agent", "jetmon/2.0 (Jetpack Site Uptime Monitor by WordPress.com)")
	for k, v := range headers {
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
		res.FinalURL = resp.Request.URL.String()
	}

	// Only record a phase duration when BOTH start and end fired. If a
	// connection errors mid-handshake the DNSStart / ConnectStart / TLS
	// HandshakeStart hook fires without its matching Done — in that case
	// the *End is the zero time.Time and *End.Sub(*Start) returns a huge
	// negative duration (roughly -unix-nanos), which then overflows the
	// jetmon_check_history INT columns and surfaces as
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
		// Flag deprecated TLS versions (TLS 1.0 = 0x0301, TLS 1.1 = 0x0302).
		if resp.TLS.Version <= tls.VersionTLS11 {
			res.ErrorCode = ErrorTLSDeprecated
		}
	}

	if redirectPolicyStr == string(RedirectAlert) && res.RedirectCount > 0 {
		res.RedirectChanged = true
	}

	forbiddenKeywords := collectForbiddenKeywords(req.ForbiddenKeyword, req.ForbiddenKeywords)
	needsBody := (req.Keyword != nil && *req.Keyword != "") || len(forbiddenKeywords) > 0
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
		res.ErrorCode = ErrorBodyRead
		res.ErrorDetail = res.BodyReadError
		return res
	}

	if needsBody {
		// Keyword check uses the same bounded body read as integrity checks.
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

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
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
	return m
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

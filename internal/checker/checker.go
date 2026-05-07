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
	"strings"
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
	maxBodyIntegrityBytes int64 = 64 << 10
	maxKeywordBodyBytes   int64 = 1 << 20
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

func newCheckTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
			// Deprecated TLS versions are still site-reachability signals.
			// Complete the handshake so the orchestrator can open an advisory
			// tls_deprecated event instead of reporting customer downtime.
			MinVersion: tls.VersionTLS10,
		},
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     30 * time.Second,
		MaxIdleConns:        1024,
		MaxIdleConnsPerHost: 8,
	}
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

	SSLExpiry        *time.Time
	TLSVersion       uint16
	CipherSuite      uint16
	DNSFailureKind   string
	DNSFailureName   string
	DNSFailureServer string
	RedirectChanged  bool
	RedirectCount    int
	RedirectChain    []string
	FinalURL         string
	KeywordRule      string

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

	headers := make(map[string]string)
	for k, v := range req.CustomHeaders {
		headers[k] = v
	}

	redirectChain := []string{}
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
	body, bodyErr := readResponseBody(resp, needsBody, req)
	if bodyErr != nil && res.HTTPCode < http.StatusBadRequest {
		res.ErrorCode = ErrorBodyRead
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

func readResponseBody(resp *http.Response, needKeyword bool, req Request) ([]byte, error) {
	limit := maxBodyIntegrityBytes
	if req.BodyReadMaxBytes > 0 {
		limit = req.BodyReadMaxBytes
	}
	if needKeyword {
		limit = maxKeywordBodyBytes
		if req.KeywordReadMaxBytes > 0 {
			limit = req.KeywordReadMaxBytes
		}
	} else if resp.ContentLength > limit && resp.ContentLength <= maxKeywordBodyBytes {
		limit = resp.ContentLength
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return body, err
	}
	if int64(len(body)) > limit {
		return body[:limit], nil
	}
	if resp.ContentLength >= 0 && resp.ContentLength <= limit && int64(len(body)) != resp.ContentLength {
		return body, io.ErrUnexpectedEOF
	}
	return body, nil
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

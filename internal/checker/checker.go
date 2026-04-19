package checker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"
)

// ErrorCode mirrors the status change email types from the original Jetmon.
const (
	ErrorNone        = 0
	ErrorTimeout     = 1
	ErrorConnect     = 2
	ErrorSSL         = 3
	ErrorRedirect    = 4
	ErrorKeyword     = 5
	ErrorTLSExpired  = 6
	ErrorTLSDeprecated = 7
)

// RedirectPolicy controls how redirect responses are handled.
type RedirectPolicy string

const (
	RedirectFollow RedirectPolicy = "follow"
	RedirectAlert  RedirectPolicy = "alert"
	RedirectFail   RedirectPolicy = "fail"
)

// Request holds the parameters for a single HTTP check.
type Request struct {
	BlogID         int64
	URL            string
	TimeoutSeconds int
	Keyword        *string
	CustomHeaders  map[string]string
	RedirectPolicy RedirectPolicy
}

// Result holds the outcome of a single HTTP check.
type Result struct {
	BlogID    int64
	URL       string
	Success   bool
	HTTPCode  int
	ErrorCode int

	RTT  time.Duration
	DNS  time.Duration
	TCP  time.Duration
	TLS  time.Duration
	TTFB time.Duration

	SSLExpiry      *time.Time
	TLSVersion     uint16
	RedirectChanged bool

	Timestamp time.Time
}

// StatusType maps the result to a WPCOM status change email type.
func (r *Result) StatusType() string {
	switch {
	case r.Success:
		return "success"
	case r.ErrorCode == ErrorSSL || r.ErrorCode == ErrorTLSExpired:
		return "https"
	case r.ErrorCode == ErrorTimeout:
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

// Check performs an HTTP check and returns the result.
func Check(ctx context.Context, req Request) Result {
	res := Result{
		BlogID:    req.BlogID,
		URL:       req.URL,
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
		dnsEnd, tcpEnd, tlsEnd               time.Time
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

	redirectCount := 0
	redirectPolicyStr := string(req.RedirectPolicy)
	if redirectPolicyStr == "" {
		redirectPolicyStr = string(RedirectFollow)
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}

	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			redirectCount++
			if redirectPolicyStr == string(RedirectFail) {
				return fmt.Errorf("redirect policy: fail")
			}
			if redirectCount > 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
		Timeout: timeout,
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		res.ErrorCode = ErrorConnect
		return res
	}

	httpReq.Header.Set("User-Agent", "jetmon/2.0 (Jetpack Site Uptime Monitor by WordPress.com)")
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := client.Do(httpReq)
	res.RTT = time.Since(start)

	if !dnsStart.IsZero() {
		res.DNS = dnsEnd.Sub(dnsStart)
	}
	if !tcpStart.IsZero() {
		res.TCP = tcpEnd.Sub(tcpStart)
	}
	if !tlsStart.IsZero() {
		res.TLS = tlsEnd.Sub(tlsStart)
	}

	if err != nil {
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

	if redirectPolicyStr == string(RedirectAlert) && redirectCount > 0 {
		res.RedirectChanged = true
	}

	// Keyword check — read body only if keyword is configured.
	if req.Keyword != nil && *req.Keyword != "" {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if !strings.Contains(string(body), *req.Keyword) {
			res.ErrorCode = ErrorKeyword
			return res
		}
	}

	res.Success = res.HTTPCode > 0 && res.HTTPCode < 400
	return res
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

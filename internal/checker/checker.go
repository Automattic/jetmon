package checker

import (
	"bytes"
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
	ErrorBodyTruncated = 8
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
	BlogID           int64
	URL              string
	TimeoutSeconds   int
	BodyReadMaxBytes int64
	BodyReadMaxMS    int
	Keyword          *string
	CustomHeaders    map[string]string
	RedirectPolicy   RedirectPolicy
}

const (
	defaultBodyReadMaxBytes int64 = 256 * 1024
	defaultBodyReadMaxMS          = 250
)

var bodyReadCounters = struct {
	strictEOFSuccess    atomic.Uint64
	strictEOFTruncated  atomic.Uint64
	strictEOFTimeout    atomic.Uint64
	budgetBytesExceeded atomic.Uint64
	budgetTimeExceeded  atomic.Uint64
	budgetEOFSuccess    atomic.Uint64
	budgetTruncated     atomic.Uint64
	skippedStatusCode   atomic.Uint64
	skippedUpgradeOr101 atomic.Uint64
	skippedSSE          atomic.Uint64
}{}

// BodyReadCounterSnapshot exposes additive internal body-read outcomes.
type BodyReadCounterSnapshot struct {
	StrictEOFSuccess    uint64
	StrictEOFTruncated  uint64
	StrictEOFTimeout    uint64
	BudgetBytesExceeded uint64
	BudgetTimeExceeded  uint64
	BudgetEOFSuccess    uint64
	BudgetTruncated     uint64
	SkippedStatusCode   uint64
	SkippedUpgradeOr101 uint64
	SkippedSSE          uint64
}

// BodyReadCounters returns current body-read outcome counters.
func BodyReadCounters() BodyReadCounterSnapshot {
	return BodyReadCounterSnapshot{
		StrictEOFSuccess:    bodyReadCounters.strictEOFSuccess.Load(),
		StrictEOFTruncated:  bodyReadCounters.strictEOFTruncated.Load(),
		StrictEOFTimeout:    bodyReadCounters.strictEOFTimeout.Load(),
		BudgetBytesExceeded: bodyReadCounters.budgetBytesExceeded.Load(),
		BudgetTimeExceeded:  bodyReadCounters.budgetTimeExceeded.Load(),
		BudgetEOFSuccess:    bodyReadCounters.budgetEOFSuccess.Load(),
		BudgetTruncated:     bodyReadCounters.budgetTruncated.Load(),
		SkippedStatusCode:   bodyReadCounters.skippedStatusCode.Load(),
		SkippedUpgradeOr101: bodyReadCounters.skippedUpgradeOr101.Load(),
		SkippedSSE:          bodyReadCounters.skippedSSE.Load(),
	}
}

type bodyReadPolicy int

const (
	bodyPolicySkip bodyReadPolicy = iota
	bodyPolicyStrictEOF
	bodyPolicyBudgeted
)

type bodyReadOutcome int

const (
	bodyReadEOF bodyReadOutcome = iota
	bodyReadBudgetBytesExceeded
	bodyReadBudgetTimeExceeded
	bodyReadTruncated
)

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

	SSLExpiry       *time.Time
	TLSVersion      uint16
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

	if res.HTTPCode >= 400 {
		bodyReadCounters.skippedStatusCode.Add(1)
		res.Success = false
		return res
	}

	matchedKeyword, bodyErr := validateBody(resp, req)
	if bodyErr != nil {
		if isTimeoutError(ctx, bodyErr) {
			res.ErrorCode = ErrorTimeout
		} else if res.HTTPCode > 0 && res.HTTPCode < 400 {
			res.ErrorCode = ErrorBodyTruncated
		}
		return res
	}

	if req.Keyword != nil && *req.Keyword != "" && !matchedKeyword {
		res.ErrorCode = ErrorKeyword
		return res
	}

	res.Success = res.HTTPCode > 0 && res.HTTPCode < 400
	return res
}

func validateBodyToEOF(body io.Reader, keyword *string) (bool, error) {
	if keyword == nil || *keyword == "" {
		_, err := io.Copy(io.Discard, body)
		return true, err
	}

	needle := []byte(*keyword)
	if len(needle) == 0 {
		_, err := io.Copy(io.Discard, body)
		return true, err
	}

	buf := make([]byte, 32*1024)
	carryLimit := len(needle) - 1
	if carryLimit < 0 {
		carryLimit = 0
	}
	carry := make([]byte, 0, carryLimit)
	found := false

	for {
		n, err := body.Read(buf)
		if n > 0 {
			window := append(carry, buf[:n]...)
			if !found && bytes.Contains(window, needle) {
				found = true
			}

			if carryLimit > 0 {
				if len(window) > carryLimit {
					carry = append(carry[:0], window[len(window)-carryLimit:]...)
				} else {
					carry = append(carry[:0], window...)
				}
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return found, nil
			}
			return found, err
		}
	}
}

func validateBody(resp *http.Response, req Request) (bool, error) {
	maxBytes := req.BodyReadMaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultBodyReadMaxBytes
	}
	maxDuration := time.Duration(req.BodyReadMaxMS) * time.Millisecond
	if maxDuration <= 0 {
		maxDuration = time.Duration(defaultBodyReadMaxMS) * time.Millisecond
	}

	policy := selectBodyReadPolicy(resp, maxBytes)
	switch policy {
	case bodyPolicySkip:
		return scanKeywordBudgeted(resp.Body, req.Keyword, maxBytes, maxDuration)
	case bodyPolicyStrictEOF:
		matched, outcome, err := scanBodyWithBudget(resp.Body, req.Keyword, 0, maxDuration)
		switch outcome {
		case bodyReadEOF:
			bodyReadCounters.strictEOFSuccess.Add(1)
			return matched, nil
		case bodyReadBudgetTimeExceeded:
			bodyReadCounters.strictEOFTimeout.Add(1)
			return matched, context.DeadlineExceeded
		case bodyReadTruncated:
			bodyReadCounters.strictEOFTruncated.Add(1)
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return matched, err
		default:
			return matched, err
		}
	case bodyPolicyBudgeted:
		matched, outcome, err := scanBodyWithBudget(resp.Body, req.Keyword, maxBytes, maxDuration)
		switch outcome {
		case bodyReadEOF:
			bodyReadCounters.budgetEOFSuccess.Add(1)
			return matched, nil
		case bodyReadBudgetBytesExceeded:
			bodyReadCounters.budgetBytesExceeded.Add(1)
			return matched, nil
		case bodyReadBudgetTimeExceeded:
			bodyReadCounters.budgetTimeExceeded.Add(1)
			return matched, nil
		case bodyReadTruncated:
			bodyReadCounters.budgetTruncated.Add(1)
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return matched, err
		default:
			return matched, err
		}
	default:
		return validateBodyToEOF(resp.Body, req.Keyword)
	}
}

func selectBodyReadPolicy(resp *http.Response, maxBytes int64) bodyReadPolicy {
	if resp.StatusCode == http.StatusSwitchingProtocols || isUpgradeResponse(resp) {
		bodyReadCounters.skippedUpgradeOr101.Add(1)
		return bodyPolicySkip
	}
	if isSSE(resp) {
		bodyReadCounters.skippedSSE.Add(1)
		return bodyPolicySkip
	}
	if resp.ContentLength >= 0 && resp.ContentLength <= maxBytes {
		return bodyPolicyStrictEOF
	}
	return bodyPolicyBudgeted
}

func isUpgradeResponse(resp *http.Response) bool {
	connHdr := strings.ToLower(resp.Header.Get("Connection"))
	if strings.Contains(connHdr, "upgrade") {
		return true
	}
	return strings.TrimSpace(resp.Header.Get("Upgrade")) != ""
}

func isSSE(resp *http.Response) bool {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.HasPrefix(ct, "text/event-stream")
}

func scanKeywordBudgeted(body io.Reader, keyword *string, maxBytes int64, maxDuration time.Duration) (bool, error) {
	matched, _, _ := scanBodyWithBudget(body, keyword, maxBytes, maxDuration)
	return matched, nil
}

func scanBodyWithBudget(body io.Reader, keyword *string, maxBytes int64, maxDuration time.Duration) (bool, bodyReadOutcome, error) {
	needle := []byte("")
	if keyword != nil {
		needle = []byte(*keyword)
	}

	buf := make([]byte, 32*1024)
	carryLimit := len(needle) - 1
	if carryLimit < 0 {
		carryLimit = 0
	}
	carry := make([]byte, 0, carryLimit)
	found := len(needle) == 0
	var readBytes int64
	start := time.Now()

	for {
		if maxDuration > 0 && time.Since(start) > maxDuration {
			return found, bodyReadBudgetTimeExceeded, context.DeadlineExceeded
		}
		n, err := body.Read(buf)
		if n > 0 {
			readBytes += int64(n)
			window := append(carry, buf[:n]...)
			if !found && len(needle) > 0 && bytes.Contains(window, needle) {
				found = true
			}

			if carryLimit > 0 {
				if len(window) > carryLimit {
					carry = append(carry[:0], window[len(window)-carryLimit:]...)
				} else {
					carry = append(carry[:0], window...)
				}
			}

			if maxBytes > 0 && readBytes > maxBytes {
				return found, bodyReadBudgetBytesExceeded, nil
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return found, bodyReadEOF, nil
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return found, bodyReadTruncated, err
			}
			if strings.Contains(strings.ToLower(err.Error()), "truncated") {
				return found, bodyReadTruncated, err
			}
			return found, bodyReadTruncated, err
		}
	}
}

func isTimeoutError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
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

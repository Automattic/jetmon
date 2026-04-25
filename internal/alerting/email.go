package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// EmailMessage is the rendered email handed to a Sender. It's
// transport-agnostic — the Sender translates it into whatever the
// underlying channel needs (HTTP POST body for WPCOM, MIME for SMTP,
// log line for stub).
type EmailMessage struct {
	From      string
	To        string
	Subject   string
	PlainBody string
	HTMLBody  string
}

// Sender abstracts the actual email-sending mechanism. Concrete impls
// in this file: WPCOMSender (production), SMTPSender (dev / staging),
// StubSender (unit tests).
//
// Send returns an error if the email could not be delivered. The
// returned error string is recorded in jetmon_alert_deliveries for
// debugging — keep it short and useful, not a stack trace.
type Sender interface {
	Send(ctx context.Context, msg EmailMessage) error
}

// emailDispatcher implements alerting.Dispatcher by translating a
// Notification into an EmailMessage and delegating to a Sender. The
// rendering lives here (not in the Sender) so swapping transports
// doesn't require re-implementing the subject/body logic.
type emailDispatcher struct {
	sender Sender
	from   string
}

// NewEmailDispatcher returns a Dispatcher that renders Notifications
// into emails and delivers them via the given Sender. The from address
// becomes the EmailMessage.From for every dispatched message.
func NewEmailDispatcher(sender Sender, from string) Dispatcher {
	return &emailDispatcher{sender: sender, from: from}
}

// emailDestination is the contact's destination JSON shape for email.
type emailDestination struct {
	Address string `json:"address"`
}

// Send renders the Notification into an EmailMessage and hands it to
// the configured Sender. Returns SMTP-style status codes for symmetry
// with the HTTP-based transports: 250 on success, 5xx on failure.
func (d *emailDispatcher) Send(ctx context.Context, destination json.RawMessage, n Notification) (int, string, error) {
	var dest emailDestination
	if err := json.Unmarshal(destination, &dest); err != nil {
		return 550, "invalid destination JSON", fmt.Errorf("parse email destination: %w", err)
	}
	if dest.Address == "" {
		return 550, "destination missing address", errors.New("alerting/email: destination missing address")
	}

	msg := EmailMessage{
		From:      d.from,
		To:        dest.Address,
		Subject:   renderEmailSubject(n),
		PlainBody: renderEmailPlain(n),
		HTMLBody:  renderEmailHTML(n),
	}

	if err := d.sender.Send(ctx, msg); err != nil {
		// Cap the error message at last_response's column width.
		summary := err.Error()
		if len(summary) > 2048 {
			summary = summary[:2048]
		}
		return 554, summary, err
	}
	return 250, "delivered", nil
}

// renderEmailSubject is short enough to fit in mobile notification
// previews. Severity name and site URL are the most-relevant info at
// a glance; recovery and test prefixes are explicit.
func renderEmailSubject(n Notification) string {
	switch {
	case n.IsTest:
		return fmt.Sprintf("[Jetmon test] %s", n.SiteURL)
	case n.Recovery:
		return fmt.Sprintf("[Recovered] %s", n.SiteURL)
	default:
		return fmt.Sprintf("[%s] %s", n.SeverityName, n.SiteURL)
	}
}

// renderEmailPlain is the plain-text body. Same fields as the HTML
// version; consumers receiving multipart see whichever their client
// prefers. The plain body is also the fallback for email clients
// that strip HTML.
func renderEmailPlain(n Notification) string {
	var b strings.Builder
	if n.IsTest {
		b.WriteString("*** Jetmon test notification ***\n\n")
	}
	if n.Recovery {
		b.WriteString("Recovery: site is back to Up.\n\n")
	}
	fmt.Fprintf(&b, "Site: %s (id %d)\n", n.SiteURL, n.SiteID)
	fmt.Fprintf(&b, "Severity: %s\n", n.SeverityName)
	if n.State != "" {
		fmt.Fprintf(&b, "State: %s\n", n.State)
	}
	fmt.Fprintf(&b, "Event: #%d (%s)\n", n.EventID, n.EventType)
	if n.Reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n", n.Reason)
	}
	fmt.Fprintf(&b, "Time: %s\n", n.Timestamp.UTC().Format(time.RFC3339))
	return b.String()
}

// renderEmailHTML mirrors the plain body in a minimal HTML wrapper.
// No external CSS or images — keeps the payload small and renders
// the same in every client.
func renderEmailHTML(n Notification) string {
	var b strings.Builder
	b.WriteString("<html><body style=\"font-family:sans-serif;\">")
	if n.IsTest {
		b.WriteString("<p><strong>*** Jetmon test notification ***</strong></p>")
	}
	if n.Recovery {
		b.WriteString("<p><strong>Recovery:</strong> site is back to Up.</p>")
	}
	b.WriteString("<table cellpadding=\"4\">")
	fmt.Fprintf(&b, "<tr><td><strong>Site</strong></td><td>%s (id %d)</td></tr>", htmlEscape(n.SiteURL), n.SiteID)
	fmt.Fprintf(&b, "<tr><td><strong>Severity</strong></td><td>%s</td></tr>", htmlEscape(n.SeverityName))
	if n.State != "" {
		fmt.Fprintf(&b, "<tr><td><strong>State</strong></td><td>%s</td></tr>", htmlEscape(n.State))
	}
	fmt.Fprintf(&b, "<tr><td><strong>Event</strong></td><td>#%d (%s)</td></tr>", n.EventID, htmlEscape(n.EventType))
	if n.Reason != "" {
		fmt.Fprintf(&b, "<tr><td><strong>Reason</strong></td><td>%s</td></tr>", htmlEscape(n.Reason))
	}
	fmt.Fprintf(&b, "<tr><td><strong>Time</strong></td><td>%s</td></tr>", n.Timestamp.UTC().Format(time.RFC3339))
	b.WriteString("</table></body></html>")
	return b.String()
}

func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// StubSender records every message in memory and (by default) also
// logs a one-line summary to stdout. Used by unit tests and by
// EMAIL_TRANSPORT="stub" in environments where a real send is not
// configured. Never returns an error.
type StubSender struct {
	Logger func(EmailMessage) // optional; defaults to log.Printf

	mu   sync.Mutex
	sent []EmailMessage
}

// Send records the message and (optionally) logs a summary.
func (s *StubSender) Send(_ context.Context, m EmailMessage) error {
	s.mu.Lock()
	s.sent = append(s.sent, m)
	s.mu.Unlock()
	if s.Logger != nil {
		s.Logger(m)
	} else {
		log.Printf("alerting/email: stub send From=%s To=%s Subject=%q", m.From, m.To, m.Subject)
	}
	return nil
}

// Sent returns a snapshot of every message recorded so far. Used by
// tests to assert against rendered output.
func (s *StubSender) Sent() []EmailMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]EmailMessage, len(s.sent))
	copy(out, s.sent)
	return out
}

// Reset clears the sent buffer. Useful between test cases.
func (s *StubSender) Reset() {
	s.mu.Lock()
	s.sent = nil
	s.mu.Unlock()
}

// SMTPSender connects to an SMTP server and sends multipart emails.
// Uses Go's stdlib net/smtp; doesn't take a per-call context (smtp
// package predates context). The worker bounds runtime via its own
// timeouts; an SMTP send that hangs blocks the worker goroutine until
// the underlying socket times out (typically 5–10 minutes on Linux).
//
// For dev/staging only — production uses WPCOMSender. STARTTLS is
// optional; AUTH PLAIN is used when Username is non-empty.
type SMTPSender struct {
	Host     string
	Port     int
	Username string // optional; if empty, no AUTH is performed
	Password string
	UseTLS   bool // controls whether AUTH PLAIN is sent (auth on plaintext SMTP is rejected by net/smtp without UseTLS)
}

// Send delivers msg via SMTP. The MIME body is multipart/alternative
// with both plain and HTML parts.
func (s *SMTPSender) Send(_ context.Context, m EmailMessage) error {
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	body := buildMIMEMessage(m)
	var auth smtp.Auth
	if s.Username != "" && s.UseTLS {
		auth = smtp.PlainAuth("", s.Username, s.Password, s.Host)
	}
	if err := smtp.SendMail(addr, auth, m.From, []string{m.To}, []byte(body)); err != nil {
		return fmt.Errorf("alerting/email/smtp: send to %s: %w", addr, err)
	}
	return nil
}

// buildMIMEMessage produces a multipart/alternative MIME body with
// both plain-text and HTML parts. Boundary is fixed; the message is
// short and self-contained, so collisions are not a concern.
func buildMIMEMessage(m EmailMessage) string {
	const boundary = "JetmonAlertBoundary_4d8f31a2"
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", m.From)
	fmt.Fprintf(&b, "To: %s\r\n", m.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", m.Subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n\r\n")
	b.WriteString(m.PlainBody)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n\r\n")
	b.WriteString(m.HTMLBody)
	b.WriteString("\r\n")

	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.String()
}

// WPCOMSender posts to a WPCOM-owned email API endpoint with a Bearer
// token. Same shape as the existing internal/wpcom client — Bearer
// auth, JSON body, 4xx/5xx → error. Body shape is intentionally
// generic; the production endpoint can adapt or we wrap the body in
// whatever shape they require.
type WPCOMSender struct {
	Endpoint   string
	AuthToken  string
	HTTPClient *http.Client // if nil, a default with a 10s timeout is used
}

// wpcomEmailRequest is the JSON body posted to the WPCOM email API.
type wpcomEmailRequest struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	PlainBody string `json:"plain"`
	HTMLBody  string `json:"html"`
}

// Send POSTs the message to the configured endpoint.
func (s *WPCOMSender) Send(ctx context.Context, m EmailMessage) error {
	if s.Endpoint == "" {
		return errors.New("alerting/email/wpcom: endpoint not configured")
	}
	body, err := json.Marshal(wpcomEmailRequest{
		From: m.From, To: m.To, Subject: m.Subject,
		PlainBody: m.PlainBody, HTMLBody: m.HTMLBody,
	})
	if err != nil {
		return fmt.Errorf("alerting/email/wpcom: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("alerting/email/wpcom: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.AuthToken)
	}

	client := s.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("alerting/email/wpcom: post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("alerting/email/wpcom: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

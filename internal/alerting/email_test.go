package alerting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/eventstore"
)

func makeTestNotification() Notification {
	return Notification{
		SiteID:       42,
		SiteURL:      "https://example.com",
		EventID:      777,
		EventType:    "event.opened",
		Severity:     eventstore.SeverityDown,
		SeverityName: "Down",
		State:        "Down",
		Reason:       "verifier_confirmed",
		Timestamp:    time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC),
	}
}

func TestRenderEmailSubjectVariants(t *testing.T) {
	cases := []struct {
		mutate func(*Notification)
		want   string
	}{
		{func(n *Notification) {}, "[Down] https://example.com"},
		{func(n *Notification) { n.Recovery = true }, "[Recovered] https://example.com"},
		{func(n *Notification) { n.IsTest = true }, "[Jetmon test] https://example.com"},
	}
	for i, tc := range cases {
		n := makeTestNotification()
		tc.mutate(&n)
		got := renderEmailSubject(n)
		if got != tc.want {
			t.Errorf("case %d: got %q, want %q", i, got, tc.want)
		}
	}
}

func TestRenderEmailPlainContainsKeyFields(t *testing.T) {
	n := makeTestNotification()
	body := renderEmailPlain(n)
	for _, want := range []string{
		"https://example.com",
		"id 42",
		"Down",
		"#777",
		"event.opened",
		"verifier_confirmed",
		"2026-04-25T12:00:00Z",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plain body missing %q\nbody:\n%s", want, body)
		}
	}
}

func TestRenderEmailHTMLEscapesUntrustedFields(t *testing.T) {
	n := makeTestNotification()
	n.SiteURL = `<script>alert("x")</script>`
	n.Reason = `a & b`
	body := renderEmailHTML(n)
	// The raw script tag must not appear.
	if strings.Contains(body, "<script>") {
		t.Errorf("HTML body contains unescaped <script> tag:\n%s", body)
	}
	// The escaped form must appear.
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("HTML body missing escaped <script>:\n%s", body)
	}
	if !strings.Contains(body, "a &amp; b") {
		t.Errorf("HTML body did not escape ampersand:\n%s", body)
	}
}

func TestRenderEmailRecoveryBannerPresent(t *testing.T) {
	n := makeTestNotification()
	n.Recovery = true
	if !strings.Contains(renderEmailPlain(n), "Recovery") {
		t.Error("plain body missing recovery banner")
	}
	if !strings.Contains(renderEmailHTML(n), "Recovery") {
		t.Error("HTML body missing recovery banner")
	}
}

func TestRenderEmailTestBannerPresent(t *testing.T) {
	n := makeTestNotification()
	n.IsTest = true
	if !strings.Contains(renderEmailPlain(n), "test notification") {
		t.Error("plain body missing test banner")
	}
	if !strings.Contains(renderEmailHTML(n), "test notification") {
		t.Error("HTML body missing test banner")
	}
}

// TestEmailDispatcherDelegatesToSender verifies the destination is parsed
// correctly and the rendered fields land in the EmailMessage.
func TestEmailDispatcherDelegatesToSender(t *testing.T) {
	stub := &StubSender{Logger: func(EmailMessage) {}} // silence test output
	d := NewEmailDispatcher(stub, "from@example.com")

	dest := json.RawMessage(`{"address":"ops@example.com"}`)
	n := makeTestNotification()

	status, _, err := d.Send(context.Background(), dest, n)
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if status != 250 {
		t.Errorf("status = %d, want 250", status)
	}

	sent := stub.Sent()
	if len(sent) != 1 {
		t.Fatalf("got %d messages, want 1", len(sent))
	}
	m := sent[0]
	if m.From != "from@example.com" {
		t.Errorf("From = %q", m.From)
	}
	if m.To != "ops@example.com" {
		t.Errorf("To = %q", m.To)
	}
	if !strings.Contains(m.Subject, "Down") {
		t.Errorf("Subject missing severity: %q", m.Subject)
	}
	if !strings.Contains(m.PlainBody, "https://example.com") {
		t.Errorf("PlainBody missing site URL")
	}
}

func TestEmailDispatcherRejectsBadDestination(t *testing.T) {
	stub := &StubSender{Logger: func(EmailMessage) {}}
	d := NewEmailDispatcher(stub, "from@example.com")

	cases := []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"address":""}`),
		json.RawMessage(`not json`),
	}
	for i, dest := range cases {
		status, _, err := d.Send(context.Background(), dest, makeTestNotification())
		if err == nil {
			t.Errorf("case %d: expected error for destination %s", i, dest)
		}
		if status < 500 {
			t.Errorf("case %d: status = %d, want >=500", i, status)
		}
	}
	if len(stub.Sent()) != 0 {
		t.Error("StubSender should not have been invoked on bad destination")
	}
}

// TestRenderEmailSubjectStripsCRLF verifies that CRLF in untrusted
// fields (site URL is operator-controlled but the DB column doesn't
// enforce CRLF-free) doesn't leak into the Subject header. Defense-
// in-depth against MIME header injection.
func TestRenderEmailSubjectStripsCRLF(t *testing.T) {
	n := makeTestNotification()
	n.SiteURL = "https://example.com\r\nBcc: attacker@evil.com"
	got := renderEmailSubject(n)
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("subject contains CRLF: %q", got)
	}
	if !strings.Contains(got, "https://example.com") {
		t.Errorf("subject lost the legitimate URL portion: %q", got)
	}
}

func TestBuildMIMEMessageStripsHeaderCRLF(t *testing.T) {
	mime := buildMIMEMessage(EmailMessage{
		From:      "from@example.com\r\nX-Injected: yes",
		To:        "to@example.com\r\nBcc: attacker@evil.com",
		Subject:   "test\r\nX-Header: malicious",
		PlainBody: "plain\r\nwith\r\nnewlines\r\nis fine in body",
		HTMLBody:  "<b>html</b>",
	})
	// Split headers from body and assert no injected header lines.
	parts := strings.SplitN(mime, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("MIME missing header/body separator:\n%s", mime)
	}
	headers := parts[0]
	// A successful injection would put the bad token at the start of a
	// header line (preceded by \r\n). The strip merges the malicious
	// content into the legitimate header value, but no new header line
	// should be created.
	for _, bad := range []string{"\r\nX-Injected:", "\r\nBcc:", "\r\nX-Header:"} {
		if strings.Contains(headers, bad) {
			t.Errorf("header injection succeeded with token %q:\n%s", bad, headers)
		}
	}
	// The legitimate body CRLFs should pass through unchanged.
	if !strings.Contains(parts[1], "plain\r\nwith\r\nnewlines") {
		t.Errorf("body CRLF was incorrectly stripped:\n%s", parts[1])
	}
}

func TestBuildMIMEMessageHasBothParts(t *testing.T) {
	mime := buildMIMEMessage(EmailMessage{
		From:      "from@example.com",
		To:        "to@example.com",
		Subject:   "test",
		PlainBody: "plain content",
		HTMLBody:  "<b>html content</b>",
	})
	for _, want := range []string{
		"From: from@example.com",
		"To: to@example.com",
		"Subject: test",
		"multipart/alternative",
		"text/plain",
		"plain content",
		"text/html",
		"<b>html content</b>",
	} {
		if !strings.Contains(mime, want) {
			t.Errorf("MIME missing %q", want)
		}
	}
}

func TestWPCOMSenderPostsCorrectly(t *testing.T) {
	var (
		gotAuth    string
		gotCT      string
		gotBody    wpcomEmailRequest
		decodeErr  error
		hits       int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		decodeErr = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	s := &WPCOMSender{
		Endpoint:  srv.URL,
		AuthToken: "TEST_TOKEN",
	}
	err := s.Send(context.Background(), EmailMessage{
		From:      "from@example.com",
		To:        "ops@example.com",
		Subject:   "test subject",
		PlainBody: "plain",
		HTMLBody:  "<b>html</b>",
	})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1", hits)
	}
	if gotAuth != "Bearer TEST_TOKEN" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if decodeErr != nil {
		t.Errorf("body decode: %v", decodeErr)
	}
	if gotBody.Subject != "test subject" || gotBody.To != "ops@example.com" {
		t.Errorf("body fields wrong: %+v", gotBody)
	}
}

func TestWPCOMSenderSurfacesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	s := &WPCOMSender{Endpoint: srv.URL, AuthToken: "x"}
	err := s.Send(context.Background(), EmailMessage{
		From: "f@x", To: "t@x", Subject: "s", PlainBody: "p", HTMLBody: "h",
	})
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500: %v", err)
	}
}

func TestWPCOMSenderRequiresEndpoint(t *testing.T) {
	s := &WPCOMSender{}
	err := s.Send(context.Background(), EmailMessage{})
	if err == nil {
		t.Fatal("expected error when endpoint missing")
	}
}

func TestStubSenderRecordsAndReset(t *testing.T) {
	s := &StubSender{Logger: func(EmailMessage) {}}
	for i := 0; i < 3; i++ {
		_ = s.Send(context.Background(), EmailMessage{Subject: "n"})
	}
	if got := len(s.Sent()); got != 3 {
		t.Errorf("Sent count = %d, want 3", got)
	}
	s.Reset()
	if got := len(s.Sent()); got != 0 {
		t.Errorf("after Reset, Sent count = %d, want 0", got)
	}
}

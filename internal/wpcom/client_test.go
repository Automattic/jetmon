package wpcom

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	c := NewWithConfig(ClientConfig{
		AuthToken:      "test-token",
		Mode:           NotifyModeModern,
		ModernEndpoint: srv.URL,
		HTTPClient:     &http.Client{Timeout: 5 * time.Second},
	})
	return c, srv.Close
}

func testNotification(blogID int64) Notification {
	return Notification{BlogID: blogID, MonitorURL: "https://example.com", StatusType: "success"}
}

func TestNotifySuccess(t *testing.T) {
	c, close := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization header = %q, want Bearer test-token", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	})
	defer close()

	if err := c.Notify(testNotification(1)); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if c.IsCircuitOpen() {
		t.Fatal("circuit should be closed after success")
	}
	if c.failures != 0 {
		t.Fatalf("failures = %d, want 0", c.failures)
	}
}

func TestNotifySendsModernPayloadShape(t *testing.T) {
	var got map[string]json.RawMessage
	c, close := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization header = %q, want Bearer test-token", r.Header.Get("Authorization"))
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	defer close()

	notification := Notification{
		BlogID:           12345,
		MonitorURL:       "https://example.com/",
		StatusID:         2,
		LastCheck:        "2026-05-03T03:00:00Z",
		LastStatusChange: "2026-05-03T03:01:00Z",
		StatusType:       "server",
		Checks: []CheckEntry{
			{Type: 1, Host: "monitor-a", Status: 0, RTT: 123, Code: 500},
			{Type: 2, Host: "verifier-us", Status: 0, RTT: 456, Code: 500},
		},
	}

	if err := c.Notify(notification); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}

	wantKeys := []string{
		"blog_id",
		"checks",
		"last_check",
		"last_status_change",
		"monitor_url",
		"status_id",
		"status_type",
	}
	var gotKeys []string
	for key := range got {
		gotKeys = append(gotKeys, key)
	}
	slices.Sort(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("payload keys = %v, want %v", gotKeys, wantKeys)
	}

	var decoded Notification
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("remarshal payload: %v", err)
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode payload into Notification: %v", err)
	}
	if !reflect.DeepEqual(decoded, notification) {
		t.Fatalf("payload = %+v, want %+v", decoded, notification)
	}
}

func TestNotifyLegacyModeSendsV1CompatibleGETWithClientCert(t *testing.T) {
	certPath, keyPath := writeClientCertificate(t)
	var got map[string]json.RawMessage
	var sawClientCert bool
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("Authorization header = %q, want empty in legacy mode", auth)
		}
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			sawClientCert = true
		}
		data := r.URL.Query().Get("data")
		if data == "" {
			t.Fatal("missing data query parameter")
		}
		if err := json.Unmarshal([]byte(data), &got); err != nil {
			t.Fatalf("decode legacy data: %v", err)
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	srv.StartTLS()
	defer srv.Close()

	c := NewWithConfig(ClientConfig{
		AuthToken:         "legacy-token",
		Mode:              NotifyModeLegacy,
		LegacyEndpoint:    srv.URL,
		LegacyCertPath:    certPath,
		LegacyKeyPath:     keyPath,
		LegacyInsecureTLS: true,
	})

	notification := Notification{
		BlogID:           12345,
		MonitorURL:       "https://example.com/",
		StatusID:         2,
		LastCheck:        "2026-05-03T03:00:00Z",
		LastStatusChange: "2026-05-03T03:01:00Z",
		StatusType:       "server",
		Checks: []CheckEntry{
			{Type: 1, Host: "monitor-a", Status: 0, RTT: 123, Code: 500},
		},
	}
	if err := c.Notify(notification); err != nil {
		t.Fatalf("Notify() error = %v", err)
	}
	if !sawClientCert {
		t.Fatal("server did not receive a client certificate")
	}

	wantKeys := []string{
		"blog_id",
		"checks",
		"last_check",
		"last_status_change",
		"monitor_url",
		"status_id",
		"token",
	}
	var gotKeys []string
	for key := range got {
		gotKeys = append(gotKeys, key)
	}
	slices.Sort(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("payload keys = %v, want %v", gotKeys, wantKeys)
	}

	var decoded legacyNotification
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("remarshal payload: %v", err)
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode payload into legacyNotification: %v", err)
	}
	if decoded.Token != "legacy-token" {
		t.Fatalf("legacy token = %q, want legacy-token", decoded.Token)
	}
	if decoded.StatusID != notification.StatusID {
		t.Fatalf("decoded status_id = %d, want %d", decoded.StatusID, notification.StatusID)
	}
}

func TestNotifyLegacyModeReportsMissingClientCert(t *testing.T) {
	c := NewWithConfig(ClientConfig{
		AuthToken:      "legacy-token",
		Mode:           NotifyModeLegacy,
		LegacyEndpoint: "https://127.0.0.1/jetmon/",
		LegacyCertPath: "/does/not/exist.crt",
		LegacyKeyPath:  "/does/not/exist.key",
	})

	err := c.Notify(testNotification(1))
	if err == nil {
		t.Fatal("Notify() expected cert load error")
	}
	if !strings.Contains(err.Error(), "load legacy wpcom client certificate") {
		t.Fatalf("Notify() error = %v, want legacy certificate load error", err)
	}
}

func TestNotifyResetsFailureCountOnSuccess(t *testing.T) {
	calls := 0
	c, close := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusBadGateway)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})
	defer close()

	_ = c.Notify(testNotification(1))
	_ = c.Notify(testNotification(2))
	if c.failures != 2 {
		t.Fatalf("failures = %d, want 2", c.failures)
	}

	_ = c.Notify(testNotification(3))
	if c.failures != 0 {
		t.Fatalf("failures after success = %d, want 0", c.failures)
	}
}

func TestNotifyOpensCircuitAfterMaxFailures(t *testing.T) {
	c, close := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	defer close()

	for range cbMaxFailures {
		_ = c.Notify(testNotification(1))
	}

	if !c.IsCircuitOpen() {
		t.Fatal("circuit should be open after max failures")
	}
}

func TestNotifyPermanentNotFoundDoesNotOpenCircuit(t *testing.T) {
	c, close := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer close()

	for range cbMaxFailures + 2 {
		err := c.Notify(testNotification(1))
		if err == nil {
			t.Fatal("Notify() expected not-found error")
		}
		if !IsPermanentStatusError(err) {
			t.Fatalf("Notify() error = %v, want permanent status error", err)
		}
		status, ok := HTTPStatusCode(err)
		if !ok || status != http.StatusNotFound {
			t.Fatalf("HTTPStatusCode() = %d, %v; want %d, true", status, ok, http.StatusNotFound)
		}
	}

	if c.IsCircuitOpen() {
		t.Fatal("circuit should stay closed for permanent per-notification failures")
	}
	if c.failures != 0 {
		t.Fatalf("failures = %d after permanent failures, want 0", c.failures)
	}
	if c.QueueDepth() != 0 {
		t.Fatalf("QueueDepth() = %d after permanent failures, want 0", c.QueueDepth())
	}
}

func TestNotifyGoneIsPermanent(t *testing.T) {
	c, close := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	})
	defer close()

	err := c.Notify(testNotification(1))
	if err == nil {
		t.Fatal("Notify() expected gone error")
	}
	if !IsPermanentStatusError(err) {
		t.Fatalf("Notify() error = %v, want permanent status error", err)
	}
}

func TestNotifyQueuesAndReturnsErrorWhenCircuitOpen(t *testing.T) {
	c, close := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer close()

	c.circuitOpen = true
	c.circuitOpenAt = time.Now()

	err := c.Notify(testNotification(42))
	if err == nil {
		t.Fatal("Notify() expected error when circuit is open")
	}
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("Notify() error = %v, want ErrCircuitOpen", err)
	}
	if c.QueueDepth() != 1 {
		t.Fatalf("QueueDepth() = %d, want 1", c.QueueDepth())
	}
}

func TestNotifyResetsCircuitAfterTimeout(t *testing.T) {
	var flushed []int64
	c, close := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var n Notification
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		flushed = append(flushed, n.BlogID)
		w.WriteHeader(http.StatusOK)
	})
	defer close()

	// Open circuit and pre-load a queued notification.
	c.circuitOpen = true
	c.circuitOpenAt = time.Now().Add(-(cbResetTimeout + time.Second))
	c.failures = cbMaxFailures
	c.queue = []queuedNotification{{n: testNotification(99)}}
	_ = flushed

	// Next Notify call should reset the circuit and flush the queue.
	err := c.Notify(testNotification(1))
	if err != nil {
		t.Fatalf("Notify() after timeout error = %v", err)
	}
	if c.IsCircuitOpen() {
		t.Fatal("circuit should be closed after reset timeout")
	}
	if c.QueueDepth() != 0 {
		t.Fatalf("QueueDepth() = %d, want 0 after flush", c.QueueDepth())
	}
	if !slices.Equal(flushed, []int64{99, 1}) {
		t.Fatalf("flushed notifications = %v, want [99 1]", flushed)
	}
}

func TestNew(t *testing.T) {
	c := New("my-token", "my-host")
	if c == nil {
		t.Fatal("New() = nil")
	}
	cfg := c.configSnapshot()
	if cfg.authToken != "my-token" {
		t.Fatalf("authToken = %q, want my-token", cfg.authToken)
	}
	if cfg.hostname != "my-host" {
		t.Fatalf("hostname = %q, want my-host", cfg.hostname)
	}
	if cfg.mode != NotifyModeLegacy {
		t.Fatalf("mode = %q, want legacy default", cfg.mode)
	}
	if !cfg.legacyInsecureTLS {
		t.Fatal("legacyInsecureTLS = false, want true for New default")
	}
}

func TestSendFlushContinuesAfterError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := NewWithConfig(ClientConfig{
		AuthToken:      "test-token",
		Mode:           NotifyModeModern,
		ModernEndpoint: srv.URL,
		HTTPClient:     &http.Client{Timeout: 5 * time.Second},
	})

	c.sendFlush([]queuedNotification{
		{n: testNotification(1)},
		{n: testNotification(2)},
	})

	if calls != 2 {
		t.Fatalf("send calls = %d, want 2 (flush should continue after first error)", calls)
	}
}

func TestSendFlushEmptyIsNoop(t *testing.T) {
	c := &Client{}
	c.sendFlush(nil)
	c.sendFlush([]queuedNotification{})
}

func TestNotifySendNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close() // close before sending — forces a connection error

	c := NewWithConfig(ClientConfig{
		AuthToken:      "token",
		Mode:           NotifyModeModern,
		ModernEndpoint: url,
		HTTPClient:     &http.Client{Timeout: time.Second},
	})

	err := c.Notify(testNotification(1))
	if err == nil {
		t.Fatal("Notify() expected error for closed server")
	}
	if c.failures != 1 {
		t.Fatalf("failures = %d after network error, want 1", c.failures)
	}
}

func writeClientCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "jetmon-test-client",
		},
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create client cert: %v", err)
	}
	dir := t.TempDir()
	certPath := dir + "/client.crt"
	keyPath := dir + "/client.key"
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatalf("write client cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("write client key: %v", err)
	}
	return certPath, keyPath
}

func TestEnqueueDropsOldestWhenFull(t *testing.T) {
	c := &Client{}
	for i := range queueMaxSize {
		c.enqueue(Notification{BlogID: int64(i)})
	}
	if c.QueueDepth() != queueMaxSize {
		t.Fatalf("QueueDepth() = %d, want %d", c.QueueDepth(), queueMaxSize)
	}

	c.enqueue(Notification{BlogID: queueMaxSize})

	if c.QueueDepth() != queueMaxSize {
		t.Fatalf("QueueDepth() after overflow = %d, want %d", c.QueueDepth(), queueMaxSize)
	}
	if c.queue[0].n.BlogID != 1 {
		t.Fatalf("oldest entry BlogID = %d, want 1 (entry 0 should have been dropped)", c.queue[0].n.BlogID)
	}
}

package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestProtectedDialContextRejectsUnsafeLiteral(t *testing.T) {
	dial := ProtectedDialContext(nil)
	_, err := dial(context.Background(), "tcp", "127.0.0.1:80")
	if !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("dial err = %v, want ErrUnsafeTarget", err)
	}
}

func TestProtectedHTTPClientRejectsUnsafeRedirectURL(t *testing.T) {
	client := NewProtectedHTTPClient(time.Second)
	req, err := http.NewRequest(http.MethodGet, "http://user@example.com/path", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := client.CheckRedirect(req, nil); err == nil {
		t.Fatal("CheckRedirect accepted userinfo URL")
	}
}

func TestProtectedHTTPClientDoesNotUseAmbientProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("NO_PROXY", "")

	client := NewProtectedHTTPClient(time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		proxyURL, err := transport.Proxy(req)
		if err != nil {
			t.Fatalf("Proxy returned error: %v", err)
		}
		t.Fatalf("protected transport uses proxy %v, want direct dial", proxyURL)
	}

	if _, err := ProtectedDialContext(nil)(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", "80")); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("protected dial err = %v, want ErrUnsafeTarget", err)
	}
}

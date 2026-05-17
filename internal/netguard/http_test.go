package netguard

import (
	"context"
	"errors"
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

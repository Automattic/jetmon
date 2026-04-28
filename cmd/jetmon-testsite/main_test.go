package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFixtureHandlerEndpoints(t *testing.T) {
	srv := httptest.NewServer(newFixtureHandler())
	defer srv.Close()

	tests := []struct {
		path string
		code int
		body string
	}{
		{path: "/health", code: http.StatusOK, body: "ok"},
		{path: "/ok", code: http.StatusOK, body: "fixture ok"},
		{path: "/keyword", code: http.StatusOK, body: "keyword present"},
		{path: "/status/403", code: http.StatusForbidden, body: "status 403"},
		{path: "/status/500", code: http.StatusInternalServerError, body: "status 500"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tt.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tt.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.code {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.code)
			}
			buf := make([]byte, 256)
			n, _ := resp.Body.Read(buf)
			if !strings.Contains(string(buf[:n]), tt.body) {
				t.Fatalf("body = %q, want substring %q", string(buf[:n]), tt.body)
			}
		})
	}
}

func TestFixtureRedirectAndDelay(t *testing.T) {
	srv := httptest.NewServer(newFixtureHandler())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(srv.URL + "/redirect")
	if err != nil {
		t.Fatalf("GET redirect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/ok" {
		t.Fatalf("redirect status=%d location=%q", resp.StatusCode, resp.Header.Get("Location"))
	}

	start := time.Now()
	resp, err = http.Get(srv.URL + "/slow?delay=10ms")
	if err != nil {
		t.Fatalf("GET slow: %v", err)
	}
	resp.Body.Close()
	if elapsed := time.Since(start); elapsed < 10*time.Millisecond {
		t.Fatalf("slow endpoint returned too quickly: %s", elapsed)
	}
}

func TestFixtureWebhookReceiverRecordsAndVerifiesSignature(t *testing.T) {
	srv := httptest.NewServer(newFixtureHandler())
	defer srv.Close()

	secret := "whsec_test_secret"
	body := []byte(`{"type":"event.opened"}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/webhook?secret="+secret, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Jetmon-Event", "event.opened")
	req.Header.Set("X-Jetmon-Delivery", "123")
	req.Header.Set("X-Jetmon-Signature", fixtureTestSignature(1700000000, body, secret))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST webhook: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST status = %d, want 204", resp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/webhook/requests")
	if err != nil {
		t.Fatalf("GET webhook requests: %v", err)
	}
	defer resp.Body.Close()
	var got struct {
		Count    int `json:"count"`
		Requests []struct {
			Event          string `json:"event"`
			Delivery       string `json:"delivery"`
			SignatureValid *bool  `json:"signature_valid"`
			Body           string `json:"body"`
		} `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode webhook requests: %v", err)
	}
	if got.Count != 1 || len(got.Requests) != 1 {
		t.Fatalf("requests = %+v, want one", got)
	}
	if got.Requests[0].Event != "event.opened" || got.Requests[0].Delivery != "123" {
		t.Fatalf("request headers = %+v", got.Requests[0])
	}
	if got.Requests[0].SignatureValid == nil || !*got.Requests[0].SignatureValid {
		t.Fatalf("signature_valid = %v, want true", got.Requests[0].SignatureValid)
	}
	if got.Requests[0].Body != string(body) {
		t.Fatalf("body = %q, want %q", got.Requests[0].Body, string(body))
	}

	req, err = http.NewRequest(http.MethodDelete, srv.URL+"/webhook/requests", nil)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE webhook requests: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
	}
}

func fixtureTestSignature(ts int64, body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%d.", ts)))
	_, _ = mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

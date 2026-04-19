package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	body, err := httpGet(srv.URL)
	if err != nil {
		t.Fatalf("httpGet() error = %v", err)
	}
	if strings.TrimSpace(body) != "ok" {
		t.Fatalf("httpGet() body = %q, want %q", body, "ok")
	}
}

func TestHTTPGetErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := httpGet(srv.URL)
	if err == nil {
		t.Fatalf("httpGet() expected error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("httpGet() error = %v, want status code", err)
	}
}

func TestEnvOrDefault(t *testing.T) {
	const key = "JETMON_TEST_ENV_OR_DEFAULT"
	t.Setenv(key, "")

	if got := envOrDefault(key, "fallback"); got != "fallback" {
		t.Fatalf("envOrDefault() = %q, want fallback", got)
	}

	t.Setenv(key, "set-value")
	if got := envOrDefault(key, "fallback"); got != "set-value" {
		t.Fatalf("envOrDefault() = %q, want set-value", got)
	}
}

func TestRepeat(t *testing.T) {
	if got := repeat("-", 5); got != "-----" {
		t.Fatalf("repeat(\"-\", 5) = %q, want -----", got)
	}
	if got := repeat("ab", 3); got != "ababab" {
		t.Fatalf("repeat(\"ab\", 3) = %q, want ababab", got)
	}
	if got := repeat("x", 0); got != "" {
		t.Fatalf("repeat(\"x\", 0) = %q, want empty", got)
	}
}

func TestReadPIDFile(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "test.pid")
	if err := os.WriteFile(pidPath, []byte("12345\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("JETMON_PID_FILE", pidPath)

	pid := readPIDFile()
	if pid != 12345 {
		t.Fatalf("readPIDFile() = %d, want 12345", pid)
	}
}

func TestWriteAndRemovePIDFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.pid")
	if err := writePIDFile(path); err != nil {
		t.Fatalf("writePIDFile() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var pid int
	if _, err := fmt.Sscan(string(data), &pid); err != nil || pid <= 0 {
		t.Fatalf("invalid PID in file: %q", string(data))
	}

	removePIDFile(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("PID file still exists after removePIDFile()")
	}
}

func TestResolveSince(t *testing.T) {
	if got := resolveSince(""); got != "" {
		t.Fatalf("resolveSince(\"\") = %q, want empty", got)
	}

	// Duration input: result should be a timestamp just before now.
	before := time.Now()
	got := resolveSince("1h")
	after := time.Now()

	ts, err := time.ParseInLocation("2006-01-02 15:04:05", got, time.Local)
	if err != nil {
		t.Fatalf("resolveSince(\"1h\") = %q, not a valid timestamp: %v", got, err)
	}
	if ts.Before(before.Add(-time.Hour-time.Second)) || ts.After(after.Add(-time.Hour+time.Second)) {
		t.Fatalf("resolveSince(\"1h\") = %q, out of expected range", got)
	}

	// Non-duration string passes through unchanged.
	const literal = "2024-01-15 10:00:00"
	if got := resolveSince(literal); got != literal {
		t.Fatalf("resolveSince(%q) = %q, want passthrough", literal, got)
	}
}

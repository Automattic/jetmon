package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultHTTPAddr  = ":8091"
	defaultHTTPSAddr = ":8443"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	httpAddr := envOrDefault("FIXTURE_HTTP_ADDR", defaultHTTPAddr)
	httpsAddr := envOrDefault("FIXTURE_HTTPS_ADDR", defaultHTTPSAddr)
	handler := newFixtureHandler()

	servers := []*http.Server{{
		Addr:    httpAddr,
		Handler: handler,
	}}
	if httpsAddr != "" {
		cert, err := selfSignedCert()
		if err != nil {
			log.Fatalf("generate tls cert: %v", err)
		}
		servers = append(servers, &http.Server{
			Addr:      httpsAddr,
			Handler:   handler,
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		})
	}

	errCh := make(chan error, len(servers))
	for _, srv := range servers {
		srv := srv
		go func() {
			log.Printf("jetmon-testsite: listening on %s", srv.Addr)
			var err error
			if srv.TLSConfig != nil {
				err = srv.ListenAndServeTLS("", "")
			} else {
				err = srv.ListenAndServe()
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Printf("jetmon-testsite: shutdown signal=%s", sig)
	case err := <-errCh:
		log.Printf("jetmon-testsite: server error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("jetmon-testsite: shutdown %s: %v", srv.Addr, err)
		}
	}
}

func newFixtureHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "jetmon fixture ok\n")
	})
	mux.HandleFunc("/tls", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "jetmon fixture tls endpoint\n")
	})
	mux.HandleFunc("/keyword", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "jetmon fixture keyword present\n")
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ok", http.StatusFound)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		delay := fixtureDelay(r.URL.Query().Get("delay"), 5*time.Second)
		time.Sleep(delay)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "slow response after %s\n", delay)
	})
	mux.HandleFunc("/status/", func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.URL.Path, "/status/")
		code, err := strconv.Atoi(raw)
		if err != nil || code < 100 || code > 599 {
			http.Error(w, "status must be 100-599", http.StatusBadRequest)
			return
		}
		w.WriteHeader(code)
		if code != http.StatusNoContent && code != http.StatusNotModified {
			fmt.Fprintf(w, "status %d\n", code)
		}
	})
	return mux
}

func fixtureDelay(raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	delay, err := time.ParseDuration(raw)
	if err != nil || delay < 0 {
		return fallback
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func selfSignedCert() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "jetmon-testsite"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", "api-fixture", "jetmon-testsite"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER := x509.MarshalPKCS1PrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func healthcheck() error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1" + envOrDefault("FIXTURE_HEALTH_PORT", ":8091") + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned %s", resp.Status)
	}
	return nil
}

func envOrDefault(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

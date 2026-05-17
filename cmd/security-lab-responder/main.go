// security-lab-responder is a lab-only helper for repeatable adversarial HTTP
// response checks. It is intentionally outside the production build targets.
package main

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", ":18080", "HTTP listen address")
	tlsAddr := flag.String("tls-addr", ":18443", "HTTPS listen address; empty disables TLS")
	largeBytes := flag.Int64("large-bytes", 4<<20, "bytes returned by /large and compressed by /gzip-bomb")
	slowDelay := flag.Duration("slow-delay", 250*time.Millisecond, "delay between /slow-body chunks")
	flag.Parse()

	handler := newHandler(*largeBytes, *slowDelay)
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 2)
	go func() {
		log.Printf("security-lab-responder: http listening on %s", *addr)
		errCh <- httpSrv.ListenAndServe()
	}()

	var tlsSrv *http.Server
	if *tlsAddr != "" {
		cfg, err := selfSignedTLSConfig()
		if err != nil {
			log.Fatalf("tls config: %v", err)
		}
		ln, err := net.Listen("tcp", *tlsAddr)
		if err != nil {
			log.Fatalf("tls listen: %v", err)
		}
		tlsSrv = &http.Server{
			Addr:              *tlsAddr,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			log.Printf("security-lab-responder: https listening on %s", *tlsAddr)
			errCh <- tlsSrv.Serve(tls.NewListener(ln, cfg))
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Printf("security-lab-responder: %s received, shutting down", sig)
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("security-lab-responder: server error: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	if tlsSrv != nil {
		_ = tlsSrv.Shutdown(ctx)
	}
}

func newHandler(largeBytes int64, slowDelay time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "ok\n")
		case "/large":
			writeLarge(w, largeBytes)
		case "/infinite":
			writeInfinite(r.Context(), w, 0)
		case "/slow-body":
			writeInfinite(r.Context(), w, slowDelay)
		case "/redirect-local":
			http.Redirect(w, r, "http://127.0.0.1:1/private", http.StatusFound)
		case "/redirect-metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		case "/redirect-loop":
			http.Redirect(w, r, "http://"+r.Host+"/redirect-loop", http.StatusFound)
		case "/huge-header":
			writeHugeHeader(w)
		case "/gzip-bomb":
			writeGzipBomb(w, largeBytes)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func writeLarge(w http.ResponseWriter, bytes int64) {
	if bytes < 0 {
		bytes = 0
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(bytes, 10))
	w.WriteHeader(http.StatusOK)
	writeBytes(w, bytes)
}

func writeInfinite(ctx context.Context, w http.ResponseWriter, delay time.Duration) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	chunk := make([]byte, 1024)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, err := w.Write(chunk); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func writeHugeHeader(w http.ResponseWriter) {
	w.Header().Set("X-Jetmon-Lab-Large", string(make([]byte, 80<<10)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "large header\n")
}

func writeGzipBomb(w http.ResponseWriter, bytes int64) {
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	zw := gzip.NewWriter(w)
	writeBytes(zw, bytes)
	_ = zw.Close()
}

func writeBytes(w io.Writer, bytes int64) {
	const chunkSize = 32 << 10
	chunk := make([]byte, chunkSize)
	for bytes > 0 {
		n := int64(len(chunk))
		if bytes < n {
			n = bytes
		}
		if _, err := w.Write(chunk[:n]); err != nil {
			return
		}
		bytes -= n
	}
}

func selfSignedTLSConfig() (*tls.Config, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse generated key pair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

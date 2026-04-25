package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/metrics"
	"github.com/Automattic/jetmon/internal/veriflier"
)

var version = "dev"

const shutdownGracePeriod = 30 * time.Second

type veriflierConfig struct {
	AuthToken string `json:"auth_token"`
	GRPCPort  string `json:"grpc_port"`
}

func main() {
	configPath := envOrDefault("VERIFLIER_CONFIG", "config/veriflier.json")

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	hostname, _ := os.Hostname()

	// Override auth token and port from environment if set (Docker entrypoint).
	if v := os.Getenv("VERIFLIER_AUTH_TOKEN"); v != "" {
		cfg.AuthToken = v
	}
	if v := os.Getenv("VERIFLIER_GRPC_PORT"); v != "" {
		cfg.GRPCPort = v
	}

	if cfg.GRPCPort == "" {
		log.Fatalf("VERIFLIER_GRPC_PORT is not set")
	}
	// Reject empty auth tokens at startup. The verifier's Bearer comparison
	// would otherwise accept any request with the literal "Bearer " header
	// (no token after the space) — a subtle auth bypass if a misconfigured
	// deploy leaves the token blank. Better to fail loud at startup.
	if cfg.AuthToken == "" {
		log.Fatalf("VERIFLIER_AUTH_TOKEN is not set; refusing to start with no authentication")
	}
	addr := fmt.Sprintf(":%s", cfg.GRPCPort)

	// Optional StatsD metrics. STATSD_ADDR is unset in standalone deploys,
	// "statsd:8125" in the docker compose stack. metrics.Init failure logs and
	// continues — the verifier should still run with metrics disabled.
	if statsdAddr := os.Getenv("STATSD_ADDR"); statsdAddr != "" {
		if err := metrics.Init(statsdAddr, hostname); err != nil {
			log.Printf("metrics: init failed (%v) — running without metrics", err)
		} else {
			log.Printf("metrics: sending to %s", statsdAddr)
		}
	}

	srv := veriflier.NewServer(addr, cfg.AuthToken, hostname, version, performCheck)

	// Graceful shutdown: SIGINT/SIGTERM triggers Shutdown(ctx) with a drain
	// budget so in-flight checks can complete before the listener closes.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("veriflier2: %s received, draining (up to %s)", sig, shutdownGracePeriod)
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("veriflier2: shutdown error: %v", err)
		}
	}()

	log.Printf("veriflier2 %s starting on %s", version, addr)
	if err := srv.Listen(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
	log.Println("veriflier2: shutdown complete")
}

// performCheck runs a single HTTP check and returns the result for the server.
func performCheck(req veriflier.CheckRequest) veriflier.CheckResult {
	res := checker.Check(context.Background(), checker.Request{
		BlogID:         req.BlogID,
		URL:            req.URL,
		TimeoutSeconds: int(req.TimeoutSeconds),
		Keyword:        stringPtr(req.Keyword),
		CustomHeaders:  req.CustomHeaders,
		RedirectPolicy: checker.RedirectPolicy(req.RedirectPolicy),
	})

	return veriflier.CheckResult{
		BlogID:    res.BlogID,
		URL:       res.URL,
		Success:   res.Success,
		HTTPCode:  int32(res.HTTPCode),
		ErrorCode: int32(res.ErrorCode),
		RTTMs:     res.RTT.Milliseconds(),
	}
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func loadConfig(path string) (*veriflierConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		// Fall back to environment-only config.
		return &veriflierConfig{
			AuthToken: os.Getenv("VERIFLIER_AUTH_TOKEN"),
			GRPCPort:  envOrDefault("VERIFLIER_GRPC_PORT", "7803"),
		}, nil
	}
	defer f.Close()

	var cfg veriflierConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

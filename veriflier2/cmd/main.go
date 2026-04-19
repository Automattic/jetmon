package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/veriflier"
)

var version = "dev"

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
	addr := fmt.Sprintf(":%s", cfg.GRPCPort)

	srv := veriflier.NewServer(addr, cfg.AuthToken, hostname, version, performCheck)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("veriflier2: shutting down")
		os.Exit(0)
	}()

	log.Printf("veriflier2 %s starting on %s", version, addr)
	if err := srv.Listen(); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

// performCheck runs a single HTTP check and returns the result for the server.
func performCheck(req veriflier.CheckRequest) veriflier.CheckResult {
	res := checker.Check(context.Background(), checker.Request{
		BlogID:         req.BlogID,
		URL:            req.URL,
		TimeoutSeconds: int(req.TimeoutSeconds),
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

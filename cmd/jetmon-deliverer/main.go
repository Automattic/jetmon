package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/deliverer"
	"github.com/Automattic/jetmon/internal/metrics"
)

// Injected at build time via -ldflags.
var (
	version   = "dev"
	buildDate = "unknown"
	goVersion = "unknown"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("jetmon-deliverer %s (built %s with %s)\n", version, buildDate, goVersion)
			return
		case "validate-config":
			cmdValidateConfig()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (want: version, validate-config)\n", os.Args[1])
			os.Exit(2)
		}
	}
	run()
}

func cmdValidateConfig() {
	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL config parse: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS config parse")

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL db connect: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS db connect")

	cfg := config.Get()
	fmt.Printf("INFO email_transport=%s\n", emailTransportLabel(cfg))
	if !emailTransportDelivers(cfg) {
		fmt.Printf("WARN email_transport=%s; alert-contact emails will be logged but not delivered\n", emailTransportLabel(cfg))
	}
	if level, msg := deliveryOwnerStatus(cfg, db.Hostname()); msg != "" {
		fmt.Printf("%s %s\n", level, msg)
	}

	fmt.Println("\nvalidation passed")
}

func run() {
	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		log.Fatalf("load config: %v", err)
	}
	cfg := config.Get()
	log.Printf("config: email_transport=%s", emailTransportLabel(cfg))
	if !emailTransportDelivers(cfg) {
		log.Printf("WARN: email_transport=%s; alert-contact emails will be logged but not delivered", emailTransportLabel(cfg))
	}

	config.LoadDB()
	if err := db.ConnectWithRetry(10); err != nil {
		log.Fatalf("db connect: %v", err)
	}
	audit.Init(db.DB())

	if err := metrics.Init("statsd:8125", db.Hostname()); err != nil {
		log.Printf("warning: statsd init failed: %v", err)
	}

	hostname := db.Hostname()
	if level, msg := deliveryOwnerStatus(cfg, hostname); msg != "" {
		if level == "WARN" {
			log.Printf("WARN: %s", msg)
		} else {
			log.Printf("config: %s", msg)
		}
	}
	if !deliveryWorkersShouldStart(cfg, hostname) {
		waitForShutdown()
		log.Println("jetmon-deliverer: shutdown complete")
		return
	}

	runtime := deliverer.Start(deliverer.Config{
		DB:          db.DB(),
		InstanceID:  hostname,
		Dispatchers: deliverer.BuildAlertDispatchers(cfg),
	})
	waitForShutdown()
	runtime.Stop()
	log.Println("jetmon-deliverer: shutdown complete")
}

func deliveryWorkersShouldStart(cfg *config.Config, hostname string) bool {
	owner := strings.TrimSpace(cfg.DeliveryOwnerHost)
	return owner == "" || owner == hostname
}

func deliveryOwnerStatus(cfg *config.Config, hostname string) (string, string) {
	owner := strings.TrimSpace(cfg.DeliveryOwnerHost)
	if owner == "" {
		return "WARN", fmt.Sprintf("delivery_owner_host is unset; standalone deliverer on host %q will run delivery workers", hostname)
	}
	if owner == hostname {
		return "INFO", fmt.Sprintf("delivery_owner_host=%q matched; delivery workers enabled on this host", owner)
	}
	return "INFO", fmt.Sprintf("delivery_owner_host=%q; standalone deliverer idle on host %q", owner, hostname)
}

func waitForShutdown() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("received %s, stopping", sig)
}

func emailTransportLabel(cfg *config.Config) string {
	if cfg.EmailTransport == "" {
		return "stub"
	}
	return cfg.EmailTransport
}

func emailTransportDelivers(cfg *config.Config) bool {
	return cfg.EmailTransport == "smtp" || cfg.EmailTransport == "wpcom"
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

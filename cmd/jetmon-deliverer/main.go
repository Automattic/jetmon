package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/deliverer"
	"github.com/Automattic/jetmon/internal/fleethealth"
	"github.com/Automattic/jetmon/internal/metrics"
	"github.com/Automattic/jetmon/internal/processcontrol"
	"github.com/Automattic/jetmon/internal/processmetrics"
)

const (
	processHealthWriteTimeout = 2 * time.Second
)

// Injected at build time via -ldflags.
var (
	version   = "dev"
	commit    = "unknown"
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
			cmdValidateConfig(os.Args[2:])
			return
		case "delivery-check":
			cmdDeliveryCheck(os.Args[2:])
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (want: version, validate-config, delivery-check)\n", os.Args[1])
			os.Exit(2)
		}
	}
	run()
}

type delivererValidationOptions struct {
	HostOverride         string
	RequireOwnerMatch    bool
	RequireEmailDelivery bool
	RequireAPIDisabled   bool
}

func parseValidateConfigOptions(args []string) (delivererValidationOptions, error) {
	var opts delivererValidationOptions
	fs := flag.NewFlagSet("validate-config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.HostOverride, "host", "", "host id to validate against DELIVERY_OWNER_HOST (default current hostname)")
	fs.BoolVar(&opts.RequireOwnerMatch, "require-owner-match", false, "fail unless DELIVERY_OWNER_HOST exactly matches the validated host")
	fs.BoolVar(&opts.RequireEmailDelivery, "require-email-delivery", false, "fail unless EMAIL_TRANSPORT is smtp or wpcom")
	fs.BoolVar(&opts.RequireAPIDisabled, "require-api-disabled", false, "fail unless API_PORT is 0 in the deliverer config")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return opts, nil
}

func cmdValidateConfig(args []string) {
	opts, err := parseValidateConfigOptions(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "usage: jetmon-deliverer validate-config [--host=<host>] [--require-owner-match] [--require-email-delivery] [--require-api-disabled]\n")
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(2)
	}

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
	hostID := strings.TrimSpace(opts.HostOverride)
	if hostID == "" {
		hostID = db.Hostname()
	}
	fmt.Printf("INFO deliverer_host=%q\n", hostID)
	fmt.Printf("INFO email_transport=%s\n", emailTransportLabel(cfg))
	if !emailTransportDelivers(cfg) {
		fmt.Printf("WARN email_transport=%s; alert-contact emails will be logged but not delivered\n", emailTransportLabel(cfg))
	}
	if cfg.APIPort > 0 {
		fmt.Printf("WARN api_port=%d; standalone deliverer ignores API_PORT, confirm this is a process-specific config\n", cfg.APIPort)
	} else {
		fmt.Println("PASS api_port=disabled")
	}
	if level, msg := deliveryOwnerStatus(cfg, hostID); msg != "" {
		fmt.Printf("%s %s\n", level, msg)
	}
	failures := validateDelivererConfigRequirements(cfg, hostID, opts)
	if len(failures) > 0 {
		for _, failure := range failures {
			fmt.Fprintf(os.Stderr, "FAIL %s\n", failure)
		}
		os.Exit(1)
	}

	fmt.Println("\nvalidation passed")
}

func run() {
	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		log.Fatalf("load config: %v", err)
	}
	cfg := config.Get()
	config.Debugf("config: email_transport=%s", emailTransportLabel(cfg))
	if !emailTransportDelivers(cfg) {
		log.Printf("WARN: email_transport=%s; alert-contact emails will be logged but not delivered", emailTransportLabel(cfg))
	}

	config.LoadDB()
	if err := db.ConnectWithRetry(10); err != nil {
		log.Fatalf("db connect: %v", err)
	}
	dbReloadCtx, stopDBReload := context.WithCancel(context.Background())
	defer stopDBReload()
	db.StartConfigReloader(dbReloadCtx, time.Duration(cfg.DBConfigUpdatesMin)*time.Minute)
	audit.Init(db.DB())

	hostname := db.Hostname()
	if addr, enabled, err := metrics.InitConfigured(cfg.StatsDAddr, cfg.StatsDMetricHost(hostname)); err != nil {
		log.Printf("warning: statsd init failed: %v", err)
	} else if enabled {
		config.Debugf("metrics: sending StatsD to %s", addr)
		if strings.TrimSpace(cfg.StatsDHostPath) == "" {
			log.Printf("WARN: STATSD_HOST_PATH is unset; StatsD metrics will use host identity %q", hostname)
		}
	} else {
		config.Debugf("metrics: StatsD disabled")
	}

	processStartedAt := time.Now().UTC()
	processID := fleethealth.ProcessID(hostname, fleethealth.ProcessDeliverer)
	workersEnabled := deliveryWorkersShouldStart(cfg, hostname)
	publishProcessHealth := func(state string) {
		snapshot := delivererProcessHealthSnapshot(hostname, processStartedAt, state, cfg, workersEnabled, delivererDependencyHealth(context.Background(), db.DB(), metrics.Global() != nil, time.Now().UTC()))
		emitDelivererResourceStats()
		ctx, cancel := context.WithTimeout(context.Background(), processHealthWriteTimeout)
		if err := fleethealth.Upsert(ctx, db.DB(), snapshot); err != nil {
			log.Printf("process health: %v", err)
		}
		cancel()
	}
	if level, msg := deliveryOwnerStatus(cfg, hostname); msg != "" {
		if level == "WARN" {
			log.Printf("WARN: %s", msg)
		} else {
			config.Debugf("config: %s", msg)
		}
	}
	initialState := fleethealth.StateRunning
	if !workersEnabled {
		initialState = fleethealth.StateIdle
	}
	publishProcessHealth(initialState)
	stopHealth := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				publishProcessHealth(initialState)
			case <-stopHealth:
				return
			}
		}
	}()

	if !workersEnabled {
		req := waitForShutdown()
		stopDBReload()
		close(stopHealth)
		publishProcessHealth(fleethealth.StateStopping)
		ctx, cancel := context.WithTimeout(context.Background(), processHealthWriteTimeout)
		if err := fleethealth.MarkStopped(ctx, db.DB(), processID, time.Now().UTC()); err != nil {
			log.Printf("process health: %v", err)
		}
		cancel()
		log.Println("jetmon-deliverer: shutdown complete")
		if req.Restart {
			log.Println("jetmon-deliverer: re-executing after graceful SIGHUP")
			if err := processcontrol.ExecSelf(); err != nil {
				log.Fatalf("jetmon-deliverer: re-exec failed: %v", err)
			}
		}
		return
	}

	runtime := deliverer.Start(deliverer.Config{
		DB:          db.DB(),
		InstanceID:  hostname,
		Dispatchers: deliverer.BuildAlertDispatchers(cfg),
	})
	req := waitForShutdown()
	stopDBReload()
	close(stopHealth)
	publishProcessHealth(fleethealth.StateStopping)
	runtime.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), processHealthWriteTimeout)
	if err := fleethealth.MarkStopped(ctx, db.DB(), processID, time.Now().UTC()); err != nil {
		log.Printf("process health: %v", err)
	}
	cancel()
	log.Println("jetmon-deliverer: shutdown complete")
	if req.Restart {
		log.Println("jetmon-deliverer: re-executing after graceful SIGHUP")
		if err := processcontrol.ExecSelf(); err != nil {
			log.Fatalf("jetmon-deliverer: re-exec failed: %v", err)
		}
	}
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

func emitDelivererResourceStats() {
	m := metrics.Global()
	if m == nil {
		return
	}
	m.EmitMemStats()
	writeDB := db.WriteDB()
	if writeDB != nil {
		m.EmitDBStats("db.write_pool", writeDB.Stats())
	}
	readDB := db.ReadDB()
	if readDB != nil && readDB != writeDB {
		m.EmitDBStats("db.read_pool", readDB.Stats())
	}
}

func validateDelivererConfigRequirements(cfg *config.Config, hostname string, opts delivererValidationOptions) []string {
	if cfg == nil {
		return []string{"config is not loaded"}
	}
	hostID := strings.TrimSpace(hostname)
	failures := []string{}
	if opts.RequireOwnerMatch {
		owner := strings.TrimSpace(cfg.DeliveryOwnerHost)
		if hostID == "" {
			failures = append(failures, "validated host id is empty")
		} else if owner == "" {
			failures = append(failures, fmt.Sprintf("DELIVERY_OWNER_HOST must be set to %q for single-owner deliverer rollout", hostID))
		} else if owner != hostID {
			failures = append(failures, fmt.Sprintf("DELIVERY_OWNER_HOST=%q does not match deliverer host %q", owner, hostID))
		}
	}
	if opts.RequireEmailDelivery && !emailTransportDelivers(cfg) {
		failures = append(failures, fmt.Sprintf("EMAIL_TRANSPORT=%q does not deliver email; set smtp or wpcom", emailTransportLabel(cfg)))
	}
	if opts.RequireAPIDisabled && cfg.APIPort > 0 {
		failures = append(failures, fmt.Sprintf("API_PORT=%d must be 0 for standalone deliverer config", cfg.APIPort))
	}
	return failures
}

func delivererProcessHealthSnapshot(hostname string, startedAt time.Time, state string, cfg *config.Config, workersEnabled bool, health []fleethealth.DependencyHealth) fleethealth.Snapshot {
	mem := processmetrics.CurrentMemory()
	healthStatus := fleethealth.RollupHealthStatus(health)
	if workersEnabled && strings.TrimSpace(cfg.DeliveryOwnerHost) == "" && healthStatus == fleethealth.HealthGreen {
		healthStatus = fleethealth.HealthAmber
	}
	if state == fleethealth.StateStopping || state == fleethealth.StateStopped {
		healthStatus = fleethealth.HealthAmber
	}
	return fleethealth.Snapshot{
		HostID:                    hostname,
		ProcessType:               fleethealth.ProcessDeliverer,
		PID:                       os.Getpid(),
		Version:                   version,
		BuildDate:                 buildDate,
		GoVersion:                 goVersion,
		State:                     state,
		HealthStatus:              healthStatus,
		StartedAt:                 startedAt,
		UpdatedAt:                 time.Now().UTC(),
		DeliveryWorkersEnabled:    workersEnabled,
		DeliveryOwnerHost:         cfg.DeliveryOwnerHost,
		GoSysMemMB:                mem.GoSysMemMB,
		RSSMemMB:                  mem.RSSMemMB,
		RuntimeGoroutines:         mem.RuntimeGoroutines,
		RuntimeGoroutinesRunnable: mem.RuntimeGoroutinesRunnable,
		RuntimeGoroutinesRunning:  mem.RuntimeGoroutinesRunning,
		RuntimeGoroutinesWaiting:  mem.RuntimeGoroutinesWaiting,
		RuntimeGoroutinesNotInGo:  mem.RuntimeGoroutinesNotInGo,
		RuntimeGoroutinesCreated:  mem.RuntimeGoroutinesCreated,
		RuntimeThreads:            mem.RuntimeThreads,
		DependencyHealth:          health,
	}
}

func delivererDependencyHealth(ctx context.Context, sqlDB *sql.DB, statsdReady bool, checkedAt time.Time) []fleethealth.DependencyHealth {
	return []fleethealth.DependencyHealth{
		delivererMySQLHealth(ctx, sqlDB, checkedAt),
		delivererDBConfigHealth(checkedAt),
		delivererStatsDHealth(statsdReady, checkedAt),
	}
}

func delivererMySQLHealth(ctx context.Context, sqlDB *sql.DB, checkedAt time.Time) fleethealth.DependencyHealth {
	entry := fleethealth.DependencyHealth{Name: "mysql", CheckedAt: checkedAt}
	if sqlDB == nil {
		entry.Status = "red"
		entry.LastError = "database pool is not initialized"
		return entry
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		entry.Status = "red"
		entry.LatencyMS = time.Since(start).Milliseconds()
		entry.LastError = err.Error()
		return entry
	}
	entry.Status = "green"
	entry.LatencyMS = time.Since(start).Milliseconds()
	return entry
}

func delivererDBConfigHealth(checkedAt time.Time) fleethealth.DependencyHealth {
	status := db.ConfigStatusSnapshot()
	entry := fleethealth.DependencyHealth{
		Name:      "db-config",
		Status:    fleethealth.HealthGreen,
		CheckedAt: checkedAt,
		Details:   status.Details(),
	}
	switch {
	case status.Mode == "uninitialized":
		entry.Status = fleethealth.HealthRed
		entry.LastError = "database manager is not initialized"
	case status.LastReloadError != "":
		entry.Status = fleethealth.HealthAmber
		entry.LastError = status.LastReloadError
	case status.Mode == "server_map" && status.ReloadEnabled && status.NextCheckAt == nil:
		entry.Status = fleethealth.HealthAmber
		entry.LastError = "server-map reload is enabled but next check is not scheduled"
	}
	return entry
}

func delivererStatsDHealth(ready bool, checkedAt time.Time) fleethealth.DependencyHealth {
	entry := fleethealth.DependencyHealth{Name: "statsd", CheckedAt: checkedAt}
	if !ready {
		entry.Status = "amber"
		entry.LastError = "statsd client is not initialized"
		return entry
	}
	entry.Status = "green"
	return entry
}

type shutdownRequest struct {
	Signal  os.Signal
	Restart bool
}

func waitForShutdown() shutdownRequest {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	sig := <-sigCh
	req := shutdownRequest{Signal: sig, Restart: processcontrol.IsGracefulRestartSignal(sig)}
	if req.Restart {
		log.Printf("received %s, draining before graceful restart", sig)
	} else {
		log.Printf("received %s, stopping", sig)
	}
	return req
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

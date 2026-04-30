package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Automattic/jetmon/internal/alerting"
	"github.com/Automattic/jetmon/internal/api"
	"github.com/Automattic/jetmon/internal/apikeys"
	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/dashboard"
	"github.com/Automattic/jetmon/internal/db"
	"github.com/Automattic/jetmon/internal/deliverer"
	"github.com/Automattic/jetmon/internal/fleethealth"
	"github.com/Automattic/jetmon/internal/metrics"
	"github.com/Automattic/jetmon/internal/orchestrator"
	"github.com/Automattic/jetmon/internal/veriflier"
	"github.com/Automattic/jetmon/internal/wpcom"
)

const processHealthWriteTimeout = 2 * time.Second

// Injected at build time via -ldflags.
var (
	version   = "dev"
	buildDate = "unknown"
	goVersion = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		runServe()
		return
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("jetmon2 %s (built %s with %s)\n", version, buildDate, goVersion)
	case "migrate":
		cmdMigrate()
	case "validate-config":
		cmdValidateConfig()
	case "status":
		cmdStatus()
	case "audit":
		cmdAudit()
	case "drain":
		cmdDrain()
	case "reload":
		cmdReload()
	case "keys":
		cmdKeys(os.Args[2:])
	case "api":
		cmdAPI(os.Args[2:])
	case "site-tenants":
		cmdSiteTenants(os.Args[2:])
	case "rollout":
		cmdRollout(os.Args[2:])
	default:
		runServe()
	}
}

// runServe is the main entry point for the monitoring service.
func runServe() {
	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")

	if err := config.Load(configPath); err != nil {
		log.Fatalf("load config: %v", err)
	}
	cfg := config.Get()
	log.Printf("config: legacy_status_projection=%s", enabledLabel(cfg.LegacyStatusProjectionEnable))
	log.Printf("config: bucket_ownership=%s", bucketOwnershipLabel(cfg))
	log.Printf("config: email_transport=%s", emailTransportLabel(cfg))
	if !emailTransportDelivers(cfg) {
		log.Printf("WARN: email_transport=%s — alert-contact emails will be logged but not delivered", emailTransportLabel(cfg))
	}
	if cfg.DashboardPort > 0 {
		if msg := dashboardBindWarning(cfg.DashboardBindAddr); msg != "" {
			log.Printf("WARN: %s", msg)
		}
	}

	config.LoadDB()
	if err := db.ConnectWithRetry(10); err != nil {
		log.Fatalf("db connect: %v", err)
	}

	pidPath := envOrDefault("JETMON_PID_FILE", "/run/jetmon2/jetmon2.pid")
	if err := writePIDFile(pidPath); err != nil {
		log.Printf("warning: could not write PID file %s: %v", pidPath, err)
	} else {
		defer removePIDFile(pidPath)
	}

	audit.Init(db.DB())

	if err := metrics.Init("statsd:8125", db.Hostname()); err != nil {
		log.Printf("warning: statsd init failed: %v", err)
	}

	hostname := db.Hostname()
	processStartedAt := time.Now().UTC()
	processID := fleethealth.ProcessID(hostname, fleethealth.ProcessMonitor)

	wp := wpcom.New(cfg.AuthToken, hostname)

	orch := orchestrator.New(cfg, wp)
	if err := orch.ClaimBuckets(); err != nil {
		log.Fatalf("claim buckets: %v", err)
	}

	var dash *dashboard.Server
	if cfg.DashboardPort > 0 {
		dash = dashboard.New(hostname)
		dash.SetFleetSource(dashboard.NewFleetStore(db.DB(), dashboard.FleetStoreOptions{
			BucketTotal:    cfg.BucketTotal,
			HeartbeatGrace: time.Duration(cfg.BucketHeartbeatGraceSec) * time.Second,
		}))
		go func() {
			addr := dashboardListenAddr(cfg)
			if err := dash.Listen(addr); err != nil {
				log.Printf("dashboard: %v", err)
			}
		}()
	}

	// pprof on localhost only — never expose this on a public interface.
	if cfg.DebugPort > 0 {
		go func() {
			addr := fmt.Sprintf("127.0.0.1:%d", cfg.DebugPort)
			if err := dashboard.ListenDebug(addr); err != nil {
				log.Printf("debug server: %v", err)
			}
		}()
	}

	// Internal API server. Disabled when API_PORT is 0. Bears auth via
	// jetmon_api_keys; key management is CLI-only (`./jetmon2 keys`).
	var apiSrv *api.Server
	if cfg.APIPort > 0 {
		apiSrv = api.New(fmt.Sprintf(":%d", cfg.APIPort), db.DB(), hostname)
		go func() {
			if err := apiSrv.Listen(); err != nil && !api.IsServerClosed(err) {
				log.Printf("api: %v", err)
			}
		}()
	}

	if level, msg := deliveryOwnerStatus(cfg, hostname); msg != "" {
		if level == "WARN" {
			log.Printf("WARN: %s", msg)
		} else {
			log.Printf("config: %s", msg)
		}
	}
	deliveryWorkersEnabled := deliveryWorkersShouldStart(cfg, hostname)

	var alertDispatchers map[alerting.Transport]alerting.Dispatcher
	if cfg.APIPort > 0 {
		alertDispatchers = deliverer.BuildAlertDispatchers(cfg)
		if apiSrv != nil {
			apiSrv.SetAlertDispatchers(alertDispatchers)
		}
	}

	// Embedded outbound delivery workers. Disabled when API_PORT is 0
	// (no API to manage webhooks or alert contacts) or when
	// DELIVERY_OWNER_HOST names another host.
	var deliveryRuntime *deliverer.Runtime
	if deliveryWorkersEnabled {
		deliveryRuntime = deliverer.Start(deliverer.Config{
			DB:          db.DB(),
			InstanceID:  hostname,
			Dispatchers: alertDispatchers,
		})
	}

	var healthMu sync.RWMutex
	var publishMu sync.Mutex
	var shuttingDown atomic.Bool
	var lastHealth []dashboard.HealthEntry
	publishHostSnapshot := func(state string, refreshDependencies bool) {
		publishMu.Lock()
		defer publishMu.Unlock()
		if shuttingDown.Load() && state == fleethealth.StateRunning {
			return
		}
		currentCfg := config.Get()
		if currentCfg == nil {
			currentCfg = cfg
		}
		checkedAt := time.Now().UTC()
		var health []dashboard.HealthEntry
		if refreshDependencies {
			health = dashboardHealthEntries(context.Background(), currentCfg, db.DB(), wp, metrics.Global() != nil, checkedAt)
			healthMu.Lock()
			lastHealth = append([]dashboard.HealthEntry(nil), health...)
			healthMu.Unlock()
		} else {
			healthMu.RLock()
			health = append([]dashboard.HealthEntry(nil), lastHealth...)
			healthMu.RUnlock()
		}
		bMin, bMax := orch.BucketRange()
		sitesPerSec, roundDuration := orch.LastRoundStats()
		goSysMemMB := currentGoSysMemMB()
		deliveryConfigEligible := deliveryWorkersShouldStart(currentCfg, hostname)
		st := dashboard.State{
			WorkerCount:                   orch.WorkerCount(),
			ActiveChecks:                  orch.ActiveChecks(),
			QueueDepth:                    orch.QueueDepth(),
			RetryQueueSize:                orch.RetryQueueSize(),
			SitesPerSec:                   sitesPerSec,
			RoundDurationMs:               roundDuration.Milliseconds(),
			WPCOMCircuitOpen:              wp.IsCircuitOpen(),
			WPCOMQueueDepth:               wp.QueueDepth(),
			GoSysMemMB:                    goSysMemMB,
			BucketMin:                     bMin,
			BucketMax:                     bMax,
			BucketOwnership:               bucketOwnershipLabel(currentCfg),
			LegacyStatusProjectionEnabled: currentCfg.LegacyStatusProjectionEnable,
			DeliveryWorkersEnabled:        deliveryWorkersEnabled,
			DeliveryConfigEligible:        deliveryConfigEligible,
			DeliveryOwnerHost:             currentCfg.DeliveryOwnerHost,
			RolloutPreflightCommand:       rolloutPreflightCommand(currentCfg),
			RolloutCutoverCommand:         cutoverCheckCommand(currentCfg),
			RolloutActivityCommand:        rolloutActivityCommand(),
			RolloutRollbackCommand:        rollbackCheckCommand(currentCfg),
			RolloutStateReportCommand:     stateReportCommand(),
			ProjectionDriftCommand:        projectionDriftCommand(),
		}
		st.Hostname = hostname
		st.UpdatedAt = checkedAt
		if dash != nil {
			if refreshDependencies {
				dash.UpdateHealth(health)
			}
			dash.Update(st)
		}
		ctx, cancel := context.WithTimeout(context.Background(), processHealthWriteTimeout)
		if err := fleethealth.Upsert(ctx, db.DB(), monitorProcessHealthSnapshot(hostname, processStartedAt, state, currentCfg, st, health)); err != nil {
			log.Printf("process health: %v", err)
		}
		cancel()
	}

	// Publish both host-dashboard state and the durable fleet-health heartbeat.
	publishHostSnapshot(fleethealth.StateRunning, false)
	stopHostPublisher := make(chan struct{})
	var stopHostPublisherOnce sync.Once
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.StatsUpdateIntervalMS) * time.Millisecond)
		defer ticker.Stop()
		publishHostSnapshot(fleethealth.StateRunning, true)
		for {
			select {
			case <-ticker.C:
				publishHostSnapshot(fleethealth.StateRunning, true)
			case <-stopHostPublisher:
				return
			}
		}
	}()

	// Signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGHUP:
				log.Println("received SIGHUP, reloading config")
				if err := config.Reload(); err != nil {
					log.Printf("config reload failed: %v", err)
				} else {
					log.Println("config reloaded")
				}
			case syscall.SIGINT, syscall.SIGTERM:
				log.Println("received shutdown signal, draining")
				shuttingDown.Store(true)
				stopHostPublisherOnce.Do(func() { close(stopHostPublisher) })
				publishHostSnapshot(fleethealth.StateStopping, false)
				if apiSrv != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					if err := apiSrv.Shutdown(ctx); err != nil {
						log.Printf("api: shutdown error: %v", err)
					}
					cancel()
				}
				if deliveryRuntime != nil {
					deliveryRuntime.Stop()
				}
				orch.Stop()
				// Hard kill if drain takes too long (e.g. a stalled HTTP check).
				time.AfterFunc(30*time.Second, func() {
					log.Println("jetmon2: shutdown timeout exceeded, forcing exit")
					os.Exit(1)
				})
			}
		}
	}()

	orch.Run()
	shuttingDown.Store(true)
	stopHostPublisherOnce.Do(func() { close(stopHostPublisher) })
	publishHostSnapshot(fleethealth.StateStopping, false)
	ctx, cancel := context.WithTimeout(context.Background(), processHealthWriteTimeout)
	if err := fleethealth.MarkStopped(ctx, db.DB(), processID, time.Now().UTC()); err != nil {
		log.Printf("process health: %v", err)
	}
	cancel()
	log.Println("jetmon2: shutdown complete")
}

func cmdMigrate() {
	config.LoadDB()
	if err := db.ConnectWithRetry(5); err != nil {
		log.Fatalf("db connect: %v", err)
	}
	if err := db.Migrate(); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	fmt.Println("migrations applied successfully")
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
	fmt.Printf("INFO legacy_status_projection=%s\n", enabledLabel(cfg.LegacyStatusProjectionEnable))
	fmt.Printf("INFO bucket_ownership=%s\n", bucketOwnershipLabel(cfg))
	for _, line := range rolloutAdviceLines(cfg) {
		fmt.Println(line)
	}
	fmt.Printf("INFO email_transport=%s\n", emailTransportLabel(cfg))
	if !emailTransportDelivers(cfg) {
		fmt.Printf("WARN email_transport=%s — alert-contact emails will be logged but not delivered\n", emailTransportLabel(cfg))
	}
	if cfg.DashboardPort > 0 {
		if msg := dashboardBindWarning(cfg.DashboardBindAddr); msg != "" {
			fmt.Printf("WARN %s\n", msg)
		}
	}
	if level, msg := deliveryOwnerStatus(cfg, db.Hostname()); msg != "" {
		fmt.Printf("%s %s\n", level, msg)
	}
	for _, v := range cfg.Verifiers {
		addr := fmt.Sprintf("%s:%s", v.Host, v.TransportPort())
		// Listing configured Verifliers is operator context, not a reachability check.
		fmt.Printf("INFO veriflier %q at %s\n", v.Name, addr)
	}

	fmt.Println("\nvalidation passed")
}

func enabledLabel(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func bucketOwnershipLabel(cfg *config.Config) string {
	if min, max, ok := cfg.PinnedBucketRange(); ok {
		return fmt.Sprintf("pinned range=%d-%d", min, max)
	}
	return "dynamic jetmon_hosts"
}

func rolloutAdviceLines(cfg *config.Config) []string {
	lines := []string{}
	if _, _, ok := cfg.PinnedBucketRange(); ok {
		lines = append(lines, "INFO rollout_static_plan="+staticPlanCheckCommand())
	}
	lines = append(lines,
		"INFO rollout_preflight="+rolloutPreflightCommand(cfg),
		"INFO rollout_activity_check="+rolloutActivityCommand(),
	)
	if cmd := cutoverCheckCommand(cfg); cmd != "" {
		lines = append(lines, "INFO rollout_cutover_check="+cmd)
	}
	if cmd := rollbackCheckCommand(cfg); cmd != "" {
		lines = append(lines, "INFO rollout_rollback_check="+cmd)
	}
	lines = append(lines, "INFO rollout_state_report="+stateReportCommand())
	lines = append(lines, "INFO rollout_drift_report="+projectionDriftCommand())
	return lines
}

func staticPlanCheckCommand() string {
	return "./jetmon2 rollout static-plan-check --file=<ranges.csv>"
}

func rolloutPreflightCommand(cfg *config.Config) string {
	if minBucket, maxBucket, ok := cfg.PinnedBucketRange(); ok {
		cmd := fmt.Sprintf("./jetmon2 rollout host-preflight --file=<ranges.csv> --host=<v1-hostname> --runtime-host=<v2-hostname> --bucket-min=%d --bucket-max=%d", minBucket, maxBucket)
		if cfg.BucketTotal > 0 {
			cmd += fmt.Sprintf(" --bucket-total=%d", cfg.BucketTotal)
		}
		return cmd
	}
	return "./jetmon2 rollout dynamic-check"
}

func rolloutActivityCommand() string {
	return "./jetmon2 rollout activity-check --since=15m"
}

func cutoverCheckCommand(cfg *config.Config) string {
	if _, _, ok := cfg.PinnedBucketRange(); ok {
		return "./jetmon2 rollout cutover-check --since=15m"
	}
	return ""
}

func rollbackCheckCommand(cfg *config.Config) string {
	if _, _, ok := cfg.PinnedBucketRange(); ok {
		return "./jetmon2 rollout rollback-check"
	}
	return ""
}

func projectionDriftCommand() string {
	return "./jetmon2 rollout projection-drift"
}

func stateReportCommand() string {
	return "./jetmon2 rollout state-report --since=15m"
}

func dashboardListenAddr(cfg *config.Config) string {
	bindAddr := "127.0.0.1"
	port := 0
	if cfg != nil {
		if strings.TrimSpace(cfg.DashboardBindAddr) != "" {
			bindAddr = strings.TrimSpace(cfg.DashboardBindAddr)
		}
		port = cfg.DashboardPort
	}
	return net.JoinHostPort(bindAddr, strconv.Itoa(port))
}

func dashboardBindWarning(bindAddr string) string {
	bindAddr = strings.TrimSpace(bindAddr)
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	host := strings.Trim(bindAddr, "[]")
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	return fmt.Sprintf("DASHBOARD_BIND_ADDR=%q exposes the unauthenticated host dashboard; restrict access to trusted operator networks", bindAddr)
}

const dashboardHealthTimeout = 2 * time.Second

func dashboardHealthEntries(ctx context.Context, cfg *config.Config, sqlDB *sql.DB, wp *wpcom.Client, statsdReady bool, checkedAt time.Time) []dashboard.HealthEntry {
	entries := []dashboard.HealthEntry{
		mysqlHealthEntry(ctx, sqlDB, checkedAt),
		wpcomHealthEntry(wp, checkedAt),
		statsdHealthEntry(statsdReady, checkedAt),
		diskHealthEntry("logs", checkedAt),
		diskHealthEntry("stats", checkedAt),
	}
	entries = append(entries, veriflierHealthEntries(ctx, cfg, checkedAt)...)
	return entries
}

func monitorProcessHealthSnapshot(hostname string, startedAt time.Time, state string, cfg *config.Config, st dashboard.State, health []dashboard.HealthEntry) fleethealth.Snapshot {
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = time.Now().UTC()
	}
	bucketMin, bucketMax := st.BucketMin, st.BucketMax
	apiPort, dashboardPort := cfg.APIPort, cfg.DashboardPort
	healthStatus := dashboard.SummarizeHost(st, health).Status
	if state == fleethealth.StateStopping || state == fleethealth.StateStopped {
		healthStatus = fleethealth.HealthAmber
	}
	return fleethealth.Snapshot{
		HostID:                 hostname,
		ProcessType:            fleethealth.ProcessMonitor,
		PID:                    os.Getpid(),
		Version:                version,
		BuildDate:              buildDate,
		GoVersion:              goVersion,
		State:                  state,
		HealthStatus:           healthStatus,
		StartedAt:              startedAt,
		UpdatedAt:              time.Now().UTC(),
		BucketMin:              &bucketMin,
		BucketMax:              &bucketMax,
		BucketOwnership:        st.BucketOwnership,
		APIPort:                &apiPort,
		DashboardPort:          &dashboardPort,
		DeliveryWorkersEnabled: st.DeliveryWorkersEnabled,
		DeliveryOwnerHost:      st.DeliveryOwnerHost,
		WorkerCount:            st.WorkerCount,
		ActiveChecks:           st.ActiveChecks,
		QueueDepth:             st.QueueDepth,
		RetryQueueSize:         st.RetryQueueSize,
		WPCOMCircuitOpen:       st.WPCOMCircuitOpen,
		WPCOMQueueDepth:        st.WPCOMQueueDepth,
		GoSysMemMB:             st.GoSysMemMB,
		DependencyHealth:       dashboardHealthToFleet(health),
	}
}

func dashboardHealthToFleet(entries []dashboard.HealthEntry) []fleethealth.DependencyHealth {
	out := make([]fleethealth.DependencyHealth, 0, len(entries))
	for _, entry := range entries {
		out = append(out, fleethealth.DependencyHealth{
			Name:      entry.Name,
			Status:    entry.Status,
			LatencyMS: entry.Latency,
			LastError: entry.LastError,
			CheckedAt: entry.CheckedAt,
		})
	}
	return out
}

func currentGoSysMemMB() int {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int(ms.Sys / 1024 / 1024)
}

func mysqlHealthEntry(ctx context.Context, sqlDB *sql.DB, checkedAt time.Time) dashboard.HealthEntry {
	entry := dashboard.HealthEntry{Name: "mysql", CheckedAt: checkedAt}
	if sqlDB == nil {
		entry.Status = "red"
		entry.LastError = "database pool is not initialized"
		return entry
	}

	pingCtx, cancel := context.WithTimeout(ctx, dashboardHealthTimeout)
	defer cancel()

	start := time.Now()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		entry.Status = "red"
		entry.Latency = time.Since(start).Milliseconds()
		entry.LastError = err.Error()
		return entry
	}
	entry.Status = "green"
	entry.Latency = time.Since(start).Milliseconds()
	return entry
}

func veriflierHealthEntries(ctx context.Context, cfg *config.Config, checkedAt time.Time) []dashboard.HealthEntry {
	if cfg == nil || len(cfg.Verifiers) == 0 {
		return []dashboard.HealthEntry{{
			Name:      "verifliers",
			Status:    "amber",
			LastError: "no verifliers configured",
			CheckedAt: checkedAt,
		}}
	}

	entries := make([]dashboard.HealthEntry, 0, len(cfg.Verifiers))
	for _, v := range cfg.Verifiers {
		addr := fmt.Sprintf("%s:%s", v.Host, v.TransportPort())
		name := "veriflier:" + v.Name
		if v.Name == "" {
			name = "veriflier:" + addr
		}
		entry := dashboard.HealthEntry{Name: name, CheckedAt: checkedAt}
		if v.Host == "" || v.TransportPort() == "" {
			entry.Status = "red"
			entry.LastError = "host or port is not configured"
			entries = append(entries, entry)
			continue
		}

		pingCtx, cancel := context.WithTimeout(ctx, dashboardHealthTimeout)
		start := time.Now()
		version, err := veriflier.NewVeriflierClient(addr, v.AuthToken).Ping(pingCtx)
		cancel()
		entry.Latency = time.Since(start).Milliseconds()
		if err != nil {
			entry.Status = "red"
			entry.LastError = err.Error()
		} else {
			entry.Status = "green"
			if version != "" {
				entry.Name = fmt.Sprintf("%s (%s)", entry.Name, version)
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func wpcomHealthEntry(wp *wpcom.Client, checkedAt time.Time) dashboard.HealthEntry {
	entry := dashboard.HealthEntry{Name: "wpcom", CheckedAt: checkedAt}
	if wp == nil {
		entry.Status = "red"
		entry.LastError = "wpcom client is not initialized"
		return entry
	}
	queueDepth := wp.QueueDepth()
	if wp.IsCircuitOpen() {
		entry.Status = "red"
		entry.LastError = fmt.Sprintf("circuit open, queued notifications=%d", queueDepth)
		return entry
	}
	if queueDepth > 0 {
		entry.Status = "amber"
		entry.LastError = fmt.Sprintf("queued notifications=%d", queueDepth)
		return entry
	}
	entry.Status = "green"
	return entry
}

func statsdHealthEntry(ready bool, checkedAt time.Time) dashboard.HealthEntry {
	entry := dashboard.HealthEntry{Name: "statsd", CheckedAt: checkedAt}
	if !ready {
		entry.Status = "amber"
		entry.LastError = "statsd client is not initialized"
		return entry
	}
	entry.Status = "green"
	return entry
}

func diskHealthEntry(dir string, checkedAt time.Time) dashboard.HealthEntry {
	entry := dashboard.HealthEntry{Name: "disk:" + dir, CheckedAt: checkedAt}
	if err := checkWritableDir(dir); err != nil {
		entry.Status = "red"
		entry.LastError = err.Error()
		return entry
	}
	entry.Status = "green"
	return entry
}

func checkWritableDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	f, err := os.CreateTemp(dir, ".jetmon-health-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	return nil
}

// emailTransportLabel collapses an empty EMAIL_TRANSPORT to its compatibility
// alias ("stub") so startup output and validate-config show a single canonical
// name regardless of which form an operator wrote in config.
func emailTransportLabel(cfg *config.Config) string {
	if cfg.EmailTransport == "" {
		return "stub"
	}
	return cfg.EmailTransport
}

// emailTransportDelivers reports whether the configured email transport
// actually delivers mail. The stub transport (and the empty-string alias for
// it) only logs, so any alert-contact configured with transport="email" will
// silently disappear into the logs in that mode.
func emailTransportDelivers(cfg *config.Config) bool {
	return cfg.EmailTransport == "smtp" || cfg.EmailTransport == "wpcom"
}

func deliveryWorkersShouldStart(cfg *config.Config, hostname string) bool {
	if cfg.APIPort <= 0 {
		return false
	}
	owner := strings.TrimSpace(cfg.DeliveryOwnerHost)
	return owner == "" || owner == hostname
}

func deliveryOwnerStatus(cfg *config.Config, hostname string) (string, string) {
	owner := strings.TrimSpace(cfg.DeliveryOwnerHost)
	if cfg.APIPort <= 0 {
		if owner == "" {
			return "INFO", "delivery_workers=disabled api_port=disabled"
		}
		return "INFO", fmt.Sprintf("delivery_owner_host=%q ignored because API_PORT is disabled", owner)
	}
	if owner == "" {
		return "WARN", fmt.Sprintf("delivery_owner_host is unset; host %q will run delivery workers because API_PORT is enabled", hostname)
	}
	if owner == hostname {
		return "INFO", fmt.Sprintf("delivery_owner_host=%q matched; delivery workers enabled on this host", owner)
	}
	return "INFO", fmt.Sprintf("delivery_owner_host=%q; delivery workers disabled on host %q", owner, hostname)
}

func cmdStatus() {
	// Connect to the running instance's internal API.
	port := envOrDefault("DASHBOARD_PORT", "8080")
	host := envOrDefault("DASHBOARD_HOST", envOrDefault("DASHBOARD_BIND_ADDR", "localhost"))
	if host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	resp, err := httpGet(fmt.Sprintf("http://%s/api/state", net.JoinHostPort(host, port)))
	if err != nil {
		log.Fatalf("status: %v", err)
	}
	fmt.Println(resp)
}

func cmdAudit() {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	blogID := fs.Int64("blog-id", 0, "blog ID to query")
	since := fs.String("since", "", "start time (RFC3339 or duration like 24h)")
	until := fs.String("until", "", "end time (RFC3339)")
	_ = fs.Parse(os.Args[2:])

	if *blogID == 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 audit --blog-id <id> [--since <time>] [--until <time>]")
		os.Exit(1)
	}

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		log.Fatalf("db: %v", err)
	}

	sinceStr := resolveSince(*since)
	rows, err := audit.Query(db.DB(), *blogID, sinceStr, *until)
	if err != nil {
		log.Fatalf("query: %v", err)
	}
	defer rows.Close()

	fmt.Printf("Audit log for blog_id=%d\n", *blogID)
	fmt.Printf("%-25s %-22s %-15s %s\n", "TIMESTAMP", "EVENT", "SOURCE", "DETAIL")
	fmt.Println(strings.Repeat("-", 90))

	for rows.Next() {
		var (
			id        int64
			bid       sql.NullInt64
			eventID   sql.NullInt64
			eventType string
			source    string
			detail    sql.NullString
			metadata  sql.NullString
			createdAt time.Time
		)
		if err := rows.Scan(&id, &bid, &eventID, &eventType, &source,
			&detail, &metadata, &createdAt); err != nil {
			log.Printf("scan: %v", err)
			continue
		}
		det := ""
		if detail.Valid {
			det = detail.String
		}
		if eventID.Valid {
			det = fmt.Sprintf("event=%d %s", eventID.Int64, det)
		}
		if metadata.Valid && metadata.String != "" {
			det = fmt.Sprintf("%s meta=%s", det, metadata.String)
		}
		fmt.Printf("%-25s %-22s %-15s %s\n",
			createdAt.Format("2006-01-02 15:04:05.000"),
			eventType, source, det)
	}
}

func cmdDrain() {
	pid := readPIDFile()
	proc, err := os.FindProcess(pid)
	if err != nil {
		log.Fatalf("find process %d: %v", pid, err)
	}
	if err := proc.Signal(syscall.SIGINT); err != nil {
		log.Fatalf("signal: %v", err)
	}
	fmt.Printf("SIGINT sent to pid %d — jetmon2 will drain and exit\n", pid)
}

func cmdReload() {
	pid := readPIDFile()
	proc, err := os.FindProcess(pid)
	if err != nil {
		log.Fatalf("find process %d: %v", pid, err)
	}
	if err := proc.Signal(syscall.SIGHUP); err != nil {
		log.Fatalf("signal: %v", err)
	}
	fmt.Printf("SIGHUP sent to pid %d\n", pid)
}

// cmdKeys is the entrypoint for `./jetmon2 keys ...` ops commands. Key
// management is intentionally CLI-only — the public API has no /keys
// endpoints. See docs/internal-api-reference.md "Authentication".
func cmdKeys(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 keys <create|list|revoke|rotate> [args]")
		os.Exit(1)
	}
	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		log.Fatalf("db: %v", err)
	}
	ctx := context.Background()

	sub := args[0]
	rest := args[1:]
	switch sub {
	case "create":
		cmdKeysCreate(ctx, rest)
	case "list":
		cmdKeysList(ctx, rest)
	case "revoke":
		cmdKeysRevoke(ctx, rest)
	case "rotate":
		cmdKeysRotate(ctx, rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown keys subcommand %q (want: create, list, revoke, rotate)\n", sub)
		os.Exit(1)
	}
}

func cmdKeysCreate(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("keys create", flag.ExitOnError)
	consumer := fs.String("consumer", "", "consumer name (e.g. 'gateway', 'alerts-worker') — required")
	scopeStr := fs.String("scope", "read", "permission scope: read | write | admin")
	rateLimit := fs.Int("rate-limit", 0, "requests per minute (0 = scope default)")
	ttl := fs.Duration("ttl", 0, "key lifetime (e.g. 90d, 720h); 0 = never expires")
	createdBy := fs.String("created-by", currentOperator(), "operator identity for audit")
	_ = fs.Parse(args)

	if *consumer == "" {
		fmt.Fprintln(os.Stderr, "--consumer is required")
		os.Exit(1)
	}

	raw, k, err := apikeys.Create(ctx, db.DB(), apikeys.CreateInput{
		ConsumerName:       *consumer,
		Scope:              apikeys.Scope(*scopeStr),
		RateLimitPerMinute: *rateLimit,
		TTL:                *ttl,
		CreatedBy:          *createdBy,
	})
	if err != nil {
		log.Fatalf("create: %v", err)
	}

	fmt.Printf("Created key id=%d for consumer=%q scope=%s rate=%d/min\n",
		k.ID, k.ConsumerName, k.Scope, k.RateLimitPerMinute)
	if k.ExpiresAt != nil {
		fmt.Printf("Expires: %s\n", k.ExpiresAt.UTC().Format(time.RFC3339))
	} else {
		fmt.Println("Expires: never")
	}
	fmt.Println()
	fmt.Println("Token (shown ONCE — save it now):")
	fmt.Println(raw)
}

func cmdKeysList(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("keys list", flag.ExitOnError)
	includeRevoked := fs.Bool("include-revoked", false, "show revoked keys too")
	_ = fs.Parse(args)

	keys, err := apikeys.List(ctx, db.DB())
	if err != nil {
		log.Fatalf("list: %v", err)
	}

	fmt.Printf("%-5s %-24s %-7s %-9s %-21s %-21s %s\n",
		"ID", "CONSUMER", "SCOPE", "RATE/MIN", "EXPIRES", "LAST USED", "STATUS")
	fmt.Println(strings.Repeat("-", 110))
	for _, k := range keys {
		status := "active"
		if k.RevokedAt != nil {
			if !*includeRevoked && k.RevokedAt.Before(time.Now().UTC()) {
				continue
			}
			if k.RevokedAt.After(time.Now().UTC()) {
				status = "revokes-at " + k.RevokedAt.UTC().Format("2006-01-02T15:04:05Z")
			} else {
				status = "revoked"
			}
		} else if k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now().UTC()) {
			status = "expired"
		}
		expires := "never"
		if k.ExpiresAt != nil {
			expires = k.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		lastUsed := "never"
		if k.LastUsedAt != nil {
			lastUsed = k.LastUsedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		fmt.Printf("%-5d %-24s %-7s %-9d %-21s %-21s %s\n",
			k.ID, k.ConsumerName, k.Scope, k.RateLimitPerMinute, expires, lastUsed, status)
	}
}

func cmdKeysRevoke(ctx context.Context, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 keys revoke <id>")
		os.Exit(1)
	}
	id, err := parseInt64(args[0])
	if err != nil {
		log.Fatalf("invalid id %q: %v", args[0], err)
	}
	if err := apikeys.Revoke(ctx, db.DB(), id); err != nil {
		log.Fatalf("revoke: %v", err)
	}
	fmt.Printf("Revoked key id=%d\n", id)
}

func cmdKeysRotate(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("keys rotate", flag.ExitOnError)
	grace := fs.Duration("grace", 5*time.Minute, "grace period before old key is revoked (0 = revoke immediately)")
	createdBy := fs.String("created-by", currentOperator(), "operator identity for audit")
	_ = fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 keys rotate [--grace=DURATION] <id>")
		os.Exit(1)
	}
	id, err := parseInt64(fs.Arg(0))
	if err != nil {
		log.Fatalf("invalid id %q: %v", fs.Arg(0), err)
	}

	raw, k, err := apikeys.Rotate(ctx, db.DB(), id, *grace, *createdBy)
	if err != nil {
		log.Fatalf("rotate: %v", err)
	}
	fmt.Printf("Rotated key id=%d → new key id=%d for consumer=%q\n", id, k.ID, k.ConsumerName)
	if *grace > 0 {
		fmt.Printf("Old key id=%d will be revoked at %s\n", id, time.Now().UTC().Add(*grace).Format(time.RFC3339))
	} else {
		fmt.Printf("Old key id=%d revoked immediately\n", id)
	}
	fmt.Println()
	fmt.Println("New token (shown ONCE — save it now):")
	fmt.Println(raw)
}

func currentOperator() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("LOGNAME"); u != "" {
		return u
	}
	return "cli"
}

func parseInt64(s string) (int64, error) {
	var v int64
	_, err := fmt.Sscan(s, &v)
	return v, err
}

func readPIDFile() int {
	pidPath := envOrDefault("JETMON_PID_FILE", "/run/jetmon2/jetmon2.pid")
	data, err := os.ReadFile(pidPath)
	if err != nil {
		log.Fatalf("read pid file %s: %v (is jetmon2 running?)", pidPath, err)
	}
	var pid int
	if _, err := fmt.Sscan(string(data), &pid); err != nil || pid <= 0 {
		log.Fatalf("invalid pid in %s", pidPath)
	}
	return pid
}

func writePIDFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, fmt.Appendf(nil, "%d\n", os.Getpid()), 0644)
}

func removePIDFile(path string) {
	_ = os.Remove(path)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func httpGet(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func resolveSince(s string) string {
	if s == "" {
		return ""
	}
	d, err := time.ParseDuration(s)
	if err == nil {
		return time.Now().Add(-d).Format("2006-01-02 15:04:05")
	}
	return s
}

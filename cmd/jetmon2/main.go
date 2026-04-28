package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	"github.com/Automattic/jetmon/internal/metrics"
	"github.com/Automattic/jetmon/internal/orchestrator"
	"github.com/Automattic/jetmon/internal/wpcom"
)

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

	wp := wpcom.New(cfg.AuthToken, db.Hostname())

	orch := orchestrator.New(cfg, wp)
	if err := orch.ClaimBuckets(); err != nil {
		log.Fatalf("claim buckets: %v", err)
	}

	var dash *dashboard.Server
	if cfg.DashboardPort > 0 {
		dash = dashboard.New(db.Hostname())
		go func() {
			addr := fmt.Sprintf(":%d", cfg.DashboardPort)
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
		apiSrv = api.New(fmt.Sprintf(":%d", cfg.APIPort), db.DB(), db.Hostname())
		go func() {
			if err := apiSrv.Listen(); err != nil && !api.IsServerClosed(err) {
				log.Printf("api: %v", err)
			}
		}()
	}

	if level, msg := deliveryOwnerStatus(cfg, db.Hostname()); msg != "" {
		if level == "WARN" {
			log.Printf("WARN: %s", msg)
		} else {
			log.Printf("config: %s", msg)
		}
	}
	deliveryWorkersEnabled := deliveryWorkersShouldStart(cfg, db.Hostname())

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
			InstanceID:  db.Hostname(),
			Dispatchers: alertDispatchers,
		})
	}

	// Push dashboard state every stats interval.
	if dash != nil {
		go func() {
			ticker := time.NewTicker(time.Duration(cfg.StatsUpdateIntervalMS) * time.Millisecond)
			defer ticker.Stop()
			for range ticker.C {
				bMin, bMax := orch.BucketRange()
				dash.Update(dashboard.State{
					WorkerCount:      orch.WorkerCount(),
					ActiveChecks:     orch.ActiveChecks(),
					QueueDepth:       orch.QueueDepth(),
					RetryQueueSize:   orch.RetryQueueSize(),
					SitesPerSec:      0,
					WPCOMCircuitOpen: wp.IsCircuitOpen(),
					WPCOMQueueDepth:  wp.QueueDepth(),
					BucketMin:        bMin,
					BucketMax:        bMax,
				})
			}
		}()
	}

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
	if _, _, ok := cfg.PinnedBucketRange(); ok {
		return []string{
			"INFO rollout_preflight=./jetmon2 rollout pinned-check",
			"INFO rollout_drift_report=./jetmon2 rollout projection-drift",
		}
	}
	return []string{
		"INFO rollout_preflight=./jetmon2 rollout dynamic-check",
		"INFO rollout_drift_report=./jetmon2 rollout projection-drift",
	}
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
	resp, err := httpGet(fmt.Sprintf("http://localhost:%s/api/state", port))
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
// endpoints. See API.md "Authentication".
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

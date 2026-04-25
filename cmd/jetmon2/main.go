package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Automattic/jetmon/internal/audit"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/dashboard"
	"github.com/Automattic/jetmon/internal/db"
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

	if cfg.DBUpdatesEnable && os.Getenv("JETMON_UNSAFE_DB_UPDATES") != "1" {
		log.Fatalf("DB_UPDATES_ENABLE is true but JETMON_UNSAFE_DB_UPDATES=1 is not set — refusing to start. This setting must only be used in local test environments.")
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
	for _, v := range cfg.Verifiers {
		addr := fmt.Sprintf("%s:%s", v.Host, v.GRPCPort)
		// Ping check is best-effort; don't fail validation on veriflier unavailability.
		fmt.Printf("INFO veriflier %q at %s\n", v.Name, addr)
	}

	fmt.Println("\nvalidation passed")
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
	fmt.Println(repeat("-", 90))

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
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0644)
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

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Automattic/jetmon/internal/fleethealth"
)

const (
	defaultFleetRequestTimeout = 3 * time.Second
	defaultFleetRecentWindow   = 15 * time.Minute
)

// FleetSource supplies the global dashboard snapshot.
type FleetSource interface {
	Snapshot(context.Context) (FleetSnapshot, error)
}

// FleetStoreOptions controls fleet dashboard summary thresholds.
type FleetStoreOptions struct {
	BucketTotal    int
	HeartbeatGrace time.Duration
	RecentWindow   time.Duration
	CacheTTL       time.Duration
	Now            func() time.Time
}

// FleetStore builds fleet dashboard snapshots from MySQL-backed process health
// and rollout state.
type FleetStore struct {
	db             *sql.DB
	bucketTotal    int
	heartbeatGrace time.Duration
	recentWindow   time.Duration
	cacheTTL       time.Duration
	now            func() time.Time
	cacheMu        sync.Mutex
	cacheSnapshot  FleetSnapshot
	cacheUntil     time.Time
}

// NewFleetStore creates a MySQL-backed fleet dashboard source.
func NewFleetStore(db *sql.DB, opts FleetStoreOptions) *FleetStore {
	if opts.HeartbeatGrace <= 0 {
		opts.HeartbeatGrace = 10 * time.Minute
	}
	if opts.RecentWindow <= 0 {
		opts.RecentWindow = defaultFleetRecentWindow
	}
	if opts.CacheTTL == 0 {
		opts.CacheTTL = 5 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &FleetStore{
		db:             db,
		bucketTotal:    opts.BucketTotal,
		heartbeatGrace: opts.HeartbeatGrace,
		recentWindow:   opts.RecentWindow,
		cacheTTL:       opts.CacheTTL,
		now:            opts.Now,
	}
}

// Snapshot returns the current fleet-wide operator view.
func (s *FleetStore) Snapshot(ctx context.Context) (FleetSnapshot, error) {
	if s == nil || s.db == nil {
		return FleetSnapshot{}, errors.New("fleet dashboard database source is not configured")
	}
	now := s.now().UTC()
	if cached, ok := s.cachedSnapshot(now); ok {
		return cached, nil
	}
	processRows, err := fleethealth.ListSnapshots(ctx, s.db)
	if err != nil {
		return FleetSnapshot{}, err
	}
	processes := summarizeFleetProcesses(processRows, now, s.heartbeatGrace)

	hosts, hostErr := queryFleetBucketHosts(ctx, s.db)
	bucketCoverage := summarizeFleetBucketCoverage(hosts, s.bucketTotal, s.heartbeatGrace, now, hostErr, processes)

	delivery := queryFleetDelivery(ctx, s.db, now, s.recentWindow)
	delivery.Posture = summarizeFleetDeliveryPosture(processes, delivery.Pending)

	projectionDrift := queryFleetProjectionDrift(ctx, s.db, s.bucketTotal)
	dependencies := summarizeFleetDependencies(processes)
	verifliers := queryFleetVerifliers(ctx, s.db, now, s.heartbeatGrace, processes)

	snapshot := FleetSnapshot{
		GeneratedAt:     now,
		Processes:       processes,
		ProcessCounts:   countFleetProcesses(processes),
		BucketCoverage:  bucketCoverage,
		Delivery:        delivery,
		ProjectionDrift: projectionDrift,
		Dependencies:    dependencies,
		Verifliers:      verifliers,
	}
	snapshot.Summary = summarizeFleet(snapshot)
	s.storeCachedSnapshot(snapshot)
	return snapshot, nil
}

func (s *FleetStore) cachedSnapshot(now time.Time) (FleetSnapshot, bool) {
	if s.cacheTTL < 0 {
		return FleetSnapshot{}, false
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cacheUntil.IsZero() || !now.Before(s.cacheUntil) {
		return FleetSnapshot{}, false
	}
	return cloneFleetSnapshot(s.cacheSnapshot), true
}

func (s *FleetStore) storeCachedSnapshot(snapshot FleetSnapshot) {
	if s.cacheTTL < 0 {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cacheSnapshot = cloneFleetSnapshot(snapshot)
	s.cacheUntil = snapshot.GeneratedAt.Add(s.cacheTTL)
}

// FleetSnapshot is the JSON model for the global dashboard.
type FleetSnapshot struct {
	GeneratedAt     time.Time                `json:"generated_at"`
	Summary         FleetSummary             `json:"summary"`
	Processes       []FleetProcess           `json:"processes"`
	ProcessCounts   map[string]int           `json:"process_counts"`
	BucketCoverage  FleetBucketCoverage      `json:"bucket_coverage"`
	Delivery        FleetDeliverySummary     `json:"delivery"`
	ProjectionDrift FleetProjectionDrift     `json:"projection_drift"`
	Dependencies    []FleetDependencySummary `json:"dependencies,omitempty"`
	Verifliers      FleetVeriflierSummary    `json:"verifliers"`
}

// FleetSummary is the top-level red/amber/green rollup for the fleet.
type FleetSummary struct {
	Status               string   `json:"status"`
	Message              string   `json:"message"`
	SuggestedNextAction  string   `json:"suggested_next_action,omitempty"`
	Issues               []string `json:"issues,omitempty"`
	RedProcesses         int      `json:"red_processes"`
	AmberProcesses       int      `json:"amber_processes"`
	GreenProcesses       int      `json:"green_processes"`
	StaleProcesses       int      `json:"stale_processes"`
	MonitorProcesses     int      `json:"monitor_processes"`
	DelivererProcesses   int      `json:"deliverer_processes"`
	DependencyRedCount   int      `json:"dependency_red_count"`
	DependencyAmberCount int      `json:"dependency_amber_count"`
}

// FleetProcess is one process-health row with dashboard-derived freshness.
type FleetProcess struct {
	ProcessID                 string                         `json:"process_id"`
	HostID                    string                         `json:"host_id"`
	ProcessType               string                         `json:"process_type"`
	PID                       int                            `json:"pid"`
	Version                   string                         `json:"version"`
	BuildDate                 string                         `json:"build_date"`
	GoVersion                 string                         `json:"go_version"`
	State                     string                         `json:"state"`
	HealthStatus              string                         `json:"health_status"`
	StartedAt                 time.Time                      `json:"started_at,omitempty"`
	UpdatedAt                 time.Time                      `json:"updated_at"`
	LastHeartbeatAgeSec       int64                          `json:"last_heartbeat_age_sec"`
	Stale                     bool                           `json:"stale"`
	BucketMin                 *int                           `json:"bucket_min,omitempty"`
	BucketMax                 *int                           `json:"bucket_max,omitempty"`
	BucketOwnership           string                         `json:"bucket_ownership"`
	APIPort                   *int                           `json:"api_port,omitempty"`
	DashboardPort             *int                           `json:"dashboard_port,omitempty"`
	DeliveryWorkersEnabled    bool                           `json:"delivery_workers_enabled"`
	DeliveryOwnerHost         string                         `json:"delivery_owner_host"`
	WorkerCount               int                            `json:"worker_count"`
	ActiveChecks              int                            `json:"active_checks"`
	QueueDepth                int                            `json:"queue_depth"`
	RetryQueueSize            int                            `json:"retry_queue_size"`
	WPCOMCircuitOpen          bool                           `json:"wpcom_circuit_open"`
	WPCOMQueueDepth           int                            `json:"wpcom_queue_depth"`
	GoSysMemMB                int                            `json:"go_sys_mem_mb"`
	RSSMemMB                  int                            `json:"rss_mem_mb"`
	RuntimeGoroutines         int                            `json:"runtime_goroutines"`
	RuntimeGoroutinesRunnable int                            `json:"runtime_goroutines_runnable"`
	RuntimeGoroutinesRunning  int                            `json:"runtime_goroutines_running"`
	RuntimeGoroutinesWaiting  int                            `json:"runtime_goroutines_waiting"`
	RuntimeGoroutinesNotInGo  int                            `json:"runtime_goroutines_not_in_go"`
	RuntimeGoroutinesCreated  uint64                         `json:"runtime_goroutines_created"`
	RuntimeThreads            int                            `json:"runtime_threads"`
	DependencyHealth          []fleethealth.DependencyHealth `json:"dependency_health,omitempty"`
}

// FleetBucketCoverage summarizes jetpack_monitor_hosts dynamic bucket ownership.
type FleetBucketCoverage struct {
	Status      string            `json:"status"`
	Mode        string            `json:"mode"`
	BucketTotal int               `json:"bucket_total"`
	HostCount   int               `json:"host_count"`
	Error       string            `json:"error,omitempty"`
	Hosts       []FleetBucketHost `json:"hosts,omitempty"`
}

// FleetBucketHost is one jetpack_monitor_hosts row with freshness metadata.
type FleetBucketHost struct {
	HostID              string    `json:"host_id"`
	BucketMin           int       `json:"bucket_min"`
	BucketMax           int       `json:"bucket_max"`
	Status              string    `json:"status"`
	LastHeartbeat       time.Time `json:"last_heartbeat"`
	LastHeartbeatAgeSec int64     `json:"last_heartbeat_age_sec"`
	Stale               bool      `json:"stale"`
}

// FleetDeliverySummary describes global outbound delivery queues.
type FleetDeliverySummary struct {
	Status              string               `json:"status"`
	Error               string               `json:"error,omitempty"`
	Since               time.Time            `json:"since"`
	Pending             int64                `json:"pending"`
	DueNow              int64                `json:"due_now"`
	FutureRetry         int64                `json:"future_retry"`
	DeliveredSince      int64                `json:"delivered_since"`
	AbandonedSince      int64                `json:"abandoned_since"`
	FailedSince         int64                `json:"failed_since"`
	OldestPendingAgeSec int64                `json:"oldest_pending_age_sec"`
	OldestDueAgeSec     int64                `json:"oldest_due_age_sec"`
	Tables              []FleetDeliveryTable `json:"tables,omitempty"`
	Posture             FleetDeliveryPosture `json:"posture"`
}

// FleetDeliveryTable is a per-table outbound delivery queue summary.
type FleetDeliveryTable struct {
	Kind                string `json:"kind"`
	Pending             int64  `json:"pending"`
	DueNow              int64  `json:"due_now"`
	FutureRetry         int64  `json:"future_retry"`
	DeliveredSince      int64  `json:"delivered_since"`
	AbandonedSince      int64  `json:"abandoned_since"`
	FailedSince         int64  `json:"failed_since"`
	OldestPendingAgeSec int64  `json:"oldest_pending_age_sec"`
	OldestDueAgeSec     int64  `json:"oldest_due_age_sec"`
}

// FleetDeliveryPosture describes which process snapshots report delivery
// workers as enabled.
type FleetDeliveryPosture struct {
	Status              string   `json:"status"`
	EnabledProcessCount int      `json:"enabled_process_count"`
	EnabledHosts        []string `json:"enabled_hosts,omitempty"`
	OwnerHosts          []string `json:"owner_hosts,omitempty"`
	Message             string   `json:"message"`
}

// FleetProjectionDrift summarizes legacy projection drift globally.
type FleetProjectionDrift struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
	Error  string `json:"error,omitempty"`
}

// FleetDependencySummary aggregates dependency health by dependency name.
type FleetDependencySummary struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	RedCount   int    `json:"red_count"`
	AmberCount int    `json:"amber_count"`
	GreenCount int    `json:"green_count"`
	StaleCount int    `json:"stale_count"`
	LastError  string `json:"last_error,omitempty"`
}

// FleetVeriflierSummary summarizes trusted vantages and monitor-collected
// Veriflier agent telemetry for the fleet dashboard.
type FleetVeriflierSummary struct {
	Status             string                         `json:"status"`
	Message            string                         `json:"message,omitempty"`
	Error              string                         `json:"error,omitempty"`
	TotalVantages      int                            `json:"total_vantages"`
	EnabledVantages    int                            `json:"enabled_vantages"`
	DisabledVantages   int                            `json:"disabled_vantages"`
	UsableVantages     int                            `json:"usable_vantages"`
	IncompleteVantages int                            `json:"incomplete_vantages"`
	TotalAgents        int                            `json:"total_agents"`
	FreshAgents        int                            `json:"fresh_agents"`
	StaleAgents        int                            `json:"stale_agents"`
	ActiveAgents       int                            `json:"active_agents"`
	DuplicateEndpoints int                            `json:"duplicate_endpoints"`
	MaxConcurrency     int                            `json:"max_concurrency"`
	QueueCapacity      int                            `json:"queue_capacity"`
	QueueDepth         int                            `json:"queue_depth"`
	ActiveChecks       int                            `json:"active_checks"`
	InFlight           int                            `json:"in_flight"`
	DiscoveryModes     []FleetVeriflierDiscoveryMode  `json:"discovery_modes,omitempty"`
	Vantages           []FleetVeriflierVantageSummary `json:"vantages,omitempty"`
	Agents             []FleetVeriflierAgentSummary   `json:"agents,omitempty"`
}

type FleetVeriflierDiscoveryMode struct {
	Mode         string   `json:"mode"`
	ProcessCount int      `json:"process_count"`
	Hosts        []string `json:"hosts,omitempty"`
}

type FleetVeriflierVantageSummary struct {
	VantageID        string    `json:"vantage_id"`
	Region           string    `json:"region,omitempty"`
	Provider         string    `json:"provider,omitempty"`
	EndpointHost     string    `json:"endpoint_host,omitempty"`
	EndpointPort     string    `json:"endpoint_port,omitempty"`
	AuthTokenPresent bool      `json:"auth_token_present"`
	Enabled          bool      `json:"enabled"`
	Usable           bool      `json:"usable"`
	ActiveAgents     int       `json:"active_agents"`
	FreshAgents      int       `json:"fresh_agents"`
	StaleAgents      int       `json:"stale_agents"`
	LastSeen         time.Time `json:"last_seen,omitempty"`
	LastSeenAgeSec   int64     `json:"last_seen_age_sec,omitempty"`
}

type FleetVeriflierAgentSummary struct {
	AgentID            string    `json:"agent_id"`
	VantageID          string    `json:"vantage_id"`
	Hostname           string    `json:"hostname,omitempty"`
	EndpointHost       string    `json:"endpoint_host,omitempty"`
	EndpointPort       string    `json:"endpoint_port,omitempty"`
	Version            string    `json:"version,omitempty"`
	Protocols          []string  `json:"protocols,omitempty"`
	MaxConcurrency     int       `json:"max_concurrency"`
	QueueCapacity      int       `json:"queue_capacity"`
	QueueDepth         int       `json:"queue_depth"`
	Active             int       `json:"active"`
	InFlight           int       `json:"in_flight"`
	Status             string    `json:"status"`
	LastSeen           time.Time `json:"last_seen"`
	LastSeenAgeSec     int64     `json:"last_seen_age_sec"`
	Stale              bool      `json:"stale"`
	VantagePreapproved bool      `json:"vantage_preapproved"`
}

func summarizeFleetProcesses(rows []fleethealth.Snapshot, now time.Time, heartbeatGrace time.Duration) []FleetProcess {
	out := make([]FleetProcess, 0, len(rows))
	for _, row := range rows {
		age := now.Sub(row.UpdatedAt)
		if age < 0 {
			age = 0
		}
		out = append(out, FleetProcess{
			ProcessID:                 row.ProcessID,
			HostID:                    row.HostID,
			ProcessType:               row.ProcessType,
			PID:                       row.PID,
			Version:                   row.Version,
			BuildDate:                 row.BuildDate,
			GoVersion:                 row.GoVersion,
			State:                     row.State,
			HealthStatus:              row.HealthStatus,
			StartedAt:                 row.StartedAt,
			UpdatedAt:                 row.UpdatedAt,
			LastHeartbeatAgeSec:       int64(age.Round(time.Second) / time.Second),
			Stale:                     age > heartbeatGrace,
			BucketMin:                 row.BucketMin,
			BucketMax:                 row.BucketMax,
			BucketOwnership:           row.BucketOwnership,
			APIPort:                   row.APIPort,
			DashboardPort:             row.DashboardPort,
			DeliveryWorkersEnabled:    row.DeliveryWorkersEnabled,
			DeliveryOwnerHost:         row.DeliveryOwnerHost,
			WorkerCount:               row.WorkerCount,
			ActiveChecks:              row.ActiveChecks,
			QueueDepth:                row.QueueDepth,
			RetryQueueSize:            row.RetryQueueSize,
			WPCOMCircuitOpen:          row.WPCOMCircuitOpen,
			WPCOMQueueDepth:           row.WPCOMQueueDepth,
			GoSysMemMB:                row.GoSysMemMB,
			RSSMemMB:                  row.RSSMemMB,
			RuntimeGoroutines:         row.RuntimeGoroutines,
			RuntimeGoroutinesRunnable: row.RuntimeGoroutinesRunnable,
			RuntimeGoroutinesRunning:  row.RuntimeGoroutinesRunning,
			RuntimeGoroutinesWaiting:  row.RuntimeGoroutinesWaiting,
			RuntimeGoroutinesNotInGo:  row.RuntimeGoroutinesNotInGo,
			RuntimeGoroutinesCreated:  row.RuntimeGoroutinesCreated,
			RuntimeThreads:            row.RuntimeThreads,
			DependencyHealth:          append([]fleethealth.DependencyHealth(nil), row.DependencyHealth...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := fleetProcessRank(out[i]), fleetProcessRank(out[j])
		if left != right {
			return left > right
		}
		leftType, rightType := fleetProcessTypeRank(out[i].ProcessType), fleetProcessTypeRank(out[j].ProcessType)
		if leftType != rightType {
			return leftType < rightType
		}
		if out[i].HostID != out[j].HostID {
			return out[i].HostID < out[j].HostID
		}
		return out[i].ProcessID < out[j].ProcessID
	})
	return out
}

func fleetProcessRank(process FleetProcess) int {
	if process.Stale {
		return 5
	}
	switch process.HealthStatus {
	case fleethealth.HealthRed:
		return 4
	case fleethealth.HealthAmber:
		return 3
	default:
		return 1
	}
}

func fleetProcessTypeRank(processType string) int {
	switch processType {
	case fleethealth.ProcessMonitor:
		return 1
	case fleethealth.ProcessDeliverer:
		return 2
	default:
		return 99
	}
}

func queryFleetBucketHosts(ctx context.Context, db *sql.DB) ([]FleetBucketHost, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT host_id, bucket_min, bucket_max, last_heartbeat, status
		  FROM jetpack_monitor_hosts
		 ORDER BY bucket_min, host_id`)
	if err != nil {
		return nil, fmt.Errorf("query jetpack_monitor_hosts: %w", err)
	}
	defer rows.Close()

	var hosts []FleetBucketHost
	for rows.Next() {
		var host FleetBucketHost
		if err := rows.Scan(&host.HostID, &host.BucketMin, &host.BucketMax, &host.LastHeartbeat, &host.Status); err != nil {
			return nil, fmt.Errorf("scan jetpack_monitor_hosts: %w", err)
		}
		host.LastHeartbeat = host.LastHeartbeat.UTC()
		hosts = append(hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jetpack_monitor_hosts: %w", err)
	}
	return hosts, nil
}

func summarizeFleetBucketCoverage(hosts []FleetBucketHost, bucketTotal int, heartbeatGrace time.Duration, now time.Time, queryErr error, processes []FleetProcess) FleetBucketCoverage {
	mode := fleetBucketOwnershipMode(processes)
	if mode == "unknown" && len(hosts) > 0 {
		mode = "dynamic"
	}
	coverage := FleetBucketCoverage{
		Status:      "green",
		Mode:        mode,
		BucketTotal: bucketTotal,
		HostCount:   len(hosts),
		Hosts:       append([]FleetBucketHost(nil), hosts...),
	}
	if queryErr != nil {
		coverage.Status = "red"
		coverage.Mode = "unknown"
		coverage.Error = queryErr.Error()
		return coverage
	}
	if bucketTotal <= 0 {
		coverage.Status = "amber"
		coverage.Mode = "unknown"
		coverage.Error = "BUCKET_TOTAL is not configured"
		return coverage
	}
	for i := range coverage.Hosts {
		age := now.Sub(coverage.Hosts[i].LastHeartbeat)
		if age < 0 {
			age = 0
		}
		coverage.Hosts[i].LastHeartbeatAgeSec = int64(age.Round(time.Second) / time.Second)
		coverage.Hosts[i].Stale = age > heartbeatGrace
	}
	if mode == "pinned" {
		coverage.Status = "amber"
		coverage.Error = "monitor process snapshots report pinned bucket ranges; dynamic jetpack_monitor_hosts coverage is not active"
		return coverage
	}
	if mode == "mixed" {
		coverage.Status = "amber"
		coverage.Error = "monitor process snapshots report mixed pinned and dynamic bucket ownership"
		return coverage
	}
	if len(hosts) == 0 {
		coverage.Status = "amber"
		coverage.Error = "jetpack_monitor_hosts has no dynamic ownership rows"
		return coverage
	}
	if err := validateFleetBucketCoverage(coverage.Hosts, bucketTotal); err != nil {
		coverage.Status = "red"
		coverage.Error = err.Error()
		return coverage
	}
	for _, host := range coverage.Hosts {
		if host.Status != "active" {
			coverage.Status = "red"
			coverage.Error = fmt.Sprintf("host %q has status=%q", host.HostID, host.Status)
			return coverage
		}
		if host.Stale {
			coverage.Status = "red"
			coverage.Error = fmt.Sprintf("host %q heartbeat is stale", host.HostID)
			return coverage
		}
	}
	return coverage
}

func fleetBucketOwnershipMode(processes []FleetProcess) string {
	hasPinned, hasDynamic := false, false
	for _, process := range processes {
		if process.ProcessType != fleethealth.ProcessMonitor {
			continue
		}
		if !processActiveForFleetOwnership(process) {
			continue
		}
		ownership := strings.ToLower(strings.TrimSpace(process.BucketOwnership))
		switch {
		case strings.Contains(ownership, "pinned"):
			hasPinned = true
		case strings.Contains(ownership, "dynamic"):
			hasDynamic = true
		}
	}
	switch {
	case hasPinned && hasDynamic:
		return "mixed"
	case hasPinned:
		return "pinned"
	case hasDynamic:
		return "dynamic"
	default:
		return "unknown"
	}
}

func processActiveForFleetOwnership(process FleetProcess) bool {
	return processActiveForFleetRollup(process)
}

func validateFleetBucketCoverage(hosts []FleetBucketHost, bucketTotal int) error {
	sortedHosts := append([]FleetBucketHost(nil), hosts...)
	sort.Slice(sortedHosts, func(i, j int) bool {
		if sortedHosts[i].BucketMin == sortedHosts[j].BucketMin {
			return sortedHosts[i].HostID < sortedHosts[j].HostID
		}
		return sortedHosts[i].BucketMin < sortedHosts[j].BucketMin
	})

	expectedMin := 0
	for _, host := range sortedHosts {
		if host.BucketMin < 0 || host.BucketMax < host.BucketMin || host.BucketMax >= bucketTotal {
			return fmt.Errorf("host %q has invalid bucket range %d-%d for BUCKET_TOTAL=%d", host.HostID, host.BucketMin, host.BucketMax, bucketTotal)
		}
		if host.BucketMin > expectedMin {
			return fmt.Errorf("dynamic bucket coverage has gap %d-%d before host %q", expectedMin, host.BucketMin-1, host.HostID)
		}
		if host.BucketMin < expectedMin {
			return fmt.Errorf("dynamic bucket coverage overlaps before host %q at bucket %d", host.HostID, host.BucketMin)
		}
		expectedMin = host.BucketMax + 1
	}
	if expectedMin < bucketTotal {
		return fmt.Errorf("dynamic bucket coverage has trailing gap %d-%d", expectedMin, bucketTotal-1)
	}
	return nil
}

func queryFleetDelivery(ctx context.Context, db *sql.DB, now time.Time, recentWindow time.Duration) FleetDeliverySummary {
	cutoff := now.Add(-recentWindow).UTC()
	summary := FleetDeliverySummary{Status: "green", Since: cutoff}
	tables := []struct {
		kind string
		name string
	}{
		{kind: "webhook", name: "jetpack_monitor_webhook_deliveries"},
		{kind: "alert", name: "jetpack_monitor_alert_deliveries"},
	}
	for _, table := range tables {
		tableSummary, err := queryFleetDeliveryTable(ctx, db, table.kind, table.name, now, cutoff)
		if err != nil {
			summary.Status = "red"
			summary.Error = err.Error()
			return summary
		}
		summary.Tables = append(summary.Tables, tableSummary)
		summary.Pending += tableSummary.Pending
		summary.DueNow += tableSummary.DueNow
		summary.FutureRetry += tableSummary.FutureRetry
		summary.DeliveredSince += tableSummary.DeliveredSince
		summary.AbandonedSince += tableSummary.AbandonedSince
		summary.FailedSince += tableSummary.FailedSince
		summary.OldestPendingAgeSec = maxInt64(summary.OldestPendingAgeSec, tableSummary.OldestPendingAgeSec)
		summary.OldestDueAgeSec = maxInt64(summary.OldestDueAgeSec, tableSummary.OldestDueAgeSec)
	}
	switch {
	case summary.AbandonedSince > 0 || summary.FailedSince > 0:
		summary.Status = "red"
	case summary.DueNow > 0:
		summary.Status = "amber"
	default:
		summary.Status = "green"
	}
	return summary
}

func queryFleetDeliveryTable(ctx context.Context, db *sql.DB, kind, table string, now, cutoff time.Time) (FleetDeliveryTable, error) {
	switch table {
	case "jetpack_monitor_webhook_deliveries", "jetpack_monitor_alert_deliveries":
	default:
		return FleetDeliveryTable{}, fmt.Errorf("unsupported delivery table %q", table)
	}
	summary := FleetDeliveryTable{Kind: kind}

	query := fmt.Sprintf(`
		SELECT 'pending' AS metric,
		       COUNT(*) AS count,
		       COALESCE(TIMESTAMPDIFF(SECOND, MIN(created_at), ?), 0) AS age_sec
		  FROM %s
		 WHERE status = 'pending'
		UNION ALL
		SELECT 'due' AS metric,
		       COUNT(*) AS count,
		       COALESCE(TIMESTAMPDIFF(SECOND, MIN(COALESCE(next_attempt_at, created_at)), ?), 0) AS age_sec
		  FROM %s
		 WHERE status = 'pending'
		   AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		UNION ALL
		SELECT 'future' AS metric,
		       COUNT(*) AS count,
		       0 AS age_sec
		  FROM %s
		 WHERE status = 'pending'
		   AND next_attempt_at > ?
		UNION ALL
		SELECT 'delivered' AS metric,
		       COUNT(*) AS count,
		       0 AS age_sec
		  FROM %s
		 WHERE status = 'delivered'
		   AND delivered_at >= ?
		UNION ALL
		SELECT 'abandoned' AS metric,
		       COUNT(*) AS count,
		       0 AS age_sec
		  FROM %s
		 WHERE status = 'abandoned'
		   AND (last_attempt_at >= ? OR (last_attempt_at IS NULL AND created_at >= ?))
		UNION ALL
		SELECT 'failed' AS metric,
		       COUNT(*) AS count,
		       0 AS age_sec
		  FROM %s
		 WHERE status = 'failed'
		   AND (last_attempt_at >= ? OR (last_attempt_at IS NULL AND created_at >= ?))`,
		table, table, table, table, table, table,
	)
	rows, err := db.QueryContext(ctx, query,
		now,
		now, now,
		now,
		cutoff,
		cutoff, cutoff,
		cutoff, cutoff,
	)
	if err != nil {
		return FleetDeliveryTable{}, fmt.Errorf("%s delivery summary: %w", kind, err)
	}
	defer rows.Close()

	for rows.Next() {
		var metric string
		var count, ageSec int64
		if err := rows.Scan(&metric, &count, &ageSec); err != nil {
			return FleetDeliveryTable{}, fmt.Errorf("%s delivery summary scan: %w", kind, err)
		}
		switch metric {
		case "pending":
			summary.Pending = count
			summary.OldestPendingAgeSec = ageSec
		case "due":
			summary.DueNow = count
			summary.OldestDueAgeSec = ageSec
		case "future":
			summary.FutureRetry = count
		case "delivered":
			summary.DeliveredSince = count
		case "abandoned":
			summary.AbandonedSince = count
		case "failed":
			summary.FailedSince = count
		default:
			return FleetDeliveryTable{}, fmt.Errorf("%s delivery summary returned unknown metric %q", kind, metric)
		}
	}
	if err := rows.Err(); err != nil {
		return FleetDeliveryTable{}, fmt.Errorf("%s delivery summary iterate: %w", kind, err)
	}
	return summary, nil
}

func summarizeFleetDeliveryPosture(processes []FleetProcess, queuedDeliveries int64) FleetDeliveryPosture {
	enabledHosts := map[string]struct{}{}
	ownerHosts := map[string]struct{}{}
	enabledCount := 0
	enabledWithoutOwner := 0
	for _, process := range processes {
		if !processActiveForDeliveryPosture(process) {
			continue
		}
		if !process.DeliveryWorkersEnabled {
			continue
		}
		enabledCount++
		enabledHosts[process.HostID] = struct{}{}
		if owner := strings.TrimSpace(process.DeliveryOwnerHost); owner != "" {
			ownerHosts[owner] = struct{}{}
		} else {
			enabledWithoutOwner++
		}
	}
	posture := FleetDeliveryPosture{
		Status:              "green",
		EnabledProcessCount: enabledCount,
		EnabledHosts:        sortedStringKeys(enabledHosts),
		OwnerHosts:          sortedStringKeys(ownerHosts),
	}
	switch {
	case enabledCount == 0 && queuedDeliveries > 0:
		posture.Status = "amber"
		posture.Message = "delivery rows are queued but no fresh process snapshot reports delivery workers enabled"
	case enabledCount == 0:
		posture.Message = "no fresh process snapshot reports delivery workers enabled; delivery queues are empty"
	case enabledWithoutOwner > 0 && len(posture.OwnerHosts) > 0:
		posture.Status = "amber"
		posture.Message = "delivery-capable processes mix explicit DELIVERY_OWNER_HOST and unset ownership"
	case enabledWithoutOwner > 0:
		posture.Status = "amber"
		posture.Message = "delivery workers are enabled without DELIVERY_OWNER_HOST"
	case len(posture.OwnerHosts) > 1:
		posture.Status = "amber"
		posture.Message = "multiple DELIVERY_OWNER_HOST values are visible across process snapshots"
	case len(posture.OwnerHosts) == 1:
		posture.Message = fmt.Sprintf("delivery owner is constrained to %s", posture.OwnerHosts[0])
	default:
		posture.Message = "delivery workers are enabled without an explicit owner"
	}
	return posture
}

func processActiveForDeliveryPosture(process FleetProcess) bool {
	return processActiveForFleetRollup(process)
}

func processActiveForFleetRollup(process FleetProcess) bool {
	if process.Stale {
		return false
	}
	switch process.State {
	case fleethealth.StateStopped, fleethealth.StateStopping:
		return false
	default:
		return true
	}
}

func queryFleetProjectionDrift(ctx context.Context, db *sql.DB, bucketTotal int) FleetProjectionDrift {
	if bucketTotal <= 0 {
		return FleetProjectionDrift{Status: "amber", Error: "BUCKET_TOTAL is not configured"}
	}
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites s
		  LEFT JOIN jetpack_monitor_events e
		    ON e.blog_id = s.blog_id
		   AND e.check_type = 'http'
		   AND e.ended_at IS NULL
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN 0 AND ?
		   AND s.site_status <> CASE
		     WHEN e.state = 'Down' THEN 2
		     WHEN e.state = 'Seems Down' THEN 0
		     ELSE 1
		   END`,
		bucketTotal-1,
	).Scan(&count)
	if err != nil {
		return FleetProjectionDrift{Status: "red", Error: fmt.Sprintf("count projection drift: %v", err)}
	}
	if count > 0 {
		return FleetProjectionDrift{Status: "red", Count: count}
	}
	return FleetProjectionDrift{Status: "green"}
}

func queryFleetVerifliers(ctx context.Context, db *sql.DB, now time.Time, staleAfter time.Duration, processes []FleetProcess) FleetVeriflierSummary {
	summary := FleetVeriflierSummary{
		Status:         "green",
		DiscoveryModes: summarizeFleetVeriflierDiscoveryModes(processes),
	}
	vantages, err := queryFleetVeriflierVantages(ctx, db)
	if err != nil {
		summary.Status = veriflierQueryErrorStatus(summary.DiscoveryModes)
		summary.Error = err.Error()
		summary.Message = "Veriflier discovery tables are unavailable"
		return summary
	}
	agents, err := queryFleetVeriflierAgents(ctx, db, now, staleAfter)
	if err != nil {
		summary.Status = veriflierQueryErrorStatus(summary.DiscoveryModes)
		summary.Error = err.Error()
		summary.Vantages = vantages
		summary.Message = "Veriflier agent telemetry is unavailable"
		return summary
	}

	summary.Vantages = vantages
	summary.Agents = agents
	summarizeFleetVeriflierRows(&summary, now)
	return summary
}

func queryFleetVeriflierVantages(ctx context.Context, db *sql.DB) ([]FleetVeriflierVantageSummary, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT vantage_id, region, provider, endpoint_host, endpoint_port,
		       IF(auth_token <> '', 1, 0) AS auth_token_present,
		       enabled, updated_at
		  FROM jetpack_monitor_veriflier_vantages
		 ORDER BY enabled DESC, vantage_id`)
	if err != nil {
		return nil, fmt.Errorf("query jetpack_monitor_veriflier_vantages: %w", err)
	}
	defer rows.Close()

	var vantages []FleetVeriflierVantageSummary
	for rows.Next() {
		var v FleetVeriflierVantageSummary
		var authTokenPresent, enabled int
		var updatedAt time.Time
		if err := rows.Scan(
			&v.VantageID,
			&v.Region,
			&v.Provider,
			&v.EndpointHost,
			&v.EndpointPort,
			&authTokenPresent,
			&enabled,
			&updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan jetpack_monitor_veriflier_vantages: %w", err)
		}
		v.AuthTokenPresent = authTokenPresent != 0
		v.Enabled = enabled != 0
		v.Usable = v.Enabled &&
			strings.TrimSpace(v.EndpointHost) != "" &&
			strings.TrimSpace(v.EndpointPort) != "" &&
			v.AuthTokenPresent
		vantages = append(vantages, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jetpack_monitor_veriflier_vantages: %w", err)
	}
	return vantages, nil
}

func queryFleetVeriflierAgents(ctx context.Context, db *sql.DB, now time.Time, staleAfter time.Duration) ([]FleetVeriflierAgentSummary, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT agent_id, vantage_id, hostname, endpoint_host, endpoint_port,
		       version, protocols, max_concurrency, queue_capacity, queue_depth,
		       active, in_flight, status, last_seen
		  FROM jetpack_monitor_veriflier_agents
		 ORDER BY last_seen DESC, vantage_id, agent_id`)
	if err != nil {
		return nil, fmt.Errorf("query jetpack_monitor_veriflier_agents: %w", err)
	}
	defer rows.Close()

	var agents []FleetVeriflierAgentSummary
	for rows.Next() {
		var agent FleetVeriflierAgentSummary
		var protocols sql.NullString
		if err := rows.Scan(
			&agent.AgentID,
			&agent.VantageID,
			&agent.Hostname,
			&agent.EndpointHost,
			&agent.EndpointPort,
			&agent.Version,
			&protocols,
			&agent.MaxConcurrency,
			&agent.QueueCapacity,
			&agent.QueueDepth,
			&agent.Active,
			&agent.InFlight,
			&agent.Status,
			&agent.LastSeen,
		); err != nil {
			return nil, fmt.Errorf("scan jetpack_monitor_veriflier_agents: %w", err)
		}
		if protocols.Valid && strings.TrimSpace(protocols.String) != "" {
			if err := json.Unmarshal([]byte(protocols.String), &agent.Protocols); err != nil {
				return nil, fmt.Errorf("decode jetpack_monitor_veriflier_agents.protocols: %w", err)
			}
		}
		agent.LastSeen = agent.LastSeen.UTC()
		age := now.Sub(agent.LastSeen)
		if age < 0 {
			age = 0
		}
		agent.LastSeenAgeSec = int64(age.Round(time.Second) / time.Second)
		agent.Stale = staleAfter > 0 && age > staleAfter
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jetpack_monitor_veriflier_agents: %w", err)
	}
	return agents, nil
}

func summarizeFleetVeriflierRows(summary *FleetVeriflierSummary, now time.Time) {
	if summary == nil {
		return
	}
	vantageIndex := map[string]int{}
	freshActiveEndpointsByVantage := map[string]map[string]struct{}{}
	for i := range summary.Vantages {
		v := &summary.Vantages[i]
		summary.TotalVantages++
		vantageIndex[v.VantageID] = i
		if v.Enabled {
			summary.EnabledVantages++
		} else {
			summary.DisabledVantages++
		}
		if v.Usable {
			summary.UsableVantages++
		} else if v.Enabled {
			summary.IncompleteVantages++
		}
	}

	for i := range summary.Agents {
		agent := &summary.Agents[i]
		summary.TotalAgents++
		if agent.Stale {
			summary.StaleAgents++
		} else {
			summary.FreshAgents++
		}
		if agent.Status == "active" {
			summary.ActiveAgents++
		}
		summary.MaxConcurrency += agent.MaxConcurrency
		summary.QueueCapacity += agent.QueueCapacity
		summary.QueueDepth += agent.QueueDepth
		summary.ActiveChecks += agent.Active
		summary.InFlight += agent.InFlight

		if idx, ok := vantageIndex[agent.VantageID]; ok {
			v := &summary.Vantages[idx]
			agent.VantagePreapproved = v.Enabled
			if agent.Status == "active" {
				v.ActiveAgents++
			}
			if agent.Stale {
				v.StaleAgents++
			} else {
				v.FreshAgents++
			}
			if v.LastSeen.IsZero() || agent.LastSeen.After(v.LastSeen) {
				v.LastSeen = agent.LastSeen
				age := now.Sub(agent.LastSeen)
				if age < 0 {
					age = 0
				}
				v.LastSeenAgeSec = int64(age.Round(time.Second) / time.Second)
			}
		}
		if !agent.Stale && agent.Status == "active" && strings.TrimSpace(agent.VantageID) != "" {
			endpoints := freshActiveEndpointsByVantage[agent.VantageID]
			if endpoints == nil {
				endpoints = map[string]struct{}{}
				freshActiveEndpointsByVantage[agent.VantageID] = endpoints
			}
			endpoints[strings.TrimSpace(agent.EndpointHost)+":"+strings.TrimSpace(agent.EndpointPort)] = struct{}{}
		}
	}
	for _, endpoints := range freshActiveEndpointsByVantage {
		if len(endpoints) > 1 {
			summary.DuplicateEndpoints++
		}
	}

	summary.Status, summary.Message = summarizeFleetVeriflierStatus(*summary)
}

func summarizeFleetVeriflierStatus(summary FleetVeriflierSummary) (string, string) {
	activeMode := fleetVeriflierModeVisible(summary.DiscoveryModes, "active")
	shadowMode := fleetVeriflierModeVisible(summary.DiscoveryModes, "shadow")
	discoveryVisible := activeMode || shadowMode

	switch {
	case activeMode && summary.UsableVantages == 0:
		return "red", "active discovery has no usable enabled Veriflier vantages"
	case activeMode && summary.IncompleteVantages > 0:
		return "red", fmt.Sprintf("active discovery has %d incomplete enabled Veriflier vantages", summary.IncompleteVantages)
	case activeMode && summary.DuplicateEndpoints > 0:
		return "red", fmt.Sprintf("%d Veriflier vantages have multiple fresh active endpoints", summary.DuplicateEndpoints)
	case discoveryVisible && summary.EnabledVantages == 0:
		return "amber", "discovery mode is visible but no trusted Veriflier vantages are enabled"
	case shadowMode && summary.IncompleteVantages > 0:
		return "amber", fmt.Sprintf("shadow registry has %d incomplete enabled Veriflier vantages", summary.IncompleteVantages)
	case discoveryVisible && summary.FreshAgents == 0:
		return "amber", "discovery mode is visible but no fresh Veriflier agent telemetry is available"
	case summary.DuplicateEndpoints > 0:
		return "amber", fmt.Sprintf("%d Veriflier vantages have multiple fresh active endpoints", summary.DuplicateEndpoints)
	case summary.StaleAgents > 0:
		return "amber", fmt.Sprintf("%d Veriflier agent telemetry rows are stale", summary.StaleAgents)
	case !discoveryVisible && summary.TotalVantages == 0:
		return "green", "static Veriflier configuration is in use; discovery registry is empty"
	case summary.IncompleteVantages > 0:
		return "amber", fmt.Sprintf("registry has %d incomplete enabled Veriflier vantages", summary.IncompleteVantages)
	case summary.EnabledVantages > 0:
		return "green", fmt.Sprintf("%d usable trusted Veriflier vantages", summary.UsableVantages)
	default:
		return "green", "Veriflier discovery registry is present"
	}
}

func summarizeFleetVeriflierDiscoveryModes(processes []FleetProcess) []FleetVeriflierDiscoveryMode {
	byMode := map[string]map[string]struct{}{}
	for _, process := range processes {
		if process.Stale || process.ProcessType != fleethealth.ProcessMonitor {
			continue
		}
		for _, dep := range process.DependencyHealth {
			mode, ok := parseVeriflierDiscoveryMode(dep.Name)
			if !ok {
				continue
			}
			hosts := byMode[mode]
			if hosts == nil {
				hosts = map[string]struct{}{}
				byMode[mode] = hosts
			}
			hosts[process.HostID] = struct{}{}
		}
	}
	modes := make([]FleetVeriflierDiscoveryMode, 0, len(byMode))
	for mode, hosts := range byMode {
		modes = append(modes, FleetVeriflierDiscoveryMode{
			Mode:         mode,
			ProcessCount: len(hosts),
			Hosts:        sortedStringKeys(hosts),
		})
	}
	sort.Slice(modes, func(i, j int) bool {
		if modes[i].Mode == modes[j].Mode {
			return modes[i].ProcessCount > modes[j].ProcessCount
		}
		return modes[i].Mode < modes[j].Mode
	})
	return modes
}

func parseVeriflierDiscoveryMode(name string) (string, bool) {
	const prefix = "veriflier-discovery:"
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	mode := strings.TrimSpace(strings.TrimPrefix(name, prefix))
	if idx := strings.IndexByte(mode, ' '); idx >= 0 {
		mode = mode[:idx]
	}
	if mode == "" {
		return "", false
	}
	return mode, true
}

func fleetVeriflierModeVisible(modes []FleetVeriflierDiscoveryMode, want string) bool {
	for _, mode := range modes {
		if mode.Mode == want && mode.ProcessCount > 0 {
			return true
		}
	}
	return false
}

func veriflierQueryErrorStatus(modes []FleetVeriflierDiscoveryMode) string {
	if fleetVeriflierModeVisible(modes, "active") {
		return "red"
	}
	return "amber"
}

func summarizeFleetDependencies(processes []FleetProcess) []FleetDependencySummary {
	byName := map[string]*FleetDependencySummary{}
	for _, process := range processes {
		for _, dep := range process.DependencyHealth {
			name := strings.TrimSpace(dep.Name)
			if name == "" {
				name = "unknown"
			}
			summary := byName[name]
			if summary == nil {
				summary = &FleetDependencySummary{Name: name, Status: "green"}
				byName[name] = summary
			}
			if process.Stale {
				summary.StaleCount++
			}
			switch dep.Status {
			case fleethealth.HealthRed:
				summary.RedCount++
				summary.Status = "red"
				if dep.LastError != "" {
					summary.LastError = dep.LastError
				}
			case fleethealth.HealthAmber:
				summary.AmberCount++
				if summary.Status != "red" {
					summary.Status = "amber"
				}
				if dep.LastError != "" && summary.LastError == "" {
					summary.LastError = dep.LastError
				}
			case fleethealth.HealthGreen:
				summary.GreenCount++
			default:
				summary.AmberCount++
				if summary.Status != "red" {
					summary.Status = "amber"
				}
			}
		}
	}
	out := make([]FleetDependencySummary, 0, len(byName))
	for _, dep := range byName {
		out = append(out, *dep)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status == out[j].Status {
			return out[i].Name < out[j].Name
		}
		return statusRank(out[i].Status) > statusRank(out[j].Status)
	})
	return out
}

func summarizeFleet(snapshot FleetSnapshot) FleetSummary {
	summary := FleetSummary{Status: "green", Message: "fleet checks are green"}
	var redIssues []string
	var amberIssues []string
	if len(snapshot.Processes) == 0 {
		amberIssues = append(amberIssues, "no process-health snapshots found")
	}
	for _, process := range snapshot.Processes {
		switch process.ProcessType {
		case fleethealth.ProcessMonitor:
			summary.MonitorProcesses++
		case fleethealth.ProcessDeliverer:
			summary.DelivererProcesses++
		}
		if process.Stale {
			summary.StaleProcesses++
			summary.RedProcesses++
			redIssues = append(redIssues, fmt.Sprintf("%s heartbeat stale age=%ds", process.ProcessID, process.LastHeartbeatAgeSec))
			continue
		}
		switch process.HealthStatus {
		case fleethealth.HealthRed:
			summary.RedProcesses++
			redIssues = append(redIssues, fmt.Sprintf("%s health_status=red", process.ProcessID))
		case fleethealth.HealthAmber:
			summary.AmberProcesses++
			amberIssues = append(amberIssues, fmt.Sprintf("%s health_status=amber", process.ProcessID))
		case fleethealth.HealthGreen:
			summary.GreenProcesses++
		default:
			summary.AmberProcesses++
			amberIssues = append(amberIssues, fmt.Sprintf("%s health_status=%q", process.ProcessID, process.HealthStatus))
		}
	}
	for _, dep := range snapshot.Dependencies {
		if dep.Status == "red" {
			summary.DependencyRedCount += dep.RedCount
		}
		if dep.Status == "amber" {
			summary.DependencyAmberCount += dep.AmberCount
		}
	}
	if summary.MonitorProcesses == 0 {
		amberIssues = append(amberIssues, "no monitor process snapshots found")
	}
	appendStatusIssue := func(prefix, status, detail string) {
		if status == "red" {
			redIssues = append(redIssues, prefix+": "+detail)
		}
		if status == "amber" {
			amberIssues = append(amberIssues, prefix+": "+detail)
		}
	}
	if snapshot.BucketCoverage.Status != "green" {
		appendStatusIssue("bucket coverage", snapshot.BucketCoverage.Status, firstNonEmpty(snapshot.BucketCoverage.Error, snapshot.BucketCoverage.Status))
	}
	if snapshot.ProjectionDrift.Status != "green" {
		detail := snapshot.ProjectionDrift.Error
		if detail == "" {
			detail = fmt.Sprintf("legacy projection drift=%d", snapshot.ProjectionDrift.Count)
		}
		appendStatusIssue("projection drift", snapshot.ProjectionDrift.Status, detail)
	}
	if snapshot.Delivery.Status != "green" {
		detail := snapshot.Delivery.Error
		if detail == "" {
			detail = fmt.Sprintf("pending=%d due=%d failed_since=%d abandoned_since=%d", snapshot.Delivery.Pending, snapshot.Delivery.DueNow, snapshot.Delivery.FailedSince, snapshot.Delivery.AbandonedSince)
		}
		appendStatusIssue("delivery queues", snapshot.Delivery.Status, detail)
	}
	if snapshot.Delivery.Posture.Status != "green" {
		appendStatusIssue("delivery posture", snapshot.Delivery.Posture.Status, snapshot.Delivery.Posture.Message)
	}
	if snapshot.Verifliers.Status != "" && snapshot.Verifliers.Status != "green" {
		appendStatusIssue("verifliers", snapshot.Verifliers.Status, firstNonEmpty(snapshot.Verifliers.Error, snapshot.Verifliers.Message, snapshot.Verifliers.Status))
	}

	summary.Issues = append(redIssues, amberIssues...)
	switch {
	case len(redIssues) > 0:
		summary.Status = "red"
		summary.Message = "fleet has rollout-blocking issues"
	case len(amberIssues) > 0:
		summary.Status = "amber"
		summary.Message = "fleet needs operator attention"
	default:
		summary.Status = "green"
		summary.Message = "fleet checks are green"
	}
	summary.SuggestedNextAction = suggestFleetNextAction(snapshot, summary)
	return summary
}

func suggestFleetNextAction(snapshot FleetSnapshot, summary FleetSummary) string {
	switch {
	case summary.StaleProcesses > 0:
		return "Investigate stale process heartbeats before advancing rollout or relying on fleet status."
	case snapshot.BucketCoverage.Status == "red":
		return "Fix jetpack_monitor_hosts bucket coverage before relying on dynamic ownership."
	case snapshot.ProjectionDrift.Status == "red":
		return "Run rollout projection-drift --limit=100 and fix legacy projection drift before continuing."
	case snapshot.Delivery.Status == "red":
		return "Investigate failed or abandoned delivery rows before moving delivery ownership."
	case snapshot.Verifliers.Status == "red":
		return "Fix Veriflier discovery registry or agent telemetry before enabling active discovery."
	case summary.RedProcesses > 0:
		return "Open the affected host dashboard and resolve red process health before rollout."
	case snapshot.Delivery.Status == "amber":
		return "Watch delivery-check and confirm due deliveries drain before moving delivery ownership."
	case snapshot.Delivery.Posture.Status == "amber":
		return "Confirm DELIVERY_OWNER_HOST posture before enabling or moving delivery workers."
	case snapshot.Verifliers.Status == "amber":
		return "Resolve Veriflier registry or telemetry warnings before moving discovery from shadow to active."
	case snapshot.BucketCoverage.Status == "amber":
		return "Confirm whether the fleet is still in pinned rollout before expecting dynamic bucket coverage."
	case summary.MonitorProcesses == 0:
		return "Confirm monitor processes are publishing jetpack_monitor_process_health snapshots."
	case summary.AmberProcesses > 0:
		return "Open amber host dashboards and clear dependency warnings before the next rollout step."
	default:
		return "Fleet checks look healthy; continue normal monitoring and rollout validation."
	}
}

func cloneFleetSnapshot(in FleetSnapshot) FleetSnapshot {
	out := in
	out.Summary.Issues = append([]string(nil), in.Summary.Issues...)
	out.Processes = append([]FleetProcess(nil), in.Processes...)
	for i := range out.Processes {
		out.Processes[i].DependencyHealth = append([]fleethealth.DependencyHealth(nil), in.Processes[i].DependencyHealth...)
		for j := range out.Processes[i].DependencyHealth {
			out.Processes[i].DependencyHealth[j].Details = cloneStringMap(in.Processes[i].DependencyHealth[j].Details)
		}
	}
	out.ProcessCounts = make(map[string]int, len(in.ProcessCounts))
	for key, value := range in.ProcessCounts {
		out.ProcessCounts[key] = value
	}
	out.BucketCoverage.Hosts = append([]FleetBucketHost(nil), in.BucketCoverage.Hosts...)
	out.Delivery.Tables = append([]FleetDeliveryTable(nil), in.Delivery.Tables...)
	out.Delivery.Posture.EnabledHosts = append([]string(nil), in.Delivery.Posture.EnabledHosts...)
	out.Delivery.Posture.OwnerHosts = append([]string(nil), in.Delivery.Posture.OwnerHosts...)
	out.Dependencies = append([]FleetDependencySummary(nil), in.Dependencies...)
	out.Verifliers.DiscoveryModes = append([]FleetVeriflierDiscoveryMode(nil), in.Verifliers.DiscoveryModes...)
	for i := range out.Verifliers.DiscoveryModes {
		out.Verifliers.DiscoveryModes[i].Hosts = append([]string(nil), in.Verifliers.DiscoveryModes[i].Hosts...)
	}
	out.Verifliers.Vantages = append([]FleetVeriflierVantageSummary(nil), in.Verifliers.Vantages...)
	out.Verifliers.Agents = append([]FleetVeriflierAgentSummary(nil), in.Verifliers.Agents...)
	for i := range out.Verifliers.Agents {
		out.Verifliers.Agents[i].Protocols = append([]string(nil), in.Verifliers.Agents[i].Protocols...)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func countFleetProcesses(processes []FleetProcess) map[string]int {
	out := map[string]int{}
	for _, process := range processes {
		out[process.ProcessType]++
	}
	return out
}

func sortedStringKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func statusRank(status string) int {
	switch status {
	case "red":
		return 3
	case "amber":
		return 2
	case "green":
		return 1
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *Server) SetFleetSource(source FleetSource) {
	s.mu.Lock()
	s.fleetSource = source
	s.mu.Unlock()
}

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	if rejectNonGet(w, r) {
		return
	}
	setDashboardNoStoreHeaders(w)
	snapshot, err := s.FleetSnapshot(r.Context())
	if err != nil {
		if errors.Is(err, ErrFleetSourceNotConfigured) {
			http.Error(w, "fleet dashboard source is not configured", http.StatusServiceUnavailable)
			return
		}
		log.Printf("fleet dashboard: %v", err)
		http.Error(w, "fleet dashboard query failed", http.StatusInternalServerError)
		return
	}
	setDashboardJSONHeaders(w)
	_ = json.NewEncoder(w).Encode(snapshot)
}

// ErrFleetSourceNotConfigured is returned when callers request a fleet
// snapshot before wiring a backing store.
var ErrFleetSourceNotConfigured = errors.New("fleet dashboard source is not configured")

// FleetSnapshot returns the current fleet dashboard model used by both the
// authenticated API and legacy dashboard JSON endpoint.
func (s *Server) FleetSnapshot(ctx context.Context) (FleetSnapshot, error) {
	s.mu.RLock()
	source := s.fleetSource
	s.mu.RUnlock()
	if source == nil {
		return FleetSnapshot{}, ErrFleetSourceNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, defaultFleetRequestTimeout)
	defer cancel()
	snapshot, err := source.Snapshot(ctx)
	if err != nil {
		return FleetSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Server) handleFleetIndex(w http.ResponseWriter, r *http.Request) {
	if rejectNonGet(w, r) {
		return
	}
	setDashboardHTMLHeaders(w)
	fmt.Fprint(w, fleetDashboardHTML)
}

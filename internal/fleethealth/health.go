// Package fleethealth publishes durable process health snapshots for fleet views.
package fleethealth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ProcessMonitor   = "monitor"
	ProcessDeliverer = "deliverer"

	StateRunning  = "running"
	StateStopping = "stopping"
	StateStopped  = "stopped"
	StateIdle     = "idle"

	HealthGreen = "green"
	HealthAmber = "amber"
	HealthRed   = "red"
)

// DependencyHealth is a compact dependency status snapshot suitable for JSON
// storage and fleet dashboard summaries.
type DependencyHealth struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

// Snapshot is the local process state written to jetmon_process_health.
type Snapshot struct {
	ProcessID              string
	HostID                 string
	ProcessType            string
	PID                    int
	Version                string
	BuildDate              string
	GoVersion              string
	State                  string
	HealthStatus           string
	StartedAt              time.Time
	UpdatedAt              time.Time
	BucketMin              *int
	BucketMax              *int
	BucketOwnership        string
	APIPort                *int
	DashboardPort          *int
	DeliveryWorkersEnabled bool
	DeliveryOwnerHost      string
	WorkerCount            int
	ActiveChecks           int
	QueueDepth             int
	RetryQueueSize         int
	WPCOMCircuitOpen       bool
	WPCOMQueueDepth        int
	GoSysMemMB             int
	DependencyHealth       []DependencyHealth
}

// ProcessID returns the stable row key for one process type on one host.
func ProcessID(hostID, processType string) string {
	hostID = strings.TrimSpace(hostID)
	processType = strings.TrimSpace(processType)
	if hostID == "" || processType == "" {
		return ""
	}
	return hostID + ":" + processType
}

// Upsert writes the current snapshot for a long-running process.
func Upsert(ctx context.Context, db *sql.DB, snapshot Snapshot) error {
	if db == nil {
		return errors.New("database pool is not initialized")
	}
	normalized, err := normalizeSnapshot(snapshot)
	if err != nil {
		return err
	}
	deps, err := json.Marshal(normalized.DependencyHealth)
	if err != nil {
		return fmt.Errorf("marshal dependency health: %w", err)
	}

	_, err = db.ExecContext(ctx, upsertSnapshotSQL,
		normalized.ProcessID,
		normalized.HostID,
		normalized.ProcessType,
		normalized.PID,
		normalized.Version,
		normalized.BuildDate,
		normalized.GoVersion,
		normalized.State,
		normalized.HealthStatus,
		normalized.StartedAt,
		normalized.UpdatedAt,
		nullableInt(normalized.BucketMin),
		nullableInt(normalized.BucketMax),
		normalized.BucketOwnership,
		nullableInt(normalized.APIPort),
		nullableInt(normalized.DashboardPort),
		boolInt(normalized.DeliveryWorkersEnabled),
		normalized.DeliveryOwnerHost,
		normalized.WorkerCount,
		normalized.ActiveChecks,
		normalized.QueueDepth,
		normalized.RetryQueueSize,
		boolInt(normalized.WPCOMCircuitOpen),
		normalized.WPCOMQueueDepth,
		normalized.GoSysMemMB,
		string(deps),
	)
	if err != nil {
		return fmt.Errorf("upsert process health: %w", err)
	}
	return nil
}

// MarkStopped records a terminal stopped state during graceful shutdown.
func MarkStopped(ctx context.Context, db *sql.DB, processID string, when time.Time) error {
	if db == nil {
		return errors.New("database pool is not initialized")
	}
	processID = strings.TrimSpace(processID)
	if processID == "" {
		return errors.New("process id is required")
	}
	if when.IsZero() {
		when = time.Now().UTC()
	}
	_, err := db.ExecContext(ctx,
		`UPDATE jetmon_process_health
		   SET state = ?, health_status = ?, updated_at = ?
		 WHERE process_id = ?`,
		StateStopped,
		HealthAmber,
		when.UTC(),
		processID,
	)
	if err != nil {
		return fmt.Errorf("mark process stopped: %w", err)
	}
	return nil
}

func normalizeSnapshot(snapshot Snapshot) (Snapshot, error) {
	snapshot.HostID = strings.TrimSpace(snapshot.HostID)
	snapshot.ProcessType = strings.TrimSpace(snapshot.ProcessType)
	if snapshot.HostID == "" {
		return Snapshot{}, errors.New("host id is required")
	}
	if snapshot.ProcessType == "" {
		return Snapshot{}, errors.New("process type is required")
	}
	snapshot.ProcessID = strings.TrimSpace(snapshot.ProcessID)
	if snapshot.ProcessID == "" {
		snapshot.ProcessID = ProcessID(snapshot.HostID, snapshot.ProcessType)
	}
	if snapshot.ProcessID == "" {
		return Snapshot{}, errors.New("process id is required")
	}
	snapshot.State = strings.TrimSpace(snapshot.State)
	if snapshot.State == "" {
		snapshot.State = StateRunning
	}
	if !validState(snapshot.State) {
		return Snapshot{}, fmt.Errorf("invalid process state %q", snapshot.State)
	}
	snapshot.HealthStatus = strings.TrimSpace(snapshot.HealthStatus)
	if snapshot.HealthStatus == "" {
		snapshot.HealthStatus = RollupHealthStatus(snapshot.DependencyHealth)
	}
	if !validHealthStatus(snapshot.HealthStatus) {
		return Snapshot{}, fmt.Errorf("invalid health status %q", snapshot.HealthStatus)
	}
	if snapshot.StartedAt.IsZero() {
		snapshot.StartedAt = time.Now().UTC()
	}
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = time.Now().UTC()
	}
	snapshot.StartedAt = snapshot.StartedAt.UTC()
	snapshot.UpdatedAt = snapshot.UpdatedAt.UTC()
	return snapshot, nil
}

func validState(state string) bool {
	switch state {
	case StateRunning, StateStopping, StateStopped, StateIdle:
		return true
	default:
		return false
	}
}

func validHealthStatus(status string) bool {
	switch status {
	case HealthGreen, HealthAmber, HealthRed:
		return true
	default:
		return false
	}
}

// RollupHealthStatus reduces dependency snapshots into a green/amber/red health
// status. Unknown dependency status is treated as amber because it needs
// operator attention but is not itself proof of failure.
func RollupHealthStatus(entries []DependencyHealth) string {
	status := HealthGreen
	for _, entry := range entries {
		switch entry.Status {
		case HealthRed:
			return HealthRed
		case HealthAmber:
			status = HealthAmber
		case HealthGreen:
		default:
			if status == HealthGreen {
				status = HealthAmber
			}
		}
	}
	return status
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

const upsertSnapshotSQL = `
INSERT INTO jetmon_process_health (
	process_id,
	host_id,
	process_type,
	pid,
	version,
	build_date,
	go_version,
	state,
	health_status,
	started_at,
	updated_at,
	bucket_min,
	bucket_max,
	bucket_ownership,
	api_port,
	dashboard_port,
	delivery_workers_enabled,
	delivery_owner_host,
	worker_count,
	active_checks,
	queue_depth,
	retry_queue_size,
	wpcom_circuit_open,
	wpcom_queue_depth,
	go_sys_mem_mb,
	dependency_health
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
	host_id = VALUES(host_id),
	process_type = VALUES(process_type),
	pid = VALUES(pid),
	version = VALUES(version),
	build_date = VALUES(build_date),
	go_version = VALUES(go_version),
	state = VALUES(state),
	health_status = VALUES(health_status),
	started_at = VALUES(started_at),
	updated_at = VALUES(updated_at),
	bucket_min = VALUES(bucket_min),
	bucket_max = VALUES(bucket_max),
	bucket_ownership = VALUES(bucket_ownership),
	api_port = VALUES(api_port),
	dashboard_port = VALUES(dashboard_port),
	delivery_workers_enabled = VALUES(delivery_workers_enabled),
	delivery_owner_host = VALUES(delivery_owner_host),
	worker_count = VALUES(worker_count),
	active_checks = VALUES(active_checks),
	queue_depth = VALUES(queue_depth),
	retry_queue_size = VALUES(retry_queue_size),
	wpcom_circuit_open = VALUES(wpcom_circuit_open),
	wpcom_queue_depth = VALUES(wpcom_queue_depth),
	go_sys_mem_mb = VALUES(go_sys_mem_mb),
	dependency_health = VALUES(dependency_health)`

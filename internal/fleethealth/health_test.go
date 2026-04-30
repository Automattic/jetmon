package fleethealth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestProcessID(t *testing.T) {
	if got := ProcessID(" host-a ", " monitor "); got != "host-a:monitor" {
		t.Fatalf("ProcessID() = %q, want host-a:monitor", got)
	}
	if got := ProcessID("", "monitor"); got != "" {
		t.Fatalf("ProcessID(empty host) = %q, want empty", got)
	}
}

func TestUpsertSnapshot(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	started := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	updated := started.Add(time.Minute)
	bucketMin, bucketMax := 0, 99
	apiPort, dashboardPort := 8090, 8080

	mock.ExpectExec("INSERT INTO jetmon_process_health").
		WithArgs(
			"host-a:monitor",
			"host-a",
			ProcessMonitor,
			123,
			"abc123",
			"2026-04-30T10:00:00Z",
			"go1.26.2",
			StateRunning,
			HealthGreen,
			started,
			updated,
			bucketMin,
			bucketMax,
			"pinned range=0-99",
			apiPort,
			dashboardPort,
			1,
			"host-a",
			12,
			3,
			4,
			5,
			0,
			2,
			88,
			`[{"name":"mysql","status":"green","checked_at":"2026-04-30T10:01:00Z"}]`,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = Upsert(context.Background(), sqlDB, Snapshot{
		HostID:                 "host-a",
		ProcessType:            ProcessMonitor,
		PID:                    123,
		Version:                "abc123",
		BuildDate:              "2026-04-30T10:00:00Z",
		GoVersion:              "go1.26.2",
		State:                  StateRunning,
		HealthStatus:           HealthGreen,
		StartedAt:              started,
		UpdatedAt:              updated,
		BucketMin:              &bucketMin,
		BucketMax:              &bucketMax,
		BucketOwnership:        "pinned range=0-99",
		APIPort:                &apiPort,
		DashboardPort:          &dashboardPort,
		DeliveryWorkersEnabled: true,
		DeliveryOwnerHost:      "host-a",
		WorkerCount:            12,
		ActiveChecks:           3,
		QueueDepth:             4,
		RetryQueueSize:         5,
		WPCOMQueueDepth:        2,
		GoSysMemMB:             88,
		DependencyHealth: []DependencyHealth{{
			Name:      "mysql",
			Status:    "green",
			CheckedAt: updated,
		}},
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestUpsertValidatesRequiredFields(t *testing.T) {
	sqlDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	err = Upsert(context.Background(), sqlDB, Snapshot{ProcessType: ProcessMonitor})
	if err == nil || !strings.Contains(err.Error(), "host id is required") {
		t.Fatalf("Upsert() error = %v, want host id validation", err)
	}
}

func TestUpsertValidatesStateAndHealthStatus(t *testing.T) {
	sqlDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	err = Upsert(context.Background(), sqlDB, Snapshot{
		HostID:       "host-a",
		ProcessType:  ProcessMonitor,
		State:        "starting",
		HealthStatus: HealthGreen,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid process state") {
		t.Fatalf("Upsert() error = %v, want invalid process state validation", err)
	}

	err = Upsert(context.Background(), sqlDB, Snapshot{
		HostID:       "host-a",
		ProcessType:  ProcessMonitor,
		State:        StateRunning,
		HealthStatus: "blue",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid health status") {
		t.Fatalf("Upsert() error = %v, want invalid health status validation", err)
	}
}

func TestMarkStopped(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	when := time.Date(2026, 4, 30, 10, 2, 0, 0, time.UTC)
	mock.ExpectExec("UPDATE jetmon_process_health").
		WithArgs(StateStopped, HealthAmber, when, "host-a:deliverer").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := MarkStopped(context.Background(), sqlDB, "host-a:deliverer", when); err != nil {
		t.Fatalf("MarkStopped() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestListSnapshots(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	started := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	updated := started.Add(time.Minute)
	rows := sqlmock.NewRows([]string{
		"process_id",
		"host_id",
		"process_type",
		"pid",
		"version",
		"build_date",
		"go_version",
		"state",
		"health_status",
		"started_at",
		"updated_at",
		"bucket_min",
		"bucket_max",
		"bucket_ownership",
		"api_port",
		"dashboard_port",
		"delivery_workers_enabled",
		"delivery_owner_host",
		"worker_count",
		"active_checks",
		"queue_depth",
		"retry_queue_size",
		"wpcom_circuit_open",
		"wpcom_queue_depth",
		"go_sys_mem_mb",
		"dependency_health",
	}).AddRow(
		"host-a:monitor",
		"host-a",
		ProcessMonitor,
		123,
		"abc123",
		"2026-04-30T10:00:00Z",
		"go1.26.2",
		StateRunning,
		HealthAmber,
		started,
		updated,
		0,
		99,
		"pinned range=0-99",
		8090,
		8080,
		1,
		"host-a",
		12,
		3,
		4,
		5,
		1,
		2,
		88,
		`[{"name":"mysql","status":"green","checked_at":"2026-04-30T10:01:00Z"}]`,
	)
	mock.ExpectQuery("SELECT process_id").WillReturnRows(rows)

	snapshots, err := ListSnapshots(context.Background(), sqlDB)
	if err != nil {
		t.Fatalf("ListSnapshots() error = %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshots len = %d, want 1", len(snapshots))
	}
	got := snapshots[0]
	if got.ProcessID != "host-a:monitor" || got.HealthStatus != HealthAmber {
		t.Fatalf("snapshot = %+v, want host-a amber", got)
	}
	if got.BucketMin == nil || *got.BucketMin != 0 || got.APIPort == nil || *got.APIPort != 8090 {
		t.Fatalf("nullable ints not decoded: BucketMin=%v APIPort=%v", got.BucketMin, got.APIPort)
	}
	if !got.DeliveryWorkersEnabled || !got.WPCOMCircuitOpen {
		t.Fatalf("bools not decoded: delivery=%v wpcom=%v", got.DeliveryWorkersEnabled, got.WPCOMCircuitOpen)
	}
	if len(got.DependencyHealth) != 1 || got.DependencyHealth[0].Name != "mysql" {
		t.Fatalf("DependencyHealth = %+v, want mysql", got.DependencyHealth)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestRollupHealthStatus(t *testing.T) {
	tests := []struct {
		name string
		in   []DependencyHealth
		want string
	}{
		{name: "empty is green", want: HealthGreen},
		{name: "green entries are green", in: []DependencyHealth{{Status: HealthGreen}}, want: HealthGreen},
		{name: "amber wins over green", in: []DependencyHealth{{Status: HealthGreen}, {Status: HealthAmber}}, want: HealthAmber},
		{name: "red wins", in: []DependencyHealth{{Status: HealthAmber}, {Status: HealthRed}}, want: HealthRed},
		{name: "unknown status is amber", in: []DependencyHealth{{Status: "unknown"}}, want: HealthAmber},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RollupHealthStatus(tt.in); got != tt.want {
				t.Fatalf("RollupHealthStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

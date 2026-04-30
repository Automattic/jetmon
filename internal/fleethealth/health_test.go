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
			StateHealthy,
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
		State:                  StateHealthy,
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
		MemRSSMB:               88,
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

func TestMarkStopped(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer sqlDB.Close()

	when := time.Date(2026, 4, 30, 10, 2, 0, 0, time.UTC)
	mock.ExpectExec("UPDATE jetmon_process_health").
		WithArgs(StateStopped, when, "host-a:deliverer").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := MarkStopped(context.Background(), sqlDB, "host-a:deliverer", when); err != nil {
		t.Fatalf("MarkStopped() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

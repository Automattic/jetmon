package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/Automattic/jetmon/internal/fleethealth"
)

func TestSummarizeFleetFlagsStaleAndDrift(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	processes := summarizeFleetProcesses([]fleethealth.Snapshot{
		{
			ProcessID:    "host-a:monitor",
			HostID:       "host-a",
			ProcessType:  fleethealth.ProcessMonitor,
			State:        fleethealth.StateRunning,
			HealthStatus: fleethealth.HealthGreen,
			UpdatedAt:    now.Add(-20 * time.Minute),
			DependencyHealth: []fleethealth.DependencyHealth{
				{Name: "mysql", Status: fleethealth.HealthGreen},
			},
		},
		{
			ProcessID:    "host-b:deliverer",
			HostID:       "host-b",
			ProcessType:  fleethealth.ProcessDeliverer,
			State:        fleethealth.StateRunning,
			HealthStatus: fleethealth.HealthAmber,
			UpdatedAt:    now.Add(-time.Minute),
			DependencyHealth: []fleethealth.DependencyHealth{
				{Name: "statsd", Status: fleethealth.HealthAmber, LastError: "not initialized"},
			},
		},
	}, now, 10*time.Minute)

	snapshot := FleetSnapshot{
		Processes:       processes,
		ProcessCounts:   countFleetProcesses(processes),
		BucketCoverage:  FleetBucketCoverage{Status: "green"},
		Delivery:        FleetDeliverySummary{Status: "green", Posture: FleetDeliveryPosture{Status: "green"}},
		ProjectionDrift: FleetProjectionDrift{Status: "red", Count: 2},
		Dependencies:    summarizeFleetDependencies(processes),
	}
	summary := summarizeFleet(snapshot)
	if summary.Status != "red" {
		t.Fatalf("summary status = %q, want red", summary.Status)
	}
	if summary.StaleProcesses != 1 {
		t.Fatalf("StaleProcesses = %d, want 1", summary.StaleProcesses)
	}
	if summary.MonitorProcesses != 1 || summary.DelivererProcesses != 1 {
		t.Fatalf("process counts = monitor %d deliverer %d, want 1/1", summary.MonitorProcesses, summary.DelivererProcesses)
	}
	if !containsIssue(summary.Issues, "heartbeat stale") {
		t.Fatalf("issues = %#v, want stale heartbeat issue", summary.Issues)
	}
	if !containsIssue(summary.Issues, "legacy projection drift=2") {
		t.Fatalf("issues = %#v, want projection drift issue", summary.Issues)
	}
	if !strings.Contains(summary.SuggestedNextAction, "stale process") {
		t.Fatalf("SuggestedNextAction = %q, want stale process guidance", summary.SuggestedNextAction)
	}
}

func TestSummarizeFleetBucketCoverage(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	coverage := summarizeFleetBucketCoverage([]FleetBucketHost{
		{HostID: "host-a", BucketMin: 0, BucketMax: 4, LastHeartbeat: now.Add(-time.Second), Status: "active"},
		{HostID: "host-b", BucketMin: 5, BucketMax: 9, LastHeartbeat: now.Add(-2 * time.Second), Status: "active"},
	}, 10, 30*time.Second, now, nil)
	if coverage.Status != "green" {
		t.Fatalf("coverage status = %q, want green (%s)", coverage.Status, coverage.Error)
	}

	coverage = summarizeFleetBucketCoverage([]FleetBucketHost{
		{HostID: "host-a", BucketMin: 0, BucketMax: 3, LastHeartbeat: now, Status: "active"},
		{HostID: "host-b", BucketMin: 5, BucketMax: 9, LastHeartbeat: now, Status: "active"},
	}, 10, 30*time.Second, now, nil)
	if coverage.Status != "red" || !strings.Contains(coverage.Error, "gap") {
		t.Fatalf("coverage = %+v, want gap error", coverage)
	}
}

func TestSummarizeFleetDeliveryPosture(t *testing.T) {
	posture := summarizeFleetDeliveryPosture([]FleetProcess{
		{HostID: "host-a", DeliveryWorkersEnabled: true},
		{HostID: "host-b", DeliveryWorkersEnabled: true},
	})
	if posture.Status != "amber" {
		t.Fatalf("posture status = %q, want amber", posture.Status)
	}
	if !strings.Contains(posture.Message, "without DELIVERY_OWNER_HOST") {
		t.Fatalf("posture message = %q, want owner warning", posture.Message)
	}

	posture = summarizeFleetDeliveryPosture([]FleetProcess{
		{HostID: "host-a", DeliveryWorkersEnabled: true, DeliveryOwnerHost: "host-a"},
		{HostID: "host-b", DeliveryOwnerHost: "host-a"},
	})
	if posture.Status != "green" {
		t.Fatalf("posture status = %q, want green", posture.Status)
	}
	if len(posture.OwnerHosts) != 1 || posture.OwnerHosts[0] != "host-a" {
		t.Fatalf("OwnerHosts = %#v, want host-a", posture.OwnerHosts)
	}
}

func TestSummarizeFleetDependencies(t *testing.T) {
	processes := []FleetProcess{
		{
			ProcessID: "host-a:monitor",
			DependencyHealth: []fleethealth.DependencyHealth{
				{Name: "mysql", Status: fleethealth.HealthGreen},
				{Name: "wpcom", Status: fleethealth.HealthRed, LastError: "500"},
			},
		},
		{
			ProcessID: "host-b:monitor",
			Stale:     true,
			DependencyHealth: []fleethealth.DependencyHealth{
				{Name: "mysql", Status: fleethealth.HealthAmber, LastError: "slow"},
			},
		},
	}
	deps := summarizeFleetDependencies(processes)
	if len(deps) != 2 {
		t.Fatalf("deps len = %d, want 2", len(deps))
	}
	if deps[0].Name != "wpcom" || deps[0].Status != "red" {
		t.Fatalf("deps[0] = %+v, want red wpcom first", deps[0])
	}
	if deps[1].Name != "mysql" || deps[1].Status != "amber" || deps[1].StaleCount != 1 {
		t.Fatalf("deps[1] = %+v, want amber mysql with stale count", deps[1])
	}
}

func containsIssue(issues []string, want string) bool {
	for _, issue := range issues {
		if strings.Contains(issue, want) {
			return true
		}
	}
	return false
}

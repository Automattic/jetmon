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
	if summary.RedProcesses != 1 {
		t.Fatalf("RedProcesses = %d, want stale process counted as red", summary.RedProcesses)
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
	}, 10, 30*time.Second, now, nil, nil)
	if coverage.Status != "green" {
		t.Fatalf("coverage status = %q, want green (%s)", coverage.Status, coverage.Error)
	}
	if coverage.Mode != "dynamic" {
		t.Fatalf("coverage mode = %q, want dynamic", coverage.Mode)
	}

	coverage = summarizeFleetBucketCoverage([]FleetBucketHost{
		{HostID: "host-a", BucketMin: 0, BucketMax: 3, LastHeartbeat: now, Status: "active"},
		{HostID: "host-b", BucketMin: 5, BucketMax: 9, LastHeartbeat: now, Status: "active"},
	}, 10, 30*time.Second, now, nil, nil)
	if coverage.Status != "red" || !strings.Contains(coverage.Error, "gap") {
		t.Fatalf("coverage = %+v, want gap error", coverage)
	}

	coverage = summarizeFleetBucketCoverage(nil, 10, 30*time.Second, now, nil, []FleetProcess{{
		ProcessType:     fleethealth.ProcessMonitor,
		BucketOwnership: "pinned range=0-4",
	}})
	if coverage.Status != "amber" || coverage.Mode != "pinned" {
		t.Fatalf("coverage = %+v, want pinned amber", coverage)
	}

	coverage = summarizeFleetBucketCoverage([]FleetBucketHost{
		{HostID: "host-a", BucketMin: 0, BucketMax: 9, LastHeartbeat: now.Add(-time.Hour), Status: "active"},
	}, 10, 30*time.Second, now, nil, []FleetProcess{{
		ProcessType:     fleethealth.ProcessMonitor,
		BucketOwnership: "pinned range=0-9",
	}})
	if coverage.Status != "amber" || coverage.Mode != "pinned" || strings.Contains(coverage.Error, "stale") {
		t.Fatalf("coverage = %+v, want pinned mode to ignore stale dynamic rows", coverage)
	}

	coverage = summarizeFleetBucketCoverage(nil, 10, 30*time.Second, now, nil, []FleetProcess{
		{ProcessType: fleethealth.ProcessMonitor, BucketOwnership: "pinned range=0-4"},
		{ProcessType: fleethealth.ProcessMonitor, BucketOwnership: "dynamic jetmon_hosts"},
	})
	if coverage.Status != "amber" || coverage.Mode != "mixed" {
		t.Fatalf("coverage = %+v, want mixed amber", coverage)
	}

	coverage = summarizeFleetBucketCoverage([]FleetBucketHost{
		{HostID: "host-a", BucketMin: 0, BucketMax: 9, LastHeartbeat: now, Status: "active"},
	}, 10, 30*time.Second, now, nil, []FleetProcess{
		{ProcessType: fleethealth.ProcessMonitor, BucketOwnership: "pinned range=0-4", Stale: true},
		{ProcessType: fleethealth.ProcessMonitor, BucketOwnership: "dynamic jetmon_hosts"},
	})
	if coverage.Status != "green" || coverage.Mode != "dynamic" {
		t.Fatalf("coverage = %+v, want stale pinned snapshots ignored for ownership mode", coverage)
	}
}

func TestSummarizeFleetDeliveryPosture(t *testing.T) {
	posture := summarizeFleetDeliveryPosture([]FleetProcess{
		{HostID: "host-a", DeliveryWorkersEnabled: true},
		{HostID: "host-b", DeliveryWorkersEnabled: true},
	}, 0)
	if posture.Status != "amber" {
		t.Fatalf("posture status = %q, want amber", posture.Status)
	}
	if !strings.Contains(posture.Message, "without DELIVERY_OWNER_HOST") {
		t.Fatalf("posture message = %q, want owner warning", posture.Message)
	}

	posture = summarizeFleetDeliveryPosture([]FleetProcess{
		{HostID: "host-a", DeliveryWorkersEnabled: true, DeliveryOwnerHost: "host-a"},
		{HostID: "host-b", DeliveryOwnerHost: "host-a"},
	}, 0)
	if posture.Status != "green" {
		t.Fatalf("posture status = %q, want green", posture.Status)
	}
	if len(posture.OwnerHosts) != 1 || posture.OwnerHosts[0] != "host-a" {
		t.Fatalf("OwnerHosts = %#v, want host-a", posture.OwnerHosts)
	}

	posture = summarizeFleetDeliveryPosture([]FleetProcess{
		{HostID: "host-a", DeliveryWorkersEnabled: true},
	}, 0)
	if posture.Status != "amber" || !strings.Contains(posture.Message, "without DELIVERY_OWNER_HOST") {
		t.Fatalf("posture = %+v, want unset owner warning", posture)
	}

	posture = summarizeFleetDeliveryPosture([]FleetProcess{
		{HostID: "host-a", DeliveryWorkersEnabled: true, DeliveryOwnerHost: "host-a"},
		{HostID: "host-b", DeliveryWorkersEnabled: true},
	}, 0)
	if posture.Status != "amber" || !strings.Contains(posture.Message, "mix") {
		t.Fatalf("posture = %+v, want mixed owner warning", posture)
	}

	posture = summarizeFleetDeliveryPosture([]FleetProcess{
		{HostID: "host-a", State: fleethealth.StateStopped, DeliveryWorkersEnabled: true, DeliveryOwnerHost: "host-a"},
		{HostID: "host-b", Stale: true, DeliveryWorkersEnabled: true, DeliveryOwnerHost: "host-b"},
	}, 0)
	if posture.Status != "green" || posture.EnabledProcessCount != 0 {
		t.Fatalf("posture = %+v, want inactive processes ignored", posture)
	}

	posture = summarizeFleetDeliveryPosture(nil, 3)
	if posture.Status != "amber" || !strings.Contains(posture.Message, "queued") {
		t.Fatalf("posture = %+v, want queued delivery warning", posture)
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

func TestSummarizeFleetProcessesOrdersUnhealthyFirst(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	processes := summarizeFleetProcesses([]fleethealth.Snapshot{
		{ProcessID: "host-c:monitor", HostID: "host-c", ProcessType: fleethealth.ProcessMonitor, HealthStatus: fleethealth.HealthGreen, UpdatedAt: now},
		{ProcessID: "host-d:deliverer", HostID: "host-d", ProcessType: fleethealth.ProcessDeliverer, HealthStatus: fleethealth.HealthGreen, UpdatedAt: now},
		{ProcessID: "host-b:monitor", HostID: "host-b", ProcessType: fleethealth.ProcessMonitor, HealthStatus: fleethealth.HealthAmber, UpdatedAt: now},
		{ProcessID: "host-a:monitor", HostID: "host-a", ProcessType: fleethealth.ProcessMonitor, HealthStatus: fleethealth.HealthGreen, UpdatedAt: now.Add(-time.Hour)},
	}, now, 10*time.Minute)
	if got := processes[0].ProcessID; got != "host-a:monitor" {
		t.Fatalf("first process = %q, want stale host first", got)
	}
	if got := processes[1].ProcessID; got != "host-b:monitor" {
		t.Fatalf("second process = %q, want amber host second", got)
	}
	if got := processes[2].ProcessID; got != "host-c:monitor" {
		t.Fatalf("third process = %q, want healthy monitors before deliverers", got)
	}
}

func TestFleetStoreCachedSnapshotIsCloned(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	store := NewFleetStore(nil, FleetStoreOptions{CacheTTL: time.Minute})
	store.storeCachedSnapshot(FleetSnapshot{
		GeneratedAt:   now,
		Summary:       FleetSummary{Issues: []string{"first issue"}},
		ProcessCounts: map[string]int{fleethealth.ProcessMonitor: 1},
		Processes: []FleetProcess{{
			ProcessID: "host-a:monitor",
			DependencyHealth: []fleethealth.DependencyHealth{{
				Name:   "mysql",
				Status: fleethealth.HealthGreen,
			}},
		}},
		BucketCoverage: FleetBucketCoverage{Hosts: []FleetBucketHost{{HostID: "host-a"}}},
		Delivery: FleetDeliverySummary{
			Tables:  []FleetDeliveryTable{{Kind: "webhook", Pending: 1}},
			Posture: FleetDeliveryPosture{EnabledHosts: []string{"host-a"}, OwnerHosts: []string{"host-a"}},
		},
		Dependencies: []FleetDependencySummary{{Name: "mysql", Status: "green"}},
	})

	cached, ok := store.cachedSnapshot(now.Add(time.Second))
	if !ok {
		t.Fatal("cachedSnapshot() missed")
	}
	cached.Summary.Issues[0] = "mutated"
	cached.ProcessCounts[fleethealth.ProcessMonitor] = 99
	cached.Processes[0].DependencyHealth[0].Status = fleethealth.HealthRed
	cached.BucketCoverage.Hosts[0].HostID = "mutated"
	cached.Delivery.Tables[0].Pending = 99
	cached.Delivery.Posture.EnabledHosts[0] = "mutated"
	cached.Dependencies[0].Status = "red"

	cachedAgain, ok := store.cachedSnapshot(now.Add(2 * time.Second))
	if !ok {
		t.Fatal("cachedSnapshot() second read missed")
	}
	if cachedAgain.Summary.Issues[0] != "first issue" {
		t.Fatalf("Summary.Issues = %#v, cache was mutated", cachedAgain.Summary.Issues)
	}
	if cachedAgain.ProcessCounts[fleethealth.ProcessMonitor] != 1 {
		t.Fatalf("ProcessCounts = %#v, cache was mutated", cachedAgain.ProcessCounts)
	}
	if cachedAgain.Processes[0].DependencyHealth[0].Status != fleethealth.HealthGreen {
		t.Fatalf("DependencyHealth = %#v, cache was mutated", cachedAgain.Processes[0].DependencyHealth)
	}
	if cachedAgain.BucketCoverage.Hosts[0].HostID != "host-a" {
		t.Fatalf("BucketCoverage.Hosts = %#v, cache was mutated", cachedAgain.BucketCoverage.Hosts)
	}
	if cachedAgain.Delivery.Tables[0].Pending != 1 || cachedAgain.Delivery.Posture.EnabledHosts[0] != "host-a" {
		t.Fatalf("Delivery = %+v, cache was mutated", cachedAgain.Delivery)
	}
	if cachedAgain.Dependencies[0].Status != "green" {
		t.Fatalf("Dependencies = %#v, cache was mutated", cachedAgain.Dependencies)
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

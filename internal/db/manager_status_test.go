package db

import (
	"strings"
	"testing"
	"time"
)

func TestManagerConfigStatusIsSanitized(t *testing.T) {
	m := &Manager{
		selection: endpointSelection{
			Source: "server-map:/private/db-servers.php dataset=misc dc=dfw address=internet",
			Read: []endpointConfig{{
				Label:    "read-a:3306/misc user=reader",
				Password: "read-secret",
			}},
			Write: []endpointConfig{{
				Label:    "write-a:3306/misc user=writer",
				Password: "write-secret",
			}},
			Signature: "source=server-map|read-secret|write-secret",
		},
		loadedAt:       time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		reloadEnabled:  true,
		reloadInterval: 10 * time.Minute,
		nextCheckAt:    time.Date(2026, 5, 18, 12, 10, 0, 0, time.UTC),
	}

	status := m.ConfigStatus()
	if status.Mode != "server_map" {
		t.Fatalf("Mode = %q, want server_map", status.Mode)
	}
	if status.ReloadIntervalSeconds != 600 {
		t.Fatalf("ReloadIntervalSeconds = %d, want 600", status.ReloadIntervalSeconds)
	}
	if got, want := len(status.ReadEndpoints), 1; got != want {
		t.Fatalf("ReadEndpoints len = %d, want %d", got, want)
	}
	serialized := strings.Join(append(status.ReadEndpoints, status.WriteEndpoints...), " ") + " " + status.ActiveFingerprint
	if strings.Contains(serialized, "read-secret") || strings.Contains(serialized, "write-secret") {
		t.Fatalf("status exposed secret material: %s", serialized)
	}

	details := status.Details()
	if details["next_check_at"] == "" {
		t.Fatal("details missing next_check_at")
	}
	if strings.Contains(strings.Join(mapValues(details), " "), "secret") {
		t.Fatalf("details exposed secret material: %#v", details)
	}
}

func TestManagerReloadStatusTracksErrorsAndChanges(t *testing.T) {
	m := &Manager{}
	checked := time.Date(2026, 5, 18, 13, 0, 0, 0, time.UTC)
	changed := checked.Add(time.Minute)
	failed := changed.Add(time.Minute)

	m.markReloadChecked(checked)
	m.markChangeSeen(changed)
	m.markReloadError(failed, errReloadStatusTest{})

	status := m.ConfigStatus()
	if status.LastCheckedAt == nil || !status.LastCheckedAt.Equal(checked) {
		t.Fatalf("LastCheckedAt = %v, want %v", status.LastCheckedAt, checked)
	}
	if status.LastChangeSeenAt == nil || !status.LastChangeSeenAt.Equal(changed) {
		t.Fatalf("LastChangeSeenAt = %v, want %v", status.LastChangeSeenAt, changed)
	}
	if status.LastReloadError != "reload failed" {
		t.Fatalf("LastReloadError = %q, want reload failed", status.LastReloadError)
	}

	m.markReloadSuccess(true, failed.Add(time.Minute))
	status = m.ConfigStatus()
	if status.LastReloadError != "" {
		t.Fatalf("LastReloadError after success = %q, want empty", status.LastReloadError)
	}
	if status.LastReloadedAt == nil {
		t.Fatal("LastReloadedAt is nil after changed success")
	}
}

type errReloadStatusTest struct{}

func (errReloadStatusTest) Error() string { return "reload failed" }

func mapValues(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

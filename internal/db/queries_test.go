package db

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAssignBucketRanges(t *testing.T) {
	tests := []struct {
		name         string
		hostIDs      []string
		bucketTotal  int
		bucketTarget int
		want         map[string][2]int
	}{
		{
			name:         "single host claims all buckets up to target",
			hostIDs:      []string{"host-a"},
			bucketTotal:  10,
			bucketTarget: 10,
			want: map[string][2]int{
				"host-a": {0, 9},
			},
		},
		{
			name:         "multiple hosts split coverage evenly",
			hostIDs:      []string{"host-a", "host-b", "host-c"},
			bucketTotal:  10,
			bucketTarget: 10,
			want: map[string][2]int{
				"host-a": {0, 3},
				"host-b": {4, 6},
				"host-c": {7, 9},
			},
		},
		{
			name:         "bucket target caps allocation",
			hostIDs:      []string{"host-a", "host-b", "host-c"},
			bucketTotal:  12,
			bucketTarget: 4,
			want: map[string][2]int{
				"host-a": {0, 3},
				"host-b": {4, 7},
				"host-c": {8, 11},
			},
		},
		{
			name:         "extra hosts get empty ranges",
			hostIDs:      []string{"host-a", "host-b", "host-c"},
			bucketTotal:  2,
			bucketTarget: 2,
			want: map[string][2]int{
				"host-a": {0, 0},
				"host-b": {1, 1},
				"host-c": {0, -1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assignBucketRanges(tt.hostIDs, tt.bucketTotal, tt.bucketTarget)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("assignBucketRanges() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCreateSiteSQLChecksBlogIDAndMonitorURL(t *testing.T) {
	if !strings.Contains(createSiteSQL, "WHERE blog_id = ? AND monitor_url = ?") {
		t.Fatalf("createSiteSQL = %q, missing duplicate guard tuple", createSiteSQL)
	}
	if !strings.Contains(createSiteSQL, "WHERE NOT EXISTS") {
		t.Fatalf("createSiteSQL = %q, missing WHERE NOT EXISTS", createSiteSQL)
	}
}

func TestBuildSitePatchUpdatesRequiresAtLeastOneField(t *testing.T) {
	_, _, err := buildSitePatchUpdates(PatchSiteInput{})
	if !errors.Is(err, ErrNoPatchFields) {
		t.Fatalf("buildSitePatchUpdates error = %v, want ErrNoPatchFields", err)
	}
}

func TestBuildSitePatchUpdatesIncludesAllowedFields(t *testing.T) {
	active := true
	status := 2
	clauses, args, err := buildSitePatchUpdates(PatchSiteInput{
		MonitorActive: &active,
		SiteStatus:    &status,
	})
	if err != nil {
		t.Fatalf("buildSitePatchUpdates error = %v", err)
	}
	if len(clauses) != 2 {
		t.Fatalf("clauses len = %d, want 2", len(clauses))
	}
	if clauses[0] != "monitor_active = ?" {
		t.Fatalf("clauses[0] = %q, want monitor_active update", clauses[0])
	}
	if clauses[1] != "site_status = ?" {
		t.Fatalf("clauses[1] = %q, want site_status update", clauses[1])
	}
	if len(args) != 2 || args[0] != 1 || args[1] != 2 {
		t.Fatalf("args = %#v, want [1 2]", args)
	}
}

package db

import (
	"reflect"
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

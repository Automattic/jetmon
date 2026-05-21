package db

import (
	"testing"
)

func TestMaxOpenConnectionsForConfig(t *testing.T) {
	tests := []struct {
		name       string
		gomaxprocs int
		want       int
	}{
		{
			name:       "streaming keeps larger IO floor",
			gomaxprocs: 1,
			want:       64,
		},
		{
			name:       "streaming scales from CPU",
			gomaxprocs: 8,
			want:       128,
		},
		{
			name:       "connection count is capped",
			gomaxprocs: 64,
			want:       256,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maxOpenConnectionsForConfig(nil, tt.gomaxprocs); got != tt.want {
				t.Fatalf("maxOpenConnectionsForConfig() = %d, want %d", got, tt.want)
			}
		})
	}
}

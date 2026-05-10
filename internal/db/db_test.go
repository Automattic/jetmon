package db

import (
	"testing"

	"github.com/Automattic/jetmon/internal/config"
)

func TestMaxOpenConnectionsForConfig(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		gomaxprocs int
		want       int
	}{
		{
			name:       "legacy default keeps modest floor",
			cfg:        &config.Config{},
			gomaxprocs: 1,
			want:       16,
		},
		{
			name:       "legacy scales from CPU",
			cfg:        &config.Config{},
			gomaxprocs: 8,
			want:       64,
		},
		{
			name:       "streaming keeps larger IO floor",
			cfg:        &config.Config{SchedulerEngine: "streaming"},
			gomaxprocs: 1,
			want:       64,
		},
		{
			name:       "streaming scales from CPU",
			cfg:        &config.Config{SchedulerEngine: "streaming"},
			gomaxprocs: 8,
			want:       128,
		},
		{
			name:       "connection count is capped",
			cfg:        &config.Config{SchedulerEngine: "streaming"},
			gomaxprocs: 64,
			want:       256,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := maxOpenConnectionsForConfig(tt.cfg, tt.gomaxprocs); got != tt.want {
				t.Fatalf("maxOpenConnectionsForConfig() = %d, want %d", got, tt.want)
			}
		})
	}
}

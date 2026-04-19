package config

import "testing"

func TestValidate(t *testing.T) {
	base := func() *Config {
		return &Config{
			AuthToken:       "token",
			NumWorkers:      10,
			BucketTotal:     100,
			BucketTarget:    50,
			NetCommsTimeout: 10,
			LogFormat:       "text",
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name:   "valid config",
			mutate: func(_ *Config) {},
		},
		{
			name:    "missing auth token",
			mutate:  func(c *Config) { c.AuthToken = "" },
			wantErr: true,
		},
		{
			name:    "num workers zero",
			mutate:  func(c *Config) { c.NumWorkers = 0 },
			wantErr: true,
		},
		{
			name:    "num workers negative",
			mutate:  func(c *Config) { c.NumWorkers = -1 },
			wantErr: true,
		},
		{
			name:    "bucket total zero",
			mutate:  func(c *Config) { c.BucketTotal = 0 },
			wantErr: true,
		},
		{
			name:    "bucket target zero",
			mutate:  func(c *Config) { c.BucketTarget = 0 },
			wantErr: true,
		},
		{
			name:    "bucket target exceeds bucket total",
			mutate:  func(c *Config) { c.BucketTarget = 101 },
			wantErr: true,
		},
		{
			name:    "bucket target equals bucket total is valid",
			mutate:  func(c *Config) { c.BucketTarget = 100 },
			wantErr: false,
		},
		{
			name:    "net comms timeout zero",
			mutate:  func(c *Config) { c.NetCommsTimeout = 0 },
			wantErr: true,
		},
		{
			name:    "net comms timeout negative",
			mutate:  func(c *Config) { c.NetCommsTimeout = -1 },
			wantErr: true,
		},
		{
			name:    "invalid log format",
			mutate:  func(c *Config) { c.LogFormat = "xml" },
			wantErr: true,
		},
		{
			name:   "json log format is valid",
			mutate: func(c *Config) { c.LogFormat = "json" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(cfg)
			err := validate(cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	cfg := defaults()
	if cfg.NumWorkers <= 0 {
		t.Fatalf("defaults().NumWorkers = %d, want > 0", cfg.NumWorkers)
	}
	if cfg.BucketTotal <= 0 {
		t.Fatalf("defaults().BucketTotal = %d, want > 0", cfg.BucketTotal)
	}
	if cfg.BucketTarget <= 0 || cfg.BucketTarget > cfg.BucketTotal {
		t.Fatalf("defaults().BucketTarget = %d out of range [1, %d]", cfg.BucketTarget, cfg.BucketTotal)
	}
	if cfg.NetCommsTimeout <= 0 {
		t.Fatalf("defaults().NetCommsTimeout = %d, want > 0", cfg.NetCommsTimeout)
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		t.Fatalf("defaults().LogFormat = %q, want text or json", cfg.LogFormat)
	}
}

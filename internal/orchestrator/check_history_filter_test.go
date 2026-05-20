package orchestrator

import (
	"testing"

	"github.com/Automattic/jetmon/internal/checker"
	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

func TestShouldRecordCheckHistory(t *testing.T) {
	upSite := db.Site{BlogID: 1, SiteStatus: statusRunning}
	downSite := db.Site{BlogID: 1, SiteStatus: statusDown}
	success := checker.Result{Success: true}
	failure := checker.Result{Success: false, ErrorCode: checker.ErrorConnect}

	cfg := func(mode string, rate int) *config.Config {
		return &config.Config{CheckHistoryModeDefault: mode, CheckHistorySampleRateDefault: rate}
	}

	cases := []struct {
		name    string
		cfg     *config.Config
		site    db.Site
		res     checker.Result
		counter uint64
		want    bool
	}{
		// disabled
		{"disabled drops success", cfg(config.CheckHistoryModeDisabled, 10), upSite, success, 1, false},
		{"disabled drops failure", cfg(config.CheckHistoryModeDisabled, 10), upSite, failure, 1, false},

		// all
		{"all keeps success", cfg(config.CheckHistoryModeAll, 10), upSite, success, 1, true},
		{"all keeps failure", cfg(config.CheckHistoryModeAll, 10), upSite, failure, 1, true},

		// status_change: only when result disagrees with stored status
		{"status_change drops steady-up", cfg(config.CheckHistoryModeStatusChange, 10), upSite, success, 1, false},
		{"status_change keeps up→down", cfg(config.CheckHistoryModeStatusChange, 10), upSite, failure, 1, true},
		{"status_change keeps down→up", cfg(config.CheckHistoryModeStatusChange, 10), downSite, success, 1, true},
		{"status_change drops still-down", cfg(config.CheckHistoryModeStatusChange, 10), downSite, failure, 1, false},

		// sample: always keep failures + transitions; sample healthy steady-state
		{"sample keeps failure regardless of counter", cfg(config.CheckHistoryModeSample, 10), upSite, failure, 3, true},
		{"sample keeps transition", cfg(config.CheckHistoryModeSample, 10), downSite, success, 3, true},
		{"sample keeps every Nth healthy", cfg(config.CheckHistoryModeSample, 10), upSite, success, 10, true},
		{"sample drops non-Nth healthy", cfg(config.CheckHistoryModeSample, 10), upSite, success, 3, false},
		{"sample rate<=1 keeps all healthy", cfg(config.CheckHistoryModeSample, 1), upSite, success, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRecordCheckHistory(tc.cfg, tc.site, tc.res, tc.counter); got != tc.want {
				t.Errorf("shouldRecordCheckHistory = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldRecordCheckHistoryPerSiteOverride(t *testing.T) {
	cfg := &config.Config{CheckHistoryModeDefault: config.CheckHistoryModeStatusChange, CheckHistorySampleRateDefault: 10}
	upSite := db.Site{BlogID: 1, SiteStatus: statusRunning}
	success := checker.Result{Success: true}

	// Default (status_change) would drop a steady-up success.
	if shouldRecordCheckHistory(cfg, upSite, success, 1) {
		t.Fatal("default status_change should drop steady-up success")
	}

	// Per-site override to "all" keeps it.
	overrideAll := upSite
	overrideAll.CheckHistoryMode = strPtr(config.CheckHistoryModeAll)
	if !shouldRecordCheckHistory(cfg, overrideAll, success, 1) {
		t.Error("per-site 'all' override should keep steady-up success")
	}

	// Per-site override to "disabled" drops even a failure that the default
	// status_change mode would keep.
	overrideDisabled := db.Site{BlogID: 1, SiteStatus: statusRunning, CheckHistoryMode: strPtr(config.CheckHistoryModeDisabled)}
	failure := checker.Result{Success: false}
	if shouldRecordCheckHistory(cfg, overrideDisabled, failure, 1) {
		t.Error("per-site 'disabled' override should drop a transition failure")
	}

	// Invalid per-site mode falls back to the default (status_change).
	overrideJunk := upSite
	overrideJunk.CheckHistoryMode = strPtr("nonsense")
	if shouldRecordCheckHistory(cfg, overrideJunk, success, 1) {
		t.Error("invalid per-site mode should fall back to status_change (drop steady-up)")
	}

	// Per-site sample rate override.
	overrideRate := db.Site{BlogID: 1, SiteStatus: statusRunning,
		CheckHistoryMode: strPtr(config.CheckHistoryModeSample), CheckHistorySampleRate: intPtr(2)}
	if !shouldRecordCheckHistory(cfg, overrideRate, success, 2) {
		t.Error("per-site sample rate=2 should keep counter=2")
	}
	if shouldRecordCheckHistory(cfg, overrideRate, success, 1) {
		t.Error("per-site sample rate=2 should drop counter=1")
	}
}

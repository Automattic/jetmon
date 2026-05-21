package main

import (
	"strings"
	"testing"

	"github.com/Automattic/jetmon/internal/config"
)

func TestDoctorStatsDCheckDisabledCanWarnOrFail(t *testing.T) {
	t.Setenv("STATSD_ADDR", "")

	status, detail := doctorStatsDCheck(&config.Config{}, false)
	if status != "WARN" || !strings.Contains(detail, "StatsD disabled") {
		t.Fatalf("doctorStatsDCheck optional = (%q, %q), want WARN disabled", status, detail)
	}

	status, detail = doctorStatsDCheck(&config.Config{}, true)
	if status != "FAIL" || !strings.Contains(detail, "STATSD_ADDR") {
		t.Fatalf("doctorStatsDCheck required = (%q, %q), want FAIL STATSD_ADDR", status, detail)
	}
}

func TestDoctorWPCOMConfigCheckDisabled(t *testing.T) {
	status, detail := doctorWPCOMConfigCheck(&config.Config{WPCOMNotifyEnable: false})
	if status != "PASS" || detail != "disabled_by_config" {
		t.Fatalf("doctorWPCOMConfigCheck disabled = (%q, %q)", status, detail)
	}
}

func TestDoctorWPCOMConfigCheckLegacyFixture(t *testing.T) {
	cfg := &config.Config{
		WPCOMNotifyEnable:         true,
		WPCOMNotifyMode:           config.WPCOMNotifyModeLegacy,
		WPCOMNotifyLegacyEndpoint: "http://wpcom-fixture.invalid/jetmon/",
	}
	status, detail := doctorWPCOMConfigCheck(cfg)
	if status != "PASS" || !strings.Contains(detail, "non_https_fixture=true") {
		t.Fatalf("doctorWPCOMConfigCheck legacy fixture = (%q, %q)", status, detail)
	}
}

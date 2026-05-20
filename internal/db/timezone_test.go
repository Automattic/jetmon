package db

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestIsUTCZone(t *testing.T) {
	utc := []string{"UTC", "utc", "+00:00", "GMT", "Etc/UTC", " Universal ", "Zulu"}
	for _, z := range utc {
		if !IsUTCZone(z) {
			t.Errorf("IsUTCZone(%q) = false, want true", z)
		}
	}
	notUTC := []string{"America/Chicago", "+05:30", "-08:00", "SYSTEM", "EST", ""}
	for _, z := range notUTC {
		if IsUTCZone(z) {
			t.Errorf("IsUTCZone(%q) = true, want false", z)
		}
	}
}

func TestEffectiveDefaultZoneResolvesSystem(t *testing.T) {
	s := TimeZoneStatus{Global: "SYSTEM", System: "America/Chicago"}
	if got := s.EffectiveDefaultZone(); got != "America/Chicago" {
		t.Errorf("EffectiveDefaultZone = %q, want America/Chicago", got)
	}
	s2 := TimeZoneStatus{Global: "+00:00", System: "America/Chicago"}
	if got := s2.EffectiveDefaultZone(); got != "+00:00" {
		t.Errorf("EffectiveDefaultZone = %q, want +00:00", got)
	}
}

func TestTimeZoneWarning(t *testing.T) {
	// UTC server: no warning regardless of how it's expressed.
	for _, s := range []TimeZoneStatus{
		{Session: "+00:00", Global: "+00:00", System: "UTC"},
		{Session: "+00:00", Global: "SYSTEM", System: "UTC"},
		{Session: "+00:00", Global: "SYSTEM", System: "Etc/UTC"},
	} {
		if msg := s.TimeZoneWarning(); msg != "" {
			t.Errorf("TimeZoneWarning(%+v) = %q, want empty", s, msg)
		}
	}
	// Non-UTC default: warning fires.
	s := TimeZoneStatus{Session: "+00:00", Global: "SYSTEM", System: "America/Chicago"}
	if msg := s.TimeZoneWarning(); msg == "" {
		t.Error("TimeZoneWarning for non-UTC server returned empty, want a warning")
	}
}

func TestQueryTimeZoneStatus(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectQuery("SELECT @@session.time_zone, @@global.time_zone, @@system_time_zone").
		WillReturnRows(sqlmock.NewRows([]string{"s", "g", "sys"}).
			AddRow("+00:00", "SYSTEM", "UTC"))

	tz, err := QueryTimeZoneStatus(context.Background())
	if err != nil {
		t.Fatalf("QueryTimeZoneStatus: %v", err)
	}
	if tz.Session != "+00:00" || tz.Global != "SYSTEM" || tz.System != "UTC" {
		t.Errorf("unexpected tz status: %+v", tz)
	}
	if tz.TimeZoneWarning() != "" {
		t.Errorf("UTC system should not warn; got %q", tz.TimeZoneWarning())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

package main

import (
	"testing"
	"time"
)

func TestDurationUntilHourUTC(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		hour int
		want time.Duration
	}{
		{
			name: "later today",
			now:  time.Date(2026, 5, 19, 1, 0, 0, 0, time.UTC),
			hour: 4,
			want: 3 * time.Hour,
		},
		{
			name: "already past, schedules tomorrow",
			now:  time.Date(2026, 5, 19, 6, 0, 0, 0, time.UTC),
			hour: 4,
			want: 22 * time.Hour,
		},
		{
			name: "exactly at the hour schedules next day",
			now:  time.Date(2026, 5, 19, 4, 0, 0, 0, time.UTC),
			hour: 4,
			want: 24 * time.Hour,
		},
		{
			name: "midnight target",
			now:  time.Date(2026, 5, 19, 23, 0, 0, 0, time.UTC),
			hour: 0,
			want: time.Hour,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := durationUntilHourUTC(tc.hour, tc.now); got != tc.want {
				t.Errorf("durationUntilHourUTC(%d, %s) = %s, want %s", tc.hour, tc.now, got, tc.want)
			}
		})
	}
}

package db

import (
	"context"
	"testing"
	"time"
)

func TestSiteEventLabels(t *testing.T) {
	if got := EventTypeLabel(EventTypeSeemsDown); got != "seems_down" {
		t.Fatalf("EventTypeLabel(seems_down) = %q, want seems_down", got)
	}
	if got := EventSeverityLabel(EventSeverityHigh); got != "high" {
		t.Fatalf("EventSeverityLabel(high) = %q, want high", got)
	}
	if got := ResolutionReasonLabel(ResolutionReasonFalseAlarm); got != "false_alarm" {
		t.Fatalf("ResolutionReasonLabel(false_alarm) = %q, want false_alarm", got)
	}
	if got := CheckTypeLabel(CheckTypeHTTP); got != "http" {
		t.Fatalf("CheckTypeLabel(http) = %q, want http", got)
	}
}

func TestSiteEventFunctionSignaturesCompile(t *testing.T) {
	var _ func(context.Context, int64, *int64, CheckType, EventType, EventSeverity, time.Time) (bool, error) = OpenSiteEvent
	var _ func(context.Context, int64, *int64, CheckType, EventType, EventSeverity) (bool, error) = UpgradeOpenSiteEvent
	var _ func(context.Context, int64, *int64, CheckType, time.Time, ResolutionReason) (bool, error) = CloseOpenSiteEvent
	var _ func(context.Context, int64, int64, *int64, CheckType, EventType, EventSeverity, time.Time, bool) error = ConfirmDownTx
}

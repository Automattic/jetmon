package db

type EventType uint8
type EventSeverity uint8
type ResolutionReason uint8
type CheckType uint8

const (
	EventTypeSeemsDown     EventType = 1
	EventTypeConfirmedDown EventType = 2

	EventSeverityLow  EventSeverity = 1
	EventSeverityHigh EventSeverity = 2

	ResolutionReasonVerifierCleared ResolutionReason = 1
	ResolutionReasonFalseAlarm      ResolutionReason = 2

	CheckTypeHTTP CheckType = 1
)

func EventTypeLabel(t EventType) string {
	switch t {
	case EventTypeSeemsDown:
		return "seems_down"
	case EventTypeConfirmedDown:
		return "confirmed_down"
	default:
		return "unknown"
	}
}

func EventSeverityLabel(s EventSeverity) string {
	switch s {
	case EventSeverityLow:
		return "low"
	case EventSeverityHigh:
		return "high"
	default:
		return "unknown"
	}
}

func ResolutionReasonLabel(r ResolutionReason) string {
	switch r {
	case ResolutionReasonVerifierCleared:
		return "verifier_cleared"
	case ResolutionReasonFalseAlarm:
		return "false_alarm"
	default:
		return "unknown"
	}
}

func CheckTypeLabel(c CheckType) string {
	switch c {
	case CheckTypeHTTP:
		return "http"
	default:
		return "unknown"
	}
}

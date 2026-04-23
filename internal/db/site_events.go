package db

const (
	EventTypeSeemsDown     = 1
	EventTypeConfirmedDown = 2

	EventSeverityLow  = 1
	EventSeverityHigh = 2
)

func EventTypeLabel(eventType uint8) string {
	switch eventType {
	case EventTypeSeemsDown:
		return "seems_down"
	case EventTypeConfirmedDown:
		return "confirmed_down"
	default:
		return "unknown"
	}
}

func EventSeverityLabel(severity uint8) string {
	switch severity {
	case EventSeverityLow:
		return "low"
	case EventSeverityHigh:
		return "high"
	default:
		return "unknown"
	}
}

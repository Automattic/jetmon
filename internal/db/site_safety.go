package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
	"unicode/utf8"
)

const (
	SiteSafetyFlagUnsafeMonitorURL = "unsafe_monitor_url"
	SiteSafetyFlagProbeSafetyBlock = "probe_safety_block"

	SiteSafetyStatusOpen        = "open"
	SiteSafetyStatusDeactivated = "deactivated"
	SiteSafetyStatusIgnored     = "ignored"
	SiteSafetyStatusResolved    = "resolved"

	siteSafetyReasonMaxRunes = 1024
	siteSafetyURLMaxRunes    = 2083
)

type SiteSafetyFlagExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// SiteSafetyFlag is a durable non-downtime remediation marker for unsafe
// monitor targets or runtime probe-safety blocks.
type SiteSafetyFlag struct {
	BlogID        int64
	MonitorSiteID int64
	FlagType      string
	Reason        string
	MonitorURL    string
	Status        string
	DeactivatedAt *time.Time
}

// UpsertSiteSafetyFlag records the latest safety finding for a monitor row
// while preserving first_seen_at. It accepts *sql.DB and *sql.Tx so cleanup
// commands can group flag writes with deactivation in one transaction.
func UpsertSiteSafetyFlag(ctx context.Context, execer SiteSafetyFlagExecer, flag SiteSafetyFlag) error {
	if execer == nil {
		return fmt.Errorf("db execer is required")
	}
	if flag.BlogID <= 0 {
		return fmt.Errorf("blog_id is required")
	}
	if flag.MonitorSiteID <= 0 {
		return fmt.Errorf("monitor_site_id is required")
	}
	if flag.FlagType == "" {
		return fmt.Errorf("flag_type is required")
	}
	if flag.Status == "" {
		flag.Status = SiteSafetyStatusOpen
	}
	if flag.Reason == "" {
		flag.Reason = "probe safety block"
	}

	_, err := execer.ExecContext(ctx, `
		INSERT INTO jetpack_monitor_site_safety_flags
			(blog_id, monitor_site_id, flag_type, reason, monitor_url, status, first_seen_at, last_seen_at, deactivated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP(3), CURRENT_TIMESTAMP(3), ?)
		ON DUPLICATE KEY UPDATE
			blog_id = VALUES(blog_id),
			reason = VALUES(reason),
			monitor_url = VALUES(monitor_url),
			status = VALUES(status),
			last_seen_at = VALUES(last_seen_at),
			deactivated_at = CASE
				WHEN VALUES(deactivated_at) IS NOT NULL THEN VALUES(deactivated_at)
				ELSE deactivated_at
			END,
			updated_at = CURRENT_TIMESTAMP(3)`,
		flag.BlogID,
		flag.MonitorSiteID,
		flag.FlagType,
		truncateRunes(flag.Reason, siteSafetyReasonMaxRunes),
		truncateRunes(flag.MonitorURL, siteSafetyURLMaxRunes),
		flag.Status,
		flag.DeactivatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert site safety flag: %w", err)
	}
	return nil
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes])
}

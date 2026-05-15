package db

import (
	"context"
	"database/sql"
	"fmt"
)

// RolloutActiveRange is the currently activated API-controlled bucket range
// for one monitor host.
type RolloutActiveRange struct {
	BucketMin int
	BucketMax int
	Count     int
}

// GetActiveRolloutRange returns the active API-controlled rollout range owned
// by hostID. A false ok value means the host should remain in standby.
func GetActiveRolloutRange(ctx context.Context, hostID string) (RolloutActiveRange, bool, error) {
	var min, max sql.NullInt64
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT MIN(bucket_no), MAX(bucket_no), COUNT(*)
		  FROM jetmon_rollout_bucket_locks
		 WHERE owner_host = ?`,
		hostID,
	).Scan(&min, &max, &count)
	if err != nil {
		return RolloutActiveRange{}, false, fmt.Errorf("query active rollout range: %w", err)
	}
	if count == 0 || !min.Valid || !max.Valid {
		return RolloutActiveRange{}, false, nil
	}
	expected := int(max.Int64-min.Int64) + 1
	if count != expected {
		return RolloutActiveRange{}, false, fmt.Errorf("active rollout buckets for host %q are not contiguous: count=%d expected=%d min=%d max=%d", hostID, count, expected, min.Int64, max.Int64)
	}
	return RolloutActiveRange{
		BucketMin: int(min.Int64),
		BucketMax: int(max.Int64),
		Count:     count,
	}, true, nil
}

package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ValueCount summarizes one integer value across the full legacy table range
// and the active subset of that range.
type ValueCount struct {
	Value  int
	Total  int64
	Active int64
}

// BucketLoadSummary summarizes row distribution across populated buckets.
type BucketLoadSummary struct {
	Distinct int
	MinRows  int64
	MaxRows  int64
	AvgRows  float64
}

// DuplicateBlogSummary describes monitor rows that share the same blog_id.
type DuplicateBlogSummary struct {
	Groups          int64
	Rows            int64
	MaxRowsPerBlog  int64
	StatusConflicts int64
}

// LegacySiteTableAudit captures production-data readiness signals from the
// v1-shaped jetpack_monitor_sites table without exposing individual site URLs.
type LegacySiteTableAudit struct {
	BucketMin int
	BucketMax int

	TotalRows  int64
	ActiveRows int64

	ObservedBucketMin      *int
	ObservedBucketMax      *int
	ObservedBucketDistinct int
	ActiveBucketDistinct   int
	ActiveBucketLoad       BucketLoadSummary

	MonitorActiveValues []ValueCount
	SiteStatusValues    []ValueCount
	CheckIntervalValues []ValueCount

	ActiveNonRunningRows     int64
	ActiveNullStatusChange   int64
	ActiveMalformedURLRows   int64
	ActiveURLNearColumnLimit int64
	MaxURLLength             int64

	DuplicateBlogs       DuplicateBlogSummary
	ActiveDuplicateBlogs DuplicateBlogSummary

	SitemetaTablePresent bool
}

// LegacyNonRunningSite is one active v1 row whose legacy projection already
// says the site is not running before v2 owns the range.
type LegacyNonRunningSite struct {
	MonitorSiteID    int64
	BlogID           int64
	BucketNo         int
	SiteStatus       int
	LastStatusChange *time.Time
}

// BuildLegacySiteTableAudit reads aggregate legacy-table signals for a bucket
// range. It is intentionally count-only so operators can safely share output.
func BuildLegacySiteTableAudit(ctx context.Context, bucketMin, bucketMax int) (LegacySiteTableAudit, error) {
	audit := LegacySiteTableAudit{
		BucketMin: bucketMin,
		BucketMax: bucketMax,
	}
	err := ReadDB().QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(monitor_active = 1), 0),
			COALESCE(SUM(monitor_active = 1 AND site_status <> 1), 0),
			COALESCE(SUM(monitor_active = 1 AND last_status_change IS NULL), 0),
			COALESCE(SUM(monitor_active = 1 AND LOWER(monitor_url) NOT REGEXP '^https?://[^/?#[:space:]]+'), 0),
			COALESCE(SUM(monitor_active = 1 AND CHAR_LENGTH(monitor_url) >= 250), 0),
			COALESCE(MAX(CHAR_LENGTH(monitor_url)), 0)
		  FROM jetpack_monitor_sites
		 WHERE bucket_no BETWEEN ? AND ?`,
		bucketMin, bucketMax,
	).Scan(
		&audit.TotalRows,
		&audit.ActiveRows,
		&audit.ActiveNonRunningRows,
		&audit.ActiveNullStatusChange,
		&audit.ActiveMalformedURLRows,
		&audit.ActiveURLNearColumnLimit,
		&audit.MaxURLLength,
	)
	if err != nil {
		return audit, fmt.Errorf("query legacy site table totals: %w", err)
	}

	var minBucket, maxBucket sql.NullInt64
	err = ReadDB().QueryRowContext(ctx, `
		SELECT MIN(bucket_no), MAX(bucket_no), COUNT(DISTINCT bucket_no),
		       COALESCE(COUNT(DISTINCT CASE WHEN monitor_active = 1 THEN bucket_no END), 0)
		  FROM jetpack_monitor_sites
		 WHERE bucket_no BETWEEN ? AND ?`,
		bucketMin, bucketMax,
	).Scan(&minBucket, &maxBucket, &audit.ObservedBucketDistinct, &audit.ActiveBucketDistinct)
	if err != nil {
		return audit, fmt.Errorf("query legacy bucket coverage: %w", err)
	}
	if minBucket.Valid {
		v := int(minBucket.Int64)
		audit.ObservedBucketMin = &v
	}
	if maxBucket.Valid {
		v := int(maxBucket.Int64)
		audit.ObservedBucketMax = &v
	}

	load, err := queryActiveBucketLoad(ctx, bucketMin, bucketMax)
	if err != nil {
		return audit, err
	}
	audit.ActiveBucketLoad = load

	audit.MonitorActiveValues, err = queryLegacyValueCounts(ctx, bucketMin, bucketMax, "monitor_active")
	if err != nil {
		return audit, err
	}
	audit.SiteStatusValues, err = queryLegacyValueCounts(ctx, bucketMin, bucketMax, "site_status")
	if err != nil {
		return audit, err
	}
	audit.CheckIntervalValues, err = queryLegacyValueCounts(ctx, bucketMin, bucketMax, "check_interval")
	if err != nil {
		return audit, err
	}

	audit.DuplicateBlogs, err = queryDuplicateBlogSummary(ctx, bucketMin, bucketMax, false)
	if err != nil {
		return audit, err
	}
	audit.ActiveDuplicateBlogs, err = queryDuplicateBlogSummary(ctx, bucketMin, bucketMax, true)
	if err != nil {
		return audit, err
	}

	audit.SitemetaTablePresent, err = queryTableExists(ctx, "jetpack_monitor_sitemeta")
	if err != nil {
		return audit, err
	}

	return audit, nil
}

func queryTableExists(ctx context.Context, tableName string) (bool, error) {
	var count int
	err := ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM information_schema.TABLES
		 WHERE TABLE_SCHEMA = DATABASE()
		   AND TABLE_NAME = ?`,
		tableName,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("query table %s existence: %w", tableName, err)
	}
	return count > 0, nil
}

func queryLegacyValueCounts(ctx context.Context, bucketMin, bucketMax int, column string) ([]ValueCount, error) {
	switch column {
	case "monitor_active", "site_status", "check_interval":
	default:
		return nil, fmt.Errorf("unsupported legacy value-count column %q", column)
	}
	rows, err := ReadDB().QueryContext(ctx, fmt.Sprintf(`
		SELECT %s AS value,
		       COUNT(*) AS total_rows,
		       COALESCE(SUM(monitor_active = 1), 0) AS active_rows
		  FROM jetpack_monitor_sites
		 WHERE bucket_no BETWEEN ? AND ?
		 GROUP BY %s
		 ORDER BY %s ASC`, column, column, column),
		bucketMin, bucketMax,
	)
	if err != nil {
		return nil, fmt.Errorf("query %s value counts: %w", column, err)
	}
	defer rows.Close()

	var out []ValueCount
	for rows.Next() {
		var row ValueCount
		if err := rows.Scan(&row.Value, &row.Total, &row.Active); err != nil {
			return nil, fmt.Errorf("scan %s value counts: %w", column, err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func queryActiveBucketLoad(ctx context.Context, bucketMin, bucketMax int) (BucketLoadSummary, error) {
	var out BucketLoadSummary
	err := ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(MIN(rows_per_bucket), 0), COALESCE(MAX(rows_per_bucket), 0), COALESCE(AVG(rows_per_bucket), 0)
		  FROM (
			SELECT bucket_no, COUNT(*) AS rows_per_bucket
			  FROM jetpack_monitor_sites
			 WHERE monitor_active = 1
			   AND bucket_no BETWEEN ? AND ?
			 GROUP BY bucket_no
		  ) active_buckets`,
		bucketMin, bucketMax,
	).Scan(&out.Distinct, &out.MinRows, &out.MaxRows, &out.AvgRows)
	if err != nil {
		return out, fmt.Errorf("query active bucket load: %w", err)
	}
	return out, nil
}

func queryDuplicateBlogSummary(ctx context.Context, bucketMin, bucketMax int, activeOnly bool) (DuplicateBlogSummary, error) {
	filter := ""
	if activeOnly {
		filter = "AND monitor_active = 1"
	}
	query := fmt.Sprintf(`
		SELECT COUNT(*), COALESCE(SUM(row_count), 0), COALESCE(MAX(row_count), 0), COALESCE(SUM(status_conflict), 0)
		  FROM (
			SELECT blog_id,
			       COUNT(*) AS row_count,
			       CASE WHEN COUNT(DISTINCT site_status) > 1 THEN 1 ELSE 0 END AS status_conflict
			  FROM jetpack_monitor_sites
			 WHERE bucket_no BETWEEN ? AND ?
			   %s
			 GROUP BY blog_id
			HAVING COUNT(*) > 1
		  ) duplicate_blogs`, filter)
	var out DuplicateBlogSummary
	err := ReadDB().QueryRowContext(ctx, query, bucketMin, bucketMax).
		Scan(&out.Groups, &out.Rows, &out.MaxRowsPerBlog, &out.StatusConflicts)
	if err != nil {
		return out, fmt.Errorf("query duplicate blog summary: %w", err)
	}
	return out, nil
}

// ListLegacyNonRunningSites pages active legacy rows whose v1 projection is not
// SITE_RUNNING. The page cursor is jetpack_monitor_site_id so duplicate blog_id
// rows are not skipped.
func ListLegacyNonRunningSites(ctx context.Context, bucketMin, bucketMax int, afterMonitorSiteID int64, limit int) ([]LegacyNonRunningSite, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := ReadDB().QueryContext(ctx, `
		SELECT jetpack_monitor_site_id, blog_id, bucket_no, site_status, last_status_change
		  FROM jetpack_monitor_sites
		 WHERE monitor_active = 1
		   AND site_status IN (0, 2)
		   AND bucket_no BETWEEN ? AND ?
		   AND jetpack_monitor_site_id > ?
		 ORDER BY jetpack_monitor_site_id ASC
		 LIMIT ?`,
		bucketMin, bucketMax, afterMonitorSiteID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query legacy non-running sites: %w", err)
	}
	defer rows.Close()

	var out []LegacyNonRunningSite
	for rows.Next() {
		var row LegacyNonRunningSite
		var lastStatusChange sql.NullTime
		if err := rows.Scan(&row.MonitorSiteID, &row.BlogID, &row.BucketNo, &row.SiteStatus, &lastStatusChange); err != nil {
			return nil, fmt.Errorf("scan legacy non-running site: %w", err)
		}
		if lastStatusChange.Valid {
			t := lastStatusChange.Time.UTC()
			row.LastStatusChange = &t
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

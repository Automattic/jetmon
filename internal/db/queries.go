package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// GetSitesForBucket fetches active sites within the given bucket range.
func GetSitesForBucket(ctx context.Context, bucketMin, bucketMax, batchSize int, useVariableIntervals bool) ([]Site, error) {
	query := `
		SELECT
			jetpack_monitor_site_id, blog_id, bucket_no, monitor_url,
			monitor_active, site_status, last_status_change, check_interval, last_checked_at,
			ssl_expiry_date, check_keyword, maintenance_start, maintenance_end,
			custom_headers, timeout_seconds, redirect_policy, alert_cooldown_minutes, last_alert_sent_at
		FROM jetpack_monitor_sites
		WHERE monitor_active = 1
		  AND bucket_no BETWEEN ? AND ?`
	if useVariableIntervals {
		query += `
		  AND (
			last_checked_at IS NULL
			OR DATE_ADD(last_checked_at, INTERVAL GREATEST(check_interval, 1) MINUTE) <= NOW()
		  )`
	}
	query += `
		ORDER BY
			COALESCE(last_checked_at, TIMESTAMP('1970-01-01 00:00:00')) ASC,
			blog_id ASC
		LIMIT ?`

	rows, err := db.QueryContext(ctx, query, bucketMin, bucketMax, batchSize)
	if err != nil {
		return nil, fmt.Errorf("query sites: %w", err)
	}
	defer rows.Close()

	var sites []Site
	for rows.Next() {
		var s Site
		var redirectPolicy sql.NullString
		err := rows.Scan(
			&s.ID, &s.BlogID, &s.BucketNo, &s.MonitorURL,
			&s.MonitorActive, &s.SiteStatus, &s.LastStatusChange, &s.CheckInterval, &s.LastCheckedAt,
			&s.SSLExpiryDate, &s.CheckKeyword, &s.MaintenanceStart, &s.MaintenanceEnd,
			&s.CustomHeaders, &s.TimeoutSeconds, &redirectPolicy, &s.AlertCooldownMinutes, &s.LastAlertSentAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan site: %w", err)
		}
		if redirectPolicy.Valid {
			s.RedirectPolicy = redirectPolicy.String
		} else {
			s.RedirectPolicy = "follow"
		}
		sites = append(sites, s)
	}
	return sites, rows.Err()
}

// CountActiveSitesForBucketRange returns the number of active monitor rows in
// the inclusive bucket range.
func CountActiveSitesForBucketRange(ctx context.Context, bucketMin, bucketMax int) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites
		 WHERE monitor_active = 1
		   AND bucket_no BETWEEN ? AND ?`,
		bucketMin, bucketMax,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active sites: %w", err)
	}
	return count, nil
}

// UpdateSiteStatus updates site_status and last_status_change for a site.
func UpdateSiteStatus(ctx context.Context, blogID int64, status int, changedAt time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetpack_monitor_sites SET site_status = ?, last_status_change = ? WHERE blog_id = ?`,
		status, changedAt.UTC(), blogID,
	)
	return err
}

// UpdateSiteStatusTx is the transaction-aware variant of UpdateSiteStatus, used
// when the projection write must commit atomically with an event mutation.
func UpdateSiteStatusTx(ctx context.Context, tx *sql.Tx, blogID int64, status int, changedAt time.Time) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE jetpack_monitor_sites SET site_status = ?, last_status_change = ? WHERE blog_id = ?`,
		status, changedAt.UTC(), blogID,
	)
	return err
}

// CountLegacyProjectionDrift returns the number of active sites in the bucket
// range whose v1 site_status projection disagrees with the authoritative open
// HTTP event, if any.
func CountLegacyProjectionDrift(ctx context.Context, bucketMin, bucketMax int) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites s
		  LEFT JOIN jetmon_events e
		    ON e.blog_id = s.blog_id
		   AND e.check_type = 'http'
		   AND e.ended_at IS NULL
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN ? AND ?
		   AND s.site_status <> CASE
		     WHEN e.state = 'Down' THEN 2
		     WHEN e.state = 'Seems Down' THEN 0
		     ELSE 1
		   END`,
		bucketMin, bucketMax,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count projection drift: %w", err)
	}
	return count, nil
}

// ProjectionDriftRow identifies one active site whose legacy site_status
// projection disagrees with the authoritative open HTTP event, if any.
type ProjectionDriftRow struct {
	BlogID         int64
	BucketNo       int
	SiteStatus     int
	ExpectedStatus int
	EventID        *int64
	EventState     *string
}

// ListLegacyProjectionDrift returns active sites in the bucket range whose v1
// site_status projection disagrees with the authoritative open HTTP event.
func ListLegacyProjectionDrift(ctx context.Context, bucketMin, bucketMax, limit int) ([]ProjectionDriftRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.QueryContext(ctx, `
		SELECT s.blog_id,
		       s.bucket_no,
		       s.site_status,
		       CASE
		         WHEN e.state = 'Down' THEN 2
		         WHEN e.state = 'Seems Down' THEN 0
		         ELSE 1
		       END AS expected_status,
		       e.id,
		       e.state
		  FROM jetpack_monitor_sites s
		  LEFT JOIN jetmon_events e
		    ON e.blog_id = s.blog_id
		   AND e.check_type = 'http'
		   AND e.ended_at IS NULL
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN ? AND ?
		   AND s.site_status <> CASE
		     WHEN e.state = 'Down' THEN 2
		     WHEN e.state = 'Seems Down' THEN 0
		     ELSE 1
		   END
		 ORDER BY s.bucket_no ASC, s.blog_id ASC
		 LIMIT ?`,
		bucketMin, bucketMax, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list projection drift: %w", err)
	}
	defer rows.Close()

	var out []ProjectionDriftRow
	for rows.Next() {
		var row ProjectionDriftRow
		var eventID sql.NullInt64
		var eventState sql.NullString
		if err := rows.Scan(
			&row.BlogID,
			&row.BucketNo,
			&row.SiteStatus,
			&row.ExpectedStatus,
			&eventID,
			&eventState,
		); err != nil {
			return nil, fmt.Errorf("scan projection drift: %w", err)
		}
		if eventID.Valid {
			v := eventID.Int64
			row.EventID = &v
		}
		if eventState.Valid {
			v := eventState.String
			row.EventState = &v
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// MarkSiteChecked records when a site was last checked.
func MarkSiteChecked(ctx context.Context, blogID int64, checkedAt time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetpack_monitor_sites SET last_checked_at = ? WHERE blog_id = ?`,
		checkedAt.UTC(), blogID,
	)
	return err
}

// UpdateLastAlertSent records when an alert was last sent for a site.
func UpdateLastAlertSent(ctx context.Context, blogID int64, sentAt time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetpack_monitor_sites SET last_alert_sent_at = ? WHERE blog_id = ?`,
		sentAt.UTC(), blogID,
	)
	return err
}

// UpdateSSLExpiry records the SSL certificate expiry date for a site.
func UpdateSSLExpiry(ctx context.Context, blogID int64, expiry time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetpack_monitor_sites SET ssl_expiry_date = ? WHERE blog_id = ?`,
		expiry, blogID,
	)
	return err
}

// ClaimBuckets registers this host in jetmon_hosts, claiming uncovered bucket
// ranges from expired peers. Returns the claimed min/max bucket numbers.
func ClaimBuckets(hostID string, bucketTotal, bucketTarget int, graceSec int) (int, int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Remove expired hosts.
	_, err = tx.Exec(
		`DELETE FROM jetmon_hosts WHERE last_heartbeat < DATE_SUB(NOW(), INTERVAL ? SECOND) AND host_id != ?`,
		graceSec, hostID,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("delete expired hosts: %w", err)
	}

	rows, err := tx.Query(`SELECT host_id FROM jetmon_hosts WHERE host_id != ? AND status = 'active' FOR UPDATE`, hostID)
	if err != nil {
		return 0, 0, fmt.Errorf("query hosts: %w", err)
	}
	hostIDs := []string{hostID}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		hostIDs = append(hostIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	sort.Strings(hostIDs)

	assignments := assignBucketRanges(hostIDs, bucketTotal, bucketTarget)

	for _, id := range hostIDs {
		rng := assignments[id]
		_, err = tx.Exec(
			`INSERT INTO jetmon_hosts (host_id, bucket_min, bucket_max, last_heartbeat, status)
			 VALUES (?, ?, ?, NOW(), 'active')
			 ON DUPLICATE KEY UPDATE bucket_min = VALUES(bucket_min), bucket_max = VALUES(bucket_max),
			 last_heartbeat = NOW(), status = 'active'`,
			id, rng[0], rng[1],
		)
		if err != nil {
			return 0, 0, fmt.Errorf("upsert host %s: %w", id, err)
		}
	}

	rng := assignments[hostID]
	return rng[0], rng[1], tx.Commit()
}

func assignBucketRanges(hostIDs []string, bucketTotal, bucketTarget int) map[string][2]int {
	assignments := make(map[string][2]int, len(hostIDs))
	nextBucket := 0
	for i, id := range hostIDs {
		if nextBucket >= bucketTotal {
			assignments[id] = [2]int{0, -1}
			continue
		}

		remainingBuckets := bucketTotal - nextBucket
		remainingHosts := len(hostIDs) - i
		size := (remainingBuckets + remainingHosts - 1) / remainingHosts
		if size > bucketTarget {
			size = bucketTarget
		}
		if size < 1 {
			assignments[id] = [2]int{0, -1}
			continue
		}

		assignments[id] = [2]int{nextBucket, nextBucket + size - 1}
		nextBucket += size
	}
	return assignments
}

// Heartbeat updates last_heartbeat for this host.
func Heartbeat(ctx context.Context, hostID string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetmon_hosts SET last_heartbeat = NOW(), status = 'active' WHERE host_id = ?`,
		hostID,
	)
	return err
}

// MarkHostDraining marks a host as draining before it releases its buckets.
func MarkHostDraining(ctx context.Context, hostID string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetmon_hosts SET status = 'draining', last_heartbeat = NOW() WHERE host_id = ?`,
		hostID,
	)
	return err
}

// ReleaseHost removes this host's row from jetmon_hosts on graceful shutdown.
func ReleaseHost(ctx context.Context, hostID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM jetmon_hosts WHERE host_id = ?`, hostID)
	return err
}

// HostRowExists reports whether a host currently has a jetmon_hosts ownership
// row.
func HostRowExists(ctx context.Context, hostID string) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx,
		`SELECT 1 FROM jetmon_hosts WHERE host_id = ? LIMIT 1`,
		hostID,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check host row: %w", err)
	}
	return true, nil
}

// ListHostRowsOverlappingBucketRange returns jetmon_hosts ownership rows whose
// bucket ranges overlap the inclusive requested range.
func ListHostRowsOverlappingBucketRange(ctx context.Context, bucketMin, bucketMax int) ([]HostRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT host_id, bucket_min, bucket_max, last_heartbeat, status
		   FROM jetmon_hosts
		  WHERE bucket_min <= ?
		    AND bucket_max >= ?
		  ORDER BY bucket_min, host_id`,
		bucketMax, bucketMin,
	)
	if err != nil {
		return nil, fmt.Errorf("query overlapping host rows: %w", err)
	}
	defer rows.Close()

	var hosts []HostRow
	for rows.Next() {
		var h HostRow
		if err := rows.Scan(&h.HostID, &h.BucketMin, &h.BucketMax, &h.LastHeartbeat, &h.Status); err != nil {
			return nil, fmt.Errorf("scan overlapping host row: %w", err)
		}
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}

// GetAllHosts returns all rows from jetmon_hosts for operator visibility.
func GetAllHosts() ([]HostRow, error) {
	rows, err := db.Query(
		`SELECT host_id, bucket_min, bucket_max, last_heartbeat, status FROM jetmon_hosts ORDER BY bucket_min`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hosts []HostRow
	for rows.Next() {
		var h HostRow
		if err := rows.Scan(&h.HostID, &h.BucketMin, &h.BucketMax, &h.LastHeartbeat, &h.Status); err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	return hosts, rows.Err()
}

// HostRow represents a row in jetmon_hosts.
type HostRow struct {
	HostID        string
	BucketMin     int
	BucketMax     int
	LastHeartbeat time.Time
	Status        string
}

// RecordFalsePositive inserts a false positive event.
func RecordFalsePositive(blogID int64, httpCode, errorCode int, rttMs int64) error {
	_, err := db.Exec(
		`INSERT INTO jetmon_false_positives (blog_id, http_code, error_code, rtt_ms, created_at)
		 VALUES (?, ?, ?, ?, NOW())`,
		blogID, httpCode, errorCode, rttMs,
	)
	return err
}

// RecordCheckHistory inserts a check timing sample.
func RecordCheckHistory(blogID int64, httpCode, errorCode int, rttMs, dnsMs, tcpMs, tlsMs, ttfbMs int64) error {
	_, err := db.Exec(
		`INSERT INTO jetmon_check_history
		    (blog_id, http_code, error_code, rtt_ms, dns_ms, tcp_ms, tls_ms, ttfb_ms, checked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NOW())`,
		blogID, httpCode, errorCode, rttMs, dnsMs, tcpMs, tlsMs, ttfbMs,
	)
	return err
}

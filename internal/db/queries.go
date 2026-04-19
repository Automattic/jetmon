package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// GetSitesForBucket fetches active sites within the given bucket range.
func GetSitesForBucket(ctx context.Context, bucketMin, bucketMax, batchSize int) ([]Site, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT
			jetpack_monitor_site_id, blog_id, bucket_no, monitor_url,
			monitor_active, site_status, last_status_change, check_interval,
			ssl_expiry_date, check_keyword, maintenance_start, maintenance_end,
			custom_headers, timeout_seconds, redirect_policy, alert_cooldown_minutes
		FROM jetpack_monitor_sites
		WHERE monitor_active = 1
		  AND bucket_no BETWEEN ? AND ?
		LIMIT ?`,
		bucketMin, bucketMax, batchSize,
	)
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
			&s.MonitorActive, &s.SiteStatus, &s.LastStatusChange, &s.CheckInterval,
			&s.SSLExpiryDate, &s.CheckKeyword, &s.MaintenanceStart, &s.MaintenanceEnd,
			&s.CustomHeaders, &s.TimeoutSeconds, &redirectPolicy, &s.AlertCooldownMinutes,
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

// UpdateSiteStatus updates site_status and last_status_change for a site.
func UpdateSiteStatus(ctx context.Context, blogID int64, status int) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetpack_monitor_sites SET site_status = ?, last_status_change = NOW() WHERE blog_id = ?`,
		status, blogID,
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

	// Find covered ranges.
	rows, err := tx.Query(`SELECT bucket_min, bucket_max FROM jetmon_hosts WHERE host_id != ? FOR UPDATE`, hostID)
	if err != nil {
		return 0, 0, fmt.Errorf("query hosts: %w", err)
	}
	covered := make([]bool, bucketTotal)
	for rows.Next() {
		var bMin, bMax int
		if err := rows.Scan(&bMin, &bMax); err != nil {
			rows.Close()
			return 0, 0, err
		}
		for i := bMin; i <= bMax && i < bucketTotal; i++ {
			covered[i] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	// Find first unclaimed range up to bucketTarget in size.
	claimMin := -1
	claimMax := -1
	count := 0
	for i := 0; i < bucketTotal; i++ {
		if !covered[i] {
			if claimMin == -1 {
				claimMin = i
			}
			claimMax = i
			count++
			if count >= bucketTarget {
				break
			}
		}
	}

	if claimMin == -1 {
		// All buckets covered; upsert with zero range so heartbeat works.
		claimMin, claimMax = 0, -1
	}

	_, err = tx.Exec(
		`INSERT INTO jetmon_hosts (host_id, bucket_min, bucket_max, last_heartbeat, status)
		 VALUES (?, ?, ?, NOW(), 'active')
		 ON DUPLICATE KEY UPDATE bucket_min = VALUES(bucket_min), bucket_max = VALUES(bucket_max),
		 last_heartbeat = NOW(), status = 'active'`,
		hostID, claimMin, claimMax,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("upsert host: %w", err)
	}

	return claimMin, claimMax, tx.Commit()
}

// Heartbeat updates last_heartbeat for this host.
func Heartbeat(ctx context.Context, hostID string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetmon_hosts SET last_heartbeat = NOW() WHERE host_id = ?`,
		hostID,
	)
	return err
}

// ReleaseHost removes this host's row from jetmon_hosts on graceful shutdown.
func ReleaseHost(ctx context.Context, hostID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM jetmon_hosts WHERE host_id = ?`, hostID)
	return err
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

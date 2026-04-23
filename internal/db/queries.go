package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrDuplicateSite = errors.New("site already exists")
	ErrNoPatchFields = errors.New("no patch fields provided")
)

type ListSitesParams struct {
	Limit         int
	Offset        int
	BlogID        *int64
	SiteStatus    *int
	MonitorActive *bool
	BucketNo      *int
}

type CreateSiteInput struct {
	BlogID        int64
	BucketNo      int
	MonitorURL    string
	MonitorActive bool
	SiteStatus    int
	CheckInterval int
}

type PatchSiteInput struct {
	MonitorActive        *bool
	SiteStatus           *int
	CheckInterval        *int
	TimeoutSeconds       *int
	RedirectPolicy       *string
	CheckKeyword         *string
	AlertCooldownMinutes *int
	MaintenanceStart     *time.Time
	MaintenanceEnd       *time.Time
	CustomHeaders        *string
}

type ListSiteEventsParams struct {
	Limit  int
	Offset int
}

type SiteEvent struct {
	ID                   int64      `json:"id"`
	JetpackMonitorSiteID int64      `json:"jetpack_monitor_site_id"`
	EventType            uint8      `json:"event_type"`
	Severity             uint8      `json:"severity"`
	StartedAt            time.Time  `json:"started_at"`
	EndedAt              *time.Time `json:"ended_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

const createSiteSQL = `
	INSERT INTO jetpack_monitor_sites
		(blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval)
	SELECT ?, ?, ?, ?, ?, ?
	WHERE NOT EXISTS (
		SELECT 1
		FROM jetpack_monitor_sites
		WHERE blog_id = ? AND monitor_url = ?
	)`

// ListSites returns sites with optional filters and pagination.
func ListSites(ctx context.Context, params ListSitesParams) ([]Site, error) {
	if params.Limit <= 0 {
		params.Limit = 100
	}
	if params.Offset < 0 {
		params.Offset = 0
	}

	query := `
		SELECT
			jetpack_monitor_site_id, blog_id, bucket_no, monitor_url,
			monitor_active, site_status, last_status_change, check_interval, last_checked_at,
			ssl_expiry_date, check_keyword, maintenance_start, maintenance_end,
			custom_headers, timeout_seconds, redirect_policy, alert_cooldown_minutes, last_alert_sent_at
		FROM jetpack_monitor_sites`

	var where []string
	var args []any

	if params.BlogID != nil {
		where = append(where, "blog_id = ?")
		args = append(args, *params.BlogID)
	}
	if params.SiteStatus != nil {
		where = append(where, "site_status = ?")
		args = append(args, *params.SiteStatus)
	}
	if params.MonitorActive != nil {
		where = append(where, "monitor_active = ?")
		if *params.MonitorActive {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if params.BucketNo != nil {
		where = append(where, "bucket_no = ?")
		args = append(args, *params.BucketNo)
	}

	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}

	query += ` ORDER BY jetpack_monitor_site_id DESC LIMIT ? OFFSET ?`
	args = append(args, params.Limit, params.Offset)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query list sites: %w", err)
	}
	defer rows.Close()

	var sites []Site
	for rows.Next() {
		s, err := scanSiteRow(rows)
		if err != nil {
			return nil, err
		}
		sites = append(sites, s)
	}
	return sites, rows.Err()
}

// GetSiteByID fetches a single site by primary key.
func GetSiteByID(ctx context.Context, id int64) (Site, error) {
	row := db.QueryRowContext(ctx, `
		SELECT
			jetpack_monitor_site_id, blog_id, bucket_no, monitor_url,
			monitor_active, site_status, last_status_change, check_interval, last_checked_at,
			ssl_expiry_date, check_keyword, maintenance_start, maintenance_end,
			custom_headers, timeout_seconds, redirect_policy, alert_cooldown_minutes, last_alert_sent_at
		FROM jetpack_monitor_sites
		WHERE jetpack_monitor_site_id = ?`, id)

	s, err := scanSiteRow(row)
	if err != nil {
		return Site{}, err
	}
	return s, nil
}

// CreateSite inserts a new site, rejecting duplicates by (blog_id, monitor_url).
func CreateSite(ctx context.Context, input CreateSiteInput) (int64, error) {
	if input.CheckInterval <= 0 {
		input.CheckInterval = 5
	}
	res, err := db.ExecContext(ctx, createSiteSQL,
		input.BlogID, input.BucketNo, input.MonitorURL, boolToTinyInt(input.MonitorActive), input.SiteStatus, input.CheckInterval,
		input.BlogID, input.MonitorURL,
	)
	if err != nil {
		return 0, fmt.Errorf("create site: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("create site rows affected: %w", err)
	}
	if affected == 0 {
		return 0, ErrDuplicateSite
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create site last insert id: %w", err)
	}
	return id, nil
}

// PatchSite updates mutable site fields and returns whether the site exists.
func PatchSite(ctx context.Context, id int64, input PatchSiteInput) (bool, error) {
	setClauses, args, err := buildSitePatchUpdates(input)
	if err != nil {
		return false, err
	}
	query := "UPDATE jetpack_monitor_sites SET " + strings.Join(setClauses, ", ") + " WHERE jetpack_monitor_site_id = ?"
	args = append(args, id)

	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("patch site: %w", err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("patch site rows affected: %w", err)
	}
	if rowsAffected > 0 {
		return true, nil
	}
	return siteExistsByID(ctx, id)
}

// DeleteSite hard deletes a site row by ID and returns whether it existed.
func DeleteSite(ctx context.Context, id int64) (bool, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM jetpack_monitor_sites WHERE jetpack_monitor_site_id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete site: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete site rows affected: %w", err)
	}
	return affected > 0, nil
}

// ListSiteEvents returns site events for one site.
func ListSiteEvents(ctx context.Context, siteID int64, params ListSiteEventsParams) ([]SiteEvent, error) {
	if params.Limit <= 0 {
		params.Limit = 100
	}
	if params.Offset < 0 {
		params.Offset = 0
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, jetpack_monitor_site_id, event_type, severity, started_at, ended_at, created_at, updated_at
		FROM jetmon_site_events
		WHERE jetpack_monitor_site_id = ?
		ORDER BY started_at DESC, id DESC
		LIMIT ? OFFSET ?`, siteID, params.Limit, params.Offset)
	if err != nil {
		return nil, fmt.Errorf("list site events: %w", err)
	}
	defer rows.Close()

	var events []SiteEvent
	for rows.Next() {
		var ev SiteEvent
		if err := rows.Scan(&ev.ID, &ev.JetpackMonitorSiteID, &ev.EventType, &ev.Severity, &ev.StartedAt, &ev.EndedAt, &ev.CreatedAt, &ev.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan site event: %w", err)
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

func buildSitePatchUpdates(input PatchSiteInput) ([]string, []any, error) {
	setClauses := make([]string, 0, 10)
	args := make([]any, 0, 10)

	if input.MonitorActive != nil {
		setClauses = append(setClauses, "monitor_active = ?")
		args = append(args, boolToTinyInt(*input.MonitorActive))
	}
	if input.SiteStatus != nil {
		setClauses = append(setClauses, "site_status = ?")
		args = append(args, *input.SiteStatus)
	}
	if input.CheckInterval != nil {
		setClauses = append(setClauses, "check_interval = ?")
		args = append(args, *input.CheckInterval)
	}
	if input.TimeoutSeconds != nil {
		setClauses = append(setClauses, "timeout_seconds = ?")
		args = append(args, *input.TimeoutSeconds)
	}
	if input.RedirectPolicy != nil {
		setClauses = append(setClauses, "redirect_policy = ?")
		args = append(args, *input.RedirectPolicy)
	}
	if input.CheckKeyword != nil {
		setClauses = append(setClauses, "check_keyword = ?")
		args = append(args, *input.CheckKeyword)
	}
	if input.AlertCooldownMinutes != nil {
		setClauses = append(setClauses, "alert_cooldown_minutes = ?")
		args = append(args, *input.AlertCooldownMinutes)
	}
	if input.MaintenanceStart != nil {
		setClauses = append(setClauses, "maintenance_start = ?")
		args = append(args, input.MaintenanceStart.UTC())
	}
	if input.MaintenanceEnd != nil {
		setClauses = append(setClauses, "maintenance_end = ?")
		args = append(args, input.MaintenanceEnd.UTC())
	}
	if input.CustomHeaders != nil {
		setClauses = append(setClauses, "custom_headers = ?")
		args = append(args, *input.CustomHeaders)
	}

	if len(setClauses) == 0 {
		return nil, nil, ErrNoPatchFields
	}
	return setClauses, args, nil
}

func siteExistsByID(ctx context.Context, id int64) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM jetpack_monitor_sites WHERE jetpack_monitor_site_id = ?`, id).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check site existence: %w", err)
	}
	return true, nil
}

func boolToTinyInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func scanSiteRow(scanner interface{ Scan(dest ...any) error }) (Site, error) {
	var s Site
	var redirectPolicy sql.NullString
	err := scanner.Scan(
		&s.ID, &s.BlogID, &s.BucketNo, &s.MonitorURL,
		&s.MonitorActive, &s.SiteStatus, &s.LastStatusChange, &s.CheckInterval, &s.LastCheckedAt,
		&s.SSLExpiryDate, &s.CheckKeyword, &s.MaintenanceStart, &s.MaintenanceEnd,
		&s.CustomHeaders, &s.TimeoutSeconds, &redirectPolicy, &s.AlertCooldownMinutes, &s.LastAlertSentAt,
	)
	if err != nil {
		return Site{}, err
	}
	if redirectPolicy.Valid {
		s.RedirectPolicy = redirectPolicy.String
	} else {
		s.RedirectPolicy = "follow"
	}
	return s, nil
}

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
		s, err := scanSiteRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan site: %w", err)
		}
		sites = append(sites, s)
	}
	return sites, rows.Err()
}

// UpdateSiteStatus updates site_status and last_status_change for a site.
func UpdateSiteStatus(ctx context.Context, blogID int64, status int, changedAt time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetpack_monitor_sites SET site_status = ?, last_status_change = ? WHERE blog_id = ?`,
		status, changedAt.UTC(), blogID,
	)
	return err
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

// OpenSiteEvent inserts a new open event for the site if none is currently open.
func OpenSiteEvent(ctx context.Context, siteID int64, eventType, severity uint8, startedAt time.Time) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO jetmon_site_events
			(jetpack_monitor_site_id, event_type, severity, started_at)
		 SELECT ?, ?, ?, ?
		 WHERE NOT EXISTS (
			SELECT 1
			FROM jetmon_site_events
			WHERE jetpack_monitor_site_id = ?
			  AND ended_at IS NULL
		 )`,
		siteID, eventType, severity, startedAt.UTC(), siteID,
	)
	return err
}

// UpgradeOpenSiteEvent updates type/severity on the currently open event for a site.
func UpgradeOpenSiteEvent(ctx context.Context, siteID int64, eventType, severity uint8) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetmon_site_events
		 SET event_type = ?, severity = ?
		 WHERE jetpack_monitor_site_id = ? AND ended_at IS NULL`,
		eventType, severity, siteID,
	)
	return err
}

// CloseOpenSiteEvent sets ended_at on the currently open event for a site.
func CloseOpenSiteEvent(ctx context.Context, siteID int64, endedAt time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE jetmon_site_events
		 SET ended_at = ?
		 WHERE jetpack_monitor_site_id = ? AND ended_at IS NULL`,
		endedAt.UTC(), siteID,
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

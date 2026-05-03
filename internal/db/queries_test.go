package db

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestAssignBucketRanges(t *testing.T) {
	tests := []struct {
		name         string
		hostIDs      []string
		bucketTotal  int
		bucketTarget int
		want         map[string][2]int
	}{
		{
			name:         "single host claims all buckets up to target",
			hostIDs:      []string{"host-a"},
			bucketTotal:  10,
			bucketTarget: 10,
			want: map[string][2]int{
				"host-a": {0, 9},
			},
		},
		{
			name:         "multiple hosts split coverage evenly",
			hostIDs:      []string{"host-a", "host-b", "host-c"},
			bucketTotal:  10,
			bucketTarget: 10,
			want: map[string][2]int{
				"host-a": {0, 3},
				"host-b": {4, 6},
				"host-c": {7, 9},
			},
		},
		{
			name:         "bucket target caps allocation",
			hostIDs:      []string{"host-a", "host-b", "host-c"},
			bucketTotal:  12,
			bucketTarget: 4,
			want: map[string][2]int{
				"host-a": {0, 3},
				"host-b": {4, 7},
				"host-c": {8, 11},
			},
		},
		{
			name:         "extra hosts get empty ranges",
			hostIDs:      []string{"host-a", "host-b", "host-c"},
			bucketTotal:  2,
			bucketTarget: 2,
			want: map[string][2]int{
				"host-a": {0, 0},
				"host-b": {1, 1},
				"host-c": {0, -1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assignBucketRanges(tt.hostIDs, tt.bucketTotal, tt.bucketTarget)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("assignBucketRanges() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func withMockDB(t *testing.T) (sqlmock.Sqlmock, func()) {
	t.Helper()
	mockDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	orig := db
	db = mockDB
	cleanup := func() {
		db = orig
		_ = mockDB.Close()
	}
	return mock, cleanup
}

func TestGlobalDBAccessors(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectPing()
	if DB() == nil {
		t.Fatal("DB() = nil")
	}
	if err := Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if Hostname() == "" {
		t.Fatal("Hostname() returned empty string")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestGetSitesForBucketScansRowsAndDefaultRedirectPolicy(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	now := time.Now().UTC()
	rows := sqlmock.NewRows([]string{
		"jetpack_monitor_site_id", "blog_id", "bucket_no", "monitor_url",
		"monitor_active", "site_status", "last_status_change", "check_interval", "last_checked_at",
		"ssl_expiry_date", "check_keyword", "maintenance_start", "maintenance_end",
		"custom_headers", "timeout_seconds", "redirect_policy", "alert_cooldown_minutes", "last_alert_sent_at",
	}).AddRow(
		int64(1), int64(42), 7, "https://site.example",
		true, 1, now, 5, now,
		nil, nil, nil, nil,
		nil, nil, nil, nil, nil,
	)
	mock.ExpectQuery("SELECT").
		WithArgs(0, 99, 50).
		WillReturnRows(rows)

	sites, err := GetSitesForBucket(context.Background(), 0, 99, 50, false)
	if err != nil {
		t.Fatalf("GetSitesForBucket: %v", err)
	}
	if len(sites) != 1 {
		t.Fatalf("sites len = %d, want 1", len(sites))
	}
	if sites[0].BlogID != 42 || sites[0].RedirectPolicy != "follow" {
		t.Fatalf("site = %+v", sites[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCountActiveSitesForBucketRange(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(10, 19).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(42))

	count, err := CountActiveSitesForBucketRange(context.Background(), 10, 19)
	if err != nil {
		t.Fatalf("CountActiveSitesForBucketRange: %v", err)
	}
	if count != 42 {
		t.Fatalf("CountActiveSitesForBucketRange = %d, want 42", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCountRecentlyCheckedActiveSitesForBucketRange(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	cutoff := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(10, 19, cutoff).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(17))

	count, err := CountRecentlyCheckedActiveSitesForBucketRange(context.Background(), 10, 19, cutoff)
	if err != nil {
		t.Fatalf("CountRecentlyCheckedActiveSitesForBucketRange: %v", err)
	}
	if count != 17 {
		t.Fatalf("CountRecentlyCheckedActiveSitesForBucketRange = %d, want 17", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSimpleMutationQueries(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	now := time.Now().UTC()
	mock.ExpectExec("UPDATE jetpack_monitor_sites SET site_status").
		WithArgs(2, now, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetpack_monitor_sites SET last_checked_at").
		WithArgs(now, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetpack_monitor_sites SET last_alert_sent_at").
		WithArgs(now, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetpack_monitor_sites SET ssl_expiry_date").
		WithArgs(now, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetmon_hosts SET last_heartbeat").
		WithArgs("host-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE jetmon_hosts SET status = 'draining'").
		WithArgs("host-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM jetmon_hosts").
		WithArgs("host-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO jetmon_false_positives").
		WithArgs(int64(42), 500, 1, int64(123)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO jetmon_check_history").
		WithArgs(int64(42), 200, 0, int64(100), int64(1), int64(2), int64(3), int64(4)).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := UpdateSiteStatus(context.Background(), 42, 2, now); err != nil {
		t.Fatalf("UpdateSiteStatus: %v", err)
	}
	if err := MarkSiteChecked(context.Background(), 42, now); err != nil {
		t.Fatalf("MarkSiteChecked: %v", err)
	}
	if err := UpdateLastAlertSent(context.Background(), 42, now); err != nil {
		t.Fatalf("UpdateLastAlertSent: %v", err)
	}
	if err := UpdateSSLExpiry(context.Background(), 42, now); err != nil {
		t.Fatalf("UpdateSSLExpiry: %v", err)
	}
	if err := Heartbeat(context.Background(), "host-a"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if err := MarkHostDraining(context.Background(), "host-a"); err != nil {
		t.Fatalf("MarkHostDraining: %v", err)
	}
	if err := ReleaseHost(context.Background(), "host-a"); err != nil {
		t.Fatalf("ReleaseHost: %v", err)
	}
	if err := RecordFalsePositive(42, 500, 1, 123); err != nil {
		t.Fatalf("RecordFalsePositive: %v", err)
	}
	if err := RecordCheckHistory(42, 200, 0, 100, 1, 2, 3, 4); err != nil {
		t.Fatalf("RecordCheckHistory: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestMarkSitesCheckedBatchesUpdates(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	first := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	mock.ExpectExec("UPDATE jetpack_monitor_sites SET last_checked_at = CASE blog_id").
		WithArgs(int64(7), first, int64(42), second, int64(7), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 2))

	err := MarkSitesChecked(context.Background(), []SiteCheck{
		{BlogID: 42, CheckedAt: second},
		{BlogID: 7, CheckedAt: first},
	})
	if err != nil {
		t.Fatalf("MarkSitesChecked: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestRecordCheckHistoriesBatchesInserts(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	first := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	mock.ExpectExec("INSERT INTO jetmon_check_history").
		WithArgs(
			int64(7), 201, 1, int64(10), int64(1), int64(2), int64(3), int64(4), first,
			int64(42), 200, 0, int64(100), int64(5), int64(6), int64(7), int64(8), second,
		).
		WillReturnResult(sqlmock.NewResult(1, 2))

	err := RecordCheckHistories(context.Background(), []CheckHistoryRow{
		{BlogID: 42, HTTPCode: 200, ErrorCode: 0, RTTMs: 100, DNSMs: 5, TCPMs: 6, TLSMs: 7, TTFBMs: 8, CheckedAt: second},
		{BlogID: 7, HTTPCode: 201, ErrorCode: 1, RTTMs: 10, DNSMs: 1, TCPMs: 2, TLSMs: 3, TTFBMs: 4, CheckedAt: first},
	})
	if err != nil {
		t.Fatalf("RecordCheckHistories: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestUpdateSiteStatusTx(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	now := time.Now().UTC()
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE jetpack_monitor_sites SET site_status").
		WithArgs(2, now, int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := UpdateSiteStatusTx(context.Background(), tx, 42, 2, now); err != nil {
		t.Fatalf("UpdateSiteStatusTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestHostRowExists(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectQuery("SELECT 1 FROM jetmon_hosts").
		WithArgs("host-a").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery("SELECT 1 FROM jetmon_hosts").
		WithArgs("host-b").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}))

	exists, err := HostRowExists(context.Background(), "host-a")
	if err != nil {
		t.Fatalf("HostRowExists(host-a): %v", err)
	}
	if !exists {
		t.Fatal("HostRowExists(host-a) = false, want true")
	}

	exists, err = HostRowExists(context.Background(), "host-b")
	if err != nil {
		t.Fatalf("HostRowExists(host-b): %v", err)
	}
	if exists {
		t.Fatal("HostRowExists(host-b) = true, want false")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHostRowsOverlappingBucketRange(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT host_id, bucket_min, bucket_max").
		WithArgs(34, 12).
		WillReturnRows(sqlmock.NewRows([]string{"host_id", "bucket_min", "bucket_max", "last_heartbeat", "status"}).
			AddRow("host-a", 0, 19, now, "active").
			AddRow("host-b", 20, 49, now, "draining"))

	hosts, err := ListHostRowsOverlappingBucketRange(context.Background(), 12, 34)
	if err != nil {
		t.Fatalf("ListHostRowsOverlappingBucketRange: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("hosts len = %d, want 2", len(hosts))
	}
	if hosts[0].HostID != "host-a" || hosts[0].BucketMin != 0 || hosts[0].BucketMax != 19 {
		t.Fatalf("host 0 = %+v", hosts[0])
	}
	if hosts[1].HostID != "host-b" || hosts[1].Status != "draining" {
		t.Fatalf("host 1 = %+v", hosts[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCountLegacyProjectionDrift(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(0, 99).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	count, err := CountLegacyProjectionDrift(context.Background(), 0, 99)
	if err != nil {
		t.Fatalf("CountLegacyProjectionDrift: %v", err)
	}
	if count != 3 {
		t.Fatalf("CountLegacyProjectionDrift = %d, want 3", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListLegacyProjectionDrift(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectQuery("SELECT drift.blog_id").
		WithArgs(0, 99, 50).
		WillReturnRows(sqlmock.NewRows([]string{
			"blog_id", "bucket_no", "site_status", "expected_status", "id", "state", "open_event_count",
		}).
			AddRow(int64(42), 7, 1, 2, int64(123), "Down", 1).
			AddRow(int64(43), 8, 0, 1, nil, nil, 0))

	rows, err := ListLegacyProjectionDrift(context.Background(), 0, 99, 0)
	if err != nil {
		t.Fatalf("ListLegacyProjectionDrift: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2", len(rows))
	}
	if rows[0].BlogID != 42 || rows[0].BucketNo != 7 || rows[0].SiteStatus != 1 || rows[0].ExpectedStatus != 2 {
		t.Fatalf("row 0 = %+v", rows[0])
	}
	if rows[0].EventID == nil || *rows[0].EventID != 123 {
		t.Fatalf("row 0 EventID = %v, want 123", rows[0].EventID)
	}
	if rows[0].EventState == nil || *rows[0].EventState != "Down" {
		t.Fatalf("row 0 EventState = %v, want Down", rows[0].EventState)
	}
	if rows[0].OpenEventCount != 1 {
		t.Fatalf("row 0 OpenEventCount = %d, want 1", rows[0].OpenEventCount)
	}
	if rows[1].EventID != nil || rows[1].EventState != nil {
		t.Fatalf("row 1 event fields = %+v, want nil", rows[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSummarizeLegacyProjectionDrift(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectQuery("SELECT drift.bucket_no").
		WithArgs(0, 99, 20).
		WillReturnRows(sqlmock.NewRows([]string{
			"bucket_no", "site_status", "expected_status", "expected_state", "max_open_event_count", "drift_count", "sample_blog_id",
		}).
			AddRow(7, 1, 2, "Down", 1, 3, int64(42)).
			AddRow(8, 0, 1, nil, 0, 2, int64(43)))

	rows, err := SummarizeLegacyProjectionDrift(context.Background(), 0, 99, 0)
	if err != nil {
		t.Fatalf("SummarizeLegacyProjectionDrift: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2", len(rows))
	}
	if rows[0].BucketNo != 7 || rows[0].SiteStatus != 1 || rows[0].ExpectedStatus != 2 || rows[0].DriftCount != 3 || rows[0].SampleBlogID != 42 {
		t.Fatalf("row 0 = %+v", rows[0])
	}
	if rows[0].EventState == nil || *rows[0].EventState != "Down" {
		t.Fatalf("row 0 EventState = %v, want Down", rows[0].EventState)
	}
	if rows[1].EventState != nil {
		t.Fatalf("row 1 EventState = %v, want nil", rows[1].EventState)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestGetAllHostsScansRows(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	now := time.Now().UTC()
	mock.ExpectQuery("SELECT host_id, bucket_min, bucket_max").
		WillReturnRows(sqlmock.NewRows([]string{"host_id", "bucket_min", "bucket_max", "last_heartbeat", "status"}).
			AddRow("host-a", 0, 49, now, "active").
			AddRow("host-b", 50, 99, now, "draining"))

	hosts, err := GetAllHosts()
	if err != nil {
		t.Fatalf("GetAllHosts: %v", err)
	}
	if len(hosts) != 2 || hosts[1].Status != "draining" {
		t.Fatalf("hosts = %+v", hosts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestClaimBucketsRebalancesKnownHosts(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM jetmon_hosts").
		WithArgs(60, "host-b").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT host_id FROM jetmon_hosts").
		WithArgs("host-b").
		WillReturnRows(sqlmock.NewRows([]string{"host_id"}).AddRow("host-a"))
	mock.ExpectExec("INSERT INTO jetmon_hosts").
		WithArgs("host-a", 0, 4).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO jetmon_hosts").
		WithArgs("host-b", 5, 9).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	minBucket, maxBucket, err := ClaimBuckets("host-b", 10, 10, 60)
	if err != nil {
		t.Fatalf("ClaimBuckets: %v", err)
	}
	if minBucket != 5 || maxBucket != 9 {
		t.Fatalf("claimed range = %d..%d, want 5..9", minBucket, maxBucket)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestMigrateAppliesOnlyPendingMigrations(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	origMigrations := migrations
	migrations = []migration{
		{id: 1, sql: "CREATE TABLE jetmon_schema_migrations"},
		{id: 2, sql: "ALTER TABLE already_done"},
		{id: 3, sql: "ALTER TABLE pending_change"},
	}
	defer func() { migrations = origMigrations }()

	mock.ExpectExec("CREATE TABLE jetmon_schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT IGNORE INTO jetmon_schema_migrations").
		WithArgs(1).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(2).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(3).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("ALTER TABLE pending_change").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT IGNORE INTO jetmon_schema_migrations").
		WithArgs(3).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

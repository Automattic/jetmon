package db

import (
	"context"
	"reflect"
	"regexp"
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

func TestOpenSiteEventDedupesByEventType(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	oldDB := db
	db = mockDB
	t.Cleanup(func() {
		db = oldDB
		_ = mockDB.Close()
	})

	query := regexp.QuoteMeta(`INSERT INTO jetmon_site_events
			(jetpack_monitor_site_id, endpoint_id, check_type, event_type, severity, started_at)
		 SELECT ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (
			SELECT 1
			FROM jetmon_site_events
			WHERE jetpack_monitor_site_id = ?
			  AND endpoint_id <=> ?
			  AND check_type = ?
			  AND event_type = ?
			  AND ended_at IS NULL
		 )`)

	ctx := context.Background()
	startedAt := time.Date(2026, time.April, 24, 10, 0, 0, 0, time.UTC)
	siteID := int64(101)
	var endpointID *int64

	mock.ExpectExec(query).
		WithArgs(siteID, endpointID, CheckTypeHTTP, EventTypeSeemsDown, EventSeverityLow, startedAt.UTC(), siteID, endpointID, CheckTypeHTTP, EventTypeSeemsDown).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(query).
		WithArgs(siteID, endpointID, CheckTypeHTTP, EventTypeConfirmedDown, EventSeverityHigh, startedAt.UTC(), siteID, endpointID, CheckTypeHTTP, EventTypeConfirmedDown).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(query).
		WithArgs(siteID, endpointID, CheckTypeHTTP, EventTypeSeemsDown, EventSeverityLow, startedAt.UTC(), siteID, endpointID, CheckTypeHTTP, EventTypeSeemsDown).
		WillReturnResult(sqlmock.NewResult(0, 0))

	insertedSeemsDown, err := OpenSiteEvent(ctx, siteID, endpointID, CheckTypeHTTP, EventTypeSeemsDown, EventSeverityLow, startedAt)
	if err != nil {
		t.Fatalf("OpenSiteEvent(seems_down) error = %v", err)
	}
	if !insertedSeemsDown {
		t.Fatal("OpenSiteEvent(seems_down) inserted = false, want true")
	}

	insertedConfirmedDown, err := OpenSiteEvent(ctx, siteID, endpointID, CheckTypeHTTP, EventTypeConfirmedDown, EventSeverityHigh, startedAt)
	if err != nil {
		t.Fatalf("OpenSiteEvent(confirmed_down) error = %v", err)
	}
	if !insertedConfirmedDown {
		t.Fatal("OpenSiteEvent(confirmed_down) inserted = false, want true")
	}

	insertedAgain, err := OpenSiteEvent(ctx, siteID, endpointID, CheckTypeHTTP, EventTypeSeemsDown, EventSeverityLow, startedAt)
	if err != nil {
		t.Fatalf("OpenSiteEvent(seems_down duplicate) error = %v", err)
	}
	if insertedAgain {
		t.Fatal("OpenSiteEvent(seems_down duplicate) inserted = true, want false")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestConfirmDownTxCarriesStartedAtFromOpenSeemsDown(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	oldDB := db
	db = mockDB
	t.Cleanup(func() {
		db = oldDB
		_ = mockDB.Close()
	})

	siteID := int64(77)
	blogID := int64(123)
	var endpointID *int64
	changedAt := time.Date(2026, time.April, 24, 11, 0, 0, 0, time.UTC)
	originalStartedAt := time.Date(2026, time.April, 24, 10, 15, 0, 0, time.UTC)

	selectQuery := regexp.QuoteMeta(`SELECT started_at
		 FROM jetmon_site_events
		 WHERE jetpack_monitor_site_id = ?
		   AND endpoint_id <=> ?
		   AND check_type = ?
		   AND event_type = ?
		   AND ended_at IS NULL
		 ORDER BY started_at ASC
		 LIMIT 1
		 FOR UPDATE`)
	closeQuery := regexp.QuoteMeta(`UPDATE jetmon_site_events
		 SET ended_at = ?, resolution_reason = ?
		 WHERE jetpack_monitor_site_id = ?
		   AND endpoint_id <=> ?
		   AND check_type = ?
		   AND event_type = ?
		   AND ended_at IS NULL`)
	insertQuery := regexp.QuoteMeta(`INSERT INTO jetmon_site_events
			(jetpack_monitor_site_id, endpoint_id, check_type, event_type, severity, started_at)
		 SELECT ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (
			SELECT 1
			FROM jetmon_site_events
			WHERE jetpack_monitor_site_id = ?
			  AND endpoint_id <=> ?
			  AND check_type = ?
			  AND event_type = ?
			  AND ended_at IS NULL
		 )`)

	mock.ExpectBegin()
	mock.ExpectQuery(selectQuery).
		WithArgs(siteID, endpointID, CheckTypeHTTP, EventTypeSeemsDown).
		WillReturnRows(sqlmock.NewRows([]string{"started_at"}).AddRow(originalStartedAt))
	mock.ExpectExec(closeQuery).
		WithArgs(changedAt.UTC(), ResolutionReasonPromotedToConfirmedDown, siteID, endpointID, CheckTypeHTTP, EventTypeSeemsDown).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(insertQuery).
		WithArgs(siteID, endpointID, CheckTypeHTTP, EventTypeConfirmedDown, EventSeverityHigh, originalStartedAt.UTC(), siteID, endpointID, CheckTypeHTTP, EventTypeConfirmedDown).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE jetpack_monitor_sites SET site_status = ?, last_status_change = ? WHERE blog_id = ?`)).
		WithArgs(statusConfirmedDown, changedAt.UTC(), blogID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = ConfirmDownTx(context.Background(), siteID, blogID, endpointID, CheckTypeHTTP, EventTypeConfirmedDown, EventSeverityHigh, changedAt, true)
	if err != nil {
		t.Fatalf("ConfirmDownTx() error = %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCloseOpenSiteEventTypeClosesOnlyMatchingEventType(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	oldDB := db
	db = mockDB
	t.Cleanup(func() {
		db = oldDB
		_ = mockDB.Close()
	})

	endedAt := time.Date(2026, time.April, 24, 12, 0, 0, 0, time.UTC)
	siteID := int64(901)
	var endpointID *int64

	query := regexp.QuoteMeta(`UPDATE jetmon_site_events
		 SET ended_at = ?, resolution_reason = ?
		 WHERE jetpack_monitor_site_id = ?
		   AND endpoint_id <=> ?
		   AND check_type = ?
		   AND event_type = ?
		   AND ended_at IS NULL`)
	mock.ExpectExec(query).
		WithArgs(endedAt.UTC(), ResolutionReasonFalseAlarm, siteID, endpointID, CheckTypeHTTP, EventTypeSeemsDown).
		WillReturnResult(sqlmock.NewResult(0, 1))

	closed, err := CloseOpenSiteEventType(context.Background(), siteID, endpointID, CheckTypeHTTP, EventTypeSeemsDown, endedAt, ResolutionReasonFalseAlarm)
	if err != nil {
		t.Fatalf("CloseOpenSiteEventType() error = %v", err)
	}
	if !closed {
		t.Fatal("CloseOpenSiteEventType() closed = false, want true")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

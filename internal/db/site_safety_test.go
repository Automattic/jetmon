package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestUpsertSiteSafetyFlag(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectExec("INSERT INTO jetpack_monitor_site_safety_flags").
		WithArgs(int64(42), int64(123), SiteSafetyFlagUnsafeMonitorURL, "blocked", "http://127.0.0.1", SiteSafetyStatusDeactivated, nil).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := UpsertSiteSafetyFlag(context.Background(), DB(), SiteSafetyFlag{
		BlogID:        42,
		MonitorSiteID: 123,
		FlagType:      SiteSafetyFlagUnsafeMonitorURL,
		Reason:        "blocked",
		MonitorURL:    "http://127.0.0.1",
		Status:        SiteSafetyStatusDeactivated,
	}); err != nil {
		t.Fatalf("UpsertSiteSafetyFlag: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestUpsertSiteSafetyFlagTruncatesLongValues(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	var gotReason string
	var gotURL string
	mock.ExpectExec("INSERT INTO jetpack_monitor_site_safety_flags").
		WithArgs(int64(42), int64(123), SiteSafetyFlagProbeSafetyBlock, sqlmock.AnyArg(), sqlmock.AnyArg(), SiteSafetyStatusOpen, nil).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := UpsertSiteSafetyFlag(context.Background(), captureSiteSafetyFlagExecer{
		t:         t,
		mock:      DB(),
		gotReason: &gotReason,
		gotURL:    &gotURL,
	}, SiteSafetyFlag{
		BlogID:        42,
		MonitorSiteID: 123,
		FlagType:      SiteSafetyFlagProbeSafetyBlock,
		Reason:        strings.Repeat("x", siteSafetyReasonMaxRunes+1),
		MonitorURL:    "https://" + strings.Repeat("a", siteSafetyURLMaxRunes) + ".example",
	}); err != nil {
		t.Fatalf("UpsertSiteSafetyFlag: %v", err)
	}
	if len([]rune(gotReason)) != siteSafetyReasonMaxRunes {
		t.Fatalf("reason runes = %d, want %d", len([]rune(gotReason)), siteSafetyReasonMaxRunes)
	}
	if len([]rune(gotURL)) != siteSafetyURLMaxRunes {
		t.Fatalf("url runes = %d, want %d", len([]rune(gotURL)), siteSafetyURLMaxRunes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

type captureSiteSafetyFlagExecer struct {
	t         *testing.T
	mock      SiteSafetyFlagExecer
	gotReason *string
	gotURL    *string
}

func (c captureSiteSafetyFlagExecer) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c.t.Helper()
	if len(args) >= 5 {
		if reason, ok := args[3].(string); ok {
			*c.gotReason = reason
		}
		if url, ok := args[4].(string); ok {
			*c.gotURL = url
		}
	}
	return c.mock.ExecContext(ctx, query, args...)
}

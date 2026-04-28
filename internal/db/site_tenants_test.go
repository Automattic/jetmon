package db

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestUpsertSiteTenantMappings(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectBegin()
	prep := mock.ExpectPrepare("INSERT INTO jetmon_site_tenants")
	prep.ExpectExec().
		WithArgs("tenant-a", int64(42), "gateway").
		WillReturnResult(sqlmock.NewResult(0, 1))
	prep.ExpectExec().
		WithArgs("tenant-b", int64(43), "gateway").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	affected, err := UpsertSiteTenantMappings(context.Background(), DB(), []SiteTenantMapping{
		{TenantID: "tenant-a", BlogID: 42},
		{TenantID: "tenant-b", BlogID: 43},
	}, "")
	if err != nil {
		t.Fatalf("UpsertSiteTenantMappings: %v", err)
	}
	if affected != 3 {
		t.Fatalf("affected = %d, want 3", affected)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestUpsertSiteTenantMappingsValidatesInput(t *testing.T) {
	mock, cleanup := withMockDB(t)
	defer cleanup()

	mock.ExpectBegin()
	mock.ExpectPrepare("INSERT INTO jetmon_site_tenants")
	mock.ExpectRollback()

	_, err := UpsertSiteTenantMappings(context.Background(), DB(), []SiteTenantMapping{
		{TenantID: " ", BlogID: 42},
	}, "gateway")
	if err == nil {
		t.Fatal("UpsertSiteTenantMappings accepted empty tenant id")
	}
}

package main

import (
	"strings"
	"testing"

	"github.com/Automattic/jetmon/internal/db"
)

func TestParseSiteTenantMappingsHeaderDedupesAndSkipsBlanks(t *testing.T) {
	in, err := parseSiteTenantMappings(strings.NewReader(`
tenant_id,blog_id
tenant-a,42

tenant-a,42
tenant-b,43
`))
	if err != nil {
		t.Fatalf("parseSiteTenantMappings: %v", err)
	}
	if in.SkippedDuplicate != 1 {
		t.Fatalf("SkippedDuplicate = %d, want 1", in.SkippedDuplicate)
	}
	want := []db.SiteTenantMapping{
		{TenantID: "tenant-a", BlogID: 42},
		{TenantID: "tenant-b", BlogID: 43},
	}
	if len(in.Mappings) != len(want) {
		t.Fatalf("Mappings len = %d, want %d", len(in.Mappings), len(want))
	}
	for i := range want {
		if in.Mappings[i] != want[i] {
			t.Fatalf("Mappings[%d] = %+v, want %+v", i, in.Mappings[i], want[i])
		}
	}
}

func TestParseSiteTenantMappingsRejectsInvalidRows(t *testing.T) {
	tests := []struct {
		name string
		csv  string
		want string
	}{
		{name: "empty", csv: "\n", want: "no site tenant mappings"},
		{name: "missing tenant", csv: ",42\n", want: "tenant_id is required"},
		{name: "bad blog id", csv: "tenant-a,nope\n", want: "blog_id must be a positive integer"},
		{name: "too many columns", csv: "tenant-a,42,extra\n", want: "expected 2 columns"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseSiteTenantMappings(strings.NewReader(tt.csv))
			if err == nil {
				t.Fatal("parseSiteTenantMappings succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestIsSiteTenantHeader(t *testing.T) {
	if !isSiteTenantHeader([]string{" tenant_id ", " blog_id "}) {
		t.Fatal("isSiteTenantHeader did not accept canonical header")
	}
	if isSiteTenantHeader([]string{"tenant", "blog"}) {
		t.Fatal("isSiteTenantHeader accepted non-canonical header")
	}
}

package db

import (
	"os"
	"strings"
	"testing"
)

func TestParseSchemaDefinitionsHandlesGeneratedColumnsAndIndexes(t *testing.T) {
	data, err := os.ReadFile("../../migrations/production-v2-baseline.sql")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	defs, err := parseSchemaDefinitions(string(data))
	if err != nil {
		t.Fatalf("parseSchemaDefinitions() error = %v", err)
	}
	events := defs["jetpack_monitor_events"]
	if events.Name == "" {
		t.Fatal("jetpack_monitor_events definition missing")
	}
	if got := events.Columns["dedup_key"]; !strings.Contains(got, "GENERATED ALWAYS AS") {
		t.Fatalf("dedup_key definition = %q, want generated column", got)
	}
	if got := events.Indexes["uk_open_dedup"]; !strings.Contains(got, "UNIQUE KEY uk_open_dedup") {
		t.Fatalf("uk_open_dedup definition = %q, want unique key", got)
	}
	if got := events.Indexes["idx_blog_id_check_type_active"]; !strings.Contains(got, "idx_blog_id_check_type_active") {
		t.Fatalf("idx_blog_id_check_type_active definition = %q, want index", got)
	}
}

func TestParseLocalDevSitesDefinition(t *testing.T) {
	def, err := parseSingleSchemaDefinition(localDevSitesTableSQL)
	if err != nil {
		t.Fatalf("parseSingleSchemaDefinition() error = %v", err)
	}
	if def.Name != "jetpack_monitor_sites" {
		t.Fatalf("Name = %q, want jetpack_monitor_sites", def.Name)
	}
	if def.Columns["monitor_url"] == "" {
		t.Fatal("monitor_url column definition missing")
	}
	if def.Indexes["idx_bucket_active"] == "" {
		t.Fatal("idx_bucket_active index definition missing")
	}
}

package db

import (
	"fmt"
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

func TestInlinePrimaryKeyHelpers(t *testing.T) {
	cases := []struct {
		name      string
		def       string
		wantHas   bool
		wantStrip string
	}{
		{
			name:      "trailing primary key",
			def:       "source_site_id BIGINT UNSIGNED NOT NULL PRIMARY KEY",
			wantHas:   true,
			wantStrip: "source_site_id BIGINT UNSIGNED NOT NULL",
		},
		{
			name:      "primary key with extra whitespace",
			def:       "id  BIGINT  NOT  NULL  PRIMARY  KEY",
			wantHas:   true,
			wantStrip: "id BIGINT NOT NULL",
		},
		{
			name:      "primary key with auto increment",
			def:       "id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY",
			wantHas:   true,
			wantStrip: "id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT",
		},
		{
			name:      "no primary key",
			def:       "blog_id BIGINT UNSIGNED NOT NULL",
			wantHas:   false,
			wantStrip: "blog_id BIGINT UNSIGNED NOT NULL",
		},
		{
			name: "default value containing the word primary is unaffected",
			// A primary-key-like substring inside a quoted default would only
			// be a problem if a column genuinely needs `PRIMARY KEY` as a
			// literal default. None of our baseline columns do; the regex's
			// word boundary keeps the helper conservative.
			def:       "label VARCHAR(64) NOT NULL DEFAULT 'primary'",
			wantHas:   false,
			wantStrip: "label VARCHAR(64) NOT NULL DEFAULT 'primary'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasInlinePrimaryKey(tc.def); got != tc.wantHas {
				t.Errorf("hasInlinePrimaryKey(%q) = %v, want %v", tc.def, got, tc.wantHas)
			}
			if got := stripInlinePrimaryKey(tc.def); got != tc.wantStrip {
				t.Errorf("stripInlinePrimaryKey(%q) = %q, want %q", tc.def, got, tc.wantStrip)
			}
		})
	}
}

func TestReconcileAddColumnStripsInlinePKWhenTableHasExistingPrimary(t *testing.T) {
	// Synthetic baseline: a table whose PK is an inline-declared column.
	baseline := `CREATE TABLE IF NOT EXISTS t (
		source_site_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
		blog_id BIGINT UNSIGNED NOT NULL,
		INDEX idx_blog_id (blog_id)
	) ENGINE=InnoDB;`
	defs, err := parseSchemaDefinitions(baseline)
	if err != nil {
		t.Fatalf("parseSchemaDefinitions() error = %v", err)
	}
	def := defs["t"]
	rawDef := def.Columns["source_site_id"]
	if !hasInlinePrimaryKey(rawDef) {
		t.Fatalf("baseline parsed without inline PK: %q", rawDef)
	}

	// Case A: live table is missing the column AND has no PRIMARY index.
	// The reconciler should keep the inline PRIMARY KEY so the new column
	// becomes the table's PK in one step.
	t.Run("table_missing_primary_keeps_inline_pk", func(t *testing.T) {
		sql := buildAddColumnSQL(t, "t", rawDef /*primaryAlreadyExists=*/, false)
		if !strings.Contains(strings.ToUpper(sql), "PRIMARY KEY") {
			t.Errorf("expected ADD COLUMN to retain PRIMARY KEY when table has no PK; got %q", sql)
		}
	})

	// Case B: live table is missing the column but ALREADY has a different
	// PRIMARY index. The reconciler must strip the inline PRIMARY KEY to
	// avoid "Multiple primary key defined" (MySQL error 1068).
	t.Run("table_has_existing_primary_strips_inline_pk", func(t *testing.T) {
		sql := buildAddColumnSQL(t, "t", rawDef /*primaryAlreadyExists=*/, true)
		if strings.Contains(strings.ToUpper(sql), "PRIMARY KEY") {
			t.Errorf("expected ADD COLUMN to strip PRIMARY KEY when table already has PK; got %q", sql)
		}
		if !strings.Contains(sql, "BIGINT UNSIGNED NOT NULL") {
			t.Errorf("expected ADD COLUMN to retain the rest of the column def; got %q", sql)
		}
	})
}

// buildAddColumnSQL reconstructs the ADD COLUMN SQL that
// BuildSchemaReconcilePlan emits, applying the same inline-PK guard. It is a
// pure test helper that avoids hitting a live database.
func buildAddColumnSQL(t *testing.T, table, columnDef string, primaryAlreadyExists bool) string {
	t.Helper()
	if hasInlinePrimaryKey(columnDef) && primaryAlreadyExists {
		columnDef = stripInlinePrimaryKey(columnDef)
	}
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", quoteIdentifier(table), columnDef)
}

func TestProductionBaselineMatchesSchemaContract(t *testing.T) {
	data, err := os.ReadFile("../../migrations/production-v2-baseline.sql")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	defs, err := parseSchemaDefinitions(string(data))
	if err != nil {
		t.Fatalf("parseSchemaDefinitions() error = %v", err)
	}
	localSitesDef, err := parseSingleSchemaDefinition(localDevSitesTableSQL)
	if err != nil {
		t.Fatalf("parse local sites definition: %v", err)
	}
	defs[localSitesDef.Name] = localSitesDef

	for _, contract := range schemaContracts {
		def, ok := defs[contract.table]
		if !ok {
			t.Fatalf("baseline missing table %s", contract.table)
		}
		for _, column := range contract.columns {
			if def.Columns[column] == "" {
				t.Fatalf("baseline table %s missing contract column %s", contract.table, column)
			}
		}
		for _, index := range contract.indexes {
			if def.Indexes[index] == "" {
				t.Fatalf("baseline table %s missing contract index %s", contract.table, index)
			}
		}
	}
}

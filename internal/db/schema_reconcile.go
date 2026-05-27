package db

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const localDevSitesTableSQL = `CREATE TABLE IF NOT EXISTS jetpack_monitor_sites (
    jetpack_monitor_site_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    blog_id BIGINT UNSIGNED NOT NULL,
    bucket_no SMALLINT UNSIGNED NOT NULL DEFAULT 0,
    monitor_url VARCHAR(2083) NOT NULL DEFAULT '',
    monitor_active TINYINT UNSIGNED NOT NULL DEFAULT 0,
    site_status TINYINT NOT NULL DEFAULT 1,
    last_status_change DATETIME NULL,
    check_interval SMALLINT UNSIGNED NOT NULL DEFAULT 5,
    INDEX idx_bucket_active (bucket_no, monitor_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;`

// SchemaReconcileStatement is one additive DDL statement needed to make a
// local/lab database satisfy the structural schema contract. It never contains
// destructive DDL such as DROP or MODIFY.
type SchemaReconcileStatement struct {
	Kind  string
	Table string
	Name  string
	SQL   string
}

// SchemaReconcilePlan describes the additive DDL the limited first-party
// reconciler can derive. Unresolved items require manual review because they
// would need destructive or ambiguous changes.
type SchemaReconcilePlan struct {
	Status     SchemaContractStatus
	Statements []SchemaReconcileStatement
	Unresolved []SchemaObjectIssue
}

func (p SchemaReconcilePlan) Empty() bool {
	return len(p.Statements) == 0 && len(p.Unresolved) == 0
}

// BuildSchemaReconcilePlan compares the live schema with the production
// baseline SQL and returns additive DDL needed for local/lab convergence.
// It deliberately does not consult jetpack_monitor_schema_migrations.
func BuildSchemaReconcilePlan(ctx context.Context, baselineSQL string) (SchemaReconcilePlan, error) {
	status, err := SchemaContract(ctx)
	plan := SchemaReconcilePlan{Status: status}
	if err != nil {
		return plan, err
	}
	if status.OK() {
		return plan, nil
	}

	defs, err := parseSchemaDefinitions(baselineSQL)
	if err != nil {
		return plan, err
	}
	localSitesDef, err := parseSingleSchemaDefinition(localDevSitesTableSQL)
	if err != nil {
		return plan, err
	}
	defs[localSitesDef.Name] = localSitesDef

	missingTables := map[string]struct{}{}
	for _, table := range status.MissingTables {
		missingTables[table] = struct{}{}
		def, ok := defs[table]
		if !ok {
			plan.Unresolved = append(plan.Unresolved, SchemaObjectIssue{Table: table, Name: "table"})
			continue
		}
		plan.Statements = append(plan.Statements, SchemaReconcileStatement{
			Kind:  "create_table",
			Table: table,
			Name:  table,
			SQL:   ensureSemicolon(def.CreateSQL),
		})
	}

	for _, issue := range status.MissingColumns {
		if _, tableWillBeCreated := missingTables[issue.Table]; tableWillBeCreated {
			continue
		}
		def, ok := defs[issue.Table]
		if !ok {
			plan.Unresolved = append(plan.Unresolved, issue)
			continue
		}
		columnDef := def.Columns[issue.Name]
		if columnDef == "" {
			plan.Unresolved = append(plan.Unresolved, issue)
			continue
		}
		if columnDefinitionHasInlinePrimaryKey(columnDef) {
			plan.Unresolved = append(plan.Unresolved, SchemaObjectIssue{
				Table: issue.Table,
				Name:  issue.Name + " (inline primary key requires reviewed table rebuild)",
			})
			continue
		}
		plan.Statements = append(plan.Statements, SchemaReconcileStatement{
			Kind:  "add_column",
			Table: issue.Table,
			Name:  issue.Name,
			SQL:   fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s;", quoteIdentifier(issue.Table), columnDef),
		})
	}

	for _, issue := range status.MissingIndexes {
		if _, tableWillBeCreated := missingTables[issue.Table]; tableWillBeCreated {
			continue
		}
		if strings.HasPrefix(issue.Name, "index_prefix(") && issue.Table == "jetpack_monitor_sites" {
			plan.Statements = append(plan.Statements, SchemaReconcileStatement{
				Kind:  "add_index",
				Table: issue.Table,
				Name:  "idx_bucket_active",
				SQL:   "CREATE INDEX idx_bucket_active ON jetpack_monitor_sites (bucket_no, monitor_active);",
			})
			continue
		}
		if issue.Name == "PRIMARY" {
			plan.Unresolved = append(plan.Unresolved, issue)
			continue
		}
		def, ok := defs[issue.Table]
		if !ok {
			plan.Unresolved = append(plan.Unresolved, issue)
			continue
		}
		indexDef := def.Indexes[issue.Name]
		if indexDef == "" {
			plan.Unresolved = append(plan.Unresolved, issue)
			continue
		}
		plan.Statements = append(plan.Statements, SchemaReconcileStatement{
			Kind:  "add_index",
			Table: issue.Table,
			Name:  issue.Name,
			SQL:   fmt.Sprintf("ALTER TABLE %s ADD %s;", quoteIdentifier(issue.Table), indexDef),
		})
	}

	sort.SliceStable(plan.Statements, func(i, j int) bool {
		if plan.Statements[i].Table == plan.Statements[j].Table {
			if plan.Statements[i].Kind == plan.Statements[j].Kind {
				return plan.Statements[i].Name < plan.Statements[j].Name
			}
			return plan.Statements[i].Kind < plan.Statements[j].Kind
		}
		return plan.Statements[i].Table < plan.Statements[j].Table
	})
	sortSchemaObjectIssues(plan.Unresolved)
	return plan, nil
}

// ApplySchemaReconcilePlan executes a plan generated by BuildSchemaReconcilePlan.
// Callers are responsible for environment guardrails; this function only
// executes statements already constrained to additive DDL by the planner.
func ApplySchemaReconcilePlan(ctx context.Context, plan SchemaReconcilePlan) error {
	if len(plan.Unresolved) > 0 {
		return fmt.Errorf("schema reconcile has unresolved items: %v", plan.Unresolved)
	}
	if len(plan.Statements) == 0 {
		return nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("schema reconcile connection: %w", err)
	}
	defer conn.Close()
	if err := acquireMigrationLock(ctx, conn); err != nil {
		return err
	}
	defer func() { _ = releaseMigrationLock(ctx, conn) }()

	for _, stmt := range plan.Statements {
		if _, err := conn.ExecContext(ctx, stmt.SQL); err != nil {
			return fmt.Errorf("schema reconcile %s %s.%s: %w", stmt.Kind, stmt.Table, stmt.Name, err)
		}
	}
	return nil
}

type schemaDefinition struct {
	Name      string
	CreateSQL string
	Columns   map[string]string
	Indexes   map[string]string
}

func parseSchemaDefinitions(sqlText string) (map[string]schemaDefinition, error) {
	out := map[string]schemaDefinition{}
	re := regexp.MustCompile(`(?is)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+` + "`?" + `([a-zA-Z0-9_]+)` + "`?" + `\s*\((.*?)\)\s*ENGINE\s*=\s*[^;]+;`)
	matches := re.FindAllStringSubmatch(sqlText, -1)
	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		def, err := parseSchemaDefinition(match[1], match[0], match[2])
		if err != nil {
			return nil, err
		}
		out[def.Name] = def
	}
	return out, nil
}

func parseSingleSchemaDefinition(sqlText string) (schemaDefinition, error) {
	defs, err := parseSchemaDefinitions(sqlText)
	if err != nil {
		return schemaDefinition{}, err
	}
	for _, def := range defs {
		return def, nil
	}
	return schemaDefinition{}, fmt.Errorf("no CREATE TABLE statement found")
}

func parseSchemaDefinition(name, createSQL, body string) (schemaDefinition, error) {
	def := schemaDefinition{
		Name:      name,
		CreateSQL: normalizeWhitespace(createSQL),
		Columns:   map[string]string{},
		Indexes:   map[string]string{},
	}
	for _, part := range splitTopLevelComma(body) {
		part = normalizeWhitespace(part)
		if part == "" {
			continue
		}
		upper := strings.ToUpper(part)
		switch {
		case strings.HasPrefix(upper, "PRIMARY KEY"):
			def.Indexes["PRIMARY"] = part
		case strings.HasPrefix(upper, "UNIQUE KEY "):
			if indexName := nthToken(part, 2); indexName != "" {
				def.Indexes[indexName] = part
			}
		case strings.HasPrefix(upper, "KEY "), strings.HasPrefix(upper, "INDEX "):
			if indexName := nthToken(part, 1); indexName != "" {
				def.Indexes[indexName] = part
			}
		case strings.HasPrefix(upper, "CONSTRAINT "):
			continue
		default:
			columnName := firstToken(part)
			if columnName == "" {
				continue
			}
			def.Columns[columnName] = part
			if strings.Contains(upper, " PRIMARY KEY") {
				def.Indexes["PRIMARY"] = "PRIMARY KEY (" + quoteIdentifier(columnName) + ")"
			}
		}
	}
	return def, nil
}

func columnDefinitionHasInlinePrimaryKey(def string) bool {
	return strings.Contains(strings.ToUpper(def), " PRIMARY KEY")
}

func splitTopLevelComma(s string) []string {
	var parts []string
	var b strings.Builder
	depth := 0
	inSingleQuote := false
	escaped := false
	for _, r := range s {
		if inSingleQuote {
			b.WriteRune(r)
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '\'' {
				inSingleQuote = false
			}
			continue
		}
		switch r {
		case '\'':
			inSingleQuote = true
			b.WriteRune(r)
		case '(':
			depth++
			b.WriteRune(r)
		case ')':
			if depth > 0 {
				depth--
			}
			b.WriteRune(r)
		case ',':
			if depth == 0 {
				parts = append(parts, b.String())
				b.Reset()
				continue
			}
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	if strings.TrimSpace(b.String()) != "" {
		parts = append(parts, b.String())
	}
	return parts
}

func firstToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return cleanIdentifier(fields[0])
}

func nthToken(s string, n int) string {
	fields := strings.Fields(s)
	if len(fields) <= n {
		return ""
	}
	return cleanIdentifier(fields[n])
}

func cleanIdentifier(s string) string {
	return strings.Trim(s, "`")
}

func quoteIdentifier(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func ensureSemicolon(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, ";") {
		return s
	}
	return s + ";"
}

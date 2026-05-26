package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// SchemaObjectIssue identifies one missing table child object.
type SchemaObjectIssue struct {
	Table string
	Name  string
}

// SchemaContractStatus summarizes the physical schema contract expected by
// this binary. It intentionally validates tables, columns, and indexes instead
// of relying on jetpack_monitor_schema_migrations so production databases can
// be managed by an external change process without maintaining a migration
// ledger solely for Jetmon's startup checks.
type SchemaContractStatus struct {
	ExpectedTables  int
	PresentTables   int
	ExpectedColumns int
	PresentColumns  int
	ExpectedIndexes int
	PresentIndexes  int
	MissingTables   []string
	MissingColumns  []SchemaObjectIssue
	MissingIndexes  []SchemaObjectIssue
}

func (s SchemaContractStatus) OK() bool {
	return len(s.MissingTables) == 0 && len(s.MissingColumns) == 0 && len(s.MissingIndexes) == 0
}

func (s SchemaContractStatus) Summary() string {
	return fmt.Sprintf("tables=%d/%d columns=%d/%d indexes=%d/%d",
		s.PresentTables, s.ExpectedTables,
		s.PresentColumns, s.ExpectedColumns,
		s.PresentIndexes, s.ExpectedIndexes)
}

type schemaTableContract struct {
	table   string
	columns []string
	indexes []string
}

type schemaIndexPrefixContract struct {
	table   string
	name    string
	columns []string
}

type schemaIndexExactContract struct {
	table   string
	name    string
	index   string
	columns []string
}

var schemaContracts = []schemaTableContract{
	{"jetpack_monitor_sites",
		[]string{"jetpack_monitor_site_id", "blog_id", "bucket_no", "monitor_url", "monitor_active", "site_status", "last_status_change", "check_interval"},
		[]string{"PRIMARY"}},
	{"jetpack_monitor_hosts",
		[]string{"host_id", "bucket_min", "bucket_max", "last_heartbeat", "status"},
		[]string{"PRIMARY"}},
	{"jetpack_monitor_audit_log",
		[]string{"id", "blog_id", "event_id", "event_type", "source", "detail", "metadata", "created_at"},
		[]string{"PRIMARY", "idx_blog_id_created", "idx_created_at", "idx_event_id", "idx_event_type_created"}},
	{"jetpack_monitor_check_history",
		[]string{"id", "blog_id", "source_site_id", "request_method", "http_code", "error_code", "rtt_ms", "dns_ms", "tcp_ms", "tls_ms", "ttfb_ms", "checked_at"},
		[]string{"PRIMARY", "idx_blog_id_checked", "idx_source_site_checked", "idx_checked_at"}},
	{"jetpack_monitor_false_positives",
		[]string{"id", "blog_id", "http_code", "error_code", "rtt_ms", "created_at"},
		[]string{"PRIMARY", "idx_blog_id"}},
	{"jetpack_monitor_events",
		[]string{"id", "blog_id", "endpoint_id", "check_type", "discriminator", "severity", "state", "started_at", "ended_at", "resolution_reason", "cause_event_id", "metadata", "updated_at", "dedup_key"},
		[]string{"PRIMARY", "uk_open_dedup", "idx_blog_id_started", "idx_blog_id_active", "idx_check_type_started", "idx_cause_event_id", "idx_blog_id_check_type_active"}},
	{"jetpack_monitor_event_transitions",
		[]string{"id", "event_id", "blog_id", "endpoint_id", "severity_before", "severity_after", "state_before", "state_after", "reason", "source", "metadata", "changed_at"},
		[]string{"PRIMARY", "idx_event_id_changed", "idx_blog_id_changed", "idx_endpoint_id_changed", "idx_changed_at"}},
	{"jetpack_monitor_api_keys",
		[]string{"id", "key_hash", "consumer_name", "scope", "rate_limit_per_minute", "expires_at", "revoked_at", "last_used_at", "created_at", "created_by"},
		[]string{"PRIMARY", "uk_key_hash", "idx_consumer"}},
	{"jetpack_monitor_webhooks",
		[]string{"id", "url", "active", "owner_tenant_id", "events", "site_filter", "state_filter", "secret", "secret_preview", "created_by", "created_at", "updated_at"},
		[]string{"PRIMARY", "idx_active", "idx_owner_tenant_id"}},
	{"jetpack_monitor_webhook_deliveries",
		[]string{"id", "webhook_id", "transition_id", "event_id", "event_type", "payload", "status", "attempt", "next_attempt_at", "last_status_code", "last_response", "last_attempt_at", "delivered_at", "created_at"},
		[]string{"PRIMARY", "uk_webhook_transition", "idx_status_next_attempt", "idx_webhook_id_created", "idx_event_id", "idx_status_delivered_at", "idx_status_last_attempt_at", "idx_status_created_at"}},
	{"jetpack_monitor_webhook_dispatch_progress",
		[]string{"instance_id", "last_transition_id", "updated_at"},
		[]string{"PRIMARY"}},
	{"jetpack_monitor_alert_contacts",
		[]string{"id", "label", "active", "owner_tenant_id", "transport", "destination", "destination_preview", "site_filter", "min_severity", "max_per_hour", "created_by", "created_at", "updated_at"},
		[]string{"PRIMARY", "idx_active", "idx_owner_tenant_id"}},
	{"jetpack_monitor_alert_deliveries",
		[]string{"id", "alert_contact_id", "transition_id", "event_id", "event_type", "severity", "payload", "status", "attempt", "next_attempt_at", "last_status_code", "last_response", "last_attempt_at", "delivered_at", "created_at"},
		[]string{"PRIMARY", "uk_alert_transition", "idx_status_next_attempt", "idx_contact_id_created", "idx_event_id", "idx_status_delivered_at", "idx_status_last_attempt_at", "idx_status_created_at"}},
	{"jetpack_monitor_alert_dispatch_progress",
		[]string{"instance_id", "last_transition_id", "updated_at"},
		[]string{"PRIMARY"}},
	{"jetpack_monitor_site_tenants",
		[]string{"tenant_id", "blog_id", "source", "created_at", "updated_at"},
		[]string{"PRIMARY", "idx_blog_id"}},
	{"jetpack_monitor_process_health",
		[]string{"process_id", "host_id", "process_type", "pid", "version", "build_date", "go_version", "state", "health_status", "started_at", "updated_at", "bucket_min", "bucket_max", "bucket_ownership", "api_port", "dashboard_port", "delivery_workers_enabled", "delivery_owner_host", "worker_count", "active_checks", "queue_depth", "retry_queue_size", "wpcom_circuit_open", "wpcom_queue_depth", "go_sys_mem_mb", "rss_mem_mb", "runtime_goroutines", "runtime_goroutines_runnable", "runtime_goroutines_running", "runtime_goroutines_waiting", "runtime_goroutines_not_in_go", "runtime_goroutines_created", "runtime_threads", "dependency_health", "created_at"},
		[]string{"PRIMARY", "idx_process_type_updated", "idx_host_updated", "idx_state_updated", "idx_health_status_updated"}},
	{"jetpack_monitor_check_targets",
		[]string{"target_id", "blog_id", "source_site_id", "bucket_no", "monitor_url", "monitor_active", "check_interval_sec", "phase_slot_sec", "config_hash", "last_config_sync_at", "last_checked_at", "last_success_at", "last_failure_at", "last_outcome", "updated_at"},
		[]string{"PRIMARY", "uk_source_site_id", "idx_bucket_phase", "idx_bucket_active", "idx_blog_id", "idx_config_sync"}},
	{"jetpack_monitor_site_check_config",
		[]string{"source_site_id", "blog_id", "request_method", "detection_profile", "check_keyword", "forbidden_keyword", "forbidden_keywords", "maintenance_start", "maintenance_end", "custom_headers", "timeout_seconds", "redirect_policy", "alert_cooldown_minutes", "check_history_mode", "check_history_sample_rate", "created_at", "updated_at"},
		[]string{"PRIMARY", "idx_blog_id", "idx_request_method", "idx_detection_profile"}},
	{"jetpack_monitor_site_runtime",
		[]string{"source_site_id", "blog_id", "last_checked_at", "next_check_at", "last_alert_sent_at", "ssl_expiry_date", "updated_at"},
		[]string{"PRIMARY", "idx_blog_id", "idx_next_check", "idx_last_checked"}},
	{"jetpack_monitor_veriflier_vantages",
		[]string{"vantage_id", "region", "provider", "endpoint_host", "endpoint_port", "auth_token", "enabled", "created_at", "updated_at"},
		[]string{"PRIMARY", "idx_enabled", "idx_endpoint"}},
	{"jetpack_monitor_veriflier_agents",
		[]string{"agent_id", "vantage_id", "hostname", "endpoint_host", "endpoint_port", "version", "protocols", "max_concurrency", "queue_capacity", "queue_depth", "active", "in_flight", "status", "last_seen", "created_at", "updated_at"},
		[]string{"PRIMARY", "idx_vantage_seen", "idx_status_seen"}},
	{"jetpack_monitor_rollout_sessions",
		[]string{"run_id", "bucket_min", "bucket_max", "owner_host", "change_ref", "operator", "status", "metadata", "created_at", "updated_at"},
		[]string{"PRIMARY", "idx_status_created", "idx_range"}},
	{"jetpack_monitor_rollout_range_locks",
		[]string{"id", "run_id", "bucket_min", "bucket_max", "owner_host", "change_ref", "status", "activated_at", "released_at", "metadata"},
		[]string{"PRIMARY", "idx_status_range", "idx_owner_status", "idx_run_id"}},
	{"jetpack_monitor_rollout_bucket_locks",
		[]string{"bucket_no", "run_id", "range_lock_id", "owner_host", "status", "activated_at"},
		[]string{"PRIMARY", "idx_owner", "idx_run_id", "idx_range_lock"}},
	{"jetpack_monitor_rollout_jobs",
		[]string{"job_id", "run_id", "operation", "status", "progress", "summary", "result", "error_code", "error_message", "created_at", "updated_at"},
		[]string{"PRIMARY", "idx_run_operation", "idx_status_created"}},
	{"jetpack_monitor_rollout_confirmation_tokens",
		[]string{"token_hash", "run_id", "operation", "request_hash", "bucket_min", "bucket_max", "operator", "expires_at", "used_at", "created_at"},
		[]string{"PRIMARY", "idx_run_operation", "idx_expires"}},
	{"jetpack_monitor_rollout_comparison_results",
		[]string{"id", "job_id", "run_id", "blog_id", "source_site_id", "bucket_no", "monitor_url", "from_method", "from_profile", "to_method", "to_profile", "from_success", "to_success", "from_http_code", "to_http_code", "from_error_code", "to_error_code", "from_rtt_ms", "to_rtt_ms", "delta_class", "created_at"},
		[]string{"PRIMARY", "idx_job", "idx_run_created", "idx_delta_created", "idx_site"}},
	{"jetpack_monitor_rollout_policy_stage_rows",
		[]string{"id", "job_id", "run_id", "blog_id", "source_site_id", "bucket_no", "previous_request_method", "previous_detection_profile", "new_request_method", "new_detection_profile", "rolled_back_at", "created_at"},
		[]string{"PRIMARY", "idx_job", "idx_run_created", "idx_rollback", "idx_blog", "idx_source_site"}},
	{"jetpack_monitor_site_safety_flags",
		[]string{"id", "blog_id", "monitor_site_id", "flag_type", "reason", "monitor_url", "status", "first_seen_at", "last_seen_at", "deactivated_at", "created_at", "updated_at"},
		[]string{"PRIMARY", "uniq_site_flag", "idx_blog_status", "idx_status_seen", "idx_type_status"}},
}

var schemaIndexPrefixContracts = []schemaIndexPrefixContract{
	{
		table:   "jetpack_monitor_sites",
		name:    "index_prefix(bucket_no,monitor_active)",
		columns: []string{"bucket_no", "monitor_active"},
	},
}

var schemaIndexExactContracts = []schemaIndexExactContract{
	{
		table:   "jetpack_monitor_site_check_config",
		name:    "index_columns(PRIMARY:source_site_id)",
		index:   "PRIMARY",
		columns: []string{"source_site_id"},
	},
	{
		table:   "jetpack_monitor_site_runtime",
		name:    "index_columns(PRIMARY:source_site_id)",
		index:   "PRIMARY",
		columns: []string{"source_site_id"},
	},
}

// SchemaContract inspects the connected database's physical schema. It does
// not read or require jetpack_monitor_schema_migrations.
func SchemaContract(ctx context.Context) (SchemaContractStatus, error) {
	return SchemaContractForDB(ctx, db)
}

// SchemaContractForDB inspects a specific database handle's physical schema.
// Use this from packages that already hold their own *sql.DB, such as the API
// server during rollout preflight tests.
func SchemaContractForDB(ctx context.Context, database *sql.DB) (SchemaContractStatus, error) {
	status := expectedSchemaContractStatus()
	if database == nil {
		return status, fmt.Errorf("schema contract database handle is nil")
	}
	conn, err := database.Conn(ctx)
	if err != nil {
		return status, fmt.Errorf("schema contract connection: %w", err)
	}
	defer conn.Close()

	var schema sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&schema); err != nil {
		return status, fmt.Errorf("schema contract database name: %w", err)
	}
	if !schema.Valid || strings.TrimSpace(schema.String) == "" {
		return status, fmt.Errorf("schema contract database name is empty")
	}

	tables, err := readInformationSchemaSet(ctx, conn,
		`SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?`,
		schema.String,
	)
	if err != nil {
		return status, fmt.Errorf("schema contract tables: %w", err)
	}
	columns, err := readInformationSchemaPairs(ctx, conn,
		`SELECT TABLE_NAME, COLUMN_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ?`,
		schema.String,
	)
	if err != nil {
		return status, fmt.Errorf("schema contract columns: %w", err)
	}
	indexes, err := readInformationSchemaPairs(ctx, conn,
		`SELECT TABLE_NAME, INDEX_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ?`,
		schema.String,
	)
	if err != nil {
		return status, fmt.Errorf("schema contract indexes: %w", err)
	}
	indexColumns, err := readInformationSchemaIndexColumns(ctx, conn, schema.String)
	if err != nil {
		return status, fmt.Errorf("schema contract index columns: %w", err)
	}

	return evaluateSchemaContract(schemaContracts, schemaIndexPrefixContracts, tables, columns, indexes, indexColumns), nil
}

// ValidateSchema fails unless the connected database satisfies the structural
// schema contract expected by this binary. It never creates tables, applies DDL,
// or requires the dev/local migration ledger.
func ValidateSchema(ctx context.Context) (SchemaContractStatus, error) {
	status, err := SchemaContract(ctx)
	if err != nil {
		return status, err
	}
	if !status.OK() {
		return status, fmt.Errorf("schema validation failed: %s missing_tables=%d missing_columns=%d missing_indexes=%d",
			status.Summary(), len(status.MissingTables), len(status.MissingColumns), len(status.MissingIndexes))
	}
	return status, nil
}

func expectedSchemaContractStatus() SchemaContractStatus {
	status := SchemaContractStatus{ExpectedTables: len(schemaContracts)}
	for _, contract := range schemaContracts {
		status.ExpectedColumns += len(contract.columns)
		status.ExpectedIndexes += len(contract.indexes)
	}
	status.ExpectedIndexes += len(schemaIndexPrefixContracts)
	status.ExpectedIndexes += len(schemaIndexExactContracts)
	return status
}

func evaluateSchemaContract(contracts []schemaTableContract, indexPrefixContracts []schemaIndexPrefixContract, tables map[string]struct{}, columns, indexes map[string]map[string]struct{}, indexColumns map[string]map[string][]string) SchemaContractStatus {
	status := SchemaContractStatus{ExpectedTables: len(contracts)}
	indexPrefixesByTable := map[string][]schemaIndexPrefixContract{}
	for _, contract := range indexPrefixContracts {
		indexPrefixesByTable[contract.table] = append(indexPrefixesByTable[contract.table], contract)
	}
	exactIndexesByTable := map[string][]schemaIndexExactContract{}
	for _, contract := range schemaIndexExactContracts {
		exactIndexesByTable[contract.table] = append(exactIndexesByTable[contract.table], contract)
	}

	for _, contract := range contracts {
		status.ExpectedColumns += len(contract.columns)
		status.ExpectedIndexes += len(contract.indexes)
		status.ExpectedIndexes += len(indexPrefixesByTable[contract.table])
		status.ExpectedIndexes += len(exactIndexesByTable[contract.table])

		if _, ok := tables[contract.table]; !ok {
			status.MissingTables = append(status.MissingTables, contract.table)
			for _, column := range contract.columns {
				status.MissingColumns = append(status.MissingColumns, SchemaObjectIssue{Table: contract.table, Name: column})
			}
			for _, index := range contract.indexes {
				status.MissingIndexes = append(status.MissingIndexes, SchemaObjectIssue{Table: contract.table, Name: index})
			}
			for _, index := range indexPrefixesByTable[contract.table] {
				status.MissingIndexes = append(status.MissingIndexes, SchemaObjectIssue{Table: contract.table, Name: index.name})
			}
			for _, index := range exactIndexesByTable[contract.table] {
				status.MissingIndexes = append(status.MissingIndexes, SchemaObjectIssue{Table: index.table, Name: index.name})
			}
			continue
		}
		status.PresentTables++

		for _, column := range contract.columns {
			if hasSchemaObject(columns, contract.table, column) {
				status.PresentColumns++
			} else {
				status.MissingColumns = append(status.MissingColumns, SchemaObjectIssue{Table: contract.table, Name: column})
			}
		}
		for _, index := range contract.indexes {
			if hasSchemaObject(indexes, contract.table, index) {
				status.PresentIndexes++
			} else {
				status.MissingIndexes = append(status.MissingIndexes, SchemaObjectIssue{Table: contract.table, Name: index})
			}
		}
		for _, index := range indexPrefixesByTable[contract.table] {
			if hasIndexColumnPrefix(indexColumns, index.table, index.columns) {
				status.PresentIndexes++
			} else {
				status.MissingIndexes = append(status.MissingIndexes, SchemaObjectIssue{Table: index.table, Name: index.name})
			}
		}
		for _, index := range exactIndexesByTable[contract.table] {
			if hasExactIndexColumns(indexColumns, index.table, index.index, index.columns) {
				status.PresentIndexes++
			} else {
				status.MissingIndexes = append(status.MissingIndexes, SchemaObjectIssue{Table: index.table, Name: index.name})
			}
		}
	}
	sort.Strings(status.MissingTables)
	sortSchemaObjectIssues(status.MissingColumns)
	sortSchemaObjectIssues(status.MissingIndexes)
	return status
}

func hasSchemaObject(objects map[string]map[string]struct{}, table, name string) bool {
	names, ok := objects[table]
	if !ok {
		return false
	}
	_, ok = names[name]
	return ok
}

func hasIndexColumnPrefix(indexColumns map[string]map[string][]string, table string, columnPrefix []string) bool {
	indexes, ok := indexColumns[table]
	if !ok {
		return false
	}
	for _, columns := range indexes {
		if len(columns) < len(columnPrefix) {
			continue
		}
		matches := true
		for i, column := range columnPrefix {
			if columns[i] != column {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func hasExactIndexColumns(indexColumns map[string]map[string][]string, table, index string, columns []string) bool {
	indexes, ok := indexColumns[table]
	if !ok {
		return false
	}
	got, ok := indexes[index]
	if !ok || len(got) != len(columns) {
		return false
	}
	for i, column := range columns {
		if got[i] != column {
			return false
		}
	}
	return true
}

func readInformationSchemaSet(ctx context.Context, conn *sql.Conn, query, schema string) (map[string]struct{}, error) {
	rows, err := conn.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]struct{}{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

func readInformationSchemaPairs(ctx context.Context, conn *sql.Conn, query, schema string) (map[string]map[string]struct{}, error) {
	rows, err := conn.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]map[string]struct{}{}
	for rows.Next() {
		var table, name string
		if err := rows.Scan(&table, &name); err != nil {
			return nil, err
		}
		if _, ok := out[table]; !ok {
			out[table] = map[string]struct{}{}
		}
		out[table][name] = struct{}{}
	}
	return out, rows.Err()
}

func readInformationSchemaIndexColumns(ctx context.Context, conn *sql.Conn, schema string) (map[string]map[string][]string, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT TABLE_NAME, INDEX_NAME, COLUMN_NAME
		  FROM information_schema.STATISTICS
		 WHERE TABLE_SCHEMA = ?
		 ORDER BY TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]map[string][]string{}
	for rows.Next() {
		var table, index, column string
		if err := rows.Scan(&table, &index, &column); err != nil {
			return nil, err
		}
		if _, ok := out[table]; !ok {
			out[table] = map[string][]string{}
		}
		out[table][index] = append(out[table][index], column)
	}
	return out, rows.Err()
}

func sortSchemaObjectIssues(issues []SchemaObjectIssue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Table == issues[j].Table {
			return issues[i].Name < issues[j].Name
		}
		return issues[i].Table < issues[j].Table
	})
}

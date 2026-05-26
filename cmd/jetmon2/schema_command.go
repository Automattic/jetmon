package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

func cmdSchema(args []string) {
	if len(args) == 0 {
		printSchemaUsage(os.Stderr)
		os.Exit(1)
	}
	var err error
	switch args[0] {
	case "ensure":
		err = cmdSchemaEnsure()
	case "validate":
		err = cmdSchemaValidate()
	case "status":
		err = cmdSchemaStatus()
	case "--help", "-h", "help":
		printSchemaUsage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown schema subcommand %q (want: ensure, validate, status)\n", args[0])
		printSchemaUsage(os.Stderr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "schema:", err)
		os.Exit(1)
	}
}

func printSchemaUsage(w *os.File) {
	fmt.Fprintln(w, "usage: jetmon2 schema <ensure|validate|status>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage or validate the Jetmon v2 database schema.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  ensure    apply or validate schema based on SCHEMA_MANAGEMENT_MODE")
	fmt.Fprintln(w, "  validate  fail unless required tables, columns, and indexes exist")
	fmt.Fprintln(w, "  status    print schema contract status and best-effort migration ledger status")
}

func cmdSchemaEnsure() error {
	configPath := envOrDefault("JETMON_CONFIG", "config/config.json")
	if err := config.Load(configPath); err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg := config.Get()
	config.LoadDB()
	if err := db.ConnectWithRetry(5); err != nil {
		return fmt.Errorf("db connect: %w", err)
	}
	mode := config.SchemaManagementModeMigrate
	if cfg != nil {
		mode = strings.TrimSpace(cfg.SchemaManagementMode)
	}
	fmt.Printf("INFO schema_management=%s\n", mode)
	switch mode {
	case config.SchemaManagementModeMigrate:
		if err := db.Migrate(); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	case config.SchemaManagementModeValidate:
		// Read-only path. Fall through to the validation below.
	default:
		return fmt.Errorf("SCHEMA_MANAGEMENT_MODE must be one of: migrate, validate")
	}
	status, err := db.ValidateSchema(context.Background())
	printSchemaContractStatus(status)
	if err != nil {
		return err
	}
	fmt.Println("PASS schema validation")
	return nil
}

func cmdSchemaValidate() error {
	if _, err := loadConfigForCommand(); err != nil {
		return err
	}
	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		return fmt.Errorf("db connect: %w", err)
	}
	status, err := db.ValidateSchema(context.Background())
	printSchemaContractStatus(status)
	if err != nil {
		return err
	}
	fmt.Println("PASS schema validation")
	return nil
}

func cmdSchemaStatus() error {
	if _, err := loadConfigForCommand(); err != nil {
		return err
	}
	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		return fmt.Errorf("db connect: %w", err)
	}
	contractStatus, contractErr := db.SchemaContract(context.Background())
	printSchemaContractStatus(contractStatus)
	migrationStatus, migrationErr := db.SchemaMigrationStatus(context.Background())
	if migrationErr != nil {
		fmt.Printf("WARN schema_migrations unavailable=%q\n", migrationErr.Error())
	} else {
		printSchemaMigrationStatus(migrationStatus)
	}
	return contractErr
}

func printSchemaContractStatus(status db.SchemaContractStatus) {
	fmt.Printf("INFO schema_contract %s\n", status.Summary())
	if len(status.MissingTables) > 0 {
		fmt.Printf("FAIL schema_missing_tables names=%v\n", status.MissingTables)
	}
	if len(status.MissingColumns) > 0 {
		fmt.Printf("FAIL schema_missing_columns count=%d examples=%v\n", len(status.MissingColumns), schemaIssueExamples(status.MissingColumns))
	}
	if len(status.MissingIndexes) > 0 {
		fmt.Printf("FAIL schema_missing_indexes count=%d examples=%v\n", len(status.MissingIndexes), schemaIssueExamples(status.MissingIndexes))
	}
}

func printSchemaMigrationStatus(status db.MigrationStatus) {
	fmt.Printf("INFO schema_migrations current=%d expected=%d applied=%d expected_count=%d\n",
		status.CurrentMaxID, status.ExpectedMaxID, status.AppliedCount, status.ExpectedCount)
	if len(status.PendingIDs) > 0 {
		fmt.Printf("FAIL schema_pending ids=%v\n", status.PendingIDs)
	}
	if len(status.UnknownIDs) > 0 {
		fmt.Printf("FAIL schema_unknown ids=%v\n", status.UnknownIDs)
	}
}

func schemaIssueExamples(issues []db.SchemaObjectIssue) []string {
	limit := 8
	if len(issues) < limit {
		limit = len(issues)
	}
	out := make([]string, 0, limit)
	for _, issue := range issues[:limit] {
		out = append(out, issue.Table+"."+issue.Name)
	}
	return out
}

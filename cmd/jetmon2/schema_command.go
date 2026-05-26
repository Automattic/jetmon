package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

const defaultSchemaBaselinePath = "migrations/production-v2-baseline.sql"

func cmdSchema(args []string) {
	if len(args) == 0 {
		printSchemaUsage(os.Stderr)
		os.Exit(1)
	}
	var err error
	switch args[0] {
	case "ensure":
		err = cmdSchemaEnsure()
	case "diff":
		err = cmdSchemaReconcile(args[1:], false)
	case "reconcile":
		err = cmdSchemaReconcile(args[1:], true)
	case "validate":
		err = cmdSchemaValidate()
	case "status":
		err = cmdSchemaStatus()
	case "--help", "-h", "help":
		printSchemaUsage(os.Stdout)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown schema subcommand %q (want: ensure, diff, reconcile, validate, status)\n", args[0])
		printSchemaUsage(os.Stderr)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "schema:", err)
		os.Exit(1)
	}
}

func printSchemaUsage(w *os.File) {
	fmt.Fprintln(w, "usage: jetmon2 schema <ensure|diff|reconcile|validate|status>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Manage or validate the Jetmon v2 database schema.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  ensure     reconcile or validate schema based on SCHEMA_MANAGEMENT_MODE")
	fmt.Fprintln(w, "  diff       print additive DDL needed to satisfy the schema contract")
	fmt.Fprintln(w, "  reconcile  print additive DDL, or apply it with --execute")
	fmt.Fprintln(w, "  validate   fail unless required tables, columns, and indexes exist")
	fmt.Fprintln(w, "  status     print schema contract status and legacy local/lab ledger status")
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
	mode := config.SchemaManagementModeValidate
	if cfg != nil {
		mode = strings.TrimSpace(cfg.SchemaManagementMode)
	}
	fmt.Printf("INFO schema_management=%s\n", mode)
	switch mode {
	case config.SchemaManagementModeMigrate:
		if err := refuseProductionSchemaReconcile(cfg); err != nil {
			return err
		}
		baselineSQL, err := loadSchemaBaseline(defaultSchemaBaselinePath)
		if err != nil {
			return err
		}
		plan, err := db.BuildSchemaReconcilePlan(context.Background(), baselineSQL)
		if err != nil {
			return err
		}
		printSchemaReconcilePlan(plan, "text")
		if err := db.ApplySchemaReconcilePlan(context.Background(), plan); err != nil {
			return err
		}
		fmt.Printf("PASS schema reconcile applied statements=%d\n", len(plan.Statements))
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

func cmdSchemaReconcile(args []string, allowExecute bool) error {
	flags := flag.NewFlagSet("schema reconcile", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	baselinePath := flags.String("baseline-file", defaultSchemaBaselinePath, "baseline DDL file")
	output := flags.String("output", "text", "output format: text or sql")
	execute := flags.Bool("execute", false, "apply additive DDL after printing the plan")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !allowExecute && *execute {
		return fmt.Errorf("schema diff is read-only; use schema reconcile --execute to apply additive DDL")
	}
	if *output != "text" && *output != "sql" {
		return fmt.Errorf("--output must be text or sql")
	}
	cfg, err := loadConfigForCommand()
	if err != nil {
		return err
	}
	if *execute {
		if err := refuseProductionSchemaReconcile(cfg); err != nil {
			return err
		}
	}
	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		return fmt.Errorf("db connect: %w", err)
	}
	baselineSQL, err := loadSchemaBaseline(*baselinePath)
	if err != nil {
		return err
	}
	plan, err := db.BuildSchemaReconcilePlan(context.Background(), baselineSQL)
	if err != nil {
		return err
	}
	printSchemaReconcilePlan(plan, *output)
	if !*execute {
		if *output == "text" {
			fmt.Printf("INFO dry_run=true statements=%d unresolved=%d\n", len(plan.Statements), len(plan.Unresolved))
		}
		return nil
	}
	if err := db.ApplySchemaReconcilePlan(context.Background(), plan); err != nil {
		return err
	}
	status, err := db.ValidateSchema(context.Background())
	if *output == "text" {
		printSchemaContractStatus(status)
	}
	if err != nil {
		return err
	}
	if *output == "text" {
		fmt.Printf("PASS schema reconcile applied statements=%d\n", len(plan.Statements))
	}
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

func loadSchemaBaseline(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read schema baseline %q: %w", path, err)
	}
	return string(data), nil
}

func refuseProductionSchemaReconcile(cfg *config.Config) error {
	if cfg != nil && cfg.ConfigProfile == config.ConfigProfileProduction {
		return fmt.Errorf("SCHEMA_MANAGEMENT_MODE=migrate is disabled for CONFIG_PROFILE=production; use validate and apply reviewed SQL externally")
	}
	return nil
}

func printSchemaReconcilePlan(plan db.SchemaReconcilePlan, output string) {
	if output == "sql" {
		for _, stmt := range plan.Statements {
			fmt.Println(stmt.SQL)
		}
		return
	}
	printSchemaContractStatus(plan.Status)
	if len(plan.Statements) == 0 && len(plan.Unresolved) == 0 {
		fmt.Println("PASS schema reconcile already satisfied")
		return
	}
	fmt.Printf("INFO schema_reconcile statements=%d unresolved=%d\n", len(plan.Statements), len(plan.Unresolved))
	for _, stmt := range plan.Statements {
		fmt.Printf("PLAN %s table=%s name=%s\n", stmt.Kind, stmt.Table, stmt.Name)
		fmt.Printf("SQL %s\n", stmt.SQL)
	}
	for _, issue := range plan.Unresolved {
		fmt.Printf("WARN schema_reconcile_unresolved table=%s name=%s\n", issue.Table, issue.Name)
	}
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

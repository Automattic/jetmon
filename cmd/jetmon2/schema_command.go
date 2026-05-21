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
	fmt.Fprintln(w, "  validate  fail unless all embedded migrations are already applied")
	fmt.Fprintln(w, "  status    print current and expected migration status")
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
	printSchemaStatus(status)
	if err != nil {
		return err
	}
	fmt.Println("PASS schema validation")
	return nil
}

func cmdSchemaValidate() error {
	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		return fmt.Errorf("db connect: %w", err)
	}
	status, err := db.ValidateSchema(context.Background())
	printSchemaStatus(status)
	if err != nil {
		return err
	}
	fmt.Println("PASS schema validation")
	return nil
}

func cmdSchemaStatus() error {
	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		return fmt.Errorf("db connect: %w", err)
	}
	status, err := db.SchemaMigrationStatus(context.Background())
	printSchemaStatus(status)
	return err
}

func printSchemaStatus(status db.MigrationStatus) {
	fmt.Printf("INFO schema_migrations current=%d expected=%d applied=%d expected_count=%d\n",
		status.CurrentMaxID, status.ExpectedMaxID, status.AppliedCount, status.ExpectedCount)
	if len(status.PendingIDs) > 0 {
		fmt.Printf("FAIL schema_pending ids=%v\n", status.PendingIDs)
	}
	if len(status.UnknownIDs) > 0 {
		fmt.Printf("FAIL schema_unknown ids=%v\n", status.UnknownIDs)
	}
}

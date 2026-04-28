package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/Automattic/jetmon/internal/config"
	"github.com/Automattic/jetmon/internal/db"
)

type siteTenantImport struct {
	Mappings         []db.SiteTenantMapping
	SkippedDuplicate int
}

func cmdSiteTenants(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 site-tenants <import> [args]")
		os.Exit(1)
	}

	switch args[0] {
	case "import":
		cmdSiteTenantsImport(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown site-tenants subcommand %q (want: import)\n", args[0])
		os.Exit(1)
	}
}

func cmdSiteTenantsImport(args []string) {
	fs := flag.NewFlagSet("site-tenants import", flag.ExitOnError)
	path := fs.String("file", "", "CSV file with tenant_id,blog_id rows; use - for stdin")
	source := fs.String("source", "gateway", "mapping source label")
	dryRun := fs.Bool("dry-run", false, "parse and validate input without writing")
	_ = fs.Parse(args)

	if strings.TrimSpace(*path) == "" {
		fmt.Fprintln(os.Stderr, "usage: jetmon2 site-tenants import --file <path|-> [--source=gateway] [--dry-run]")
		os.Exit(1)
	}

	rc, err := openSiteTenantImport(*path)
	if err != nil {
		log.Fatalf("open import file: %v", err)
	}
	defer rc.Close()

	in, err := parseSiteTenantMappings(rc)
	if err != nil {
		log.Fatalf("parse import file: %v", err)
	}

	if *dryRun {
		fmt.Printf("Validated %d site tenant mappings", len(in.Mappings))
		if in.SkippedDuplicate > 0 {
			fmt.Printf(" (%d duplicate rows skipped)", in.SkippedDuplicate)
		}
		fmt.Println()
		return
	}

	config.LoadDB()
	if err := db.ConnectWithRetry(3); err != nil {
		log.Fatalf("db: %v", err)
	}
	affected, err := db.UpsertSiteTenantMappings(context.Background(), db.DB(), in.Mappings, *source)
	if err != nil {
		log.Fatalf("import: %v", err)
	}

	fmt.Printf("Imported %d site tenant mappings", len(in.Mappings))
	if in.SkippedDuplicate > 0 {
		fmt.Printf(" (%d duplicate rows skipped)", in.SkippedDuplicate)
	}
	fmt.Printf("; database rows affected=%d\n", affected)
}

func openSiteTenantImport(path string) (io.ReadCloser, error) {
	if strings.TrimSpace(path) == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(path)
}

func parseSiteTenantMappings(r io.Reader) (siteTenantImport, error) {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	out := siteTenantImport{}
	seen := make(map[db.SiteTenantMapping]struct{})
	line := 0
	sawData := false
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			return out, err
		}
		if emptyCSVRecord(record) {
			continue
		}
		if !sawData && isSiteTenantHeader(record) {
			sawData = true
			continue
		}
		sawData = true
		if len(record) != 2 {
			return out, fmt.Errorf("line %d: expected 2 columns tenant_id,blog_id; got %d", line, len(record))
		}

		tenantID := strings.TrimSpace(record[0])
		if tenantID == "" {
			return out, fmt.Errorf("line %d: tenant_id is required", line)
		}
		blogID, err := strconv.ParseInt(strings.TrimSpace(record[1]), 10, 64)
		if err != nil || blogID <= 0 {
			return out, fmt.Errorf("line %d: blog_id must be a positive integer", line)
		}

		mapping := db.SiteTenantMapping{TenantID: tenantID, BlogID: blogID}
		if _, ok := seen[mapping]; ok {
			out.SkippedDuplicate++
			continue
		}
		seen[mapping] = struct{}{}
		out.Mappings = append(out.Mappings, mapping)
	}

	if len(out.Mappings) == 0 {
		return out, errors.New("no site tenant mappings found")
	}
	return out, nil
}

func isSiteTenantHeader(record []string) bool {
	if len(record) != 2 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(record[0]), "tenant_id") &&
		strings.EqualFold(strings.TrimSpace(record[1]), "blog_id")
}

func emptyCSVRecord(record []string) bool {
	for _, field := range record {
		if strings.TrimSpace(field) != "" {
			return false
		}
	}
	return true
}

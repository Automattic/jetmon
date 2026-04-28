package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// SiteTenantMapping links one gateway/customer tenant to one monitored site.
// The mapping is many-to-many so gateway-side shared ownership or delegated
// access does not require changing the legacy site row.
type SiteTenantMapping struct {
	TenantID string
	BlogID   int64
}

// UpsertSiteTenantMappings inserts or refreshes site tenant mappings from a
// gateway-owned source of truth. It intentionally does not delete mappings;
// pruning requires a source-specific reconciliation policy.
func UpsertSiteTenantMappings(ctx context.Context, conn *sql.DB, mappings []SiteTenantMapping, source string) (int64, error) {
	if conn == nil {
		return 0, errors.New("db is nil")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "gateway"
	}
	if len(mappings) == 0 {
		return 0, nil
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin site tenant import: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO jetmon_site_tenants (tenant_id, blog_id, source)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE
			source = VALUES(source),
			updated_at = CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, fmt.Errorf("prepare site tenant import: %w", err)
	}
	defer stmt.Close()

	var affected int64
	for _, m := range mappings {
		tenantID := strings.TrimSpace(m.TenantID)
		if tenantID == "" {
			return 0, errors.New("tenant id is required")
		}
		if m.BlogID <= 0 {
			return 0, fmt.Errorf("blog id must be positive for tenant %q", tenantID)
		}
		res, err := stmt.ExecContext(ctx, tenantID, m.BlogID, source)
		if err != nil {
			return 0, fmt.Errorf("upsert site tenant mapping tenant=%q blog_id=%d: %w", tenantID, m.BlogID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("read site tenant import result: %w", err)
		}
		affected += n
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit site tenant import: %w", err)
	}
	return affected, nil
}

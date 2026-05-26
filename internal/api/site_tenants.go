package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
)

const insertSiteTenantSQL = `
		INSERT INTO jetpack_monitor_site_tenants (tenant_id, blog_id, source)
		VALUES (?, ?, 'gateway')
		ON DUPLICATE KEY UPDATE updated_at = CURRENT_TIMESTAMP`

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Server) assignSiteTenant(ctx context.Context, exec sqlExecer, blogID int64, tenantID string) error {
	if tenantID == "" {
		return errors.New("tenant id is required")
	}
	if _, err := exec.ExecContext(ctx, insertSiteTenantSQL, tenantID, blogID); err != nil {
		return fmt.Errorf("assign site tenant: %w", err)
	}
	return nil
}

func (s *Server) siteVisibleToRequest(ctx context.Context, r *http.Request, blogID int64) (bool, error) {
	tenantID, ok := ownerTenantIDFromRequest(r)
	if !ok {
		return true, nil
	}
	var dummy int64
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM jetpack_monitor_site_tenants WHERE tenant_id = ? AND blog_id = ? LIMIT 1`,
		tenantID, blogID,
	).Scan(&dummy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Server) blogIDForMonitorSite(ctx context.Context, monitorSiteID int64) (int64, error) {
	var blogID int64
	err := s.db.QueryRowContext(ctx,
		`SELECT blog_id FROM jetpack_monitor_sites WHERE jetpack_monitor_site_id = ? LIMIT 1`,
		monitorSiteID,
	).Scan(&blogID)
	if err != nil {
		return 0, err
	}
	return blogID, nil
}

func (s *Server) ensureMonitorSiteVisibleForRequest(w http.ResponseWriter, r *http.Request, monitorSiteID int64) (int64, bool) {
	blogID, err := s.blogIDForMonitorSite(r.Context(), monitorSiteID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeSiteNotFound(w, r, monitorSiteID)
			return 0, false
		}
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site lookup failed: "+err.Error())
		return 0, false
	}
	if !s.ensureSiteVisibleForRequest(w, r, blogID) {
		return 0, false
	}
	return blogID, true
}

func (s *Server) ensureSiteVisibleForRequest(w http.ResponseWriter, r *http.Request, blogID int64) bool {
	ok, err := s.siteVisibleToRequest(r.Context(), r, blogID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "db_error",
			"site tenant lookup failed: "+err.Error())
		return false
	}
	if !ok {
		writeSiteNotFound(w, r, blogID)
		return false
	}
	return true
}

func writeSiteNotFound(w http.ResponseWriter, r *http.Request, siteID int64) {
	writeError(w, r, http.StatusNotFound, "site_not_found",
		fmt.Sprintf("Site %d does not exist", siteID))
}

func writeEventNotFound(w http.ResponseWriter, r *http.Request, eventID int64) {
	writeError(w, r, http.StatusNotFound, "event_not_found",
		fmt.Sprintf("Event %d does not exist", eventID))
}

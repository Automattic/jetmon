package dashboard

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/db"
)

type clientRateState struct {
	tokens     float64
	lastRefill time.Time
}

type apiErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type apiSuccessEnvelope struct {
	Data any `json:"data"`
}

type createSiteRequest struct {
	BlogID        int64  `json:"blog_id"`
	MonitorURL    string `json:"monitor_url"`
	MonitorActive *bool  `json:"monitor_active,omitempty"`
	SiteStatus    *int   `json:"site_status,omitempty"`
	CheckInterval *int   `json:"check_interval,omitempty"`
}

type patchSiteRequest struct {
	MonitorActive        *bool   `json:"monitor_active,omitempty"`
	SiteStatus           *int    `json:"site_status,omitempty"`
	CheckInterval        *int    `json:"check_interval,omitempty"`
	TimeoutSeconds       *int    `json:"timeout_seconds,omitempty"`
	RedirectPolicy       *string `json:"redirect_policy,omitempty"`
	CheckKeyword         *string `json:"check_keyword,omitempty"`
	AlertCooldownMinutes *int    `json:"alert_cooldown_minutes,omitempty"`
	MaintenanceStart     *string `json:"maintenance_start,omitempty"`
	MaintenanceEnd       *string `json:"maintenance_end,omitempty"`
	CustomHeaders        *string `json:"custom_headers,omitempty"`
}

func (s *Server) apiMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthorized(r) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		if !s.allowRequest(r) {
			writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAPIV1(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	if path == "" || path == "/" {
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}

	if path == "/sites" {
		s.handleSitesCollection(w, r)
		return
	}

	if strings.HasPrefix(path, "/sites/") {
		s.handleSiteResource(w, r, strings.TrimPrefix(path, "/sites/"))
		return
	}

	writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found")
}

func (s *Server) handleSitesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sites, err := s.listSites(r)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to list sites")
			return
		}
		writeAPISuccess(w, http.StatusOK, mapSitesForAPI(sites))
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, s.maxRequestBodyBytes)
		defer r.Body.Close()

		var req createSiteRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
		if req.BlogID <= 0 || strings.TrimSpace(req.MonitorURL) == "" {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "blog_id and monitor_url are required")
			return
		}

		monitorActive := true
		if req.MonitorActive != nil {
			monitorActive = *req.MonitorActive
		}
		siteStatus := 1
		if req.SiteStatus != nil {
			siteStatus = *req.SiteStatus
		}
		checkInterval := 5
		if req.CheckInterval != nil {
			checkInterval = *req.CheckInterval
		}

		id, err := s.createSite(r, db.CreateSiteInput{
			BlogID:        req.BlogID,
			BucketNo:      int(req.BlogID % int64(s.bucketTotal)),
			MonitorURL:    strings.TrimSpace(req.MonitorURL),
			MonitorActive: monitorActive,
			SiteStatus:    siteStatus,
			CheckInterval: checkInterval,
		})
		if err != nil {
			if errors.Is(err, db.ErrDuplicateSite) {
				writeAPIError(w, http.StatusConflict, "duplicate_site", "site already exists for this blog_id and monitor_url")
				return
			}
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to create site")
			return
		}

		site, err := s.getSiteByID(r, id)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "created site could not be loaded")
			return
		}
		writeAPISuccess(w, http.StatusCreated, mapSiteForAPI(site))
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleSiteResource(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid site id")
		return
	}

	if len(parts) == 2 && parts[1] == "events" {
		s.handleSiteEvents(w, r, id)
		return
	}
	if len(parts) != 1 {
		writeAPIError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		site, err := s.getSiteByID(r, id)
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "site not found")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load site")
			return
		}
		writeAPISuccess(w, http.StatusOK, mapSiteForAPI(site))
	case http.MethodPatch:
		r.Body = http.MaxBytesReader(w, r.Body, s.maxRequestBodyBytes)
		defer r.Body.Close()

		var raw map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid JSON request body")
			return
		}
		if _, blocked := raw["monitor_url"]; blocked {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "monitor_url cannot be patched")
			return
		}

		payloadBytes, err := json.Marshal(raw)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid patch payload")
			return
		}
		var req patchSiteRequest
		if err := json.Unmarshal(payloadBytes, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid patch payload")
			return
		}

		patchInput, err := buildPatchInput(req)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		found, err := s.patchSite(r, id, patchInput)
		if errors.Is(err, db.ErrNoPatchFields) {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "no patchable fields supplied")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to patch site")
			return
		}
		if !found {
			writeAPIError(w, http.StatusNotFound, "not_found", "site not found")
			return
		}

		site, err := s.getSiteByID(r, id)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load patched site")
			return
		}
		writeAPISuccess(w, http.StatusOK, mapSiteForAPI(site))
	case http.MethodDelete:
		deleted, err := s.deleteSite(r, id)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to delete site")
			return
		}
		if !deleted {
			writeAPIError(w, http.StatusNotFound, "not_found", "site not found")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPatch, http.MethodDelete)
	}
}

func (s *Server) handleSiteEvents(w http.ResponseWriter, r *http.Request, siteID int64) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	if _, err := s.getSiteByID(r, siteID); errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "site not found")
		return
	} else if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to load site")
		return
	}

	limit, offset := parseLimitOffset(r.URL.Query().Get("limit"), r.URL.Query().Get("offset"), 100, 500)
	events, err := s.listSiteEvents(r, siteID, limit, offset)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "failed to list site events")
		return
	}

	resp := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		resp = append(resp, map[string]any{
			"id":                      ev.ID,
			"jetpack_monitor_site_id": ev.JetpackMonitorSiteID,
			"event_type":              ev.EventType,
			"event_type_label":        db.EventTypeLabel(ev.EventType),
			"severity":                ev.Severity,
			"severity_label":          db.EventSeverityLabel(ev.Severity),
			"started_at":              ev.StartedAt,
			"ended_at":                ev.EndedAt,
			"created_at":              ev.CreatedAt,
			"is_recovered":            ev.EventType == db.EventTypeConfirmedDown && ev.EndedAt != nil,
		})
	}
	writeAPISuccess(w, http.StatusOK, resp)
}

func parseListSitesParams(r *http.Request) db.ListSitesParams {
	limit, offset := parseLimitOffset(r.URL.Query().Get("limit"), r.URL.Query().Get("offset"), 100, 500)
	params := db.ListSitesParams{Limit: limit, Offset: offset}

	if v := r.URL.Query().Get("blog_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			params.BlogID = &n
		}
	}
	if v := r.URL.Query().Get("site_status"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			params.SiteStatus = &n
		}
	}
	if v := r.URL.Query().Get("monitor_active"); v != "" {
		if b, ok := parseBoolQuery(v); ok {
			params.MonitorActive = &b
		}
	}
	if v := r.URL.Query().Get("bucket_no"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			params.BucketNo = &n
		}
	}
	return params
}

func buildPatchInput(req patchSiteRequest) (db.PatchSiteInput, error) {
	input := db.PatchSiteInput{
		MonitorActive:        req.MonitorActive,
		SiteStatus:           req.SiteStatus,
		CheckInterval:        req.CheckInterval,
		TimeoutSeconds:       req.TimeoutSeconds,
		RedirectPolicy:       req.RedirectPolicy,
		CheckKeyword:         req.CheckKeyword,
		AlertCooldownMinutes: req.AlertCooldownMinutes,
		CustomHeaders:        req.CustomHeaders,
	}

	if req.MaintenanceStart != nil {
		ts, err := time.Parse(time.RFC3339, *req.MaintenanceStart)
		if err != nil {
			return db.PatchSiteInput{}, fmt.Errorf("maintenance_start must be RFC3339")
		}
		input.MaintenanceStart = &ts
	}
	if req.MaintenanceEnd != nil {
		ts, err := time.Parse(time.RFC3339, *req.MaintenanceEnd)
		if err != nil {
			return db.PatchSiteInput{}, fmt.Errorf("maintenance_end must be RFC3339")
		}
		input.MaintenanceEnd = &ts
	}

	return input, nil
}

func mapSitesForAPI(sites []db.Site) []map[string]any {
	out := make([]map[string]any, 0, len(sites))
	for _, site := range sites {
		out = append(out, mapSiteForAPI(site))
	}
	return out
}

func mapSiteForAPI(site db.Site) map[string]any {
	return map[string]any{
		"jetpack_monitor_site_id": site.ID,
		"blog_id":                 site.BlogID,
		"bucket_no":               site.BucketNo,
		"monitor_url":             site.MonitorURL,
		"monitor_active":          site.MonitorActive,
		"site_status":             site.SiteStatus,
		"last_status_change":      site.LastStatusChange,
		"check_interval":          site.CheckInterval,
		"last_checked_at":         site.LastCheckedAt,
		"ssl_expiry_date":         site.SSLExpiryDate,
		"check_keyword":           site.CheckKeyword,
		"maintenance_start":       site.MaintenanceStart,
		"maintenance_end":         site.MaintenanceEnd,
		"custom_headers":          site.CustomHeaders,
		"timeout_seconds":         site.TimeoutSeconds,
		"redirect_policy":         site.RedirectPolicy,
		"alert_cooldown_minutes":  site.AlertCooldownMinutes,
		"last_alert_sent_at":      site.LastAlertSentAt,
	}
}

func writeAPISuccess(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiSuccessEnvelope{Data: data})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := apiErrorEnvelope{}
	resp.Error.Code = code
	resp.Error.Message = message
	_ = json.NewEncoder(w).Encode(resp)
}

func writeMethodNotAllowed(w http.ResponseWriter, allow ...string) {
	w.Header().Set("Allow", strings.Join(allow, ", "))
	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func parseLimitOffset(limitStr, offsetStr string, defaultLimit, maxLimit int) (int, int) {
	limit := defaultLimit
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
		if n > maxLimit {
			n = maxLimit
		}
		limit = n
	}
	offset := 0
	if n, err := strconv.Atoi(offsetStr); err == nil && n >= 0 {
		offset = n
	}
	return limit, offset
}

func parseBoolQuery(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true, true
	case "0", "false", "no":
		return false, true
	default:
		return false, false
	}
}

func (s *Server) isAuthorized(r *http.Request) bool {
	if len(s.apiTokens) == 0 {
		return true
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	if token == "" {
		return false
	}
	_, ok := s.apiTokens[token]
	return ok
}

func (s *Server) allowRequest(r *http.Request) bool {
	ip := requestIP(r)
	now := time.Now()

	s.apiRateMu.Lock()
	defer s.apiRateMu.Unlock()

	st, ok := s.apiRateStates[ip]
	if !ok {
		s.apiRateStates[ip] = &clientRateState{
			tokens:     s.apiRateLimitBurst - 1,
			lastRefill: now,
		}
		return true
	}

	elapsed := now.Sub(st.lastRefill).Seconds()
	st.tokens += elapsed * s.apiRateLimitRPS
	if st.tokens > s.apiRateLimitBurst {
		st.tokens = s.apiRateLimitBurst
	}
	st.lastRefill = now

	if st.tokens < 1 {
		return false
	}
	st.tokens -= 1
	return true
}

func requestIP(r *http.Request) string {
	xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Automattic/jetmon/internal/apikeys"
	"github.com/Automattic/jetmon/internal/audit"
)

// Scope aliases for handler ergonomics. The api package speaks in apikeys.Scope
// internally but routes use these constants for brevity.
const (
	scopeRead  = apikeys.ScopeRead
	scopeWrite = apikeys.ScopeWrite
)

// ctxKey is an unexported type so handlers from other packages can't trample
// our request-scoped state.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyAPIKey
	ctxKeyGatewayContext
)

const (
	gatewayConsumerName      = "gateway"
	headerTenantID           = "X-Jetmon-Tenant-ID"
	headerActorID            = "X-Jetmon-Actor-ID"
	headerPublicScopes       = "X-Jetmon-Public-Scopes"
	headerGatewayRequestID   = "X-Jetmon-Gateway-Request-ID"
	headerGatewayPlan        = "X-Jetmon-Plan"
	errForbiddenGatewayCtx   = "forbidden_gateway_context"
	errInvalidGatewayContext = "invalid_gateway_context"
)

// gatewayContext is the trusted customer context asserted by the public API
// gateway after it has authenticated and authorized the caller.
type gatewayContext struct {
	TenantID         string
	ActorID          string
	PublicScopes     []string
	GatewayRequestID string
	Plan             string
}

// keyFromRequest returns the authenticated key for r, or nil if the request
// hasn't been through the auth middleware.
func keyFromRequest(r *http.Request) *apikeys.Key {
	k, _ := r.Context().Value(ctxKeyAPIKey).(*apikeys.Key)
	return k
}

func gatewayContextFromRequest(r *http.Request) (*gatewayContext, bool) {
	gw, ok := r.Context().Value(ctxKeyGatewayContext).(*gatewayContext)
	if !ok || gw == nil {
		return nil, false
	}
	return gw, true
}

func ownerTenantIDFromRequest(r *http.Request) (string, bool) {
	gw, ok := gatewayContextFromRequest(r)
	if !ok {
		return "", false
	}
	return gw.TenantID, true
}

// requestIDFromRequest returns the request id assigned by the middleware.
// Always non-empty — middleware ensures a value is set before the handler runs.
func requestIDFromRequest(r *http.Request) string {
	id, _ := r.Context().Value(ctxKeyRequestID).(string)
	return id
}

// requireScope returns an http.HandlerFunc that:
//  1. assigns a request id (echoed in headers and used in error responses),
//  2. parses the Bearer token,
//  3. resolves it to a Key via apikeys.Lookup,
//  4. enforces the required scope,
//  5. logs the access to jetmon_audit_log on the way out,
//  6. invokes the wrapped handler.
//
// Internal API quirks: 401 vs 403 is honest (no 404-disguised-as-403), and
// error messages name the resource type so consumers debugging integrations
// can tell at a glance what went wrong.
func (s *Server) requireScope(required apikeys.Scope, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := newRequestID()
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, reqID)
		req := r.WithContext(ctx)
		w.Header().Set("X-Request-ID", reqID)

		token := bearerToken(r)
		if token == "" {
			writeError(w, req, http.StatusUnauthorized, "missing_token",
				"Authorization header with Bearer token is required")
			s.audit(reqID, nil, req, http.StatusUnauthorized, time.Time{}, "missing token")
			return
		}

		key, err := apikeys.Lookup(ctx, s.db, token)
		if err != nil {
			status, code, msg := mapAuthError(err)
			writeError(w, req, status, code, msg)
			s.audit(reqID, nil, req, status, time.Time{}, code)
			return
		}

		if !key.Scope.Includes(required) {
			writeError(w, req, http.StatusForbidden, "insufficient_scope",
				"this endpoint requires scope "+string(required)+
					"; your key has scope "+string(key.Scope))
			s.audit(reqID, key, req, http.StatusForbidden, time.Time{}, "insufficient scope")
			return
		}

		// Rate limit per key.
		allowed, remaining, resetAt := s.limiter.allow(key.ID, key.RateLimitPerMinute)
		writeRateLimitHeaders(w, key.RateLimitPerMinute, remaining, resetAt)
		if !allowed {
			writeRateLimited(w, req, key.RateLimitPerMinute, remaining, resetAt)
			s.audit(reqID, key, req, http.StatusTooManyRequests, time.Time{}, "rate limited")
			return
		}

		ctx = context.WithValue(ctx, ctxKeyAPIKey, key)
		gw, status, code, msg := parseGatewayContext(r, key)
		if status != 0 {
			req = r.WithContext(ctx)
			writeError(w, req, status, code, msg)
			s.audit(reqID, key, req, status, time.Time{}, code)
			return
		}
		if gw != nil {
			ctx = context.WithValue(ctx, ctxKeyGatewayContext, gw)
		}
		req = r.WithContext(ctx)
		started := time.Now()

		// We wrap the response writer so we can capture the final status code
		// for the audit log. Default to 200 if the handler doesn't write
		// explicitly (Go's http.ResponseWriter implicitly flushes 200 on first
		// body write).
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, req)

		s.audit(reqID, key, req, rec.status, started, "")
	}
}

func parseGatewayContext(r *http.Request, key *apikeys.Key) (*gatewayContext, int, string, string) {
	hasContext := false
	for _, h := range []string{
		headerTenantID,
		headerActorID,
		headerPublicScopes,
		headerGatewayRequestID,
		headerGatewayPlan,
	} {
		if r.Header.Get(h) != "" {
			hasContext = true
			break
		}
	}
	if !hasContext {
		return nil, 0, "", ""
	}

	if key == nil || key.ConsumerName != gatewayConsumerName {
		return nil, http.StatusForbidden, errForbiddenGatewayCtx,
			"gateway tenant context headers are only honored for the gateway API consumer"
	}

	tenantID := strings.TrimSpace(r.Header.Get(headerTenantID))
	if tenantID == "" {
		return nil, http.StatusBadRequest, errInvalidGatewayContext,
			headerTenantID + " is required when gateway context headers are present"
	}
	gatewayRequestID := strings.TrimSpace(r.Header.Get(headerGatewayRequestID))
	if gatewayRequestID == "" {
		return nil, http.StatusBadRequest, errInvalidGatewayContext,
			headerGatewayRequestID + " is required when gateway context headers are present"
	}
	publicScopes := strings.Fields(r.Header.Get(headerPublicScopes))
	if len(publicScopes) == 0 {
		return nil, http.StatusBadRequest, errInvalidGatewayContext,
			headerPublicScopes + " is required when gateway context headers are present"
	}

	return &gatewayContext{
		TenantID:         tenantID,
		ActorID:          strings.TrimSpace(r.Header.Get(headerActorID)),
		PublicScopes:     publicScopes,
		GatewayRequestID: gatewayRequestID,
		Plan:             strings.TrimSpace(r.Header.Get(headerGatewayPlan)),
	}, 0, "", ""
}

// statusRecorder wraps an http.ResponseWriter to expose the final status code
// after the handler returns. We need this for audit logging — Go's stdlib
// doesn't expose the status code post-write.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

// Flush passes through to the underlying writer if it supports it (SSE,
// streaming responses).
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// bearerToken extracts the token from "Authorization: Bearer <token>", or
// returns "" if the header is missing or malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func mapAuthError(err error) (status int, code, msg string) {
	switch {
	case errors.Is(err, apikeys.ErrInvalidToken):
		return http.StatusUnauthorized, "invalid_token", "the provided token is invalid"
	case errors.Is(err, apikeys.ErrKeyRevoked):
		return http.StatusUnauthorized, "token_revoked", "the provided token has been revoked"
	case errors.Is(err, apikeys.ErrKeyExpired):
		return http.StatusUnauthorized, "token_expired", "the provided token has expired"
	default:
		return http.StatusInternalServerError, "auth_error", "internal error during authentication: " + err.Error()
	}
}

// audit writes the request to jetmon_audit_log. Done synchronously today;
// could be moved to a buffered channel if write latency becomes a concern.
// Errors are logged but never returned to the consumer — audit is observability,
// not gate.
//
// reqID is passed explicitly rather than pulled from r's context so callers
// can't accidentally drop it by handing in a request whose context wasn't
// extended with the middleware's request id.
func (s *Server) audit(reqID string, key *apikeys.Key, r *http.Request, status int, started time.Time, note string) {
	consumerName := "unknown"
	var keyID int64
	if key != nil {
		consumerName = key.ConsumerName
		keyID = key.ID
	}

	durationMs := int64(0)
	if !started.IsZero() {
		durationMs = time.Since(started).Milliseconds()
	}

	metaMap := map[string]any{
		"key_id":      keyID,
		"consumer":    consumerName,
		"method":      r.Method,
		"path":        r.URL.Path,
		"status":      status,
		"duration_ms": durationMs,
		"request_id":  reqID,
		"remote_addr": r.RemoteAddr,
		"note":        note,
	}
	if gw, ok := gatewayContextFromRequest(r); ok {
		metaMap["tenant_id"] = gw.TenantID
		metaMap["actor_id"] = gw.ActorID
		metaMap["public_scopes"] = gw.PublicScopes
		metaMap["gateway_request_id"] = gw.GatewayRequestID
		metaMap["plan"] = gw.Plan
	}
	meta, _ := json.Marshal(metaMap)

	// Derive the audit context from Background, not r.Context(): a client
	// disconnect must not silence the audit row, since audit is for the
	// operator, not the caller. The timeout caps any wedged-DB hang.
	ctx, cancel := context.WithTimeout(context.Background(), auditWriteTimeout)
	defer cancel()
	if err := audit.Log(ctx, audit.Entry{
		EventType: audit.EventAPIAccess,
		Source:    consumerName,
		Detail:    r.Method + " " + r.URL.Path,
		Metadata:  meta,
	}); err != nil {
		log.Printf("api: audit log failed: %v", err)
	}
}

// auditWriteTimeout caps a single audit insert so a wedged DB cannot block
// the request goroutine indefinitely. Audit is observability, not gate; if
// the write times out we log and move on.
const auditWriteTimeout = 5 * time.Second

// newRequestID returns a 16-byte random hex id (32 chars). Same shape as the
// verifier's NewRequestID for consistency in operator log-greppage.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to a timestamp; collisions are non-load-bearing here.
		return "ts-" + time.Now().UTC().Format("20060102T150405.000")
	}
	return hex.EncodeToString(b[:])
}

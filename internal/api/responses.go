package api

import (
	"encoding/json"
	"log"
	"net/http"
)

// ListEnvelope wraps every list response. Single-resource responses are
// returned as bare objects without an envelope. See docs/internal-api-reference.md "Response envelope".
type ListEnvelope struct {
	Data any  `json:"data"`
	Page Page `json:"page"`
}

// Page describes the cursor for the next page of a list response. Cursor is
// nil on the last page. Limit is the limit applied to *this* response (which
// may differ from the request if the server clamped it).
type Page struct {
	Next  *string `json:"next"`
	Limit int     `json:"limit"`
}

// errorEnvelope is the JSON shape returned on any non-2xx response. The
// `code` is a stable machine-readable identifier; `message` is for humans
// and may improve over time. `request_id` correlates with server logs.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// writeJSON serializes v as JSON and writes it with the given status code.
// Errors during marshaling are logged but produce a generic 500 to the
// consumer (we can't recover from a marshaling failure mid-response).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		log.Printf("api: response encode failed: %v", err)
	}
}

// writeError writes a structured error envelope. The request id is pulled
// from request context if available so consumers can correlate with server
// logs; the X-Request-ID header is also set if not already present.
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	reqID := requestIDFromRequest(r)
	if reqID != "" && w.Header().Get("X-Request-ID") == "" {
		w.Header().Set("X-Request-ID", reqID)
	}
	writeJSON(w, status, errorEnvelope{
		Error: errorBody{
			Code:      code,
			Message:   message,
			RequestID: reqID,
		},
	})
}

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeJSONBodyDecodeError(w, r, err)
		return false
	}
	return true
}

func decodeOptionalJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeJSONBodyDecodeError(w, r, err)
		return false
	}
	return true
}

func writeJSONBodyDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "request_body_too_large",
			"request body exceeds the maximum supported JSON payload size")
		return
	}
	writeError(w, r, http.StatusBadRequest, "invalid_body",
		"request body must be valid JSON: "+err.Error())
}

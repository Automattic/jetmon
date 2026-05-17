package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := decodeSingleJSONValue(json.NewDecoder(r.Body), dst); err != nil {
		writeJSONBodyDecodeError(w, r, err)
		return false
	}
	return true
}

func decodeOptionalJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := decodeSingleJSONValue(json.NewDecoder(r.Body), dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeJSONBodyDecodeError(w, r, err)
		return false
	}
	return true
}

func decodeSingleJSONValue(dec *json.Decoder, dst any) error {
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain a single JSON value")
		}
		return err
	}
	return nil
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

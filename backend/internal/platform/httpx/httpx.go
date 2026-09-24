// Package httpx holds the JSON request/response conventions every handler
// shares, so error bodies look the same across the whole API.
package httpx

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
)

// Error is the body of every non-2xx JSON response.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, Error{Code: code, Message: message})
}

// WriteInternal logs the real error server-side and returns a generic 500,
// so database details never leak to the client.
func WriteInternal(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	WriteError(w, http.StatusInternalServerError, "INTERNAL", "internal error")
}

const maxBodyBytes = 1 << 20

// DecodeJSON decodes a request body strictly: unknown fields, trailing data,
// and bodies over 1 MiB are all rejected.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after JSON body")
	}
	return nil
}

// BadRequest writes a 400 INVALID_REQUEST with the given detail.
func BadRequest(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusBadRequest, "INVALID_REQUEST", message)
}

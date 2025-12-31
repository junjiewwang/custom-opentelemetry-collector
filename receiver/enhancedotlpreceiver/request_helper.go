// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package enhancedotlpreceiver

import (
	"encoding/json"
	"net/http"
)

// decodeJSON decodes JSON request body into the given type.
func decodeJSON[T any](r *http.Request) (*T, error) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeError writes an error response.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// errorResponse is a standard error response structure.
type errorResponse struct {
	Error string `json:"error"`
}

// successResponse is a standard success response structure.
type successResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

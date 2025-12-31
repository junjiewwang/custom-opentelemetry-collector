// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"encoding/json"
	"fmt"
	"net/http"

	"go.uber.org/zap"
)

// ============================================================================
// API Error Types
// ============================================================================

// APIError represents a structured API error with HTTP status code.
type APIError struct {
	Status  int    `json:"-"`
	Message string `json:"error"`
}

func (e *APIError) Error() string {
	return e.Message
}

// Common error constructors
func errBadRequest(msg string) *APIError {
	return &APIError{Status: http.StatusBadRequest, Message: msg}
}

func errNotFound(msg string) *APIError {
	return &APIError{Status: http.StatusNotFound, Message: msg}
}

func errInternal(msg string) *APIError {
	return &APIError{Status: http.StatusInternalServerError, Message: msg}
}

func errNotImplemented(msg string) *APIError {
	return &APIError{Status: http.StatusNotImplemented, Message: msg}
}

// ============================================================================
// JSON Request/Response Helpers
// ============================================================================

// decodeJSON decodes JSON request body into the given struct.
func decodeJSON[T any](r *http.Request) (*T, error) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return &v, nil
}

// writeJSON writes a JSON response with the given status code.
func (e *Extension) writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		e.logger.Error("Failed to encode JSON response", zap.Error(err))
	}
}

// writeError writes an error response.
func (e *Extension) writeError(w http.ResponseWriter, status int, message string) {
	e.writeJSON(w, status, map[string]string{"error": message})
}

// handleError handles APIError or generic error and writes appropriate response.
func (e *Extension) handleError(w http.ResponseWriter, err error) {
	if apiErr, ok := err.(*APIError); ok {
		e.writeError(w, apiErr.Status, apiErr.Message)
	} else {
		e.writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// ============================================================================
// Response Builders
// ============================================================================

// listResponse creates a standard list response with items and total count.
func listResponse(key string, items any, total int) map[string]any {
	return map[string]any{
		key:     items,
		"total": total,
	}
}

// successResponse creates a standard success response.
func successResponse(message string, extra ...map[string]any) map[string]any {
	resp := map[string]any{
		"success": true,
		"message": message,
	}
	for _, m := range extra {
		for k, v := range m {
			resp[k] = v
		}
	}
	return resp
}

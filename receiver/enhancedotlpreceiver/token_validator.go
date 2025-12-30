// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package enhancedotlpreceiver

import (
	"encoding/json"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

// tokenValidationError represents a token validation error response.
type tokenValidationError struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// tokenValidationResult holds the result of token validation from header.
type tokenValidationResult struct {
	valid bool
	token string
}

// extractTokenFromHeader extracts the token from the HTTP header.
// It supports both "Authorization: Bearer <token>" and custom header formats.
func (r *enhancedOTLPReceiver) extractTokenFromHeader(req *http.Request) string {
	headerName := r.config.GetTokenAuthHeaderName()
	headerPrefix := r.config.GetTokenAuthHeaderPrefix()

	headerValue := req.Header.Get(headerName)
	if headerValue == "" {
		return ""
	}

	// If prefix is specified, strip it
	if headerPrefix != "" && strings.HasPrefix(headerValue, headerPrefix) {
		return strings.TrimPrefix(headerValue, headerPrefix)
	}

	return headerValue
}

// validateTokenFromHeader validates the token from HTTP header using the control plane extension.
// Returns the validation result containing validity and the token value.
// Behavior:
//   - If token auth is disabled: pass through (valid=true, token="")
//   - If header has token: validate it, reject if invalid
//   - If header has no token: pass through to let downstream processor validate from attributes
func (r *enhancedOTLPReceiver) validateTokenFromHeader(w http.ResponseWriter, req *http.Request) tokenValidationResult {
	if !r.config.TokenAuth.Enabled {
		return tokenValidationResult{valid: true, token: ""}
	}

	// Extract token from header
	token := r.extractTokenFromHeader(req)

	// If no token in header, pass through to let processor validate from attributes
	if token == "" {
		r.logger.Debug("Token not found in header, passing through to processor for attribute validation")
		return tokenValidationResult{valid: true, token: ""}
	}

	// Validate the token from header
	if r.controlPlane == nil {
		r.logger.Warn("Token validation skipped: control plane not available")
		r.writeTokenError(w)
		return tokenValidationResult{valid: false, token: ""}
	}

	result, err := r.controlPlane.ValidateToken(req.Context(), token)
	if err != nil {
		r.logger.Error("Token validation error", zap.Error(err))
		r.writeTokenError(w)
		return tokenValidationResult{valid: false, token: ""}
	}

	if !result.Valid {
		r.logger.Debug("Token validation failed",
			zap.String("reason", result.Reason),
		)
		r.writeTokenError(w)
		return tokenValidationResult{valid: false, token: ""}
	}

	r.logger.Debug("Token validated successfully",
		zap.String("app_id", result.AppID),
		zap.String("app_name", result.AppName),
	)
	return tokenValidationResult{valid: true, token: token}
}

// writeTokenError writes a token validation error response.
func (r *enhancedOTLPReceiver) writeTokenError(w http.ResponseWriter) {
	errResp := tokenValidationError{
		Error:   "unauthorized",
		Code:    "INVALID_TOKEN",
		Message: "Token不合法",
	}

	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(http.StatusUnauthorized)

	if err := json.NewEncoder(w).Encode(errResp); err != nil {
		r.logger.Error("Failed to write token error response", zap.Error(err))
	}
}

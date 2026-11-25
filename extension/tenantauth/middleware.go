// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tenantauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

type middlewareOptions struct {
	preferBearer bool
	mcpErrors    bool
}

// Option customizes the middleware for a consumer's existing HTTP contract.
type Option func(*middlewareOptions)

// WithBearerPriority keeps the MCP contract: Authorization: Bearer wins over
// X-API-Key when both headers are present. The admin API keeps the opposite
// order by not passing this option.
func WithBearerPriority() Option {
	return func(o *middlewareOptions) { o.preferBearer = true }
}

// WithMCPErrorResponses keeps the MCP JSON error bodies and the 403 status for
// invalid keys (a missing key remains 401).
func WithMCPErrorResponses() Option {
	return func(o *middlewareOptions) { o.mcpErrors = true }
}

const (
	mcpMissingKeyBody = `{"error":"unauthorized","message":"API key is required. Use 'Authorization: Bearer <key>' or 'X-API-Key: <key>' header."}`
	mcpInvalidKeyBody = `{"error":"forbidden","message":"Invalid API key."}`
)

// New returns middleware that authenticates API keys:
//
//	sk_            → static super key (global, full privileges)
//	ok_            → dynamic operator key (global)
//	tk_            → dynamic tenant key (hard-bound to its tenant)
//	<unprefixed>   → static match first, then dynamic fallback
//
// The TenantID is only trusted when resolved by the validator from the key.
// X-Tenant-Id is impersonation for super/operator keys and ignored for tk_.
func New(staticKeys []string, validator Validator, logger *zap.Logger, opts ...Option) func(http.Handler) http.Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	var options middlewareOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublic(r) {
				next.ServeHTTP(w, r)
				return
			}
			key := extractAPIKey(r, options.preferBearer)
			if key == "" {
				writeAuthError(w, http.StatusUnauthorized, mcpMissingKeyBody, options.mcpErrors)
				return
			}
			id, ok := authenticate(r, key, staticKeys, validator, logger)
			if !ok {
				status := http.StatusUnauthorized
				if options.mcpErrors {
					status = http.StatusForbidden
				}
				writeAuthError(w, status, mcpInvalidKeyBody, options.mcpErrors)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
		})
	}
}

func writeAuthError(w http.ResponseWriter, status int, mcpBody string, mcpErrors bool) {
	if mcpErrors {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(mcpBody))
		return
	}
	http.Error(w, "Unauthorized", status)
}

func authenticate(
	r *http.Request,
	key string,
	staticKeys []string,
	validator Validator,
	logger *zap.Logger,
) (Identity, bool) {
	switch {
	case strings.HasPrefix(key, KeyPrefixSuper):
		if isStaticAPIKey(key, staticKeys) {
			return applyImpersonation(r, staticSuperIdentity(key), logger), true
		}

	case strings.HasPrefix(key, KeyPrefixOperator), strings.HasPrefix(key, KeyPrefixTenant):
		if id, ok := validateDynamic(r.Context(), key, validator); ok {
			return applyImpersonation(r, id, logger), true
		}

	default:
		if isStaticAPIKey(key, staticKeys) {
			return applyImpersonation(r, staticSuperIdentity(key), logger), true
		}
		if id, ok := validateDynamic(r.Context(), key, validator); ok {
			return applyImpersonation(r, id, logger), true
		}
	}
	return Identity{}, false
}

func validateDynamic(ctx context.Context, key string, validator Validator) (Identity, bool) {
	if validator == nil {
		return Identity{}, false
	}
	id, ok := validator.Validate(ctx, key)
	if !ok {
		return Identity{}, false
	}
	keyType := NormalizeKeyType(id.KeyType)
	if keyType == "" {
		return Identity{}, false
	}
	id.KeyType = keyType
	switch keyType {
	case KeyTypeTenant:
		if id.TenantID == "" {
			return Identity{}, false
		}
	case KeyTypeOperator:
		// Operator keys are global; a tenant scope must never be derived from
		// the validator payload for them.
		id.TenantID = ""
	}
	return id, true
}

func staticSuperIdentity(key string) Identity {
	return Identity{KeyType: KeyTypeSuper, KeyID: ShortDigest(key)}
}

// ShortDigest produces a stable, non-reversible identifier for a static key.
func ShortDigest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

func applyImpersonation(r *http.Request, id Identity, logger *zap.Logger) Identity {
	if id.KeyType == KeyTypeTenant {
		return id
	}
	target := strings.TrimSpace(r.Header.Get(ImpersonateTenantHeader))
	if target == "" {
		return id
	}
	logger.Info("admin impersonating tenant",
		zap.String("actor_key_type", id.KeyType),
		zap.String("target_tenant", target),
		zap.String("path", r.URL.Path),
	)
	id.TenantID = target
	id.Impersonating = true
	return id
}

func isPublic(r *http.Request) bool {
	return r.URL.Path == "/health" ||
		r.Method == http.MethodOptions ||
		(isWebSocketRequest(r) && strings.HasSuffix(r.URL.Path, "/ws"))
}

func isWebSocketRequest(r *http.Request) bool {
	connection := strings.ToLower(r.Header.Get("Connection"))
	return strings.Contains(connection, "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// extractAPIKey is case-insensitive for the Bearer scheme (R1). The header
// priority is configurable because the MCP and admin clients historically
// used opposite orders (R2).
func extractAPIKey(r *http.Request, preferBearer bool) string {
	xAPIKey := r.Header.Get("X-API-Key")
	bearer := ""
	if auth := r.Header.Get("Authorization"); auth != "" {
		parts := strings.SplitN(auth, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			bearer = strings.TrimSpace(parts[1])
		}
	}
	if preferBearer {
		if bearer != "" {
			return bearer
		}
		return xAPIKey
	}
	if xAPIKey != "" {
		return xAPIKey
	}
	return bearer
}

func isStaticAPIKey(key string, staticKeys []string) bool {
	for _, candidate := range staticKeys {
		if subtle.ConstantTimeCompare([]byte(key), []byte(candidate)) == 1 {
			return true
		}
	}
	return false
}

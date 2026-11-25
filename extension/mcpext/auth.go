// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"strings"

	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

const anonymousMCPPrincipal = "mcp:anonymous"

// mcpPrincipal derives a stable, non-sensitive audit identity from the
// authenticated tenant identity. The plaintext API key is never stored,
// logged, or persisted; KeyID is either a key record ID or a short digest.
func mcpPrincipal(ctx context.Context) string {
	identity, ok := tenantauth.IdentityFromContext(ctx)
	if !ok {
		return anonymousMCPPrincipal
	}
	parts := []string{"mcp", identity.KeyType}
	if identity.TenantID != "" {
		parts = append(parts, identity.TenantID)
	}
	if identity.KeyID != "" {
		parts = append(parts, identity.KeyID)
	}
	return strings.Join(parts, ":")
}

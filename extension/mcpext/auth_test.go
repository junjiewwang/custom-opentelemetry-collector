// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

func TestMCPPrincipal(t *testing.T) {
	tests := []struct {
		name     string
		identity *tenantauth.Identity
		want     string
	}{
		{name: "unauthenticated", want: anonymousMCPPrincipal},
		{
			name:     "static super key",
			identity: &tenantauth.Identity{KeyType: tenantauth.KeyTypeSuper, KeyID: "deadbeef"},
			want:     "mcp:super:deadbeef",
		},
		{
			name:     "global operator key",
			identity: &tenantauth.Identity{KeyType: tenantauth.KeyTypeOperator, KeyID: "key-2"},
			want:     "mcp:operator:key-2",
		},
		{
			name: "tenant key",
			identity: &tenantauth.Identity{
				KeyType:  tenantauth.KeyTypeTenant,
				TenantID: "tn-acme",
				KeyID:    "key-3",
			},
			want: "mcp:tenant:tn-acme:key-3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.identity != nil {
				ctx = tenantauth.WithIdentity(ctx, *tt.identity)
			}
			assert.Equal(t, tt.want, mcpPrincipal(ctx))
		})
	}
}

func TestMCPPrincipalNeverContainsPlaintextKey(t *testing.T) {
	ctx := tenantauth.WithIdentity(context.Background(), tenantauth.Identity{
		KeyType: tenantauth.KeyTypeTenant,
		KeyID:   "key-record-id",
	})
	assert.NotContains(t, mcpPrincipal(ctx), "tk_super_secret_value")
}

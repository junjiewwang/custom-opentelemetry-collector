// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
)

func TestWithInstrumentationActor(t *testing.T) {
	tests := []struct {
		name       string
		keyType    string
		tenantID   string
		wantID     string
		wantTenant string
	}{
		{name: "super global", keyType: KeyTypeSuper, wantID: "admin:super"},
		{name: "operator with tenant", keyType: KeyTypeOperator, tenantID: "tenant-a", wantID: "admin:operator:tenant-a", wantTenant: "tenant-a"},
		{name: "missing key type falls back", wantID: "admin:admin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				got   instrumentationmanager.Actor
				found bool
			)
			handler := withInstrumentationActor(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, found = instrumentationmanager.ActorFromContext(r.Context())
			}))

			ctx := context.Background()
			if tt.keyType != "" {
				ctx = WithKeyType(ctx, tt.keyType)
			}
			if tt.tenantID != "" {
				ctx = WithTenantID(ctx, tt.tenantID)
			}
			req := httptest.NewRequest(http.MethodPost, "/instrumentation/rules", nil).WithContext(ctx)
			handler.ServeHTTP(httptest.NewRecorder(), req)

			require.True(t, found)
			assert.Equal(t, tt.wantID, got.ID)
			assert.Equal(t, instrumentationmanager.ActorTypeAdmin, got.Type)
			assert.Equal(t, tt.wantTenant, got.TenantID)
		})
	}
}

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
	"go.opentelemetry.io/collector/custom/extension/tenantauth"
)

// withInstrumentationActor injects the authenticated identity for the dynamic
// instrumentation routes. The rule manager treats this as authoritative and
// ignores operator IDs supplied in request bodies. No raw API key is stored.
func withInstrumentationActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyType := KeyTypeFromContext(r.Context())
		if keyType == "" {
			keyType = "admin"
		}
		tenantID := TenantIDFromContext(r.Context())
		keyID := tenantauth.KeyIDFromContext(r.Context())
		actor := instrumentationmanager.Actor{
			ID:       "admin:" + keyType,
			Type:     instrumentationmanager.ActorTypeAdmin,
			TenantID: tenantID,
		}
		if tenantID != "" {
			actor.ID += ":" + tenantID
		}
		if keyID != "" {
			actor.ID += ":" + keyID
		}
		next.ServeHTTP(w, r.WithContext(instrumentationmanager.WithActor(r.Context(), actor)))
	})
}

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import "context"

// AccountResolver maps a tenant ID to a VictoriaMetrics account ID for native
// multitenancy (vm-cluster). It is injected by the extension layer, which owns
// the tenant→account mapping (tenantmanager.ResolveAccountID). A nil resolver
// means account scoping is not wired; an empty tenantID always resolves to
// account 0 (the default / global account).
type AccountResolver func(ctx context.Context, tenantID string) (uint32, error)

// resolveAccountID resolves the account for a tenant, defaulting to 0 for a nil
// resolver or empty tenant.
func resolveAccountID(r AccountResolver, ctx context.Context, tenantID string) (uint32, error) {
	if r == nil || tenantID == "" {
		return 0, nil
	}
	return r(ctx, tenantID)
}

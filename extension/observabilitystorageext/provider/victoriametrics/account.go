// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"context"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/tenantctx"
)

// resolveAccountID resolves the account for a tenant, defaulting to 0 for a nil
// resolver or empty tenant. AccountResolver is defined in tenantctx (a shared
// leaf package) so both the VM provider and the extension layer can name it.
func resolveAccountID(r tenantctx.AccountResolver, ctx context.Context, tenantID string) (uint32, error) {
	if r == nil || tenantID == "" {
		return 0, nil
	}
	return r(ctx, tenantID)
}

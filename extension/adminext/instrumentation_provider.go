// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import "go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"

var _ instrumentationmanager.Provider = (*Extension)(nil)

// InstrumentationManager exposes the single shared rule manager owned by this
// extension. Other extensions must reuse it rather than creating their own
// instance: the manager starts reconcile and GC workers of its own.
func (e *Extension) InstrumentationManager() instrumentationmanager.InstrumentationManager {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.instrMgr
}

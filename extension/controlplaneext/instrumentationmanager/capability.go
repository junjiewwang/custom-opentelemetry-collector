// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

import (
	"context"
	"fmt"
	"strings"
)

// CapabilityProvider decides whether agents belonging to an app may be
// instrumented. Implementations resolve appID to a tenant and read that
// tenant's capability. It is deliberately defined here so the rule manager
// does not depend on any concrete tenant or admin package.
type CapabilityProvider interface {
	IsInstrumentationAllowed(ctx context.Context, appID string) (bool, error)
}

// Option configures optional collaborators of the instrumentation manager.
// Using functional options keeps every existing constructor call site intact.
type Option func(*InstrumentationService)

// WithCapabilityProvider injects the tenant capability check. A nil provider
// disables the check entirely, preserving current behavior.
func WithCapabilityProvider(provider CapabilityProvider) Option {
	return func(s *InstrumentationService) {
		s.capabilities = provider
	}
}

// ensureInstrumentationAllowed is checked before any write that creates or
// re-applies instrumentation. It fails closed: provider errors are treated as
// "not allowed".
func (s *InstrumentationService) ensureInstrumentationAllowed(ctx context.Context, appID string) error {
	if s.capabilities == nil {
		return nil
	}
	app := strings.TrimSpace(appID)
	allowed, err := s.capabilities.IsInstrumentationAllowed(ctx, app)
	if err != nil {
		return fmt.Errorf("check instrumentation capability for app %q: %w", app, err)
	}
	if !allowed {
		return fmt.Errorf("instrumentation is disabled by tenant capability for app %q", app)
	}
	return nil
}

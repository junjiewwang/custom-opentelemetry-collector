// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package instrumentationmanager

// Provider is an optional capability implemented by extensions that own the
// shared InstrumentationManager instance. Consumers depend on this interface
// instead of on a concrete extension, so ownership can move between extensions
// without changing callers.
//
// Implementations must return nil until the manager has been started.
type Provider interface {
	InstrumentationManager() InstrumentationManager
}

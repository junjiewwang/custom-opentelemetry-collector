// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package registry provides the storage provider registration mechanism.
// Providers register a factory at init time; the extension resolves
// config.Type → factory once, instead of hard-coding per-provider switches
// in every accessor (the pre-registry pattern required editing five switch
// blocks in extension.go for each new provider).
package registry

import (
	"context"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// LifecycleProvider is the write-lifecycle surface the extension drives.
// It mirrors the former extension-internal internalProvider interface. It
// depends only on storedmodel + pdata so this package forms no import
// cycle with the extension package (StoredSpan aliases storedmodel.StoredSpan).
type LifecycleProvider interface {
	Name() string
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	HealthCheck(ctx context.Context) (bool, string, map[string]any)

	WriteSpans(ctx context.Context, spans []storedmodel.StoredSpan) error
	WriteTraces(ctx context.Context, td ptrace.Traces) error
	WriteMetrics(ctx context.Context, md pmetric.Metrics) error
	WriteLogs(ctx context.Context, ld plog.Logs) error

	FlushTraces(ctx context.Context) error
	FlushMetrics(ctx context.Context) error
	FlushLogs(ctx context.Context) error
}

// FactoryContext carries extension-level dependencies a factory needs to
// assemble its provider (e.g. the ES admin adapter needs the extension
// config and retention store). It is deliberately opaque to this package.
type FactoryContext struct {
	Logger        *zap.Logger
	ExtensionCfg  any // *config of the observabilitystorageext package
	RetentionStore any
}

// Factory builds one provider type from the extension configuration.
// The returned Accessors-compatible value is provider-specific; the caller
// (extension) narrows it via the AccessorsProvider optional interface, or
// casts to its own accessor struct. Keeping Accessors out of this package
// preserves acyclicity: this package must not import the extension package
// where the public reader interfaces live.
type Factory interface {
	// Name is the config.Type value this factory serves.
	Name() string
	// Create builds the provider lifecycle surface.
	Create(cfg any, fctx FactoryContext) (LifecycleProvider, error)
}

// AccessorsProvider is an optional Factory extension returning per-signal
// readers/admin in the types the factory's host package defines. The
// extension layer type-asserts this to obtain its concrete accessor struct.
type AccessorsProvider interface {
	Accessors() any
}

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register adds a factory. Duplicate names panic at init time — a silent
// override would route one provider's config to another's constructor.
func Register(f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[f.Name()]; dup {
		panic(fmt.Sprintf("observabilitystorageext: provider factory %q registered twice", f.Name()))
	}
	factories[f.Name()] = f
}

// Get returns the factory for a provider type name.
func Get(name string) (Factory, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := factories[name]
	if !ok {
		return nil, fmt.Errorf("unsupported provider type: %q", name)
	}
	return f, nil
}

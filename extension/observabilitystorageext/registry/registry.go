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
	"time"

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

// VMConfigView is the field-for-field view of the extension-level
// VictoriaMetricsConfig. It lives here (not in the extension or provider
// packages) because both sides can import the registry package without
// cycles: the extension builds it, the providerregistry bridge converts it
// to the concrete provider Config.
type VMConfigView struct {
	Endpoint      string
	WriteEndpoint string
	ReadEndpoint  string
	BatchSize     int
	FlushInterval time.Duration
	WriteTimeout  time.Duration
	ReadTimeout   time.Duration
	MaxRetries    int
	ExtraLabels   map[string]string
	RegistryPath  string
}

// ConfigTranslator converts an extension-level provider config value into
// the provider package's own config type, returned as any. It exists so the
// extension package can hand configs to factories without importing the
// provider packages (some of which import the extension package — the
// direction inversion is resolved by bridge packages registering a
// translator here).
type ConfigTranslator func(extCfg any) (any, error)

var (
	mu          sync.RWMutex
	factories   = map[string]Factory{}
	translators = map[string]ConfigTranslator{}
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

// RegisterConfigTranslator registers a config translator for a provider
// type. The bridge package for providers that implement public interfaces
// directly (e.g. VictoriaMetrics) registers one at init.
func RegisterConfigTranslator(name string, t ConfigTranslator) {
	mu.Lock()
	defer mu.Unlock()
	translators[name] = t
}

// TranslateConfig converts an extension-level config value into the
// provider's own config type using the registered translator.
func TranslateConfig(name string, extCfg any) (any, error) {
	mu.RLock()
	t, ok := translators[name]
	mu.RUnlock()
	if !ok {
		// No translator means extCfg is already the provider-native type.
		return extCfg, nil
	}
	return t(extCfg)
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

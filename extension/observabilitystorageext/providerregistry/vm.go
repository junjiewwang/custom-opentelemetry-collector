// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package providerregistry bridges providers that implement the public
// observabilitystorageext interfaces directly (VictoriaMetrics) with the
// registry package. It exists as a separate package to break the import
// cycle: victoriametrics imports observabilitystorageext (public interfaces),
// so observabilitystorageext cannot import it back — not even blank, and not
// even indirectly through hybrid. The collector's component wiring imports
// this package to make the VM factory resolvable.
package providerregistry

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/victoriametrics"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/registry"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// TranslateVMConfig maps the extension-level VictoriaMetricsConfig (defined
// in the extension package, which cannot import the provider package) to the
// provider's own Config. Registered with the registry at init as the VM
// config translator so the extension/hybrid wiring can convert configs
// without importing this package.
func TranslateVMConfig(ext any) (any, error) {
	v, ok := ext.(registry.VMConfigView)
	if !ok {
		return nil, fmt.Errorf("victoriametrics config translation: unsupported type %T", ext)
	}
	cfg := &victoriametrics.Config{
		Endpoint:      v.Endpoint,
		WriteEndpoint: v.WriteEndpoint,
		ReadEndpoint:  v.ReadEndpoint,
		BatchSize:     v.BatchSize,
		FlushInterval: v.FlushInterval,
		WriteTimeout:  v.WriteTimeout,
		ReadTimeout:   v.ReadTimeout,
		MaxRetries:    v.MaxRetries,
		ExtraLabels:   v.ExtraLabels,
		RegistryPath:  v.RegistryPath,
	}
	cfg.ApplyDefaults()
	return cfg, nil
}



// vmFactory adapts the VM provider to the registry contract. The reader
// implements the public MetricReader directly (no adapter layer).
type vmFactory struct{}

func (vmFactory) Name() string { return storedmodel.BackendVM }

func (vmFactory) Create(cfg any, fctx registry.FactoryContext) (registry.LifecycleProvider, error) {
	vmCfg, ok := cfg.(*victoriametrics.Config)
	if !ok {
		return nil, fmt.Errorf("victoriametrics factory: unexpected config type %T", cfg)
	}
	logger := fctx.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	p, err := victoriametrics.NewProvider(vmCfg, logger)
	if err != nil {
		return nil, err
	}
	return vmLifecycle{Provider: p}, nil
}

// vmLifecycle adapts the metric-only provider to the registry lifecycle
// surface. Trace/log writes are configuration errors; valid hybrid routing
// never sends them here.
type vmLifecycle struct {
	*victoriametrics.Provider
}

func (l vmLifecycle) WriteSpans(_ context.Context, _ []storedmodel.StoredSpan) error {
	return fmt.Errorf("victoriametrics backend does not accept traces (fix hybrid trace routing)")
}

func (l vmLifecycle) WriteTraces(_ context.Context, _ ptrace.Traces) error {
	return fmt.Errorf("victoriametrics backend does not accept traces (fix hybrid trace routing)")
}

func (l vmLifecycle) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	return l.Provider.MetricWriter().WriteMetrics(ctx, md)
}

func (l vmLifecycle) WriteLogs(_ context.Context, _ plog.Logs) error {
	return fmt.Errorf("victoriametrics backend does not accept logs (fix hybrid log routing)")
}

func (l vmLifecycle) FlushTraces(_ context.Context) error { return nil }

func (l vmLifecycle) FlushMetrics(ctx context.Context) error {
	return l.Provider.MetricWriter().Flush(ctx)
}

func (l vmLifecycle) FlushLogs(_ context.Context) error { return nil }

// VMMetricReader satisfies hybrid's vmReaderProvider: it exposes the reader
// as an opaque value (hybrid cannot name the public interface type — the
// extension package imports hybrid).
func (l vmLifecycle) VMMetricReader() any {
	return l.Provider.MetricReader()
}

// MetricReaderAsPublic narrows the opaque VM reader value to the public
// MetricReader interface (used by the extension layer).
func MetricReaderAsPublic(v any) (observabilitystorageext.MetricReader, bool) {
	r, ok := v.(observabilitystorageext.MetricReader)
	return r, ok
}

func init() {
	registry.Register(vmFactory{})
	registry.RegisterConfigTranslator(storedmodel.BackendVM, TranslateVMConfig)
}

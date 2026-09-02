// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package providerregistry bridges providers that implement the public
// observabilitystorageext interfaces directly (VictoriaMetrics) with the
// registry package. It exists as a separate package to break the import
// cycle: victoriametrics imports observabilitystorageext (public interfaces),
// so observabilitystorageext cannot import it back — not even blank.
// The collector's component registry blank-imports this package to trigger
// VM factory registration.
package providerregistry

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/victoriametrics"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/registry"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

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

// Lifecycle surface for VM: metric-only backend. Trace/log writes return
// unsupported errors; hybrid routing never sends them here in a valid config.
type vmLifecycle struct {
	*victoriametrics.Provider
}

func (l vmLifecycle) WriteSpans(_ context.Context, _ []storedmodel.StoredSpan) error {
	return fmt.Errorf("victoriametrics provider does not handle traces")
}

func (l vmLifecycle) WriteTraces(_ context.Context, _ ptrace.Traces) error {
	return fmt.Errorf("victoriametrics provider does not handle traces")
}

func (l vmLifecycle) WriteMetrics(ctx context.Context, md pmetric.Metrics) error {
	return l.Provider.MetricWriter().WriteMetrics(ctx, md)
}

// FlushMetrics flushes pending metric writes.
func (l vmLifecycle) FlushMetrics(ctx context.Context) error {
	return l.Provider.MetricWriter().Flush(ctx)
}

func (l vmLifecycle) WriteLogs(_ context.Context, _ plog.Logs) error {
	return fmt.Errorf("victoriametrics provider does not handle logs")
}

func (l vmLifecycle) FlushTraces(_ context.Context) error { return nil }

func (l vmLifecycle) FlushLogs(_ context.Context) error { return nil }

func init() { registry.Register(vmFactory{}) }

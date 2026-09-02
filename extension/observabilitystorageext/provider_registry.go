// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"sync"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/postgresql"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/registry"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// ═══════════════════════════════════════════════════
// Provider factories — registry wiring
// ═══════════════════════════════════════════════════
//
// ES/PG factories live in this package (not in the provider packages)
// because their adapter assembly needs private types from reader_adapter.go
// / pg_reader_adapter.go. The VictoriaMetrics factory lives in its own
// package (its reader implements the public interface directly) and
// self-registers via init.

// accessors carries a provider's per-signal readers and admin, built by the
// factory alongside the provider. Unsupported signals are nil.
type accessors struct {
	traceReader  TraceReader
	metricReader MetricReader
	logReader    LogReader
	storageAdmin StorageAdmin
}

// accessorProvider is implemented by factories that expose readers/admin
// for the provider they created. The extension type-asserts after Create.
type accessorProvider interface {
	Accessors() *accessors
}

// esFactory adapts NewProvider + private adapters to the registry contract.
type esFactory struct {
	acc *accessors
}

func (esFactory) Name() string { return storedmodel.BackendES }

func (f *esFactory) Create(cfg any, fctx registry.FactoryContext) (registry.LifecycleProvider, error) {
	esCfg, ok := cfg.(*elasticsearch.Config)
	if !ok {
		return nil, &factoryConfigTypeError{provider: "elasticsearch"}
	}
	logger := fctx.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	p, err := elasticsearch.NewProvider(esCfg, logger)
	if err != nil {
		return nil, err
	}
	acc := &accessors{}
	if p.MetricReader() != nil {
		acc.metricReader = &metricReaderAdapter{inner: p.MetricReader()}
	}
	if p.TraceReader() != nil {
		acc.traceReader = &traceReaderAdapter{inner: p.TraceReader()}
	}
	if p.LogReader() != nil {
		acc.logReader = &logReaderAdapter{inner: p.LogReader()}
	}
	if p.Admin() != nil {
		extCfg, _ := fctx.ExtensionCfg.(*Config)
		acc.storageAdmin = &storageAdminAdapter{inner: p.Admin(), config: extCfg, retentionStore: toRetentionStore(fctx.RetentionStore)}
	}
	f.acc = acc
	return p, nil
}

func (f *esFactory) Accessors() *accessors { return f.acc }

// pgFactory adapts the PostgreSQL provider the same way.
type pgFactory struct {
	acc *accessors
}

func (pgFactory) Name() string { return storedmodel.BackendPG }

func (f *pgFactory) Create(cfg any, fctx registry.FactoryContext) (registry.LifecycleProvider, error) {
	pgCfg, ok := cfg.(*postgresql.Config)
	if !ok {
		return nil, &factoryConfigTypeError{provider: "postgresql"}
	}
	logger := fctx.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	p, err := postgresql.NewProvider(pgCfg, logger)
	if err != nil {
		return nil, err
	}
	acc := &accessors{}
	if p.MetricReader() != nil {
		acc.metricReader = &pgMetricReaderAdapter{inner: p.MetricReader()}
	}
	if p.TraceReader() != nil {
		acc.traceReader = &pgTraceReaderAdapter{inner: p.TraceReader()}
	}
	if p.LogReader() != nil {
		acc.logReader = &pgLogReaderAdapter{inner: p.LogReader()}
	}
	if p.Admin() != nil {
		extCfg, _ := fctx.ExtensionCfg.(*Config)
		acc.storageAdmin = &pgStorageAdminAdapter{inner: p.Admin(), config: extCfg}
	}
	f.acc = acc
	return p, nil
}

func (f *pgFactory) Accessors() *accessors { return f.acc }

// ensureFactoriesRegistered registers built-in factories exactly once.
var ensureFactoriesRegistered sync.Once

func registerAllFactories() {
	registry.Register(&esFactory{})
	registry.Register(&pgFactory{})
	// victoriametrics self-registers via its package init (blank import above).
}

// toRetentionStore narrows the opaque FactoryContext field. A nil or
// wrong-typed value yields nil, which the ES admin adapter already tolerates.
func toRetentionStore(v any) lifecycle.RetentionStore {
	if rs, ok := v.(lifecycle.RetentionStore); ok {
		return rs
	}
	return nil
}

type factoryConfigTypeError struct{ provider string }

func (e *factoryConfigTypeError) Error() string {
	return e.provider + " factory: unexpected config type"
}

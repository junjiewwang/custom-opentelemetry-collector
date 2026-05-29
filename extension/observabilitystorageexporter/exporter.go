// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageexporter

import (
	"context"
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// storageExporter bridges the OTel exporter pipeline to the observability storage extension.
// It implements exporter.Traces, exporter.Metrics, and exporter.Logs interfaces.
type storageExporter struct {
	config    *Config
	logger    *zap.Logger
	extension *observabilitystorageext.ObservabilityStorage
}

// Ensure storageExporter implements required interfaces.
var (
	_ exporter.Traces  = (*storageExporter)(nil)
	_ exporter.Metrics = (*storageExporter)(nil)
	_ exporter.Logs    = (*storageExporter)(nil)
)

// newStorageExporter creates a new storageExporter instance.
func newStorageExporter(set exporter.Settings, cfg *Config) (*storageExporter, error) {
	return &storageExporter{
		config: cfg,
		logger: set.Logger,
	}, nil
}

// Start resolves the storage extension from the host and stores a reference to it.
func (e *storageExporter) Start(ctx context.Context, host component.Host) error {
	ext, err := e.resolveExtension(host)
	if err != nil {
		return err
	}
	e.extension = ext
	e.logger.Info("Observability storage exporter started",
		zap.String("extension", e.config.StorageExtension.String()),
	)
	return nil
}

// Shutdown flushes all pending data before stopping.
func (e *storageExporter) Shutdown(ctx context.Context) error {
	if e.extension == nil {
		return nil
	}

	e.logger.Info("Shutting down observability storage exporter, flushing buffers...")

	var firstErr error
	if err := e.extension.FlushTraces(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := e.extension.FlushMetrics(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := e.extension.FlushLogs(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Capabilities returns the exporter capabilities.
func (e *storageExporter) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

// ConsumeTraces writes trace data to the storage extension.
func (e *storageExporter) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	if e.extension == nil {
		return fmt.Errorf("storage extension not resolved")
	}
	return e.extension.WriteTraces(ctx, td)
}

// ConsumeMetrics writes metric data to the storage extension.
func (e *storageExporter) ConsumeMetrics(ctx context.Context, md pmetric.Metrics) error {
	if e.extension == nil {
		return fmt.Errorf("storage extension not resolved")
	}
	return e.extension.WriteMetrics(ctx, md)
}

// ConsumeLogs writes log data to the storage extension.
func (e *storageExporter) ConsumeLogs(ctx context.Context, ld plog.Logs) error {
	if e.extension == nil {
		return fmt.Errorf("storage extension not resolved")
	}
	return e.extension.WriteLogs(ctx, ld)
}

// resolveExtension looks up the observability storage extension from the host.
func (e *storageExporter) resolveExtension(host component.Host) (*observabilitystorageext.ObservabilityStorage, error) {
	extID := e.config.StorageExtension

	extensions := host.GetExtensions()
	ext, ok := extensions[extID]
	if !ok {
		return nil, fmt.Errorf(
			"observability storage extension %q not found; available extensions: %v",
			extID, availableExtensionIDs(extensions),
		)
	}

	storageExt, ok := ext.(*observabilitystorageext.ObservabilityStorage)
	if !ok {
		return nil, fmt.Errorf(
			"extension %q is not an observability storage extension (got %T)",
			extID, ext,
		)
	}

	return storageExt, nil
}

// availableExtensionIDs returns the IDs of all available extensions for error messages.
func availableExtensionIDs(extensions map[component.ID]component.Component) []string {
	ids := make([]string, 0, len(extensions))
	for id := range extensions {
		ids = append(ids, id.String())
	}
	return ids
}

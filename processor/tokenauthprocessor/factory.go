// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package tokenauthprocessor

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
)

const (
	// TypeStr is the type string for the token auth processor.
	TypeStr = "tokenauth"
)

// Type is the component type.
var Type = component.MustNewType(TypeStr)

// NewFactory creates a new factory for the token auth processor.
func NewFactory() processor.Factory {
	return processor.NewFactory(
		Type,
		createDefaultConfig,
		processor.WithTraces(createTracesProcessor, component.StabilityLevelDevelopment),
		processor.WithMetrics(createMetricsProcessor, component.StabilityLevelDevelopment),
		processor.WithLogs(createLogsProcessor, component.StabilityLevelDevelopment),
	)
}

// createTracesProcessor creates a traces processor.
func createTracesProcessor(
	ctx context.Context,
	set processor.Settings,
	cfg component.Config,
	nextConsumer consumer.Traces,
) (processor.Traces, error) {
	return newTokenAuthProcessor(set, cfg.(*Config), nextConsumer, nil, nil)
}

// createMetricsProcessor creates a metrics processor.
func createMetricsProcessor(
	ctx context.Context,
	set processor.Settings,
	cfg component.Config,
	nextConsumer consumer.Metrics,
) (processor.Metrics, error) {
	return newTokenAuthProcessor(set, cfg.(*Config), nil, nextConsumer, nil)
}

// createLogsProcessor creates a logs processor.
func createLogsProcessor(
	ctx context.Context,
	set processor.Settings,
	cfg component.Config,
	nextConsumer consumer.Logs,
) (processor.Logs, error) {
	return newTokenAuthProcessor(set, cfg.(*Config), nil, nil, nextConsumer)
}

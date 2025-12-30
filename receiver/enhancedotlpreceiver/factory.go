// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package enhancedotlpreceiver

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

const (
	// TypeStr is the type string for this receiver.
	TypeStr = "enhanced_otlp"
)

var (
	// Type is the component type for this receiver.
	Type = component.MustNewType(TypeStr)
)

// NewFactory creates a new factory for the enhanced OTLP receiver.
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		Type,
		func() component.Config {
			return createDefaultConfig()
		},
		receiver.WithTraces(createTracesReceiver, component.StabilityLevelAlpha),
		receiver.WithMetrics(createMetricsReceiver, component.StabilityLevelAlpha),
		receiver.WithLogs(createLogsReceiver, component.StabilityLevelAlpha),
	)
}

// createTracesReceiver creates a traces receiver.
func createTracesReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	nextConsumer consumer.Traces,
) (receiver.Traces, error) {
	oCfg := cfg.(*Config)
	r, err := newEnhancedOTLPReceiver(set, oCfg)
	if err != nil {
		return nil, err
	}
	r.registerTracesConsumer(nextConsumer)
	return r, nil
}

// createMetricsReceiver creates a metrics receiver.
func createMetricsReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	nextConsumer consumer.Metrics,
) (receiver.Metrics, error) {
	oCfg := cfg.(*Config)
	r, err := newEnhancedOTLPReceiver(set, oCfg)
	if err != nil {
		return nil, err
	}
	r.registerMetricsConsumer(nextConsumer)
	return r, nil
}

// createLogsReceiver creates a logs receiver.
func createLogsReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	nextConsumer consumer.Logs,
) (receiver.Logs, error) {
	oCfg := cfg.(*Config)
	r, err := newEnhancedOTLPReceiver(set, oCfg)
	if err != nil {
		return nil, err
	}
	r.registerLogsConsumer(nextConsumer)
	return r, nil
}

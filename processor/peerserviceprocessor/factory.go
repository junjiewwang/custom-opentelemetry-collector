// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package peerserviceprocessor

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
)

const (
	// TypeStr is the type string for the peer service processor.
	TypeStr = "peer_service"
)

// Type is the component type.
var Type = component.MustNewType(TypeStr)

// NewFactory creates a new factory for the peer service processor.
func NewFactory() processor.Factory {
	return processor.NewFactory(
		Type,
		createDefaultConfig,
		processor.WithTraces(createTracesProcessor, component.StabilityLevelDevelopment),
	)
}

// createTracesProcessor creates a traces processor.
func createTracesProcessor(
	_ context.Context,
	set processor.Settings,
	cfg component.Config,
	nextConsumer consumer.Traces,
) (processor.Traces, error) {
	return newProcessor(set, cfg.(*Config), nextConsumer)
}

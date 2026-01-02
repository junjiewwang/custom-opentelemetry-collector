// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package agentgatewayreceiver

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

const (
	typeStr   = "agent_gateway"
	stability = component.StabilityLevelDevelopment
)

// Type is the component type for the agent gateway receiver.
var Type = component.MustNewType(typeStr)

// NewFactory creates a new factory for the agent gateway receiver.
func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		Type,
		func() component.Config { return createDefaultConfig() },
		receiver.WithTraces(createTracesReceiver, stability),
		receiver.WithMetrics(createMetricsReceiver, stability),
		receiver.WithLogs(createLogsReceiver, stability),
	)
}

func createTracesReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	consumer consumer.Traces,
) (receiver.Traces, error) {
	oCfg := cfg.(*Config)
	r, err := getOrCreateReceiver(set, oCfg)
	if err != nil {
		return nil, err
	}
	r.registerTracesConsumer(consumer)
	return r, nil
}

func createMetricsReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	consumer consumer.Metrics,
) (receiver.Metrics, error) {
	oCfg := cfg.(*Config)
	r, err := getOrCreateReceiver(set, oCfg)
	if err != nil {
		return nil, err
	}
	r.registerMetricsConsumer(consumer)
	return r, nil
}

func createLogsReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	consumer consumer.Logs,
) (receiver.Logs, error) {
	oCfg := cfg.(*Config)
	r, err := getOrCreateReceiver(set, oCfg)
	if err != nil {
		return nil, err
	}
	r.registerLogsConsumer(consumer)
	return r, nil
}

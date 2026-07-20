// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/consumer"
)

const (
	typeStr = "metricgen"
)

// NewFactory creates a connector.Factory for the MetricGenerator connector.
func NewFactory() connector.Factory {
	return connector.NewFactory(
		component.MustNewType(typeStr),
		CreateDefaultConfig,
		connector.WithTracesToMetrics(createTracesToMetrics, component.StabilityLevelAlpha),
	)
}

func createTracesToMetrics(
	ctx context.Context,
	set connector.Settings,
	cfg component.Config,
	nextConsumer consumer.Metrics,
) (connector.Traces, error) {
	c := cfg.(*Config)

	conn := &metricGenConnector{
		config:        c,
		metricsConsumer: nextConsumer,
		logger:        set.Logger,
		done:          make(chan struct{}),
	}

	if c.RED != nil && c.RED.Enabled {
		conn.redGen = NewREDGenerator(c.RED, c.CardinalityLimit)
	}

	if c.ServiceGraph != nil && c.ServiceGraph.Enabled {
		conn.sgGen = NewServiceGraphGenerator(c.ServiceGraph)
	}

	conn.flusher = &metricFlusher{
		interval: c.MetricsFlushInterval,
		consumer: nextConsumer,
		redGen:   conn.redGen,
		sgGen:    conn.sgGen,
		logger:   set.Logger,
	}

	return conn, nil
}

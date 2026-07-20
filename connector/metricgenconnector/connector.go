// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"context"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

type metricGenConnector struct {
	config          *Config
	metricsConsumer consumer.Metrics
	redGen          *REDGenerator
	sgGen           *ServiceGraphGenerator
	flusher         *metricFlusher
	logger          *zap.Logger
	done            chan struct{}
	wg              sync.WaitGroup
}

// Capabilities implements the consumer interface.
func (c *metricGenConnector) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

// ConsumeTraces implements the traces connector interface.
func (c *metricGenConnector) ConsumeTraces(ctx context.Context, td ptrace.Traces) error {
	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		resource := rs.Resource()
		svcName := extractServiceName(resource)
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			appID := extractAppID(resource)
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				if c.redGen != nil {
					c.redGen.ProcessSpan(svcName, appID, resource, span)
				}
				if c.sgGen != nil {
					c.sgGen.ProcessSpan(svcName, appID, resource, span)
				}
			}
		}
	}
	return nil
}

// Start starts the background flush goroutine.
func (c *metricGenConnector) Start(ctx context.Context, host component.Host) error {
	c.wg.Add(1)
	go c.flusher.run(ctx, &c.wg)
	return nil
}

// Shutdown stops the background flush goroutine.
func (c *metricGenConnector) Shutdown(ctx context.Context) error {
	close(c.done)
	c.wg.Wait()
	// Final flush.
	c.flusher.flush()
	return nil
}

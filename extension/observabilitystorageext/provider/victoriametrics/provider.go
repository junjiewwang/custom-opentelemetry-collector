// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"context"
	"fmt"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/tenantctx"
	"go.uber.org/zap"
)

// Provider is the VictoriaMetrics storage provider. It handles the metric
// signal only; trace/log/admin are out of scope (hybrid routing sends those
// to other backends). The rollup engine is intentionally NOT started here —
// rollup exists to compensate for ES aggregation/paging limits, which VM's
// columnar TSDB does not have.
type Provider struct {
	config       *Config
	logger       *zap.Logger
	client       VMClient
	metricWriter *MetricWriter
	metricReader *MetricReader
	// resolveAccount stores the tenant→account resolver so it can be applied in
	// Start, once the reader/writer exist. It makes SetAccountResolver order-
	// independent with respect to Start.
	resolveAccount tenantctx.AccountResolver
}

// NewProvider builds the provider (does not start I/O; call Start).
func NewProvider(cfg *Config, logger *zap.Logger) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.ApplyDefaults()
	return &Provider{
		config: cfg,
		logger: logger,
		client: newHTTPVMClient(cfg),
	}, nil
}

// Name identifies the provider in hybrid routing.
func (p *Provider) Name() string { return "victoriametrics" }

// Start initializes the writer flush loop and reader.
func (p *Provider) Start(ctx context.Context) error {
	if healthy, msg, _ := p.HealthCheck(ctx); !healthy {
		p.logger.Warn("victoriametrics: initial health check failed (will retry on writes)", zap.String("msg", msg))
	}
	p.metricWriter = NewMetricWriter(p.client, p.config, p.logger)
	p.metricReader = newMetricReader(p.client, p.config.AccountScope, p.logger)
	if p.resolveAccount != nil {
		p.metricWriter.SetAccountResolver(p.resolveAccount)
		p.metricReader.SetAccountResolver(p.resolveAccount)
	}
	return nil
}

// Shutdown flushes pending writes and persists the registry.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p.metricWriter != nil {
		p.metricWriter.Stop()
		if err := p.metricWriter.Flush(ctx); err != nil {
			p.logger.Warn("victoriametrics: final flush failed", zap.Error(err))
		}
	}
	return nil
}

// HealthCheck reports VM reachability via /health in the registry contract
// shape (bool, message, details).
func (p *Provider) HealthCheck(ctx context.Context) (bool, string, map[string]any) {
	if err := p.client.Health(ctx); err != nil {
		return false, fmt.Sprintf("victoriametrics backend unhealthy: %v", err), map[string]any{
			"endpoint": p.config.readBase(),
		}
	}
	return true, "ok", map[string]any{
		"endpoint": p.config.readBase(),
	}
}

// MetricWriter returns the metric writer.
func (p *Provider) MetricWriter() *MetricWriter { return p.metricWriter }

// MetricReader returns the metric reader.
func (p *Provider) MetricReader() *MetricReader { return p.metricReader }

// SetClient overrides the HTTP client (tests inject fakes before Start).
func (p *Provider) SetClient(c VMClient) { p.client = c }

// SetAccountResolver wires the tenant→account mapping (tenantmanager.ResolveAccountID)
// into the reader and writer. A nil resolver disables account scoping (every
// tenant resolves to account 0). Call before Start.
func (p *Provider) SetAccountResolver(resolve tenantctx.AccountResolver) {
	p.resolveAccount = resolve
	if p.metricReader != nil {
		p.metricReader.SetAccountResolver(resolve)
	}
	if p.metricWriter != nil {
		p.metricWriter.SetAccountResolver(resolve)
	}
}

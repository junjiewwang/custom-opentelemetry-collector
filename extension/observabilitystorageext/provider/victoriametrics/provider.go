// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"context"
	"fmt"
	"time"

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
	registry     *typeRegistry
	metricWriter *MetricWriter
	metricReader *MetricReader

	// registryFlushTicker drives periodic typeRegistry persistence.
	registryFlushStop chan struct{}
}

// NewProvider builds the provider (does not start I/O; call Start).
func NewProvider(cfg *Config, logger *zap.Logger) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.ApplyDefaults()
	registry := newTypeRegistry(cfg.RegistryPath)
	return &Provider{
		config:   cfg,
		logger:   logger,
		client:   newHTTPVMClient(cfg),
		registry: registry,
	}, nil
}

// Name identifies the provider in hybrid routing.
func (p *Provider) Name() string { return "victoriametrics" }

// Start initializes the type registry persistence and the writer flush loop.
func (p *Provider) Start(ctx context.Context) error {
	if err := p.registry.loadFromFile(); err != nil {
		// A corrupt registry file must not take the storage extension down;
		// metrics types repopulate on the next write.
		p.logger.Warn("victoriametrics: type registry load failed (starting empty)", zap.Error(err))
	}
	if healthy, msg, _ := p.HealthCheck(ctx); !healthy {
		p.logger.Warn("victoriametrics: initial health check failed (will retry on writes)", zap.String("msg", msg))
	}
	p.metricWriter = NewMetricWriter(p.client, p.config, p.registry, p.logger)
	p.metricReader = newMetricReader(p.client, p.registry, p.logger)

	if p.config.RegistryPath != "" {
		p.registryFlushStop = make(chan struct{})
		go func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-p.registryFlushStop:
					return
				case <-ticker.C:
					if err := p.registry.flushToFile(); err != nil {
						p.logger.Warn("victoriametrics: type registry flush failed", zap.Error(err))
					}
				}
			}
		}()
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
	if p.registryFlushStop != nil {
		close(p.registryFlushStop)
	}
	if err := p.registry.flushToFile(); err != nil {
		p.logger.Warn("victoriametrics: type registry final flush failed", zap.Error(err))
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

// TypeRegistry exposes the internal registry for tests.
func (p *Provider) TypeRegistry() *typeRegistry { return p.registry }

// SetClient overrides the HTTP client (tests inject fakes before Start).
func (p *Provider) SetClient(c VMClient) { p.client = c }

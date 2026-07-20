// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"time"

	"go.opentelemetry.io/collector/component"
)

// Config holds the configuration for the MetricGenerator connector.
type Config struct {
	MetricsFlushInterval time.Duration `mapstructure:"metrics_flush_interval"`
	CardinalityLimit     int           `mapstructure:"cardinality_limit"`
	RED                  *REDConfig    `mapstructure:"red"`
	ServiceGraph         *ServiceGraphConfig `mapstructure:"service_graph"`
}

type REDConfig struct {
	Enabled    bool            `mapstructure:"enabled"`
	Dimensions []string        `mapstructure:"dimensions"`
	Histogram  HistogramConfig `mapstructure:"histogram"`
}

type ServiceGraphConfig struct {
	Enabled           bool            `mapstructure:"enabled"`
	Dimensions        []string        `mapstructure:"dimensions"`
	Histogram         HistogramConfig `mapstructure:"histogram"`           // latency buckets (seconds)
	MessageSizeHisto  HistogramConfig `mapstructure:"message_size_histo"`  // message size buckets (bytes)
}

// DefaultServiceGraphLatencyBuckets returns latency buckets in seconds.
// Aligned with Tempo: 5ms to 10s range.
func DefaultServiceGraphLatencyBuckets() []float64 {
	return []float64{
		0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
	}
}

// DefaultServiceGraphMessageSizeBuckets returns message size buckets in bytes.
func DefaultServiceGraphMessageSizeBuckets() []float64 {
	return []float64{
		128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072,
	}
}

type HistogramConfig struct {
	Buckets []float64 `mapstructure:"buckets"`
}

var _ component.Config = (*Config)(nil)

func (c *Config) Validate() error { return nil }

var defaultHistogramBuckets = []float64{
	2, 4, 6, 8, 10, 15, 20, 30, 40, 50, 75, 100, 150, 200, 300, 400, 500,
	750, 1000, 1500, 2000, 3000, 4000, 5000, 7500, 10000,
}

var defaultDimensions = []string{
	"http.method", "http.status_code", "http.route",
	"rpc.method", "rpc.service", "peer.service",
}

func CreateDefaultConfig() component.Config {
	return &Config{
		MetricsFlushInterval: 15 * time.Second,
		CardinalityLimit:     2000,
		RED: &REDConfig{
			Enabled:    true,
			Dimensions: defaultDimensions,
			Histogram:  HistogramConfig{Buckets: defaultHistogramBuckets},
		},
		ServiceGraph: &ServiceGraphConfig{
			Enabled:          true,
			Dimensions:       []string{"http.method"},
			Histogram:        HistogramConfig{Buckets: DefaultServiceGraphLatencyBuckets()},
			MessageSizeHisto: HistogramConfig{Buckets: DefaultServiceGraphMessageSizeBuckets()},
		},
	}
}

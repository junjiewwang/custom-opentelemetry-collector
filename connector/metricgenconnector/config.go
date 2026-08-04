// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package metricgenconnector

import (
	"fmt"
	"time"

	"go.opentelemetry.io/collector/component"
)

// Aggregation temporality configuration values, aligned with the OTel
// spanmetrics connector so users can reuse the same config strings.
const (
	TemporalityCumulative = "AGGREGATION_TEMPORALITY_CUMULATIVE"
	TemporalityDelta      = "AGGREGATION_TEMPORALITY_DELTA"
)

// DefaultStaleSeriesCycles is how many consecutive flush cycles a series can go
// without new data before being evicted in cumulative mode. With the default
// 15s flush interval this is 75s.
const DefaultStaleSeriesCycles = 5

// Config holds the configuration for the MetricGenerator connector.
type Config struct {
	MetricsFlushInterval   time.Duration       `mapstructure:"metrics_flush_interval"`
	CardinalityLimit       int                 `mapstructure:"cardinality_limit"`
	AggregationTemporality string              `mapstructure:"aggregation_temporality"`
	StaleSeriesCycles      int                 `mapstructure:"stale_series_cycles"`
	RED                    *REDConfig          `mapstructure:"red"`
	ServiceGraph           *ServiceGraphConfig `mapstructure:"service_graph"`
}

type REDConfig struct {
	Enabled    bool            `mapstructure:"enabled"`
	Dimensions []string        `mapstructure:"dimensions"`
	Histogram  HistogramConfig `mapstructure:"histogram"`
}

type ServiceGraphConfig struct {
	Enabled          bool            `mapstructure:"enabled"`
	Dimensions       []string        `mapstructure:"dimensions"`
	Histogram        HistogramConfig `mapstructure:"histogram"`          // latency buckets (seconds)
	MessageSizeHisto HistogramConfig `mapstructure:"message_size_histo"` // message size buckets (bytes)
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

func (c *Config) Validate() error {
	switch c.AggregationTemporality {
	case TemporalityCumulative, TemporalityDelta:
	default:
		return fmt.Errorf("aggregation_temporality must be %q or %q, got %q", TemporalityCumulative, TemporalityDelta, c.AggregationTemporality)
	}
	return nil
}

// IsCumulative reports whether the connector emits cumulative-temporality metrics.
func (c *Config) IsCumulative() bool { return c.AggregationTemporality == TemporalityCumulative }

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
		MetricsFlushInterval:   15 * time.Second,
		CardinalityLimit:       2000,
		AggregationTemporality: TemporalityCumulative,
		StaleSeriesCycles:      DefaultStaleSeriesCycles,
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

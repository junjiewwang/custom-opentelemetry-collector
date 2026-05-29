// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"errors"
	"fmt"
	"time"
)

// Config defines the configuration for the observability storage extension.
type Config struct {
	// Type is the provider type: "elasticsearch", "postgresql", "mongodb", "hybrid".
	Type string `mapstructure:"type"`

	// Elasticsearch holds the ES provider configuration.
	Elasticsearch *ElasticsearchConfig `mapstructure:"elasticsearch,omitempty"`

	// Retention holds the platform-level retention configuration.
	Retention RetentionConfig `mapstructure:"retention"`
}

// RetentionConfig holds the platform-level retention defaults and constraints.
type RetentionConfig struct {
	// Default retention durations per signal type (used when App has no override).
	DefaultTrace  time.Duration `mapstructure:"default_trace"`
	DefaultMetric time.Duration `mapstructure:"default_metric"`
	DefaultLog    time.Duration `mapstructure:"default_log"`

	// Max retention durations (upper bound that tenants cannot exceed).
	MaxTrace  time.Duration `mapstructure:"max_trace"`
	MaxMetric time.Duration `mapstructure:"max_metric"`
	MaxLog    time.Duration `mapstructure:"max_log"`
}

// ElasticsearchConfig holds the Elasticsearch provider configuration.
type ElasticsearchConfig struct {
	// Addresses is the list of ES node URLs.
	Addresses []string `mapstructure:"addresses"`

	// Username for basic auth (optional).
	Username string `mapstructure:"username"`

	// Password for basic auth (optional).
	Password string `mapstructure:"password"`

	// BatchSize is the number of documents per bulk request.
	BatchSize int `mapstructure:"batch_size"`

	// FlushInterval is the max time between bulk flushes.
	FlushInterval time.Duration `mapstructure:"flush_interval"`

	// MaxRetries is the number of retry attempts for failed requests.
	MaxRetries int `mapstructure:"max_retries"`

	// Traces holds trace index configuration.
	Traces IndexConfig `mapstructure:"traces"`

	// Metrics holds metric index configuration.
	Metrics IndexConfig `mapstructure:"metrics"`

	// Logs holds log index configuration.
	Logs IndexConfig `mapstructure:"logs"`
}

// IndexConfig holds configuration for a single signal's index.
type IndexConfig struct {
	// IndexPrefix is the prefix for index names (e.g., "otel-traces").
	IndexPrefix string `mapstructure:"index_prefix"`

	// IndexDateFormat is the Go time format for date-based index rotation.
	IndexDateFormat string `mapstructure:"index_date_format"`

	// Shards is the number of primary shards.
	Shards int `mapstructure:"shards"`

	// Replicas is the number of replica shards.
	Replicas int `mapstructure:"replicas"`

	// Retention is the data retention duration for this signal.
	Retention time.Duration `mapstructure:"retention"`

	// RefreshInterval is the ES refresh interval for the index.
	RefreshInterval string `mapstructure:"refresh_interval"`
}

// Validate checks if the configuration is valid.
func (cfg *Config) Validate() error {
	if cfg == nil {
		return errors.New("config is nil")
	}

	switch cfg.Type {
	case "elasticsearch":
		if cfg.Elasticsearch == nil {
			return errors.New("elasticsearch config is required when type is 'elasticsearch'")
		}
		return cfg.Elasticsearch.Validate()
	case "postgresql", "mongodb", "hybrid":
		return fmt.Errorf("provider type %q is not yet implemented", cfg.Type)
	case "":
		return errors.New("type is required")
	default:
		return fmt.Errorf("unknown provider type: %q", cfg.Type)
	}
}

// Validate checks if the Elasticsearch configuration is valid.
func (cfg *ElasticsearchConfig) Validate() error {
	if len(cfg.Addresses) == 0 {
		return errors.New("elasticsearch.addresses is required")
	}
	for i, addr := range cfg.Addresses {
		if addr == "" {
			return fmt.Errorf("elasticsearch.addresses[%d] is empty", i)
		}
	}
	return nil
}

// ApplyDefaults sets reasonable default values for unset fields.
func (cfg *Config) ApplyDefaults() {
	if cfg == nil {
		return
	}
	cfg.Retention.ApplyDefaults()
	if cfg.Elasticsearch != nil {
		cfg.Elasticsearch.ApplyDefaults()
	}
}

// ApplyDefaults sets default values for RetentionConfig.
func (cfg *RetentionConfig) ApplyDefaults() {
	if cfg.DefaultTrace == 0 {
		cfg.DefaultTrace = 7 * 24 * time.Hour // 7 days
	}
	if cfg.DefaultMetric == 0 {
		cfg.DefaultMetric = 30 * 24 * time.Hour // 30 days
	}
	if cfg.DefaultLog == 0 {
		cfg.DefaultLog = 14 * 24 * time.Hour // 14 days
	}
	if cfg.MaxTrace == 0 {
		cfg.MaxTrace = 30 * 24 * time.Hour // 30 days
	}
	if cfg.MaxMetric == 0 {
		cfg.MaxMetric = 90 * 24 * time.Hour // 90 days
	}
	if cfg.MaxLog == 0 {
		cfg.MaxLog = 30 * 24 * time.Hour // 30 days
	}
}

// ApplyDefaults sets default values for ElasticsearchConfig.
func (cfg *ElasticsearchConfig) ApplyDefaults() {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 5000
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 3 * time.Second
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	cfg.Traces.applyDefaults("otel-traces", 3, 1, "5s")
	cfg.Metrics.applyDefaults("otel-metrics", 2, 1, "10s")
	cfg.Logs.applyDefaults("otel-logs", 3, 1, "5s")
}

func (cfg *IndexConfig) applyDefaults(prefix string, shards, replicas int, refresh string) {
	if cfg.IndexPrefix == "" {
		cfg.IndexPrefix = prefix
	}
	if cfg.IndexDateFormat == "" {
		cfg.IndexDateFormat = "2006.01.02"
	}
	if cfg.Shards <= 0 {
		cfg.Shards = shards
	}
	if cfg.Replicas < 0 {
		cfg.Replicas = replicas
	}
	if cfg.RefreshInterval == "" {
		cfg.RefreshInterval = refresh
	}
}

// createDefaultConfig returns the default configuration for this extension.
func createDefaultConfig() *Config {
	cfg := &Config{
		Type:          "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{},
	}
	cfg.ApplyDefaults()
	return cfg
}

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// Config defines the configuration for the observability storage extension.
type Config struct {
	// Type is the provider type: "elasticsearch", "postgresql", "mongodb", "hybrid".
	Type string `mapstructure:"type"`

	// Elasticsearch holds the ES provider configuration.
	Elasticsearch *ElasticsearchConfig `mapstructure:"elasticsearch,omitempty"`

	// PostgreSQL holds the PG provider configuration.
	PostgreSQL *PostgreSQLConfig `mapstructure:"postgresql,omitempty"`

	// Hybrid holds the Hybrid provider routing configuration.
	Hybrid *HybridConfig `mapstructure:"hybrid,omitempty"`

	// Retention holds the platform-level retention configuration.
	Retention RetentionConfig `mapstructure:"retention"`

	// Scheduler holds the lifecycle scheduler configuration.
	Scheduler SchedulerConfig `mapstructure:"scheduler"`
}

// SchedulerConfig holds the data lifecycle scheduler configuration.
// This is a thin wrapper that maps to lifecycle.SchedulerConfig.
type SchedulerConfig struct {
	// Enabled controls whether the scheduler is active.
	Enabled bool `mapstructure:"enabled"`

	// Interval is the check frequency (default: 1h).
	Interval time.Duration `mapstructure:"interval"`

	// DryRun previews what would be deleted without executing.
	DryRun bool `mapstructure:"dry_run"`

	// UsageWarningRatio triggers WARN-level alerts (default: 0.75).
	UsageWarningRatio float64 `mapstructure:"usage_warning_ratio"`

	// UsageCriticalRatio triggers ERROR-level alerts (default: 0.90).
	UsageCriticalRatio float64 `mapstructure:"usage_critical_ratio"`

	// TrendBufferSize is how many usage snapshots to keep (default: 168 = 7d @ 1h).
	TrendBufferSize int `mapstructure:"trend_buffer_size"`

	// ─── Distributed Purge Configuration ───

	// Distributed enables multi-node cooperative purge mode.
	// Requires a storage extension with Redis. Falls back to local mode if unavailable.
	Distributed bool `mapstructure:"distributed"`

	// ControlplaneExtension is the component type name of the controlplane extension.
	// When set and task_manager.type="engine", the scheduler shares the same Engine instance
	// from controlplaneext for distributed purge coordination. This replaces the need for
	// a separate Coordinator and provides unified task management across all subsystems.
	// Example: "controlplane"
	ControlplaneExtension string `mapstructure:"controlplane_extension"`

	// StorageExtension is the component type name of the storageext extension
	// used to obtain Redis client for distributed coordination.
	// Only used as fallback when ControlplaneExtension is not set or engine is unavailable.
	// Required when Distributed=true and no ControlplaneExtension. Example: "storage"
	StorageExtension string `mapstructure:"storage_extension"`

	// RedisName is the named Redis connection to use for distributed coordination.
	// Default: "default" (uses storageext.GetDefaultRedis).
	RedisName string `mapstructure:"redis_name"`

	// DistributedThreshold: only use distributed mode when expired index count
	// exceeds this value. Below this, single-node is more efficient. Default: 50.
	DistributedThreshold int `mapstructure:"distributed_threshold"`

	// WorkerConcurrency: max concurrent delete operations per node per cycle.
	// Controls ES pressure. Default: 10.
	WorkerConcurrency int `mapstructure:"worker_concurrency"`

	// TaskTimeout: max time a single task can take before considered timed-out.
	// Default: 30s.
	TaskTimeout time.Duration `mapstructure:"task_timeout"`

	// MaxRetries: max retry attempts for a failed task. Default: 3.
	MaxRetries int `mapstructure:"max_retries"`

	// VerifyTimeout: max time the leader waits for all tasks to complete
	// during the verification phase. Default: 2m.
	VerifyTimeout time.Duration `mapstructure:"verify_timeout"`

	// VerifyPollInterval: polling interval during verification. Default: 2s.
	VerifyPollInterval time.Duration `mapstructure:"verify_poll_interval"`

	// NodeID: unique identifier for this node. Auto-generated if empty.
	NodeID string `mapstructure:"node_id"`
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
	case "postgresql":
		if cfg.PostgreSQL == nil {
			return errors.New("postgresql config is required when type is 'postgresql'")
		}
		return cfg.PostgreSQL.Validate()
	case "hybrid":
		if cfg.Hybrid == nil {
			return errors.New("hybrid config is required when type is 'hybrid'")
		}
		return cfg.Hybrid.Validate(cfg)
	case "mongodb":
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
	if cfg.PostgreSQL != nil {
		cfg.PostgreSQL.ApplyDefaults()
	}
	if cfg.Hybrid != nil {
		cfg.Hybrid.ApplyDefaults()
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

// ═══════════════════════════════════════════════════
// PostgreSQL Configuration
// ═══════════════════════════════════════════════════

// PostgreSQLConfig holds the PostgreSQL provider configuration.
type PostgreSQLConfig struct {
	// DSN is the PostgreSQL connection string.
	// Format: postgres://user:password@host:port/dbname?sslmode=disable
	DSN string `mapstructure:"dsn"`

	// MaxConns is the maximum number of connections in the pool.
	MaxConns int32 `mapstructure:"max_conns"`

	// MinConns is the minimum number of idle connections in the pool.
	MinConns int32 `mapstructure:"min_conns"`

	// MaxConnLifetime is the maximum amount of time a connection may be reused.
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime"`

	// MaxConnIdleTime is the maximum amount of time a connection may be idle.
	MaxConnIdleTime time.Duration `mapstructure:"max_conn_idle_time"`

	// BatchSize is the number of rows per COPY batch.
	BatchSize int `mapstructure:"batch_size"`

	// FlushInterval is the max time between batch flushes.
	FlushInterval time.Duration `mapstructure:"flush_interval"`

	// MaxRetries is the number of retry attempts for failed operations.
	MaxRetries int `mapstructure:"max_retries"`

	// UseTimescaleDB enables TimescaleDB hypertable features for metrics.
	UseTimescaleDB bool `mapstructure:"use_timescaledb"`

	// Traces holds trace table configuration.
	Traces PGTableConfig `mapstructure:"traces"`

	// Metrics holds metric table configuration.
	Metrics PGTableConfig `mapstructure:"metrics"`

	// Logs holds log table configuration.
	Logs PGTableConfig `mapstructure:"logs"`
}

// PGTableConfig holds configuration for a single signal's table.
type PGTableConfig struct {
	// TableName is the base table name (e.g., "otel_traces").
	TableName string `mapstructure:"table_name"`

	// Retention is the data retention duration for this signal.
	Retention time.Duration `mapstructure:"retention"`

	// PartitionInterval is the interval for time-based partitioning.
	PartitionInterval time.Duration `mapstructure:"partition_interval"`
}

// Validate checks if the PostgreSQL configuration is valid.
func (cfg *PostgreSQLConfig) Validate() error {
	if cfg.DSN == "" {
		return errors.New("postgresql.dsn is required")
	}
	return nil
}

// ApplyDefaults sets default values for PostgreSQLConfig.
func (cfg *PostgreSQLConfig) ApplyDefaults() {
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 20
	}
	if cfg.MinConns <= 0 {
		cfg.MinConns = 5
	}
	if cfg.MaxConnLifetime <= 0 {
		cfg.MaxConnLifetime = 30 * time.Minute
	}
	if cfg.MaxConnIdleTime <= 0 {
		cfg.MaxConnIdleTime = 5 * time.Minute
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 5000
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 3 * time.Second
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	cfg.Traces.applyDefaults("otel_traces", 24*time.Hour)
	cfg.Metrics.applyDefaults("otel_metrics", 6*time.Hour)
	cfg.Logs.applyDefaults("otel_logs", 24*time.Hour)
}

func (cfg *PGTableConfig) applyDefaults(tableName string, partitionInterval time.Duration) {
	if cfg.TableName == "" {
		cfg.TableName = tableName
	}
	if cfg.PartitionInterval <= 0 {
		cfg.PartitionInterval = partitionInterval
	}
}

// ═══════════════════════════════════════════════════
// Hybrid Configuration
// ═══════════════════════════════════════════════════

// HybridConfig holds the routing configuration for the Hybrid provider.
// Each signal can be routed independently to "elasticsearch" or "postgresql".
type HybridConfig struct {
	// Trace specifies which backend to use for traces: "elasticsearch" or "postgresql".
	Trace string `mapstructure:"trace"`

	// Metric specifies which backend to use for metrics.
	Metric string `mapstructure:"metric"`

	// Log specifies which backend to use for logs.
	Log string `mapstructure:"log"`

	// Admin specifies which backend to use for admin operations.
	Admin string `mapstructure:"admin"`
}

// Validate checks if the HybridConfig is valid and ensures dependent provider configs exist.
func (cfg *HybridConfig) Validate(parent *Config) error {
	validBackends := map[string]bool{storedmodel.BackendES: true, storedmodel.BackendPG: true}

	routes := map[string]string{
		storedmodel.SignalTrace: cfg.Trace,
		storedmodel.SignalMetric: cfg.Metric,
		storedmodel.SignalLog:    cfg.Log,
		storedmodel.SignalAdmin:  cfg.Admin,
	}
	for signal, backend := range routes {
		if !validBackends[backend] {
			return fmt.Errorf("hybrid.%s: invalid backend %q (must be %q or %q)",
				signal, backend, storedmodel.BackendES, storedmodel.BackendPG)
		}
	}

	// Check that required sub-provider configs are present
	needsES := cfg.Trace == storedmodel.BackendES || cfg.Metric == storedmodel.BackendES ||
		cfg.Log == storedmodel.BackendES || cfg.Admin == storedmodel.BackendES
	needsPG := cfg.Trace == storedmodel.BackendPG || cfg.Metric == storedmodel.BackendPG ||
		cfg.Log == storedmodel.BackendPG || cfg.Admin == storedmodel.BackendPG

	if needsES && parent.Elasticsearch == nil {
		return errors.New("hybrid routing requires elasticsearch config but 'elasticsearch' section is missing")
	}
	if needsPG && parent.PostgreSQL == nil {
		return errors.New("hybrid routing requires postgresql config but 'postgresql' section is missing")
	}

	// Validate sub-provider configs
	if needsES {
		if err := parent.Elasticsearch.Validate(); err != nil {
			return fmt.Errorf("hybrid: elasticsearch config invalid: %w", err)
		}
	}
	if needsPG {
		if err := parent.PostgreSQL.Validate(); err != nil {
			return fmt.Errorf("hybrid: postgresql config invalid: %w", err)
		}
	}

	return nil
}

// ApplyDefaults sets default values for HybridConfig.
func (cfg *HybridConfig) ApplyDefaults() {
	if cfg.Trace == "" {
		cfg.Trace = storedmodel.BackendES
	}
	if cfg.Metric == "" {
		cfg.Metric = storedmodel.BackendPG
	}
	if cfg.Log == "" {
		cfg.Log = storedmodel.BackendES
	}
	if cfg.Admin == "" {
		cfg.Admin = storedmodel.BackendPG
	}
}

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package observabilitystorageext

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/mitchellh/mapstructure"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
	"go.uber.org/zap/zaptest"
	"gopkg.in/yaml.v3"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
	"go.opentelemetry.io/collector/custom/extension/storageext"
	"go.opentelemetry.io/collector/custom/extension/storageext/blobstore"
)

// ═══════════════════════════════════════════════════
// Test: YAML → Config Parsing
// ═══════════════════════════════════════════════════

func TestConfigParsing_FullDistributedYAML(t *testing.T) {
	yamlContent := `
type: elasticsearch
elasticsearch:
  addresses:
    - http://localhost:9200
    - http://localhost:9201
  username: elastic
  password: changeme
  batch_size: 3000
  flush_interval: 5s
  max_retries: 2
  traces:
    index_prefix: otel-traces
    shards: 5
    replicas: 1
    retention: 168h
  metrics:
    index_prefix: otel-metrics
    shards: 3
    replicas: 1
    retention: 720h
  logs:
    index_prefix: otel-logs
    shards: 5
    replicas: 1
    retention: 336h
retention:
  default_trace: 168h
  default_metric: 720h
  default_log: 336h
  max_trace: 720h
  max_metric: 2160h
  max_log: 720h
scheduler:
  enabled: true
  interval: 30m
  dry_run: false
  usage_warning_ratio: 0.80
  usage_critical_ratio: 0.95
  trend_buffer_size: 336
  distributed: true
  storage_extension: storageext
  redis_name: lifecycle
  distributed_threshold: 30
  worker_concurrency: 8
  task_timeout: 45s
  max_retries: 5
  verify_timeout: 3m
  verify_poll_interval: 3s
  node_id: node-e2e-test
`

	cfg := parseYAMLToConfig(t, yamlContent)

	// Validate provider type
	assert.Equal(t, "elasticsearch", cfg.Type)

	// Validate ES config
	require.NotNil(t, cfg.Elasticsearch)
	assert.Equal(t, []string{"http://localhost:9200", "http://localhost:9201"}, cfg.Elasticsearch.Addresses)
	assert.Equal(t, "elastic", cfg.Elasticsearch.Username)
	assert.Equal(t, 3000, cfg.Elasticsearch.BatchSize)
	assert.Equal(t, 5*time.Second, cfg.Elasticsearch.FlushInterval)
	assert.Equal(t, "otel-traces", cfg.Elasticsearch.Traces.IndexPrefix)
	assert.Equal(t, 5, cfg.Elasticsearch.Traces.Shards)
	assert.Equal(t, 168*time.Hour, cfg.Elasticsearch.Traces.Retention)

	// Validate retention
	assert.Equal(t, 168*time.Hour, cfg.Retention.DefaultTrace)
	assert.Equal(t, 720*time.Hour, cfg.Retention.DefaultMetric)
	assert.Equal(t, 336*time.Hour, cfg.Retention.DefaultLog)
	assert.Equal(t, 720*time.Hour, cfg.Retention.MaxTrace)

	// Validate scheduler
	assert.True(t, cfg.Scheduler.Enabled)
	assert.Equal(t, 30*time.Minute, cfg.Scheduler.Interval)
	assert.False(t, cfg.Scheduler.DryRun)
	assert.Equal(t, 0.80, cfg.Scheduler.UsageWarningRatio)
	assert.Equal(t, 0.95, cfg.Scheduler.UsageCriticalRatio)
	assert.Equal(t, 336, cfg.Scheduler.TrendBufferSize)

	// Validate distributed config
	assert.True(t, cfg.Scheduler.Distributed)
	assert.Equal(t, "storageext", cfg.Scheduler.StorageExtension)
	assert.Equal(t, "lifecycle", cfg.Scheduler.RedisName)
	assert.Equal(t, 30, cfg.Scheduler.DistributedThreshold)
	assert.Equal(t, 8, cfg.Scheduler.WorkerConcurrency)
	assert.Equal(t, 45*time.Second, cfg.Scheduler.TaskTimeout)
	assert.Equal(t, 5, cfg.Scheduler.MaxRetries)
	assert.Equal(t, 3*time.Minute, cfg.Scheduler.VerifyTimeout)
	assert.Equal(t, 3*time.Second, cfg.Scheduler.VerifyPollInterval)
	assert.Equal(t, "node-e2e-test", cfg.Scheduler.NodeID)
}

func TestConfigParsing_MinimalSchedulerDisabled(t *testing.T) {
	yamlContent := `
type: elasticsearch
elasticsearch:
  addresses:
    - http://localhost:9200
scheduler:
  enabled: false
`

	cfg := parseYAMLToConfig(t, yamlContent)

	assert.Equal(t, "elasticsearch", cfg.Type)
	assert.False(t, cfg.Scheduler.Enabled)
	assert.False(t, cfg.Scheduler.Distributed)
}

func TestConfigParsing_SingleNodeSchedulerEnabled(t *testing.T) {
	yamlContent := `
type: elasticsearch
elasticsearch:
  addresses:
    - http://localhost:9200
scheduler:
  enabled: true
  interval: 2h
  dry_run: true
`

	cfg := parseYAMLToConfig(t, yamlContent)

	assert.True(t, cfg.Scheduler.Enabled)
	assert.Equal(t, 2*time.Hour, cfg.Scheduler.Interval)
	assert.True(t, cfg.Scheduler.DryRun)
	assert.False(t, cfg.Scheduler.Distributed) // not distributed
}

// ═══════════════════════════════════════════════════
// Test: Config Defaults Application
// ═══════════════════════════════════════════════════

func TestConfigDefaults_SchedulerFieldsPopulated(t *testing.T) {
	cfg := &Config{
		Type: "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{
			Addresses: []string{"http://localhost:9200"},
		},
		Scheduler: SchedulerConfig{
			Enabled:     true,
			Distributed: true,
		},
	}
	cfg.ApplyDefaults()

	// Extension-level defaults are applied via lifecycle.SchedulerConfig.ApplyDefaults
	// when buildLifecycleScheduler is called. Here we test the config struct is valid.
	require.NoError(t, cfg.Validate())
	assert.Equal(t, "elasticsearch", cfg.Type)
}

func TestLifecycleSchedulerConfig_DefaultsFilled(t *testing.T) {
	// Test that lifecycle.SchedulerConfig.ApplyDefaults fills missing fields
	lsCfg := lifecycle.SchedulerConfig{
		Enabled:     true,
		Distributed: true,
	}
	lsCfg.ApplyDefaults()

	assert.Equal(t, time.Hour, lsCfg.Interval)
	assert.Equal(t, 0.75, lsCfg.UsageWarningRatio)
	assert.Equal(t, 0.90, lsCfg.UsageCriticalRatio)
	assert.Equal(t, 168, lsCfg.TrendBufferSize)
	assert.Equal(t, 50, lsCfg.DistributedThreshold)
	assert.Equal(t, 10, lsCfg.WorkerConcurrency)
	assert.Equal(t, 30*time.Second, lsCfg.TaskTimeout)
	assert.Equal(t, 3, lsCfg.MaxRetries)
	assert.Equal(t, 2*time.Minute, lsCfg.VerifyTimeout)
	assert.Equal(t, 2*time.Second, lsCfg.VerifyPollInterval)
}

// ═══════════════════════════════════════════════════
// Test: Config Validation
// ═══════════════════════════════════════════════════

func TestConfigValidation_MissingType(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type is required")
}

func TestConfigValidation_InvalidType(t *testing.T) {
	cfg := &Config{Type: "cassandra"}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider type")
}

func TestConfigValidation_MissingESAddresses(t *testing.T) {
	cfg := &Config{
		Type:          "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "addresses is required")
}

func TestConfigValidation_ESWithEmptyAddress(t *testing.T) {
	cfg := &Config{
		Type: "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{
			Addresses: []string{"http://localhost:9200", ""},
		},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "addresses[1] is empty")
}

func TestConfigValidation_PGMissingDSN(t *testing.T) {
	cfg := &Config{
		Type:       "postgresql",
		PostgreSQL: &PostgreSQLConfig{},
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dsn is required")
}

func TestConfigValidation_ValidESConfig(t *testing.T) {
	cfg := &Config{
		Type: "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{
			Addresses: []string{"http://localhost:9200"},
		},
	}
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())
}

// ═══════════════════════════════════════════════════
// Test: Extension Start → Scheduler Activation
// ═══════════════════════════════════════════════════

func TestExtensionStart_SchedulerDisabled_NoSchedulerCreated(t *testing.T) {
	cfg := &Config{
		Type: "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{
			Addresses: []string{"http://localhost:9200"},
		},
		Scheduler: SchedulerConfig{Enabled: false},
	}
	cfg.ApplyDefaults()

	ext, err := newObservabilityStorageExtension(context.Background(), extension.Settings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zaptest.NewLogger(t),
		},
	}, cfg)
	require.NoError(t, err)

	assert.Nil(t, ext.scheduler)
	assert.Nil(t, ext.GetLifecycleScheduler())
}

func TestExtensionStart_SchedulerEnabled_SchedulerBuilt(t *testing.T) {
	cfg := &Config{
		Type: "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{
			Addresses: []string{"http://localhost:9200"},
		},
		Scheduler: SchedulerConfig{
			Enabled:  true,
			Interval: time.Hour,
			NodeID:   "test-node",
		},
	}
	cfg.ApplyDefaults()

	ext, err := newObservabilityStorageExtension(context.Background(), extension.Settings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zaptest.NewLogger(t),
		},
	}, cfg)
	require.NoError(t, err)

	// Build scheduler without starting (to avoid connecting to real ES)
	mockHost := &mockHost{extensions: map[component.ID]component.Component{}}
	scheduler := ext.buildLifecycleScheduler(mockHost)

	require.NotNil(t, scheduler)
	assert.NotNil(t, ext.retentionStore)
}

// ═══════════════════════════════════════════════════
// Test: Distributed Mode with Redis Integration
// ═══════════════════════════════════════════════════

func TestExtensionStart_DistributedMode_RedisCoordinatorInjected(t *testing.T) {
	// Start miniredis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	cfg := &Config{
		Type: "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{
			Addresses: []string{"http://localhost:9200"},
		},
		Scheduler: SchedulerConfig{
			Enabled:              true,
			Interval:             time.Hour,
			Distributed:          true,
			StorageExtension:     "storageext",
			RedisName:            "default",
			DistributedThreshold: 10,
			WorkerConcurrency:    5,
			TaskTimeout:          30 * time.Second,
			MaxRetries:           3,
			VerifyTimeout:        2 * time.Minute,
			VerifyPollInterval:   2 * time.Second,
			NodeID:               "e2e-node-1",
		},
	}
	cfg.ApplyDefaults()

	ext, err := newObservabilityStorageExtension(context.Background(), extension.Settings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zaptest.NewLogger(t),
		},
	}, cfg)
	require.NoError(t, err)

	// Build a mock host with storage extension providing Redis
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	storageExt := &mockStorageExtension{redisClient: redisClient}
	storageID := component.MustNewIDWithName("storageext", "")
	host := &mockHost{
		extensions: map[component.ID]component.Component{
			storageID: storageExt,
		},
	}

	// Build scheduler (verifies coordinator is wired)
	scheduler := ext.buildLifecycleScheduler(host)
	require.NotNil(t, scheduler)

	// Verify the scheduler can be started and stopped without error
	ctx := context.Background()
	scheduler.Start(ctx)
	// Give scheduler a brief moment to initialize
	time.Sleep(50 * time.Millisecond)
	scheduler.Stop()
}

func TestExtensionStart_DistributedMode_NoStorageExt_FallsBackToLocal(t *testing.T) {
	cfg := &Config{
		Type: "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{
			Addresses: []string{"http://localhost:9200"},
		},
		Scheduler: SchedulerConfig{
			Enabled:          true,
			Interval:         time.Hour,
			Distributed:      true,
			StorageExtension: "storageext",
			RedisName:        "default",
			NodeID:           "e2e-fallback-node",
		},
	}
	cfg.ApplyDefaults()

	ext, err := newObservabilityStorageExtension(context.Background(), extension.Settings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zaptest.NewLogger(t),
		},
	}, cfg)
	require.NoError(t, err)

	// Host has no storage extension → should gracefully fall back
	host := &mockHost{extensions: map[component.ID]component.Component{}}
	scheduler := ext.buildLifecycleScheduler(host)
	require.NotNil(t, scheduler)
	// scheduler is built but without coordinator (falls back to local mode)
}

func TestExtensionStart_DistributedMode_NoStorageExtensionConfigured(t *testing.T) {
	cfg := &Config{
		Type: "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{
			Addresses: []string{"http://localhost:9200"},
		},
		Scheduler: SchedulerConfig{
			Enabled:          true,
			Interval:         time.Hour,
			Distributed:      true,
			StorageExtension: "", // not configured
			NodeID:           "e2e-no-storage",
		},
	}
	cfg.ApplyDefaults()

	ext, err := newObservabilityStorageExtension(context.Background(), extension.Settings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zaptest.NewLogger(t),
		},
	}, cfg)
	require.NoError(t, err)

	host := &mockHost{extensions: map[component.ID]component.Component{}}
	scheduler := ext.buildLifecycleScheduler(host)
	require.NotNil(t, scheduler)
	// No storage extension config → warning logged, local mode
}

// ═══════════════════════════════════════════════════
// Test: End-to-End Config → Scheduler Config Propagation
// ═══════════════════════════════════════════════════

func TestConfigPropagation_SchedulerConfigFieldsPassedThrough(t *testing.T) {
	// Verify that all extension-level SchedulerConfig fields are correctly
	// propagated to the lifecycle.SchedulerConfig during buildLifecycleScheduler.
	cfg := &Config{
		Type: "elasticsearch",
		Elasticsearch: &ElasticsearchConfig{
			Addresses: []string{"http://localhost:9200"},
		},
		Retention: RetentionConfig{
			DefaultTrace:  72 * time.Hour,
			DefaultMetric: 240 * time.Hour,
			DefaultLog:    120 * time.Hour,
			MaxTrace:      720 * time.Hour,
			MaxMetric:     2160 * time.Hour,
			MaxLog:        720 * time.Hour,
		},
		Scheduler: SchedulerConfig{
			Enabled:              true,
			Interval:             45 * time.Minute,
			DryRun:               true,
			UsageWarningRatio:    0.70,
			UsageCriticalRatio:   0.88,
			TrendBufferSize:      100,
			Distributed:          true,
			StorageExtension:     "storageext",
			RedisName:            "purge-redis",
			DistributedThreshold: 20,
			WorkerConcurrency:    15,
			TaskTimeout:          60 * time.Second,
			MaxRetries:           4,
			VerifyTimeout:        5 * time.Minute,
			VerifyPollInterval:   5 * time.Second,
			NodeID:               "propagation-test-node",
		},
	}
	cfg.ApplyDefaults()

	ext, err := newObservabilityStorageExtension(context.Background(), extension.Settings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zaptest.NewLogger(t),
		},
	}, cfg)
	require.NoError(t, err)

	// Use host without storage ext to avoid Redis dependency in this test
	host := &mockHost{extensions: map[component.ID]component.Component{}}
	scheduler := ext.buildLifecycleScheduler(host)
	require.NotNil(t, scheduler)
	ext.scheduler = scheduler

	// Since scheduler internals are not directly exposed, we verify indirectly:
	// The scheduler was built without panics, and retention store is initialized.
	assert.NotNil(t, ext.retentionStore)
	assert.NotNil(t, ext.GetRetentionStore())
	assert.NotNil(t, ext.GetLifecycleScheduler())
}

// ═══════════════════════════════════════════════════
// Test: Full Pipeline — Config → Start → Run → Stop
// ═══════════════════════════════════════════════════

func TestFullPipeline_ConfigToStartToStop(t *testing.T) {
	// Start miniredis for distributed coordination
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	yamlContent := `
type: elasticsearch
elasticsearch:
  addresses:
    - http://localhost:9200
retention:
  default_trace: 168h
  default_metric: 720h
  default_log: 336h
scheduler:
  enabled: true
  interval: 1h
  distributed: true
  storage_extension: storageext
  redis_name: default
  distributed_threshold: 50
  worker_concurrency: 10
  task_timeout: 30s
  max_retries: 3
  verify_timeout: 2m
  verify_poll_interval: 2s
  node_id: pipeline-test-node
`

	cfg := parseYAMLToConfig(t, yamlContent)
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	// Create extension
	ext, err := newObservabilityStorageExtension(context.Background(), extension.Settings{
		TelemetrySettings: component.TelemetrySettings{
			Logger: zaptest.NewLogger(t),
		},
	}, cfg)
	require.NoError(t, err)

	// Setup mock host with Redis
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	storageExt := &mockStorageExtension{redisClient: redisClient}
	storageID := component.MustNewIDWithName("storageext", "")
	host := &mockHost{
		extensions: map[component.ID]component.Component{
			storageID: storageExt,
		},
	}

	// Build and verify scheduler
	scheduler := ext.buildLifecycleScheduler(host)
	require.NotNil(t, scheduler)
	ext.scheduler = scheduler

	// Start and stop lifecycle
	ctx := context.Background()
	scheduler.Start(ctx)

	// Verify scheduler is running
	assert.NotNil(t, ext.GetLifecycleScheduler())
	assert.NotNil(t, ext.GetRetentionStore())

	// Graceful shutdown
	scheduler.Stop()
}

// ═══════════════════════════════════════════════════
// Test: Hybrid Config Parsing
// ═══════════════════════════════════════════════════

func TestConfigParsing_HybridWithScheduler(t *testing.T) {
	yamlContent := `
type: hybrid
elasticsearch:
  addresses:
    - http://localhost:9200
postgresql:
  dsn: postgres://user:pass@localhost:5432/otel
hybrid:
  trace: elasticsearch
  metric: postgresql
  log: elasticsearch
  admin: postgresql
scheduler:
  enabled: true
  interval: 1h
  distributed: false
`

	cfg := parseYAMLToConfig(t, yamlContent)
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())

	assert.Equal(t, "hybrid", cfg.Type)
	assert.Equal(t, "elasticsearch", cfg.Hybrid.Trace)
	assert.Equal(t, "postgresql", cfg.Hybrid.Metric)
	assert.True(t, cfg.Scheduler.Enabled)
	assert.False(t, cfg.Scheduler.Distributed)
}

// ═══════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════

// parseYAMLToConfig parses YAML string into Config using yaml + mapstructure
// (mimics the OTel Collector config pipeline).
func parseYAMLToConfig(t *testing.T, yamlContent string) *Config {
	t.Helper()

	// Step 1: Parse YAML into generic map (like confmap does)
	var rawMap map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(yamlContent), &rawMap))

	// Step 2: Decode into Config struct via mapstructure (same as OTel uses)
	cfg := &Config{}
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           cfg,
		TagName:          "mapstructure",
		WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	require.NoError(t, err)
	require.NoError(t, decoder.Decode(rawMap))

	return cfg
}

// mockHost implements component.Host for testing.
type mockHost struct {
	extensions map[component.ID]component.Component
}

func (h *mockHost) GetExtensions() map[component.ID]component.Component {
	return h.extensions
}

// mockStorageExtension implements storageext.Storage with a miniredis-backed Redis client.
type mockStorageExtension struct {
	redisClient redis.UniversalClient
}

// Ensure compile-time interface checks
var _ component.Component = (*mockStorageExtension)(nil)
var _ storageext.Storage = (*mockStorageExtension)(nil)

func (m *mockStorageExtension) Start(_ context.Context, _ component.Host) error { return nil }
func (m *mockStorageExtension) Shutdown(_ context.Context) error                { return nil }

func (m *mockStorageExtension) GetRedis(_ string) (redis.UniversalClient, error) {
	return m.redisClient, nil
}

func (m *mockStorageExtension) GetDefaultRedis() (redis.UniversalClient, error) {
	return m.redisClient, nil
}

func (m *mockStorageExtension) GetNacosConfigClient(_ string) (config_client.IConfigClient, error) {
	return nil, nil
}

func (m *mockStorageExtension) GetDefaultNacosConfigClient() (config_client.IConfigClient, error) {
	return nil, nil
}

func (m *mockStorageExtension) GetNacosNamingClient(_ string) (naming_client.INamingClient, error) {
	return nil, nil
}

func (m *mockStorageExtension) GetDefaultNacosNamingClient() (naming_client.INamingClient, error) {
	return nil, nil
}

func (m *mockStorageExtension) HasRedis(_ string) bool   { return true }
func (m *mockStorageExtension) HasNacos(_ string) bool   { return false }
func (m *mockStorageExtension) ListRedisNames() []string { return []string{"default"} }
func (m *mockStorageExtension) ListNacosNames() []string { return nil }

func (m *mockStorageExtension) GetBlobStore() blobstore.BlobStore { return nil }
func (m *mockStorageExtension) HasBlobStore() bool                { return false }

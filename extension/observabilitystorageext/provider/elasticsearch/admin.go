// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// Admin provides administrative operations for the Elasticsearch backend.
type Admin struct {
	client *Client
	config *Config
	logger *zap.Logger
}

// NewAdmin creates a new ES admin instance.
func NewAdmin(client *Client, config *Config, logger *zap.Logger) *Admin {
	return &Admin{
		client: client,
		config: config,
		logger: logger.Named("es-admin"),
	}
}

// InitSchema creates index templates and ILM policies for all signal types.
func (a *Admin) InitSchema(ctx context.Context) error {
	a.logger.Info("Initializing ES schema (index templates + ILM policies)")

	// Create ILM policies
	if err := a.createILMPolicy(ctx, a.config.Traces.IndexPrefix+"-policy", a.config.Traces.Retention); err != nil {
		return fmt.Errorf("failed to create trace ILM policy: %w", err)
	}
	if err := a.createILMPolicy(ctx, a.config.Metrics.IndexPrefix+"-policy", a.config.Metrics.Retention); err != nil {
		return fmt.Errorf("failed to create metric ILM policy: %w", err)
	}
	if err := a.createILMPolicy(ctx, a.config.Logs.IndexPrefix+"-policy", a.config.Logs.Retention); err != nil {
		return fmt.Errorf("failed to create log ILM policy: %w", err)
	}

	// Create index templates
	if err := a.createTraceTemplate(ctx); err != nil {
		return fmt.Errorf("failed to create trace index template: %w", err)
	}
	if err := a.createMetricTemplate(ctx); err != nil {
		return fmt.Errorf("failed to create metric index template: %w", err)
	}
	if err := a.createLogTemplate(ctx); err != nil {
		return fmt.Errorf("failed to create log index template: %w", err)
	}

	a.logger.Info("ES schema initialized successfully")
	return nil
}

// GetStatus returns the ES cluster status.
func (a *Admin) GetStatus(ctx context.Context) (map[string]any, error) {
	return a.client.ClusterHealth(ctx)
}

// GetIndicesStats returns statistics for observability indices.
func (a *Admin) GetIndicesStats(ctx context.Context) (map[string]any, error) {
	// Use configured index prefixes to cover all app-scoped indices.
	// Pattern: {prefix}-* matches both {prefix}-{appID}-{date} and legacy {prefix}-{date} formats.
	pattern := fmt.Sprintf("%s-*,%s-*,%s-*",
		a.config.Traces.IndexPrefix,
		a.config.Metrics.IndexPrefix,
		a.config.Logs.IndexPrefix,
	)
	return a.client.GetIndicesStats(ctx, pattern)
}

// GetNodesDiskStats returns aggregated total and available disk bytes from ES data nodes.
func (a *Admin) GetNodesDiskStats(ctx context.Context) (totalBytes int64, availableBytes int64, err error) {
	return a.client.GetNodesDiskStats(ctx)
}

// SetRetention updates the ILM policy for the given signal type's index.
// It modifies the delete phase min_age to the new retention duration.
func (a *Admin) SetRetention(ctx context.Context, indexPrefix string, retention time.Duration) error {
	if retention <= 0 {
		return fmt.Errorf("retention must be positive, got %v", retention)
	}

	policyName := indexPrefix + "-policy"
	a.logger.Info("Updating ILM retention policy",
		zap.String("policy", policyName),
		zap.Duration("retention", retention),
	)

	return a.createILMPolicy(ctx, policyName, retention)
}

// Purge deletes all documents older than `before` in the indices matching the given prefix.
// It uses delete_by_query with a timestamp range filter.
func (a *Admin) Purge(ctx context.Context, indexPrefix string, timestampField string, before time.Time) (int64, error) {
	indexPattern := indexPrefix + "-*"
	a.logger.Info("Purging data",
		zap.String("index_pattern", indexPattern),
		zap.String("timestamp_field", timestampField),
		zap.Time("before", before),
	)

	query := map[string]any{
		"range": map[string]any{
			timestampField: map[string]any{
				"lt": before.Format(time.RFC3339Nano),
			},
		},
	}

	deleted, err := a.client.DeleteByQuery(ctx, indexPattern, query)
	if err != nil {
		return 0, fmt.Errorf("purge delete_by_query failed: %w", err)
	}

	a.logger.Info("Purge completed",
		zap.String("index_pattern", indexPattern),
		zap.Int64("deleted_count", deleted),
	)
	return deleted, nil
}

// PurgeByApp deletes documents for a specific app_id older than `before`.
// Uses app-scoped index pattern for better performance.
func (a *Admin) PurgeByApp(ctx context.Context, indexPrefix string, timestampField string, appID string, before time.Time) (int64, error) {
	// Use app-scoped index pattern to limit search scope.
	sanitizedAppID := sanitizeAppID(appID)
	indexPattern := indexPrefix + "-" + sanitizedAppID + "-*"
	a.logger.Info("Purging data by app",
		zap.String("index_pattern", indexPattern),
		zap.String("app_id", appID),
		zap.Time("before", before),
	)

	query := map[string]any{
		"bool": map[string]any{
			"must": []map[string]any{
				{
					"range": map[string]any{
						timestampField: map[string]any{
							"lt": before.Format(time.RFC3339Nano),
						},
					},
				},
				{
					"term": map[string]any{
						"app_id": appID,
					},
				},
			},
		},
	}

	deleted, err := a.client.DeleteByQuery(ctx, indexPattern, query)
	if err != nil {
		return 0, fmt.Errorf("purge by app delete_by_query failed: %w", err)
	}

	a.logger.Info("Purge by app completed",
		zap.String("index_pattern", indexPattern),
		zap.String("app_id", appID),
		zap.Int64("deleted_count", deleted),
	)
	return deleted, nil
}

// createILMPolicy creates an ILM policy with the given retention.
func (a *Admin) createILMPolicy(ctx context.Context, name string, retention time.Duration) error {
	if retention <= 0 {
		retention = 7 * 24 * time.Hour // default 7 days
	}

	policy := map[string]any{
		"policy": map[string]any{
			"phases": map[string]any{
				"hot": map[string]any{
					"actions": map[string]any{
						"rollover": map[string]any{
							"max_size": "30gb",
							"max_age":  "1d",
						},
					},
				},
				"warm": map[string]any{
					"min_age": "2d",
					"actions": map[string]any{
						"shrink":     map[string]any{"number_of_shards": 1},
						"forcemerge": map[string]any{"max_num_segments": 1},
					},
				},
				"delete": map[string]any{
					"min_age": formatDuration(retention),
					"actions": map[string]any{
						"delete": map[string]any{},
					},
				},
			},
		},
	}
	return a.client.PutILMPolicy(ctx, name, policy)
}

// createTraceTemplate creates the trace index template.
func (a *Admin) createTraceTemplate(ctx context.Context) error {
	cfg := a.config.Traces
	template := map[string]any{
		"index_patterns": []string{cfg.IndexPrefix + "-*"},
		"template": map[string]any{
			"settings": map[string]any{
				"number_of_shards":               cfg.Shards,
				"number_of_replicas":             cfg.Replicas,
				"refresh_interval":               cfg.RefreshInterval,
				"index.lifecycle.name":           cfg.IndexPrefix + "-policy",
				"index.lifecycle.rollover_alias": cfg.IndexPrefix,
			},
			"mappings": map[string]any{
				"properties": map[string]any{
					"trace_id":        map[string]any{"type": "keyword"},
					"span_id":         map[string]any{"type": "keyword"},
					"parent_span_id":  map[string]any{"type": "keyword"},
					"operation_name":  map[string]any{"type": "keyword"},
					"service_name":    map[string]any{"type": "keyword"},
					"span_kind":       map[string]any{"type": "keyword"},
					"status_code":     map[string]any{"type": "keyword"},
					"status_message":  map[string]any{"type": "text"},
					"start_time":      map[string]any{"type": "date_nanos"},
					"end_time":        map[string]any{"type": "date_nanos"},
					"duration_us":     map[string]any{"type": "long"},
					"app_id":          map[string]any{"type": "keyword"},
					"attributes":      map[string]any{"type": "object", "dynamic": true},
					"resource": map[string]any{
						"properties": map[string]any{
							"service.name":      map[string]any{"type": "keyword"},
							"service.namespace": map[string]any{"type": "keyword"},
							"service.version":   map[string]any{"type": "keyword"},
							"host.name":         map[string]any{"type": "keyword"},
							"app_id":            map[string]any{"type": "keyword"},
						},
					},
					"events": map[string]any{
						"type": "nested",
						"properties": map[string]any{
							"name":       map[string]any{"type": "keyword"},
							"timestamp":  map[string]any{"type": "date_nanos"},
							"attributes": map[string]any{"type": "object", "dynamic": true},
						},
					},
					"links": map[string]any{
						"type": "nested",
						"properties": map[string]any{
							"trace_id": map[string]any{"type": "keyword"},
							"span_id":  map[string]any{"type": "keyword"},
						},
					},
				},
			},
		},
	}
	return a.client.PutIndexTemplate(ctx, cfg.IndexPrefix, template)
}

// createMetricTemplate creates the metric index template.
func (a *Admin) createMetricTemplate(ctx context.Context) error {
	cfg := a.config.Metrics
	template := map[string]any{
		"index_patterns": []string{cfg.IndexPrefix + "-*"},
		"template": map[string]any{
			"settings": map[string]any{
				"number_of_shards":               cfg.Shards,
				"number_of_replicas":             cfg.Replicas,
				"refresh_interval":               cfg.RefreshInterval,
				"index.lifecycle.name":           cfg.IndexPrefix + "-policy",
				"index.lifecycle.rollover_alias": cfg.IndexPrefix,
			},
			"mappings": map[string]any{
				"properties": map[string]any{
					"@timestamp":   map[string]any{"type": "date"},
					"metric_name":  map[string]any{"type": "keyword"},
					"metric_type":  map[string]any{"type": "keyword"},
					"value":        map[string]any{"type": "double"},
					"service_name": map[string]any{"type": "keyword"},
					"app_id":       map[string]any{"type": "keyword"},
					"labels":       map[string]any{"type": "flattened"},
					"resource":     map[string]any{"type": "flattened"},
					"histogram": map[string]any{
						"properties": map[string]any{
							"counts": map[string]any{"type": "long"},
							"values": map[string]any{"type": "double"},
						},
					},
				},
			},
		},
	}
	return a.client.PutIndexTemplate(ctx, cfg.IndexPrefix, template)
}

// createLogTemplate creates the log index template.
func (a *Admin) createLogTemplate(ctx context.Context) error {
	cfg := a.config.Logs
	template := map[string]any{
		"index_patterns": []string{cfg.IndexPrefix + "-*"},
		"template": map[string]any{
			"settings": map[string]any{
				"number_of_shards":               cfg.Shards,
				"number_of_replicas":             cfg.Replicas,
				"refresh_interval":               cfg.RefreshInterval,
				"index.lifecycle.name":           cfg.IndexPrefix + "-policy",
				"index.lifecycle.rollover_alias": cfg.IndexPrefix,
			},
			"mappings": map[string]any{
				"properties": map[string]any{
					"timestamp":       map[string]any{"type": "date_nanos"},
					"observed_time":   map[string]any{"type": "date_nanos"},
					"trace_id":        map[string]any{"type": "keyword"},
					"span_id":         map[string]any{"type": "keyword"},
					"severity":        map[string]any{"type": "keyword"},
					"severity_number": map[string]any{"type": "integer"},
					"body":            map[string]any{"type": "text", "analyzer": "standard"},
					"service_name":    map[string]any{"type": "keyword"},
					"app_id":          map[string]any{"type": "keyword"},
					"attributes":      map[string]any{"type": "object", "dynamic": true},
					"resource":        map[string]any{"type": "flattened"},
				},
			},
		},
	}
	return a.client.PutIndexTemplate(ctx, cfg.IndexPrefix, template)
}

// formatDuration converts a Go duration to an ES-compatible duration string.
func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	hours := int(d.Hours())
	if hours > 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return "1d"
}

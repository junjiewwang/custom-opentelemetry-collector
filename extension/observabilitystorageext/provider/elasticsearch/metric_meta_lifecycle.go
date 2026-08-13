// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// cleanStaleMetadata removes meta docs whose lastSeenAt is older than
// beforeUnixMilli. It is the single cleanup mechanism for the singleton
// metadata index, which is intentionally NOT ILM-managed (its template sets
// index.lifecycle.name:null) — without this it would grow without bound: a
// metric that stops reporting leaves a meta doc whose name ListMetricNames
// would keep returning forever.
//
// lastSeenAt is refreshed on every write (see metaCache's refresh cadence), so
// a doc older than the metric retention window is genuinely stale. Idempotent:
// on a fresh deployment (no meta index) delete_by_query returns
// ErrESIndexNotFound, which is swallowed here.
func cleanStaleMetadata(ctx context.Context, client *Client, prefix string, beforeUnixMilli int64, logger *zap.Logger) (int64, error) {
	indexName := metaIndexName(prefix)
	query := map[string]any{
		"range": map[string]any{
			FieldMetaLastSeenAt: map[string]any{"lt": beforeUnixMilli},
		},
	}

	deleted, err := client.DeleteByQuery(ctx, indexName, query)
	if err != nil {
		if errors.Is(err, ErrESIndexNotFound) {
			logger.Debug("metadata index absent, skipping stale cleanup", zap.String("index", indexName))
			return 0, nil
		}
		return 0, fmt.Errorf("clean stale metadata failed: %w", err)
	}

	if deleted > 0 {
		logger.Info("Cleaned stale metric metadata",
			zap.String("index", indexName),
			zap.Int64("deleted", deleted),
			zap.Int64("before_unix_milli", beforeUnixMilli),
		)
	}
	return deleted, nil
}

// CleanStaleMetadata removes meta docs older than beforeUnixMilli (epoch
// millis, matching the lastSeenAt field type).
func (a *Admin) CleanStaleMetadata(ctx context.Context, beforeUnixMilli int64) (int64, error) {
	return cleanStaleMetadata(ctx, a.client, a.config.Metrics.IndexPrefix, beforeUnixMilli, a.logger)
}

// CleanStaleMetadataByRetention derives the cutoff from the metric retention
// duration: a meta doc whose lastSeenAt predates "now - retention" is stale.
func (a *Admin) CleanStaleMetadataByRetention(ctx context.Context) (int64, error) {
	retention := a.config.Metrics.Retention
	if retention <= 0 {
		retention = 7 * 24 * time.Hour // fallback 7d (matches createILMPolicy default)
	}
	cutoff := time.Now().UnixMilli() - retention.Milliseconds()
	return a.CleanStaleMetadata(ctx, cutoff)
}

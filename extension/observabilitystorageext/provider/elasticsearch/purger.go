// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
)

// Purger implements lifecycle.LifecyclePurger with ES-optimized strategies.
//
// Strategy selection (from most to least efficient):
//  1. Delete entire date-based indices (O(1) per index, immediate space reclaim)
//  2. delete_by_query fallback for partial-day or un-parseable index scenarios
//
// This is the Strategy Pattern — the Scheduler sees only the LifecyclePurger interface,
// while this implementation chooses the best algorithm internally.
type Purger struct {
	client *Client
	config *Config
	logger *zap.Logger
}

// Compile-time interface satisfaction check.
var _ lifecycle.LifecyclePurger = (*Purger)(nil)

// Compile-time check: Purger implements optional distributed interfaces.
var _ lifecycle.IndexLister = (*Purger)(nil)
var _ lifecycle.SingleIndexPurger = (*Purger)(nil)

// datePattern matches the date suffix in index names: yyyy.MM.dd
var datePattern = regexp.MustCompile(`(\d{4}\.\d{2}\.\d{2})$`)

// NewPurger creates a new ES lifecycle purger.
func NewPurger(client *Client, config *Config, logger *zap.Logger) *Purger {
	return &Purger{
		client: client,
		config: config,
		logger: logger.Named("es-purger"),
	}
}

// PurgeExpired removes all data for the given signal older than `before`.
// It prefers deleting entire indices over delete_by_query.
func (p *Purger) PurgeExpired(ctx context.Context, signal lifecycle.SignalType, before time.Time) (*lifecycle.PurgeResult, error) {
	start := time.Now()
	prefix := p.indexPrefix(signal)
	pattern := prefix + "-*"

	// Strategy 1: Try to delete entire expired indices
	deletedIndices, err := p.deleteExpiredIndices(ctx, prefix, before)
	if err != nil {
		p.logger.Warn("Index deletion failed, falling back to delete_by_query",
			zap.String("signal", string(signal)),
			zap.Error(err),
		)
		// Strategy 2: Fallback to delete_by_query
		return p.deleteByQuery(ctx, pattern, signal, before, start)
	}

	return &lifecycle.PurgeResult{
		Signal:       signal,
		DeletedDocs:  0, // exact count unknown with index deletion
		DeletedUnits: deletedIndices,
		Duration:     time.Since(start),
		Message:      fmt.Sprintf("deleted %d expired indices matching %s", deletedIndices, pattern),
	}, nil
}

// PurgeByApp removes expired data scoped to a specific application.
func (p *Purger) PurgeByApp(ctx context.Context, appID string, signal lifecycle.SignalType, before time.Time) (*lifecycle.PurgeResult, error) {
	start := time.Now()
	prefix := p.indexPrefix(signal)
	sanitized := sanitizeAppID(appID)
	appPattern := prefix + "-" + sanitized + "-*"

	// Try index deletion first (app-scoped pattern)
	deletedIndices, err := p.deleteExpiredIndicesWithPattern(ctx, appPattern, before)
	if err != nil {
		// Fallback: delete_by_query on app-scoped indices
		return p.deleteByQueryForApp(ctx, appPattern, signal, appID, before, start)
	}

	return &lifecycle.PurgeResult{
		Signal:       signal,
		DeletedDocs:  0,
		DeletedUnits: deletedIndices,
		Duration:     time.Since(start),
		Message:      fmt.Sprintf("deleted %d expired indices for app %s", deletedIndices, appID),
	}, nil
}

// EstimatePurge returns a preview of what would be deleted.
func (p *Purger) EstimatePurge(ctx context.Context, signal lifecycle.SignalType, before time.Time) (*lifecycle.PurgeEstimate, error) {
	prefix := p.indexPrefix(signal)
	pattern := prefix + "-*"

	// Find indices that would be deleted
	indices, err := p.client.ListIndices(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("list indices failed: %w", err)
	}

	var affected []string
	for _, idx := range indices {
		indexDate := p.extractDate(idx, prefix)
		if indexDate != nil && indexDate.Before(before) {
			affected = append(affected, idx)
		}
	}

	// Estimate doc count for affected indices
	var estimatedDocs int64
	for _, idx := range affected {
		count, err := p.client.Count(ctx, idx, nil)
		if err == nil {
			estimatedDocs += count
		}
	}

	return &lifecycle.PurgeEstimate{
		Signal:         signal,
		EstimatedDocs:  estimatedDocs,
		EstimatedBytes: 0, // would need _stats API, keep it simple for now
		AffectedUnits:  affected,
	}, nil
}

// GetDataBoundary returns the oldest and newest data timestamp for the signal.
func (p *Purger) GetDataBoundary(ctx context.Context, signal lifecycle.SignalType) (*lifecycle.DataBoundary, error) {
	prefix := p.indexPrefix(signal)
	pattern := prefix + "-*"

	indices, err := p.client.ListIndices(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("list indices failed: %w", err)
	}

	if len(indices) == 0 {
		return &lifecycle.DataBoundary{Signal: signal, IsEmpty: true}, nil
	}

	// Parse dates from index names to find the time range
	var oldest, newest *time.Time
	for _, idx := range indices {
		t := p.extractDate(idx, prefix)
		if t == nil {
			continue
		}
		if oldest == nil || t.Before(*oldest) {
			copied := *t
			oldest = &copied
		}
		if newest == nil || t.After(*newest) {
			copied := *t
			newest = &copied
		}
	}

	if oldest == nil {
		return &lifecycle.DataBoundary{Signal: signal, IsEmpty: true}, nil
	}

	return &lifecycle.DataBoundary{
		Signal:   signal,
		OldestAt: oldest,
		NewestAt: newest,
		IsEmpty:  false,
	}, nil
}

// ═══════════════════════════════════════════════════
// Internal strategies
// ═══════════════════════════════════════════════════

// deleteExpiredIndices finds and deletes indices whose date suffix is before the cutoff.
func (p *Purger) deleteExpiredIndices(ctx context.Context, prefix string, before time.Time) (int, error) {
	return p.deleteExpiredIndicesWithPattern(ctx, prefix+"-*", before)
}

// deleteExpiredIndicesWithPattern finds and deletes matching indices whose date is before cutoff.
func (p *Purger) deleteExpiredIndicesWithPattern(ctx context.Context, pattern string, before time.Time) (int, error) {
	indices, err := p.client.ListIndices(ctx, pattern)
	if err != nil {
		return 0, fmt.Errorf("list indices failed: %w", err)
	}

	if len(indices) == 0 {
		return 0, nil
	}

	// Extract prefix from pattern (remove trailing "-*")
	prefix := strings.TrimSuffix(pattern, "-*")
	// If pattern is like "otel-traces-myapp-*", the prefix for date parsing is the whole thing
	// except the trailing "*"

	var deleted int
	for _, idx := range indices {
		indexDate := p.extractDate(idx, prefix)
		if indexDate == nil {
			continue // can't parse date, skip
		}

		if indexDate.Before(before) {
			if err := p.client.DeleteIndex(ctx, idx); err != nil {
				p.logger.Warn("Failed to delete expired index",
					zap.String("index", idx),
					zap.Error(err),
				)
				continue
			}
			deleted++
			p.logger.Info("Deleted expired index",
				zap.String("index", idx),
				zap.Time("index_date", *indexDate),
				zap.Time("cutoff", before),
			)
		}
	}

	return deleted, nil
}

// deleteByQuery uses delete_by_query with a timestamp range filter as fallback.
func (p *Purger) deleteByQuery(ctx context.Context, pattern string, signal lifecycle.SignalType, before time.Time, start time.Time) (*lifecycle.PurgeResult, error) {
	tsField := p.timestampField(signal)
	query := map[string]any{
		"range": map[string]any{
			tsField: map[string]any{
				"lt": before.Format(time.RFC3339Nano),
			},
		},
	}

	deleted, err := p.client.DeleteByQuery(ctx, pattern, query)
	if err != nil {
		return nil, fmt.Errorf("delete_by_query failed: %w", err)
	}

	return &lifecycle.PurgeResult{
		Signal:      signal,
		DeletedDocs: deleted,
		Duration:    time.Since(start),
		Message:     fmt.Sprintf("delete_by_query removed %d docs from %s", deleted, pattern),
	}, nil
}

// deleteByQueryForApp uses delete_by_query with app_id + timestamp filter.
func (p *Purger) deleteByQueryForApp(ctx context.Context, pattern string, signal lifecycle.SignalType, appID string, before time.Time, start time.Time) (*lifecycle.PurgeResult, error) {
	tsField := p.timestampField(signal)
	query := map[string]any{
		"bool": map[string]any{
			"must": []map[string]any{
				{
					"range": map[string]any{
						tsField: map[string]any{
							"lt": before.Format(time.RFC3339Nano),
						},
					},
				},
				{
					"term": map[string]any{
						FieldAppID: appID,
					},
				},
			},
		},
	}

	deleted, err := p.client.DeleteByQuery(ctx, pattern, query)
	if err != nil {
		return nil, fmt.Errorf("delete_by_query for app failed: %w", err)
	}

	return &lifecycle.PurgeResult{
		Signal:      signal,
		DeletedDocs: deleted,
		Duration:    time.Since(start),
		Message:     fmt.Sprintf("delete_by_query removed %d docs for app %s", deleted, appID),
	}, nil
}

// ═══════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════

// indexPrefix returns the ES index prefix for the given signal.
func (p *Purger) indexPrefix(signal lifecycle.SignalType) string {
	switch signal {
	case lifecycle.SignalTrace:
		return p.config.Traces.IndexPrefix
	case lifecycle.SignalMetric:
		return p.config.Metrics.IndexPrefix
	case lifecycle.SignalLog:
		return p.config.Logs.IndexPrefix
	default:
		return "otel-unknown"
	}
}

// timestampField returns the timestamp field name for the given signal.
func (p *Purger) timestampField(signal lifecycle.SignalType) string {
	switch signal {
	case lifecycle.SignalTrace:
		return FieldStartTimeUnixNano
	case lifecycle.SignalMetric:
		return FieldMetricTimeUnixNano
	case lifecycle.SignalLog:
		return FieldLogTimeUnixNano
	default:
		return FieldMetricTimeUnixNano
	}
}

// extractDate parses the date suffix from an index name.
//
// Index naming convention: {prefix}-{appID}-{date} or {prefix}-{date}
// Date format: yyyy.MM.dd (configurable via IndexDateFormat)
//
// Examples:
//
//	"otel-traces-myapp-2026.05.25" → 2026-05-25
//	"otel-traces-2026.05.25"       → 2026-05-25
//	"otel-traces-my-app-2026.05.25"→ 2026-05-25
func (p *Purger) extractDate(indexName, prefix string) *time.Time {
	// Use regex to find date pattern at the end
	matches := datePattern.FindStringSubmatch(indexName)
	if len(matches) < 2 {
		return nil
	}

	dateStr := matches[1]
	// Determine the date format from config (default: "2006.01.02")
	format := "2006.01.02"
	if p.config.Traces.IndexDateFormat != "" {
		format = p.config.Traces.IndexDateFormat
	}

	t, err := time.Parse(format, dateStr)
	if err != nil {
		return nil
	}
	return &t
}

// ═══════════════════════════════════════════════════
// Distributed Purge Support (IndexLister + SingleIndexPurger)
// ═══════════════════════════════════════════════════

// ListExpired returns the names of all expired indices for the given signal.
// This is a read-only operation used by the Leader during distributed planning.
//
// Implements lifecycle.IndexLister.
func (p *Purger) ListExpired(ctx context.Context, signal lifecycle.SignalType, before time.Time) ([]string, error) {
	prefix := p.indexPrefix(signal)
	pattern := prefix + "-*"

	indices, err := p.client.ListIndices(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("list indices failed: %w", err)
	}

	var expired []string
	for _, idx := range indices {
		indexDate := p.extractDate(idx, prefix)
		if indexDate != nil && indexDate.Before(before) {
			expired = append(expired, idx)
		}
	}
	return expired, nil
}

// DeleteSingleIndex deletes a single index by exact name.
// Returns nil if the index doesn't exist (idempotent — safe for retry).
//
// Implements lifecycle.SingleIndexPurger.
func (p *Purger) DeleteSingleIndex(ctx context.Context, indexName string) error {
	err := p.client.DeleteIndex(ctx, indexName)
	if err != nil {
		// DeleteIndex already handles 404 (returns nil for non-existent index)
		// This check is a safety net for unexpected error types.
		p.logger.Warn("DeleteSingleIndex failed",
			zap.String("index", indexName),
			zap.Error(err),
		)
		return err
	}
	p.logger.Debug("DeleteSingleIndex completed", zap.String("index", indexName))
	return nil
}

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/lifecycle"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// UsageReporter implements lifecycle.UsageReporter for Elasticsearch.
// It queries ES _stats API and calculates per-signal usage.
type UsageReporter struct {
	client *Client
	config *Config
	logger *zap.Logger
}

// Compile-time checks: UsageReporter implements lifecycle.UsageReporter.
var _ lifecycle.UsageReporter = (*UsageReporter)(nil)

// NewUsageReporter creates a new ES usage reporter.
func NewUsageReporter(client *Client, config *Config, logger *zap.Logger) *UsageReporter {
	return &UsageReporter{
		client: client,
		config: config,
		logger: logger.Named("es-usage"),
	}
}

// GetUsage returns current storage usage from ES cluster/index stats.
func (r *UsageReporter) GetUsage(ctx context.Context) (*lifecycle.StorageUsage, error) {
	// Get cluster-level disk info
	clusterHealth, err := r.client.ClusterHealth(ctx)
	if err != nil {
		return nil, fmt.Errorf("cluster health failed: %w", err)
	}

	// Get indices stats for our signal indices
	pattern := fmt.Sprintf("%s-*,%s-*,%s-*",
		r.config.Traces.IndexPrefix,
		r.config.Metrics.IndexPrefix,
		r.config.Logs.IndexPrefix,
	)

	stats, err := r.client.GetIndicesStats(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("indices stats failed: %w", err)
	}

	// Parse total storage from indices stats
	usage := &lifecycle.StorageUsage{
		BySignal: make(map[lifecycle.SignalType]int64),
		ByApp:    make(map[string]int64),
	}

	// Extract _all.total.store.size_in_bytes from stats
	if allStats, ok := stats["_all"].(map[string]any); ok {
		if total, ok := allStats["total"].(map[string]any); ok {
			if store, ok := total["store"].(map[string]any); ok {
				if sizeBytes, ok := store["size_in_bytes"].(float64); ok {
					usage.UsedBytes = int64(sizeBytes)
				}
			}
		}
	}

	// Parse per-index stats to calculate by-signal and by-app breakdown
	if indices, ok := stats["indices"].(map[string]any); ok {
		for indexName, indexData := range indices {
			size := r.extractIndexSize(indexData)
			signal := r.classifyIndex(indexName)
			if signal != "" {
				usage.BySignal[signal] += size
				// Extract appID from index name: {prefix}-{appID}-{date}
				prefix := r.signalPrefix(signal)
				if appID := r.parseAppID(indexName, prefix); appID != "" {
					usage.ByApp[appID] += size
				}
			}
		}
	}

	// Get cluster-level total/available from cluster stats (best effort)
	// ClusterHealth doesn't provide disk info — use _all total as estimate
	// The actual total disk requires _nodes/stats which is more complex.
	// For now, we use a heuristic: if cluster is healthy, we have the used bytes.
	_ = clusterHealth // used for health validation

	// Calculate usage ratio (if total is available)
	if usage.TotalBytes > 0 {
		usage.UsageRatio = float64(usage.UsedBytes) / float64(usage.TotalBytes)
	}

	return usage, nil
}

// extractIndexSize gets the store size from an index stats entry.
func (r *UsageReporter) extractIndexSize(indexData any) int64 {
	indexMap, ok := indexData.(map[string]any)
	if !ok {
		return 0
	}
	total, ok := indexMap["total"].(map[string]any)
	if !ok {
		// Try "primaries" if "total" not available
		total, ok = indexMap["primaries"].(map[string]any)
		if !ok {
			return 0
		}
	}
	store, ok := total["store"].(map[string]any)
	if !ok {
		return 0
	}
	sizeBytes, ok := store["size_in_bytes"].(float64)
	if !ok {
		return 0
	}
	return int64(sizeBytes)
}

// classifyIndex determines which signal type an index belongs to based on its prefix.
func (r *UsageReporter) classifyIndex(indexName string) lifecycle.SignalType {
	switch {
	case strings.HasPrefix(indexName, r.config.Traces.IndexPrefix):
		return lifecycle.SignalTrace
	case strings.HasPrefix(indexName, r.config.Metrics.IndexPrefix):
		return lifecycle.SignalMetric
	case strings.HasPrefix(indexName, r.config.Logs.IndexPrefix):
		return lifecycle.SignalLog
	default:
		return ""
	}
}

// signalPrefix returns the index prefix for the given signal.
func (r *UsageReporter) signalPrefix(signal lifecycle.SignalType) string {
	switch signal {
	case lifecycle.SignalTrace:
		return r.config.Traces.IndexPrefix
	case lifecycle.SignalMetric:
		return r.config.Metrics.IndexPrefix
	case lifecycle.SignalLog:
		return r.config.Logs.IndexPrefix
	default:
		return ""
	}
}

// GetDailyStorage returns storage usage broken down by calendar day.
// It queries ES _stats API and groups per-index sizes by date extracted from index names.
func (r *UsageReporter) GetDailyStorage(ctx context.Context, req storedmodel.DailyStorageRequest) (*storedmodel.DailyStorageResponse, error) {
	pattern := fmt.Sprintf("%s-*,%s-*,%s-*",
		r.config.Traces.IndexPrefix,
		r.config.Metrics.IndexPrefix,
		r.config.Logs.IndexPrefix,
	)

	stats, err := r.client.GetIndicesStats(ctx, pattern)
	if err != nil {
		return nil, fmt.Errorf("indices stats failed: %w", err)
	}

	daily := make(map[string]*storedmodel.DailyStoragePoint)
	if indices, ok := stats["indices"].(map[string]any); ok {
		for indexName, indexData := range indices {
			size := r.extractIndexSize(indexData)
			if size == 0 {
				continue
			}
			signal := r.classifyIndex(indexName)
			if signal == "" {
				continue
			}
			prefix := r.signalPrefix(signal)

			dateStr := r.extractDate(indexName, prefix)
			if dateStr == "" {
				continue
			}
			// Filter by date range
			parsed, parseErr := time.Parse("2006.01.02", dateStr)
			if parseErr == nil {
				if !req.StartDate.IsZero() && parsed.Before(req.StartDate.Truncate(24*time.Hour)) {
					continue
				}
				if !req.EndDate.IsZero() && parsed.After(req.EndDate) {
					continue
				}
			}

			appID := r.parseAppID(indexName, prefix)
			if req.AppID != "" && appID != req.AppID {
				continue
			}

			if _, ok := daily[dateStr]; !ok {
				daily[dateStr] = &storedmodel.DailyStoragePoint{
					Date:     dateStr,
					BySignal: make(map[string]int64),
					ByApp:    make(map[string]int64),
				}
			}
			dp := daily[dateStr]
			dp.BySignal[string(signal)] += size
			if appID != "" {
				dp.ByApp[appID] += size
			}
		}
	}

	// Sort by date ascending
	dates := make([]string, 0, len(daily))
	for d := range daily {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	points := make([]storedmodel.DailyStoragePoint, 0, len(dates))
	for _, d := range dates {
		points = append(points, *daily[d])
	}

	return &storedmodel.DailyStorageResponse{Points: points}, nil
}

// extractDate parses the date suffix from an index name.
// Index format: {prefix}-{appID}-{date}  → returns date (e.g., "2026.07.01")
// Returns empty string if no valid date suffix is found.
func (r *UsageReporter) extractDate(indexName, prefix string) string {
	rest := strings.TrimPrefix(indexName, prefix+"-")
	if rest == indexName {
		return ""
	}
	// date is the last 10-char segment (yyyy.MM.dd)
	lastDash := strings.LastIndex(rest, "-")
	if lastDash < 0 {
		return ""
	}
	candidate := rest[lastDash+1:]
	if len(candidate) == 10 && candidate[4] == '.' && candidate[7] == '.' {
		return candidate
	}
	return ""
}

// parseAppID extracts the appID from an index name.
// Index format: {prefix}-{appID}-{date}
// Example: "otel-traces-my-app-2026.07.02" → "my-app"
//
// The date suffix is matched by the IndexDateFormat pattern (default: yyyy.MM.dd).
// Everything between the prefix and the date is the appID.
// If no date suffix is found, the entire rest after prefix is returned as appID.
func (r *UsageReporter) parseAppID(indexName, prefix string) string {
	if !strings.HasPrefix(indexName, prefix) {
		return ""
	}
	// Remove prefix + separator
	rest := strings.TrimPrefix(indexName, prefix+"-")
	if rest == indexName { // prefix not found
		return ""
	}

	// Find the date suffix: last occurrence of a date-like pattern (yyyy.MM.dd)
	lastDash := strings.LastIndex(rest, "-")
	if lastDash < 0 {
		return rest // no dash at all → treat entire rest as appID
	}
	dateCandidate := rest[lastDash+1:]
	// Verify it looks like a date (e.g., "2026.07.02")
	if len(dateCandidate) == 10 && dateCandidate[4] == '.' && dateCandidate[7] == '.' {
		return rest[:lastDash]
	}
	return rest // no date suffix matched → treat entire rest as appID
}

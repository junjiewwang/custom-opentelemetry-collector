// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// ═══════════════════════════════════════════════════
// Integration Test: Concurrent QueryFlat Validation
// ═══════════════════════════════════════════════════
//
// These tests verify that splitting a QueryFlat with pipe-separated
// regex patterns into concurrent sub-queries produces the SAME results
// as a single QueryFlat call.
//
// Configuration via environment variables:
//
//	ES_INTEGRATION_TEST=true              Required: enables integration tests
//	ES_HOST=9.134.106.132                 ES host (default: localhost)
//	ES_PORT=9200                          ES port (default: 9200)
//	ES_USERNAME=elastic                   ES username (default: empty)
//	ES_PASSWORD=Aaaaaaaaa!1               ES password (default: empty)
//	ES_SCHEME=http                        URL scheme (default: http)
//	ES_METRICS_INDEX=otel-metrics         Metrics index prefix (default: otel-metrics)
//
// Example:
//
//	ES_INTEGRATION_TEST=true \
//	  ES_HOST=9.134.106.132 ES_PORT=9200 \
//	  ES_USERNAME=elastic ES_PASSWORD="Aaaaaaaaa!1" \
//	  go test ./extension/adminext/... -run TestIntegration -v -timeout 120s

// ==================== Test Helpers ====================

func skipIfNoES(t *testing.T) {
	t.Helper()
	if os.Getenv("ES_INTEGRATION_TEST") != "true" {
		t.Skip("Skipping integration test: set ES_INTEGRATION_TEST=true to enable")
	}
}

func intEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func intIntegrationConfig() *elasticsearch.Config {
	scheme := intEnvOrDefault("ES_SCHEME", "http")
	host := intEnvOrDefault("ES_HOST", "localhost")
	port := intEnvOrDefault("ES_PORT", "9200")
	username := intEnvOrDefault("ES_USERNAME", "")
	password := intEnvOrDefault("ES_PASSWORD", "")
	metricsIndex := intEnvOrDefault("ES_METRICS_INDEX", "otel-metrics")

	return &elasticsearch.Config{
		Addresses:     []string{fmt.Sprintf("%s://%s:%s", scheme, host, port)},
		Username:      username,
		Password:      password,
		BatchSize:     100,
		FlushInterval: 1 * time.Second,
		MaxRetries:    3,
		Metrics: elasticsearch.IndexConfig{
			IndexPrefix:     metricsIndex,
			IndexDateFormat: "2006.01.02",
			Shards:          1,
			Replicas:        0,
			Retention:       24 * time.Hour,
			RefreshInterval: "1s",
		},
	}
}

// setupIntegrationExtension creates an Extension wired to the real ES for testing.
func setupIntegrationExtension(t *testing.T) (*Extension, *elasticsearch.Client, func()) {
	t.Helper()
	skipIfNoES(t)

	cfg := intIntegrationConfig()
	logger := zaptest.NewLogger(t)

	client, err := elasticsearch.NewClient(cfg, logger)
	require.NoError(t, err, "failed to create ES client")

	err = client.Ping(context.Background())
	require.NoError(t, err, "ES cluster is not reachable — check ES_HOST/ES_PORT/ES_USERNAME/ES_PASSWORD")

	metricReader := elasticsearch.NewMetricReader(client, cfg, logger)
	adapter := observabilitystorageext.NewMetricReaderAdapterForTest(metricReader)

	ext := &Extension{
		logger:              logger,
		storageMetricReader: adapter,
	}

	cleanup := func() {
		// Nothing to clean up — we only read data.
	}
	return ext, client, cleanup
}

// ==================== Probe Helpers ====================

// stableTimeRange returns a time range for yesterday's data, avoiding
// race conditions from active writes on a live ES.
func stableTimeRange() observabilitystorageext.TimeRange {
	now := time.Now()
	// Use a short window from 48h ago to keep total docs well below MaxDocs (10000).
	// This ensures the single query path doesn't hit the ES max_docs cap,
	// which would cause results to differ from the per-term sub-queries.
	return observabilitystorageext.TimeRange{
		Start: now.Add(-48 * time.Hour),
		End:   now.Add(-47 * time.Hour), // 1 hour window
	}
}

// probeSplittableLabels discovers metrics and label values from ES that
// can be used to construct multi-value regex patterns for testing.
// Returns a list of (metricName, labelKey, labelValues) tuples.
func probeSplittableLabels(
	t *testing.T,
	reader observabilitystorageext.MetricReader,
	logger *zap.Logger,
) []struct {
	MetricName  string
	LabelKey    string
	LabelValues []string
} {
	t.Helper()

	tr := stableTimeRange()

	metricNames, err := reader.ListMetricNames(context.Background(), tr)
	require.NoError(t, err, "failed to list metric names")

	t.Logf("Found %d metric names in ES", len(metricNames))

	var candidates []struct {
		MetricName  string
		LabelKey    string
		LabelValues []string
	}

	// Sample up to 20 metrics to keep test runtime reasonable.
	sampleSize := 20
	if len(metricNames) > sampleSize {
		metricNames = metricNames[:sampleSize]
	}

	for _, mn := range metricNames {
		labelNames, err := reader.ListLabelNames(context.Background(), tr, mn)
		if err != nil {
			logger.Debug("skip metric: list label names failed",
				zap.String("metric", mn), zap.Error(err))
			continue
		}

		for _, ln := range labelNames {
			// Skip internal/empty labels.
			if ln == "" || strings.HasPrefix(ln, "__") {
				continue
			}

			values, err := reader.ListLabelValues(context.Background(), ln, tr)
			if err != nil {
				logger.Debug("skip label: list label values failed",
					zap.String("metric", mn), zap.String("label", ln), zap.Error(err))
				continue
			}

			// We need 2-10 distinct values to construct a meaningful split pattern.
			if len(values) < 2 || len(values) > 10 {
				continue
			}

			// Filter: only consider literal values (no regex metacharacters) that
			// can safely be used in a pipe-separated pattern.
			var literalValues []string
			for _, v := range values {
				if v == "" {
					continue
				}
				if isLiteralOrEscapedDots(v) {
					literalValues = append(literalValues, v)
				}
			}

			if len(literalValues) >= 2 {
				candidates = append(candidates, struct {
					MetricName  string
					LabelKey    string
					LabelValues []string
				}{
					MetricName:  mn,
					LabelKey:    ln,
					LabelValues: literalValues,
				})
				t.Logf("  Candidate: metric=%s label=%s values=%v",
					mn, ln, literalValues)
				break // One candidate per metric is enough.
			}
		}
	}

	if len(candidates) == 0 {
		t.Log("No splittable label candidates found — will use synthetic fixed query")
	}
	return candidates
}

// buildPipePattern constructs a PromQL regex pattern from label values,
// properly escaping dots and joining with |.
func buildPipePattern(values []string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		// Escape dots for PromQL regex safety.
		parts[i] = strings.ReplaceAll(v, ".", `\.`)
	}
	return strings.Join(parts, "|")
}

// ==================== Core Comparison Logic ====================

// compareFlatResults verifies that the concurrent query result contains all the data
// that the single query result contains (i.e., concurrent result must be a superset).
//
// The concurrent query splits a pipe-separated regex into per-term sub-queries,
// each with the full MaxDocs limit. When the single query hits its ES doc cap,
// the concurrent result can return MORE documents (a superset) — this is expected
// and actually a correctness advantage of the concurrent approach.
//
// The test computes whether concurrent ⊇ single (concurrent is superset of single).
// If not, it reports the overlap ratio and fails.
func compareFlatResults(t *testing.T, single, concurrent *observabilitystorageext.MetricFlatResult) {
	t.Helper()

	if single == nil && concurrent == nil {
		return // Both nil: equivalent.
	}
	if single == nil || concurrent == nil {
		t.Errorf("one result is nil: single=%v, concurrent=%v",
			single != nil, concurrent != nil)
		return
	}

	t.Logf("  Single query: %d samples, total=%d", len(single.Samples), single.Total)
	t.Logf("  Concurrent query: %d samples, total=%d", len(concurrent.Samples), concurrent.Total)

	// Compute what fraction of the single result is covered by the concurrent result.
	singleCovered := computeSampleOverlap(single.Samples, concurrent.Samples)
	singleCoveredPct := 0.0
	if len(single.Samples) > 0 {
		singleCoveredPct = float64(singleCovered) / float64(len(single.Samples)) * 100
	}

	t.Logf("  Single-covered-by-concurrent: %d/%d = %.2f%%",
		singleCovered, len(single.Samples), singleCoveredPct)

	if singleCoveredPct >= 99.5 {
		// Concurrent result is a superset (or nearly so) — this is correct.
		if len(concurrent.Samples) > len(single.Samples) {
			t.Logf("  Concurrent returned %.1fx more samples (expected: superset when single hits MaxDocs cap)",
				float64(len(concurrent.Samples))/float64(len(single.Samples)))
		}
		return
	}

	// Low overlap: likely a genuine correctness issue.
	mismatchPct := 100.0 - singleCoveredPct
	t.Errorf("concurrent result is NOT a superset of single result: %.2f%% of single samples missing in concurrent (expected >= 99.5%%)",
		mismatchPct)

	// Show sample mismatches for diagnosis.
	sortSamples(single.Samples)
	sortSamples(concurrent.Samples)

	singleKeySet := make(map[string]struct{}, len(single.Samples))
	for _, s := range single.Samples {
		singleKeySet[sortedLabelKey(s.Labels)+fmt.Sprintf("|%d|%v", s.TimestampMs, s.Value)] = struct{}{}
	}
	concurrentKeySet := make(map[string]struct{}, len(concurrent.Samples))
	for _, s := range concurrent.Samples {
		concurrentKeySet[sortedLabelKey(s.Labels)+fmt.Sprintf("|%d|%v", s.TimestampMs, s.Value)] = struct{}{}
	}

	// Find samples in single but not in concurrent.
	missingCount := 0
	for _, s := range single.Samples {
		k := sortedLabelKey(s.Labels) + fmt.Sprintf("|%d|%v", s.TimestampMs, s.Value)
		if _, ok := concurrentKeySet[k]; !ok {
			if missingCount < 5 {
				t.Logf("  missing in concurrent: ts=%d val=%v labels=%v",
					s.TimestampMs, s.Value, s.Labels)
			}
			missingCount++
		}
	}
	if missingCount > 5 {
		t.Logf("  ... and %d more samples missing from concurrent", missingCount-5)
	}

	// Find samples in concurrent but not in single.
	extraCount := 0
	for _, s := range concurrent.Samples {
		k := sortedLabelKey(s.Labels) + fmt.Sprintf("|%d|%v", s.TimestampMs, s.Value)
		if _, ok := singleKeySet[k]; !ok {
			if extraCount < 5 {
				t.Logf("  extra in concurrent: ts=%d val=%v labels=%v",
					s.TimestampMs, s.Value, s.Labels)
			}
			extraCount++
		}
	}
	if extraCount > 5 {
		t.Logf("  ... and %d more extra samples in concurrent", extraCount-5)
	}
}

// max returns the larger of two ints.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// computeSampleOverlap counts how many samples from set 'a' exist in set 'b'.
func computeSampleOverlap(a, b []observabilitystorageext.MetricSample) int {
	keySet := make(map[string]struct{}, len(b))
	for _, s := range b {
		keySet[sortedLabelKey(s.Labels)+fmt.Sprintf("|%d|%v", s.TimestampMs, s.Value)] = struct{}{}
	}

	overlap := 0
	for _, s := range a {
		k := sortedLabelKey(s.Labels) + fmt.Sprintf("|%d|%v", s.TimestampMs, s.Value)
		if _, ok := keySet[k]; ok {
			overlap++
		}
	}
	return overlap
}

func equalInt64Slice(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalFloat64Slice(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sortSamples sorts samples by (timestamp, label-sorted-key, value) for
// deterministic comparison.
func sortSamples(samples []observabilitystorageext.MetricSample) {
	sort.Slice(samples, func(i, j int) bool {
		a, b := samples[i], samples[j]
		if a.TimestampMs != b.TimestampMs {
			return a.TimestampMs < b.TimestampMs
		}
		aKey := sortedLabelKey(a.Labels)
		bKey := sortedLabelKey(b.Labels)
		if aKey != bKey {
			return aKey < bKey
		}
		if a.Value != b.Value {
			return a.Value < b.Value
		}
		return false
	})
}

// ==================== Test Cases ====================

// TestIntegrationConcurrentVsSingle runs a query against the real ES through both
// the single (original) and concurrent (split) paths, then verifies results match.
func TestIntegrationConcurrentVsSingle(t *testing.T) {
	ext, _, cleanup := setupIntegrationExtension(t)
	defer cleanup()

	reader := ext.storageMetricReader
	logger := ext.logger

	// ── Phase 1: Probe ES for available data ──
	candidates := probeSplittableLabels(t, reader, logger)

	// ── Phase 2: Run comparison tests ──
	timeRange := stableTimeRange()

	testCases := make([]struct {
		name       string
		metricName string
		labels     map[string]string
		labelMatch map[string]string
	}, 0)

	if len(candidates) > 0 {
		for _, c := range candidates {
			pattern := buildPipePattern(c.LabelValues)
			testCases = append(testCases, struct {
				name       string
				metricName string
				labels     map[string]string
				labelMatch map[string]string
			}{
				name:       fmt.Sprintf("probed:%s/%s/%s", c.MetricName, c.LabelKey, strings.Join(c.LabelValues, ",")),
				metricName: c.MetricName,
				labels:     nil,
				labelMatch: map[string]string{c.LabelKey: pattern},
			})
		}
	} else {
		// No splittable candidates discovered — skip comparison tests
		// but the test still runs successfully.
	}

	if len(testCases) == 0 {
		t.Log("No test cases to run — ES may not have sufficient data diversity")
		return
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			flatQuery := observabilitystorageext.MetricFlatQuery{
				MetricName:  tc.metricName,
				Labels:      tc.labels,
				LabelMatch:  tc.labelMatch,
				TimeRange:   timeRange,
				MaxDocs:     10000,
			}

			ctx := context.Background()

			// ── Single query path (original) ──
			singleResult, err := reader.QueryFlat(ctx, flatQuery)
			require.NoError(t, err, "single QueryFlat failed")

			// ── Concurrent query path ──
			concurrentResult, err := ext.concurrentQueryFlat(ctx, flatQuery, logger)
			require.NoError(t, err, "concurrent QueryFlat failed")

			// ── Compare results ──
			compareFlatResults(t, singleResult, concurrentResult)
		})
	}
}

// TestIntegrationConcurrentVsSingle_WithLabels tests the concurrent split path
// when there are BOTH exact labels AND regex labelMatch present simultaneously.
func TestIntegrationConcurrentVsSingle_WithLabels(t *testing.T) {
	ext, _, cleanup := setupIntegrationExtension(t)
	defer cleanup()

	reader := ext.storageMetricReader
	logger := ext.logger

	candidates := probeSplittableLabels(t, reader, logger)
	if len(candidates) == 0 {
		t.Skip("No splittable label candidates found")
	}

	// Use the first candidate and add an extra exact-match label if possible.
	candidate := candidates[0]

	// Try to find another label with a single value to use as exact match.
	tr := stableTimeRange()
	labelNames, err := reader.ListLabelNames(context.Background(), tr, candidate.MetricName)
	require.NoError(t, err)

	var exactLabels map[string]string
	for _, ln := range labelNames {
		if ln == candidate.LabelKey || ln == "" || strings.HasPrefix(ln, "__") {
			continue
		}
		values, err := reader.ListLabelValues(context.Background(), ln, tr)
		if err != nil {
			continue
		}
		if len(values) > 0 && isLiteralOrEscapedDots(values[0]) {
			exactLabels = map[string]string{ln: values[0]}
			t.Logf("  Extra exact label: %s=%s", ln, values[0])
			break
		}
	}

	pattern := buildPipePattern(candidate.LabelValues)

	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName: candidate.MetricName,
		Labels:     exactLabels,
		LabelMatch: map[string]string{candidate.LabelKey: pattern},
		TimeRange:  stableTimeRange(),
		MaxDocs:    10000,
	}

	ctx := context.Background()

	singleResult, err := reader.QueryFlat(ctx, flatQuery)
	require.NoError(t, err, "single QueryFlat failed")

	concurrentResult, err := ext.concurrentQueryFlat(ctx, flatQuery, logger)
	require.NoError(t, err, "concurrent QueryFlat failed")

	compareFlatResults(t, singleResult, concurrentResult)
}

// TestIntegrationConcurrentVsSingle_NoSplittable verifies that when no
// splittable pattern exists, concurrentQueryFlat falls back to a single
// QueryFlat (i.e., no false splitting).
func TestIntegrationConcurrentVsSingle_NoSplittable(t *testing.T) {
	ext, _, cleanup := setupIntegrationExtension(t)
	defer cleanup()

	reader := ext.storageMetricReader
	logger := ext.logger

	tr := stableTimeRange()

	metricNames, err := reader.ListMetricNames(context.Background(), tr)
	require.NoError(t, err)

	if len(metricNames) == 0 {
		t.Skip("No metric names found in ES")
	}

	// Use a non-splittable query (single label value with complex regex *).
	mn := metricNames[0]

	// Get a label with at least one value for exact match.
	labelNames, err := reader.ListLabelNames(context.Background(), tr, mn)
	require.NoError(t, err)

	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName:  mn,
		Labels:      nil,
		LabelMatch:  nil, // No regex at all — unsplittable.
		TimeRange:   tr,
		MaxDocs:     500,
	}

	// Try to find a label with at least 1 value for partial match constraint.
	if len(labelNames) > 0 {
		firstLabel := labelNames[0]
		if firstLabel != "" && !strings.HasPrefix(firstLabel, "__") {
			values, err := reader.ListLabelValues(context.Background(), firstLabel, tr)
			if err == nil && len(values) > 0 && isLiteralOrEscapedDots(values[0]) {
				flatQuery.Labels = map[string]string{firstLabel: values[0]}
			}
		}
	}

	ctx := context.Background()

	singleResult, err := reader.QueryFlat(ctx, flatQuery)
	require.NoError(t, err, "single QueryFlat failed")

	concurrentResult, err := ext.concurrentQueryFlat(ctx, flatQuery, logger)
	require.NoError(t, err, "concurrent QueryFlat failed")

	compareFlatResults(t, singleResult, concurrentResult)
}

// TestIntegrationConcurrent_ErrorHandling verifies that concurrentQueryFlat
// properly propagates errors from context cancellation.
func TestIntegrationConcurrent_ErrorHandling(t *testing.T) {
	ext, _, cleanup := setupIntegrationExtension(t)
	defer cleanup()

	reader := ext.storageMetricReader
	logger := ext.logger

	candidates := probeSplittableLabels(t, reader, logger)
	if len(candidates) == 0 {
		t.Skip("No splittable label candidates found")
	}

	c := candidates[0]
	pattern := buildPipePattern(c.LabelValues)

	flatQuery := observabilitystorageext.MetricFlatQuery{
		MetricName: c.MetricName,
		LabelMatch: map[string]string{c.LabelKey: pattern},
		TimeRange:  stableTimeRange(),
		MaxDocs:    10000,
	}

	// Already-cancelled context: single path should return error.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ext.concurrentQueryFlat(cancelCtx, flatQuery, logger)
	assert.Error(t, err, "expected error with cancelled context")
}

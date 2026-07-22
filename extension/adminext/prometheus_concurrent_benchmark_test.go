// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch"
	"go.uber.org/zap/zaptest"
)

// ═══════════════════════════════════════════════════
// Benchmarks: Single vs Concurrent QueryFlat
// ═══════════════════════════════════════════════════
//
// These benchmarks measure the performance difference between:
// 1. A single QueryFlat with pipe-separated regex in LabelMatch
// 2. Concurrent QueryFlat with sub-queries split by term values
//
// Configuration (same as integration test):
//
//	ES_INTEGRATION_TEST=true \
//	  ES_HOST=9.134.106.132 ES_PORT=9200 \
//	  ES_USERNAME=elastic ES_PASSWORD="Aaaaaaaaa!1" \
//	  go test ./extension/adminext/... -bench=BenchmarkIntegration -benchtime=5x -v -timeout 120s

// ═══════════════════════════════════════════════════
// Unit Benchmarks: Splitting & Merging Logic
// ═══════════════════════════════════════════════════

func BenchmarkFindSplitCandidate(b *testing.B) {
	labelMatch := map[string]string{
		"span_name": `opentelemetry\.proto\.collector\.trace\.v1\.TraceService/Export|opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export|user\.UserService/GetAllUserInfo|market\.MarketService/GetAllProductInfo|GET order-service-route`,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		findSplitCandidate(labelMatch, 2)
	}
}

func BenchmarkSplitPipeLiterals(b *testing.B) {
	pattern := `opentelemetry\.proto\.collector\.trace\.v1\.TraceService/Export|opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export|user\.UserService/GetAllUserInfo|market\.MarketService/GetAllProductInfo|GET order-service-route`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		splitPipeLiterals(pattern)
	}
}

func BenchmarkSplitPipeLiterals_Short(b *testing.B) {
	pattern := `GET /api/v1/orders|POST /api/v1/orders`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		splitPipeLiterals(pattern)
	}
}

func BenchmarkSplitPipeLiterals_NoSplit(b *testing.B) {
	pattern := `opentelemetry\.proto\.collector\.logs\.v1\.LogsService/Export`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		splitPipeLiterals(pattern)
	}
}

func BenchmarkCloneLabelsWithTerm(b *testing.B) {
	labels := map[string]string{
		"service_name": "my-svc",
		"status_code":  "STATUS_CODE_ERROR",
		"http_method":  "GET",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cloneLabelsWithTerm(labels, "span_name", "GET /api")
	}
}

func BenchmarkCloneLabelMatchWithout(b *testing.B) {
	labelMatch := map[string]string{
		"span_name":   "foo|bar",
		"http_method": "GET|POST",
		"status_code": "OK|ERROR",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cloneLabelMatchWithout(labelMatch, "span_name")
	}
}

func BenchmarkIsLiteralOrEscapedDots(b *testing.B) {
	tests := []string{
		`opentelemetry\.proto\.collector`,
		"GET order-service-route",
		`user\.UserService/Get`,
		"simple-string",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		isLiteralOrEscapedDots(tests[i%len(tests)])
	}
}

// ═══════════════════════════════════════════════════
// Integration Benchmarks: Single vs Concurrent ES queries
// ═══════════════════════════════════════════════════

// setupBenchExtension creates an Extension wired to ES for benchmarks.
func setupBenchExtension(b *testing.B) (*Extension, *elasticsearch.Client, func()) {
	b.Helper()
	if os.Getenv("ES_INTEGRATION_TEST") != "true" {
		b.Skip("Skipping integration benchmark: set ES_INTEGRATION_TEST=true to enable")
	}

	cfg := intIntegrationConfig()
	logger := zaptest.NewLogger(b)

	client, err := elasticsearch.NewClient(cfg, logger)
	if err != nil {
		b.Fatalf("failed to create ES client: %v", err)
	}

	err = client.Ping(context.Background())
	if err != nil {
		b.Fatalf("ES cluster is not reachable: %v", err)
	}

	metricReader := elasticsearch.NewMetricReader(client, cfg, logger)
	adapter := observabilitystorageext.NewMetricReaderAdapterForTest(metricReader)

	ext := &Extension{
		logger:              logger,
		storageMetricReader: adapter,
	}

	return ext, client, func() {}
}

// discoverBenchQuery discovers a splittable query from ES for benchmarking.
func discoverBenchQuery(b *testing.B, ext *Extension) observabilitystorageext.MetricFlatQuery {
	b.Helper()

	reader := ext.storageMetricReader
	tr := observabilitystorageext.TimeRange{
		Start: time.Now().Add(-24 * time.Hour),
		End:   time.Now(),
	}

	metricNames, err := reader.ListMetricNames(context.Background(), tr)
	if err != nil || len(metricNames) == 0 {
		b.Fatalf("no metrics found: err=%v, count=%d", err, len(metricNames))
	}

	// Search for a label with 3-5 distinct values for parallel benchmark.
	for _, mn := range metricNames {
		labelNames, err := reader.ListLabelNames(context.Background(), tr, mn)
		if err != nil {
			continue
		}

		for _, ln := range labelNames {
			if ln == "" || strings.HasPrefix(ln, "__") {
				continue
			}

			values, err := reader.ListLabelValues(context.Background(), ln, tr)
			if err != nil || len(values) < 3 || len(values) > 10 {
				continue
			}

			// Filter for literal values only.
			var literals []string
			for _, v := range values {
				if v != "" && isLiteralOrEscapedDots(v) {
					literals = append(literals, v)
				}
			}

			if len(literals) >= 3 {
				pattern := buildPipePattern(literals)
				return observabilitystorageext.MetricFlatQuery{
					MetricName: mn,
					LabelMatch: map[string]string{ln: pattern},
					TimeRange: observabilitystorageext.TimeRange{
						Start: time.Now().Add(-6 * time.Hour),
						End:   time.Now(),
					},
					MaxDocs: 10000,
				}
			}
		}
	}

	b.Fatal("no splittable query found — ES may not have enough diverse data")
	return observabilitystorageext.MetricFlatQuery{} // unreachable
}

// BenchmarkIntegration_SingleQuery measures a single QueryFlat with pipe-separated regex.
func BenchmarkIntegration_SingleQuery(b *testing.B) {
	ext, _, cleanup := setupBenchExtension(b)
	defer cleanup()

	flatQuery := discoverBenchQuery(b, ext)
	ctx := context.Background()

	b.Logf("Benchmark: metric=%s, labelMatch=%v", flatQuery.MetricName, flatQuery.LabelMatch)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ext.storageMetricReader.QueryFlat(ctx, flatQuery)
		if err != nil {
			b.Fatalf("single QueryFlat failed: %v", err)
		}
	}
}

// BenchmarkIntegration_ConcurrentQuery measures concurrent QueryFlat with term splitting.
func BenchmarkIntegration_ConcurrentQuery(b *testing.B) {
	ext, _, cleanup := setupBenchExtension(b)
	defer cleanup()

	flatQuery := discoverBenchQuery(b, ext)
	ctx := context.Background()

	b.Logf("Benchmark: metric=%s, labelMatch=%v", flatQuery.MetricName, flatQuery.LabelMatch)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ext.concurrentQueryFlat(ctx, flatQuery, ext.logger)
		if err != nil {
			b.Fatalf("concurrent QueryFlat failed: %v", err)
		}
	}
}

// BenchmarkIntegration_SingleVsConcurrent runs both paths side-by-side for
// direct comparison output.
func BenchmarkIntegration_SingleVsConcurrent(b *testing.B) {
	ext, _, cleanup := setupBenchExtension(b)
	defer cleanup()

	flatQuery := discoverBenchQuery(b, ext)
	ctx := context.Background()

	b.Logf("Benchmark: metric=%s, labelMatch=%v", flatQuery.MetricName, flatQuery.LabelMatch)

	b.Run("Single", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := ext.storageMetricReader.QueryFlat(ctx, flatQuery)
			if err != nil {
				b.Fatalf("single QueryFlat failed: %v", err)
			}
		}
	})

	b.Run("Concurrent", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := ext.concurrentQueryFlat(ctx, flatQuery, ext.logger)
			if err != nil {
				b.Fatalf("concurrent QueryFlat failed: %v", err)
			}
		}
	})
}

// BenchmarkIntegration_ConcurrentScaling benchmarks how performance scales with
// the number of concurrent terms (2, 3, 5, 8-way split).
func BenchmarkIntegration_ConcurrentScaling(b *testing.B) {
	ext, _, cleanup := setupBenchExtension(b)
	defer cleanup()

	reader := ext.storageMetricReader
	logger := ext.logger

	tr := observabilitystorageext.TimeRange{
		Start: time.Now().Add(-24 * time.Hour),
		End:   time.Now(),
	}

	metricNames, err := reader.ListMetricNames(context.Background(), tr)
	if err != nil || len(metricNames) == 0 {
		b.Fatalf("no metrics found: err=%v, count=%d", err, len(metricNames))
	}

	// Find a metric with a label that has at least 8 distinct literal values.
	var baseMetric, baseLabel string
	var allValues []string

	for _, mn := range metricNames {
		labelNames, err := reader.ListLabelNames(context.Background(), tr, mn)
		if err != nil {
			continue
		}
		for _, ln := range labelNames {
			if ln == "" || strings.HasPrefix(ln, "__") {
				continue
			}
			values, err := reader.ListLabelValues(context.Background(), ln, tr)
			if err != nil {
				continue
			}
			var literals []string
			for _, v := range values {
				if v != "" && isLiteralOrEscapedDots(v) {
					literals = append(literals, v)
				}
			}
			if len(literals) >= 8 {
				baseMetric = mn
				baseLabel = ln
				allValues = literals
				break
			}
		}
		if baseMetric != "" {
			break
		}
	}

	if baseMetric == "" {
		// Fallback: find any metric with at least 5 values.
		for _, mn := range metricNames {
			labelNames, err := reader.ListLabelNames(context.Background(), tr, mn)
			if err != nil {
				continue
			}
			for _, ln := range labelNames {
				if ln == "" || strings.HasPrefix(ln, "__") {
					continue
				}
				values, err := reader.ListLabelValues(context.Background(), ln, tr)
				if err != nil {
					continue
				}
				var literals []string
				for _, v := range values {
					if v != "" && isLiteralOrEscapedDots(v) {
						literals = append(literals, v)
					}
				}
				if len(literals) >= 5 {
					baseMetric = mn
					baseLabel = ln
					allValues = literals
					break
				}
			}
			if baseMetric != "" {
				break
			}
		}
	}

	if baseMetric == "" {
		b.Skip("No metric with >=5 distinct label values found")
	}

	b.Logf("Scaling benchmark: metric=%s, label=%s, values=%v",
		baseMetric, baseLabel, allValues)

	timeRange := observabilitystorageext.TimeRange{
		Start: time.Now().Add(-6 * time.Hour),
		End:   time.Now(),
	}

	// Test scaling from 2 to len(allValues) terms.
	for _, numTerms := range []int{2, 3, 5, 8} {
		if numTerms > len(allValues) {
			break
		}

		values := allValues[:numTerms]
		pattern := buildPipePattern(values)

		flatQuery := observabilitystorageext.MetricFlatQuery{
			MetricName: baseMetric,
			LabelMatch: map[string]string{baseLabel: pattern},
			TimeRange:  timeRange,
			MaxDocs:    10000,
		}

		ctx := context.Background()

		b.Run(fmt.Sprintf("Single_%dterms", numTerms), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, err := ext.storageMetricReader.QueryFlat(ctx, flatQuery)
				if err != nil {
					b.Fatalf("single QueryFlat failed: %v", err)
				}
			}
		})

		b.Run(fmt.Sprintf("Concurrent_%dterms", numTerms), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, err := ext.concurrentQueryFlat(ctx, flatQuery, logger)
				if err != nil {
					b.Fatalf("concurrent QueryFlat failed: %v", err)
				}
			}
		})
	}
}

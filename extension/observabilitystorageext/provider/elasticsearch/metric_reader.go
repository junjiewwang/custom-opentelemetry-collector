// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	esq "go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch/query"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.uber.org/zap"
)

// MetricReader implements metric query operations against Elasticsearch.
// Metrics are stored as per-datapoint documents with fields:
//
//	timeUnixMilli, name, type, serviceName, value, labels, resource
type MetricReader struct {
	searcher      Searcher
	config        *Config
	logger        *zap.Logger
	labelResolver MetricLabelResolver // stateless; resolves PromQL labels → ES fields (promotion + .keyword)
}

// NewMetricReader creates a new MetricReader instance.
func NewMetricReader(searcher Searcher, config *Config, logger *zap.Logger) *MetricReader {
	return &MetricReader{
		searcher: searcher,
		config:   config,
		logger:   logger.Named("metric-reader"),
	}
}

// Query executes an instant metric query, returning the latest value(s) before the given time.
// AppID is optional: when empty, queries all app indices (admin mode).
//
// Returns one data point per distinct label set (the latest sample for each
// series), matching the PromQL instant-vector contract consumed by the
// Prometheus handler. Grouping is done in Go (not via an ES terms aggregation
// on the "labels" object field, which ES silently returns empty for — object
// fields have no terms to aggregate).
func (r *MetricReader) Query(ctx context.Context, query MetricQuery) (*MetricResult, error) {
	// Use buildMetricFilter for consistent label/labelMatch handling across all query paths.
	var timeRange TimeRange
	if !query.Time.IsZero() {
		timeRange = TimeRange{End: query.Time}
	}
	filterResult := r.buildMetricFilter(query.MetricName, query.ServiceName, query.Labels, query.LabelMatch, timeRange,
		metricNegations{Not: query.LabelNot, NotMatch: query.LabelNotMatch})
	esQuery := filterResult.Query

	// Fetch matching documents sorted newest-first, then dedupe by label set in
	// Go. top_hits cannot group by an arbitrary object field, and a terms agg on
	// "labels" returns no buckets. Size is a per-query cap on documents scanned
	// for dedup; it bounds the result series count (latest-per-series) and the
	// worst-case scan when many series exist.
	const instantScanSize = 500
	searchReq := &SearchRequest{
		Query: esQuery,
		Size:  instantScanSize,
		Sort: []map[string]any{
			{FieldMetricTimeUnixMilli: map[string]any{"order": "desc"}},
		},
		Source: []string{
			FieldMetricTimeUnixMilli, FieldMetricValue, FieldMetricLabels,
			FieldServiceName,
			FieldMetricBucketCounts, FieldMetricExplicitBounds,
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPatternForRange(query.AppID, query.Time.Add(-24*time.Hour), query.Time.Add(24*time.Hour)), searchReq)
	if err != nil {
		return nil, fmt.Errorf("metric query failed: %w", err)
	}

	// Dedupe by label set: since hits are newest-first, the first hit seen for
	// a given label set is that series' latest sample.
	seen := make(map[string]struct{}, len(resp.Hits.Hits))
	result := &MetricResult{Data: make([]MetricDataPoint, 0, len(resp.Hits.Hits))}
	for _, hit := range resp.Hits.Hits {
		dp := r.hitToDataPoint(hit)
		key := labelsKey(dp.Labels)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		result.Data = append(result.Data, dp)
	}

	// Post-filter for complex regex patterns unsupported by ES flattened fields.
	result.Data = postFilterDataPoints(result.Data, filterResult.PostFilters)

	return result, nil
}

// labelsKey returns a stable string key for a metric label set, for dedup.
// Empty/nil labels map to "". Keys are sorted for determinism.
func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}

// queryDirect was removed: Query no longer uses an object-field aggregation
// that could fail, so the direct-search fallback is unnecessary. Query itself
// now fetches newest-first and dedupes by label set in Go.

// QueryRange executes a range metric query, returning time series data.
// AppID is optional: when empty, queries all app indices (admin mode).
//
// Supports InfluxQL-aligned semantics:
//   - aggregation: avg, sum, max, min, count, last, first, p50, p90, p95, p99
//   - groupBy: composite aggregation by label keys
//   - fill: null, none, 0, previous, linear
//   - seriesLimit: max number of series to return
func (r *MetricReader) QueryRange(ctx context.Context, query MetricRangeQuery) (*MetricRangeResult, error) {
	// 1. Validate and get the aggregation function.
	aggFunc, err := GetAggregation(query.Aggregation)
	if err != nil {
		return nil, err
	}

	// 2. Build ES query filter (metric name + labels + labelMatch + service + time range).
	filterResult := r.buildQueryFilter(query)
	esQuery := filterResult.Query

	// 3. Calculate interval for date_histogram.
	// The time-axis bucket cap is ESHardMaxBuckets (65535) for BOTH grouped and
	// flat queries. Empirically verified against the real ES cluster: a composite
	// aggregation with a nested date_histogram counts buckets PER composite key
	// (at most 65535 time buckets per key), NOT as composite.size × time_buckets
	// as previously assumed. The prior `maxBuckets = ESHardMaxBuckets/seriesLimit`
	// (65535/100 = 655) was based on that false accumulation model and caused
	// point-collapse for >6h windows (a 24h@60s query was clamped to 132s step,
	// returning 655 pts instead of 1441).
	//
	// NOTE: seriesLimit (the composite `size`) is the hard cap on returned series
	// per request (parseGroupedResult has no after_key pagination). It is
	// orthogonal to the time-axis bucket cap and is NOT used here. Metrics with
	// >100 series are silently truncated at seriesLimit — a separate, pre-existing
	// limitation that this fix intentionally leaves untouched.
	maxBuckets := esq.DefaultMaxBucketsFlat
	interval := r.calculateInterval(query.TimeRange, query.Step, maxBuckets)

	// 4. Determine min_doc_count based on fill strategy.
	minDocCount := 0
	if query.Fill == "none" {
		minDocCount = 1
	}

	// 5. Build ES aggregations (with or without groupBy).
	aggs := r.buildAggregation(query.GroupBy, interval, aggFunc, minDocCount, query.SeriesLimit, query.MissingBucket)

	searchReq := &SearchRequest{
		Query:        esQuery,
		Size:         0,
		Aggregations: aggs,
	}

	resp, err := r.searcher.Search(ctx, r.routeIndexPattern(query.AppID, query.TimeRange.Start, query.TimeRange.End), searchReq)
	if err != nil {
		if strings.Contains(err.Error(), "too_many_buckets") {
			return nil, fmt.Errorf("metric range query: time range too large for the given step, try a larger step or shorter time range")
		}
		return nil, fmt.Errorf("metric range query failed: %w", err)
	}

	// 6. Parse the result (simple or grouped).
	result, err := r.parseQueryRangeResult(resp, len(query.GroupBy) > 0, aggFunc)
	if err != nil {
		return nil, err
	}

	// 7. Post-filter series for unsupported regex patterns.
	result.Data = postFilterSeries(result.Data, filterResult.PostFilters)

	// 8. Apply fill strategy (post-processing).
	fillFn := GetFillStrategy(query.Fill)
	for i := range result.Data {
		result.Data[i].Values = fillFn(result.Data[i].Values)
	}

	// 9. Normalize labels (ensure non-nil).
	for i := range result.Data {
		if result.Data[i].Labels == nil {
			result.Data[i].Labels = make(map[string]string)
		}
	}

	return result, nil
}

// ListMetricNames returns all available metric names within the time range.
//
// Prefers the singleton metadata index ({prefix}-meta): reading a small table
// is O(metric count) and does not touch the fielddata that a terms aggregation
// over the full data index requires (the ES 429 circuit_breaker trigger). When
// the meta index does not exist (fresh deployment, or the write path has not
// yet populated it), falls back to the terms aggregation.
func (r *MetricReader) ListMetricNames(ctx context.Context, timeRange TimeRange) ([]string, error) {
	if names, ok, err := r.listMetricNamesFromMeta(ctx); ok {
		return names, err
	} else if err != nil {
		r.logger.Warn("meta metric names lookup failed, falling back to aggregation", zap.Error(err))
	}

	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"metric_names": map[string]any{
				"terms": map[string]any{
					"field": FieldName,
					"size":  5000,
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.rawIndexPatternForRange(timeRange.Start, timeRange.End), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list metric names failed: %w", err)
	}

	raw, ok := resp.Aggregations["metric_names"]
	if !ok {
		return nil, nil
	}
	names, err := esq.ParseTermsAgg(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metric_names aggregation: %w", err)
	}
	return names, nil
}

// metaSearchAll runs a match-all search over the singleton meta index and
// returns the parsed hits. A missing index yields (nil, ErrESIndexNotFound);
// the caller distinguishes that from a genuine error to decide fallback.
func (r *MetricReader) metaSearchAll(ctx context.Context, source []string) (*SearchResponse, error) {
	req := &SearchRequest{
		Query:  map[string]any{"match_all": map[string]any{}},
		Size:   10000, // meta docs are low-cardinality; one page is enough
		Source: source,
	}
	return r.searcher.Search(ctx, metaIndexName(r.config.Metrics.IndexPrefix), req)
}

func (r *MetricReader) listMetricNamesFromMeta(ctx context.Context) ([]string, bool, error) {
	resp, err := r.metaSearchAll(ctx, []string{FieldName})
	if err != nil {
		return nil, false, err
	}
	seen := make(map[string]struct{}, len(resp.Hits.Hits))
	names := make([]string, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		var doc struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(hit.Source, &doc); err != nil || doc.Name == "" {
			continue
		}
		if _, dup := seen[doc.Name]; dup {
			continue
		}
		seen[doc.Name] = struct{}{}
		names = append(names, doc.Name)
	}
	sort.Strings(names)
	return names, true, nil
}

// ListMetricTypes returns each metric name mapped to its stored OTel-derived
// type ("gauge", "counter", "histogram", "summary").
//
// The type is written per data point by storedmodel (monotonic Sum → counter,
// Gauge and non-monotonic Sum → gauge, ...), so a sub-aggregation recovers it
// without scanning documents.
//
// Reports the type of the NEWEST data point, not the most frequent one. When a
// metric is re-typed — as every non-monotonic Sum was, on being corrected from
// counter to gauge — the old documents outnumber the new ones for as long as
// the retention window holds them. Picking by frequency kept serving the stale
// type indefinitely, and Grafana Metrics Drilldown then wrapped gauges like
// jvm.thread.count in rate(). Grafana sends no start/end to /metadata, so the
// query spans all history and the stale majority always won.
func (r *MetricReader) ListMetricTypes(ctx context.Context, timeRange TimeRange) (map[string]storedmodel.MetricMeta, error) {
	if types, ok, err := r.listMetricTypesFromMeta(ctx); ok {
		return types, err
	} else if err != nil {
		r.logger.Warn("meta metric types lookup failed, falling back to aggregation", zap.Error(err))
	}

	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"metric_names": map[string]any{
				"terms": map[string]any{
					"field": FieldName,
					"size":  5000,
				},
				"aggs": map[string]any{
					"metric_type": map[string]any{
						"top_hits": map[string]any{
							"size":    1,
							"sort":    []map[string]any{{FieldMetricTimeUnixMilli: map[string]any{"order": "desc"}}},
							"_source": []string{FieldMetricType, FieldMetricUnit},
						},
					},
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.rawIndexPatternForRange(timeRange.Start, timeRange.End), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list metric types failed: %w", err)
	}

	raw, ok := resp.Aggregations["metric_names"]
	if !ok {
		return nil, nil
	}

	var agg struct {
		Buckets []struct {
			Key        string `json:"key"`
			MetricType struct {
				Hits struct {
					Hits []struct {
						Source struct {
							Type string `json:"type"`
							Unit string `json:"unit"`
						} `json:"_source"`
					} `json:"hits"`
				} `json:"hits"`
			} `json:"metric_type"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("failed to parse metric_names aggregation: %w", err)
	}

	out := make(map[string]storedmodel.MetricMeta, len(agg.Buckets))
	for _, b := range agg.Buckets {
		if hits := b.MetricType.Hits.Hits; len(hits) > 0 {
			out[b.Key] = storedmodel.MetricMeta{Type: hits[0].Source.Type, Unit: hits[0].Source.Unit}
		} else {
			out[b.Key] = storedmodel.MetricMeta{}
		}
	}
	return out, nil
}

// listMetricTypesFromMeta reads type/unit from the meta index. It is the
// preferred path over the terms+top_hits aggregation: the meta doc stores the
// last-writer-wins type/unit, which is exactly the "newest data point" semantics
// ListMetricTypes documents, without scanning the data index.
func (r *MetricReader) listMetricTypesFromMeta(ctx context.Context) (map[string]storedmodel.MetricMeta, bool, error) {
	resp, err := r.metaSearchAll(ctx, []string{FieldName, FieldMetricType, FieldMetricUnit})
	if err != nil {
		return nil, false, err
	}
	out := make(map[string]storedmodel.MetricMeta, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		var doc struct {
			Name string `json:"name"`
			Type string `json:"type"`
			Unit string `json:"unit"`
		}
		if err := json.Unmarshal(hit.Source, &doc); err != nil || doc.Name == "" {
			continue
		}
		out[doc.Name] = storedmodel.MetricMeta{Type: doc.Type, Unit: doc.Unit}
	}
	return out, true, nil
}

// ListLabelNames returns label names for the specified metric within the time range.
// If metricName is empty, all label names across all metrics are returned.
//
// Prefers the meta index, which stores the exact label-key union per metric
// (scoped: a term query on name) or across all metrics (unscoped: union of all
// docs). This is strictly more accurate than sampling 2000 data documents and
// avoids the fielddata scan. Falls back to the sample when the meta index is
// absent.
func (r *MetricReader) ListLabelNames(ctx context.Context, timeRange TimeRange, metricName string) ([]string, error) {
	if names, ok, err := r.listLabelNamesFromMeta(ctx, metricName); ok {
		return names, err
	} else if err != nil {
		r.logger.Warn("meta label names lookup failed, falling back to sample", zap.Error(err))
	}

	// Sampling the newest documents is a heuristic, not an exhaustive scan: the
	// label set is the union of whatever these hits happen to carry. Keep the
	// sample large, because the unscoped call competes across every metric and
	// the highest-frequency writers otherwise crowd out rarer labels entirely —
	// the unscoped list came back MISSING labels that the metric-scoped list
	// returned. A larger sample narrows that gap; it cannot close it.
	const labelSampleSize = 2000
	searchReq := &SearchRequest{
		Query:  r.timeRangeQuery(timeRange),
		Size:   labelSampleSize,
		Source: []string{FieldMetricLabels, FieldServiceName},
		Sort: []map[string]any{
			{FieldMetricTimeUnixMilli: map[string]any{"order": "desc"}},
		},
	}

	// Filter by metric name if specified.
	if metricName != "" {
		searchReq.Query = map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{
					searchReq.Query,
					{"term": map[string]any{FieldName: metricName}},
				},
			},
		}
	}

	resp, err := r.searcher.Search(ctx, r.indexPatternForRange("", timeRange.Start, timeRange.End), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list label names failed: %w", err)
	}

	labelSet := make(map[string]struct{})
	for _, hit := range resp.Hits.Hits {
		var doc struct {
			ServiceName string         `json:"serviceName"`
			Labels      map[string]any `json:"labels"`
		}
		if err := json.Unmarshal(hit.Source, &doc); err == nil {
			for k := range doc.Labels {
				labelSet[k] = struct{}{}
			}
			// service_name lives in the top-level serviceName field, not "labels".
			if doc.ServiceName != "" {
				labelSet["service_name"] = struct{}{}
			}
		}
	}

	names := make([]string, 0, len(labelSet))
	for k := range labelSet {
		names = append(names, k)
	}
	return names, nil
}

// listLabelNamesFromMeta reads label keys from the meta index. When metricName
// is empty it unions labelKeys across all meta docs; otherwise it scopes to
// that metric name. The scoped path is a term query on name, not a single
// GetDocument, because ListLabelNames has no appID — the same metric name may
// exist under multiple appIDs and their label keys must be unioned.
func (r *MetricReader) listLabelNamesFromMeta(ctx context.Context, metricName string) ([]string, bool, error) {
	query := map[string]any{"match_all": map[string]any{}}
	if metricName != "" {
		query = map[string]any{
			"term": map[string]any{FieldName: metricName},
		}
	}
	req := &SearchRequest{
		Query:  query,
		Size:   10000,
		Source: []string{FieldMetaLabelKeys},
	}
	resp, err := r.searcher.Search(ctx, metaIndexName(r.config.Metrics.IndexPrefix), req)
	if err != nil {
		return nil, false, err
	}

	labelSet := make(map[string]struct{})
	for _, hit := range resp.Hits.Hits {
		var doc struct {
			LabelKeys []string `json:"labelKeys"`
		}
		if err := json.Unmarshal(hit.Source, &doc); err != nil {
			continue
		}
		for _, k := range doc.LabelKeys {
			labelSet[k] = struct{}{}
		}
	}
	names := make([]string, 0, len(labelSet))
	for k := range labelSet {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, true, nil
}

// ListLabelValues returns values for a specific label within the time range,
// across all metrics.
func (r *MetricReader) ListLabelValues(ctx context.Context, label string, timeRange TimeRange) ([]string, error) {
	return r.ListLabelValuesForMetric(ctx, label, "", timeRange)
}

// ListLabelValuesForMetric returns values for a specific label, restricted to a
// single metric. An empty metricName means "across all metrics".
//
// Scoping matters for Prometheus /label/{name}/values?match[]=...: Grafana's
// breakdown UI uses it to populate the value picker, and an unscoped list offers
// values that exist on some other metric entirely and yield an empty panel.
func (r *MetricReader) ListLabelValuesForMetric(ctx context.Context, label, metricName string, timeRange TimeRange) ([]string, error) {
	// Read the stored `labels` object from a sample of documents and extract the
	// requested label's value in Go. This mirrors ListLabelNames (which the
	// /labels endpoint uses) and the `sum by (label)` path (which goes through
	// QueryFlat) — both read the labels object directly and work reliably.
	//
	// We deliberately do NOT use an ES `terms` aggregation on `labels.<key>.keyword`:
	// in the running indices that aggregation returns empty buckets (or
	// illegal_argument for text fields) for string-valued labels, which blanked
	// the Grafana breakdown value picker even though the values are present on
	// the documents. Reading the typed labels object also handles numeric and
	// boolean attributes uniformly (the metricLabels decoder yields their string
	// form, e.g. "200", "true"), so a single path covers every label type.
	//
	// Sampling the newest documents is a heuristic, not an exhaustive scan — the
	// value set is the union of whatever these hits happen to carry. Keep the
	// sample large (see ListLabelNames for the same trade-off); it narrows the
	// gap vs an exhaustive scan but cannot close it.
	const labelSampleSize = 2000

	// Normalize the Prometheus/OTel label key to the ES storage form (dots →
	// underscores), matching how the writer stored it via SanitizeMetricKey and
	// how ListLabelNames reads the raw keys back.
	esKey := storedmodel.SanitizeMetricKey(label)

	esQuery := r.timeRangeQuery(timeRange)
	if metricName != "" {
		esQuery = map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{
					esQuery,
					{"term": map[string]any{FieldName: metricName}},
				},
			},
		}
	}

	searchReq := &SearchRequest{
		Query:  esQuery,
		Size:   labelSampleSize,
		Source: []string{FieldMetricLabels, FieldServiceName},
		Sort: []map[string]any{
			{FieldMetricTimeUnixMilli: map[string]any{"order": "desc"}},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPatternForRange("", timeRange.Start, timeRange.End), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list label values failed: %w", err)
	}

	valueSet := make(map[string]struct{})
	for _, hit := range resp.Hits.Hits {
		var doc struct {
			ServiceName string       `json:"serviceName"`
			Labels      metricLabels `json:"labels"`
		}
		if err := json.Unmarshal(hit.Source, &doc); err == nil {
			// service_name is sourced from the top-level serviceName field, not
			// the "labels" object.
			allLabels := mergeServiceName(doc.Labels, doc.ServiceName)
			if v, ok := allLabels[esKey]; ok && v != "" {
				valueSet[v] = struct{}{}
			}
		}
	}

	values := make([]string, 0, len(valueSet))
	for v := range valueSet {
		values = append(values, v)
	}
	return values, nil
}

// ==================== Internal Helpers ====================

// indexPattern returns the ES index pattern for metrics.
// When appID is provided, returns an app-scoped pattern; otherwise falls back to global wildcard.
func (r *MetricReader) indexPattern(appID ...string) string {
	id := ""
	if len(appID) > 0 {
		id = appID[0]
	}
	return esq.IndexPattern(r.config.Metrics.IndexPrefix, id)
}

// indexPatternForRange narrows the index pattern to daily partitions overlapping
// [start,end]. See esq.IndexPatternForRange — avoids scanning every historical
// day's shards for a short-range query (the ES heap OOM trigger).
func (r *MetricReader) indexPatternForRange(appID string, start, end time.Time) string {
	return esq.IndexPatternForRange(r.config.Metrics.IndexPrefix, appID, start, end)
}

// rawIndexPatternForRange is like indexPatternForRange but EXCLUDES rollup-tier
// indices and the metadata index via ES negative index patterns. Metadata
// listing (ListMetricNames / ListMetricTypes) must only scan raw indices:
// including rollup indices in a terms-aggregation over the wildcard
// "{prefix}-*-{date}" blew ES fielddata (429 circuit_breaker_exception, ~1.4GB
// > 1.3GB limit) because rollup indices carry the same "name" field and were
// being aggregated again.
//
// The meta exclusion is a safety net for the zero-time fallback: when start or
// end is zero, indexPatternForRange degrades to the bare wildcard "{prefix}-*",
// which would match "{prefix}-meta". (The date-partitioned patterns emitted for
// a real range never match the meta index, since "-meta" is not a date suffix.)
func (r *MetricReader) rawIndexPatternForRange(start, end time.Time) string {
	base := r.indexPatternForRange("", start, end)
	if base == "" {
		return base
	}
	// Append negative patterns for rollup indices and the metadata index. ES
	// excludes indices matching a "-" prefixed pattern. The rollup index format
	// is "{prefix}-rollup-{tier}-{appID}-{date}" (see getRollupIndexName), and
	// the metadata index is the singleton "{prefix}-meta" (see metaIndexName).
	prefix := r.config.Metrics.IndexPrefix
	exclude := "-" + prefix + "-rollup-*,-" + prefix + "-meta"
	return base + "," + exclude
}

// routeIndexPattern selects the rollup tier based on query time span, then
// narrows to daily partitions. Windows >2h query the 5m rollup tier (which has
// pre-aggregated docs), windows ≤2h query the raw tier. The rollup tier uses
// the same appID/date partition structure under a "-rollup-5m-" prefix.
//
// Routing coherence invariant: a window must ONLY route to the rollup tier if
// its ENTIRE span is "stabilized" (rollup data is guaranteed to exist). The
// rollup engine only aggregates indices older than RollupReadyAfter (default
// 24h), so today's raw index has no rollup counterpart yet. A window whose end
// is within now-RollupReadyAfter must therefore fall back to raw, otherwise a
// 6h window on today routes to a rollup index that does not exist → empty.
//
// This routing is transparent to the PromQL layer — the same reader methods
// (QueryRange/QueryFlat) call routeIndexPattern instead of indexPatternForRange,
// and the ES responses are shape-compatible.
func (r *MetricReader) routeIndexPattern(appID string, start, end time.Time) string {
	const rollupThreshold = 2 * time.Hour
	readyAfter := r.config.RollupReadyAfter
	if readyAfter <= 0 {
		readyAfter = 24 * time.Hour // match RollupEngine's default
	}
	// Only route to rollup when the window is fully stabilized (its end is at
	// least RollupReadyAfter in the past). Otherwise fall back to raw.
	if r.config.RollupEnabled && end.Sub(start) > rollupThreshold && end.Before(time.Now().Add(-readyAfter)) {
		return esq.IndexPatternForRange(r.config.Metrics.IndexPrefix+"-rollup-5m", appID, start, end)
	}
	return esq.IndexPatternForRange(r.config.Metrics.IndexPrefix, appID, start, end)
}

// buildMetricQuery constructs the ES query for metric search.
func (r *MetricReader) buildMetricQuery(metricName string, labels map[string]string, serviceName string) map[string]any {
	qb := esq.NewBuilder()

	if metricName != "" {
		qb.Term(FieldName, metricName)
	}
	if serviceName != "" {
		qb.Term(FieldServiceName, serviceName)
	}
	for k, v := range labels {
		field := aggregatableField("metric", fmt.Sprintf(FieldMetricLabels+".%s", k))
		qb.Term(field, v)
	}

	return qb.Build()
}

// buildQueryFilter builds the complete ES bool query for a MetricRangeQuery,
// including metric name, service, labels, labelMatch (regex), and time range.
// Uses buildMetricFilter for consistent regex→ES query translation.
func (r *MetricReader) buildQueryFilter(query MetricRangeQuery) metricFilterResult {
	return r.buildMetricFilter(query.MetricName, query.ServiceName, query.Labels, query.LabelMatch, query.TimeRange,
		metricNegations{Not: query.LabelNot, NotMatch: query.LabelNotMatch})
}

// buildAggregation constructs the ES aggregation for metric range queries.
// Uses simple date_histogram when groupBy is empty, or composite+date_histogram when grouping.
// missingBucket controls whether documents lacking a grouped label are dropped
// (false, for explicit "by (label)" queries) or included as null-key buckets
// (true, for bare-metric selectors that must return every series).
func (r *MetricReader) buildAggregation(groupBy []string, interval string, aggFunc *AggregationFunc, minDocCount int, seriesLimit int, missingBucket bool) map[string]any {
	// The sub-aggregation for each time bucket.
	timeAgg := map[string]any{
		"date_histogram": map[string]any{
			"field":          FieldMetricTimeUnixMilli,
			"fixed_interval": interval,
			"min_doc_count":  minDocCount,
		},
		"aggs": map[string]any{
			"agg_value": aggFunc.Build(FieldMetricValue),
		},
	}

	if len(groupBy) == 0 {
		// Simple case: single time_series aggregation.
		return map[string]any{"time_series": timeAgg}
	}

	// Grouped case: composite aggregation by label keys.
	if seriesLimit <= 0 {
		seriesLimit = 100
	}

	sources := make([]map[string]any, 0, len(groupBy))
	for _, label := range groupBy {
		// Resolve via labelResolver: service_name → top-level serviceName
		// (resource-derived; grouping on labels.service_name.keyword yields 0
		// buckets for jvm/runtime metrics). Other labels → labels.<key>.keyword.
		aggField := r.labelResolver.Resolve(label).ESField
		sources = append(sources, map[string]any{
			label: map[string]any{
				"terms": map[string]any{
					"field": aggField,
					// missing_bucket=false (explicit "by (label)"): documents lacking
					// this label are dropped — matching PromQL "by" semantics.
					// missing_bucket=true (bare-metric selector): documents lacking
					// this label fall into a null-key bucket — every series appears
					// regardless of which labels it has.
					"missing_bucket": missingBucket,
				},
			},
		})
	}

	return map[string]any{
		"by_group": map[string]any{
			"composite": map[string]any{
				"size":    seriesLimit,
				"sources": sources,
			},
			"aggs": map[string]any{
				"time_series": timeAgg,
			},
		},
	}
}

// parseQueryRangeResult parses the ES aggregation response into a MetricRangeResult.
func (r *MetricReader) parseQueryRangeResult(resp *SearchResponse, grouped bool, aggFunc *AggregationFunc) (*MetricRangeResult, error) {
	if grouped {
		return r.parseGroupedResult(resp, aggFunc)
	}
	return r.parseSimpleResult(resp, aggFunc)
}

// parseSimpleResult parses a non-grouped date_histogram aggregation.
// Includes all buckets (including empty ones with NilValue sentinel) so fill strategies work.
func (r *MetricReader) parseSimpleResult(resp *SearchResponse, aggFunc *AggregationFunc) (*MetricRangeResult, error) {
	result := &MetricRangeResult{}

	raw, ok := resp.Aggregations["time_series"]
	if !ok {
		return result, nil
	}

	var agg struct {
		Buckets []struct {
			Key      int64           `json:"key"`
			AggValue json.RawMessage `json:"agg_value"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return result, nil
	}

	series := MetricSeries{
		Labels: make(map[string]string),
		Values: make([]MetricDataPoint, 0, len(agg.Buckets)),
	}
	for _, b := range agg.Buckets {
		v := aggFunc.ParseValue(b.AggValue)
		dp := MetricDataPoint{
			Labels: make(map[string]string),
			Time:   time.UnixMilli(b.Key),
		}
		if v != nil {
			dp.Value = *v
		} else {
			dp.Value = NilValue // sentinel for empty bucket
		}
		series.Values = append(series.Values, dp)
	}
	result.Data = append(result.Data, series)

	return result, nil
}

// parseGroupedResult parses a composite + date_histogram aggregation response.
// Includes all buckets (including empty ones with NilValue sentinel) so fill strategies work.
func (r *MetricReader) parseGroupedResult(resp *SearchResponse, aggFunc *AggregationFunc) (*MetricRangeResult, error) {
	result := &MetricRangeResult{}

	raw, ok := resp.Aggregations["by_group"]
	if !ok {
		return result, nil
	}

	var composite struct {
		Buckets []struct {
			Key        map[string]any `json:"key"`
			TimeSeries struct {
				Buckets []struct {
					Key      int64           `json:"key"`
					AggValue json.RawMessage `json:"agg_value"`
				} `json:"buckets"`
			} `json:"time_series"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &composite); err != nil {
		return result, nil
	}

	for _, group := range composite.Buckets {
		// Extract labels from composite key.
		labels := make(map[string]string)
		for k, v := range group.Key {
			if v != nil {
				labels[k] = fmt.Sprintf("%v", v)
			}
		}

		series := MetricSeries{
			Labels: labels,
			Values: make([]MetricDataPoint, 0, len(group.TimeSeries.Buckets)),
		}
		for _, b := range group.TimeSeries.Buckets {
			v := aggFunc.ParseValue(b.AggValue)
			dp := MetricDataPoint{
				Labels: labels,
				Time:   time.UnixMilli(b.Key),
			}
			if v != nil {
				dp.Value = *v
			} else {
				dp.Value = NilValue // sentinel for empty bucket
			}
			series.Values = append(series.Values, dp)
		}
		result.Data = append(result.Data, series)
	}

	return result, nil
}

// timeRangeQuery returns a millisecond-precision time range filter for metrics.
// Uses TimeRangeFilterMilli because metric fields are stored as ES date type with epoch_millis format.
func (r *MetricReader) timeRangeQuery(tr TimeRange) map[string]any {
	return esq.TimeRangeFilterMilli(FieldMetricTimeUnixMilli, tr)
}

// calculateInterval determines the appropriate histogram interval,
// ensuring bucket count stays within ES max_buckets limit.
// Delegates to esq.SafeInterval which implements clamping when a user-
// specified step would produce too many buckets.
//
// maxBuckets caps the TIME-axis bucket count (per composite key). It is
// ESHardMaxBuckets (65535) for both grouped and flat queries — ES counts the
// nested date_histogram buckets per composite key, not as seriesLimit ×
// time_buckets. See QueryRange for the empirical verification.
func (r *MetricReader) calculateInterval(tr TimeRange, step time.Duration, maxBuckets int) string {
	duration := time.Duration(0)
	if !tr.Start.IsZero() && !tr.End.IsZero() {
		duration = tr.End.Sub(tr.Start)
	}

	interval, clamped := esq.SafeInterval(esq.BucketParams{
		Duration:   duration,
		Step:       step,
		MaxBuckets: maxBuckets,
	})

	if clamped {
		r.logger.Warn("metric range query step clamped to avoid too_many_buckets",
			zap.Duration("original_step", step),
			zap.String("clamped_interval", interval),
			zap.Duration("duration", duration),
			zap.Int("max_buckets", maxBuckets),
		)
	}

	return interval
}

// metricLabels decodes the "labels" object, tolerating non-string scalars.
//
// OTel attributes are typed, so a label can arrive as a JSON number or bool
// (http.response.status_code=200, rpc.grpc.status_code=0). Decoding straight
// into map[string]string makes encoding/json fail on the FIRST such key and
// abandon the rest of the document — the caller then sees an empty sample and
// loses bucket_counts/explicit_bounds too. That is why histogram_quantile and
// the heatmap returned nothing for every http.* and rpc.client.* metric while
// the bucket data sat intact in Elasticsearch.
type metricLabels map[string]string

func (m *metricLabels) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(metricLabels, len(raw))
	for k, v := range raw {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = s
			continue
		}
		// Numbers, booleans and null: keep the literal JSON text, which for a
		// scalar is exactly its Prometheus label value ("200", "true"). null
		// becomes the empty string, matching an absent attribute.
		if string(v) == "null" {
			out[k] = ""
			continue
		}
		out[k] = string(v)
	}
	*m = out
	return nil
}

// mergeServiceName promotes the stored TOP-LEVEL "serviceName" field onto a
// sample's label set as the canonical "service_name" Prometheus label.
//
// Metrics whose only service identifier is the resource attribute
// service.name are stored with it in the top-level serviceName field (see
// storedmodel.ConvertOTLPMetric), NOT inside the "labels" object. The query
// layer must promote it, otherwise such metrics have no service_name label and
// Grafana Metrics Drilldown — which breaks every metric down by service_name
// by default — returns 0 series and shows "no data" on the page even though
// the raw samples exist (e.g. db.client.connections.*, kafka.consumer.*).
//
// A data-point label already named service_name (e.g. emitted by the spanmetrics
// connector) lives inside "labels" and wins; we never override it.
func mergeServiceName(labels metricLabels, serviceName string) metricLabels {
	if serviceName == "" {
		return labels
	}
	if labels == nil {
		labels = make(metricLabels)
	}
	if _, ok := labels["service_name"]; !ok {
		labels["service_name"] = serviceName
	}
	return labels
}

// hitToDataPoint converts an ES search hit to a MetricDataPoint.
func (r *MetricReader) hitToDataPoint(hit SearchHit) MetricDataPoint {
	var doc struct {
		TimeUnixMilli  int64        `json:"timeUnixMilli"`
		Value          float64      `json:"value"`
		ServiceName    string       `json:"serviceName"`
		Labels         metricLabels `json:"labels"`
		BucketCounts   []int64      `json:"bucket_counts"`
		ExplicitBounds []float64    `json:"explicit_bounds"`
	}
	if err := json.Unmarshal(hit.Source, &doc); err != nil {
		r.logger.Warn("Failed to unmarshal metric hit", zap.String("id", hit.ID), zap.Error(err))
		return MetricDataPoint{}
	}

	return MetricDataPoint{
		Labels:         mergeServiceName(doc.Labels, doc.ServiceName),
		Value:          doc.Value,
		Time:           time.UnixMilli(doc.TimeUnixMilli),
		BucketCounts:   doc.BucketCounts,
		ExplicitBounds: doc.ExplicitBounds,
	}
}

// hitToSample converts an ES search hit to a MetricSample with labels.
// Used by QueryFlat to return samples with their original labels for Go-side grouping.
func (r *MetricReader) hitToSample(hit SearchHit) MetricSample {
	var doc struct {
		TimeUnixMilli  int64        `json:"timeUnixMilli"`
		Value          float64      `json:"value"`
		ServiceName    string       `json:"serviceName"`
		Labels         metricLabels `json:"labels"`
		BucketCounts   []int64      `json:"bucket_counts"`
		ExplicitBounds []float64    `json:"explicit_bounds"`
	}
	if err := json.Unmarshal(hit.Source, &doc); err != nil {
		r.logger.Warn("Failed to unmarshal metric hit", zap.String("id", hit.ID), zap.Error(err))
		return MetricSample{}
	}

	return MetricSample{
		TimestampMs:  doc.TimeUnixMilli,
		Value:        doc.Value,
		BucketCounts: doc.BucketCounts,
		Bounds:       doc.ExplicitBounds,
		Labels:       mergeServiceName(doc.Labels, doc.ServiceName),
	}
}

// QueryRaw returns raw sample points for series matching the criteria.
// Unlike QueryRange which returns aggregated buckets, QueryRaw returns
// original data points (sorted by time ASC) for PromQL functions like
// rate() and increase() that need the original sample sequence.
//
// Uses ES composite aggregation to group by label set, then top_hits
// within each group to retrieve individual sample points.
func (r *MetricReader) QueryRaw(ctx context.Context, query MetricRawQuery) ([]MetricRawSeries, error) {
	// 1. Build ES filter (metric name + labels + labelMatch + service + time range).
	filterResult := r.buildRawQueryFilter(query)
	esQuery := filterResult.Query

	// Set limit, default 100.
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	// 3. Use composite aggregation by label set + top_hits within each group.
	// Each group returns raw (timestamp, value) pairs sorted by time ASC.
	// ES 7.x compatible: use composite aggregation with script-based
	// deterministic concatenation of label doc values. This avoids the
	// object-field key-ordering issue (multi_terms requires ES 7.12+).
	aggs := map[string]any{
		"by_series": map[string]any{
			"composite": map[string]any{
				"size": 100,
				"sources": []map[string]any{
					{"labels_hash": map[string]any{
						"terms": map[string]any{
							"script": map[string]any{
								"source": `doc['labels.client'].value + '|' + doc['labels.server'].value + '|' + doc['labels.connection_type'].value`,
								"lang":   "painless",
							},
						},
					}},
				},
			},
			"aggs": map[string]any{
				"samples": map[string]any{
					"top_hits": map[string]any{
						"size":    limit,
						"sort":    []map[string]any{{FieldMetricTimeUnixMilli: map[string]any{"order": "asc"}}},
						"_source": []string{FieldMetricTimeUnixMilli, FieldMetricValue, FieldMetricLabels, FieldServiceName, FieldMetricBucketCounts, FieldMetricExplicitBounds},
					},
				},
			},
		},
	}

	searchReq := &SearchRequest{
		Query:        esQuery,
		Size:         0,
		Aggregations: aggs,
	}

	resp, err := r.searcher.Search(ctx, r.indexPatternForRange(query.AppID, query.TimeRange.Start, query.TimeRange.End), searchReq)
	if err != nil {
		return nil, fmt.Errorf("metric raw query failed: %w", err)
	}

	series, err := r.parseRawResult(resp)
	if err != nil {
		return nil, err
	}

	// Post-filter series for unsupported regex patterns.
	series = postFilterRawSeries(series, filterResult.PostFilters)

	return series, nil
}

// metricFilterResult holds the ES query and any regex patterns that require post-filtering.
type metricFilterResult struct {
	// Query is the ES bool query to execute.
	Query map[string]any
	// PostFilters contains label regex patterns that ES cannot handle natively
	// (flattened field limitation) and must be applied in the application layer.
	// Key: normalized ES label key, Value: PromQL regex pattern.
	PostFilters map[string]string
}

// buildMetricFilter constructs an ES bool query from metric filter criteria + time range.
// Shared by QueryRaw and QueryFlat to avoid duplicated filter-building logic.
//
// For labelMatch (regex patterns), ES flattened fields do NOT support "regexp" queries.
// Instead, we translate PromQL regex patterns into ES-compatible queries:
//   - "value1|value2|..." → terms query (multi-value exact match)
//   - "literal_with_escaped_dots" → term query (single exact match)
//   - "prefix.*" → prefix query
//   - Complex regex → no ES filter (returned in PostFilters for application-layer filtering)
//
// metricNegations carries the not-equal / not-regex matchers for a query.
// Grouped into a struct so buildMetricFilter keeps a readable signature.
type metricNegations struct {
	Not      map[string]string // != value
	NotMatch map[string]string // !~ pattern
}

func (r *MetricReader) buildMetricFilter(metricName, serviceName string, labels, labelMatch map[string]string, timeRange TimeRange, neg metricNegations) metricFilterResult {
	// Translate PromQL-style labels (underscores, full enum values) to ES storage format
	// (dots, short enum values). Known OTel standard attributes get translated;
	// custom labels pass through unchanged.
	labels, labelMatch = normalizeMetricQueryLabels(labels, labelMatch)

	qb := esq.NewBuilder()

	if metricName != "" {
		qb.Term(FieldName, metricName)
	}
	if serviceName != "" {
		qb.Term(FieldServiceName, serviceName)
	}
	for k, v := range labels {
		// Resolve via labelResolver: service_name is promoted to the top-level
		// serviceName field (jvm/runtime metrics have empty labels); other labels
		// target labels.<key>.keyword.
		qb.Term(r.labelResolver.Resolve(k).ESField, v)
	}

	// Translate regex patterns to ES-compatible queries for flattened fields.
	// Uses the same field resolution as exact matches above (service_name is
	// promoted; others use the .keyword sub-field because metric labels are
	// dynamically mapped text+keyword, and term/regex on the bare analyzed
	// field never matches a multi-token value).
	var postFilters map[string]string
	for k, pattern := range labelMatch {
		field := r.labelResolver.Resolve(k).ESField
		translation := TranslatePromQLRegex(pattern)
		clause := BuildESClauseFromRegex(field, translation)
		if clause != nil {
			qb.Raw(clause)
		} else {
			// StrategyUnsupported: collect for post-filtering in application layer.
			if postFilters == nil {
				postFilters = make(map[string]string)
			}
			postFilters[k] = pattern
		}
	}

	// Negated matchers. Exact != becomes must_not term; !~ reuses the same
	// regex translation as =~ and is negated. A pattern the translator cannot
	// express in ES is dropped rather than silently narrowing the result set —
	// post-filtering only supports the positive direction today.
	negLabels, negMatch := normalizeMetricQueryLabels(neg.Not, neg.NotMatch)
	for k, v := range negLabels {
		qb.Raw(esq.MustNotQ(esq.T(r.labelResolver.Resolve(k).ESField, v)))
	}
	for k, pattern := range negMatch {
		field := r.labelResolver.Resolve(k).ESField
		if clause := BuildESClauseFromRegex(field, TranslatePromQLRegex(pattern)); clause != nil {
			qb.Raw(esq.MustNotQ(clause))
		} else {
			r.logger.Warn("metric filter: unsupported !~ pattern, ignoring",
				zap.String("label", k), zap.String("pattern", pattern))
		}
	}

	baseQuery := qb.Build()
	must := []map[string]any{baseQuery}
	timeFilter := r.timeRangeQuery(timeRange)
	if _, isMatchAll := timeFilter["match_all"]; !isMatchAll {
		must = append(must, timeFilter)
	}

	return metricFilterResult{
		Query:       map[string]any{"bool": map[string]any{"must": must}},
		PostFilters: postFilters,
	}
}

// buildRawQueryFilter builds an ES bool query from a MetricRawQuery.
func (r *MetricReader) buildRawQueryFilter(query MetricRawQuery) metricFilterResult {
	return r.buildMetricFilter(query.MetricName, query.ServiceName, query.Labels, query.LabelMatch, query.TimeRange,
		metricNegations{Not: query.LabelNot, NotMatch: query.LabelNotMatch})
}

// adaptiveFlatMaxDocs scales the QueryFlat document cap with the query span so
// long-range rate()/increase()/histogram_quantile() queries fetch enough raw
// samples instead of being silently truncated at 10000, while staying under a
// memory ceiling.
//
// Heuristic: ~2000 docs per hour of span (≈ covers a single series at ~2s scrape
// interval, or a handful of series at 15s). Floor 10000 (short ranges keep the
// old default), ceiling 50000 (bounds heap: ~10s of MB worst case).
//
// Long-range rate over MANY series still cannot be fully served from raw docs —
// that is the remit of the rollup tiers (Phase 2). This only lifts the silent
// truncation ceiling for the common single-series / low-cardinality case.
func adaptiveFlatMaxDocs(tr TimeRange) int {
	const floor, ceiling, perHour = 10000, 50000, 2000
	span := 5 * time.Hour // default floor span
	if !tr.Start.IsZero() && !tr.End.IsZero() && tr.End.After(tr.Start) {
		span = tr.End.Sub(tr.Start)
	}
	estimate := int(span.Hours()) * perHour
	if estimate < floor {
		return floor
	}
	if estimate > ceiling {
		return ceiling
	}
	return estimate
}

// QueryFlat returns all matching metric documents without ES-side grouping.
// Uses a simple ES search (no aggregation) with a configurable MaxDocs cap.
// Grouping by label set happens in Go via the Labels field on each MetricSample.
//
// Designed for histogram_quantile which needs complete bucket_counts data
// across all matching documents in a time range.
func (r *MetricReader) QueryFlat(ctx context.Context, query MetricFlatQuery) (*MetricFlatResult, error) {
	filterResult := r.buildMetricFilter(query.MetricName, query.ServiceName, query.Labels, query.LabelMatch, query.TimeRange,
		metricNegations{Not: query.LabelNot, NotMatch: query.LabelNotMatch})

	maxDocs := query.MaxDocs
	if maxDocs <= 0 {
		maxDocs = adaptiveFlatMaxDocs(query.TimeRange)
	}

	searchReq := &SearchRequest{
		Query: filterResult.Query,
		Size:  maxDocs,
		Sort:  []map[string]any{{FieldMetricTimeUnixMilli: map[string]any{"order": "asc"}}},
		Source: []string{
			FieldMetricTimeUnixMilli, FieldMetricValue,
			FieldMetricLabels, FieldServiceName, FieldMetricBucketCounts, FieldMetricExplicitBounds,
		},
	}

	indexPattern := query.IndexPattern
	if indexPattern == "" {
		indexPattern = r.indexPatternForRange(query.AppID, query.TimeRange.Start, query.TimeRange.End)
	}
	resp, err := r.searcher.Search(ctx, indexPattern, searchReq)
	if err != nil {
		return nil, fmt.Errorf("metric flat query failed: %w", err)
	}

	samples := make([]MetricSample, 0, len(resp.Hits.Hits))
	for _, hit := range resp.Hits.Hits {
		samples = append(samples, r.hitToSample(hit))
	}

	// Post-filter for unsupported regex patterns that ES flattened fields cannot handle.
	samples = postFilterSamples(samples, filterResult.PostFilters)

	total := int64(len(samples))
	if resp.Hits.Total.Value > 0 && total == int64(len(resp.Hits.Hits)) {
		total = resp.Hits.Total.Value
	}

	return &MetricFlatResult{
		Samples:   samples,
		Total:     total,
		Truncated: resp.Hits.Total.Relation == "gte",
	}, nil
}

// postFilterSamples applies application-layer regex filtering for patterns
// that cannot be translated to ES queries (StrategyUnsupported on flattened fields).
// postFilters map: key = normalized ES label key, value = PromQL regex pattern.
func postFilterSamples(samples []MetricSample, postFilters map[string]string) []MetricSample {
	if len(postFilters) == 0 {
		return samples
	}

	// Filter samples: keep only those matching ALL regex patterns.
	filtered := samples[:0]
	for _, sample := range samples {
		if matchesPostFilters(sample.Labels, postFilters) {
			filtered = append(filtered, sample)
		}
	}
	return filtered
}

// postFilterDataPoints applies application-layer regex filtering on MetricDataPoint slices.
// Used by Query() for instant queries with unsupported regex patterns.
func postFilterDataPoints(data []MetricDataPoint, postFilters map[string]string) []MetricDataPoint {
	if len(postFilters) == 0 {
		return data
	}

	filtered := data[:0]
	for _, dp := range data {
		if matchesPostFilters(dp.Labels, postFilters) {
			filtered = append(filtered, dp)
		}
	}
	return filtered
}

// postFilterSeries applies application-layer regex filtering on MetricSeries slices.
// Used by QueryRange() for grouped aggregation results with unsupported regex patterns.
func postFilterSeries(data []MetricSeries, postFilters map[string]string) []MetricSeries {
	if len(postFilters) == 0 {
		return data
	}

	filtered := data[:0]
	for _, series := range data {
		if matchesPostFilters(series.Labels, postFilters) {
			filtered = append(filtered, series)
		}
	}
	return filtered
}

// postFilterRawSeries applies application-layer regex filtering on MetricRawSeries slices.
// Used by QueryRaw() for raw aggregation results with unsupported regex patterns.
func postFilterRawSeries(data []MetricRawSeries, postFilters map[string]string) []MetricRawSeries {
	if len(postFilters) == 0 {
		return data
	}

	filtered := data[:0]
	for _, series := range data {
		if matchesPostFilters(series.Labels, postFilters) {
			filtered = append(filtered, series)
		}
	}
	return filtered
}

// matchesPostFilters checks if a label set matches ALL given regex post-filters.
func matchesPostFilters(labels map[string]string, postFilters map[string]string) bool {
	for key, pattern := range postFilters {
		val, ok := labels[key]
		if !ok || !PostFilterByRegex(val, pattern) {
			return false
		}
	}
	return true
}

// parseRawResult parses the ES composite+top_hits response into MetricRawSeries.
func (r *MetricReader) parseRawResult(resp *SearchResponse) ([]MetricRawSeries, error) {
	raw, ok := resp.Aggregations["by_series"]
	if !ok {
		return nil, nil
	}

	var composite struct {
		Buckets []struct {
			Key     any `json:"key"` // composite: map[string]any, multi_terms: []any
			Samples struct {
				Hits struct {
					Hits []SearchHit `json:"hits"`
				} `json:"hits"`
			} `json:"samples"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &composite); err != nil {
		return nil, fmt.Errorf("failed to parse QueryRaw result: %w", err)
	}

	result := make([]MetricRawSeries, 0, len(composite.Buckets))
	for _, bucket := range composite.Buckets {
		hits := bucket.Samples.Hits.Hits
		if len(hits) == 0 {
			continue
		}

		samples := make([]MetricSample, 0, len(hits))
		var labels map[string]string
		for _, hit := range hits {
			var doc struct {
				TimeUnixMilli  int64        `json:"timeUnixMilli"`
				Value          float64      `json:"value"`
				ServiceName    string       `json:"serviceName"`
				Labels         metricLabels `json:"labels"`
				BucketCounts   []int64      `json:"bucket_counts"`
				ExplicitBounds []float64    `json:"explicit_bounds"`
			}
			if err := json.Unmarshal(hit.Source, &doc); err != nil {
				continue
			}
			merged := mergeServiceName(doc.Labels, doc.ServiceName)
			if labels == nil {
				labels = merged
			}
			samples = append(samples, MetricSample{
				TimestampMs:  doc.TimeUnixMilli,
				Value:        doc.Value,
				BucketCounts: doc.BucketCounts,
				Bounds:       doc.ExplicitBounds,
				Labels:       merged,
			})
		}
		if labels == nil {
			labels = make(map[string]string)
		}
		result = append(result, MetricRawSeries{
			Labels:  labels,
			Samples: samples,
		})
	}

	return result, nil
}

// LabelCombinationsQuery is the ES-specific options for label exploration.
type MetricLabelQuery struct {
	MetricName  string
	Labels      map[string]string
	LabelKeys   []string
	ServiceName string
}

// MetricCombinationsResult holds flattened label combinations.
type MetricCombinationsResult struct {
	Combinations []map[string]string
}

// ListLabelCombinations returns unique label value combinations for the
// specified metric. Uses ES terms aggregation on label fields.
func (r *MetricReader) ListLabelCombinations(ctx context.Context, query MetricLabelQuery) (*MetricCombinationsResult, error) {
	esQuery := r.buildMetricQuery(query.MetricName, query.Labels, query.ServiceName)

	searchReq := &SearchRequest{
		Size: 0,
		Query: map[string]any{
			"bool": map[string]any{
				"must": []map[string]any{esQuery},
			},
		},
		Aggregations: map[string]any{"combo_root": r.buildLabelComboAgg(query.LabelKeys)},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("label combinations search failed: %w", err)
	}

	var aggMap map[string]any
	if raw, ok := resp.Aggregations["combo_root"]; ok {
		json.Unmarshal(raw, &aggMap)
	}
	combos := r.flattenLabelCombos(map[string]any{"combo_root": aggMap}, query.LabelKeys)
	return &MetricCombinationsResult{Combinations: combos}, nil
}

func (r *MetricReader) buildLabelComboAgg(keys []string) map[string]any {
	if len(keys) == 0 {
		return nil
	}
	outer := map[string]any{
		"terms": map[string]any{
			"field": "labels." + keys[0],
			"size":  100,
		},
	}
	if len(keys) > 1 {
		outer["aggs"] = map[string]any{
			"next": r.buildLabelComboAgg(keys[1:]),
		}
	}
	return outer
}

func (r *MetricReader) flattenLabelCombos(result map[string]any, keys []string) []map[string]string {
	root, _ := result["combo_root"].(map[string]any)
	if root == nil {
		return nil
	}
	buckets, _ := root["buckets"].([]any)
	if buckets == nil {
		return nil
	}

	var combos []map[string]string
	for _, b := range buckets {
		bucket, _ := b.(map[string]any)
		val := fmt.Sprint(bucket["key"])
		base := map[string]string{keys[0]: val}

		if sub, ok := bucket["next"].(map[string]any); ok && len(keys) > 1 {
			subCombos := r.flattenSubCombos(sub, keys[1:])
			for _, sc := range subCombos {
				for k, v := range base {
					sc[k] = v
				}
				combos = append(combos, sc)
			}
		} else {
			combos = append(combos, base)
		}
	}
	return combos
}

func (r *MetricReader) flattenSubCombos(result map[string]any, keys []string) []map[string]string {
	buckets, _ := result["buckets"].([]any)
	if buckets == nil {
		return nil
	}
	var combos []map[string]string
	for _, b := range buckets {
		bucket, _ := b.(map[string]any)
		val := fmt.Sprint(bucket["key"])
		base := map[string]string{keys[0]: val}

		if sub, ok := bucket["next"].(map[string]any); ok && len(keys) > 1 {
			subCombos := r.flattenSubCombos(sub, keys[1:])
			for _, sc := range subCombos {
				for k, v := range base {
					sc[k] = v
				}
				combos = append(combos, sc)
			}
		} else {
			combos = append(combos, base)
		}
	}
	return combos
}

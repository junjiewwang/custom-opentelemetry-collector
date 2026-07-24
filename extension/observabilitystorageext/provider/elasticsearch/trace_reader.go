// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	esq "go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch/query"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
	"go.uber.org/zap"
)

// Stored-span types are aliased from the canonical storedmodel package.
type (
	StoredSpan  = storedmodel.StoredSpan
	StoredEvent = storedmodel.StoredEvent
	StoredLink  = storedmodel.StoredLink
)

// MaxResultWindow is the ES default max_result_window (from + size limit).
// We cap both aggregation bucket size and bulk fetch size to stay within this.
const MaxResultWindow = 10000

// TraceReader implements trace query operations against Elasticsearch.
// It uses a two-pass search strategy:
//   - Step 1: terms aggregation on trace_id to discover matching traces with pagination.
//   - Step 2: bulk fetch all spans for those trace IDs.
type TraceReader struct {
	searcher Searcher
	config *Config
	logger *zap.Logger
}

// NewTraceReader creates a new TraceReader instance.
func NewTraceReader(searcher Searcher, config *Config, logger *zap.Logger) *TraceReader {
	return &TraceReader{
		searcher: searcher,
		config: config,
		logger: logger.Named("trace-reader"),
	}
}

// SearchTraces searches for traces matching the query parameters.
// AppID is optional: when empty, queries all app indices (admin mode).
func (r *TraceReader) SearchTraces(ctx context.Context, query TraceQuery) (*TraceSearchResult, error) {

	esQuery := r.buildTraceSearchQuery(query)

	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	// Cap limit to prevent ES max_result_window violations downstream.
	if limit > MaxResultWindow/100 {
		limit = MaxResultWindow / 100 // 100 spans/trace estimate
	}

	// Step 1: Find distinct trace_ids matching the filters via aggregation.
	aggSize := limit + query.Offset
	if aggSize > MaxResultWindow {
		aggSize = MaxResultWindow
	}
	searchReq := &SearchRequest{
		Query: esQuery,
		Size:  0, // We only want aggregation results.
		Aggregations: map[string]any{
			"traces": map[string]any{
				"terms": map[string]any{
					"field": FieldTraceID,
					"size":  aggSize,
					"order": map[string]any{"max_start": "desc"},
				},
				"aggs": map[string]any{
					"max_start": map[string]any{
						"max": map[string]any{"field": FieldStartTimeUnixNano},
					},
				},
			},
			"total_traces": map[string]any{
				"cardinality": map[string]any{"field": FieldTraceID},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("trace search failed: %w", err)
	}

	// Parse aggregation results.
	traceIDs, totalTraces, err := r.parseTraceAggregation(resp, query.Offset, limit)
	if err != nil {
		return nil, err
	}

	if len(traceIDs) == 0 {
		return &TraceSearchResult{Traces: []Trace{}, Total: totalTraces}, nil
	}

	// Step 2: Fetch all spans for the matching trace_ids.
	traces, err := r.fetchTracesByIDs(ctx, traceIDs, query.AppID)
	if err != nil {
		return nil, err
	}

	return &TraceSearchResult{
		Traces: traces,
		Total:  totalTraces,
	}, nil
}

// SearchTraceSummaries searches for trace summaries using a single ES query:
// terms aggregation on traceID + top_hits to get only root/first N spans.
// No bulk fetch — avoids the len(traceIDs)×100 multiplier that hits max_result_window.
func (r *TraceReader) SearchTraceSummaries(ctx context.Context, query TraceQuery, spss int) (*TraceSummaryResult, error) {
	if spss <= 0 {
		spss = 3 // Tempo default
	}
	if spss > 100 {
		spss = 100
	}

	esQuery := r.buildTraceSearchQuery(query)
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > MaxResultWindow/100 {
		limit = MaxResultWindow / 100
	}

	aggSize := limit + query.Offset
	if aggSize > MaxResultWindow {
		aggSize = MaxResultWindow
	}

	searchReq := &SearchRequest{
		Query: esQuery,
		Size:  0,
		Aggregations: map[string]any{
			"traces": map[string]any{
				"terms": map[string]any{
					"field": FieldTraceID,
					"size":  aggSize,
					"order": map[string]any{"max_start": "desc"},
				},
				"aggs": map[string]any{
					"max_start": map[string]any{
						"max": map[string]any{"field": FieldStartTimeUnixNano},
					},
					"root_span": map[string]any{
						"top_hits": map[string]any{
							"size":  spss,
							"sort": []map[string]any{
								{FieldStartTimeUnixNano: map[string]any{"order": "asc"}},
							},
							"_source": []string{
								FieldTraceID, FieldName, FieldServiceName,
								FieldSpanID, FieldParentSpanID,
								FieldStartTimeUnixNano, FieldDurationNano,
								FieldKind, FieldAttributes, FieldResource,
								"status", "events", "links",
							},
						},
					},
				},
			},
			"total_traces": map[string]any{
				"cardinality": map[string]any{"field": FieldTraceID},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("trace summary search failed: %w", err)
	}

	return r.parseTraceSummaryResult(resp, query.Offset, limit)
}

// parseTraceSummaryResult extracts TraceSummary entries from the aggregation response.
func (r *TraceReader) parseTraceSummaryResult(resp *SearchResponse, offset, limit int) (*TraceSummaryResult, error) {
	raw, ok := resp.Aggregations["traces"]
	if !ok {
		return &TraceSummaryResult{}, nil
	}

	var agg struct {
		Buckets []struct {
			Key      string `json:"key"`
			RootSpan struct {
				Hits struct {
					Hits []SearchHit `json:"hits"`
				} `json:"hits"`
			} `json:"root_span"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("parse trace summary agg: %w", err)
	}

	// Parse total count
	var total int64
	if rawTotal, ok := resp.Aggregations["total_traces"]; ok {
		var cardAgg struct {
			Value int64 `json:"value"`
		}
		if err := json.Unmarshal(rawTotal, &cardAgg); err == nil {
			total = cardAgg.Value
		}
	}

	// Apply offset/limit
	if offset >= len(agg.Buckets) {
		return &TraceSummaryResult{Total: total}, nil
	}
	buckets := agg.Buckets[offset:]
	if len(buckets) > limit {
		buckets = buckets[:limit]
	}

	summaries := make([]TraceSummary, 0, len(buckets))
	for _, b := range buckets {
		spans, err := r.hitsToStoredSpans(b.RootSpan.Hits.Hits)
		if err != nil {
			continue
		}
		if len(spans) == 0 {
			continue
		}

		ts := TraceSummary{
			TraceID:   b.Key,
			SpanCount: int64(len(spans)),
			SpanSet:   spans,
		}

		// Root span is the one without parentSpanId, or the first span.
		for _, ss := range spans {
			if ss.ParentSpanID == "" {
				ts.RootServiceName = ss.ServiceName
				ts.RootSpanName = ss.Name
				break
			}
		}
		if ts.RootServiceName == "" && len(spans) > 0 {
			ts.RootServiceName = spans[0].ServiceName
			ts.RootSpanName = spans[0].Name
		}

		// Compute start time and duration from the span set.
		// Note: _source projection does not include endTimeUnixNano (to reduce payload),
		// so we derive end time from StartUnixNano + DurationNano instead.
		var minStart, maxEnd int64
		for _, ss := range spans {
			if minStart == 0 || ss.StartUnixNano < minStart {
				minStart = ss.StartUnixNano
			}
			end := ss.StartUnixNano + ss.DurationNano
			if end > maxEnd {
				maxEnd = end
			}
		}
		if minStart > 0 {
			ts.StartTimeUnixNano = strconv.FormatInt(minStart, 10)
		}
		if maxEnd > minStart {
			ts.DurationMs = (maxEnd - minStart) / int64(time.Millisecond)
		}

		summaries = append(summaries, ts)
	}

	return &TraceSummaryResult{Summaries: summaries, Total: total}, nil
}

// GetTrace retrieves a single trace by its trace ID.
func (r *TraceReader) GetTrace(ctx context.Context, traceID string) (*Trace, error) {
	searchReq := &SearchRequest{
		Query: esq.TermQ(FieldTraceID, traceID),
		Size:  1000, // Max spans per trace.
		Sort: []map[string]any{
			{FieldStartTimeUnixNano: map[string]any{"order": "asc"}},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("get trace failed: %w", err)
	}

	if len(resp.Hits.Hits) == 0 {
		return nil, fmt.Errorf("trace %s not found", traceID)
	}

	spans, err := r.hitsToStoredSpans(resp.Hits.Hits)
	if err != nil {
		return nil, err
	}

	trace := r.assembleTrace(traceID, spans)
	return &trace, nil
}

// GetTraceSpans retrieves all spans for a trace as StoredSpan.
func (r *TraceReader) GetTraceSpans(ctx context.Context, traceID string) ([]StoredSpan, error) {
	searchReq := &SearchRequest{
		Query: esq.TermQ(FieldTraceID, traceID),
		Size:  1000,
		Sort: []map[string]any{
			{FieldStartTimeUnixNano: map[string]any{"order": "asc"}},
		},
	}
	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("get trace spans failed: %w", err)
	}
	return r.hitsToStoredSpans(resp.Hits.Hits)
}

// SearchSpans searches for spans matching the query and returns StoredSpan format.
func (r *TraceReader) SearchSpans(ctx context.Context, query TraceQuery) ([]StoredSpan, []string, error) {
	esQuery := r.buildTraceSearchQuery(query)
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > MaxResultWindow/100 {
		limit = MaxResultWindow / 100
	}

	// Step 1: aggregation to find trace IDs
	aggSize := limit + query.Offset
	if aggSize > MaxResultWindow {
		aggSize = MaxResultWindow
	}
	searchReq := &SearchRequest{
		Query: esQuery,
		Size:  0,
		Aggregations: map[string]any{
			"traces": map[string]any{
				"terms": map[string]any{
					"field": FieldTraceID,
					"size":  aggSize,
					"order": map[string]any{"max_start": "desc"},
				},
				"aggs": map[string]any{
					"max_start": map[string]any{"max": map[string]any{"field": FieldStartTimeUnixNano}},
				},
			},
		},
	}
	resp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), searchReq)
	if err != nil {
		return nil, nil, fmt.Errorf("search spans failed: %w", err)
	}
	traceIDs, _, err := r.parseTraceAggregation(resp, query.Offset, limit)
	if err != nil || len(traceIDs) == 0 {
		return nil, traceIDs, err
	}

	// Step 2: fetch all spans, capped to ES max_result_window.
	fetchSize := len(traceIDs) * 100
	if fetchSize > MaxResultWindow {
		fetchSize = MaxResultWindow
	}
	fetchReq := &SearchRequest{
		Query: esq.TermsQ(FieldTraceID, traceIDs),
		Size:  fetchSize,
	}
	fetchResp, err := r.searcher.Search(ctx, r.indexPattern(query.AppID), fetchReq)
	if err != nil {
		return nil, traceIDs, fmt.Errorf("fetch spans by IDs failed: %w", err)
	}
	spans, err := r.hitsToStoredSpans(fetchResp.Hits.Hits)
	return spans, traceIDs, err
}

// hitsToStoredSpans converts ES search hits to StoredSpan directly (no local Span conversion).
func (r *TraceReader) hitsToStoredSpans(hits []SearchHit) ([]StoredSpan, error) {
	spans := make([]StoredSpan, 0, len(hits))
	for _, hit := range hits {
		var ss StoredSpan
		if err := json.Unmarshal(hit.Source, &ss); err != nil {
			r.logger.Warn("Failed to unmarshal span document", zap.String("id", hit.ID), zap.Error(err))
			continue
		}
		ss = compatStoredSpan(ss, hit.Source)
		spans = append(spans, ss)
	}
	return spans, nil
}

// GetServices returns all service names within the time range.
func (r *TraceReader) GetServices(ctx context.Context, timeRange TimeRange) ([]Service, error) {
	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"services": map[string]any{
				"terms": map[string]any{
					"field": FieldServiceName,
					"size":  1000,
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("get services failed: %w", err)
	}

	return r.parseServicesAggregation(resp, "services")
}

// GetOperations returns operations for a given service.
func (r *TraceReader) GetOperations(ctx context.Context, service string, timeRange TimeRange) ([]Operation, error) {
	esQuery := esq.NewBuilder().
		Term(FieldServiceName, service).
		Raw(esq.TimeRangeFilter(FieldStartTimeUnixNano, timeRange)).
		Build()

	searchReq := &SearchRequest{
		Query: esQuery,
		Size:  0,
		Aggregations: map[string]any{
			"operations": map[string]any{
				"terms": map[string]any{
					"field": FieldName,
					"size":  1000,
				},
				"aggs": map[string]any{
					"span_kinds": map[string]any{
						"terms": map[string]any{
							"field": FieldKind,
							"size":  10,
						},
					},
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("get operations failed: %w", err)
	}

	return r.parseOperationsAggregation(resp)
}

// GetDependencies returns service-to-service dependencies for the service map.
// It uses service co-occurrence within traces as an approximation since ES
// doesn't support joins natively.
func (r *TraceReader) GetDependencies(ctx context.Context, timeRange TimeRange) ([]Dependency, error) {
	return r.calculateDependencies(ctx, timeRange)
}

// ==================== Internal Helpers ====================

// indexPattern returns the ES index pattern for traces.
func (r *TraceReader) indexPattern(appID ...string) string {
	id := ""
	if len(appID) > 0 {
		id = appID[0]
	}
	return esq.IndexPattern(r.config.Traces.IndexPrefix, id)
}

// buildTraceSearchQuery constructs the ES query from TraceQuery parameters.
func (r *TraceReader) buildTraceSearchQuery(tq TraceQuery) map[string]any {
	qb := esq.NewBuilder().
		Raw(esq.TimeRangeFilter(FieldStartTimeUnixNano, tq.TimeRange))

	if tq.ServiceName != "" {
		qb.Term(FieldServiceName, tq.ServiceName)
	}
	if tq.OperationName != "" {
		qb.Term(FieldName, tq.OperationName)
	}
	if tq.MinDuration > 0 {
		qb.Range(FieldDurationNano, tq.MinDuration.Nanoseconds(), nil, nil, nil)
	}
	if tq.MaxDuration > 0 {
		qb.Range(FieldDurationNano, nil, tq.MaxDuration.Nanoseconds(), nil, nil)
	}

	// ── Intrinsic filters (from TraceQL engine) ──
	if tq.SpanKind != "" {
		// TraceQL uses lowercase (server, client), ES stores capitalized (Server, Client).
		qb.Term(FieldKind, capitalizeFirst(tq.SpanKind))
	}
	if tq.Status != "" {
		// ES stores status.code values as-is from OTel enum String():
		// STATUS_CODE_ERROR -> "STATUS_CODE_ERROR", STATUS_CODE_OK -> "STATUS_CODE_OK".
		// But actual data in ES is lowercase due to historical write behavior.
		// Use ToUpper for API consistency while matching ES data.
		qb.Term(FieldStatus+".code", tq.Status)
	}
	if tq.IsRoot {
		// Root span: parentSpanId field is absent (omitempty) for new data,
		// or "0000000000000000" for historical data written before the writer bug fix.
		qb.Should(1,
			esq.MustNotQ(esq.ExistsQ(FieldParentSpanID)),       // field absent (new data)
			esq.T(FieldParentSpanID, "0000000000000000"),        // zero-value (historical data)
		)
	}

	// ── Root span intrinsic filters ──
	// rootName: the root span's name must match, AND that span
	// must have no parentSpanId (it IS the root).
	if tq.RootName != "" {
		qb.Raw(map[string]any{
			"bool": map[string]any{"must": []map[string]any{
				esq.T(FieldName, tq.RootName),
				esq.MustNotQ(esq.ExistsQ(FieldParentSpanID)),
			}},
		})
	}
	// rootService: the root span's serviceName must match.
	if tq.RootService != "" {
		qb.Raw(map[string]any{
			"bool": map[string]any{"must": []map[string]any{
				esq.T(FieldServiceName, tq.RootService),
				esq.MustNotQ(esq.ExistsQ(FieldParentSpanID)),
			}},
		})
	}

	// AND conditions: each tag must match.
	for k, v := range tq.Tags {
		clauses := resolveTagTermClauses(k, v)
		if len(clauses) == 1 {
			qb.Raw(clauses[0])
		} else {
			qb.Should(1, clauses...)
		}
	}

	// ── Event filters (nested queries on the events field) ──
	for _, eventTag := range tq.EventTags {
		for k, v := range eventTag {
			qb.Raw(esq.NestedQuery(FieldEvents, esq.T(FieldEvents+"."+k, v)))
		}
	}
	for _, eventOrGroup := range tq.EventTagsOr {
		var nestedClauses []map[string]any
		for _, branchMaps := range eventOrGroup {
			builder := esq.NewBuilder()
			for _, branchMap := range branchMaps {
				for k, v := range branchMap {
					builder.Raw(esq.T(FieldEvents+"."+k, v))
				}
			}
			nestedClauses = append(nestedClauses, esq.NestedQuery(FieldEvents, builder.Build()))
		}
		if len(nestedClauses) > 0 {
			qb.Should(1, nestedClauses...)
		}
	}

		// ── TagsNotOr (Sprint 3): OR-grouped != conditions → must_not+should ──
	for _, orGroup := range tq.TagsNotOr {
		var notClauses []map[string]any
		for _, branchMap := range orGroup {
			builder := esq.NewBuilder()
			for k, v := range branchMap {
				clauses := resolveTagTermClauses(k, v)
				for _, clause := range clauses {
					builder.Raw(esq.MustNotQ(clause))
				}
			}
			notClauses = append(notClauses, builder.Build())
		}
		if len(notClauses) > 0 {
			qb.Should(1, notClauses...)
		}
	}

	// ── TagsRegexOr (Sprint 3): OR-grouped =~ conditions → regexp+should ──
	for _, orGroup := range tq.TagsRegexOr {
		var regexClauses []map[string]any
		for _, branchMap := range orGroup {
			builder := esq.NewBuilder()
			for k, v := range branchMap {
				paths := resolveTagFieldPaths(k)
				for _, field := range paths {
					builder.Raw(map[string]any{
						"regexp": map[string]any{
							field: map[string]any{"value": v, "flags": "ALL", "case_insensitive": true},
						},
					})
				}
			}
			regexClauses = append(regexClauses, builder.Build())
		}
		if len(regexClauses) > 0 {
			qb.Should(1, regexClauses...)
		}
	}

	// OR conditions: each TagsOr group is an independent bool.should block (AND-ed together).
	// Within each group, branches are OR-ed (min_should_match=1).
	for _, orGroup := range tq.TagsOr {
		var orClauses []map[string]any
		for _, branchMap := range orGroup {
			builder := esq.NewBuilder()
			for k, v := range branchMap {
				clauses := resolveTagTermClauses(k, v)
				if len(clauses) == 1 {
					builder.Raw(clauses[0])
				} else {
					builder.Should(1, clauses...)
				}
			}
			orClauses = append(orClauses, builder.Build())
		}
		if len(orClauses) > 0 {
			qb.Should(1, orClauses...)
		}
	}

	// ── TagsNot (Sprint 2): != value → must_not + term ──
	for k, v := range tq.TagsNot {
		for _, clause := range resolveTagTermClauses(k, v) {
			qb.Raw(esq.MustNotQ(clause))
		}
	}

	// ── TagsNotExists (Sprint 4): = nil → must_not exists ──
	for _, k := range tq.TagsNotExists {
		paths := resolveTagFieldPaths(k)
		for _, p := range paths {
			qb.Raw(esq.MustNotQ(esq.ExistsQ(p)))
		}
	}

	// ── TagsExists (Sprint 2): != nil → exists ──
	for _, k := range tq.TagsExists {
		paths := resolveTagFieldPaths(k)
		if len(paths) == 1 {
			qb.Raw(esq.ExistsQ(paths[0]))
		} else {
			var existsClauses []map[string]any
			for _, p := range paths {
				existsClauses = append(existsClauses, esq.ExistsQ(p))
			}
			qb.Should(1, existsClauses...)
		}
	}

	// ── TagsRegex (Sprint 2): =~ regex → regexp ──
	for k, pattern := range tq.TagsRegex {
		paths := resolveTagFieldPaths(k)
		if len(paths) == 1 {
			qb.Raw(map[string]any{
				"regexp": map[string]any{paths[0]: map[string]any{"value": pattern}},
			})
		} else {
			var regexClauses []map[string]any
			for _, p := range paths {
				regexClauses = append(regexClauses, map[string]any{
					"regexp": map[string]any{p: map[string]any{"value": pattern}},
				})
			}
			qb.Should(1, regexClauses...)
		}
	}

	return qb.Build()
}

// capitalizeFirst returns the string with the first letter capitalized.
// Used to convert TraceQL lowercase values (server, error) to ES stored format (Server, Error).
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ═══════════════════════════════════════════════════
// Attribute-Resolved Tag Helpers
// ═══════════════════════════════════════════════════

// attrResolver is the package-level AttributeResolver for field mapping.
var attrResolver = &AttributeResolver{}

// resolveTagTermClauses resolves a scoped tag key=value pair to ES term clauses.
//
// Resolution rules:
//   - Scoped keys (span.x, resource.x): single precise term on the correct ES field.
//   - Unscoped intrinsics (kind, status, name): single precise term.
//   - Unscoped custom keys: [attributes.key, resource.key] for backward compat
//     (should be OR-ed by caller).
//
// The returned value is transformed (capitalizeFirst for kind/status fields).
//
// Special case: status.message is a "text" field (no .keyword sub-field),
// so a "match" query is used instead of a "term" query to support
// full-text search on the analyzed field. A "term" query on a text
// field would always return 0 results.
func resolveTagTermClauses(key, value string) []map[string]any {
	fields, val := resolveTagESFields(key, value)
	clauses := make([]map[string]any, 0, len(fields))
	for _, f := range fields {
		if f == FieldStatus+".message" {
			// text field → use match query (term returns 0 on text fields)
			clauses = append(clauses, map[string]any{
				"match": map[string]any{f: val},
			})
		} else {
			clauses = append(clauses, esq.T(f, val))
		}
	}
	return clauses
}

// resolveTagFieldPaths resolves a scoped tag key to ES field paths (no value).
// Used by exists and regex queries that only need field paths, not values.
func resolveTagFieldPaths(key string) []string {
	scope, plainKey := parseScopeAndKey(key)
	resolved := attrResolver.Resolve(key)
	esField := resolved.ESField

	// Scoped or intrinsic: precise single-field mapping.
	if scope != "" || (!strings.HasPrefix(esField, FieldAttributes+".") &&
		!strings.HasPrefix(esField, FieldResource+".")) {
		return []string{esField}
	}

	// Unscoped custom attribute: backward-compatible dual search.
	sanitized := storedmodel.SanitizeKey(plainKey)
	return []string{FieldAttributes + "." + sanitized, FieldResource + "." + sanitized}
}

// resolveTagESFields resolves a scoped tag key to ES field paths and typed value.
func resolveTagESFields(key, value string) (fields []string, val string) {
	scope, plainKey := parseScopeAndKey(key)
	resolved := attrResolver.Resolve(key)
	esField := resolved.ESField

	// Transform value for fields stored with capitalized first letter.
	val = value
	switch esField {
	case FieldKind:
		val = capitalizeFirst(value)
	case FieldStatus + ".code":
		val = value // ES stores lowercase; no transform needed
	}

	// Scoped keys: use precise resolver mapping.
	if scope != "" {
		return []string{esField}, val
	}

	// Intrinsic fields (mapped to non-attributes/non-resource paths): precise.
	if !strings.HasPrefix(esField, FieldAttributes+".") &&
		!strings.HasPrefix(esField, FieldResource+".") {
		return []string{esField}, val
	}

	// Unscoped custom attribute: backward-compatible dual search.
	sanitized := storedmodel.SanitizeKey(plainKey)
	return []string{FieldAttributes + "." + sanitized, FieldResource + "." + sanitized}, val
}

// timeRangeQuery returns a simple time range query using nanosecond long values.
func (r *TraceReader) timeRangeQuery(tr TimeRange) map[string]any {
	return esq.TimeRangeQuery(FieldStartTimeUnixNano, tr)
}

// parseTraceAggregation extracts trace IDs and total count from the aggregation response.
func (r *TraceReader) parseTraceAggregation(resp *SearchResponse, offset, limit int) ([]string, int64, error) {
	raw, ok := resp.Aggregations["traces"]
	if !ok {
		return nil, 0, nil
	}

	var agg struct {
		Buckets []struct {
			Key string `json:"key"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, 0, fmt.Errorf("failed to parse trace aggregation: %w", err)
	}

	// Parse total distinct traces.
	var totalTraces int64
	if rawTotal, ok := resp.Aggregations["total_traces"]; ok {
		var cardAgg struct {
			Value int64 `json:"value"`
		}
		if err := json.Unmarshal(rawTotal, &cardAgg); err == nil {
			totalTraces = cardAgg.Value
		}
	}

	// Apply offset/limit to the trace_id list.
	traceIDs := make([]string, 0)
	for i, bucket := range agg.Buckets {
		if i < offset {
			continue
		}
		if len(traceIDs) >= limit {
			break
		}
		traceIDs = append(traceIDs, bucket.Key)
	}

	return traceIDs, totalTraces, nil
}

// fetchTracesByIDs fetches all spans for the given trace IDs and assembles them into Trace objects.
func (r *TraceReader) fetchTracesByIDs(ctx context.Context, traceIDs []string, appID string) ([]Trace, error) {
	// Cap size to stay within ES max_result_window (default 10000).
	size := len(traceIDs) * 100 // Assume average 100 spans per trace.
	if size > MaxResultWindow {
		size = MaxResultWindow
	}
	searchReq := &SearchRequest{
		Query: esq.TermsQ(FieldTraceID, traceIDs),
		Size: size,
		Sort: []map[string]any{
			{FieldTraceID: map[string]any{"order": "asc"}},
			{FieldStartTimeUnixNano: map[string]any{"order": "asc"}},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(appID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("fetch traces by IDs failed: %w", err)
	}

	spans, err := r.hitsToStoredSpans(resp.Hits.Hits)
	if err != nil {
		return nil, err
	}

	// Group spans by trace_id.
	traceMap := make(map[string][]StoredSpan)
	for _, span := range spans {
		traceMap[span.TraceID] = append(traceMap[span.TraceID], span)
	}

	// Assemble traces in the order requested.
	traces := make([]Trace, 0, len(traceIDs))
	for _, tid := range traceIDs {
		if spanList, ok := traceMap[tid]; ok && len(spanList) > 0 {
			traces = append(traces, r.assembleTrace(tid, spanList))
		}
	}
	return traces, nil
}

// assembleTrace creates a Trace from a list of StoredSpans.
func (r *TraceReader) assembleTrace(traceID string, storedSpans []StoredSpan) Trace {
	spans := make([]Span, len(storedSpans))
	var minStart, maxEnd int64
	for i, ss := range storedSpans {
		spans[i] = storedSpanToLocalSpan(ss)
		if minStart == 0 || ss.StartUnixNano < minStart {
			minStart = ss.StartUnixNano
		}
		if ss.EndUnixNano > maxEnd {
			maxEnd = ss.EndUnixNano
		}
	}

	duration := int64(0)
	if minStart > 0 && maxEnd > 0 {
		duration = (maxEnd - minStart) / 1000 // nanoseconds → microseconds
	}

	return Trace{
		TraceID:  traceID,
		Spans:    spans,
		Duration: duration,
	}
}

// storedSpanToLocalSpan converts a StoredSpan to the local Span type.
func storedSpanToLocalSpan(ss StoredSpan) Span {
	return Span{
		TraceID:       ss.TraceID,
		SpanID:        ss.SpanID,
		ParentSpanID:  ss.ParentSpanID,
		OperationName: ss.Name,
		ServiceName:   ss.ServiceName,
		SpanKind:      ss.Kind,
		StatusCode:    ss.Status.Code,
		StatusMessage: ss.Status.Message,
		StartTime:     time.Unix(0, ss.StartUnixNano),
		EndTime:       time.Unix(0, ss.EndUnixNano),
		DurationUS:    ss.DurationNano / 1000,
		Attributes:    ss.Attributes,
		Resource:      ss.Resource,
		Events:        storedEventsToLocal(ss.Events),
		Links:         storedLinksToLocal(ss.Links),
	}
}

func storedEventsToLocal(events []StoredEvent) []SpanEvent {
	if len(events) == 0 {
		return nil
	}
	result := make([]SpanEvent, len(events))
	for i, e := range events {
		result[i] = SpanEvent{
			Name:       e.Name,
			Timestamp:  time.Unix(0, e.TimeUnixNano),
			Attributes: e.Attributes,
		}
	}
	return result
}

func storedLinksToLocal(links []StoredLink) []SpanLink {
	if len(links) == 0 {
		return nil
	}
	result := make([]SpanLink, len(links))
	for i, l := range links {
		result[i] = SpanLink{
			TraceID: l.TraceID,
			SpanID:  l.SpanID,
		}
	}
	return result
}

func compatStoredSpan(ss StoredSpan, raw json.RawMessage) StoredSpan {
	if ss.Name == "" {
		var legacy struct {
			OperationName string `json:"operation_name"`
			SpanKind      string `json:"span_kind"`
			StatusCode    string `json:"status_code"`
			StatusMsg     string `json:"status_message"`
		}
		if json.Unmarshal(raw, &legacy) == nil {
			if ss.Name == "" {
				ss.Name = legacy.OperationName
			}
			if ss.Kind == "" {
				ss.Kind = legacy.SpanKind
			}
			if ss.Status.Code == "" {
				ss.Status.Code = legacy.StatusCode
			}
			if ss.Status.Message == "" {
				ss.Status.Message = legacy.StatusMsg
			}
		}
	}
	return ss
}

// parseServicesAggregation parses a terms aggregation to extract Service names.
func (r *TraceReader) parseServicesAggregation(resp *SearchResponse, aggName string) ([]Service, error) {
	raw, ok := resp.Aggregations[aggName]
	if !ok {
		return nil, nil
	}
	keys, err := esq.ParseTermsAgg(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s aggregation: %w", aggName, err)
	}
	services := make([]Service, len(keys))
	for i, key := range keys {
		services[i] = Service{Name: key}
	}
	return services, nil
}

// parseOperationsAggregation parses operations + span_kind aggregation.
func (r *TraceReader) parseOperationsAggregation(resp *SearchResponse) ([]Operation, error) {
	raw, ok := resp.Aggregations["operations"]
	if !ok {
		return nil, nil
	}

	var agg struct {
		Buckets []struct {
			Key       string `json:"key"`
			SpanKinds struct {
				Buckets []struct {
					Key string `json:"key"`
				} `json:"buckets"`
			} `json:"span_kinds"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &agg); err != nil {
		return nil, fmt.Errorf("failed to parse operations aggregation: %w", err)
	}

	operations := make([]Operation, 0)
	for _, bucket := range agg.Buckets {
		if len(bucket.SpanKinds.Buckets) > 0 {
			for _, kindBucket := range bucket.SpanKinds.Buckets {
				operations = append(operations, Operation{
					Name:     bucket.Key,
					SpanKind: kindBucket.Key,
				})
			}
		} else {
			operations = append(operations, Operation{Name: bucket.Key})
		}
	}
	return operations, nil
}

// calculateDependencies uses service co-occurrence within traces to compute dependencies.
func (r *TraceReader) calculateDependencies(ctx context.Context, timeRange TimeRange) ([]Dependency, error) {
	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"by_trace": map[string]any{
				"terms": map[string]any{
					"field": FieldTraceID,
					"size":  5000,
				},
				"aggs": map[string]any{
					"services": map[string]any{
						"terms": map[string]any{
							"field": FieldServiceName,
							"size":  100,
						},
					},
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("dependency search failed: %w", err)
	}

	raw, ok := resp.Aggregations["by_trace"]
	if !ok {
		return nil, nil
	}

	var traceAgg struct {
		Buckets []struct {
			Key      string `json:"key"`
			Services struct {
				Buckets []struct {
					Key      string `json:"key"`
					DocCount int64  `json:"doc_count"`
				} `json:"buckets"`
			} `json:"services"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &traceAgg); err != nil {
		return nil, fmt.Errorf("failed to parse dependency aggregation: %w", err)
	}

	// Build dependency counts from service co-occurrence within traces.
	depCounts := make(map[string]int64) // "parent->child" → count
	for _, traceBucket := range traceAgg.Buckets {
		services := make([]string, 0, len(traceBucket.Services.Buckets))
		for _, svcBucket := range traceBucket.Services.Buckets {
			services = append(services, svcBucket.Key)
		}
		// For each pair of services in the same trace, record a dependency.
		for i := 0; i < len(services); i++ {
			for j := i + 1; j < len(services); j++ {
				key := services[i] + "->" + services[j]
				depCounts[key]++
			}
		}
	}

	// Convert to Dependency slice.
	deps := make([]Dependency, 0, len(depCounts))
	for key, count := range depCounts {
		// Parse "parent->child".
		for i := 0; i < len(key)-1; i++ {
			if key[i] == '-' && key[i+1] == '>' {
				deps = append(deps, Dependency{
					Parent:    key[:i],
					Child:     key[i+2:],
					CallCount: count,
				})
				break
			}
		}
	}
	return deps, nil
}

// ==================== Tag Discovery ====================

// GetTagKeys returns all distinct attribute keys for the given scope.
// Uses sampler aggregation to pick a representative set of documents,
// then extracts keys from _source in Go (works with flattened field type).
// scope: "resource" or "span".
func (r *TraceReader) GetTagKeys(ctx context.Context, timeRange TimeRange, scope string) ([]string, error) {
	fieldPrefix := FieldAttributes
	if scope == "resource" {
		fieldPrefix = FieldResource
	}

	searchReq := &SearchRequest{
		Query: r.timeRangeQuery(timeRange),
		Size:  0,
		Aggregations: map[string]any{
			"sample": map[string]any{
				"sampler": map[string]any{"shard_size": 500},
				"aggs": map[string]any{
					"docs": map[string]any{
						"top_hits": map[string]any{
							"size":    100,
							"_source": []string{fieldPrefix},
						},
					},
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("get tag keys failed (scope=%s): %w", scope, err)
	}

	keys, err := r.extractTagKeysFromResponse(resp, fieldPrefix)
	if err != nil {
		return nil, fmt.Errorf("extract tag keys failed (scope=%s): %w", scope, err)
	}

	return keys, nil
}

// extractTagKeysFromResponse walks the sampler→top_hits results and collects distinct keys
// from the _source attributes/resource map across all sampled documents.
func (r *TraceReader) extractTagKeysFromResponse(resp *SearchResponse, fieldPrefix string) ([]string, error) {
	raw, ok := resp.Aggregations["sample"]
	if !ok {
		return nil, nil
	}

	var sampleAgg struct {
		Docs struct {
			Hits struct {
				Hits []struct {
					Source json.RawMessage `json:"_source"`
				} `json:"hits"`
			} `json:"hits"`
		} `json:"docs"`
	}
	if err := json.Unmarshal(raw, &sampleAgg); err != nil {
		return nil, fmt.Errorf("unmarshal sample agg: %w", err)
	}

	keySet := make(map[string]struct{})
	for _, hit := range sampleAgg.Docs.Hits.Hits {
		var source map[string]any
		if err := json.Unmarshal(hit.Source, &source); err != nil {
			continue
		}
		if attrs, ok := source[fieldPrefix].(map[string]any); ok {
			for k := range attrs {
				if k != "" {
					keySet[k] = struct{}{}
				}
			}
		}
	}

	if len(keySet) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	// Simple sort for deterministic output.
	sortKeys(keys)
	return keys, nil
}

// GetTagValues returns distinct values for a specific tag key within the given scope.
// Uses terms aggregation on attributes.{key} or resource.{key} (flattened sub-fields).
// scope: "resource" or "span".
// filterTags: optional filter conditions to narrow the aggregation scope (e.g. service.name=X).
func (r *TraceReader) GetTagValues(ctx context.Context, tagKey string, timeRange TimeRange, scope string, filterTags map[string]string) ([]string, error) {
	fieldPrefix := FieldAttributes
	if scope == "resource" {
		fieldPrefix = FieldResource
	}
	fieldName := fieldPrefix + "." + tagKey

	// Build query with time range + optional filter conditions.
	qb := esq.NewBuilder().
		Raw(esq.TimeRangeFilter(FieldStartTimeUnixNano, timeRange))

	for k, v := range filterTags {
		clauses := resolveTagTermClauses(k, v)
		qb.Should(1, clauses...)
	}

	searchReq := &SearchRequest{
		Query: qb.Build(),
		Size:  0,
		Aggregations: map[string]any{
			"tag_values": map[string]any{
				"terms": map[string]any{
					"field": fieldName,
					"size":  10000,
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(), searchReq)
	if err != nil {
		return nil, fmt.Errorf("get tag values failed (key=%s, scope=%s): %w", tagKey, scope, err)
	}

	raw, ok := resp.Aggregations["tag_values"]
	if !ok {
		return nil, nil
	}

	values, err := esq.ParseTermsAgg(raw)
	if err != nil {
		return nil, fmt.Errorf("parse tag values: %w", err)
	}

	return values, nil
}

// ListRootSpanNames returns distinct root span names for the given time range.
func (r *TraceReader) ListRootSpanNames(ctx context.Context, timeRange TimeRange, appID string) ([]string, error) {
	return r.listIntrinsicTagValues(ctx, FieldName, timeRange, appID)
}

// ListRootSpanServices returns distinct root span service names for the given time range.
func (r *TraceReader) ListRootSpanServices(ctx context.Context, timeRange TimeRange, appID string) ([]string, error) {
	return r.listIntrinsicTagValues(ctx, FieldServiceName, timeRange, appID)
}

// listIntrinsicTagValues returns distinct values for a top-level ES keyword field
// restricted to root spans (parentSpanId absent). Internal helper shared by
// ListRootSpanNames and ListRootSpanServices.
func (r *TraceReader) listIntrinsicTagValues(ctx context.Context, esField string, timeRange TimeRange, appID string) ([]string, error) {
	searchReq := &SearchRequest{
		Query: map[string]any{
			"bool": map[string]any{
				"must":     []map[string]any{r.timeRangeQuery(timeRange)},
				"must_not": []map[string]any{esq.MustNotQ(esq.ExistsQ(FieldParentSpanID))},
			},
		},
		Size: 0,
		Aggregations: map[string]any{
			"tag_values": map[string]any{
				"terms": map[string]any{
					"field": esField,
					"size":  1000,
				},
			},
		},
	}

	resp, err := r.searcher.Search(ctx, r.indexPattern(appID), searchReq)
	if err != nil {
		return nil, fmt.Errorf("list intrinsic tag values failed (field=%s): %w", esField, err)
	}

	raw, ok := resp.Aggregations["tag_values"]
	if !ok {
		return nil, nil
	}

	values, err := esq.ParseTermsAgg(raw)
	if err != nil {
		return nil, fmt.Errorf("parse intrinsic tag values: %w", err)
	}

	return values, nil
}

// sortKeys does a simple insertion sort for small key sets (typically < 100).
func sortKeys(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// ==================== Span Document Model ====================
// spanDocument is replaced by storedmodel.StoredSpan from Layer 1.
// For backward compatibility with old index data (operation_name, span_kind, etc.),
// see the compat.go unmarshal helpers.

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

// ═══════════════════════════════════════════════════
// Unified Constants for Prometheus & Tempo APIs
// ═══════════════════════════════════════════════════
//
// This file centralizes all hardcoded string constants used across
// prometheus_handler.go, tempo_handler.go, influxdb_handler.go,
// and traceql/ast.go.
//
// Rules:
//   - Do NOT add raw string literals for these values in handler/parser code.
//   - Use named constants to enable tool-assisted rename and eliminate typos.
//   - When adding a new intrinsic tag or aggregation function, add it here first.
//

// ── Prometheus Labels ──────────────────────────────

const (
	// PromLabelName is the Prometheus internal label for metric name.
	// Prometheus requires this label in every time series response.
	PromLabelName = "__name__"

	// PromLabelIgnoreUsage is injected by Grafana Explore Metrics to mark
	// labels that should be converted to groupBy dimensions instead of filters.
	PromLabelIgnoreUsage = "__ignore_usage__"

	// PromLabelLe is the histogram bucket boundary label.
	// Used in histogram_quantile queries: {le="0.005"}.
	PromLabelLe = "le"

	// PromInternalLabelPrefix is the prefix for Prometheus/Grafana internal labels.
	// Labels starting with "__" (e.g. __name__, __ignore_usage__) are metadata
	// and must not be forwarded to the storage backend.
	PromInternalLabelPrefix = "__"
)

// ── PromQL Function Names ──────────────────────────

const (
	// AggHistogramQuantile is the histogram_quantile(θ, expr) function name.
	AggHistogramQuantile = "histogram_quantile"

	// AggTopK is the topk(K, expr) function name.
	AggTopK = "topk"

	// AggBottomK is the bottomk(K, expr) function name.
	AggBottomK = "bottomk"

	// AggSum is the sum aggregation function.
	AggSum = "sum"

	// AggAvg is the avg aggregation function.
	AggAvg = "avg"

	// AggMax is the max aggregation function.
	AggMax = "max"

	// AggMin is the min aggregation function.
	AggMin = "min"

	// AggCount is the count aggregation function.
	AggCount = "count"

	// FnRate is the rate() PromQL function.
	FnRate = "rate"

	// FnIncrease is the increase() PromQL function.
	FnIncrease = "increase"

	// FnIrate is the irate() PromQL function.
	FnIrate = "irate"
)

// AggFuncs is the ordered list of aggregation functions recognized by parseAggWrapper.
// Order matters: longer names must appear before shorter ones to avoid partial matches
// (e.g. "count" must not match inside "count_values").
var AggFuncs = []string{AggSum, AggAvg, AggMax, AggMin, AggCount}

// ── Histogram Sub-Series ───────────────────────────

const (
	// HistogramSubSum is the _sum suffix for histogram sum time series.
	HistogramSubSum = "sum"

	// HistogramSubBucket is the _bucket suffix for histogram bucket time series.
	HistogramSubBucket = "bucket"

	// HistogramSuffixSum is the literal "_sum" suffix used in metric name detection.
	HistogramSuffixSum = "_sum"

	// HistogramSuffixBucket is the literal "_bucket" suffix used in metric name detection.
	HistogramSuffixBucket = "_bucket"

	// HistogramSuffixTotal is checked to avoid matching "_sum" inside "_total".
	HistogramSuffixTotal = "_total"
)

// ── PromQL Response Types ──────────────────────────

const (
	// ResultTypeVector is the Prometheus instant vector result type.
	ResultTypeVector = "vector"

	// ResultTypeMatrix is the Prometheus range vector (matrix) result type.
	ResultTypeMatrix = "matrix"
)

// ── OTel Span Attribute Keys ───────────────────────

const (
	// SpanAttrPromQLExpr is the span attribute key for the raw PromQL query string.
	SpanAttrPromQLExpr = "promql.expr"

	// SpanAttrPromQLMetric is the span attribute key for the parsed metric name.
	SpanAttrPromQLMetric = "promql.metric"

	// SpanAttrPromQLAggregation is the span attribute key for the aggregation function.
	SpanAttrPromQLAggregation = "promql.aggregation"

	// SpanAttrPromQLGroupBy is the span attribute key for the groupBy labels.
	SpanAttrPromQLGroupBy = "promql.group_by"

	// SpanAttrPromQLFunction is the span attribute key for the rate/increase/irate function.
	SpanAttrPromQLFunction = "promql.function"

	// SpanAttrPromQLTopK is the span attribute key for the topk K value.
	SpanAttrPromQLTopK = "promql.top_k"

	// SpanAttrPromQLIsBottomK is the span attribute key for whether this is bottomk.
	SpanAttrPromQLIsBottomK = "promql.is_bottom_k"

	// SpanAttrPromQLSeriesCount is the span attribute key for the series count.
	SpanAttrPromQLSeriesCount = "promql.series_count"

	// SpanAttrPromQLAggregatedCount is the span attribute key for post-aggregation count.
	SpanAttrPromQLAggregatedCount = "promql.aggregated_count"

	// SpanAttrPromQLTopKCount is the span attribute key for post-topk count.
	SpanAttrPromQLTopKCount = "promql.topk_count"

	// SpanAttrESLabels is the span attribute key for the ES query labels.
	SpanAttrESLabels = "es.labels"

	// SpanAttrESGroupByFull is the span attribute key for the ES groupBy fields.
	SpanAttrESGroupByFull = "es.group_by_full"

	// SpanAttrESGroupBy is the span attribute key for the ES groupBy fields (compact).
	SpanAttrESGroupBy = "es.group_by"

	// SpanAttrErrorType is the span attribute key for the error type.
	SpanAttrErrorType = "error.type"
)

// ── Tempo Intrinsic Tag Names ──────────────────────
//
// These represent built-in span/trace properties exposed in the
// /api/v2/search/tags response for Tempo API compatibility.

const (
	// TempoIntrinsicDuration is the span duration (nanoseconds).
	TempoIntrinsicDuration = "duration"

	// TempoIntrinsicKind is the span kind (unspecified/internal/server/client/producer/consumer).
	TempoIntrinsicKind = "kind"

	// TempoIntrinsicName is the span operation name.
	TempoIntrinsicName = "name"

	// TempoIntrinsicStatus is the span status code (unset/ok/error).
	TempoIntrinsicStatus = "status"

	// TempoIntrinsicStatusMessage is the span status message.
	// NOTE: value retrieval not yet implemented (TODO).
	TempoIntrinsicStatusMessage = "statusMessage"

	// TempoIntrinsicRootName is the trace root span operation name.
	// NOTE: not stored in ES; requires trace root span derivation (TODO).
	TempoIntrinsicRootName = "rootName"

	// TempoIntrinsicRootServiceName is the trace root span service name.
	// NOTE: not stored in ES; requires trace root span derivation (TODO).
	TempoIntrinsicRootServiceName = "rootServiceName"
)

// ── Tempo NestedSet Intrinsic Fields ───────────────

const (
	// TempoIntrinsicNestedSetParent is the nested set parent node ID.
	TempoIntrinsicNestedSetParent = "nestedSetParent"

	// TempoIntrinsicNestedSetLeft is the nested set left boundary.
	TempoIntrinsicNestedSetLeft = "nestedSetLeft"

	// TempoIntrinsicNestedSetRight is the nested set right boundary.
	TempoIntrinsicNestedSetRight = "nestedSetRight"

	// TempoIntrinsicTraceDuration is the full trace duration.
	TempoIntrinsicTraceDuration = "traceDuration"
)

// TempoIntrinsicTags is the ordered list of intrinsic tags reported in the /api/v2/search/tags response.
var TempoIntrinsicTags = []string{
	TempoIntrinsicDuration,
	TempoIntrinsicKind,
	TempoIntrinsicName,
	TempoIntrinsicStatus,
	TempoIntrinsicStatusMessage,
	TempoIntrinsicRootName,
	TempoIntrinsicRootServiceName,
}

// ── Tempo Span Kind Values ─────────────────────────

const (
	// SpanKindUnspecifiedStr is the Tempo API string for unspecified span kind.
	SpanKindUnspecifiedStr = "unspecified"

	// SpanKindInternalStr is the Tempo API string for internal span kind.
	SpanKindInternalStr = "internal"

	// SpanKindServerStr is the Tempo API string for server span kind.
	SpanKindServerStr = "server"

	// SpanKindClientStr is the Tempo API string for client span kind.
	SpanKindClientStr = "client"

	// SpanKindProducerStr is the Tempo API string for producer span kind.
	SpanKindProducerStr = "producer"

	// SpanKindConsumerStr is the Tempo API string for consumer span kind.
	SpanKindConsumerStr = "consumer"
)

// TempoSpanKindValues is the ordered list of span kind values reported by the Tempo API.
var TempoSpanKindValues = []string{
	SpanKindUnspecifiedStr,
	SpanKindInternalStr,
	SpanKindServerStr,
	SpanKindClientStr,
	SpanKindProducerStr,
	SpanKindConsumerStr,
}

// ── Tempo Status Code Values ───────────────────────

const (
	// StatusCodeUnsetStr is the Tempo API string for unset status.
	StatusCodeUnsetStr = "unset"

	// StatusCodeOkStr is the Tempo API string for ok status.
	StatusCodeOkStr = "ok"

	// StatusCodeErrorStr is the Tempo API string for error status.
	StatusCodeErrorStr = "error"
)

// TempoStatusCodeValues is the ordered list of status code values reported by the Tempo API.
var TempoStatusCodeValues = []string{
	StatusCodeUnsetStr,
	StatusCodeOkStr,
	StatusCodeErrorStr,
}

// ── Tempo Scope Names ──────────────────────────────

const (
	// TempoScopeResource is the resource scope prefix.
	TempoScopeResource = "resource"

	// TempoScopeSpan is the span scope prefix.
	TempoScopeSpan = "span"

	// TempoScopeIntrinsic is the intrinsic scope name.
	TempoScopeIntrinsic = "intrinsic"

	// TempoScopeEvent is the event scope prefix.
	TempoScopeEvent = "event"

	// TempoScopeTrace is the trace scope prefix.
	TempoScopeTrace = "trace"
)

// ── Tempo Common Attribute Keys (Fallback) ─────────
// Used when backend tag discovery returns empty results.

// TempoCommonSpanAttributeKeys are the default span attribute keys for /api/search/tags.
var TempoCommonSpanAttributeKeys = []string{
	"http.method", "http.url", "http.status_code", "http.route",
	"db.system", "db.name", "db.operation",
	"rpc.system", "rpc.service", "rpc.method",
	"error", "span.kind",
}

// TempoCommonResourceAttributeKeys are the default resource attribute keys for /api/v2/search/tags.
var TempoCommonResourceAttributeKeys = []string{
	"service.name", "service.namespace", "service.version",
	"host.name", "deployment.environment",
}

// TempoV1CommonSpanAttributeKeys are the default span attribute keys for /api/search/tags (v1).
var TempoV1CommonSpanAttributeKeys = []string{
	"http.method", "http.url", "http.status_code",
	"http.route", "http.target",
	"db.system", "db.name", "db.operation",
	"rpc.system", "rpc.service", "rpc.method",
	"messaging.system", "messaging.destination",
	"net.host.name", "net.peer.name",
	"error", "span.kind",
}

// ── OTel Semantic Convention Attribute Keys ────────
// Used as canonical fallback values for PromQL label translation.

const (
	// OTelAttrServiceName is the OTel service.name attribute.
	OTelAttrServiceName = "service.name"

	// OTelAttrSpanKind is the OTel span.kind attribute.
	OTelAttrSpanKind = "span.kind"
)

// ── PromQL → OTel Label Key Translation ────────────
//
// OpenTelemetry standard attributes use dot-separated keys (e.g. "span.kind",
// "service.name"). The Prometheus exporter replaces dots with underscores
// (e.g. "span_kind", "service_name"). ES stores the original OTel dot-format
// keys under the "labels" field.
//
// When ES returns data, label keys are in dot format. GroupBy labels from
// PromQL are in underscore format. We must translate between the two:
//   - Query:   PromQL underscore → ES dot (label_translator.go in elasticsearch)
//   - Response: ES dot → PromQL underscore (translateLabelToPromQL, below)
//
// Custom/user-defined labels (e.g. "client", "server") do NOT need translation
// because they never had dots in OTel.
//
// NOTE: This mapping MUST stay in sync with the prometheusToOtelLabelKeys map
// in provider/elasticsearch/label_translator.go.

// prometheusToOtelLabelKeys maps PromQL underscore-format labels to ES dot-format.
// Used to translate GroupBy labels before aggregation so they match ES response keys.
var prometheusToOtelLabelKeys = map[string]string{
	"span_kind":           OTelAttrSpanKind,
	"span_name":           "span.name",
	"service_name":        OTelAttrServiceName,
	"service_instance_id": "service.instance.id",
	"service_version":     "service.version",
	"service_namespace":   "service.namespace",
	"status_code":         "status.code",
	"status_message":      "status.message",
	"peer_service":        "peer.service",
	"net_peer_name":       "net.peer.name",
	"net_peer_port":       "net.peer.port",
	"net_transport":       "net.transport",
	"net_host_name":       "net.host.name",
	"net_host_port":       "net.host.port",
	"http_method":         "http.method",
	"http_status_code":    "http.status_code",
	"http_route":          "http.route",
	"http_scheme":         "http.scheme",
	"http_host":           "http.host",
	"http_url":            "http.url",
	"http_target":         "http.target",
	"http_client_ip":      "http.client_ip",
	"http_request_size":   "http.request_size",
	"rpc_method":          "rpc.method",
	"rpc_service":         "rpc.service",
	"rpc_system":          "rpc.system",
	"rpc_grpc_status_code": "rpc.grpc.status_code",
	"db_system":           "db.system",
	"db_name":             "db.name",
	"db_operation":        "db.operation",
	"db_statement":        "db.statement",
	"db_user":             "db.user",
	"messaging_system":    "messaging.system",
	"messaging_destination": "messaging.destination",
	"messaging_message_id":  "messaging.message_id",
	"exception_type":      "exception.type",
	"exception_message":   "exception.message",
	"thread_name":         "thread.name",
	"thread_id":           "thread.id",
	"code_function":       "code.function",
	"code_namespace":      "code.namespace",
	"code_filepath":       "code.filepath",
	"url_scheme":          "url.scheme",
	"url_full":            "url.full",
	"url_path":            "url.path",
	"url_query":           "url.query",
}

// otelToPrometheusLabelKeys is the reverse of prometheusToOtelLabelKeys.
// Built at init() to avoid duplication errors.
var otelToPrometheusLabelKeys map[string]string

func init() {
	otelToPrometheusLabelKeys = make(map[string]string, len(prometheusToOtelLabelKeys))
	for promKey, otelKey := range prometheusToOtelLabelKeys {
		otelToPrometheusLabelKeys[otelKey] = promKey
	}
}

// translateLabelToPromQL converts ES dot-format label keys back to PromQL
// underscore format for Prometheus API responses (e.g. "span.name" → "span_name").
func translateLabelToPromQL(otelKey string) string {
	if promKey, ok := otelToPrometheusLabelKeys[otelKey]; ok {
		return promKey
	}
	return otelKey
}

// translateGroupByToOtel converts GroupBy labels from PromQL underscore format
// to ES dot format so they match the keys in ES response data.
func translateGroupByToOtel(groupBy []string) []string {
	result := make([]string, len(groupBy))
	for i, k := range groupBy {
		if otelKey, ok := prometheusToOtelLabelKeys[k]; ok {
			result[i] = otelKey
		} else {
			result[i] = k
		}
	}
	return result
}

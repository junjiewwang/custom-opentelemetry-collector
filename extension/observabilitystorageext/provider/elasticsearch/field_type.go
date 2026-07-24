// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

// field_type.go centralizes the "does this ES field need a .keyword suffix?"
// decision that was previously spread across three divergent mechanisms:
//
//   1. knownAggregatableFields (global map) — used by the trace and metric
//      readers (metricsAggField, buildMetricQuery, buildAggregation,
//      GetTagValues).
//   2. labelFieldsNeedKeyword (log-only map) — used by resolveLogLabelESField.
//   3. status.message special-cases duplicated in trace_reader.go and
//      trace_metrics.go.
//
// The catch: a field's ES type is per-signal (per index template). For example
// resource.host.name is explicitly mapped as keyword in the TRACE template but
// is a dynamic text field in the LOG template (whose resource is a bare
// {"type":"object","dynamic":true}). A single global table cannot represent
// that, so the table is keyed by signal.

// aggregatableFields lists, per signal, the ES fields that are mapped as an
// aggregatable type (keyword/long/integer/boolean/...) and therefore do NOT
// need a .keyword suffix for terms/composite aggregation or exact term query.
//
// Any field NOT listed for a given signal is assumed to be a dynamic text field
// (via the strings_as_keyword dynamic template) and gets a .keyword suffix —
// the safe default (worst case: aggregation returns empty, never a panic).
//
// The trace entries must match the trace index template
// (traceTemplateMappings); this is enforced by
// TestKnownAggregatableFields_MatchTraceTemplate in field_consistency_test.go.
// The attributes.* / resource.process.pid "dynamic numeric" entries are typed
// by ES at runtime (not in the static template) and are documented in
// TestKnownAggregatableFields_DynamicNumericEntriesAreDocumented.
var aggregatableFields = map[string]map[string]bool{
	"trace": {
		// Intrinsic top-level fields (explicitly mapped in trace template).
		FieldKind: true, FieldName: true, FieldSpanID: true, FieldTraceID: true,
		FieldParentSpanID: true, FieldServiceName: true,
		FieldStartTimeUnixNano: true, FieldEndTimeUnixNano: true, FieldDurationNano: true,

		// Dynamic numeric/bool attributes — typed by ES at runtime (no .keyword
		// sub-field exists, so .keyword must NOT be added).
		FieldAttributes + ".thread.id":                          true, // long
		FieldAttributes + ".order_error":                        true, // boolean
		FieldAttributes + ".rpc.grpc_status_code":               true, // int
		FieldAttributes + ".server.port":                        true, // long
		FieldAttributes + ".network.peer_port":                  true, // long
		FieldAttributes + ".messaging.kafka_message_offset":     true, // long
		FieldAttributes + ".messaging.message_body_size":        true, // long
		FieldAttributes + ".messaging.destination_partition_id": true, // long
		FieldAttributes + ".http.status_code":                   true, // long
		FieldAttributes + ".http.response_status_code":          true, // long

		// resource.* fields explicitly mapped as keyword in the trace template.
		FieldResource + ".service.name":      true,
		FieldResource + ".host.name":         true,
		FieldResource + ".service.namespace": true,
		FieldResource + ".service.version":   true,
		FieldResource + ".process.pid":       true, // dynamically mapped long
	},
	// metric: top-level keyword/numeric fields from the metric template. metric
	// labels.* are dynamic text+keyword (via strings_as_keyword) and are NOT
	// listed here, so they always get .keyword — correct.
	"metric": {
		FieldMetricTimeUnixMilli: true, // date (epoch_millis)
		FieldName:                true, // keyword
		FieldMetricType:          true, // keyword
		FieldMetricValue:         true, // double
		FieldServiceName:         true, // keyword
		FieldAppID:               true, // keyword
	},
	// log: top-level keyword/numeric fields from the log template. resource.*
	// sub-fields are dynamic text (the log template maps resource as a bare
	// dynamic object), so none are aggregatable without .keyword — the
	// per-signal difference from trace (where resource.host.name is keyword).
	// Exception: resource.process.pid is dynamically mapped as long by ES at
	// runtime (numeric → aggregatable, no .keyword sub-field), so it is listed.
	"log": {
		FieldLogTimeUnixNano:           true, // long
		FieldLogObservedTimeUnixNano:   true, // long
		FieldTraceID:                   true, // keyword
		FieldSpanID:                    true, // keyword
		FieldLogSeverityText:           true, // keyword
		FieldLogSeverityNumber:         true, // integer
		FieldServiceName:               true, // keyword
		FieldAppID:                     true, // keyword
		FieldResource + ".process.pid": true, // dynamically mapped long
	},
}

// needsKeyword reports whether the given ES field needs a .keyword suffix for
// exact term matching or terms/composite aggregation on the given signal.
// Returns false (no suffix) for fields explicitly mapped as an aggregatable
// type in that signal's template; true (needs .keyword) otherwise.
func needsKeyword(signal, esField string) bool {
	return !aggregatableFields[signal][esField]
}

// aggregatableField returns the ES field to use for terms/composite aggregation
// on the given signal: the field itself if it is aggregatable, else the field
// with a .keyword suffix. This is the single decision point replacing the old
// `if !knownAggregatableFields[field] { field += ".keyword" }` idiom.
func aggregatableField(signal, esField string) string {
	if needsKeyword(signal, esField) {
		return esField + ".keyword"
	}
	return esField
}

// matchFields lists ES fields that must be queried with a "match" (analyzed,
// full-text) clause rather than a "term" (exact) clause, even though a
// .keyword sub-field exists. This is a semantic choice (substring/token
// matching), NOT a .keyword-availability issue — status.message has a .keyword
// sub-field (see traceTemplateMappings) but status-message filters want
// analyzed matching.
//
// Kept separate from the .keyword decision so the two concerns (aggregation
// field selection vs. query clause kind) don't tangle, replacing the
// duplicated special-cases that previously lived in trace_reader.go and
// trace_metrics.go.
var matchFields = map[string]bool{
	FieldStatus + ".message": true,
}

// needsMatchQuery reports whether the given ES field should be queried with a
// "match" clause (analyzed full-text) instead of "term" (exact). Callers that
// build term/value filters use this to pick the clause kind.
func needsMatchQuery(esField string) bool {
	return matchFields[esField]
}

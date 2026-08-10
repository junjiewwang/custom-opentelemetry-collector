// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

// ═══════════════════════════════════════════════════
// MetricLabelResolver — single source of truth for label→ES-field mapping
// ═══════════════════════════════════════════════════
//
// Some PromQL labels do NOT live under the ES "labels" object — they are
// resource-derived attributes promoted to TOP-LEVEL fields during write.
// Notably service.name → top-level "serviceName" (jvm/runtime metrics have
// an EMPTY labels object, so a filter on labels.service_name.keyword matches
// 0 docs). A query like {service_name="x"} must target serviceName, not
// labels.service_name.keyword.
//
// Before this resolver, that promotion was a scattered "if label == service_name"
// check in buildMetricFilter (×4 matcher kinds) and buildAggregation (group-by).
// Adding a second promoted field would have meant N more call-site edits.
// This file is the single source: add a row to promotedFields and every matcher
// kind + group-by picks it up automatically.

// promotedFields maps a normalized PromQL label key (underscore form, the output
// of translateLabelKey) to its TOP-LEVEL ES field. A label here is "promoted":
// it is filtered/grouped against the top-level field, never labels.<key>.
//
// To add a future promoted field: add one row here. No call-site changes.
var promotedFields = map[string]string{
	"service_name": FieldServiceName,
}

// MetricLabelResolver maps a PromQL label name to its ES field path and reports
// whether it is a promoted top-level field. It merges three concerns that were
// previously scattered across call sites:
//  1. key normalization (translateLabelKey — service_name/service.name variants)
//  2. aggregatable-field suffixing (aggregatableField — labels.<key>.keyword)
//  3. promotion to a top-level field (promotedFields)
//
// It is stateless; MetricReader embeds a zero-value instance.
type MetricLabelResolver struct{}

// ResolvedLabel is the result of resolving a label name.
type ResolvedLabel struct {
	// ESField is the full ES field path to use for term/terms/composite/regex
	// matching: either a top-level field ("serviceName") for promoted labels,
	// or "labels.<key>.keyword" for regular metric labels.
	ESField string
	// IsPromoted is true when the label maps to a top-level field rather than
	// the labels object. Callers that need to distinguish (e.g. mergeServiceName
	// output-side logic) can check this without re-parsing the field string.
	IsPromoted bool
}

// Resolve normalizes the label key then resolves it to an ES field.
//
// Idempotent on already-normalized keys: buildMetricFilter receives keys already
// passed through normalizeMetricQueryLabels (→ translateLabelKey), while
// buildAggregation's group-by keys arrive raw. Both paths resolve identically
// because translateLabelKey is a fixed point on underscore-form keys.
//
// Grafana occasionally emits camelCase variants (e.g. "serviceName", "spanName").
// translateLabelKey does not recognize those (they are not in
// prometheusToOtelLabelKeys), so we first camelCase→snake_case to unify them
// with the underscore form the rest of the pipeline expects.
func (r MetricLabelResolver) Resolve(label string) ResolvedLabel {
	esKey := translateLabelKey(camelToSnake(label))
	if topField, ok := promotedFields[esKey]; ok {
		return ResolvedLabel{ESField: topField, IsPromoted: true}
	}
	// Regular metric label: stored under the dynamic "labels" object as
	// text+keyword. Aggregation/exact-match must use the .keyword sub-field.
	return ResolvedLabel{
		ESField:    aggregatableField("metric", FieldMetricLabels+"."+esKey),
		IsPromoted: false,
	}
}

// camelToSnake converts camelCase to snake_case (serviceName → service_name,
// spanName → span_name). Already-snake input is unchanged (no uppercase to
// split on). Used so Grafana's camelCase label variants resolve the same as
// the underscore form.
func camelToSnake(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				b = append(b, '_')
			}
			b = append(b, c+('a'-'A'))
		} else {
			b = append(b, c)
		}
	}
	return string(b)
}

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package victoriametrics

import (
	"fmt"
	"sort"
	"strings"
)

// buildSelector builds a MetricsQL series selector from query fields.
// Every part is optional; the AppID matcher is injected first so app
// isolation applies to all reads. %q performs the same string-literal
// escaping MetricsQL requires (backslash, quote, newline).
func buildSelector(metricName, appID, serviceName string, labels, labelMatch, labelNot, labelNotMatch map[string]string) string {
	var parts []string
	if appID != "" {
		parts = append(parts, fmt.Sprintf("app_id=%q", appID))
	}
	if serviceName != "" {
		parts = append(parts, fmt.Sprintf("service_name=%q", serviceName))
	}
	// Deterministic order (map iteration is random): exact, then regex,
	// then negations, each key-sorted.
	addAll := func(m map[string]string, op string) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s%s%q", k, op, m[k]))
		}
	}
	addAll(labels, "=")
	addAll(labelMatch, "=~")
	addAll(labelNot, "!=")
	addAll(labelNotMatch, "!~")
	if len(parts) == 0 {
		return metricName
	}
	return metricName + "{" + strings.Join(parts, ",") + "}"
}

// buildRangeAggregation wraps a selector in a MetricsQL aggregation for
// MetricRangeQuery semantics: agg(selector) or agg by (keys) (selector).
// agg is one of avg/sum/min/max/count/last/first; empty returns the bare selector.
func buildRangeAggregation(agg, selector string, groupBy []string) string {
	if agg == "" {
		return selector
	}
	if len(groupBy) == 0 {
		return fmt.Sprintf("%s(%s)", agg, selector)
	}
	// groupBy keys are PromQL underscore-form already; sort for determinism.
	keys := append([]string(nil), groupBy...)
	sort.Strings(keys)
	return fmt.Sprintf("%s by (%s) (%s)", agg, strings.Join(keys, ","), selector)
}

// stripAppIDLabel removes the internal app_id label from a returned label set
// so Grafana never sees it (mirrors ES per-app index isolation invisibility).
// Returns a new map; the input is not mutated.
func stripAppIDLabel(labels map[string]string) map[string]string {
	if _, ok := labels["app_id"]; !ok {
		return labels
	}
	out := make(map[string]string, len(labels)-1)
	for k, v := range labels {
		if k != "app_id" {
			out[k] = v
		}
	}
	return out
}

// isVMPrefix reports whether a metric name belongs to VictoriaMetrics' own
// self-monitoring namespace (vm_*), which must not surface in user-facing
// metric listings.
func isVMPrefix(name string) bool {
	return strings.HasPrefix(name, "vm_") || strings.HasPrefix(name, "vmalert_") || strings.HasPrefix(name, "vmagent_")
}

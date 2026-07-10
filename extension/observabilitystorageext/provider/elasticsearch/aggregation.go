// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"encoding/json"
	"fmt"
	"sort"
)

// AggregationFunc defines an aggregation function for metric range queries.
// Build returns the ES aggregation DSL sub-clause.
// ParseValue extracts the numeric value from the ES aggregation result.
type AggregationFunc struct {
	Name       string
	Build      func(field string) map[string]any
	ParseValue func(raw json.RawMessage) *float64
}

// aggregationRegistry maps aggregation names to their implementations.
// Uses a strategy pattern so new aggregations can be added without modifying existing code.
var aggregationRegistry = map[string]*AggregationFunc{
	"avg": {
		Name: "avg",
		Build: func(field string) map[string]any {
			return map[string]any{"avg": map[string]any{"field": field}}
		},
		ParseValue: parseSimpleValue,
	},
	"sum": {
		Name: "sum",
		Build: func(field string) map[string]any {
			return map[string]any{"sum": map[string]any{"field": field}}
		},
		ParseValue: parseSimpleValue,
	},
	"max": {
		Name: "max",
		Build: func(field string) map[string]any {
			return map[string]any{"max": map[string]any{"field": field}}
		},
		ParseValue: parseSimpleValue,
	},
	"min": {
		Name: "min",
		Build: func(field string) map[string]any {
			return map[string]any{"min": map[string]any{"field": field}}
		},
		ParseValue: parseSimpleValue,
	},
	"count": {
		Name: "count",
		Build: func(field string) map[string]any {
			return map[string]any{"value_count": map[string]any{"field": field}}
		},
		ParseValue: parseSimpleValue,
	},
	"last": {
		Name: "last",
		Build: func(field string) map[string]any {
			return map[string]any{
				"top_hits": map[string]any{
					"size":    1,
					"sort":    []map[string]any{{FieldMetricTimeUnixMilli: map[string]any{"order": "desc"}}},
					"_source": []string{field},
				},
			}
		},
		ParseValue: parseTopHitsValue(),
	},
	"first": {
		Name: "first",
		Build: func(field string) map[string]any {
			return map[string]any{
				"top_hits": map[string]any{
					"size":    1,
					"sort":    []map[string]any{{FieldMetricTimeUnixMilli: map[string]any{"order": "asc"}}},
					"_source": []string{field},
				},
			}
		},
		ParseValue: parseTopHitsValue(),
	},
	"p50": {
		Name:  "p50",
		Build: buildPercentile(50),
		ParseValue: func(raw json.RawMessage) *float64 {
			return parsePercentileValue(raw, 50)
		},
	},
	"p90": {
		Name:  "p90",
		Build: buildPercentile(90),
		ParseValue: func(raw json.RawMessage) *float64 {
			return parsePercentileValue(raw, 90)
		},
	},
	"p95": {
		Name:  "p95",
		Build: buildPercentile(95),
		ParseValue: func(raw json.RawMessage) *float64 {
			return parsePercentileValue(raw, 95)
		},
	},
	"p99": {
		Name:  "p99",
		Build: buildPercentile(99),
		ParseValue: func(raw json.RawMessage) *float64 {
			return parsePercentileValue(raw, 99)
		},
	},
}

// GetAggregation returns the aggregation function by name, or an error if not found.
func GetAggregation(name string) (*AggregationFunc, error) {
	if name == "" {
		name = "avg" // default
	}
	agg, ok := aggregationRegistry[name]
	if !ok {
		return nil, fmt.Errorf("unsupported aggregation: %q, valid values: %v", name, ValidAggregations())
	}
	return agg, nil
}

// ValidAggregations returns the list of valid aggregation names.
func ValidAggregations() []string {
	names := make([]string, 0, len(aggregationRegistry))
	for k := range aggregationRegistry {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// parseSimpleValue extracts value from simple aggregation results (avg, sum, max, min, value_count).
func parseSimpleValue(raw json.RawMessage) *float64 {
	var result struct {
		Value *float64 `json:"value"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	return result.Value
}

// parseTopHitsValue extracts value from top_hits results.
func parseTopHitsValue() func(json.RawMessage) *float64 {
	return func(raw json.RawMessage) *float64 {
		var result struct {
			Hits struct {
				Hits []struct {
					Source struct {
						Value float64 `json:"value"`
					} `json:"_source"`
				} `json:"hits"`
			} `json:"hits"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil
		}
		if len(result.Hits.Hits) == 0 {
			return nil
		}
		v := result.Hits.Hits[0].Source.Value
		return &v
	}
}

// buildPercentile returns a builder for percentile aggregations at the given percentile.
func buildPercentile(pct float64) func(field string) map[string]any {
	return func(field string) map[string]any {
		return map[string]any{
			"percentiles": map[string]any{
				"field":    field,
				"percents": []float64{pct},
			},
		}
	}
}

// parsePercentileValue extracts a specific percentile from the percentiles aggregation result.
func parsePercentileValue(raw json.RawMessage, pct float64) *float64 {
	var result struct {
		Values map[string]float64 `json:"values"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	key := fmt.Sprintf("%.1f", pct)
	if v, ok := result.Values[key]; ok {
		return &v
	}
	return nil
}

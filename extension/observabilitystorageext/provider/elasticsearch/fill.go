// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"math"
	"sort"
)

// NilValue is a sentinel value used to represent empty/nil metric data points.
// NaN is chosen because it's not a valid metric value and can be detected via math.IsNaN().
var NilValue = math.NaN()

// FillStrategy applies a fill strategy to metric data points.
type FillStrategy func(values []MetricDataPoint) []MetricDataPoint

// fillStrategies maps fill strategy names to their implementations.
var fillStrategies = map[string]FillStrategy{
	"null":     fillNull,
	"none":     fillNone,
	"0":        fillZero,
	"previous": fillPrevious,
	"linear":   fillLinear,
}

// GetFillStrategy returns the fill strategy by name. Empty or unknown fall back to fillNull.
func GetFillStrategy(name string) FillStrategy {
	if name == "" {
		name = "null"
	}
	if fn, ok := fillStrategies[name]; ok {
		return fn
	}
	return fillNull
}

// ValidFillStrategies returns the list of valid fill strategy names.
func ValidFillStrategies() []string {
	names := make([]string, 0, len(fillStrategies))
	for k := range fillStrategies {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// fillNull keeps nil values as-is (chart renders as gaps).
func fillNull(values []MetricDataPoint) []MetricDataPoint {
	return values
}

// fillNone removes nil data points, leaving only points with real values.
func fillNone(values []MetricDataPoint) []MetricDataPoint {
	filtered := make([]MetricDataPoint, 0, len(values))
	for _, v := range values {
		if !isNilPoint(v) {
			filtered = append(filtered, v)
		}
	}
	return filtered
}

// fillZero replaces nil values with 0.
func fillZero(values []MetricDataPoint) []MetricDataPoint {
	for i := range values {
		if isNilPoint(values[i]) {
			values[i].Value = 0
		}
	}
	return values
}

// fillPrevious replaces nil values with the previous non-nil value.
func fillPrevious(values []MetricDataPoint) []MetricDataPoint {
	if len(values) == 0 {
		return values
	}
	// Forward pass: carry forward
	var lastValid float64
	hasLastValid := false
	for i := range values {
		if !isNilPoint(values[i]) {
			lastValid = values[i].Value
			hasLastValid = true
		} else if hasLastValid {
			values[i].Value = lastValid
		}
	}
	return values
}

// fillLinear performs linear interpolation between non-nil values.
func fillLinear(values []MetricDataPoint) []MetricDataPoint {
	if len(values) < 3 {
		return values
	}

	// Find the first and last non-nil indices
	firstValid := -1
	lastValid := -1
	for i, v := range values {
		if !isNilPoint(v) {
			if firstValid == -1 {
				firstValid = i
			}
			lastValid = i
		}
	}
	if firstValid == -1 {
		return values // all nil
	}

	// Interpolate between consecutive non-nil points
	prevIdx := firstValid
	for i := firstValid + 1; i <= lastValid; i++ {
		if !isNilPoint(values[i]) {
			gapSize := i - prevIdx
			if gapSize > 1 {
				startVal := values[prevIdx].Value
				endVal := values[i].Value
				step := (endVal - startVal) / float64(gapSize)
				for j := 1; j < gapSize; j++ {
					idx := prevIdx + j
					values[idx].Value = startVal + step*float64(j)
				}
			}
			prevIdx = i
		}
	}

	return values
}

// isNilPoint returns true if the data point has a nil/sentinel value.
func isNilPoint(dp MetricDataPoint) bool {
	return math.IsNaN(dp.Value)
}


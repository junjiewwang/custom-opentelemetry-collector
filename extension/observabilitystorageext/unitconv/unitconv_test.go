// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package unitconv

import (
	"math"
	"testing"
)

func TestToSeconds(t *testing.T) {
	tests := []struct {
		name       string
		value      float64
		sourceUnit DurationSourceUnit
		want       float64
	}{
		// Nanoseconds → Seconds
		{
			name:       "nanoseconds: 1 second in nanos",
			value:      1_000_000_000,
			sourceUnit: DurationUnitNanoseconds,
			want:       1.0,
		},
		{
			name:       "nanoseconds: 150ms in nanos",
			value:      150_000_000,
			sourceUnit: DurationUnitNanoseconds,
			want:       0.15,
		},
		{
			name:       "nanoseconds: 1.5s in nanos",
			value:      1_500_000_000,
			sourceUnit: DurationUnitNanoseconds,
			want:       1.5,
		},
		{
			name:       "nanoseconds: sub-millisecond (500μs)",
			value:      500_000,
			sourceUnit: DurationUnitNanoseconds,
			want:       0.0005,
		},
		{
			name:       "nanoseconds: zero",
			value:      0,
			sourceUnit: DurationUnitNanoseconds,
			want:       0,
		},
		{
			name:       "nanoseconds: large value (10 minutes)",
			value:      600_000_000_000,
			sourceUnit: DurationUnitNanoseconds,
			want:       600.0,
		},

		// Milliseconds → Seconds
		{
			name:       "milliseconds: 1 second",
			value:      1000,
			sourceUnit: DurationUnitMilliseconds,
			want:       1.0,
		},
		{
			name:       "milliseconds: 150ms",
			value:      150,
			sourceUnit: DurationUnitMilliseconds,
			want:       0.15,
		},
		{
			name:       "milliseconds: 2.5 seconds",
			value:      2500,
			sourceUnit: DurationUnitMilliseconds,
			want:       2.5,
		},
		{
			name:       "milliseconds: sub-ms precision (0.5ms)",
			value:      0.5,
			sourceUnit: DurationUnitMilliseconds,
			want:       0.0005,
		},
		{
			name:       "milliseconds: zero",
			value:      0,
			sourceUnit: DurationUnitMilliseconds,
			want:       0,
		},

		// Already in seconds — no conversion
		{
			name:       "seconds: passthrough",
			value:      1.5,
			sourceUnit: DurationUnitSeconds,
			want:       1.5,
		},
		{
			name:       "seconds: zero",
			value:      0,
			sourceUnit: DurationUnitSeconds,
			want:       0,
		},

		// No unit semantics — no conversion
		{
			name:       "none: passthrough (rate value)",
			value:      42.0,
			sourceUnit: DurationUnitNone,
			want:       42.0,
		},
		{
			name:       "none: zero",
			value:      0,
			sourceUnit: DurationUnitNone,
			want:       0,
		},

		// Unknown unit — safe passthrough
		{
			name:       "unknown unit: passthrough",
			value:      99.9,
			sourceUnit: DurationSourceUnit(99),
			want:       99.9,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToSeconds(tt.value, tt.sourceUnit)
			if math.Abs(got-tt.want) > 1e-12 {
				t.Errorf("ToSeconds(%v, %v) = %v, want %v",
					tt.value, tt.sourceUnit, got, tt.want)
			}
		})
	}
}

func TestNormalizeSlice(t *testing.T) {
	tests := []struct {
		name       string
		values     []float64
		sourceUnit DurationSourceUnit
		want       []float64
	}{
		{
			name:       "milliseconds batch",
			values:     []float64{100, 200, 500, 1000},
			sourceUnit: DurationUnitMilliseconds,
			want:       []float64{0.1, 0.2, 0.5, 1.0},
		},
		{
			name:       "nanoseconds batch",
			values:     []float64{1e8, 5e8, 1e9},
			sourceUnit: DurationUnitNanoseconds,
			want:       []float64{0.1, 0.5, 1.0},
		},
		{
			name:       "none: no modification",
			values:     []float64{1, 2, 3},
			sourceUnit: DurationUnitNone,
			want:       []float64{1, 2, 3},
		},
		{
			name:       "seconds: no modification",
			values:     []float64{1.5, 2.5},
			sourceUnit: DurationUnitSeconds,
			want:       []float64{1.5, 2.5},
		},
		{
			name:       "empty slice",
			values:     []float64{},
			sourceUnit: DurationUnitMilliseconds,
			want:       []float64{},
		},
		{
			name:       "nil slice",
			values:     nil,
			sourceUnit: DurationUnitMilliseconds,
			want:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Copy to avoid modifying test data
			var input []float64
			if tt.values != nil {
				input = make([]float64, len(tt.values))
				copy(input, tt.values)
			}

			NormalizeSlice(input, tt.sourceUnit)

			if len(input) != len(tt.want) {
				t.Fatalf("length mismatch: got %d, want %d", len(input), len(tt.want))
			}
			for i := range input {
				if math.Abs(input[i]-tt.want[i]) > 1e-12 {
					t.Errorf("index %d: got %v, want %v", i, input[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsDurationFunction(t *testing.T) {
	tests := []struct {
		function string
		field    string
		want     bool
	}{
		{"quantile_over_time", "duration", true},
		{"histogram_over_time", "duration", false}, // histogram_over_time returns count, not duration
		{"rate", "duration", false},
		{"count_over_time", "duration", false},
		{"quantile_over_time", "other_field", false},
		{"histogram_over_time", "", false},
		{"", "duration", false},
		{"", "", false},
	}

	for _, tt := range tests {
		name := tt.function + "/" + tt.field
		if name == "/" {
			name = "empty/empty"
		}
		t.Run(name, func(t *testing.T) {
			got := IsDurationFunction(tt.function, tt.field)
			if got != tt.want {
				t.Errorf("IsDurationFunction(%q, %q) = %v, want %v",
					tt.function, tt.field, got, tt.want)
			}
		})
	}
}

func TestSourceUnitForTraceReader(t *testing.T) {
	tests := []struct {
		function string
		field    string
		want     DurationSourceUnit
	}{
		{"quantile_over_time", "duration", DurationUnitNanoseconds},
		{"histogram_over_time", "duration", DurationUnitNone}, // histogram_over_time returns count, not duration
		{"rate", "duration", DurationUnitNone},
		{"count_over_time", "duration", DurationUnitNone},
		{"quantile_over_time", "other", DurationUnitNone},
	}

	for _, tt := range tests {
		t.Run(tt.function+"/"+tt.field, func(t *testing.T) {
			got := SourceUnitForTraceReader(tt.function, tt.field)
			if got != tt.want {
				t.Errorf("SourceUnitForTraceReader(%q, %q) = %v, want %v",
					tt.function, tt.field, got, tt.want)
			}
		})
	}
}

func TestSourceUnitForMetricReader(t *testing.T) {
	tests := []struct {
		function string
		field    string
		want     DurationSourceUnit
	}{
		{"quantile_over_time", "duration", DurationUnitMilliseconds},
		{"histogram_over_time", "duration", DurationUnitNone}, // histogram_over_time returns count, not duration
		{"rate", "duration", DurationUnitNone},
		{"count_over_time", "duration", DurationUnitNone},
		{"quantile_over_time", "other", DurationUnitNone},
	}

	for _, tt := range tests {
		t.Run(tt.function+"/"+tt.field, func(t *testing.T) {
			got := SourceUnitForMetricReader(tt.function, tt.field)
			if got != tt.want {
				t.Errorf("SourceUnitForMetricReader(%q, %q) = %v, want %v",
					tt.function, tt.field, got, tt.want)
			}
		})
	}
}

// Benchmark to prove the conversion is zero-allocation and near-free.
func BenchmarkToSeconds(b *testing.B) {
	b.Run("nanoseconds", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = ToSeconds(150_000_000, DurationUnitNanoseconds)
		}
	})
	b.Run("milliseconds", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = ToSeconds(150, DurationUnitMilliseconds)
		}
	})
	b.Run("none_passthrough", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = ToSeconds(42, DurationUnitNone)
		}
	})
}

func BenchmarkNormalizeSlice(b *testing.B) {
	values := make([]float64, 1000)
	for i := range values {
		values[i] = float64(i) * 1_000_000
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NormalizeSlice(values, DurationUnitNanoseconds)
	}
}

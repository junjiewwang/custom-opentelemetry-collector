package adminext

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func topkSeries(name string, values ...float64) promMatrixSample {
	pts := make([][]any, 0, len(values))
	for i, v := range values {
		pts = append(pts, []any{float64(i), formatPromValue(v)})
	}
	return promMatrixSample{Metric: promMetric{"series": name}, Values: pts}
}

func matrixNames(ms []promMatrixSample) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Metric["series"])
	}
	return out
}

// topk was applied on the instant path only, so a range query returned every
// series and Grafana drew N lines for a topk(K) panel. Ranking is by each
// series' extreme over the whole range, giving a stable set of K lines.
func TestApplyTopKMatrix(t *testing.T) {
	tests := []struct {
		name      string
		k         int
		isBottomK bool
		input     []promMatrixSample
		expected  []string
	}{
		{
			name: "topk picks the highest peaks",
			k:    2,
			input: []promMatrixSample{
				topkSeries("low", 1, 2, 1),
				topkSeries("high", 50, 60, 55),
				topkSeries("mid", 10, 12, 11),
			},
			expected: []string{"high", "mid"},
		},
		{
			name:      "bottomk picks the lowest dips",
			k:         2,
			isBottomK: true,
			input: []promMatrixSample{
				topkSeries("low", 1, 2, 1),
				topkSeries("high", 50, 60, 55),
				topkSeries("mid", 10, 12, 11),
			},
			expected: []string{"low", "mid"},
		},
		{
			name: "ranks by peak, not by final value",
			k:    1,
			input: []promMatrixSample{
				// Spikes high then falls below the other series' last point.
				topkSeries("spiky", 100, 5, 1),
				topkSeries("steady", 20, 20, 20),
			},
			expected: []string{"spiky"},
		},
		{
			name: "k >= series count returns everything",
			k:    5,
			input: []promMatrixSample{
				topkSeries("a", 1),
				topkSeries("b", 2),
			},
			expected: []string{"a", "b"},
		},
		{
			name: "series with no usable points rank last",
			k:    1,
			input: []promMatrixSample{
				{Metric: promMetric{"series": "empty"}, Values: [][]any{}},
				topkSeries("real", 7),
			},
			expected: []string{"real"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := applyTopKMatrix(tc.k, tc.isBottomK, tc.input)
			assert.Equal(t, tc.expected, matrixNames(got))
		})
	}
}

func TestApplyTopKMatrix_ZeroKIsNoOp(t *testing.T) {
	in := []promMatrixSample{topkSeries("a", 1), topkSeries("b", 2)}
	assert.Equal(t, in, applyTopKMatrix(0, false, in))
}

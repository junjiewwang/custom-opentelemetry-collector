package adminext

import (
	"testing"
	"time"

	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext"
)

// TestReproSubsetFallbackGrouping simulates the subset-parser fallback for
// sum by (http_method) (rate(m[4m])) over samples with/without http_method,
// verifying aggregateMatrix keeps the http_method dimension.
func TestReproSubsetFallbackGrouping(t *testing.T) {
	base := time.Now()
	mk := func(labels map[string]string, startVal float64) []observabilitystorageext.MetricSample {
		var out []observabilitystorageext.MetricSample
		for i := 0; i < 5; i++ {
			out = append(out, observabilitystorageext.MetricSample{
				TimestampMs: base.Add(time.Duration(i)*time.Minute).UnixMilli(),
				Value:       startVal + float64(i)*10,
				Labels:      labels,
			})
		}
		return out
	}
	samples := []observabilitystorageext.MetricSample{}
	samples = append(samples, mk(map[string]string{"http_method": "GET", "service_name": "a"}, 100)...)
	samples = append(samples, mk(map[string]string{"http_method": "POST", "service_name": "a"}, 500)...)
	samples = append(samples, mk(map[string]string{"service_name": "b"}, 900)...) // no http_method

	groups := groupMetricSamplesByLabels(samples)
	if len(groups) != 3 {
		t.Fatalf("expected 3 sample groups, got %d", len(groups))
	}

	// Build matrix like execRateRange does, then aggregate with GroupBy=[http_method].
	var matrix []promMatrixSample
	for _, sg := range groups {
		m := promMetric{PromLabelName: "m"}
		for k, v := range sg.Labels {
			m[translateLabelToPromQL(k)] = v
		}
		values := make([][]any, 0, len(sg.Samples))
		for _, s := range sg.Samples {
			values = append(values, []any{float64(s.TimestampMs) / 1000.0, formatPromValue(s.Value)})
		}
		matrix = append(matrix, promMatrixSample{Metric: m, Values: values})
	}
	out := aggregateMatrix("sum", []string{"http_method"}, matrix)
	t.Logf("aggregated series=%d", len(out))
	for _, s := range out {
		t.Logf("  labels=%v points=%d", map[string]string(s.Metric), len(s.Values))
	}
	if len(out) != 3 {
		t.Errorf("expected 3 output series (GET/POST/none), got %d", len(out))
	}
}

package adminext

import "testing"

func TestHasDivisionOperator(t *testing.T) {
	cases := []struct {
		name string
		q    string
		want bool
	}{
		{"true division", "jvm.memory.used / jvm.memory.limit * 100", true},
		{"slash in label value", `traces_spanmetrics_latency_count{span_name="GET /order/mockGenerated"}`, false},
		{"slash in backtick label", "traces_spanmetrics_calls_total{span_name=`GET /order`}", false},
		{"no slash", "traces_spanmetrics_latency_count{span_name=\"order-topic process\"}", false},
		{"multiple slashes in labels", `rate(traces_spanmetrics_latency_sum{http_route="/api/v1/users"}[5m])`, false},
		{"division with rate", "rate(a[5m]) / rate(b[5m])", true},
	}
	for _, c := range cases {
		got := hasDivisionOperator(c.q)
		if got != c.want {
			t.Errorf("%s: hasDivisionOperator(%q) = %v, want %v", c.name, c.q, got, c.want)
		}
	}
}

func TestIsComplexPromQL_SlashInLabelValue(t *testing.T) {
	// A bare histogram _count selector with a URL-path span_name must NOT be
	// misrouted to the engine (which cannot resolve the _count suffix).
	q := `traces_spanmetrics_latency_count{span_name="GET /order/mockGenerated"}`
	if isComplexPromQL(q) {
		t.Errorf("isComplexPromQL(%q) = true, want false (slash is a label value, not division)", q)
	}
	// A bare _count with no slash also stays in the subset parser.
	if isComplexPromQL(`traces_spanmetrics_latency_count{span_name="order-topic process"}`) {
		t.Errorf("bare _count with plain label should not be complex")
	}
	// A true division still routes to the engine.
	if !isComplexPromQL(`jvm_memory_used / jvm_memory_limit`) {
		t.Errorf("true division must be complex")
	}
}

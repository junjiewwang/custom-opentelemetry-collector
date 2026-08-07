package elasticsearch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// Metric labels are dynamically mapped as text + .keyword. term/terms/prefix on
// the bare (analyzed) field silently matches nothing for multi-token values, so
// every matcher kind — exact, regex, and their negations — must target .keyword.
// Regression test for =~ / !~ having used the bare field while = / != did not,
// which made Grafana ad-hoc filters return empty results.
func TestBuildMetricFilter_AllMatchersUseKeyword(t *testing.T) {
	r := &MetricReader{logger: zap.NewNop()}
	tr := TimeRange{Start: time.Unix(1000, 0), End: time.Unix(2000, 0)}

	tests := []struct {
		name  string
		neg   metricNegations
		exact map[string]string
		regex map[string]string
	}{
		{name: "exact =", exact: map[string]string{"span_kind": "Client"}},
		{name: "regex =~ literal", regex: map[string]string{"span_kind": "Client"}},
		{name: "regex =~ prefix", regex: map[string]string{"service_name": "test-java.*"}},
		{name: "regex =~ alternation", regex: map[string]string{"service_name": "a|b"}},
		{name: "negated !=", neg: metricNegations{Not: map[string]string{"span_kind": "Client"}}},
		{name: "negated !~", neg: metricNegations{NotMatch: map[string]string{"service_name": "test-java.*"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := r.buildMetricFilter("m", "", tc.exact, tc.regex, tr, tc.neg)
			raw, err := json.Marshal(res.Query)
			assert.NoError(t, err)
			q := string(raw)

			assert.Contains(t, q, "labels.", "expected a labels.* clause")
			// Every labels.* reference must carry the .keyword sub-field.
			for _, frag := range strings.Split(q, `"labels.`)[1:] {
				field := frag[:strings.IndexAny(frag, `"`)]
				assert.True(t, strings.HasSuffix(field, ".keyword"),
					"labels.%s must use the .keyword sub-field", field)
			}
		})
	}
}

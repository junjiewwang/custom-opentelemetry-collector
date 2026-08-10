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
//
// Exception: service_name is NOT a metric label — it is promoted to the
// top-level serviceName field (resource-derived, jvm/runtime metrics have empty
// labels). service_name matchers must target serviceName (a keyword, no .keyword
// sub-field), not labels.service_name.keyword.
func TestBuildMetricFilter_AllMatchersUseKeyword(t *testing.T) {
	r := &MetricReader{logger: zap.NewNop()}
	tr := TimeRange{Start: time.Unix(1000, 0), End: time.Unix(2000, 0)}

	tests := []struct {
		name       string
		neg        metricNegations
		exact      map[string]string
		regex      map[string]string
		wantField  string // the ES field the matcher must target
		wantLabels bool   // true → expect a labels.* clause; false → expect serviceName
	}{
		{name: "exact =", exact: map[string]string{"span_kind": "Client"}, wantField: "labels.span_kind.keyword", wantLabels: true},
		{name: "regex =~ literal", regex: map[string]string{"span_kind": "Client"}, wantField: "labels.span_kind.keyword", wantLabels: true},
		{name: "service_name = is promoted to serviceName", exact: map[string]string{"service_name": "test-java-stock-service"}, wantField: "serviceName", wantLabels: false},
		{name: "service_name =~ is promoted to serviceName", regex: map[string]string{"service_name": "test-java.*"}, wantField: "serviceName", wantLabels: false},
		{name: "negated !=", neg: metricNegations{Not: map[string]string{"span_kind": "Client"}}, wantField: "labels.span_kind.keyword", wantLabels: true},
		{name: "negated !~", neg: metricNegations{NotMatch: map[string]string{"service_name": "test-java.*"}}, wantField: "serviceName", wantLabels: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := r.buildMetricFilter("m", "", tc.exact, tc.regex, tr, tc.neg)
			raw, err := json.Marshal(res.Query)
			assert.NoError(t, err)
			q := string(raw)

			assert.Contains(t, q, tc.wantField, "expected matcher to target %s", tc.wantField)
			if !tc.wantLabels {
				// service_name promotion: must NOT touch labels.service_name.
				assert.NotContains(t, q, "labels.service_name", "service_name must be promoted, not filtered as a label")
			}
			// Every labels.* reference that DOES appear must carry the .keyword sub-field.
			for _, frag := range strings.Split(q, `"labels.`)[1:] {
				field := frag[:strings.IndexAny(frag, `"`)]
				assert.True(t, strings.HasSuffix(field, ".keyword"),
					"labels.%s must use the .keyword sub-field", field)
			}
		})
	}
}

package logql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_StreamSelector(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		nMatcher int
	}{
		{name: "single equal", input: `{app="order-service"}`, nMatcher: 1},
		{name: "multiple matchers", input: `{app="foo", env=~"prod|stag"}`, nMatcher: 2},
		{name: "all operators", input: `{a="v1", b!="v2", c=~"pat", d!~"neg"}`, nMatcher: 4},
		{name: "with dots in name", input: `{service.name="gateway"}`, nMatcher: 1},
		{name: "with underscore", input: `{container_name="nginx"}`, nMatcher: 1},
		{name: "escaped_quote", input: `{app="val\"ue"}`, nMatcher: 1},
		{name: "missing close brace", input: `{app="foo"`, wantErr: true},
		{name: "missing open brace", input: `app="foo"}`, wantErr: true},
		{name: "empty", input: ``, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := Parse(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, q.StreamSelector.Matchers, tt.nMatcher,
				"expected %d matchers, got %d", tt.nMatcher, len(q.StreamSelector.Matchers))
		})
	}
}

func TestParse_LabelMatchers(t *testing.T) {
	q, err := Parse(`{app="order-service", env=~"prod|stag", dc!="us-east", ns!~"test-.*"}`)
	require.NoError(t, err)

	assert.Equal(t, "app", q.StreamSelector.Matchers[0].Name)
	assert.Equal(t, MatchEqual, q.StreamSelector.Matchers[0].Type)
	assert.Equal(t, "order-service", q.StreamSelector.Matchers[0].Value)

	assert.Equal(t, "env", q.StreamSelector.Matchers[1].Name)
	assert.Equal(t, MatchRegex, q.StreamSelector.Matchers[1].Type)
	assert.Equal(t, "prod|stag", q.StreamSelector.Matchers[1].Value)

	assert.Equal(t, "dc", q.StreamSelector.Matchers[2].Name)
	assert.Equal(t, MatchNotEqual, q.StreamSelector.Matchers[2].Type)

	assert.Equal(t, "ns", q.StreamSelector.Matchers[3].Name)
	assert.Equal(t, MatchNotRegex, q.StreamSelector.Matchers[3].Type)
}

func TestParse_LineFilters(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		nFilter    int
		firstType  FilterType
		firstPat   string
	}{
		{
			name: "contains", input: `{app="foo"} |= "error"`,
			nFilter: 1, firstType: FilterContains, firstPat: "error",
		},
		{
			name: "not contains", input: `{app="foo"} != "error"`,
			nFilter: 1, firstType: FilterNotContains, firstPat: "error",
		},
		{
			name: "regex", input: `{app="foo"} |~ "timeout|failed"`,
			nFilter: 1, firstType: FilterRegex, firstPat: "timeout|failed",
		},
		{
			name: "not regex", input: `{app="foo"} !~ "timeout|failed"`,
			nFilter: 1, firstType: FilterNotRegex, firstPat: "timeout|failed",
		},
		{
			name: "multiple filters", input: `{app="foo"} |= "error" != "debug"`,
			nFilter: 2, firstType: FilterContains, firstPat: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := Parse(tt.input)
			require.NoError(t, err)
			assert.Len(t, q.LineFilters, tt.nFilter)
			if tt.nFilter > 0 && tt.firstPat != "" {
				assert.Equal(t, tt.firstType, q.LineFilters[0].Type)
				assert.Equal(t, tt.firstPat, q.LineFilters[0].Pattern)
			}
		})
	}
}

func TestEvaluate_StreamSelector(t *testing.T) {
	q, _ := Parse(`{app="order-service", env=~"prod|stag"}`)
	e := &Evaluator{}
	lq := e.Evaluate(q)

	assert.Equal(t, "order-service", lq.Labels["app"])
	assert.Equal(t, "prod|stag", lq.LabelMatch["env"])
}

func TestParse_Pipeline(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		nStage int
		firstType PipelineType
	}{
		{"json parser", `{app="foo"} | json`, 1, PipelineParser},
		{"logfmt parser", `{app="foo"} | logfmt`, 1, PipelineParser},
		{"unpack parser", `{app="foo"} | unpack`, 1, PipelineParser},
		// | json | level = "error" → single combined stage (parser + label filter)
		{"json + label filter", `{app="foo"} | json | level = "error"`, 1, PipelineParser},
		{"json + not-equal filter", `{app="foo"} | json | level != "warn"`, 1, PipelineParser},
		{"line format", `{app="foo"} | line_format "{{.level}}: {{.msg}}"`, 1, PipelineLineFormat},
		{"json + line_format", `{app="foo"} | json | line_format "{{.level}}"`, 2, PipelineParser},
		// | json | level = "error" → combined + | line_format → 2 total
		{"json + label_filter + line_format", `{app="foo"} | json | level = "error" | line_format "{{.msg}}"`, 2, PipelineParser},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := Parse(tt.input)
			require.NoError(t, err)
			assert.Len(t, q.Pipeline, tt.nStage)
			if tt.nStage > 0 {
				assert.Equal(t, tt.firstType, q.Pipeline[0].Type)
			}
		})
	}
}

func TestParse_PipelineWithLabelFilter(t *testing.T) {
	q, err := Parse(`{app="foo"} | json | level = "error"`)
	require.NoError(t, err)
	assert.Len(t, q.Pipeline, 1, "json + label filter is combined into 1 stage")
	stage := q.Pipeline[0]
	assert.Equal(t, PipelineParser, stage.Type)
	assert.Equal(t, "json", stage.Parser)
	assert.NotNil(t, stage.LabelFilter)
	assert.Equal(t, "level", stage.LabelFilter.Name)
	assert.Equal(t, "error", stage.LabelFilter.Value)
}

func TestApplyPipeline_JSON(t *testing.T) {
	stages := []PipelineStage{
		{Type: PipelineParser, Parser: "json"},
	}
	raw := [][]string{{"1784707266594000000", `{"level":"error","msg":"connection timeout"}`}}
	result := ApplyPipeline(raw, nil, stages)

	require.Len(t, result, 1)
	assert.Equal(t, "error", result[0].Labels["level"])
	assert.Equal(t, "connection timeout", result[0].Labels["msg"])
}

func TestApplyPipeline_JSONWithLabelFilter(t *testing.T) {
	stages := []PipelineStage{
		{Type: PipelineParser, Parser: "json"},
		{Type: PipelineLabelFilter, LabelFilter: &LabelMatcher{Name: "level", Type: MatchEqual, Value: "error"}},
	}
	raw := [][]string{
		{"1784707266594000000", `{"level":"error","msg":"timeout"}`},
		{"1784707266595000000", `{"level":"warn","msg":"slow"}`},
	}
	result := ApplyPipeline(raw, nil, stages)

	assert.Len(t, result, 1, "warn log should be filtered out")
	assert.Equal(t, "error", result[0].Labels["level"])
}

func TestApplyPipeline_LineFormat(t *testing.T) {
	stages := []PipelineStage{
		{Type: PipelineParser, Parser: "json"},
		{Type: PipelineLineFormat, LineFormat: "{{.level}}: {{.msg}}"},
	}
	raw := [][]string{{"1784707266594000000", `{"level":"error","msg":"timeout"}`}}
	result := ApplyPipeline(raw, nil, stages)

	require.Len(t, result, 1)
	assert.Equal(t, "error: timeout", result[0].Line)
}

func TestParseLogfmt(t *testing.T) {
	result := parseLogfmt(`level=error msg="connection timeout" duration=500ms`)
	assert.Equal(t, "error", result["level"])
	assert.Equal(t, "connection timeout", result["msg"])
	assert.Equal(t, "500ms", result["duration"])
}

func TestEvaluate_LineFilters(t *testing.T) {
	q, _ := Parse(`{app="foo"} |= "error" != "debug"`)
	e := &Evaluator{}
	lq := e.Evaluate(q)

	assert.Contains(t, lq.Query, `"error"`)
	assert.Contains(t, lq.Query, `-"debug"`)
}

// ═══════════════════════════════════════════════════
// Metric Query Parser Tests
// ═══════════════════════════════════════════════════

func TestIsMetricQuery(t *testing.T) {
	assert.True(t, IsMetricQuery(`sum by (level) (count_over_time({}[5m]))`))
	assert.True(t, IsMetricQuery(`count_over_time({app="foo"}[5m])`))
	assert.True(t, IsMetricQuery(`rate({}[1m])`))
	assert.False(t, IsMetricQuery(`{app="foo"} |= "error"`))
	assert.False(t, IsMetricQuery(`{app="foo"}`))
	assert.False(t, IsMetricQuery(``))
}

func TestParseMetric_CountOverTime(t *testing.T) {
	expr, err := ParseMetric(`count_over_time({}[5m])`)
	require.NoError(t, err)
	assert.Equal(t, "count_over_time", expr.Function)
	assert.Equal(t, "", expr.Aggregation)
	assert.Empty(t, expr.By)
	assert.Equal(t, 5*time.Minute, expr.RangeDuration)
	assert.Empty(t, expr.Inner.LineFilters)
}

func TestParseMetric_SumByCountOverTime(t *testing.T) {
	expr, err := ParseMetric(`sum by (level, service_name) (count_over_time({}[10m]))`)
	require.NoError(t, err)
	assert.Equal(t, "sum", expr.Aggregation)
	assert.Equal(t, "count_over_time", expr.Function)
	assert.Equal(t, []string{"level", "service_name"}, expr.By)
	assert.Equal(t, 10*time.Minute, expr.RangeDuration)
}

func TestParseMetric_WithLineFilter(t *testing.T) {
	expr, err := ParseMetric(`sum (count_over_time({} |= "error"[5m]))`)
	require.NoError(t, err)
	assert.Equal(t, "sum", expr.Aggregation)
	assert.Equal(t, "count_over_time", expr.Function)
	assert.Len(t, expr.Inner.LineFilters, 1)
	assert.Equal(t, FilterContains, expr.Inner.LineFilters[0].Type)
	assert.Equal(t, "error", expr.Inner.LineFilters[0].Pattern)
	assert.Equal(t, 5*time.Minute, expr.RangeDuration)
}

func TestParseMetric_Rate(t *testing.T) {
	expr, err := ParseMetric(`avg by (service_name) (rate({app="foo"}[1m]))`)
	require.NoError(t, err)
	assert.Equal(t, "avg", expr.Aggregation)
	assert.Equal(t, "rate", expr.Function)
	assert.Equal(t, []string{"service_name"}, expr.By)
	assert.Equal(t, 1*time.Minute, expr.RangeDuration)
	assert.Equal(t, "foo", expr.Inner.StreamSelector.Matchers[0].Value)
}

func TestParseMetric_Errors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "unclosed aggregation", input: `sum by (level) (count_over_time({}[5m])`},
		{name: "missing function", input: `sum by (level) ({})`},
		{name: "bad duration", input: `count_over_time({}[abc])`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMetric(tt.input)
			assert.Error(t, err)
		})
	}
}

// ═══════════════════════════════════════════════════
// Evaluator: Empty Pattern Handling
// ═══════════════════════════════════════════════════

func TestEvaluate_EmptyPatternFilter(t *testing.T) {
	// |= "" means "match everything" — should produce no content filter.
	q, err := Parse(`{} |= ""`)
	require.NoError(t, err)

	e := &Evaluator{}
	lq := e.Evaluate(q)

	// Query should be empty (no content filter added for empty pattern).
	assert.Equal(t, "", lq.Query, "empty pattern should skip content filter")
}

func TestEvaluate_MixedEmptyAndNonEmptyFilters(t *testing.T) {
	// |= "" combined with |= "error" — empty filter skipped, non-empty kept.
	q, err := Parse(`{} |= "" |= "error"`)
	require.NoError(t, err)

	e := &Evaluator{}
	lq := e.Evaluate(q)

	assert.Contains(t, lq.Query, `"error"`)
	// The empty filter should not contribute to the query string.
}

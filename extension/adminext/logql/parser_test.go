package logql

import (
	"testing"

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

func TestEvaluate_LineFilters(t *testing.T) {
	q, _ := Parse(`{app="foo"} |= "error" != "debug"`)
	e := &Evaluator{}
	lq := e.Evaluate(q)

	assert.Contains(t, lq.Query, `"error"`)
	assert.Contains(t, lq.Query, `-"debug"`)
}

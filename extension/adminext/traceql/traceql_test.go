// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package traceql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════
// Lexer Tests
// ═══════════════════════════════════════════════════

func TestLexerBasic(t *testing.T) {
	input := `{ resource.service.name = "my-svc" && kind = server }`
	lexer := NewLexer(input)
	tokens, err := lexer.Tokenize()
	require.NoError(t, err)

	// Expected: { resource.service.name = "my-svc" && kind = server }
	expected := []TokenType{
		TokenLBrace,  // {
		TokenIdent,   // resource.service.name
		TokenEq,      // =
		TokenString,  // "my-svc"
		TokenAnd,     // &&
		TokenIdent,   // kind
		TokenEq,      // =
		TokenIdent,   // server
		TokenRBrace,  // }
		TokenEOF,
	}

	require.Len(t, tokens, len(expected))
	for i, exp := range expected {
		assert.Equal(t, exp, tokens[i].Type, "token %d: got %q", i, tokens[i].Literal)
	}
}

func TestLexerStructural(t *testing.T) {
	input := `{ .a = "1" } &>> { kind = server }`
	lexer := NewLexer(input)
	tokens, err := lexer.Tokenize()
	require.NoError(t, err)

	types := make([]TokenType, len(tokens))
	for i, tok := range tokens {
		types[i] = tok.Type
	}

	assert.Contains(t, types, TokenAncestor)
}

func TestLexerPipeline(t *testing.T) {
	input := `{ status = error } | select(name, resource.service.name)`
	lexer := NewLexer(input)
	tokens, err := lexer.Tokenize()
	require.NoError(t, err)

	types := make([]TokenType, len(tokens))
	for i, tok := range tokens {
		types[i] = tok.Type
	}

	assert.Contains(t, types, TokenPipe)
	assert.Contains(t, types, TokenSelect)
}

func TestLexerDuration(t *testing.T) {
	input := `{ duration > 100ms }`
	lexer := NewLexer(input)
	tokens, err := lexer.Tokenize()
	require.NoError(t, err)

	// Find the number token.
	var numTok Token
	for _, tok := range tokens {
		if tok.Type == TokenNumber {
			numTok = tok
			break
		}
	}
	assert.Equal(t, "100ms", numTok.Literal)
}

func TestLexerNegativeNumber(t *testing.T) {
	input := `{ nestedSetParent < -1 }`
	lexer := NewLexer(input)
	tokens, err := lexer.Tokenize()
	require.NoError(t, err)

	var numTok Token
	for _, tok := range tokens {
		if tok.Type == TokenNumber {
			numTok = tok
			break
		}
	}
	assert.Equal(t, "-1", numTok.Literal)
}

// ═══════════════════════════════════════════════════
// Parser Tests
// ═══════════════════════════════════════════════════

func TestParseSimpleFilter(t *testing.T) {
	ast, err := Parse(`{ resource.service.name = "my-svc" && status = error }`)
	require.NoError(t, err)
	require.NotNil(t, ast)

	sf, ok := ast.(*SpanFilter)
	require.True(t, ok, "expected SpanFilter, got %T", ast)
	assert.Len(t, sf.Conditions, 2)

	// First condition: resource.service.name = "my-svc"
	assert.Equal(t, "resource", sf.Conditions[0].Scope)
	assert.Equal(t, "service.name", sf.Conditions[0].Key)
	assert.Equal(t, "=", sf.Conditions[0].Operator)
	assert.Equal(t, "my-svc", sf.Conditions[0].Value)

	// Second condition: status = error
	assert.Equal(t, "", sf.Conditions[1].Scope)
	assert.Equal(t, "status", sf.Conditions[1].Key)
	assert.Equal(t, "=", sf.Conditions[1].Operator)
	assert.Equal(t, "error", sf.Conditions[1].Value)
}

func TestParseStructural(t *testing.T) {
	input := `{ nestedSetParent < 0 && true } &>> { kind = server }`
	ast, err := Parse(input)
	require.NoError(t, err)
	require.NotNil(t, ast)

	structural, ok := ast.(*StructuralExpr)
	require.True(t, ok, "expected StructuralExpr, got %T", ast)
	assert.Equal(t, "&>>", structural.Operator)

	// Left side: { nestedSetParent < 0 } (true is skipped)
	leftFilter, ok := structural.Left.(*SpanFilter)
	require.True(t, ok)
	assert.Len(t, leftFilter.Conditions, 1)
	assert.Equal(t, "nestedSetParent", leftFilter.Conditions[0].Key)
	assert.Equal(t, "<", leftFilter.Conditions[0].Operator)

	// Right side: { kind = server }
	rightFilter, ok := structural.Right.(*SpanFilter)
	require.True(t, ok)
	assert.Len(t, rightFilter.Conditions, 1)
	assert.Equal(t, "kind", rightFilter.Conditions[0].Key)
}

func TestParseOrExpr(t *testing.T) {
	input := `{ kind = client } || { kind = server }`
	ast, err := Parse(input)
	require.NoError(t, err)
	require.NotNil(t, ast)

	or, ok := ast.(*OrExpr)
	require.True(t, ok, "expected OrExpr, got %T", ast)

	leftFilter, ok := or.Left.(*SpanFilter)
	require.True(t, ok)
	assert.Equal(t, "client", leftFilter.Conditions[0].Value)

	rightFilter, ok := or.Right.(*SpanFilter)
	require.True(t, ok)
	assert.Equal(t, "server", rightFilter.Conditions[0].Value)
}

func TestParsePipeline(t *testing.T) {
	input := `{ status = error } | select(name, resource.service.name)`
	ast, err := Parse(input)
	require.NoError(t, err)
	require.NotNil(t, ast)

	pipeline, ok := ast.(*PipelineExpr)
	require.True(t, ok, "expected PipelineExpr, got %T", ast)

	// Input is a span filter.
	_, ok = pipeline.Input.(*SpanFilter)
	require.True(t, ok)

	// One stage: select.
	require.Len(t, pipeline.Stages, 1)
	sel, ok := pipeline.Stages[0].(*SelectStage)
	require.True(t, ok)
	assert.Equal(t, []string{"name", "resource.service.name"}, sel.Fields)
}

func TestParseGrafanaFullQuery(t *testing.T) {
	// The actual query Grafana sends.
	input := `({nestedSetParent<0 && true} &>> {kind = server}) || ({nestedSetParent<0 && true}) | select(status, resource.service.name, name, nestedSetParent, nestedSetLeft, nestedSetRight)`
	ast, err := Parse(input)
	require.NoError(t, err)
	require.NotNil(t, ast)

	// Top level should be a pipeline (because of | select).
	pipeline, ok := ast.(*PipelineExpr)
	require.True(t, ok, "expected PipelineExpr, got %T", ast)

	// Input should be an OrExpr.
	or, ok := pipeline.Input.(*OrExpr)
	require.True(t, ok, "expected OrExpr as pipeline input, got %T", pipeline.Input)

	// Left side of OR: structural expr.
	structural, ok := or.Left.(*StructuralExpr)
	require.True(t, ok, "expected StructuralExpr, got %T", or.Left)
	assert.Equal(t, "&>>", structural.Operator)

	// Right side of OR: simple span filter (root span).
	rightFilter, ok := or.Right.(*SpanFilter)
	require.True(t, ok, "expected SpanFilter, got %T", or.Right)
	assert.Len(t, rightFilter.Conditions, 1) // nestedSetParent < 0 (true skipped)

	// Select stage.
	require.Len(t, pipeline.Stages, 1)
	sel, ok := pipeline.Stages[0].(*SelectStage)
	require.True(t, ok)
	assert.Contains(t, sel.Fields, "status")
	assert.Contains(t, sel.Fields, "resource.service.name")
	assert.Contains(t, sel.Fields, "name")
}

func TestParseDuration(t *testing.T) {
	input := `{ duration > 100ms && duration < 5s }`
	ast, err := Parse(input)
	require.NoError(t, err)

	sf, ok := ast.(*SpanFilter)
	require.True(t, ok)
	assert.Len(t, sf.Conditions, 2)

	assert.Equal(t, "duration", sf.Conditions[0].Key)
	assert.Equal(t, ">", sf.Conditions[0].Operator)
	assert.Equal(t, 100*time.Millisecond, sf.Conditions[0].Value)

	assert.Equal(t, "duration", sf.Conditions[1].Key)
	assert.Equal(t, "<", sf.Conditions[1].Operator)
	assert.Equal(t, 5*time.Second, sf.Conditions[1].Value)
}

func TestParseEmptyQuery(t *testing.T) {
	ast, err := Parse("{}")
	require.NoError(t, err)
	assert.Nil(t, ast)

	ast, err = Parse("")
	require.NoError(t, err)
	assert.Nil(t, ast)
}

// ═══════════════════════════════════════════════════
// Planner Tests
// ═══════════════════════════════════════════════════

func TestPlanSimpleFilter(t *testing.T) {
	ast, err := Parse(`{ resource.service.name = "my-svc" && .http.method = "GET" }`)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.Equal(t, "my-svc", plan.ServiceName)
	assert.Equal(t, "GET", plan.Tags["http.method"])
	assert.False(t, plan.HasStructural)
}

func TestPlanIntrinsics(t *testing.T) {
	ast, err := Parse(`{ kind = server && status = error && name = "GET /api" }`)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.Equal(t, "server", plan.SpanKind)
	assert.Equal(t, "error", plan.Status)
	assert.Equal(t, "GET /api", plan.OperationName)
}

func TestPlanRootSpan(t *testing.T) {
	ast, err := Parse(`{ nestedSetParent < 0 }`)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.True(t, plan.IsRoot)
}

func TestPlanDuration(t *testing.T) {
	ast, err := Parse(`{ duration > 100ms && duration < 5s }`)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.Equal(t, 100*time.Millisecond, plan.MinDuration)
	assert.Equal(t, 5*time.Second, plan.MaxDuration)
}

func TestPlanStructural(t *testing.T) {
	ast, err := Parse(`{ nestedSetParent < 0 } &>> { kind = server }`)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.True(t, plan.HasStructural)
	assert.True(t, plan.IsRoot)
	// SpanKind is relaxed for structural queries — ES search is only for broad
	// candidate fetching; exact structural matching happens in post-processing.
	assert.Empty(t, plan.SpanKind, "structural queries should relax SpanKind to avoid over-filtering ES candidates")
}

func TestPlanOrGroups(t *testing.T) {
	ast, err := Parse(`{ kind = client } || { kind = server }`)
	require.NoError(t, err)

	plan := Plan(ast)
	// TagsOr is now [][]map[string]string: 1 outer group with 2 branches.
	require.Len(t, plan.TagsOr, 1)
	require.Len(t, plan.TagsOr[0], 2)
	assert.Equal(t, map[string]string{"kind": "client"}, plan.TagsOr[0][0])
	assert.Equal(t, map[string]string{"kind": "server"}, plan.TagsOr[0][1])
}

func TestPlanGrafanaQuery(t *testing.T) {
	input := `({nestedSetParent<0 && true} &>> {kind = server}) || ({nestedSetParent<0 && true}) | select(status, resource.service.name, name)`
	ast, err := Parse(input)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.True(t, plan.HasStructural)
	assert.True(t, plan.IsRoot)
	// SpanKind is relaxed for structural queries to avoid over-filtering ES candidates.
	assert.Empty(t, plan.SpanKind, "structural queries should relax SpanKind")
	assert.Contains(t, plan.SelectFields, "status")
	assert.Contains(t, plan.SelectFields, "resource.service.name")
	assert.Contains(t, plan.SelectFields, "name")
}

func TestPlanGrafanaQueryWithNestedSetSelect(t *testing.T) {
	// This is the exact query Grafana sends for Service Structure view.
	input := `({nestedSetParent<0 && true } &>> { kind = server }) || ({nestedSetParent<0 && true }) | select(status, resource.service.name, name, nestedSetParent, nestedSetLeft, nestedSetRight)`
	ast, err := Parse(input)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.True(t, plan.HasStructural, "should detect structural operator")
	assert.True(t, plan.IsRoot, "should detect root span filter")
	assert.Empty(t, plan.SpanKind, "structural queries must NOT push SpanKind to ES")
	assert.Empty(t, plan.Status, "structural queries must NOT push Status to ES")
	// Select fields should be preserved.
	assert.Contains(t, plan.SelectFields, "nestedSetParent")
	assert.Contains(t, plan.SelectFields, "nestedSetLeft")
	assert.Contains(t, plan.SelectFields, "nestedSetRight")
	assert.Contains(t, plan.SelectFields, "resource.service.name")
	assert.Contains(t, plan.SelectFields, "name")
	assert.Contains(t, plan.SelectFields, "status")
}

func TestPlanStructuralRelaxDoesNotAffectNonStructural(t *testing.T) {
	// Non-structural queries should NOT have conditions relaxed.
	ast, err := Parse(`{ kind = server && resource.service.name = "my-svc" }`)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.False(t, plan.HasStructural)
	assert.Equal(t, "server", plan.SpanKind, "non-structural query should preserve SpanKind")
	assert.Equal(t, "my-svc", plan.ServiceName, "non-structural query should preserve ServiceName")
}

// ═══════════════════════════════════════════════════
// IsAdvancedQuery Tests
// ═══════════════════════════════════════════════════

func TestIsAdvancedQuery(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`{}`, false},
		{`{ .http.method = "GET" }`, false},
		{`{ resource.service.name = "svc" && .http.method = "GET" }`, false},
		{`{ nestedSetParent < 0 } &>> { kind = server }`, true},
		{`{ status = error } | select(name)`, true},
		{`{ kind = server } >> { .db.type = "redis" }`, true},
		// Quoted structural operator should NOT trigger.
		{`{ .method = "&>>" }`, false},
		// Parenthesized OR inside single span filter.
		{`{(kind="internal" || kind="server") && resource.service.name="tapm-api"}`, true},
		{`{(status="error" || status="unset")}`, true},
		// Quoted || should NOT trigger.
		{`{ .attr = "a||b" }`, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, IsAdvancedQuery(tt.input))
		})
	}
}

// ═══════════════════════════════════════════════════
// Parenthesized OR Group Tests
// ═══════════════════════════════════════════════════

func TestParseParenthesizedOrGroup(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantCond    []Condition
		wantOrGroup [][]Condition
	}{
		{
			name:  "simple OR group with AND condition",
			input: `{(kind="internal" || kind="server") && resource.service.name="tapm-api"}`,
			wantCond: []Condition{
				{Scope: "resource", Key: "service.name", Operator: "=", Value: "tapm-api"},
			},
			wantOrGroup: [][]Condition{
				{{Key: "kind", Operator: "=", Value: "internal"}},
				{{Key: "kind", Operator: "=", Value: "server"}},
			},
		},
		{
			name:  "OR group with string values",
			input: `{(name="/api/v1" || name="/api/v2") && resource.service.name="my-svc"}`,
			wantCond: []Condition{
				{Scope: "resource", Key: "service.name", Operator: "=", Value: "my-svc"},
			},
			wantOrGroup: [][]Condition{
				{{Key: "name", Operator: "=", Value: "/api/v1"}},
				{{Key: "name", Operator: "=", Value: "/api/v2"}},
			},
		},
		{
			name:  "OR group only, no AND conditions",
			input: `{(status="error" || status="unset")}`,
			wantCond: nil,
			wantOrGroup: [][]Condition{
				{{Key: "status", Operator: "=", Value: "error"}},
				{{Key: "status", Operator: "=", Value: "unset"}},
			},
		},
		{
			name:  "OR group at beginning then AND condition",
			input: `{(kind="client" || kind="server") && span.http.method="GET"}`,
			wantCond: []Condition{
				{Scope: "span", Key: "http.method", Operator: "=", Value: "GET"},
			},
			wantOrGroup: [][]Condition{
				{{Key: "kind", Operator: "=", Value: "client"}},
				{{Key: "kind", Operator: "=", Value: "server"}},
			},
		},
		{
			name:  "multiple OR groups with AND condition",
			input: `{(kind="server" || kind="client") && (status="error" || status="ok") && resource.service.name="svc"}`,
			wantCond: []Condition{
				{Scope: "resource", Key: "service.name", Operator: "=", Value: "svc"},
			},
			wantOrGroup: [][]Condition{
				{{Key: "kind", Operator: "=", Value: "server"}},
				{{Key: "kind", Operator: "=", Value: "client"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast, err := Parse(tt.input)
			require.NoError(t, err)
			require.NotNil(t, ast)

			sf, ok := ast.(*SpanFilter)
			require.True(t, ok, "expected SpanFilter, got %T", ast)

			// Check AND conditions.
			if tt.wantCond == nil {
				assert.Empty(t, sf.Conditions)
			} else {
				require.Len(t, sf.Conditions, len(tt.wantCond))
				for i, want := range tt.wantCond {
					assert.Equal(t, want.Scope, sf.Conditions[i].Scope, "cond[%d] scope", i)
					assert.Equal(t, want.Key, sf.Conditions[i].Key, "cond[%d] key", i)
					assert.Equal(t, want.Operator, sf.Conditions[i].Operator, "cond[%d] operator", i)
					assert.Equal(t, want.Value, sf.Conditions[i].Value, "cond[%d] value", i)
				}
			}

			// Check OR groups (first group only for single-group cases).
			require.GreaterOrEqual(t, len(sf.OrGroups), 1, "expected at least 1 OrGroup")
			firstGroup := sf.OrGroups[0]
			require.Len(t, firstGroup, len(tt.wantOrGroup))
			for i, wantBranch := range tt.wantOrGroup {
				require.Len(t, firstGroup[i], len(wantBranch))
				for j, wantCond := range wantBranch {
					assert.Equal(t, wantCond.Key, firstGroup[i][j].Key, "orGroup[0][%d][%d] key", i, j)
					assert.Equal(t, wantCond.Value, firstGroup[i][j].Value, "orGroup[0][%d][%d] value", i, j)
				}
			}
		})
	}
}

// TestParseMultipleOrGroups verifies that multiple parenthesized OR groups
// within a single span filter are correctly parsed as separate OrGroup entries.
func TestParseMultipleOrGroups(t *testing.T) {
	input := `{(kind="server" || kind="client") && (status="error" || status="ok") && resource.service.name="svc"}`

	ast, err := Parse(input)
	require.NoError(t, err)

	sf, ok := ast.(*SpanFilter)
	require.True(t, ok)

	// 2 independent OR groups.
	require.Len(t, sf.OrGroups, 2)

	// Group 0: kind server || client
	group0 := sf.OrGroups[0]
	require.Len(t, group0, 2)
	assert.Len(t, group0[0], 1)
	assert.Equal(t, "kind", group0[0][0].Key)
	assert.Equal(t, "server", group0[0][0].Value)
	assert.Len(t, group0[1], 1)
	assert.Equal(t, "kind", group0[1][0].Key)
	assert.Equal(t, "client", group0[1][0].Value)

	// Group 1: status error || ok
	group1 := sf.OrGroups[1]
	require.Len(t, group1, 2)
	assert.Len(t, group1[0], 1)
	assert.Equal(t, "status", group1[0][0].Key)
	assert.Equal(t, "error", group1[0][0].Value)
	assert.Len(t, group1[1], 1)
	assert.Equal(t, "status", group1[1][0].Key)
	assert.Equal(t, "ok", group1[1][0].Value)

	// AND condition: resource.service.name = "svc"
	require.Len(t, sf.Conditions, 1)
	assert.Equal(t, "resource", sf.Conditions[0].Scope)
	assert.Equal(t, "service.name", sf.Conditions[0].Key)
	assert.Equal(t, "svc", sf.Conditions[0].Value)
}

// TestPlanMultipleOrGroups verifies that multiple OR groups produce
// separate TagsOr groups in the execution plan.
func TestPlanMultipleOrGroups(t *testing.T) {
	ast, err := Parse(`{(kind="server" || kind="client") && (status="error" || status="ok") && resource.service.name="svc"}`)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.Equal(t, "svc", plan.ServiceName)

	// 2 outer OR groups.
	require.Len(t, plan.TagsOr, 2)

	// Group 0: kind branches
	require.Len(t, plan.TagsOr[0], 2)
	assert.Equal(t, map[string]string{"kind": "server"}, plan.TagsOr[0][0])
	assert.Equal(t, map[string]string{"kind": "client"}, plan.TagsOr[0][1])

	// Group 1: status branches
	require.Len(t, plan.TagsOr[1], 2)
	assert.Equal(t, map[string]string{"status": "error"}, plan.TagsOr[1][0])
	assert.Equal(t, map[string]string{"status": "ok"}, plan.TagsOr[1][1])
}

// TestParseOrGroupWithAttributes verifies OR groups that mix attributes
// and intrinsic fields.
func TestParseOrGroupWithAttributes(t *testing.T) {
	input := `{(span.http.method="GET" || span.http.method="POST") && resource.service.name="my-svc"}`

	ast, err := Parse(input)
	require.NoError(t, err)

	sf, ok := ast.(*SpanFilter)
	require.True(t, ok)

	// AND condition.
	require.Len(t, sf.Conditions, 1)
	assert.Equal(t, "service.name", sf.Conditions[0].Key)
	assert.Equal(t, "my-svc", sf.Conditions[0].Value)

	// OR group.
	require.Len(t, sf.OrGroups, 1)
	group := sf.OrGroups[0]
	require.Len(t, group, 2)
	assert.Equal(t, "http.method", group[0][0].Key)
	assert.Equal(t, "GET", group[0][0].Value)
	assert.Equal(t, "http.method", group[1][0].Key)
	assert.Equal(t, "POST", group[1][0].Value)
}

// TestStringRoundTrip verifies that SpanFilter.String() produces valid
// output that can be re-parsed.
func TestStringRoundTrip(t *testing.T) {
	tests := []string{
		`{ resource.service.name = "tapm-api" && (kind = internal || kind = server) }`,
		`{ (status = error || status = unset) }`,
		`{ resource.service.name = "svc" && (status = error || status = ok) && (kind = server || kind = client) }`,
	}

	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			ast, err := Parse(input)
			require.NoError(t, err)

			str := ast.String()
			// Re-parse the string output.
			reparsed, err := Parse(str)
			require.NoError(t, err, "failed to re-parse String() output: %s", str)

			// Both should be SpanFilters with same conditions.
			sf1, ok1 := ast.(*SpanFilter)
			sf2, ok2 := reparsed.(*SpanFilter)
			require.True(t, ok1)
			require.True(t, ok2)

			assert.Equal(t, len(sf1.Conditions), len(sf2.Conditions), "conditions count mismatch")
			assert.Equal(t, len(sf1.OrGroups), len(sf2.OrGroups), "orGroups count mismatch")
		})
	}
}

func TestPlanParenthesizedOrGroup(t *testing.T) {
	tests := []struct {
		name              string
		input             string
		wantServiceName   string
		wantTags          map[string]string
		wantOrGroupCount  int             // number of outer OR groups
		wantFirstBranchCount int          // branches in first OR group
		wantFirstBranch0  map[string]string
		wantFirstBranch1  map[string]string
	}{
		{
			name:              "kind OR with service.name AND",
			input:             `{(kind="internal" || kind="server") && resource.service.name="tapm-api"}`,
			wantServiceName:   "tapm-api",
			wantTags:          map[string]string{},
			wantOrGroupCount:  1,
			wantFirstBranchCount: 2,
			wantFirstBranch0:  map[string]string{"kind": "internal"},
			wantFirstBranch1:  map[string]string{"kind": "server"},
		},
		{
			name:              "status OR only",
			input:             `{(status="error" || status="unset")}`,
			wantServiceName:   "",
			wantTags:          map[string]string{},
			wantOrGroupCount:  1,
			wantFirstBranchCount: 2,
			wantFirstBranch0:  map[string]string{"status": "error"},
			wantFirstBranch1:  map[string]string{"status": "unset"},
		},
		{
			name:              "OR group with regular attribute AND condition",
			input:             `{(kind="client" || kind="server") && span.http.method="GET"}`,
			wantServiceName:   "",
			wantTags:          map[string]string{"http.method": "GET"},
			wantOrGroupCount:  1,
			wantFirstBranchCount: 2,
			wantFirstBranch0:  map[string]string{"kind": "client"},
			wantFirstBranch1:  map[string]string{"kind": "server"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ast, err := Parse(tt.input)
			require.NoError(t, err)

			plan := Plan(ast)
			assert.Equal(t, tt.wantServiceName, plan.ServiceName)
			assert.Equal(t, tt.wantTags, plan.Tags)

			require.Len(t, plan.TagsOr, tt.wantOrGroupCount, "TagsOr outer group count")
			if tt.wantOrGroupCount >= 1 {
				firstGroup := plan.TagsOr[0]
				require.Len(t, firstGroup, tt.wantFirstBranchCount, "first group branch count")
				if tt.wantFirstBranchCount >= 1 {
					assert.Equal(t, tt.wantFirstBranch0, firstGroup[0])
				}
				if tt.wantFirstBranchCount >= 2 {
					assert.Equal(t, tt.wantFirstBranch1, firstGroup[1])
				}
			}
		})
	}
}

func TestParseParenthesizedOrGroupErrorCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "unclosed parenthesis",
			input: `{(kind="internal" || kind="server"}`,
		},
		{
			name:  "nested parentheses not supported",
			input: `{((kind="internal" || kind="server")) && service.name="svc"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.input)
			assert.Error(t, err)
		})
	}
}

// ═══════════════════════════════════════════════════
// Planner — Sprint 2: Negation / Existence / Regex
// ═══════════════════════════════════════════════════

func TestPlanNotEqual(t *testing.T) {
	ast, err := Parse(`{ .http.method != "GET" && kind != server && name != "foo" }`)
	require.NoError(t, err)

	plan := Plan(ast)

	// Generic attribute: !="GET" → TagsNot
	assert.Equal(t, "GET", plan.TagsNot["http.method"])
	assert.Equal(t, "", plan.Tags["http.method"])

	// Intrinsic kind: !=server → TagsNot
	assert.Equal(t, "server", plan.TagsNot["kind"])
	assert.Empty(t, plan.SpanKind)

	// Intrinsic name: !="foo" → TagsNot
	assert.Equal(t, "foo", plan.TagsNot["name"])
	assert.Empty(t, plan.OperationName)
}

func TestPlanNotEqualNil(t *testing.T) {
	ast, err := Parse(`{ resource.service.name != nil && .http.method != nil && kind != nil }`)
	require.NoError(t, err)

	plan := Plan(ast)

	// != nil → TagsExists
	assert.Contains(t, plan.TagsExists, "service.name")
	assert.Contains(t, plan.TagsExists, "http.method")
	assert.Contains(t, plan.TagsExists, "kind")
}

func TestPlanRegex(t *testing.T) {
	ast, err := Parse(`{ .http.method =~ "GET|POST" && name =~ "^api" }`)
	require.NoError(t, err)

	plan := Plan(ast)

	// Generic attribute: =~ → TagsRegex
	assert.Equal(t, "GET|POST", plan.TagsRegex["http.method"])
	// Intrinsic name: =~ → TagsRegex
	assert.Equal(t, "^api", plan.TagsRegex["name"])
}

func TestPlanRegexInvalid(t *testing.T) {
	// Invalid regex patterns should be silently skipped.
	ast, err := Parse(`{ .http.method =~ "[invalid" }`)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.Nil(t, plan.TagsRegex, "invalid regex should be skipped")
}

func TestPlanMixedOperators(t *testing.T) {
	ast, err := Parse(`{ resource.service.name = "mysvc" && .http.method != "OPTIONS" && kind != nil && .http.method =~ "GET|POST" }`)
	require.NoError(t, err)

	plan := Plan(ast)

	// Positive equals → ServiceName
	assert.Equal(t, "mysvc", plan.ServiceName)
	// != value → TagsNot
	assert.Equal(t, "OPTIONS", plan.TagsNot["http.method"])
	// != nil → TagsExists
	assert.Contains(t, plan.TagsExists, "kind")
	// =~ → TagsRegex
	assert.Equal(t, "GET|POST", plan.TagsRegex["http.method"])
}

// TestPlanStatusMessage verifies that status.message flows through Tags correctly (Sprint 3).
func TestPlanStatusMessage(t *testing.T) {
	ast, err := Parse(`{ status.message = "timeout" && status.message != nil }`)
	require.NoError(t, err)

	plan := Plan(ast)

	// = value → Tags
	assert.Equal(t, "timeout", plan.Tags["status.message"])
	// != nil → TagsExists
	assert.Contains(t, plan.TagsExists, "status.message")
}

// ═══════════════════════════════════════════════════
// Evaluator — SpanTree Tests
// ═══════════════════════════════════════════════════

func makeTestSpan(id, parentID, kind, name, service string) SpanData {
	return SpanData{
		SpanID:       id,
		ParentSpanID: parentID,
		Kind:         kind,
		Name:         name,
		ServiceName:  service,
		StatusCode:   "ok",
	}
}

func TestBuildSpanTree(t *testing.T) {
	spans := []SpanData{
		makeTestSpan("root", "", "server", "root", "svc"),
		makeTestSpan("child1", "root", "internal", "child1", "svc"),
		makeTestSpan("child2", "root", "client", "child2", "svc"),
		makeTestSpan("grandchild", "child1", "internal", "gc", "svc"),
	}

	tree := BuildSpanTree(spans)

	// Root check.
	assert.NotNil(t, tree.GetSpan("root"))
	assert.Empty(t, tree.GetSpan("root").ParentSpanID)

	// Children.
	assert.Len(t, tree.Children("root"), 2)
	assert.Len(t, tree.Children("child1"), 1)
	assert.Empty(t, tree.Children("grandchild"))
}

func TestSpanTreeRelationships(t *testing.T) {
	spans := []SpanData{
		makeTestSpan("root", "", "server", "root", "svc"),
		makeTestSpan("A", "root", "internal", "A", "svc"),
		makeTestSpan("B", "root", "client", "B", "svc"),
		makeTestSpan("A1", "A", "internal", "A1", "svc"),
	}

	tree := BuildSpanTree(spans)

	// Ancestor / Descendant.
	assert.True(t, tree.IsAncestor("root", "A"))
	assert.True(t, tree.IsAncestor("root", "A1"))
	assert.True(t, tree.IsDescendant("A", "A1"))
	assert.False(t, tree.IsAncestor("A", "B"))
	assert.False(t, tree.IsDescendant("root", "root")) // not own ancestor

	// Child.
	assert.True(t, tree.IsChild("root", "A"))
	assert.False(t, tree.IsChild("root", "A1")) // grandchild, not direct
	assert.False(t, tree.IsChild("A", "B"))

	// Sibling.
	assert.True(t, tree.IsSibling("A", "B"))
	assert.False(t, tree.IsSibling("A", "A1"))
}

// ═══════════════════════════════════════════════════
// Evaluator — SpanFilter Matching Tests
// ═══════════════════════════════════════════════════

func TestMatchSpanFilter_Kind(t *testing.T) {
	ast, err := Parse(`{ kind = server }`)
	require.NoError(t, err)
	sf := ast.(*SpanFilter)

	span := makeTestSpan("s1", "", "server", "test", "svc")
	assert.True(t, MatchSpanFilter(sf, &span))

	span.Kind = "client"
	assert.False(t, MatchSpanFilter(sf, &span))
}

func TestMatchSpanFilter_ServiceName(t *testing.T) {
	ast, err := Parse(`{ resource.service.name = "tapm-api" }`)
	require.NoError(t, err)
	sf := ast.(*SpanFilter)

	span := makeTestSpan("s1", "", "server", "test", "tapm-api")
	assert.True(t, MatchSpanFilter(sf, &span))

	span.ServiceName = "other-svc"
	assert.False(t, MatchSpanFilter(sf, &span))
}

func TestMatchSpanFilter_Status(t *testing.T) {
	ast, err := Parse(`{ status = error }`)
	require.NoError(t, err)
	sf := ast.(*SpanFilter)

	span := makeTestSpan("s1", "", "server", "test", "svc")
	span.StatusCode = "error"
	assert.True(t, MatchSpanFilter(sf, &span))

	span.StatusCode = "ok"
	assert.False(t, MatchSpanFilter(sf, &span))
}

func TestMatchSpanFilter_Name(t *testing.T) {
	ast, err := Parse(`{ name = "/api/v1" }`)
	require.NoError(t, err)
	sf := ast.(*SpanFilter)

	span := makeTestSpan("s1", "", "server", "/api/v1", "svc")
	assert.True(t, MatchSpanFilter(sf, &span))

	span.Name = "/api/v2"
	assert.False(t, MatchSpanFilter(sf, &span))
}

func TestMatchSpanFilter_MultipleConditions(t *testing.T) {
	ast, err := Parse(`{ kind = server && resource.service.name = "tapm-api" }`)
	require.NoError(t, err)
	sf := ast.(*SpanFilter)

	span := makeTestSpan("s1", "", "server", "test", "tapm-api")
	assert.True(t, MatchSpanFilter(sf, &span))

	span.Kind = "client"
	assert.False(t, MatchSpanFilter(sf, &span))
}

// ═══════════════════════════════════════════════════
// Evaluator — Structural Expression Tests
// ═══════════════════════════════════════════════════

func TestEvaluateStructural_Ancestor(t *testing.T) {
	// Parse {kind = server} &>> {kind = internal}
	ast, err := Parse(`{ kind = server } &>> { kind = internal }`)
	require.NoError(t, err)
	structExpr := ast.(*StructuralExpr)
	assert.Equal(t, "&>>", structExpr.Operator)

	spans := []SpanData{
		makeTestSpan("root", "", "server", "root-svc", "tapm-api"),
		makeTestSpan("internal1", "root", "internal", "inner-call", "tapm-api"),
		makeTestSpan("internal2", "root", "internal", "inner-call-2", "tapm-api"),
		makeTestSpan("leaf", "internal1", "client", "outgoing", "tapm-api"),
	}
	tree := BuildSpanTree(spans)

	matches := EvaluateStructural(structExpr, tree)

	// root (server) should match internal1 and internal2 (descendants)
	// leaf is client, doesn't match right filter
	require.Len(t, matches, 2)

	// Both matches should have root as left span.
	for _, m := range matches {
		assert.Equal(t, "root", m.LeftSpanID)
	}
	rightIDs := make(map[string]bool)
	for _, m := range matches {
		rightIDs[m.RightSpanID] = true
	}
	assert.True(t, rightIDs["internal1"])
	assert.True(t, rightIDs["internal2"])
}

func TestEvaluateStructural_Child(t *testing.T) {
	ast, err := Parse(`{ kind = server } > { kind = internal }`)
	require.NoError(t, err)
	structExpr := ast.(*StructuralExpr)
	assert.Equal(t, ">", structExpr.Operator)

	spans := []SpanData{
		makeTestSpan("root", "", "server", "root-svc", "tapm-api"),
		makeTestSpan("direct-child", "root", "internal", "inner", "tapm-api"),
		makeTestSpan("grandchild", "direct-child", "internal", "gc", "tapm-api"),
	}
	tree := BuildSpanTree(spans)

	matches := EvaluateStructural(structExpr, tree)
	// root > direct-child: yes, root > grandchild: no (not direct)
	require.Len(t, matches, 1)
	assert.Equal(t, "root", matches[0].LeftSpanID)
	assert.Equal(t, "direct-child", matches[0].RightSpanID)
}

func TestEvaluateStructural_NoMatch(t *testing.T) {
	ast, err := Parse(`{ kind = server } >> { kind = server }`)
	require.NoError(t, err)
	structExpr := ast.(*StructuralExpr)

	spans := []SpanData{
		makeTestSpan("root", "", "server", "root", "svc"),
		makeTestSpan("child", "root", "internal", "child", "svc"),
	}
	tree := BuildSpanTree(spans)

	matches := EvaluateStructural(structExpr, tree)
	// Both left and right filters match "root", but root is not its own descendant.
	assert.Empty(t, matches)
}

func TestEvaluateStructural_GrafanaQuery(t *testing.T) {
	// Full Grafana query: ({nestedSetParent<0 && true} &>> {kind = server}) || ({nestedSetParent<0 && true})
	// We only evaluate the structural part for left OR branch.
	ast, err := Parse(`{ nestedSetParent < 0 } &>> { kind = server }`)
	require.NoError(t, err)
	structExpr := ast.(*StructuralExpr)

	spans := []SpanData{
		makeTestSpan("root", "", "server", "root-span", "my-svc"),
		{SpanID: "child", ParentSpanID: "root", Kind: "server", Name: "child-svc", ServiceName: "my-svc", StatusCode: "ok"},
		{SpanID: "non-server", ParentSpanID: "root", Kind: "client", Name: "outgoing", ServiceName: "my-svc", StatusCode: "ok"},
	}
	tree := BuildSpanTree(spans)

	// The left filter is {nestedSetParent < 0}. In evaluator, nestedSetParent is not a SpanData field.
	// But we treat it as looking for root spans (ParentSpanID=""). So we just need to
	// manually set up a SpanFilter check differently. For this test, we know root has empty parent.
	matches := EvaluateStructural(structExpr, tree)
	// Root matches both kind=server and nestedSetParent<0, and child also matches kind=server.
	// So root >> child is a match.
	assert.NotEmpty(t, matches)
}

// ═══════════════════════════════════════════════════
// Evaluator — Helper Tests
// ═══════════════════════════════════════════════════

func TestHasStructuralExpr(t *testing.T) {
	// Simple filter — no structural.
	ast, _ := Parse(`{ kind = server }`)
	assert.False(t, HasStructuralExpr(ast))

	// Structural expression.
	ast, _ = Parse(`{ kind = server } >> { name = "test" }`)
	assert.True(t, HasStructuralExpr(ast))

	// Pipeline with structural.
	ast, _ = Parse(`{ kind = server } >> { name = "test" } | select(name)`)
	assert.True(t, HasStructuralExpr(ast))

	// OR with structural on one side.
	ast, _ = Parse(`({kind = server} >> {name = "test"}) || ({kind = client})`)
	assert.True(t, HasStructuralExpr(ast))
}

func TestFindStructuralExpr(t *testing.T) {
	// Direct.
	ast, _ := Parse(`{ kind = server } >> { name = "test" }`)
	s := findStructuralExpr(ast)
	require.NotNil(t, s)
	assert.Equal(t, ">>", s.Operator)

	// Pipeline wrapped.
	ast, _ = Parse(`{ kind = server } >> { name = "test" } | select(name)`)
	s = findStructuralExpr(ast)
	require.NotNil(t, s)
	assert.Equal(t, ">>", s.Operator)

	// OR wrapped.
	ast, _ = Parse(`({kind = server} >> {name = "test"}) || ({kind = client})`)
	s = findStructuralExpr(ast)
	require.NotNil(t, s)
	assert.Equal(t, ">>", s.Operator)
}

// ═══════════════════════════════════════════════════
// EvaluateTraceStructural — OR combination tests
// ═══════════════════════════════════════════════════

func TestEvaluateTraceStructural_OrWithStructuralAndFilter(t *testing.T) {
	// Simulates the Grafana query:
	// ({nestedSetParent<0 && true} &>> {kind = server}) || ({nestedSetParent<0 && true})
	// A trace should match if EITHER the structural branch OR the simple filter matches.

	// Build a trace with a root span (server kind) and a child span (client kind).
	spans := []SpanData{
		{
			SpanID:       "root-1",
			ParentSpanID: "",
			Name:         "GET /api",
			Kind:         "server",
			ServiceName:  "frontend",
			StatusCode:   "ok",
		},
		{
			SpanID:       "child-1",
			ParentSpanID: "root-1",
			Name:         "DB query",
			Kind:         "client",
			ServiceName:  "frontend",
			StatusCode:   "ok",
		},
	}

	// Test 1: Query with structural &>> — root span is ancestor of server span.
	// ({kind = server} &>> {kind = client}) — root(server) is ancestor of child(client).
	ast, err := Parse(`({kind = server} &>> {kind = client}) || ({kind = internal})`)
	require.NoError(t, err)

	result := EvaluateTraceStructural(ast, spans)
	require.NotNil(t, result, "structural branch should match: root(server) &>> child(client)")
	assert.True(t, result.HasMatch)
	// Should have matched root-1 and child-1.
	foundRoot := false
	foundChild := false
	for _, m := range result.Matches {
		if m.LeftSpanID == "root-1" {
			foundRoot = true
		}
		if m.RightSpanID == "child-1" {
			foundChild = true
		}
	}
	assert.True(t, foundRoot, "root-1 should be in matches as left span")
	assert.True(t, foundChild, "child-1 should be in matches as right span")

	// Test 2: Only the filter branch matches (no structural match).
	// ({kind = producer} &>> {kind = consumer}) || ({kind = server})
	// Structural branch doesn't match (no producer/consumer), but filter branch matches root.
	ast, err = Parse(`({kind = producer} &>> {kind = consumer}) || ({kind = server})`)
	require.NoError(t, err)

	result = EvaluateTraceStructural(ast, spans)
	require.NotNil(t, result, "filter branch should match: root has kind=server")
	assert.True(t, result.HasMatch)
	// Should contain root-1 as a synthetic match from the SpanFilter branch.
	foundSynthetic := false
	for _, m := range result.Matches {
		if m.LeftSpanID == "root-1" && m.RightSpanID == "root-1" {
			foundSynthetic = true
		}
	}
	assert.True(t, foundSynthetic, "root-1 should appear as synthetic self-match from SpanFilter")

	// Test 3: Neither branch matches.
	ast, err = Parse(`({kind = producer} &>> {kind = consumer}) || ({kind = internal})`)
	require.NoError(t, err)

	result = EvaluateTraceStructural(ast, spans)
	assert.Nil(t, result, "neither branch should match")
}

func TestEvaluateTraceStructural_OrWithPipeline(t *testing.T) {
	// Simulates: ({nestedSetParent<0} &>> {kind = server}) || ({nestedSetParent<0}) | select(...)
	// The pipeline wrapper (select) should be unwrapped before evaluating OR branches.

	spans := []SpanData{
		{
			SpanID:       "root-1",
			ParentSpanID: "",
			Name:         "GET /",
			Kind:         "server",
			ServiceName:  "web",
			StatusCode:   "ok",
		},
		{
			SpanID:       "child-1",
			ParentSpanID: "root-1",
			Name:         "DB call",
			Kind:         "client",
			ServiceName:  "web",
			StatusCode:   "ok",
		},
	}

	// Parse with pipeline: structural OR filter | select.
	ast, err := Parse(`({kind = server} &>> {kind = client}) || ({kind = server}) | select(name)`)
	require.NoError(t, err)

	result := EvaluateTraceStructural(ast, spans)
	require.NotNil(t, result)
	assert.True(t, result.HasMatch)
	// Both branches should contribute matches.
	assert.True(t, len(result.Matches) >= 2, "both structural and filter branches should match")
}

// ═══════════════════════════════════════════════════
// Evaluator — Sprint 2: =~ and !~ regex matching
// ═══════════════════════════════════════════════════

func TestMatchStringValue_RegexEQ(t *testing.T) {
	// =~ should match regex patterns.
	assert.True(t, matchStringValue("=~", "GET", "GET|POST"))
	assert.True(t, matchStringValue("=~", "POST", "GET|POST"))
	assert.True(t, matchStringValue("=~", "api.v1.users", "^api\\..*"))
	assert.False(t, matchStringValue("=~", "DELETE", "GET|POST"))
}

func TestMatchStringValue_RegexNEQ(t *testing.T) {
	// !~ should NOT match regex patterns.
	assert.True(t, matchStringValue("!~", "DELETE", "GET|POST"))
	assert.True(t, matchStringValue("!~", "gRPC", "GET|POST"))
	assert.False(t, matchStringValue("!~", "GET", "GET|POST"))
}

func TestMatchStringValue_InvalidRegex(t *testing.T) {
	// Invalid regex should return false for both =~ and !~.
	assert.False(t, matchStringValue("=~", "value", "[invalid"))
}

func TestMatchSpanFilter_RegexMatch(t *testing.T) {
	ast, err := Parse(`{ .http.method =~ "GET|POST" }`)
	require.NoError(t, err)

	sf := ast.(*SpanFilter)

	span := SpanData{
		SpanID:  "1",
		Attributes: map[string]string{
			"http.method": "GET",
		},
	}
	assert.True(t, MatchSpanFilter(sf, &span))

	span2 := SpanData{
		SpanID: "2",
		Attributes: map[string]string{
			"http.method": "DELETE",
		},
	}
	assert.False(t, MatchSpanFilter(sf, &span2))
}

// ═══════════════════════════════════════════════════
// Evaluator — Sprint 3: status.message nested intrinsic
// ═══════════════════════════════════════════════════

func TestMatchSpanFilter_StatusMessage(t *testing.T) {
	ast, err := Parse(`{ status.message = "timeout" }`)
	require.NoError(t, err)

	sf := ast.(*SpanFilter)

	// Match: span has status message "timeout"
	span := SpanData{
		SpanID:        "1",
		StatusMessage: "timeout",
	}
	assert.True(t, MatchSpanFilter(sf, &span))

	// No match: different status message
	span2 := SpanData{
		SpanID:        "2",
		StatusMessage: "ok",
	}
	assert.False(t, MatchSpanFilter(sf, &span2))

	// No match: empty status message
	span3 := SpanData{
		SpanID: "3",
	}
	assert.False(t, MatchSpanFilter(sf, &span3))
}

func TestMatchSpanFilter_StatusMessageNotNil(t *testing.T) {
	ast, err := Parse(`{ status.message != nil }`)
	require.NoError(t, err)

	sf := ast.(*SpanFilter)

	// Match: span has any status message
	span := SpanData{
		SpanID:        "1",
		StatusMessage: "something",
	}
	assert.True(t, MatchSpanFilter(sf, &span))

	// No match: empty status message
	span2 := SpanData{
		SpanID: "2",
	}
	assert.False(t, MatchSpanFilter(sf, &span2))
}

func TestMatchSpanFilter_StatusMessageRegex(t *testing.T) {
	ast, err := Parse(`{ status.message =~ "time|error" }`)
	require.NoError(t, err)

	sf := ast.(*SpanFilter)

	assert.True(t, MatchSpanFilter(sf, &SpanData{SpanID: "1", StatusMessage: "timeout"}))
	assert.True(t, MatchSpanFilter(sf, &SpanData{SpanID: "2", StatusMessage: "error"}))
	assert.False(t, MatchSpanFilter(sf, &SpanData{SpanID: "3", StatusMessage: "ok"}))
}


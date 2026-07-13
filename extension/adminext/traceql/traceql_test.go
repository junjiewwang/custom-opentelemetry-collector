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
	assert.Equal(t, "server", plan.SpanKind)
}

func TestPlanOrGroups(t *testing.T) {
	ast, err := Parse(`{ kind = client } || { kind = server }`)
	require.NoError(t, err)

	plan := Plan(ast)
	require.Len(t, plan.TagsOr, 2)
	assert.Equal(t, map[string]string{"kind": "client"}, plan.TagsOr[0])
	assert.Equal(t, map[string]string{"kind": "server"}, plan.TagsOr[1])
}

func TestPlanGrafanaQuery(t *testing.T) {
	input := `({nestedSetParent<0 && true} &>> {kind = server}) || ({nestedSetParent<0 && true}) | select(status, resource.service.name, name)`
	ast, err := Parse(input)
	require.NoError(t, err)

	plan := Plan(ast)
	assert.True(t, plan.HasStructural)
	assert.True(t, plan.IsRoot)
	assert.Equal(t, "server", plan.SpanKind)
	assert.Contains(t, plan.SelectFields, "status")
	assert.Contains(t, plan.SelectFields, "resource.service.name")
	assert.Contains(t, plan.SelectFields, "name")
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
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.want, IsAdvancedQuery(tt.input))
		})
	}
}

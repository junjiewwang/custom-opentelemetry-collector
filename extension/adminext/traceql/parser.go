// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package traceql

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ═══════════════════════════════════════════════════
// Parser — Recursive Descent TraceQL Parser
// ═══════════════════════════════════════════════════

// Parser parses a stream of tokens into an AST.
type Parser struct {
	tokens []Token
	pos    int
}

// Parse parses a raw TraceQL query string into an AST expression.
// Returns nil, nil for empty or no-op queries (like "{}").
func Parse(raw string) (Expr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil, nil
	}

	lexer := NewLexer(raw)
	tokens, err := lexer.Tokenize()
	if err != nil {
		return nil, fmt.Errorf("lexer error: %w", err)
	}

	p := &Parser{tokens: tokens}
	expr, err := p.parseTopLevel()
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	// After parsing the main expression, check for pipeline stages.
	if p.peek().Type == TokenPipe {
		expr, err = p.parsePipeline(expr)
		if err != nil {
			return nil, err
		}
	}

	return expr, nil
}

// ═══════════════════════════════════════════════════
// Top-level parsing
// ═══════════════════════════════════════════════════

// parseTopLevel parses the top-level expression (may contain || or structural ops).
func (p *Parser) parseTopLevel() (Expr, error) {
	left, err := p.parseStructural()
	if err != nil {
		return nil, err
	}

	// Check for || (OR).
	for p.peek().Type == TokenOr {
		p.advance() // consume ||
		right, err := p.parseStructural()
		if err != nil {
			return nil, err
		}
		left = &OrExpr{Left: left, Right: right}
	}

	return left, nil
}

// parseStructural handles structural operators: &>>, >>, >, ~, !>, !>>
func (p *Parser) parseStructural() (Expr, error) {
	left, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}

	for {
		tok := p.peek()
		switch tok.Type {
		case TokenAncestor, TokenDescendant, TokenChild, TokenSibling, TokenNotChild, TokenNotDescendant:
			p.advance()
			right, err := p.parsePrimary()
			if err != nil {
				return nil, err
			}
			left = &StructuralExpr{
				Left:     left,
				Right:    right,
				Operator: tok.Literal,
			}
		default:
			return left, nil
		}
	}
}

// parsePrimary parses primary expressions: span filter {}, or (grouped expr).
func (p *Parser) parsePrimary() (Expr, error) {
	tok := p.peek()

	switch tok.Type {
	case TokenLBrace:
		return p.parseSpanFilter()
	case TokenLParen:
		return p.parseGrouped()
	default:
		return nil, fmt.Errorf("unexpected token %s at position %d, expected '{' or '('", tok.Literal, tok.Pos)
	}
}

// parseGrouped parses a parenthesized expression: ( expr )
func (p *Parser) parseGrouped() (Expr, error) {
	p.advance() // consume (
	expr, err := p.parseTopLevel()
	if err != nil {
		return nil, err
	}
	if p.peek().Type != TokenRParen {
		return nil, fmt.Errorf("expected ')' at position %d, got %s", p.peek().Pos, p.peek().Literal)
	}
	p.advance() // consume )
	return expr, nil
}

// ═══════════════════════════════════════════════════
// Span Filter Parsing: { cond1 && cond2 }
// ═══════════════════════════════════════════════════

// parseSpanFilter parses: { condition1 && condition2 && ... }
func (p *Parser) parseSpanFilter() (Expr, error) {
	p.advance() // consume {

	var conditions []Condition

	for p.peek().Type != TokenRBrace && p.peek().Type != TokenEOF {
		// Handle "true" literal.
		if p.peek().Type == TokenTrue {
			p.advance()
			// Skip "true" — it's a no-op condition.
			if p.peek().Type == TokenAnd {
				p.advance() // consume &&
			}
			continue
		}

		cond, err := p.parseCondition()
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, cond)

		// Consume optional && between conditions.
		if p.peek().Type == TokenAnd {
			p.advance()
		}
	}

	if p.peek().Type != TokenRBrace {
		return nil, fmt.Errorf("expected '}' at position %d, got %s", p.peek().Pos, p.peek().Literal)
	}
	p.advance() // consume }

	return &SpanFilter{Conditions: conditions}, nil
}

// parseCondition parses a single condition: [scope.]key op value
func (p *Parser) parseCondition() (Condition, error) {
	// Read the key (may include scope prefix).
	keyTok := p.peek()
	if keyTok.Type != TokenIdent {
		return Condition{}, fmt.Errorf("expected identifier at position %d, got %q", keyTok.Pos, keyTok.Literal)
	}
	p.advance()

	scope, key := parseScopeAndKey(keyTok.Literal)

	// Read operator.
	opTok := p.peek()
	op, ok := tokenToOperator(opTok.Type)
	if !ok {
		return Condition{}, fmt.Errorf("expected operator at position %d, got %q", opTok.Pos, opTok.Literal)
	}
	p.advance()

	// Read value.
	value, err := p.parseValue()
	if err != nil {
		return Condition{}, err
	}

	return Condition{
		Scope:    scope,
		Key:      key,
		Operator: op,
		Value:    value,
	}, nil
}

// parseValue parses a value: string, number, true, false, or unquoted ident.
func (p *Parser) parseValue() (any, error) {
	tok := p.peek()
	p.advance()

	switch tok.Type {
	case TokenString:
		return tok.Literal, nil
	case TokenNumber:
		return parseNumericValue(tok.Literal)
	case TokenTrue:
		return true, nil
	case TokenFalse:
		return false, nil
	case TokenIdent:
		// Unquoted string value (e.g., "error", "server", "client").
		return tok.Literal, nil
	default:
		return nil, fmt.Errorf("expected value at position %d, got %q", tok.Pos, tok.Literal)
	}
}

// ═══════════════════════════════════════════════════
// Pipeline Parsing: expr | stage1 | stage2
// ═══════════════════════════════════════════════════

// parsePipeline wraps the input expr with pipeline stages.
func (p *Parser) parsePipeline(input Expr) (Expr, error) {
	pipeline := &PipelineExpr{Input: input}

	for p.peek().Type == TokenPipe {
		p.advance() // consume |
		stage, err := p.parsePipelineStage()
		if err != nil {
			return nil, err
		}
		pipeline.Stages = append(pipeline.Stages, stage)
	}

	return pipeline, nil
}

// parsePipelineStage parses a single pipeline stage (currently only select()).
func (p *Parser) parsePipelineStage() (PipelineStage, error) {
	tok := p.peek()
	if tok.Type == TokenSelect {
		return p.parseSelectStage()
	}
	// Unknown stage — skip tokens until next | or EOF as graceful degradation.
	return p.parseUnknownStage()
}

// parseSelectStage parses: select(field1, field2, ...)
func (p *Parser) parseSelectStage() (*SelectStage, error) {
	p.advance() // consume "select"

	if p.peek().Type != TokenLParen {
		return nil, fmt.Errorf("expected '(' after select at position %d", p.peek().Pos)
	}
	p.advance() // consume (

	var fields []string
	for p.peek().Type != TokenRParen && p.peek().Type != TokenEOF {
		tok := p.peek()
		if tok.Type == TokenIdent || tok.Type == TokenString {
			fields = append(fields, tok.Literal)
			p.advance()
		} else {
			return nil, fmt.Errorf("unexpected token %q in select() at position %d", tok.Literal, tok.Pos)
		}

		if p.peek().Type == TokenComma {
			p.advance() // consume ,
		}
	}

	if p.peek().Type != TokenRParen {
		return nil, fmt.Errorf("expected ')' at position %d", p.peek().Pos)
	}
	p.advance() // consume )

	return &SelectStage{Fields: fields}, nil
}

// parseUnknownStage skips tokens for an unknown pipeline stage (graceful degradation).
type unknownStage struct {
	raw string
}

func (u *unknownStage) stageType() string { return "unknown" }
func (u *unknownStage) String() string    { return u.raw }

func (p *Parser) parseUnknownStage() (PipelineStage, error) {
	var parts []string
	// Consume tokens until we hit | or EOF.
	for p.peek().Type != TokenPipe && p.peek().Type != TokenEOF {
		parts = append(parts, p.peek().Literal)
		p.advance()
	}
	return &unknownStage{raw: strings.Join(parts, " ")}, nil
}

// ═══════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════

func (p *Parser) peek() Token {
	if p.pos >= len(p.tokens) {
		return Token{Type: TokenEOF, Pos: -1}
	}
	return p.tokens[p.pos]
}

func (p *Parser) advance() Token {
	tok := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
	}
	return tok
}

// parseScopeAndKey splits "resource.service.name" → ("resource", "service.name")
// and ".http.method" → ("", "http.method")
// and "kind" → ("", "kind")
func parseScopeAndKey(raw string) (scope, key string) {
	// Leading dot: unscoped attribute.
	if strings.HasPrefix(raw, ".") {
		return "", raw[1:]
	}

	// Check for explicit scope prefixes.
	for _, prefix := range []string{"resource.", "span."} {
		if strings.HasPrefix(raw, prefix) {
			return strings.TrimSuffix(prefix, "."), raw[len(prefix):]
		}
	}

	// No scope prefix — intrinsic field.
	return "", raw
}

// tokenToOperator maps a token type to its operator string.
func tokenToOperator(tt TokenType) (string, bool) {
	switch tt {
	case TokenEq:
		return "=", true
	case TokenNeq:
		return "!=", true
	case TokenLt:
		return "<", true
	case TokenGt, TokenChild: // > is used as both GT and child operator depending on context
		return ">", true
	case TokenLte:
		return "<=", true
	case TokenGte:
		return ">=", true
	case TokenRegex:
		return "=~", true
	default:
		return "", false
	}
}

// parseNumericValue parses a number literal which may include a duration suffix.
func parseNumericValue(s string) (any, error) {
	// Try as int64 first (bare numbers without suffix).
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}

	// Try as float64 (e.g., 3.14).
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		// Check if it has a duration suffix (time.ParseDuration("3.14") would fail).
		if !hasDurationSuffix(s) {
			return f, nil
		}
	}

	// Try as duration (handles 100ms, 1s, 500us, 0s, etc.)
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	return s, nil
}

// hasDurationSuffix checks if the string ends with a duration unit suffix.
func hasDurationSuffix(s string) bool {
	suffixes := []string{"ns", "us", "µs", "ms", "s", "m", "h"}
	for _, suffix := range suffixes {
		if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package logql

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Parse parses a LogQL log query string into a LogQLQuery.
//
// Supported syntax (MVP):
//
//	{label="value", label=~"pattern"}           — stream selector
//	{label="value"} |= "substring"               — line contains filter
//	{label="value"} != "substring"               — line not contains filter
//	{label="value"} |~ "regex"                   — line regex filter
//	{label="value"} !~ "regex"                   — line not regex filter
func Parse(input string) (*LogQLQuery, error) {
	p := &parser{input: input}
	return p.parse()
}

// knownAggregations lists aggregation function keywords.
var knownAggregations = map[string]bool{
	"sum":      true,
	"avg":      true,
	"max":      true,
	"min":      true,
	"topk":     true,
	"bottomk":  true,
	"count":    true,
}

// knownRangeFunctions lists range vector function keywords.
var knownRangeFunctions = map[string]bool{
	"count_over_time": true,
	"rate":            true,
	"increase":        true,
	"bytes_rate":      true,
	"bytes_over_time": true,
}

// IsMetricQuery returns true if the raw input looks like a LogQL metric query
// rather than a log query. Metric queries start with an aggregation keyword or
// range function, while log queries start with '{'.
func IsMetricQuery(input string) bool {
	s := strings.TrimSpace(input)
	if s == "" {
		return false
	}
	// Log queries always start with '{'.
	if s[0] == '{' {
		return false
	}
	// Quick check: identify the first word.
	first := ""
	for i := 0; i < len(s) && isIdentChar(s[i]); i++ {
		first += string(s[i])
	}
	return knownAggregations[first] || knownRangeFunctions[first]
}

// ParseMetric parses a LogQL metric query into a MetricExpr.
//
// Supported syntax:
//
//	count_over_time({label="value"}[duration])
//	sum by (label1, label2) (count_over_time({label="value"}[duration]))
//	avg (rate({}[duration]))
func ParseMetric(input string) (*MetricExpr, error) {
	p := &parser{input: input}
	return p.parseMetric()
}

// parseMetric parses a metric query expression.
// Grammar:
//
//	metric  = [aggregation [by "(" labellist ")"]] "(" function "(" logquery "[" duration "]" ")" ")"
func (p *parser) parseMetric() (*MetricExpr, error) {
	expr := &MetricExpr{}

	p.skipWhitespace()
	first := p.parseIdentifier()
	if first == "" {
		return nil, p.errorf("expected metric expression")
	}

	// Check if the first identifier is an aggregation keyword.
	if knownAggregations[first] {
		expr.Aggregation = first
		p.skipWhitespace()

		// Parse optional by (label1, label2)
		if p.matchPrefix("by") {
			p.advanceN(2)
			p.skipWhitespace()
			if !p.match('(') {
				return nil, p.errorf("expected '(' after 'by'")
			}
			p.advance()
			p.skipWhitespace()
			labels, err := p.parseIdentList()
			if err != nil {
				return nil, err
			}
			expr.By = labels
			p.skipWhitespace()
			if !p.match(')') {
				return nil, p.errorf("expected ')' after 'by' labels")
			}
			p.advance()
		}

		// Expect '(' before function
		p.skipWhitespace()
		if !p.match('(') {
			return nil, p.errorf("expected '(' after aggregation")
		}
		p.advance()
		p.skipWhitespace()

		// Parse function name
		funcName := p.parseIdentifier()
		if !knownRangeFunctions[funcName] {
			return nil, p.errorf("unknown range function %q", funcName)
		}
		expr.Function = funcName
	} else if knownRangeFunctions[first] {
		// Plain function without outer aggregation: rate({}[1m])
		expr.Function = first
	} else {
		return nil, p.errorf("expected aggregation or range function, got %q", first)
	}

	// Parse function arguments: ( INNER_LOGQL [DURATION] )
	p.skipWhitespace()
	if !p.match('(') {
		return nil, p.errorf("expected '(' before function arguments")
	}
	p.advance()
	p.skipWhitespace()

	// Parse inner log query (stream selector + optional filters)
	inner, err := p.parse()
	if err != nil {
		return nil, err
	}
	expr.Inner = inner

	// Parse range vector: [5m], [1h30s]
	p.skipWhitespace()
	dur, err := p.parseRangeVector()
	if err != nil {
		return nil, err
	}
	expr.RangeDuration = dur

	// Close function parentheses
	p.skipWhitespace()
	if !p.match(')') {
		return nil, p.errorf("expected ')' to close function call")
	}
	p.advance()

	// Close aggregation parentheses (if we opened one)
	if expr.Aggregation != "" {
		p.skipWhitespace()
		if !p.match(')') {
			return nil, p.errorf("expected ')' to close aggregation")
		}
		p.advance()
	}

	p.skipWhitespace()
	if p.pos < len(p.input) {
		return nil, p.errorf("unexpected trailing input")
	}

	return expr, nil
}

// parseIdentList parses a comma-separated list of identifiers: label1, label2
func (p *parser) parseIdentList() ([]string, error) {
	var list []string
	for {
		id := p.parseIdentifier()
		if id == "" {
			return nil, p.errorf("expected identifier")
		}
		list = append(list, id)
		p.skipWhitespace()
		if !p.match(',') {
			break
		}
		p.advance()
		p.skipWhitespace()
	}
	return list, nil
}

// parseRangeVector parses a duration range vector: [5m], [1h30s]
func (p *parser) parseRangeVector() (time.Duration, error) {
	if !p.match('[') {
		return 0, p.errorf("expected '[' for range vector")
	}
	p.advance()

	start := p.pos
	for p.pos < len(p.input) && p.input[p.pos] != ']' {
		p.pos++
	}
	if p.pos >= len(p.input) {
		return 0, p.errorf("unterminated range vector, expected ']'")
	}
	durStr := strings.TrimSpace(p.input[start:p.pos])
	p.advance() // skip ']'

	dur, err := time.ParseDuration(durStr)
	if err != nil {
		return 0, p.errorf("invalid duration %q: %v", durStr, err)
	}
	return dur, nil
}

type parser struct {
	input string
	pos   int
}

func (p *parser) parse() (*LogQLQuery, error) {
	q := &LogQLQuery{}

	// Parse stream selector: { ... }
	sel, err := p.parseStreamSelector()
	if err != nil {
		return nil, err
	}
	q.StreamSelector = *sel

	// Parse optional line filters and pipeline stages.
	// Line filters: |=, !=, |~, !~ (second char is = or ~)
	// Pipeline:     | json, | logfmt, | line_format (second char is letter)
	for p.pos < len(p.input) {
		p.skipWhitespace()
		if p.pos >= len(p.input) {
			break
		}
		if !p.match('|') && !p.match('!') {
			break
		}

		// Peek: is the next char '=' or '~'? → line filter
		// Otherwise (letter, etc.) → pipeline stage
		if p.pos+1 < len(p.input) {
			next := p.input[p.pos+1]
			if next == '=' || next == '~' || (p.match('!') && p.pos+2 < len(p.input) && (p.input[p.pos+2] == '=' || p.input[p.pos+2] == '~')) {
				f, err := p.parseLineFilter()
				if err != nil {
					return nil, err
				}
				q.LineFilters = append(q.LineFilters, f)
				continue
			}
		}

		stage, err := p.parsePipelineStage()
		if err != nil {
			return nil, err
		}
		q.Pipeline = append(q.Pipeline, *stage)
	}

	return q, nil
}

func (p *parser) parseStreamSelector() (*StreamSelector, error) {
	p.skipWhitespace()
	if !p.match('{') {
		return nil, p.errorf("expected '{'")
	}
	p.advance()

	sel := &StreamSelector{}
	first := true

	for p.pos < len(p.input) {
		p.skipWhitespace()
		if p.peek() == '}' {
			break
		}
		if !first {
			if !p.match(',') {
				return nil, p.errorf("expected ',' between label matchers")
			}
			p.advance()
			p.skipWhitespace()
		}
		first = false

		m, err := p.parseLabelMatcher()
		if err != nil {
			return nil, err
		}
		sel.Matchers = append(sel.Matchers, *m)
	}

	if !p.match('}') {
		return nil, p.errorf("expected '}'")
	}
	p.advance()

	return sel, nil
}

func (p *parser) parseLabelMatcher() (*LabelMatcher, error) {
	name := p.parseIdentifier()
	if name == "" {
		return nil, p.errorf("expected label name")
	}
	p.skipWhitespace()

	mt, err := p.parseMatchOp()
	if err != nil {
		return nil, err
	}
	p.skipWhitespace()

	val, err := p.parseQuotedString()
	if err != nil {
		return nil, err
	}

	return &LabelMatcher{Name: name, Type: mt, Value: val}, nil
}

func (p *parser) parseMatchOp() (MatchType, error) {
	switch {
	case p.matchPrefix("=~"):
		p.advanceN(2)
		return MatchRegex, nil
	case p.matchPrefix("!~"):
		p.advanceN(2)
		return MatchNotRegex, nil
	case p.match('='):
		p.advance()
		if p.match('~') { // edge case: =~ already caught
			return MatchEqual, nil
		}
		return MatchEqual, nil
	case p.matchPrefix("!="):
		p.advanceN(2)
		return MatchNotEqual, nil
	default:
		return 0, p.errorf("expected label match operator (=, !=, =~, !~)")
	}
}

// knownParserNames lists pipeline parser keywords (json, logfmt, unpack).
var knownParserNames = map[string]bool{
	"json":   true,
	"logfmt": true,
	"unpack": true,
}

// knownPipelineKeywords lists all pipeline keywords (parser + formatter).
var knownPipelineKeywords = map[string]bool{
	"json":        true,
	"logfmt":      true,
	"unpack":      true,
	"line_format": true,
	"label_format": true,
}

func (p *parser) parsePipelineStage() (*PipelineStage, error) {
	if !p.match('|') {
		return nil, p.errorf("expected '|' before pipeline stage")
	}
	p.advance()
	p.skipWhitespace()

	keyword := p.parseIdentifier()
	switch {
	case knownParserNames[keyword]:
		// Parser stage: | json, | logfmt, | unpack
		stage := &PipelineStage{Type: PipelineParser, Parser: keyword}

		// Check for chained label filter: | json | level = "error"
		p.skipWhitespace()
		if p.match('|') {
			// Peek ahead: is the next item an identifier (label filter) or a keyword?
			saved := p.pos
			p.advance()
			p.skipWhitespace()
			next := p.parseIdentifier()
			if next != "" && !knownPipelineKeywords[next] {
				// It's a label filter: | json | level = "error"
				p.pos = saved // rewind to re-parse as label filter
				lf, err := p.parsePipelineLabelFilter()
				if err != nil {
					return nil, err
				}
				stage.LabelFilter = lf
			} else {
				p.pos = saved // rewind, let outer loop handle next stage
			}
		}
		return stage, nil

	case keyword == "line_format":
		p.skipWhitespace()
		tmpl, err := p.parseQuotedString()
		if err != nil {
			return nil, err
		}
		return &PipelineStage{Type: PipelineLineFormat, LineFormat: tmpl}, nil

	case keyword == "label_format":
		p.skipWhitespace()
		m, err := p.parseLabelMatcher()
		if err != nil {
			return nil, err
		}
		return &PipelineStage{Type: PipelineLabelFormat, LabelFormat: m}, nil

	default:
		// Not a known keyword — treat as pipeline label filter
		// Rewind to before the identifier
		p.pos -= len(keyword)
		lf, err := p.parsePipelineLabelFilter()
		if err != nil {
			return nil, err
		}
		return &PipelineStage{Type: PipelineLabelFilter, LabelFilter: lf}, nil
	}
}

// parsePipelineLabelFilter parses a label filter within the pipeline:
//
//	| json | level = "error"   (the "| level = 'error'" part)
func (p *parser) parsePipelineLabelFilter() (*LabelMatcher, error) {
	if !p.match('|') {
		return nil, p.errorf("expected '|' before pipeline label filter")
	}
	p.advance()
	p.skipWhitespace()
	return p.parseLabelMatcher()
}

func (p *parser) parseLineFilter() (LineFilter, error) {
	// Line filters start with | (pipe) or ! (negation):
	//   |=  match substring    !=  not match substring
	//   |~  regex match        !~  regex not match
	switch {
	case p.match('|'):
		p.advance()
		switch {
		case p.match('='):
			p.advance()
			p.skipWhitespace()
			pat, err := p.parseQuotedString()
			return LineFilter{Type: FilterContains, Pattern: pat}, err
		case p.match('~'):
			p.advance()
			p.skipWhitespace()
			pat, err := p.parseQuotedString()
			return LineFilter{Type: FilterRegex, Pattern: pat}, err
		default:
			return LineFilter{}, p.errorf("expected |= or |~")
		}
	case p.match('!'):
		p.advance()
		switch {
		case p.match('='):
			p.advance()
			p.skipWhitespace()
			pat, err := p.parseQuotedString()
			return LineFilter{Type: FilterNotContains, Pattern: pat}, err
		case p.match('~'):
			p.advance()
			p.skipWhitespace()
			pat, err := p.parseQuotedString()
			return LineFilter{Type: FilterNotRegex, Pattern: pat}, err
		default:
			return LineFilter{}, p.errorf("expected != or !~")
		}
	default:
		return LineFilter{}, p.errorf("expected line filter operator (|=, !=, |~, !~)")
	}
}

func (p *parser) parseIdentifier() string {
	start := p.pos
	for p.pos < len(p.input) && isIdentChar(p.input[p.pos]) {
		p.pos++
	}
	return p.input[start:p.pos]
}

func (p *parser) parseQuotedString() (string, error) {
	if !p.match('"') && !p.match('`') {
		return "", p.errorf("expected quoted string")
	}
	quote := p.input[p.pos]
	p.advance()

	var sb strings.Builder
	for p.pos < len(p.input) && p.input[p.pos] != quote {
		ch := p.input[p.pos]
		if ch == '\\' && p.pos+1 < len(p.input) {
			p.advance()
			switch p.input[p.pos] {
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case '\\':
				sb.WriteByte('\\')
			case '"':
				sb.WriteByte('"')
			default:
				sb.WriteByte(p.input[p.pos])
			}
		} else {
			sb.WriteByte(ch)
		}
		p.advance()
	}

	if !p.match(quote) {
		return "", p.errorf("unterminated string")
	}
	p.advance()
	return sb.String(), nil
}

// --- helpers ---

func (p *parser) peek() byte {
	if p.pos >= len(p.input) {
		return 0
	}
	return p.input[p.pos]
}

func (p *parser) match(b byte) bool {
	return p.pos < len(p.input) && p.input[p.pos] == b
}

func (p *parser) matchPrefix(s string) bool {
	return p.pos+len(s) <= len(p.input) && p.input[p.pos:p.pos+len(s)] == s
}

func (p *parser) advance() { p.pos++ }

func (p *parser) advanceN(n int) { p.pos += n }

func (p *parser) skipWhitespace() {
	for p.pos < len(p.input) && isSpace(p.input[p.pos]) {
		p.pos++
	}
}

func isSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

func isIdentChar(ch byte) bool {
	return unicode.IsLetter(rune(ch)) || unicode.IsDigit(rune(ch)) || ch == '_' || ch == '.'
}

func (p *parser) errorf(format string, args ...interface{}) error {
	msg := fmt.Sprintf(format, args...)
	return fmt.Errorf("logql:%d: %s (near %q)", p.pos, msg, context(p.input, p.pos))
}

func context(input string, pos int) string {
	start := pos
	if start > 10 {
		start = pos - 10
	}
	end := pos + 20
	if end > len(input) {
		end = len(input)
	}
	return input[start:end]
}

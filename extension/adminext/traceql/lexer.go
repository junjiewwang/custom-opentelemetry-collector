// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package traceql

import (
	"fmt"
	"strings"
	"unicode"
)

// ═══════════════════════════════════════════════════
// Token Types
// ═══════════════════════════════════════════════════

// TokenType represents the type of a lexer token.
type TokenType int

const (
	TokenEOF          TokenType = iota
	TokenLBrace                 // {
	TokenRBrace                 // }
	TokenLParen                 // (
	TokenRParen                 // )
	TokenPipe                   // |
	TokenOr                     // ||
	TokenAnd                    // &&
	TokenAncestor               // &>>
	TokenDescendant             // >>
	TokenChild                  // >
	TokenSibling                // ~
	TokenNotChild               // !>
	TokenNotDescendant          // !>>
	TokenEq                     // =
	TokenNeq                    // !=
	TokenRegex                  // =~
	TokenNotRegex               // !~
	TokenLt                     // <
	TokenGt                     // >  (reused from TokenChild in different context)
	TokenLte                    // <=
	TokenGte                    // >=
	TokenIdent                  // identifiers: service.name, kind, status, etc.
	TokenString                 // "quoted string"
	TokenNumber                 // 123, 3.14, 100ms, 1s, 500us
	TokenTrue                   // true
	TokenFalse                  // false
	TokenSelect                 // select
	TokenBy                     // by
	TokenWith                   // with
	TokenRate                   // rate
	TokenQuantileOverTime       // quantile_over_time
	TokenHistogramOverTime      // histogram_over_time
	TokenComma                  // ,
	TokenDot                    // . (leading dot for unscoped attributes)
)

// Token represents a single lexer token.
type Token struct {
	Type    TokenType
	Literal string
	Pos     int // byte position in input
}

func (t Token) String() string {
	return fmt.Sprintf("Token(%d, %q, pos=%d)", t.Type, t.Literal, t.Pos)
}

// ═══════════════════════════════════════════════════
// Lexer
// ═══════════════════════════════════════════════════

// Lexer tokenizes a TraceQL query string.
type Lexer struct {
	input  string
	pos    int
	tokens []Token
}

// NewLexer creates a new lexer for the given input.
func NewLexer(input string) *Lexer {
	return &Lexer{input: input}
}

// Tokenize processes the entire input and returns all tokens.
func (l *Lexer) Tokenize() ([]Token, error) {
	l.tokens = nil
	for l.pos < len(l.input) {
		// Skip whitespace.
		if unicode.IsSpace(rune(l.input[l.pos])) {
			l.pos++
			continue
		}

		tok, err := l.nextToken()
		if err != nil {
			return nil, err
		}
		l.tokens = append(l.tokens, tok)
	}
	l.tokens = append(l.tokens, Token{Type: TokenEOF, Pos: l.pos})
	return l.tokens, nil
}

// nextToken reads the next token from current position.
func (l *Lexer) nextToken() (Token, error) {
	pos := l.pos
	ch := l.input[l.pos]

	switch {
	case ch == '{':
		l.pos++
		return Token{Type: TokenLBrace, Literal: "{", Pos: pos}, nil
	case ch == '}':
		l.pos++
		return Token{Type: TokenRBrace, Literal: "}", Pos: pos}, nil
	case ch == '(':
		l.pos++
		return Token{Type: TokenLParen, Literal: "(", Pos: pos}, nil
	case ch == ')':
		l.pos++
		return Token{Type: TokenRParen, Literal: ")", Pos: pos}, nil
	case ch == ',':
		l.pos++
		return Token{Type: TokenComma, Literal: ",", Pos: pos}, nil
	case ch == '|':
		return l.readPipeOrOr(pos)
	case ch == '&':
		return l.readAmpersand(pos)
	case ch == '!':
		return l.readBang(pos)
	case ch == '=':
		return l.readEquals(pos)
	case ch == '<':
		return l.readLessThan(pos)
	case ch == '>':
		return l.readGreaterThan(pos)
	case ch == '~':
		l.pos++
		return Token{Type: TokenSibling, Literal: "~", Pos: pos}, nil
	case ch == '"' || ch == '\'':
		return l.readString(pos)
	case ch == '.' && l.pos+1 < len(l.input) && isIdentStart(rune(l.input[l.pos+1])):
		// Leading dot for unscoped attribute: .http.method
		return l.readDottedIdent(pos)
	case isDigit(ch) || (ch == '-' && l.pos+1 < len(l.input) && isDigit(l.input[l.pos+1])):
		return l.readNumber(pos)
	case isIdentStart(rune(ch)):
		return l.readIdent(pos)
	default:
		return Token{}, fmt.Errorf("unexpected character %q at position %d", ch, pos)
	}
}

// ═══════════════════════════════════════════════════
// Multi-character token readers
// ═══════════════════════════════════════════════════

func (l *Lexer) readPipeOrOr(pos int) (Token, error) {
	l.pos++
	if l.pos < len(l.input) && l.input[l.pos] == '|' {
		l.pos++
		return Token{Type: TokenOr, Literal: "||", Pos: pos}, nil
	}
	return Token{Type: TokenPipe, Literal: "|", Pos: pos}, nil
}

func (l *Lexer) readAmpersand(pos int) (Token, error) {
	l.pos++
	if l.pos < len(l.input) && l.input[l.pos] == '&' {
		l.pos++
		return Token{Type: TokenAnd, Literal: "&&", Pos: pos}, nil
	}
	// &>> (ancestor operator)
	if l.pos < len(l.input) && l.input[l.pos] == '>' {
		l.pos++
		if l.pos < len(l.input) && l.input[l.pos] == '>' {
			l.pos++
			return Token{Type: TokenAncestor, Literal: "&>>", Pos: pos}, nil
		}
		// &> is not a valid operator, treat as error
		return Token{}, fmt.Errorf("invalid operator '&>' at position %d, did you mean '&>>'?", pos)
	}
	return Token{}, fmt.Errorf("unexpected '&' at position %d", pos)
}

func (l *Lexer) readBang(pos int) (Token, error) {
	l.pos++
	if l.pos < len(l.input) && l.input[l.pos] == '=' {
		l.pos++
		return Token{Type: TokenNeq, Literal: "!=", Pos: pos}, nil
	}
	if l.pos < len(l.input) && l.input[l.pos] == '~' {
		l.pos++
		return Token{Type: TokenNotRegex, Literal: "!~", Pos: pos}, nil
	}
	if l.pos < len(l.input) && l.input[l.pos] == '>' {
		l.pos++
		if l.pos < len(l.input) && l.input[l.pos] == '>' {
			l.pos++
			return Token{Type: TokenNotDescendant, Literal: "!>>", Pos: pos}, nil
		}
		return Token{Type: TokenNotChild, Literal: "!>", Pos: pos}, nil
	}
	return Token{}, fmt.Errorf("unexpected '!' at position %d", pos)
}

func (l *Lexer) readEquals(pos int) (Token, error) {
	l.pos++
	if l.pos < len(l.input) && l.input[l.pos] == '~' {
		l.pos++
		return Token{Type: TokenRegex, Literal: "=~", Pos: pos}, nil
	}
	return Token{Type: TokenEq, Literal: "=", Pos: pos}, nil
}

func (l *Lexer) readLessThan(pos int) (Token, error) {
	l.pos++
	if l.pos < len(l.input) && l.input[l.pos] == '=' {
		l.pos++
		return Token{Type: TokenLte, Literal: "<=", Pos: pos}, nil
	}
	return Token{Type: TokenLt, Literal: "<", Pos: pos}, nil
}

func (l *Lexer) readGreaterThan(pos int) (Token, error) {
	l.pos++
	if l.pos < len(l.input) && l.input[l.pos] == '>' {
		l.pos++
		return Token{Type: TokenDescendant, Literal: ">>", Pos: pos}, nil
	}
	if l.pos < len(l.input) && l.input[l.pos] == '=' {
		l.pos++
		return Token{Type: TokenGte, Literal: ">=", Pos: pos}, nil
	}
	return Token{Type: TokenChild, Literal: ">", Pos: pos}, nil
}

func (l *Lexer) readString(pos int) (Token, error) {
	quote := l.input[l.pos]
	l.pos++
	var sb strings.Builder
	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		if ch == '\\' && l.pos+1 < len(l.input) {
			l.pos++
			sb.WriteByte(l.input[l.pos])
			l.pos++
			continue
		}
		if ch == quote {
			l.pos++
			return Token{Type: TokenString, Literal: sb.String(), Pos: pos}, nil
		}
		sb.WriteByte(ch)
		l.pos++
	}
	return Token{}, fmt.Errorf("unterminated string starting at position %d", pos)
}

// readDottedIdent reads a dotted attribute reference: .http.method
func (l *Lexer) readDottedIdent(pos int) (Token, error) {
	l.pos++ // skip leading dot
	start := l.pos
	for l.pos < len(l.input) && (isIdentChar(rune(l.input[l.pos])) || l.input[l.pos] == '.') {
		l.pos++
	}
	literal := "." + l.input[start:l.pos]
	return Token{Type: TokenIdent, Literal: literal, Pos: pos}, nil
}

func (l *Lexer) readNumber(pos int) (Token, error) {
	start := l.pos
	if l.input[l.pos] == '-' {
		l.pos++
	}
	for l.pos < len(l.input) && isDigit(l.input[l.pos]) {
		l.pos++
	}
	// Handle decimal point.
	if l.pos < len(l.input) && l.input[l.pos] == '.' {
		l.pos++
		for l.pos < len(l.input) && isDigit(l.input[l.pos]) {
			l.pos++
		}
	}
	// Handle duration suffixes: ns, us, µs, ms, s, m, h
	if l.pos < len(l.input) && isLetter(l.input[l.pos]) {
		for l.pos < len(l.input) && isLetter(l.input[l.pos]) {
			l.pos++
		}
	}
	return Token{Type: TokenNumber, Literal: l.input[start:l.pos], Pos: pos}, nil
}

func (l *Lexer) readIdent(pos int) (Token, error) {
	start := l.pos
	for l.pos < len(l.input) && (isIdentChar(rune(l.input[l.pos])) || l.input[l.pos] == '.') {
		l.pos++
	}
	literal := l.input[start:l.pos]

	// Check for keywords.
	switch literal {
	case "true":
		return Token{Type: TokenTrue, Literal: literal, Pos: pos}, nil
	case "false":
		return Token{Type: TokenFalse, Literal: literal, Pos: pos}, nil
	case "select":
		return Token{Type: TokenSelect, Literal: literal, Pos: pos}, nil
	case "by":
		return Token{Type: TokenBy, Literal: literal, Pos: pos}, nil
	case "with":
		return Token{Type: TokenWith, Literal: literal, Pos: pos}, nil
	case "rate":
		return Token{Type: TokenRate, Literal: literal, Pos: pos}, nil
	case "quantile_over_time":
		return Token{Type: TokenQuantileOverTime, Literal: literal, Pos: pos}, nil
	case "histogram_over_time":
		return Token{Type: TokenHistogramOverTime, Literal: literal, Pos: pos}, nil
	case "nil":
		return Token{Type: TokenIdent, Literal: literal, Pos: pos}, nil
	}

	return Token{Type: TokenIdent, Literal: literal, Pos: pos}, nil
}

// ═══════════════════════════════════════════════════
// Character classification helpers
// ═══════════════════════════════════════════════════

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

func isLetter(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isIdentStart(ch rune) bool {
	return unicode.IsLetter(ch) || ch == '_'
}

func isIdentChar(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' || ch == '-'
}

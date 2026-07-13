// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package traceql implements a subset of the TraceQL query language used by Grafana Tempo.
// It provides lexing, parsing (to AST), and condition extraction for downstream ES queries.
package traceql

import "fmt"

// ═══════════════════════════════════════════════════
// Node Types
// ═══════════════════════════════════════════════════

// NodeType classifies AST node categories.
type NodeType int

const (
	NodeSpanFilter  NodeType = iota // { conditions }
	NodeStructural                  // &>>, >>, >, ~, !>, !>>
	NodePipeline                    // |
	NodeSelect                      // select(fields...)
	NodeOr                          // ||
	NodeAnd                         // &&  (implicit inside span filter)
	NodeComparison                  // key op value
)

// ═══════════════════════════════════════════════════
// Interfaces
// ═══════════════════════════════════════════════════

// Expr is the interface implemented by all AST nodes.
type Expr interface {
	Type() NodeType
	String() string
}

// ═══════════════════════════════════════════════════
// Concrete AST Nodes
// ═══════════════════════════════════════════════════

// SpanFilter represents a span selector: { cond1 && cond2 && ... }
type SpanFilter struct {
	Conditions []Condition
}

func (s *SpanFilter) Type() NodeType { return NodeSpanFilter }
func (s *SpanFilter) String() string {
	out := "{ "
	for i, c := range s.Conditions {
		if i > 0 {
			out += " && "
		}
		out += c.String()
	}
	return out + " }"
}

// Condition represents a single comparison: key op value
type Condition struct {
	Scope    string // "resource", "span", "" (intrinsic or unscoped)
	Key      string // "service.name", "kind", "status", "nestedSetParent", "duration", etc.
	Operator string // "=", "!=", "<", ">", ">=", "<=", "=~"
	Value    any    // string / int64 / float64 / bool / nil
}

func (c Condition) String() string {
	scope := ""
	if c.Scope != "" {
		scope = c.Scope + "."
	}
	return fmt.Sprintf("%s%s %s %v", scope, c.Key, c.Operator, c.Value)
}

// IsIntrinsic returns true if the condition references a built-in span field
// (not a user-defined attribute).
func (c Condition) IsIntrinsic() bool {
	switch c.Key {
	case "name", "status", "kind", "duration",
		"nestedSetParent", "nestedSetLeft", "nestedSetRight",
		"rootName", "rootServiceName", "traceDuration":
		return c.Scope == "" // intrinsics have no scope prefix
	}
	return false
}

// StructuralExpr represents a structural relationship: {A} &>> {B}
type StructuralExpr struct {
	Left     Expr
	Right    Expr
	Operator string // "&>>", ">>", ">", "~", "!>", "!>>"
}

func (s *StructuralExpr) Type() NodeType { return NodeStructural }
func (s *StructuralExpr) String() string {
	return fmt.Sprintf("(%s %s %s)", s.Left.String(), s.Operator, s.Right.String())
}

// PipelineExpr represents a pipeline: expr | stage1 | stage2
type PipelineExpr struct {
	Input  Expr
	Stages []PipelineStage
}

func (p *PipelineExpr) Type() NodeType { return NodePipeline }
func (p *PipelineExpr) String() string {
	out := p.Input.String()
	for _, s := range p.Stages {
		out += " | " + s.String()
	}
	return out
}

// PipelineStage is implemented by all pipeline stage types.
type PipelineStage interface {
	stageType() string
	String() string
}

// SelectStage represents: select(field1, field2, ...)
type SelectStage struct {
	Fields []string
}

func (s *SelectStage) stageType() string { return "select" }
func (s *SelectStage) String() string {
	out := "select("
	for i, f := range s.Fields {
		if i > 0 {
			out += ", "
		}
		out += f
	}
	return out + ")"
}

// OrExpr represents: exprA || exprB
type OrExpr struct {
	Left  Expr
	Right Expr
}

func (o *OrExpr) Type() NodeType { return NodeOr }
func (o *OrExpr) String() string {
	return fmt.Sprintf("(%s || %s)", o.Left.String(), o.Right.String())
}

// TrueExpr is a sentinel representing the literal "true" in {nestedSetParent<0 && true}.
type TrueExpr struct{}

func (t *TrueExpr) Type() NodeType { return NodeComparison }
func (t *TrueExpr) String() string { return "true" }

// ═══════════════════════════════════════════════════
// Intrinsic Constants
// ═══════════════════════════════════════════════════

// SpanKind values used in TraceQL.
const (
	SpanKindClient   = "client"
	SpanKindServer   = "server"
	SpanKindProducer = "producer"
	SpanKindConsumer = "consumer"
	SpanKindInternal = "internal"
)

// SpanStatus values used in TraceQL.
const (
	SpanStatusOk    = "ok"
	SpanStatusError = "error"
	SpanStatusUnset = "unset"
)

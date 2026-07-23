// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package traceql implements a subset of the TraceQL query language used by Grafana Tempo.
// It provides lexing, parsing (to AST), and condition extraction for downstream ES queries.
package traceql

import (
	"fmt"
	"strings"
)

// ═══════════════════════════════════════════════════
// Node Types
// ═══════════════════════════════════════════════════

// NodeType classifies AST node categories.
type NodeType int

const (
	NodeSpanFilter        NodeType = iota // { conditions }
	NodeStructural                        // &>>, >>, >, ~, !>, !>>
	NodePipeline                          // |
	NodeSelect                            // select(fields...)
	NodeMetrics                           // rate(), quantile_over_time(), histogram_over_time()
	NodeOr                                // ||
	NodeAnd                               // &&  (implicit inside span filter)
	NodeComparison                        // key op value
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

// SpanFilter represents a span selector with mixed AND/OR conditions.
//
//	Conditions: AND-ed flat conditions (e.g., resource.service.name="tapm-api")
//	OrGroups:   parenthesized OR groups inside the span filter, AND-ed with Conditions.
//	            Each element of OrGroups is an independent OR group.
//	            Each OR group contains branches that are OR-ed together.
//	            Each branch contains conditions that are AND-ed together.
//
// Example: {(kind="internal" || kind="server") && resource.service.name="tapm-api"}
//
//	Conditions: [{resource.service.name = "tapm-api"}]
//	OrGroups:   [[[{kind = "internal"}], [{kind = "server"}]]]
type SpanFilter struct {
	Conditions []Condition     // AND conditions (mutually AND-ed)
	OrGroups   [][][]Condition // Parenthesized OR groups, AND-ed with Conditions.
}

func (s *SpanFilter) Type() NodeType { return NodeSpanFilter }
func (s *SpanFilter) String() string {
	var parts []string
	for _, c := range s.Conditions {
		parts = append(parts, c.String())
	}
	for _, orGroup := range s.OrGroups {
		var orParts []string
		for _, branch := range orGroup {
			var branchParts []string
			for _, c := range branch {
				branchParts = append(branchParts, c.String())
			}
			orParts = append(orParts, strings.Join(branchParts, " && "))
		}
		parts = append(parts, "("+strings.Join(orParts, " || ")+")")
	}
	return "{ " + strings.Join(parts, " && ") + " }"
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

// IsIntrinsic returns true if the condition references a built-in field
// (not a user-defined attribute). Accepts unscoped ("") and scoped ("span"/"trace")
// forms for compatibility with Grafana Explore queries (e.g. {span:kind=server}).
func (c Condition) IsIntrinsic() bool {
	switch c.Key {
	// span-scoped intrinsics (also work unscoped).
	case IntrinsicName, IntrinsicStatus, IntrinsicKind, IntrinsicDuration,
		IntrinsicNestedSetParent, IntrinsicNestedSetLeft, IntrinsicNestedSetRight:
		return c.Scope == "" || c.Scope == "span"
	// trace-scoped intrinsics.
	case IntrinsicRootName, IntrinsicRootServiceName, IntrinsicTraceDuration:
		return c.Scope == "" || c.Scope == "trace"
	// span:id / span:spanID -> spanId (ES-pushable via AttributeResolver).
	case "id", "spanID", IntrinsicParentID, "parentId":
		return c.Scope == "" || c.Scope == "span"
	// trace:id / trace:traceID -> traceID.
	case "traceID":
		return c.Scope == "" || c.Scope == "trace"
	// statusMessage (status.message).
	case IntrinsicStatusMessage:
		return c.Scope == "" || c.Scope == "span"
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

// ── Metrics Pipeline Stages ──────────────────────

// CountStage represents | count() pipeline stage.
type CountStage struct {
	By string // optional by(field) modifier, empty if not specified
}

func (cs *CountStage) stageType() string { return "count" }

// String returns a human-readable representation.
func (cs *CountStage) String() string {
	if cs.By != "" {
		return fmt.Sprintf("count() by(%s)", cs.By)
	}
	return "count()"
}

// MetricsFunc identifies the type of a metrics pipeline stage.
type MetricsFunc string

const (
	MetricsRate             MetricsFunc = "rate"
	MetricsQuantileOverTime MetricsFunc = "quantile_over_time"
	MetricsHistogramOverTime MetricsFunc = "histogram_over_time"
)

// MetricsStage represents a metrics pipeline stage:
//
//	rate()
//	quantile_over_time(duration, 0.5, 0.95, 0.99)
//	histogram_over_time(duration)
//
// with optional:
//   - by(label1, label2) — group-by labels
//   - with(sample=true)  — sample hint
type MetricsStage struct {
	Function    MetricsFunc // rate / quantile_over_time / histogram_over_time
	Field       string      // the intrinsic field (e.g., "duration")
	Percentiles []float64   // for quantile_over_time
	ByLabels    []string    // group-by labels from by(...)
	Sample      bool        // with(sample=true)
}

func (m *MetricsStage) stageType() string { return "metrics" }
func (m *MetricsStage) Type() NodeType    { return NodeMetrics }
func (m *MetricsStage) String() string {
	var sb strings.Builder
	sb.WriteString(string(m.Function))
	sb.WriteString("(")
	if m.Field != "" {
		sb.WriteString(m.Field)
		for _, p := range m.Percentiles {
			sb.WriteString(", ")
			sb.WriteString(fmt.Sprintf("%g", p))
		}
	}
	sb.WriteString(")")
	if len(m.ByLabels) > 0 {
		sb.WriteString(" by(")
		sb.WriteString(strings.Join(m.ByLabels, ", "))
		sb.WriteString(")")
	}
	if m.Sample {
		sb.WriteString(" with(sample=true)")
	}
	return sb.String()
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

// Intrinsic field keys shared across the traceql package.
const (
	IntrinsicName              = "name"
	IntrinsicStatus            = "status"
	IntrinsicStatusMessage     = "status.message"
	IntrinsicKind              = "kind"
	IntrinsicDuration          = "duration"
	IntrinsicNestedSetParent   = "nestedSetParent"
	IntrinsicNestedSetLeft     = "nestedSetLeft"
	IntrinsicNestedSetRight    = "nestedSetRight"
	IntrinsicParentID         = "parentID"
	IntrinsicRootName          = "rootName"
	IntrinsicRootServiceName   = "rootServiceName"
	IntrinsicTraceDuration     = "traceDuration"
)

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

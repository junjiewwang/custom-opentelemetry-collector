// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package traceql

import (
	"time"
)

// ═══════════════════════════════════════════════════
// Execution Plan
// ═══════════════════════════════════════════════════

// ExecutionPlan holds the result of the query planner:
// what can be pushed down to ES, and what needs post-processing.
type ExecutionPlan struct {
	// ── ES-pushable conditions ──
	// Tags are AND conditions that can be pushed as term queries.
	// Key format: "service.name", "http.method" (ES will search both attributes and resource).
	Tags map[string]string

	// TagsOr are OR-grouped conditions for ES bool.should.
	TagsOr []map[string]string

	// ServiceName is extracted from resource.service.name for dedicated ES field.
	ServiceName string

	// OperationName is extracted from name for dedicated ES field.
	OperationName string

	// SpanKind is extracted from kind condition for dedicated ES field.
	SpanKind string

	// Status is extracted from status condition for dedicated ES field.
	Status string

	// IsRoot indicates the query filters for root spans (nestedSetParent<0 or parentSpanId="").
	IsRoot bool

	// MinDuration / MaxDuration extracted from duration conditions.
	MinDuration time.Duration
	MaxDuration time.Duration

	// ── Post-processing indicators ──
	// HasStructural means the query contains &>>, >>, >, ~ operators
	// and requires in-memory structural matching after ES search.
	HasStructural bool

	// SelectFields from | select() pipeline stage.
	SelectFields []string

	// FullAST is the complete parsed AST for post-processing phases.
	FullAST Expr
}

// ═══════════════════════════════════════════════════
// Planner — Extract Pushdown Conditions from AST
// ═══════════════════════════════════════════════════

// Plan analyzes the AST and produces an ExecutionPlan.
// It extracts all pushable conditions from any span filters found in the AST,
// and marks whether structural post-processing is needed.
func Plan(ast Expr) *ExecutionPlan {
	if ast == nil {
		return &ExecutionPlan{}
	}

	plan := &ExecutionPlan{
		Tags:    make(map[string]string),
		FullAST: ast,
	}

	// Walk the AST and extract conditions.
	plan.extract(ast)

	return plan
}

// extract recursively walks the AST and populates the plan.
func (p *ExecutionPlan) extract(expr Expr) {
	if expr == nil {
		return
	}

	switch e := expr.(type) {
	case *SpanFilter:
		p.extractFromSpanFilter(e)

	case *OrExpr:
		// For OR expressions, extract conditions from both sides.
		// The overall strategy: collect all leaf SpanFilter conditions
		// and push them as widened (should) conditions for ES candidate fetching.
		p.extractOrConditions(e)

	case *StructuralExpr:
		p.HasStructural = true
		// Extract pushable conditions from both sides of structural expr.
		// These become widened (should) conditions for ES — exact structural
		// matching happens in post-processing.
		p.extract(e.Left)
		p.extract(e.Right)

	case *PipelineExpr:
		p.extract(e.Input)
		for _, stage := range e.Stages {
			if sel, ok := stage.(*SelectStage); ok {
				p.SelectFields = sel.Fields
			}
		}

	case *TrueExpr:
		// No-op.
	}
}

// extractFromSpanFilter extracts pushable conditions from a span filter.
func (p *ExecutionPlan) extractFromSpanFilter(sf *SpanFilter) {
	for _, cond := range sf.Conditions {
		p.extractCondition(cond)
	}
}

// extractCondition maps a single condition to the appropriate plan field.
func (p *ExecutionPlan) extractCondition(cond Condition) {
	// Only exact-match (=) conditions are pushable to ES term queries.
	// Range conditions are handled separately for duration.
	key := cond.Key
	valStr := condValueToString(cond.Value)

	switch {
	// ── Intrinsic: service.name (usually resource-scoped) ──
	case key == "service.name" && (cond.Scope == "resource" || cond.Scope == ""):
		if cond.Operator == "=" && valStr != "" {
			p.ServiceName = valStr
		}

	// ── Intrinsic: name (operation name) ──
	case key == "name" && cond.Scope == "" && cond.IsIntrinsic():
		if cond.Operator == "=" && valStr != "" {
			p.OperationName = valStr
		}

	// ── Intrinsic: kind ──
	case key == "kind" && cond.Scope == "" && cond.IsIntrinsic():
		if cond.Operator == "=" && valStr != "" {
			p.SpanKind = valStr
		}

	// ── Intrinsic: status ──
	case key == "status" && cond.Scope == "" && cond.IsIntrinsic():
		if cond.Operator == "=" && valStr != "" {
			p.Status = valStr
		}

	// ── Intrinsic: nestedSetParent < 0 (root span) ──
	case key == "nestedSetParent" && cond.Scope == "":
		if cond.Operator == "<" {
			if n, ok := cond.Value.(int64); ok && n <= 0 {
				p.IsRoot = true
			}
		}

	// ── Intrinsic: duration ──
	case key == "duration" && cond.Scope == "":
		if d, ok := cond.Value.(time.Duration); ok {
			switch cond.Operator {
			case ">", ">=":
				p.MinDuration = d
			case "<", "<=":
				p.MaxDuration = d
			}
		}

	// ── Generic attribute conditions (pushable as Tags) ──
	default:
		if cond.Operator == "=" && valStr != "" {
			p.Tags[key] = valStr
		}
	}
}

// extractOrConditions handles OR expressions.
// Strategy: if it's a flat OR of simple span filters, produce TagsOr groups.
// Otherwise, extract all conditions as broadened filters for candidate search.
func (p *ExecutionPlan) extractOrConditions(or *OrExpr) {
	groups := flattenOrGroups(or)
	if groups == nil {
		// Complex OR — just extract from both sides for widened search.
		p.extract(or.Left)
		p.extract(or.Right)
		return
	}

	// All leaf nodes are SpanFilters — produce OR groups.
	for _, sf := range groups {
		group := make(map[string]string)
		for _, cond := range sf.Conditions {
			if cond.Operator == "=" {
				valStr := condValueToString(cond.Value)
				if valStr != "" {
					group[cond.Key] = valStr
				}
			}
		}
		if len(group) > 0 {
			p.TagsOr = append(p.TagsOr, group)
		}
	}
}

// flattenOrGroups collects all SpanFilter leaves from a tree of OrExpr nodes.
// Returns nil if any leaf is not a SpanFilter (complex OR).
func flattenOrGroups(expr Expr) []*SpanFilter {
	switch e := expr.(type) {
	case *SpanFilter:
		return []*SpanFilter{e}
	case *OrExpr:
		left := flattenOrGroups(e.Left)
		if left == nil {
			return nil
		}
		right := flattenOrGroups(e.Right)
		if right == nil {
			return nil
		}
		return append(left, right...)
	default:
		return nil
	}
}

// ═══════════════════════════════════════════════════
// Helpers
// ═══════════════════════════════════════════════════

// condValueToString converts a condition value to its string representation.
func condValueToString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case int64:
		return ""
	case float64:
		return ""
	case bool:
		return ""
	case time.Duration:
		return ""
	default:
		return ""
	}
}

// IsAdvancedQuery returns true if the raw TraceQL string contains syntax
// that requires the new parser (structural ops, pipeline, etc).
func IsAdvancedQuery(raw string) bool {
	// Quick heuristic checks before full parsing.
	if raw == "" || raw == "{}" {
		return false
	}
	// Check for structural operators, pipeline, select, or multi-brace patterns.
	for _, marker := range []string{"&>>", ">>", "| select", "| count", "| rate", "nestedSetParent"} {
		if containsUnquoted(raw, marker) {
			return true
		}
	}
	// Check for multiple span selectors (e.g., {A} >> {B}).
	braceCount := 0
	for _, ch := range raw {
		if ch == '{' {
			braceCount++
		}
	}
	if braceCount > 1 {
		return true
	}
	return false
}

// containsUnquoted checks if marker appears in s outside of quoted strings.
func containsUnquoted(s, marker string) bool {
	inQuote := false
	quoteChar := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' || ch == '\'' {
			if !inQuote {
				inQuote = true
				quoteChar = ch
			} else if ch == quoteChar {
				inQuote = false
			}
			continue
		}
		if !inQuote && i+len(marker) <= len(s) && s[i:i+len(marker)] == marker {
			return true
		}
	}
	return false
}

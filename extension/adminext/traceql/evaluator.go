// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package traceql

import "regexp"

// ═══════════════════════════════════════════════════
// Span Data Model for TraceQL Evaluation
// ═══════════════════════════════════════════════════

// SpanData is a minimal span representation used by the TraceQL evaluator.
// Callers convert their native span objects into this struct before evaluation.
type SpanData struct {
	SpanID        string            // unique span identifier
	ParentSpanID  string            // empty for root spans
	Name          string            // operation name
	Kind          string            // "client", "server", "internal", "producer", "consumer"
	ServiceName   string            // resource.service.name
	StatusCode    string            // "ok", "error", "unset"
	StatusMessage string            // optional status detail
	StartUnixNano int64             // start timestamp in nanoseconds
	EndUnixNano   int64             // end timestamp in nanoseconds
	DurationNano  int64             // duration in nanoseconds
	Attributes    map[string]string // span attributes
	Resource      map[string]string // resource attributes
}

// ═══════════════════════════════════════════════════
// Span Tree — parentId-based span hierarchy
// ═══════════════════════════════════════════════════

// SpanTree represents a tree of spans for structural relationship evaluation.
type SpanTree struct {
	spans    []SpanData                // all spans in the trace
	byID     map[string]*SpanData      // spanID → span lookup
	children map[string][]*SpanData     // parentSpanID → child spans
	rootSet  []*SpanData               // spans with empty ParentSpanID
}

// BuildSpanTree constructs a SpanTree from a slice of SpanData.
func BuildSpanTree(spans []SpanData) *SpanTree {
	t := &SpanTree{
		spans:    spans,
		byID:     make(map[string]*SpanData, len(spans)),
		children: make(map[string][]*SpanData),
	}

	for i := range spans {
		span := &spans[i]
		t.byID[span.SpanID] = span
		if span.ParentSpanID == "" {
			t.rootSet = append(t.rootSet, span)
		} else {
			t.children[span.ParentSpanID] = append(t.children[span.ParentSpanID], span)
		}
	}

	return t
}

// GetSpan returns the span with the given ID, or nil if not found.
func (t *SpanTree) GetSpan(id string) *SpanData {
	return t.byID[id]
}

// Children returns the direct children of the given span.
func (t *SpanTree) Children(parentID string) []*SpanData {
	return t.children[parentID]
}

// IsDescendant returns true if 'descendant' is in the ancestry subtree of 'ancestor'.
// Walks up from descendant to root checking parentSpanId chain.
func (t *SpanTree) IsDescendant(ancestorID, descendantID string) bool {
	if ancestorID == descendantID {
		return false // a span is not its own descendant
	}
	current := t.byID[descendantID]
	if current == nil {
		return false
	}
	for current.ParentSpanID != "" {
		if current.ParentSpanID == ancestorID {
			return true
		}
		current = t.byID[current.ParentSpanID]
		if current == nil {
			return false
		}
	}
	return false
}

// IsAncestor returns true if 'ancestor' is an ancestor of 'descendant'.
func (t *SpanTree) IsAncestor(ancestorID, descendantID string) bool {
	return t.IsDescendant(ancestorID, descendantID)
}

// IsChild returns true if 'parent' is the direct parent of 'child'.
func (t *SpanTree) IsChild(parentID, childID string) bool {
	child := t.byID[childID]
	if child == nil {
		return false
	}
	return child.ParentSpanID == parentID
}

// IsSibling returns true if both spans share the same parent.
func (t *SpanTree) IsSibling(aID, bID string) bool {
	a := t.byID[aID]
	b := t.byID[bID]
	if a == nil || b == nil {
		return false
	}
	if a.ParentSpanID == "" && b.ParentSpanID == "" {
		return true // both are root spans
	}
	return a.ParentSpanID == b.ParentSpanID
}

// ═══════════════════════════════════════════════════
// Condition Matching
// ═══════════════════════════════════════════════════

// MatchSpanFilter checks whether a span matches all conditions in a SpanFilter.
// Conditions are AND-ed together. OrGroups are OR-ed within each group, AND-ed across groups.
// Returns true if the span satisfies the entire filter.
func MatchSpanFilter(filter *SpanFilter, span *SpanData) bool {
	if filter == nil || span == nil {
		return true
	}

	// All AND conditions must match.
	for _, cond := range filter.Conditions {
		if !matchCondition(cond, span) {
			return false
		}
	}

	// Each OR group must have at least one matching branch.
	for _, orGroup := range filter.OrGroups {
		groupMatched := false
		for _, branch := range orGroup {
			branchMatched := true
			for _, cond := range branch {
				if !matchCondition(cond, span) {
					branchMatched = false
					break
				}
			}
			if branchMatched {
				groupMatched = true
				break
			}
		}
		if !groupMatched {
			return false
		}
	}

	return true
}

// matchCondition checks a single Condition against a span.
func matchCondition(cond Condition, span *SpanData) bool {
	key := cond.Key

	switch {
	// ── Intrinsic: name ──
	case key == "name" && cond.IsIntrinsic():
		return matchStringValue(cond.Operator, span.Name, cond.Value)

	// ── Intrinsic: kind ──
	case key == "kind" && cond.IsIntrinsic():
		return matchStringValue(cond.Operator, span.Kind, cond.Value)

	// ── Intrinsic: status ──
	case key == "status" && cond.IsIntrinsic():
		return matchStringValue(cond.Operator, span.StatusCode, cond.Value)

	// ── Intrinsic: duration ──
	case key == "duration" && cond.IsIntrinsic():
		return matchNumericValue(cond.Operator, span.DurationNano, cond.Value)

	// ── Intrinsic: nestedSetParent (Tempo internal, maps to root span) ──
	// nestedSetParent < 0  →  span has no parent (root span).
	case key == "nestedSetParent" && cond.Scope == "":
		if cond.Operator == "<" {
			if n, ok := cond.Value.(int64); ok && n <= 0 {
				return span.ParentSpanID == ""
			}
		}
		return false

	// ── service.name (resource-scoped) ──
	case key == "service.name":
		return matchStringValue(cond.Operator, span.ServiceName, cond.Value)

	// ── Intrinsic: status.message (nested intrinsic, Sprint 3) ──
	case key == "status.message" && cond.Scope == "":
		if cond.Operator == "!=" && cond.Value == nil {
			return span.StatusMessage != ""
		}
		return matchStringValue(cond.Operator, span.StatusMessage, cond.Value)

	// ── Generic attribute/resource (check both) ──
	default:
		// Check attributes first, then resource.
		if val, ok := span.Attributes[key]; ok {
			return matchStringValue(cond.Operator, val, cond.Value)
		}
		if val, ok := span.Resource[key]; ok {
			return matchStringValue(cond.Operator, val, cond.Value)
		}
		return false
	}
}

// matchStringValue compares a string field against a condition value.
// Supports =, !=, =~ (regex match), and !~ (regex not match) operators.
func matchStringValue(op string, fieldValue string, condValue any) bool {
	valStr, ok := condValue.(string)
	if !ok {
		return false
	}

	switch op {
	case "=":
		return fieldValue == valStr
	case "!=":
		return fieldValue != valStr
	case "=~":
		matched, err := regexp.MatchString(valStr, fieldValue)
		return err == nil && matched
	case "!~":
		matched, err := regexp.MatchString(valStr, fieldValue)
		return err != nil || !matched
	default:
		return false
	}
}

// matchNumericValue compares a numeric field (int64 nanos) against a condition value.
func matchNumericValue(op string, fieldValue int64, condValue any) bool {
	var condNum int64
	switch v := condValue.(type) {
	case int64:
		condNum = v
	case float64:
		condNum = int64(v)
	default:
		return false
	}

	switch op {
	case "=":
		return fieldValue == condNum
	case "!=":
		return fieldValue != condNum
	case "<":
		return fieldValue < condNum
	case ">":
		return fieldValue > condNum
	case "<=":
		return fieldValue <= condNum
	case ">=":
		return fieldValue >= condNum
	default:
		return false
	}
}

// ═══════════════════════════════════════════════════
// Structural Matching Results
// ═══════════════════════════════════════════════════

// MatchType distinguishes the origin of a StructuralMatch.
type MatchType int

const (
	// MatchTypeStructural indicates the match is from a structural relationship (e.g., &>>).
	MatchTypeStructural MatchType = iota
	// MatchTypeFilter indicates the match is from a plain SpanFilter in an OR branch.
	// In this case LeftSpanID == RightSpanID (self-match).
	MatchTypeFilter
)

// StructuralMatch represents a matching result from the structural evaluator.
// For structural matches: LeftSpanID and RightSpanID form a pair satisfying the relationship.
// For filter matches: LeftSpanID == RightSpanID (the single span that matched the filter).
type StructuralMatch struct {
	LeftSpanID  string    // span matching the left filter (or the matched span for filter type)
	RightSpanID string    // span matching the right filter (or same as LeftSpanID for filter type)
	Type        MatchType // origin of this match
}

// ═══════════════════════════════════════════════════
// Structural Evaluator
// ═══════════════════════════════════════════════════

// EvaluateStructural evaluates a StructuralExpr against a span tree.
// Returns all (left, right) span pairs that satisfy the structural relationship.
func EvaluateStructural(expr *StructuralExpr, tree *SpanTree) []StructuralMatch {
	if expr == nil || tree == nil {
		return nil
	}

	// Find all spans matching the left filter.
	var leftSpans []*SpanData
	leftFilter, leftIsFilter := expr.Left.(*SpanFilter)
	if leftIsFilter {
		for i := range tree.spans {
			if MatchSpanFilter(leftFilter, &tree.spans[i]) {
				leftSpans = append(leftSpans, &tree.spans[i])
			}
		}
	}

	// Find all spans matching the right filter.
	var rightSpans []*SpanData
	rightFilter, rightIsFilter := expr.Right.(*SpanFilter)
	if rightIsFilter {
		for i := range tree.spans {
			if MatchSpanFilter(rightFilter, &tree.spans[i]) {
				rightSpans = append(rightSpans, &tree.spans[i])
			}
		}
	}

	// Evaluate structural relationship for each pair.
	var matches []StructuralMatch
	for _, left := range leftSpans {
		for _, right := range rightSpans {
			if checkStructuralRelation(expr.Operator, left.SpanID, right.SpanID, tree) {
				matches = append(matches, StructuralMatch{
					LeftSpanID:  left.SpanID,
					RightSpanID: right.SpanID,
					Type:        MatchTypeStructural,
				})
			}
		}
	}

	return matches
}

// checkStructuralRelation evaluates a single structural operator between two spans.
func checkStructuralRelation(op string, leftID, rightID string, tree *SpanTree) bool {
	switch op {
	case "&>>": // left is ancestor of right
		return tree.IsAncestor(leftID, rightID)
	case ">>": // left has right as descendant
		return tree.IsDescendant(leftID, rightID)
	case ">": // left is direct parent of right
		return tree.IsChild(leftID, rightID)
	case "~": // left and right are siblings
		return tree.IsSibling(leftID, rightID)
	case "!>": // left is NOT direct parent of right
		return !tree.IsChild(leftID, rightID)
	case "!>>": // left is NOT ancestor of right (alias: right is NOT descendant of left)
		return !tree.IsAncestor(leftID, rightID)
	default:
		return false
	}
}

// ═══════════════════════════════════════════════════
// Trace-Level Evaluation
// ═══════════════════════════════════════════════════

// TraceResult holds the structural evaluation result for a single trace.
type TraceResult struct {
	TraceID       string
	Spans         []SpanData // all spans in the trace
	Matches       []StructuralMatch
	HasMatch      bool // true if at least one structural pair matched
}

// EvaluateTraceStructural evaluates a structural expression against a single trace.
// Supports OR combinations: (StructuralExpr) || (SpanFilter) — if either branch matches,
// the trace is considered a match.
// Returns nil if the trace has no matching spans/pairs.
func EvaluateTraceStructural(ast Expr, spans []SpanData) *TraceResult {
	if ast == nil || len(spans) == 0 {
		return nil
	}

	tree := BuildSpanTree(spans)

	// Unwrap PipelineExpr to get the core expression.
	coreExpr := unwrapPipeline(ast)

	// Handle OrExpr at the top level: evaluate each branch independently.
	// A trace matches if ANY branch matches.
	matches := evaluateExprStructural(coreExpr, tree)
	if len(matches) == 0 {
		return nil
	}

	return &TraceResult{
		Spans:    spans,
		Matches:  matches,
		HasMatch: true,
	}
}

// evaluateExprStructural recursively evaluates an expression tree for structural matching.
// For OrExpr: union results from both branches (either branch matching is sufficient).
// For StructuralExpr: evaluate the structural relationship.
// For SpanFilter: match individual spans (returns synthetic self-matches for matched spans).
func evaluateExprStructural(expr Expr, tree *SpanTree) []StructuralMatch {
	if expr == nil {
		return nil
	}

	switch e := expr.(type) {
	case *StructuralExpr:
		return EvaluateStructural(e, tree)

	case *OrExpr:
		// Evaluate both branches — union the results.
		leftMatches := evaluateExprStructural(e.Left, tree)
		rightMatches := evaluateExprStructural(e.Right, tree)
		return append(leftMatches, rightMatches...)

	case *SpanFilter:
		// For a plain SpanFilter in an OR branch, find spans that match it.
		// Return MatchTypeFilter entries (LeftSpanID == RightSpanID) to indicate
		// which spans satisfied this non-structural branch.
		var matches []StructuralMatch
		for i := range tree.spans {
			if MatchSpanFilter(e, &tree.spans[i]) {
				matches = append(matches, StructuralMatch{
					LeftSpanID:  tree.spans[i].SpanID,
					RightSpanID: tree.spans[i].SpanID,
					Type:        MatchTypeFilter,
				})
			}
		}
		return matches

	case *PipelineExpr:
		return evaluateExprStructural(e.Input, tree)

	default:
		return nil
	}
}

// unwrapPipeline extracts the Input expression from a PipelineExpr (strips select/coalesce stages).
func unwrapPipeline(expr Expr) Expr {
	if p, ok := expr.(*PipelineExpr); ok {
		return p.Input
	}
	return expr
}

// SetTraceResultTraceID sets the trace ID on a TraceResult (called by the handler layer).
func (r *TraceResult) SetTraceResultTraceID(traceID string) {
	r.TraceID = traceID
}

// findStructuralExpr walks the AST to find the first StructuralExpr.
// Handles wrapping by PipelineExpr and OrExpr (e.g., Grafana queries).
func findStructuralExpr(expr Expr) *StructuralExpr {
	if expr == nil {
		return nil
	}

	switch e := expr.(type) {
	case *StructuralExpr:
		return e
	case *PipelineExpr:
		return findStructuralExpr(e.Input)
	case *OrExpr:
		// Check left first, then right.
		if s := findStructuralExpr(e.Left); s != nil {
			return s
		}
		return findStructuralExpr(e.Right)
	default:
		return nil
	}
}

// HasStructuralExpr checks if the AST contains any structural expression.
func HasStructuralExpr(expr Expr) bool {
	return findStructuralExpr(expr) != nil
}

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package traceql

import (
	"regexp"
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
	// Outer groups are AND-ed together (each becomes a separate bool.should block).
	// Within each group, maps are OR-ed (all in one bool.should with min_should_match=1).
	// Within each map, entries are AND-ed.
	//
	// Example: {(kind=server || kind=client) && (status=error || status=ok)}
	// TagsOr: [[{kind:server},{kind:client}], [{status:error},{status:ok}]]
	// ES: must=[should[server,client], should[error,ok]]
	TagsOr [][]map[string]string

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

	// EventTags are conditions scoped to the event scope (e.g., event:name = "exception").
	// These require ES nested queries on the events field.
	EventTags map[string]string

	// EventTagsOr holds parenthesized OR groups with event-scoped conditions.
	EventTagsOr [][][]map[string]string

	// ── Post-processing indicators ──
	// HasStructural means the query contains &>>, >>, >, ~ operators
	// and requires in-memory structural matching after ES search.
	HasStructural bool

	// ── Negation / Existence / Regex filters (Sprint 2) ──
	// TagsNot: != value conditions → ES must_not term.
	TagsNot map[string]string
	// TagsExists: != nil conditions → ES exists query.
	TagsExists []string
	// TagsRegex: =~ regex conditions → ES regexp query.
	TagsRegex map[string]string

	// SelectFields from | select() pipeline stage.
	SelectFields []string

	// MetricsStage from | rate() / quantile_over_time() / histogram_over_time() pipeline stage.
	MetricsStage *MetricsStage

	// HasMetrics is true when the query contains a metrics pipeline stage.
	HasMetrics bool

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

	// For structural queries, the ES search is only used for candidate fetching.
	// Conditions extracted from DIFFERENT span filters (e.g., left and right of &>>)
	// should NOT be AND-ed together in ES because they refer to different spans.
	// Relax intrinsic filters that came from different structural arms to avoid
	// filtering out valid candidate traces at the ES level.
	if plan.HasStructural {
		plan.relaxStructuralConditions()
	}

	return plan
}

// relaxStructuralConditions loosens the ES search conditions for structural queries.
// For structural queries, ES search serves only as a broad candidate filter — the
// exact structural matching is performed in-memory during post-processing.
//
// The problem: when a query like `{nestedSetParent<0} &>> {kind=server}` is planned,
// both IsRoot=true and SpanKind="server" are extracted and AND-ed into the ES query.
// This means ES only finds spans that are BOTH root AND kind=server, but these
// conditions target DIFFERENT spans in the structural relationship.
//
// Solution: for structural queries, clear SpanKind and Status since they likely
// come from the non-root side of the structural expression. Keep IsRoot because
// it helps narrow candidates (finding traces that have root spans).
func (p *ExecutionPlan) relaxStructuralConditions() {
	// Clear conditions that likely came from the non-root filter in a structural expr.
	// These would incorrectly narrow the ES search when AND-ed with IsRoot.
	p.SpanKind = ""
	p.Status = ""
	// Keep IsRoot — it helps find trace candidates by identifying root span presence.
	// Keep ServiceName/OperationName — if present, they usually come from the same
	// filter as IsRoot (e.g., {resource.service.name="X" && nestedSetParent<0}).
	// Keep Tags/TagsOr — they may still be useful as broadened candidate filters.
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
			if ms, ok := stage.(*MetricsStage); ok {
				p.HasMetrics = true
				p.MetricsStage = ms
			}
		}

	case *TrueExpr:
		// No-op.
	}
}

// extractFromSpanFilter extracts pushable conditions from a span filter.
func (p *ExecutionPlan) extractFromSpanFilter(sf *SpanFilter) {
	// Extract AND conditions (top-level).
	for _, cond := range sf.Conditions {
		p.extractCondition(cond)
	}

	// Extract parenthesized OR groups.
	// Each OrGroup is a [][]Condition — becomes its own TagsOr group.
	for _, orGroup := range sf.OrGroups {
		var groupMaps []map[string]string
		for _, branch := range orGroup {
			branchMap := make(map[string]string)
			for _, cond := range branch {
				if cond.Operator == "=" {
					valStr := condValueToString(cond.Value)
					if valStr != "" {
						branchMap[scopedKey(cond.Scope, cond.Key)] = valStr
					}
				}
			}
			if len(branchMap) > 0 {
				groupMaps = append(groupMaps, branchMap)
			}
		}
		if len(groupMaps) > 0 {
			p.TagsOr = append(p.TagsOr, groupMaps)
		}
	}
}

// extractCondition maps a single condition to the appropriate plan field.
// Supports =, !=, != nil, and =~ operators (Sprint 2).
func (p *ExecutionPlan) extractCondition(cond Condition) {
	key := cond.Key
	valStr := condValueToString(cond.Value)
	isNil := cond.Value == nil

	switch {
	// ── Intrinsic: service.name (usually resource-scoped) ──
	case key == "service.name" && (cond.Scope == "resource" || cond.Scope == ""):
		switch cond.Operator {
		case "=":
			if valStr != "" {
				p.ServiceName = valStr
			}
		case "!=":
			if isNil {
				p.addTagExists("service.name")
			} else if valStr != "" {
				p.addTagNot("service.name", valStr)
			}
		case "=~":
			if valStr != "" {
				p.addTagRegex("service.name", valStr)
			}
		}

	// ── Intrinsic: name (operation name) ──
	case key == "name" && cond.Scope == "" && cond.IsIntrinsic():
		switch cond.Operator {
		case "=":
			if valStr != "" {
				p.OperationName = valStr
			}
		case "!=":
			if isNil {
				p.addTagExists("name")
			} else if valStr != "" {
				p.addTagNot("name", valStr)
			}
		case "=~":
			if valStr != "" {
				p.addTagRegex("name", valStr)
			}
		}

	// ── Intrinsic: kind ──
	case key == "kind" && cond.Scope == "" && cond.IsIntrinsic():
		switch cond.Operator {
		case "=":
			if valStr != "" {
				p.SpanKind = valStr
			}
		case "!=":
			if isNil {
				p.addTagExists("kind")
			} else if valStr != "" {
				p.addTagNot("kind", valStr)
			}
		case "=~":
			if valStr != "" {
				p.addTagRegex("kind", valStr)
			}
		}

	// ── Intrinsic: status ──
	case key == "status" && cond.Scope == "" && cond.IsIntrinsic():
		switch cond.Operator {
		case "=":
			if valStr != "" {
				p.Status = valStr
			}
		case "!=":
			if isNil {
				p.addTagExists("status")
			} else if valStr != "" {
				p.addTagNot("status", valStr)
			}
		case "=~":
			if valStr != "" {
				p.addTagRegex("status", valStr)
			}
		}

	// ── Intrinsic: nestedSetParent < 0 (root span) ──
	case key == "nestedSetParent" && cond.Scope == "":
		if cond.Operator == "<" {
			if n, ok := cond.Value.(int64); ok && n <= 0 {
				p.IsRoot = true
			}
		}

	// ── Intrinsic: duration (span or trace scope) ──
	case key == "duration" && (cond.Scope == "" || cond.Scope == "trace"):
		if d, ok := cond.Value.(time.Duration); ok {
			switch cond.Operator {
			case ">", ">=":
				p.MinDuration = d
			case "<", "<=":
				p.MaxDuration = d
			}
		}

	// ── Intrinsic: trace:rootName / trace:rootService ──
	case key == "rootName" && cond.Scope == "trace":
		if cond.Operator == "=" && valStr != "" {
			if p.Tags == nil {
				p.Tags = make(map[string]string)
			}
			p.Tags["rootName"] = valStr
		}
	case key == "rootService" && cond.Scope == "trace":
		if cond.Operator == "=" && valStr != "" {
			if p.Tags == nil {
				p.Tags = make(map[string]string)
			}
			p.Tags["rootService"] = valStr
		}

	// ── Generic attribute conditions (pushable as Tags/TagsNot/TagsExists/TagsRegex) ──
	default:
		if cond.Scope == "event" {
			if cond.Operator == "=" && valStr != "" {
				if p.EventTags == nil {
					p.EventTags = make(map[string]string)
				}
				p.EventTags[key] = valStr
			}
			return
		}
		// Preserve scope prefix so that AttributeResolver can correctly map
		// resource-scoped attributes to their ES field path.
		fullKey := scopedKey(cond.Scope, key)
		switch cond.Operator {
		case "=":
			if valStr != "" {
				p.Tags[fullKey] = valStr
			}
		case "!=":
			if isNil {
				p.addTagExists(fullKey)
			} else if valStr != "" {
				p.addTagNot(fullKey, valStr)
			}
		case "=~":
			if valStr != "" {
				p.addTagRegex(fullKey, valStr)
			}
		}
	}
}

// addTagNot records a != negation condition.
func (p *ExecutionPlan) addTagNot(key, val string) {
	if p.TagsNot == nil {
		p.TagsNot = make(map[string]string)
	}
	p.TagsNot[key] = val
}

// addTagExists records a != nil existence condition.
func (p *ExecutionPlan) addTagExists(key string) {
	p.TagsExists = append(p.TagsExists, key)
}

// addTagRegex records a =~ regex condition, with Compile pre-validation.
func (p *ExecutionPlan) addTagRegex(key, pattern string) {
	// Validate regex at plan time to fail early on invalid patterns.
	if _, err := regexp.Compile(pattern); err != nil {
		return // skip invalid regex silently (degraded filtering)
	}
	if p.TagsRegex == nil {
		p.TagsRegex = make(map[string]string)
	}
	p.TagsRegex[key] = pattern
}

// scopedKey returns "scope.key" if scope is non-empty, otherwise just "key".
// This ensures resource-scoped attributes (e.g. resource.app_id) are preserved
// with their scope prefix so that AttributeResolver can correctly map them
// to the ES field path (e.g. "resource.app_id" instead of "attributes.app_id").
func scopedKey(scope, key string) string {
	if scope != "" {
		return scope + "." + key
	}
	return key
}

// extractOrConditions handles OR expressions.
// Strategy: if it's a flat OR of simple span filters, produce a single TagsOr group.
// Otherwise, extract all conditions as broadened filters for candidate search.
func (p *ExecutionPlan) extractOrConditions(or *OrExpr) {
	groups := flattenOrGroups(or)
	if groups == nil {
		// Complex OR — just extract from both sides for widened search.
		p.extract(or.Left)
		p.extract(or.Right)
		return
	}

	// All leaf nodes are SpanFilters — produce one OR group.
	var orGroup []map[string]string
	for _, sf := range groups {
		m := make(map[string]string)
		for _, cond := range sf.Conditions {
			if cond.Operator == "=" {
				valStr := condValueToString(cond.Value)
				if valStr != "" {
					m[scopedKey(cond.Scope, cond.Key)] = valStr
				}
			}
		}
		if len(m) > 0 {
			orGroup = append(orGroup, m)
		}
	}
	if len(orGroup) > 0 {
		p.TagsOr = append(p.TagsOr, orGroup)
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

// Deprecated: IsAdvancedQuery is no longer used for routing in parseTempoSearchParams.
// All TraceQL queries now go through the unified AST parser (Parse → Plan).
// This function is retained only for backward compatibility with tests.
//
// IsAdvancedQuery returns true if the raw TraceQL string contains syntax
// that requires the new parser (structural ops, pipeline, parenthesized OR, etc).
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
	// Check for || inside a single span filter (parenthesized OR groups).
	// e.g., {(kind="internal" || kind="server") && resource.service.name="tapm-api"}
	if braceCount == 1 && containsUnquoted(raw, "||") {
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

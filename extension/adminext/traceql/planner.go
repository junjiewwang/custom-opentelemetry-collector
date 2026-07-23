// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package traceql

import (
	"fmt"
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

	// TagsNotOr are OR-grouped conditions with != (not-equal) operator for ES must_not+should.
	// Structure mirrors TagsOr: outer AND, middle OR, inner AND.
	TagsNotOr [][]map[string]string

	// TagsRegexOr are OR-grouped conditions with =~ (regex) operator for ES regexp+should.
	TagsRegexOr [][]map[string]string

	// ── Post-processing indicators ──
	// HasStructural means the query contains &>>, >>, >, ~ operators
	// and requires in-memory structural matching after ES search.
	HasStructural bool

	// ── Negation / Existence / Regex filters (Sprint 2) ──
	// TagsNot: != value conditions → ES must_not term.
	TagsNot map[string]string
	// TagsExists: != nil conditions → ES exists query.
	TagsExists    []string
	// TagsNotExists: = nil conditions → ES must_not exists query.
	TagsNotExists  []string
	// TagsRegex: =~ regex conditions → ES regexp query.
	TagsRegex map[string]string

	// ── Root span intrinsic filters (Sprint 3) ──
	// RootName filters by root span's name.
	RootName string
	// RootService filters by root span's serviceName.
	RootService string

	// SelectFields from | select() pipeline stage.
	SelectFields []string

	// HasCount indicates the query has a count() pipeline stage (Sprint 4).
	HasCount bool

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
// Solution: compute the intersection of root-side conditions across all OR branches.
// Only conditions that appear in ALL branches' root filters are safe to push to ES.
// This preserves useful narrowing (e.g., status=error when all branches require it)
// while avoiding over-filtering from non-root span conditions.
func (p *ExecutionPlan) relaxStructuralConditions() {
	// Compute safe-to-push conditions by intersecting root-side filters across OR branches.
	safe := computeSafeStructuralConditions(p.FullAST)

	// Only keep conditions that are confirmed safe across all branches.
	if !safe.IsRoot {
		p.IsRoot = false
	}
	if !safe.HasStatus {
		p.Status = ""
	}
	if !safe.HasSpanKind {
		p.SpanKind = ""
	}
	if !safe.HasServiceName {
		p.ServiceName = ""
	}
	if !safe.HasOperationName {
		p.OperationName = ""
	}
	if !safe.HasRootName {
		p.RootName = ""
	}
	if !safe.HasRootService {
		p.RootService = ""
	}
	// Tags from non-root filters could be incorrect — clear tags that aren't in safe set.
	if len(safe.SafeTags) == 0 {
		p.Tags = make(map[string]string)
	} else {
		for k := range p.Tags {
			if _, ok := safe.SafeTags[k]; !ok {
				delete(p.Tags, k)
			}
		}
	}
	// TagsOr are complex to intersect — clear them for structural queries
	// to avoid over-filtering.
	p.TagsOr = nil
}

// safeConditions represents conditions that are safe to push to ES for structural queries.
// A condition is "safe" if it appears in the root-side filter of ALL OR branches,
// meaning any matching trace must satisfy it regardless of which branch matches.
type safeConditions struct {
	IsRoot           bool
	HasStatus        bool
	Status           string
	HasSpanKind      bool
	SpanKind         string
	HasServiceName   bool
	ServiceName      string
	HasOperationName bool
	OperationName    string
	HasRootName      bool
	RootName         string
	HasRootService   bool
	RootService      string
	SafeTags         map[string]string
}

// computeSafeStructuralConditions analyzes the AST to find conditions that are common
// to the root-side filters of ALL OR branches. These can safely be pushed to ES
// without risking exclusion of valid candidate traces.
//
// For a query like:
//
//	({nestedSetParent<0 && status=error} &>> {status=error}) || ({nestedSetParent<0 && status=error})
//
// Both branches have root filters with nestedSetParent<0 and status=error,
// so IsRoot=true and Status="error" are safe to push to ES.
//
// For a query like:
//
//	({nestedSetParent<0} &>> {kind=server}) || ({nestedSetParent<0 && status=error})
//
// Only IsRoot is common — status=error is only in one branch, kind=server is non-root.
func computeSafeStructuralConditions(ast Expr) safeConditions {
	// Collect root-side filters from all OR branches.
	branches := collectRootFilters(unwrapPipelineForPlanner(ast))
	if len(branches) == 0 {
		// Fallback: no recognizable root filters found.
		// Only keep IsRoot as safe (conservative default).
		return safeConditions{IsRoot: true}
	}

	// Start with the first branch's conditions, then intersect with the rest.
	result := extractFilterConditions(branches[0])
	for i := 1; i < len(branches); i++ {
		other := extractFilterConditions(branches[i])
		result = intersectConditions(result, other)
	}

	return result
}

// collectRootFilters extracts the "root-side" SpanFilter from each OR branch.
// For a StructuralExpr, the root-side is the Left operand (typically has nestedSetParent<0).
// For a plain SpanFilter, it IS the root filter.
// For an OrExpr, recursively collect from both sides.
func collectRootFilters(expr Expr) []*SpanFilter {
	if expr == nil {
		return nil
	}

	switch e := expr.(type) {
	case *SpanFilter:
		return []*SpanFilter{e}

	case *StructuralExpr:
		// For structural expressions, the left side is the root-span filter.
		if leftFilter, ok := e.Left.(*SpanFilter); ok {
			return []*SpanFilter{leftFilter}
		}
		// Left could be a nested structural — recursively extract.
		return collectRootFilters(e.Left)

	case *OrExpr:
		left := collectRootFilters(e.Left)
		right := collectRootFilters(e.Right)
		if left == nil || right == nil {
			// If either branch can't provide a root filter, we can't safely intersect.
			return nil
		}
		return append(left, right...)

	case *PipelineExpr:
		return collectRootFilters(e.Input)

	default:
		return nil
	}
}

// extractFilterConditions extracts intrinsic conditions from a SpanFilter.
func extractFilterConditions(sf *SpanFilter) safeConditions {
	sc := safeConditions{
		SafeTags: make(map[string]string),
	}
	if sf == nil {
		return sc
	}

	for _, cond := range sf.Conditions {
		if cond.Operator != "=" {
			// Only equality conditions can be safely pushed as term queries.
			// Still handle special cases like nestedSetParent < 0.
			if cond.Key == "nestedSetParent" && cond.Operator == "<" {
				if n, ok := cond.Value.(int64); ok && n <= 0 {
					sc.IsRoot = true
				}
			}
			continue
		}

		valStr := condValueToString(cond.Value)
		key := cond.Key

		switch {
		case key == IntrinsicStatus && cond.IsIntrinsic():
			sc.HasStatus = true
			sc.Status = valStr
		case key == IntrinsicKind && cond.IsIntrinsic():
			sc.HasSpanKind = true
			sc.SpanKind = valStr
		case key == "service.name" && (cond.Scope == "resource" || cond.Scope == ""):
			sc.HasServiceName = true
			sc.ServiceName = valStr
		case key == IntrinsicName && cond.IsIntrinsic():
			sc.HasOperationName = true
			sc.OperationName = valStr
		case key == IntrinsicRootName && cond.Scope == "trace":
			sc.HasRootName = true
			sc.RootName = valStr
		case key == IntrinsicRootServiceName && cond.Scope == "trace":
			sc.HasRootService = true
			sc.RootService = valStr
		case key == IntrinsicNestedSetParent:
			// Skip — handled above for < operator.
		default:
			if valStr != "" {
				sc.SafeTags[scopedKey(cond.Scope, key)] = valStr
			}
		}
	}

	return sc
}

// intersectConditions returns the intersection of two safeConditions.
// A condition is retained only if it appears with the same value in both.
func intersectConditions(a, b safeConditions) safeConditions {
	result := safeConditions{
		SafeTags: make(map[string]string),
	}

	// IsRoot: keep only if both have it.
	result.IsRoot = a.IsRoot && b.IsRoot

	// Status: keep only if both have same value.
	if a.HasStatus && b.HasStatus && a.Status == b.Status {
		result.HasStatus = true
		result.Status = a.Status
	}

	// SpanKind: keep only if both have same value.
	if a.HasSpanKind && b.HasSpanKind && a.SpanKind == b.SpanKind {
		result.HasSpanKind = true
		result.SpanKind = a.SpanKind
	}

	// ServiceName: keep only if both have same value.
	if a.HasServiceName && b.HasServiceName && a.ServiceName == b.ServiceName {
		result.HasServiceName = true
		result.ServiceName = a.ServiceName
	}

	// OperationName: keep only if both have same value.
	if a.HasOperationName && b.HasOperationName && a.OperationName == b.OperationName {
		result.HasOperationName = true
		result.OperationName = a.OperationName
	}

	// RootName: keep only if both have same value.
	if a.HasRootName && b.HasRootName && a.RootName == b.RootName {
		result.HasRootName = true
		result.RootName = a.RootName
	}

	// RootService: keep only if both have same value.
	if a.HasRootService && b.HasRootService && a.RootService == b.RootService {
		result.HasRootService = true
		result.RootService = a.RootService
	}

	// Tags: keep only tags present in both with same value.
	for k, v := range a.SafeTags {
		if bv, ok := b.SafeTags[k]; ok && bv == v {
			result.SafeTags[k] = v
		}
	}

	return result
}

// unwrapPipelineForPlanner extracts the Input from a PipelineExpr (for planner use).
func unwrapPipelineForPlanner(expr Expr) Expr {
	if p, ok := expr.(*PipelineExpr); ok {
		return p.Input
	}
	return expr
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
		// Only extract from the LEFT side of structural expressions.
		// The left side is typically the root-span filter (e.g., {nestedSetParent<0 && status=error}).
		// The right side targets DIFFERENT spans (descendants/children), so its conditions
		// should NOT be mixed into the same ES query — they would incorrectly narrow results.
		// relaxStructuralConditions() will further refine what's safe to keep.
		p.extract(e.Left)

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
			if _, ok := stage.(*CountStage); ok {
				p.HasCount = true
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
	case key == IntrinsicName && (cond.Scope == "" || cond.Scope == "span") && cond.IsIntrinsic():
		switch cond.Operator {
		case "=":
			if valStr != "" {
				p.OperationName = valStr
			}
		case "!=":
			if isNil {
				p.addTagExists(IntrinsicName)
			} else if valStr != "" {
				p.addTagNot(IntrinsicName, valStr)
			}
		case "=~":
			if valStr != "" {
				p.addTagRegex(IntrinsicName, valStr)
			}
		}

	// ── Intrinsic: kind ──
	case key == IntrinsicKind && (cond.Scope == "" || cond.Scope == "span") && cond.IsIntrinsic():
		switch cond.Operator {
		case "=":
			if valStr != "" {
				p.SpanKind = valStr
			}
		case "!=":
			if isNil {
				p.addTagExists(IntrinsicKind)
			} else if valStr != "" {
				p.addTagNot(IntrinsicKind, valStr)
			}
		case "=~":
			if valStr != "" {
				p.addTagRegex(IntrinsicKind, valStr)
			}
		}

	// ── Intrinsic: status ──
	case key == IntrinsicStatus && (cond.Scope == "" || cond.Scope == "span") && cond.IsIntrinsic():
		switch cond.Operator {
		case "=":
			if valStr != "" {
				p.Status = valStr
			}
		case "!=":
			if isNil {
				p.addTagExists(IntrinsicStatus)
			} else if valStr != "" {
				p.addTagNot(IntrinsicStatus, valStr)
			}
		case "=~":
			if valStr != "" {
				p.addTagRegex(IntrinsicStatus, valStr)
			}
		}

	// ── Intrinsic: nestedSetParent < 0 (root span) ──
	case key == IntrinsicNestedSetParent && (cond.Scope == "" || cond.Scope == "span"):
		if cond.Operator == "<" {
			if n, ok := cond.Value.(int64); ok && n <= 0 {
				p.IsRoot = true
			}
		}

	// ── Intrinsic: duration (span or trace scope) ──
	case key == IntrinsicDuration && (cond.Scope == "" || cond.Scope == "trace"):
		if d, ok := cond.Value.(time.Duration); ok {
			switch cond.Operator {
			case ">", ">=":
				p.MinDuration = d
			case "<", "<=":
				p.MaxDuration = d
			}
		}

	// ── Intrinsic: rootName / rootServiceName ──
	// These are extracted to dedicated fields in the plan (not mixed into Tags)
	// because they require a composite ES query (name=X + parentSpanId not exists),
	// not a simple attribute lookup. Both trace-scope and unscoped are handled
	// since the Tempo search API passes these as raw tag keys.
	//
	// Operator mapping:
	//   = "value" → set RootName / RootService to the value (filter by root span name/service)
	//   != nil     → rootName / rootService exists → same as IsRoot (every root span has a name)
	//                Fall through to default to push as TagsExists if there are additional
	//                non-rootName intrinsic semantics needed.
	case key == IntrinsicRootName && (cond.Scope == "trace" || cond.Scope == ""):
		if cond.Operator == "=" && valStr != "" {
			p.RootName = valStr
		} else if cond.Operator == "!=" && cond.Value == nil {
			// rootName != nil: the root span's name is non-nil.
			// Since every otel span has a name, this is equivalent to
			// filtering for root spans. Do NOT push to TagsExists
			// (would generate exists:attributes.rootName which doesn't exist).
			p.IsRoot = true
		}
	case key == IntrinsicRootServiceName && (cond.Scope == "trace" || cond.Scope == ""):
		if cond.Operator == "=" && valStr != "" {
			p.RootService = valStr
		} else if cond.Operator == "!=" && cond.Value == nil {
			// rootServiceName != nil: same semantics as rootName != nil.
			p.IsRoot = true
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
			} else if isNil {
				p.addTagNotExists(fullKey)
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

// addTagNotExists records a = nil absence condition.
func (p *ExecutionPlan) addTagNotExists(key string) {
	p.TagsNotExists = append(p.TagsNotExists, key)
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

	// All leaf nodes are SpanFilters — classify conditions by operator.
	var orGroup, notOrGroup, regexOrGroup []map[string]string
	for _, sf := range groups {
		mEq := make(map[string]string)
		mNot := make(map[string]string)
		mRegex := make(map[string]string)
		for _, cond := range sf.Conditions {
			valStr := condValueToString(cond.Value)
			if valStr == "" {
				continue
			}
			key := scopedKey(cond.Scope, cond.Key)
			switch cond.Operator {
			case "=":
				mEq[key] = valStr
			case "!=":
				mNot[key] = valStr
			case "=~":
				mRegex[key] = valStr
			}
		}
		if len(mEq) > 0 {
			orGroup = append(orGroup, mEq)
		}
		if len(mNot) > 0 {
			notOrGroup = append(notOrGroup, mNot)
		}
		if len(mRegex) > 0 {
			regexOrGroup = append(regexOrGroup, mRegex)
		}
	}
	if len(orGroup) > 0 {
		p.TagsOr = append(p.TagsOr, orGroup)
	}
	if len(notOrGroup) > 0 {
		p.TagsNotOr = append(p.TagsNotOr, notOrGroup)
	}
	if len(regexOrGroup) > 0 {
		p.TagsRegexOr = append(p.TagsRegexOr, regexOrGroup)
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
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%f", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case time.Duration:
		return fmt.Sprintf("%d", int64(val))
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

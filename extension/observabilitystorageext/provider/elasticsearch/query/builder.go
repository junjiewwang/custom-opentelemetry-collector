// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

// Package query provides a fluent API for building Elasticsearch query DSL.
// It is intentionally independent of the elasticsearch package to avoid
// circular imports — field name constants are passed in as strings.
package query

// Builder provides a fluent API for constructing ES bool queries.
// It accumulates must clauses and produces the final query map.
//
// Usage:
//
//	q := NewBuilder().
//	    Term("serviceName", "my-app").
//	    Range("startTimeUnixNano", startNs, endNs, nil, nil).
//	    Build()
type Builder struct {
	mustClauses []map[string]any
}

// NewBuilder creates a new query builder.
func NewBuilder() *Builder {
	return &Builder{}
}

// Clone returns a deep copy of the builder.
func (b *Builder) Clone() *Builder {
	clauses := make([]map[string]any, len(b.mustClauses))
	copy(clauses, b.mustClauses)
	return &Builder{mustClauses: clauses}
}

// Term adds a term query clause (exact match).
//
//	ES: {"term": {field: value}}
func (b *Builder) Term(field, value string) *Builder {
	b.mustClauses = append(b.mustClauses, map[string]any{
		"term": map[string]any{field: value},
	})
	return b
}

// Terms adds a terms query clause (match any of the values).
//
//	ES: {"terms": {field: values}}
func (b *Builder) Terms(field string, values []string) *Builder {
	b.mustClauses = append(b.mustClauses, map[string]any{
		"terms": map[string]any{field: values},
	})
	return b
}

// Range adds a range query clause for numeric fields.
// Use nil for unset bounds. All bounds are stored as-is (caller ensures correct type).
//
//	ES: {"range": {field: {"gte": gte, "lte": lte, "gt": gt, "lt": lt}}}
func (b *Builder) Range(field string, gte, lte, gt, lt any) *Builder {
	rangeSpec := map[string]any{}
	if gte != nil {
		rangeSpec["gte"] = gte
	}
	if lte != nil {
		rangeSpec["lte"] = lte
	}
	if gt != nil {
		rangeSpec["gt"] = gt
	}
	if lt != nil {
		rangeSpec["lt"] = lt
	}
	b.mustClauses = append(b.mustClauses, map[string]any{
		"range": map[string]any{field: rangeSpec},
	})
	return b
}

// Match adds a match query clause (full-text search).
//
//	ES: {"match": {field: {"query": query, ...opts}}}
func (b *Builder) Match(field, query string, opts map[string]any) *Builder {
	matchSpec := map[string]any{"query": query}
	for k, v := range opts {
		matchSpec[k] = v
	}
	b.mustClauses = append(b.mustClauses, map[string]any{
		"match": map[string]any{field: matchSpec},
	})
	return b
}

// Should adds a should (OR) compound clause, wrapping the given sub-clauses
// in a bool.should with a minimum_should_match constraint.
//
//	ES: {"bool": {"should": subClauses, "minimum_should_match": minMatch}}
func (b *Builder) Should(minMatch int, subClauses ...map[string]any) *Builder {
	shouldSpec := map[string]any{
		"should": subClauses,
	}
	if minMatch > 0 {
		shouldSpec["minimum_should_match"] = minMatch
	}
	b.mustClauses = append(b.mustClauses, map[string]any{
		"bool": shouldSpec,
	})
	return b
}

// Raw appends a pre-built must clause (for complex sub-queries that don't fit
// the fluent API).
func (b *Builder) Raw(clause map[string]any) *Builder {
	b.mustClauses = append(b.mustClauses, clause)
	return b
}

// Build returns the final ES query map.
// Returns {"match_all": {}} if no clauses have been added.
func (b *Builder) Build() map[string]any {
	if len(b.mustClauses) == 0 {
		return map[string]any{"match_all": map[string]any{}}
	}
	return map[string]any{
		"bool": map[string]any{"must": b.mustClauses},
	}
}

// TermQ returns a standalone term query (no bool wrapper).
//
//	ES: {"term": {field: value}}
func TermQ(field, value string) map[string]any {
	return map[string]any{"term": map[string]any{field: value}}
}

// TermsQ returns a standalone terms query (no bool wrapper).
//
//	ES: {"terms": {field: values}}
func TermsQ(field string, values []string) map[string]any {
	return map[string]any{"terms": map[string]any{field: values}}
}

// T is a convenience alias for constructing a term sub-clause (used with Should).
func T(field, value string) map[string]any {
	return TermQ(field, value)
}

// MustNot adds a must_not compound clause, wrapping the given sub-clauses
// in a bool.must_not (all must not match).
//
//	ES: {"bool": {"must_not": subClauses}}
func (b *Builder) MustNot(subClauses ...map[string]any) *Builder {
	b.mustClauses = append(b.mustClauses, map[string]any{
		"bool": map[string]any{"must_not": subClauses},
	})
	return b
}

// MustClauses returns the accumulated must clauses (for composition).
func (b *Builder) MustClauses() []map[string]any {
	return b.mustClauses
}

// ExistsQ returns a standalone exists query clause.
//
//	ES: {"exists": {"field": field}}
func ExistsQ(field string) map[string]any {
	return map[string]any{"exists": map[string]any{"field": field}}
}

// MustNotQ returns a standalone bool.must_not clause wrapping the given sub-clauses.
//
//	ES: {"bool": {"must_not": subClauses}}
func MustNotQ(subClauses ...map[string]any) map[string]any {
	return map[string]any{"bool": map[string]any{"must_not": subClauses}}
}

// NestedQuery wraps a query in a nested query for querying nested documents.
// Used for event and link scopes in TraceQL.
//
//	ES: {"nested": {"path": path, "query": innerQuery}}
func NestedQuery(path string, innerQuery map[string]any) map[string]any {
	return map[string]any{
		"nested": map[string]any{
			"path":  path,
			"query": innerQuery,
		},
	}
}

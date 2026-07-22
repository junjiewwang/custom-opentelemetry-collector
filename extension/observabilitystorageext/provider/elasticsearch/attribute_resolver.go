// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import "strings"

// ResolvedField represents a TraceQL attribute resolved to its ES field mapping.
type ResolvedField struct {
	// ESField is the ES document field path (e.g. "serviceName", "attributes.http.method", "resource.host.name").
	ESField string
	// IsExists indicates whether the query semantics is "field exists" (e.g. != nil).
	// Used by Sprint 2 for exists query support.
	IsExists bool
}

// AttributeResolver maps TraceQL attribute references to ES field paths.
//
// It handles:
//  1. Intrinsic fields — serviceName, name, kind, status.code, durationNano
//  2. Scope-prefixed intrinsics — resource.service.name, span.name, span.kind
//  3. Custom attributes — resource.X → resource.X, span.X → attributes.X
type AttributeResolver struct{}

// Resolve resolves a raw TraceQL attribute reference to its ES field mapping.
// The raw input may or may not have a scope prefix.
//
// Examples:
//
//	Resolve("resource.service.name") → ESField="serviceName" (intrinsic)
//	Resolve("span.name")             → ESField="name"        (intrinsic)
//	Resolve("service.name")          → ESField="serviceName" (intrinsic, no prefix)
//	Resolve("span.kind")             → ESField="kind"
//	Resolve("span.status")           → ESField="status.code"
//	Resolve("http.method")           → ESField="attributes.http.method" (custom)
//	Resolve("resource.host.name")    → ESField="resource.host.name"     (custom resource)
func (r *AttributeResolver) Resolve(raw string) ResolvedField {
	scope, key := parseScopeAndKey(raw)
	return r.resolveWithScope(scope, key)
}

// resolveWithScope applies scope-aware mapping rules.
func (r *AttributeResolver) resolveWithScope(scope, key string) ResolvedField {
	// Intrinsic fields — these map to top-level ES fields regardless of scope.
	switch key {
	case "service.name":
		return ResolvedField{ESField: FieldServiceName}
	case "name":
		if scope == "" || scope == "span" {
			return ResolvedField{ESField: FieldName}
		}
	case "kind":
		if scope == "" || scope == "span" {
			return ResolvedField{ESField: FieldKind}
		}
	case "status":
		if scope == "" || scope == "span" {
			// status.code is the canonical term/terms aggregation field.
			// status.code and status.message are stored as nested fields under
			// the "status" object in ES.
			return ResolvedField{ESField: FieldStatus + ".code"}
		}
	case "status.message":
		if scope == "" || scope == "span" {
			return ResolvedField{ESField: FieldStatus + ".message"}
		}
	// Grafana Tempo data sources send intrinsic tags in Tempo canonical form
	// (e.g., "statusMessage", "statusCode"), which differ from the dotted ES field
	// names ("status.message", "status.code"). Map both forms to the correct ES field.
	case "statusMessage":
		if scope == "" || scope == "span" {
			return ResolvedField{ESField: FieldStatus + ".message"}
		}
	case "statusCode":
		if scope == "" || scope == "span" {
			return ResolvedField{ESField: FieldStatus + ".code"}
		}
	case "duration":
		if scope == "" || scope == "span" || scope == "trace" {
			return ResolvedField{ESField: FieldDurationNano}
		}
	// rootName / rootServiceName are derived intrinsic fields:
	// rootName = root span's name, rootServiceName = root span's serviceName.
	// Map to the top-level ES fields for filter/aggregation compatibility.
	case "rootName":
		if scope == "" || scope == "trace" {
			return ResolvedField{ESField: FieldName}
		}
	case "rootServiceName":
		if scope == "" || scope == "trace" {
			return ResolvedField{ESField: FieldServiceName}
		}
	}

	// Custom attributes — scope determines the ES field path prefix.
	switch scope {
	case "resource":
		return ResolvedField{ESField: FieldResource + "." + key}
	case "span", "":
		return ResolvedField{ESField: FieldAttributes + "." + key}
	default:
		// event, link, trace scope — fallback to attributes path.
		return ResolvedField{ESField: FieldAttributes + "." + key}
	}
}

// parseScopeAndKey splits a raw attribute reference into (scope, key).
//
// Mapping rules (aligned with traceql.parseScopeAndKey):
//
//	".http.method"               → ("", "http.method")       // leading dot = unscoped
//	"event:name"                 → ("event", "name")          // colon separator
//	"resource.host.name"         → ("resource", "host.name")  // dot prefix
//	"span.http.method"            → ("span", "http.method")
//	"service.name"               → ("", "service.name")       // no prefix = intrinsic
func parseScopeAndKey(raw string) (scope, key string) {
	// Leading dot: unscoped attribute (e.g. .http.method).
	if strings.HasPrefix(raw, ".") {
		return "", raw[1:]
	}

	// Colon separator: event:name, link:traceID, etc.
	if idx := strings.Index(raw, ":"); idx > 0 {
		return raw[:idx], raw[idx+1:]
	}

	// Dot prefix: resource.xxx, span.xxx, event.xxx, trace.xxx.
	for _, prefix := range []string{"resource.", "span.", "event.", "trace."} {
		if strings.HasPrefix(raw, prefix) {
			return strings.TrimSuffix(prefix, "."), raw[len(prefix):]
		}
	}

	// No scope prefix — treat as unscoped intrinsic or attribute.
	return "", raw
}

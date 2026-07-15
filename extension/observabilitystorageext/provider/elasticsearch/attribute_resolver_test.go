// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAttributeResolver_Resolve covers all intrinsic, scope-prefixed, and custom cases.
func TestAttributeResolver_Resolve(t *testing.T) {
	r := &AttributeResolver{}

	tests := []struct {
		name     string
		raw      string
		want     string
	}{
		// ── Intrinsic fields (no scope prefix) ──
	{name: "service.name intrinsic", raw: "service.name", want: FieldServiceName},
	{name: "name intrinsic", raw: "name", want: FieldName},
	{name: "kind intrinsic", raw: "kind", want: FieldKind},
	{name: "status intrinsic → status.code", raw: "status", want: FieldStatus + ".code"},
	{name: "status.message intrinsic → status.message", raw: "status.message", want: FieldStatus + ".message"},
	{name: "span.status.message → status.message", raw: "span.status.message", want: FieldStatus + ".message"},
	{name: "duration intrinsic", raw: "duration", want: FieldDurationNano},

		// ── Scope-prefixed intrinsics ──
		{name: "resource.service.name → serviceName", raw: "resource.service.name", want: FieldServiceName},
		{name: "span.name → name", raw: "span.name", want: FieldName},
		{name: "span.kind → kind", raw: "span.kind", want: FieldKind},
		{name: "span.status → status.code", raw: "span.status", want: FieldStatus + ".code"},
		{name: "span.duration → durationNano", raw: "span.duration", want: FieldDurationNano},
		{name: "trace.duration → durationNano", raw: "trace.duration", want: FieldDurationNano},

		// ── Custom attributes without scope ──
		{name: "custom attribute → attributes.xxx", raw: "http.method", want: FieldAttributes + ".http.method"},
		{name: "custom attribute → attributes.xxx", raw: "http.status_code", want: FieldAttributes + ".http.status_code"},
		{name: "leading dot unscoped", raw: ".custom.key", want: FieldAttributes + ".custom.key"},

		// ── Custom attributes with scope ──
		{name: "resource custom → resource.xxx", raw: "resource.host.name", want: FieldResource + ".host.name"},
		{name: "resource custom → resource.xxx", raw: "resource.telemetry.sdk.language", want: FieldResource + ".telemetry.sdk.language"},
		{name: "span custom → attributes.xxx", raw: "span.user.id", want: FieldAttributes + ".user.id"},
		{name: "span custom → attributes.xxx", raw: "span.db.system", want: FieldAttributes + ".db.system"},

		// ── Colon separator (e.g. event:name) ──
		{name: "event:name → attributes.xxx", raw: "event:name", want: FieldAttributes + ".name"},
		{name: "link:traceID → attributes.xxx", raw: "link:traceID", want: FieldAttributes + ".traceID"},

		// ── Edge cases ──
		{name: "empty string", raw: "", want: FieldAttributes + "."},
		{name: "dot only", raw: ".", want: FieldAttributes + "."},
		{name: "nested dot attributes", raw: "a.b.c.d", want: FieldAttributes + ".a.b.c.d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Resolve(tt.raw)
			assert.Equal(t, tt.want, got.ESField, "raw=%q", tt.raw)
		})
	}
}

// TestAttributeResolver_Resolve_Regression verifies that existing (no-scope-prefix)
// behavior is preserved — critical for backward compatibility.
func TestAttributeResolver_Resolve_Regression(t *testing.T) {
	r := &AttributeResolver{}

	// These are the exact inputs the old fieldForAttribute received from Tags path.
	// The Resolver must produce the same output for them.
	regressionTests := []struct {
		name string
		raw  string
		want string
	}{
		{"service.name → serviceName (unchanged)", "service.name", FieldServiceName},
		{"name → name (unchanged)", "name", FieldName},
		{"kind → kind (unchanged)", "kind", FieldKind},
		// Note: "status" previously returned FieldStatus ("status") without ".code".
		// This was a bug — status.code is the correct ES field for term/terms queries.
		// The Tags path doesn't include "status" (planner uses p.Status separately),
		// so this change only affects ByLabels which was already broken.
		{"status → status.code (fixed from FieldStatus)", "status", FieldStatus + ".code"},
		{"custom attr → attributes.xxx (unchanged)", "http.method", FieldAttributes + ".http.method"},
		{"custom attr → attributes.xxx (unchanged)", "db.system", FieldAttributes + ".db.system"},
	}

	for _, tt := range regressionTests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Resolve(tt.raw)
			assert.Equal(t, tt.want, got.ESField, "regression check for raw=%q", tt.raw)
		})
	}
}

// TestAttributeResolver_parseScopeAndKey verifies the scope parsing logic
// is aligned with traceql.parseScopeAndKey.
func TestAttributeResolver_parseScopeAndKey(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantScope  string
		wantKey    string
	}{
		{name: "no prefix", raw: "service.name", wantScope: "", wantKey: "service.name"},
		{name: "resource prefix", raw: "resource.host.name", wantScope: "resource", wantKey: "host.name"},
		{name: "span prefix", raw: "span.http.method", wantScope: "span", wantKey: "http.method"},
		{name: "event prefix", raw: "event.name", wantScope: "event", wantKey: "name"},
		{name: "trace prefix", raw: "trace.duration", wantScope: "trace", wantKey: "duration"},
		{name: "leading dot", raw: ".attr", wantScope: "", wantKey: "attr"},
		{name: "colon separator", raw: "event:name", wantScope: "event", wantKey: "name"},
		{name: "intrinsic", raw: "duration", wantScope: "", wantKey: "duration"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope, key := parseScopeAndKey(tt.raw)
			assert.Equal(t, tt.wantScope, scope, "scope mismatch for raw=%q", tt.raw)
			assert.Equal(t, tt.wantKey, key, "key mismatch for raw=%q", tt.raw)
		})
	}
}

// TestAttributeResolver_Resolve_IRootOrNil ensures IsExists is false by default
// (exists query support is Sprint 2).
func TestAttributeResolver_Resolve_IsExistsDefault(t *testing.T) {
	r := &AttributeResolver{}
	result := r.Resolve("service.name")
	assert.False(t, result.IsExists, "IsExists should default to false")
}

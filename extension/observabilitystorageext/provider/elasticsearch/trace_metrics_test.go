// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════
// buildMetricsFilter tests
// ═══════════════════════════════════════════════════

func TestBuildMetricsFilter_RootName(t *testing.T) {
	r := &TraceReader{logger: zap.NewNop()}
	now := time.Now()

	query := TraceMetricsQuery{
		RootName: "GET /api",
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	filter := r.buildMetricsFilter(query)
	raw, err := json.Marshal(filter)
	require.NoError(t, err)

	queryStr := string(raw)

	// RootName should generate a composite bool query:
	//   name = "GET /api" AND parentSpanId not exists
	assert.Contains(t, queryStr, `"name"`)
	assert.Contains(t, queryStr, `"GET /api"`)
	assert.Contains(t, queryStr, `"parentSpanId"`)
	assert.Contains(t, queryStr, `"must_not"`)
	assert.Contains(t, queryStr, `"exists"`)

	// Must NOT use attributes.rootName path.
	assert.NotContains(t, queryStr, `"attributes.rootName"`)
}

func TestBuildMetricsFilter_RootService(t *testing.T) {
	r := &TraceReader{logger: zap.NewNop()}
	now := time.Now()

	query := TraceMetricsQuery{
		RootService: "gateway",
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	filter := r.buildMetricsFilter(query)
	raw, err := json.Marshal(filter)
	require.NoError(t, err)

	queryStr := string(raw)

	assert.Contains(t, queryStr, `"serviceName"`)
	assert.Contains(t, queryStr, `"gateway"`)
	assert.Contains(t, queryStr, `"parentSpanId"`)
	assert.NotContains(t, queryStr, `"attributes.rootServiceName"`)
}

func TestBuildMetricsFilter_RootName_WithIsRoot(t *testing.T) {
	// When both IsRoot and RootName are set, both filters should be present.
	r := &TraceReader{logger: zap.NewNop()}
	now := time.Now()

	query := TraceMetricsQuery{
		IsRoot:    true,
		RootName:  "GET /api",
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	filter := r.buildMetricsFilter(query)
	raw, err := json.Marshal(filter)
	require.NoError(t, err)

	queryStr := string(raw)

	// Both IsRoot's root span detection AND RootName's composite filter should be present.
	assert.Contains(t, queryStr, `"0000000000000000"`,
		"IsRoot should add historical parentSpanId zero-value check")
	assert.Contains(t, queryStr, `"GET /api"`,
		"RootName should be present in composite filter")
}

func TestBuildMetricsFilter_EmptyRootName_NoExtraFilter(t *testing.T) {
	// Empty RootName should NOT add any filter.
	r := &TraceReader{logger: zap.NewNop()}
	now := time.Now()

	query := TraceMetricsQuery{
		RootName: "",
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	filter := r.buildMetricsFilter(query)
	raw, _ := json.Marshal(filter)
	queryStr := string(raw)

	// No parentSpanId, no rootName filter.
	assert.NotContains(t, queryStr, `"parentSpanId"`)
}

// ═══════════════════════════════════════════════════
// AttributeResolver: rootName/rootServiceName mapping
// ═══════════════════════════════════════════════════

func TestAttributeResolver_RootName(t *testing.T) {
	resolver := &AttributeResolver{}

	// rootName (unscoped) → FieldName
	result := resolver.Resolve("rootName")
	assert.Equal(t, FieldName, result.ESField,
		"rootName should map to name (the root span's name field)")
}

func TestAttributeResolver_RootServiceName(t *testing.T) {
	resolver := &AttributeResolver{}

	result := resolver.Resolve("rootServiceName")
	assert.Equal(t, FieldServiceName, result.ESField,
		"rootServiceName should map to serviceName (the root span's serviceName field)")
}

func TestAttributeResolver_RootName_TraceScoped(t *testing.T) {
	resolver := &AttributeResolver{}

	// trace:rootName should also map to name.
	result := resolver.Resolve("trace.rootName")
	assert.Equal(t, FieldName, result.ESField,
		"trace:rootName should also map to name field")
}

func TestAttributeResolver_RootName_NotAffectOtherFields(t *testing.T) {
	resolver := &AttributeResolver{}

	// Regular attributes should not be affected.
	result := resolver.Resolve("http.method")
	assert.Equal(t, "attributes.http.method", result.ESField,
		"non-intrinsic fields should still use attributes.* prefix")
}

func TestAttributeResolver_StatusMessage(t *testing.T) {
	resolver := &AttributeResolver{}

	// Grafana sends "statusMessage" (Tempo intrinsic) without a dot.
	result := resolver.Resolve("statusMessage")
	assert.Equal(t, FieldStatus+".message", result.ESField,
		"statusMessage should map to status.message")
}

func TestAttributeResolver_StatusCode(t *testing.T) {
	resolver := &AttributeResolver{}

	result := resolver.Resolve("statusCode")
	assert.Equal(t, FieldStatus+".code", result.ESField,
		"statusCode should map to status.code")
}

func TestAttributeResolver_StatusMessage_BackwardCompat(t *testing.T) {
	// The dotted form should still work.
	resolver := &AttributeResolver{}
	result := resolver.Resolve("status.message")
	assert.Equal(t, FieldStatus+".message", result.ESField,
		"status.message (dotted) should still map correctly")
}

func TestBuildMetricsFilter_StatusMessageExists(t *testing.T) {
	// Verify TagsExists with statusMessage generates exists:status.message.
	r := &TraceReader{logger: zap.NewNop()}
	now := time.Now()

	query := TraceMetricsQuery{
		TagsExists: []string{"statusMessage"},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	filter := r.buildMetricsFilter(query)
	raw, err := json.Marshal(filter)
	require.NoError(t, err)

	queryStr := string(raw)

	// Should generate exists on status.message, NOT attributes.statusMessage.
	assert.Contains(t, queryStr, `"field":"status.message"`,
		"statusMessage should generate exists:status.message")
	assert.NotContains(t, queryStr, `"field":"attributes.statusMessage"`,
		"statusMessage must NOT generate exists:attributes.statusMessage")
}

func TestMetricsTermClause_StatusMessage(t *testing.T) {
	// status.message is text → must use match query.
	clause := metricsTermClause(FieldStatus+".message", "库存更新失败")

	raw, _ := json.Marshal(clause)
	str := string(raw)

	assert.Contains(t, str, `"match"`, "text field should use match query")
	assert.NotContains(t, str, `"term"`, "text field must NOT use term query")
}

func TestMetricsTermClause_KeywordField(t *testing.T) {
	// Keyword fields (like http.method) should still use term.
	clause := metricsTermClause("attributes.http.method", "GET")

	raw, _ := json.Marshal(clause)
	str := string(raw)

	assert.Contains(t, str, `"term"`, "keyword field should use term query")
	assert.NotContains(t, str, `"match"`, "keyword field must NOT use match query")
}

func TestBuildMetricsFilter_StatusMessageExactMatch(t *testing.T) {
	// Tags with statusMessage=value should use match query.
	r := &TraceReader{logger: zap.NewNop()}
	now := time.Now()

	query := TraceMetricsQuery{
		Tags: map[string]string{
			"statusMessage": "库存更新失败",
		},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	filter := r.buildMetricsFilter(query)
	raw, err := json.Marshal(filter)
	require.NoError(t, err)

	queryStr := string(raw)

	assert.Contains(t, queryStr, `"match"`, "statusMessage=value should use match query")
	assert.Contains(t, queryStr, `"status.message"`, "should resolve to status.message field")
	assert.NotContains(t, queryStr, `"attributes.statusMessage"`,
		"must not use attributes prefix")
}

func TestMetricsAggField_StatusMessage(t *testing.T) {
	resolver := &AttributeResolver{}

	// by(statusMessage) → status.message.keyword (for terms agg on text field).
	field := metricsAggField(resolver, "statusMessage")
	assert.Equal(t, FieldStatus+".message.keyword", field,
		"statusMessage should aggregate on status.message.keyword")
}

func TestMetricsAggField_StatusCode(t *testing.T) {
	resolver := &AttributeResolver{}

	// by(status) → status.code.keyword (for terms agg on text field).
	field := metricsAggField(resolver, "status")
	assert.Equal(t, FieldStatus+".code.keyword", field,
		"status should aggregate on status.code.keyword")
}

func TestMetricsAggField_StatusMessageWithDot(t *testing.T) {
	resolver := &AttributeResolver{}

	field := metricsAggField(resolver, "status.message")
	assert.Equal(t, FieldStatus+".message.keyword", field,
		"status.message should also aggregate on status.message.keyword")
}

func TestMetricsAggField_OtherFields(t *testing.T) {
	resolver := &AttributeResolver{}

	// Intrinsic keyword fields → no .keyword needed.
	// status resolves to status.code (text) → needs .keyword, tested separately.
	for _, label := range []string{"rootName", "kind", "name"} {
		field := metricsAggField(resolver, label)
		assert.NotContains(t, field, ".keyword",
			"intrinsic %q should NOT get .keyword (got %q)", label, field)
	}

	// Custom attributes (text via dynamic template) → needs .keyword.
	for _, label := range []string{"http.method", "span.peer.service"} {
		field := metricsAggField(resolver, label)
		assert.Contains(t, field, ".keyword",
			"custom attribute %q should get .keyword (got %q)", label, field)
	}

	// resource.* (text sub-fields) → needs .keyword.
	field := metricsAggField(resolver, "resource.service.instance.id")
	assert.Equal(t, "resource.service.instance.id.keyword", field,
		"dynamic resource sub-field should get .keyword")

	// resource.app_id is also text → needs .keyword (removed from NoKeyword list).
	field = metricsAggField(resolver, "resource.app_id")
	assert.Equal(t, "resource.app_id.keyword", field,
		"resource.app_id is text field, needs .keyword suffix")

	// resource.* (explicit keyword/long fields) → no .keyword needed.
	for _, label := range []string{"resource.host.name", "resource.service.namespace", "resource.process.pid"} {
		field := metricsAggField(resolver, label)
		assert.NotContains(t, field, ".keyword",
			"keyword/long resource field %q should NOT get .keyword (got %q)", label, field)
	}
}

func TestMetricsAggField_ResourceFields(t *testing.T) {
	resolver := &AttributeResolver{}

	// resource.* fields are text → needs .keyword.
	for _, label := range []string{
		"resource.service.instance.id",
		"resource.telemetry.distro.name",
	} {
		field := metricsAggField(resolver, label)
		assert.True(t, strings.HasSuffix(field, ".keyword"),
			"resource text field %q should get .keyword suffix (got %q)", label, field)
	}
}

// TestBuildMetricsFilter_SharedTagPath_SpanKindTransform is a regression test for
// the filter unification: buildMetricsFilter must route span.kind through the
// shared resolveTagTermClauses, which capitalizes the value (server → Server).
// Previously it called resolver.Resolve + metricsTermClause directly, leaving
// "server" lowercase — which never matched ES's stored "Server".
func TestBuildMetricsFilter_SharedTagPath_SpanKindTransform(t *testing.T) {
	r := &TraceReader{logger: zap.NewNop()}
	now := time.Now()

	query := TraceMetricsQuery{
		Tags: map[string]string{"span.kind": "server"},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	filter := r.buildMetricsFilter(query)
	raw, err := json.Marshal(filter)
	require.NoError(t, err)
	queryStr := string(raw)

	assert.Contains(t, queryStr, `"kind":"Server"`,
		"span.kind=server must be capitalized to match ES-stored form")
	assert.NotContains(t, queryStr, `"kind":"server"`,
		"raw lowercase value must not leak into the query")
}

// TestBuildMetricsFilter_SharedTagPath_DualAttributeSearch verifies that an
// unscoped custom attribute tag produces the backward-compatible dual-path
// (attributes.X + resource.X) should query, matching the search path — which
// the old inline resolver.Resolve logic omitted entirely.
func TestBuildMetricsFilter_SharedTagPath_DualAttributeSearch(t *testing.T) {
	r := &TraceReader{logger: zap.NewNop()}
	now := time.Now()

	query := TraceMetricsQuery{
		Tags: map[string]string{"http.method": "GET"},
		TimeRange: TimeRange{
			Start: now.Add(-1 * time.Hour),
			End:   now,
		},
	}

	filter := r.buildMetricsFilter(query)
	raw, err := json.Marshal(filter)
	require.NoError(t, err)
	queryStr := string(raw)

	assert.Contains(t, queryStr, `"attributes.http.method"`,
		"unscoped attribute must search the attributes.* path")
	assert.Contains(t, queryStr, `"resource.http.method"`,
		"unscoped attribute must also search the resource.* path (backward compat)")
	assert.Contains(t, queryStr, `"should"`,
		"dual-path search must be wrapped in a bool.should")
}

// TestBuildMetricsFilter_MatchesSearchPath_SpanKind confirms the metrics and
// search filter builders now emit identical sub-clauses for span.kind, so the
// two paths can never drift again.
func TestBuildMetricsFilter_MatchesSearchPath_SpanKind(t *testing.T) {
	r := &TraceReader{logger: zap.NewNop()}
	now := time.Now()
	tr := TimeRange{Start: now.Add(-1 * time.Hour), End: now}

	metricsFilter := r.buildMetricsFilter(TraceMetricsQuery{
		Tags:      map[string]string{"span.kind": "client"},
		TimeRange: tr,
	})
	// buildTraceSearchQuery emits the full bool query including the time range;
	// extract just the kind clause for comparison by checking both contain the
	// capitalized value via the same transformation.
	searchQuery := r.buildTraceSearchQuery(TraceQuery{
		Tags:      map[string]string{"span.kind": "client"},
		TimeRange: tr,
	})

	mRaw, _ := json.Marshal(metricsFilter)
	sRaw, _ := json.Marshal(searchQuery)
	assert.Contains(t, string(mRaw), `"kind":"Client"`)
	assert.Contains(t, string(sRaw), `"kind":"Client"`,
		"search and metrics paths must agree on the capitalized kind value")
}

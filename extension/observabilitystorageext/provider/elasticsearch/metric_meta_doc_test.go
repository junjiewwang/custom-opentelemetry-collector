// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── metaTemplateMappings ────────────────────────────────────────────────

func TestMetaTemplateMappings_Structure(t *testing.T) {
	tmpl := metaTemplateMappings(IndexConfig{IndexPrefix: "otel-metrics", Replicas: 1, RefreshInterval: "30s"})

	// index_patterns must be the exact singleton name (no wildcard).
	assert.Equal(t, []string{"otel-metrics-meta"}, tmpl["index_patterns"])
	assert.Equal(t, 200, tmpl["priority"], "must beat rollup (100) and raw (0)")

	tmplVal := tmpl["template"].(map[string]any)

	// settings: no delete ILM — the raw template's delete policy must NOT leak in.
	settings := tmplVal["settings"].(map[string]any)
	lifecycle, ok := settings["index.lifecycle.name"]
	require.True(t, ok, "index.lifecycle.name must be explicitly set (to null)")
	assert.Nil(t, lifecycle, "meta index must not be ILM-managed, or retention deletes it")

	// mappings: dynamic:false + explicit keyword-array fields.
	mappings := tmplVal["mappings"].(map[string]any)
	assert.Equal(t, false, mappings["dynamic"], "dynamic must be false to avoid mapping conflicts")
	props := mappings["properties"].(map[string]any)

	// labelKeys/serviceNames must be keyword (ES keyword arrays accept both
	// single values and arrays without conflict — the auto-create race guard).
	labelKeys := props[FieldMetaLabelKeys].(map[string]any)
	assert.Equal(t, "keyword", labelKeys["type"])
	serviceNames := props[FieldMetaServiceNames].(map[string]any)
	assert.Equal(t, "keyword", serviceNames["type"])

	// lastSeenAt date / docCount long.
	lastSeen := props[FieldMetaLastSeenAt].(map[string]any)
	assert.Equal(t, "date", lastSeen["type"])
	assert.Equal(t, "epoch_millis", lastSeen["format"])
	assert.Equal(t, "long", props[FieldMetaDocCount].(map[string]any)["type"])

	// name/appId/type/unit are keyword.
	for _, f := range []string{FieldName, FieldAppID, FieldMetricType, FieldMetricUnit} {
		assert.Equal(t, "keyword", props[f].(map[string]any)["type"], "field %s must be keyword", f)
	}
}

// ── metaIndexName / metaDocID ───────────────────────────────────────────

func TestMetaIndexName(t *testing.T) {
	assert.Equal(t, "otel-metrics-meta", metaIndexName("otel-metrics"))
}

func TestMetaDocID_Short(t *testing.T) {
	assert.Equal(t, "myapp:jvm.memory.used", metaDocID("myapp", "jvm.memory.used"))
}

func TestMetaDocID_LongTruncatesUnder512(t *testing.T) {
	longName := strings.Repeat("a", 1000)
	id := metaDocID("myapp", longName)
	assert.LessOrEqual(t, len(id), 512, "ES _id must stay under 512 bytes")
	assert.True(t, strings.Contains(id, "#"), "truncated id should carry a hash suffix for uniqueness")
}

func TestMetaDocID_UniqueForDifferentInputs(t *testing.T) {
	a := metaDocID("app", strings.Repeat("x", 500))
	b := metaDocID("app", strings.Repeat("y", 500))
	assert.NotEqual(t, a, b)
}

// ── metaScriptedUpsert ──────────────────────────────────────────────────

func TestMetaScriptedUpsert_Shape(t *testing.T) {
	doc := MetaDoc{
		Name:         "jvm.memory.used",
		AppID:        "myapp",
		Type:         "gauge",
		Unit:         "By",
		LabelKeys:    []string{"area", "pool"},
		ServiceNames: []string{"my-service"},
		LastSeenAt:   1700000000000,
	}
	body := metaScriptedUpsert(doc)

	// scripted_upsert must be true so ES inserts the upsert doc on first-seen.
	assert.Equal(t, true, body["scripted_upsert"])

	script := body["script"].(map[string]any)
	assert.Equal(t, "painless", script["lang"])
	src := script["source"].(string)
	// The script must union labelKeys, union serviceNames (capped), guard lastSeenAt.
	for _, want := range []string{
		"labelKeys",
		"serviceNames",
		"lastSeenAt",
		"serviceCap",
	} {
		assert.True(t, strings.Contains(src, want), "script source must reference %q", want)
	}
	// "type" is a Painless reserved word — must use ctx._source.put('type', ...).
	assert.True(t, strings.Contains(src, "ctx._source.put('type'"), "script must use put() for reserved 'type'")

	params := script["params"].(metaUpsertParams)
	assert.Equal(t, []string{"area", "pool"}, params.LabelKeys)
	assert.Equal(t, []string{"my-service"}, params.ServiceNames)
	assert.Equal(t, int64(1700000000000), params.LastSeenAt)
	assert.Equal(t, "gauge", params.Type)
	assert.Equal(t, "By", params.Unit)
	assert.Equal(t, metaServiceNameCap, params.ServiceCap)

	// upsert doc carries the first-seen fields under the canonical field names.
	upsert := body["upsert"].(map[string]any)
	assert.Equal(t, "jvm.memory.used", upsert[FieldName])
	assert.Equal(t, "myapp", upsert[FieldAppID])
	assert.Equal(t, "gauge", upsert[FieldMetricType])
	assert.Equal(t, "By", upsert[FieldMetricUnit])
	assert.Equal(t, []string{"area", "pool"}, upsert[FieldMetaLabelKeys])
	assert.Equal(t, []string{"my-service"}, upsert[FieldMetaServiceNames])
	assert.Equal(t, int64(1700000000000), upsert[FieldMetaLastSeenAt])
}

// ── rawIndexPatternForRange meta exclusion ──────────────────────────────

func newMetaPatternReader(prefix string) *MetricReader {
	return &MetricReader{config: &Config{Metrics: IndexConfig{IndexPrefix: prefix}}}
}

func TestRawIndexPatternForRange_ExcludesMeta(t *testing.T) {
	r := newMetaPatternReader("otel-metrics")

	// Non-zero range: date-partitioned patterns + rollup + meta exclusion.
	start := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	pat := r.rawIndexPatternForRange(start, end)

	// The negative pattern must be comma-delimited with a leading "-" so ES
	// treats it as an exclusion (not an inclusion).
	assert.True(t, strings.Contains(pat, ",-otel-metrics-rollup-*"), "must exclude rollup: %s", pat)
	assert.True(t, strings.Contains(pat, ",-otel-metrics-meta"), "must exclude meta: %s", pat)

	// Zero time range: degrades to bare wildcard, but the exclusion still applies.
	zeroPat := r.rawIndexPatternForRange(time.Time{}, time.Time{})
	assert.True(t, strings.Contains(zeroPat, "-otel-metrics-meta"), "zero-time fallback must still exclude meta: %s", zeroPat)
}

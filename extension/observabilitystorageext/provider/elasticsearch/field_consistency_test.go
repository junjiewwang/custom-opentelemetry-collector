// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/custom/extension/observabilitystorageext/storedmodel"
)

// field_consistency_test.go guards the invariant documented in fields.go: the
// ES document field name has THREE sources of truth that must agree —
//   1. fields.go Field* constants (readers),
//   2. storedmodel struct json tags (writers), and
//   3. admin.go index template mapping property keys (schema).
//
// A drift in any one silently breaks reads or writes (the P0 GetLogStats
// "severity" vs "severityText" bug was exactly this class). These tests make
// drift fail the build.

// jsonTags returns the set of json field tags (name only, options stripped)
// declared on struct type T, including tags reachable through nested struct
// fields (e.g. StoredSpan.Status → StoredStatus). Map/slice fields are skipped
// (their element types are not ES top-level property keys).
func jsonTags[T any]() map[string]struct{} {
	tags := map[string]struct{}{}
	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		if rt.Kind() == reflect.Ptr {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := f.Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			name := strings.Split(tag, ",")[0]
			if name != "" {
				tags[name] = struct{}{}
			}
			// Recurse into nested struct fields (Status, Scope).
			if f.Type.Kind() == reflect.Struct {
				walk(f.Type)
			}
		}
	}
	var zero T
	walk(reflect.TypeOf(zero))
	return tags
}

// TestFieldConstants_MatchStoredModelTags asserts every Field* constant used by
// a signal's reader/query layer corresponds to a json tag on that signal's
// storedmodel writer type. This catches a constant renamed without updating
// the stored struct (or vice versa).
func TestFieldConstants_MatchStoredModelTags(t *testing.T) {
	spanTags := jsonTags[storedmodel.StoredSpan]()
	logTags := jsonTags[storedmodel.StoredLogRecord]()
	metricTags := jsonTags[storedmodel.StoredMetricDataPoint]()

	// Trace fields → StoredSpan tags (including nested StoredStatus/Scope).
	for _, c := range []string{
		FieldTraceID, FieldSpanID, FieldParentSpanID, FieldName, FieldKind,
		FieldStartTimeUnixNano, FieldEndTimeUnixNano, FieldDurationNano,
		FieldTraceState, FieldStatus, FieldScope, FieldEvents, FieldLinks,
		FieldServiceName, FieldAppID, FieldAttributes, FieldResource,
	} {
		assert.Contains(t, spanTags, c, "trace field constant %q must match a StoredSpan json tag", c)
	}
	// status.code / status.message are nested under the "status" object; the
	// constant FieldStatus ("status") is the object key, verified above. The
	// sub-keys ("code", "message") come from StoredStatus tags.
	assert.Contains(t, spanTags, "code")
	assert.Contains(t, spanTags, "message")

	// Log fields → StoredLogRecord tags.
	for _, c := range []string{
		FieldLogTimeUnixNano, FieldLogObservedTimeUnixNano, FieldLogSeverityNumber,
		FieldLogSeverityText, FieldLogBody, FieldTraceID, FieldSpanID,
		FieldServiceName, FieldAppID, FieldAttributes, FieldResource,
	} {
		assert.Contains(t, logTags, c, "log field constant %q must match a StoredLogRecord json tag", c)
	}

	// Metric fields → StoredMetricDataPoint tags.
	for _, c := range []string{
		FieldMetricTimeUnixMilli, FieldName, FieldMetricType, FieldMetricValue,
		FieldMetricLabels, FieldServiceName, FieldAppID, FieldResource,
		FieldMetricBucketCounts, FieldMetricExplicitBounds,
	} {
		assert.Contains(t, metricTags, c, "metric field constant %q must match a StoredMetricDataPoint json tag", c)
	}
}

// templatePropertyKeys returns the set of property keys in a template's
// mappings.properties, recursing one level into nested "properties" maps
// (status, events, links, scope, resource) so nested field constants are
// checked too.
func templatePropertyKeys(tmpl map[string]any) map[string]struct{} {
	keys := map[string]struct{}{}
	tmplVal, _ := tmpl["template"].(map[string]any)
	mappings, _ := tmplVal["mappings"].(map[string]any)
	props, _ := mappings["properties"].(map[string]any)
	for k, v := range props {
		keys[k] = struct{}{}
		// Recurse into nested properties (e.g. status, events, links, scope).
		if vMap, ok := v.(map[string]any); ok {
			if nested, ok := vMap["properties"].(map[string]any); ok {
				for nk := range nested {
					keys[nk] = struct{}{}
				}
			}
		}
	}
	return keys
}

// TestTemplateMappings_ContainFieldConstants asserts every Field* constant for
// a signal appears as a property key in that signal's index template (top-level
// or nested). This catches a constant added without a mapping (or a mapping
// whose key drifted from the constant).
func TestTemplateMappings_ContainFieldConstants(t *testing.T) {
	cfg := IndexConfig{IndexPrefix: "otel", IndexDateFormat: "2006.01.02"}

	traceKeys := templatePropertyKeys(traceTemplateMappings(cfg))
	for _, c := range []string{
		FieldTraceID, FieldSpanID, FieldParentSpanID, FieldName, FieldKind,
		FieldStartTimeUnixNano, FieldEndTimeUnixNano, FieldDurationNano,
		FieldTraceState, FieldStatus, FieldScope, FieldEvents, FieldLinks,
		FieldServiceName, FieldAppID, FieldAttributes, FieldResource,
	} {
		assert.Contains(t, traceKeys, c, "trace template must map field constant %q", c)
	}
	// Nested status sub-keys ("code", "message") must appear under status.
	assert.Contains(t, traceKeys, "code")
	assert.Contains(t, traceKeys, "message")

	metricKeys := templatePropertyKeys(metricTemplateMappings(cfg))
	for _, c := range []string{
		FieldMetricTimeUnixMilli, FieldName, FieldMetricType, FieldMetricValue,
		FieldServiceName, FieldAppID, FieldMetricLabels, FieldResource,
	} {
		assert.Contains(t, metricKeys, c, "metric template must map field constant %q", c)
	}

	logKeys := templatePropertyKeys(logTemplateMappings(cfg))
	for _, c := range []string{
		FieldLogTimeUnixNano, FieldLogObservedTimeUnixNano, FieldTraceID,
		FieldSpanID, FieldLogSeverityText, FieldLogSeverityNumber, FieldLogBody,
		FieldServiceName, FieldAppID, FieldAttributes, FieldResource,
	} {
		assert.Contains(t, logKeys, c, "log template must map field constant %q", c)
	}
}

// TestSignalSpec_TableComplete asserts the SignalSpec table covers exactly the
// three signals, each with a non-empty TimeField matching the corresponding
// Field* constant, and the correct BoundNano kind for its ES field type.
func TestSignalSpec_TableComplete(t *testing.T) {
	want := map[string]struct {
		timeField string
		boundNano bool
	}{
		"trace":  {FieldStartTimeUnixNano, true},
		"metric": {FieldMetricTimeUnixMilli, false},
		"log":    {FieldLogTimeUnixNano, true},
	}
	require.Len(t, signalSpecs, len(want), "signalSpecs must cover exactly trace/metric/log")
	seen := map[string]bool{}
	for _, s := range signalSpecs {
		exp, ok := want[s.Signal]
		require.True(t, ok, "unexpected signal %q in signalSpecs", s.Signal)
		assert.NotEmpty(t, s.TimeField, "signal %q has empty TimeField", s.Signal)
		assert.Equal(t, exp.timeField, s.TimeField, "signal %q TimeField", s.Signal)
		assert.Equal(t, exp.boundNano, s.BoundNano, "signal %q BoundNano", s.Signal)
		assert.NotNil(t, s.Prefix, "signal %q Prefix func is nil", s.Signal)
		assert.NotNil(t, s.DateFormat, "signal %q DateFormat func is nil", s.Signal)
		seen[s.Signal] = true
	}
	for sig := range want {
		assert.True(t, seen[sig], "signal %q missing from signalSpecs", sig)
	}
}

// TestSignalSpec_Lookups verifies specFor and the delegated lookup helpers
// return the configured values (and the "" default for unknown signals).
func TestSignalSpec_Lookups(t *testing.T) {
	cfg := &Config{
		Traces:  IndexConfig{IndexPrefix: "otel-traces", IndexDateFormat: "2006.01.02"},
		Metrics: IndexConfig{IndexPrefix: "otel-metrics", IndexDateFormat: "2006.01.02"},
		Logs:    IndexConfig{IndexPrefix: "otel-logs", IndexDateFormat: "2006.01.02"},
	}
	assert.Equal(t, "otel-traces", signalPrefix(cfg, "trace"))
	assert.Equal(t, "otel-metrics", signalPrefix(cfg, "metric"))
	assert.Equal(t, "otel-logs", signalPrefix(cfg, "log"))
	assert.Equal(t, "", signalPrefix(cfg, "unknown"))
	assert.Equal(t, "2006.01.02", signalDateFormat(cfg, "trace"))
	assert.Equal(t, FieldStartTimeUnixNano, signalTimestampField("trace"))
	assert.Equal(t, FieldMetricTimeUnixMilli, signalTimestampField("metric"))
	assert.Equal(t, FieldLogTimeUnixNano, signalTimestampField("log"))
}

// templateFieldTypes returns a map from dotted field path → ES mapping "type"
// for every explicitly-mapped property in a template, recursing into nested
// "properties" (status, events, links, scope, resource). Fields reached only
// via dynamic_templates (e.g. unmapped attributes.*) are NOT present — their
// type is determined at runtime by ES, so it cannot be statically asserted.
func templateFieldTypes(tmpl map[string]any) map[string]string {
	out := map[string]string{}
	tmplVal, _ := tmpl["template"].(map[string]any)
	mappings, _ := tmplVal["mappings"].(map[string]any)
	props, _ := mappings["properties"].(map[string]any)
	var walk func(prefix string, props map[string]any)
	walk = func(prefix string, props map[string]any) {
		for k, v := range props {
			vMap, ok := v.(map[string]any)
			if !ok {
				continue
			}
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if t, ok := vMap["type"].(string); ok && t != "" {
				out[path] = t
			}
			if nested, ok := vMap["properties"].(map[string]any); ok {
				walk(path, nested)
			}
		}
	}
	walk("", props)
	return out
}

// aggregatableType reports whether an ES mapping type supports terms/composite
// aggregation directly (without a .keyword sub-field). keyword/long/integer/
// boolean/double/date are all aggregatable; "text" and "object" are not.
func aggregatableType(t string) bool {
	switch t {
	case "keyword", "long", "integer", "short", "byte", "boolean", "double", "float", "date":
		return true
	}
	return false
}

// TestKnownAggregatableFields_MatchTraceTemplate guards the .keyword decision
// table against the trace index template. Every explicitly-mapped field that
// knownAggregatableFields claims is aggregatable (no .keyword needed) must
// actually be mapped as an aggregatable type in the trace template. A field
// that drifted to "text" in the template but stayed true in the table would
// cause silent empty aggregations — the same class of bug as the P0
// GetLogStats "severity" field.
//
// The attributes.* "dynamic numeric" entries (thread.id, server.port, etc.)
// are deliberately excluded: they are NOT explicitly mapped — their type is
// assigned at runtime by ES dynamic mapping, so it cannot be asserted from the
// static template. They are documented empirical knowledge.
func TestKnownAggregatableFields_MatchTraceTemplate(t *testing.T) {
	cfg := IndexConfig{IndexPrefix: "otel", IndexDateFormat: "2006.01.02"}
	traceTypes := templateFieldTypes(traceTemplateMappings(cfg))

	// Explicitly-mapped fields whose aggregatability CAN be verified from the
	// trace template. (Excludes attributes.* and resource.process.pid
	// dynamic-numeric entries — those are dynamically typed by ES at runtime.)
	verifiable := []string{
		FieldKind, FieldName, FieldSpanID, FieldTraceID, FieldParentSpanID,
		FieldServiceName, FieldStartTimeUnixNano, FieldEndTimeUnixNano, FieldDurationNano,
		FieldResource + ".service.name",
		FieldResource + ".host.name",
		FieldResource + ".service.namespace",
		FieldResource + ".service.version",
	}
	for _, f := range verifiable {
		require.True(t, knownAggregatableFields[f],
			"field %q is verifiable from the template but missing from knownAggregatableFields — "+
				"if it was removed, confirm it is still aggregatable, else it needs .keyword", f)
		typ, ok := traceTypes[f]
		require.True(t, ok, "field %q in knownAggregatableFields is not explicitly mapped in the trace template "+
			"(it may be dynamically typed — move it to the dynamic-numeric group if so)", f)
		assert.True(t, aggregatableType(typ),
			"field %q is in knownAggregatableFields (claims no .keyword needed) but trace template maps it as %q — "+
				"terms aggregation on a text field returns empty buckets", f, typ)
	}
}

// TestKnownAggregatableFields_DynamicNumericEntriesAreDocumented ensures the
// attributes.* entries in knownAggregatableFields (which cannot be verified
// from the static template) are at least explicitly enumerated here, so a new
// entry is a conscious decision rather than an accident. If someone adds an
// attributes.* entry, they must also add it to this list.
func TestKnownAggregatableFields_DynamicNumericEntriesAreDocumented(t *testing.T) {
	dynamicNumeric := []string{
		FieldAttributes + ".thread.id",
		FieldAttributes + ".order_error",
		FieldAttributes + ".rpc.grpc_status_code",
		FieldAttributes + ".server.port",
		FieldAttributes + ".network.peer_port",
		FieldAttributes + ".messaging.kafka_message_offset",
		FieldAttributes + ".messaging.message_body_size",
		FieldAttributes + ".messaging.destination_partition_id",
		// resource.process.pid is dynamically mapped (long) by ES at runtime —
		// not in the trace template's explicit resource.properties.
		FieldResource + ".process.pid",
	}
	for _, f := range dynamicNumeric {
		assert.True(t, knownAggregatableFields[f],
			"dynamic-numeric field %q is documented but missing from knownAggregatableFields", f)
	}
}

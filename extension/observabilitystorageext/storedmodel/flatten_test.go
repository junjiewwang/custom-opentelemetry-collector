package storedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

// ═══════════ pcommonMapToFlat ("." for traces/logs/resources) ═══════════

func TestPcommonMapToFlat_DotSeparator(t *testing.T) {
	// Traces/logs default: nested maps use "." separator.
	m := pcommon.NewMap()
	m.PutStr("top", "val")
	env := m.PutEmptyMap("env")
	env.PutStr("name", "prod")

	result := pcommonMapToFlat(m)

	assert.Equal(t, "val", result["top"])
	// Dot separator: "env" + "." + "name" = "env.name"
	assert.Equal(t, "prod", result["env.name"])
	assert.NotContains(t, result, "env") // no intermediate object leaked
}

func TestPcommonMapToFlat_DeepNestedDots(t *testing.T) {
	m := pcommon.NewMap()
	a := m.PutEmptyMap("a")
	b := a.PutEmptyMap("b")
	b.PutStr("c", "value")

	result := pcommonMapToFlat(m)

	// "a" + "." + "b" + "." + "c" = "a.b.c" → SanitizeKey → "a.b_c"
	assert.Equal(t, "value", result["a.b_c"])
}

func TestPcommonMapToFlat_TraceAttributeCompat(t *testing.T) {
	// Verify that existing trace attribute behavior is preserved.
	// http.method (2-segment OTel key) and db.operation.name (3-segment) are unaffected.
	m := pcommon.NewMap()
	m.PutStr("http.method", "GET")
	m.PutStr("db.operation.name", "SELECT")

	result := pcommonMapToFlat(m)

	assert.Equal(t, "GET", result["http.method"])        // 2-segment → unchanged
	assert.Equal(t, "SELECT", result["db.operation_name"]) // 3-segment → sanitized
}

// ═══════════ pcommonMapToFlatMetric ("_" for metric labels) ═══════════

func TestPcommonMapToFlatMetric_UnderscoreSep(t *testing.T) {
	// Metric labels: nested maps use "_" to avoid ES dot-nesting conflicts.
	m := pcommon.NewMap()
	m.PutStr("server", "my-svc")
	srv := m.PutEmptyMap("env")
	srv.PutStr("name", "prod")

	result := pcommonMapToFlatMetric(m)

	assert.Equal(t, "my-svc", result["server"])
	assert.Equal(t, "prod", result["env_name"]) // "_" separator
	assert.NotContains(t, result, "env")
}

func TestPcommonMapToFlatMetric_ProductionServerConflict(t *testing.T) {
	// Exact reproduction of the production error:
	// Doc A: server = "mock_project_db" (string → keyword)
	// Doc B: server = {address: "10.0.0.5"} (map → would be object)
	// After _ flatten: completely independent ES fields → zero conflict.
	m1 := pcommon.NewMap()
	m1.PutStr("server", "mock_project_db")
	m1.PutStr("connection_type", "database")

	m2 := pcommon.NewMap()
	srv := m2.PutEmptyMap("server")
	srv.PutStr("address", "10.0.0.5")
	srv.PutStr("port", "8080")

	r1 := pcommonMapToFlatMetric(m1)
	r2 := pcommonMapToFlatMetric(m2)

	assert.Equal(t, "mock_project_db", r1["server"])
	assert.Equal(t, "10.0.0.5", r2["server_address"])
	assert.Equal(t, "8080", r2["server_port"])
	// Critical: "server" must NOT appear in r2 (no intermediate object)
	assert.NotContains(t, r2, "server",
		"deep flatten eliminated the intermediate server object")
}

func TestPcommonMapToFlatMetric_MultiPolymorphic(t *testing.T) {
	// All 3 production conflict patterns: server, status, service.
	m1 := pcommon.NewMap()
	m1.PutStr("server", "my-server")
	m1.PutStr("status", "ok")
	m1.PutStr("service", "my-app")

	m2 := pcommon.NewMap()
	srv := m2.PutEmptyMap("server")
	srv.PutStr("address", "10.0.0.1")
	stat := m2.PutEmptyMap("status")
	stat.PutInt("code", 200)
	svc := m2.PutEmptyMap("service")
	svc.PutStr("name", "my-app-svc")

	r1 := pcommonMapToFlatMetric(m1)
	r2 := pcommonMapToFlatMetric(m2)

	assert.Equal(t, "my-server", r1["server"])
	assert.Equal(t, "ok", r1["status"])
	assert.Equal(t, "my-app", r1["service"])

	assert.Equal(t, "10.0.0.1", r2["server_address"])
	assert.Equal(t, int64(200), r2["status_code"])
	assert.Equal(t, "my-app-svc", r2["service_name"])

	assert.NotContains(t, r2, "server")
	assert.NotContains(t, r2, "status")
	assert.NotContains(t, r2, "service")
}

// ═══════════ Cross-validation: both variants coexist ═══════════

func TestPcommonMapToFlat_BothVariants(t *testing.T) {
	m := pcommon.NewMap()
	nested := m.PutEmptyMap("a")
	nested.PutStr("b", "v")

	rDot := pcommonMapToFlat(m)
	rUnderscore := pcommonMapToFlatMetric(m)

	assert.Equal(t, "v", rDot["a.b"],       "traces/logs: dot separator")
	assert.Equal(t, "v", rUnderscore["a_b"], "metrics: underscore separator")
	assert.NotContains(t, rDot, "a")
	assert.NotContains(t, rUnderscore, "a")
}

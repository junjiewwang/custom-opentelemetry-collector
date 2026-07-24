package storedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

func TestFlattenMapToFlat_NestedMap(t *testing.T) {
	m := pcommon.NewMap()
	// string value
	m.PutStr("server", "my-svc")
	// nested map value → flattened
	env := m.PutEmptyMap("env")
	env.PutStr("name", "prod")
	env.PutStr("region", "us-east-1")

	result := pcommonMapToFlat(m)

	assert.Equal(t, "my-svc", result["server"])
	assert.Equal(t, "prod", result["env.name"])
	assert.Equal(t, "us-east-1", result["env.region"])
	assert.NotContains(t, result, "env") // no nested objects
}

func TestFlattenMapToFlat_DeepNested(t *testing.T) {
	m := pcommon.NewMap()
	nested := m.PutEmptyMap("a")
	nested2 := nested.PutEmptyMap("b")
	nested2.PutStr("c", "value")

	result := pcommonMapToFlat(m)

	// "a.b.c" (3 segments) → SanitizeKey → "a.b_c" (2 segments)
	assert.Equal(t, "value", result["a.b_c"])
	assert.NotContains(t, result, "a") // no intermediate object
}

func TestFlattenMapToFlat_NoConflict(t *testing.T) {
	// Simulates: doc1 has server=string, doc2 has server={name:...}
	m1 := pcommon.NewMap()
	m1.PutStr("server", "my-svc")

	m2 := pcommon.NewMap()
	srv := m2.PutEmptyMap("server")
	srv.PutStr("name", "foo-svc")

	r1 := pcommonMapToFlat(m1)
	r2 := pcommonMapToFlat(m2)

	// Different keys → no ES mapping conflict.
	assert.Equal(t, "my-svc", r1["server"])
	assert.Equal(t, "foo-svc", r2["server.name"])
	assert.NotContains(t, r2, "server")
}

func TestFlattenMapToFlat_ProductionServerConflict(t *testing.T) {
	// Exact reproduction of production error:
	// Doc A: labels.server = "mock_project_db" (string → keyword)
	// Doc B: labels.server = {address: "10.0.0.5"} (map → would be object)
	// After flatten, they become different field paths → zero conflict.
	m1 := pcommon.NewMap()
	m1.PutStr("server", "mock_project_db")
	m1.PutStr("connection_type", "database")

	m2 := pcommon.NewMap()
	srv := m2.PutEmptyMap("server")
	srv.PutStr("address", "10.0.0.5")
	srv.PutStr("port", "8080")

	r1 := pcommonMapToFlat(m1)
	r2 := pcommonMapToFlat(m2)

	// Doc A: labels.server = "mock_project_db"
	assert.Equal(t, "mock_project_db", r1["server"])
	// Doc B: labels.server.address, labels.server.port (flattened, NOT sub-objects of labels.server)
	assert.Equal(t, "10.0.0.5", r2["server.address"])
	assert.Equal(t, "8080", r2["server.port"])
	// Critical: labels.server must NOT appear as a key in r2 (no intermediate object)
	assert.NotContains(t, r2, "server",
		"deep flatten eliminated the intermediate server object")
}

func TestFlattenMapToFlat_MultiPolymorphic(t *testing.T) {
	// Simulates the exact 3 conflict patterns from production logs:
	// server: "string" vs {address:..., port:...}
	// status: "ok" vs {code: 200}
	// service: "app" vs {name: "app-svc"}
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

	r1 := pcommonMapToFlat(m1)
	r2 := pcommonMapToFlat(m2)

	// Doc 1: flat scalars
	assert.Equal(t, "my-server", r1["server"])
	assert.Equal(t, "ok", r1["status"])
	assert.Equal(t, "my-app", r1["service"])

	// Doc 2: nested maps become dotted keys
	assert.Equal(t, "10.0.0.1", r2["server.address"])
	assert.Equal(t, int64(200), r2["status.code"])
	assert.Equal(t, "my-app-svc", r2["service.name"])

	// No intermediate objects leaked
	assert.NotContains(t, r2, "server")
	assert.NotContains(t, r2, "status")
	assert.NotContains(t, r2, "service")
}

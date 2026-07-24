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

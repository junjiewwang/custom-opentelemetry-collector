// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package elasticsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
)

func TestAttributesToMap_Empty(t *testing.T) {
	attrs := pcommon.NewMap()
	result := attributesToMap(attrs)
	assert.Nil(t, result, "empty attributes should return nil")
}

func TestAttributesToMap_StringValues(t *testing.T) {
	attrs := pcommon.NewMap()
	attrs.PutStr("key1", "value1")
	attrs.PutStr("key2", "value2")

	result := attributesToMap(attrs)
	require.NotNil(t, result)
	assert.Equal(t, "value1", result["key1"])
	assert.Equal(t, "value2", result["key2"])
}

func TestAttributesToMap_MixedTypes(t *testing.T) {
	attrs := pcommon.NewMap()
	attrs.PutStr("str_key", "hello")
	attrs.PutInt("int_key", 42)
	attrs.PutDouble("double_key", 3.14)
	attrs.PutBool("bool_key", true)

	result := attributesToMap(attrs)
	require.NotNil(t, result)
	assert.Equal(t, "hello", result["str_key"])
	assert.Equal(t, int64(42), result["int_key"])
	assert.Equal(t, 3.14, result["double_key"])
	assert.Equal(t, true, result["bool_key"])
}

func TestAttributesToMap_NestedMap(t *testing.T) {
	attrs := pcommon.NewMap()
	attrs.PutStr("top", "level")

	nestedMap := attrs.PutEmptyMap("nested")
	nestedMap.PutStr("inner_key", "inner_value")
	nestedMap.PutInt("inner_num", 100)

	result := attributesToMap(attrs)
	require.NotNil(t, result)
	assert.Equal(t, "level", result["top"])

	nested, ok := result["nested"].(map[string]any)
	require.True(t, ok, "nested should be map[string]any")
	assert.Equal(t, "inner_value", nested["inner_key"])
	assert.Equal(t, int64(100), nested["inner_num"])
}

func TestAttributesToMap_Slice(t *testing.T) {
	attrs := pcommon.NewMap()

	slice := attrs.PutEmptySlice("tags")
	slice.AppendEmpty().SetStr("tag1")
	slice.AppendEmpty().SetStr("tag2")
	slice.AppendEmpty().SetStr("tag3")

	result := attributesToMap(attrs)
	require.NotNil(t, result)

	tags, ok := result["tags"].([]any)
	require.True(t, ok, "tags should be []any")
	assert.Equal(t, []any{"tag1", "tag2", "tag3"}, tags)
}

func TestAttributesToMap_ByteSlice(t *testing.T) {
	attrs := pcommon.NewMap()
	bs := attrs.PutEmptyBytes("binary_data")
	bs.Append(0x01, 0x02, 0x03)

	result := attributesToMap(attrs)
	require.NotNil(t, result)

	data, ok := result["binary_data"].([]byte)
	require.True(t, ok, "binary_data should be []byte")
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, data)
}

func TestValueToAny_AllTypes(t *testing.T) {
	tests := []struct {
		name     string
		setup    func() pcommon.Value
		expected any
	}{
		{
			name: "string",
			setup: func() pcommon.Value {
				v := pcommon.NewValueStr("hello")
				return v
			},
			expected: "hello",
		},
		{
			name: "int",
			setup: func() pcommon.Value {
				v := pcommon.NewValueInt(123)
				return v
			},
			expected: int64(123),
		},
		{
			name: "double",
			setup: func() pcommon.Value {
				v := pcommon.NewValueDouble(2.718)
				return v
			},
			expected: 2.718,
		},
		{
			name: "bool_true",
			setup: func() pcommon.Value {
				v := pcommon.NewValueBool(true)
				return v
			},
			expected: true,
		},
		{
			name: "bool_false",
			setup: func() pcommon.Value {
				v := pcommon.NewValueBool(false)
				return v
			},
			expected: false,
		},
		{
			name: "empty",
			setup: func() pcommon.Value {
				v := pcommon.NewValueEmpty()
				return v
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := tt.setup()
			result := valueToAny(v)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetServiceName_Present(t *testing.T) {
	res := pcommon.NewResource()
	res.Attributes().PutStr("service.name", "my-service")
	res.Attributes().PutStr("host.name", "server-1")

	name := getServiceName(res)
	assert.Equal(t, "my-service", name)
}

func TestGetServiceName_Missing(t *testing.T) {
	res := pcommon.NewResource()
	res.Attributes().PutStr("host.name", "server-1")

	name := getServiceName(res)
	assert.Equal(t, "unknown", name)
}

func TestGetServiceNameFromResource_Present(t *testing.T) {
	res := pcommon.NewResource()
	res.Attributes().PutStr("service.name", "metric-service")

	name := getServiceNameFromResource(res)
	assert.Equal(t, "metric-service", name)
}

func TestGetServiceNameFromResource_Missing(t *testing.T) {
	res := pcommon.NewResource()

	name := getServiceNameFromResource(res)
	assert.Equal(t, "unknown", name)
}

func TestGetServiceNameFromResourceLogs_Present(t *testing.T) {
	res := pcommon.NewResource()
	res.Attributes().PutStr("service.name", "log-service")

	name := getServiceNameFromResourceLogs(res)
	assert.Equal(t, "log-service", name)
}

func TestGetServiceNameFromResourceLogs_Missing(t *testing.T) {
	res := pcommon.NewResource()

	name := getServiceNameFromResourceLogs(res)
	assert.Equal(t, "unknown", name)
}

func TestExtractResourceAttributes(t *testing.T) {
	res := pcommon.NewResource()
	res.Attributes().PutStr("service.name", "test-svc")
	res.Attributes().PutStr("service.version", "1.0.0")
	res.Attributes().PutStr("host.name", "prod-server-01")
	res.Attributes().PutInt("process.pid", 12345)

	result := extractResourceAttributes(res)
	require.NotNil(t, result)
	assert.Equal(t, "test-svc", result["service.name"])
	assert.Equal(t, "1.0.0", result["service.version"])
	assert.Equal(t, "prod-server-01", result["host.name"])
	assert.Equal(t, int64(12345), result["process.pid"])
}

func TestExtractResourceAttributes_Empty(t *testing.T) {
	res := pcommon.NewResource()

	result := extractResourceAttributes(res)
	assert.Nil(t, result)
}

func TestAttributesToMap_DeeplyNested(t *testing.T) {
	attrs := pcommon.NewMap()
	level1 := attrs.PutEmptyMap("level1")
	level2 := level1.PutEmptyMap("level2")
	level2.PutStr("deep_value", "found")

	result := attributesToMap(attrs)
	require.NotNil(t, result)

	l1, ok := result["level1"].(map[string]any)
	require.True(t, ok)
	l2, ok := l1["level2"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "found", l2["deep_value"])
}

func TestAttributesToMap_SliceOfMaps(t *testing.T) {
	attrs := pcommon.NewMap()
	slice := attrs.PutEmptySlice("items")

	item1 := slice.AppendEmpty()
	item1Map := item1.SetEmptyMap()
	item1Map.PutStr("name", "item1")
	item1Map.PutInt("count", 10)

	item2 := slice.AppendEmpty()
	item2Map := item2.SetEmptyMap()
	item2Map.PutStr("name", "item2")
	item2Map.PutInt("count", 20)

	result := attributesToMap(attrs)
	require.NotNil(t, result)

	items, ok := result["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)

	item1Result, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "item1", item1Result["name"])
	assert.Equal(t, int64(10), item1Result["count"])
}

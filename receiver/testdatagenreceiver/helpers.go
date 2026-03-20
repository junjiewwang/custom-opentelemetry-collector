// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package testdatagenreceiver

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

// ParseString 从配置 map 中获取字符串值，支持默认值
func ParseString(cfg map[string]interface{}, key string, defaultVal string) string {
	if v, ok := cfg[key]; ok {
		switch val := v.(type) {
		case string:
			return val
		default:
			return fmt.Sprintf("%v", val)
		}
	}
	return defaultVal
}

// ParseInt 从配置 map 中获取整数值，支持默认值
func ParseInt(cfg map[string]interface{}, key string, defaultVal int) int {
	if v, ok := cfg[key]; ok {
		switch val := v.(type) {
		case int:
			return val
		case int64:
			return int(val)
		case float64:
			return int(val)
		case json.Number:
			if i, err := val.Int64(); err == nil {
				return int(i)
			}
		}
	}
	return defaultVal
}

// ParseFloat64 从配置 map 中获取浮点数值，支持默认值
func ParseFloat64(cfg map[string]interface{}, key string, defaultVal float64) float64 {
	if v, ok := cfg[key]; ok {
		switch val := v.(type) {
		case float64:
			return val
		case float32:
			return float64(val)
		case int:
			return float64(val)
		case int64:
			return float64(val)
		case json.Number:
			if f, err := val.Float64(); err == nil {
				return f
			}
		}
	}
	return defaultVal
}

// ParseBool 从配置 map 中获取布尔值，支持默认值
func ParseBool(cfg map[string]interface{}, key string, defaultVal bool) bool {
	if v, ok := cfg[key]; ok {
		if val, ok := v.(bool); ok {
			return val
		}
	}
	return defaultVal
}

// NewTraceID 生成随机 TraceID
func NewTraceID() pcommon.TraceID {
	var tid [16]byte
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := range tid {
		tid[i] = byte(r.Intn(256))
	}
	return pcommon.TraceID(tid)
}

// NewSpanID 生成随机 SpanID
func NewSpanID() pcommon.SpanID {
	var sid [8]byte
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := range sid {
		sid[i] = byte(r.Intn(256))
	}
	return pcommon.SpanID(sid)
}

// RandomDuration 生成 [min, max] 范围内的随机时间（毫秒）
func RandomDuration(minMs, maxMs int) time.Duration {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	ms := minMs + r.Intn(maxMs-minMs+1)
	return time.Duration(ms) * time.Millisecond
}

// ShouldError 根据错误率决定是否产生错误
func ShouldError(errorRate float64) bool {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return r.Float64() < errorRate
}

// RandomPick 从字符串数组中随机选择一个
func RandomPick(items []string) string {
	if len(items) == 0 {
		return ""
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return items[r.Intn(len(items))]
}

// SetResourceAttributes 批量设置 Resource Attributes
func SetResourceAttributes(attrs pcommon.Map, kvs map[string]string) {
	for k, v := range kvs {
		attrs.PutStr(k, v)
	}
}

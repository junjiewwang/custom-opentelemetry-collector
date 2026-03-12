// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"go.opentelemetry.io/collector/custom/extension/arthastunnelext"
)

func TestFormatArthasExecResult_Succeeded(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "SUCCEEDED",
	}
	result.Body.Command = "version"
	result.Body.Results = []json.RawMessage{
		json.RawMessage(`{"type":"version","version":"3.7.2"}`),
	}

	formatted := formatArthasExecResult("agent-1", "version", result)

	assert.Contains(t, formatted, "agent-1")
	assert.Contains(t, formatted, "version")
	assert.Contains(t, formatted, "SUCCEEDED")
	assert.Contains(t, formatted, "3.7.2")
}

func TestFormatArthasExecResult_Failed(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "FAILED",
	}
	result.Body.Results = []json.RawMessage{
		json.RawMessage(`{"type":"status","statusCode":1,"message":"class not found"}`),
	}

	formatted := formatArthasExecResult("agent-1", "trace NonExist method", result)

	assert.Contains(t, formatted, "命令执行未成功")
	assert.Contains(t, formatted, "FAILED")
}

func TestFormatArthasExecResult_RawState(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "RAW",
	}

	formatted := formatArthasExecResult("agent-1", "version", result)

	assert.Contains(t, formatted, "RAW")
	assert.Contains(t, formatted, "非 JSON 格式")
}

func TestFormatArthasExecResult_WatchResult(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "SUCCEEDED",
	}
	result.Body.Command = "watch com.example.Service method"
	result.Body.Results = []json.RawMessage{
		json.RawMessage(`{"type":"enhancer","success":true,"effect":{"classCount":1,"methodCount":1,"cost":24}}`),
		json.RawMessage(`{"type":"watch","cost":0.033,"ts":1596703454241,"value":{"params":[1],"returnObj":[2,5,17]}}`),
		json.RawMessage(`{"type":"status","statusCode":0,"jobId":3}`),
	}

	formatted := formatArthasExecResult("agent-1", "watch com.example.Service method", result)

	assert.Contains(t, formatted, "SUCCEEDED")
	assert.Contains(t, formatted, "类增强成功")
	assert.Contains(t, formatted, "Watch 结果")
	assert.Contains(t, formatted, "0.033")
	// statusCode 0 should not show error
	assert.NotContains(t, formatted, "命令执行状态")
}

func TestFormatArthasExecResult_StatusError(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "SUCCEEDED",
	}
	result.Body.Results = []json.RawMessage{
		json.RawMessage(`{"type":"status","statusCode":500,"message":"Internal error"}`),
	}

	formatted := formatArthasExecResult("agent-1", "thread", result)

	assert.Contains(t, formatted, "❌")
	assert.Contains(t, formatted, "失败")
	assert.Contains(t, formatted, "Internal error")
}

func TestFormatArthasExecResult_EnhancerFailed(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "SUCCEEDED",
	}
	result.Body.Results = []json.RawMessage{
		json.RawMessage(`{"type":"enhancer","success":false}`),
	}

	formatted := formatArthasExecResult("agent-1", "trace", result)

	assert.Contains(t, formatted, "类增强失败")
}

func TestFormatArthasExecResult_TraceResult(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "SUCCEEDED",
	}
	result.Body.Results = []json.RawMessage{
		json.RawMessage(`{"type":"trace","root":{"className":"com.example.Service","methodName":"create","cost":120.5}}`),
	}

	formatted := formatArthasExecResult("agent-1", "trace com.example.Service create", result)

	assert.Contains(t, formatted, "Trace 调用树")
	assert.Contains(t, formatted, "com.example.Service")
}

func TestFormatArthasExecResult_ThreadResult(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "SUCCEEDED",
	}
	result.Body.Results = []json.RawMessage{
		json.RawMessage(`{"type":"thread","threads":[{"id":1,"name":"main","state":"RUNNABLE"}]}`),
	}

	formatted := formatArthasExecResult("agent-1", "thread", result)

	assert.Contains(t, formatted, "线程信息")
	assert.Contains(t, formatted, "main")
}

func TestFormatArthasExecResult_TimeExpired(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "SUCCEEDED",
	}
	result.Body.TimeExpired = true
	result.Body.Results = []json.RawMessage{}

	formatted := formatArthasExecResult("agent-1", "trace com.example.Service method", result)

	assert.Contains(t, formatted, "超时")
	assert.Contains(t, formatted, "timeExpired")
}

func TestFormatArthasExecResult_EmptyResults(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "SUCCEEDED",
	}
	result.Body.Results = []json.RawMessage{}

	formatted := formatArthasExecResult("agent-1", "version", result)

	assert.Contains(t, formatted, "无结果数据")
}

func TestFormatArthasExecResult_UnknownType(t *testing.T) {
	result := &arthastunnelext.ArthasExecResult{
		State: "SUCCEEDED",
	}
	result.Body.Results = []json.RawMessage{
		json.RawMessage(`{"type":"unknown_custom","data":"hello"}`),
	}

	formatted := formatArthasExecResult("agent-1", "custom_cmd", result)

	assert.Contains(t, formatted, "[unknown_custom]")
	assert.Contains(t, formatted, "hello")
}

func TestFormatRawJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    json.RawMessage
		expected string
	}{
		{
			name:  "valid JSON object",
			input: json.RawMessage(`{"key":"value"}`),
		},
		{
			name:  "valid JSON array",
			input: json.RawMessage(`[1,2,3]`),
		},
		{
			name:     "invalid JSON returns raw",
			input:    json.RawMessage(`not json`),
			expected: "not json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatRawJSON(tt.input)
			if tt.expected != "" {
				assert.Equal(t, tt.expected, result)
			} else {
				assert.True(t, strings.Contains(result, "\n") || len(result) > 0)
			}
		})
	}
}

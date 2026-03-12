// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"encoding/json"
	"fmt"
	"strings"

	"go.opentelemetry.io/collector/custom/extension/arthastunnelext"
)

// formatArthasExecResult formats an Arthas execution result into an AI-friendly text format.
//
// Strategy:
//   - For known result types (watch, trace, thread, status, enhancer): extract and format key information
//   - For unknown types or parse failures: output raw JSON as fallback
func formatArthasExecResult(agentID, command string, result *arthastunnelext.ArthasExecResult) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("## Arthas 命令执行结果\n\n"))
	sb.WriteString(fmt.Sprintf("- **Agent**: %s\n", agentID))
	sb.WriteString(fmt.Sprintf("- **命令**: `%s`\n", command))
	sb.WriteString(fmt.Sprintf("- **状态**: %s\n", result.State))

	if result.State == "RAW" {
		sb.WriteString("\n### 原始输出\n\n")
		sb.WriteString("Arthas 返回了非 JSON 格式的原始输出，请检查命令是否正确。\n")
		return sb.String()
	}

	if result.State != "SUCCEEDED" {
		sb.WriteString(fmt.Sprintf("\n**命令执行未成功**（状态: %s）\n", result.State))
		if len(result.Body.Results) > 0 {
			sb.WriteString("\n### 详细信息\n\n")
			for _, raw := range result.Body.Results {
				sb.WriteString(formatRawJSON(raw))
				sb.WriteString("\n")
			}
		}
		return sb.String()
	}

	sb.WriteString("\n### 结果\n\n")

	if len(result.Body.Results) == 0 {
		sb.WriteString("（无结果数据）\n")
	} else {
		for _, raw := range result.Body.Results {
			// Parse the result item to determine its type
			var item map[string]interface{}
			if err := json.Unmarshal(raw, &item); err != nil {
				sb.WriteString(formatRawJSON(raw))
				sb.WriteString("\n")
				continue
			}

			resultType, _ := item["type"].(string)

			switch resultType {
			case "status":
				formatStatusResult(&sb, item)
			case "enhancer":
				formatEnhancerResult(&sb, item)
			case "watch":
				formatWatchResult(&sb, item)
			case "trace":
				formatTraceResult(&sb, item)
			case "thread":
				formatThreadResult(&sb, item)
			case "version":
				formatVersionResult(&sb, item)
			default:
				// Unknown type: output as formatted JSON
				sb.WriteString(fmt.Sprintf("**[%s]**\n", resultType))
				sb.WriteString("```json\n")
				sb.WriteString(formatRawJSON(raw))
				sb.WriteString("\n```\n\n")
			}
		}
	}

	// timeExpired check is outside the if-else block so it works regardless of whether results are empty
	if result.Body.TimeExpired {
		sb.WriteString("\n> ⚠️ 命令执行超时（timeExpired）\n")
	}

	return sb.String()
}

// formatStatusResult formats a status-type result item.
func formatStatusResult(sb *strings.Builder, item map[string]interface{}) {
	statusCode, _ := item["statusCode"].(float64)
	message, _ := item["message"].(string)

	if statusCode != 0 {
		sb.WriteString(fmt.Sprintf("❌ **命令执行状态**: 失败 (code=%d)\n", int(statusCode)))
		if message != "" {
			sb.WriteString(fmt.Sprintf("   错误信息: %s\n", message))
		}
	}
	// statusCode == 0 means success, no need to show
}

// formatEnhancerResult formats an enhancer-type result item.
func formatEnhancerResult(sb *strings.Builder, item map[string]interface{}) {
	success, _ := item["success"].(bool)
	effect, _ := item["effect"].(map[string]interface{})

	if !success {
		sb.WriteString("❌ **类增强失败**\n")
		return
	}

	if effect != nil {
		classCount, _ := effect["classCount"].(float64)
		methodCount, _ := effect["methodCount"].(float64)
		cost, _ := effect["cost"].(float64)
		sb.WriteString(fmt.Sprintf("✅ **类增强成功**: 影响 %d 个类、%d 个方法（耗时 %.0fms）\n",
			int(classCount), int(methodCount), cost))
	}
}

// formatWatchResult formats a watch-type result item.
func formatWatchResult(sb *strings.Builder, item map[string]interface{}) {
	ts, _ := item["ts"].(float64)
	cost, _ := item["cost"].(float64)
	value := item["value"]

	sb.WriteString("#### Watch 结果\n\n")
	sb.WriteString(fmt.Sprintf("- **耗时**: %.3fms\n", cost))
	if ts > 0 {
		sb.WriteString(fmt.Sprintf("- **时间戳**: %.0f\n", ts))
	}

	if value != nil {
		valueJSON, err := json.MarshalIndent(value, "", "  ")
		if err == nil {
			sb.WriteString("\n**观测值**:\n```json\n")
			sb.WriteString(string(valueJSON))
			sb.WriteString("\n```\n\n")
		}
	}
}

// formatTraceResult formats a trace-type result item.
func formatTraceResult(sb *strings.Builder, item map[string]interface{}) {
	sb.WriteString("#### Trace 调用树\n\n")

	// Trace results typically contain a tree structure
	// Try to format it as a readable tree
	valueJSON, err := json.MarshalIndent(item, "", "  ")
	if err == nil {
		sb.WriteString("```json\n")
		sb.WriteString(string(valueJSON))
		sb.WriteString("\n```\n\n")
	}
}

// formatThreadResult formats a thread-type result item.
func formatThreadResult(sb *strings.Builder, item map[string]interface{}) {
	sb.WriteString("#### 线程信息\n\n")

	// Thread results contain thread list or specific thread info
	valueJSON, err := json.MarshalIndent(item, "", "  ")
	if err == nil {
		sb.WriteString("```json\n")
		sb.WriteString(string(valueJSON))
		sb.WriteString("\n```\n\n")
	}
}

// formatVersionResult formats a version-type result item.
func formatVersionResult(sb *strings.Builder, item map[string]interface{}) {
	version, _ := item["version"].(string)
	if version != "" {
		sb.WriteString(fmt.Sprintf("**Arthas 版本**: %s\n\n", version))
	}
}

// formatRawJSON formats raw JSON bytes into a pretty-printed string.
func formatRawJSON(data json.RawMessage) string {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(data)
	}
	return string(pretty)
}

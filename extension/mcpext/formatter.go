// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
)

// formatAgentList formats a list of agents into a human-readable text for AI consumption.
func formatAgentList(agents []*agentregistry.AgentInfo) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("在线 Agent 列表（共 %d 个）:\n\n", len(agents)))

	for i, agent := range agents {
		sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, agent.AgentID))

		if agent.AppID != "" {
			sb.WriteString(fmt.Sprintf("   - 应用 ID: %s\n", agent.AppID))
		}
		if agent.ServiceName != "" && agent.ServiceName != "_unknown" {
			sb.WriteString(fmt.Sprintf("   - 服务名: %s\n", agent.ServiceName))
		}
		if agent.IP != "" {
			sb.WriteString(fmt.Sprintf("   - IP: %s\n", agent.IP))
		}
		if agent.Hostname != "" {
			sb.WriteString(fmt.Sprintf("   - 主机名: %s\n", agent.Hostname))
		}
		if agent.Version != "" {
			sb.WriteString(fmt.Sprintf("   - 版本: %s\n", agent.Version))
		}
		if agent.Status != nil {
			sb.WriteString(fmt.Sprintf("   - 状态: %s\n", agent.Status.State))
		}

		sb.WriteString("\n")
	}

	return sb.String()
}

// formatAgentInfo formats agent details into a comprehensive text for AI consumption.
func formatAgentInfo(agent *agentregistry.AgentInfo) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Agent 详细信息: %s\n", agent.AgentID))
	sb.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n")

	// Basic info
	sb.WriteString("基本信息:\n")
	sb.WriteString(fmt.Sprintf("  - Agent ID: %s\n", agent.AgentID))
	if agent.AppID != "" {
		sb.WriteString(fmt.Sprintf("  - 应用 ID: %s\n", agent.AppID))
	}
	if agent.ServiceName != "" && agent.ServiceName != "_unknown" {
		sb.WriteString(fmt.Sprintf("  - 服务名: %s\n", agent.ServiceName))
	}
	if agent.IP != "" {
		sb.WriteString(fmt.Sprintf("  - IP: %s\n", agent.IP))
	}
	if agent.Hostname != "" {
		sb.WriteString(fmt.Sprintf("  - 主机名: %s\n", agent.Hostname))
	}
	if agent.Version != "" {
		sb.WriteString(fmt.Sprintf("  - Agent 版本: %s\n", agent.Version))
	}
	if agent.StartTime > 0 {
		t := time.UnixMilli(agent.StartTime)
		sb.WriteString(fmt.Sprintf("  - 启动时间: %s\n", t.Format("2006-01-02 15:04:05")))
	}
	if agent.RegisteredAt > 0 {
		t := time.UnixMilli(agent.RegisteredAt)
		sb.WriteString(fmt.Sprintf("  - 注册时间: %s\n", t.Format("2006-01-02 15:04:05")))
	}
	if agent.LastHeartbeat > 0 {
		t := time.UnixMilli(agent.LastHeartbeat)
		sb.WriteString(fmt.Sprintf("  - 最后心跳: %s\n", t.Format("2006-01-02 15:04:05")))
	}
	sb.WriteString("\n")

	// Status info
	if agent.Status != nil {
		sb.WriteString("运行状态:\n")
		sb.WriteString(fmt.Sprintf("  - 状态: %s\n", agent.Status.State))
		if agent.Status.ConfigVersion != "" {
			sb.WriteString(fmt.Sprintf("  - 配置版本: %s\n", agent.Status.ConfigVersion))
		}
		if agent.Status.CurrentTask != "" {
			sb.WriteString(fmt.Sprintf("  - 当前任务: %s\n", agent.Status.CurrentTask))
		}

		// Health status
		if agent.Status.Health != nil {
			sb.WriteString(fmt.Sprintf("  - 健康状态: %s\n", agent.Status.Health.State.String()))
			if agent.Status.Health.SuccessRate > 0 {
				sb.WriteString(fmt.Sprintf("  - 成功率: %.1f%%\n", agent.Status.Health.SuccessRate*100))
			}
		}

		// Metrics
		if agent.Status.Metrics != nil {
			m := agent.Status.Metrics
			if m.CPUUsagePercent > 0 {
				sb.WriteString(fmt.Sprintf("  - CPU 使用率: %.1f%%\n", m.CPUUsagePercent))
			}
			if m.MemoryUsageMB > 0 {
				sb.WriteString(fmt.Sprintf("  - 内存使用: %.1f MB\n", m.MemoryUsageMB))
			}
			if m.UptimeSeconds > 0 {
				sb.WriteString(fmt.Sprintf("  - 运行时长: %s\n", formatDurationSeconds(m.UptimeSeconds)))
			}
			if m.TracesReceived > 0 {
				sb.WriteString(fmt.Sprintf("  - 接收 Traces: %d\n", m.TracesReceived))
			}
			if m.MetricsReceived > 0 {
				sb.WriteString(fmt.Sprintf("  - 接收 Metrics: %d\n", m.MetricsReceived))
			}
			if m.LogsReceived > 0 {
				sb.WriteString(fmt.Sprintf("  - 接收 Logs: %d\n", m.LogsReceived))
			}
			if m.TasksCompleted > 0 {
				sb.WriteString(fmt.Sprintf("  - 完成任务: %d\n", m.TasksCompleted))
			}
			if m.TasksFailed > 0 {
				sb.WriteString(fmt.Sprintf("  - 失败任务: %d\n", m.TasksFailed))
			}
		}
		sb.WriteString("\n")
	}

	// Labels
	if len(agent.Labels) > 0 {
		sb.WriteString("标签:\n")
		for k, v := range agent.Labels {
			sb.WriteString(fmt.Sprintf("  - %s: %s\n", k, v))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// formatArthasStatus formats Arthas status check result.
func formatArthasStatus(agentID, status, message string) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("Arthas 状态检查: %s\n\n", agentID))
	sb.WriteString(fmt.Sprintf("状态: %s\n", status))
	sb.WriteString(fmt.Sprintf("说明: %s\n", message))

	return sb.String()
}

// formatDurationSeconds formats seconds into human-readable duration.
func formatDurationSeconds(seconds int64) string {
	d := seconds / (60 * 60 * 24)
	h := (seconds / (60 * 60)) % 24
	m := (seconds / 60) % 60

	if d > 0 {
		return fmt.Sprintf("%d天%d小时%d分钟", d, h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%d小时%d分钟", h, m)
	}
	return fmt.Sprintf("%d分钟", m)
}

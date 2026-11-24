// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ========== Tool: arthas_attach ==========

func (w *mcpServerWrapper) registerArthasAttachTool() {
	tool := mcp.NewTool("arthas_attach",
		mcp.WithDescription(attachToolDescription),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。可通过 list_agents 工具获取。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasAttach)
}

func (w *mcpServerWrapper) handleArthasAttach(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, ok := args["agent_id"].(string)
	if !ok || agentID == "" {
		return mcp.NewToolResultError("参数 agent_id 不能为空"), nil
	}

	w.logger.Info("[arthas_attach] 执行",
		zap.String("agent_id", agentID),
	)

	// 命令工具自带 auto_attach，只有明确需要交互式 Tunnel 会话时才需要 attach。
	if w.ext.arthasTunnel.IsAgentConnected(agentID) {
		w.logger.Info("[arthas_attach] Agent 已连接到 Tunnel",
			zap.String("agent_id", agentID),
		)
		return mcp.NewToolResultText(fmt.Sprintf(
			"## Arthas Attach 结果\n\n"+
				"- **Agent**: %s\n"+
				"- **状态**: Tunnel 已注册 ✅\n"+
				"- **说明**: 交互式 Web Terminal 可用；结构化命令本就不需要 Tunnel。",
			agentID,
		)), nil
	}

	// 通过 Orchestrator 执行 attach（支持自动重试）
	resp, err := w.orchestrator.Attach(ctx, agentID)
	if err != nil {
		w.logger.Error("[arthas_attach] 执行失败",
			zap.String("agent_id", agentID),
			zap.Error(err),
		)
		return mcp.NewToolResultError("Arthas attach 失败: " + err.Error()), nil
	}

	if resp.Parsed != nil && !resp.Parsed.Success {
		return mcp.NewToolResultError(fmt.Sprintf(
			"Arthas attach 失败:\n- Agent: %s\n- 错误码: %s\n- 错误: %s",
			agentID, resp.Parsed.ErrorCode, resp.Parsed.ErrorMessage,
		)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"## Arthas Attach 请求已受理\n\n"+
			"- **Agent**: %s\n"+
			"- **accepted**: `%v`\n"+
			"- **pending**: `%v`\n"+
			"- **状态**: %s ⏳\n"+
			"- **任务 ID**: %s\n"+
			"- **确认方式**: %s\n\n"+
			"%s\n\n"+
			"> Runtime 就绪可用 `arthas_status` 的 arthas_runtime 或 `arthas_exec('version')` 确认；"+
			"只有需要交互式 Web Terminal 时才等待 tunnel_registered。",
		agentID, resp.Accepted, resp.Pending, resp.State, resp.TaskID, resp.ConfirmationMode, resp.ConfirmationHint,
	)), nil
}

// ========== Tool: arthas_detach ==========

func (w *mcpServerWrapper) registerArthasDetachTool() {
	tool := mcp.NewTool("arthas_detach",
		mcp.WithDescription(
			"停止目标 Agent 上运行的 Arthas 进程并释放资源。"+
				"在完成诊断后应调用此工具清理 Arthas，避免长时间占用目标 JVM 资源。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。可通过 list_agents 工具获取。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasDetach)
}

func (w *mcpServerWrapper) handleArthasDetach(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, ok := args["agent_id"].(string)
	if !ok || agentID == "" {
		return mcp.NewToolResultError("参数 agent_id 不能为空"), nil
	}

	w.logger.Info("[arthas_detach] 执行", zap.String("agent_id", agentID))

	// 通过 Orchestrator 执行 detach
	resp, err := w.orchestrator.Detach(ctx, agentID)
	if err != nil {
		return mcp.NewToolResultError("Arthas detach 失败: " + err.Error()), nil
	}

	parsed := resp.Parsed
	if parsed.Success {
		return mcp.NewToolResultText(fmt.Sprintf(
			"## Arthas Detach 结果\n\n"+
				"- **Agent**: %s\n"+
				"- **状态**: 已停止 ✅\n"+
				"- **任务 ID**: %s\n"+
				"- **说明**: Arthas 已从目标 JVM 分离并释放资源。",
			agentID, resp.TaskID,
		)), nil
	}

	return mcp.NewToolResultError(fmt.Sprintf(
		"Arthas detach 失败:\n- Agent: %s\n- 错误码: %s\n- 错误: %s",
		agentID, parsed.ErrorCode, parsed.ErrorMessage,
	)), nil
}
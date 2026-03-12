// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// ========== Tool: arthas_attach ==========

func (w *mcpServerWrapper) registerArthasAttachTool() {
	tool := mcp.NewTool("arthas_attach",
		mcp.WithDescription(
			"在目标 Agent 上启动 Arthas 并连接到 Collector 的 Tunnel Server。"+
				"这是执行任何 Arthas 命令的前提条件。调用后会通过 ControlPlane 下发 arthas_attach 任务到目标 Java Agent，"+
				"Java Agent 会启动 Arthas 进程并将其连接到 Tunnel。"+
				"操作完成后，可以通过 arthas_status 验证 Arthas 是否已成功连接。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。可通过 list_agents 工具获取。"),
		),
		mcp.WithString("pid",
			mcp.Description("目标 JVM 的进程 ID。可选，默认 attach 到 Agent 自身的 JVM。"),
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

	pid, _ := args["pid"].(string)

	w.logger.Info("[arthas_attach] Executing",
		zap.String("agent_id", agentID),
		zap.String("pid", pid),
	)

	// Check if agent exists
	agent, err := w.ext.controlPlane.GetAgent(ctx, agentID)
	if err != nil {
		w.logger.Error("[arthas_attach] Failed to get agent", zap.String("agent_id", agentID), zap.Error(err))
		return mcp.NewToolResultError("获取 Agent 信息失败: " + err.Error()), nil
	}
	if agent == nil {
		return mcp.NewToolResultError("Agent '" + agentID + "' 不存在或已离线"), nil
	}

	// Check if already attached
	if w.ext.arthasTunnel.IsAgentConnected(agentID) {
		w.logger.Info("[arthas_attach] Agent already connected to tunnel",
			zap.String("agent_id", agentID),
		)
		return mcp.NewToolResultText(fmt.Sprintf(
			"## Arthas Attach 结果\n\n"+
				"- **Agent**: %s\n"+
				"- **状态**: 已连接 ✅\n"+
				"- **说明**: Arthas 已经在运行并连接到 Tunnel，无需重复 attach。可以直接执行 Arthas 命令。",
			agentID,
		)), nil
	}

	// Build task parameters
	params := map[string]interface{}{
		"action": "attach",
	}
	if pid != "" {
		params["pid"] = pid
	}
	paramsJSON, _ := json.Marshal(params)

	// Submit task via ControlPlane
	task := &model.Task{
		ID:            uuid.New().String(),
		TypeName:      "arthas_attach",
		ParametersJSON: paramsJSON,
		TimeoutMillis: 60000, // 60 seconds
		TargetAgentID: agentID,
	}

	if err := w.ext.controlPlane.SubmitTaskForAgent(ctx, agentID, task); err != nil {
		w.logger.Error("[arthas_attach] Failed to submit task",
			zap.String("agent_id", agentID),
			zap.Error(err),
		)
		return mcp.NewToolResultError("下发 arthas_attach 任务失败: " + err.Error()), nil
	}

	w.logger.Info("[arthas_attach] Task submitted, waiting for result",
		zap.String("agent_id", agentID),
		zap.String("task_id", task.ID),
	)

	// Wait for task result with polling
	result, err := w.waitForTaskResult(ctx, task.ID, 60*time.Second)
	if err != nil {
		return mcp.NewToolResultError("等待 arthas_attach 结果超时: " + err.Error()), nil
	}

	if result.Status == model.TaskStatusSuccess {
		// Verify tunnel connection
		time.Sleep(2 * time.Second) // Brief delay for tunnel registration
		connected := w.ext.arthasTunnel.IsAgentConnected(agentID)

		var tunnelStatus string
		if connected {
			tunnelStatus = "已连接 ✅"
		} else {
			tunnelStatus = "等待连接中 ⏳（Arthas 可能仍在启动中，请稍后通过 arthas_status 检查）"
		}

		return mcp.NewToolResultText(fmt.Sprintf(
			"## Arthas Attach 结果\n\n"+
				"- **Agent**: %s\n"+
				"- **任务状态**: 成功 ✅\n"+
				"- **Tunnel 状态**: %s\n"+
				"- **任务 ID**: %s\n\n"+
				"如果 Tunnel 尚未连接，请等待几秒后调用 `arthas_status` 检查。",
			agentID, tunnelStatus, task.ID,
		)), nil
	}

	// Task failed
	errMsg := result.ErrorMessage
	if errMsg == "" {
		errMsg = "未知错误"
	}
	return mcp.NewToolResultError(fmt.Sprintf(
		"Arthas attach 失败:\n- Agent: %s\n- 错误: %s\n- 错误码: %s",
		agentID, errMsg, result.ErrorCode,
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

	w.logger.Info("[arthas_detach] Executing", zap.String("agent_id", agentID))

	// Check if agent exists
	agent, err := w.ext.controlPlane.GetAgent(ctx, agentID)
	if err != nil {
		return mcp.NewToolResultError("获取 Agent 信息失败: " + err.Error()), nil
	}
	if agent == nil {
		return mcp.NewToolResultError("Agent '" + agentID + "' 不存在或已离线"), nil
	}

	// Build task
	params := map[string]interface{}{
		"action": "detach",
	}
	paramsJSON, _ := json.Marshal(params)

	task := &model.Task{
		ID:            uuid.New().String(),
		TypeName:      "arthas_detach",
		ParametersJSON: paramsJSON,
		TimeoutMillis: 30000, // 30 seconds
		TargetAgentID: agentID,
	}

	if err := w.ext.controlPlane.SubmitTaskForAgent(ctx, agentID, task); err != nil {
		return mcp.NewToolResultError("下发 arthas_detach 任务失败: " + err.Error()), nil
	}

	// Wait for result
	result, err := w.waitForTaskResult(ctx, task.ID, 30*time.Second)
	if err != nil {
		return mcp.NewToolResultError("等待 arthas_detach 结果超时: " + err.Error()), nil
	}

	if result.Status == model.TaskStatusSuccess {
		return mcp.NewToolResultText(fmt.Sprintf(
			"## Arthas Detach 结果\n\n"+
				"- **Agent**: %s\n"+
				"- **状态**: 已停止 ✅\n"+
				"- **说明**: Arthas 已从目标 JVM 分离并释放资源。",
			agentID,
		)), nil
	}

	errMsg := result.ErrorMessage
	if errMsg == "" {
		errMsg = "未知错误"
	}
	return mcp.NewToolResultError(fmt.Sprintf(
		"Arthas detach 失败:\n- Agent: %s\n- 错误: %s",
		agentID, errMsg,
	)), nil
}

// waitForTaskResult polls for task result until success, failure, or timeout.
func (w *mcpServerWrapper) waitForTaskResult(ctx context.Context, taskID string, timeout time.Duration) (*model.TaskResult, error) {
	deadline := time.After(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("timeout after %v", timeout)
		case <-ticker.C:
			result, found := w.ext.controlPlane.GetTaskResult(taskID)
			if !found {
				continue
			}
			// Check if task is in a terminal state
			switch result.Status {
			case model.TaskStatusSuccess, model.TaskStatusFailed, model.TaskStatusTimeout, model.TaskStatusCancelled:
				return result, nil
			case model.TaskStatusRunning, model.TaskStatusPending:
				continue // Still in progress
			default:
				return result, nil
			}
		}
	}
}

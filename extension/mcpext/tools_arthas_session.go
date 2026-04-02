// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ========== Arthas Session MCP 工具 ==========
//
// 提供异步会话化的 Arthas 命令执行能力：
//   - arthas_session_open: 创建异步会话
//   - arthas_session_exec: 在会话中启动异步命令
//   - arthas_session_pull: 拉取增量结果
//   - arthas_session_interrupt: 中断异步任务
//   - arthas_session_close: 关闭会话
//
// 适用于 trace/watch 等需要等待方法调用触发的长命令。

// ========== Tool: arthas_session_open ==========

func (w *mcpServerWrapper) registerArthasSessionOpenTool() {
	tool := mcp.NewTool("arthas_session_open",
		mcp.WithDescription(
			"创建一个 Arthas 异步会话。异步会话适用于 trace、watch 等需要等待方法调用触发的长命令。\n\n"+
				"创建会话后，可以通过 arthas_session_exec 在会话中启动异步命令，"+
				"通过 arthas_session_pull 多轮拉取增量结果，"+
				"通过 arthas_session_interrupt 中断正在执行的命令，"+
				"最后通过 arthas_session_close 关闭会话释放资源。\n\n"+
				"会话有 TTL（总存活时间）和 idle timeout（空闲超时），超时后会自动回收。\n\n"+
				"前提条件：Arthas 必须已 attach 到目标 Agent。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。可通过 list_agents 工具获取。"),
		),
		mcp.WithNumber("ttl_seconds",
			mcp.Description("会话总存活时间（秒）。默认 600（10 分钟）。超过此时间会话将自动关闭。"),
		),
		mcp.WithNumber("idle_timeout_seconds",
			mcp.Description("空闲超时（秒）。默认 120（2 分钟）。长时间未操作会话将自动关闭。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasSessionOpen)
}

func (w *mcpServerWrapper) handleArthasSessionOpen(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, _ := args["agent_id"].(string)
	ttlSeconds, _ := args["ttl_seconds"].(float64)
	idleTimeoutSeconds, _ := args["idle_timeout_seconds"].(float64)

	if agentID == "" {
		return mcp.NewToolResultError("参数 agent_id 不能为空"), nil
	}

	w.logger.Info("[arthas_session_open] 创建会话",
		zap.String("agent_id", agentID),
	)

	var ttl, idleTimeout time.Duration
	if ttlSeconds > 0 {
		ttl = time.Duration(ttlSeconds) * time.Second
	}
	if idleTimeoutSeconds > 0 {
		idleTimeout = time.Duration(idleTimeoutSeconds) * time.Second
	}

	// 使用独立 context，不受 MCP 客户端超时影响
	// session_open 需要较长时间（任务提交 + Agent 执行 + 结果回传 + 可能的重试）
	opCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := w.sessionOrchestrator.OpenSession(opCtx, &OpenSessionRequest{
		AgentID:     agentID,
		TTL:         ttl,
		IdleTimeout: idleTimeout,
	})
	if err != nil {
		return mcp.NewToolResultError("创建会话失败: " + err.Error()), nil
	}

	if resp.Error != nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"创建会话失败:\n- Agent: %s\n- 错误码: %s\n- 错误: %s",
			agentID, resp.Error.Code, resp.Error.Message,
		)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"## Arthas 异步会话已创建\n\n"+
			"- **会话 ID**: `%s`\n"+
			"- **Agent**: %s\n"+
			"- **状态**: %s ✅\n"+
			"- **消费者 ID**: %s\n\n"+
			"### 使用说明\n\n"+
			"1. 使用 `arthas_session_exec` 在此会话中启动异步命令（如 trace、watch）\n"+
			"2. 使用 `arthas_session_pull` 多轮拉取增量结果\n"+
			"3. 使用 `arthas_session_interrupt` 中断正在执行的命令\n"+
			"4. 使用 `arthas_session_close` 关闭会话释放资源\n\n"+
			"> 会话将在 TTL 超时或空闲超时后自动回收。",
		resp.CollectorSessionID, agentID, resp.State, resp.ConsumerID,
	)), nil
}

// ========== Tool: arthas_session_exec ==========

func (w *mcpServerWrapper) registerArthasSessionExecTool() {
	tool := mcp.NewTool("arthas_session_exec",
		mcp.WithDescription(
			"在指定的异步会话中启动一个 Arthas 命令。\n\n"+
				"适用于 trace、watch、stack 等需要等待方法调用触发的命令。"+
				"命令提交成功后会立即返回受理结果，后续请使用 arthas_session_pull 拉取增量结果。\n\n"+
				"注意：每个会话同一时间只能执行一个命令。如需执行新命令，请先 interrupt 当前命令。"),
		mcp.WithString("session_id",
			mcp.Required(),
			mcp.Description("会话 ID。通过 arthas_session_open 获取。"),
		),
		mcp.WithString("command",
			mcp.Required(),
			mcp.Description("要执行的 Arthas 命令。例如: 'trace com.example.OrderService createOrder'"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasSessionExec)
}

func (w *mcpServerWrapper) handleArthasSessionExec(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	sessionID, _ := args["session_id"].(string)
	command, _ := args["command"].(string)

	if sessionID == "" {
		return mcp.NewToolResultError("参数 session_id 不能为空"), nil
	}
	if command == "" {
		return mcp.NewToolResultError("参数 command 不能为空"), nil
	}

	w.logger.Info("[arthas_session_exec] 执行异步命令",
		zap.String("session_id", sessionID),
		zap.String("command", command),
	)

	// 使用独立 context，不受 MCP 客户端超时影响
	opCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := w.sessionOrchestrator.ExecuteAsync(opCtx, &ExecAsyncRequest{
		CollectorSessionID: sessionID,
		Command:            command,
	})
	if err != nil {
		return mcp.NewToolResultError("执行异步命令失败: " + err.Error()), nil
	}

	if resp.Error != nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"执行异步命令失败:\n- 会话: %s\n- 命令: `%s`\n- 错误码: %s\n- 错误: %s",
			sessionID, command, resp.Error.Code, resp.Error.Message,
		)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"## 异步命令已受理\n\n"+
			"- **会话 ID**: `%s`\n"+
			"- **命令**: `%s`\n"+
			"- **accepted**: `%v`\n"+
			"- **pending**: `%v`\n"+
			"- **状态**: %s ⏳\n"+
			"- **任务 ID**: %s\n"+
			"- **确认方式**: %s\n\n"+
			"%s\n\n"+
			"> 不要等待本次调用直接返回最终结果；请通过 `arthas_session_pull` 观察命令是否开始产出数据及何时结束。",
		sessionID, command, resp.Accepted, resp.Pending, resp.State, resp.TaskID, resp.ConfirmationMode, resp.ConfirmationHint,
	)), nil
}

// ========== Tool: arthas_session_pull ==========

func (w *mcpServerWrapper) registerArthasSessionPullTool() {
	tool := mcp.NewTool("arthas_session_pull",
		mcp.WithDescription(
			"从异步会话中拉取增量结果。\n\n"+
				"每次调用返回自上次 pull 以来的新增结果。"+
				"如果没有新数据，返回空结果（不视为错误）。\n\n"+
				"当返回 `endOfStream=true` 时，表示当前异步命令已执行完成。\n\n"+
				"建议轮询间隔：1-3 秒，单次等待尽量保持短轮询。"),
		mcp.WithString("session_id",
			mcp.Required(),
			mcp.Description("会话 ID。通过 arthas_session_open 获取。"),
		),
		mcp.WithNumber("wait_timeout_seconds",
			mcp.Description("等待超时（秒）。默认 3。在此时间内如果有新数据会立即返回。建议保持在 1-3 秒，最多 5-6 秒。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasSessionPull)
}

func (w *mcpServerWrapper) handleArthasSessionPull(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	sessionID, _ := args["session_id"].(string)
	waitTimeoutSeconds, _ := args["wait_timeout_seconds"].(float64)

	if sessionID == "" {
		return mcp.NewToolResultError("参数 session_id 不能为空"), nil
	}

	var waitTimeoutMs int64
	if waitTimeoutSeconds > 0 {
		waitTimeoutMs = int64(waitTimeoutSeconds * 1000)
	}

	// 使用独立 context，不受 MCP 客户端超时影响
	// pull 超时由 waitTimeoutMs 参数控制
	opCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := w.sessionOrchestrator.PullResults(opCtx, &PullResultsRequest{
		CollectorSessionID: sessionID,
		WaitTimeoutMs:      waitTimeoutMs,
	})
	if err != nil {
		return mcp.NewToolResultError("拉取结果失败: " + err.Error()), nil
	}

	if resp.Error != nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"拉取结果失败:\n- 会话: %s\n- 错误码: %s\n- 错误: %s",
			sessionID, resp.Error.Code, resp.Error.Message,
		)), nil
	}

	// 格式化 delta 结果
	return mcp.NewToolResultText(formatSessionPullResult(sessionID, resp.Delta)), nil
}

// ========== Tool: arthas_session_interrupt ==========

func (w *mcpServerWrapper) registerArthasSessionInterruptTool() {
	tool := mcp.NewTool("arthas_session_interrupt",
		mcp.WithDescription(
			"中断异步会话中正在执行的命令。\n\n"+
				"中断后会话回到空闲状态，可以执行新的命令。"+
				"已产生的结果不会丢失，仍可通过 pull 获取。"),
		mcp.WithString("session_id",
			mcp.Required(),
			mcp.Description("会话 ID。通过 arthas_session_open 获取。"),
		),
		mcp.WithString("reason",
			mcp.Description("中断原因。可选。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasSessionInterrupt)
}

func (w *mcpServerWrapper) handleArthasSessionInterrupt(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	sessionID, _ := args["session_id"].(string)
	reason, _ := args["reason"].(string)

	if sessionID == "" {
		return mcp.NewToolResultError("参数 session_id 不能为空"), nil
	}

	w.logger.Info("[arthas_session_interrupt] 中断命令",
		zap.String("session_id", sessionID),
		zap.String("reason", reason),
	)

	// 使用独立 context，不受 MCP 客户端超时影响
	opCtx, cancel := context.WithTimeout(context.Background(), w.sessionOrchestrator.interruptOperationTimeout())
	defer cancel()

	resp, err := w.sessionOrchestrator.Interrupt(opCtx, &InterruptRequest{
		CollectorSessionID: sessionID,
		Reason:             reason,
	})
	if err != nil {
		return mcp.NewToolResultError("中断命令失败: " + err.Error()), nil
	}

	if resp.Error != nil {
		return mcp.NewToolResultError(fmt.Sprintf(
			"中断命令失败:\n- 会话: %s\n- 错误码: %s\n- 错误: %s",
			sessionID, resp.Error.Code, resp.Error.Message,
		)), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"## 中断请求已受理\n\n"+
			"- **会话 ID**: `%s`\n"+
			"- **accepted**: `%v`\n"+
			"- **pending**: `%v`\n"+
			"- **状态**: %s\n"+
			"- **任务 ID**: %s\n"+
			"- **确认方式**: %s\n\n"+
			"%s\n\n"+
			"> 如果会话随后回到 idle，或再次中断时返回“无前台任务”，都表示目标 job 已不再运行。",
		sessionID, resp.Accepted, resp.Pending, resp.State, resp.TaskID, resp.ConfirmationMode, resp.ConfirmationHint,
	)), nil
}

// ========== Tool: arthas_session_close ==========

func (w *mcpServerWrapper) registerArthasSessionCloseTool() {
	tool := mcp.NewTool("arthas_session_close",
		mcp.WithDescription(
			"关闭异步会话并释放资源。\n\n"+
				"在完成诊断后应关闭会话，避免长时间占用 Agent 资源。"+
				"如果会话中有正在执行的命令，会先中断再关闭。"),
		mcp.WithString("session_id",
			mcp.Required(),
			mcp.Description("会话 ID。通过 arthas_session_open 获取。"),
		),
		mcp.WithString("reason",
			mcp.Description("关闭原因。可选。"),
		),
		mcp.WithBoolean("force",
			mcp.Description("是否强制关闭。默认 false。设为 true 时即使 Agent 侧关闭失败也会在 Collector 侧标记为已关闭。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasSessionClose)
}

func (w *mcpServerWrapper) handleArthasSessionClose(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	sessionID, _ := args["session_id"].(string)
	reason, _ := args["reason"].(string)
	force, _ := args["force"].(bool)

	if sessionID == "" {
		return mcp.NewToolResultError("参数 session_id 不能为空"), nil
	}

	startTime := time.Now()
	w.logger.Info("[arthas_session_close] 关闭会话",
		zap.String("session_id", sessionID),
		zap.String("reason", reason),
		zap.Bool("force", force),
	)

	// 使用独立 context，不受 MCP 客户端超时影响
	opCtx, cancel := context.WithTimeout(context.Background(), w.sessionOrchestrator.closeOperationTimeout())
	defer cancel()

	resp, err := w.sessionOrchestrator.CloseSession(opCtx, &CloseSessionRequest{
		CollectorSessionID: sessionID,
		Reason:             reason,
		Force:              force,
	})
	if err != nil {
		w.logger.Warn("[arthas_session_close] 关闭会话失败",
			zap.String("session_id", sessionID),
			zap.String("reason", reason),
			zap.Bool("force", force),
			zap.Duration("elapsed", time.Since(startTime)),
			zap.Error(err),
		)
		return mcp.NewToolResultError("关闭会话失败: " + err.Error()), nil
	}

	if resp.Error != nil && !resp.Closed {
		w.logger.Warn("[arthas_session_close] 关闭会话返回业务错误",
			zap.String("session_id", sessionID),
			zap.String("task_id", resp.TaskID),
			zap.String("reason", reason),
			zap.Bool("force", force),
			zap.Duration("elapsed", time.Since(startTime)),
			zap.String("error_code", resp.Error.Code),
			zap.String("error_message", resp.Error.Message),
		)
		return mcp.NewToolResultError(fmt.Sprintf(
			"关闭会话失败:\n- 会话: %s\n- 错误码: %s\n- 错误: %s",
			sessionID, resp.Error.Code, resp.Error.Message,
		)), nil
	}

	var note string
	if resp.Error != nil {
		note = fmt.Sprintf("\n\n> ⚠️ Agent 侧关闭时出现错误: [%s] %s（Collector 侧已强制关闭）", resp.Error.Code, resp.Error.Message)
		w.logger.Warn("[arthas_session_close] 关闭会话完成（含远端错误）",
			zap.String("session_id", sessionID),
			zap.String("task_id", resp.TaskID),
			zap.String("reason", reason),
			zap.Bool("force", force),
			zap.Bool("closed", resp.Closed),
			zap.Bool("has_error", true),
			zap.String("error_code", resp.Error.Code),
			zap.String("error_message", resp.Error.Message),
			zap.Duration("elapsed", time.Since(startTime)),
		)
	} else {
		w.logger.Info("[arthas_session_close] 关闭会话完成",
			zap.String("session_id", sessionID),
			zap.String("task_id", resp.TaskID),
			zap.String("reason", reason),
			zap.Bool("force", force),
			zap.Bool("closed", resp.Closed),
			zap.Bool("has_error", false),
			zap.Duration("elapsed", time.Since(startTime)),
		)
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"## 会话关闭请求已受理\n\n"+
			"- **会话 ID**: `%s`\n"+
			"- **accepted**: `%v`\n"+
			"- **pending**: `%v`\n"+
			"- **状态**: %s\n"+
			"- **任务 ID**: %s\n"+
			"- **确认方式**: %s\n"+
			"- **说明**: %s%s",
		sessionID, resp.Accepted, resp.Pending, resp.State, resp.TaskID, resp.ConfirmationMode, resp.ConfirmationHint, note,
	)), nil
}

// ========== 格式化辅助 ==========

// formatSessionPullResult 格式化 pull 结果。
func formatSessionPullResult(sessionID string, delta *SessionDelta) string {
	if delta == nil {
		return fmt.Sprintf(
			"## Pull 结果\n\n"+
				"- **会话 ID**: `%s`\n"+
				"- **状态**: 无新数据\n"+
				"- **说明**: 当前没有新的结果数据。请稍后再次 pull。",
			sessionID,
		)
	}

	var statusStr string
	if delta.EndOfStream {
		statusStr = "命令已完成 ✅"
	} else if delta.TotalItems > 0 {
		statusStr = "有新数据"
	} else {
		statusStr = "无新数据（等待中）"
	}

	result := fmt.Sprintf(
		"## Pull 结果\n\n"+
			"- **会话 ID**: `%s`\n"+
			"- **状态**: %s\n"+
			"- **本次条目数**: %d\n"+
			"- **还有更多**: %v\n"+
			"- **命令已结束**: %v\n",
		sessionID, statusStr, delta.TotalItems, delta.HasMore, delta.EndOfStream,
	)

	if len(delta.Items) > 0 {
		result += "\n### 增量结果\n\n"
		for i, item := range delta.Items {
			result += fmt.Sprintf("#### 条目 %d\n\n```json\n", i+1)
			var v interface{}
			if err := json.Unmarshal(item, &v); err == nil {
				if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
					result += string(pretty)
				} else {
					result += string(item)
				}
			} else {
				result += string(item)
			}
			result += "\n```\n\n"
		}
	}

	if delta.EndOfStream {
		result += "\n> 命令已执行完成。会话已回到空闲状态，可以执行新的命令或关闭会话。\n"
	} else if delta.TotalItems == 0 {
		result += "\n> 暂无新数据。对于 trace/watch 等命令，需要等待目标方法被调用。请稍后再次 pull。\n"
	} else {
		result += "\n> 还有更多数据，请继续 pull。\n"
	}

	return result
}
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ========== Tool: arthas_exec (通用命令执行) ==========

func (w *mcpServerWrapper) registerArthasExecTool() {
	tool := mcp.NewTool("arthas_exec",
		mcp.WithDescription(
			"在目标 Agent 上执行任意 Arthas 命令。这是一个通用的 Arthas 命令执行工具，"+
				"适用于不在专用工具列表中的命令（如 ognl、classloader、vmoption 等）。\n\n"+
				"前提条件：Arthas 必须已经 attach 到目标 Agent（使用 arthas_attach）。\n\n"+
				"执行方式：通过 Control Plane 任务链路下发 arthas_exec_sync 任务到 Agent，"+
				"Agent 通过结构化命令桥接执行并返回 JSON 结果。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。可通过 list_agents 工具获取。"),
		),
		mcp.WithString("command",
			mcp.Required(),
			mcp.Description("要执行的 Arthas 命令。例如: 'version', 'thread', 'ognl @java.lang.System@getProperty(\"java.version\")'"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasExec)
}

func (w *mcpServerWrapper) handleArthasExec(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, _ := args["agent_id"].(string)
	command, _ := args["command"].(string)

	if agentID == "" {
		return mcp.NewToolResultError("参数 agent_id 不能为空"), nil
	}
	if command == "" {
		return mcp.NewToolResultError("参数 command 不能为空"), nil
	}

	return w.executeArthasCommandViaOrchestrator(ctx, agentID, command)
}

// ========== Tool: arthas_trace ==========

func (w *mcpServerWrapper) registerArthasTraceTool() {
	tool := mcp.NewTool("arthas_trace",
		mcp.WithDescription(
			"方法内部调用路径追踪，显示每个方法的调用耗时。"+
				"用于定位慢方法的具体瓶颈点。返回方法调用树和每个节点的耗时。\n\n"+
				"前提条件：Arthas 必须已 attach。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。"),
		),
		mcp.WithString("class_name",
			mcp.Required(),
			mcp.Description("完整类名，支持通配符。例如: 'com.example.OrderService'"),
		),
		mcp.WithString("method_name",
			mcp.Required(),
			mcp.Description("方法名。例如: 'createOrder'"),
		),
		mcp.WithString("condition",
			mcp.Description("过滤条件（OGNL 表达式）。例如: '#cost>100' 表示只追踪耗时超过100ms的调用。"),
		),
		mcp.WithBoolean("skip_jdk",
			mcp.Description("是否跳过 JDK 内部方法。默认 true。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasTrace)
}

func (w *mcpServerWrapper) handleArthasTrace(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, _ := args["agent_id"].(string)
	className, _ := args["class_name"].(string)
	methodName, _ := args["method_name"].(string)
	condition, _ := args["condition"].(string)
	skipJDK, hasSkipJDK := args["skip_jdk"].(bool)

	if agentID == "" || className == "" || methodName == "" {
		return mcp.NewToolResultError("参数 agent_id、class_name 和 method_name 不能为空"), nil
	}

	// Build trace command
	cmd := fmt.Sprintf("trace %s %s", className, methodName)
	if condition != "" {
		cmd += " '" + condition + "'"
	}
	if hasSkipJDK && !skipJDK {
		// Default is skip JDK, only add flag if explicitly set to false
	} else {
		cmd += " --skipJDKMethod true"
	}
	cmd += " -n 1" // Capture only 1 invocation by default

	return w.executeArthasCommandViaOrchestrator(ctx, agentID, cmd)
}

// ========== Tool: arthas_watch ==========

func (w *mcpServerWrapper) registerArthasWatchTool() {
	tool := mcp.NewTool("arthas_watch",
		mcp.WithDescription(
			"监控方法调用，输出方法的入参、返回值和异常信息。"+
				"用于在运行时查看方法的实际传入参数和返回结果。\n\n"+
				"前提条件：Arthas 必须已 attach。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。"),
		),
		mcp.WithString("class_name",
			mcp.Required(),
			mcp.Description("完整类名，支持通配符。例如: 'com.example.OrderService'"),
		),
		mcp.WithString("method_name",
			mcp.Required(),
			mcp.Description("方法名。例如: 'createOrder'"),
		),
		mcp.WithString("express",
			mcp.Description("观察表达式（OGNL）。默认为 '{params, returnObj, throwExp}'。"+
				"可自定义如 '{params[0], returnObj.size()}'"),
		),
		mcp.WithString("condition",
			mcp.Description("过滤条件（OGNL 表达式）。例如: '#cost>100' 或 'params[0]>10'"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasWatch)
}

func (w *mcpServerWrapper) handleArthasWatch(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, _ := args["agent_id"].(string)
	className, _ := args["class_name"].(string)
	methodName, _ := args["method_name"].(string)
	express, _ := args["express"].(string)
	condition, _ := args["condition"].(string)

	if agentID == "" || className == "" || methodName == "" {
		return mcp.NewToolResultError("参数 agent_id、class_name 和 method_name 不能为空"), nil
	}

	// Build watch command
	if express == "" {
		express = "'{params, returnObj, throwExp}'"
	} else if !strings.HasPrefix(express, "'") {
		express = "'" + express + "'"
	}

	cmd := fmt.Sprintf("watch %s %s %s", className, methodName, express)
	if condition != "" {
		cmd += " '" + condition + "'"
	}
	cmd += " -n 1 -x 2" // 1 invocation, expand depth 2

	return w.executeArthasCommandViaOrchestrator(ctx, agentID, cmd)
}

// ========== Tool: arthas_jad ==========

func (w *mcpServerWrapper) registerArthasJadTool() {
	tool := mcp.NewTool("arthas_jad",
		mcp.WithDescription(
			"反编译类查看运行时代码。用于查看类的实际运行版本源码，"+
				"确认代码是否为预期版本，排查类加载问题。\n\n"+
				"前提条件：Arthas 必须已 attach。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。"),
		),
		mcp.WithString("class_name",
			mcp.Required(),
			mcp.Description("完整类名。例如: 'com.example.OrderService'"),
		),
		mcp.WithString("method_name",
			mcp.Description("指定反编译的方法名。可选，不指定则反编译整个类。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasJad)
}

func (w *mcpServerWrapper) handleArthasJad(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, _ := args["agent_id"].(string)
	className, _ := args["class_name"].(string)
	methodName, _ := args["method_name"].(string)

	if agentID == "" || className == "" {
		return mcp.NewToolResultError("参数 agent_id 和 class_name 不能为空"), nil
	}

	cmd := fmt.Sprintf("jad %s", className)
	if methodName != "" {
		cmd += " " + methodName
	}

	return w.executeArthasCommandViaOrchestrator(ctx, agentID, cmd)
}

// ========== Tool: arthas_sc ==========

func (w *mcpServerWrapper) registerArthasScTool() {
	tool := mcp.NewTool("arthas_sc",
		mcp.WithDescription(
			"搜索已加载的类。用于查找类是否存在、被哪个 ClassLoader 加载、"+
				"以及类的详细信息（接口、父类、注解等）。\n\n"+
				"前提条件：Arthas 必须已 attach。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。"),
		),
		mcp.WithString("pattern",
			mcp.Required(),
			mcp.Description("类名模式，支持通配符。例如: 'com.example.Order*' 或 '*OrderService'"),
		),
		mcp.WithBoolean("detail",
			mcp.Description("是否显示详细信息（ClassLoader、接口、父类、注解等）。默认 false。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasSc)
}

func (w *mcpServerWrapper) handleArthasSc(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, _ := args["agent_id"].(string)
	pattern, _ := args["pattern"].(string)
	detail, _ := args["detail"].(bool)

	if agentID == "" || pattern == "" {
		return mcp.NewToolResultError("参数 agent_id 和 pattern 不能为空"), nil
	}

	cmd := fmt.Sprintf("sc %s", pattern)
	if detail {
		cmd += " -d"
	}

	return w.executeArthasCommandViaOrchestrator(ctx, agentID, cmd)
}

// ========== Tool: arthas_thread ==========

func (w *mcpServerWrapper) registerArthasThreadTool() {
	tool := mcp.NewTool("arthas_thread",
		mcp.WithDescription(
			"查看 JVM 线程信息。用于排查死锁、查看线程状态、找出 CPU 占用最高的线程。\n\n"+
				"前提条件：Arthas 必须已 attach。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。"),
		),
		mcp.WithNumber("thread_id",
			mcp.Description("指定线程 ID 查看该线程的栈信息。不指定则显示所有线程。"),
		),
		mcp.WithNumber("top_n",
			mcp.Description("显示 CPU 使用率最高的 N 个线程。例如: 5"),
		),
		mcp.WithBoolean("find_deadlock",
			mcp.Description("是否检测死锁。设为 true 时仅显示死锁线程。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasThread)
}

func (w *mcpServerWrapper) handleArthasThread(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, _ := args["agent_id"].(string)
	threadID, hasThreadID := args["thread_id"].(float64)
	topN, hasTopN := args["top_n"].(float64)
	findDeadlock, _ := args["find_deadlock"].(bool)

	if agentID == "" {
		return mcp.NewToolResultError("参数 agent_id 不能为空"), nil
	}

	cmd := "thread"
	if findDeadlock {
		cmd += " -b" // find blocked threads (deadlock detection)
	} else if hasThreadID && threadID > 0 {
		cmd += fmt.Sprintf(" %d", int(threadID))
	} else if hasTopN && topN > 0 {
		cmd += fmt.Sprintf(" -n %d", int(topN))
	}

	return w.executeArthasCommandViaOrchestrator(ctx, agentID, cmd)
}

// ========== Tool: arthas_stack ==========

func (w *mcpServerWrapper) registerArthasStackTool() {
	tool := mcp.NewTool("arthas_stack",
		mcp.WithDescription(
			"输出方法的调用栈。用于查看一个方法是从哪里被调用的，"+
				"帮助理解代码执行路径。\n\n"+
				"前提条件：Arthas 必须已 attach。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。"),
		),
		mcp.WithString("class_name",
			mcp.Required(),
			mcp.Description("完整类名，支持通配符。例如: 'com.example.OrderService'"),
		),
		mcp.WithString("method_name",
			mcp.Required(),
			mcp.Description("方法名。例如: 'createOrder'"),
		),
		mcp.WithString("condition",
			mcp.Description("过滤条件（OGNL 表达式）。例如: '#cost>100'"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasStack)
}

func (w *mcpServerWrapper) handleArthasStack(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, _ := args["agent_id"].(string)
	className, _ := args["class_name"].(string)
	methodName, _ := args["method_name"].(string)
	condition, _ := args["condition"].(string)

	if agentID == "" || className == "" || methodName == "" {
		return mcp.NewToolResultError("参数 agent_id、class_name 和 method_name 不能为空"), nil
	}

	cmd := fmt.Sprintf("stack %s %s", className, methodName)
	if condition != "" {
		cmd += " '" + condition + "'"
	}
	cmd += " -n 1" // 1 invocation

	return w.executeArthasCommandViaOrchestrator(ctx, agentID, cmd)
}

// ========== Common: executeArthasCommandViaOrchestrator ==========

// executeArthasCommandViaOrchestrator 通过 Orchestrator 执行 Arthas 命令。
// 使用 Control Plane 任务链路（arthas_exec_sync），而非 WebSocket Tunnel。
func (w *mcpServerWrapper) executeArthasCommandViaOrchestrator(ctx context.Context, agentID, command string) (*mcp.CallToolResult, error) {
	w.logger.Info("[arthas_cmd] 通过 Orchestrator 执行命令",
		zap.String("agent_id", agentID),
		zap.String("command", command),
	)

	resp, err := w.orchestrator.ExecSync(ctx, &ExecSyncRequest{
		AgentID:            agentID,
		Command:            command,
		AutoAttach:         true,
		RequireTunnelReady: false, // exec_sync 不依赖 tunnel
	})
	if err != nil {
		w.logger.Error("[arthas_cmd] Orchestrator 执行失败",
			zap.String("agent_id", agentID),
			zap.String("command", command),
			zap.Error(err),
		)
		return mcp.NewToolResultError("执行 Arthas 命令失败: " + err.Error()), nil
	}

	// 格式化结果
	formatted := FormatExecSyncForMCP(resp)

	if resp.Parsed != nil && !resp.Parsed.Success {
		w.logger.Warn("[arthas_cmd] 命令执行失败",
			zap.String("agent_id", agentID),
			zap.String("command", command),
			zap.String("error_code", resp.Parsed.ErrorCode),
			zap.String("error_message", resp.Parsed.ErrorMessage),
		)
		return mcp.NewToolResultError(formatted), nil
	}

	w.logger.Info("[arthas_cmd] 命令执行完成",
		zap.String("agent_id", agentID),
		zap.String("command", command),
		zap.String("task_id", resp.TaskID),
	)

	return mcp.NewToolResultText(formatted), nil
}
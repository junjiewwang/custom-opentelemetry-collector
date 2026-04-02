// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"
)

// mcpServerWrapper wraps mcp-go's MCPServer and manages tool registration.
type mcpServerWrapper struct {
	ext                  *Extension
	mcpServer            *server.MCPServer
	httpSrv              *server.StreamableHTTPServer
	logger               *zap.Logger
	orchestrator         *ArthasOrchestrator
	sessionManager       *SessionManager
	sessionOrchestrator  *SessionOrchestrator
}

// newMCPServerWrapper creates a new MCP server wrapper with all tools registered.
func newMCPServerWrapper(ext *Extension) (*mcpServerWrapper, error) {
	s := server.NewMCPServer(
		"OTel Collector MCP",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	// 创建 Arthas Orchestrator
	orchConfig := DefaultOrchestratorConfig()
	if ext.config.ToolTimeout > 0 {
		orchConfig.DefaultExecTimeoutMs = int64(ext.config.ToolTimeout) * 1000
	}
	orch := NewArthasOrchestrator(ext.controlPlane, ext.logger, orchConfig)

	// 创建 Session Manager
	smConfig := DefaultSessionManagerConfig()
	sm := NewSessionManager(ext.logger, smConfig)
	sm.Start()

	// 创建 Session Orchestrator
	soConfig := DefaultSessionOrchestratorConfig()
	so := NewSessionOrchestrator(ext.controlPlane, sm, ext.logger, soConfig)

	wrapper := &mcpServerWrapper{
		ext:                  ext,
		mcpServer:            s,
		logger:               ext.logger.Named("mcp-server"),
		orchestrator:         orch,
		sessionManager:       sm,
		sessionOrchestrator:  so,
	}

	// Register all tools
	wrapper.registerTools()

	// Create Streamable HTTP server
	wrapper.httpSrv = server.NewStreamableHTTPServer(s)

	return wrapper, nil
}

// Handler returns the HTTP handler for the MCP server.
func (w *mcpServerWrapper) Handler() http.Handler {
	return w.httpSrv
}

// registerTools registers all MCP tools.
func (w *mcpServerWrapper) registerTools() {
	// ========== Agent Management Tools ==========
	w.registerListAgentsTool()
	w.registerAgentInfoTool()
	w.registerArthasStatusTool()

	// ========== Arthas Lifecycle Tools ==========
	w.registerArthasAttachTool()
	w.registerArthasDetachTool()

	// ========== Arthas Command Tools ==========
	w.registerArthasExecTool()
	w.registerArthasTraceTool()
	w.registerArthasWatchTool()
	w.registerArthasJadTool()
	w.registerArthasScTool()
	w.registerArthasThreadTool()
	w.registerArthasStackTool()

	// ========== Arthas Session Tools ==========
	w.registerArthasSessionOpenTool()
	w.registerArthasSessionExecTool()
	w.registerArthasSessionPullTool()
	w.registerArthasSessionInterruptTool()
	w.registerArthasSessionCloseTool()
}

// ========== Tool: list_agents ==========

func (w *mcpServerWrapper) registerListAgentsTool() {
	tool := mcp.NewTool("list_agents",
		mcp.WithDescription("列出所有在线的 Java Agent。返回每个 Agent 的 ID、应用名、服务名、IP 地址等信息。"),
	)
	w.mcpServer.AddTool(tool, w.handleListAgents)
}

func (w *mcpServerWrapper) handleListAgents(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	w.logger.Info("[list_agents] Executing")

	agents, err := w.ext.controlPlane.GetOnlineAgents(ctx)
	if err != nil {
		w.logger.Error("[list_agents] Failed to get online agents", zap.Error(err))
		return mcp.NewToolResultError("获取在线 Agent 列表失败: " + err.Error()), nil
	}

	if len(agents) == 0 {
		return mcp.NewToolResultText("当前没有在线的 Agent。"), nil
	}

	// Build result text
	result := formatAgentList(agents)
	w.logger.Info("[list_agents] Completed", zap.Int("agent_count", len(agents)))

	return mcp.NewToolResultText(result), nil
}

// ========== Tool: agent_info ==========

func (w *mcpServerWrapper) registerAgentInfoTool() {
	tool := mcp.NewTool("agent_info",
		mcp.WithDescription("获取指定 Agent 的详细信息，包括 JVM 信息、操作系统信息、应用信息等。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。可通过 list_agents 工具获取。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleAgentInfo)
}

func (w *mcpServerWrapper) handleAgentInfo(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, ok := args["agent_id"].(string)
	if !ok || agentID == "" {
		return mcp.NewToolResultError("参数 agent_id 不能为空"), nil
	}

	w.logger.Info("[agent_info] Executing", zap.String("agent_id", agentID))

	agent, err := w.ext.controlPlane.GetAgent(ctx, agentID)
	if err != nil {
		w.logger.Error("[agent_info] Failed to get agent", zap.String("agent_id", agentID), zap.Error(err))
		return mcp.NewToolResultError("获取 Agent 信息失败: " + err.Error()), nil
	}

	if agent == nil {
		return mcp.NewToolResultError("Agent '" + agentID + "' 不存在或已离线"), nil
	}

	result := formatAgentInfo(agent)
	w.logger.Info("[agent_info] Completed", zap.String("agent_id", agentID))

	return mcp.NewToolResultText(result), nil
}

// ========== Tool: arthas_status ==========

func (w *mcpServerWrapper) registerArthasStatusTool() {
	tool := mcp.NewTool("arthas_status",
		mcp.WithDescription("检查目标 Agent 上 Arthas 是否已 attach 并注册到 Tunnel。返回状态: not_attached（Arthas 未启动）、tunnel_registered（Arthas 已连接 Tunnel，可以执行命令）。"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("目标 Agent 的 ID。可通过 list_agents 工具获取。"),
		),
	)
	w.mcpServer.AddTool(tool, w.handleArthasStatus)
}

func (w *mcpServerWrapper) handleArthasStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := request.GetArguments()
	agentID, ok := args["agent_id"].(string)
	if !ok || agentID == "" {
		return mcp.NewToolResultError("参数 agent_id 不能为空"), nil
	}

	w.logger.Info("[arthas_status] Executing", zap.String("agent_id", agentID))

	// Check if agent exists in ControlPlane
	agent, err := w.ext.controlPlane.GetAgent(ctx, agentID)
	if err != nil {
		w.logger.Error("[arthas_status] Failed to get agent", zap.String("agent_id", agentID), zap.Error(err))
		return mcp.NewToolResultError("获取 Agent 信息失败: " + err.Error()), nil
	}
	if agent == nil {
		return mcp.NewToolResultError("Agent '" + agentID + "' 不存在或已离线"), nil
	}

	// Check if Arthas is connected to tunnel
	isConnected := w.ext.arthasTunnel.IsAgentConnected(agentID)

	var status, message string
	if isConnected {
		status = "tunnel_registered"
		message = "Arthas 已连接到 Tunnel，可以执行 Arthas 命令。"
	} else {
		status = "not_attached"
		message = "Arthas 未连接到 Tunnel。请先调用 arthas_attach 工具启动 Arthas。"
	}

	result := formatArthasStatus(agentID, status, message)
	w.logger.Info("[arthas_status] Completed",
		zap.String("agent_id", agentID),
		zap.String("status", status),
	)

	return mcp.NewToolResultText(result), nil
}

// toolTimeout returns the configured tool timeout duration.
func (w *mcpServerWrapper) toolTimeout() time.Duration {
	return time.Duration(w.ext.config.ToolTimeout) * time.Second
}

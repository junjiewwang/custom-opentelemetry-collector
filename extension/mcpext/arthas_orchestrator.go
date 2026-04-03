// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/controlplane/model"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext"
)

// ========== Collector Arthas Orchestrator ==========
//
// 负责编排 Arthas 同步任务的完整生命周期：
//   - attach 检查 / 自动 attach
//   - arthas_exec_sync 任务构造与提交
//   - 结果等待与解析
//   - 超时与有限重试
//
// 设计原则：
//   - 不直接依赖 WebSocket Tunnel，仅通过 Control Plane 任务链路
//   - 使用 Phase 0 冻结的协议常量
//   - 对上游（MCP 工具层）暴露统一的 ExecSync 接口

// ArthasOrchestrator 封装 Arthas 同步任务编排逻辑。
type ArthasOrchestrator struct {
	controlPlane controlplaneext.ControlPlane
	logger       *zap.Logger

	// 默认超时配置
	defaultExecTimeoutMs int64 // exec_sync 默认超时（毫秒）
	attachTimeoutMs      int64 // attach 默认超时（毫秒）
	maxAttachRetries     int   // attach 最大重试次数
	pollInterval         time.Duration // 轮询间隔
}

// OrchestratorConfig 编排器配置。
type OrchestratorConfig struct {
	// DefaultExecTimeoutMs exec_sync 默认超时（毫秒），默认 30000
	DefaultExecTimeoutMs int64
	// AttachTimeoutMs attach 默认超时（毫秒），默认 60000
	AttachTimeoutMs int64
	// MaxAttachRetries attach 最大重试次数，默认 2
	MaxAttachRetries int
	// PollInterval 轮询间隔，默认 500ms
	PollInterval time.Duration
}

// DefaultOrchestratorConfig 返回默认编排器配置。
func DefaultOrchestratorConfig() OrchestratorConfig {
	return OrchestratorConfig{
		DefaultExecTimeoutMs: 30000,
		AttachTimeoutMs:      60000,
		MaxAttachRetries:     2,
		PollInterval:         500 * time.Millisecond,
	}
}

// NewArthasOrchestrator 创建 Arthas 编排器。
func NewArthasOrchestrator(cp controlplaneext.ControlPlane, logger *zap.Logger, config OrchestratorConfig) *ArthasOrchestrator {
	if config.DefaultExecTimeoutMs <= 0 {
		config.DefaultExecTimeoutMs = 30000
	}
	if config.AttachTimeoutMs <= 0 {
		config.AttachTimeoutMs = 60000
	}
	if config.MaxAttachRetries < 0 {
		config.MaxAttachRetries = 0
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 500 * time.Millisecond
	}

	return &ArthasOrchestrator{
		controlPlane:         cp,
		logger:               logger.Named("arthas-orchestrator"),
		defaultExecTimeoutMs: config.DefaultExecTimeoutMs,
		attachTimeoutMs:      config.AttachTimeoutMs,
		maxAttachRetries:     config.MaxAttachRetries,
		pollInterval:         config.PollInterval,
	}
}

// ========== ExecSync 请求/响应 ==========

// ExecSyncRequest 是 ExecSync 的请求参数。
type ExecSyncRequest struct {
	// AgentID 目标 Agent ID（必填）
	AgentID string
	// Command Arthas 命令字符串（必填）
	Command string
	// TimeoutMs 命令执行超时（毫秒），0 使用默认值
	TimeoutMs int64
	// AutoAttach 未启动 Arthas 时是否自动 attach，默认 true
	AutoAttach bool
	// RequireTunnelReady 是否要求 tunnel 已就绪，默认 true
	RequireTunnelReady bool
	// ResultLimitBytes 单次结果大小限制（字节），0 不限制
	ResultLimitBytes int64
	// UserID 操作者身份
	UserID string
	// TraceID 跨端日志关联
	TraceID string
}

// ExecSyncResponse 是 ExecSync 的响应。
type ExecSyncResponse struct {
	// Parsed 解析后的结构化结果
	Parsed *ParsedArthasResult
	// TaskID 任务 ID（用于追踪）
	TaskID string
	// AgentID 目标 Agent ID
	AgentID string
	// Accepted 是否已提交并被 Collector 受理
	Accepted bool
	// Pending 是否仍需后续确认
	Pending bool
	// State 当前状态，如 PENDING_CONFIRMATION
	State string
	// ConfirmationMode 后续确认方式
	ConfirmationMode string
	// ConfirmationHint 后续确认提示
	ConfirmationHint string
}

// ========== 核心编排方法 ==========

// ExecSync 执行同步 Arthas 命令。
//
// 编排流程：
//  1. 验证 Agent 是否存在
//  2. 构造 arthas_exec_sync 任务
//  3. 提交任务到 Control Plane
//  4. 轮询等待结果
//  5. 解析并返回结构化结果
//
// 注意：exec_sync 默认不自动重试（可能重复执行命令）。
func (o *ArthasOrchestrator) ExecSync(ctx context.Context, req *ExecSyncRequest) (*ExecSyncResponse, error) {
	if req.AgentID == "" {
		return nil, fmt.Errorf("agent_id 不能为空")
	}
	if req.Command == "" {
		return nil, fmt.Errorf("command 不能为空")
	}

	o.logger.Info("[exec_sync] 开始执行",
		zap.String("agent_id", req.AgentID),
		zap.String("command", req.Command),
	)

	// Step 1: 验证 Agent 是否存在
	agent, err := o.controlPlane.GetAgent(ctx, req.AgentID)
	if err != nil {
		return nil, fmt.Errorf("获取 Agent 信息失败: %w", err)
	}
	if agent == nil {
		return nil, fmt.Errorf("Agent '%s' 不存在或已离线", req.AgentID)
	}

	// Step 2: 构造任务
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = o.defaultExecTimeoutMs
	}

	task := o.buildExecSyncTask(req, timeoutMs)

	// Step 3: 提交任务
	if err := o.controlPlane.SubmitTaskForAgent(ctx, req.AgentID, task); err != nil {
		o.logger.Error("[exec_sync] 提交任务失败",
			zap.String("agent_id", req.AgentID),
			zap.String("task_id", task.ID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("提交 arthas_exec_sync 任务失败: %w", err)
	}

	o.logger.Info("[exec_sync] 任务已提交，等待结果",
		zap.String("agent_id", req.AgentID),
		zap.String("task_id", task.ID),
		zap.String("command", req.Command),
		zap.Int64("timeout_ms", timeoutMs),
	)

	// Step 4: 等待结果
	// 等待超时 = 任务超时 + 额外缓冲（10s）
	waitTimeout := time.Duration(timeoutMs)*time.Millisecond + 10*time.Second
	taskResult, err := o.waitForTaskResult(ctx, task.ID, waitTimeout)
	if err != nil {
		o.logger.Error("[exec_sync] 等待结果失败",
			zap.String("agent_id", req.AgentID),
			zap.String("task_id", task.ID),
			zap.Error(err),
		)
		return &ExecSyncResponse{
			TaskID:  task.ID,
			AgentID: req.AgentID,
			Parsed: &ParsedArthasResult{
				TaskType:     model.ArthasTaskTypeExecSync,
				Timeout:      true,
				ErrorCode:    model.ArthasErrCommandTimeout,
				ErrorMessage: fmt.Sprintf("等待任务结果超时: %v", err),
			},
		}, nil
	}

	// Step 5: 解析结果
	parsed := ParseExecSyncResult(taskResult)

	o.logger.Info("[exec_sync] 执行完成",
		zap.String("agent_id", req.AgentID),
		zap.String("task_id", task.ID),
		zap.Bool("success", parsed.Success),
		zap.String("error_code", parsed.ErrorCode),
	)

	return &ExecSyncResponse{
		Parsed:  parsed,
		TaskID:  task.ID,
		AgentID: req.AgentID,
	}, nil
}

// Attach 执行 Arthas attach，支持有限重试。
func (o *ArthasOrchestrator) Attach(ctx context.Context, agentID string) (*ExecSyncResponse, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id 不能为空")
	}

	// 验证 Agent 是否存在
	agent, err := o.controlPlane.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("获取 Agent 信息失败: %w", err)
	}
	if agent == nil {
		return nil, fmt.Errorf("Agent '%s' 不存在或已离线", agentID)
	}

	task := o.buildAttachTask(agentID)
	if err := o.controlPlane.SubmitTaskForAgent(ctx, agentID, task); err != nil {
		o.logger.Error("[attach] 提交任务失败",
			zap.String("agent_id", agentID),
			zap.String("task_id", task.ID),
			zap.Error(err),
		)
		return &ExecSyncResponse{
			AgentID: agentID,
			Parsed: &ParsedArthasResult{
				TaskType:     model.ArthasTaskTypeAttach,
				ErrorCode:    model.ArthasErrAttachError,
				ErrorMessage: fmt.Sprintf("提交 attach 任务失败: %v", err),
			},
		}, nil
	}

	go o.confirmAttach(task.ID, agentID)

	return &ExecSyncResponse{
		TaskID:            task.ID,
		AgentID:           agentID,
		Accepted:          true,
		Pending:           true,
		State:             "PENDING_CONFIRMATION",
		ConfirmationMode:  "arthas_status",
		ConfirmationHint:  "attach 任务已提交，请稍后调用 arthas_status，直到状态变为 tunnel_registered",
	}, nil
}

// Detach 执行 Arthas detach，支持有限重试。
func (o *ArthasOrchestrator) Detach(ctx context.Context, agentID string) (*ExecSyncResponse, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id 不能为空")
	}

	agent, err := o.controlPlane.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("获取 Agent 信息失败: %w", err)
	}
	if agent == nil {
		return nil, fmt.Errorf("Agent '%s' 不存在或已离线", agentID)
	}

	task := o.buildDetachTask(agentID)

	if err := o.controlPlane.SubmitTaskForAgent(ctx, agentID, task); err != nil {
		return nil, fmt.Errorf("提交 arthas_detach 任务失败: %w", err)
	}

	waitTimeout := 30*time.Second + 10*time.Second
	taskResult, err := o.waitForTaskResult(ctx, task.ID, waitTimeout)
	if err != nil {
		return &ExecSyncResponse{
			TaskID:  task.ID,
			AgentID: agentID,
			Parsed: &ParsedArthasResult{
				TaskType:     model.ArthasTaskTypeDetach,
				Timeout:      true,
				ErrorCode:    model.ArthasErrDetachError,
				ErrorMessage: fmt.Sprintf("等待 detach 结果超时: %v", err),
			},
		}, nil
	}

	parsed := ParseDetachResult(taskResult)
	return &ExecSyncResponse{
		Parsed:  parsed,
		TaskID:  task.ID,
		AgentID: agentID,
	}, nil
}

// ========== 任务构造器 ==========

// buildExecSyncTask 构造 arthas_exec_sync 任务。
func (o *ArthasOrchestrator) buildExecSyncTask(req *ExecSyncRequest, timeoutMs int64) *model.Task {
	params := map[string]interface{}{
		model.ArthasParamCommand:   req.Command,
		model.ArthasParamTimeoutMs: timeoutMs,
	}

	// 始终显式传递布尔参数，避免依赖 Agent 侧默认值
	// Bug fix: 之前仅在 true 时传递，Agent 侧默认 require_tunnel_ready=true，
	// 导致 Collector 设置 false 时 Agent 仍然等待 Tunnel 就绪而超时
	params[model.ArthasParamAutoAttach] = req.AutoAttach
	params[model.ArthasParamRequireTunnelReady] = req.RequireTunnelReady
	if req.ResultLimitBytes > 0 {
		params[model.ArthasParamResultLimitBytes] = req.ResultLimitBytes
	}
	if req.UserID != "" {
		params[model.ArthasParamUserID] = req.UserID
	}
	if req.TraceID != "" {
		params[model.ArthasParamTraceID] = req.TraceID
	}

	// 幂等键
	requestID := uuid.New().String()
	params[model.ArthasParamRequestID] = requestID

	paramsJSON, _ := json.Marshal(params)

	return &model.Task{
		ID:             uuid.New().String(),
		TypeName:       model.ArthasTaskTypeExecSync,
		ParametersJSON: paramsJSON,
		TimeoutMillis:  timeoutMs,
		TargetAgentID:  req.AgentID,
	}
}

// buildAttachTask 构造 arthas_attach 任务。
func (o *ArthasOrchestrator) buildAttachTask(agentID string) *model.Task {
	params := map[string]interface{}{
		model.ArthasParamAction:    "attach",
		model.ArthasParamRequestID: uuid.New().String(),
	}
	paramsJSON, _ := json.Marshal(params)

	return &model.Task{
		ID:             uuid.New().String(),
		TypeName:       model.ArthasTaskTypeAttach,
		ParametersJSON: paramsJSON,
		TimeoutMillis:  o.attachTimeoutMs,
		TargetAgentID:  agentID,
	}
}

// buildDetachTask 构造 arthas_detach 任务。
func (o *ArthasOrchestrator) buildDetachTask(agentID string) *model.Task {
	params := map[string]interface{}{
		model.ArthasParamAction:    "detach",
		model.ArthasParamRequestID: uuid.New().String(),
	}
	paramsJSON, _ := json.Marshal(params)

	return &model.Task{
		ID:             uuid.New().String(),
		TypeName:       model.ArthasTaskTypeDetach,
		ParametersJSON: paramsJSON,
		TimeoutMillis:  30000,
		TargetAgentID:  agentID,
	}
}

// ========== 任务结果等待 ==========

// waitForTaskResult 轮询等待任务结果直到终态或超时。
func (o *ArthasOrchestrator) waitForTaskResult(ctx context.Context, taskID string, timeout time.Duration) (*model.TaskResult, error) {
	deadline := time.After(timeout)
	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("timeout after %v", timeout)
		case <-ticker.C:
			result, found, err := o.controlPlane.GetTaskResult(taskID)
			if err != nil {
				return nil, fmt.Errorf("get task result for %s: %w", taskID, err)
			}
			if !found {
				continue
			}
			switch result.Status {
			case model.TaskStatusSuccess, model.TaskStatusFailed, model.TaskStatusTimeout, model.TaskStatusCancelled:
				return result, nil
			case model.TaskStatusRunning, model.TaskStatusPending:
				continue
			default:
				return result, nil
			}
		}
	}
}

func (o *ArthasOrchestrator) confirmAttach(taskID, agentID string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(o.attachTimeoutMs)*time.Millisecond+10*time.Second)
	defer cancel()

	taskResult, err := o.waitForTaskResult(ctx, taskID, time.Duration(o.attachTimeoutMs)*time.Millisecond+10*time.Second)
	if err != nil {
		o.logger.Warn("[attach] 后台确认超时",
			zap.String("agent_id", agentID),
			zap.String("task_id", taskID),
			zap.Error(err),
		)
		return
	}

	parsed := ParseAttachResult(taskResult)
	if parsed.Success {
		o.logger.Info("[attach] 后台确认成功",
			zap.String("agent_id", agentID),
			zap.String("task_id", taskID),
			zap.String("arthas_state", parsed.ArthasState),
			zap.Bool("tunnel_ready", parsed.TunnelReady),
		)
		return
	}

	o.logger.Warn("[attach] 后台确认失败",
		zap.String("agent_id", agentID),
		zap.String("task_id", taskID),
		zap.String("error_code", parsed.ErrorCode),
		zap.String("error_message", parsed.ErrorMessage),
	)
}

// ========== 结构化结果格式化（面向 MCP 工具层） ==========

// FormatExecSyncForMCP 将 ExecSyncResponse 格式化为 MCP 工具友好的文本。
func FormatExecSyncForMCP(resp *ExecSyncResponse) string {
	parsed := resp.Parsed
	if parsed == nil {
		return "## Arthas 命令执行结果\n\n- **状态**: 未知错误\n"
	}

	var result string

	if parsed.Success {
		result = fmt.Sprintf(
			"## Arthas 命令执行结果\n\n"+
				"- **Agent**: %s\n"+
				"- **命令**: `%s`\n"+
				"- **状态**: 成功 ✅\n"+
				"- **任务 ID**: %s\n",
			resp.AgentID, parsed.Command, resp.TaskID,
		)

		// 添加结构化结果
		if len(parsed.Payload) > 0 && string(parsed.Payload) != "null" {
			result += "\n### 结构化结果\n\n```json\n"
			// 尝试美化 JSON
			var v interface{}
			if err := json.Unmarshal(parsed.Payload, &v); err == nil {
				if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
					result += string(pretty)
				} else {
					result += string(parsed.Payload)
				}
			} else {
				result += string(parsed.Payload)
			}
			result += "\n```\n"
		}

		// 添加原始 JSON（如果有且与 payload 不同）
		if parsed.RawJSON != "" {
			result += "\n### Arthas 原始输出\n\n```json\n"
			var v interface{}
			if err := json.Unmarshal([]byte(parsed.RawJSON), &v); err == nil {
				if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
					result += string(pretty)
				} else {
					result += parsed.RawJSON
				}
			} else {
				result += parsed.RawJSON
			}
			result += "\n```\n"
		}

		// 如果既没有 payload 也没有 rawJSON，尝试透传原始 result_json
		if (len(parsed.Payload) == 0 || string(parsed.Payload) == "null") && parsed.RawJSON == "" && len(parsed.RawResultJSON) > 0 {
			result += "\n### 原始结果\n\n```json\n"
			var v interface{}
			if err := json.Unmarshal(parsed.RawResultJSON, &v); err == nil {
				if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
					result += string(pretty)
				} else {
					result += string(parsed.RawResultJSON)
				}
			} else {
				result += string(parsed.RawResultJSON)
			}
			result += "\n```\n"
		}

		// 添加元信息
		if len(parsed.Meta) > 0 {
			result += "\n### 执行元信息\n\n"
			for k, v := range parsed.Meta {
				result += fmt.Sprintf("- **%s**: %v\n", k, v)
			}
		}
	} else {
		// 失败情况
		timeoutStr := ""
		if parsed.Timeout {
			timeoutStr = "（超时）"
		}

		result = fmt.Sprintf(
			"## Arthas 命令执行结果\n\n"+
				"- **Agent**: %s\n"+
				"- **命令**: `%s`\n"+
				"- **状态**: 失败 ❌%s\n"+
				"- **错误码**: %s\n"+
				"- **错误信息**: %s\n"+
				"- **任务 ID**: %s\n",
			resp.AgentID, parsed.Command, timeoutStr,
			parsed.ErrorCode, parsed.ErrorMessage, resp.TaskID,
		)

		// 如果有原始结果，也附上
		if len(parsed.RawResultJSON) > 0 {
			result += "\n### 原始错误详情\n\n```json\n"
			var v interface{}
			if err := json.Unmarshal(parsed.RawResultJSON, &v); err == nil {
				if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
					result += string(pretty)
				} else {
					result += string(parsed.RawResultJSON)
				}
			} else {
				result += string(parsed.RawResultJSON)
			}
			result += "\n```\n"
		}
	}

	return result
}
// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/collector/custom/controlplane/model"
)

// ========== Arthas 结构化结果解析器 ==========
//
// 统一解析 Agent 返回的 result_json（遵循 Phase 0 冻结的协议 envelope），
// 收敛错误码 / timeout 语义，供 Orchestrator 和 MCP 工具层消费。
//
// 对应 Agent 侧 ArthasTaskProtocol.ResultField 定义的字段。

// ArthasExecSyncResult 是 arthas_exec_sync 任务的结构化结果 envelope。
// 对应协议设计文档 §7.3 result_json。
type ArthasExecSyncResult struct {
	// Success 是否执行成功
	Success bool `json:"success"`
	// TaskType 当前任务类型
	TaskType string `json:"taskType"`
	// Command 原始命令
	Command string `json:"command,omitempty"`
	// SessionID session 标识（同步命令通常为空）
	SessionID string `json:"sessionId,omitempty"`
	// Timeout 是否超时
	Timeout bool `json:"timeout"`
	// ErrorCode 协议级错误码
	ErrorCode string `json:"errorCode,omitempty"`
	// ErrorMessage 错误信息
	ErrorMessage string `json:"errorMessage,omitempty"`
	// Payload 结构化结果主体
	Payload json.RawMessage `json:"payload,omitempty"`
	// RawJSON Arthas 原始结构化 JSON
	RawJSON string `json:"rawJson,omitempty"`
	// Meta 执行元信息
	Meta map[string]interface{} `json:"meta,omitempty"`
}

// ArthasAttachResult 是 arthas_attach 任务的结构化结果 envelope。
type ArthasAttachResult struct {
	Success      bool                   `json:"success"`
	TaskType     string                 `json:"taskType"`
	ErrorCode    string                 `json:"errorCode,omitempty"`
	ErrorMessage string                 `json:"errorMessage,omitempty"`
	ArthasState  string                 `json:"arthas_state,omitempty"`
	TunnelReady  bool                   `json:"tunnel_ready,omitempty"`
	Meta         map[string]interface{} `json:"meta,omitempty"`
}

// ArthasDetachResult 是 arthas_detach 任务的结构化结果 envelope。
type ArthasDetachResult struct {
	Success      bool                   `json:"success"`
	TaskType     string                 `json:"taskType"`
	ErrorCode    string                 `json:"errorCode,omitempty"`
	ErrorMessage string                 `json:"errorMessage,omitempty"`
	Meta         map[string]interface{} `json:"meta,omitempty"`
}

// ParsedArthasResult 是统一的解析结果，屏蔽不同 task_type 的差异。
type ParsedArthasResult struct {
	// Success 业务是否成功
	Success bool
	// Timeout 是否超时
	Timeout bool
	// ErrorCode 协议级错误码（来自 result_json 或 TaskResult）
	ErrorCode string
	// ErrorMessage 错误信息
	ErrorMessage string
	// TaskType 任务类型
	TaskType string
	// Command 原始命令（仅 exec_sync）
	Command string

	// Payload 结构化结果主体（仅 exec_sync）
	Payload json.RawMessage
	// RawJSON Arthas 原始结构化 JSON（仅 exec_sync）
	RawJSON string
	// Meta 执行元信息
	Meta map[string]interface{}

	// ArthasState Arthas 运行状态（仅 attach）
	ArthasState string
	// TunnelReady tunnel 是否就绪（仅 attach）
	TunnelReady bool

	// RawResultJSON 原始 result_json 字节（用于兜底透传）
	RawResultJSON json.RawMessage
}

// ParseExecSyncResult 解析 arthas_exec_sync 的 TaskResult。
//
// 解析策略：
//  1. 优先从 TaskResult.ResultJSON 解析结构化 envelope
//  2. 如果 TaskResult 本身是失败/超时状态，映射为统一错误
//  3. 如果 ResultJSON 解析失败，降级为原始透传
func ParseExecSyncResult(taskResult *model.TaskResult) *ParsedArthasResult {
	parsed := &ParsedArthasResult{
		TaskType:      model.ArthasTaskTypeExecSync,
		RawResultJSON: taskResult.ResultJSON,
	}

	// 先检查 TaskResult 级别的状态
	switch taskResult.Status {
	case model.TaskStatusTimeout:
		parsed.Timeout = true
		parsed.ErrorCode = model.ArthasErrCommandTimeout
		parsed.ErrorMessage = "任务执行超时"
		if taskResult.ErrorMessage != "" {
			parsed.ErrorMessage = taskResult.ErrorMessage
		}
		return parsed
	case model.TaskStatusCancelled:
		parsed.ErrorCode = model.ArthasErrInterrupted
		parsed.ErrorMessage = "任务已取消"
		if taskResult.ErrorMessage != "" {
			parsed.ErrorMessage = taskResult.ErrorMessage
		}
		return parsed
	case model.TaskStatusFailed:
		// 尝试从 ResultJSON 解析更详细的错误信息
		if len(taskResult.ResultJSON) == 0 {
			parsed.ErrorCode = taskResult.ErrorCode
			parsed.ErrorMessage = taskResult.ErrorMessage
			if parsed.ErrorCode == "" {
				parsed.ErrorCode = model.ArthasErrCommandExecutionFailed
			}
			if parsed.ErrorMessage == "" {
				parsed.ErrorMessage = "任务执行失败"
			}
			return parsed
		}
		// 继续尝试解析 ResultJSON
	case model.TaskStatusSuccess:
		// 继续解析 ResultJSON
	default:
		// 其他状态（如 RUNNING/PENDING），不应该到这里
		parsed.ErrorCode = model.ArthasErrCommandExecutionFailed
		parsed.ErrorMessage = fmt.Sprintf("意外的任务状态: %d", taskResult.Status)
		return parsed
	}

	// 尝试解析 ResultJSON
	if len(taskResult.ResultJSON) == 0 {
		if taskResult.Status == model.TaskStatusSuccess {
			parsed.Success = true
		}
		return parsed
	}

	var execResult ArthasExecSyncResult
	if err := json.Unmarshal(taskResult.ResultJSON, &execResult); err != nil {
		// JSON 解析失败，降级为原始透传
		parsed.ErrorCode = model.ArthasErrJSONParseFailed
		parsed.ErrorMessage = fmt.Sprintf("解析 result_json 失败: %v", err)
		return parsed
	}

	// 映射结构化字段
	parsed.Success = execResult.Success
	parsed.Timeout = execResult.Timeout
	parsed.ErrorCode = execResult.ErrorCode
	parsed.ErrorMessage = execResult.ErrorMessage
	parsed.Command = execResult.Command
	parsed.Payload = execResult.Payload
	parsed.RawJSON = execResult.RawJSON
	parsed.Meta = execResult.Meta
	parsed.TaskType = execResult.TaskType

	// 如果 Agent 返回 timeout=true，确保错误码一致
	if execResult.Timeout && execResult.ErrorCode == "" {
		parsed.ErrorCode = model.ArthasErrCommandTimeout
	}

	return parsed
}

// ParseAttachResult 解析 arthas_attach 的 TaskResult。
func ParseAttachResult(taskResult *model.TaskResult) *ParsedArthasResult {
	parsed := &ParsedArthasResult{
		TaskType:      model.ArthasTaskTypeAttach,
		RawResultJSON: taskResult.ResultJSON,
	}

	switch taskResult.Status {
	case model.TaskStatusTimeout:
		parsed.Timeout = true
		parsed.ErrorCode = model.ArthasErrStartFailed
		parsed.ErrorMessage = "Arthas attach 超时"
		if taskResult.ErrorMessage != "" {
			parsed.ErrorMessage = taskResult.ErrorMessage
		}
		return parsed
	case model.TaskStatusFailed:
		parsed.ErrorCode = taskResult.ErrorCode
		parsed.ErrorMessage = taskResult.ErrorMessage
		if parsed.ErrorCode == "" {
			parsed.ErrorCode = model.ArthasErrAttachError
		}
		if parsed.ErrorMessage == "" {
			parsed.ErrorMessage = "Arthas attach 失败"
		}
		// 尝试从 ResultJSON 获取更多信息
		if len(taskResult.ResultJSON) > 0 {
			var attachResult ArthasAttachResult
			if err := json.Unmarshal(taskResult.ResultJSON, &attachResult); err == nil {
				if attachResult.ErrorCode != "" {
					parsed.ErrorCode = attachResult.ErrorCode
				}
				if attachResult.ErrorMessage != "" {
					parsed.ErrorMessage = attachResult.ErrorMessage
				}
				parsed.ArthasState = attachResult.ArthasState
				parsed.Meta = attachResult.Meta
			}
		}
		return parsed
	case model.TaskStatusSuccess:
		parsed.Success = true
		if len(taskResult.ResultJSON) > 0 {
			var attachResult ArthasAttachResult
			if err := json.Unmarshal(taskResult.ResultJSON, &attachResult); err == nil {
				parsed.Success = attachResult.Success
				parsed.ArthasState = attachResult.ArthasState
				parsed.TunnelReady = attachResult.TunnelReady
				parsed.Meta = attachResult.Meta
				if !attachResult.Success {
					parsed.ErrorCode = attachResult.ErrorCode
					parsed.ErrorMessage = attachResult.ErrorMessage
				}
			}
		}
		return parsed
	default:
		parsed.ErrorCode = model.ArthasErrAttachError
		parsed.ErrorMessage = fmt.Sprintf("意外的任务状态: %d", taskResult.Status)
		return parsed
	}
}

// ParseDetachResult 解析 arthas_detach 的 TaskResult。
func ParseDetachResult(taskResult *model.TaskResult) *ParsedArthasResult {
	parsed := &ParsedArthasResult{
		TaskType:      model.ArthasTaskTypeDetach,
		RawResultJSON: taskResult.ResultJSON,
	}

	switch taskResult.Status {
	case model.TaskStatusTimeout:
		parsed.Timeout = true
		parsed.ErrorCode = model.ArthasErrDetachError
		parsed.ErrorMessage = "Arthas detach 超时"
		if taskResult.ErrorMessage != "" {
			parsed.ErrorMessage = taskResult.ErrorMessage
		}
		return parsed
	case model.TaskStatusFailed:
		parsed.ErrorCode = taskResult.ErrorCode
		parsed.ErrorMessage = taskResult.ErrorMessage
		if parsed.ErrorCode == "" {
			parsed.ErrorCode = model.ArthasErrDetachError
		}
		if parsed.ErrorMessage == "" {
			parsed.ErrorMessage = "Arthas detach 失败"
		}
		return parsed
	case model.TaskStatusSuccess:
		parsed.Success = true
		return parsed
	default:
		parsed.ErrorCode = model.ArthasErrDetachError
		parsed.ErrorMessage = fmt.Sprintf("意外的任务状态: %d", taskResult.Status)
		return parsed
	}
}

// IsRetryable 判断错误码是否建议重试。
func (r *ParsedArthasResult) IsRetryable() bool {
	switch r.ErrorCode {
	case model.ArthasErrNotRunning,
		model.ArthasErrNotReady,
		model.ArthasErrTunnelNotReady,
		model.ArthasErrClassLoaderUnavailable,
		model.ArthasErrBootstrapUnavailable,
		model.ArthasErrSessionManagerUnavailable,
		model.ArthasErrCommandExecutorInitFailed:
		// 初始化类错误，可能通过 attach 恢复
		return true
	default:
		return false
	}
}

// FormatError 返回人类可读的错误描述。
func (r *ParsedArthasResult) FormatError() string {
	if r.Success {
		return ""
	}
	msg := r.ErrorMessage
	if msg == "" {
		msg = "未知错误"
	}
	if r.ErrorCode != "" {
		return fmt.Sprintf("[%s] %s", r.ErrorCode, msg)
	}
	return msg
}

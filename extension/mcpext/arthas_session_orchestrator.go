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

// ========== Collector Session Orchestrator ==========
//
// 负责编排 Arthas 异步会话任务的完整生命周期：
//   - openSession: 创建会话 → 下发 arthas_session_open → 等待 Agent 返回 session_id
//   - executeAsync: 在会话中启动异步命令 → 下发 arthas_session_exec
//   - pullResults: 拉取增量结果 → 下发 arthas_session_pull → 返回 delta
//   - interrupt: 中断异步任务 → 下发 arthas_session_interrupt
//   - closeSession: 关闭会话 → 下发 arthas_session_close
//
// 设计原则：
//   - 不把持续结果塞进任务 RUNNING 状态
//   - 不新增独立长连接物理通道
//   - 不把 Arthas 原生 session 细节直接暴露给最上层
//   - 每次 pull 都是一个独立任务，空轮询不视为失败

// SessionOrchestrator 封装 Arthas 异步会话编排逻辑。
type SessionOrchestrator struct {
	controlPlane   controlplaneext.ControlPlane
	sessionManager *SessionManager
	logger         *zap.Logger

	// 超时配置
	sessionOpenTimeoutMs  int64         // session_open 超时（毫秒）
	sessionExecTimeoutMs  int64         // session_exec 超时（毫秒）
	sessionPullTimeoutMs  int64         // session_pull 超时（毫秒）
	sessionCloseTimeoutMs int64         // session_close 超时（毫秒）
	pollInterval          time.Duration // 轮询间隔

	// 重试配置
	maxOpenRetries      int // session_open 最大重试次数
	maxPullRetries      int // session_pull 最大重试次数
	maxInterruptRetries int // session_interrupt 最大重试次数
	maxCloseRetries     int // session_close 最大重试次数
}

// SessionOrchestratorConfig Session 编排器配置。
type SessionOrchestratorConfig struct {
	SessionOpenTimeoutMs  int64
	SessionExecTimeoutMs  int64
	SessionPullTimeoutMs  int64
	SessionCloseTimeoutMs int64
	PollInterval          time.Duration
	MaxOpenRetries        int
	MaxPullRetries        int
	MaxInterruptRetries   int
	MaxCloseRetries       int
}

// DefaultSessionOrchestratorConfig 返回默认配置。
func DefaultSessionOrchestratorConfig() SessionOrchestratorConfig {
	return SessionOrchestratorConfig{
		SessionOpenTimeoutMs:  30000,
		SessionExecTimeoutMs:  30000,
		SessionPullTimeoutMs:  3000, // pull 单次等待保持短轮询
		SessionCloseTimeoutMs: 15000,
		PollInterval:          500 * time.Millisecond,
		MaxOpenRetries:        2,
		MaxPullRetries:        3,
		MaxInterruptRetries:   2,
		MaxCloseRetries:       2,
	}
}

const retryBackoff = 1 * time.Second

// NewSessionOrchestrator 创建 Session 编排器。
func NewSessionOrchestrator(
	cp controlplaneext.ControlPlane,
	sm *SessionManager,
	logger *zap.Logger,
	config SessionOrchestratorConfig,
) *SessionOrchestrator {
	if config.SessionOpenTimeoutMs <= 0 {
		config.SessionOpenTimeoutMs = 30000
	}
	if config.SessionExecTimeoutMs <= 0 {
		config.SessionExecTimeoutMs = 30000
	}
	if config.SessionPullTimeoutMs <= 0 {
		config.SessionPullTimeoutMs = 3000
	}
	if config.SessionCloseTimeoutMs <= 0 {
		config.SessionCloseTimeoutMs = 15000
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 500 * time.Millisecond
	}

	return &SessionOrchestrator{
		controlPlane:          cp,
		sessionManager:        sm,
		logger:                logger.Named("session-orchestrator"),
		sessionOpenTimeoutMs:  config.SessionOpenTimeoutMs,
		sessionExecTimeoutMs:  config.SessionExecTimeoutMs,
		sessionPullTimeoutMs:  config.SessionPullTimeoutMs,
		sessionCloseTimeoutMs: config.SessionCloseTimeoutMs,
		pollInterval:          config.PollInterval,
		maxOpenRetries:        config.MaxOpenRetries,
		maxPullRetries:        config.MaxPullRetries,
		maxInterruptRetries:   config.MaxInterruptRetries,
		maxCloseRetries:       config.MaxCloseRetries,
	}
}

// ========== 请求/响应类型 ==========

// OpenSessionRequest 打开会话请求。
type OpenSessionRequest struct {
	AgentID     string
	RequestID   string        // 幂等键
	TTL         time.Duration // 会话总存活时间，0 使用默认值
	IdleTimeout time.Duration // 空闲超时，0 使用默认值
}

// OpenSessionResponse 打开会话响应。
type OpenSessionResponse struct {
	CollectorSessionID string
	AgentSessionID     string
	ConsumerID         string
	State              SessionState
	TaskID             string
	Error              *SessionError
}

// ExecAsyncRequest 异步执行命令请求。
type ExecAsyncRequest struct {
	CollectorSessionID string
	Command            string
	RequestID          string // 幂等键
}

// ExecAsyncResponse 异步执行命令响应。
type ExecAsyncResponse struct {
	CollectorSessionID string
	TaskID             string
	Accepted           bool   // 是否已受理
	Pending            bool   // 是否仍需后续确认
	State              string // 当前返回状态，如 EXECUTING / PENDING_CONFIRMATION
	ConfirmationMode   string // 后续确认方式
	ConfirmationHint   string // 后续确认提示
	Error              *SessionError
}

// PullResultsRequest 拉取结果请求。
type PullResultsRequest struct {
	CollectorSessionID string
	WaitTimeoutMs      int64 // pull 等待超时（毫秒），0 使用默认值
	MaxItems           int   // 最大条目数，0 不限制
	MaxBytes           int64 // 最大字节数，0 不限制
}

// PullResultsResponse 拉取结果响应。
type PullResultsResponse struct {
	CollectorSessionID string
	TaskID             string
	Delta              *SessionDelta
	Error              *SessionError
}

// SessionDelta 增量结果。
type SessionDelta struct {
	// Items 结构化结果条目
	Items []json.RawMessage `json:"items,omitempty"`
	// HasMore 是否还有更多数据
	HasMore bool `json:"hasMore"`
	// EndOfStream 当前异步命令是否已结束
	EndOfStream bool `json:"endOfStream"`
	// TotalItems 本次返回的条目数
	TotalItems int `json:"totalItems"`
}

// InterruptRequest 中断请求。
type InterruptRequest struct {
	CollectorSessionID string
	Reason             string
}

// InterruptResponse 中断响应。
type InterruptResponse struct {
	CollectorSessionID string
	TaskID             string
	Accepted           bool
	Pending            bool
	State              string
	ConfirmationMode   string
	ConfirmationHint   string
	Interrupted        bool
	Error              *SessionError
}

// CloseSessionRequest 关闭会话请求。
type CloseSessionRequest struct {
	CollectorSessionID string
	Reason             string
	Force              bool
}

// CloseSessionResponse 关闭会话响应。
type CloseSessionResponse struct {
	CollectorSessionID string
	TaskID             string
	Accepted           bool
	Pending            bool
	State              string
	ConfirmationMode   string
	ConfirmationHint   string
	Closed             bool
	Error              *SessionError
}

// ========== 核心编排方法 ==========

// OpenSession 打开异步会话。
//
// 编排流程：
//  1. 在 SessionManager 中创建会话（幂等检查）
//  2. 构造 arthas_session_open 任务
//  3. 提交到 Control Plane
//  4. 等待 Agent 返回 session_id
//  5. 激活会话
func (so *SessionOrchestrator) OpenSession(ctx context.Context, req *OpenSessionRequest) (*OpenSessionResponse, error) {
	if req.AgentID == "" {
		return nil, fmt.Errorf("agent_id 不能为空")
	}

	// Step 1: 在 SessionManager 中创建会话
	session, err := so.sessionManager.CreateSession(&CreateSessionRequest{
		AgentID:     req.AgentID,
		RequestID:   req.RequestID,
		TTL:         req.TTL,
		IdleTimeout: req.IdleTimeout,
	})
	if err != nil {
		return &OpenSessionResponse{
			Error: &SessionError{Code: "SESSION_CREATE_FAILED", Message: err.Error()},
		}, nil
	}

	// 如果会话已经激活（幂等命中），直接返回
	if session.State == SessionStateIdle {
		return &OpenSessionResponse{
			CollectorSessionID: session.CollectorSessionID,
			AgentSessionID:     session.AgentSessionID,
			ConsumerID:         session.ConsumerID,
			State:              session.State,
		}, nil
	}

	so.logger.Info("[session-open] 开始打开会话",
		zap.String("session_id", session.CollectorSessionID),
		zap.String("agent_id", req.AgentID),
	)

	// Step 2-4: 带重试的 session_open
	var lastErr *SessionError
	for attempt := 0; attempt <= so.maxOpenRetries; attempt++ {
		if attempt > 0 {
			so.logger.Info("[session-open] 重试",
				zap.String("session_id", session.CollectorSessionID),
				zap.Int("attempt", attempt),
				zap.String("last_error", lastErr.Message),
			)
			select {
			case <-ctx.Done():
				so.logger.Warn("[session-open] ctx 已取消，放弃重试",
					zap.String("session_id", session.CollectorSessionID),
					zap.Int("attempt", attempt),
					zap.Error(ctx.Err()),
				)
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}

		task := so.buildSessionOpenTask(session, req)

		so.logger.Info("[session-open] 提交任务",
			zap.String("session_id", session.CollectorSessionID),
			zap.String("task_id", task.ID),
			zap.String("task_type", task.TypeName),
			zap.String("agent_id", req.AgentID),
			zap.Int("attempt", attempt),
		)

		if err := so.controlPlane.SubmitTaskForAgent(ctx, req.AgentID, task); err != nil {
			so.logger.Error("[session-open] 任务提交失败",
				zap.String("session_id", session.CollectorSessionID),
				zap.String("task_id", task.ID),
				zap.Error(err),
			)
			lastErr = &SessionError{Code: "TASK_SUBMIT_FAILED", Message: err.Error()}
			continue
		}

		so.logger.Info("[session-open] 任务提交成功，开始轮询等待结果",
			zap.String("session_id", session.CollectorSessionID),
			zap.String("task_id", task.ID),
			zap.Int64("timeout_ms", so.sessionOpenTimeoutMs),
		)

		waitTimeout := time.Duration(so.sessionOpenTimeoutMs)*time.Millisecond + 10*time.Second
		taskResult, err := so.waitForTaskResult(ctx, task.ID, waitTimeout)
		if err != nil {
			so.logger.Warn("[session-open] 等待结果失败",
				zap.String("session_id", session.CollectorSessionID),
				zap.String("task_id", task.ID),
				zap.Duration("wait_timeout", waitTimeout),
				zap.Error(err),
			)
			lastErr = &SessionError{Code: "TASK_TIMEOUT", Message: fmt.Sprintf("等待 session_open 结果超时: %v", err)}
			continue
		}

		// 解析结果
		so.logger.Info("[session-open] 收到任务结果",
			zap.String("session_id", session.CollectorSessionID),
			zap.String("task_id", task.ID),
			zap.String("task_status", string(taskResult.Status)),
			zap.String("result_data", string(taskResult.ResultJSON)),
		)

		parsed := ParseSessionOpenResult(taskResult)
		if parsed.Success {
			// Step 5: 激活会话
			agentSessionID := parsed.AgentSessionID
			consumerID := parsed.ConsumerID
			if err := so.sessionManager.ActivateSession(session.CollectorSessionID, agentSessionID, consumerID); err != nil {
				so.logger.Error("[session-open] 激活会话失败",
					zap.String("session_id", session.CollectorSessionID),
					zap.String("agent_session_id", agentSessionID),
					zap.String("consumer_id", consumerID),
					zap.Error(err),
				)
				lastErr = &SessionError{Code: "SESSION_ACTIVATE_FAILED", Message: err.Error()}
				continue
			}

			so.logger.Info("[session-open] 会话打开成功",
				zap.String("collector_session_id", session.CollectorSessionID),
				zap.String("agent_session_id", agentSessionID),
				zap.String("consumer_id", consumerID),
			)

			return &OpenSessionResponse{
				CollectorSessionID: session.CollectorSessionID,
				AgentSessionID:     agentSessionID,
				ConsumerID:         consumerID,
				State:              SessionStateIdle,
				TaskID:             task.ID,
			}, nil
		}

		lastErr = &SessionError{Code: parsed.ErrorCode, Message: parsed.ErrorMessage}
		so.logger.Warn("[session-open] 执行失败",
			zap.String("session_id", session.CollectorSessionID),
			zap.String("error_code", parsed.ErrorCode),
			zap.Int("attempt", attempt),
		)
	}

	// 所有重试都失败
	so.sessionManager.SetError(session.CollectorSessionID, lastErr.Code, lastErr.Message)
	return &OpenSessionResponse{
		CollectorSessionID: session.CollectorSessionID,
		Error:              lastErr,
	}, nil
}

// ExecuteAsync 在会话中启动异步命令。
//
// 编排流程：
//  1. 验证会话状态（必须为 Idle）
//  2. 构造 arthas_session_exec 任务
//  3. 提交到 Control Plane
//  4. 等待 Agent 确认受理
//  5. 更新会话状态为 Executing
//
// 注意：session_exec 不自动重试（避免重复提交异步作业）。
func (so *SessionOrchestrator) ExecuteAsync(ctx context.Context, req *ExecAsyncRequest) (*ExecAsyncResponse, error) {
	if req.CollectorSessionID == "" {
		return nil, fmt.Errorf("session_id 不能为空")
	}
	if req.Command == "" {
		return nil, fmt.Errorf("command 不能为空")
	}

	// Step 1: 验证会话状态
	session, err := so.sessionManager.ValidateSessionForExec(req.CollectorSessionID)
	if err != nil {
		if se, ok := err.(*SessionError); ok {
			return &ExecAsyncResponse{
				CollectorSessionID: req.CollectorSessionID,
				Error:              se,
			}, nil
		}
		return nil, err
	}

	so.logger.Info("[session-exec] 开始执行异步命令",
		zap.String("session_id", req.CollectorSessionID),
		zap.String("command", req.Command),
	)

	// Step 2: 构造任务
	task := so.buildSessionExecTask(session, req)

	// Step 3: 提交任务
	so.logger.Info("[session-exec] 提交任务",
		zap.String("session_id", req.CollectorSessionID),
		zap.String("task_id", task.ID),
		zap.String("agent_id", session.AgentID),
		zap.String("agent_session_id", session.AgentSessionID),
		zap.String("command", req.Command),
	)

	if err := so.controlPlane.SubmitTaskForAgent(ctx, session.AgentID, task); err != nil {
		so.logger.Error("[session-exec] 任务提交失败",
			zap.String("session_id", req.CollectorSessionID),
			zap.String("task_id", task.ID),
			zap.Error(err),
		)
		return &ExecAsyncResponse{
			CollectorSessionID: req.CollectorSessionID,
			Error:              &SessionError{Code: "TASK_SUBMIT_FAILED", Message: err.Error()},
		}, nil
	}

	if err := so.sessionManager.SetExecuting(req.CollectorSessionID, req.Command, task.ID); err != nil {
		so.logger.Warn("[session-exec] 更新会话状态失败",
			zap.String("session_id", req.CollectorSessionID),
			zap.Error(err),
		)
	}

	so.logger.Info("[session-exec] 任务已受理，等待后续 pull/任务结果确认",
		zap.String("session_id", req.CollectorSessionID),
		zap.String("task_id", task.ID),
		zap.String("agent_session_id", session.AgentSessionID),
	)

	return &ExecAsyncResponse{
		CollectorSessionID: req.CollectorSessionID,
		TaskID:             task.ID,
		Accepted:           true,
		Pending:            true,
		State:              "EXECUTING",
		ConfirmationMode:   "session_pull_or_task_result",
		ConfirmationHint:   "命令已提交，请通过 session_pull 观察增量结果或等待任务结果进入终态",
	}, nil
}

// PullResults 拉取异步结果增量。
//
// 编排流程：
//  1. 验证会话状态
//  2. 构造 arthas_session_pull 任务
//  3. 提交到 Control Plane
//  4. 等待结果
//  5. 解析 delta
//  6. 如果 endOfStream=true，更新会话状态为 Idle
//
// 空轮询不视为失败（返回 items=[], hasMore=false, endOfStream=false）。
// session_pull 支持重试（读取型操作）。
func (so *SessionOrchestrator) PullResults(ctx context.Context, req *PullResultsRequest) (*PullResultsResponse, error) {
	if req.CollectorSessionID == "" {
		return nil, fmt.Errorf("session_id 不能为空")
	}

	// Step 1: 验证会话状态
	session, err := so.sessionManager.ValidateSessionForPull(req.CollectorSessionID)
	if err != nil {
		if se, ok := err.(*SessionError); ok {
			return &PullResultsResponse{
				CollectorSessionID: req.CollectorSessionID,
				Error:              se,
			}, nil
		}
		return nil, err
	}

	// 更新活跃时间
	so.sessionManager.TouchSession(req.CollectorSessionID)

	// Step 2-5: 带重试的 pull
	var lastErr *SessionError
	for attempt := 0; attempt <= so.maxPullRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}

		task := so.buildSessionPullTask(session, req)

		so.logger.Info("[session-pull] 提交任务",
			zap.String("collector_session_id", req.CollectorSessionID),
			zap.String("agent_session_id", session.AgentSessionID),
			zap.String("consumer_id", session.ConsumerID),
			zap.String("task_id", task.ID),
			zap.Int("attempt", attempt),
		)

		if err := so.controlPlane.SubmitTaskForAgent(ctx, session.AgentID, task); err != nil {
			lastErr = &SessionError{Code: "TASK_SUBMIT_FAILED", Message: err.Error()}
			continue
		}

		waitTimeoutMs := req.WaitTimeoutMs
		if waitTimeoutMs <= 0 {
			waitTimeoutMs = so.sessionPullTimeoutMs
		}
		waitTimeout := time.Duration(waitTimeoutMs)*time.Millisecond + 10*time.Second
		taskResult, err := so.waitForTaskResult(ctx, task.ID, waitTimeout)
		if err != nil {
			lastErr = &SessionError{Code: "TASK_TIMEOUT", Message: fmt.Sprintf("等待 session_pull 结果超时: %v", err)}
			continue
		}

		parsed := ParseSessionPullResult(taskResult)
		if !parsed.Success {
			if so.closeSessionOnRemoteTerminal(req.CollectorSessionID, session, parsed.ErrorCode, parsed.ErrorMessage, "session-pull") {
				return &PullResultsResponse{
					CollectorSessionID: req.CollectorSessionID,
					TaskID:             task.ID,
					Error:              &SessionError{Code: "SESSION_ALREADY_CLOSED", Message: "会话已关闭"},
				}, nil
			}
			lastErr = &SessionError{Code: parsed.ErrorCode, Message: parsed.ErrorMessage}
			if !parsed.IsRetryable() {
				break // 不可重试的错误，直接退出
			}
			continue
		}

		// Step 6: 如果 endOfStream，更新会话状态
		delta := parsed.Delta
		if delta != nil && delta.EndOfStream {
			if err := so.sessionManager.SetIdle(req.CollectorSessionID); err != nil {
				so.logger.Warn("[session-pull] 更新会话状态失败",
					zap.String("session_id", req.CollectorSessionID),
					zap.Error(err),
				)
			}
		}

		return &PullResultsResponse{
			CollectorSessionID: req.CollectorSessionID,
			TaskID:             task.ID,
			Delta:              delta,
		}, nil
	}

	return &PullResultsResponse{
		CollectorSessionID: req.CollectorSessionID,
		Error:              lastErr,
	}, nil
}

// Interrupt 中断异步任务。
//
// 编排流程：
//  1. 验证会话状态
//  2. 构造 arthas_session_interrupt 任务
//  3. 提交到 Control Plane
//  4. 等待确认
//  5. 更新会话状态为 Idle
func (so *SessionOrchestrator) Interrupt(ctx context.Context, req *InterruptRequest) (*InterruptResponse, error) {
	if req.CollectorSessionID == "" {
		return nil, fmt.Errorf("session_id 不能为空")
	}

	// Step 1: 验证会话状态
	session, err := so.sessionManager.ValidateSessionForInterrupt(req.CollectorSessionID)
	if err != nil {
		if se, ok := err.(*SessionError); ok {
			return &InterruptResponse{
				CollectorSessionID: req.CollectorSessionID,
				Error:              se,
			}, nil
		}
		return nil, err
	}

	if session.State == SessionStateIdle {
		so.logger.Info("[session-interrupt] 会话已是 idle，按幂等中断成功返回",
			zap.String("session_id", req.CollectorSessionID),
			zap.String("agent_session_id", session.AgentSessionID),
		)
		return &InterruptResponse{
			CollectorSessionID: req.CollectorSessionID,
			Accepted:           true,
			Interrupted:        true,
			State:              "IDLE",
			ConfirmationMode:   "session_state",
			ConfirmationHint:   "当前会话已无前台任务，可直接执行新命令或关闭会话",
		}, nil
	}

	so.logger.Info("[session-interrupt] 中断异步任务",
		zap.String("session_id", req.CollectorSessionID),
		zap.String("reason", req.Reason),
	)

	task := so.buildSessionInterruptTask(session, req)
	if err := so.controlPlane.SubmitTaskForAgent(ctx, session.AgentID, task); err != nil {
		return &InterruptResponse{
			CollectorSessionID: req.CollectorSessionID,
			Error:              &SessionError{Code: "TASK_SUBMIT_FAILED", Message: err.Error()},
		}, nil
	}

	go so.confirmInterrupt(task.ID, req.CollectorSessionID, session, req.Reason)

	return &InterruptResponse{
		CollectorSessionID: req.CollectorSessionID,
		TaskID:             task.ID,
		Accepted:           true,
		Pending:            true,
		State:              "PENDING_CONFIRMATION",
		ConfirmationMode:   "task_result_or_session_state",
		ConfirmationHint:   "中断请求已提交，请稍后查看 session 状态；若会话回到 idle 或再次中断返回无前台任务，则视为已完成",
	}, nil
}

// CloseSession 关闭会话。
//
// 编排流程：
//  1. 验证会话存在
//  2. 构造 arthas_session_close 任务
//  3. 提交到 Control Plane
//  4. 等待确认
//  5. 在 SessionManager 中关闭会话
func (so *SessionOrchestrator) CloseSession(ctx context.Context, req *CloseSessionRequest) (*CloseSessionResponse, error) {
	if req.CollectorSessionID == "" {
		return nil, fmt.Errorf("session_id 不能为空")
	}

	session, ok := so.sessionManager.GetSession(req.CollectorSessionID)
	if !ok {
		so.logger.Info("[session-close] 本地会话不存在，按幂等关闭返回",
			zap.String("collector_session_id", req.CollectorSessionID),
			zap.String("reason", req.Reason),
			zap.Bool("force", req.Force),
		)
		return &CloseSessionResponse{
			CollectorSessionID: req.CollectorSessionID,
			Accepted:           true,
			Closed:             true,
			State:              "CLOSED",
			ConfirmationMode:   "session_terminal",
			ConfirmationHint:   "Collector 本地会话已不存在，可视为关闭完成",
		}, nil
	}

	if session.IsTerminal() {
		fields := []zap.Field{
			zap.String("collector_session_id", req.CollectorSessionID),
			zap.String("agent_id", session.AgentID),
			zap.String("agent_session_id", session.AgentSessionID),
			zap.String("state", string(session.State)),
			zap.String("reason", req.Reason),
			zap.Bool("force", req.Force),
		}
		if session.LastErrorCode != "" {
			fields = append(fields, zap.String("last_error_code", session.LastErrorCode))
		}
		if session.LastErrorMessage != "" {
			fields = append(fields, zap.String("last_error_message", session.LastErrorMessage))
		}
		so.logger.Info("[session-close] 会话已是终态，按幂等关闭返回", fields...)
		return &CloseSessionResponse{
			CollectorSessionID: req.CollectorSessionID,
			Accepted:           true,
			Closed:             true,
			State:              "CLOSED",
			ConfirmationMode:   "session_terminal",
			ConfirmationHint:   "会话已处于终态，无需重复关闭",
		}, nil
	}

	closeFields := []zap.Field{
		zap.String("collector_session_id", req.CollectorSessionID),
		zap.String("agent_id", session.AgentID),
		zap.String("agent_session_id", session.AgentSessionID),
		zap.String("state", string(session.State)),
		zap.String("reason", req.Reason),
		zap.Bool("force", req.Force),
	}
	if session.CurrentCommand != "" {
		closeFields = append(closeFields, zap.String("current_command", session.CurrentCommand))
	}
	if session.CurrentTaskID != "" {
		closeFields = append(closeFields, zap.String("current_task_id", session.CurrentTaskID))
	}
	so.logger.Info("[session-close] 关闭会话", closeFields...)

	if session.AgentSessionID == "" {
		so.logger.Info("[session-close] 会话尚未激活远端 session，直接本地关闭",
			zap.String("collector_session_id", req.CollectorSessionID),
			zap.String("agent_id", session.AgentID),
			zap.String("state", string(session.State)),
			zap.String("reason", req.Reason),
			zap.Bool("force", req.Force),
		)
		_ = so.sessionManager.CloseSessionWithSource(req.CollectorSessionID, "local_only_close")
		return &CloseSessionResponse{
			CollectorSessionID: req.CollectorSessionID,
			Accepted:           true,
			Closed:             true,
			State:              "CLOSED",
			ConfirmationMode:   "session_terminal",
			ConfirmationHint:   "远端 session 尚未创建，Collector 已直接完成本地关闭",
		}, nil
	}

	previousState := session.State
	previousCommand := session.CurrentCommand
	previousTaskID := session.CurrentTaskID
	task := so.buildSessionCloseTask(session, req)
	if err := so.controlPlane.SubmitTaskForAgent(ctx, session.AgentID, task); err != nil {
		so.logger.Warn("[session-close] 提交任务失败",
			zap.String("collector_session_id", req.CollectorSessionID),
			zap.String("agent_session_id", session.AgentSessionID),
			zap.String("task_id", task.ID),
			zap.Error(err),
		)
		return &CloseSessionResponse{
			CollectorSessionID: req.CollectorSessionID,
			Error:              &SessionError{Code: "TASK_SUBMIT_FAILED", Message: err.Error()},
		}, nil
	}

	if err := so.sessionManager.SetClosing(req.CollectorSessionID, task.ID); err != nil {
		so.logger.Warn("[session-close] 标记会话为 closing 失败",
			zap.String("collector_session_id", req.CollectorSessionID),
			zap.String("task_id", task.ID),
			zap.Error(err),
		)
	}

	go so.confirmClose(task.ID, req.CollectorSessionID, session, req, previousState, previousCommand, previousTaskID)

	return &CloseSessionResponse{
		CollectorSessionID: req.CollectorSessionID,
		TaskID:             task.ID,
		Accepted:           true,
		Pending:            true,
		State:              "CLOSING",
		ConfirmationMode:   "task_result_or_session_terminal",
		ConfirmationHint:   "关闭请求已提交；若任务结果成功、返回 SESSION_NOT_FOUND，或本地 session 进入终态，均视为关闭完成",
	}, nil
}

func (so *SessionOrchestrator) closeSessionOnRemoteTerminal(collectorSessionID string, session *SessionInfo, errorCode, errorMessage, operation string) bool {
	if errorCode != model.ArthasErrSessionNotFound {
		return false
	}

	fields := []zap.Field{
		zap.String("operation", operation),
		zap.String("collector_session_id", collectorSessionID),
		zap.String("error_code", errorCode),
		zap.String("error_message", errorMessage),
	}
	if session != nil {
		fields = append(fields,
			zap.String("agent_session_id", session.AgentSessionID),
			zap.String("state_before", string(session.State)),
		)
		if session.CurrentCommand != "" {
			fields = append(fields, zap.String("current_command", session.CurrentCommand))
		}
		if session.CurrentTaskID != "" {
			fields = append(fields, zap.String("current_task_id", session.CurrentTaskID))
		}
	}

	if err := so.sessionManager.CloseSessionWithReason(collectorSessionID, errorCode, errorMessage); err != nil {
		so.logger.Warn("[session-terminal] 远端会话已终止，但本地收口失败",
			append(fields, zap.Error(err))...,
		)
		return false
	}

	so.logger.Warn("[session-terminal] 远端会话已终止，Collector 已完成终态收口", fields...)
	return true
}

// ========== 任务构造器 ==========

func (so *SessionOrchestrator) buildSessionOpenTask(session *SessionInfo, req *OpenSessionRequest) *model.Task {
	params := map[string]interface{}{
		model.ArthasParamRequestID: uuid.New().String(),
	}

	if session.TTL > 0 {
		params[model.ArthasParamTTLMs] = session.TTL.Milliseconds()
	}
	if session.IdleTimeout > 0 {
		params[model.ArthasParamIdleTimeoutMs] = session.IdleTimeout.Milliseconds()
	}

	paramsJSON, _ := json.Marshal(params)

	return &model.Task{
		ID:             uuid.New().String(),
		TypeName:       model.ArthasTaskTypeSessionOpen,
		ParametersJSON: paramsJSON,
		TimeoutMillis:  so.sessionOpenTimeoutMs,
		TargetAgentID:  req.AgentID,
	}
}

func (so *SessionOrchestrator) buildSessionExecTask(session *SessionInfo, req *ExecAsyncRequest) *model.Task {
	params := map[string]interface{}{
		model.ArthasParamSessionID: session.AgentSessionID,
		model.ArthasParamCommand:   req.Command,
		model.ArthasParamRequestID: uuid.New().String(),
	}

	paramsJSON, _ := json.Marshal(params)

	return &model.Task{
		ID:             uuid.New().String(),
		TypeName:       model.ArthasTaskTypeSessionExec,
		ParametersJSON: paramsJSON,
		TimeoutMillis:  so.sessionExecTimeoutMs,
		TargetAgentID:  session.AgentID,
	}
}

func (so *SessionOrchestrator) buildSessionPullTask(session *SessionInfo, req *PullResultsRequest) *model.Task {
	params := map[string]interface{}{
		model.ArthasParamSessionID:  session.AgentSessionID,
		model.ArthasParamConsumerID: session.ConsumerID,
		model.ArthasParamRequestID:  uuid.New().String(),
	}

	waitTimeoutMs := req.WaitTimeoutMs
	if waitTimeoutMs <= 0 {
		waitTimeoutMs = so.sessionPullTimeoutMs
	}
	params[model.ArthasParamWaitTimeoutMs] = waitTimeoutMs

	if req.MaxItems > 0 {
		params[model.ArthasParamMaxItems] = req.MaxItems
	}
	if req.MaxBytes > 0 {
		params[model.ArthasParamMaxBytes] = req.MaxBytes
	}

	paramsJSON, _ := json.Marshal(params)

	return &model.Task{
		ID:             uuid.New().String(),
		TypeName:       model.ArthasTaskTypeSessionPull,
		ParametersJSON: paramsJSON,
		TimeoutMillis:  waitTimeoutMs + 10000, // pull 任务超时 = 等待超时 + 缓冲
		TargetAgentID:  session.AgentID,
	}
}

func (so *SessionOrchestrator) buildSessionInterruptTask(session *SessionInfo, req *InterruptRequest) *model.Task {
	params := map[string]interface{}{
		model.ArthasParamSessionID: session.AgentSessionID,
		model.ArthasParamRequestID: uuid.New().String(),
	}
	if req.Reason != "" {
		params[model.ArthasParamReason] = req.Reason
	}

	paramsJSON, _ := json.Marshal(params)

	return &model.Task{
		ID:             uuid.New().String(),
		TypeName:       model.ArthasTaskTypeSessionInterrupt,
		ParametersJSON: paramsJSON,
		TimeoutMillis:  so.sessionCloseTimeoutMs,
		TargetAgentID:  session.AgentID,
	}
}

func (so *SessionOrchestrator) buildSessionCloseTask(session *SessionInfo, req *CloseSessionRequest) *model.Task {
	params := map[string]interface{}{
		model.ArthasParamSessionID: session.AgentSessionID,
		model.ArthasParamRequestID: uuid.New().String(),
	}
	if req.Reason != "" {
		params[model.ArthasParamReason] = req.Reason
	}
	if req.Force {
		params[model.ArthasParamForce] = true
	}

	paramsJSON, _ := json.Marshal(params)

	return &model.Task{
		ID:             uuid.New().String(),
		TypeName:       model.ArthasTaskTypeSessionClose,
		ParametersJSON: paramsJSON,
		TimeoutMillis:  so.sessionCloseTimeoutMs,
		TargetAgentID:  session.AgentID,
	}
}

func (so *SessionOrchestrator) sessionInterruptAttemptTimeout() time.Duration {
	return time.Duration(so.sessionCloseTimeoutMs) * time.Millisecond
}

func (so *SessionOrchestrator) sessionCloseAttemptTimeout() time.Duration {
	return time.Duration(so.sessionCloseTimeoutMs) * time.Millisecond
}

func (so *SessionOrchestrator) interruptOperationTimeout() time.Duration {
	return totalRetryBudget(so.sessionInterruptAttemptTimeout(), so.maxInterruptRetries)
}

func (so *SessionOrchestrator) closeOperationTimeout() time.Duration {
	return totalRetryBudget(so.sessionCloseAttemptTimeout(), so.maxCloseRetries)
}

func totalRetryBudget(attemptTimeout time.Duration, maxRetries int) time.Duration {
	attempts := maxRetries + 1
	if attempts <= 0 {
		attempts = 1
	}
	total := time.Duration(attempts) * attemptTimeout
	if attempts > 1 {
		total += time.Duration(attempts-1) * retryBackoff
	}
	return total + 5*time.Second
}

// ========== 任务结果等待 ==========

// waitForTaskResult 轮询等待任务结果直到终态或超时。
func (so *SessionOrchestrator) waitForTaskResult(ctx context.Context, taskID string, timeout time.Duration, extraFields ...zap.Field) (*model.TaskResult, error) {
	deadline := time.After(timeout)
	ticker := time.NewTicker(so.pollInterval)
	defer ticker.Stop()

	pollCount := 0
	startTime := time.Now()

	for {
		select {
		case <-ctx.Done():
			fields := []zap.Field{
				zap.String("task_id", taskID),
				zap.Duration("elapsed", time.Since(startTime)),
				zap.Int("poll_count", pollCount),
				zap.Error(ctx.Err()),
			}
			fields = append(fields, extraFields...)
			so.logger.Warn("[waitForTaskResult] ctx 已取消", fields...)
			return nil, ctx.Err()
		case <-deadline:
			fields := []zap.Field{
				zap.String("task_id", taskID),
				zap.Duration("timeout", timeout),
				zap.Duration("elapsed", time.Since(startTime)),
				zap.Int("poll_count", pollCount),
			}
			fields = append(fields, extraFields...)
			so.logger.Warn("[waitForTaskResult] 轮询超时", fields...)
			return nil, fmt.Errorf("timeout after %v (polled %d times)", timeout, pollCount)
		case <-ticker.C:
			pollCount++
			result, found, err := so.controlPlane.GetTaskResult(taskID)
			if err != nil {
				fields := []zap.Field{
					zap.String("task_id", taskID),
					zap.Int("poll_count", pollCount),
					zap.Duration("elapsed", time.Since(startTime)),
					zap.Error(err),
				}
				fields = append(fields, extraFields...)
				so.logger.Warn("[waitForTaskResult] 读取任务结果失败", fields...)
				return nil, fmt.Errorf("get task result for %s: %w", taskID, err)
			}
			if !found {
				continue
			}
			fields := []zap.Field{
				zap.String("task_id", taskID),
				zap.String("status", string(result.Status)),
				zap.Int("poll_count", pollCount),
				zap.Duration("elapsed", time.Since(startTime)),
			}
			fields = append(fields, extraFields...)
			so.logger.Debug("[waitForTaskResult] 获取到任务结果", fields...)
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

func (so *SessionOrchestrator) confirmInterrupt(taskID, collectorSessionID string, session *SessionInfo, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), so.interruptOperationTimeout())
	defer cancel()

	waitTimeout := so.sessionInterruptAttemptTimeout()
	taskResult, err := so.waitForTaskResult(
		ctx,
		taskID,
		waitTimeout,
		zap.String("operation", "session-interrupt"),
		zap.String("collector_session_id", collectorSessionID),
		zap.String("agent_session_id", session.AgentSessionID),
	)
	if err != nil {
		so.logger.Warn("[session-interrupt] 后台确认超时",
			zap.String("collector_session_id", collectorSessionID),
			zap.String("agent_session_id", session.AgentSessionID),
			zap.String("task_id", taskID),
			zap.String("reason", reason),
			zap.Error(err),
		)
		return
	}

	parsed := ParseSessionInterruptResult(taskResult)
	if parsed.Success || so.isInterruptNoopTerminal(parsed, collectorSessionID) {
		if err := so.sessionManager.SetIdle(collectorSessionID); err != nil {
			so.logger.Warn("[session-interrupt] 后台确认成功，但设置 idle 失败",
				zap.String("collector_session_id", collectorSessionID),
				zap.String("task_id", taskID),
				zap.Error(err),
			)
		}
		so.logger.Info("[session-interrupt] 后台确认完成，会话已回到 idle",
			zap.String("collector_session_id", collectorSessionID),
			zap.String("agent_session_id", session.AgentSessionID),
			zap.String("task_id", taskID),
		)
		return
	}

	if so.closeSessionOnRemoteTerminal(collectorSessionID, session, parsed.ErrorCode, parsed.ErrorMessage, "session-interrupt") {
		return
	}

	so.logger.Warn("[session-interrupt] 后台确认失败，保持当前会话状态",
		zap.String("collector_session_id", collectorSessionID),
		zap.String("agent_session_id", session.AgentSessionID),
		zap.String("task_id", taskID),
		zap.String("error_code", parsed.ErrorCode),
		zap.String("error_message", parsed.ErrorMessage),
	)
}

func (so *SessionOrchestrator) confirmClose(taskID, collectorSessionID string, session *SessionInfo, req *CloseSessionRequest, previousState SessionState, previousCommand, previousTaskID string) {
	ctx, cancel := context.WithTimeout(context.Background(), so.closeOperationTimeout())
	defer cancel()

	waitTimeout := so.sessionCloseAttemptTimeout()
	taskResult, err := so.waitForTaskResult(
		ctx,
		taskID,
		waitTimeout,
		zap.String("operation", "session-close"),
		zap.String("collector_session_id", collectorSessionID),
		zap.String("agent_session_id", session.AgentSessionID),
	)
	if err != nil {
		so.logger.Warn("[session-close] 后台确认失败",
			zap.String("collector_session_id", collectorSessionID),
			zap.String("agent_session_id", session.AgentSessionID),
			zap.String("task_id", taskID),
			zap.Error(err),
		)
		if req.Force {
			_ = so.sessionManager.CloseSessionWithSource(collectorSessionID, "force_close")
			return
		}
		_ = so.sessionManager.RestoreAfterCloseFailure(collectorSessionID, previousState, previousCommand, previousTaskID, "TASK_TIMEOUT", err.Error())
		return
	}

	parsed := ParseSessionCloseResult(taskResult)
	if parsed.Success {
		if err := so.sessionManager.CloseSessionWithSource(collectorSessionID, "explicit_close"); err != nil {
			so.logger.Warn("[session-close] 后台确认成功，但本地关闭失败",
				zap.String("collector_session_id", collectorSessionID),
				zap.String("task_id", taskID),
				zap.Error(err),
			)
		}
		return
	}

	if so.closeSessionOnRemoteTerminal(collectorSessionID, session, parsed.ErrorCode, parsed.ErrorMessage, "session-close") {
		return
	}

	if req.Force {
		so.logger.Warn("[session-close] 后台确认失败，按 force 收口本地会话",
			zap.String("collector_session_id", collectorSessionID),
			zap.String("agent_session_id", session.AgentSessionID),
			zap.String("task_id", taskID),
			zap.String("error_code", parsed.ErrorCode),
			zap.String("error_message", parsed.ErrorMessage),
		)
		_ = so.sessionManager.CloseSessionWithSource(collectorSessionID, "force_close")
		return
	}

	_ = so.sessionManager.RestoreAfterCloseFailure(collectorSessionID, previousState, previousCommand, previousTaskID, parsed.ErrorCode, parsed.ErrorMessage)
	so.logger.Warn("[session-close] 后台确认失败，会话已恢复",
		zap.String("collector_session_id", collectorSessionID),
		zap.String("agent_session_id", session.AgentSessionID),
		zap.String("task_id", taskID),
		zap.String("error_code", parsed.ErrorCode),
		zap.String("error_message", parsed.ErrorMessage),
	)
}

func (so *SessionOrchestrator) isInterruptNoopTerminal(parsed *ParsedSessionResult, collectorSessionID string) bool {
	if parsed == nil {
		return false
	}
	if parsed.ErrorCode == model.ArthasErrInterrupted {
		return true
	}
	if parsed.ErrorCode != model.ArthasErrCommandExecutionFailed {
		return false
	}
	if parsed.ErrorMessage == "no foreground job is running" {
		so.logger.Info("[session-interrupt] 远端返回无前台任务，按幂等中断成功收敛",
			zap.String("collector_session_id", collectorSessionID),
			zap.String("error_code", parsed.ErrorCode),
			zap.String("error_message", parsed.ErrorMessage),
		)
		return true
	}
	return false
}
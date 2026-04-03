// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mcpext

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ========== Collector Session Manager ==========
//
// 管理 Arthas 异步会话的完整生命周期：
//   - collector_session_id ↔ agent_session_id 映射
//   - 会话状态机（Opening → Idle → Executing → Idle → Closed）
//   - TTL / idle 超时回收
//   - 基于 request_id 的幂等控制
//
// 设计原则：
//   - 不直接暴露 Arthas 原生 session 细节给上层
//   - Collector 侧 session_id 与 Agent 侧 session_id 解耦
//   - 会话级错误映射统一在此层处理

// SessionState 会话状态。
type SessionState string

const (
	// SessionStateOpening 正在打开（等待 Agent 侧创建完成）
	SessionStateOpening SessionState = "opening"
	// SessionStateIdle 空闲（可以执行新命令）
	SessionStateIdle SessionState = "idle"
	// SessionStateExecuting 正在执行异步命令
	SessionStateExecuting SessionState = "executing"
	// SessionStateClosing 正在关闭（已受理 close，请等待终态收口）
	SessionStateClosing SessionState = "closing"
	// SessionStateClosed 已关闭
	SessionStateClosed SessionState = "closed"
	// SessionStateError 错误状态
	SessionStateError SessionState = "error"
)

// SessionInfo 会话信息。
type SessionInfo struct {
	// CollectorSessionID Collector 侧会话 ID（对外暴露）
	CollectorSessionID string
	// AgentID 目标 Agent ID
	AgentID string
	// AgentSessionID Agent 侧会话 ID（由 Agent 返回）
	AgentSessionID string
	// ConsumerID 消费者标识（用于 pull）
	ConsumerID string
	// State 当前状态
	State SessionState
	// CurrentCommand 当前正在执行的命令（仅 Executing 状态）
	CurrentCommand string
	// CurrentTaskID 当前任务 ID（用于追踪）
	CurrentTaskID string

	// 时间戳
	CreatedAt    time.Time
	LastActiveAt time.Time

	// 超时配置
	TTL         time.Duration // 总存活时间
	IdleTimeout time.Duration // 空闲超时

	// 错误信息
	LastErrorCode    string
	LastErrorMessage string
}

// IsTerminal 判断会话是否处于终态。
func (s *SessionInfo) IsTerminal() bool {
	return s.State == SessionStateClosed || s.State == SessionStateError
}

// IsExpired 判断会话是否已过期（TTL 超限）。
func (s *SessionInfo) IsExpired() bool {
	if s.TTL <= 0 {
		return false
	}
	return time.Since(s.CreatedAt) > s.TTL
}

// IsIdle 判断会话是否空闲超时。
func (s *SessionInfo) IsIdle() bool {
	if s.IdleTimeout <= 0 {
		return false
	}
	return time.Since(s.LastActiveAt) > s.IdleTimeout
}

// SessionManagerConfig Session Manager 配置。
type SessionManagerConfig struct {
	// DefaultTTL 默认会话总存活时间，默认 10 分钟
	DefaultTTL time.Duration
	// DefaultIdleTimeout 默认空闲超时，默认 2 分钟
	DefaultIdleTimeout time.Duration
	// MaxSessions 最大并发会话数，默认 20
	MaxSessions int
	// ReapInterval 回收检查间隔，默认 30 秒
	ReapInterval time.Duration
	// IdempotencyTTL 幂等键缓存时间，默认 5 分钟
	IdempotencyTTL time.Duration
}

// DefaultSessionManagerConfig 返回默认配置。
func DefaultSessionManagerConfig() SessionManagerConfig {
	return SessionManagerConfig{
		DefaultTTL:         10 * time.Minute,
		DefaultIdleTimeout: 2 * time.Minute,
		MaxSessions:        20,
		ReapInterval:       30 * time.Second,
		IdempotencyTTL:     5 * time.Minute,
	}
}

// SessionManager 管理 Arthas 异步会话。
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*SessionInfo // collector_session_id → SessionInfo
	logger   *zap.Logger
	config   SessionManagerConfig

	// 幂等控制：request_id → collector_session_id
	idempotencyCache map[string]*idempotencyEntry

	// 回收控制
	stopReaper chan struct{}
	reaperDone chan struct{}
}

// idempotencyEntry 幂等缓存条目。
type idempotencyEntry struct {
	CollectorSessionID string
	CreatedAt          time.Time
}

// NewSessionManager 创建 Session Manager。
func NewSessionManager(logger *zap.Logger, config SessionManagerConfig) *SessionManager {
	if config.DefaultTTL <= 0 {
		config.DefaultTTL = 10 * time.Minute
	}
	if config.DefaultIdleTimeout <= 0 {
		config.DefaultIdleTimeout = 2 * time.Minute
	}
	if config.MaxSessions <= 0 {
		config.MaxSessions = 20
	}
	if config.ReapInterval <= 0 {
		config.ReapInterval = 30 * time.Second
	}
	if config.IdempotencyTTL <= 0 {
		config.IdempotencyTTL = 5 * time.Minute
	}

	return &SessionManager{
		sessions:         make(map[string]*SessionInfo),
		logger:           logger.Named("session-manager"),
		config:           config,
		idempotencyCache: make(map[string]*idempotencyEntry),
		stopReaper:       make(chan struct{}),
		reaperDone:       make(chan struct{}),
	}
}

// Start 启动 Session Manager（包括后台回收协程）。
func (sm *SessionManager) Start() {
	go sm.reapLoop()
	sm.logger.Info("[session-manager] 已启动",
		zap.Duration("default_ttl", sm.config.DefaultTTL),
		zap.Duration("default_idle_timeout", sm.config.DefaultIdleTimeout),
		zap.Int("max_sessions", sm.config.MaxSessions),
		zap.Duration("reap_interval", sm.config.ReapInterval),
	)
}

// Stop 停止 Session Manager。
func (sm *SessionManager) Stop() {
	close(sm.stopReaper)
	<-sm.reaperDone
	sm.logger.Info("[session-manager] 已停止")
}

// ========== 会话生命周期操作 ==========

// CreateSessionRequest 创建会话请求。
type CreateSessionRequest struct {
	AgentID     string
	RequestID   string        // 幂等键
	TTL         time.Duration // 0 使用默认值
	IdleTimeout time.Duration // 0 使用默认值
}

// CreateSession 创建新会话。
// 如果 RequestID 已存在（幂等），返回已有的 session。
func (sm *SessionManager) CreateSession(req *CreateSessionRequest) (*SessionInfo, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 幂等检查
	if req.RequestID != "" {
		if entry, ok := sm.idempotencyCache[req.RequestID]; ok {
			if session, exists := sm.sessions[entry.CollectorSessionID]; exists {
				sm.logger.Info("[session-manager] 幂等命中，返回已有会话",
					zap.String("request_id", req.RequestID),
					zap.String("session_id", entry.CollectorSessionID),
				)
				return session, nil
			}
		}
	}

	// 检查并发会话数限制
	activeCount := 0
	for _, s := range sm.sessions {
		if !s.IsTerminal() {
			activeCount++
		}
	}
	if activeCount >= sm.config.MaxSessions {
		return nil, fmt.Errorf("已达到最大并发会话数限制 (%d)", sm.config.MaxSessions)
	}

	// 创建新会话
	ttl := req.TTL
	if ttl <= 0 {
		ttl = sm.config.DefaultTTL
	}
	idleTimeout := req.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = sm.config.DefaultIdleTimeout
	}

	now := time.Now()
	session := &SessionInfo{
		CollectorSessionID: uuid.New().String(),
		AgentID:            req.AgentID,
		State:              SessionStateOpening,
		CreatedAt:          now,
		LastActiveAt:       now,
		TTL:                ttl,
		IdleTimeout:        idleTimeout,
	}

	sm.sessions[session.CollectorSessionID] = session

	// 记录幂等键
	if req.RequestID != "" {
		sm.idempotencyCache[req.RequestID] = &idempotencyEntry{
			CollectorSessionID: session.CollectorSessionID,
			CreatedAt:          now,
		}
	}

	sm.logger.Info("[session-manager] 会话已创建",
		zap.String("session_id", session.CollectorSessionID),
		zap.String("agent_id", req.AgentID),
		zap.Duration("ttl", ttl),
		zap.Duration("idle_timeout", idleTimeout),
	)

	return session, nil
}

// ActivateSession 激活会话（Agent 侧创建成功后调用）。
func (sm *SessionManager) ActivateSession(collectorSessionID, agentSessionID, consumerID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return fmt.Errorf("会话不存在: %s", collectorSessionID)
	}
	if session.IsTerminal() {
		return fmt.Errorf("会话已关闭: %s", collectorSessionID)
	}
	if agentSessionID == "" {
		return fmt.Errorf("agent_session_id 不能为空: %s", collectorSessionID)
	}
	if consumerID == "" {
		return fmt.Errorf("consumer_id 不能为空: %s", collectorSessionID)
	}

	session.AgentSessionID = agentSessionID
	session.ConsumerID = consumerID
	session.State = SessionStateIdle
	session.LastActiveAt = time.Now()

	sm.logger.Info("[session-manager] 会话已激活",
		zap.String("session_id", collectorSessionID),
		zap.String("agent_session_id", agentSessionID),
		zap.String("consumer_id", consumerID),
	)

	return nil
}

// SetExecuting 设置会话为执行中状态。
func (sm *SessionManager) SetExecuting(collectorSessionID, command, taskID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return fmt.Errorf("会话不存在: %s", collectorSessionID)
	}
	if session.IsTerminal() {
		return fmt.Errorf("会话已关闭: %s", collectorSessionID)
	}
	if session.State == SessionStateExecuting {
		return fmt.Errorf("会话正在执行命令，不能同时执行多个命令: %s", collectorSessionID)
	}
	if session.State == SessionStateOpening {
		return fmt.Errorf("会话尚未激活: %s", collectorSessionID)
	}

	session.State = SessionStateExecuting
	session.CurrentCommand = command
	session.CurrentTaskID = taskID
	session.LastActiveAt = time.Now()

	return nil
}

// SetIdle 设置会话为空闲状态（命令执行完成后）。
func (sm *SessionManager) SetIdle(collectorSessionID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return fmt.Errorf("会话不存在: %s", collectorSessionID)
	}
	if session.IsTerminal() {
		return nil // 已关闭，忽略
	}

	session.State = SessionStateIdle
	session.CurrentCommand = ""
	session.CurrentTaskID = ""
	session.LastActiveAt = time.Now()

	return nil
}

// SetClosing 设置会话为关闭中状态，阻止后续继续使用该会话。
func (sm *SessionManager) SetClosing(collectorSessionID, taskID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return fmt.Errorf("会话不存在: %s", collectorSessionID)
	}
	if session.IsTerminal() {
		return nil
	}

	session.State = SessionStateClosing
	if taskID != "" {
		session.CurrentTaskID = taskID
	}
	session.LastActiveAt = time.Now()

	return nil
}

// RestoreAfterCloseFailure 在异步 close 最终失败时恢复会话状态。
func (sm *SessionManager) RestoreAfterCloseFailure(collectorSessionID string, previousState SessionState, previousCommand, previousTaskID, errorCode, errorMessage string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return fmt.Errorf("会话不存在: %s", collectorSessionID)
	}
	if session.IsTerminal() {
		return nil
	}

	if previousState == "" || previousState == SessionStateClosing {
		previousState = SessionStateIdle
	}

	session.State = previousState
	session.CurrentCommand = previousCommand
	session.CurrentTaskID = previousTaskID
	session.LastActiveAt = time.Now()
	session.LastErrorCode = errorCode
	session.LastErrorMessage = errorMessage

	sm.logger.Warn("[session-manager] 异步 close 最终失败，已恢复会话状态",
		zap.String("session_id", collectorSessionID),
		zap.String("restored_state", string(previousState)),
		zap.String("previous_command", previousCommand),
		zap.String("previous_task_id", previousTaskID),
		zap.String("error_code", errorCode),
		zap.String("error_message", errorMessage),
	)
	return nil
}

// TouchSession 更新会话活跃时间（pull 时调用）。
func (sm *SessionManager) TouchSession(collectorSessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if session, ok := sm.sessions[collectorSessionID]; ok {
		session.LastActiveAt = time.Now()
	}
}

// SetError 设置会话错误状态。
func (sm *SessionManager) SetError(collectorSessionID, errorCode, errorMessage string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if session, ok := sm.sessions[collectorSessionID]; ok {
		session.State = SessionStateError
		session.LastErrorCode = errorCode
		session.LastErrorMessage = errorMessage
	}
}

func (sm *SessionManager) closeSessionLocked(collectorSessionID string, session *SessionInfo, errorCode, errorMessage, closeSource string) error {
	if session.IsTerminal() {
		return nil // 已关闭，幂等
	}

	stateBefore := session.State
	currentCommand := session.CurrentCommand
	currentTaskID := session.CurrentTaskID

	session.State = SessionStateClosed
	session.CurrentCommand = ""
	session.CurrentTaskID = ""
	session.LastActiveAt = time.Now()
	if errorCode != "" || errorMessage != "" {
		session.LastErrorCode = errorCode
		session.LastErrorMessage = errorMessage
	}

	fields := []zap.Field{
		zap.String("session_id", collectorSessionID),
		zap.String("agent_id", session.AgentID),
		zap.String("state_before", string(stateBefore)),
	}
	if closeSource != "" {
		fields = append(fields, zap.String("close_source", closeSource))
	}
	if currentCommand != "" {
		fields = append(fields, zap.String("current_command", currentCommand))
	}
	if currentTaskID != "" {
		fields = append(fields, zap.String("current_task_id", currentTaskID))
	}
	if errorCode != "" {
		fields = append(fields, zap.String("error_code", errorCode))
	}
	if errorMessage != "" {
		fields = append(fields, zap.String("error_message", errorMessage))
	}

	sm.logger.Info("[session-manager] 会话已关闭", fields...)
	return nil
}

// CloseSession 关闭会话。
func (sm *SessionManager) CloseSession(collectorSessionID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return fmt.Errorf("会话不存在: %s", collectorSessionID)
	}

	return sm.closeSessionLocked(collectorSessionID, session, "", "", "explicit_close")
}

// CloseSessionWithSource 关闭会话并记录关闭来源。
func (sm *SessionManager) CloseSessionWithSource(collectorSessionID, closeSource string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return fmt.Errorf("会话不存在: %s", collectorSessionID)
	}

	return sm.closeSessionLocked(collectorSessionID, session, "", "", closeSource)
}

// CloseSessionWithReason 关闭会话并记录终态原因。
func (sm *SessionManager) CloseSessionWithReason(collectorSessionID, errorCode, errorMessage string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return fmt.Errorf("会话不存在: %s", collectorSessionID)
	}

	return sm.closeSessionLocked(collectorSessionID, session, errorCode, errorMessage, "remote_terminal")
}

// ========== 查询操作 ==========

// GetSession 获取会话信息。
func (sm *SessionManager) GetSession(collectorSessionID string) (*SessionInfo, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return nil, false
	}
	// 返回副本，避免外部修改
	copy := *session
	return &copy, true
}

// GetSessionForAgent 获取指定 Agent 的所有活跃会话。
func (sm *SessionManager) GetSessionForAgent(agentID string) []*SessionInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var result []*SessionInfo
	for _, s := range sm.sessions {
		if s.AgentID == agentID && !s.IsTerminal() {
			copy := *s
			result = append(result, &copy)
		}
	}
	return result
}

// GetActiveSessions 获取所有活跃会话。
func (sm *SessionManager) GetActiveSessions() []*SessionInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var result []*SessionInfo
	for _, s := range sm.sessions {
		if !s.IsTerminal() {
			copy := *s
			result = append(result, &copy)
		}
	}
	return result
}

// GetSessionStats 获取会话统计信息。
func (sm *SessionManager) GetSessionStats() map[string]int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	stats := map[string]int{
		"total":     len(sm.sessions),
		"opening":   0,
		"idle":      0,
		"executing": 0,
		"closing":   0,
		"closed":    0,
		"error":     0,
	}
	for _, s := range sm.sessions {
		switch s.State {
		case SessionStateOpening:
			stats["opening"]++
		case SessionStateIdle:
			stats["idle"]++
		case SessionStateExecuting:
			stats["executing"]++
		case SessionStateClosing:
			stats["closing"]++
		case SessionStateClosed:
			stats["closed"]++
		case SessionStateError:
			stats["error"]++
		}
	}
	return stats
}

// ========== 后台回收 ==========

// reapLoop 后台回收过期和空闲超时的会话。
func (sm *SessionManager) reapLoop() {
	defer close(sm.reaperDone)

	ticker := time.NewTicker(sm.config.ReapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sm.stopReaper:
			return
		case <-ticker.C:
			sm.reapExpiredSessions()
			sm.reapIdempotencyCache()
		}
	}
}

// reapExpiredSessions 回收过期和空闲超时的会话。
func (sm *SessionManager) reapExpiredSessions() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var reaped []string
	for id, session := range sm.sessions {
		if session.IsTerminal() {
			// 终态会话保留一段时间后清理
			if time.Since(session.LastActiveAt) > sm.config.IdempotencyTTL {
				reaped = append(reaped, id)
			}
			continue
		}

		if session.IsExpired() {
			session.State = SessionStateClosed
			session.LastErrorCode = "SESSION_TTL_EXCEEDED"
			session.LastErrorMessage = fmt.Sprintf("会话总存活时间超过限制 (%v)", session.TTL)
			sm.logger.Warn("[session-manager] 会话 TTL 超时，已回收",
				zap.String("session_id", id),
				zap.String("agent_id", session.AgentID),
				zap.Duration("ttl", session.TTL),
			)
			continue
		}

		if session.IsIdle() && session.State == SessionStateIdle {
			session.State = SessionStateClosed
			session.LastErrorCode = "SESSION_IDLE_TIMEOUT"
			session.LastErrorMessage = fmt.Sprintf("会话空闲超时 (%v)", session.IdleTimeout)
			sm.logger.Warn("[session-manager] 会话空闲超时，已回收",
				zap.String("session_id", id),
				zap.String("agent_id", session.AgentID),
				zap.Duration("idle_timeout", session.IdleTimeout),
			)
		}
	}

	// 清理已终态且过期的会话
	for _, id := range reaped {
		delete(sm.sessions, id)
	}

	if len(reaped) > 0 {
		sm.logger.Info("[session-manager] 清理终态会话",
			zap.Int("count", len(reaped)),
		)
	}
}

// reapIdempotencyCache 清理过期的幂等缓存。
func (sm *SessionManager) reapIdempotencyCache() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var expired []string
	for requestID, entry := range sm.idempotencyCache {
		if time.Since(entry.CreatedAt) > sm.config.IdempotencyTTL {
			expired = append(expired, requestID)
		}
	}

	for _, requestID := range expired {
		delete(sm.idempotencyCache, requestID)
	}
}

// ========== 会话级错误映射 ==========

// ValidateSessionForExec 验证会话是否可以执行新命令。
func (sm *SessionManager) ValidateSessionForExec(collectorSessionID string) (*SessionInfo, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return nil, &SessionError{Code: "SESSION_NOT_FOUND", Message: "会话不存在"}
	}

	if session.IsExpired() {
		return nil, &SessionError{Code: "SESSION_TTL_EXCEEDED", Message: "会话已过期"}
	}

	switch session.State {
	case SessionStateClosed:
		return nil, &SessionError{Code: "SESSION_ALREADY_CLOSED", Message: "会话已关闭"}
	case SessionStateError:
		return nil, &SessionError{Code: "SESSION_ERROR", Message: fmt.Sprintf("会话处于错误状态: %s", session.LastErrorMessage)}
	case SessionStateOpening:
		return nil, &SessionError{Code: "SESSION_NOT_READY", Message: "会话尚未激活"}
	case SessionStateClosing:
		return nil, &SessionError{Code: "SESSION_CLOSING", Message: "会话正在关闭，请等待终态收口"}
	case SessionStateExecuting:
		return nil, &SessionError{Code: "SESSION_NOT_IDLE", Message: "会话正在执行命令，不能同时执行多个命令"}
	case SessionStateIdle:
		copy := *session
		return &copy, nil
	default:
		return nil, &SessionError{Code: "SESSION_ERROR", Message: fmt.Sprintf("未知会话状态: %s", session.State)}
	}
}

// ValidateSessionForPull 验证会话是否可以拉取结果。
func (sm *SessionManager) ValidateSessionForPull(collectorSessionID string) (*SessionInfo, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return nil, &SessionError{Code: "SESSION_NOT_FOUND", Message: "会话不存在"}
	}

	switch session.State {
	case SessionStateClosed:
		return nil, &SessionError{Code: "SESSION_ALREADY_CLOSED", Message: "会话已关闭"}
	case SessionStateError:
		return nil, &SessionError{Code: "SESSION_ERROR", Message: fmt.Sprintf("会话处于错误状态: %s", session.LastErrorMessage)}
	case SessionStateOpening:
		return nil, &SessionError{Code: "SESSION_NOT_READY", Message: "会话尚未激活"}
	case SessionStateClosing:
		return nil, &SessionError{Code: "SESSION_CLOSING", Message: "会话正在关闭，请勿继续拉取结果"}
	case SessionStateIdle:
		// 空闲状态也可以 pull（可能有残留结果）
		copy := *session
		return &copy, nil
	case SessionStateExecuting:
		copy := *session
		return &copy, nil
	default:
		return nil, &SessionError{Code: "SESSION_ERROR", Message: fmt.Sprintf("未知会话状态: %s", session.State)}
	}
}

// ValidateSessionForInterrupt 验证会话是否可以中断。
func (sm *SessionManager) ValidateSessionForInterrupt(collectorSessionID string) (*SessionInfo, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[collectorSessionID]
	if !ok {
		return nil, &SessionError{Code: "SESSION_NOT_FOUND", Message: "会话不存在"}
	}

	if session.IsTerminal() {
		return nil, &SessionError{Code: "SESSION_ALREADY_CLOSED", Message: "会话已关闭"}
	}
	if session.State == SessionStateOpening {
		return nil, &SessionError{Code: "SESSION_NOT_READY", Message: "会话尚未激活"}
	}
	if session.State == SessionStateClosing {
		return nil, &SessionError{Code: "SESSION_CLOSING", Message: "会话正在关闭，不能继续中断"}
	}

	copy := *session
	return &copy, nil
}

// SessionError 会话级错误。
type SessionError struct {
	Code    string
	Message string
}

func (e *SessionError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}
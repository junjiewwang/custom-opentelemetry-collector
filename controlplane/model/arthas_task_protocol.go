// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package model

// Arthas Collector / Agent 任务协议常量。
//
// 该文件用于冻结双端共享的协议标识，避免 task type、参数键、结果字段和错误码
// 在多个模块中散落定义。
//
// 设计约束：
//   - 这里只定义协议层常量，不承载执行逻辑
//   - 允许先冻结未来 Phase 使用的 task type，但实际能力上报仍应只声明当前真实已支持的执行器
//   - 后续新增 Arthas 任务能力时，应优先复用此文件而不是重新硬编码字符串
//
// 对应 Agent 侧：
//   io.opentelemetry.sdk.extension.controlplane.arthas.ArthasTaskProtocol

// ========== 冻结的 Arthas task type 列表 ==========

// FrozenArthasTaskTypes 表达协议边界，不代表当前 Collector/Agent 已全部实现。
var FrozenArthasTaskTypes = []string{
	ArthasTaskTypeAttach,
	ArthasTaskTypeDetach,
	ArthasTaskTypeExecSync,
	ArthasTaskTypeSessionOpen,
	ArthasTaskTypeSessionExec,
	ArthasTaskTypeSessionPull,
	ArthasTaskTypeSessionInterrupt,
	ArthasTaskTypeSessionClose,
}

// ========== Arthas 任务类型 ==========

const (
	// ArthasTaskTypeAttach 启动 / 接入 Arthas（非会话化）
	ArthasTaskTypeAttach = "arthas_attach"
	// ArthasTaskTypeDetach 停止 / 断开 Arthas（非会话化）
	ArthasTaskTypeDetach = "arthas_detach"
	// ArthasTaskTypeExecSync 执行同步查询命令（非会话化）
	ArthasTaskTypeExecSync = "arthas_exec_sync"
	// ArthasTaskTypeSessionOpen 创建异步会话（会话化）
	ArthasTaskTypeSessionOpen = "arthas_session_open"
	// ArthasTaskTypeSessionExec 在指定 session 中启动异步命令（会话化）
	ArthasTaskTypeSessionExec = "arthas_session_exec"
	// ArthasTaskTypeSessionPull 拉取异步结果增量（会话化）
	ArthasTaskTypeSessionPull = "arthas_session_pull"
	// ArthasTaskTypeSessionInterrupt 中断异步任务（会话化）
	ArthasTaskTypeSessionInterrupt = "arthas_session_interrupt"
	// ArthasTaskTypeSessionClose 关闭会话并回收资源（会话化）
	ArthasTaskTypeSessionClose = "arthas_session_close"
)

// ========== Arthas 任务公共参数键 ==========
// 对应 Agent 侧 ArthasTaskProtocol.ParameterKey

const (
	// ArthasParamRequestID 幂等键，用于重试去重
	ArthasParamRequestID = "request_id"
	// ArthasParamTraceID 便于跨端日志关联
	ArthasParamTraceID = "trace_id"
	// ArthasParamUserID 操作者身份
	ArthasParamUserID = "user_id"
	// ArthasParamAuthSubject 透传给 Arthas 的认证主体
	ArthasParamAuthSubject = "auth_subject"
	// ArthasParamCommand Arthas 命令字符串
	ArthasParamCommand = "command"
	// ArthasParamAction 操作动作（如 attach/detach）
	ArthasParamAction = "action"
	// ArthasParamReason 操作原因（如 interrupt/close 的原因）
	ArthasParamReason = "reason"
	// ArthasParamSessionID 会话标识
	ArthasParamSessionID = "session_id"
	// ArthasParamConsumerID 消费者标识（用于 pull）
	ArthasParamConsumerID = "consumer_id"
	// ArthasParamForce 是否强制执行
	ArthasParamForce = "force"
	// ArthasParamAutoAttach 未启动 Arthas 时是否自动 attach
	ArthasParamAutoAttach = "auto_attach"
	// ArthasParamRequireTunnelReady 是否要求 Arthas 已处于可交互就绪
	ArthasParamRequireTunnelReady = "require_tunnel_ready"
	// ArthasParamTimeoutMs 命令执行超时（毫秒）
	ArthasParamTimeoutMs = "timeout_ms"
	// ArthasParamWaitTimeoutMs pull 等待超时（毫秒）
	ArthasParamWaitTimeoutMs = "wait_timeout_ms"
	// ArthasParamResultLimitBytes 单次结果大小限制（字节）
	ArthasParamResultLimitBytes = "result_limit_bytes"
	// ArthasParamMaxItems pull 最大条目数
	ArthasParamMaxItems = "max_items"
	// ArthasParamMaxBytes pull 最大字节数
	ArthasParamMaxBytes = "max_bytes"
	// ArthasParamTTLMs 会话总存活时间（毫秒）
	ArthasParamTTLMs = "ttl_ms"
	// ArthasParamIdleTimeoutMs 会话空闲超时（毫秒）
	ArthasParamIdleTimeoutMs = "idle_timeout_ms"

	// ArthasParamStartTimeoutMs attach 启动超时（毫秒）
	ArthasParamStartTimeoutMs = "start_timeout_millis"
	// ArthasParamConnectTimeoutMs attach 连接超时（毫秒）
	ArthasParamConnectTimeoutMs = "connect_timeout_millis"
	// ArthasParamStopTimeoutMs detach 停止超时（毫秒）
	ArthasParamStopTimeoutMs = "stop_timeout_millis"
	// ArthasParamHealthCheckGracePeriodMs 健康检查宽限期（毫秒）
	ArthasParamHealthCheckGracePeriodMs = "health_check_grace_period_millis"
)

// ========== Arthas 任务结果 JSON 字段 ==========
// 对应 Agent 侧 ArthasTaskProtocol.ResultField

const (
	// ArthasResultTaskType 当前任务类型
	ArthasResultTaskType = "taskType"
	// ArthasResultSuccess 是否执行成功
	ArthasResultSuccess = "success"
	// ArthasResultCommand 原始命令
	ArthasResultCommand = "command"
	// ArthasResultSessionID session 标识
	ArthasResultSessionID = "sessionId"
	// ArthasResultConsumerID 消费者标识
	ArthasResultConsumerID = "consumerId"
	// ArthasResultState 状态
	ArthasResultState = "state"
	// ArthasResultTimeout 是否超时
	ArthasResultTimeout = "timeout"
	// ArthasResultErrorCode 错误码
	ArthasResultErrorCode = "errorCode"
	// ArthasResultErrorMessage 错误信息
	ArthasResultErrorMessage = "errorMessage"
	// ArthasResultPayload 结构化结果主体
	ArthasResultPayload = "payload"
	// ArthasResultRawJSON Arthas 原始结构化 JSON
	ArthasResultRawJSON = "rawJson"
	// ArthasResultMeta 执行元信息
	ArthasResultMeta = "meta"
	// ArthasResultDelta 增量结果（用于 session_pull）
	ArthasResultDelta = "delta"
	// ArthasResultClosed 是否已关闭
	ArthasResultClosed = "closed"
	// ArthasResultInterrupted 是否已中断
	ArthasResultInterrupted = "interrupted"
	// ArthasResultArthasState Arthas 运行状态
	ArthasResultArthasState = "arthas_state"
	// ArthasResultTunnelReady tunnel 是否就绪
	ArthasResultTunnelReady = "tunnel_ready"
)

// ========== Arthas 协议级错误码 ==========
// 对应 Agent 侧 ArthasTaskProtocol.ErrorCode

const (
	// --- 参数类错误 ---

	// ArthasErrInvalidParameters 参数错误
	ArthasErrInvalidParameters = "INVALID_PARAMETERS"

	// --- 初始化类错误 ---

	// ArthasErrNotConfigured Arthas 未配置
	ArthasErrNotConfigured = "ARTHAS_NOT_CONFIGURED"
	// ArthasErrNotRunning Arthas 未运行
	ArthasErrNotRunning = "ARTHAS_NOT_RUNNING"
	// ArthasErrNotReady Arthas 未就绪
	ArthasErrNotReady = "ARTHAS_NOT_READY"
	// ArthasErrTunnelNotReady tunnel 未注册
	ArthasErrTunnelNotReady = "TUNNEL_NOT_READY"
	// ArthasErrClassLoaderUnavailable Arthas ClassLoader 不存在
	ArthasErrClassLoaderUnavailable = "ARTHAS_CLASSLOADER_UNAVAILABLE"
	// ArthasErrBootstrapUnavailable Bootstrap 实例不存在
	ArthasErrBootstrapUnavailable = "ARTHAS_BOOTSTRAP_UNAVAILABLE"
	// ArthasErrSessionManagerUnavailable SessionManager 获取失败
	ArthasErrSessionManagerUnavailable = "SESSION_MANAGER_UNAVAILABLE"
	// ArthasErrCommandExecutorInitFailed CommandExecutorImpl 初始化失败
	ArthasErrCommandExecutorInitFailed = "COMMAND_EXECUTOR_INIT_FAILED"

	// --- 执行类错误 ---

	// ArthasErrCommandExecutionFailed 命令执行失败
	ArthasErrCommandExecutionFailed = "COMMAND_EXECUTION_FAILED"
	// ArthasErrCommandTimeout 命令执行超时
	ArthasErrCommandTimeout = "COMMAND_TIMEOUT"
	// ArthasErrSessionNotFound session 不存在
	ArthasErrSessionNotFound = "SESSION_NOT_FOUND"
	// ArthasErrSessionAlreadyClosed session 已关闭
	ArthasErrSessionAlreadyClosed = "SESSION_ALREADY_CLOSED"
	// ArthasErrSessionNotIdle session 当前不可执行新命令
	ArthasErrSessionNotIdle = "SESSION_NOT_IDLE"
	// ArthasErrAsyncJobInterrupted 异步任务已中断
	ArthasErrAsyncJobInterrupted = "ASYNC_JOB_INTERRUPTED"
	// ArthasErrPullResultFailed 拉取结果失败
	ArthasErrPullResultFailed = "PULL_RESULT_FAILED"
	// ArthasErrResultTooLarge 结果超过大小限制
	ArthasErrResultTooLarge = "RESULT_TOO_LARGE"

	// --- 状态类错误 ---

	// ArthasErrSessionExpired session 已过期
	ArthasErrSessionExpired = "SESSION_EXPIRED"
	// ArthasErrSessionTTLExceeded session 总存活时间超过限制
	ArthasErrSessionTTLExceeded = "SESSION_TTL_EXCEEDED"
	// ArthasErrSessionIdleTimeout session 空闲超时
	ArthasErrSessionIdleTimeout = "SESSION_IDLE_TIMEOUT"

	// --- 序列化类错误 ---

	// ArthasErrJSONSerializationFailed JSON 序列化失败
	ArthasErrJSONSerializationFailed = "RESULT_JSON_SERIALIZATION_FAILED"
	// ArthasErrJSONParseFailed JSON 解析失败
	ArthasErrJSONParseFailed = "RESULT_JSON_PARSE_FAILED"

	// --- Attach/Detach 特有错误 ---

	// ArthasErrNoScheduler 无可用调度器
	ArthasErrNoScheduler = "NO_SCHEDULER"
	// ArthasErrStartFailed Arthas 启动失败
	ArthasErrStartFailed = "ARTHAS_START_FAILED"
	// ArthasErrAttachError Arthas attach 异常
	ArthasErrAttachError = "ARTHAS_ATTACH_ERROR"
	// ArthasErrAttachStateInvalid Arthas attach 状态异常
	ArthasErrAttachStateInvalid = "ARTHAS_ATTACH_STATE_INVALID"
	// ArthasErrStopped Arthas 已停止
	ArthasErrStopped = "ARTHAS_STOPPED"
	// ArthasErrDetachError Arthas detach 异常
	ArthasErrDetachError = "ARTHAS_DETACH_ERROR"
	// ArthasErrStopRequestFailed 停止请求失败
	ArthasErrStopRequestFailed = "STOP_REQUEST_FAILED"
	// ArthasErrStopCancelled 停止被取消
	ArthasErrStopCancelled = "STOP_CANCELLED"
	// ArthasErrInterrupted 操作被中断
	ArthasErrInterrupted = "INTERRUPTED"
)

// ========== 超时语义说明 ==========
//
// 协议中区分以下超时：
//
// 1. attach 超时：Arthas 启动或 tunnel 注册未在预期时间内完成
//    - 推荐错误码：ArthasErrStartFailed / ArthasErrTunnelNotReady
//
// 2. command 超时：executeSync 未在 timeout_ms 内完成
//    - 推荐错误码：ArthasErrCommandTimeout
//
// 3. pull 等待超时：在 wait_timeout_ms 内没有新数据
//    - 不视为失败，应返回 success=true, items=[], hasMore=false, endOfStream=false
//
// 4. session TTL 超时：从 session_open 起累计生命周期超限
//    - 推荐错误码：ArthasErrSessionTTLExceeded
//
// 5. idle timeout：长时间未 pull / 未 exec / 未访问
//    - 推荐错误码：ArthasErrSessionIdleTimeout

// ========== 重试语义说明 ==========
//
// Collector 侧重试建议：
//
// | task_type              | 是否建议自动重试 | 说明                           |
// |------------------------|-----------------|-------------------------------|
// | arthas_attach          | 是              | 启动失败可有限重试（最多 2 次）   |
// | arthas_detach          | 是              | 幂等性较强（最多 2 次）          |
// | arthas_exec_sync       | 否              | 可能重复执行命令                 |
// | arthas_session_open    | 是              | 可通过幂等键避免重复创建（最多 2 次）|
// | arthas_session_exec    | 否              | 避免重复提交异步作业              |
// | arthas_session_pull    | 是              | 读取型操作，适合重试（最多 3 次）  |
// | arthas_session_interrupt| 是             | 幂等性较强（最多 2 次）          |
// | arthas_session_close   | 是              | 幂等性较强（最多 2 次）          |

// ========== TaskStatus 与 result_json 配合语义 ==========
//
// 对同步任务（arthas_exec_sync）：
//   - RUNNING：表示任务开始执行
//   - SUCCESS/FAILED/TIMEOUT/CANCELLED：携带最终 result_json
//
// 对异步会话任务（arthas_session_exec）：
//   - RUNNING：表示已受理并发起执行
//   - SUCCESS：表示"受理成功"，不表示结果流结束
//
// 对 arthas_session_pull：
//   - 每次 pull 都是一个独立任务
//   - 任务本身返回 SUCCESS
//   - delta.endOfStream=true 才表示当前异步命令真正结束
//
// 即：任务成功 != 异步命令已结束；pull 返回 EOS 才是命令完成信号

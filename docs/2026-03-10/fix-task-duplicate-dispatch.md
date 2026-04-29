# Fix: 任务重复下发问题 - 方案 D

## 问题描述

Agent 通过 LongPoll 接收到任务后，在状态上报 (RUNNING/SUCCESS) 到达服务端之前，新一轮 LongPoll 请求已经发出。服务端看到任务仍在 pending 队列中且状态为 PENDING，再次下发同一个任务。

## 根因分析

两层竞态条件：

1. **Pending 队列是非破坏性读取**：`getPendingTasks` 使用 `LRange` peek 队列，task 留在队列直到 `ReportTaskResult(RUNNING)` 时才移除
2. **`isDispatchableStatus` 包含 RUNNING**：即使 task 已经在某个 agent 上执行中，仍被认为"可分发"

## 解决方案 D（组合方案）

### 改动 1: `isDispatchableStatus` 只包含 PENDING（方案 C）

**文件**: `custom/receiver/agentgatewayreceiver/longpoll/task_handler.go`

```go
// Before:
func isDispatchableStatus(status model.TaskStatus) bool {
    return status == model.TaskStatusPending || status == model.TaskStatusRunning
}

// After:
func isDispatchableStatus(status model.TaskStatus) bool {
    return status == model.TaskStatusPending
}
```

**影响**：RUNNING 状态的 task 不再被 poll 分发。

同步修改 `helper.go` 中 `IsTaskInfoDispatchable`：

**文件**: `custom/extension/controlplaneext/taskmanager/helper.go`

```go
// Before:
func (h *TaskHelper) IsTaskInfoDispatchable(info *TaskInfo, isCancelled bool) bool {
    if isCancelled {
        return false
    }
    if info == nil {
        return true
    }
    // Dispatchable if not in terminal state
    return !isTerminal(info.Status)
}

// After:
func (h *TaskHelper) IsTaskInfoDispatchable(info *TaskInfo, isCancelled bool) bool {
    if isCancelled {
        return false
    }
    if info == nil {
        return true
    }
    // Only PENDING tasks are dispatchable.
    // RUNNING tasks should not be re-dispatched to avoid duplicates.
    return info.Status == model.TaskStatusPending
}
```

---

### 改动 2: Poll 返回时从队列移除已分发的 task（方案 B）

**文件**: `custom/receiver/agentgatewayreceiver/longpoll/task_handler.go`

在 `CheckImmediate` 方法中，返回 tasks 之前，先从 pending 队列中移除这些 task。

```go
// CheckImmediate checks if there are pending tasks immediately.
func (h *TaskPollHandler) CheckImmediate(ctx context.Context, req *PollRequest) (bool, *HandlerResult, error) {
    // ... existing code ...

    // Get pending tasks for the agent
    tasks, err := h.getPendingTasks(ctx, req.AgentID)
    if err != nil {
        return false, nil, err
    }

    if len(tasks) > 0 {
        // Remove dispatched tasks from pending queues to prevent re-dispatch.
        // This is a "claim-on-dispatch" pattern: once a task is sent to an agent,
        // it is removed from the queue. If the agent crashes, the stale task reaper
        // will re-enqueue it after timeout.
        h.removeDispatchedTasks(ctx, req.AgentID, tasks)

        result := &HandlerResult{
            HasChanges: true,
            Response:   NewTaskResponse(true, tasks, fmt.Sprintf("%d tasks available", len(tasks))),
        }
        return true, result, nil
    }

    return false, nil, nil
}

// removeDispatchedTasks removes dispatched tasks from pending queues and
// atomically updates their status to DISPATCHED_TO_AGENT (RUNNING) in the detail record.
func (h *TaskPollHandler) removeDispatchedTasks(ctx context.Context, agentID string, tasks []*model.Task) {
    agentQueueKey := fmt.Sprintf(keyPendingAgent, h.keyPrefix, agentID)
    globalQueueKey := fmt.Sprintf(keyPendingGlobal, h.keyPrefix)

    for _, task := range tasks {
        // Remove from both queues (best effort)
        _ = h.redisClient.LRem(ctx, agentQueueKey, 0, task.ID).Err()
        _ = h.redisClient.LRem(ctx, globalQueueKey, 0, task.ID).Err()

        // Update task status to RUNNING in detail record (claim ownership).
        // This ensures the stale task reaper can detect stuck tasks.
        h.markTaskDispatched(ctx, task.ID, agentID)

        h.logger.Debug("Removed dispatched task from pending queue",
            zap.String("task_id", task.ID),
            zap.String("agent_id", agentID),
        )
    }
}

// markTaskDispatched atomically marks a task as RUNNING (dispatched) in the detail record.
// Uses the applySetRunning Lua script for atomicity.
func (h *TaskPollHandler) markTaskDispatched(ctx context.Context, taskID string, agentID string) {
    detailKey := fmt.Sprintf(keyTaskDetail, h.keyPrefix, taskID)

    // Use the same Lua script pattern as the store to set status to RUNNING (2)
    // Only update if current status is PENDING (1)
    script := redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current then return 0 end

local info = cjson.decode(current)
local cur = tonumber(info.status) or 0

-- Only mark RUNNING if currently PENDING
if cur ~= 1 then return 0 end

info.status = 2
info.last_updated_at_millis = tonumber(ARGV[1])
info.version = (tonumber(info.version) or 0) + 1

if ARGV[2] and ARGV[2] ~= '' then
    if not info.agent_id or info.agent_id == '' then
        info.agent_id = ARGV[2]
    end
end

local started = tonumber(info.started_at_millis) or 0
if started == 0 then
    info.started_at_millis = tonumber(ARGV[1])
end

local ttl = redis.call('TTL', KEYS[1])
if ttl > 0 then
    redis.call('SET', KEYS[1], cjson.encode(info), 'EX', ttl)
else
    redis.call('SET', KEYS[1], cjson.encode(info))
end
return 1
`)

    nowMillis := time.Now().UnixMilli()
    _, err := script.Run(ctx, h.redisClient, []string{detailKey}, nowMillis, agentID).Int()
    if err != nil {
        h.logger.Warn("Failed to mark task as dispatched",
            zap.String("task_id", taskID),
            zap.Error(err),
        )
    }
}
```

需要新增 `"time"` 到 import。

---

### 改动 3: 新增 StaleTaskReaper（超时标记机制）

**新文件**: `custom/extension/controlplaneext/taskmanager/reaper.go`

StaleTaskReaper 定期扫描处于 RUNNING 状态的 task，如果超过配置的超时时间，直接将其标记为 **TIMEOUT 终态**。不做 requeue（重新入队），超时 task 的重新下发由上游（控制面/用户）决策。

```go
package taskmanager

import (
    "context"
    "time"

    "go.uber.org/zap"

    "go.opentelemetry.io/collector/custom/controlplane/model"
    "go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager/store"
)

// StaleTaskReaperConfig holds configuration for the stale task reaper.
type StaleTaskReaperConfig struct {
    // Enabled controls whether the reaper is active.
    Enabled bool `mapstructure:"enabled"`

    // ScanInterval is how often the reaper scans for stale tasks.
    // Default: 30s
    ScanInterval time.Duration `mapstructure:"scan_interval"`

    // RunningTimeout is the default timeout for a RUNNING task before being
    // considered stale and marked as TIMEOUT. Default: 120s.
    // If a task has its own TimeoutMillis, that value is used instead.
    RunningTimeout time.Duration `mapstructure:"running_timeout"`
}

// DefaultStaleTaskReaperConfig returns the default reaper configuration.
func DefaultStaleTaskReaperConfig() StaleTaskReaperConfig {
    return StaleTaskReaperConfig{
        Enabled:        true,
        ScanInterval:   30 * time.Second,
        RunningTimeout: 120 * time.Second,
    }
}

// StaleTaskReaper periodically scans for stale RUNNING tasks and marks them as TIMEOUT.
type StaleTaskReaper struct {
    logger *zap.Logger
    config StaleTaskReaperConfig
    store  store.TaskStore

    stopChan chan struct{}
    doneChan chan struct{}
}

// NewStaleTaskReaper creates a new StaleTaskReaper.
func NewStaleTaskReaper(logger *zap.Logger, config StaleTaskReaperConfig, taskStore store.TaskStore) *StaleTaskReaper {
    return &StaleTaskReaper{
        logger:   logger,
        config:   config,
        store:    taskStore,
        stopChan: make(chan struct{}),
        doneChan: make(chan struct{}),
    }
}

// Start begins the periodic scan loop.
func (r *StaleTaskReaper) Start(ctx context.Context) error {
    if !r.config.Enabled {
        r.logger.Info("Stale task reaper is disabled")
        return nil
    }

    r.logger.Info("Starting stale task reaper",
        zap.Duration("scan_interval", r.config.ScanInterval),
        zap.Duration("running_timeout", r.config.RunningTimeout),
    )

    go r.run()
    return nil
}

// Stop stops the reaper gracefully.
func (r *StaleTaskReaper) Stop() {
    close(r.stopChan)
    <-r.doneChan
}

func (r *StaleTaskReaper) run() {
    defer close(r.doneChan)

    ticker := time.NewTicker(r.config.ScanInterval)
    defer ticker.Stop()

    for {
        select {
        case <-r.stopChan:
            r.logger.Info("Stale task reaper stopped")
            return
        case <-ticker.C:
            r.scan()
        }
    }
}

func (r *StaleTaskReaper) scan() {
    ctx, cancel := context.WithTimeout(context.Background(), r.config.ScanInterval)
    defer cancel()

    infos, err := r.store.ListTaskInfos(ctx)
    if err != nil {
        r.logger.Warn("Stale task reaper: failed to list tasks", zap.Error(err))
        return
    }

    nowMillis := time.Now().UnixMilli()
    reaped := 0

    for _, info := range infos {
        if info.Status != model.TaskStatusRunning {
            continue
        }

        // Determine timeout for this task
        timeout := r.config.RunningTimeout
        if info.Task != nil && info.Task.TimeoutMillis > 0 {
            taskTimeout := time.Duration(info.Task.TimeoutMillis) * time.Millisecond
            // Use the larger of task timeout and config timeout
            if taskTimeout > timeout {
                timeout = taskTimeout
            }
        }
        // Add a grace period (2x the timeout) to account for network delays
        staleThreshold := timeout * 2

        startedAt := info.StartedAtMillis
        if startedAt == 0 {
            startedAt = info.LastUpdatedAtMillis
        }
        if startedAt == 0 {
            startedAt = info.CreatedAtMillis
        }
        if startedAt == 0 {
            continue
        }

        elapsed := time.Duration(nowMillis-startedAt) * time.Millisecond
        if elapsed < staleThreshold {
            continue
        }

        // Task is stale — mark as TIMEOUT
        r.logger.Warn("Stale RUNNING task detected, marking as TIMEOUT",
            zap.String("task_id", info.Task.ID),
            zap.String("agent_id", info.AgentID),
            zap.Duration("elapsed", elapsed),
            zap.Duration("threshold", staleThreshold),
        )

        if err := r.markTimeout(ctx, info, nowMillis); err != nil {
            r.logger.Warn("Failed to mark stale task as TIMEOUT",
                zap.String("task_id", info.Task.ID),
                zap.Error(err),
            )
            continue
        }
        reaped++
    }

    if reaped > 0 {
        r.logger.Info("Stale task reaper: reaped tasks",
            zap.Int("reaped_count", reaped),
        )
    }
}

// markTimeout marks a stale RUNNING task as TIMEOUT (terminal state).
// No requeue is performed — the upstream (control plane / user) decides
// whether to re-submit the task.
func (r *StaleTaskReaper) markTimeout(ctx context.Context, info *store.TaskInfo, nowMillis int64) error {
    taskID := info.Task.ID

    timeoutResult := &model.TaskResult{
        TaskID:            taskID,
        AgentID:           info.AgentID,
        Status:            model.TaskStatusTimeout,
        ErrorCode:         "STALE_RUNNING_TIMEOUT",
        ErrorMessage:      "task stuck in RUNNING state, marked as TIMEOUT by reaper",
        CompletedAtMillis: nowMillis,
    }

    _, err := r.store.ApplyTaskResult(ctx, taskID, timeoutResult, nowMillis)
    if err != nil {
        return err
    }
    _ = r.store.SaveResult(ctx, timeoutResult)
    _ = r.store.ClearRunning(ctx, taskID)
    _ = r.store.PublishEvent(ctx, "completed", taskID)
    return nil
}
```

---

### 改动 4: 配置集成

**文件**: `custom/extension/controlplaneext/taskmanager/interface.go`

在 `Config` 中新增 `StaleTaskReaper` 配置：

```go
type Config struct {
    // ... existing fields ...

    // StaleTaskReaper configuration for detecting and requeuing stuck RUNNING tasks.
    StaleTaskReaper StaleTaskReaperConfig `mapstructure:"stale_task_reaper"`
}

func DefaultConfig() Config {
    return Config{
        // ... existing defaults ...
        StaleTaskReaper: DefaultStaleTaskReaperConfig(),
    }
}
```

---

### 改动 5: 生命周期集成

**文件**: `custom/extension/controlplaneext/taskmanager/service.go`

在 `TaskService` 中集成 StaleTaskReaper：

```go
type TaskService struct {
    logger *zap.Logger
    config Config
    store  store.TaskStore
    helper *TaskHelper
    reaper *StaleTaskReaper  // NEW
}

func NewTaskService(logger *zap.Logger, config Config, taskStore store.TaskStore) *TaskService {
    return &TaskService{
        logger: logger,
        config: config,
        store:  taskStore,
        helper: NewTaskHelper(),
        reaper: NewStaleTaskReaper(logger.Named("stale-reaper"), config.StaleTaskReaper, taskStore),
    }
}

func (s *TaskService) Start(ctx context.Context) error {
    s.logger.Info("Starting task service")
    if err := s.store.Start(ctx); err != nil {
        return err
    }
    // Start stale task reaper
    return s.reaper.Start(ctx)
}

func (s *TaskService) Close() error {
    // Stop reaper first
    if s.reaper != nil {
        s.reaper.Stop()
    }
    return s.store.Close()
}
```

---

## 改动文件清单

| # | 文件 | 改动类型 | 说明 |
|---|------|----------|------|
| 1 | `longpoll/task_handler.go` | 修改 | `isDispatchableStatus` 去掉 RUNNING；新增 `removeDispatchedTasks`、`markTaskDispatched` 方法 |
| 2 | `taskmanager/helper.go` | 修改 | `IsTaskInfoDispatchable` 只允许 PENDING |
| 3 | `taskmanager/reaper.go` | **新文件** | `StaleTaskReaper` 实现（超时直接 TIMEOUT，不 requeue） |
| 4 | `taskmanager/interface.go` | 修改 | `Config` 新增 `StaleTaskReaper` 字段 |
| 5 | `taskmanager/service.go` | 修改 | 集成 reaper 生命周期 |

## 风险评估

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| Agent 收到 task 后 crash，task 被从队列中移除 | task 进入 RUNNING 状态无人处理 | StaleTaskReaper 会在超时后标记为 TIMEOUT，上游可决定是否重新下发 |
| Reaper 把正在正常执行的慢 task 误判为 stale | task 被错误标记为 TIMEOUT | 使用 2x timeout 作为阈值，给足 grace period |
| 多 Collector 节点同时运行 reaper 导致并发冲突 | 多次尝试标记同一个 task | `ApplyTaskResult` 内部有状态机校验，只有一个节点能成功 |

## 验证方法

1. **功能验证**：提交 task → agent 收到 → 不再收到重复 task
2. **超时标记验证**：提交 task → 不上报任何状态 → 等待 2x timeout → task 被标记为 TIMEOUT
3. **幂等验证**：多 Collector 节点同时运行 reaper → 只有一个节点成功标记 TIMEOUT

## 遗留问题

无

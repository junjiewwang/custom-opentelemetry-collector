# 分布式协作 Purge 设计方案

> **日期**: 2026-06-02  
> **状态**: ✅ 核心实施完成（含 Redis 集成测试、多节点模拟、轮询优化、重试机制）  
> **前置依赖**: Sprint 1 lifecycle 核心（已完成）、Redis 基础设施（`storageext`，已有）  
> **目标**: 多节点协作执行 Purge，解决单节点独占锁的吞吐瓶颈和容错风险

---

## 一、问题分析

### 1.1 当前单节点模式的风险

```
Node A (获得 Redis 锁) → 独自清理 N 个过期索引 → 可能失败
Node B (等待)
Node C (等待)
```

| 风险 | 场景 | 后果 |
|------|------|------|
| **超时失败** | N > 1000，ES 响应慢，耗时超过锁 TTL | 锁过期，另节点抢锁重复执行 |
| **半途崩溃** | Node A OOM/Kill | 只删了一半，无记录，下一轮全重来 |
| **ES 过载** | 单节点串行发大量 DELETE 请求 | ES 集群负载飙升，影响在线查询 |
| **资源浪费** | N-1 个节点完全闲置 | 水平扩展的计算资源未利用 |
| **锁续期复杂** | 长时间持锁需 watchdog 续期 | 增加 Redis 依赖复杂度 |

### 1.2 适用阈值判断

| 索引数量 | 推荐策略 | 理由 |
|----------|----------|------|
| ≤ 50 | 单节点（现有方案） | 延迟低，无协调开销 |
| 50 ~ 500 | 分布式协作 | 吞吐和容错收益显著 |
| > 500 | 分布式 + 限流 | 需要控制 ES 并发压力 |

---

## 二、整体架构

### 2.1 核心思想：Work Stealing + 三阶段流水线

```
Phase 1 (Leader only): 扫描过期索引 → 生成任务清单 → 推入 Redis 任务池
Phase 2 (All Nodes):   每个节点从池中原子抢任务 → 执行删除 → 汇报结果
Phase 3 (Leader only): 校验完成度 → 处理失败/超时 → 审计记录 → 清理
```

### 2.2 架构图

```
┌─────────────────────────────────────────────────────────────────────────┐
│                       Redis (Coordination Layer)                          │
│                                                                         │
│  ┌────────────────┐  ┌──────────────────────────┐  ┌────────────────┐   │
│  │ Leader Lock    │  │ Task Queue (LIST)         │  │ Results (HASH) │   │
│  │ lifecycle:     │  │ lifecycle:tasks:{epoch}   │  │ lifecycle:     │   │
│  │   leader       │  │                          │  │   results:     │   │
│  │ TTL: 30s       │  │ [task1, task2, ...]      │  │   {epoch}      │   │
│  │                │  │                          │  │                │   │
│  └────────────────┘  └──────────────────────────┘  └────────────────┘   │
│                                                                         │
│  ┌──────────────────────────────────────────────────────────────────┐   │
│  │ Metadata (HASH)  lifecycle:meta:{epoch}                          │   │
│  │   total_tasks: 150     status: "planning"|"executing"|"done"     │   │
│  │   created_at: ...      leader_node: "node-a"                     │   │
│  │   deadline: ...        strategy: "distributed"                   │   │
│  └──────────────────────────────────────────────────────────────────┘   │
└───────────────┬────────────────────┬────────────────────┬───────────────┘
                │                    │                    │
                ▼                    ▼                    ▼
          ┌──────────┐        ┌──────────┐         ┌──────────┐
          │  Node A  │        │  Node B  │         │  Node C  │
          │ (Leader) │        │ (Worker) │         │ (Worker) │
          │          │        │          │         │          │
          │ Phase 1  │        │ Phase 2  │         │ Phase 2  │
          │ Phase 2  │        │ (grab &  │         │ (grab &  │
          │ Phase 3  │        │  purge)  │         │  purge)  │
          └──────────┘        └──────────┘         └──────────┘
```

### 2.3 与现有架构的集成点

```
extension/observabilitystorageext/
├── lifecycle/
│   ├── interfaces.go            # 新增 TaskCoordinator 接口
│   ├── scheduler.go             # 改造：注入 Coordinator，策略选择
│   ├── coordinator.go           # NEW: DistributedCoordinator 抽象
│   ├── coordinator_redis.go     # NEW: Redis 实现
│   ├── coordinator_local.go     # NEW: 本地单节点实现（降级/小规模）
│   ├── coordinator_test.go      # NEW: 单元测试（mock Redis）
│   └── ...
└── config.go                    # 扩展 SchedulerConfig
```

**关键设计约束**：
- `LifecyclePurger` 接口不变 — Worker 节点复用现有 `esPurger` 实例
- `LifecycleScheduler` 保持 DIP — 通过新接口 `TaskCoordinator` 注入协调逻辑
- 向下兼容 — `coordinator_local.go` 保留现有行为

---

## 三、接口设计

### 3.1 新增接口：TaskCoordinator

```go
// TaskCoordinator abstracts the distributed task coordination mechanism.
// Two implementations: LocalCoordinator (single-node) and RedisCoordinator (distributed).
//
// Design: Strategy Pattern — Scheduler doesn't know if it's running in single-node
// or distributed mode. It always calls the same interface.
type TaskCoordinator interface {
    // TryBecomeLeader attempts to acquire leader role (non-blocking).
    // Returns true if this node is the leader for the current cycle.
    TryBecomeLeader(ctx context.Context) (bool, error)

    // ReleaseLeader explicitly releases leader role.
    ReleaseLeader(ctx context.Context) error

    // SubmitTasks publishes a batch of purge tasks for distributed execution.
    // Only the leader calls this.
    SubmitTasks(ctx context.Context, epoch int64, tasks []PurgeTask) error

    // ClaimTask atomically claims one task from the pool.
    // Returns nil when pool is empty. All nodes call this.
    ClaimTask(ctx context.Context, epoch int64) (*PurgeTask, error)

    // ReportResult records the outcome of a single task execution.
    ReportResult(ctx context.Context, epoch int64, taskID string, result TaskResult) error

    // GetProgress returns the current epoch's execution progress.
    // Only the leader calls this for verification.
    GetProgress(ctx context.Context, epoch int64) (*PurgeProgress, error)

    // GetActiveEpoch returns the current in-progress epoch, or 0 if none.
    GetActiveEpoch(ctx context.Context) (int64, error)

    // CompleteEpoch marks the epoch as done and schedules cleanup.
    CompleteEpoch(ctx context.Context, epoch int64) error
}
```

### 3.2 任务与结果类型

```go
// PurgeTask represents a single unit of work in the distributed purge.
// Granularity: 1 task = 1 ES index deletion.
type PurgeTask struct {
    ID        string     `json:"id"`        // unique task ID (epoch:signal:indexName)
    Epoch     int64      `json:"epoch"`     // batch epoch (unix millis)
    Signal    SignalType `json:"signal"`    // trace/metric/log
    IndexName string     `json:"indexName"` // exact ES index to delete
    Cutoff    time.Time  `json:"cutoff"`    // retention cutoff time
}

// TaskResult records the outcome of executing a single PurgeTask.
type TaskResult struct {
    Status    TaskStatus `json:"status"`
    NodeID    string     `json:"nodeId"`    // which node executed this
    Error     string     `json:"error,omitempty"`
    StartedAt time.Time  `json:"startedAt"`
    DoneAt    time.Time  `json:"doneAt"`
}

// TaskStatus represents the execution state of a task.
type TaskStatus string

const (
    TaskStatusSuccess  TaskStatus = "success"
    TaskStatusFailed   TaskStatus = "failed"
    TaskStatusSkipped  TaskStatus = "skipped"   // index already gone
    TaskStatusTimeout  TaskStatus = "timeout"
)

// PurgeProgress aggregates the execution state for an epoch.
type PurgeProgress struct {
    Epoch       int64  `json:"epoch"`
    TotalTasks  int    `json:"totalTasks"`
    Completed   int    `json:"completed"`   // success + skipped
    Failed      int    `json:"failed"`
    Remaining   int    `json:"remaining"`   // still in queue
    Status      string `json:"status"`      // "executing" | "done" | "timeout"
}
```

### 3.3 LifecyclePurger 接口扩展

```go
// 在 interfaces.go 中新增（不修改已有方法）：

// IndexLister extends LifecyclePurger with the ability to list expired indices
// without deleting them. Used by the Leader to plan distributed tasks.
type IndexLister interface {
    // ListExpired returns the index names that are expired (before cutoff).
    // Does NOT delete anything — read-only operation.
    ListExpired(ctx context.Context, signal SignalType, before time.Time) ([]string, error)
}

// SingleIndexPurger extends LifecyclePurger with single-index deletion capability.
// Used by Workers to execute one task at a time.
type SingleIndexPurger interface {
    // DeleteIndex deletes a single index by exact name. Idempotent.
    DeleteIndex(ctx context.Context, indexName string) error
}
```

---

## 四、Redis 数据结构设计

### 4.1 Key 命名规范

```
lifecycle:leader                      — Leader 锁 (STRING, TTL 30s)
lifecycle:active_epoch                — 当前活跃 epoch (STRING)
lifecycle:meta:{epoch}                — 元数据 (HASH, TTL 2h)
lifecycle:tasks:{epoch}               — 任务队列 (LIST, TTL 2h)
lifecycle:results:{epoch}             — 结果集 (HASH, TTL 24h)
lifecycle:retry:{epoch}               — 重试队列 (LIST, TTL 2h)
```

### 4.2 操作时序

```
Leader:                                Workers:
  │                                      │
  │ SET lifecycle:leader {nodeID} EX 30 NX
  │ → success (I am leader)              │
  │                                      │
  │ -- Phase 1: Plan --                  │
  │ ListExpired(trace) → [idx1..idx50]   │
  │ ListExpired(metric) → [idx51..idx80] │
  │ ListExpired(log) → [idx81..idx100]   │
  │                                      │
  │ HSET lifecycle:meta:{epoch}          │
  │   total_tasks=100, status=executing  │
  │                                      │
  │ LPUSH lifecycle:tasks:{epoch}        │
  │   [task1_json, task2_json, ...]      │
  │                                      │
  │ SET lifecycle:active_epoch {epoch}   │
  │                                      │
  │ -- Phase 2: Execute (all nodes) --   │
  │                                      │
  │ RPOP lifecycle:tasks:{epoch}         │ RPOP lifecycle:tasks:{epoch}
  │ → task1                              │ → task2
  │ DeleteIndex(task1.IndexName)         │ DeleteIndex(task2.IndexName)
  │ HSET lifecycle:results:{epoch}       │ HSET lifecycle:results:{epoch}
  │   task1.ID → {success, node-a}       │   task2.ID → {success, node-b}
  │                                      │
  │ RPOP → task3 ... repeat              │ RPOP → task4 ... repeat
  │ RPOP → nil (pool empty)             │ RPOP → nil (pool empty)
  │                                      │
  │ -- Phase 3: Verify (leader only) --  │
  │ HLEN lifecycle:results:{epoch}       │
  │ → 100 (all reported)                 │
  │                                      │
  │ HGETALL lifecycle:results:{epoch}    │
  │ → aggregate: 95 success, 3 failed, 2 skipped
  │                                      │
  │ Requeue failed (max 3 retries)       │
  │ Audit log + metrics                  │
  │ DEL lifecycle:active_epoch           │
  │ EXPIRE lifecycle:meta:{epoch} 86400  │
  │ EXPIRE lifecycle:results:{epoch} 86400
  │                                      │
  ▼                                      ▼
```

### 4.3 锁续期策略

Leader 锁采用 **短 TTL (30s) + 主动续期** 模式：

```go
// Leader 在 Plan 和 Verify 阶段才需要锁
// Execute 阶段不需要锁（所有节点平等抢任务）
//
// 续期时机：Phase 1 (Plan) 和 Phase 3 (Verify) 中每 10s 续期一次
// 如果续期失败 → 不影响 Phase 2（任务池已经创建，Worker 继续消费）
// Phase 3 由下一个 Leader 接手
```

---

## 五、核心实现

### 5.1 RedisCoordinator

```go
// coordinator_redis.go

package lifecycle

import (
    "context"
    "encoding/json"
    "fmt"
    "time"

    "github.com/redis/go-redis/v9"
    "go.uber.org/zap"
)

// RedisCoordinator implements TaskCoordinator using Redis LIST + HASH.
type RedisCoordinator struct {
    client   redis.UniversalClient
    nodeID   string        // unique node identifier
    logger   *zap.Logger
    leaderTTL time.Duration // default 30s
}

func NewRedisCoordinator(client redis.UniversalClient, nodeID string, logger *zap.Logger) *RedisCoordinator {
    return &RedisCoordinator{
        client:    client,
        nodeID:    nodeID,
        logger:    logger.Named("purge-coordinator"),
        leaderTTL: 30 * time.Second,
    }
}

const (
    keyLeader      = "lifecycle:leader"
    keyActiveEpoch = "lifecycle:active_epoch"
    keyMetaPrefix  = "lifecycle:meta:"
    keyTaskPrefix  = "lifecycle:tasks:"
    keyResultPrefix = "lifecycle:results:"
)

func (c *RedisCoordinator) TryBecomeLeader(ctx context.Context) (bool, error) {
    ok, err := c.client.SetNX(ctx, keyLeader, c.nodeID, c.leaderTTL).Result()
    if err != nil {
        return false, fmt.Errorf("leader election failed: %w", err)
    }
    return ok, nil
}

func (c *RedisCoordinator) ReleaseLeader(ctx context.Context) error {
    // Only release if we own the lock (Lua script for atomicity)
    script := `
        if redis.call("GET", KEYS[1]) == ARGV[1] then
            return redis.call("DEL", KEYS[1])
        end
        return 0
    `
    _, err := c.client.Eval(ctx, script, []string{keyLeader}, c.nodeID).Result()
    return err
}

func (c *RedisCoordinator) SubmitTasks(ctx context.Context, epoch int64, tasks []PurgeTask) error {
    if len(tasks) == 0 {
        return nil
    }

    metaKey := fmt.Sprintf("%s%d", keyMetaPrefix, epoch)
    taskKey := fmt.Sprintf("%s%d", keyTaskPrefix, epoch)

    // Pipeline: set meta + push all tasks atomically
    pipe := c.client.Pipeline()

    // Metadata
    pipe.HSet(ctx, metaKey, map[string]interface{}{
        "total_tasks": len(tasks),
        "status":      "executing",
        "created_at":  time.Now().Unix(),
        "leader_node": c.nodeID,
    })
    pipe.Expire(ctx, metaKey, 2*time.Hour)

    // Push tasks (serialized as JSON)
    taskValues := make([]interface{}, 0, len(tasks))
    for i := range tasks {
        data, _ := json.Marshal(&tasks[i])
        taskValues = append(taskValues, data)
    }
    pipe.LPush(ctx, taskKey, taskValues...)
    pipe.Expire(ctx, taskKey, 2*time.Hour)

    // Set active epoch
    pipe.Set(ctx, keyActiveEpoch, epoch, 2*time.Hour)

    _, err := pipe.Exec(ctx)
    if err != nil {
        return fmt.Errorf("submit tasks failed: %w", err)
    }

    c.logger.Info("Tasks submitted",
        zap.Int64("epoch", epoch),
        zap.Int("count", len(tasks)),
    )
    return nil
}

func (c *RedisCoordinator) ClaimTask(ctx context.Context, epoch int64) (*PurgeTask, error) {
    taskKey := fmt.Sprintf("%s%d", keyTaskPrefix, epoch)

    // RPOP: atomic claim (no two nodes get the same task)
    data, err := c.client.RPop(ctx, taskKey).Bytes()
    if err == redis.Nil {
        return nil, nil // pool empty
    }
    if err != nil {
        return nil, fmt.Errorf("claim task failed: %w", err)
    }

    var task PurgeTask
    if err := json.Unmarshal(data, &task); err != nil {
        return nil, fmt.Errorf("unmarshal task failed: %w", err)
    }
    return &task, nil
}

func (c *RedisCoordinator) ReportResult(ctx context.Context, epoch int64, taskID string, result TaskResult) error {
    resultKey := fmt.Sprintf("%s%d", keyResultPrefix, epoch)
    data, _ := json.Marshal(&result)
    return c.client.HSet(ctx, resultKey, taskID, data).Err()
}

func (c *RedisCoordinator) GetProgress(ctx context.Context, epoch int64) (*PurgeProgress, error) {
    metaKey := fmt.Sprintf("%s%d", keyMetaPrefix, epoch)
    taskKey := fmt.Sprintf("%s%d", keyTaskPrefix, epoch)
    resultKey := fmt.Sprintf("%s%d", keyResultPrefix, epoch)

    pipe := c.client.Pipeline()
    totalCmd := pipe.HGet(ctx, metaKey, "total_tasks")
    remainingCmd := pipe.LLen(ctx, taskKey)
    completedCmd := pipe.HLen(ctx, resultKey)
    _, _ = pipe.Exec(ctx)

    total, _ := totalCmd.Int()
    remaining := int(remainingCmd.Val())
    completed := int(completedCmd.Val())

    // Count failures from results
    failed := 0
    results, _ := c.client.HGetAll(ctx, resultKey).Result()
    for _, v := range results {
        var r TaskResult
        if json.Unmarshal([]byte(v), &r) == nil && r.Status == TaskStatusFailed {
            failed++
        }
    }

    status := "executing"
    if remaining == 0 && completed >= total {
        status = "done"
    }

    return &PurgeProgress{
        Epoch:      epoch,
        TotalTasks: total,
        Completed:  completed - failed,
        Failed:     failed,
        Remaining:  remaining,
        Status:     status,
    }, nil
}

func (c *RedisCoordinator) GetActiveEpoch(ctx context.Context) (int64, error) {
    val, err := c.client.Get(ctx, keyActiveEpoch).Int64()
    if err == redis.Nil {
        return 0, nil
    }
    return val, err
}

func (c *RedisCoordinator) CompleteEpoch(ctx context.Context, epoch int64) error {
    metaKey := fmt.Sprintf("%s%d", keyMetaPrefix, epoch)
    resultKey := fmt.Sprintf("%s%d", keyResultPrefix, epoch)

    pipe := c.client.Pipeline()
    pipe.HSet(ctx, metaKey, "status", "done")
    pipe.Del(ctx, keyActiveEpoch)
    // Keep results for audit (24h)
    pipe.Expire(ctx, resultKey, 24*time.Hour)
    pipe.Expire(ctx, metaKey, 24*time.Hour)
    _, err := pipe.Exec(ctx)
    return err
}
```

### 5.2 LocalCoordinator（降级/单节点）

```go
// coordinator_local.go

package lifecycle

import (
    "context"
    "sync"
)

// LocalCoordinator implements TaskCoordinator for single-node mode.
// All tasks are executed in-process, no Redis required.
// Used when: node_count == 1, Redis unavailable, or task_count <= threshold.
type LocalCoordinator struct {
    mu       sync.Mutex
    tasks    []PurgeTask
    results  map[string]TaskResult
    epoch    int64
}

func NewLocalCoordinator() *LocalCoordinator {
    return &LocalCoordinator{
        results: make(map[string]TaskResult),
    }
}

func (c *LocalCoordinator) TryBecomeLeader(_ context.Context) (bool, error) {
    return true, nil // always leader in local mode
}

func (c *LocalCoordinator) ReleaseLeader(_ context.Context) error { return nil }

func (c *LocalCoordinator) SubmitTasks(_ context.Context, epoch int64, tasks []PurgeTask) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.epoch = epoch
    c.tasks = tasks
    c.results = make(map[string]TaskResult, len(tasks))
    return nil
}

func (c *LocalCoordinator) ClaimTask(_ context.Context, _ int64) (*PurgeTask, error) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if len(c.tasks) == 0 {
        return nil, nil
    }
    task := c.tasks[len(c.tasks)-1]
    c.tasks = c.tasks[:len(c.tasks)-1]
    return &task, nil
}

func (c *LocalCoordinator) ReportResult(_ context.Context, _ int64, taskID string, result TaskResult) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.results[taskID] = result
    return nil
}

func (c *LocalCoordinator) GetProgress(_ context.Context, _ int64) (*PurgeProgress, error) {
    c.mu.Lock()
    defer c.mu.Unlock()
    total := len(c.results) + len(c.tasks)
    return &PurgeProgress{
        Epoch:      c.epoch,
        TotalTasks: total,
        Completed:  len(c.results),
        Remaining:  len(c.tasks),
        Status:     "executing",
    }, nil
}

func (c *LocalCoordinator) GetActiveEpoch(_ context.Context) (int64, error) {
    c.mu.Lock()
    defer c.mu.Unlock()
    if len(c.tasks) > 0 {
        return c.epoch, nil
    }
    return 0, nil
}

func (c *LocalCoordinator) CompleteEpoch(_ context.Context, _ int64) error {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.tasks = nil
    c.results = make(map[string]TaskResult)
    return nil
}
```

### 5.3 Scheduler 改造

```go
// scheduler.go 中 runCycle 的改造逻辑（伪代码）

func (s *LifecycleScheduler) runCycle(ctx context.Context) {
    start := time.Now()

    // Phase 0: Collect usage + alerts (unchanged)
    s.collectUsageSnapshot(ctx)
    s.evaluateAlerts(ctx)

    // Phase 1+2+3: Distributed purge
    if s.coordinator != nil {
        s.distributedPurge(ctx)
    } else {
        // Fallback: original single-node purge
        for _, signal := range AllSignals() {
            s.purgeSignal(ctx, signal)
        }
    }

    s.logger.Debug("Lifecycle cycle completed", zap.Duration("elapsed", time.Since(start)))
}

func (s *LifecycleScheduler) distributedPurge(ctx context.Context) {
    // Check if there's already an active epoch (another leader started it)
    activeEpoch, _ := s.coordinator.GetActiveEpoch(ctx)

    if activeEpoch == 0 {
        // No active batch — try to become leader and plan
        isLeader, err := s.coordinator.TryBecomeLeader(ctx)
        if err != nil {
            s.logger.Warn("Leader election failed, falling back to local", zap.Error(err))
            s.fallbackLocalPurge(ctx)
            return
        }

        if isLeader {
            activeEpoch = s.planTasks(ctx)
            if activeEpoch == 0 {
                s.coordinator.ReleaseLeader(ctx)
                return // nothing to purge
            }
        } else {
            // Not leader and no active epoch — nothing to do this cycle
            return
        }
    }

    // Phase 2: ALL nodes participate in execution
    s.executeTasks(ctx, activeEpoch)

    // Phase 3: Leader verifies (only if we're leader)
    isLeader, _ := s.coordinator.TryBecomeLeader(ctx)
    if isLeader {
        s.verifyAndComplete(ctx, activeEpoch)
        s.coordinator.ReleaseLeader(ctx)
    }
}

func (s *LifecycleScheduler) planTasks(ctx context.Context) int64 {
    epoch := time.Now().UnixMilli()
    var tasks []PurgeTask

    for _, signal := range AllSignals() {
        retention, err := s.resolver.Resolve(ctx, signal, "")
        if err != nil || retention.Duration <= 0 {
            continue
        }
        cutoff := time.Now().Add(-retention.Duration)

        // Check boundary first
        boundary, err := s.purger.GetDataBoundary(ctx, signal)
        if err != nil || boundary.IsEmpty || boundary.OldestAt == nil || !boundary.OldestAt.Before(cutoff) {
            continue
        }

        // Use IndexLister to get the list without deleting
        if lister, ok := s.purger.(IndexLister); ok {
            indices, err := lister.ListExpired(ctx, signal, cutoff)
            if err != nil {
                s.logger.Warn("ListExpired failed", zap.String("signal", string(signal)), zap.Error(err))
                continue
            }
            for _, idx := range indices {
                tasks = append(tasks, PurgeTask{
                    ID:        fmt.Sprintf("%d:%s:%s", epoch, signal, idx),
                    Epoch:     epoch,
                    Signal:    signal,
                    IndexName: idx,
                    Cutoff:    cutoff,
                })
            }
        }
    }

    if len(tasks) == 0 {
        return 0
    }

    // Strategy selection: small batch → local, large batch → distributed
    if len(tasks) <= s.config.DistributedThreshold {
        s.logger.Info("Small batch, using local purge", zap.Int("tasks", len(tasks)))
        s.fallbackLocalPurge(ctx)
        return 0
    }

    if err := s.coordinator.SubmitTasks(ctx, epoch, tasks); err != nil {
        s.logger.Error("Failed to submit tasks", zap.Error(err))
        return 0
    }

    s.audit.Emit(ctx, LifecycleEvent{
        Timestamp: time.Now(),
        Action:    ActionAutoPurge,
        Operator:  "scheduler:leader",
        Input:     map[string]any{"epoch": epoch, "total_tasks": len(tasks), "strategy": "distributed"},
    })

    return epoch
}

func (s *LifecycleScheduler) executeTasks(ctx context.Context, epoch int64) {
    executed := 0
    for {
        task, err := s.coordinator.ClaimTask(ctx, epoch)
        if err != nil {
            s.logger.Warn("Claim task error", zap.Error(err))
            break
        }
        if task == nil {
            break // pool empty
        }

        result := s.executeSingleTask(ctx, task)
        _ = s.coordinator.ReportResult(ctx, epoch, task.ID, result)
        executed++
    }

    if executed > 0 {
        s.logger.Info("Worker batch complete",
            zap.Int64("epoch", epoch),
            zap.Int("executed", executed),
            zap.String("node", s.nodeID),
        )
    }
}

func (s *LifecycleScheduler) executeSingleTask(ctx context.Context, task *PurgeTask) TaskResult {
    start := time.Now()

    // Idempotent: use SingleIndexPurger if available
    if purger, ok := s.purger.(SingleIndexPurger); ok {
        err := purger.DeleteIndex(ctx, task.IndexName)
        if err != nil {
            return TaskResult{
                Status:    TaskStatusFailed,
                NodeID:    s.nodeID,
                Error:     err.Error(),
                StartedAt: start,
                DoneAt:    time.Now(),
            }
        }
        return TaskResult{
            Status:    TaskStatusSuccess,
            NodeID:    s.nodeID,
            StartedAt: start,
            DoneAt:    time.Now(),
        }
    }

    // Fallback: shouldn't happen if wired correctly
    return TaskResult{Status: TaskStatusFailed, NodeID: s.nodeID, Error: "no SingleIndexPurger"}
}

func (s *LifecycleScheduler) verifyAndComplete(ctx context.Context, epoch int64) {
    progress, err := s.coordinator.GetProgress(ctx, epoch)
    if err != nil {
        s.logger.Error("Get progress failed", zap.Error(err))
        return
    }

    // If still tasks remaining, let next cycle handle it
    if progress.Remaining > 0 {
        s.logger.Info("Tasks still pending, will retry next cycle",
            zap.Int("remaining", progress.Remaining),
        )
        return
    }

    // All tasks processed — emit audit
    s.audit.Emit(ctx, LifecycleEvent{
        Timestamp: time.Now(),
        Action:    ActionAutoPurge,
        Operator:  "scheduler:verifier",
        Result: map[string]any{
            "epoch":     epoch,
            "completed": progress.Completed,
            "failed":    progress.Failed,
            "total":     progress.TotalTasks,
        },
    })

    if progress.Failed > 0 {
        s.logger.Warn("Some purge tasks failed",
            zap.Int("failed", progress.Failed),
            zap.Int("total", progress.TotalTasks),
        )
        // TODO: requeue failed tasks (with retry limit)
    }

    _ = s.coordinator.CompleteEpoch(ctx, epoch)
    s.logger.Info("Distributed purge epoch completed",
        zap.Int64("epoch", epoch),
        zap.Int("success", progress.Completed),
        zap.Int("failed", progress.Failed),
    )
}
```

---

## 六、配置扩展

### 6.1 SchedulerConfig 新增字段

```go
type SchedulerConfig struct {
    // ... 现有字段保持不变 ...
    Enabled            bool          `mapstructure:"enabled"`
    Interval           time.Duration `mapstructure:"interval"`
    DryRun             bool          `mapstructure:"dry_run"`
    UsageWarningRatio  float64       `mapstructure:"usage_warning_ratio"`
    UsageCriticalRatio float64       `mapstructure:"usage_critical_ratio"`
    TrendBufferSize    int           `mapstructure:"trend_buffer_size"`

    // === NEW: Distributed Purge Config ===

    // Distributed enables multi-node cooperative purge mode.
    // Requires Redis. Falls back to local mode if Redis unavailable.
    Distributed bool `mapstructure:"distributed"`

    // DistributedThreshold: only use distributed mode when task count exceeds this.
    // Below this threshold, single-node is more efficient (no coordination overhead).
    // Default: 50
    DistributedThreshold int `mapstructure:"distributed_threshold"`

    // WorkerConcurrency: max concurrent delete operations per node.
    // Controls ES pressure. Default: 5
    WorkerConcurrency int `mapstructure:"worker_concurrency"`

    // TaskTimeout: max time a single task can take before considered failed.
    // Default: 30s
    TaskTimeout time.Duration `mapstructure:"task_timeout"`

    // MaxRetries: max retry attempts for a failed task.
    // Default: 3
    MaxRetries int `mapstructure:"max_retries"`

    // RedisName: the named Redis connection to use (from storageext).
    // Default: "default"
    RedisName string `mapstructure:"redis_name"`

    // NodeID: unique identifier for this node. Auto-generated if empty.
    NodeID string `mapstructure:"node_id"`
}
```

### 6.2 配置示例

```yaml
extensions:
  observability_storage:
    type: elasticsearch
    elasticsearch:
      addresses: ["http://elasticsearch:9200"]
      traces:  { index_prefix: otel-traces,  retention: 168h }
      metrics: { index_prefix: otel-metrics, retention: 720h }
      logs:    { index_prefix: otel-logs,    retention: 336h }

    scheduler:
      enabled: true
      interval: 1h
      dry_run: false

      # Distributed purge
      distributed: true
      distributed_threshold: 50    # 超过50个索引时启用分布式
      worker_concurrency: 5        # 每节点最多5个并发删除
      task_timeout: 30s
      max_retries: 3
      redis_name: "default"        # storageext 中的 Redis 连接名
```

---

## 七、ES Purger 扩展

现有 `Purger` 需要实现新的 `IndexLister` 和 `SingleIndexPurger` 接口：

```go
// provider/elasticsearch/purger.go — 新增方法

// Compile-time interface checks
var _ lifecycle.IndexLister = (*Purger)(nil)
var _ lifecycle.SingleIndexPurger = (*Purger)(nil)

// ListExpired returns expired index names without deleting them.
func (p *Purger) ListExpired(ctx context.Context, signal lifecycle.SignalType, before time.Time) ([]string, error) {
    prefix := p.indexPrefix(signal)
    pattern := prefix + "-*"

    indices, err := p.client.ListIndices(ctx, pattern)
    if err != nil {
        return nil, fmt.Errorf("list indices failed: %w", err)
    }

    var expired []string
    for _, idx := range indices {
        indexDate := p.extractDate(idx, prefix)
        if indexDate != nil && indexDate.Before(before) {
            expired = append(expired, idx)
        }
    }
    return expired, nil
}

// DeleteIndex deletes a single index by exact name. Idempotent.
func (p *Purger) DeleteIndex(ctx context.Context, indexName string) error {
    err := p.client.DeleteIndex(ctx, indexName)
    if err != nil {
        return fmt.Errorf("delete index %s failed: %w", indexName, err)
    }
    p.logger.Info("Deleted index", zap.String("index", indexName))
    return nil
}
```

---

## 八、Extension 集成改造

```go
// extension.go — buildLifecycleScheduler 中注入 Coordinator

func (e *ObservabilityStorage) buildLifecycleScheduler() *lifecycle.LifecycleScheduler {
    // ... existing resolver/purger/usage setup ...

    opts := []lifecycle.SchedulerOption{
        lifecycle.WithResolver(resolver),
        lifecycle.WithPurger(purger),
        lifecycle.WithUsageReporter(usageReporter),
        lifecycle.WithAuditEmitter(lifecycle.NewZapAuditEmitter(e.logger)),
        lifecycle.WithConfig(schedulerCfg),
        lifecycle.WithLogger(e.logger),
    }

    // NEW: Inject TaskCoordinator based on config
    if schedulerCfg.Distributed {
        coordinator := e.buildCoordinator(schedulerCfg)
        if coordinator != nil {
            opts = append(opts, lifecycle.WithCoordinator(coordinator))
        }
    }

    return lifecycle.NewScheduler(opts...)
}

func (e *ObservabilityStorage) buildCoordinator(cfg lifecycle.SchedulerConfig) lifecycle.TaskCoordinator {
    // Get Redis from storageext
    redisName := cfg.RedisName
    if redisName == "" {
        redisName = "default"
    }

    storageExt := e.getStorageExtension() // from component.Host
    if storageExt == nil {
        e.logger.Warn("Storage extension not available, falling back to local coordinator")
        return lifecycle.NewLocalCoordinator()
    }

    redisClient, err := storageExt.GetRedis(redisName)
    if err != nil {
        e.logger.Warn("Redis not available, falling back to local coordinator",
            zap.String("redis_name", redisName),
            zap.Error(err),
        )
        return lifecycle.NewLocalCoordinator()
    }

    nodeID := cfg.NodeID
    if nodeID == "" {
        nodeID = generateNodeID() // hostname + pid + random suffix
    }

    return lifecycle.NewRedisCoordinator(redisClient, nodeID, e.logger)
}
```

---

## 九、容错与降级策略

### 9.1 降级链

```
RedisCoordinator (full distributed)
        │ Redis 不可用
        ▼
LocalCoordinator (single-node, in-process)
        │ Purger 不可用
        ▼
NoOp (skip purge, log warning)
```

### 9.2 异常处理矩阵

| 异常场景 | 处理方式 | 数据一致性保证 |
|----------|----------|---------------|
| Leader 在 Plan 后崩溃 | 任务池已在 Redis，其他节点正常消费 | ✅ 安全 |
| Worker 在执行中崩溃 | 该 task 无结果，leader verify 时识别为 unreported → 重新入队 | ✅ 幂等 |
| Redis 不可用 | 降级为 LocalCoordinator | ✅ 功能降级不丢失 |
| ES 拒绝删除（权限/只读） | TaskResult.Failed，不重试 | ✅ 审计记录 |
| 所有 Worker 都报失败 | Leader verify 限流后重试 3 次，仍失败则告警 | ✅ 有限重试 |
| 锁 TTL 内未完成 Plan | 锁自动过期，下一轮新 Leader 重新扫描 | ✅ 无副作用 |
| epoch 超 2h 未完成 | Redis TTL 过期，key 自动清理 | ✅ 下一轮重新执行 |

### 9.3 幂等性保证

```go
// ES DeleteIndex 天然幂等：
// - 索引存在 → 删除成功 (200)
// - 索引不存在 → 返回 nil (client_admin.go:199 已处理 404)
//
// 因此即使同一个索引被重复尝试删除（极端情况），也不会产生错误。
```

---

## 十、可观测性

### 10.1 审计事件扩展

```go
const (
    ActionDistributedPlan   LifecycleAction = "distributed_plan"
    ActionDistributedVerify LifecycleAction = "distributed_verify"
)
```

### 10.2 Metrics（Prometheus）

```
# 每个 epoch 的概览
lifecycle_purge_epoch_total{status="success|failed|timeout"}
lifecycle_purge_tasks_total{node_id, status="success|failed|skipped"}
lifecycle_purge_duration_seconds{phase="plan|execute|verify"}

# 节点级别
lifecycle_purge_tasks_claimed_total{node_id}
lifecycle_purge_tasks_executed_total{node_id, status}
```

### 10.3 日志输出

```
INFO  scheduler:leader  Purge plan created    epoch=1717310400000 total_tasks=150 strategy=distributed
INFO  scheduler:worker  Worker batch complete epoch=1717310400000 executed=52 node=node-a
INFO  scheduler:worker  Worker batch complete epoch=1717310400000 executed=51 node=node-b
INFO  scheduler:worker  Worker batch complete epoch=1717310400000 executed=47 node=node-c
INFO  scheduler:verifier Distributed purge completed epoch=1717310400000 success=148 failed=2
WARN  scheduler:verifier Some purge tasks failed  failed=2 total=150
```

---

## 十一、实施计划

### Sprint 2a: 分布式 Purge 核心（本次实施）

| # | 任务 | 文件 | 说明 |
|---|------|------|------|
| 1 | 定义 `TaskCoordinator` 接口 + 新增类型 | `lifecycle/interfaces.go` + `lifecycle/types.go` | 新增接口 + PurgeTask/TaskResult/PurgeProgress |
| 2 | 实现 `LocalCoordinator` | `lifecycle/coordinator_local.go` | 单节点模式（向下兼容） |
| 3 | 实现 `RedisCoordinator` | `lifecycle/coordinator_redis.go` | Redis LIST + HASH 实现 |
| 4 | 改造 `LifecycleScheduler` | `lifecycle/scheduler.go` | 注入 Coordinator, 三阶段逻辑 |
| 5 | ES Purger 实现 `IndexLister` + `SingleIndexPurger` | `provider/elasticsearch/purger.go` | 新增 ListExpired + DeleteIndex 方法 |
| 6 | 扩展 `SchedulerConfig` | `lifecycle/types.go` + `config.go` | 新增分布式相关字段 |
| 7 | Extension 集成 | `extension.go` | buildCoordinator + Redis 获取 |
| 8 | 单元测试 | `lifecycle/coordinator_test.go` | Mock Redis 测试 RedisCoordinator |
| 9 | 集成测试 | `lifecycle/distributed_purge_test.go` | 多 goroutine 模拟多节点抢任务 |
| 10 | 文档更新 | `docs/` | 更新架构文档 |

**验收标准**:
- 3 个 goroutine 模拟 3 节点，100 个任务分布式执行，全部成功
- Leader 崩溃后，任务池中剩余任务下一轮被新 Leader 发现并继续
- Redis 不可用时自动降级为单节点模式
- 所有现有单元测试继续通过（向下兼容）

### 估时

| 任务 | 预估 |
|------|------|
| 接口定义 + 类型 | 0.5h |
| LocalCoordinator | 0.5h |
| RedisCoordinator | 2h |
| Scheduler 改造 | 2h |
| ES Purger 扩展 | 0.5h |
| Config 扩展 + Extension 集成 | 1h |
| 单元测试 | 2h |
| 集成测试 | 1.5h |
| **总计** | **~10h** |

---

## 十二、风险与缓解

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| Redis 频繁不可用 | 低 | 降级为单节点 | 监控 Redis 可用性 + 告警 |
| 任务粒度太细（100K+ 索引） | 低 | Redis 内存消耗大 | 分批 SubmitTasks（每批 1000） |
| 节点数频繁变化（扩缩容） | 中 | Work Stealing 天然适应 | 无需预分配，新节点上来就抢 |
| 网络分区导致脑裂 | 低 | 可能有多个 "Leader" | RPOP 原子语义保证不重复；verify 最终一致 |
| 删除操作耗时不均（大索引 vs 小索引） | 中 | 某些 Worker 空闲 | Work Stealing 自动平衡 |

---

## 实施进度记录

### ✅ 已完成 (2026-06-02)

| # | 任务 | 文件 | 状态 |
|---|------|------|------|
| 1 | 接口定义 | `lifecycle/interfaces.go` — `TaskCoordinator`, `IndexLister`, `SingleIndexPurger` | ✅ |
| 2 | 类型扩展 | `lifecycle/types.go` — `PurgeTask`, `TaskResult`, `PurgeProgress`, `SchedulerConfig` 分布式字段 | ✅ |
| 3 | LocalCoordinator | `lifecycle/coordinator_local.go` — 单节点内存协调器 | ✅ |
| 4 | RedisCoordinator | `lifecycle/coordinator_redis.go` — Redis 分布式协调器 | ✅ |
| 5 | Scheduler 改造 | `lifecycle/scheduler.go` — `distributedPurge` 三阶段编排 + `WithCoordinator` option | ✅ |
| 6 | ES Purger 扩展 | `provider/elasticsearch/purger.go` — `ListExpired` + `DeleteSingleIndex` | ✅ |
| 7 | Config + Extension 集成 | `config.go` + `extension.go` — `buildCoordinator` 从 storageext 获取 Redis | ✅ |
| 8 | 单元测试 | `coordinator_local_test.go` (7 tests) + `distributed_scheduler_test.go` (10 tests) | ✅ 全部通过 (race) |
| 9 | Redis 集成测试 (miniredis) | `lifecycle/coordinator_redis_test.go` — 9 tests: TryBecomeLeader, ReleaseLeader, CAS释放, Submit/Claim, ReportResult/Progress, CompleteEpoch, GetFailedTasks, EmptySubmit, ClaimFromEmptyPool | ✅ 全部通过 (race) |
| 10 | 多节点模拟测试 | `lifecycle/coordinator_redis_test.go` — 4 tests: ConcurrentClaimNoDuplication (5 nodes, 100 tasks), LeaderElection (10 nodes), FullDistributedPurge (3 schedulers, 80 indices), WorkerJoinsActiveEpoch | ✅ 全部通过 (race) |
| 11 | verifyAndComplete 轮询优化 | `lifecycle/scheduler.go` — 替代 `time.Sleep(5s)` 为 `time.Ticker` + deadline 轮询机制，新增 `VerifyTimeout`/`VerifyPollInterval` 配置项 | ✅ |
| 12 | 失败任务重试入队机制 | `lifecycle/scheduler.go` + `interfaces.go` + `coordinator_local.go` + `coordinator_redis.go` — 新增 `RetryableCoordinator` 接口, 循环重试 (`retryFailedTasks` + `waitForRetryCompletion`), `GetFailedTasks` 实现 | ✅ 全部通过 (race) |
| 13 | 端到端 YAML 配置测试 | `config_e2e_test.go` — 19 tests: YAML 解析 (full/minimal/hybrid), 默认值填充, 配置校验, Extension 启动, 分布式模式注入/降级, 完整 pipeline Start→Stop | ✅ 全部通过 (race) |

### 🔲 待完成

（无 — Sprint 2a 全部完成）

### 遗留问题

1. ~~`verifyAndComplete` 中使用 `time.Sleep(5s)` 等待其他节点完成~~ → ✅ 已改为 `time.Ticker` + `VerifyTimeout` 轮询机制
2. ~~失败任务的重试逻辑（`MaxRetries`）未实现~~ → ✅ 已实现循环重试，通过 `RetryableCoordinator` 接口 + 多轮 retry 直到无可重试任务
3. 大规模场景下需要考虑 `SubmitTasks` 分批提交以避免单次 Redis pipeline 过大
4. 预先存在的 `TestBulkBuffer_ConcurrentAdds` 数据竞争 flaky test（与本次变更无关），待单独修复

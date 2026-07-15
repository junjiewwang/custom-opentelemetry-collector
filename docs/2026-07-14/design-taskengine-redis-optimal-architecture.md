# Task Engine Redis 数据结构最优架构设计

> 文档状态：方案设计（待确认）  
> 创建时间：2026-07-14  
> 关联文档：[analysis-reaper-context-deadline-exceeded.md](./analysis-reaper-context-deadline-exceeded.md)

---

## 一、问题本质

当前设计的核心矛盾：**不同角色对同一数据有不同的读取需求，但共享了同一份巨大的序列化体**。

| 角色 | 实际需要的字段 | 当前读取的数据 |
|------|---------------|---------------|
| Reaper（看门人） | status, createdAt, timeout, claimedBy | 完整 Task JSON（含 KB 级 Payload） |
| Progress（聚合器） | status | 完整 Task JSON |
| Consumer/Claim | 完整 Task（含 Payload） | 完整 Task JSON ✓ |
| Observer/GetTask | 完整 Task | 完整 Task JSON ✓ |

只有 Consumer 和 Observer 真正需要完整 Task。**Reaper 和 Progress 读取了 90%+ 的无用数据**。

### 设计原则

1. **CQRS**：不同读者的视图应该不同
2. **Redis 最佳实践**：小 Value、按访问模式选择数据结构、避免大 Key 反模式
3. **故障域隔离**：后台维护任务（Reaper）的故障不应影响主业务路径（Submit/Claim/Report）

---

## 二、当前数据结构

```
te:task:{taskID}       — STRING (完整 Task JSON，含 Payload ~2KB)
te:q:{queueID}        — LIST (task IDs)
te:running_tasks      — ZSET (task keys, score=createdAt)
te:group:{groupID}    — SET (task IDs)
te:result:{taskID}    — STRING (TaskResult JSON, TTL 24h)
te:events:{type}      — Pub/Sub channel
```

**核心缺陷**：Reaper/Progress 无法只读索引，必须 MGET 完整 Task → 反序列化 → 仅用几个字段。每轮 200 task × 2KB = 400KB 网络传输 + 200 次 JSON decode。

---

## 三、目标架构：分层数据模型

> **"Store the minimum data in the hottest path; let cold data be pulled on demand."**

### 3.1 Key 布局（Cluster-Ready 最终版）

```
# ─── Layer 1: Task 数据（同 slot by {id} HashTag）───
te:{id}:meta        — HASH    (元数据，~200B)
te:{id}:payload     — STRING  (业务参数，~1.5KB)
te:{id}:result      — STRING  (执行结果，TTL 24h)

# ─── Layer 2: 全局索引（同 slot by {idx} HashTag）───
te:{idx}:running    — ZSET    (member=taskID, score=deadline)
te:{idx}:pending    — ZSET    (member=taskID, score=createdAt)

# ─── Layer 3: 队列和分组（自然分散）───
te:q:{queueID}      — LIST    (task IDs)
te:group:{groupID}  — SET     (task IDs)

# ─── Layer 4: 事件（Pub/Sub，不受 slot 约束）───
te:events:{type}    — Pub/Sub channel
```

**HASH 字段**：

```
te:{id}:meta → {
    status, type, createdAt, timeout, claimedBy,
    groupId, priority, maxRetries, retryCount,
    expiresAt, routeStrategy, routeTarget
}
```

### 3.2 各角色的读取路径

| 角色 | 读取方式 | 对比当前 |
|------|---------|---------|
| Reaper | `ZRANGEBYSCORE te:{idx}:running -inf {now} LIMIT 0 500` | 从 MGET 200×2KB → 1 条命令 ~4KB |
| Progress | `ZCARD te:{idx}:running` + `ZCARD te:{idx}:pending` | 从 SCAN+MGET+decode → O(1) |
| Consumer | `HGETALL te:{id}:meta` + `GET te:{id}:payload`（Pipeline 1 RT） | 无退化 |

### 3.3 ZSET Score = Deadline（关键设计）

```
score = createdAt + timeout（即 deadline 时间点）
Reaper 查询：ZRANGEBYSCORE te:{idx}:running -inf {now}
含义：所有 deadline 已过的 = 超时任务
```

使 Reaper 查询退化为单条命令 O(logN + K)，无需 MGET 和反序列化。

---

## 四、状态变更 Lua 脚本

状态变更拆为两步以兼容 Redis Cluster（meta 和索引在不同 slot）：

**Step 1：Lua 原子更新 meta（同 slot）**

```lua
-- KEYS[1] = te:{id}:meta
-- KEYS[2] = te:{id}:payload
-- ARGV[1] = newStatus, ARGV[2] = claimedBy, ARGV[3] = nowMillis, ARGV[4] = timeout, ARGV[5] = terminalTTL

local current = redis.call('HGET', KEYS[1], 'status')
if not current then return redis.error_reply('NOT_FOUND') end

-- 状态机校验 ...

redis.call('HSET', KEYS[1], 'status', ARGV[1])
if ARGV[2] ~= '' then
    redis.call('HSET', KEYS[1], 'claimedBy', ARGV[2])
end

-- Terminal TTL：进入终态时同时 EXPIRE meta 和 payload
local terminal = {success=true, failed=true, timeout=true, skipped=true, cancelled=true}
if terminal[ARGV[1]] then
    redis.call('EXPIRE', KEYS[1], tonumber(ARGV[5]))
    redis.call('EXPIRE', KEYS[2], tonumber(ARGV[5]))
end

return current  -- 返回旧状态供 Step 2 使用
```

**Step 2：异步维护索引（跨 slot Pipeline）**

```go
func (s *RedisStore) updateIndex(ctx context.Context, taskID string, from, to TaskStatus, nowMillis, timeoutMs int64) {
    pipe := s.client.Pipeline()
    if to == StatusRunning {
        pipe.ZAdd(ctx, s.runningIndexKey(), redis.Z{Score: float64(nowMillis + timeoutMs), Member: taskID})
    }
    if from == StatusRunning && to != StatusRunning {
        pipe.ZRem(ctx, s.runningIndexKey(), taskID)
    }
    if from == StatusPending {
        pipe.ZRem(ctx, s.pendingIndexKey(), taskID)
    }
    if _, err := pipe.Exec(ctx); err != nil {
        s.logger.Warn("failed to update task index", zap.String("taskID", taskID), zap.Error(err))
    }
}
```

**索引最终一致性保证**：索引是查询加速器，不是事实真相（真相在 meta HASH）。短暂不一致由 Reaper Safety-Net 兜底——查到任务后校验 meta.status，不匹配则 ZREM。

---

## 五、Reaper 优化设计

```go
const reaperMaxBatchSize = 500

func (r *StaleTaskReaperEngine) scan() {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    if !r.breaker.Allow() {
        return // 熔断状态，跳过本轮
    }

    staleTaskIDs, err := r.store.GetOverdueRunningTasks(ctx, time.Now().UnixMilli())
    if err != nil {
        r.breaker.RecordFailure()
        return
    }
    r.breaker.RecordSuccess()

    for _, taskID := range staleTaskIDs {
        r.reapTask(ctx, taskID)
    }
}
```

**关键改进**：

| 维度 | 当前 | 目标 |
|------|------|------|
| Redis 命令数 | ZRANGE + N/20 次 MGET | 1 次 ZRANGEBYSCORE |
| 网络传输 | ~400KB/轮 | ~4KB/轮 |
| JSON 反序列化 | 200 次 | 0 次 |
| 超时风险 | 高（30s 内完成 MGET） | 极低（< 1ms） |
| 故障半径 | Redis 慢 → Reaper 卡死 | Circuit Breaker 隔离 |

**LIMIT 背压**：每轮最多返回 500 个超时任务，30s 后再处理下一批，避免 Reaper 自身被撑爆。

**监控告警**：ZCARD > 5000 时告警，触发人工排查根因。

---

## 六、故障域隔离

### Circuit Breaker

```go
type CircuitBreaker struct {
    maxFailures    int           // 连续失败次数阈值
    resetTimeout   time.Duration // 半开状态等待时间
    state          State         // closed / open / half-open
    failures       int
    lastFailureAt  time.Time
}
```

### 指数退避

```go
func (b *backoffState) recordFailure() {
    b.consecutiveFailures++
    delay := time.Duration(1<<min(b.consecutiveFailures, 6)) * time.Second // 2s → 64s max
    b.nextRetryAt = time.Now().Add(delay)
}
```

---

## 七、Store 接口

```go
type Store interface {
    // ... 现有方法不变 ...

    // Reaper 专用轻量路径，O(logN + K)
    GetOverdueRunningTasks(ctx context.Context, nowMillis int64) ([]string, error)

    // 只读取元数据（不含 Payload）
    GetTaskMeta(ctx context.Context, taskID string) (*TaskMeta, error)
    GetTasksMeta(ctx context.Context, taskIDs []string) ([]*TaskMeta, error)
}

type TaskMeta struct {
    ID        string
    Type      TaskType
    Status    TaskStatus
    CreatedAt int64
    Timeout   time.Duration
    ClaimedBy string
    GroupID   string
    Priority  int32
}
```

### SaveTask

```go
func (s *RedisStore) SaveTask(ctx context.Context, task *Task) error {
    pipe := s.client.Pipeline()

    pipe.HSet(ctx, s.metaKey(task.ID), map[string]interface{}{
        "status": string(task.Status), "type": string(task.Type),
        "createdAt": task.CreatedAt, "timeout": int64(task.Timeout),
        "claimedBy": task.ClaimedBy, "groupId": task.GroupID,
        "priority": task.Priority, "maxRetries": task.MaxRetries,
        "retryCount": task.RetryCount, "expiresAt": task.ExpiresAt,
        "routeStrategy": string(task.Routing.Strategy),
        "routeTarget": task.Routing.TargetNodeID,
    })

    if len(task.Payload) > 0 {
        pipe.Set(ctx, s.payloadKey(task.ID), task.Payload, 0)
    }
    if task.Status == StatusPending {
        pipe.ZAdd(ctx, s.pendingIndexKey(), redis.Z{Score: float64(task.CreatedAt), Member: task.ID})
    }
    if task.GroupID != "" {
        pipe.SAdd(ctx, s.groupKey(task.GroupID), task.ID)
    }

    _, err := pipe.Exec(ctx)
    return err
}
```

### GetTask

```go
func (s *RedisStore) GetTask(ctx context.Context, taskID string) (*Task, error) {
    pipe := s.client.Pipeline()
    metaCmd := pipe.HGetAll(ctx, s.metaKey(taskID))
    payloadCmd := pipe.Get(ctx, s.payloadKey(taskID))
    _, _ = pipe.Exec(ctx)

    metaMap := metaCmd.Val()
    if len(metaMap) == 0 {
        return nil, nil
    }

    task := s.metaMapToTask(taskID, metaMap)
    if data, err := payloadCmd.Result(); err == nil {
        task.Payload = json.RawMessage(data)
    }
    return task, nil
}
```

---

## 八、TTL 管理策略

**方案：Lua 原子设 TTL + 后台 Safety-Net 兜底**

### TTL 时序

```
SaveTask           → meta PERSIST, payload PERSIST
Status → running   → 无变化
Status → 终态      → [Lua 原子] meta EXPIRE 14d, payload EXPIRE 14d
14 天后            → Redis 自动淘汰
```

### Safety-Net（6h 一次）

```go
func (s *RedisStore) gcOrphanKeys(ctx context.Context) {
    // SCAN te:*:payload → 若对应 meta 不存在 → 设 TTL 24h
    // SCAN te:*:meta 有 TTL → 若对应 payload 无 TTL → 补设相同 TTL
}
```

正常情况 Lua 已保证一致，Safety-Net 只处理极端异常（如 Lua 执行中 Redis 故障）。

---

## 九、Redis Cluster 兼容性

### 分层 HashTag 策略

| 层 | Key 模式 | HashTag | 设计意图 |
|----|---------|---------|---------|
| Task 数据 | `te:{id}:meta/payload/result` | `{id}` | 同 slot → Lua/Pipeline 零退化 |
| 全局索引 | `te:{idx}:running/pending` | `{idx}` | 固定 slot → Reaper 单 key 操作 |
| 队列/分组 | `te:q:{queueID}`, `te:group:{groupID}` | 无约束 | 自然分散 → 负载均衡 |

### 各操作 Cluster 行为

| 操作 | Slot 数 | RT 数 |
|------|---------|-------|
| SaveTask (meta+payload) | 1 | 1 |
| SaveTask (入队+分组) | 1~2 | +1~2 |
| GetTask | 1 | 1 |
| UpdateStatus (Lua) | 1 | 1 |
| UpdateStatus (索引) | 1 | +1 |
| Reaper scan | 1 | 1 |

### 热点分析

全局索引 `{idx}` 集中在单 slot，但 QPS（~50-100 ops/s）远低于单节点极限（~10万 ops/s），不构成热点。

---

## 十、实施优先级

| 优先级 | 改动 | 风险 |
|--------|------|------|
| **P0** | ZSET score 改为 deadline + ZRANGEBYSCORE | 低 |
| **P0** | Circuit Breaker + 指数退避 | 极低（纯新增） |
| **P1** | HASH 元数据分离 + Payload STRING 拆分 | 中（核心路径） |
| **P1** | 状态变更 Lua 改为 HASH 操作 | 中 |
| **P2** | Progress 基于 ZCARD 的 O(1) 查询 | 低 |
| **P2** | Store 接口新增 GetOverdueRunningTasks/GetTaskMeta | 低 |

---

## 十一、新旧架构对比

| 维度 | 当前 | 目标 |
|------|------|------|
| Task 存储 | 1 × STRING ~2KB | HASH ~200B + STRING ~1.5KB |
| 状态变更 | GET→decode→modify→encode→SET (2KB) | HSET 1-2 字段 |
| Reaper 查询 | ZRANGE + N/20 MGET + N decode | 1× ZRANGEBYSCORE |
| Progress | SCAN + MGET + decode + count | ZCARD × 2 |
| Reaper 网络传输 | ~400KB/轮 | ~4KB/轮 |
| Reaper 延迟 | 数百 ms~30s | < 5ms |
| Redis 故障时 | 连锁雪崩 | Circuit Breaker 隔离 |
| 扩展性 | 改序列化全量影响 | HASH 字段级扩展 |

---

## 十二、实施路径

### 依赖关系

```
Store 接口新增 GetOverdueRunningTasks / GetTaskMeta (P2，已定义)
    │
    ├──→ [P0] Step 1: Reaper 改用新接口 + Circuit Breaker
    │
    ├──→ [P1] Step 2: SaveTask 拆分写入 (HASH + STRING + ZSET)
    │         │
    │         ├──→ [P1] Step 3: UpdateTaskStatus Lua 改为 HASH 操作
    │         │
    │         └──→ [P1] Step 4: GetTask 改为 Pipeline 组装
    │
    └──→ [P2] Step 5: GetProgress 改用 ZCARD
```

### Step 1（P0）：Reaper 改用 GetOverdueRunningTasks + 熔断器

**目标**：根治 "context deadline exceeded"，风险最低、ROI 最高。

**改动范围**：
- `extension/controlplaneext/taskmanager/reaper_engine.go`
- 新增 `taskengine/circuit_breaker.go`

**具体事项**：
1. Reaper 新增 `store Store` 依赖（当前只依赖 `Engine`）
2. `scan()` 方法从 `engine.ListTasks(Status=Running)` 改为 `store.GetOverdueRunningTasks(ctx, nowMillis)`
3. 新增 CircuitBreaker 结构体，包装 Redis 调用
4. 新增指数退避逻辑，消除无信息增益的重复日志
5. MemoryStore 实现 `GetOverdueRunningTasks`（遍历内存 map，按 deadline 过滤）

**验收标准**：
- Reaper 单轮扫描延迟 < 5ms（当前数百 ms~30s）
- Redis 不可达时 Reaper 熔断，不产生日志洪水
- 现有 Engine 测试全部通过

### Step 2（P1）：SaveTask 拆分写入

**目标**：写入路径从单个 STRING 改为 HASH + STRING + ZSET。

**改动范围**：
- `taskengine/store_redis.go`：`SaveTask`、`DeleteTask`
- `taskengine/store_memory.go`：同步改造内存结构

**具体事项**：
1. 新增 key 生成方法：`metaKey(id)`、`payloadKey(id)`、`runningIndexKey()`、`pendingIndexKey()`
2. `SaveTask` 改为 Pipeline 写入 HASH + STRING + ZSET
3. `DeleteTask` 同步清理三层 key + 索引
4. 新增 `metaMapToTask()` 辅助方法：HASH map → Task 结构体

**验收标准**：
- `SaveTask` → `GetTask` 读写一致性测试通过
- Pipeline 在 Cluster 模式下不报 CROSSSLOT（meta 和 payload 同 slot）

### Step 3（P1）：UpdateTaskStatus Lua 改为 HASH 操作

**目标**：状态变更从 GET/SET 2KB JSON 降为 HGET/HSET ~100B。

**改动范围**：
- `taskengine/store_redis.go`：Lua 脚本重写 + 新增 `updateIndex()`

**具体事项**：
1. 新版 Lua 脚本只操作 `te:{id}:meta` + `te:{id}:payload`（同 slot）
2. 索引维护（ZADD/ZREM `te:{idx}:running/pending`）作为 Step 2 异步 Pipeline 执行
3. 索引失败只记日志，Reaper Safety-Net 兜底修复不一致
4. 终态 TTL 在 Lua 内原子设置（meta + payload 同时 EXPIRE）

**验收标准**：
- 状态机转换测试全部通过（合法/非法/幂等）
- 终态后 14 天 TTL 正确设置在 meta 和 payload 上
- 索引与 meta.status 最终一致（Reaper 校验逻辑覆盖）

### Step 4（P1）：GetTask 改为 Pipeline 组装

**目标**：读取路径适配新的分层存储。

**改动范围**：
- `taskengine/store_redis.go`：`GetTask`、`GetTasks`、`ListTasks`

**具体事项**：
1. `GetTask` 改为 Pipeline: `HGETALL meta` + `GET payload`
2. `GetTasks` 改为批量 Pipeline（每批仍 20 个，但每个只需 HGETALL + GET）
3. `ListTasks` 的 Running 快速路径改为从 `te:{idx}:running` ZSET 查询
4. 实现 `GetTaskMeta`：只 `HGETALL meta`，不读 payload
5. 实现 `GetTasksMeta`：批量 Pipeline HGETALL

**验收标准**：
- 所有现有 Engine 集成测试通过
- `GetTaskMeta` 返回的字段与 `GetTask` 一致（除 Payload 外）

### Step 5（P2）：GetProgress 改用 ZCARD

**目标**：Progress 查询从 O(N) SCAN+decode 降为 O(1)。

**改动范围**：
- `taskengine/store_redis.go`：`GetProgress`
- 可能新增终态计数器（或基于 group SET 遍历）

**具体事项**：
1. Running 计数：`ZCARD te:{idx}:running`
2. Pending 计数：`ZCARD te:{idx}:pending`
3. 终态计数方案（二选一）：
   - **方案 A**：维护 `te:counter:{groupID}:{status}` 原子计数器（INCR/DECR）
   - **方案 B**：仍从 `te:group:{groupID}` SMEMBERS → 批量 `HGET status`（规模小时可接受）
4. 按实际 group 规模选择方案（< 1000 task/group 选 B，否则选 A）

**验收标准**：
- `GetProgress` 返回值与当前实现一致
- Running/Pending 计数为 O(1)

### Step 6（贯穿）：MemoryStore 同步实现

每个 Step 中 RedisStore 的改动都需要 MemoryStore 同步适配：
- Step 1：实现 `GetOverdueRunningTasks`（遍历 + deadline 过滤）
- Step 2：内存结构从 `map[string]*Task` 拆分为 meta + payload
- Step 3：状态变更逻辑适配新结构
- Step 4：实现 `GetTaskMeta` / `GetTasksMeta`
- Step 5：Progress 适配

### 实施状态

| Step | 状态 | 备注 |
|------|------|------|
| Step 1 | ✅ 已完成 | 2026-07-15 完成 |
| Step 2 | ✅ 已完成 | SaveTask 拆分 + GetTask 组装 + Bridge 同步 |
| Step 3 | ✅ 已完成 | Lua 改为 HASH 操作，索引异步维护，Bridge 移除 |
| Step 4 | ✅ 合并至 Step 2 | GetTask/GetTasks 已在 Step 2 中一并实现 |
| Step 5 | 待实施 | 独立，可并行 |
| Step 6 | 待实施 | 贯穿各 Step |

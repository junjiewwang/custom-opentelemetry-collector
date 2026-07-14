# Task Engine Redis 数据结构最优架构设计

> 文档状态：方案设计（待确认）  
> 创建时间：2026-07-14  
> 关联问题：Engine stale reaper context deadline exceeded  
> 关联文档：[analysis-reaper-context-deadline-exceeded.md](./analysis-reaper-context-deadline-exceeded.md)

---

## 一、从第一性原理出发

### 1.1 问题本质

当前设计的核心矛盾：**不同角色对同一数据有不同的读取需求，但共享了同一份巨大的序列化体**。

| 角色 | 实际需要的字段 | 当前读取的数据 |
|------|---------------|---------------|
| Reaper（看门人） | status, createdAt, timeout, claimedBy | 完整 Task JSON（含 KB 级 Payload） |
| Progress（聚合器） | status | 完整 Task JSON |
| Consumer/Claim | 完整 Task（含 Payload） | 完整 Task JSON ✓ |
| Observer/GetTask | 完整 Task | 完整 Task JSON ✓ |

只有 Consumer 和 Observer 真正需要完整 Task。**Reaper 和 Progress 读取了 90%+ 的无用数据**。

### 1.2 设计原则

1. **CQRS（Command Query Responsibility Segregation）**：写入路径和查询路径应该有独立优化的数据视图
2. **读写分离的粒度问题**：不是"读写分离"就够了，是"不同读者的视图应该不同"
3. **Redis 最佳实践**：小 Value、按访问模式选择数据结构、避免大 Key 反模式
4. **故障域隔离**：后台维护任务（Reaper）的故障不应该影响主业务路径（Submit/Claim/Report）

---

## 二、当前架构的根本缺陷

### 2.1 数据结构现状

```
te:task:{taskID}       — STRING (完整 Task JSON，含 Payload)
te:q:{queueID}        — LIST (task IDs)
te:running_tasks      — ZSET (task keys, score=createdAt)
te:group:{groupID}    — SET (task IDs)
te:result:{taskID}    — STRING (TaskResult JSON, TTL 24h)
te:events:{type}      — Pub/Sub channel
```

### 2.2 缺陷分析

| # | 缺陷 | 影响 | 根因 |
|---|------|------|------|
| 1 | **Task 存储为 STRING 整体** | Reaper/Progress 必须反序列化整个 JSON 只为读几个字段 | 未按访问模式拆分 |
| 2 | **ZSET 只是索引不是数据** | 查到 running taskID 后还要 MGET 读完整 Task，两次 IO | 索引与数据分离但无法只读索引 |
| 3 | **MGET 批量读完整 Task** | 200 个 task × 2KB = 400KB 网络传输 + 200 次反序列化 | 无轻量查询路径 |
| 4 | **Reaper 与业务共享 Redis 客户端** | Redis 慢时 Reaper 和 Claim/Report 同时受影响 | 无故障域隔离 |
| 5 | **所有状态变更靠 Lua 脚本 GET+SET** | 每次 UpdateTaskStatus 读写整个 Task JSON | 状态未独立存储 |

---

## 三、最优解：分层数据模型 + CQRS 视图

### 3.1 设计哲学

> **"Store the minimum data in the hottest path; let cold data be pulled on demand."**

将 Task 拆分为三个语义明确的存储层：

```
┌─────────────────────────────────────────────────────────┐
│                    Write Path (Submit/Report)            │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  │
│  │ Task Payload │  │ Task Meta    │  │ Status Index │  │
│  │  (STRING)    │  │  (HASH)      │  │  (ZSET)      │  │
│  │  Cold/Large  │  │  Hot/Small   │  │  Hot/Tiny    │  │
│  └──────────────┘  └──────────────┘  └──────────────┘  │
└─────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────┐
│                    Read Paths (by Role)                  │
│                                                         │
│  Reaper:   Status Index (ZSET) only                     │
│  Progress: Status Index (ZSET) count only               │
│  Consumer: Payload (STRING) + Meta (HASH)               │
│  Observer: Payload (STRING) + Meta (HASH)               │
└─────────────────────────────────────────────────────────┘
```

### 3.2 新的 Key 布局

```
# ─── Layer 1: 状态索引（ZSET，Reaper/Progress 专用）───
te:idx:running          — ZSET { taskKey → score=createdAt }
te:idx:pending          — ZSET { taskKey → score=createdAt }

# ─── Layer 2: 任务元数据（HASH，轻量读取）───
te:meta:{taskID}        — HASH {
                            status:      "running"
                            type:        "arthas:attach"
                            createdAt:   "1720950000000"
                            timeout:     "300000000000"  (纳秒)
                            claimedBy:   "node-abc"
                            groupId:     "epoch-001"
                            priority:    "10"
                            maxRetries:  "3"
                            retryCount:  "0"
                            expiresAt:   "0"
                            routeStrategy: "direct"
                            routeTarget:   "agent-xyz"
                          }

# ─── Layer 3: 任务 Payload（STRING，仅 Consumer/Observer 需要）───
te:payload:{taskID}     — STRING (json.RawMessage，业务参数)

# ─── 辅助结构（不变）───
te:q:{queueID}          — LIST (task IDs，队列)
te:group:{groupID}      — SET (task IDs，分组)
te:result:{taskID}      — STRING (TaskResult JSON, TTL 24h)
te:events:{type}        — Pub/Sub channel
```

### 3.3 各角色的数据流（对比）

#### Reaper（看门人）—— O(1) 操作，零反序列化

```
当前：ZRANGE → MGET(200 个完整 Task JSON) → 逐个反序列化 → 检查超时
最优：ZRANGEBYSCORE te:idx:running -inf (now - threshold) → 直接得到超时的 taskIDs
```

**关键洞察**：如果 ZSET 的 score 是 `createdAt`，那么 `ZRANGEBYSCORE running -inf (now - maxTimeout)` 就能直接得到所有超时的任务，**完全不需要 MGET 和反序列化**。

但存在一个细微问题：不同 task 可能有不同的 timeout 值。解决方案：

**方案 A（推荐）：Score = DeadlineAt**

```
ZSET score = createdAt + timeout（即 deadline 时间点）
Reaper 查询：ZRANGEBYSCORE te:idx:running -inf {now}
含义：所有 deadline 已过的 running task = 所有超时任务
```

这使得 Reaper 的查询退化为一条命令，O(logN + K)，K = 超时任务数。

**方案 B：Score = createdAt + 统一阈值过滤**

如果 timeout 差异不大（大部分都用默认值），可以用 `createdAt` 做 score，然后 `ZRANGEBYSCORE -inf (now - defaultTimeout)`，再对个别自定义 timeout 的任务补充检查其 HASH 的 timeout 字段。

#### Progress（聚合器）—— O(1) 操作

```
当前：SCAN 全部 task keys → MGET → 逐个反序列化 → 按 status 计数
最优：ZCARD te:idx:running + ZCARD te:idx:pending + 其他计数器
```

增加计数器（或直接用 ZSET 的 ZCARD）实现 O(1) 进度查询。

#### Consumer/Claim —— 正常路径，按需读取

```
当前：Dequeue → GetTask(完整 JSON)
最优：Dequeue → HGETALL te:meta:{id} + GET te:payload:{id}（Pipeline 一次 RT）
```

Consumer 确实需要完整数据，两次读取但在 Pipeline 中只需一次 RTT。

### 3.4 状态变更设计（Lua 脚本优化）

当前 Lua 脚本对整个 Task JSON 做 GET → decode → modify → SET，非常重。

新设计中，状态变更只操作 HASH 和 ZSET：

```lua
-- UpdateTaskStatus (新版)
-- KEYS[1] = te:meta:{taskID}
-- KEYS[2] = te:idx:running
-- KEYS[3] = te:idx:pending
-- ARGV[1] = newStatus
-- ARGV[2] = claimedBy
-- ARGV[3] = nowMillis (for deadline calculation)
-- ARGV[4] = timeout (nanoseconds, for ZADD score)

local current = redis.call('HGET', KEYS[1], 'status')
if not current then return 0 end  -- not found

-- 状态机校验（同当前逻辑）
-- ...

-- 原子更新 HASH 字段
redis.call('HSET', KEYS[1], 'status', ARGV[1])
if ARGV[2] ~= '' then
    redis.call('HSET', KEYS[1], 'claimedBy', ARGV[2])
end

-- 维护 ZSET 索引
if ARGV[1] == 'running' then
    -- score = nowMillis + timeoutMillis = deadline
    local timeoutMs = tonumber(ARGV[4]) / 1000000  -- ns → ms
    local deadline = tonumber(ARGV[3]) + timeoutMs
    redis.call('ZADD', KEYS[2], deadline, KEYS[1])
    redis.call('ZREM', KEYS[3], KEYS[1])
elseif current == 'running' then
    redis.call('ZREM', KEYS[2], KEYS[1])
end

if current == 'pending' then
    redis.call('ZREM', KEYS[3], KEYS[1])
end

-- Terminal TTL
local terminal = {success=true, failed=true, timeout=true, skipped=true, cancelled=true}
if terminal[ARGV[1]] then
    redis.call('EXPIRE', KEYS[1], 1209600)  -- 14d
end

return 1
```

对比当前方案：
- 当前：GET 2KB JSON → cjson.decode → modify → cjson.encode → SET 2KB
- 新版：HGET 1 个字段 → HSET 1-2 个字段 → ZADD/ZREM

**性能提升：Lua 脚本内 IO 从 2KB+ 降到 ~100 字节，消除 JSON 编解码开销。**

---

## 四、Reaper 最优设计

### 4.1 新版 Reaper 逻辑

```go
func (r *StaleTaskReaperEngine) scan() {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // 一条命令搞定：查所有 deadline 已过的 running tasks
    // ZRANGEBYSCORE te:idx:running -inf {now}
    staleTaskIDs, err := r.store.GetOverdueRunningTasks(ctx, time.Now().UnixMilli())
    if err != nil {
        // Circuit breaker + 指数退避
        r.handleError(err)
        return
    }

    for _, taskID := range staleTaskIDs {
        r.reapTask(ctx, taskID)
    }
}
```

### 4.2 关键改进

| 维度 | 当前 | 最优 |
|------|------|------|
| Redis 命令数 | ZRANGE + N/20 次 MGET | 1 次 ZRANGEBYSCORE |
| 网络传输 | ~400KB（200 task × 2KB） | ~4KB（200 taskID × 20B） |
| JSON 反序列化 | 200 次 | 0 次 |
| CPU 开销 | 高（decode + 逐个比较 timeout） | 极低（score 比较在 Redis 侧完成） |
| 超时风险 | 高（30s 内完成 200 次 MGET） | 极低（1 条命令 < 1ms） |
| 故障半径 | Redis 慢 → Reaper 卡死 → 无日志限频 | 短超时 + Circuit Breaker |

---

## 五、附加优化：故障域隔离

### 5.1 Circuit Breaker（熔断器）

Reaper 是后台守护进程，不应该在 Redis 不可达时无限重试。

```go
type CircuitBreaker struct {
    maxFailures    int           // 连续失败次数阈值
    resetTimeout   time.Duration // 半开状态等待时间
    state          State         // closed / open / half-open
    failures       int
    lastFailureAt  time.Time
}

// Reaper 使用 Circuit Breaker 包装 Redis 调用
func (r *StaleTaskReaperEngine) scan() {
    if !r.breaker.Allow() {
        return // 熔断状态，跳过本轮
    }

    err := r.doScan()
    if err != nil {
        r.breaker.RecordFailure()
        r.logger.Warn("reaper scan failed",
            zap.Error(err),
            zap.String("breaker_state", r.breaker.State()),
        )
    } else {
        r.breaker.RecordSuccess()
    }
}
```

### 5.2 指数退避 + 日志限频

```go
type backoffState struct {
    consecutiveFailures int
    nextRetryAt         time.Time
}

func (b *backoffState) shouldSkip() bool {
    return time.Now().Before(b.nextRetryAt)
}

func (b *backoffState) recordFailure() {
    b.consecutiveFailures++
    delay := time.Duration(1<<min(b.consecutiveFailures, 6)) * time.Second // 2s, 4s, 8s, ... 64s max
    b.nextRetryAt = time.Now().Add(delay)
}
```

当前问题中 12 分钟内产生了 24 条完全相同的 warn 日志，这是典型的"无信息增益噪音"。

---

## 六、Store 接口演进

### 6.1 新增方法（向后兼容）

```go
type Store interface {
    // ... 现有方法不变 ...

    // ─── 高效查询路径（新增）───

    // GetOverdueRunningTasks 返回 deadline 已过的 running task IDs。
    // 实现：ZRANGEBYSCORE te:idx:running -inf {nowMillis}
    // 这是 Reaper 的专用轻量路径，O(logN + K)。
    GetOverdueRunningTasks(ctx context.Context, nowMillis int64) ([]string, error)

    // GetTaskMeta 只读取任务元数据（不含 Payload）。
    // 实现：HGETALL te:meta:{taskID}
    GetTaskMeta(ctx context.Context, taskID string) (*TaskMeta, error)

    // GetTasksMeta 批量读取元数据。
    // 实现：Pipeline HGETALL × N（每个 HGETALL ~100 字节，vs MGET 完整 JSON ~2KB）
    GetTasksMeta(ctx context.Context, taskIDs []string) ([]*TaskMeta, error)
}

// TaskMeta 是 Task 的轻量视图（不含 Payload）。
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

### 6.2 SaveTask 改造

```go
func (s *RedisStore) SaveTask(ctx context.Context, task *Task) error {
    pipe := s.client.Pipeline()

    // Layer 2: 元数据 HASH
    metaKey := s.metaKey(task.ID)
    pipe.HSet(ctx, metaKey, map[string]interface{}{
        "status":        string(task.Status),
        "type":          string(task.Type),
        "createdAt":     task.CreatedAt,
        "timeout":       int64(task.Timeout),
        "claimedBy":     task.ClaimedBy,
        "groupId":       task.GroupID,
        "priority":      task.Priority,
        "maxRetries":    task.MaxRetries,
        "retryCount":    task.RetryCount,
        "expiresAt":     task.ExpiresAt,
        "routeStrategy": string(task.Routing.Strategy),
        "routeTarget":   task.Routing.TargetNodeID,
    })

    // Layer 3: Payload STRING（仅当有 Payload 时）
    if len(task.Payload) > 0 {
        pipe.Set(ctx, s.payloadKey(task.ID), task.Payload, 0)
    }

    // Layer 1: 状态索引
    if task.Status == StatusPending {
        pipe.ZAdd(ctx, s.pendingIndexKey(), redis.Z{
            Score:  float64(task.CreatedAt),
            Member: task.ID,
        })
    }

    // 分组索引
    if task.GroupID != "" {
        pipe.SAdd(ctx, s.groupKey(task.GroupID), task.ID)
    }

    _, err := pipe.Exec(ctx)
    return err
}
```

### 6.3 GetTask 改造（组装完整 Task）

```go
func (s *RedisStore) GetTask(ctx context.Context, taskID string) (*Task, error) {
    pipe := s.client.Pipeline()
    metaCmd := pipe.HGetAll(ctx, s.metaKey(taskID))
    payloadCmd := pipe.Get(ctx, s.payloadKey(taskID))
    _, _ = pipe.Exec(ctx)

    metaMap := metaCmd.Val()
    if len(metaMap) == 0 {
        return nil, nil // not found
    }

    task := s.metaMapToTask(taskID, metaMap)

    // Payload 可能不存在（某些 task 类型没有 payload）
    if data, err := payloadCmd.Result(); err == nil {
        task.Payload = json.RawMessage(data)
    }

    return task, nil
}
```

---

## 七、数据迁移策略

### 7.1 向后兼容的渐进迁移

```
Phase 1: Dual-Write（双写）
  - SaveTask 同时写 STRING(旧) + HASH(新) + STRING:payload(新)
  - 读取仍从旧 STRING 读
  - Reaper 开始从 ZSET 直接读（无需 MGET）

Phase 2: Switch Read（切读）
  - GetTask 改为从 HASH + Payload STRING 读取
  - 旧 STRING 仅作为 fallback
  - 验证数据一致性

Phase 3: Remove Legacy（清理）
  - 停止写入旧 STRING
  - 批量迁移历史数据
  - 删除旧 key pattern
```

### 7.2 Feature Flag 控制

```go
type RedisStoreConfig struct {
    KeyPrefix       string
    ResultTTL       time.Duration
    UseSplitStorage bool   // Phase 1: dual-write; Phase 2: split-read; Phase 3: remove legacy
}
```

---

## 八、完整对比

| 维度 | 当前架构 | 最优架构 |
|------|---------|---------|
| Task 存储 | 1 个 STRING（2KB 整体） | HASH(~200B) + STRING(~1.5KB Payload) |
| 状态变更 | GET 2KB → decode → modify → encode → SET 2KB | HSET 1 字段 + ZADD/ZREM |
| Reaper 查询 | ZRANGE + N/20 MGET + N decode | 1x ZRANGEBYSCORE（0 decode） |
| Progress 查询 | SCAN + MGET + decode + count | ZCARD × 2（O(1)） |
| 网络传输（Reaper） | ~400KB/轮 | ~4KB/轮 |
| 单次 Reaper 延迟 | 数百 ms~30s | < 5ms |
| Redis 慢时影响 | 连锁雪崩 | Circuit Breaker 隔离 |
| Lua 脚本复杂度 | cjson.decode + cjson.encode 整体 | HGET + HSET 字段级 |
| 扩展性 | 加字段需改所有序列化 | HASH 天然支持字段级扩展 |

---

## 九、实施优先级

| 优先级 | 改动 | ROI | 风险 |
|--------|------|-----|------|
| **P0** | Reaper: ZSET score 改为 deadline + ZRANGEBYSCORE | ★★★★★ 根治超时 | 低（只改写入 score 逻辑） |
| **P0** | Reaper: Circuit Breaker + 指数退避 | ★★★★ 防雪崩 | 极低（纯新增） |
| **P1** | 数据层: HASH 元数据分离 | ★★★★ 全局优化 | 中（需双写迁移） |
| **P1** | 状态变更 Lua 脚本: 改为 HASH 操作 | ★★★★ 降低 Redis 负载 | 中（核心路径） |
| **P2** | Progress: 基于 ZCARD 的 O(1) 查询 | ★★★ 避免 SCAN | 低 |
| **P2** | Store 接口: 新增 GetOverdueRunningTasks/GetTaskMeta | ★★★ 接口职责清晰 | 低 |
| **P3** | 完整双写迁移 + 清理旧 STRING | ★★ 减少存储 | 中（需灰度） |

---

## 十、决策摘要

### 最优解的核心思想

**"让数据结构服务于访问模式，而不是让访问模式迁就数据结构。"**

1. **ZSET Score = Deadline**：Reaper 的核心操作从"读取所有 running tasks → 逐个判断超时"退化为"直接查超时的"，复杂度从 O(N) × MGET 降为 O(logN + K) 单命令
2. **HASH 元数据分离**：Task 的"描述性信息"和"业务参数"天然是不同的生命周期和访问频率，应该在存储层体现这种差异
3. **故障域隔离**：后台维护进程（Reaper）绝不能因为 infra 故障导致无限重试和日志洪水，必须有 Circuit Breaker 兜底

### 不推荐的方案

| 方案 | 为什么不推荐 |
|------|-------------|
| 只加 PING 前置检查 | 治标不治本，MGET 本身仍是瓶颈 |
| 只加指数退避 | 减少噪音但不解决设计缺陷 |
| 增大 context timeout | 掩盖问题，大时间窗口 = 更晚发现 |
| 改用 Scan + HSCAN | 仍是 O(N) 全量扫描 |
| 单独部署 Reaper 专用 Redis | 架构复杂度爆炸，是逃避而非解决 |

---

## 十一、深度分析：四大关键设计问题

### 11.1 Payload 的 TTL 管理

#### 问题本质

Task 拆分为 `te:{id}:meta`（HASH）和 `te:{id}:payload`（STRING）后，两者的生命周期必须一致。如果 meta 被清理但 payload 残留（或反之），会产生：
- 孤儿 Payload：浪费内存，永不回收
- 幽灵 Meta：meta 存在但 payload 丢失，Consumer 读到不完整数据

#### 方案对比

| 方案 | 原理 | 优点 | 缺点 |
|------|------|------|------|
| **A. Lua 原子同步 TTL** | 状态变更 Lua 脚本中同时对 meta 和 payload EXPIRE | 100% 一致；无后台清理延迟 | Lua 脚本需传入 payload key；Cluster 模式要求同 slot |
| **B. 后台 GC 清理** | 定期扫描有 TTL 的 meta 或 payload，补对方 TTL | 业务主路径无额外开销 | 有时间窗口不一致；增加后台组件复杂度 |
| **C. meta EXPIRE + payload 惰性删除** | meta 设 TTL，payload 不设；读时发现 meta 不存在则删 payload | 简单，payload 不多余存活太久 | 如果无人读取，payload 永远残留；不可接受 |
| **D. 统一 Lua 设 TTL + 后台 Safety-Net** | 主路径 Lua 保证同步 TTL；后台定期 check 孤儿 key 做兜底 | 最健壮：正常情况零不一致 + 异常兜底 | 略增复杂度 |

#### 推荐方案：D（Lua 主路径 + 后台 Safety-Net）

**核心规则**：所有导致 TTL 变更的操作都必须通过 Lua 脚本原子地同时操作两个 key。

```lua
-- Terminal TTL: 任务进入终态时同时给 meta 和 payload 设置 TTL
-- KEYS[1] = te:{id}:meta
-- KEYS[2] = te:{id}:payload
-- ARGV[n] = TTL seconds (14 天 = 1209600)

if terminal[newStatus] then
    redis.call('EXPIRE', KEYS[1], 1209600)
    -- payload 可能不存在（某些 task 无 payload），EXPIRE 不存在的 key 返回 0，无副作用
    redis.call('EXPIRE', KEYS[2], 1209600)
end
```

**为什么 Lua 足够**：
1. Task 生命周期中只有 **一个时刻** 需要设 TTL：进入终态（success/failed/timeout/cancelled/skipped）
2. 这个操作在状态变更 Lua 脚本内完成，是原子的
3. SaveTask 时两个 key 都不设 TTL（`0` 表示持久），因为活跃任务不应过期

**Safety-Net（兜底）**：防止 Lua 脚本异常中断（如 Redis 故障重启丢失写入）导致的孤儿：

```go
// 后台 GC：每 6 小时跑一次，检查孤儿 key
func (s *RedisStore) gcOrphanKeys(ctx context.Context) {
    // 方式 1：SCAN te:*:payload → 检查对应 meta 是否存在
    //   如果 meta 不存在 → payload 是孤儿 → 设 TTL 24h（而非立即删除，留 debug 时间窗口）
    // 方式 2：SCAN te:*:meta 有 TTL 的 → 检查对应 payload 是否也有 TTL
    //   如果 payload 没有 TTL → 补设相同 TTL
}
```

**关键细节**：
- Safety-Net 不是必须的，正常情况下 Lua 已保证一致。它只是应对极端异常的兜底
- GC 频率不需要高（6h~24h），因为残留的 payload 只是多占内存，不影响功能正确性
- GC 给孤儿 key 设 TTL（如 24h）而非立即删除，留出人工 debug 的窗口

#### 完整 TTL 时序图

```
SaveTask:
  meta → PERSIST (无 TTL)
  payload → PERSIST (无 TTL)

UpdateTaskStatus → running:
  meta → PERSIST (无变化)
  payload → PERSIST (无变化)

UpdateTaskStatus → success/failed/timeout (终态):
  [Lua 原子操作]
  meta → EXPIRE 14d
  payload → EXPIRE 14d

14 天后:
  Redis 自动淘汰 meta 和 payload → 完全清理
```

---

### 11.2 ZSET 容量上限与性能边界

#### 问题场景

如果出现异常情况（如 Consumer 宕机、网络隔离、Reaper 熔断后长时间不清理），running ZSET 可能积累大量 task。需要分析 ZRANGEBYSCORE 的性能极限。

#### Redis ZSET 内部实现

Redis ZSET 底层使用 **skiplist + hashtable** 双结构：
- skiplist：按 score 排序，支持范围查询 O(logN + M)
- hashtable：支持 O(1) 精确查找 member

`ZRANGEBYSCORE key min max`：
- **时间复杂度**：O(log(N) + M)，N = ZSET 总元素数，M = 返回的元素数
- logN 用于定位 score 区间起点（skiplist 查找）
- M 是线性遍历返回的元素

#### 性能数据分析

| ZSET 大小 (N) | logN 定位 | 超时 100 个 (M=100) | 超时 1000 个 (M=1000) | 超时 10000 个 (M=10000) |
|--------------|-----------|--------------------|-----------------------|------------------------|
| 1,000 | ~10 级跳跃 | < 0.1ms | < 0.5ms | < 2ms |
| 10,000 | ~14 级跳跃 | < 0.1ms | < 0.5ms | < 5ms |
| 100,000 | ~17 级跳跃 | < 0.1ms | < 0.5ms | < 10ms |
| 1,000,000 | ~20 级跳跃 | < 0.2ms | < 1ms | ~20ms |

**结论**：即使 ZSET 积累到 100 万级别（极端异常），单次 ZRANGEBYSCORE 仍在毫秒级。真正的瓶颈不是 Redis 命令本身，而是：
1. **返回数据量（M）过大** → 网络传输 + 客户端内存
2. **Reaper 处理能力** → 每轮清理 M 个超时任务的下游操作

#### 防御性设计：LIMIT 兜底

使用 `ZRANGEBYSCORE ... LIMIT 0 {batchSize}` 限制每轮返回的最大数量：

```go
const reaperMaxBatchSize = 500 // 每轮最多处理 500 个超时任务

func (s *RedisStore) GetOverdueRunningTasks(ctx context.Context, nowMillis int64) ([]string, error) {
    // ZRANGEBYSCORE te:{idx}:running -inf {nowMillis} LIMIT 0 500
    return s.client.ZRangeByScore(ctx, s.runningIndexKey(), &redis.ZRangeBy{
        Min:    "-inf",
        Max:    fmt.Sprintf("%d", nowMillis),
        Offset: 0,
        Count:  reaperMaxBatchSize,
    }).Result()
}
```

**为什么 LIMIT 是正确的**：
- Reaper 每 30s 执行一次，每次最多处理 500 个，30s 后再处理下一批
- 避免单轮处理 10000 个超时任务导致 Reaper 自身超时
- 本质是 **背压控制（backpressure）**：下游处理能力有限时，主动限流

#### 异常积累的根因治理

ZSET 积累的根因是"任务超时了但没有被清理"。设计上需要区分两种情况：

| 情况 | 表现 | 治理 |
|------|------|------|
| **正常积累** | Consumer 正常运行但任务耗时长 | 不需要治理，score=deadline 自然保证只返回真正超时的 |
| **异常积累** | Consumer 宕机，大量 running task 超时 | LIMIT 背压 + 告警（ZCARD > 阈值时报警） |
| **Reaper 故障** | 熔断后长时间不清理 | Circuit Breaker 恢复后会逐步 catch up |

#### 监控指标

```go
// 定期上报 running ZSET 的 cardinality，异常时告警
func (r *StaleTaskReaperEngine) reportMetrics() {
    count, _ := r.store.client.ZCard(ctx, r.store.runningIndexKey()).Result()
    r.metrics.RunningTasksGauge.Set(float64(count))
    
    if count > runningTasksAlertThreshold { // e.g., 5000
        r.logger.Error("running tasks ZSET exceeds alert threshold",
            zap.Int64("count", count),
            zap.Int64("threshold", runningTasksAlertThreshold),
        )
    }
}
```

---

### 11.3 Redis Cluster 模式兼容性

#### 问题本质

Redis Cluster 将 16384 个 slot 分配到多个节点。关键约束：
1. **Lua 脚本**：所有 KEYS 必须在同一个 slot，否则报 `CROSSSLOT` 错误
2. **Pipeline**：跨 slot 的命令会被 go-redis ClusterClient 自动分组到不同节点发送，产生 **多次 RT**
3. **MULTI/EXEC 事务**：严格要求所有 key 同 slot

#### go-redis ClusterClient Pipeline 行为

go-redis v9 的 ClusterClient.Pipeline() 实现：
- **自动按 slot 分组**：将 pipeline 中的命令按 key 的 slot 分组，向每个节点发送一个子 pipeline
- **多次 RT**：如果 pipeline 中有 N 个不同 slot 的命令，会产生最多 N 次（实际是 N 个不同节点数次）RT
- **不会报错**：不像 Lua 那样直接拒绝，而是静默退化

#### 当前设计中的跨 slot 场景分析

| 操作 | 涉及的 Key | 是否跨 slot |
|------|-----------|-------------|
| SaveTask | `te:task:{id}`, `te:group:{groupId}` | ❌ 不同前缀 → 大概率不同 slot |
| UpdateTaskStatus (Lua) | `te:task:{id}`, `te:running_tasks` | ❌ 不同前缀 → 必然不同 slot |
| GetTask + Payload (新设计) | `te:{id}:meta`, `te:{id}:payload` | ✅ 取决于 HashTag |
| 状态变更 Lua (新设计) | `te:{id}:meta`, `te:{idx}:running` | ❌ 不同 HashTag |

#### 推荐方案：分层 HashTag 策略

##### 原则：**将 "必须原子操作的 key" 放同一 slot，接受 "跨节点但可并行" 的操作退化为多 RT**

##### Layer 1: Task 自身数据 —— 同 slot

```
te:{id}:meta      — HASH (元数据)
te:{id}:payload   — STRING (业务参数)
te:{id}:result    — STRING (结果)
```

**HashTag = `{id}`**，三者始终在同一 slot。好处：
- `GetTask` 的 Pipeline（HGETALL + GET）→ 单次 RT
- 终态 Lua（EXPIRE meta + EXPIRE payload）→ 单次 RT，无 CROSSSLOT
- `SaveTask` 中 meta + payload 的写入 → 单次 RT

##### Layer 2: 全局索引 —— 固定 slot

```
te:{idx}:running   — ZSET (running tasks 按 deadline 排序)
te:{idx}:pending   — ZSET (pending tasks)
```

**HashTag = `{idx}`**，所有索引在同一 slot。好处：
- Reaper 只操作 `te:{idx}:running`，单 key 单 slot，无跨 slot 问题
- Progress 查询 ZCARD 也是单 key 操作

##### Layer 3: 队列 —— 自然分散

```
te:q:{queueID}     — LIST
te:group:{groupID} — SET
```

队列天然是独立的，无需 HashTag 约束。不同队列分散到不同节点反而是好事（负载均衡）。

#### 状态变更 Lua 的 Cluster 兼容方案

**核心矛盾**：状态变更需要同时操作 `te:{id}:meta`（slot A）和 `te:{idx}:running`（slot B），跨 slot Lua 会报 CROSSSLOT。

**解决方案：拆分为两步操作**

```go
func (s *RedisStore) UpdateTaskStatus(ctx context.Context, taskID string, status TaskStatus, claimedBy string) error {
    // Step 1: Lua 脚本只操作 meta（单 slot，原子状态机校验）
    // KEYS[1] = te:{id}:meta
    // KEYS[2] = te:{id}:payload (同 slot，用于设 TTL)
    prevStatus, err := s.updateMetaScript.Run(ctx, s.client,
        []string{s.metaKey(taskID), s.payloadKey(taskID)},
        string(status), claimedBy, nowMillis, timeoutMs, terminalTTL,
    ).Text()
    if err != nil {
        return err
    }

    // Step 2: 异步/后续维护 ZSET 索引（跨 slot，非原子但可接受）
    // 索引是"最终一致"的辅助结构，短暂不一致不影响正确性
    s.updateIndexAsync(ctx, taskID, TaskStatus(prevStatus), status, nowMillis, timeoutMs)
    return nil
}

func (s *RedisStore) updateIndexAsync(ctx context.Context, taskID string, from, to TaskStatus, nowMillis, timeoutMs int64) {
    pipe := s.client.Pipeline()
    
    if to == StatusRunning {
        deadline := nowMillis + timeoutMs
        pipe.ZAdd(ctx, s.runningIndexKey(), redis.Z{Score: float64(deadline), Member: taskID})
    }
    if from == StatusRunning && to != StatusRunning {
        pipe.ZRem(ctx, s.runningIndexKey(), taskID)
    }
    if from == StatusPending {
        pipe.ZRem(ctx, s.pendingIndexKey(), taskID)
    }
    
    if _, err := pipe.Exec(ctx); err != nil {
        // 索引更新失败只记日志，不影响主流程
        // Reaper 的 Safety-Net 会兜底：发现 meta.status != running 但仍在 ZSET 中的 → ZREM
        s.logger.Warn("failed to update task index", zap.String("taskID", taskID), zap.Error(err))
    }
}
```

**为什么 "索引最终一致" 是安全的**：

| 不一致场景 | 影响 | 兜底机制 |
|-----------|------|---------|
| task 已终态但仍在 running ZSET | Reaper 查到后发现 meta.status 是终态 → ZREM | Reaper 自动修复 |
| task 进入 running 但未加入 ZSET | 不会被 Reaper 超时回收 | 如果真超时了没人回收，Consumer 端 heartbeat 会发现并上报 |
| task 被 ZREM 了但 meta 还是 running | 不影响 Consumer 执行 | Reaper 下一轮扫描会重新发现（如果仍然超时） |

**关键洞察**：ZSET 索引的语义是 "提供高效查询路径"，不是 "持有事实真相"。真相在 meta HASH 中。索引允许短暂不一致，只要最终收敛即可。

#### Slot 热点分析

| Key | 预期 QPS | 是否热点 |
|-----|---------|---------|
| `te:{idx}:running` | Reaper: 1/30s; 状态变更: ~10-50/s | 低风险：写入 50/s + 读取 1/30s 远低于单 slot 上限 |
| `te:{idx}:pending` | 提交: ~10-50/s; 无读取查询 | 低风险 |
| `te:{id}:meta` | 每 task 生命周期 3-5 次操作 | 极低：分散在所有 slot |

**结论**：全局索引 `{idx}` 即使集中在单 slot，其 QPS（~50-100 ops/s）远低于单节点处理极限（~10万+ ops/s），不构成热点。

#### 替代方案：分片索引（如果未来规模爆炸）

如果未来系统扩展到每秒万级任务，全局 ZSET 可能成为瓶颈。此时可采用分片索引：

```
te:{idx0}:running  — slot 固定（taskID hash % 16 == 0 的 task）
te:{idx1}:running  — slot 固定（taskID hash % 16 == 1 的 task）
...
te:{idxF}:running  — slot 固定（taskID hash % 16 == 15 的 task）
```

Reaper 并发扫描 16 个分片，每个分片独立。但以当前规模（< 100 ops/s），完全不需要。

---

### 11.4 完整 Key 布局（Cluster-Ready 最终版）

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

#### 各操作的 Cluster 行为

| 操作 | Keys | Slot 数 | RT 数 | 备注 |
|------|------|---------|-------|------|
| SaveTask | `{id}:meta` + `{id}:payload` | 1 | 1 | Pipeline 同 slot |
| SaveTask (入队) | `te:q:{queueID}` | 1 | +1 | 与 meta 不同 slot |
| SaveTask (分组) | `te:group:{groupID}` | 1 | +1 | 与 meta 不同 slot |
| GetTask | `{id}:meta` + `{id}:payload` | 1 | 1 | Pipeline 同 slot |
| UpdateStatus (Lua) | `{id}:meta` + `{id}:payload` | 1 | 1 | 同 slot Lua |
| UpdateStatus (索引) | `{idx}:running` | 1 | +1 | 异步 Pipeline |
| Reaper scan | `{idx}:running` | 1 | 1 | 单 key 操作 |
| Reaper reap (Lua) | `{id}:meta` + `{id}:payload` | 1 | 1 per task | 同 slot |

**SaveTask 总 RT**：最多 3 次（meta+payload | queue | group），但 Pipeline 会自动合并同节点请求，实际大概率 2 次。相比当前的 2-3 次无退化。

---

## 十二、设计决策总结

### 12.1 TTL 管理决策

> **Lua 脚本原子设置 TTL（主路径） + 后台 GC 兜底（异常恢复）**

- 正常路径：终态 Lua 脚本同时 EXPIRE meta 和 payload，14 天后自动清理
- 异常兜底：每 6h 后台 SCAN 检查孤儿 key，设置 24h TTL
- 不需要旧数据迁移（题目约束：不关注旧数据）

### 12.2 ZSET 容量决策

> **LIMIT 背压 + 告警 + 分片预留**

- ZRANGEBYSCORE 加 LIMIT 500，避免单轮处理过多
- 监控 ZCARD，超过 5000 告警
- 预留分片索引设计，未来需要时可平滑升级

### 12.3 Cluster 兼容决策

> **分层 HashTag + 索引最终一致**

- Task 数据（meta/payload/result）用 `{id}` HashTag，保证同 slot
- 全局索引用 `{idx}` 固定 HashTag，集中管理
- 状态变更：Lua 只操作同 slot 的 meta+payload，索引更新异步执行
- 索引短暂不一致由 Reaper Safety-Net 兜底

### 12.4 不需要旧数据迁移

根据约束，不关注旧 `te:task:{id}` STRING 数据。新系统直接使用新的 key 布局，老数据自然过期或手动清理。

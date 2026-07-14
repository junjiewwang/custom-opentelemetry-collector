# Engine Stale Reaper: Context Deadline Exceeded 根因分析

## 1. 问题描述

**现象**: `StaleTaskReaperEngine.scan()` 每 30 秒执行一次，持续报错：

```
Engine stale reaper: failed to list running tasks
error: "batch get tasks: mget tasks: context deadline exceeded"
```

**频率**: 从 16:39 到 16:51 持续 12 分钟以上，每 30s 一次（与 ScanInterval 吻合），无间断。

**影响**: Reaper 无法检测 stale RUNNING 任务 → 超时任务永远不会被清理 → 消费者/执行者可能被永久阻塞。

---

## 2. 五 Why 分析

### Why 1: 为什么 `ListTasks` 返回 `context deadline exceeded`？

**直接原因**: `scan()` 创建了一个 `context.WithTimeout(ctx, ScanInterval=30s)` 的 context，在 30s 内未能完成 Redis 操作就超时。

```go
// reaper_engine.go:132
ctx, cancel := context.WithTimeout(context.Background(), r.config.ScanInterval)
```

**证据**: 日志中每 30s 精确一条错误。

---

### Why 2: 为什么 MGET 操作无法在 30 秒内完成？

**错误链路**: `ListTasks(Status=Running)` → `listRunningTasks()` → `ZRange(running_tasks ZSET)` → `getTasksChunked()` → `MGET`

三种可能场景（从概率排序）：

| 场景 | 可能性 | 说明 |
|------|--------|------|
| A. Redis 节点不可达/网络中断 | ★★★★★ | 连续 12 分钟 100% 失败，最像网络层面问题 |
| B. Redis 慢查询阻塞 | ★★☆ | 如果只是 MGET 慢，30s 内通常能返回 |
| C. ZSET `running_tasks` 中有大量 key + MGET 数据膨胀 | ★☆☆ | `zrangeMaxTasks=200`，`taskMGetChunkSize=20`，最多 10 次 MGET，每次 20 key |

**结论**: 场景 A 概率最高。但从代码架构角度，即使 Redis 暂时不可达，设计上应有更好的容错机制。

---

### Why 3: 为什么网络中断/Redis 不可达时，Reaper 没有有效的应对机制？

**代码层面缺陷**:

1. **无 backoff**: Redis 不可达时，仍然每 30s 尝试，每次都创建新 context 但都会超时，产生大量无效日志
2. **无健康检查**: 不先做 `PING` 确认连通性就直接执行复合操作（ZRANGE + N*MGET）
3. **MGET 无独立超时**: `getTasksChunked` 内部 10 次串行 MGET，共享同一个 30s context，前几次超时后面的 chunk 也全部超时
4. **无 circuit breaker**: 连续失败不会触发降级（如暂停扫描、延长间隔）

---

### Why 4: 为什么 Redis 数据结构设计会加剧这个问题？

#### 当前数据结构

```
te:task:{taskID}      — STRING (整个 Task JSON，包含 Payload)
te:running_tasks      — ZSET  (score=createdAt, member=te:task:{taskID} 完整key)
te:q:{queueID}        — LIST  (task IDs)
te:group:{groupID}    — SET   (task IDs)
te:result:{taskID}    — STRING (TaskResult JSON, TTL 24h)
te:events:{eventType} — Pub/Sub channel
```

#### 设计缺陷分析

| 问题 | 说明 | 影响 |
|------|------|------|
| **Task JSON 过大** | `Task.Payload` 是 `json.RawMessage`，可能包含 KB 级业务数据（如 Arthas 命令参数）。Reaper 只需 `status`, `createdAt`, `timeout`, `claimedBy`，但 MGET 返回完整 JSON | 网络传输量 × 200 放大 |
| **ZSET member 是完整 key** | `running_tasks` 的 member 存的是 `"te:task:{taskID}"` 而非纯 `taskID`，浪费空间且需要额外字符串裁剪 | 小问题 |
| **查询-获取两阶段耦合** | 先 ZRANGE 拿 ID 列表 → 再 MGET 拿完整 JSON。如果 Reaper 只需要判断超时，第二阶段可以避免 | Reaper 的核心需求（判断超时）被迫承担全量数据读取的成本 |
| **无轻量元数据分离** | Task 的生命周期元数据（status, createdAt, timeout, claimedBy）和业务数据（Payload）存在同一个 STRING 中 | 所有只读状态的查询都必须反序列化完整 Payload |
| **终态任务延迟清理** | 终态任务设置 14 天 TTL (`EXPIRE 1209600`)，在此期间 task key 仍存在于 keyspace | 如果 ZSET 有清理遗漏（如 UpdateTaskStatus 脚本执行失败），stale member 会指向已不存在的 key |

---

### Why 5: 从业务架构角度，为什么 Reaper 的可用性如此关键？

**Reaper 在任务引擎中的角色**:

```
┌─────────────────────────────────────────────────────┐
│                  Task Engine                          │
│                                                       │
│  Producer ──Submit──→ Queue ──Claim──→ Consumer       │
│                          │                 │          │
│                          │            (执行中)         │
│                          │                 │          │
│                          │            Report(结果)     │
│                          │                 ↓          │
│                    ┌─────────────┐   Terminal State    │
│                    │   Reaper    │         │          │
│                    │ (兜底清理)   │←────────┘          │
│                    └─────────────┘  （Consumer 崩溃时  │
│                                      无 Report）       │
└─────────────────────────────────────────────────────┘
```

**Reaper 失效的连锁影响**:

1. **任务泄漏**: Consumer 崩溃后，其 RUNNING 任务永远不会超时
2. **资源浪费**: Queue 中后续任务无法分配给同类 Consumer（如 Direct routing 场景）
3. **业务影响**:
   - Arthas 诊断任务卡住 → 用户等待无响应
   - Lifecycle purge 任务卡住 → 过期索引不清理，磁盘持续增长
4. **ZSET 膨胀**: `running_tasks` ZSET 持续增长（只有 `UpdateTaskStatus` 的 Lua 脚本会 ZREM），可能加剧后续 ZRANGE 性能退化

---

## 3. 最优解方案

### 方案一：Reaper 层面优化（短期，低风险）

**核心思路**: 在不改变数据结构的前提下，让 Reaper 更健壮。

| 改进 | 详情 |
|------|------|
| 1. 添加 Redis PING 前置检查 | scan() 开始前先 PING，失败直接返回，避免浪费 30s |
| 2. 指数退避 | 连续失败时拉长 ScanInterval（30s → 60s → 120s → max 5min），恢复后重置 |
| 3. 独立 context 给每个 MGET chunk | 避免前面的 chunk 耗尽后面的时间预算 |
| 4. 限制日志频率 | 连续相同错误每 5 分钟只打一条 warn（含累计次数），避免日志洪水 |

**优点**: 改动小，不影响现有数据结构  
**缺点**: 不解决根本问题（MGET 读取全量 JSON 的开销）

---

### 方案二：数据结构优化 — 元数据分离（中期，推荐 ★）

**核心思路**: 将 Reaper 需要的轻量元数据存到 ZSET 或 HASH 中，避免 MGET 大 JSON。

#### 新 Redis Key Layout

```
te:task:{taskID}           — STRING (完整 Task JSON，含 Payload)  【不变】
te:task:meta:{taskID}      — HASH   (轻量元数据：status, createdAt, timeout, claimedBy)
te:running_tasks           — ZSET   (score=createdAt, member=taskID)  【member 只存 taskID】
```

#### 关键改动

1. **SaveTask**: 同时写入 `te:task:{id}` 和 `te:task:meta:{id}` (Pipeline)
2. **UpdateTaskStatus Lua**: 同时更新 meta HASH
3. **listRunningTasks（Reaper 专用路径）**: 只读 meta HASH，不 MGET 完整 JSON
4. **Reaper scan**: `ZRANGE running_tasks 0 200` → `HMGET te:task:meta:{id} createdAt timeout claimedBy` → 判断超时

#### Reaper 专用查询（仅 4 字段，无 Payload）

```go
func (s *RedisStore) ListRunningTasksMeta(ctx context.Context, limit int) ([]TaskMeta, error) {
    // 1. ZRANGE te:running_tasks 0 limit → taskIDs
    // 2. Pipeline: for each taskID → HMGET te:task:meta:{id} createdAt timeout claimedBy
    // 3. 返回轻量 TaskMeta（不含 Payload）
}
```

**数据大小对比**:

| 场景 | 当前 MGET | 优化后 HMGET |
|------|-----------|-------------|
| 200 个 running tasks | 200 × ~2KB = 400KB | 200 × ~80B = 16KB |
| 网络 RTT 在 Redis 集群 | 10 次 MGET (每次 20 keys) | 200 次 HMGET (Pipeline 合并) |

**优点**: 
- Reaper 查询耗时从秒级降到毫秒级
- 即使 Redis 网络恢复后，不会因为大量数据传输再次超时
- 向后兼容：完整 Task JSON 仍在 STRING 中

**缺点**: 
- 写入时多一次 HASH 操作（Pipeline 合并，开销极小）
- 需要数据迁移（对已存在的 running tasks 补写 meta）

---

### 方案三：ZSET 内联元数据（轻量替代方案）

**思路**: 利用 ZSET member 直接编码关键信息，避免二次查询。

```
te:running_tasks — ZSET
  score  = createdAt (毫秒时间戳)
  member = "{taskID}|{timeoutMs}|{claimedBy}"
```

Reaper 只需 ZRANGEBYSCORE 按时间过滤 + 解析 member 即可判断超时，**零 MGET**。

**优点**: 极致性能，一次 ZRANGEBYSCORE 完成所有判断  
**缺点**: 
- ZSET member 膨胀
- timeout/claimedBy 变更时需要 ZREM + ZADD（非原子，需 Lua）
- 不够灵活（未来新增判断字段需改 member 格式）

---

### 方案四：引入 Circuit Breaker 模式（架构层面）

```go
type ResilientStore struct {
    inner   Store
    breaker *CircuitBreaker  // 3 次连续失败 → 熔断 60s → 半开尝试
}
```

**与方案二组合使用效果最佳**: 熔断避免无效尝试，元数据分离降低恢复后的查询成本。

---

## 4. 推荐方案：方案二 + 方案一（组合）

### 实施优先级

| 阶段 | 改动 | 收益 | 风险 |
|------|------|------|------|
| Phase 1（立即） | Reaper 添加 PING 检查 + 指数退避 + 日志限频 | 减少无效操作和日志洪水 | 极低 |
| Phase 2（本周） | `te:task:meta:{id}` HASH 分离 + Reaper 专用 ListRunningTasksMeta | 查询耗时从秒级降到 ms 级 | 低（新写路径，旧数据兼容） |
| Phase 3（下周） | ZSET member 只存 taskID + 数据迁移脚本 | 减少 ZSET 内存占用 | 中（需迁移在线数据） |

---

## 5. Redis 数据结构合理性评估

### 当前设计评分

| 维度 | 评分 | 说明 |
|------|------|------|
| 功能完备性 | 8/10 | Queue (LIST) + 状态索引 (ZSET) + Pub/Sub + 分组 (SET) 覆盖所有需求 |
| 查询性能 | 5/10 | Reaper/Observer 被迫读取全量 JSON；ZSET 快速路径好但后续 MGET 是瓶颈 |
| 内存效率 | 6/10 | STRING 存完整 JSON（含大 Payload）且终态 TTL 14 天；ZSET member 过长 |
| 容错性 | 4/10 | 无熔断、无退避、无独立超时；Lua 脚本部分失败无补偿 |
| 一致性 | 7/10 | Lua 原子更新状态+ZSET 是正确的；但 SADD group 失败只 warn 不回滚 |
| 可扩展性 | 6/10 | 单 ZSET `running_tasks` 是全局热点；如果未来多个引擎实例共享 Redis，需前缀隔离 |

### 设计原则对照

| 设计原则 | 是否满足 | 问题 |
|----------|---------|------|
| 读写分离 | ❌ | 读路径（Reaper）和写路径（Claim/Report）操作相同的 STRING key，竞争锁 |
| 最小数据原则 | ❌ | Reaper 只需 4 字段但被迫读取完整 Task（含 Payload） |
| 失败隔离 | ❌ | Redis 不可达时所有组件同时失败，无降级 |
| 操作原子性 | ✅ | Lua 脚本保证状态转换 + ZSET 维护的原子性 |
| 数据过期策略 | ✅ | 终态 14 天 TTL + Result 24h TTL |

---

## 6. 总结

### 根本原因

**Redis 连接不可达（网络层面）** 叠加 **Reaper 无容错设计（代码层面）** 叠加 **数据结构未针对 Reaper 查询场景优化（架构层面）**。

### 核心结论

1. **即时缓解**: 添加 PING 前置检查 + 指数退避，减少无效操作和日志洪水
2. **根本解决**: 元数据分离，让 Reaper 的关键路径只读取 ~80B/task 而非 ~2KB/task
3. **架构提升**: Circuit Breaker + 独立超时，避免一个慢操作拖垮整个 scan 周期

---

## 变更记录

| 日期 | 内容 |
|------|------|
| 2026-07-14 | 初始分析报告 |

# 任务下发与任务查询 Bug 分析报告

## 概述

本文档记录了 `taskengine` 模块中任务下发（Submit/Claim/Report）和任务查询（ListTasks/GetTask/GetProgress）的全面分析结果。所有 bug 均通过代码审查发现，并编写了针对性测试验证。

## Bug 汇总

| # | Bug 名称 | 严重度 | 分类 | 验证状态 |
|---|---------|--------|------|---------|
| 1 | retryTask 重试后 Failed→Running 非法状态转换 | **CRITICAL** | 任务下发 | 测试确认 |
| 2 | 任务优先级参数被 Enqueue 完全忽略 | MEDIUM | 任务下发 | 测试确认 |
| 3 | Submit 中 Enqueue 失败导致任务孤儿化 | MEDIUM | 任务下发 | 测试确认 |
| 4 | GetPendingTasks 忽略 RoutingCapability 策略 | **HIGH** | 任务查询 | 测试确认 |
| 5 | ListTasks cursor 分页在客户端过滤后 hasMore/Total 不准 | **HIGH** | 任务查询 | 分析确认 |
| 6 | MemoryStore offset > total 时不修正 Offset 字段 | MEDIUM | 任务查询 | 测试确认 |
| 7 | GetPendingTasks/GetAllTasks 硬编码 limit 静默截断 | **HIGH** | 任务查询 | 测试确认 |
| 8 | Redis listRunningTasks Total 仅反映最多 200 条 | **HIGH** | 任务查询 | 代码分析 |
| 9 | Redis listTasksSlow 只扫描旧格式 key | MEDIUM | 任务查询 | 代码分析 |

---

## 详细分析

### Bug 1: retryTask 重试逻辑缺陷 (CRITICAL)

**位置**: `taskengine/engine_impl.go:298-337`

**问题**: `retryTask()` 将任务从 Running→Failed 后，创建一个本地拷贝修改为 Pending 并入队，但**从未调用 `SaveTask` 持久化**。消费者 Claim 时尝试 Failed→Running 转换，状态机 `StatusFailed: {}` 不允许任何转换，返回 `InvalidTransitionError`。

**测试文件**: `taskengine/engine_bug_verification_test.go::TestBug_RetryTask_InvalidStateTransition`

**测试结果**:
```
After retry: status=failed, retryCount=0, claimedBy=purger-1
BUG CONFIRMED: Task stuck in Failed state after retry
Second claim result: task=<nil>, err=<nil>
BUG CONFIRMED: Task was re-enqueued to queue but claim returned nil
Final task state: status=failed, retryCount=0, maxRetries=2
```

**影响**: 所有需要重试的任务永久卡在 Failed 状态，任务重试功能完全失效。

---

### Bug 2: 优先级参数被忽略 (MEDIUM)

**位置**:
- `taskengine/store_memory.go:198`: `Enqueue(..., _ int32)`
- `taskengine/store_redis.go:888`: `Enqueue(..., _ int32)`

**验证结果**: 按优先级 1, 5, 10 提交三个任务，Claim 顺序为 [prio-low, prio-med, prio-high]，FIFO 顺序，优先级完全未生效。

---

### Bug 3: Enqueue 失败导致孤儿任务 (MEDIUM)

**位置**: `taskengine/engine_impl.go:84-98`

**问题**: `Submit()` 先 `SaveTask` 再 `Enqueue`。Enqueue 失败时任务已持久化但不在任何队列中，消费者无法 Claim，但 ListTasks 仍能看到。

**验证结果**: 手动保存任务到 Store 不入队，ListTasks 发现该任务但 Claim 返回 nil。

---

### Bug 4: GetPendingTasks 忽略 RoutingCapability (HIGH)

**位置**: `extension/controlplaneext/taskmanager/service_engine.go:214-222`

**问题**: `GetPendingTasks` 的过滤逻辑仅处理 `RoutingDirect` 和 `RoutingBroadcast`，完全忽略 `RoutingCapability`。导致通过 capability 路由的 pending 任务对 agent 不可见，与实际 Claim 能力不一致。

**验证结果**: 直接路由和广播任务可见，capability 任务不可见但 Claim 可以获取。

---

### Bug 5: ListTasks 分页数据不一致 (HIGH)

**位置**: `extension/controlplaneext/taskmanager/service_engine.go:319-325`

**问题**: `service_engine.ListTasks` 在 engine 层获取数据后，再做客户端侧过滤（多状态、appID、serviceName）。但 `nextOffset` 和 `hasMore` 基于 engine 层的原始 Total 和 Offset 计算，不反映客户端过滤后的实际结果。

---

### Bug 6: Offset 字段不修正 (MEDIUM)

**位置**: `taskengine/store_memory.go:172-175,191`

**问题**: 当 `query.Offset > total` 时，返回空切片但 `ListPage.Offset` 仍为请求值而非修正值。导致调用方 `nextOffset = page.Offset + len(page.Tasks)` 计算错误。

**验证结果**: 总共 3 个任务，offset=100，返回 Offset=100, Tasks=0，nextOffset=100 跳过有效数据。

---

### Bug 7: 硬编码 limit 静默截断 (HIGH)

**位置**: `extension/controlplaneext/taskmanager/service_engine.go:205,252`

**问题**: `GetPendingTasks(limit=1000)` 和 `GetAllTasks(limit=10000)` 硬编码 limit 且无分页循环。任务数超过 limit 时静默丢失，无警告。

**验证结果**: 创建 50 个任务，limit=10 只返回 10 个，Total=50 但实际数据被截断。

---

### Bug 8: Redis listRunningTasks Total 不准 (HIGH)

**位置**: `taskengine/store_redis.go:739-791`

**问题**: `ZRANGE` 最多取 `min(limit, 200)` 条目，返回的 `Total` 基于这批截断数据计数，不是全部 running tasks 的真实总数。

---

### Bug 9: Redis listTasksSlow 只扫描旧格式 (MEDIUM)

**位置**: `taskengine/store_redis.go:794-818`

**问题**: 使用 `SCAN te:task:*` 模式扫描旧格式 key，依赖 legacy dual-write。如果 dual-write 被移除或失败，此路径将遗漏数据。

---

## 测试文件

| 文件 | 覆盖 Bug |
|------|---------|
| `taskengine/engine_bug_verification_test.go` | Bug 1, 2, 3 |
| `taskengine/engine_query_bug_test.go` | Bug 4, 6, 7, 以及 Bug 5 分析 |

## 测试覆盖率缺口

现有测试(`engine_test.go`)覆盖了 Submit/Claim/Report/Cancel 的基本 happy path，但以下场景未被覆盖:

- 任务重试 (retryTask)
- 优先级排序
- Enqueue 失败恢复
- 分页 offset 越界
- 多状态查询过滤
- 路由策略一致性 (Claim vs ListTasks)
- 大数据量截断行为

---

---

## 架构修复实施记录 (2026-08-10)

### 修复内容

| 文件 | 修改 | 对应 Bug |
|------|------|---------|
| `taskengine/store_redis.go` | 重写 `ListTasks` — pending/running/无过滤走 ZSET 索引；SCAN 加 1000 key 上限 | #4, #5, #6, #7, #8, #9 |
| `taskengine/store_redis.go` | 新增 `listByPendingIndex` (ZSET O(logN) 快速路径) | #4 |
| `taskengine/store_redis.go` | 新增 `listTasksByActiveIndexes` (pending+running ZSET 合并) | #5 |
| `taskengine/store_redis.go` | `listTasksSlow` → `listTasksScanCapped` (加扫描上限防超时) | #7, #8 |
| `taskengine/engine_impl.go` | `retryTask` 增加 `SaveTask` 持久化 + DeleteTask→SaveTask upsert | #1 |
| `taskengine/engine_impl.go` | `Submit` Enqueue 失败时回滚 DeleteTask | #3 |
| `taskengine/engine_impl.go` | `Submit` 增加去重检查 (GetTask → 拒绝重复 ID) | 回归修复 |
| `taskengine/state_machine.go` | Failed/Timeout → Pending (支持 retry) | #1 |
| `taskengine/store_memory.go` | SaveTask upsert 语义 + offset 修正 | #1, #6 |
| `extension/controlplaneext/taskmanager/service_engine.go` | GetPendingTasks/GetGlobalPendingTasks 包含 RoutingCapability | #4 |
| `extension/controlplaneext/taskmanager/service_engine.go` | ListTasks hasMore/nextOffset 基于客户端过滤计数 | #5 |

### 集成测试结果

| 测试项 | 修复前 | 修复后 |
|--------|--------|--------|
| GET /api/v2/tasks（无过滤） | **超时 (i/o timeout)** | **694ms** |
| GET /api/v2/tasks?status=pending | **超时** | **677ms** |
| GET /api/v2/tasks?status=running | **超时** | **689ms** |
| POST /api/v2/tasks（提交） | **超时** | **430ms** |
| 分页遍历 | **超时** | **正常** |
| 按 ID 查询单个任务 | 部分超时 | **正常** |

### 已知限制

1. **待重建 ZSET 索引**: 历史任务 (~13k) 的 pending/running ZSET 为空，新提交的任务自动入 ZSET 后走快速路径。建议批量重建索引或逐批清理终态任务后重建。
2. **Redis SCAN 上限 1000**: 当两个 ZSET 均为空时会 fallback 到 capped SCAN（最多 1000 个 key），有日志 warn 提示。

### 发布方式

```bash
export DOCKER_CONTEXT=minikube
make docker-build
kubectl rollout restart deploy/custom-otlp-collector
```

*最后更新: 2026-08-10*

---

## 第三轮修复 (2026-08-10): agent_id 索引化查询

### 问题

`agent_id` 过滤仅作为 client-side filter，底层查询走 capped SCAN (maxScanKeys=1000)。当 agent 的任务 key 不在前 1000 个被扫到的 `te:task:*` key 中时，查询返回空。

### 修复

| 文件 | 修改 |
|------|------|
| `taskengine/model.go` | `ListQuery` 新增 `AgentID` 字段 |
| `taskengine/store_redis.go` | 新增 `agentIndexKey()` / `listByAgentIndex()` — per-agent ZSET O(logN) 查询 |
| `taskengine/store_redis.go` | `ListTasks` 最优先路由: AgentID→ZREVRANGE agent index; 失败 fallback capped SCAN |
| `taskengine/store_redis.go` | `SaveTask`: Direct 路由任务写入 agent ZSET |
| `taskengine/store_redis.go` | `updateIndex`: Claim 时维护 agent ZSET + `readTaskCreatedAt` helper |
| `taskengine/store_redis.go` | Lua 脚本: 状态机同步 (Failed/Timeout→Pending retry, fullyTerminal guard) |
| `taskengine/engine_impl.go` | `retryTask`: Delete+Save 替代 UpdateTaskStatus (Redis/Memory Store 兼容) |
| `extension/controlplaneext/taskmanager/service_engine.go` | `ListTasks` 将 AgentID 下传到 engine 层 |

### 集成测试结果

| 测试项 | 修复前 | 修复后 |
|--------|--------|--------|
| `?agent_id=b96b738b6e74-7-1786359963690` | `{"total":0,"tasks":[]}` | **total=6→7, 返回历史+新任务** |
| `?agent_id=95217b544731-7-1785754737121` | 空 | **查到历史 + 新 arthas 任务** |
| 新提交任务 → agent_id 查询 | 不可见 | **秒级可见** |

*最后更新: 2026-08-10*

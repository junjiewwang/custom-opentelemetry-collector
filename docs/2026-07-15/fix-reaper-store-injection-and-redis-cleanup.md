# Fix: Reaper Store 注入缺失导致 context deadline exceeded

## 需求背景

Reaper（过期任务收割器）持续报错 `context deadline exceeded`，每 30s 打印一条 warn 日志：

```
Engine stale reaper: failed to list running tasks
  error: "batch get tasks: mget tasks (legacy): context deadline exceeded"
```

## 根因分析

### 错误链路

```
reaper_engine.go:256 (scanLegacy)
  → engine.ListTasks(Status=Running)
    → RedisStore.ListTasks(query.Status=Running)
      → listRunningTasks()
        → ZRANGE te:running_tasks 0 199  → 返回 0 条（ZSET 为空）
          → fallback: listTasksSlow() 全量 SCAN
            → SCAN te:task:* → 扫出 11,808 个 legacy keys
              → getTasksChunked() → GetTasks()
                → Phase 2 getTasksLegacy() MGET 11,808 个 keys
                  → context deadline exceeded (30s timeout)
```

### 根因总结

| # | 问题 | 影响 |
|---|------|------|
| **1** | `component_factory.go` 中 `createTaskEngine()` 创建的 `store` 未暴露给 `CreateTaskManager` | Reaper 走 `scanLegacy()` 而非 `scanOptimized()` (ZRANGEBYSCORE 快路径) |
| **2** | `te:running_tasks` ZSET 为空 (0 条) | `listRunningTasks()` fallback 到全量 SCAN |
| **3** | Redis DB1 累积了 **11,808 个** legacy format 的 `te:task:*` keys（全部为 failed 终态，无 TTL） | SCAN 扫出全部 → MGET 全量加载 → 30s 超时 |

## 修复方案

### 1. 代码修复：注入 Store 到 Reaper

**文件**: `extension/controlplaneext/component_factory.go`

- `createTaskEngine()` 返回签名从 `(taskengine.Engine, error)` 改为 `(taskengine.Engine, taskengine.Store, error)`
- `CreateTaskManager()` 接收 store 并通过 `taskmanager.WithServiceStore(store)` 注入

**文件**: `extension/controlplaneext/taskmanager/factory.go`

- `NewTaskManager()` 和 `NewTaskManagerWithEngine()` 接受 `opts ...ServiceOption` 并透传给 `NewTaskServiceEngine`

修复后 Reaper 会走 `scanOptimized()` 路径，使用 `ZRANGEBYSCORE` 精确查询超时任务，不再需要全量 SCAN + MGET。

### 2. 数据清理：删除积累的终态 legacy keys

- 清理了 DB1 中 11,808 个 `te:task:*` 格式的 legacy keys
- 全部为 `failed` 终态，使用 `UNLINK` 异步批量删除
- 清理后 DB1 从 ~11,820 keys 降至 12 keys

## 验证

- [x] `go build ./...` 全量编译通过
- [x] `go test ./extension/controlplaneext/taskmanager/...` 单元测试通过
- [x] `go test ./taskengine/...` 单元测试通过
- [ ] 重新部署后确认 warn 日志消失

## 遗留问题

1. **Legacy keys 无 TTL 问题**：需要确认 RedisStore 在任务到达终态时是否为 legacy format keys 设置了 TTL，避免再次积累
2. **ZSET 空时的 fallback 性能**：当 `te:running_tasks` ZSET 为空时，`listRunningTasks` 会 fallback 到 `listTasksSlow()` 全量 SCAN，如果后续有大量非 running 任务堆积，仍可能超时。但修复后 Reaper 走优化路径不经过此逻辑。

## 变更清单

| 文件 | 变更类型 |
|------|----------|
| `extension/controlplaneext/component_factory.go` | 修改 `createTaskEngine` 返回 store，`CreateTaskManager` 注入 store |
| `extension/controlplaneext/taskmanager/factory.go` | `NewTaskManager` 签名增加 `opts ...ServiceOption` |
| Redis DB1 | 清理 11,808 个 `te:task:*` legacy keys |

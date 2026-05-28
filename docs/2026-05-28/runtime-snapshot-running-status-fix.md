# Runtime Snapshot RUNNING 状态提前返回 Bug 修复

## 日期
2026-05-28

## 问题描述

Instrumentation Runtime tab 中，已成功 apply 且增强已生效的规则显示 `Effective=0`，`Refresh Status=failed`，Diagnostic 消息为：

```
runtime snapshot task finished with status running
```

## 根因分析

### Bug 位置
`extension/controlplaneext/instrumentationmanager/runtime_snapshot_service.go`  
函数 `waitForRuntimeSnapshotResult`，第 341 行

### 时序问题

Java Agent 的 `TaskDispatcher` 在执行 `dynamic_instrument_list` 任务时：
1. **T+0ms**：立即上报 `status=RUNNING`（空 payload）→ 控制面 `ReportTaskResult` → `SaveResult` 存入 store
2. **T+~200ms**：执行器完成 → 上报 `status=SUCCESS`（携带完整 payload，包含 `is_effective=true`）

控制面 `waitForRuntimeSnapshotResult` 的轮询逻辑：
```go
// ❌ BUG: 只要 GetTaskResult 返回 found=true 就直接 return
if result, found, err := s.taskMgr.GetTaskResult(waitCtx, taskID); err == nil && found && result != nil {
    return result  // 返回了 status=RUNNING 的中间结果！
}
```

由于 agent 第一次上报 RUNNING 时 store 已经存了 TaskResult（`SaveResult` 不区分终态与非终态），控制面第一次 poll（200ms 后）就读到了 `status=RUNNING` 的 result 并立即返回。

后续流程：
- `runtimeRefreshStatusFromTaskStatus(RUNNING)` → `Failed`（只有 SUCCESS 映射为 Success）
- 进入 `recordFailedRuntimeSnapshot` → 不解析 payload → `IsEffective` 永远为 false

### 对比：正确实现

`extension/mcpext/arthas_orchestrator.go` 的 `waitForTaskResult` 有正确的状态检查：
```go
switch result.Status {
case model.TaskStatusSuccess, model.TaskStatusFailed, model.TaskStatusTimeout, model.TaskStatusCancelled:
    return result, nil  // ✅ 只在终态时才返回
case model.TaskStatusRunning, model.TaskStatusPending:
    continue  // ✅ RUNNING/PENDING 继续等待
}
```

## 修复方案

在 `GetTaskResult` 返回后增加终态检查：

```go
// ✅ 修复后: 只在终态时返回
if result, found, err := s.taskMgr.GetTaskResult(waitCtx, taskID); err == nil && found && result != nil {
    if isTerminalTaskStatus(result.Status) {
        return result
    }
}
```

## 修改文件

| 文件 | 变更 |
|------|------|
| `extension/controlplaneext/instrumentationmanager/runtime_snapshot_service.go` | `waitForRuntimeSnapshotResult` 增加终态检查 |
| `extension/controlplaneext/instrumentationmanager/runtime_snapshot_service_test.go` | 新增两个测试用例 |

## 测试验证

- `TestWaitForRuntimeSnapshotResult_SkipsRunningStatus`：验证 agent 先上报 RUNNING 再上报 SUCCESS 时，控制面能正确等待并返回 SUCCESS 结果
- `TestWaitForRuntimeSnapshotResult_TimeoutWhenNeverTerminal`：验证 agent 永不完成时，控制面正确超时而非挂起

## 影响范围

- 修复后，Runtime Snapshot 能正确显示 `Effective` 数量
- 不影响超时行为（超时仍然返回 `TaskStatusTimeout`）
- 不影响其他使用 `GetTaskResult` 的模块（MCP/Arthas 有独立的等待逻辑）

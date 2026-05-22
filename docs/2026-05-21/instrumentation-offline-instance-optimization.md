# Instrumentation 离线实例规则优化

> 日期：2026-05-21  
> 状态：全部实施完成（P0 + P1）

## 1. 问题描述

Instrumentation 规则在 reconcile 时，会对**所有实例**（包括离线实例）尝试应用规则。对于离线实例：
- 新发现的离线实例会被创建 `TargetStateOffline` 记录（无意义的记录膨胀）
- 前端展示离线实例与在线实例混在一起，视觉噪音大

虽然当前代码已有以下保护措施：
- `dispatchOperationToAgent()` 内部检查 `isAgentOnline()`，离线时标记 skip 不真正下发
- `reconcileExistingTarget()` 将在线 target 降级为 `offline` 状态
- `isTargetExpired()` 离线超 7 天自动变为 `expired`
- Runtime Snapshot 刷新时跳过离线实例

但仍然存在**不必要的 target 记录创建**和**前端展示不友好**的问题。

## 2. 优化方案

### 2.1 核心原则

- **不在 `resolveTargets` 中过滤**：保持其"解析规则目标实例"的单一职责（SRP）
- **在 `reconcileRule` 编排层做策略过滤**：新实例发现路径中加入在线检查
- **已有 target 的 reconcile 逻辑完全不变**：保留离线降级 + 自动恢复 + 7 天过期的完整生命周期

### 2.2 分层设计

```
┌────────────────────────────────────────────────────────────┐
│                    reconcileRule()                          │
│                                                            │
│  1. allTargets = resolveTargets(rule)  // 全量，不过滤     │
│                                                            │
│  2. 新实例发现路径：                                        │
│     for instance in allTargets:                            │
│       if isNewInstance && isAgentOnline(instance):          │ ← 加过滤
│         dispatchOperationToAgent(...)                       │
│       elif isNewInstance && !isAgentOnline(instance):       │
│         skip (不创建 target 记录)                           │ ← 核心优化
│                                                            │
│  3. 已有 target 路径：保持现有逻辑不变                       │
│     reconcileExistingTarget() → 离线降级/过期清理           │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

### 2.3 优先级

| 优先级 | 改动 | 位置 | 说明 |
|--------|------|------|------|
| P0 | 新实例发现路径加在线检查 | `reconcile.go` - `reconcileRule()` 第 157-170 行 | 离线实例不创建 target |
| P0 | Runtime Snapshot 跳过离线实例 | `runtime_snapshot_service.go` | ✅ 已实现 |
| P1 | 前端离线实例折叠/分组展示 | 前端 UI | 减少视觉噪音 |

## 3. 架构评审

### 3.1 设计原则符合性

| 维度 | 评分 | 说明 |
|------|------|------|
| **SRP（单一职责）** | ✅ | `resolveTargets` 只解析目标，`reconcileRule` 做策略编排 |
| **OCP（开闭原则）** | ✅ | 未来新增 filter（灰度/机房）只改编排层，不改 resolve |
| **高内聚低耦合** | ✅ | 每层职责清晰，不互相侵入 |
| **健壮性** | ✅ | 保留离线降级 + 自动恢复 + 7 天过期完整生命周期 |
| **可扩展性** | ✅ | 编排层可灵活组合多种 filter 策略 |
| **最小改动** | ✅ | 只改新实例发现路径 2-3 行代码 |
| **测试兼容** | ✅ | `TestReconcileOfflineTargetExpiresToExpired` 不受影响 |

### 3.2 关键设计决策

**Q: 离线实例的 target 记录是否保留？**

**A: 已有的保留，新发现的不创建。**

理由：
1. 已有 target 的离线降级逻辑（`reconcileExistingTarget` 第 214-226 行）提供状态追踪
2. 7 天超时自动清理机制（`isTargetExpired()`）提供生命周期管理
3. Agent 恢复在线后，`shouldDispatchReconcileApply()` 触发自动重新下发（自愈能力）
4. 新发现的离线实例创建 target 记录无价值 — agent 在线后会在下一轮 reconcile 中被重新发现

### 3.3 不在 `resolveTargets` 中过滤的理由

1. `resolveTargets` 语义是"解析规则应覆盖的全部目标"，过滤属于策略决策
2. `reconcileExistingTarget()` 需要全量 targets 来做离线降级匹配
3. 未来可能需要对离线实例做其他操作（如通知告警），保持全量有利于扩展

## 4. 涉及文件

| 文件 | 改动类型 | 说明 |
|------|----------|------|
| `extension/controlplaneext/instrumentationmanager/reconcile.go` | 修改 | 新实例发现路径加在线检查 |
| `extension/controlplaneext/instrumentationmanager/reconcile_test.go` | 新增用例 | 验证离线实例不创建 target |
| 前端 Instrumentation 页面 | 修改 | P1 离线实例折叠展示 |

## 5. 实施进展

- [x] 问题分析
- [x] 方案设计
- [x] 架构评审
- [x] P0 后端实施：新实例发现路径加在线检查（2026-05-22）
- [x] P0 后端实施：补充单元测试 `TestReconcileSkipsOfflineNewInstance`（2026-05-22）
- [x] P1 前端实施：离线实例折叠展示（2026-05-22）
- [x] P0 前端 UX 提升：HealthSummaryBar 分段进度条 + 一句话结论 + Coverage%（2026-05-22）
- [x] P0 前端 UX 提升：DetailTabs 组件化（Targets / Runtime / Config & Op / Audit 四标签切换）（2026-05-22）
- [x] P0 前端 UX 提升：Target 三段分组 Active/Completed/Offline，消除 removed 混淆（2026-05-22）
- [x] P0 前端 UX 提升：HealthSummaryBar 只统计 active targets，paused 规则特殊提示（2026-05-22）
- [x] 原型页面同步更新三段分组 + active scope（2026-05-22）
- [x] BUG FIX：refreshTargetStatus 数据一致性修复 — 离线 target 不被旧 task 结果覆盖（2026-05-22）
- [x] Runtime Tab 数据一致性对齐：Summary 只统计 Reachable + Offline 分组折叠 + Effective 列 offline 标记（2026-05-22）
- [x] 原型页面同步 Runtime Tab 改进（2026-05-22）

## 6. 设计决策记录

### 6.1 Target 三段分组（2026-05-22）

**问题**：`removed` 状态的 target 与 active（applied/running/pending/failed）混在一起展示，用户误以为有大量"在运行"的实例，实际上只是历史终态记录。

**方案**：三段分组
- **Active**（applied / running / pending / failed / dispatched）— 默认展开，这是用户最关心的
- **Completed / Removed**（removed）— 默认折叠，带 last activity 时间提示
- **Offline / Expired**（offline / expired）— 默认折叠

**Health Summary Bar 修正**：
- Coverage% = applied / activeTotal（不含 removed/offline/expired）
- 进度条只反映 active targets 的状态分布
- inactive 数量作为补充信息显示 "(+N inactive)"

**Paused 规则特殊处理**：
- Active 区域为空时显示 "Rule is paused — all targets have been uninstrumented"
- Health 判定为 "Paused" 而非 "No Active Targets"

### 6.2 Runtime Tab 数据一致性对齐（2026-05-22）

**问题**：Runtime Tab 的 Summary 统计和 Target 列表平铺展示所有 agent（包括离线的），导致：
- Summary 的 TotalTargets=5，Effective=2 — 用户误以为 3 个 agent "not effective"，实际是离线不可达
- 离线 agent 显示旧缓存的 `✓ yes`（Effective），与 Targets Tab 的 "Offline" 状态矛盾

**方案**：
- **Summary 只统计 Reachable agents**：`Reachable / Effective / Drifted / Missing` 四卡片
- **后端**：`summarizeRuleRuntimeSnapshotTargets` 中按 `controlplane_state` 区分 offline/reachable，Effective/Drifted/Missing 只统计 reachable targets
- **前端**：表格分两段
  - **Reachable agents**：正常展示 Effective / Refresh / Drift / Diagnostic
  - **Offline / Expired**：默认折叠，Effective 列显示灰色 "offline" 标签（而非旧缓存的 yes/no）
- **Tab badge**：只显示 reachable 数量（原 total 会误导）

**对齐原则**：Runtime Tab 与 Targets Tab 的 "offline" 概念对齐，离线 agent 在两个 Tab 都折叠/淡化展示。

## 7. 遗留问题

- 暂无

## 附录 A：Bug Fix — refreshTargetStatus 数据一致性问题（2026-05-22）

### A.1 问题现象

Resume 规则后，4 个 target 中有 3 个离线 agent 显示 `state=applied, task_status=success`，但实际只有 1 个在线实例。用户看到的数据与实际情况不一致，造成误导。

### A.2 根因分析

**时序路径**：
1. Pause → 4 个 agent 执行 Remove task → 全部 SUCCESS
2. 3 个 agent 离线（heartbeat 超时）
3. Resume → `dispatchRuleOperation(Apply)`:
   - 在线 agent: 创建新 TaskID, State=dispatched ✅
   - 离线 agent: State=offline, **但 TaskID 未清空**，仍指向旧 Remove task（已 SUCCESS）
4. 用户通过 `ListTargetStatuses` API 查询 → 触发 `refreshRule` → `refreshTargetStatus`:
   - 对离线 target 检查 TaskID → 找到旧 Remove task 的 TaskResult=SUCCESS
   - `mapResultStatusToTargetState(SUCCESS, Active)` → **TargetStateApplied** ← 💥 BUG
   - 因为规则现在是 Active 状态，SUCCESS 被映射为 applied
   - 离线 target 的 state 被错误覆写为 applied

**根本原因**：`refreshTargetStatus` 只根据 task result 映射 state，**不检查 target 当前是否为 offline/expired 状态**。

### A.3 修复方案（双层防护）

**修复 1 — `refreshTargetStatus` 保护 offline/expired 状态**（service.go）：
```go
// 在函数开头添加：
if target.State == TargetStateOffline || target.State == TargetStateExpired {
    return false, nil  // 不刷新，状态由 reconcileExistingTarget 管理
}
```

**修复 2 — `dispatchOperationToAgent` 离线分支清空 TaskID**（reconcile.go）：
```go
if agent == nil || !isAgentOnline(agent) {
    next.State = TargetStateOffline
    next.TaskID = ""       // 清空旧 task 引用
    next.TaskStatus = ""   // 清空
    // ...
}
```

### A.4 测试覆盖

| 测试用例 | 验证点 |
|---------|--------|
| `TestRefreshTargetStatusSkipsOfflineTarget` | offline target 不被旧 task SUCCESS 覆盖为 applied |
| `TestResumeRuleDoesNotShowOfflineTargetsAsApplied` | 完整 Pause→offline→Resume 场景端到端验证 |
| `TestDispatchOperationToAgentClearsTaskIDWhenOffline` | 离线分支正确清空 TaskID/TaskStatus |

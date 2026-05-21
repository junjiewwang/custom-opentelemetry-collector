# Instrumentation 离线实例规则优化

> 日期：2026-05-21  
> 状态：方案已确认，待实施

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
- [ ] P0 后端实施：新实例发现路径加在线检查
- [ ] P0 后端实施：补充单元测试
- [ ] P1 前端实施：离线实例折叠展示

## 6. 遗留问题

- 暂无

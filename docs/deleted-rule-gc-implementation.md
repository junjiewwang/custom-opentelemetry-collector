# 已删除规则 GC 机制实施文档

> **来源评审**：`docs/six-hats-review-deletion-logic.md` 蓝帽综合结论 — "立即可做"行动项  
> **实施时间**：2026-04-16  
> **涉及包**：`extension/controlplaneext/instrumentationmanager`

---

## 需求概述

基于六顶思考帽评审报告中识别的三个优先行动项，本次实施解决以下问题：

1. **最小 GC goroutine** — 定期扫描已删除规则，当所有 target 均为终态且超过保留期后物理删除
2. **Reconcile timeout** — 超时 offline target 自动标记为 expired，防止 deleted 规则永久挂起
3. **`ever_apply_succeeded` 单调字段** — 为未来条件硬删除 fast path 打基础

## 实施状态

| 功能 | 状态 | 说明 |
|------|------|------|
| 最小 GC goroutine | ✅ 已完成 | 新增 `gc.go`，GC worker 按配置间隔运行 |
| Reconcile timeout | ✅ 已完成 | offline target 超时自动标记为 expired |
| `ever_apply_succeeded` 字段 | ✅ 已完成 | 单调字段，任何 target 成功 apply 后设为 true |
| 全量测试通过 | ✅ 已通过 | 所有现有测试无回归 |

## 变更清单

### 1. `interface.go` — 模型层扩展

- **`TargetState` 枚举**：新增 `TargetStateExpired = "expired"`
- **`Rule` struct**：新增 `EverApplySucceeded bool` 字段（JSON: `ever_apply_succeeded`）
- **`Config` struct**：新增三个配置项
  - `GCInterval int64` — GC 扫描间隔（毫秒），默认 60000（60 秒）
  - `DeletedRuleRetention int64` — 已删除规则保留期（毫秒），默认 7 天
  - `ReconcileTargetExpireTimeout int64` — offline target 过期超时（毫秒），默认 7 天
- **`RuleSummary` + `OperationSummary`**：新增 `ExpiredTargets int` 字段

### 2. `store.go` — 存储接口扩展

- **`RuleStore` 接口**：新增 `PhysicalDeleteRule(ctx, ruleID) error` 方法
- **`MemoryRuleStore`**：实现 `PhysicalDeleteRule`，从 map 中删除 rule 和 targets

### 3. `redis_store.go` — Redis 存储实现

- **`RedisRuleStore`**：实现 `PhysicalDeleteRule`，使用 Pipeline 原子执行 `HDEL rules ruleID` + `DEL targets:{ruleID}`

### 4. `reconcile.go` — 收敛逻辑增强

- **offline → expired 自动转换**：`reconcileExistingTarget()` 中，当 target 已处于 `TargetStateOffline` 且超过 `ReconcileTargetExpireTimeout` 时，自动标记为 `TargetStateExpired`
- **expired target 恢复支持**：`shouldDispatchReconcileApply()` 和 `shouldDispatchReconcileRemove()` 新增 `TargetStateExpired` 分支，当 agent 恢复在线时可以重新 dispatch 操作
- **`ExpiredTargets` 同步**：reconcileRule 中 lastOperation 新增 `ExpiredTargets` 字段同步
- **新增方法**：
  - `reconcileTargetExpireTimeout()` — 读取超时配置
  - `isTargetExpired(target, now)` — 判断 target 是否已超时

### 5. `service.go` — 服务层增强

- **`InstrumentationService` struct**：新增 `gcCancel context.CancelFunc` + `gcWG sync.WaitGroup`
- **`Start()`**：启动 GC worker（`s.startGCWorker()`）
- **`Close()`**：停止 GC worker（先停 GC，再停 reconcile）
- **`refreshTargetStatus()`**：当 target 进入 `TargetStateApplied` 时设置 `rule.EverApplySucceeded = true`
- **`summarizeTargets()`**：新增 `TargetStateExpired` 统计分支
- **`deriveOperationStatus()`**：`ExpiredTargets` 视为终态，与 `AppliedTargets` 一起计入已完成判断
- **`ExpiredTargets` 同步**：refreshRule 和 dispatchRuleOperation 中 lastOperation 新增字段同步

### 6. `gc.go` — 新文件，GC worker 实现

- **`startGCWorker()` / `stopGCWorker()`**：GC goroutine 生命周期管理
- **`gcOnce()`**：核心 GC 逻辑
  - 扫描所有 `DesiredState == deleted` 的规则
  - 检查 `now - UpdatedAtMillis >= retention`
  - 检查所有 target 是否为终态（`removed` / `failed` / `expired` / `offline`）
  - 三个条件同时满足 → 调用 `store.PhysicalDeleteRule()` 物理删除
- **`allTargetsTerminal()`**：判断所有 target 是否为终态
- **`isTerminalTargetState()`**：判断单个 target 状态是否为终态
- **`gcInterval()` / `deletedRuleRetention()`**：配置读取方法

## 配置项参考

```yaml
instrumentation_manager:
  # ... 现有配置 ...
  gc_interval_millis: 60000                        # GC 扫描间隔，默认 60 秒
  deleted_rule_retention_millis: 604800000          # 已删除规则保留期，默认 7 天
  reconcile_target_expire_timeout_millis: 604800000 # offline target 过期超时，默认 7 天
```

## 设计决策

### GC 判断条件（三重保护）

1. **`DesiredState == deleted`** — 只有已删除的规则才会被 GC
2. **所有 target 为终态** — 确保没有进行中的操作
3. **保留期已过** — 给运维人员足够的观察窗口

### `TargetStateExpired` 语义

- expired 表示 "这个 target 因超时被放弃"，是一种"受控放弃"
- expired target 在 GC 中被视为终态
- 如果 agent 后来恢复在线，reconcile 会重新尝试 dispatch（从 expired 恢复）
- expired 和 offline 的区别：offline 仍在等待，expired 已放弃等待

### `EverApplySucceeded` 单调性

- 一旦设为 `true`，永远不会回退为 `false`
- 即使 target 后来被 prune、removed 或 expired，此字段不变
- 为未来条件硬删除 fast path 提供保守但正确的判断依据
- 设置时机：`refreshTargetStatus()` 中 `target.State == TargetStateApplied` 时

## 遗留问题实施方案

> 以下方案已于 2026-04-16 完成分析，后续说"开始实施"即可按方案执行。

### 遗留 1：GC / Expired 专项单测

**状态**：⏳ 待实施

#### 现有测试模式

- `reconcile_test.go`（191 行）：使用 `newTestInstrumentationService(t)` 组装内存依赖（nop logger + memory agent registry + memory task manager + memory rule store + DefaultConfig）
- 直接调用内部方法 `svc.reconcileRule()` 而非启动 worker，手动操作 store 中的 target 状态模拟场景
- `redis_store_integration_test.go`（254 行）：通过 `newTestRedisRuleStore(t)` 启动临时 `redis-server` 进程
- `runtime_snapshot_store_redis_integration_test.go`（368 行）：`runtimeSnapshotTaskManagerStub` 是唯一的 mock 对象

#### 测试场景清单（7 个）

| # | 测试名称 | 核心验证点 | 目标文件 |
|---|---------|-----------|---------|
| 1 | `TestReconcileOfflineTargetExpiresToExpired` | offline target 超时后自动变为 expired | `reconcile_test.go` |
| 2 | `TestReconcileExpiredTargetRecoversWhenOnline` | expired target agent 恢复在线后被重新 dispatch | `reconcile_test.go` |
| 3 | `TestGCPhysicallyDeletesEligibleRule` | 满足三条件的 deleted 规则被物理删除 | `gc_test.go`（新建） |
| 4 | `TestGCSkipsRuleWithinRetention` | retention 未到期时规则不被删除 | `gc_test.go` |
| 5 | `TestGCSkipsRuleWithNonTerminalTarget` | 有 running/dispatched target 的规则不被删除 | `gc_test.go` |
| 6 | `TestEverApplySucceededSetOnFirstApply` | target 成功 apply 后 `EverApplySucceeded == true` | `reconcile_test.go` |
| 7 | `TestPhysicalDeleteRule` | Memory 和 Redis 两种 store 的物理删除验证 | `store_test.go`（Memory）+ `redis_store_integration_test.go`（Redis） |

#### 测试 1：`TestReconcileOfflineTargetExpiresToExpired`

```go
func TestReconcileOfflineTargetExpiresToExpired(t *testing.T) {
    svc, registry, tm := newTestInstrumentationService(t)
    svc.config.ReconcileTargetExpireTimeout = 1 // 1ms，极短超时

    registerTestAgent(t, registry, "agent-1")
    rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{
        AppID: "app-a", ServiceName: "svc-a",
        ClassName: "demo.OrderService", MethodName: "submit",
        InstrumentType: InstrumentTypeTrace,
    })
    require.NoError(t, err)
    markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

    // 手动设置 target 为 offline + 过去的时间戳
    targets, err := svc.store.ListTargetStatuses(context.Background(), rule.ID)
    require.NoError(t, err)
    target := findTargetByAgent(targets, "agent-1")
    require.NotNil(t, target)
    target.State = TargetStateOffline
    target.UpdatedAtMillis = time.Now().Add(-1 * time.Second).UnixMilli()
    target.TaskID = ""
    target.TaskStatus = ""
    require.NoError(t, svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets))

    // 关键：让 agent 心跳过期变为 offline（HeartbeatTTL=2ms）
    time.Sleep(3 * time.Millisecond)

    require.NoError(t, svc.reconcileRule(context.Background(), rule))

    targets, err = svc.ListTargetStatuses(context.Background(), rule.ID)
    require.NoError(t, err)
    target = findTargetByAgent(targets, "agent-1")
    require.NotNil(t, target)
    assert.Equal(t, TargetStateExpired, target.State)
}
```

**关键技巧**：`reconcileExistingTarget()` 中 `!isAgentOnline(agent)` → 走 expired 逻辑。需要 agent 已注册但心跳过期（`HeartbeatTTL=2ms`，`sleep 3ms` 即可）。

#### 测试 2：`TestReconcileExpiredTargetRecoversWhenOnline`

```go
func TestReconcileExpiredTargetRecoversWhenOnline(t *testing.T) {
    svc, registry, tm := newTestInstrumentationService(t)

    registerTestAgent(t, registry, "agent-1")
    rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{
        AppID: "app-a", ServiceName: "svc-a",
        ClassName: "demo.OrderService", MethodName: "submit",
        InstrumentType: InstrumentTypeTrace,
    })
    require.NoError(t, err)
    markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

    // 手动设置 target 为 expired
    targets, err := svc.store.ListTargetStatuses(context.Background(), rule.ID)
    require.NoError(t, err)
    target := findTargetByAgent(targets, "agent-1")
    target.State = TargetStateExpired
    target.TaskID = ""
    target.TaskStatus = ""
    target.UpdatedAtMillis = time.Now().UnixMilli()
    require.NoError(t, svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets))

    // 重新注册 agent（模拟恢复在线）+ 刷新心跳
    registerTestAgent(t, registry, "agent-1")
    require.NoError(t, svc.reconcileRule(context.Background(), rule))

    targets, err = svc.ListTargetStatuses(context.Background(), rule.ID)
    require.NoError(t, err)
    target = findTargetByAgent(targets, "agent-1")
    require.NotNil(t, target)
    assert.Equal(t, TargetStateDispatched, target.State)  // 被重新 dispatch
}
```

**验证点**：`shouldDispatchReconcileApply()` 中 `case TargetStateExpired: return true, "expired target is online again"` 分支。

#### 测试 3：`TestGCPhysicallyDeletesEligibleRule`

```go
func TestGCPhysicallyDeletesEligibleRule(t *testing.T) {
    svc, registry, tm := newTestInstrumentationService(t)
    svc.config.DeletedRuleRetention = 1 // 1ms retention

    registerTestAgent(t, registry, "agent-1")
    rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{...})
    require.NoError(t, err)
    markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

    // 删除规则
    _, err = svc.DeleteRule(context.Background(), rule.ID)
    require.NoError(t, err)

    // 手动设置所有 target 为终态
    targets, _ := svc.store.ListTargetStatuses(context.Background(), rule.ID)
    for _, tgt := range targets {
        tgt.State = TargetStateRemoved
    }
    svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets)

    // 设置 rule.UpdatedAtMillis 为过去时间
    rule, _ = svc.store.GetRule(context.Background(), rule.ID)
    rule.UpdatedAtMillis = time.Now().Add(-1 * time.Second).UnixMilli()
    svc.store.SaveRule(context.Background(), rule, false)

    // 执行一次 GC
    svc.gcOnce(context.Background())

    // 验证规则已被物理删除
    _, err = svc.store.GetRule(context.Background(), rule.ID)
    assert.ErrorIs(t, err, ErrRuleNotFound)
}
```

#### 测试 4：`TestGCSkipsRuleWithinRetention`

```go
func TestGCSkipsRuleWithinRetention(t *testing.T) {
    svc, registry, tm := newTestInstrumentationService(t)
    svc.config.DeletedRuleRetention = 999999999 // 很长的 retention

    // ... 创建规则 + 删除 + target 设为终态 ...
    svc.gcOnce(context.Background())

    // 验证规则仍存在
    _, err := svc.store.GetRule(context.Background(), rule.ID)
    assert.NoError(t, err)
}
```

#### 测试 5：`TestGCSkipsRuleWithNonTerminalTarget`

```go
func TestGCSkipsRuleWithNonTerminalTarget(t *testing.T) {
    svc, registry, tm := newTestInstrumentationService(t)
    svc.config.DeletedRuleRetention = 1

    // ... 创建规则 + 删除 ...
    // 关键：保留一个 target 为 TargetStateRunning（非终态）
    targets, _ := svc.store.ListTargetStatuses(context.Background(), rule.ID)
    targets[0].State = TargetStateRunning
    svc.store.SaveTargetStatuses(context.Background(), rule.ID, targets)

    // 设置过期时间
    rule.UpdatedAtMillis = time.Now().Add(-1 * time.Second).UnixMilli()
    svc.store.SaveRule(context.Background(), rule, false)

    svc.gcOnce(context.Background())

    // 验证规则仍存在（因为有非终态 target）
    _, err := svc.store.GetRule(context.Background(), rule.ID)
    assert.NoError(t, err)
}
```

#### 测试 6：`TestEverApplySucceededSetOnFirstApply`

```go
func TestEverApplySucceededSetOnFirstApply(t *testing.T) {
    svc, registry, tm := newTestInstrumentationService(t)
    registerTestAgent(t, registry, "agent-1")

    rule, err := svc.CreateRule(context.Background(), &CreateRuleRequest{...})
    require.NoError(t, err)
    assert.False(t, rule.EverApplySucceeded) // 初始为 false

    markTargetTaskSuccess(t, svc, tm, rule.ID, "agent-1")

    rule, err = svc.GetRule(context.Background(), rule.ID)
    require.NoError(t, err)
    assert.True(t, rule.EverApplySucceeded) // 成功 apply 后为 true
}
```

#### 测试 7：`TestPhysicalDeleteRule`

**Memory 版**（在 `store_test.go` 或内联在 `gc_test.go`）：

```go
func TestMemoryRuleStorePhysicalDelete(t *testing.T) {
    store := NewMemoryRuleStore()
    // SaveRule + SaveTargetStatuses
    // PhysicalDeleteRule
    // GetRule → ErrRuleNotFound
    // ListTargetStatuses → empty
}
```

**Redis 版**（在 `redis_store_integration_test.go`）：

```go
func TestRedisRuleStorePhysicalDelete(t *testing.T) {
    store := newTestRedisRuleStore(t)
    rule := newTestRule("rule-pd")
    targets := newTestTargets("rule-pd")
    // SaveRule + SaveTargetStatuses
    // PhysicalDeleteRule
    // GetRule → ErrRuleNotFound
    // ListTargetStatuses → empty
}
```

---

### 遗留 2：前端 ExpiredTargets 展示

**状态**：⏳ 待实施

#### 改动清单

**文件 1：`extension/adminext/webui-react/src/types/instrumentation.ts`**（3 处）

1. **第 5 行** — `InstrumentationTargetState` 新增 `'expired'`：
   ```typescript
   export type InstrumentationTargetState = 'pending' | 'dispatched' | 'running' | 'applied' | 'removed' | 'failed' | 'offline' | 'expired';
   ```

2. **第 10-18 行** — `InstrumentationRuleSummary` 新增字段：
   ```typescript
   export interface InstrumentationRuleSummary {
     // ... 现有字段 ...
     offline_targets: number;
     expired_targets: number;  // 新增
   }
   ```

3. **第 20-32 行** — `InstrumentationOperationSummary` 新增字段：
   ```typescript
   export interface InstrumentationOperationSummary {
     // ... 现有字段 ...
     offline_targets: number;
     expired_targets: number;  // 新增
   }
   ```

**文件 2：`extension/adminext/webui-react/src/pages/InstrumentationPage.tsx`**（3 处）

1. **第 76-93 行** — `targetStateClass()` 新增 `'expired'` 颜色映射：
   ```typescript
   case 'expired':
     return 'bg-amber-50 text-amber-700 ring-amber-200';
   ```
   > 选择 amber（琥珀色）：区分于 offline（slate 灰蓝）和 failed（red 红色），语义为"已超时放弃"。

2. **第 822-837 行** — Summary Cards 第 4 张卡片扩展：
   ```diff
   - { label: 'Failed / Offline', value: selectedRule.summary.failed_targets + selectedRule.summary.offline_targets, color: 'bg-red-50 text-red-700' },
   + { label: 'Failed / Offline / Expired', value: selectedRule.summary.failed_targets + selectedRule.summary.offline_targets + (selectedRule.summary.expired_targets || 0), color: 'bg-red-50 text-red-700' },
   ```
   > `|| 0` 做向后兼容，防止旧版后端无此字段时 NaN。

3. **第 927-930 行** — Last Operation 区域，在 Offline 之后新增 Expired 字段：
   ```html
   <div>
     <div className="text-xs text-gray-400">Expired</div>
     <div className="mt-1 text-gray-700">{selectedRule.last_operation.expired_targets || 0}</div>
   </div>
   ```

---

### 遗留 3：配置模板注释更新

**状态**：⏳ 待实施

#### 改动位置

`config/template/config.yaml` 第 323 行（`audit_retention: 20` 之后），新增注释掉的配置项：

```yaml
      audit_retention: 20
      # GC (Garbage Collection) settings for deleted rules
      # gc_interval_millis: 60000                        # How often GC scans for eligible rules (default: 60s)
      # deleted_rule_retention_millis: 604800000          # How long to keep deleted rules before physical removal (default: 7 days)
      # Reconcile target expiration
      # reconcile_target_expire_timeout_millis: 604800000 # Auto-expire offline targets after this duration (default: 7 days)
```

**设计决策**：
- 三个配置项全部注释掉（`#` 开头），`DefaultConfig()` 已有合理默认值
- 英文注释，与现有模板风格一致
- GC 设置 2 项分组，expire timeout 单独分组

---

### 遗留 4：`runtime_snapshot_service.go` 适配

**状态**：✅ 已确认不需要改动

#### 分析依据

1. **`detectRuntimeDrift()`**（第 629-658 行）不直接检查 `TargetState`，只关心 `RuleRuntimeSnapshotTarget` 中的 runtime 字段（`RuntimeFound`、`IsEffective`、`InstrumentationAvailable`、`EnhancementCapability`）
2. **expired target**（agent 离线）在 `buildRuleRuntimeSnapshotTarget()`（第 132-189 行）中的表现：
   - `SnapshotAvailable = false`（无法查询 agent）
   - `LastRefreshStatus = "skipped"`
   - `Dirty = true`、`IsStale = true`
3. 这些标记使 expired target 自动归入 `stale_targets` 统计，**不会**产生 `missing`、`ineffective` 等误判的 drift reason
4. 前端 Runtime Snapshot 视图中 `controlplane_state` 字段会正确显示 `'expired'`（类型定义中已有该字段绑定）

**结论**：expired target 在 runtime snapshot 中表现为 "stale/skipped"，语义正确，无需代码改动。

---

## 遗留问题汇总

| 遗留问题 | 状态 | 涉及文件 | 复杂度 | 优先级 |
|---------|------|---------|--------|--------|
| GC/expired 专项单测 | ⏳ 待实施 | `reconcile_test.go` + `gc_test.go`（新建）+ `redis_store_integration_test.go` | 中 | P0 |
| 前端 ExpiredTargets 展示 | ⏳ 待实施 | `instrumentation.ts` + `InstrumentationPage.tsx` | 低 | P1 |
| 配置模板注释更新 | ⏳ 待实施 | `config/template/config.yaml` | 低 | P2 |
| runtime_snapshot 适配 | ✅ 不需要 | 无 | — | — |

---

*本文档将随后续实施进展持续更新。*

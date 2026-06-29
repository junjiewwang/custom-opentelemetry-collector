# Long Poll Waiter 健壮性修复 (Sprint 1)

> **状态**：已实施  
> **创建日期**：2026-06-29  
> **完成日期**：2026-06-29  

---

## 一、修复内容

### Sprint 1 — 最小修复（健壮性）

| # | 问题 | 严重程度 | 类型 |
|---|------|----------|------|
| 1 | defer delete 竞态 — 旧 Poll 的 defer 可能误删新 waiter | 🔴 高 | 正确性 bug |
| 2 | serviceState 永不清除 — 每个连接过的 service 永久驻留内存 | 🟡 中 | 内存泄漏 |
| 3 | Nacos watch 不释放 — 最后一个 waiter 退出后 watch 持续运行 | 🟡 中 | 资源泄漏 |

### Sprint 2 — 消除重复（设计优化）

| # | 问题 | 类型 |
|---|------|------|
| 4 | Waiter 管理逻辑在 ConfigPollHandler 和 TaskPollHandlerEngine 中重复实现 | DRY 违反 |
| 5 | serviceState 一把锁管 config + waiters 两个不相关关注点 | 锁粒度问题 |

### Sprint 3 — 架构优化（关注点分离）

| # | 问题 | 类型 |
|---|------|------|
| 6 | serviceState 混合了配置生命周期和 waiter 管理 | 单一职责 |
| 7 | ConfigHandler 直接操作 configCache 的锁 (`state.RLock/config/Unlock`) | 封装性 |
| 8 | 配置变更通知链路耦合在 handler 中 | 关注点分离 |

---

## 二、变更文件

### 新增：`receiver/agentgatewayreceiver/longpoll/waiter_map.go`

泛型 `WaiterMap[W]` — 线程安全的 waiter 管理容器：

```go
type WaiterMap[W any] struct { m sync.Map }

// API:
func (wm *WaiterMap[W]) Register(key string, waiter *W)
func (wm *WaiterMap[W]) Deregister(key string, waiter *W) // CompareAndDelete 防竞态
func (wm *WaiterMap[W]) Load(key string) (*W, bool)
func (wm *WaiterMap[W]) Range(f func(key string, waiter *W) bool)
func (wm *WaiterMap[W]) IsEmpty() bool
func (wm *WaiterMap[W]) Count() int
func (wm *WaiterMap[W]) Clear(cancelFn func(waiter *W))
```

所有方法继承 `sync.Map` 的并发安全语义，`Deregister` 内置 `CompareAndDelete` 防竞态。

### `config_handler.go`

- `serviceState.waiters` 类型：`map[string]*ConfigWaiter` → `WaiterMap[ConfigWaiter]`
- `serviceState.RWMutex` 职责缩小：只保护 `config` 和 `isWatching`
- 移除 `getWaiters()` 方法（未使用）
- Poll 注册/注销不再需要外部 Lock
- `handleConfigChangeEvent` 中 config 写和 waiter 遍历解耦
- `cleanupIdleServiceState` 双重 IsEmpty 检查
- `Stop()` 使用 `Clear(cancelFn)` 一行取消所有 waiter

### `task_handler_engine.go`

- `waiters` 类型：`sync.Map` → `WaiterMap[TaskWaiter]`
- Register/Deregister 替代 Store/CompareAndDelete
- `handleNotification` Range/Load 类型安全（不再 `.(*TaskWaiter)` 断言）
- `GetWaiterCount()` 一行 `return h.waiters.Count()`
- `Stop()` 使用 `Clear(cancelFn)`

---

## 二、变更文件

### `receiver/agentgatewayreceiver/longpoll/config_handler.go`

**修复 #1 — defer 竞态**：

```go
// Before: 盲目删除
defer func() {
    state.Lock()
    delete(state.waiters, req.AgentID)  // ❌ 可能删除新 waiter
    state.Unlock()
    cancel()
}()

// After: 指针比较
defer func() {
    state.Lock()
    if w, ok := state.waiters[req.AgentID]; ok && w == waiter {
        delete(state.waiters, req.AgentID)               // ✅ 只删自己的 waiter
    }
    isEmpty := len(state.waiters) == 0
    state.Unlock()

    if isEmpty {
        h.cleanupIdleServiceState(state)                  // ✅ drain 清理
    }
    cancel()
}()
```

**修复 #2 + #3 — serviceState drain 清理 + Nacos unwatch**：

新增 `cleanupIdleServiceState()`：
1. 双重检查 waiter 列表（防止新 Poll 在间隙中注册）
2. 调用 `configMgr.UnwatchServiceConfig()` 释放 Nacos 监听
3. 使用 `sync.Map.LoadAndDelete()` 安全删除 serviceState

**Stop() 重构**：
- 取消所有 waiter → 清空 → unwatch → 删除 serviceState
- 复用 `unwatchService()` helper，消除重复代码

### `receiver/agentgatewayreceiver/longpoll/task_handler_engine.go`

**修复 #1 — defer 竞态**：

```go
// Before: 盲目删除
defer func() {
    h.waiters.Delete(req.AgentID)                         // ❌ 可能删除新 waiter
    cancel()
}()

// After: CompareAndDelete
defer func() {
    h.waiters.CompareAndDelete(req.AgentID, waiter)       // ✅ 只删自己的 waiter
    cancel()
}()
```

---

## 三、验证结果

- `go build ./receiver/agentgatewayreceiver/...` ✅
- `go test ./receiver/agentgatewayreceiver/...` ✅ (1.048s, 全部通过)
- `go vet` ✅
- 向后兼容：已存在测试无需修改，全部通过

---

## 四、设计决策

### `sync.Map.LoadAndDelete` 防 race

```
cleanupIdleServiceState:
  1. state.Lock()
  2. 双重检查 waiters 为空
  3. unwatch Nacos
  4. state.Unlock()
  5. LoadAndDelete(key) → 只有返回的 value == state 时才确认删除
```

**为什么不用 `Delete(key)`**：如果 `5` 之前并发 `getOrCreateServiceState` 创建了新的 serviceState，`Delete(key)` 会误删新 state。`LoadAndDelete` + 指针比较确保只删除自己的 state。

### `CompareAndDelete` vs 普通 `Delete`

TaskPollHandlerEngine 使用 `sync.Map.CompareAndDelete`（Go 1.20+），是原子操作，性能优于 Load + if + Delete。

---

## 五、遗留问题（Sprint 2+）

- [ ] 抽象 `WaiterRegistry`，消除 ConfigPollHandler 和 TaskPollHandlerEngine 的 waiter 管理重复代码
- [ ] 拆 serviceState 为 `configCache` + `waiterRegistry`，分离锁粒度
- [ ] 方法名 `loadConfigFromNacos` 重命名为无实现细节的名称

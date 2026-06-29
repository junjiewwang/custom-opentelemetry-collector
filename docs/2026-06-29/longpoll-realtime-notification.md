# Long Poll 实时任务通知方案

> **状态**：已实施，待集成验证  
> **创建日期**：2026-06-29  
> **完成日期**：2026-06-29（代码实施）  
> **问题**：任务创建后 agent 需等待 48-60s（下一次 poll 超时返回后再发新请求）才能收到任务  
> **目标**：任务创建后 ~100ms 内通过当前活跃的 long poll 连接返回给 agent

---

## 一、问题分析

### 现状

```
Agent                   Collector                    Redis
  │                        │                          │
  │── poll (timeout=60s) ──►│                          │
  │                        │ CheckImmediate → 无 task  │
  │                        │ 注册 waiter               │
  │                        │ select { ctx.Done() }     │
  │                        │                          │
  │                        │◄── SubmitTask ────────────│
  │                        │ PublishEvent → te:events: │──► Redis Pub/Sub (无人订阅!)
  │                        │                          │
  │                        │  (60s 超时)               │
  │◄── NoChangeResponse ──│                          │
  │                        │                          │
  │── poll (新请求) ───────►│                          │
  │                        │ CheckImmediate → 有 task! │
  │◄── task 返回 ──────────│                          │
```

**延迟 = poll timeout（最多 60s）**

### 根因

1. `EngineImpl.Submit()` 正确发布了 `EventTaskSubmitted` 到 Redis 频道 `te:events:submitted`
2. **全代码库没有任何组件订阅该频道**——事件被丢弃
3. `TaskPollHandlerEngine.NotifyTaskSubmitted()` 方法已实现，但**生产代码中从未被调用**（仅存在于测试代码中）
4. 结果：long poll 退化为"立即检查 + 超时返回"的纯轮询模式

---

## 二、修复方案

### 目标架构

```
Agent                   Collector (Node A)           Redis         Collector (Node B)
  │                        │                          │                │
  │── poll (timeout=60s) ──►│                          │                │
  │                        │ CheckImmediate → 无 task  │                │
  │                        │ 注册 waiter               │                │
  │                        │                          │                │
  │                        │  SubscribeEvents ─────────│──► PSubscribe  │
  │                        │                          │                │
  │                        │                          │ ◄── SubmitTask (from Node B)
  │                        │                          │──► PublishEvent │
  │                        │◄── TaskEvent ────────────│                │
  │                        │ NotifyTaskSubmitted()    │                │
  │                        │ wakeWaiter() → Claim     │                │
  │◄── task 返回 (~100ms) ─│                          │                │
```

**延迟 = Redis Pub/Sub 传播时间（~1-5ms）+ Claim 操作（~10ms）**

---

## 三、改动范围

| # | 文件 | 改动 | 说明 |
|---|------|------|------|
| 1 | `taskengine/model.go` | `TaskEvent` 加 `TargetNodeID string` | 通知需要知道目标 agent |
| 2 | `taskengine/engine_impl.go` | `Submit()` 中填充 `TargetNodeID: task.Routing.TargetNodeID` | 路由信息写入事件 |
| 3 | `taskengine/store.go` | Store 接口加 `SubscribeEvents(ctx) (<-chan TaskEvent, error)` | 统一订阅入口 |
| 4 | `taskengine/store_redis.go` | 实现 `SubscribeEvents`：PSubscribe `{prefix}:events:*` → goroutine 解析 JSON → 写 channel | 分布式广播 |
| 5 | `taskengine/store_memory.go` | 实现 `SubscribeEvents`：subscriber 列表 + `PublishEvent` 时扇出 | 单节点广播 |
| 6 | `taskengine/engine.go` | 新增 `TaskEventSubscriber` 可选接口 | 不强制所有 Engine 实现 |
| 7 | `receiver/agentgatewayreceiver/manager_init.go` | 订阅事件，goroutine 桥接 `handler.NotifyTaskSubmitted()` | 连线处 |

---

## 四、关键设计决策

### 1. Subscribe 放 Store 层而不是 Engine 层

- MemoryStore 不需要 Redis，channel 扇出即可
- RedisStore 用 PSubscribe，天然支持多节点
- Engine 层只做委托，不引入新概念
- **符合 DIP**：Engine → Store 接口

### 2. 可选接口（TaskEventSubscriber）

```go
// taskengine/engine.go
type TaskEventSubscriber interface {
    SubscribeEvents(ctx context.Context) (<-chan TaskEvent, error)
}
```

- `manager_init.go` 用类型断言 `engine.(taskengine.TaskEventSubscriber)`
- 如果 Engine 不支持，降级为超时轮询（完全兼容已有行为）
- **符合 OCP**：不改 Engine 主接口

### 3. 自收不是问题

- 节点 A submit → publish 到 Redis → 所有节点（包括 A 自己）通过 PSubscribe 收到
- 如果当前节点有该 agent 的 waiter，就唤醒 ✅
- 如果无 waiter，notifyCh buffer(256) 满了就 drop，退化为超时轮询 ✅
- **at-most-once 语义 + 轮询兜底 = 不丢任务**

### 4. MemoryStore 扇出防 panic

- 用 `sync.RWMutex` 保护 subscriber 列表
- subscriber 取消时从列表中移除
- `PublishEvent` 中写 channel 前用 `select` 带 default 防阻塞
- channel 关闭后不再写入（移除 + recover 双保险）

### 5. 生命周期管理

- `SubscribeEvents(ctx)` 返回的 channel 绑定 ctx 生命周期
- ctx 取消 → close channel → 消费方 for-range 退出
- RedisStore 中 PSubscribe goroutine 通过 ctx.Done() 退出并 Unsubscribe

---

## 五、接口设计

```go
// taskengine/store.go — 新增方法
type Store interface {
    // ... existing methods ...

    // SubscribeEvents returns a channel that receives task lifecycle events.
    // The channel is closed when ctx is cancelled or store is closed.
    // Callers should not close the returned channel.
    SubscribeEvents(ctx context.Context) (<-chan TaskEvent, error)
}

// taskengine/engine.go — 可选接口
type TaskEventSubscriber interface {
    SubscribeEvents(ctx context.Context) (<-chan TaskEvent, error)
}
```

---

## 六、健壮性保证

| 场景 | 行为 | 影响 |
|------|------|------|
| Redis 断连 | go-redis PSubscribe 自动重连 | 短暂丢失事件，超时轮询兜底 |
| Channel 满 | select+default drop | 不阻塞 publisher，waiter 超时后兜底 |
| Subscriber goroutine 退出 | 从列表中移除，不再接收 | 干净退出 |
| 消息丢失（Pub/Sub at-most-once） | poll 超时后下次 CheckImmediate 兜底 | 最多退化为原来的 60s |
| Engine 不支持 SubscribeEvents | 类型断言失败，不订阅 | 行为不变（超时轮询） |

---

## 七、设计原则验证

| 原则 | 评估 | 说明 |
|------|------|------|
| **SRP** | ✅ | Store 层管事件收发，Engine 委托，init 层连线 |
| **OCP** | ✅ | 新消费者只需 `engine.(TaskEventSubscriber)` + goroutine 消费 |
| **DIP** | ✅ | 依赖 Store 接口，不依赖具体实现 |
| **高内聚** | ✅ | Events 关注点内聚在 Store 层（Publish + Subscribe） |
| **低耦合** | ✅ | 各层通过 channel/接口通信，无直接引用 |
| **健壮性** | ✅ | 多级兜底（Pub/Sub + buffer + 超时轮询） |
| **可扩展** | ✅ | 未来 metrics/audit 也可订阅同一 channel |

---

## 八、预期效果

| 指标 | 修复前 | 修复后 |
|------|--------|--------|
| 任务下发延迟（无 pending） | ~48-60s | **~100ms** |
| 已有 pending task | ~0ms | ~0ms（不变） |
| 跨节点 submit | ~48-60s | **~100ms** |
| Arthas attach 端到端 | ~52s | **~3-4s** |

---

## 九、实施进度

- [x] `model.go` — TaskEvent 加 TargetNodeID ✅
- [x] `engine_impl.go` — Submit 填充 TargetNodeID ✅
- [x] `store.go` — Store 接口加 SubscribeEvents ✅
- [x] `store_redis.go` — PSubscribe `{prefix}:events:*` + goroutine 解 JSON → channel ✅
- [x] `store_memory.go` — subscriber list + 扇出 + recover 防 panic ✅
- [x] `engine.go` — TaskEventSubscriber 可选接口 ✅
- [x] `engine_impl.go` — 实现 TaskEventSubscriber（委托 Store） ✅
- [x] `manager_init.go` — 类型断言 + goroutine 桥接 `handler.NotifyTaskSubmitted()` ✅
- [x] 编译验证 — `go build` + `go test` + `go vet` 全部通过 ✅
- [ ] Git commit
- [ ] 集成测试 / 端到端验证

---

## 十、遗留问题

- Arthas 启动本身耗时 3s（getInstance 反射 2.3s）—— Agent 侧优化，不在本次范围
- `arthas.aliyun.com` 版本检查是外部网络依赖 —— Agent 侧 Arthas 内部行为

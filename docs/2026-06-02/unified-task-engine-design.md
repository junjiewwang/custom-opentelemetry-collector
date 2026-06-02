# 统一任务引擎 + 节点能力模型 重构方案

> **状态**：Sprint 3 全部完成 + resolveEngine 两层策略实现（共享 controlplane engine → 本地创建 engine → single-node 降级）  
> **创建日期**：2026-06-02  
> **最后更新**：2026-06-02  
> **核心目标**：将 controlplane/taskmanager 和 lifecycle/coordinator 统一为一个 Task Engine，同时引入节点能力模型

---

## 一、问题陈述

### 现状：两套平行的分布式任务系统

| 维度 | controlplane/taskmanager | lifecycle/coordinator |
|------|--------------------------|----------------------|
| **职责** | Agent 远程任务（Arthas诊断、配置下发） | 数据清理协作（分布式 Purge） |
| **模型** | `model.Task` / `model.TaskResult` / `TaskStatus` | `PurgeTask` / `TaskResult` / `TaskStatus` |
| **状态机** | `store.ValidateStateTransition()` | 隐式在 GetProgress 中推断 |
| **队列** | `EnqueueTask` / `DequeueTask` (Redis LIST) | `LPUSH` / `RPOP` (Redis LIST) |
| **路由** | `TargetAgentID` 精确路由 + Global | 无路由（任何节点 RPOP） |
| **结果** | `SaveResult` / `GetResult` (Redis HASH) | `ReportResult` / Redis HASH |
| **重试** | `RetryCount` + StaleTaskReaper | `MaxRetries` + retryFailedTasks |
| **超时** | `TimeoutMillis` + Reaper 检测 | `TaskTimeout` + context deadline |
| **通知** | Redis Pub/Sub | 轮询 |
| **消费方式** | Agent via HTTP LongPoll | Collector Node via Redis RPOP |

### 违反的设计原则

| 原则 | 问题 |
|------|------|
| **DRY** | 两套近乎同构的 Task/Result/Status/Queue 实现 |
| **SRP** | 每套系统各自混合了：状态机 + 队列 + 路由 + 重试 + 存储 |
| **OCP** | 新增任务类型（archive、compact）需要重建一套 |
| **高内聚** | 任务引擎逻辑散落在 `longpoll/task_handler.go` + `taskmanager/service.go` + `lifecycle/scheduler.go` |
| **低耦合** | 业务逻辑（Purge/Arthas）和基础设施（Redis队列/状态机）紧耦合 |

---

## 二、设计目标

1. **统一任务引擎**：一套模型 + 一套状态机 + 一套队列 + 一套存储
2. **节点能力模型**：每个消费者声明能力，引擎按能力路由任务
3. **去掉 RoleAll**：用 roles 组合 + 自动推导，每个节点有明确边界
4. **业务解耦**：Purge/Arthas/Archive 各自只需实现 TaskHandler，不感知引擎内部
5. **协议无关**：引擎不关心消费端是 LongPoll、WebSocket 还是 Redis RPOP
6. **向后兼容**：controlplane TaskManager 接口可以作为引擎之上的 Facade 保留

---

## 三、统一架构分层

```
┌──────────────────────────────────────────────────────────────────────┐
│ Layer 5: Application (应用层) — 各业务域独立                            │
│                                                                       │
│  ┌───────────────┐  ┌──────────────┐  ┌──────────────┐              │
│  │ lifecycle/    │  │ controlplane/│  │ (future)     │              │
│  │ scheduler.go  │  │ arthas/mcp   │  │ archive/     │              │
│  │               │  │              │  │ compact/     │              │
│  │ 产出 purge    │  │ 产出 arthas  │  │ 产出 archive │              │
│  │ tasks         │  │ tasks        │  │ tasks        │              │
│  └───────┬───────┘  └──────┬───────┘  └──────┬───────┘              │
│          │                  │                  │                      │
└──────────┼──────────────────┼──────────────────┼─────────────────────┘
           │ Submit()         │ Submit()         │ Submit()
           ▼                  ▼                  ▼
┌──────────────────────────────────────────────────────────────────────┐
│ Layer 4: Task Engine (引擎层) — 统一核心                               │
│                                                                       │
│  ┌─────────────────────────────────────────────────────────────────┐ │
│  │                    TaskEngine                                     │ │
│  │                                                                   │ │
│  │  Submit() → Router.Route() → Store.Enqueue()                     │ │
│  │  Claim()  → Store.Dequeue(matchedQueues)                         │ │
│  │  Report() → StateMachine.Transition() → Store.UpdateResult()     │ │
│  │                                                                   │ │
│  └─────────────────────────────────────────────────────────────────┘ │
│                                                                       │
│  ┌──────────────┐  ┌─────────────┐  ┌───────────────┐               │
│  │ StateMachine │  │   Router    │  │  RetryPolicy  │               │
│  │              │  │             │  │               │               │
│  │ Pending      │  │ Direct      │  │ MaxRetries    │               │
│  │ → Running    │  │ Capability  │  │ Backoff       │               │
│  │ → Success    │  │ Broadcast   │  │ DeadLetter    │               │
│  │ → Failed     │  │             │  │               │               │
│  │ → Timeout    │  │             │  │               │               │
│  │ → Cancelled  │  │             │  │               │               │
│  └──────────────┘  └─────────────┘  └───────────────┘               │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
           │                  │                  │
           ▼                  ▼                  ▼
┌──────────────────────────────────────────────────────────────────────┐
│ Layer 3: Transport (传输适配层) — 消费端协议适配                         │
│                                                                       │
│  ┌──────────────────┐  ┌────────────────────┐  ┌──────────────────┐ │
│  │ DirectConsumer   │  │ LongPollConsumer   │  │ PubSubConsumer   │ │
│  │ (进程内 RPOP)     │  │ (HTTP 等待)        │  │ (订阅通知)       │ │
│  │                  │  │                    │  │                  │ │
│  │ Collector Node   │  │ Remote Agent       │  │ (预留)           │ │
│  │ 用于 purge       │  │ 用于 arthas        │  │                  │ │
│  └──────────────────┘  └────────────────────┘  └──────────────────┘ │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
           │
           ▼
┌──────────────────────────────────────────────────────────────────────┐
│ Layer 2: Store (存储层) — 持久化抽象                                    │
│                                                                       │
│  ┌──────────────────────────────────────────────────────────────────┐│
│  │                      TaskStore (接口)                              ││
│  │                                                                    ││
│  │  SaveTask / GetTask / UpdateTask                                   ││
│  │  Enqueue / Dequeue / DequeueMulti                                  ││
│  │  SaveResult / GetResult                                            ││
│  │  PublishEvent                                                       ││
│  └──────────────────────────────────────────────────────────────────┘│
│       │                           │                                    │
│  ┌────┴─────┐               ┌─────┴──────┐                           │
│  │ RedisStore│               │ MemoryStore│                           │
│  └──────────┘               └────────────┘                           │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
           │
           ▼
┌──────────────────────────────────────────────────────────────────────┐
│ Layer 1: Node (节点基础设施) — 全局共享                                  │
│                                                                       │
│  ┌──────────────┐  ┌──────────────────┐  ┌───────────────────┐      │
│  │  Identity    │  │  Capability      │  │  NodeRegistry     │      │
│  │              │  │                  │  │                   │      │
│  │  NodeID      │  │  CapabilitySet   │  │  Register()       │      │
│  │  ResolveID() │  │  Role → Caps     │  │  Heartbeat()      │      │
│  │              │  │  Infer()         │  │  ListNodes()      │      │
│  └──────────────┘  └──────────────────┘  └───────────────────┘      │
│                                                                       │
└──────────────────────────────────────────────────────────────────────┘
```

---

## 四、核心接口设计

### 4.1 统一 Task 模型

```go
// taskengine/model.go — 统一模型，替代两处重复定义

package taskengine

// TaskType 任务类型（域:动作 格式，可扩展）
type TaskType string

const (
    // Lifecycle 域
    TaskTypePurgeIndex    TaskType = "lifecycle:purge_index"
    TaskTypeArchiveIndex  TaskType = "lifecycle:archive_index"  // 预留
    TaskTypeCompactIndex  TaskType = "lifecycle:compact_index"  // 预留

    // Controlplane 域
    TaskTypeArthasAttach         TaskType = "arthas:attach"
    TaskTypeArthasDetach         TaskType = "arthas:detach"
    TaskTypeArthasExecSync       TaskType = "arthas:exec_sync"
    TaskTypeArthasSessionOpen    TaskType = "arthas:session_open"
    TaskTypeArthasSessionExec    TaskType = "arthas:session_exec"
    TaskTypeArthasSessionPull    TaskType = "arthas:session_pull"
    TaskTypeArthasSessionClose   TaskType = "arthas:session_close"
    
    // Instrumentation 域
    TaskTypeInstrApply   TaskType = "instrumentation:apply"   // 预留
    TaskTypeInstrRemove  TaskType = "instrumentation:remove"  // 预留
)

// Task 统一任务模型
type Task struct {
    ID          string          `json:"id"`
    Type        TaskType        `json:"type"`
    Payload     json.RawMessage `json:"payload"`       // 业务参数（类型无关的 JSON）
    Priority    int32           `json:"priority"`
    CreatedAt   int64           `json:"createdAt"`     // unix millis
    ExpiresAt   int64           `json:"expiresAt"`     // 0 = never
    Timeout     time.Duration   `json:"timeout"`       // 单任务执行超时
    MaxRetries  int             `json:"maxRetries"`
    RetryCount  int             `json:"retryCount"`

    // 路由
    Routing     TaskRouting     `json:"routing"`

    // 元数据（可扩展）
    Metadata    map[string]string `json:"metadata,omitempty"`
}

// TaskRouting 声明任务如何路由到消费者
type TaskRouting struct {
    Strategy             RoutingStrategy `json:"strategy"`
    TargetNodeID         string          `json:"targetNodeId,omitempty"`         // Direct
    RequiredCapabilities []Capability    `json:"requiredCapabilities,omitempty"` // Capability
    RequiredRoles        []Role          `json:"requiredRoles,omitempty"`        // Role
}

type RoutingStrategy string

const (
    RoutingDirect     RoutingStrategy = "direct"     // 精确到某个 Node/Agent
    RoutingCapability RoutingStrategy = "capability" // 按能力匹配
    RoutingBroadcast  RoutingStrategy = "broadcast"  // 全局队列
)

// TaskResult 统一结果
type TaskResult struct {
    TaskID      string          `json:"taskId"`
    NodeID      string          `json:"nodeId"`
    Status      TaskStatus      `json:"status"`
    Output      json.RawMessage `json:"output,omitempty"`  // 业务结果
    Error       string          `json:"error,omitempty"`
    StartedAt   int64           `json:"startedAt"`         // unix millis
    CompletedAt int64           `json:"completedAt"`       // unix millis
    RetryCount  int             `json:"retryCount"`
}

// TaskStatus 统一状态枚举
type TaskStatus string

const (
    StatusPending   TaskStatus = "pending"
    StatusRunning   TaskStatus = "running"
    StatusSuccess   TaskStatus = "success"
    StatusFailed    TaskStatus = "failed"
    StatusTimeout   TaskStatus = "timeout"
    StatusSkipped   TaskStatus = "skipped"    // 幂等操作（已删除的index）
    StatusCancelled TaskStatus = "cancelled"
)
```

### 4.2 Task Engine 接口

```go
// taskengine/engine.go — 核心引擎接口

// Engine 统一任务引擎（对上层业务暴露）
type Engine interface {
    // === Producer 侧 ===
    Submit(ctx context.Context, task *Task) error
    SubmitBatch(ctx context.Context, tasks []*Task) error
    Cancel(ctx context.Context, taskID string) error

    // === Consumer 侧 ===
    // Claim 从匹配的队列中原子认领一个任务
    // consumer 描述消费者能力，引擎据此选择队列
    Claim(ctx context.Context, consumer *ConsumerDescriptor) (*Task, error)
    
    // Report 上报任务执行结果
    Report(ctx context.Context, result *TaskResult) error

    // === Observer 侧 ===
    GetTask(ctx context.Context, taskID string) (*Task, error)
    GetResult(ctx context.Context, taskID string) (*TaskResult, error)
    GetProgress(ctx context.Context, filter ProgressFilter) (*Progress, error)
    ListTasks(ctx context.Context, query ListQuery) (*ListPage, error)

    // === Lifecycle ===
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}

// ConsumerDescriptor 统一了 "Agent" 和 "Collector Node" 的概念
// — 任何能消费任务的实体
type ConsumerDescriptor struct {
    ID           string         `json:"id"`
    Roles        []Role         `json:"roles"`
    Capabilities *CapabilitySet `json:"capabilities"`
}

// ProgressFilter 进度查询条件
type ProgressFilter struct {
    TaskType TaskType // 按类型过滤
    GroupID  string   // 按批次/epoch 过滤（lifecycle 用）
}

// Progress 聚合进度
type Progress struct {
    Total     int `json:"total"`
    Pending   int `json:"pending"`
    Running   int `json:"running"`
    Completed int `json:"completed"` // success + skipped
    Failed    int `json:"failed"`
    Cancelled int `json:"cancelled"`
}
```

### 4.3 Router 接口

```go
// taskengine/router.go — 路由策略

// Router 决定任务进入哪个队列
type Router interface {
    // Route 根据任务路由信息，返回目标队列 ID
    Route(task *Task) string

    // MatchQueues 根据消费者能力，返回它应该监听的队列 ID 列表
    MatchQueues(consumer *ConsumerDescriptor) []string
}

// 实现：
// DirectRouter:     task.Routing.TargetNodeID → queue = "q:node:{nodeID}"
// CapabilityRouter: task.Routing.RequiredCapabilities → queue = "q:cap:{cap}"
// BroadcastRouter:  → queue = "q:global"

// CompositeRouter 组合路由（根据 RoutingStrategy 分派）
type CompositeRouter struct {
    direct     *DirectRouter
    capability *CapabilityRouter
    broadcast  *BroadcastRouter
}

func (r *CompositeRouter) Route(task *Task) string {
    switch task.Routing.Strategy {
    case RoutingDirect:
        return r.direct.Route(task)
    case RoutingCapability:
        return r.capability.Route(task)
    default:
        return r.broadcast.Route(task)
    }
}

func (r *CompositeRouter) MatchQueues(consumer *ConsumerDescriptor) []string {
    var queues []string
    // 1. 自己的 direct queue
    queues = append(queues, fmt.Sprintf("q:node:%s", consumer.ID))
    // 2. 能力匹配的 capability queues
    for _, cap := range consumer.Capabilities.List() {
        queues = append(queues, fmt.Sprintf("q:cap:%s", cap))
    }
    // 3. 全局 broadcast queue（所有消费者都监听）
    queues = append(queues, "q:global")
    return queues
}
```

### 4.4 State Machine

```go
// taskengine/state_machine.go — 统一状态机（复用 controlplane 已有的设计）

// 合法状态转换表
var validTransitions = map[TaskStatus][]TaskStatus{
    StatusPending:   {StatusRunning, StatusCancelled, StatusTimeout},
    StatusRunning:   {StatusSuccess, StatusFailed, StatusTimeout, StatusSkipped, StatusCancelled},
    // Terminal states: no outgoing transitions
    StatusSuccess:   {},
    StatusFailed:    {},
    StatusTimeout:   {},
    StatusSkipped:   {},
    StatusCancelled: {},
}

func IsTerminal(status TaskStatus) bool {
    return len(validTransitions[status]) == 0
}

func ValidateTransition(from, to TaskStatus) error {
    allowed := validTransitions[from]
    for _, s := range allowed {
        if s == to {
            return nil
        }
    }
    return fmt.Errorf("invalid transition: %s → %s", from, to)
}
```

### 4.5 Store 接口

```go
// taskengine/store.go — 存储抽象（对齐 controlplane/store.TaskStore，但更简洁）

type Store interface {
    // Task CRUD
    SaveTask(ctx context.Context, task *Task) error
    GetTask(ctx context.Context, taskID string) (*Task, error)
    UpdateTaskStatus(ctx context.Context, taskID string, status TaskStatus, nodeID string) error
    ListTasks(ctx context.Context, query ListQuery) (*ListPage, error)
    DeleteTask(ctx context.Context, taskID string) error

    // Queue
    Enqueue(ctx context.Context, queueID string, taskID string, priority int32) error
    Dequeue(ctx context.Context, queueIDs []string) (string, error)  // RPOP from first non-empty
    RemoveFromQueue(ctx context.Context, queueID string, taskID string) error

    // Result
    SaveResult(ctx context.Context, result *TaskResult) error
    GetResult(ctx context.Context, taskID string) (*TaskResult, error)
    ListResults(ctx context.Context, filter ResultFilter) ([]*TaskResult, error)

    // Event (optional, Pub/Sub notification)
    PublishEvent(ctx context.Context, event TaskEvent) error

    // Lifecycle
    Start(ctx context.Context) error
    Close() error
}
```

---

## 五、节点能力模型

### 5.1 能力定义（去掉 RoleAll）

```go
// taskengine/node/capability.go

type Capability string

const (
    // Storage 能力
    CapStorageRead   Capability = "storage:read"
    CapStorageWrite  Capability = "storage:write"
    CapStorageDelete Capability = "storage:delete"

    // Lifecycle 能力
    CapPurgeExecute  Capability = "purge:execute"
    CapPurgePlan     Capability = "purge:plan"

    // Controlplane 能力
    CapArthasExec    Capability = "arthas:execute"   // 能执行 Arthas 任务
    CapConfigPush    Capability = "config:push"      // 能推送配置

    // Query 能力
    CapQueryServe    Capability = "query:serve"

    // UI 能力
    CapUIServe       Capability = "ui:serve"
)
```

### 5.2 Role 定义（组合，非全能）

```go
// taskengine/node/role.go

type Role string

const (
    RoleWriter  Role = "writer"
    RoleReader  Role = "reader"
    RolePurger  Role = "purger"
    RoleUI      Role = "ui"
    RoleAgent   Role = "agent"  // 远端 Agent（Arthas 执行者）
)

// RoleCapabilities 每个角色隐含的能力
var RoleCapabilities = map[Role][]Capability{
    RoleWriter:  {CapStorageWrite},
    RoleReader:  {CapStorageRead, CapQueryServe},
    RolePurger:  {CapStorageRead, CapStorageDelete, CapPurgeExecute, CapPurgePlan},
    RoleUI:      {CapUIServe},
    RoleAgent:   {CapArthasExec},
}

// 节点声明 roles: [writer, purger] → 推导能力 = union(RoleCapabilities[writer], RoleCapabilities[purger])
```

### 5.3 自动推导

```go
// taskengine/node/inferrer.go

// InferRoles 根据运行时实际加载的组件推导节点角色
func InferRoles(components LoadedComponents) []Role {
    var roles []Role

    if components.HasStorageProvider {
        roles = append(roles, RoleWriter, RoleReader)
    }
    if components.HasPurger {
        roles = append(roles, RolePurger)
    }
    if components.HasAdminExt {
        roles = append(roles, RoleUI)
    }
    // Agent 角色不会在 Collector 内推导（Agent 是独立进程）

    return roles
}

// LoadedComponents 描述当前进程加载了哪些组件
type LoadedComponents struct {
    HasStorageProvider bool  // observabilitystorageext 已启动
    HasPurger          bool  // Purger 实现了 IndexLister + SingleIndexPurger
    HasAdminExt        bool  // adminext 已加载
    HasControlPlaneExt bool  // controlplaneext 已加载
}
```

### 5.4 NodeRegistry

```go
// taskengine/node/registry.go

type NodeDescriptor struct {
    ID           string            `json:"id"`
    Roles        []Role            `json:"roles"`
    Capabilities *CapabilitySet    `json:"capabilities"`
    Labels       map[string]string `json:"labels,omitempty"`
    StartedAt    int64             `json:"startedAt"`
}

type NodeRegistry interface {
    Register(ctx context.Context, node *NodeDescriptor, ttl time.Duration) error
    Deregister(ctx context.Context, nodeID string) error
    Heartbeat(ctx context.Context, nodeID string) error
    ListNodes(ctx context.Context, filter NodeFilter) ([]*NodeDescriptor, error)
    CountByCapability(ctx context.Context, cap Capability) (int, error)
}

type NodeFilter struct {
    RequiredCapabilities []Capability
    Roles                []Role
}
```

---

## 六、迁移策略

### 核心原则：Engine 在下面，Facade 在上面

```
controlplane/taskmanager/TaskManager (现有接口) 
    → 内部委托给 taskengine.Engine
    → 对外接口不变（向后兼容）

lifecycle/TaskCoordinator (现有接口)
    → 内部委托给 taskengine.Engine
    → 可渐进废弃（scheduler 直接调用 Engine）
```

### 6.1 controlplane 侧适配

```go
// controlplane/taskmanager 保留 TaskManager 接口作为 Facade
type TaskManagerFacade struct {
    engine taskengine.Engine
    logger *zap.Logger
}

func (f *TaskManagerFacade) SubmitTask(ctx context.Context, task *model.Task) error {
    // 转换 model.Task → taskengine.Task
    engineTask := convertToEngineTask(task, RoutingBroadcast, "")
    return f.engine.Submit(ctx, engineTask)
}

func (f *TaskManagerFacade) SubmitTaskForAgent(ctx context.Context, agentMeta *AgentMeta, task *model.Task) error {
    // Agent 特定 → Direct 路由
    engineTask := convertToEngineTask(task, RoutingDirect, agentMeta.AgentID)
    return f.engine.Submit(ctx, engineTask)
}

func (f *TaskManagerFacade) ReportTaskResult(ctx context.Context, result *model.TaskResult) error {
    engineResult := convertToEngineResult(result)
    return f.engine.Report(ctx, engineResult)
}
```

### 6.2 lifecycle 侧适配

```go
// lifecycle/scheduler.go — 直接使用 Engine

func (s *LifecycleScheduler) distributedPurge(ctx context.Context) {
    // 构建 purge tasks
    tasks := s.planTasks(ctx, lister)
    
    // 批量提交（Capability 路由到 purge:execute 队列）
    for _, task := range tasks {
        engineTask := &taskengine.Task{
            ID:      task.ID,
            Type:    taskengine.TaskTypePurgeIndex,
            Payload: marshalPurgePayload(task),
            Timeout: s.config.TaskTimeout,
            MaxRetries: s.config.MaxRetries,
            Routing: taskengine.TaskRouting{
                Strategy:             taskengine.RoutingCapability,
                RequiredCapabilities: []node.Capability{node.CapPurgeExecute},
            },
        }
        s.engine.Submit(ctx, engineTask)
    }
    
    // 当前节点也参与消费
    s.consumeTasks(ctx)
}

func (s *LifecycleScheduler) consumeTasks(ctx context.Context) {
    for {
        task, err := s.engine.Claim(ctx, s.consumer)
        if task == nil { break }
        s.executePurgeTask(ctx, task)
    }
}
```

### 6.3 LongPoll 适配

```go
// receiver/agentgatewayreceiver/longpoll/task_handler.go
// 不再直接操作 Redis，而是通过 Engine.Claim

func (h *TaskPollHandler) getPendingTasks(ctx context.Context, agentID string) ([]*model.Task, error) {
    // Agent 的 ConsumerDescriptor
    consumer := &taskengine.ConsumerDescriptor{
        ID:    agentID,
        Roles: []node.Role{node.RoleAgent},
        Capabilities: node.NewCapabilitySet(node.CapArthasExec),
    }
    
    // 从引擎认领（Direct + Broadcast 队列）
    var tasks []*model.Task
    for {
        task, err := h.engine.Claim(ctx, consumer)
        if task == nil { break }
        tasks = append(tasks, convertFromEngineTask(task))
    }
    return tasks, nil
}
```

---

## 七、Redis Key 统一设计

```
# 命名规范: {prefix}:{domain}:{type}:{id}
# prefix 默认: "te" (task engine)

# === 任务详情 ===
te:task:{taskID}                    — HASH (Task JSON + status + nodeID)

# === 队列（按路由策略分） ===
te:q:node:{nodeID}                  — LIST (Direct 路由：指定节点的任务)
te:q:cap:purge:execute              — LIST (Capability 路由：需要 purge:execute 能力)
te:q:cap:arthas:execute             — LIST (Capability 路由：需要 arthas:execute 能力)
te:q:global                         — LIST (Broadcast 路由：全局队列)

# === 结果 ===
te:result:{taskID}                  — STRING (TaskResult JSON, TTL 24h)

# === 批次/进度（lifecycle 用） ===
te:batch:{groupID}                  — HASH (total, status, createdAt)
te:batch:{groupID}:tasks            — SET (taskIDs in this batch)

# === 节点注册 ===
te:node:{nodeID}                    — HASH (NodeDescriptor JSON)
te:node:{nodeID}:heartbeat          — STRING "1" (TTL, 过期=离线)

# === 事件通知 ===
te:events:submitted                 — Pub/Sub channel
te:events:completed                 — Pub/Sub channel

# === Leader 选举 (lifecycle 专用) ===
te:leader:lifecycle                 — STRING nodeID (TTL 30s)
```

### 向后兼容

controlplane 现有的 `otel:tasks:*` key 格式，通过配置 `key_prefix` 兼容：
```go
// 旧系统: "otel:tasks:pending:global" → 映射为 "te:q:global"
// 迁移期间可双写或一次性迁移
```

---

## 八、文件/包结构

### 新增包: `taskengine/`

```
taskengine/
├── model.go                    # Task, TaskResult, TaskStatus, TaskType
├── engine.go                   # Engine 接口
├── engine_impl.go              # 引擎实现（组合 Router + Store + StateMachine）
├── engine_test.go
├── state_machine.go            # 统一状态机
├── state_machine_test.go
├── router.go                   # Router 接口 + CompositeRouter
├── router_direct.go            # Direct 路由实现
├── router_capability.go        # Capability 路由实现
├── router_broadcast.go         # Broadcast 路由实现
├── router_test.go
├── store.go                    # Store 接口
├── store_redis.go              # Redis 实现
├── store_redis_test.go
├── store_memory.go             # 内存实现（测试 + 单节点）
├── store_memory_test.go
├── retry.go                    # 统一重试策略
├── retry_test.go
├── reaper.go                   # Stale task reaper（从 controlplane 统一）
├── reaper_test.go
├── node/
│   ├── capability.go           # Capability 常量 + CapabilitySet
│   ├── capability_test.go
│   ├── role.go                 # Role 定义 + RoleCapabilities
│   ├── role_test.go
│   ├── descriptor.go           # NodeDescriptor
│   ├── inferrer.go             # 自动推导
│   ├── inferrer_test.go
│   ├── registry.go             # NodeRegistry 接口
│   ├── registry_redis.go       # Redis 实现
│   ├── registry_redis_test.go
│   └── registry_local.go       # 本地 no-op
└── batch.go                    # Batch/Epoch 管理（lifecycle 批量任务用）
```

### 修改的现有包

| 包 | 变更 |
|---|---|
| `lifecycle/` | 删除 `coordinator_redis.go` / `coordinator_local.go`；`scheduler.go` 改为依赖 `taskengine.Engine` |
| `lifecycle/interfaces.go` | 删除 `TaskCoordinator` / `RetryableCoordinator` 接口（由 Engine 替代） |
| `lifecycle/types.go` | 删除 `PurgeTask` / `TaskResult` / `TaskStatus`（用 taskengine 的） |
| `controlplane/taskmanager/` | `TaskService` 改为 Facade 委托到 `taskengine.Engine` |
| `controlplane/taskmanager/store/` | 保留接口但标记 deprecated，新代码用 `taskengine.Store` |
| `receiver/longpoll/task_handler.go` | 不再直接操作 Redis，通过 Engine.Claim |
| `extension/observabilitystorageext/extension.go` | 构建 Engine + NodeDescriptor + Registry |
| `identity/resolver.go` | 保持不变，被 `node/descriptor.go` 调用 |

---

## 九、实施步骤

### Sprint 2b: Task Engine 核心 + Node 模型 + Lifecycle 迁移

| Step | 内容 | 文件 | 状态 |
|------|------|------|------|
| 1 | Node 能力模型 | `taskengine/node/capability.go`, `role.go`, `descriptor.go`, `inferrer.go` | ✅ Done |
| 2 | Node Registry | `taskengine/node/registry.go`, `registry_redis.go`, `registry_local.go` | ✅ Done |
| 3 | 统一 Task 模型 + 状态机 | `taskengine/model.go`, `state_machine.go` | ✅ Done |
| 4 | Store 接口 + Redis/Memory 实现 | `taskengine/store.go`, `store_redis.go`, `store_memory.go` | ✅ Done |
| 5 | Router（Direct + Capability + Broadcast） | `taskengine/router.go` | ✅ Done |
| 6 | Engine 实现 | `taskengine/engine.go`, `engine_impl.go` | ✅ Done |
| 7 | Lifecycle Scheduler 迁移 | 重构 `scheduler.go` 使用 Engine | ✅ Done |
| 8 | 端到端测试 | 验证 purge 全链路 | ✅ Done |

### Sprint 3: Controlplane 迁移

| Step | 内容 | 文件 | 状态 |
|------|------|------|------|
| 9 | TaskManager Facade 适配 | `taskmanager/service_engine.go`, `model_conversion.go`, `factory.go` | ✅ Done |
| 10 | TaskPollHandler 迁移 | `longpoll/task_handler_engine.go`, `longpoll/engine_adapter.go` | ✅ Done |
| 11 | StaleTaskReaper 统一 | `taskmanager/reaper_engine.go` | ✅ Done |
| 12 | 旧 store 包 deprecated | `taskmanager/store/interface.go` 添加 deprecated 标注 | ✅ Done |
| 13 | Redis key 统一 + factory 路由 | `manager_init.go` 添加 engine 初始化路径 | ✅ Done |

#### Step 9 实施详情（2026-06-02）

**新增文件**：

| 文件 | 作用 |
|------|------|
| `extension/controlplaneext/taskmanager/model_conversion.go` | controlplane/model ↔ taskengine 双向转换（Task、TaskResult、Status、TaskType、Routing） |
| `extension/controlplaneext/taskmanager/service_engine.go` | `TaskServiceEngine` Facade —— 实现全部 14 个 `TaskManager` 接口方法，委托 `taskengine.Engine` |
| `extension/controlplaneext/taskmanager/service_engine_test.go` | 23 个单元测试，覆盖完整生命周期 + 模型转换 round-trip |

**修改文件**：

| 文件 | 变更 |
|------|------|
| `extension/controlplaneext/taskmanager/factory.go` | 新增 `NewTaskManagerWithEngine()` 工厂函数 |

**关键设计决策**：

1. **Facade 模式**：`TaskServiceEngine` 实现 `TaskManager` 接口，外部调用方（MCP handlers / HTTP handlers / longpoll）无需任何改动
2. **模型转换边界**：转换在 Facade 层一次性完成，内部全部使用 engine 模型
3. **Status 映射**：`model.TaskStatusResultTooLarge` → `StatusFailed`（engine 无对应状态）；`StatusSkipped` → `TaskStatusSuccess`
4. **TaskType 映射**：`arthas_attach` ↔ `arthas:attach`，下划线⇄冒号约定；未知类型 fallback 为原始字符串
5. **Routing 推导**：`TargetAgentID != ""` → `RoutingDirect`；否则 → `RoutingBroadcast`
6. **FetchTask 阻塞语义**：Engine.Claim() 是非阻塞的，Facade 用 polling loop + exponential backoff 模拟阻塞
7. **AgentMeta → Metadata**：`AppID`/`ServiceName` 存储在 engine Task 的 `Metadata` map 中，列表查询时恢复
8. **RUNNING 报告兼容**：旧 Agent 报告 `Status=RUNNING` 时，Facade 将其转换为 `Engine.Claim()` 操作

#### Step 10 实施详情（2026-06-02）

**新增文件**：

| 文件 | 作用 |
|------|------|
| `receiver/agentgatewayreceiver/longpoll/task_handler_engine.go` | `TaskPollHandlerEngine` — engine-backed LongPollHandler，替代直接 Redis 操作 |
| `receiver/agentgatewayreceiver/longpoll/engine_adapter.go` | `EngineAdapter` — 将 `taskengine.Engine` 适配为 `TaskClaimEngine` 接口 |
| `receiver/agentgatewayreceiver/longpoll/task_handler_engine_test.go` | 7 个单元测试 |

**关键设计决策**：

1. **接口隔离**：引入 `TaskClaimEngine` 接口（仅 3 方法），避免 longpoll 包直接依赖完整 taskengine
2. **Claim-on-dispatch**：使用 `ClaimTaskForAgent()` 替代 Redis LRem + Lua 脚本，原子完成 dequeue + status 转换
3. **通知机制**：提供 `NotifyTaskSubmitted()` 方法，由 engine 事件监听器调用唤醒 waiters
4. **向后兼容**：旧的 `TaskPollHandler` 保留不变，新旧 handler 可通过 factory 函数选择

#### Step 11 实施详情（2026-06-02）

**新增文件**：

| 文件 | 作用 |
|------|------|
| `extension/controlplaneext/taskmanager/reaper_engine.go` | `StaleTaskReaperEngine` — 通过 Engine.ListTasks + Engine.Report 检测超时 |
| `extension/controlplaneext/taskmanager/reaper_engine_test.go` | 3 个单元测试 |

**关键设计决策**：

1. **简化**：不再需要 `ClearRunning`/`RemoveFromAllQueues`/`PublishEvent` 等侧面操作 — Engine.Report 内部处理一切
2. **集成**：Reaper 生命周期绑定到 `TaskServiceEngine.Start/Close`
3. **超时计算**：沿用 `max(task.Timeout, config.RunningTimeout) × 2` 的 grace period 策略

#### Step 12-13 实施详情（2026-06-02）

**修改文件**：

| 文件 | 变更 |
|------|------|
| `extension/controlplaneext/taskmanager/store/interface.go` | 包文档注释添加 `Deprecated` 标注，指引使用 `taskengine` |
| `extension/controlplaneext/taskmanager/interface.go` | Config.Type 文档更新，支持 `"engine"` 选项 |
| `receiver/agentgatewayreceiver/manager_init.go` | 新增 `initTaskPollHandlerWithEngine()` 方法 + 导入 taskengine 包 |

**Key 统一策略**：

- 新部署直接使用 engine（`Config.Type = "engine"`），key 格式为 `te:{domain}:{id}`
- 旧部署继续使用 `"redis"` 类型，key 格式为 `otel:tasks:*`
- 配置开关驱动，无需数据迁移

#### 生产集成切换实施详情（2026-06-02）

**目标**：让 `Config.Type = "engine"` 成为可部署的端到端路径。

**修改文件**：

| 文件 | 变更 |
|------|------|
| `extension/controlplaneext/component_factory.go` | `CreateTaskManager()` 新增 `"engine"` 分支 + `createTaskEngine()` 方法 |
| `extension/controlplaneext/taskmanager/interface.go` | 新增 `EngineProvider` 可选接口 + 导入 `taskengine` |
| `extension/controlplaneext/taskmanager/service_engine.go` | 新增 `GetEngine()` 方法暴露底层 engine |
| `receiver/agentgatewayreceiver/manager_init.go` | 新增 `initTaskPollHandlerAuto()` —— 自动检测 EngineProvider 并路由 |
| `config/template/config.yaml` | 更新 task_manager 配置注释，标注 engine 类型 |

**端到端路径**：

```
config.yaml: task_manager.type = "engine"
    ↓
ComponentFactory.CreateTaskManager()
    ↓ createTaskEngine() → taskengine.NewRedisStore + taskengine.NewEngine
    ↓ NewTaskManagerWithEngine() → TaskServiceEngine (Facade)
    ↓
Extension.Start() → taskMgr.Start()
    ↓ → engine.Start() + reaper.Start()
    ↓
initLongPollManager()
    ↓ initTaskPollHandlerAuto()
        ↓ type-assert TaskManager → EngineProvider
        ↓ GetEngine() → initTaskPollHandlerWithEngine()
            ↓ NewEngineAdapter(engine) → TaskPollHandlerEngine
```

**启用方式**：只需将 `config.yaml` 中的 `task_manager.type` 从 `"redis"` 改为 `"engine"` 即可完成切换，无需其他代码修改。

**回滚方式**：将 `task_manager.type` 改回 `"redis"` 即可立即回退到旧路径。

#### 统一引擎解析实施详情（2026-06-02，v2 修订）

**目标**：`observabilitystorageext`（lifecycle 分布式 purge）使用统一的 `taskengine.Engine`，无硬依赖 `controlplaneext`。

**核心原则**：**能共享就共享，不能共享就自己创建，但都是统一的 engine，而不是自己搞的。**

**架构（resolveEngine 两层策略）**：

```
┌────────────────────────────────────────────────────────────┐
│              observabilitystorageext                         │
│                                                             │
│  scheduler.distributed = true                               │
│  scheduler.controlplane_extension = "controlplane" (可选)    │
│                                                             │
│  resolveEngine(host):                                       │
│    ┌─────────────────────────────────────────────────────┐ │
│    │ Strategy 1: getSharedEngine(host)                    │ │
│    │   → 从 controlplaneext 获取共享 Engine              │ │
│    │   → 同进程共享，零额外资源开销                        │ │
│    │   → 若 controlplaneext 不存在或非 engine 模式 → nil  │ │
│    ├─────────────────────────────────────────────────────┤ │
│    │ Strategy 2: buildLocalEngine(host)                   │ │
│    │   → 本地创建独立 taskengine.Engine                   │ │
│    │   → 使用相同的 Redis + RedisStore (prefix: "te")    │ │
│    │   → 独立部署场景（无 controlplaneext）的正常路径      │ │
│    │   → 若 Redis 不可用 → nil                           │ │
│    ├─────────────────────────────────────────────────────┤ │
│    │ Fallback: engine == nil                              │ │
│    │   → 日志 warn                                       │ │
│    │   → distributed=true 退化为 single-node 模式         │ │
│    │   → 无分布式协作，但功能不中断                        │ │
│    └─────────────────────────────────────────────────────┘ │
│                                                             │
│  buildLifecycleScheduler()                                  │
│    ├── resolveEngine(host) → engine                         │
│    ├── buildRedisLeaderElector() → LeaderElector             │
│    └── lifecycle.WithEngine(engine, elector)                 │
│                                                             │
└────────────────────────────────────────────────────────────┘
           ↑ (可选共享)            ↑ (本地创建)
           │                       │
┌──────────┴───────────────┐  ┌────┴────────────────────────┐
│  controlplaneext          │  │  Redis (storage extension)   │
│  (同进程部署时)            │  │  (独立部署时)                │
│                           │  │                             │
│  GetTaskEngine()          │  │  taskengine.NewRedisStore() │
│  → EngineProvider 接口     │  │  taskengine.NewEngine()     │
│                           │  │                             │
└───────────────────────────┘  └─────────────────────────────┘
```

**关键差异（vs v1 设计）**：

| 维度 | v1（旧） | v2（当前） |
|------|---------|----------|
| 降级路径 | 回退到旧 `TaskCoordinator` | 本地创建相同的 `taskengine.Engine` |
| 代码路径 | 两条：engine 路径 + coordinator 路径 | 一条：engine only |
| 对 controlplaneext 的依赖 | 软依赖但 fallback 路径完全不同 | 纯优化（共享 vs 独立创建，行为一致） |
| scheduler 内部 coordinator 字段 | 保留 | **已删除** |
| `WithCoordinator` Option | 保留 | **已删除** |
| `distributedPurge` 旧方法 | 保留（~400 行） | **已删除** |
| 最终降级 | 走旧 coordinator 逻辑 | single-node（直接 PurgeExpired） |

**修改文件**：

| 文件 | 变更 |
|------|------|
| `extension/observabilitystorageext/extension.go` | 新增 `resolveEngine()` + `buildLocalEngine()` 方法；移除 `buildCoordinator` fallback |
| `extension/observabilitystorageext/lifecycle/scheduler.go` | 删除 `coordinator` 字段、`WithCoordinator` Option、`distributedPurge` 方法及所有子方法（~400 行） |
| `extension/observabilitystorageext/lifecycle/distributed_scheduler_test.go` | 移除引用 `WithCoordinator`/`distributedPurge` 的测试；新增 `TestScheduler_RunCycle_SingleNodeWhenNoOrchestrator` |
| `extension/observabilitystorageext/lifecycle/coordinator_redis_test.go` | 移除调度器集成测试（保留纯 RedisCoordinator 单元测试） |
| `config/template/config.yaml` | 更新注释：engine 解析顺序说明 |

**关键设计决策**：

1. **消除扩展硬依赖**：`observabilitystorageext` 不 require `controlplaneext`。`controlplane_extension` 配置是可选的优化手段
2. **统一 Engine 类型**：无论共享还是本地创建，都是同一个 `taskengine.Engine` + `taskengine.RedisStore`，行为完全一致
3. **单一代码路径**：`distributed: true` 只走 engine-based `DistributedPurgeOrchestrator`，消除了旧 coordinator 的双路径分支
4. **Dead Code 清除**：~400 行旧 coordinator 相关代码（`distributedPurge`, `executeTasks`, `verifyAndComplete`, `retryFailedTasks` 等）已全部删除
5. **Import cycle 规避**：不变（本地接口 + 类型断言）
6. **Leader 选举分离**：不变（独立的 `LeaderElector`）
7. **优雅降级层级**：共享 engine → 本地 engine → single-node（功能不中断）

**配置示例**：

```yaml
# 场景 1: 同进程部署（共享 engine）
controlplane:
  task_manager:
    type: engine
observability_storage:
  scheduler:
    distributed: true
    controlplane_extension: controlplane  # 可选优化

# 场景 2: 独立部署（本地创建 engine）
observability_storage:
  scheduler:
    distributed: true
    # 无 controlplane_extension → 自动创建本地 engine（使用相同 Redis）

# 场景 3: 无 Redis（单节点模式）
observability_storage:
  scheduler:
    distributed: false  # 或 true 但无 Redis → 自动退化为 single-node
```

**验证结果**：
- ✅ 全项目编译通过（`go build ./...`）
- ✅ lifecycle 测试全部通过（`go test ./extension/observabilitystorageext/lifecycle/... -count=1`）
- ✅ observabilitystorageext 全子包测试通过
- ✅ taskengine 测试全部通过
- ✅ scheduler 代码路径简化为单一 engine 路径
- ✅ 无未使用 import / 无编译警告

**已废弃但保留的代码**：
- `coordinator_redis.go` / `coordinator_local.go`：`RedisCoordinator` 和 `LocalCoordinator` 类型编译正常，但不再被 scheduler 使用。保留原因：(1) 纯 RedisCoordinator 单元测试仍验证其协议正确性；(2) 未来如有其他模块需要低级别协调原语可复用。标记为候选删除。

---

## 十、设计原则验证

| 原则 | 统一后如何满足 |
|------|-------------|
| **SRP** | Engine = 任务编排；Router = 路由决策；Store = 持久化；StateMachine = 状态规则；Node = 身份/能力。各自只有一个变化原因 |
| **OCP** | 新增任务类型 = 注册 TaskType + 写 Handler。不改 Engine/Router/Store |
| **LSP** | ConsumerDescriptor 统一了 Agent 和 Collector Node。只要满足能力契约，就能消费对应任务 |
| **ISP** | Producer 只看到 `Submit()`；Consumer 只看到 `Claim()+Report()`；Observer 只看到 `GetProgress()+ListTasks()` |
| **DIP** | Scheduler/Arthas/MCP 都依赖 `Engine` 接口，不知道底层是 Redis 还是 Memory |
| **DRY** | 一套 Task 模型、一套状态机、一套 Reaper、一套 Store |
| **高内聚** | taskengine 包内各文件围绕"任务编排"高度内聚；node 包围绕"节点能力"内聚 |
| **低耦合** | 业务(lifecycle/arthas) ↔ 引擎(Engine) 通过接口；引擎 ↔ 存储(Store) 通过接口 |
| **可扩展** | 加 archive 任务 = +1 TaskType + 1 Handler + 配置路由到 `q:cap:archive:execute` |
| **健壮性** | 统一重试 + 统一超时 + 统一死信 + 统一 Reaper，一次建设全部享用 |

---

## 十一、风险与缓解

| 风险 | 缓解 |
|------|------|
| 改动面大（lifecycle + controlplane + receiver） | Sprint 2b 先只迁移 lifecycle；Sprint 3 迁移 controlplane |
| controlplane 已有完善的 TaskStore 接口 | Facade 模式保留外部接口，内部委托；不破坏已有调用方 |
| Redis key 命名变更 | 开发阶段，无需兼容旧 key，直接使用新格式 |
| 统一模型过度抽象 | `Payload json.RawMessage` 保留灵活性，各域自行序列化/反序列化业务参数 |
| Agent 消费模式（LongPoll）与 Collector 消费模式（RPOP）差异 | Transport 适配层隔离，Engine 不感知协议差异 |

---

## 十二、验收标准

1. **单元测试**：taskengine 包所有文件 ≥ 90% 覆盖率
2. **状态机测试**：覆盖所有合法/非法转换路径
3. **路由测试**：Direct/Capability/Broadcast 三种策略的正确性
4. **能力匹配测试**：Purger 节点只能认领 purge 任务，Agent 只能认领 arthas 任务
5. **向后兼容测试**：TaskManager Facade 对外行为不变
6. **端到端测试**：lifecycle 完整 purge 链路通过新 Engine 执行
7. **降级测试**：Registry 不可用 → 引擎降级为 broadcast 模式
8. **Reaper 测试**：超时任务被正确标记 + 重试

---

## 十三、与现有 controlplane/store.TaskStore 的关系

```
                    ┌────────────────────┐
                    │ taskengine.Store    │ ← 新的统一接口（更简洁）
                    │ (Layer 2)           │
                    └────────┬───────────┘
                             │
              ┌──────────────┼──────────────┐
              │              │              │
              ▼              ▼              ▼
      ┌──────────────┐ ┌──────────┐ ┌──────────────────────────┐
      │ RedisStore   │ │ MemStore │ │ LegacyStoreAdapter       │
      │ (新实现)      │ │          │ │ (包装 controlplane/store) │
      │              │ │          │ │                          │
      │ 统一 key 格式 │ │ 测试用   │ │ 迁移过渡期使用            │
      └──────────────┘ └──────────┘ └──────────────────────────┘
```

**策略**：开发阶段，直接替换，无需兼容旧 key 格式。
1. Sprint 2b：taskengine 自己的 `store_redis.go` 实现，lifecycle 直接使用
2. Sprint 3：controlplane/taskmanager 内部替换为 taskengine.Store，废弃旧 store 包

---

## 十四、总结

> **一句话**：把"分布式任务编排"从散落在各业务模块中的**实现细节**，提升为一个独立的、可复用的**平台能力**。
>
> - 业务层只关心"产出什么任务"和"如何执行任务"
> - 引擎层负责"路由到哪"、"谁来做"、"状态怎么流转"、"失败怎么重试"
> - 节点层负责"我是谁"、"我能做什么"、"我还活着吗"

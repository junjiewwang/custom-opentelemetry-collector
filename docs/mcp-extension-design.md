# MCP Extension 设计文档

> **状态**: Phase 2 已完成，Phase 3 待实施  
> **创建时间**: 2026-03-12  
> **目标**: 在 OTel Collector 中实现 MCP Server Extension，让 AI Agent 通过 MCP 协议调用 Arthas、性能剖析、动态增强等能力

---

## 1. 背景与目标

### 1.1 核心想法

将 OTel Collector 的 Extension 作为一个 **MCP (Model Context Protocol) Server**，暴露 Arthas 操作、性能剖析、动态增强等能力作为 MCP Tools，AI Agent（如 Claude、GPT 等）通过 MCP 协议直接调用这些工具，实现 AI 驱动的应用诊断与排查。

### 1.2 价值

- 把现有 OTel Collector 控制面能力"AI 化"
- AI Agent 可以自主编排诊断流程（如发现慢接口 → trace → watch → 火焰图 → 给出报告）
- 降低运维排查门槛，非专家也能通过 AI 完成复杂诊断

---

## 2. 整体架构

```
┌──────────┐     MCP Protocol      ┌───────────────────────────────────────┐      WS (Tunnel)     ┌──────────────┐
│ AI Agent │ ◄──────────────────►   │         OTel Collector                │ ◄──────────────────► │ Arthas Agent │
│ (Claude/ │   Streamable HTTP     │                                       │  已建立的 Tunnel      │ (目标JVM)    │
│  GPT等)  │                       │  ┌──────────┐   ┌───────────────────┐ │  WS 连接              │              │
└──────────┘                       │  │ MCP Ext  │──►│ Tunnel Server     │ │                      └──────────────┘
                                   │  │          │   │ (arthastunnelext) │ │
                                   │  │          │──►│                   │ │
                                   │  └──────────┘   └───────────────────┘ │
                                   │       │                               │
                                   │       │ gRPC Task                     │      gRPC
                                   │       ▼                               │ ◄──────────────────►  ┌──────────────┐
                                   │  ┌──────────────┐                     │                       │ Java Agent   │
                                   │  │ControlPlane  │                     │                       │ (你的Agent)  │
                                   │  │Extension     │                     │                       └──────────────┘
                                   │  └──────────────┘                     │
                                   └───────────────────────────────────────┘
```

### 2.1 两条通道各司其职

| 通道 | 用途 | 示例操作 |
|------|------|----------|
| **gRPC（ControlPlane）** | 管理面操作 | `arthas_attach`、`arthas_detach`、`list_agents`、`agent_info` |
| **WebSocket（Tunnel）** | 数据面操作 | 执行 Arthas 命令（`trace`、`watch`、`jad` 等）、获取输出 |

---

## 3. Arthas 交互链路

### 3.1 前提条件

Arthas 的 Tunnel 连接有个前提：**Arthas Agent 必须先 attach 到目标 JVM 并注册到 Tunnel Server**。如果 Arthas 还没有 attach，需要先通过 `arthas_attach` 启动。

### 3.2 完整链路

```
arthas_attach (前提条件)                arthas_trace / watch / jad / ... (命令执行)
─────────────────────────────────►    ────────────────────────────────────────────
执行者: Java Agent (通过 gRPC Task)    执行者: MCP Ext → Tunnel WS relay → Arthas Agent
```

### 3.3 Attach 流程

```
AI Agent ──MCP──► Collector (MCP Ext)
                      │
                      │  gRPC Task: arthas_attach
                      ▼
                  Java Agent (你的 Agent, 已在目标机器上)
                      │
                      │  执行: java -jar arthas-boot.jar
                      │         --pid <target_pid>
                      │         --tunnel-server ws://collector:7777/ws
                      │         --agent-id <agent_id>
                      ▼
                  Arthas 启动
                      │
                      │  WS 注册到 Collector Tunnel Server
                      ▼
                  Tunnel 中出现该 Agent ──► 可以执行 Arthas 命令了
```

### 3.4 命令执行流程（以 trace 为例）

```
AI Agent                    MCP Extension                   arthastunnelext              Arthas Agent
  │                              │                                │                          │
  │ tools/call                   │                                │                          │
  │ arthas_trace(agent,class)    │                                │                          │
  │ ──────────────────────────►  │                                │                          │
  │                              │                                │                          │
  │                              │  1. 检查 Agent 是否已注册 Tunnel │                          │
  │                              │  ─────────────────────────────►│                          │
  │                              │    ◄── 已注册 ✅                │                          │
  │                              │                                │                          │
  │                              │  2. ConnectToAgent(agentID)    │                          │
  │                              │  ─────────────────────────────►│                          │
  │                              │    (编程式 connectArthas)       │  startTunnel             │
  │                              │                                │ ────────────────────────► │
  │                              │                                │                          │
  │                              │                                │  openTunnel (新WS回连)    │
  │                              │                                │ ◄──────────────────────── │
  │                              │                                │                          │
  │                              │  3. relay 建立                  │                          │
  │                              │  ◄─────────────────────────────│                          │
  │                              │                                │                          │
  │                              │  4. 发送 Arthas 命令 (通过 relay WS)                       │
  │                              │  ══════════════════════════════════════════════════════►   │
  │                              │    "trace com.example.OrderService createOrder '#cost>100'"│
  │                              │                                                           │
  │                              │  5. 接收 Arthas 输出                                      │
  │                              │  ◄══════════════════════════════════════════════════════   │
  │                              │                                                           │
  │                              │  6. 收集 + 格式化结果            │                          │
  │                              │                                │                          │
  │  MCP 结果返回                 │                                │                          │
  │  ◄──────────────────────────  │                                │                          │
```

**核心要点**：现有的 Tunnel Server 是**透明中继（relay）模式**，不解析/构造 Arthas 命令。MCP Extension 需要**扮演 Browser 的角色**，模拟一个 Arthas Web 终端来发送命令和接收输出。

---

## 4. MCP Tools 设计

### 4.1 Arthas 生命周期管理

```yaml
- name: arthas_status
  description: "检查目标 Agent 上 Arthas 是否已 attach 并注册到 Tunnel"
  params: { target_agent: string }
  returns: { status: "not_attached" | "attached" | "tunnel_registered" }

- name: arthas_attach
  description: "在目标 Agent 上启动 Arthas 并连接到 Collector Tunnel"
  params:
    target_agent: string   # 目标 Java Agent ID
    pid?: string           # 目标 JVM PID, 可选, 默认 attach 到 Agent 自身 JVM
  returns: { success: bool, agent_id: string, message: string }

- name: arthas_detach
  description: "停止目标 Agent 上的 Arthas（释放资源）"
  params: { target_agent: string }
```

### 4.2 Arthas 命令（需要先 attach）

```yaml
- name: arthas_trace
  description: "方法内部调用路径追踪，显示耗时"
  params: { target_agent, class_name, method_name, condition?, skip_jdk? }
  prerequisite: arthas_status == "tunnel_registered"

- name: arthas_watch
  description: "监控方法调用，输出入参/返回值/异常"
  params: { target_agent, class_name, method_name, express?, condition? }

- name: arthas_stack
  description: "输出方法的调用栈"
  params: { target_agent, class_name, method_name }

- name: arthas_jad
  description: "反编译类查看运行时代码"
  params: { target_agent, class_name }

- name: arthas_sc
  description: "搜索已加载的类"
  params: { target_agent, pattern, detail? }

- name: arthas_thread
  description: "查看线程信息，排查死锁"
  params: { target_agent, thread_id?, top_n? }
```

### 4.3 性能剖析

```yaml
- name: profile_cpu
  description: "CPU 性能剖析，生成火焰图"
  params: { target_agent, duration, format? }

- name: profile_alloc
  description: "内存分配剖析"
  params: { target_agent, duration }

- name: profile_lock
  description: "锁竞争剖析"
  params: { target_agent, duration }
```

### 4.4 动态增强

```yaml
- name: instrument_add
  description: "动态添加插桩点"
  params: { target_agent, class_name, method_name, instrument_type }

- name: instrument_remove
  description: "移除动态插桩点"
  params: { target_agent, task_id }

- name: instrument_list
  description: "列出当前所有动态插桩点"
  params: { target_agent }
```

### 4.5 诊断辅助

```yaml
- name: list_agents
  description: "列出所有在线 Agent"
  params: { }

- name: agent_info
  description: "获取 Agent 详细信息(JVM/OS/应用)"
  params: { target_agent }

- name: heap_dump
  description: "触发 Heap Dump"
  params: { target_agent, live_only? }

- name: gc_info
  description: "查看 GC 统计信息"
  params: { target_agent }
```

---

## 5. 关键设计决策

### 5.1 传输协议

推荐 **Streamable HTTP**，因为 Collector 本身就有 HTTP Server（adminext），可以直接复用。

### 5.2 arthastunnelext 需要新增的接口

现有 `ArthasTunnel` 接口面向 HTTP WebSocket，MCP 需要一个**编程式调用**接口：

```go
type ArthasTunnel interface {
    // 已有
    HandleAgentWebSocket(w http.ResponseWriter, r *http.Request)
    HandleBrowserWebSocket(w http.ResponseWriter, r *http.Request)
    HandleInternalProxy(w http.ResponseWriter, r *http.Request)
    ListConnectedAgents(ctx context.Context) ([]ConnectedAgent, error)

    // 新增 - 面向内部编程式调用
    ConnectToAgent(ctx context.Context, agentID string) (*ArthasSession, error)
}

type ArthasSession struct {
    conn      *websocket.Conn
    sessionID string
    agentID   string
}

func (s *ArthasSession) SendCommand(command string) error { ... }
func (s *ArthasSession) ReadResult(timeout time.Duration) ([]byte, error) { ... }
func (s *ArthasSession) Close() error { ... }
```

实现方式：用 **in-process pipe** 代替真实 WebSocket，复用现有的 `connectArthas → startTunnel → openTunnel` 流程。

### 5.3 安全性

| 维度 | 措施 |
|------|------|
| 认证鉴权 | API Key / JWT |
| 操作审计 | 所有 MCP Tool 调用记录日志 |
| 权限控制 | 按 Tool 粒度控制（如只读 vs 可写） |
| 操作审批 | 高危操作（如 heap_dump）需审批 |

### 5.4 异步与超时

- 性能剖析（如 CPU profiling）是异步的，需要等几十秒
- 使用 MCP 的 Notifications 或 SSE 流式返回进度
- 每个 Tool 调用有超时控制

---

## 6. AI Agent 交互场景示例

```
用户: "order-service 接口响应变慢了，帮我排查"

AI Agent 思考链:

  ① list_agents()
     → 找到 agent: "order-service-pod-abc"

  ② arthas_status("order-service-pod-abc")
     → "not_attached"   ← Arthas 还没启动

  ③ arthas_attach("order-service-pod-abc")          ★ 先 attach
     → "success, tunnel_registered"
     → (Java Agent 启动 Arthas → attach JVM → WS 注册 Tunnel)

  ④ arthas_trace("order-service-pod-abc", "com.example.OrderService", "createOrder")
     → 发现 DB 调用耗时 120ms，占总耗时 94%

  ⑤ arthas_watch("order-service-pod-abc", "com.example.OrderRepository", "save",
                  "{params, returnObj, throwExp}", "#cost>100")
     → 定位到具体 SQL

  ⑥ profile_cpu("order-service-pod-abc", duration=30)
     → 火焰图确认 CPU 瓶颈

  ⑦ 综合分析 → 给出诊断报告:
     "OrderRepository.save() 耗时 120ms，建议检查数据库慢查询和索引"

  ⑧ arthas_detach("order-service-pod-abc")           ← 释放资源
```

---

## 7. 实现路径

### Phase 1: MCP Server 基础框架 ✅

- [x] 在 Collector 中实现 MCP Server (Streamable HTTP) — `mcpext/extension.go`, `mcpext/mcp_server.go`
- [x] Tool 注册/发现机制 — `mcpext/mcp_server.go` 中的 `registerTools()`
- [x] 认证鉴权 (API Key) — `mcpext/auth.go`
- [x] 与现有 arthastunnelext / controlplaneext 集成 — 通过 `Dependencies()` + `discoverDependencies()`
- [x] 首批 MCP Tools: `list_agents`, `agent_info`, `arthas_status`
- [x] 单元测试覆盖（Config / Auth / Formatter，25 tests PASS）
- [x] 注册到 `components.go`
- **SDK**: `github.com/mark3labs/mcp-go v0.45.0`
- **端口**: 独立端口 `0.0.0.0:8686`
- **传输**: Streamable HTTP (JSON-RPC 2.0)

### Phase 2: 核心 Tool 实现 ✅

- [x] Agent 管理类: `list_agents`, `agent_info`, `arthas_status`（Phase 1 已完成）
- [x] Arthas 生命周期: `arthas_attach`, `arthas_detach`（通过 ControlPlane Task 下发 + 轮询等待结果）
- [x] 给 arthastunnelext 新增 `ConnectToAgent()` 编程式接口 + `ArthasSession`
- [x] Arthas 命令类: `arthas_exec`, `arthas_trace`, `arthas_watch`, `arthas_stack`, `arthas_jad`, `arthas_sc`, `arthas_thread`
- [x] Arthas 输出结构化解析/格式化器（双层策略：已知类型结构化 + 未知类型 JSON 兜底）
- [x] 单元测试覆盖（37 tests PASS）

### Phase 3: 高级能力

- [ ] 性能剖析类: `profile_cpu`, `profile_alloc`, `profile_lock`
- [ ] 动态增强类: `instrument_add`, `instrument_remove`, `instrument_list`
- [ ] 异步任务 + 进度通知
- [ ] 结果流式返回
- [ ] 操作审批工作流
- [ ] MCP Resources（暴露 Agent 状态/指标作为 Resource）
- [ ] MCP Prompts（预定义排查模板）

---

## 8. 设计决策详细分析

### 8.1 Arthas Attach 方式选择

**结论：MCP Extension 不需要关心 Arthas 具体怎么 attach，只需下发 `arthas_attach` 任务。**

MCP Extension 的职责边界是：
- 将 AI Agent 的 `arthas_attach` Tool 调用翻译成 ControlPlane Task
- 通过 gRPC 下发到目标 Java Agent
- 等待 Java Agent 回报执行结果

具体的 attach 实现（`arthas-boot.jar` 还是 Java Attach API）是 **Java Agent 侧的实现细节**，对 Collector MCP Extension 透明。MCP Extension 只关心：
1. 任务下发成功/失败
2. Arthas 是否最终注册到 Tunnel（通过 `arthastunnelext.IsAgentRegistered()` 检查）

```
MCP Extension 视角:
  arthas_attach(agent_id) → ControlPlane.DispatchTask("arthas_attach", agent_id) → 等结果 → 检查 Tunnel 注册状态
                                                                                     ↑
                                                                          Java Agent 内部怎么做不关心
```

---

### 8.2 MCP Server 端口选择

**结论：独立端口，不复用 AdminExt 的 HTTP Server。**

理由：
- **职责分离**：MCP Server 面向 AI Agent，AdminExt 面向人类运维，用户群和协议完全不同
- **安全隔离**：MCP 端口可以独立做网络策略控制（如只对 AI 平台开放）
- **生命周期独立**：MCP Extension 和 AdminExt 是独立的 Extension，可以独立启停
- **协议差异**：MCP 使用 Streamable HTTP（JSON-RPC 2.0），AdminExt 是 REST API + WebSocket

配置示例：
```yaml
extensions:
  mcp:
    endpoint: "0.0.0.0:8686"        # MCP 独立端口
    auth:
      type: api_key
      keys: ["sk-xxx"]
  admin:
    endpoint: "0.0.0.0:13133"       # AdminExt 端口（已有）
```

---

### 8.3 多租户隔离

**设计方案：基于 Session 的隔离 + 资源级并发控制**

#### 8.3.1 Session 隔离

每个 AI Agent 连接对应一个独立 Session，Session 内维护：
- 认证身份（API Key → 租户 ID）
- 活跃的 Arthas 连接列表
- 操作历史（审计日志）

```go
type MCPSession struct {
    SessionID   string
    TenantID    string                    // 由 API Key 映射
    ActiveConns map[string]*ArthasSession // agentID → session
    CreatedAt   time.Time
}
```

#### 8.3.2 并发控制策略

| 场景 | 策略 |
|------|------|
| 同一 Agent 被多个 AI Agent 同时操作 | **互斥锁**：同一时间只允许一个 Session 对同一 Agent 执行 Arthas 命令 |
| 不同 Agent 被不同 AI Agent 操作 | **允许并行**：无冲突 |
| 同一 AI Agent 对同一 Agent 多次调用 | **复用连接**：同一 Session 内复用已建立的 Arthas Tunnel 连接 |
| 资源保护 | **全局限制**：最大并发 Arthas 会话数（如 10），超出排队或拒绝 |

```go
type ConcurrencyManager struct {
    mu          sync.Mutex
    agentLocks  map[string]*AgentLock  // agentID → lock info
    maxSessions int                    // 全局最大并发数
    current     int                    // 当前并发数
}

type AgentLock struct {
    HeldBy    string    // SessionID
    AcquireAt time.Time
    TTL       time.Duration  // 自动释放，防止泄漏
}
```

---

### 8.4 Arthas 输出解析

**结论：需要结构化解析。Arthas HTTP API 本身就支持 JSON 结构化输出，通过 Tunnel 传输的也是同样的格式。**

#### 8.4.1 Arthas 返回格式分析

Arthas HTTP API 的 `exec` 返回 **结构化 JSON**，而非纯终端文本：

```json
{
  "state": "SUCCEEDED",
  "body": {
    "results": [
      {
        "type": "enhancer",
        "success": true,
        "effect": { "classCount": 1, "methodCount": 1, "cost": 24 }
      },
      {
        "type": "watch",
        "cost": 0.033375,
        "ts": 1596703454241,
        "value": {
          "params": [1],
          "returnObj": [2, 5, 17],
          "throwExp": null
        }
      },
      {
        "type": "status",
        "statusCode": 0,
        "jobId": 3
      }
    ],
    "timeExpired": false,
    "command": "watch ...",
    "jobStatus": "TERMINATED"
  }
}
```

**`results` 数组中每个元素通过 `type` 字段区分类型：**

| type | 含义 | 关键字段 |
|------|------|----------|
| `status` | 命令执行状态 | `statusCode`（0=成功）、`message` |
| `enhancer` | 类增强结果 | `effect.classCount`、`effect.methodCount` |
| `watch` | watch 命令输出 | `value`（支持 OGNL 自定义结构）、`cost`、`ts` |
| `trace` | trace 命令输出 | 调用树结构 |
| `thread` | 线程信息 | 线程列表数据 |
| `version` | 版本信息 | `version` 字符串 |
| `command` | 命令回显 | `command` 字符串 |
| `input_status` | 输入状态控制 | `inputStatus`（ALLOW_INPUT/ALLOW_INTERRUPT/DISABLED）|

#### 8.4.2 MCP Extension 的解析策略

采用**双层策略**：

1. **结构化解析（优先）**：对已知 `type` 的结果进行结构化解析，提取关键信息后组织成 AI 友好的格式
2. **原文透传（兜底）**：对未知 `type` 或解析失败的结果，直接将 JSON 原文作为 text 返回

```go
// 解析 Arthas 结果
func formatArthasResult(results []map[string]interface{}) []mcp.Content {
    var contents []mcp.Content
    
    for _, result := range results {
        switch result["type"] {
        case "watch":
            // 结构化提取 watch 数据
            contents = append(contents, mcp.Content{
                Type: "text",
                Text: formatWatchResult(result),
            })
        case "trace":
            // 结构化提取 trace 调用树
            contents = append(contents, mcp.Content{
                Type: "text",
                Text: formatTraceTree(result),
            })
        case "status":
            // 命令执行状态
            if statusCode, ok := result["statusCode"].(float64); ok && statusCode != 0 {
                contents = append(contents, mcp.Content{
                    Type: "text",
                    Text: fmt.Sprintf("命令执行失败: %v", result["message"]),
                })
            }
        case "enhancer":
            // 忽略或简要提示
        default:
            // 兜底：原文 JSON 输出
            data, _ := json.MarshalIndent(result, "", "  ")
            contents = append(contents, mcp.Content{
                Type: "text",
                Text: string(data),
            })
        }
    }
    return contents
}
```

**对 AI Agent 的好处**：结构化输出让 AI 可以精确提取数值（如耗时、参数值），而不是从终端文本中用正则匹配。

---

### 8.5 MCP Go SDK 选型

**结论：选择 [mcp-go](https://github.com/mark3labs/mcp-go)（`github.com/mark3labs/mcp-go`）**

#### 8.5.1 候选对比

| | mcp-go (mark3labs) | Go-MCP (ThinkInAI) |
|---|---|---|
| **GitHub Stars** | 4000+ ⭐ | 较新，社区小 |
| **最新版本** | v0.33.0（2026年仍在活跃更新） | 早期版本 |
| **MCP 协议版本** | 完整支持最新规范 | 基础支持 |
| **Streamable HTTP** | ✅ 支持（v0.27+ 起） | ❓ 未确认 |
| **SSE 传输** | ✅ 支持 | ✅ 支持 |
| **Server 端能力** | ✅ Tools / Resources / Prompts / Hooks | 基础 Tools |
| **Session 管理** | ✅ 内置 Session 支持 | ❓ |
| **社区活跃度** | 高（2332+ workflow runs，持续迭代） | 低 |
| **MCP 官方推荐** | ✅ 官方未提供 Go SDK，mcp-go 是事实标准 | 否 |
| **生产案例** | 大量社区使用（GFast、各类 MCP Server） | 较少 |

#### 8.5.2 选择理由

1. **事实标准**：MCP 官方没有 Go SDK，`mcp-go` 是 Go 社区使用最广泛的实现
2. **功能完整**：支持 Streamable HTTP、Session 管理、Hooks 等高级特性，满足所有需求
3. **持续维护**：截至 2026 年仍在活跃更新（v0.33.0），API 趋于稳定
4. **API 简洁**：Tool 注册和 Handler 开发体验好

#### 8.5.3 基础用法示例

```go
import (
    "github.com/mark3labs/mcp-go/mcp"
    "github.com/mark3labs/mcp-go/server"
)

func NewMCPServer() *server.MCPServer {
    s := server.NewMCPServer(
        "OTel Collector MCP",
        "1.0.0",
        server.WithToolCapabilities(true),
        server.WithResourceCapabilities(true, true),
    )

    // 注册 Tool
    arthasTrace := mcp.NewTool("arthas_trace",
        mcp.WithDescription("方法内部调用路径追踪，显示耗时"),
        mcp.WithString("target_agent", mcp.Required(), mcp.Description("目标 Agent ID")),
        mcp.WithString("class_name", mcp.Required(), mcp.Description("类名")),
        mcp.WithString("method_name", mcp.Required(), mcp.Description("方法名")),
        mcp.WithString("condition", mcp.Description("过滤条件，如 #cost>100")),
    )
    s.AddTool(arthasTrace, handleArthasTrace)

    return s
}

// 启动 Streamable HTTP Server
func startMCPServer(s *server.MCPServer, endpoint string) error {
    httpServer := server.NewStreamableHTTPServer(s)
    return httpServer.Start(endpoint)
}
```

---

### 8.6 安全边界

**核心原则：AI Agent 操控生产环境必须有完善的安全防线。**

#### 8.6.1 分层安全模型

```
┌─────────────────────────────────────────┐
│  Layer 1: 网络层                         │
│  - MCP 端口独立，防火墙/安全组控制        │
│  - 仅对 AI 平台 IP 开放                  │
├─────────────────────────────────────────┤
│  Layer 2: 认证层                         │
│  - API Key 认证（Phase 1）               │
│  - JWT + RBAC（Phase 2）                 │
├─────────────────────────────────────────┤
│  Layer 3: 授权层                         │
│  - Tool 粒度权限控制                      │
│  - 只读 vs 可写 vs 高危                   │
├─────────────────────────────────────────┤
│  Layer 4: 审计层                         │
│  - 所有操作记录日志                       │
│  - 结构化审计：who + when + what + result │
├─────────────────────────────────────────┤
│  Layer 5: 防护层                         │
│  - 速率限制                              │
│  - 操作超时自动回收                       │
│  - 高危命令拦截                           │
└─────────────────────────────────────────┘
```

#### 8.6.2 Tool 安全分级

| 等级 | Tool 示例 | 策略 |
|------|----------|------|
| **低危（只读）** | `list_agents`, `agent_info`, `arthas_sc`, `arthas_thread`, `gc_info` | 直接执行，记录日志 |
| **中危（有侵入）** | `arthas_trace`, `arthas_watch`, `arthas_stack`, `arthas_jad`, `profile_cpu` | 执行 + 自动超时回收（如 30s 后自动 stop） |
| **高危（状态变更）** | `arthas_attach`, `arthas_detach`, `instrument_add`, `heap_dump` | 需要审批（Phase 3）或限定白名单环境 |

#### 8.6.3 Phase 1 安全方案（最小可行）

- API Key 认证
- 所有操作结构化日志
- Tool 执行超时控制（防止 watch/trace 遗忘回收）
- 高危命令白名单（只在 dev/staging 环境开放 `heap_dump`、`instrument_add`）

#### 8.6.4 Phase 3 审批工作流（远期）

```
AI Agent → arthas_attach(prod-order-service)
                    │
                    ▼
            MCP Extension 检查: 生产环境 + 高危操作
                    │
                    ▼
            发送审批请求 → 运维人员（企微/钉钉/Slack）
                    │
                    ▼
            审批通过 → 执行操作
            审批拒绝 → 返回拒绝给 AI Agent
```

---

## 9. 遗留问题

> 以下问题在实施过程中逐步细化

1. **Tunnel WS 消息格式确认**：通过 Tunnel relay 传输的消息是否和 HTTP API 完全一致的 JSON 格式，还是终端字符流？需要实际抓包确认
2. **Arthas 长时间命令处理**：`watch` 和 `trace` 是持续监听类命令（不主动 stop 会一直运行），MCP Tool 如何优雅处理——是等 N 条结果后自动 stop，还是提供 `arthas_stop` 命令？
3. **MCP Streamable HTTP 的 Session 恢复**：AI Agent 断线重连后，之前的 Arthas 会话如何恢复或清理？

---

## 10. 实施日志

### 2026-03-12: Phase 1 基础框架完成

**完成内容：**

1. **创建 `custom/extension/mcpext/` 包**，包含以下文件：
   - `factory.go` — Extension Factory，组件类型 `mcp`，稳定性 Development
   - `config.go` — 配置结构（endpoint、auth、依赖扩展名、并发数、超时）
   - `extension.go` — Extension 核心实现（生命周期管理、依赖发现、HTTP Server）
   - `mcp_server.go` — MCP Server 包装器（集成 mcp-go SDK、Tool 注册与处理）
   - `auth.go` — API Key 认证中间件（Bearer Token / X-API-Key）
   - `formatter.go` — AI 友好的文本格式化器

2. **已实现的 MCP Tools**：
   - `list_agents` — 列出所有在线 Agent
   - `agent_info` — 获取指定 Agent 详细信息
   - `arthas_status` — 检查 Agent 的 Arthas Tunnel 连接状态

3. **依赖集成**：
   - 通过 `extensioncapabilities.Dependent` 声明依赖 `controlplaneext` 和 `arthastunnelext`
   - 启动时通过 `host.GetExtensions()` 发现并注入依赖
   - 已注册到 `cmd/customcol/components.go`

4. **MCP SDK**：
   - `github.com/mark3labs/mcp-go v0.45.0`
   - 使用 `StreamableHTTPServer` 传输

5. **测试**：25 个单元测试全部通过
   - `config_test.go` — 配置验证（7 个用例）
   - `auth_test.go` — 认证中间件（10 个用例）
   - `formatter_test.go` — 格式化器（8 个用例）

**文件结构：**
```
custom/extension/mcpext/
├── factory.go           # Extension Factory
├── config.go            # 配置结构
├── extension.go         # 核心实现
├── mcp_server.go        # MCP Server + Tool 注册
├── auth.go              # API Key 认证
├── formatter.go         # 输出格式化
├── config_test.go       # 配置测试
├── auth_test.go         # 认证测试
└── formatter_test.go    # 格式化测试
```

**配置示例：**
```yaml
extensions:
  mcp:
    endpoint: "0.0.0.0:8686"
    auth:
      type: api_key
      api_keys: ["sk-your-key-here"]
    controlplane_extension: "controlplane"
    arthas_tunnel_extension: "arthas_tunnel"
    max_concurrent_sessions: 10
    tool_timeout: 30
```

**下一步：Phase 2 — 核心 Tool 实现** ✅ 已完成

### 2026-03-12: Phase 2 核心 Tool 实现完成

**完成内容：**

#### 1. arthastunnelext 新增编程式接口

- **`ArthasTunnel` 接口扩展**：新增 `ConnectToAgent(ctx, agentID) (*ArthasSession, error)` 方法
- **`ArthasSession` 结构**（`arthas_session.go`）：
  - 封装 WebSocket 连接，提供 `ExecCommand(ctx, command, timeout)` 发送 Arthas 命令并接收 JSON 响应
  - 支持 context 取消、超时控制、并发安全（mutex + closed flag）
  - 发送格式：`{"action":"exec","command":"..."}`
  - 返回类型：`ArthasExecResult`（含 `State`、`Body.Results[]`、`Body.TimeExpired` 等）
- **`connectToAgentProgrammatic()`**（`arthasuri_compat.go`）：
  - 复用现有 `pendingStore.Create()` → `startTunnel` → `openTunnel` 流程
  - 仅支持本地 Agent（分布式跨节点暂返回明确错误）

#### 2. Arthas 生命周期工具（`tools_arthas_lifecycle.go`）

- `arthas_attach` — 检查 Agent → 构建 Task → SubmitTaskForAgent → waitForTaskResult → 验证 Tunnel
- `arthas_detach` — 类似流程，Task 类型为 `arthas_detach`
- `waitForTaskResult(ctx, taskID, timeout)` — 每 500ms 轮询，支持 context 取消和超时

#### 3. Arthas 命令工具（`tools_arthas_commands.go`）— 7 个 MCP Tool

| Tool | 说明 | 特殊参数 |
|------|------|----------|
| `arthas_exec` | 通用命令执行 | 自由命令字符串 |
| `arthas_trace` | 方法调用追踪 | 自动 `--skipJDKMethod true -n 1` |
| `arthas_watch` | 方法监控 | 默认 `'{params, returnObj, throwExp}' -n 1 -x 2` |
| `arthas_jad` | 反编译类 | — |
| `arthas_sc` | 搜索已加载的类 | `-d` 详细模式 |
| `arthas_thread` | 线程信息 | `-b` 死锁、`-n N` top N、指定线程 ID |
| `arthas_stack` | 方法调用栈 | — |

#### 4. Arthas 输出结构化解析/格式化器（`arthas_formatter.go`）

- 双层策略：已知类型结构化解析 + 未知类型 JSON 兜底
- 支持类型：`status`、`enhancer`、`watch`、`trace`、`thread`、`version`
- 边界处理：空结果、`timeExpired` 超时、非 SUCCEEDED、RAW 状态

#### 5. 测试

- `arthas_formatter_test.go` — 14 个用例覆盖所有分支
- `tools_arthas_lifecycle_test.go` — 7 个用例覆盖 waitForTaskResult 各场景

**文件变更：**
```
# 修改
arthastunnelext/interface.go, arthasuri_compat.go, extension.go
mcpext/mcp_server.go
cmd/customcol/components.go

# 新增
arthastunnelext/arthas_session.go
mcpext/tools_arthas_lifecycle.go, tools_arthas_commands.go
mcpext/arthas_formatter.go, arthas_formatter_test.go
mcpext/tools_arthas_lifecycle_test.go
```

**测试**：37 个单元测试全部通过 | **编译**：`go build ./...` 通过

**下一步：Phase 3 — 高级能力**
- [ ] 性能剖析类: `profile_cpu`, `profile_alloc`, `profile_lock`
- [ ] 动态增强类: `instrument_add`, `instrument_remove`, `instrument_list`
- [ ] 异步任务 + 进度通知
- [ ] 结果流式返回
- [ ] 操作审批工作流
- [ ] MCP Resources（暴露 Agent 状态/指标作为 Resource）
- [ ] MCP Prompts（预定义排查模板）

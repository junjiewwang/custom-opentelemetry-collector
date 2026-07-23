# 🔭 Custom OpenTelemetry Collector

> **"不只是收集数据，还能远程看病、AI 问诊，甚至帮你反编译 Java 代码。"**

一个在 [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) 之上长出「控制面大脑」的定制化 Collector —— 集 **可观测数据收集** + **可观测数据存储与查询 (Loki/Tempo API)** + **Java Agent 全生命周期管理** + **Arthas 远程诊断** + **AI 驱动运维 (MCP)** 于一体的统一平台。

---

## 🤔 这玩意儿解决了什么问题？

你是否经历过：

- 🔥 线上 Java 应用 CPU 飙了，想 `arthas trace` 一下，但得先 SSH 到机器、找容器、attach... 黄花菜都凉了
- 📊 Traces、Metrics、Logs 分散在不同收集器里，每个都要单独配一遍；存储在不同后端，查一个 Trace 要跳三个系统
- 🤖 想让 AI 帮你排查问题，但 AI 只能"纸上谈兵"，碰不到你的线上环境
- 🎛️ 几百个 Agent 分散在各处，配置更新得一台台去改
- 📦 ES 索引模板被覆盖/删了，半夜被 mapping 错误告警炸醒

**这个项目就是来治这些"病"的。** 一个 Collector 搞定所有事 —— 收数据、存数据、查数据、诊断线上、AI 问诊。

---

## 🏗️ 整体架构

> 看一张图，秒懂全貌。

```mermaid
graph TB
    subgraph Users["👥 使用者"]
        Human["🧑‍💻 运维人员<br/>(浏览器)"]
        AI["🤖 AI Agent<br/>(Claude / GPT)"]
        JavaAgent["☕ Java Agent<br/>(探针)"]
        ArthasAgent["🔧 Arthas<br/>(JVM 诊断进程)"]
        Grafana["📊 Grafana<br/>(Dashboard 用户)"]
    end

    subgraph Collector["🔭 Custom OpenTelemetry Collector"]
        subgraph Gateway["🚪 统一网关层"]
            Receiver["AgentGateway Receiver<br/>:4317 gRPC / :4318 HTTP+WS"]
            OTLP["OTLP Receiver<br/>:4317/:4318"]
        end

        subgraph Pipeline["⚙️ 数据处理管线"]
            TokenAuth["TokenAuth Processor<br/>(Token 验证 + 缓存)"]
            Batch["Batch Processor"]
            MetricGen["MetricGen Connector<br/>(Traces → RED + Service Graph)"]
            StorageExporter["ObsStorage Exporter<br/>(Traces/Metrics/Logs → ES/PG)"]
            Exporters["Exporters<br/>(OTLP / Jaeger / PrometheusRW)"]
        end

        subgraph Extensions["🧠 扩展层 (核心大脑)"]
            ObsStorage["📊 ObsStorage Extension<br/>(Elasticsearch / Postgres 存储)<br/>Loki API + Tempo API + Prometheus API"]
            Storage["📦 Storage Extension<br/>(Redis / Nacos / COS)"]
            ControlPlane["🧠 ControlPlane Extension<br/>(注册 / 任务 / 配置 / Token)"]
            ArthasTunnel["🔧 Arthas Tunnel Extension<br/>(WebSocket 隧道)"]
            Admin["🖥️ Admin Extension<br/>:8088 REST API + WebUI"]
            MCP["🤖 MCP Extension<br/>:8686 AI Tool Server"]
        end
    end

    subgraph External["☁️ 外部服务"]
        Redis[("Redis<br/>(注册表 / 任务队列)")]
        Nacos[("Nacos<br/>(配置中心)")]
        BlobStore[("BlobStore<br/>(COS / Local)")]
        ES[("Elasticsearch<br/>(可观测数据存储)")]
        PG[("PostgreSQL<br/>(可观测数据存储)")]
        Backend["📊 后端<br/>(Jaeger / Prometheus)"]
    end

    %% 用户连接
    Human -->|"REST API / WebUI"| Admin
    AI -->|"MCP Protocol"| MCP
    JavaAgent -->|"Protobuf / OTLP / gRPC"| Receiver
    ArthasAgent -->|"WebSocket"| Receiver
    Grafana -->|"Loki / Tempo / Prometheus API"| Admin

    %% 网关到管线
    Receiver -->|"OTLP 数据"| TokenAuth
    TokenAuth --> Batch
    Batch --> MetricGen
    MetricGen -->|"Metrics"| StorageExporter
    MetricGen -->|"Traces"| StorageExporter
    Batch --> StorageExporter
    StorageExporter --> ES
    StorageExporter --> PG
    Batch --> Exporters
    Exporters --> Backend

    %% 网关到扩展
    Receiver -->|"控制面 API"| ControlPlane
    Receiver -->|"Arthas WS"| ArthasTunnel

    %% 扩展间依赖
    Storage --> Redis
    Storage --> Nacos
    Storage --> BlobStore
    ControlPlane -.->|"依赖"| Storage
    ArthasTunnel -.->|"分布式模式"| Storage
    ObsStorage -.->|"查询/管理"| ES
    ObsStorage -.->|"查询/管理"| PG
    Admin -.->|"Loki/Tempo/PromQL API"| ObsStorage
    Admin -.->|"依赖"| ControlPlane
    Admin -.->|"依赖"| ArthasTunnel
    MCP -.->|"依赖"| ControlPlane
    MCP -.->|"依赖"| ArthasTunnel
    TokenAuth -.->|"验证 Token"| ControlPlane
```

**一句话总结**：Java Agent 通过统一网关连进来，控制面管理它们的生死，数据存入 ES/PostgreSQL 并提供 Loki/Tempo/PromQL 兼容 API 供 Grafana 查询，Arthas Tunnel 让你能远程"把脉"，MCP 让 AI 也能亲自上手"问诊"。

---

## 🧩 Extension 依赖关系 — 谁先启动，谁依赖谁

> Extensions 是整个系统的"大脑"，它们之间有严格的依赖与启动顺序。

```mermaid
graph LR
    Storage["📦 Storage<br/><i>地基层</i><br/>Redis / Nacos / COS"]
    ObsStorage["📊 ObsStorage<br/><i>数据存储层</i><br/>ES / PG 读写 + 索引生命周期"]
    CP["🧠 ControlPlane<br/><i>核心业务层</i><br/>注册 / 任务 / 配置"]
    AT["🔧 ArthasTunnel<br/><i>诊断能力层</i><br/>WebSocket 隧道"]
    Admin["🖥️ Admin<br/><i>人类交互层</i><br/>REST API + Grafana API + WebUI"]
    MCP["🤖 MCP<br/><i>AI 交互层</i><br/>Tool Server"]

    Storage ==>|"硬依赖"| CP
    Storage -.->|"分布式模式"| AT
    ObsStorage ==>|"硬依赖"| Admin
    CP ==>|"硬依赖"| Admin
    CP ==>|"硬依赖"| MCP
    AT ==>|"硬依赖"| Admin
    AT ==>|"硬依赖"| MCP
    CP -.->|"auto_detach"| AT
```

**启动顺序**：`Storage` → `ObsStorage` / `ControlPlane` → `ArthasTunnel` → `Admin` / `MCP`

---

## 🔌 九大组件详解

### 1. 📦 Storage Extension — 存储界的"万能插座"

> **角色**：最底层的基础设施，给所有组件提供外部存储连接。别人都得喊它一声"大哥"。

| 能力 | 说明 |
|------|------|
| **Redis** | 支持 Standalone / Cluster / Sentinel 三种模式，按名称获取（如 `GetRedis("default")`） |
| **Nacos** | 配置中心 + 服务发现客户端 |
| **BlobStore** | 大文件存储（Profiling 数据等），支持 Local / COS / Noop |

---

### 2. 📊 ObservabilityStorage Extension — 可观测数据的"私人仓库"

> **角色**：替代外部 Jaeger/Prometheus/Loki 后端，直接在 Collector 内存储 Traces/Metrics/Logs 到 Elasticsearch 或 PostgreSQL，并通过 Grafana 兼容 API 对外暴露查询能力。

```mermaid
graph TB
    subgraph ObsStorage["📊 ObsStorage Extension"]
        Provider["Provider 层<br/>(ES / PG 适配)"]
        Lifecycle["🔄 生命周期管理<br/>索引 Purge/Archive<br/>分布式调度 + 选主"]
        Retained["📦 Retention 策略<br/>按数据量/时间/索引数"]
        StoredModel["🗂️ StoredModel<br/>(统一存储模型)"]
        Readers["🔍 Reader 层<br/>TraceReader / LogReader<br/>MetricReader"]
    end

    Grafana["📊 Grafana"]
    Admin["🖥️ Admin Extension"]

    Admin -->|"Loki API"| Readers
    Admin -->|"Tempo API"| Readers
    Admin -->|"Prometheus API"| Readers
    Grafana -->|"Loki Data Source"| Admin
    Grafana -->|"Tempo Data Source"| Admin
    Grafana -->|"Prometheus Data Source"| Admin
    Provider --> ES[("Elasticsearch")]
    Provider --> PG[("PostgreSQL")]
    Lifecycle -->|"Index Lifecycle"| ES
```

**核心能力**：

| 能力 | 说明 |
|------|------|
| **多后端** | Elasticsearch + PostgreSQL 双后端适配 |
| **写入路径** | Collector pipeline → ObsStorage Exporter → Provider → ES/PG |
| **读取路径** | Admin → Provider (ES/PG Reader) → Grafana |
| **Loki API** | `/loki/api/v1/query`, `/loki/api/v1/query_range`, `/loki/api/v1/labels`, `/loki/api/v1/series`, `/loki/api/v1/index/volume`, `/loki/api/v1/detected_fields`, `/loki/api/v1/detected_labels`, `/loki/api/v1/index/stats` |
| **Tempo API** | `/api/traces/{traceId}`, `/api/v2/search/traces`, `/api/v2/search/tags`, `/api/v2/search/tag/{tagName}/values` 等完整 TraceQL 查询 |
| **Prometheus API** | `/api/v1/query`, `/api/v1/query_range`, `/api/v1/labels`, `/api/v1/label/{name}/values` |
| **索引生命周期** | 自动 Purge (删除过期索引)、Archive (归档)、基于 Leader Election 的分布式调度 |
| **Retention 策略** | 按最大索引数、最大天数、最大磁盘使用量自动清理 |

---

### 2.1 🔍 LogQL 查询引擎

> 自研 LogQL Parser + Evaluator，完整支持 Loki 日志查询语法。

**已支持的 LogQL 特性：**

| 特性 | 语法 | 说明 |
|------|------|------|
| 流选择器 | `{app="foo", env=~"prod.*"}` | 完整支持 `=` / `!=` / `=~` / `!~` |
| 行过滤器 | `\|= "error"`, `\|~ "timeout"`, `!=` , `!~` | 内容/正则匹配与排除 |
| 管道解析 | `\| json`, `\| logfmt`, `\| unpack` | 结构化日志字段提取 |
| 管道过滤 | `\| json \| level="error"` | 按解析后的字段过滤 |
| 管道格式化 | `\| line_format "{{.level}}: {{.msg}}"` | 自定义输出格式 |
| OR 表达式 | `{app="foo"} \| level="error" OR level="warn"` | 多分支查询 (Sprint 1-2) |
| Metric 查询 | `sum by (level) (count_over_time({}[5m]))` | Histogram 聚合 (Logs Volume) |
| Metric OR | `sum by (level) (count_over_time({app="foo"} \|= "err" OR \|= "warn"[5m]))` | Metric + OR 组合 |

**数据流 (Log Query)**：
```
Grafana → /loki/api/v1/query_range
  → LogQL Parser → LogQLExpression (含 OR 分解)
  → Evaluator → LogQuery (ES 查询结构)
  → ES LogReader → ES Search → 结果
  → Pipeline 处理器 (json/logfmt/line_format)
  → writeLokiStreamsResponse → Grafana
```

**数据流 (Metric Query)**：
```
Grafana → /loki/api/v1/query_range?query=sum by(level)(count_over_time({}[5m]))
  → IsMetricQuery? → ParseMetric → MetricExpr
  → Evaluator → LogMetricQuery
  → ES nested terms aggregation + date_histogram
  → parseMetricAggResult → writeLokiMatrixResponse → Grafana
```

---

### 2.2 🔍 TraceQL 查询引擎

> 完整实现 Tempo TraceQL 查询引擎，支持 Grafana Tempo 数据源。

**已支持的 TraceQL 特性：**

| 特性 | 说明 |
|------|------|
| **Intrinsic 字段** | `name`, `duration`, `kind`, `status`, `traceDuration`, `traceRootService`, `traceRootName` |
| **Attribute 过滤** | `resource.service.name`, `http.method`, `span.http.status_code` 等任意属性 |
| **嵌套 Set 字段** | `resource.service.namespace`, `resource.k8s.pod.name` 等嵌套字段 |
| **Structural 查询** | `{ span.a >> span.b }` 结构算子 (descendant/child/sibling) |
| **OR 表达式** | 括号化 OR: `{ kind=server && (http.method="GET" \|\| http.method="POST") }` |
| **Tag Keys/Values** | `/api/v2/search/tags`, `/api/v2/search/tag/{name}/values` (含 scope 分组) |
| **Duration Unit** | ns/ms/s/m 自动转换 |

---

### 2.3 📊 Prometheus 兼容 API

> 对 Metric 数据提供 PromQL 风格的查询接口。

| 端点 | 功能 |
|------|------|
| `/api/v1/query` | Instant query |
| `/api/v1/query_range` | Range query |
| `/api/v1/labels` | 获取所有 label 名 |
| `/api/v1/label/{name}/values` | 获取 label 值列表 |

---

### 3. 🧠 ControlPlane Extension — 整个系统的"大脑"

> **角色**：核心业务中枢。Agent 的生老病死、任务的分发调度、配置的下发管理，全归它管。

| 子组件 | 功能 | 存储后端 |
|--------|------|---------|
| **AgentRegistry** | Agent 注册/心跳/在线状态检测/层级索引 (App→Service→Instance) | memory / redis |
| **TaskManager** | 任务生命周期 (PENDING→RUNNING→SUCCESS/FAILED/TIMEOUT)，含 StaleTaskReaper | memory / redis |
| **ConfigManager** | 配置存储与下发，on_demand 模式按需从 Nacos 加载 | memory / nacos / on_demand |
| **TokenManager** | App 创建、Token 生成与验证 | memory / redis |
| **ChunkManager** | 大文件分片上传后合并 | - |
| **ArtifactManager** | 性能剖析数据等产物的持久化与查询 | BlobStore |
| **Notification** | 任务完成后通知外部分析服务（如 perf-analysis） | - |

---

### 4. 🔧 Arthas Tunnel Extension — Java 诊断的"远程手臂"

> **角色**：实现 Arthas Tunnel Server 协议，让你坐在办公室就能远程"把脉"线上 JVM。

```mermaid
sequenceDiagram
    participant Admin as 🖥️ Admin / 🤖 AI
    participant Tunnel as 🔧 Arthas Tunnel
    participant AgentWS as ☕ Arthas Agent (JVM)

    Note over AgentWS: 1️⃣ Arthas 启动后注册
    AgentWS->>Tunnel: WebSocket: agentRegister(agent_id)
    Tunnel-->>AgentWS: ✅ 注册成功，保持长连接
    Tunnel->>Tunnel: 周期性 Ping/Pong 保活

    Note over Admin: 2️⃣ 用户/AI 请求连接
    Admin->>Tunnel: connectArthas(agent_id)
    Tunnel->>Tunnel: 注册 Pending Connection
    Tunnel->>AgentWS: 控制连接: startTunnel 指令

    Note over AgentWS: 3️⃣ Agent 建立数据隧道
    AgentWS->>Tunnel: WebSocket: openTunnel(新连接)
    Tunnel->>Tunnel: Pending 匹配 ✅

    Note over Admin,AgentWS: 4️⃣ 双向透传开始 🎉
    Admin->>Tunnel: Arthas 命令 (trace/watch/jad...)
    Tunnel->>AgentWS: relay →
    AgentWS->>Tunnel: ← 诊断结果
    Tunnel->>Admin: ← 返回结果
```

> 分布式模式下，Agent 连 Node-A，用户连 Node-B 也无妨 —— Redis 索引 + 内部代理自动搞定。

---

### 5. 🖥️ Admin Extension — 给人类的"仪表盘"

> **角色**：REST API + 嵌入式 WebUI。运维人员通过浏览器就能管理一切。同时承载 Loki/Tempo/Prometheus Grafana 兼容 API 供 Grafana Dashboard 消费。

**核心 API 一览：**

| 资源 | 端点 | 能力 |
|------|------|------|
| **应用** | `/api/v2/apps` | CRUD 应用、生成/重置 Token |
| **实例** | `/api/v2/instances` | 列表（支持排序/过滤）、详情、踢下线、统计 |
| **服务** | `/api/v2/services` | 全局服务视图 |
| **配置** | `/api/v2/apps/{id}/config/services/{name}` | 按服务级别读写配置 |
| **任务** | `/api/v2/tasks` | 创建/查看/取消/批量操作、下载产物 |
| **Arthas** | `/api/v2/arthas/agents` | 已连接 Agent 列表 |
| **Arthas WS** | `/api/v2/arthas/ws` | 浏览器 WebSocket 终端（需 WS Token） |
| **通知** | `/api/v2/notifications` | 分析通知记录、重试 |
| **仪表盘** | `/api/v2/dashboard/overview` | 总览统计 |
| **Loki API** | `/loki/*` | Loki 兼容查询 API（用于 Grafana Loki 数据源） |
| **Tempo API** | `/api/traces/*`, `/api/v2/search/*` | Tempo 兼容查询 API（用于 Grafana Tempo 数据源） |
| **Prometheus API** | `/api/v1/*` | Prometheus 兼容查询 API（用于 Grafana Prometheus 数据源） |

---

### 6. 🤖 MCP Extension — 给 AI 的"操作手柄"

> **角色**：让 AI Agent 拥有"双手"——通过 [MCP 协议](https://modelcontextprotocol.io/) 直接操作 Arthas，不再纸上谈兵。

```mermaid
sequenceDiagram
    participant You as 👤 你
    participant AI as 🤖 AI Agent
    participant MCP as MCP Server
    participant CP as 🧠 ControlPlane
    participant Tunnel as 🔧 ArthasTunnel
    participant JVM as ☕ Java App

    You->>AI: "订单服务响应很慢，帮我查一下"

    AI->>MCP: list_agents()
    MCP->>CP: GetOnlineAgents()
    CP-->>MCP: [order-service-pod-1, pod-2, pod-3]
    MCP-->>AI: 找到 3 个实例

    AI->>MCP: arthas_attach(pod-1)
    MCP->>CP: SubmitTaskForAgent("pod-1", arthas_attach)
    Note over JVM: Agent 收到任务，启动 Arthas
    JVM->>Tunnel: WebSocket 注册
    MCP-->>AI: Arthas 已就绪 ✅

    AI->>MCP: arthas_trace("com.example.OrderService", "createOrder")
    MCP->>Tunnel: ConnectToAgent("pod-1")
    Tunnel->>JVM: trace com.example.OrderService createOrder
    JVM-->>Tunnel: DB 查询耗时 2.3s 🐌
    Tunnel-->>MCP: 结构化结果
    MCP-->>AI: 方法追踪结果

    AI->>You: 📋 "发现 createOrder 中 DB 查询耗时 2.3s，<br/>疑似 N+1 问题，建议使用 JOIN 优化"
```

**已支持的 MCP Tools：**

| 工具 | 类型 | 功能 | AI 怎么用 |
|------|------|------|----------|
| `list_agents` | 📋 查询 | 列出所有在线 Agent | "有哪些服务在线？" |
| `agent_info` | 📋 查询 | Agent 详细信息 | "这个实例的 JVM 参数？" |
| `arthas_status` | 📋 查询 | Arthas 连接状态 | "Arthas 连上了吗？" |
| `arthas_attach` | 🔄 生命周期 | 启动 Arthas | "帮我连上这个实例" |
| `arthas_detach` | 🔄 生命周期 | 停止 Arthas | "诊断完了，断开吧" |
| `arthas_exec` | ⚡ 命令 | 通用命令执行 | "执行 dashboard 命令" |
| `arthas_trace` | ⚡ 命令 | 方法调用追踪 | "trace createOrder 方法" |
| `arthas_watch` | ⚡ 命令 | 方法监控 | "watch 入参和返回值" |
| `arthas_jad` | ⚡ 命令 | 反编译 | "看看这个类的源码" |
| `arthas_sc` | ⚡ 命令 | 搜索已加载类 | "这个类加载了吗？" |
| `arthas_thread` | ⚡ 命令 | 线程分析 | "有没有死锁？" |
| `arthas_stack` | ⚡ 命令 | 调用栈 | "谁调用了这个方法？" |

---

### 6.1 🔧 MCP 配置指南

#### Collector 侧配置

在 `config.yaml` 的 `extensions` 中添加 `mcp` 配置段：

```yaml
extensions:
  mcp:
    endpoint: "0.0.0.0:8686"
    auth:
      type: api_key
      api_keys:
        - "sk-your-secret-key-1"
    max_concurrent_sessions: 10
    tool_timeout: 30

service:
  extensions:
    - storage
    - controlplane
    - arthas_tunnel
    - admin
    - mcp
```

#### AI 客户端接入

**Claude Desktop** — `claude_desktop_config.json`：

```json
{
  "mcpServers": {
    "otel-collector": {
      "url": "http://your-collector-host:8686/mcp",
      "headers": {
        "Authorization": "Bearer sk-your-secret-key-1"
      }
    }
  }
}
```

**Cursor** — `.cursor/mcp.json`：

```json
{
  "mcpServers": {
    "otel-collector": {
      "url": "http://your-collector-host:8686/mcp",
      "headers": {
        "Authorization": "Bearer sk-your-secret-key-1"
      }
    }
  }
}
```

> 💡 **健康检查**：`GET http://your-collector-host:8686/health` → `{"status":"ok"}`

---

### 7. 🚪 AgentGateway Receiver — 统一的"大门"

> **角色**：一个 Receiver 扛起三份工作——OTLP 数据收集、控制面 API、Arthas WebSocket。Java Agent 只需要知道一个地址。

**统一长轮询（UnifiedPoll）**—— 一次请求，同时等配置和任务：

```mermaid
sequenceDiagram
    participant Agent as ☕ Java Agent
    participant Poll as ⏳ UnifiedPoll
    participant Config as ConfigHandler
    participant Task as TaskHandler
    participant Nacos as Nacos
    participant Redis as Redis

    Agent->>Poll: POST /v1/control/poll<br/>(config_version + agent_id)

    par 并行等待
        Poll->>Config: 检查配置变更
        Config->>Nacos: 版本比对 / Watch
        Note over Config,Nacos: 阻塞等待 Nacos 推送...
        Poll->>Task: 检查待处理任务
        Task->>Redis: LRANGE pending:{agentID}
        alt 有任务
            Redis-->>Task: 任务列表
            Task-->>Poll: ✅ 有任务！
            Poll-->>Agent: 🎉 Early Return (任务)
        else 无任务
            Task->>Redis: SUBSCRIBE events:task:submitted
            Note over Task,Redis: 阻塞等待 Pub/Sub...
        end
    end

    Note over Poll: 任一 Handler 有变更 → 立即取消其他 → 返回
```

---

### 8. 🔗 MetricGen Connector — Traces 变 Metrics

> **角色**：在 Pipeline 中消费 Traces，自动生成 RED Metrics (Rate/Error/Duration) 和 Service Graph，供 Prometheus API 查询。

```mermaid
graph LR
    Traces["📊 Traces<br/>(Pipeline)"]
    MetricGen["🔗 MetricGen<br/>Connector"]
    RED["📈 RED 指标<br/>(请求数/错误率/延迟)"]
    ServiceGraph["🕸️ Service Graph<br/>(服务依赖拓扑)"]
    Flusher["⏰ 定时 Flusher<br/>(15s 默认)"]
    Metrics["📉 Metrics<br/>(Pipeline)"]

    Traces --> MetricGen
    MetricGen --> RED
    MetricGen --> ServiceGraph
    RED --> Flusher
    ServiceGraph --> Flusher
    Flusher --> Metrics
```

| 特性 | 说明 |
|------|------|
| **RED 指标** | Rate (count)、Error (status_code≥400)、Duration (histogram) |
| **Service Graph** | 服务调用拓扑 + latency histogram + message size histogram |
| **Cardinality Limit** | 默认 2000，可配置降维 |
| **可配置维度** | `http.method`, `http.status_code`, `http.route`, `rpc.method`, `peer.service` 等 |
| **Histogram Buckets** | RED: 2ms~10s 自定义；Service Graph: 5ms~10s 默认 |

---

### 9. 🔐 TokenAuth Processor — Pipeline 里的"安检员"

> **角色**：在 OTel Pipeline 中验证数据合法性。Token 不对？整批数据直接扔掉。

```mermaid
graph LR
    Receiver["🚪 Receiver<br/>(注入 Token 到<br/>Resource Attributes)"]
    TokenAuth["🔐 TokenAuth<br/>Processor"]
    Cache["💾 TTL 缓存<br/>valid: 5min<br/>invalid: 30s"]
    CP["🧠 ControlPlane<br/>ValidateToken()"]
    Next["✅ 下一个 Processor<br/>/ Exporter"]
    Drop["❌ 丢弃"]

    Receiver --> TokenAuth
    TokenAuth -->|"1. 提取 Token"| TokenAuth
    TokenAuth -->|"2. 查缓存"| Cache
    Cache -->|"缓存未命中"| CP
    CP -->|"验证结果"| Cache
    TokenAuth -->|"✅ 合法 + 移除 Token 属性"| Next
    TokenAuth -->|"❌ 非法"| Drop
```

---

### 10. 🔄 TaskEngine — 统一任务引擎

> **角色**：统一的任务调度引擎，替代旧的 `controlplane/taskmanager` 和 `lifecycle/coordinator`，提供统一模型、状态机、队列、存储和路由。

```mermaid
graph TB
    subgraph TaskEngine["🔄 TaskEngine"]
        StateMachine["状态机<br/>Pending→Running→Success/Failed/Timeout/Skipped/Cancelled"]
        Router["路由器<br/>Direct / Capability / Broadcast"]
        Store["存储层<br/>memory / Redis"]
        CircuitBreaker["熔断器<br/>失败重试 + 退避"]
    end

    Producers["📤 Producers<br/>(Admin / MCP / Lifecycle)"]
    Consumers["📥 Consumers<br/>(Collector Nodes / Java Agents)"]

    Producers -->|"Submit / Cancel"| TaskEngine
    TaskEngine -->|"Claim / Report"| Consumers
    StateMachine --> Store
    Store --> Router
```

**核心设计：**

| 特性 | 说明 |
|------|------|
| **统一模型** | 单一 `Task` 类型取代多个旧模型，含 Payload、Routing、Retry 等 |
| **三种路由策略** | Direct（指定节点）、Capability（按能力匹配）、Broadcast（任意消费者） |
| **多后端存储** | Memory（开发） / Redis（生产） |
| **事件订阅** | Pub/Sub 实时通知 + 轮询回退 |
| **熔断器** | 自动重试 + 指数退避 |
| **消费者描述符** | Role + Capability 驱动队列匹配 |

**已定义 Task 类型：**

| 域 | TaskType | 说明 |
|------|----------|------|
| Lifecycle | `lifecycle:purge_index` | 删除过期索引 |
| Arthas | `arthas:attach` / `arthas:detach` / `arthas:exec_sync` | Arthas 诊断 |
| Arthas Session | `arthas:session_open` / `arthas:session_exec` / `arthas:session_pull` / `arthas:session_close` | AI 会话 |

---

### 11. 🔌 ObservabilityStorage Exporter — Pipeline 到存储的"桥梁"

> **角色**：OTel Exporter 接口实现，将 Pipeline 中的 Traces/Metrics/Logs 批量写入 ObsStorage Extension，最终持久化到 ES/PostgreSQL。

```mermaid
graph LR
    Pipeline["⚙️ Pipeline<br/>(Traces/Metrics/Logs)"]
    Exporter["🔌 ObsStorage<br/>Exporter"]
    Extension["📊 ObsStorage<br/>Extension"]
    ES[("Elasticsearch")]
    PG[("PostgreSQL")]

    Pipeline -->|"ConsumeTraces/Metrics/Logs"| Exporter
    Exporter -->|"WriteTraces/Metrics/Logs"| Extension
    Extension --> ES
    Extension --> PG
```

---

## 🔄 Java Agent 完整生命周期

> 一个 Java Agent 从出生到干活，经历了什么？

```mermaid
stateDiagram-v2
    [*] --> Registering: Agent 启动

    state "1️⃣ 注册" as Registering {
        [*] --> StatusReport: POST /v1/control/status
        StatusReport --> Registered: RegisterOrHeartbeat (Upsert)
    }

    Registered --> Polling: 开始轮询

    state "2️⃣ 轮询循环" as Polling {
        [*] --> UnifiedPoll: POST /v1/control/poll
        UnifiedPoll --> CheckConfig: 并行检查配置
        UnifiedPoll --> CheckTask: 并行检查任务

        state configResult <<choice>>
        CheckConfig --> configResult
        configResult --> ApplyConfig: 有更新
        configResult --> WaitConfig: 无更新 (阻塞)
        ApplyConfig --> UnifiedPoll

        state taskResult <<choice>>
        CheckTask --> taskResult
        taskResult --> ExecuteTask: 有任务
        taskResult --> WaitTask: 无更新 (阻塞 Pub/Sub)
        ExecuteTask --> ReportResult
    }

    state "3️⃣ 执行任务" as ExecuteTask {
        [*] --> Claimed: Claim-on-Dispatch (RUNNING)
        Claimed --> Working: 执行中
        Working --> Success: 成功
        Working --> Failed: 失败
        Working --> Timeout: 超时 (StaleTaskReaper)
    }

    state "4️⃣ 上报结果" as ReportResult {
        [*] --> SmallResult: POST /v1/control/tasks/result
        [*] --> LargeResult: POST /v1/control/upload-chunk
        LargeResult --> ChunkMerge: 分片合并
        ChunkMerge --> BlobStore: 持久化到 BlobStore
        BlobStore --> Notify: 通知分析服务
    }

    ReportResult --> Polling: 继续轮询

    state "5️⃣ 遥测数据" as Telemetry {
        [*] --> OTLP: POST /v1/traces|metrics|logs
        OTLP --> TokenValidation: Token 验证
        TokenValidation --> Export: 导出/存储到后端
    }

    Registered --> Telemetry: 持续上报
```

---

## 📂 项目结构

```
custom-opentelemetry-collector/
│
├── 🚀 cmd/customcol/              # 启动入口
│   ├── main.go                    #   OTel Collector 标准启动流程
│   └── components.go              #   注册所有自定义 + 官方组件
│
├── ⚙️ config/template/            # 配置模板
│   └── config.yaml                #   完整的 Collector 配置示例 (600+ 行)
│
├── 🧠 controlplane/               # 控制面领域层
│   ├── model/                     #   统一领域模型
│   └── conv/probeconv/            #   Proto ↔ Model 转换器
│
├── 🆔 identity/                   # 节点身份解析
│
├── 📡 proto/controlplane/v1/      # Protobuf 定义 (Agent ↔ Collector 通信协议)
│
├── 🔌 extension/                  # ★ 核心扩展模块 (200+ 文件)
│   ├── storageext/                #   📦 存储抽象层 (Redis/Nacos/BlobStore)
│   ├── observabilitystorageext/   #   📊 可观测数据存储扩展 (ES/PG 读写)
│   │   ├── storedmodel/           #     统一存储模型 + Key Sanitization
│   │   ├── lifecycle/             #     索引生命周期 (Purge/Archive/分布式调度)
│   │   └── provider/              #     多后端 Provider 接口
│   ├── observabilitystorageexporter/ # 🔌 Pipeline → Storage 导出器
│   ├── controlplaneext/           #   🧠 控制面核心
│   │   ├── agentregistry/         #     Agent 注册中心 (memory/redis)
│   │   ├── taskmanager/           #     任务管理
│   │   ├── configmanager/         #     配置管理 (memory/nacos/on_demand)
│   │   ├── tokenmanager/          #     Token & 应用管理
│   │   ├── chunk_manager/         #     分片上传
│   │   ├── artifact_manager/      #     产物管理
│   │   └── notification/          #     分析通知
│   ├── arthastunnelext/           #   🔧 Arthas WebSocket Tunnel
│   ├── adminext/                  #   🖥️ Admin API + WebUI
│   │   └── logql/                 #     LogQL Parser / Evaluator / Pipeline
│   └── mcpext/                    #   🤖 MCP Server (AI 接口)
│
├── 📥 receiver/
│   └── agentgatewayreceiver/      #   🚪 统一网关 (OTLP + 控制面 + Arthas WS)
│       └── longpoll/              #     ⏳ 长轮询 (Config + Task)
│
├── 🔗 connector/
│   └── metricgenconnector/        #   🧩 Traces → RED Metrics + Service Graph
│
├── 🔄 taskengine/                 #   🔧 统一任务引擎
│   ├── engine.go                  #     Engine 接口 + 状态机
│   ├── model.go                   #     统一 Task/TaskResult 模型
│   ├── router.go                  #     路由策略 (Direct/Capability/Broadcast)
│   ├── store.go                   #     存储接口
│   ├── store_memory.go            #     Memory 存储实现
│   ├── store_redis.go             #     Redis 存储实现
│   ├── state_machine.go           #     状态机
│   ├── circuit_breaker.go         #     熔断器
│   └── node/                      #     节点抽象 (Role/Capability/Registry)
│
├── 🔐 processor/
│   └── tokenauthprocessor/        #   Token 认证 + TTL 缓存
│
├── prototype/                     #   原型代码
├── k8s-manifests/                 #   Kubernetes 部署清单
└── 📚 docs/                       # 设计文档 (40+ 篇，按日期归档)
```

---

## 🔌 端口一览

| 端口 | 协议 | 用途 | 面向 |
|------|------|------|------|
| `4317` | gRPC | OTLP + ControlPlane gRPC | ☕ Java Agent |
| `4318` | HTTP/WS | OTLP + 控制面 REST + Arthas WS | ☕ Java Agent / 🔧 Arthas |
| `8088` | HTTP/WS | Admin API + WebUI + Grafana API (Loki/Tempo/Prometheus) | 🧑‍💻 运维人员 / 📊 Grafana |
| `8686` | HTTP | MCP Server (Streamable HTTP, JSON-RPC 2.0) | 🤖 AI Agent |
| `6831` | UDP | Jaeger Agent 兼容 | Legacy 客户端 |
| `8888` | HTTP | Prometheus 自身 Metrics | 📊 监控系统 |
| `55679` | HTTP | zPages 调试页面 | 🛠️ 开发者 |

---

## 🚀 快速开始

### 构建

```bash
# 编译（静态链接，适合容器部署）
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/custom-otlp-collector ./cmd/customcol

# 或使用 Makefile
make build
```

### 运行

```bash
# 使用配置模板启动
./bin/custom-otlp-collector --config config/template/config.yaml
```

### Docker Compose

```bash
# 一键启动（含 Redis / Elasticsearch 等依赖）
docker compose up -d
```

### K8s 部署

```bash
# 通过 Makefile 一键部署
make cicd-deploy
```

### Grafana 数据源配置

在 Grafana 中添加三个数据源，全部指向 Collector Admin 端口 `http://<collector-host>:8088`：

| 数据源类型 | URL | 说明 |
|-----------|-----|------|
| Loki | `http://<collector-host>:8088/loki` | 日志查询 |
| Tempo | `http://<collector-host>:8088` | Trace 查询 |
| Prometheus | `http://<collector-host>:8088` | 指标查询 |

---

## 🛠️ 技术栈

| 组件 | 技术选型 |
|------|---------|
| 语言 | Go 1.25 |
| 核心框架 | OpenTelemetry Collector v0.120.0 |
| gRPC | google.golang.org/grpc v1.74 |
| HTTP Router | go-chi/chi/v5 v5.2 |
| WebSocket | gorilla/websocket v1.5 |
| 缓存/存储 | go-redis/v9 v9.17 |
| 服务发现/配置 | nacos-sdk-go/v2 v2.3 |
| 可观测存储 | Elasticsearch / PostgreSQL |
| LogQL 解析 | 自研递归下降 Parser + participle/v2 |
| InfluxQL 解析 | influxdata/influxql (用于 PromQL 兼容) |
| AI 协议 | MCP (mark3labs/mcp-go v0.45) |
| 对象存储 | 腾讯云 COS |
| 前端 | Alpine.js + Tailwind CSS (嵌入式 SPA) |
| 容器化 | Docker + Kubernetes |

---

## 🎯 设计亮点

### 1. 🎭 多后端策略 — 开发生产无缝切换

几乎每个核心组件都支持 **memory**（开发/测试）和 **redis**（生产）两种存储后端，零配置起步，一行改配置切生产：

```yaml
# 开发环境：轻量起步，零外部依赖
agent_registry:
  storage_mode: memory

# 生产环境：分布式、高可用
agent_registry:
  storage_mode: redis
```

### 2. 🏥 Claim-on-Dispatch — 任务不会被"抢"

任务分发采用"认领即锁定"模式：Agent 拿到任务的瞬间就标记为 RUNNING，配合 `StaleTaskReaper` 清理卡死的任务，杜绝重复下发。

### 3. 🚪 统一网关 — 一个 Receiver 统治它们

`AgentGatewayReceiver` 一个端口同时搞定 OTLP 数据上报 + 控制面 API + Arthas WebSocket，Java Agent 只需要记一个地址。

### 4. ⏳ 统一长轮询 — 一次请求等两样

UnifiedPoll 并行等待配置变更和新任务，任一有更新就 Early Return，既省带宽又降延迟。

### 5. 🤖 AI-Native 设计 — 不是"锦上添花"，是"原生能力"

MCP Extension 对 Arthas 输出做结构化解析，AI 能真正"理解"诊断结果，而不是面对一堆文本"一脸懵"。

### 6. 📊 All-in-One 可观测后端 — 告别多系统跳转

一个 Collector 同时提供 Loki + Tempo + Prometheus 兼容 API，Grafana 三个数据源全部指向同一个 `:8088` 端口，Traces/Metrics/Logs 无缝关联。

### 7. 🧩 自研查询引擎 — LogQL + TraceQL 全覆盖

从零构建的 LogQL Parser（递归下降）和 TraceQL Parser，不依赖 Loki/Tempo 二进制，轻量级内嵌在 Collector 进程中。

### 8. 🔄 ES 索引自动管理 — 不再半夜被 mapping 错误炸醒

Template Reconciler 自动检测并修复索引模板漂移；Key Sanitization 自动处理 dotted key 导致的 mapping 冲突。

---

## 🗺️ Roadmap

- [x] ✅ Phase 1 — 基础 MCP Tools (list_agents / agent_info / arthas_status)
- [x] ✅ Phase 2 — Arthas 诊断工具 (trace / watch / jad / thread / stack / sc)
- [x] ✅ Loki 兼容 API — LogQL Parser + Log Query + Metric Query + OR 表达式
- [x] ✅ Tempo 兼容 API — TraceQL Parser + Structural Query + OR 表达式
- [x] ✅ Prometheus 兼容 API — Instant/Range Query + Labels
- [x] ✅ MetricGen Connector — RED Metrics + Service Graph
- [x] ✅ TaskEngine — 统一任务调度引擎
- [x] ✅ ES 索引生命周期 — 自动 Purge + 分布式调度
- [x] ✅ ES Key Sanitization — Dotted key 防冲突
- [ ] 🚧 ES 索引模板 Reconciler — 自动检测 + 修复模板漂移
- [ ] 🚧 Phase 3 — 性能剖析工具 + 异步任务进度通知 + MCP Resources/Prompts
- [ ] 📋 Multi-tenancy 多租户架构
- [ ] 📋 WebUI 持续优化

---

## 📚 相关文档

| 文档 | 说明 |
|------|------|
| [Loki Metric Query 支持](docs/loki-metric-query-support.md) | LogQL Metric 查询完整实现记录 |
| [LogQL OR 表达式设计](docs/2026-07-23/logql-or-expression-design.md) | OR-branching 解析与合并方案 |
| [ES Attribute Key Sanitization 设计](docs/2026-07-23/es-attribute-key-sanitization-design.md) | Dotted key 导致的 mapping 冲突修复 |
| [ES 索引模板 Reconciler 设计](docs/2026-07-23/es-index-template-reconciler-design.md) | 自动校验与修复索引模板 |
| [TaskEngine Redis 优化实现](docs/2026-07-15/implementation-plan-taskengine-redis-optimization.md) | 统一任务引擎 Redis 存储优化 |
| [TaskEngine 最优架构设计](docs/2026-07-14/design-taskengine-redis-optimal-architecture.md) | TaskEngine 架构方案评审 |
| [Multi-tenancy 多租户设计](docs/2026-07-15/multi-tenancy-architecture-design.md) | 多租户隔离架构设计 |
| [Metric Generator 设计](docs/2026-07-14/metric-generator-design.md) | RED + Service Graph Connector 设计 |
| [TraceQL Engine 设计](docs/2026-07-13/traceql-engine-design.md) | Tempo 兼容查询引擎设计 |
| [Prometheus API 设计](docs/2026-07-10/prometheus-api-design.md) | PromQL 兼容 API 设计 |
| [Tempo API 设计](docs/2026-07-10/tempo-api-design.md) | Tempo API 兼容设计 |
| [MCP Extension 设计](docs/mcp-extension-design.md) | AI 诊断能力的完整设计方案 |
| [数据模型重构](docs/refeactor.md) | 从 Legacy JSON 到统一 Model 的重构之路 |
| [任务重复下发修复](docs/fix-task-duplicate-dispatch.md) | Claim-on-Dispatch + StaleTaskReaper |

---

## 📜 License

Internal Project — 仅限内部使用。

---

<p align="center">
  <em>"能收数据的 Collector 很多，能远程诊断 + AI 问诊 + 自建可观测后端的，这是独一份。"</em>
  <br/>
  <strong>Built with ❤️ and way too much ☕</strong>
</p>

# Phase 1 & 2: 统一观测数据存储层 — 实施记录

> **日期**: 2026-05-29  
> **状态**: ✅ Phase 1 完成 | 🚧 Phase 2 进行中（Reader 层已完成，待 adminext 对接）  

---

## 一、需求概述

实现统一观测数据存储层（Unified Observability Storage Layer）的 Phase 1：

- 定义核心接口（Provider / Writer / Reader / Admin）
- 实现 Elasticsearch Provider 的 TraceWriter / MetricWriter / LogWriter
- 支持批量写入（batch_size + flush_interval）
- 自动创建 ES Index Template 和 ILM Policy
- 注册为 OTel Extension，可在 Collector 配置中启用
- **实现 Exporter 桥接 Pipeline → Extension 完成数据链路闭环**

---

## 二、实施进展

### ✅ 已完成

| # | 任务 | 状态 |
|---|------|------|
| 1 | 创建 `observabilitystorageext` 包骨架 | ✅ |
| 2 | 定义 Provider / Writer / Reader / Admin 接口 (`provider.go`) | ✅ |
| 3 | 定义公共类型 Query/Result/TimeRange 等 (`types.go`) | ✅ |
| 4 | 实现 Config + Validate + ApplyDefaults (`config.go`) | ✅ |
| 5 | 实现 OTel Extension Factory (`factory.go`) | ✅ |
| 6 | 实现 Extension 主文件 + Provider 生命周期 (`extension.go`) | ✅ |
| 7 | 实现 ES Provider 连接管理 (`provider/elasticsearch/client.go`) | ✅ |
| 8 | 实现 ES Index Template + ILM Policy 初始化 (`admin.go`) | ✅ |
| 9 | 实现批量缓冲器 (`bulk_buffer.go`) | ✅ |
| 10 | 实现 ES TraceWriter: ptrace.Traces → ES Bulk (`trace_writer.go`) | ✅ |
| 11 | 实现 ES MetricWriter: pmetric.Metrics → ES Bulk (`metric_writer.go`) | ✅ |
| 12 | 实现 ES LogWriter: plog.Logs → ES Bulk (`log_writer.go`) | ✅ |
| 13 | pdata → ES 文档模型转换 (`model.go`) | ✅ |
| 14 | 注册 Extension 到 `cmd/customcol/components.go` | ✅ |
| 15 | 实现 `observabilitystorageexporter`（Pipeline → Extension 桥接） | ✅ |
| 16 | 注册 Exporter 到 `cmd/customcol/components.go` | ✅ |
| 17 | 更新 config 配置（template + build），完成 Pipeline 串联 | ✅ |
| 18 | 全项目 `go build ./...` 编译通过 | ✅ |
| 19 | 单元测试：bulkBuffer（批量触发/重试/并发/定时flush） | ✅ |
| 20 | 单元测试：TraceWriter（Span→Doc/Events/Links/EndToEnd） | ✅ |
| 21 | 单元测试：MetricWriter（Gauge/Sum/Histogram/Summary） | ✅ |
| 22 | 单元测试：LogWriter（LogRecord→Doc/TraceContext/EmptyBody） | ✅ |
| 23 | 单元测试：model.go（attributesToMap/valueToAny/getServiceName） | ✅ |
| 24 | Bug 修复：SpanID/TraceID 全零时 String() 返回空字符串 | ✅ |
| 25 | 集成测试重构：脱敏化、环境变量驱动、通用 helper | ✅ |
| 26 | 新增 Client.Count/RefreshIndex/ListIndices/DeleteIndicesByPattern | ✅ |
| 27 | 集成测试：Retention Purge + PurgeByApp（全信号类型） | ✅ |
| 28 | ES Reader 本地类型定义 (`types_reader.go`)，解决循环导入 | ✅ |
| 29 | ES TraceReader: SearchTraces/GetTrace/GetServices/GetOperations/GetDependencies | ✅ |
| 30 | ES LogReader: SearchLogs/GetLogContext/ListLogFields/GetLogStats | ✅ |
| 31 | ES MetricReader: Query/QueryRange/ListMetricNames/ListLabelNames/ListLabelValues | ✅ |
| 32 | ES Search API 基础设施 (`client_search.go`: Search/MultiSearch) | ✅ |
| 33 | Reader Adapter 层: 类型适配 elasticsearch ↔ observabilitystorageext 公共接口 | ✅ |
| 34 | Provider.Start 初始化 Reader + Extension 暴露 GetTraceReader/GetMetricReader/GetLogReader | ✅ |
| 35 | Reader 集成测试：写入→查询端到端验证（Trace/Log/Metric 全通过） | ✅ |

### 📋 待完成（后续迭代）

| # | 任务 | 阶段 |
|---|------|------|
| 1 | adminext API Handler 对接新 Reader 接口 | Phase 2 |
| 2 | Log 查询 API: /api/logs/search, /api/logs/:id/context, /api/logs/stats | Phase 2 |
| 3 | Metric 查询 API: /api/metrics/query, /api/metrics/names, /api/metrics/labels | Phase 2 |
| 4 | StorageAdmin 完整实现 (GetStatus/GetRetention/SetRetention/GetDiskUsage) | Phase 2 |
| 5 | RetentionEnforcer 分布式协调清理机制 | Phase 2 |

---

## 三、目录结构

```
extension/observabilitystorageext/          # 统一存储 Extension
├── provider.go                             # Provider 统一门面接口定义
├── types.go                                # 公共类型 (Query/Result/TimeRange/Retention)
├── config.go                               # 配置结构 + 校验 + 默认值
├── factory.go                              # OTel Extension Factory
├── extension.go                            # Extension 主文件 (持有 ES Provider, 暴露 Write/Read/HealthCheck)
├── reader_adapter.go                       # Reader 适配器 (elasticsearch 类型 → 公共接口类型)
└── provider/
    └── elasticsearch/
        ├── config.go                       # ES Provider 内部配置 (避免循环导入)
        ├── provider.go                     # ES Provider 主入口 (Writer + Reader 生命周期)
        ├── client.go                       # ES HTTP Client 核心 (Ping/Health/doRequest)
        ├── client_bulk.go                  # 批量写入 (BulkIndex)
        ├── client_admin.go                 # 索引管理 (Template/ILM/Delete/Count/Refresh)
        ├── client_search.go                # 搜索 API (Search/MultiSearch)
        ├── admin.go                        # Schema 初始化 + ILM + 集群状态
        ├── bulk_buffer.go                  # 批量写入缓冲器 (batch_size + flush_interval + retry)
        ├── trace_writer.go                 # ptrace.Traces → ES 文档 + Bulk
        ├── metric_writer.go                # pmetric.Metrics → ES 文档 + Bulk
        ├── log_writer.go                   # plog.Logs → ES 文档 + Bulk
        ├── trace_reader.go                 # Trace 查询 (SearchTraces/GetTrace/GetServices/GetDependencies)
        ├── metric_reader.go                # Metric 查询 (Query/QueryRange/ListMetricNames/Labels)
        ├── log_reader.go                   # Log 查询 (SearchLogs/GetLogContext/ListLogFields/GetLogStats)
        ├── types_reader.go                 # Reader 本地类型 (避免循环导入父包)
        └── model.go                        # pdata ↔ ES 文档转换工具

extension/observabilitystorageexporter/     # 统一存储 Exporter (Pipeline 桥接)
├── config.go                               # Exporter 配置 (引用 Extension ID)
├── factory.go                              # OTel Exporter Factory (Traces/Metrics/Logs)
└── exporter.go                             # 桥接实现 (Start解析Extension, Consume委托Write, Shutdown刷新)
```

---

## 四、数据流

```
Agent → OTLP Receiver → tokenauth Processor → batch Processor
                                                     ↓
                              observability_storage Exporter
                                                     ↓
                              observability_storage Extension
                                                     ↓
                                ES Provider (Bulk Buffer → ES Cluster)
```

Pipeline 配置：
```yaml
pipelines:
  traces:
    receivers: [agent_gateway, jaeger]
    processors: [tokenauth, memory_limiter, batch]
    exporters: [otlphttp/jaeger, spanmetrics, observability_storage]
  metrics:
    receivers: [agent_gateway]
    processors: [tokenauth, memory_limiter, batch]
    exporters: [prometheusremotewrite, observability_storage]
  logs:
    receivers: [agent_gateway]
    processors: [tokenauth, memory_limiter, batch]
    exporters: [observability_storage]
```

---

## 五、配置示例

### Extension 配置
```yaml
extensions:
  observability_storage:
    type: "elasticsearch"
    elasticsearch:
      addresses:
        - "http://es-node1:9200"
      username: ""
      password: ""
      batch_size: 5000
      flush_interval: 3s
      max_retries: 3
      traces:
        index_prefix: "otel-traces"
        index_date_format: "2006.01.02"
        shards: 3
        replicas: 1
        refresh_interval: "5s"
      metrics:
        index_prefix: "otel-metrics"
        index_date_format: "2006.01.02"
        shards: 2
        replicas: 1
        refresh_interval: "10s"
      logs:
        index_prefix: "otel-logs"
        index_date_format: "2006.01.02"
        shards: 3
        replicas: 1
        refresh_interval: "5s"
    retention:
      default_trace: 168h      # 7 days
      default_metric: 720h     # 30 days
      default_log: 336h        # 14 days
      max_trace: 720h          # 30 days max
      max_metric: 2160h        # 90 days max
      max_log: 720h            # 30 days max
```

### Exporter 配置
```yaml
exporters:
  observability_storage:
    storage_extension: observability_storage
```

---

## 六、设计决策

### 6.1 避免循环导入

ES Provider 定义了自己的 `Config` 和 `IndexConfig` 类型（在 `provider/elasticsearch/config.go`），
而非导入父包 `observabilitystorageext`。父包的 `extension.go` 负责在创建 provider 时做 config 转换。

### 6.2 接口与实现分离

`provider.go` 定义了理想化的 `Provider` 接口（包含 Reader），但 Phase 1 中 ES Provider 不直接实现该接口。
`ObservabilityStorage` extension 直接暴露具体方法（WriteTraces/WriteMetrics/WriteLogs），
Phase 2 添加 Reader 后再由 extension 适配完整的 `Provider` 接口。

### 6.3 批量写入策略

- 每个信号类型有独立的 `bulkBuffer` 实例
- 达到 `batch_size` 时立即 flush
- 每 `flush_interval` 定时 flush（即使未达到 batch_size）
- 失败时指数退避重试（最多 `max_retries` 次）

### 6.4 Exporter → Extension 桥接模式

- Exporter 不直接持有 ES 连接，而是通过 `component.Host.GetExtensions()` 获取 Extension 引用
- 这样做的好处：
  - Extension 管理所有连接生命周期，Exporter 是轻量级委托
  - 多个 Pipeline 的 Exporter 实例共享同一个 Extension（同一个 ES Provider）
  - Extension 可被其他组件（如 adminext）复用，做查询/管理操作
- Shutdown 时 Exporter 负责 Flush 所有缓冲，确保数据不丢失

### 6.5 Retention 语义

- YAML 配置中的 `retention` 是 **平台默认值 + 最大约束**
- `default_*`：新建 App 没有显式设置时的回退默认值
- `max_*`：平台强制上限，任何 App 设置不得超过此值
- 已有 App 的 Retention 设置存储在 Redis/DB 中，优先级高于 YAML 默认值

### 6.6 Reader 适配器模式（Phase 2）

- **问题**：`elasticsearch` 包不能导入父包 `observabilitystorageext`（循环导入）
- **解决方案**：
  - `elasticsearch` 包定义自己的 Reader 类型（`types_reader.go`），镜像公共接口类型
  - `observabilitystorageext/reader_adapter.go` 实现公共 `TraceReader`/`MetricReader`/`LogReader` 接口
  - 适配器内部委托给 ES Reader 实例，做类型转换
  - 这与 Phase 1 中 Config 避免循环导入的模式完全一致（设计决策 6.1）
- **好处**：
  - 内部包类型可以自由演进，不影响公共 API
  - 公共接口保持稳定，消费者（如 adminext）只依赖接口层
  - 未来可轻松添加其他 Provider（如 PostgreSQL）实现相同接口

---

## 七、遗留问题

1. **~~Reader 未实现~~**：✅ Phase 2 已完成 TraceReader / MetricReader / LogReader。
2. **adminext API 对接**：需要将现有 adminext Handler 重构为使用新的 Reader 接口，替换现有直接 ES 查询。
3. **Retention Enforcer**：架构设计文档中定义的分布式协调清理机制，待实现。
4. **~~Reader 集成测试~~**：✅ 写入→查询端到端验证全部通过（16/16 子测试 PASS）。
5. **端到端验证**：需要部署到测试环境，验证 Agent → Pipeline → ES → Reader 全链路。

# 统一观测数据存储层架构设计

> **版本**: v1.1  
> **日期**: 2026-05-29（更新: 2026-06-01）  
> **状态**: ES-First 实施中 — 核心模块已完成，进入端到端链路验证阶段  
> **策略**: 先通过 ES 验证标准化架构，PG/MongoDB 延后作为插件扩展（详见 `docs/2026-06-01/next-steps-es-first-pipeline.md`）  

---

## 一、背景与目标

### 1.1 现状问题

当前系统的观测数据（Trace / Metric / Log）存储与查询存在以下问题：

| 问题 | 详情 |
|------|------|
| **存储分散** | Trace → Jaeger (via otlphttp)，Metric → Prometheus (via remote write)，Log → 无 |
| **配置碎片化** | 写入配置在 pipeline exporter，查询配置在 adminext.observability，两者独立维护 |
| **查询能力弱** | 仅透传 raw query 到后端 API，无法做跨信号关联查询 |
| **Log 缺失** | 无 Log 写入/查询实现 |
| **无法自主部署** | 依赖外部 Jaeger + Prometheus 独立部署 |
| **扩展性差** | 新增后端需改多处（exporter yaml + adminext config + reader 实现） |

### 1.2 目标

构建统一的观测数据存储层（Unified Observability Storage Layer），实现：

1. **一个配置块**控制 Trace / Metric / Log 的写入、查询、管理
2. **一个接口标准**适配不同存储后端（ES / PostgreSQL / MongoDB）
3. **即插即用**——只需修改配置即可切换后端，无需改代码
4. **写入与查询一致**——同一个 Provider 同时管控写入和查询
5. **内置管理能力**——Schema 初始化、TTL、数据清理、健康检查

### 1.3 可用存储资源

| 存储 | 版本 | 最适合 |
|------|------|--------|
| **Elasticsearch** | 7.x / 8.x | Trace、Log（全文检索、聚合） |
| **PostgreSQL** | 14+ | Metric（时序聚合）、Admin 元数据 |
| **MongoDB** | 5.0+ | 文档型存储、时序集合（备选） |

---

## 二、架构设计

### 2.1 总体架构

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                        OTel Collector Pipeline                                 │
│                                                                               │
│  Receivers                Processors            Exporter (新)                  │
│  ┌───────────────┐       ┌──────────┐          ┌─────────────────────┐       │
│  │agent_gateway  │──────▶│tokenauth │─────────▶│  observability_     │       │
│  │(OTLP HTTP/gRPC)│      │batch     │          │  storage_exporter   │       │
│  │               │       │mem_limit │          │  (trace/metric/log) │       │
│  └───────────────┘       └──────────┘          └──────────┬──────────┘       │
│                                                            │                  │
└────────────────────────────────────────────────────────────┼──────────────────┘
                                                             │
                         ┌───────────────────────────────────┼───────────────┐
                         │        observabilitystorageext     │               │
                         │        (新 Extension)             │               │
                         │                                   ▼               │
                         │  ┌─────────────────────────────────────────────┐  │
                         │  │         StorageProvider (interface)           │  │
                         │  │  ───────────────────────────────────────────  │  │
                         │  │  TraceWriter / MetricWriter / LogWriter       │  │
                         │  │  TraceReader / MetricReader / LogReader       │  │
                         │  │  StorageAdmin                                 │  │
                         │  └────────┬────────────────┬────────────────┬──┘  │
                         │           │                │                │      │
                         │  ┌────────▼──┐   ┌────────▼──┐   ┌────────▼──┐   │
                         │  │    ES      │   │ PostgreSQL │   │  MongoDB  │   │
                         │  │  Provider  │   │  Provider  │   │  Provider │   │
                         │  └───────────┘   └───────────┘   └───────────┘   │
                         │                                                   │
                         └───────────────────────────────────────────────────┘
                                                             ▲
                         ┌───────────────────────────────────┼───────────────┐
                         │           adminext                 │               │
                         │                                   │               │
                         │  API Handlers ─── Provider.Reader()               │
                         │  /api/v2/observability/traces/*                    │
                         │  /api/v2/observability/metrics/*                   │
                         │  /api/v2/observability/logs/*      (新)            │
                         │  /api/v2/observability/admin/*     (新)            │
                         │                                                   │
                         └───────────────────────────────────────────────────┘
```

### 2.2 组件职责

| 组件 | 职责 | 类型 |
|------|------|------|
| `observabilitystorageext` | 持有 StorageProvider 单例，管理连接池和生命周期 | OTel Extension |
| `observabilitystorageexporter` | 实现 `exporter.Traces/Metrics/Logs`，桥接 Pipeline → Provider.Writer | OTel Exporter |
| `StorageProvider` | 统一门面接口，封装 Reader/Writer/Admin | Interface |
| `ElasticsearchProvider` | ES 后端实现 | Provider impl |
| `PostgreSQLProvider` | PG 后端实现 | Provider impl |
| `MongoDBProvider` | MongoDB 后端实现 | Provider impl |
| `HybridProvider` | 组合模式：不同信号路由到不同后端 | Provider impl |

### 2.3 核心接口定义

```go
package observabilitystorage

import (
    "context"
    "time"
    
    "go.opentelemetry.io/collector/pdata/plog"
    "go.opentelemetry.io/collector/pdata/pmetric"
    "go.opentelemetry.io/collector/pdata/ptrace"
)

// ═══════════════════════════════════════════════════
// Provider — 统一门面
// ═══════════════════════════════════════════════════

type Provider interface {
    // 元信息
    Name() string
    Capabilities() Capabilities

    // Writer 工厂
    TraceWriter() TraceWriter
    MetricWriter() MetricWriter
    LogWriter() LogWriter

    // Reader 工厂
    TraceReader() TraceReader
    MetricReader() MetricReader
    LogReader() LogReader

    // 管理
    Admin() StorageAdmin

    // 生命周期
    Start(ctx context.Context) error
    Shutdown(ctx context.Context) error
    HealthCheck(ctx context.Context) (*HealthStatus, error)
}

type Capabilities struct {
    Trace  SignalCapability
    Metric SignalCapability
    Log    SignalCapability
    Admin  bool
}

type SignalCapability struct {
    Write bool
    Read  bool
}

// ═══════════════════════════════════════════════════
// Writer — 写入接口 (由 Exporter 调用)
// ═══════════════════════════════════════════════════

type TraceWriter interface {
    WriteTraces(ctx context.Context, td ptrace.Traces) error
    Flush(ctx context.Context) error
}

type MetricWriter interface {
    WriteMetrics(ctx context.Context, md pmetric.Metrics) error
    Flush(ctx context.Context) error
}

type LogWriter interface {
    WriteLogs(ctx context.Context, ld plog.Logs) error
    Flush(ctx context.Context) error
}

// ═══════════════════════════════════════════════════
// Reader — 查询接口 (由 adminext API Handler 调用)
// ═══════════════════════════════════════════════════

type TraceReader interface {
    SearchTraces(ctx context.Context, query TraceQuery) (*TraceSearchResult, error)
    GetTrace(ctx context.Context, traceID string) (*Trace, error)
    GetServices(ctx context.Context, timeRange TimeRange) ([]Service, error)
    GetOperations(ctx context.Context, service string, timeRange TimeRange) ([]Operation, error)
    GetDependencies(ctx context.Context, timeRange TimeRange) ([]Dependency, error)
}

type MetricReader interface {
    Query(ctx context.Context, query MetricQuery) (*MetricResult, error)
    QueryRange(ctx context.Context, query MetricRangeQuery) (*MetricRangeResult, error)
    ListMetricNames(ctx context.Context, timeRange TimeRange) ([]string, error)
    ListLabelNames(ctx context.Context, timeRange TimeRange) ([]string, error)
    ListLabelValues(ctx context.Context, label string, timeRange TimeRange) ([]string, error)
}

type LogReader interface {
    SearchLogs(ctx context.Context, query LogQuery) (*LogSearchResult, error)
    GetLogContext(ctx context.Context, logID string, lines int) (*LogContext, error)
    ListLogFields(ctx context.Context, timeRange TimeRange) ([]LogField, error)
    GetLogStats(ctx context.Context, query LogStatsQuery) (*LogStats, error)
}

// ═══════════════════════════════════════════════════
// Admin — 管理接口
// ═══════════════════════════════════════════════════

type StorageAdmin interface {
    GetStatus(ctx context.Context) (*StorageStatus, error)
    InitSchema(ctx context.Context) error
    GetRetention(ctx context.Context) (map[SignalType]RetentionPolicy, error)
    SetRetention(ctx context.Context, signal SignalType, policy RetentionPolicy) error
    Purge(ctx context.Context, signal SignalType, before time.Time) (*PurgeResult, error)
    GetDiskUsage(ctx context.Context) (*DiskUsage, error)
}
```

### 2.4 配置设计

```yaml
extensions:
  # 新的统一观测数据存储 Extension
  observability_storage:
    # Provider 类型: "elasticsearch", "postgresql", "mongodb", "hybrid"
    type: "elasticsearch"
    
    # === Elasticsearch Provider 配置 ===
    elasticsearch:
      addresses:
        - "http://es-node1:9200"
        - "http://es-node2:9200"
      username: ""
      password: ""
      
      # 通用参数
      batch_size: 5000
      flush_interval: 3s
      max_retries: 3
      
      # Trace 索引配置
      traces:
        index_prefix: "otel-traces"
        index_date_format: "2006.01.02"    # 按天分索引
        shards: 3
        replicas: 1
        retention: "7d"
        refresh_interval: "5s"
      
      # Metric 索引配置
      metrics:
        index_prefix: "otel-metrics"
        index_date_format: "2006.01.02"
        shards: 2
        replicas: 1
        retention: "30d"
        refresh_interval: "10s"
      
      # Log 索引配置
      logs:
        index_prefix: "otel-logs"
        index_date_format: "2006.01.02"
        shards: 3
        replicas: 1
        retention: "14d"
        refresh_interval: "5s"
    
    # === PostgreSQL Provider 配置 (备选) ===
    # postgresql:
    #   dsn: "postgres://user:pass@pghost:5432/otel?sslmode=disable"
    #   max_open_conns: 20
    #   max_idle_conns: 5
    #   conn_max_lifetime: "30m"
    #   auto_migrate: true
    #   traces:
    #     table_prefix: "otel_traces"
    #     partition_interval: "1d"
    #     retention: "7d"
    #   metrics:
    #     table_prefix: "otel_metrics"
    #     hypertable: true             # TimescaleDB
    #     chunk_interval: "1d"
    #     retention: "90d"
    #   logs:
    #     table_prefix: "otel_logs"
    #     partition_interval: "1d"
    #     retention: "14d"
    
    # === Hybrid Provider 配置 (混合模式) ===
    # type: "hybrid"
    # hybrid:
    #   trace: "elasticsearch"         # Trace → ES
    #   metric: "postgresql"           # Metric → PG (TimescaleDB)
    #   log: "elasticsearch"           # Log → ES
    #   admin: "postgresql"            # Admin → PG

exporters:
  # 新的统一存储 Exporter（替代 otlphttp/jaeger + prometheusremotewrite）
  observability_storage:
    # 引用 Extension
    storage_extension: observability_storage

service:
  extensions: [storage, controlplane, observability_storage, arthas_tunnel, admin, mcp]
  
  pipelines:
    traces:
      receivers: [agent_gateway, jaeger]
      processors: [tokenauth, memory_limiter, batch]
      exporters: [observability_storage]           # ← 替代 otlphttp/jaeger
    
    metrics:
      receivers: [agent_gateway, spanmetrics]
      processors: [tokenauth, memory_limiter, batch]
      exporters: [observability_storage]           # ← 替代 prometheusremotewrite
    
    logs:
      receivers: [agent_gateway]
      processors: [tokenauth, memory_limiter, batch]
      exporters: [observability_storage]           # ← 新增 Log pipeline
```

### 2.5 目录结构

```
extension/
├── observabilitystorageext/           # 统一存储 Extension (新建)
│   ├── extension.go                   # Extension 主文件，持有 Provider 单例
│   ├── config.go                      # 配置结构
│   ├── factory.go                     # OTel Extension factory
│   ├── provider.go                    # Provider interface 定义
│   ├── types.go                       # 公共类型 (Query/Result/TimeRange 等)
│   ├── provider/                      # Provider 实现
│   │   ├── registry.go               # Provider 注册表 (工厂模式)
│   │   ├── elasticsearch/            # ES Provider
│   │   │   ├── provider.go           # Provider 主入口
│   │   │   ├── config.go
│   │   │   ├── client.go             # ES 连接管理
│   │   │   ├── schema.go             # Index Template / Mapping
│   │   │   ├── trace_writer.go
│   │   │   ├── trace_reader.go
│   │   │   ├── metric_writer.go
│   │   │   ├── metric_reader.go
│   │   │   ├── log_writer.go
│   │   │   ├── log_reader.go
│   │   │   ├── admin.go
│   │   │   └── *_test.go
│   │   ├── postgresql/               # PG Provider
│   │   │   ├── provider.go
│   │   │   ├── config.go
│   │   │   ├── schema.go             # DDL / Migration
│   │   │   ├── trace_writer.go
│   │   │   ├── trace_reader.go
│   │   │   ├── metric_writer.go
│   │   │   ├── metric_reader.go
│   │   │   ├── log_writer.go
│   │   │   ├── log_reader.go
│   │   │   ├── admin.go
│   │   │   └── *_test.go
│   │   ├── mongodb/                  # MongoDB Provider (Phase 3)
│   │   │   └── ...
│   │   └── hybrid/                   # Hybrid Provider (组合模式)
│   │       ├── provider.go           # 路由不同信号到不同子 Provider
│   │       └── config.go
│   └── internal/                     # 内部公共工具
│       ├── batch.go                  # 批量写入缓冲器
│       ├── model.go                  # OTLP pdata → 存储模型转换
│       └── retry.go                  # 重试/熔断策略
│
├── observabilitystorageexporter/      # 统一存储 Exporter (新建)
│   ├── exporter.go                   # 实现 exporter.Traces/Metrics/Logs
│   ├── config.go
│   └── factory.go
│
├── adminext/
│   ├── observability/                # 重构: 使用 Provider 替代直连
│   │   ├── interfaces.go            # 保留接口定义（对齐新 Reader）
│   │   ├── handler.go               # 统一 Handler (调用 Provider.Reader)
│   │   └── ...
│   └── ...
```

---

## 三、Elasticsearch Provider 详细设计

### 3.1 Trace 数据模型 (ES Mapping)

```json
{
  "index_patterns": ["otel-traces-*"],
  "template": {
    "settings": {
      "number_of_shards": 3,
      "number_of_replicas": 1,
      "refresh_interval": "5s",
      "index.lifecycle.name": "otel-traces-policy"
    },
    "mappings": {
      "properties": {
        "trace_id":        { "type": "keyword" },
        "span_id":         { "type": "keyword" },
        "parent_span_id":  { "type": "keyword" },
        "operation_name":  { "type": "keyword" },
        "service_name":    { "type": "keyword" },
        "span_kind":       { "type": "keyword" },
        "status_code":     { "type": "keyword" },
        "status_message":  { "type": "text" },
        "start_time":      { "type": "date_nanos" },
        "end_time":        { "type": "date_nanos" },
        "duration_us":     { "type": "long" },
        "attributes":      { "type": "object", "dynamic": true },
        "resource": {
          "properties": {
            "service.name":      { "type": "keyword" },
            "service.namespace": { "type": "keyword" },
            "service.version":   { "type": "keyword" },
            "host.name":         { "type": "keyword" },
            "app_id":            { "type": "keyword" }
          }
        },
        "events": {
          "type": "nested",
          "properties": {
            "name":       { "type": "keyword" },
            "timestamp":  { "type": "date_nanos" },
            "attributes": { "type": "object", "dynamic": true }
          }
        },
        "links": {
          "type": "nested",
          "properties": {
            "trace_id": { "type": "keyword" },
            "span_id":  { "type": "keyword" }
          }
        }
      }
    }
  }
}
```

### 3.2 Metric 数据模型 (ES Data Stream)

```json
{
  "index_patterns": ["otel-metrics-*"],
  "data_stream": {},
  "template": {
    "settings": {
      "number_of_shards": 2,
      "number_of_replicas": 1,
      "index.lifecycle.name": "otel-metrics-policy"
    },
    "mappings": {
      "properties": {
        "@timestamp":     { "type": "date" },
        "metric_name":    { "type": "keyword" },
        "metric_type":    { "type": "keyword" },
        "value":          { "type": "double" },
        "service_name":   { "type": "keyword" },
        "app_id":         { "type": "keyword" },
        "labels":         { "type": "flattened" },
        "resource":       { "type": "flattened" },
        "histogram": {
          "properties": {
            "counts": { "type": "long" },
            "values": { "type": "double" }
          }
        }
      }
    }
  }
}
```

### 3.3 Log 数据模型 (ES Mapping)

```json
{
  "index_patterns": ["otel-logs-*"],
  "template": {
    "settings": {
      "number_of_shards": 3,
      "number_of_replicas": 1,
      "index.lifecycle.name": "otel-logs-policy"
    },
    "mappings": {
      "properties": {
        "timestamp":       { "type": "date_nanos" },
        "observed_time":   { "type": "date_nanos" },
        "trace_id":        { "type": "keyword" },
        "span_id":         { "type": "keyword" },
        "severity":        { "type": "keyword" },
        "severity_number": { "type": "integer" },
        "body":            { "type": "text", "analyzer": "standard" },
        "service_name":    { "type": "keyword" },
        "app_id":          { "type": "keyword" },
        "attributes":      { "type": "object", "dynamic": true },
        "resource":        { "type": "flattened" }
      }
    }
  }
}
```

### 3.4 ILM (Index Lifecycle Management) — 自动 TTL

```json
{
  "policy": {
    "phases": {
      "hot": {
        "actions": {
          "rollover": {
            "max_size": "30gb",
            "max_age": "1d"
          }
        }
      },
      "warm": {
        "min_age": "2d",
        "actions": {
          "shrink": { "number_of_shards": 1 },
          "forcemerge": { "max_num_segments": 1 }
        }
      },
      "delete": {
        "min_age": "7d",
        "actions": { "delete": {} }
      }
    }
  }
}
```

---

## 四、PostgreSQL Provider 详细设计

### 4.1 Schema (适合 Metric + Admin)

```sql
-- ==========================================
-- Metric 表 (推荐搭配 TimescaleDB)
-- ==========================================

CREATE TABLE otel_metrics (
    time           TIMESTAMPTZ      NOT NULL,
    metric_name    TEXT             NOT NULL,
    metric_type    TEXT             NOT NULL,  -- gauge/counter/histogram/summary
    value          DOUBLE PRECISION,
    service_name   TEXT             NOT NULL,
    app_id         TEXT,
    labels         JSONB            DEFAULT '{}',
    resource       JSONB            DEFAULT '{}',
    -- Histogram specific
    histogram_counts BIGINT[],
    histogram_bounds DOUBLE PRECISION[]
);

-- TimescaleDB 超级表 (如果安装了 TimescaleDB)
SELECT create_hypertable('otel_metrics', 'time', chunk_time_interval => INTERVAL '1 day');

-- 索引
CREATE INDEX idx_metrics_name_time ON otel_metrics (metric_name, time DESC);
CREATE INDEX idx_metrics_service_time ON otel_metrics (service_name, time DESC);
CREATE INDEX idx_metrics_labels ON otel_metrics USING GIN (labels);

-- 自动 Retention (TimescaleDB)
SELECT add_retention_policy('otel_metrics', INTERVAL '90 days');

-- ==========================================
-- Trace 表 (PG 方案)
-- ==========================================

CREATE TABLE otel_traces (
    trace_id       TEXT             NOT NULL,
    span_id        TEXT             NOT NULL,
    parent_span_id TEXT,
    operation_name TEXT             NOT NULL,
    service_name   TEXT             NOT NULL,
    span_kind      TEXT,
    status_code    SMALLINT         DEFAULT 0,
    start_time     TIMESTAMPTZ      NOT NULL,
    end_time       TIMESTAMPTZ      NOT NULL,
    duration_us    BIGINT           NOT NULL,
    attributes     JSONB            DEFAULT '{}',
    resource       JSONB            DEFAULT '{}',
    events         JSONB            DEFAULT '[]',
    links          JSONB            DEFAULT '[]',
    app_id         TEXT,
    PRIMARY KEY (trace_id, span_id)
) PARTITION BY RANGE (start_time);

-- 按天分区
CREATE TABLE otel_traces_default PARTITION OF otel_traces DEFAULT;

-- 索引
CREATE INDEX idx_traces_service_time ON otel_traces (service_name, start_time DESC);
CREATE INDEX idx_traces_trace_id ON otel_traces (trace_id);
CREATE INDEX idx_traces_duration ON otel_traces (duration_us DESC);
CREATE INDEX idx_traces_attributes ON otel_traces USING GIN (attributes);

-- ==========================================
-- Log 表
-- ==========================================

CREATE TABLE otel_logs (
    id             BIGSERIAL,
    timestamp      TIMESTAMPTZ      NOT NULL,
    trace_id       TEXT,
    span_id        TEXT,
    severity       TEXT,
    severity_number SMALLINT,
    body           TEXT,
    service_name   TEXT             NOT NULL,
    app_id         TEXT,
    attributes     JSONB            DEFAULT '{}',
    resource       JSONB            DEFAULT '{}'
) PARTITION BY RANGE (timestamp);

-- 全文索引
CREATE INDEX idx_logs_body_fts ON otel_logs USING GIN (to_tsvector('simple', body));
CREATE INDEX idx_logs_service_time ON otel_logs (service_name, timestamp DESC);
CREATE INDEX idx_logs_trace_id ON otel_logs (trace_id) WHERE trace_id IS NOT NULL;

-- ==========================================
-- Admin: 存储状态元数据
-- ==========================================

CREATE TABLE otel_storage_meta (
    key            TEXT PRIMARY KEY,
    value          JSONB NOT NULL,
    updated_at     TIMESTAMPTZ DEFAULT NOW()
);
```

---

## 五、RoadMap 与实施阶段

### 总览

```
Phase 1 (2周)          Phase 2 (2周)          Phase 3 (1.5周)       Phase 4 (1周)
──────────────         ──────────────         ──────────────        ──────────────
接口定义 +             ES 查询 +              PG Provider +         WebUI 集成 +
ES 写入实现            Log 全链路             Hybrid 模式           文档 & 发布
```

---

### Phase 1: 基础框架 + ES 写入 (预计 2 周)

#### 目标
- 定义所有核心接口
- 实现 ES Provider 的 TraceWriter / MetricWriter / LogWriter
- 实现统一 Exporter，替代 `otlphttp/jaeger` + `prometheusremotewrite`
- 数据能正确写入 ES

#### 任务清单

| # | 任务 | 优先级 | 工作量 |
|---|------|--------|--------|
| 1.1 | 创建 `observabilitystorageext` 包骨架 + Provider/Writer/Reader 接口定义 | P0 | 1d |
| 1.2 | 实现 Provider Registry (工厂模式注册/查找) | P0 | 0.5d |
| 1.3 | 实现 ES Provider 连接管理 (连接池, health check, retry) | P0 | 1d |
| 1.4 | 实现 ES Schema 管理 (Index Template 创建/更新, ILM Policy) | P0 | 1d |
| 1.5 | 实现 ES TraceWriter (ptrace.Traces → ES Bulk) | P0 | 1.5d |
| 1.6 | 实现 ES MetricWriter (pmetric.Metrics → ES Bulk) | P0 | 1.5d |
| 1.7 | 实现 ES LogWriter (plog.Logs → ES Bulk) | P0 | 1d |
| 1.8 | 实现内部批量缓冲器 (flush interval + batch size) | P0 | 1d |
| 1.9 | 实现 `observabilitystorageexporter` (OTel Exporter 适配) | P0 | 1d |
| 1.10 | 配置集成 + 端到端测试 (Agent → Pipeline → ES) | P0 | 1.5d |

#### 验收标准
- [x] Agent 发送的 Trace/Metric/Log 数据成功写入 ES 对应索引
- [x] 支持批量写入，flush_interval 和 batch_size 可配置
- [x] ES 连接失败时有重试和错误日志
- [x] `InitSchema` 自动创建 Index Template 和 ILM Policy
- [x] 单元测试覆盖 Writer 核心路径

#### 预期效果
```yaml
# 配置从
exporters:
  otlphttp/jaeger: ...
  prometheusremotewrite: ...

# 变为
extensions:
  observability_storage:
    type: elasticsearch
    elasticsearch: { addresses: [...] }
exporters:
  observability_storage:
    storage_extension: observability_storage
```

---

### Phase 2: ES 查询 + Log 全链路 (预计 2 周)

#### 目标
- 实现 ES TraceReader / MetricReader / LogReader
- adminext API 层对接新 Provider
- 前端新增 Log 查询页面
- 实现 StorageAdmin 基础功能

#### 任务清单

| # | 任务 | 优先级 | 工作量 |
|---|------|--------|--------|
| 2.1 | 实现 ES TraceReader (SearchTraces, GetTrace, GetServices, GetOperations, GetDependencies) | P0 | 2d |
| 2.2 | 实现 ES MetricReader (Query, QueryRange, ListMetricNames, ListLabels) | P0 | 2d |
| 2.3 | 实现 ES LogReader (SearchLogs, GetLogContext, ListLogFields, GetLogStats) | P0 | 2d |
| 2.4 | 重构 adminext observability handler：从 Provider.Reader() 获取实例 | P0 | 1d |
| 2.5 | 新增 Log 查询 API (`/api/v2/observability/logs/*`) | P0 | 1d |
| 2.6 | 实现 ES StorageAdmin (GetStatus, InitSchema, SetRetention, Purge, GetDiskUsage) | P1 | 1d |
| 2.7 | 新增 Storage Admin API (`/api/v2/observability/admin/*`) | P1 | 0.5d |
| 2.8 | 前端 WebUI: Log 查询页面 (搜索/过滤/上下文展开) | P1 | 2d |
| 2.9 | 前端 WebUI: Storage Status 面板 | P2 | 1d |
| 2.10 | 集成测试 + 压力测试 | P0 | 1d |

#### 验收标准
- [x] 通过 Admin API 可以搜索 Trace、查询 Metric、搜索 Log
- [x] Trace → Log 关联查询可用 (通过 trace_id 关联)
- [x] Log 全文搜索功能可用
- [x] Storage Admin 可以查看磁盘用量、设置 TTL、手动清理数据
- [x] WebUI Log 页面可用

#### 预期效果
```
adminext 配置从：
  observability:
    jaeger: { endpoint: "..." }
    prometheus: { endpoint: "..." }

变为 (自动从 Extension 获取)：
  observability:
    storage_extension: observability_storage
    # 无需手动配置后端地址！
```

---

### Phase 3: PostgreSQL Provider + Hybrid 模式 (预计 1.5 周)

#### 目标
- 实现 PostgreSQL Provider (全信号)
- 实现 Hybrid Provider (混合路由)
- 支持运行时切换/灰度

#### 任务清单

| # | 任务 | 优先级 | 工作量 |
|---|------|--------|--------|
| 3.1 | 实现 PG Provider 连接管理 + Schema Migration | P0 | 1d |
| 3.2 | 实现 PG TraceWriter/Reader (COPY 批量写入, SQL 查询) | P0 | 2d |
| 3.3 | 实现 PG MetricWriter/Reader (TimescaleDB 适配) | P0 | 2d |
| 3.4 | 实现 PG LogWriter/Reader (全文索引) | P1 | 1.5d |
| 3.5 | 实现 PG StorageAdmin (partition 管理, retention policy) | P1 | 1d |
| 3.6 | 实现 HybridProvider (路由逻辑: trace→ES, metric→PG, log→ES) | P0 | 1d |
| 3.7 | 配置热切换能力 (不重启切换 Provider) | P2 | 1d |
| 3.8 | 性能对比测试 (ES vs PG vs Hybrid) | P1 | 1d |

#### 验收标准
- [x] 修改配置 `type: "postgresql"` 即可完整切换到 PG 后端
- [x] Hybrid 模式：Metric 走 PG (利用时序优势)，Trace/Log 走 ES (利用全文检索)
- [x] Schema migration 自动执行，无需手工 DDL
- [x] PG 的 metric 查询性能不低于直连 Prometheus API（中等数据量）

#### 预期效果
```yaml
# 混合模式配置
observability_storage:
  type: "hybrid"
  hybrid:
    trace: "elasticsearch"
    metric: "postgresql"
    log: "elasticsearch"
    admin: "postgresql"
  elasticsearch:
    addresses: ["http://es:9200"]
  postgresql:
    dsn: "postgres://..."
```

---

### Phase 4: WebUI 完善 + MongoDB + 文档发布 (预计 1 周)

#### 目标
- WebUI 完善：Metric 页面支持新查询方式、跨信号关联
- MongoDB Provider (基础实现)
- 完善文档和运维工具

#### 任务清单

| # | 任务 | 优先级 | 工作量 |
|---|------|--------|--------|
| 4.1 | 前端 Metric 页面适配新 MetricReader API (不再是 raw Prometheus) | P0 | 1.5d |
| 4.2 | 前端 Trace ↔ Log 关联展示 (在 Trace Detail 中展示关联 Log) | P1 | 1d |
| 4.3 | MongoDB Provider 基础实现 (TraceWriter/Reader) | P2 | 2d |
| 4.4 | 运维文档：部署指南、配置参考、性能调优 | P0 | 1d |
| 4.5 | 清理旧代码：移除 `jaeger_reader.go`、`prometheus_reader.go`、旧 exporter 配置 | P0 | 0.5d |
| 4.6 | 端到端演示 & 发布 | P0 | 0.5d |

#### 验收标准
- [x] 完整的 Trace / Metric / Log 写入 + 查询 + 管理闭环
- [x] 支持至少 2 种后端 (ES + PG)，可通过配置切换
- [x] WebUI 所有页面功能正常
- [x] 文档完整（架构说明 + 配置参考 + 运维手册）
- [x] 旧代码全部清理

---

## 六、设计模式与原则

| 设计模式 | 应用场景 |
|----------|----------|
| **策略模式 (Strategy)** | Provider 接口 + 多种实现，运行时按配置选择 |
| **工厂模式 (Factory)** | Provider Registry 根据 `type` 创建对应 Provider 实例 |
| **门面模式 (Facade)** | Provider 作为统一门面，屏蔽 Writer/Reader/Admin 内部复杂性 |
| **适配器模式 (Adapter)** | Exporter 适配 OTel Pipeline 接口 → 调用 Provider.Writer |
| **组合模式 (Composite)** | HybridProvider 将多个子 Provider 组合为一个 |
| **模板方法 (Template)** | Writer 的 batch/flush/retry 逻辑抽象到 `internal/batch.go` |
| **依赖倒置 (DIP)** | adminext 依赖 Reader 接口，不依赖具体 ES/PG |
| **开闭原则 (OCP)** | 新增 MongoDB Provider 不修改已有 ES/PG 代码 |

---

## 七、风险与应对

| 风险 | 影响 | 应对 |
|------|------|------|
| ES Metric 聚合性能不足 | 高数据量下 metric 查询慢 | Phase 3 引入 PG/TimescaleDB；或预聚合 |
| ES 版本兼容性 (7.x vs 8.x) | API 差异导致写入/查询失败 | 使用 `go-elasticsearch` 官方库，条件编译兼容 |
| 大批量写入背压 | Pipeline 阻塞，数据丢失 | 内部 ring buffer + 溢出告警 + 持久化队列(可选) |
| Schema 变更不兼容 | 升级后旧数据无法查询 | 版本化 Schema + 在线 reindex 工具 |
| TimescaleDB 不可用 | PG metric 性能下降 | 代码兼容无 TimescaleDB 的纯 PG 模式 |

---

## 八、成功指标

| 指标 | Phase 1 | Phase 2 | Phase 3 | Phase 4 |
|------|---------|---------|---------|---------|
| 信号覆盖 | 写入 3/3 | 读写 3/3 | 读写 3/3 | 读写 3/3 |
| 后端支持 | ES | ES | ES + PG + Hybrid | ES + PG + MongoDB |
| 配置项数 | 1 块 | 1 块 | 1 块 | 1 块 |
| 旧代码 | 保留 | 共存 | 可选 | 清除 |
| WebUI | 无变化 | Trace+Log+Admin | 无变化 | Metric+关联 |
| 切换后端 | 改配置 | 改配置 | 改配置 | 改配置 |

---

## 九、扩展设计：App 级 Retention 策略

### 9.1 需求背景

不同 App（业务应用）对观测数据的保留时长需求不同：
- **核心交易 App**：链路保留 30 天，指标保留 90 天
- **内部工具 App**：链路保留 3 天，指标保留 7 天
- **测试环境 App**：全部保留 1 天

**为什么选 App 粒度而非 Service 粒度？**

| 维度 | App 粒度 ✅ | Service 粒度 ❌ |
|------|------------|----------------|
| 管理对象数 | 几个～几十个 app | 几十～几百个 service |
| 与认证对齐 | `app_id` 是 token 认证核心，写入链路天然携带 | 需额外提取 `service.name` |
| 计费/配额 | app 是计费的天然边界 | service 不是计费单元 |
| 租户语义 | app ≈ 业务线/租户 | service 是 app 的子级 |
| 策略维护成本 | 低（几条策略覆盖全部） | 高（策略爆炸增长） |

系统中已有明确层级：`App (app_id)` → `Service (service.name)` → `Instance (agent)`，策略挂在 App 级最合理。

### 9.2 核心设计思路：Retention 是 App 的属性

**关键洞察**：Retention 策略不应作为独立实体存在，它本质上是 App 的一个属性字段。

**之前的设计（❌ 过度抽象）**：

```
独立 RetentionPolicy 表 → app_pattern 通配匹配 → priority 优先级 → PolicyStore → RetentionRouter
```

**问题**：
- 配置文件中写 `app_pattern: "app-core-*"` 通配匹配 → 但用户在页面操作，不会改配置文件
- 独立 PolicyStore + 独立 CRUD API + 独立管理页面 → 过度设计
- Pattern 匹配 + Priority 排序 → 引入不必要的复杂度和冲突风险
- App 是明确实体，有 ID，不需要通配匹配

**现在的设计（✅ 高内聚）**：

```
App 实体直接增加 Retention 字段 → 没设就 fallback 平台默认值 → 完毕
```

### 9.3 架构总览

```mermaid
graph TB
    subgraph "App 实体 (Redis/DB)"
        APP1["App: payment-app<br/>tenant: acme-corp<br/>retention: {trace:14d, metric:60d, log:14d}"]
        APP2["App: internal-tools<br/>tenant: acme-corp<br/>retention: null (用默认)"]
        APP3["App: data-pipeline<br/>tenant: acme-corp<br/>retention: {trace:3d, metric:7d, log:3d}"]
    end

    subgraph "平台配置 (Collector Config)"
        CFG["retention:<br/>  default: {trace:7d, metric:30d, log:14d}<br/>  max: {trace:30d, metric:90d, log:30d}"]
    end

    subgraph "数据写入"
        EXP[StorageExporter]
        EXP -->|"读 app.retention<br/>决定写入目标"| APP1
        EXP -->|"fallback"| CFG
        EXP --> ES[Storage Backend]
    end

    subgraph "数据清理"
        ENF[RetentionEnforcer<br/>定时任务]
        ENF -->|"遍历所有 App"| APP1
        ENF -->|"遍历所有 App"| APP2
        ENF -->|"遍历所有 App"| APP3
        ENF -->|"fallback default"| CFG
        ENF -->|"Purge expired"| ES
    end

    style APP1 fill:#bbf,stroke:#333
    style CFG fill:#fbb,stroke:#333
    style ENF fill:#f9f,stroke:#333
```

### 9.4 App 模型增强

```go
// ═══════════════ App 实体（已存在，增加 Retention 字段） ═══════════════

type App struct {
    ID        string    `json:"id"`         // e.g. "payment-app"
    TenantID  string    `json:"tenant_id"`  // e.g. "acme-corp"
    Name      string    `json:"name"`
    // ... 已有字段: services, api_key, created_at 等 ...

    // 🆕 Retention 配置（App 的属性，不是独立实体）
    Retention *AppRetention `json:"retention,omitempty"` // nil = 使用平台默认值
}

type AppRetention struct {
    TraceTTL  time.Duration `json:"trace_ttl,omitempty"`  // 0 = 使用默认
    MetricTTL time.Duration `json:"metric_ttl,omitempty"` // 0 = 使用默认
    LogTTL    time.Duration `json:"log_ttl,omitempty"`    // 0 = 使用默认
}
```

### 9.5 平台配置（仅 Collector 配置文件管的事）

```yaml
# 配置文件只管两件事：平台默认值 + 平台上限
observability_storage:
  retention:
    default:
      trace_ttl: "7d"
      metric_ttl: "30d"
      log_ttl: "14d"
    max:
      trace_ttl: "30d"
      metric_ttl: "90d"
      log_ttl: "30d"
    enforcer:
      enabled: true
      schedule: "0 2 * * *"    # 每天凌晨 2 点
      batch_size: 10000
```

**配置文件不再包含任何 App 级策略**——那是 App 自己的属性，通过 UI/API 管理，存在 Redis/DB 中。

### 9.6 核心逻辑：解析 Retention

```go
// 解析某个 App 某个信号的保留时长
// 逻辑极简：app 自定义 > 平台默认
func ResolveRetention(app *App, signal SignalType, platformDefault *PlatformRetention) time.Duration {
    if app.Retention == nil {
        return platformDefault.Get(signal)
    }
    
    var ttl time.Duration
    switch signal {
    case SignalTrace:
        ttl = app.Retention.TraceTTL
    case SignalMetric:
        ttl = app.Retention.MetricTTL
    case SignalLog:
        ttl = app.Retention.LogTTL
    }
    
    if ttl <= 0 {
        return platformDefault.Get(signal) // 未设置该信号，fallback
    }
    return ttl
}
```

### 9.7 权限分层与校验

```mermaid
sequenceDiagram
    participant T as Tenant User
    participant API as Admin API
    participant APP as App Store (Redis)
    participant CFG as Platform Config

    T->>API: PUT /api/v2/tenant/apps/:app_id/retention<br/>{trace_ttl: "14d", metric_ttl: "60d"}
    API->>CFG: 读取 platform max
    CFG-->>API: {trace: 30d, metric: 90d, log: 30d}
    
    Note over API: 校验: 14d ≤ 30d ✅, 60d ≤ 90d ✅
    
    API->>APP: 更新 app.retention = {trace:14d, metric:60d}
    APP-->>API: OK
    API-->>T: 200 Updated
```

**权限矩阵**：

| 角色 | 能力 |
|------|------|
| **Platform Admin** | ① 设平台默认值/上限（改配置 + 重启/热加载） ② 查看所有 App 的 retention ③ 代替租户修改 ④ 手动触发清理 |
| **Tenant User** | ① 查看/修改自己 App 的 retention（≤ 平台上限） ② 查看存储用量 ③ 预览清理影响 |

**校验逻辑**：

```go
func ValidateRetention(retention *AppRetention, platformMax *PlatformRetention) error {
    if retention.TraceTTL > platformMax.TraceTTL {
        return fmt.Errorf("trace_ttl %v exceeds platform max %v", retention.TraceTTL, platformMax.TraceTTL)
    }
    if retention.MetricTTL > platformMax.MetricTTL {
        return fmt.Errorf("metric_ttl %v exceeds platform max %v", retention.MetricTTL, platformMax.MetricTTL)
    }
    if retention.LogTTL > platformMax.LogTTL {
        return fmt.Errorf("log_ttl %v exceeds platform max %v", retention.LogTTL, platformMax.LogTTL)
    }
    return nil
}
```

### 9.8 RetentionEnforcer（定时清理）

```mermaid
sequenceDiagram
    participant Cron as RetentionEnforcer (每日02:00)
    participant AS as App Store
    participant CFG as Platform Config
    participant S as Storage Backend

    Cron->>AS: ListAllApps()
    AS-->>Cron: [payment-app, internal-tools, data-pipeline, ...]
    Cron->>CFG: GetDefault()
    CFG-->>Cron: {trace:7d, metric:30d, log:14d}
    
    loop 每个 App
        Note over Cron: resolve: payment-app → trace:14d
        Cron->>S: PurgeByApp("payment-app", Trace, before=now-14d)
        S-->>Cron: deleted: 8,234 spans
        Cron->>S: PurgeByApp("payment-app", Metric, before=now-60d)
        S-->>Cron: deleted: 2,100 series
        Cron->>S: PurgeByApp("payment-app", Log, before=now-14d)
        S-->>Cron: deleted: 5,420 entries
    end
    
    Note over Cron: 生成 CleanupReport
```

```go
// RetentionEnforcer — 定时清理器
type RetentionEnforcer struct {
    appStore     AppStore            // 读取所有 App
    config       *PlatformRetention  // 平台默认值
    storageAdmin StorageAdmin        // 执行清理
    schedule     string              // cron 表达式
}

func (e *RetentionEnforcer) RunCleanup(ctx context.Context) (*CleanupReport, error) {
    apps, err := e.appStore.ListAll(ctx)
    if err != nil {
        return nil, err
    }

    report := &CleanupReport{StartedAt: time.Now()}

    for _, app := range apps {
        for _, signal := range []SignalType{SignalTrace, SignalMetric, SignalLog} {
            ttl := ResolveRetention(app, signal, e.config)
            cutoff := time.Now().Add(-ttl)

            result, err := e.storageAdmin.PurgeByApp(ctx, app.ID, signal, cutoff)
            report.AddResult(app.ID, signal, result, err)
        }
    }

    report.FinishedAt = time.Now()
    return report, nil
}

type CleanupReport struct {
    StartedAt  time.Time       `json:"started_at"`
    FinishedAt time.Time       `json:"finished_at"`
    Results    []CleanupResult `json:"results"`
}

type CleanupResult struct {
    AppID   string     `json:"app_id"`
    Signal  SignalType `json:"signal"`
    Cutoff  time.Time  `json:"cutoff"`
    Deleted int64      `json:"deleted"`
    Error   string     `json:"error,omitempty"`
}
```

### 9.9 分布式部署协调

在多实例部署场景下，多个 Collector 同时触发 Enforcer 会导致重复清理、IO 浪费和报告混乱。需要协调机制确保**只有一个实例执行清理**。

#### 方案对比

| 方案 | 适用场景 | 复杂度 | 额外依赖 |
|------|----------|--------|----------|
| **无协调（单实例）** | 开发/测试环境 | 无 | 无 |
| **Redis 分布式锁（推荐）** | 已有 Redis 的生产环境 | 低 | Redis（已有） |
| **K8s Lease** | K8s 部署 + 无 Redis | 中 | K8s API |
| **外部 CronJob 触发** | 不想内建调度 | 最低 | K8s CronJob / 外部定时系统 |

#### 推荐方案：Redis 分布式锁

```mermaid
sequenceDiagram
    participant C1 as Collector-1
    participant C2 as Collector-2
    participant C3 as Collector-3
    participant R as Redis
    participant S as Storage (ES)

    Note over C1,C3: 每日 02:00 Cron 触发

    C1->>R: SET retention:enforcer:lock<br/>instance-1 NX EX 600
    R-->>C1: OK (获得锁)
    
    C2->>R: SET retention:enforcer:lock<br/>instance-2 NX EX 600
    R-->>C2: FAIL (锁已存在)
    Note over C2: 跳过本轮
    
    C3->>R: SET retention:enforcer:lock<br/>instance-3 NX EX 600
    R-->>C3: FAIL (锁已存在)
    Note over C3: 跳过本轮

    C1->>S: PurgeByApp("payment-app", Trace, ...)
    C1->>S: PurgeByApp("data-pipeline", Log, ...)
    C1->>R: DEL retention:enforcer:lock
    Note over C1: 清理完成，释放锁
```

#### 实现代码

```go
type RetentionEnforcer struct {
    appStore     AppStore
    config       *PlatformRetention
    storageAdmin StorageAdmin
    locker       DistributedLocker   // 分布式锁
    schedule     string
}

// DistributedLocker — 分布式锁接口
type DistributedLocker interface {
    // Acquire 尝试获取锁，获取失败返回 error
    Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, error)
}

type Lock interface {
    Release(ctx context.Context) error
}

func (e *RetentionEnforcer) RunCleanup(ctx context.Context) (*CleanupReport, error) {
    // 1. 尝试获取分布式锁
    lock, err := e.locker.Acquire(ctx, "retention:enforcer:lock", 10*time.Minute)
    if err != nil {
        // 没抢到锁 = 其他实例在执行，静默跳过
        return nil, nil
    }
    defer lock.Release(ctx)

    // 2. 只有持锁实例执行清理
    return e.doCleanup(ctx)
}

// Redis 实现
type RedisLocker struct {
    client *redis.Client
    id     string // 实例唯一标识
}

func (l *RedisLocker) Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, error) {
    ok, err := l.client.SetNX(ctx, key, l.id, ttl).Result()
    if err != nil {
        return nil, err
    }
    if !ok {
        return nil, fmt.Errorf("lock held by another instance")
    }
    return &redisLock{client: l.client, key: key, id: l.id}, nil
}

// NoopLocker — 单实例模式，不需要协调
type NoopLocker struct{}

func (l *NoopLocker) Acquire(_ context.Context, _ string, _ time.Duration) (Lock, error) {
    return &noopLock{}, nil // 总是成功
}
```

#### 配置

```yaml
observability_storage:
  retention:
    enforcer:
      enabled: true
      schedule: "0 2 * * *"
      batch_size: 10000
      # 分布式协调
      coordination:
        type: "redis"              # "none" | "redis" | "k8s_lease"
        lock_key: "retention:enforcer:lock"
        lock_ttl: "10m"            # 锁超时（防止持锁实例崩溃后死锁）
```

#### 备选：外部 CronJob 触发（最简方案）

如果不想在 Collector 内管调度，直接用外部定时系统调 API：

```yaml
# k8s CronJob 示例
apiVersion: batch/v1
kind: CronJob
metadata:
  name: retention-cleanup
spec:
  schedule: "0 2 * * *"
  concurrencyPolicy: Forbid       # 禁止并发，天然避免重复
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: trigger
            image: curlimages/curl
            command: ["curl", "-X", "POST", "http://collector-svc:8080/api/v2/admin/retention/cleanup"]
```

此方案下 Enforcer 只暴露 API 不内建 Cron，**零协调问题**。

#### 决策指南

| 部署方式 | 推荐 | 理由 |
|----------|------|------|
| 单实例 | `type: "none"` | 不需要协调 |
| 多实例 + 已有 Redis | `type: "redis"` | 零额外依赖，实现简单 |
| K8s + 不想内建调度 | 外部 CronJob + `enforcer.enabled: false` | 最简单，零代码 |
| K8s + 需内建调度 + 无 Redis | `type: "k8s_lease"` | K8s 原生 leader election |

### 9.10 API 设计

```
# ═══════ Platform Admin ═══════
GET  /api/v2/admin/retention/config            — 获取平台默认值 + 上限
PUT  /api/v2/admin/retention/config            — 修改（热加载，无需重启）
GET  /api/v2/admin/apps/retention              — 查看所有 App 的 retention 概览
POST /api/v2/admin/retention/cleanup           — 手动触发清理
GET  /api/v2/admin/retention/cleanup/dry-run   — 预览清理计划
GET  /api/v2/admin/retention/report            — 最近清理报告

# ═══════ Tenant User ═══════
GET  /api/v2/tenant/apps/:app_id/retention     — 获取某 App 的 retention
PUT  /api/v2/tenant/apps/:app_id/retention     — 修改（校验 ≤ 平台上限）
DELETE /api/v2/tenant/apps/:app_id/retention   — 重置为平台默认
GET  /api/v2/tenant/retention/limits           — 查看平台上限（只读）
GET  /api/v2/tenant/retention/usage            — 查看存储用量
GET  /api/v2/tenant/apps/:app_id/retention/dry-run — 预览清理影响
```

注意 Tenant 的 API 是挂在 **App 资源路径下** 的（`/apps/:app_id/retention`），因为 Retention 是 App 的属性。

### 9.11 ES 后端清理策略

```mermaid
graph LR
    subgraph "统一 Index（推荐中小规模）"
        I1["otel-traces-2026.05.*"]
        I2["otel-metrics-2026.05.*"]
        I3["otel-logs-2026.05.*"]
    end

    subgraph "RetentionEnforcer 执行"
        E[Enforcer]
        E -->|"DELETE BY QUERY<br/>app_id='payment-app'<br/>AND @timestamp < now-14d"| I1
        E -->|"DELETE BY QUERY<br/>app_id='data-pipeline'<br/>AND @timestamp < now-3d"| I1
    end
```

- **中小规模**（< 50 App）：统一 Index + Delete By Query，简单直接
- **大规模**：按 TTL 分 Index Group（hot/extended/short），ILM 自动管理

### 9.12 设计原则对照

| 原则 | 对应设计 |
|------|----------|
| **高内聚** | Retention 是 App 的属性，不是独立实体。管理 App 时顺便管理 Retention |
| **低耦合** | Enforcer 只依赖 AppStore 接口 + StorageAdmin 接口，不关心具体存储后端 |
| **SRP** | App 管理自己的配置；Enforcer 只负责清理执行；配置文件只管平台级参数 |
| **DRY** | 解析逻辑一行：`app.Retention ?? default`，无重复 |
| **KISS** | 去掉 pattern 匹配、priority、独立 PolicyStore，代码量减少 70%+ |
| **OCP** | 未来加 Service 级覆盖只需在 App 内加字段，不改 Enforcer |
| **健壮性** | App.Retention=nil 时安全 fallback 到默认值；单 App 清理失败不影响其他 |
| **可扩展** | 预留 Service 级覆盖：`AppRetention.ServiceOverrides map[string]*SignalRetention` |

### 9.13 模块依赖图

```mermaid
graph TB
    subgraph "observabilitystorageext"
        Provider["Provider<br/>(ES/PG/Hybrid)"]
        
        subgraph "provider/"
            TW[TraceWriter]
            MW[MetricWriter]
            LW[LogWriter]
            SA[StorageAdmin]
        end
        
        ENF[RetentionEnforcer]
    end

    subgraph "已有模块"
        AS[AppStore<br/>Redis/DB]
        CFG[Platform Config<br/>retention.default + max]
    end

    subgraph "外部调用"
        EXP[StorageExporter] --> Provider
        ADM[adminext API] --> AS
        ADM --> ENF
    end

    ENF -->|"ListAllApps()"| AS
    ENF -->|"GetDefault()"| CFG
    ENF -->|"PurgeByApp()"| SA

    EXP -->|"ResolveRetention()"| AS
    EXP -->|"fallback"| CFG

    style ENF fill:#f9f,stroke:#333
    style AS fill:#bbf,stroke:#333
    style CFG fill:#fbb,stroke:#333
```

### 9.14 与之前设计的对比

| 维度 | 之前（独立 Policy + Pattern） | 现在（App 属性） |
|------|-------------------------------|------------------|
| 存储位置 | 独立 PolicyStore（ES/PG/Config 三选一） | App 实体自带字段（已有 Redis/DB） |
| 匹配逻辑 | `app_pattern` + `priority` + fallback 链 | `app.Retention ?? default` |
| 配置文件角色 | 存放 App 级策略（矛盾：UI 操作 vs 配置文件） | 只存平台默认值 + 上限（合理） |
| 管理入口 | 独立 Retention CRUD API + 独立页面 | App 设置页的一部分 |
| 新增模块 | RetentionPolicy + PolicyStore + Router + Enforcer | 仅 Enforcer（App 模型加字段） |
| 代码量估算 | ~1500 行 | ~300 行 |
| 理解成本 | 高（5 个抽象层） | 低（App 有个 retention 字段，一目了然） |

---

## 十、参考资料

| 项目 | 参考点 |
|------|--------|
| [Jaeger ES Storage](https://github.com/jaegertracing/jaeger/tree/main/plugin/storage/es) | ES Trace 写入/查询的最佳实践 |
| [SigNoz](https://github.com/SigNoz/signoz) | 统一 ClickHouse 存储的三合一设计 |
| [otel-collector-contrib ES exporter](https://github.com/open-telemetry/opentelemetry-collector-contrib/tree/main/exporter/elasticsearchexporter) | OTel → ES 的数据模型映射 |
| [Promscale/TimescaleDB](https://github.com/timescale/promscale) | PG 上的 Metric 时序存储 |
| [Uptrace](https://github.com/uptrace/uptrace) | 统一存储 + PromQL 兼容查询 |
| [Grafana Tempo Retention](https://grafana.com/docs/tempo/latest/configuration/#compactor) | Per-tenant retention 策略设计 |
| [ES ILM](https://www.elastic.co/guide/en/elasticsearch/reference/current/index-lifecycle-management.html) | Index Lifecycle Management 最佳实践 |

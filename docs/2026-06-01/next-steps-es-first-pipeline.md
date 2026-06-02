# 统一观测存储层 — ES-First 策略与下一步计划

> **日期**: 2026-06-01（最后更新: 2026-06-02）  
> **策略**: ES-First — 先通过 ES 实现验证标准化架构，PG/MongoDB 延后作为插件扩展  
> **状态**: ✅ 全部核心链路已打通 — 端到端验证通过（Write → Store → Query → Display）  

---

## 一、战略决策

### 1.1 ES-First 策略

优先通过 Elasticsearch Provider 验证整个抽象架构的正确性和可行性：

- **先聚焦 ES** — 打通完整的 Write → Store → Query → Display 链路
- **PG/MongoDB 延后** — 已有代码实现，但暂不作为优先级，待 ES 链路验证通过后再启用
- **验证抽象设计** — 确保 `StorageProvider` 接口设计合理，后续扩展只需实现接口 + 改配置

### 1.2 数据层次与隔离模型

```
Tenant (tenant_id)
  └─ App (app_id)        ← 数据隔离粒度
       └─ Service (service.name)
            └─ Instance (agent)
```

- **隔离粒度**: App 级别（`app_id`）
- **ES 索引命名**: `{prefix}-{appID}-{date}` (如 `otel-traces-ecommerce-2026.06.01`)
- **写入强制**: 无 `app_id` 的数据直接拒绝写入
- **核心查询强制**: `SearchTraces`/`Query`/`QueryRange`/`SearchLogs` 需传入 `AppID`

---

## 二、已完成模块

### ✅ Phase 1: ES Writer（完成）

| 模块 | 文件 | 说明 |
|------|------|------|
| TraceWriter | `provider/elasticsearch/trace_writer.go` | ptrace.Traces → ES docs, bulk buffer |
| MetricWriter | `provider/elasticsearch/metric_writer.go` | Gauge/Sum/Histogram/Summary 支持 |
| LogWriter | `provider/elasticsearch/log_writer.go` | plog.Logs → ES docs |
| BulkBuffer | `provider/elasticsearch/bulk_buffer.go` | 批量写入 + 后台 flush loop |
| Model | `provider/elasticsearch/model.go` | 数据转换 + `getAppID()` + `sanitizeAppID()` |
| App-Scoped Index | 所有 Writer | `getIndexName(appID, time)` → `{prefix}-{appID}-{date}` |

### ✅ Phase 2: ES Reader + Admin（完成）

| 模块 | 文件 | 说明 |
|------|------|------|
| TraceReader | `provider/elasticsearch/trace_reader.go` | SearchTraces/GetTrace/GetServices/GetOperations/GetDependencies |
| MetricReader | `provider/elasticsearch/metric_reader.go` | Query/QueryRange/ListMetricNames/ListLabelNames/ListLabelValues |
| LogReader | `provider/elasticsearch/log_reader.go` | SearchLogs/GetLogContext/ListLogFields/GetLogStats |
| StorageAdmin | `provider/elasticsearch/admin.go` | InitSchema/GetStatus/GetIndicesStats/SetRetention/Purge/PurgeByApp |
| Reader Types | `provider/elasticsearch/types_reader.go` | Query 结构体 + AppID 字段 |

### ✅ Exporter Bridge（完成）

| 模块 | 文件 | 说明 |
|------|------|------|
| Factory | `observabilitystorageexporter/factory.go` | 注册 Traces/Metrics/Logs exporter |
| Exporter | `observabilitystorageexporter/exporter.go` | Pipeline → Extension.Writer 桥接 |
| Config | `observabilitystorageexporter/config.go` | `StorageExtension` ID 引用 |
| 组件注册 | `cmd/customcol/components.go` | 已注册到 Collector factories |

### ✅ Extension 门面层（完成）

| 模块 | 文件 | 说明 |
|------|------|------|
| Extension | `observabilitystorageext/extension.go` | 持有 Provider 单例, Write/Flush/HealthCheck |
| Types | `observabilitystorageext/types.go` | 公共 Reader/Admin 接口定义 |
| Reader Adapter | `observabilitystorageext/reader_adapter.go` | ES Reader → 公共 Reader 适配 |
| Config | `observabilitystorageext/config.go` | 多后端配置支持 |

### ✅ OTel 标准化数据模型重构（2026-06-01 完成）

**目标**: 所有 API 响应对齐 OpenTelemetry OTLP JSON Protobuf Encoding 标准

| 变更 | 文件 | 说明 |
|------|------|------|
| 核心类型 | `observabilitystorageext/types.go` | KeyValue/AnyValue OTel 属性模型、SpanKind/StatusCode 枚举、纳秒时间戳字符串 |
| ES Adapter | `observabilitystorageext/reader_adapter.go` | ES 内部类型 → OTel 标准模型转换 |
| PG Adapter | `observabilitystorageext/pg_reader_adapter.go` | PG 内部类型 → OTel 标准模型转换 |
| Helper | `observabilitystorageext/types.go` | MapToKeyValues/TimeToUnixNano/NormalizeSpanKind/NormalizeStatusCode |
| 设计文档 | `docs/2026-06-01/otel-standard-data-model-design.md` | 完整的 OTel 标准对齐设计 |

**关键变更**:
- `OperationName` → `Name`（OTel Span 用 `name`）
- `StartTime/EndTime time.Time` → `StartTimeUnixNano/EndTimeUnixNano string`
- `Attributes/Resource map[string]any` → `[]KeyValue`（OTel 标准 typed attributes）
- `SpanKind string` → `Kind SpanKind`（枚举: `SPAN_KIND_SERVER` 等）
- `StatusCode string + StatusMessage string` → `Status SpanStatus`（嵌套结构）
- `MetricSeries.Values []MetricDataPoint` → `[]MetricTimeValue`
- 所有 JSON tag 改为 camelCase

### ✅ AdminExt V2 Handler（完成）

| 模块 | 文件 | 说明 |
|------|------|------|
| V2 Handler | `adminext/observability_handler_v2.go` | 结构化 JSON 响应（非代理） |
| Router | `adminext/router.go` | storageReader != nil → V2 模式 |
| 初始化 | `adminext/extension.go:500` | 通过 Extension 获取 Reader/Admin |

### ✅ 配置集成（完成）

| 配置 | 位置 | 说明 |
|------|------|------|
| Extension config | `config/template/config.yaml` | `observability_storage` extension 块 |
| AdminExt 引用 | `config/template/config.yaml:377` | `storage_extension: "observability_storage"` |
| Exporter 注册 | `cmd/customcol/components.go:94` | Factory 已注册 |

### ✅ 测试（28个用例全部通过）

```
ok  go.opentelemetry.io/collector/custom/extension/observabilitystorageext/provider/elasticsearch  0.638s
```

---

## 三、架构验证状态

从代码分析来看，**端到端链路的核心代码已全部就绪**：

```
Agent (OTLP)
  │
  ▼
agent_gateway receiver (HTTP/gRPC)
  │  token_auth → inject app_id
  ▼
tokenauthprocessor (验证 token, 提取 app_id)
  │
  ▼
observability_storage exporter (ConsumeTraces/Metrics/Logs)
  │  extension.WriteTraces/Metrics/Logs
  ▼
observabilitystorageext (Extension 门面)
  │  provider.WriteTraces/Metrics/Logs
  ▼
elasticsearch.Provider (ES Writer)
  │  bulkBuffer → Bulk API
  ▼
Elasticsearch (索引: otel-traces-{appID}-{date})
  │
  ▼ (查询路径)
adminext V2 Handler
  │  extension.GetTraceReader/MetricReader/LogReader
  ▼
elasticsearch.TraceReader/MetricReader/LogReader
  │  ES Search API
  ▼
WebUI (React: TracesPage, MetricsPage, LogsPage)
```

---

## 四、下一步 — 端到端链路打通

### 4.0 前端适配 OTel 标准数据模型 — ✅ 已完成（2026-06-01）

**目标**: 前端 WebUI 适配后端新的 OTel 标准 JSON 响应格式

- [x] **Traces 前端适配**: `operationName` → `name`、`startTime` → `startTimeUnixNano`、`duration` → `durationNano`、`attributes {}` → `attributes []KeyValue`
- [x] **Metrics 前端适配**: `time` → `timeUnixNano`、`values []MetricDataPoint` → `values []MetricTimeValue`
- [x] **API client 更新**: `src/api/client.ts` 中的类型定义和解析逻辑
- [x] **工具函数更新**: `src/utils/trace.ts` 中的 span 处理逻辑

### 4.1 配置联调 — ✅ 已完成（2026-06-02）

**目标**: 确保 pipeline YAML 配置正确串联所有组件

- [x] **配置 `observability_storage` exporter 到 pipeline** — 已在 service.pipelines 中配置
- [x] **ES 连接配置验证** — ES 集群 green，数据正常写入/查询
- [x] **AdminExt V2 模式启用** — `storage_extension: observability_storage` 配置生效

### 4.2 集成验证 — ✅ 已完成（2026-06-02）

**目标**: 端到端跑通 Write → Query → Display

- [x] **写入验证**: 通过 OTLP HTTP (4318) 发送 Traces/Metrics/Logs，ES 索引创建成功，文档写入正确
- [x] **查询验证**: V2 API 返回正确的 OTel 标准 JSON 格式
- [x] **WebUI 验证**: 前端页面正确渲染（构建产物更新后白屏问题修复）

**验证结果**:
- 3 个 Service: `e2e-test-service`、`payment-service`、`order-service`
- 3 条 Trace: 含跨服务调用（2 services / 2 spans）
- 3 条 Log: 含 INFO/WARN 级别，关联 traceId/spanId
- 2 个 Metric: `http_requests_total`、`payment_total`
- 服务依赖图: 自动计算 `order-service → payment-service`

### 4.3 前端 AppID 透传 — ✅ 部分完成（2026-06-02）

**目标**: 前端查询 API 携带 `app_id` 参数

- [x] Handler 层支持可选 `app_id` 查询参数（`parseTraceQuery`/`parseMetricQuery`/`parseLogQuery`）
- [x] ES Reader 层移除 AppID 强制校验，支持 Admin 全局查询（`indexPattern("")` → 通配）
- [ ] 前端 UI 提供 App 选择器（可选增强，当前全局查询已满足 Admin 需求）

### 4.4 Index Template 自动初始化 — ✅ 已完成

- [x] `admin.InitSchema(ctx)` 在 Provider.Start() 中调用
- [x] trace/metric/log 的 template 字段映射正确

### 4.5 数据生命周期管理（P2 — 设计中）

详见下方 **第七章：数据生命周期管理设计**。

---

## 五、延后的模块

以下模块已有代码实现，验证 ES 链路后根据实际需求启用：

| 模块 | 状态 | 启用条件 |
|------|------|----------|
| PostgreSQL Provider | 代码完成 + 集成测试通过 | 当需要时序聚合优化 |
| MongoDB Provider | 设计完成 | 当需要文档型存储 |
| Hybrid Provider | 代码完成 | 当需要信号级路由（如 metric→PG, trace→ES） |
| 配置热切换 | 未实现 | Phase 3+ |
| Multi-App PG 分区 | 设计完成 | 启用 PG 后实施 |

---

## 六、相关文档索引

| 文档 | 路径 | 说明 |
|------|------|------|
| 架构总设计 | `docs/2026-05-29/unified-observability-storage-design.md` | 核心架构、接口定义、分阶段计划 |
| Phase 3 进展 | `docs/2026-06-01/phase3-postgresql-provider.md` | PG 实现 + ES App-Scoped 索引 + 测试修复记录 |
| Multi-App 分区设计 | `docs/2026-06-01/multi-app-pg-partition-design.md` | PG Schema-per-App 隔离方案（延后） |
| 前端实现 | `docs/2026-06-01/phase2-frontend-implementation.md` | WebUI React 页面实现记录 |

---

## 七、数据生命周期管理设计

> **设计原则**: 存储后端无关的统一抽象、SOLID、策略模式、依赖倒置、高内聚低耦合

### 7.1 设计哲学

数据生命周期管理（Data Lifecycle Management, DLM）是一个**与存储实现无关**的横切关注点。
无论底层是 ES、PG 还是 MongoDB，过期数据的发现、决策、执行三步骤的**业务逻辑是完全一致的**。

**核心设计原则**：

| 原则 | 体现 |
|------|------|
| **SRP** (单一职责) | 策略解析、调度编排、过期执行、审计记录各自独立组件 |
| **OCP** (开放封闭) | 新增存储后端只需实现 `LifecyclePurger` 接口，无需修改调度器 |
| **LSP** (里氏替换) | ES/PG/MongoDB 的 Purger 实现可互换使用 |
| **ISP** (接口隔离) | `RetentionResolver` / `LifecyclePurger` / `UsageReporter` 职责分离 |
| **DIP** (依赖倒置) | Scheduler 依赖抽象接口，不依赖任何具体 Provider |
| **策略模式** | RetentionPolicy 通过 Resolver 链式解析（Per-App → Platform → Builtin） |

### 7.2 分层架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                    Application Layer (API/UI)                        │
│                                                                     │
│  Admin API Handler ─── StorageAdminPage (React)                     │
│  GET/PUT retention, POST purge, GET usage-trend                     │
└────────────────────────────────┬────────────────────────────────────┘
                                 │ depends on abstractions
┌────────────────────────────────▼────────────────────────────────────┐
│                   Domain Layer (Business Logic)                      │
│                                                                     │
│  ┌──────────────┐  ┌──────────────────┐  ┌───────────────────────┐ │
│  │ Lifecycle    │  │ RetentionResolver │  │ UsageMonitor          │ │
│  │ Scheduler    │  │                  │  │                       │ │
│  │              │  │ Chain-of-Resp:   │  │ • collect snapshots   │ │
│  │ • tick loop  │  │  AppOverride     │  │ • evaluate thresholds │ │
│  │ • orchestrate│  │  → PlatformDefault│  │ • emit alerts        │ │
│  │ • audit      │  │  → BuiltinDefault│  │                       │ │
│  └──────┬───────┘  └────────┬─────────┘  └───────────┬───────────┘ │
│         │                   │                        │             │
│         ▼                   ▼                        ▼             │
│  ╔══════════════════════════════════════════════════════════════╗   │
│  ║              Lifecycle Abstractions (Interfaces)             ║   │
│  ║                                                             ║   │
│  ║  LifecyclePurger       RetentionStore      UsageReporter    ║   │
│  ║  ├─ PurgeExpired()     ├─ Get()            ├─ GetUsage()    ║   │
│  ║  ├─ PurgeByApp()       ├─ Set()            └─ GetTrend()    ║   │
│  ║  ├─ EstimatePurge()    ├─ GetForApp()                       ║   │
│  ║  └─ GetDataBoundary()  └─ SetForApp()                       ║   │
│  ╚══════════════════════════════════════════════════════════════╝   │
└────────────────────────────────┬────────────────────────────────────┘
                                 │ implemented by
┌────────────────────────────────▼────────────────────────────────────┐
│                  Infrastructure Layer (Providers)                    │
│                                                                     │
│  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐  │
│  │ ES Purger        │  │ PG Purger        │  │ Mongo Purger     │  │
│  │                  │  │                  │  │                  │  │
│  │ • ILM policy     │  │ • DROP PARTITION │  │ • TTL index      │  │
│  │ • delete index   │  │ • DELETE WHERE   │  │ • drop collection│  │
│  │ • delete_by_query│  │   ts < cutoff    │  │                  │  │
│  └──────────────────┘  └──────────────────┘  └──────────────────┘  │
│                                                                     │
│  ┌────────────────────────────────────────────────────────────────┐ │
│  │ RetentionStore Implementations                                 │ │
│  │ • InMemory (default: from config)                              │ │
│  │ • ControlPlane (per-app: from App metadata store)              │ │
│  └────────────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────┘
```

### 7.3 核心抽象接口

#### 7.3.1 LifecyclePurger — 过期数据执行器

```go
// LifecyclePurger is the SOLE abstraction for data expiration execution.
// Each storage backend implements this interface with its native cleanup mechanism.
//
// Design: Interface Segregation — only exposes lifecycle-related operations,
// NOT mixed with read/write/schema concerns.
type LifecyclePurger interface {
    // PurgeExpired removes all data for the given signal that is older than `before`.
    // The implementation decides the most efficient strategy (delete index, delete by query,
    // drop partition, etc.) — callers don't need to know.
    PurgeExpired(ctx context.Context, signal SignalType, before time.Time) (*PurgeResult, error)

    // PurgeByApp removes expired data scoped to a specific application.
    PurgeByApp(ctx context.Context, appID string, signal SignalType, before time.Time) (*PurgeResult, error)

    // EstimatePurge returns a preview of what would be deleted WITHOUT executing.
    // Used for dry-run and UI preview scenarios.
    EstimatePurge(ctx context.Context, signal SignalType, before time.Time) (*PurgeEstimate, error)

    // GetDataBoundary returns the oldest and newest data timestamp per signal.
    // Used by the scheduler to determine if cleanup is needed and by UI for display.
    GetDataBoundary(ctx context.Context, signal SignalType) (*DataBoundary, error)
}

// PurgeEstimate previews a purge operation without executing.
type PurgeEstimate struct {
    Signal         SignalType `json:"signal"`
    EstimatedDocs  int64     `json:"estimatedDocs"`
    EstimatedBytes int64     `json:"estimatedBytes"`
    AffectedUnits  []string  `json:"affectedUnits"` // indices / partitions / collections
}

// DataBoundary represents the time range of existing data for a signal.
type DataBoundary struct {
    Signal    SignalType  `json:"signal"`
    OldestAt  *time.Time `json:"oldestAt,omitempty"`
    NewestAt  *time.Time `json:"newestAt,omitempty"`
    IsEmpty   bool       `json:"isEmpty"`
}
```

**关键设计点**：
- `PurgeExpired` 是**策略模式**的核心 — ES 实现可以选择删索引或 delete_by_query，PG 实现可以 DROP PARTITION，Mongo 可以 drop collection。调度器完全不关心具体策略。
- `EstimatePurge` 支持 dry-run，保证安全性。
- `GetDataBoundary` 使调度器能判断"是否有需要清理的数据"，避免无意义的清理调用。

#### 7.3.2 RetentionResolver — 策略解析链

```go
// RetentionResolver resolves the effective retention policy for a given context.
// It follows the Chain-of-Responsibility pattern:
//   AppOverride → PlatformDefault → BuiltinFallback
//
// Design: Open-Closed — new resolution sources (e.g., TenantTier) can be added
// as new Resolver nodes without modifying existing ones.
type RetentionResolver interface {
    // Resolve returns the effective retention for a signal, optionally scoped to an app.
    // If appID is empty, returns the platform-level default.
    Resolve(ctx context.Context, signal SignalType, appID string) (EffectiveRetention, error)

    // ResolveAll returns retention for all signals, optionally scoped to an app.
    ResolveAll(ctx context.Context, appID string) (map[SignalType]EffectiveRetention, error)
}

// EffectiveRetention holds the resolved retention with provenance metadata.
type EffectiveRetention struct {
    Duration   time.Duration `json:"duration"`
    Source     string        `json:"source"`     // "app_override" | "platform_default" | "builtin"
    MaxAllowed time.Duration `json:"maxAllowed"` // upper bound enforced by platform
    Clamped    bool          `json:"clamped"`    // true if original request exceeded max
}
```

**Chain-of-Responsibility 实现**:

```go
// retentionResolverChain implements RetentionResolver with ordered fallback.
type retentionResolverChain struct {
    store    RetentionStore    // per-app override store
    platform *RetentionConfig // platform-level config
}

func (r *retentionResolverChain) Resolve(ctx context.Context, signal SignalType, appID string) (EffectiveRetention, error) {
    // 1. Try per-app override
    if appID != "" {
        if override, err := r.store.GetForApp(ctx, appID, signal); err == nil && override != nil {
            return r.clamp(signal, *override, "app_override"), nil
        }
    }
    // 2. Platform default
    dur := r.platformDefault(signal)
    if dur > 0 {
        return r.clamp(signal, dur, "platform_default"), nil
    }
    // 3. Builtin fallback
    return r.clamp(signal, builtinDefault(signal), "builtin"), nil
}

// clamp ensures retention does not exceed platform max.
func (r *retentionResolverChain) clamp(signal SignalType, dur time.Duration, source string) EffectiveRetention {
    max := r.platformMax(signal)
    clamped := dur > max && max > 0
    if clamped { dur = max }
    return EffectiveRetention{Duration: dur, Source: source, MaxAllowed: max, Clamped: clamped}
}
```

#### 7.3.3 RetentionStore — 策略持久化

```go
// RetentionStore abstracts the persistence of retention policies.
// Decoupled from how policies are stored (config file, DB, KV store, etc.)
//
// Design: ISP — only policy CRUD, no scheduling/execution logic.
type RetentionStore interface {
    // Get returns the platform-level retention for a signal.
    Get(ctx context.Context, signal SignalType) (*time.Duration, error)

    // Set updates the platform-level retention for a signal.
    Set(ctx context.Context, signal SignalType, retention time.Duration) error

    // GetForApp returns the per-app override (nil if no override exists).
    GetForApp(ctx context.Context, appID string, signal SignalType) (*time.Duration, error)

    // SetForApp sets a per-app retention override.
    SetForApp(ctx context.Context, appID string, signal SignalType, retention time.Duration) error

    // DeleteForApp removes a per-app override, falling back to platform default.
    DeleteForApp(ctx context.Context, appID string, signal SignalType) error

    // ListAppOverrides returns all apps that have custom retention settings.
    ListAppOverrides(ctx context.Context) ([]AppRetentionEntry, error)
}

// AppRetentionEntry represents one app's retention overrides.
type AppRetentionEntry struct {
    AppID     string                      `json:"appId"`
    Overrides map[SignalType]time.Duration `json:"overrides"`
}
```

#### 7.3.4 UsageReporter — 存储观测

```go
// UsageReporter provides storage usage information in a backend-agnostic way.
// The Scheduler and UI consume this interface without knowing the storage type.
type UsageReporter interface {
    // GetUsage returns current storage usage.
    GetUsage(ctx context.Context) (*StorageUsage, error)

    // GetTrend returns historical usage snapshots within the time range.
    GetTrend(ctx context.Context, from, to time.Time) ([]UsageSnapshot, error)
}

// StorageUsage represents current storage resource consumption.
type StorageUsage struct {
    TotalBytes     int64                 `json:"totalBytes"`
    UsedBytes      int64                 `json:"usedBytes"`
    AvailableBytes int64                 `json:"availableBytes"`
    BySignal       map[SignalType]int64  `json:"bySignal,omitempty"`
    ByApp          map[string]int64      `json:"byApp,omitempty"` // optional
    UsageRatio     float64               `json:"usageRatio"`      // usedBytes/totalBytes
}

// UsageSnapshot is a point-in-time storage usage record.
type UsageSnapshot struct {
    Timestamp  time.Time               `json:"timestamp"`
    TotalBytes int64                   `json:"totalBytes"`
    UsedBytes  int64                   `json:"usedBytes"`
    BySignal   map[SignalType]int64    `json:"bySignal,omitempty"`
}
```

#### 7.3.5 AuditEmitter — 审计事件发射

```go
// AuditEmitter emits lifecycle audit events.
// Implementations can log to structured logger, write to storage, or send webhooks.
//
// Design: ISP + SRP — audit is a separate concern from execution.
type AuditEmitter interface {
    Emit(ctx context.Context, event LifecycleEvent)
}

// LifecycleEvent is an immutable record of a lifecycle operation.
type LifecycleEvent struct {
    Timestamp time.Time       `json:"timestamp"`
    Action    LifecycleAction `json:"action"`
    Signal    SignalType       `json:"signal"`
    AppID     string           `json:"appId,omitempty"`
    Operator  string           `json:"operator"`  // "scheduler" | "api:admin" | "api:{user}"
    Input     any              `json:"input"`     // action-specific input (e.g., before time)
    Result    any              `json:"result"`    // action-specific result (e.g., PurgeResult)
    DryRun   bool             `json:"dryRun"`
    Error     string           `json:"error,omitempty"`
}

type LifecycleAction string
const (
    ActionAutoPurge    LifecycleAction = "auto_purge"
    ActionManualPurge  LifecycleAction = "manual_purge"
    ActionSetRetention LifecycleAction = "set_retention"
    ActionEstimate     LifecycleAction = "estimate"
)
```

### 7.4 LifecycleScheduler — 编排引擎

Scheduler 是纯粹的**编排器**（Orchestrator），自身不包含任何存储特定逻辑。
它组合上述接口完成"决策 → 执行 → 报告"闭环。

```go
// LifecycleScheduler orchestrates periodic data lifecycle operations.
// It depends ONLY on abstractions (DIP), making it testable and provider-agnostic.
//
// Responsibilities (SRP):
//   1. Periodic tick management
//   2. Orchestrate: resolve retention → compute cutoff → invoke purger → audit
//
// NOT responsible for: HOW to purge, WHERE policies are stored, HOW to measure usage.
type LifecycleScheduler struct {
    resolver RetentionResolver
    purger   LifecyclePurger
    usage    UsageReporter
    audit    AuditEmitter
    config   SchedulerConfig
    logger   *zap.Logger

    // Internal state
    ticker   *time.Ticker
    stopCh   chan struct{}
    wg       sync.WaitGroup

    // Usage trend buffer (ring buffer, in-memory)
    trendMu  sync.RWMutex
    trendBuf []UsageSnapshot
}

// SchedulerConfig holds scheduler behavior configuration.
// It is completely decoupled from any storage-specific settings.
type SchedulerConfig struct {
    Enabled  bool          `mapstructure:"enabled"`
    Interval time.Duration `mapstructure:"interval"` // default: 1h
    DryRun   bool          `mapstructure:"dry_run"`  // preview only, no delete

    // Alert thresholds
    UsageWarningRatio  float64 `mapstructure:"usage_warning_ratio"`  // default: 0.75
    UsageCriticalRatio float64 `mapstructure:"usage_critical_ratio"` // default: 0.90

    // TrendBufferSize is how many snapshots to keep (default: 168 = 7d @ 1h)
    TrendBufferSize int `mapstructure:"trend_buffer_size"`
}
```

#### 核心调度逻辑

```go
func (s *LifecycleScheduler) runCycle(ctx context.Context) {
    // Phase 1: Collect usage snapshot (观测)
    s.collectUsageSnapshot(ctx)

    // Phase 2: Check capacity alerts (告警)
    s.evaluateAlerts(ctx)

    // Phase 3: For each signal, resolve retention and purge expired data (清理)
    for _, signal := range []SignalType{SignalTrace, SignalMetric, SignalLog} {
        s.purgeSignal(ctx, signal)
    }
}

func (s *LifecycleScheduler) purgeSignal(ctx context.Context, signal SignalType) {
    // Step 1: Resolve effective retention (platform level)
    retention, err := s.resolver.Resolve(ctx, signal, "" /* platform */)
    if err != nil {
        s.logger.Error("Failed to resolve retention", zap.String("signal", string(signal)), zap.Error(err))
        return
    }

    // Step 2: Compute cutoff time
    cutoff := time.Now().Add(-retention.Duration)

    // Step 3: Check if there's data to purge (avoid unnecessary operations)
    boundary, err := s.purger.GetDataBoundary(ctx, signal)
    if err != nil || boundary.IsEmpty || boundary.OldestAt == nil || !boundary.OldestAt.Before(cutoff) {
        return // nothing to purge
    }

    // Step 4: Execute or estimate
    if s.config.DryRun {
        estimate, _ := s.purger.EstimatePurge(ctx, signal, cutoff)
        s.audit.Emit(ctx, LifecycleEvent{
            Action: ActionEstimate, Signal: signal, Operator: "scheduler",
            Input: cutoff, Result: estimate, DryRun: true,
        })
        return
    }

    result, err := s.purger.PurgeExpired(ctx, signal, cutoff)
    s.audit.Emit(ctx, LifecycleEvent{
        Action: ActionAutoPurge, Signal: signal, Operator: "scheduler",
        Input: cutoff, Result: result, Error: errStr(err),
    })
}
```

#### Per-App 清理扩展

```go
func (s *LifecycleScheduler) purgeByApps(ctx context.Context) {
    // Get all apps with custom retention
    store := s.resolver.(interface{ Store() RetentionStore }).Store()
    entries, err := store.ListAppOverrides(ctx)
    if err != nil { return }

    for _, entry := range entries {
        for signal, retention := range entry.Overrides {
            cutoff := time.Now().Add(-retention)
            result, err := s.purger.PurgeByApp(ctx, entry.AppID, signal, cutoff)
            s.audit.Emit(ctx, LifecycleEvent{
                Action: ActionAutoPurge, Signal: signal, AppID: entry.AppID,
                Operator: "scheduler", Input: cutoff, Result: result, Error: errStr(err),
            })
        }
    }
}
```

### 7.5 Provider 实现策略

每个 Provider 实现 `LifecyclePurger` 时，选择**自身最高效**的清理策略。
这是**策略模式**的经典应用 — 统一接口，不同算法。

#### Elasticsearch

```go
// esPurger implements LifecyclePurger with ES-optimized strategies.
// Strategy selection: prefer index deletion > delete_by_query
type esPurger struct {
    client *Client
    config *ElasticsearchConfig
    logger *zap.Logger
}

func (p *esPurger) PurgeExpired(ctx context.Context, signal SignalType, before time.Time) (*PurgeResult, error) {
    prefix := p.indexPrefix(signal)

    // Strategy 1: Delete entire date-based indices (O(1) per index, immediate space reclaim)
    deletedIndices, freedBytes, err := p.deleteExpiredIndices(ctx, prefix, before)
    if err != nil {
        // Strategy 2 (fallback): delete_by_query for partial-day data
        p.logger.Warn("Index deletion failed, falling back to delete_by_query", zap.Error(err))
        return p.deleteByQuery(ctx, prefix, signal, before)
    }

    return &PurgeResult{
        DeletedCount: deletedIndices,
        FreedBytes:   freedBytes,
        Message:      fmt.Sprintf("deleted %d expired indices", deletedIndices),
    }, nil
}

// deleteExpiredIndices leverages our date-based index naming convention:
//   {prefix}-{appID}-{yyyy.MM.dd}
// Parse the date suffix, compare with cutoff, delete entire index.
func (p *esPurger) deleteExpiredIndices(ctx context.Context, prefix string, before time.Time) (int64, int64, error) {
    // ... implementation uses p.client.ListIndices + date parsing + p.client.DeleteIndices
}
```

#### PostgreSQL

```go
// pgPurger implements LifecyclePurger with PG-optimized strategies.
// Strategy: DROP PARTITION (if time-partitioned) > DELETE WHERE
type pgPurger struct {
    pool   *pgxpool.Pool
    config *PostgreSQLConfig
    logger *zap.Logger
}

func (p *pgPurger) PurgeExpired(ctx context.Context, signal SignalType, before time.Time) (*PurgeResult, error) {
    table := p.tableName(signal)

    // Strategy 1: DROP old partitions (instant, no dead tuples)
    if p.config.UseTimescaleDB {
        return p.dropChunks(ctx, table, before)
    }
    if p.hasNativePartitions(ctx, table) {
        return p.dropPartitions(ctx, table, before)
    }

    // Strategy 2 (fallback): DELETE WHERE timestamp < cutoff
    return p.deleteByTimestamp(ctx, table, before)
}
```

#### MongoDB (future)

```go
// mongoPurger implements LifecyclePurger with MongoDB-optimized strategies.
// Strategy: TTL index (automatic) + drop time-based collections
type mongoPurger struct { ... }

func (p *mongoPurger) PurgeExpired(ctx context.Context, signal SignalType, before time.Time) (*PurgeResult, error) {
    // MongoDB TTL indexes handle automatic expiration.
    // For immediate cleanup: drop old time-bucketed collections.
    return p.dropOldCollections(ctx, signal, before)
}
```

### 7.6 与现有 StorageAdmin 的关系

现有的 `StorageAdmin` 接口已定义了 `Purge`/`PurgeByApp`/`GetDiskUsage`/`SetRetention`。
新设计**不替换**它，而是**对齐并增强**：

```
StorageAdmin (existing, for API Handler consumption)
    ├─ GetRetention()      → delegates to RetentionResolver.ResolveAll(ctx, "")
    ├─ SetRetention()      → delegates to RetentionStore.Set()
    ├─ Purge()             → delegates to LifecyclePurger.PurgeExpired()
    ├─ PurgeByApp()        → delegates to LifecyclePurger.PurgeByApp()
    ├─ GetDiskUsage()      → delegates to UsageReporter.GetUsage()
    └─ GetStatus()         → (unchanged, health check)

New interfaces (for Scheduler internal use):
    ├─ LifecyclePurger     → backend-specific purge strategies
    ├─ RetentionResolver   → policy resolution chain
    ├─ RetentionStore      → policy persistence
    ├─ UsageReporter       → storage metrics
    └─ AuditEmitter        → event emission
```

`StorageAdmin` 成为面向外部（API Handler）的**门面（Facade）**，内部委托给精细的子接口。
新增的接口仅在 lifecycle 包内部使用，不暴露给 API 层。

### 7.7 包结构与内聚性

```
observabilitystorageext/
├── provider.go              # Provider + Writer/Reader/Admin interfaces (existing)
├── config.go                # Config structs (existing, add SchedulerConfig)
├── extension.go             # Extension lifecycle (existing, add scheduler start/stop)
├── types.go                 # OTel types (existing)
├── reader_adapter.go        # Adapter: ES/PG → public interfaces (existing)
│
├── lifecycle/               # NEW: self-contained lifecycle management package
│   ├── interfaces.go        # LifecyclePurger, RetentionResolver, RetentionStore,
│   │                        # UsageReporter, AuditEmitter (all abstractions)
│   ├── types.go             # PurgeEstimate, DataBoundary, EffectiveRetention,
│   │                        # LifecycleEvent, UsageSnapshot, SchedulerConfig
│   ├── scheduler.go         # LifecycleScheduler (orchestrator)
│   ├── scheduler_test.go    # Unit tests with mock interfaces
│   ├── resolver.go          # retentionResolverChain implementation
│   ├── resolver_test.go
│   ├── audit_logger.go      # AuditEmitter impl: zap structured logging
│   └── trend_buffer.go      # Ring buffer for UsageSnapshot storage
│
├── provider/
│   ├── elasticsearch/
│   │   ├── purger.go        # NEW: esPurger implements LifecyclePurger
│   │   ├── purger_test.go
│   │   ├── usage.go         # NEW: esUsageReporter implements UsageReporter
│   │   └── ... (existing files)
│   ├── postgresql/
│   │   ├── purger.go        # NEW: pgPurger implements LifecyclePurger
│   │   ├── usage.go         # NEW: pgUsageReporter implements UsageReporter
│   │   └── ...
│   └── ...
│
└── store/                   # NEW: RetentionStore implementations
    ├── config_store.go      # ConfigRetentionStore: reads from static config
    └── memory_store.go      # InMemoryRetentionStore: per-app overrides in memory
```

**内聚性分析**:
- `lifecycle/` 包只关注"什么时候清理什么" — 调度 + 策略解析 + 审计
- `provider/{es,pg}/purger.go` 只关注"用哪种后端命令执行清理"
- `store/` 只关注"策略数据存在哪"
- 三者通过接口解耦，可独立测试、独立演进

### 7.8 Extension 集成点

```go
// extension.go — Start() 中增加 Scheduler 启动
func (e *ObservabilityStorage) Start(ctx context.Context, _ component.Host) error {
    // ... existing provider start logic ...

    // Initialize lifecycle management (if enabled)
    if e.config.Scheduler.Enabled {
        e.scheduler = e.buildLifecycleScheduler()
        e.scheduler.Start(ctx)
    }
    return nil
}

func (e *ObservabilityStorage) buildLifecycleScheduler() *lifecycle.LifecycleScheduler {
    // Wire dependencies via constructor injection (DIP)
    return lifecycle.NewScheduler(
        lifecycle.WithResolver(e.buildRetentionResolver()),
        lifecycle.WithPurger(e.buildLifecyclePurger()),
        lifecycle.WithUsageReporter(e.buildUsageReporter()),
        lifecycle.WithAuditEmitter(lifecycle.NewZapAuditEmitter(e.logger)),
        lifecycle.WithConfig(e.config.Scheduler),
        lifecycle.WithLogger(e.logger),
    )
}

// buildLifecyclePurger returns the appropriate purger based on provider type.
// Factory Method pattern — Extension decides which impl to wire.
func (e *ObservabilityStorage) buildLifecyclePurger() lifecycle.LifecyclePurger {
    switch e.config.Type {
    case "elasticsearch":
        return elasticsearch.NewPurger(e.esProvider.Client(), e.config.Elasticsearch, e.logger)
    case "postgresql":
        return postgresql.NewPurger(e.pgProvider.Pool(), e.config.PostgreSQL, e.logger)
    case "hybrid":
        // Composite purger: delegate each signal to its designated backend
        return hybrid.NewCompositePurger(
            e.buildESPurger(),
            e.buildPGPurger(),
            e.config.Hybrid,
        )
    default:
        return lifecycle.NoOpPurger{} // safe fallback
    }
}
```

### 7.9 配置设计

```yaml
extensions:
  observability_storage:
    type: elasticsearch
    
    # === Storage Backend Config (existing) ===
    elasticsearch:
      addresses: ["http://elasticsearch:9200"]
      traces:  { index_prefix: otel-traces,  retention: 168h  }
      metrics: { index_prefix: otel-metrics, retention: 720h  }
      logs:    { index_prefix: otel-logs,    retention: 336h  }

    # === Platform Retention Constraints (existing) ===
    retention:
      default_trace:  168h     # 7 days
      default_metric: 720h     # 30 days
      default_log:    336h     # 14 days
      max_trace:      720h     # 30 days max
      max_metric:     2160h    # 90 days max
      max_log:        720h     # 30 days max

    # === Lifecycle Scheduler (NEW) ===
    scheduler:
      enabled: true
      interval: 1h             # check frequency
      dry_run: false           # set true for first-week observation
      usage_warning_ratio: 0.75
      usage_critical_ratio: 0.90
      trend_buffer_size: 168   # 7 days of hourly snapshots
```

### 7.10 可测试性设计

```go
// lifecycle/scheduler_test.go — 完全基于 mock 的单元测试

func TestScheduler_PurgesExpiredData(t *testing.T) {
    // Arrange: mock all dependencies
    mockPurger := &MockPurger{
        DataBoundary: &DataBoundary{OldestAt: timePtr(daysAgo(10))},
        PurgeResult:  &PurgeResult{DeletedCount: 100},
    }
    mockResolver := &MockResolver{
        Retention: EffectiveRetention{Duration: 7 * 24 * time.Hour, Source: "platform_default"},
    }
    mockUsage := &MockUsageReporter{Usage: &StorageUsage{UsedBytes: 1000}}
    mockAudit := &MockAuditEmitter{}

    sched := NewScheduler(
        WithPurger(mockPurger),
        WithResolver(mockResolver),
        WithUsageReporter(mockUsage),
        WithAuditEmitter(mockAudit),
        WithConfig(SchedulerConfig{Enabled: true, Interval: time.Minute}),
    )

    // Act
    sched.runCycle(context.Background())

    // Assert
    assert.Equal(t, 3, mockPurger.PurgeCalls) // 3 signals
    assert.Equal(t, 3, mockAudit.EmitCalls)
    assert.Equal(t, ActionAutoPurge, mockAudit.LastEvent.Action)
}

func TestScheduler_DryRunDoesNotDelete(t *testing.T) {
    mockPurger := &MockPurger{...}
    sched := NewScheduler(
        ...,
        WithConfig(SchedulerConfig{DryRun: true}),
    )
    sched.runCycle(context.Background())

    assert.Equal(t, 0, mockPurger.PurgeCalls)
    assert.Equal(t, 3, mockPurger.EstimateCalls)
}
```

### 7.11 API 扩展（补充已有路由）

| Method | Path | Handler | 说明 |
|--------|------|---------|------|
| GET | `/admin/retention` | handleGetRetention | 获取平台级保留策略 (✅ 已有) |
| PUT | `/admin/retention/{signal}` | handleSetRetention | 设置平台级保留策略 (✅ 已有) |
| POST | `/admin/purge/{signal}` | handlePurge | 手动清除 (✅ 已有) |
| GET | `/admin/disk-usage` | handleDiskUsage | 磁盘使用 (✅ 已有) |
| GET | `/admin/retention/app/{appID}` | handleGetAppRetention | **NEW**: 获取 App 有效策略 |
| PUT | `/admin/retention/app/{appID}/{signal}` | handleSetAppRetention | **NEW**: 设置 App 保留覆盖 |
| DELETE | `/admin/retention/app/{appID}/{signal}` | handleDeleteAppRetention | **NEW**: 删除覆盖 |
| GET | `/admin/usage-trend` | handleUsageTrend | **NEW**: 存储用量趋势 |
| POST | `/admin/purge/{signal}/estimate` | handlePurgeEstimate | **NEW**: 清理预览 |
| GET | `/admin/data-boundary` | handleDataBoundary | **NEW**: 各信号最早/最新数据时间 |

### 7.12 实施计划

#### Sprint 1: 核心抽象 + Scheduler + ES Purger

| 任务 | 说明 | 状态 |
|------|------|------|
| 创建 `lifecycle/` 包 | interfaces.go + types.go | ✅ 已完成 |
| 实现 `LifecycleScheduler` | scheduler.go — tick loop, orchestrate | ✅ 已完成 |
| 实现 `retentionResolverChain` | resolver.go — 链式解析 | ✅ 已完成 |
| 实现 `TrendBuffer` | trend_buffer.go — ring buffer | ✅ 已完成 |
| 实现 `ZapAuditEmitter` | audit_logger.go | ✅ 已完成 |
| 实现 `esPurger` | provider/elasticsearch/purger.go — 索引删除 + fallback | ✅ 已完成 |
| 实现 `esUsageReporter` | provider/elasticsearch/usage.go | ✅ 已完成 |
| 实现 `InMemoryRetentionStore` | lifecycle/retention_store_memory.go | ✅ 已完成 |
| Extension 集成 | extension.go — buildLifecycleScheduler + Start/Shutdown | ✅ 已完成 |
| 新增 `SchedulerConfig` | config.go | ✅ 已完成 |
| 单元测试 | scheduler_test.go + resolver_test.go + retention_store_memory_test.go + trend_buffer_test.go (全 mock, 覆盖率 97.4%) | ✅ 已完成 |
| 集成测试 | purger_test.go — httptest mock ES 验证索引删除/跳过/App隔离/多信号/边界 (12 test cases, -race pass) | ✅ 已完成 |

**验收**: Collector 启动后，scheduler 按 interval 自动清理过期索引，审计日志正常输出。
**进度**: Sprint 1 全部任务已完成 (2026-06-02)。单元测试覆盖率 97.4%，集成测试 12 用例全部通过（含 race detection）。

#### Sprint 2: Per-App 策略 + RetentionStore + API

| 任务 | 说明 |
|------|------|
| ~~实现 `InMemoryRetentionStore`~~ | ~~store/memory_store.go~~ (已在 Sprint 1 中实现: lifecycle/retention_store_memory.go) |
| 实现 `ConfigRetentionStore` | store/config_store.go — 从 YAML 读取 |
| Scheduler 增加 `purgeByApps()` | 遍历 app overrides 独立清理 |
| 新增 API routes | app retention CRUD + data-boundary + estimate |
| 前端 Per-App UI | StorageAdminPage 增加 App 保留策略表格 |

**验收**: App A 和 App B 可独立配置不同 retention，scheduler 按各自策略清理。

#### Sprint 3: 监控 + 趋势 + PG Purger

| 任务 | 说明 |
|------|------|
| ~~实现 `TrendBuffer`~~ | ~~lifecycle/trend_buffer.go — ring buffer~~ (已在 Sprint 1 中实现) |
| `/admin/usage-trend` API | 暴露 7 天趋势数据 |
| 容量告警 | evaluateAlerts() + webhook |
| 前端趋势图 | ECharts 折线图 |
| 实现 `pgPurger` | provider/postgresql/purger.go — DROP PARTITION |

**验收**: 前端展示 7 天趋势图，超阈值时有告警日志。

### 7.13 风险与缓解

| 风险 | 缓解 |
|------|------|
| Scheduler 误删 | DryRun 默认首周开启；`GetDataBoundary` 预检；审计全记录 |
| 接口过度抽象 | 初期只有 ES/PG 两个实现，接口契约通过集成测试验证 |
| Per-App store 持久化 | Sprint 1 用 InMemory（重启丢失），Sprint 2+ 可接入 DB/KV |
| 清理耗时阻塞 | Scheduler 单独 goroutine，context 超时控制，每轮有时间上限 |
| Provider 升级不兼容 | 接口版本化（`LifecyclePurgerV1`），旧实现可适配 |

### 7.14 设计 vs 原始方案对比

| 维度 | 原始方案 (v1) | 重构方案 (v2) |
|------|--------------|--------------|
| 耦合度 | Scheduler 直接引用 ES ILM / Index 命名 | Scheduler 只依赖 `LifecyclePurger` 接口 |
| 可扩展性 | 新增后端需修改 Scheduler | 新增后端只需实现接口 |
| 可测试性 | 需真实 ES 才能测 | 全 mock 单测 |
| 职责分离 | Scheduler 混合了策略解析+执行+审计 | 4 个独立接口，各司其职 |
| 策略灵活性 | 硬编码 "prefer_index_deletion" | 策略模式，Provider 自主选择最优算法 |
| Per-App | 作为 Scheduler 内部逻辑 | 独立 `RetentionStore` + `RetentionResolver` |

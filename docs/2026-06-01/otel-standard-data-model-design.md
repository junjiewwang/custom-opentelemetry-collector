# 统一观测数据模型 — 对齐 OpenTelemetry 协议规范

> **日期**: 2026-06-01  
> **状态**: ✅ 全部完成 — 后端 + 前端 + 端到端验证（2026-06-02）  
> **目标**: 前后端统一采用符合 OTel 标准的数据模型，替代 Jaeger/Prometheus 格式  

---

## 一、设计原则

1. **OTel 为唯一标准** — 数据从 OTel Agent 进来就是 OTel 格式，存储是 OTel 格式，查询返回也是 OTel 格式，前端渲染也是 OTel 格式
2. **JSON 字段名遵循 OTLP JSON Protobuf Encoding** — 使用 camelCase（`traceId` 而非 `trace_id`），与 OTel 官方 JSON 导出格式一致
3. **查询接口自定义但模型标准** — Query API 不照搬 OTLP（那是传输协议），但返回的数据结构中 Span/Metric/Log 本身遵循 OTel 数据模型
4. **不依赖第三方格式** — 不包装成 Jaeger Response 或 Prometheus Response

---

## 二、OTel 标准数据模型（参考 OTLP Proto v1.10）

### 2.1 Trace 数据模型层级

```
TracesData
  └─ ResourceSpans[]
       ├─ resource: Resource { attributes: KeyValue[] }
       └─ scopeSpans: ScopeSpans[]
            ├─ scope: InstrumentationScope { name, version }
            └─ spans: Span[]
                 ├─ traceId (hex string, 32 chars)
                 ├─ spanId (hex string, 16 chars)
                 ├─ parentSpanId (hex string or "")
                 ├─ name (operation name)
                 ├─ kind (SPAN_KIND_*)
                 ├─ startTimeUnixNano (string)
                 ├─ endTimeUnixNano (string)
                 ├─ attributes: KeyValue[]
                 ├─ events: Event[]
                 ├─ links: Link[]
                 └─ status: { code, message }
```

### 2.2 Metric 数据模型层级

```
MetricsData
  └─ ResourceMetrics[]
       ├─ resource: Resource { attributes: KeyValue[] }
       └─ scopeMetrics: ScopeMetrics[]
            ├─ scope: InstrumentationScope
            └─ metrics: Metric[]
                 ├─ name
                 ├─ description
                 ├─ unit
                 └─ data: Gauge | Sum | Histogram | Summary
                      └─ dataPoints: NumberDataPoint[] | HistogramDataPoint[]
                           ├─ attributes: KeyValue[]
                           ├─ startTimeUnixNano
                           ├─ timeUnixNano
                           └─ value (asDouble / asInt)
```

### 2.3 Log 数据模型层级

```
LogsData
  └─ ResourceLogs[]
       ├─ resource: Resource { attributes: KeyValue[] }
       └─ scopeLogs: ScopeLogs[]
            ├─ scope: InstrumentationScope
            └─ logRecords: LogRecord[]
                 ├─ timeUnixNano
                 ├─ observedTimeUnixNano
                 ├─ severityNumber (1-24)
                 ├─ severityText ("ERROR", "WARN", etc.)
                 ├─ body: AnyValue
                 ├─ attributes: KeyValue[]
                 ├─ traceId
                 └─ spanId
```

---

## 三、API 查询接口设计

API 路径和参数是我们自定义的（查询不是 OTLP 的职责），但**返回的数据结构遵循 OTel 模型**。

### 3.1 Trace API

```
GET  /api/v2/observability/traces
     ?service={name}&operation={name}&tags=key:value
     &start={unixMs}&end={unixMs}&minDuration={duration}&maxDuration={duration}
     &limit={n}&offset={n}&appId={appId}

GET  /api/v2/observability/traces/{traceId}
GET  /api/v2/observability/traces/services?start=&end=
GET  /api/v2/observability/traces/services/{service}/operations?start=&end=
GET  /api/v2/observability/dependencies?endTs=&lookback=
```

#### Response 模型

```typescript
// SearchTraces 响应
interface TraceSearchResponse {
  traces: TraceData[];        // OTel 标准格式
  total: number;
}

// 单条 Trace（扁平化的 OTel 格式，便于 UI 渲染）
interface TraceData {
  traceId: string;            // 32 char hex
  spans: OTelSpan[];
  // 衍生字段（由后端计算，方便前端直接使用）
  durationUs: number;         // 总耗时（微秒）
  serviceCount: number;
  spanCount: number;
  rootServiceName?: string;
  rootSpanName?: string;
}

// OTel Span（遵循 OTLP 规范字段名）
interface OTelSpan {
  traceId: string;
  spanId: string;
  parentSpanId: string;
  name: string;                        // operation name
  kind: SpanKind;
  startTimeUnixNano: string;           // 纳秒时间戳字符串
  endTimeUnixNano: string;
  attributes: KeyValue[];              // OTel 标准 KeyValue
  events: SpanEvent[];
  links: SpanLink[];
  status: SpanStatus;
  // 衍生字段（扁平化展示用，从 resource 提取）
  serviceName: string;
  resource: KeyValue[];
}

// OTel 标准枚举
type SpanKind = 'SPAN_KIND_UNSPECIFIED' | 'SPAN_KIND_INTERNAL' | 'SPAN_KIND_SERVER' 
             | 'SPAN_KIND_CLIENT' | 'SPAN_KIND_PRODUCER' | 'SPAN_KIND_CONSUMER';

type StatusCode = 'STATUS_CODE_UNSET' | 'STATUS_CODE_OK' | 'STATUS_CODE_ERROR';

interface SpanStatus {
  code: StatusCode;
  message?: string;
}

// OTel KeyValue（标准属性模型）
interface KeyValue {
  key: string;
  value: AnyValue;
}

interface AnyValue {
  stringValue?: string;
  intValue?: string;          // int64 as string
  doubleValue?: number;
  boolValue?: boolean;
  arrayValue?: { values: AnyValue[] };
  kvlistValue?: { values: KeyValue[] };
  bytesValue?: string;        // base64
}

interface SpanEvent {
  timeUnixNano: string;
  name: string;
  attributes: KeyValue[];
}

interface SpanLink {
  traceId: string;
  spanId: string;
  attributes: KeyValue[];
  traceState?: string;
}
```

#### Services / Operations 响应

```typescript
// GetServices
interface ServicesResponse {
  data: ServiceInfo[];
}
interface ServiceInfo {
  name: string;
  spanCount?: number;        // 可选：该 service 的 span 数量
}

// GetOperations
interface OperationsResponse {
  data: OperationInfo[];
}
interface OperationInfo {
  name: string;
  spanKind: SpanKind;
}

// Dependencies
interface DependenciesResponse {
  data: DependencyLink[];
}
interface DependencyLink {
  parent: string;
  child: string;
  callCount: number;
}
```

### 3.2 Metric API

```
GET  /api/v2/observability/metrics/query
     ?metric={name}&service={name}&time={unixMs}&labels=key:value,key:value&appId={appId}

GET  /api/v2/observability/metrics/query_range
     ?metric={name}&service={name}&start={unixMs}&end={unixMs}&step={duration}&labels=key:value&appId={appId}

GET  /api/v2/observability/metrics/names?start=&end=
GET  /api/v2/observability/metrics/labels?start=&end=
GET  /api/v2/observability/metrics/labels/{labelName}/values?start=&end=
```

#### Response 模型

```typescript
// 即时查询结果
interface MetricQueryResponse {
  data: MetricDataPoint[];
}

interface MetricDataPoint {
  metric: string;                       // metric name
  labels: Record<string, string>;       // OTel attributes 展开为 flat labels
  value: number;
  timeUnixNano: string;                 // 纳秒时间戳
}

// 范围查询结果
interface MetricRangeResponse {
  data: MetricSeries[];
}

interface MetricSeries {
  metric: string;
  labels: Record<string, string>;
  values: MetricTimeValue[];
}

interface MetricTimeValue {
  timeUnixNano: string;
  value: number;
}

// 名称/标签列表
interface MetricNamesResponse {
  data: string[];
}

interface MetricLabelsResponse {
  data: string[];
}

interface MetricLabelValuesResponse {
  data: string[];
}
```

### 3.3 Log API（当前已对齐，小调整）

```
GET  /api/v2/observability/logs?query=&service=&severity=&traceId=&spanId=&start=&end=&limit=&offset=&appId=
GET  /api/v2/observability/logs/{logId}/context?lines=
GET  /api/v2/observability/logs/fields?start=&end=
GET  /api/v2/observability/logs/stats?service=&start=&end=&groupBy=
```

#### Response 模型（当前基本对齐 OTel LogRecord，字段名统一为 camelCase）

```typescript
interface OTelLogRecord {
  id: string;                           // ES doc ID
  timeUnixNano: string;
  observedTimeUnixNano?: string;
  severityNumber: number;               // 1-24 (OTel standard)
  severityText: string;                 // "ERROR", "WARN", "INFO", "DEBUG"
  body: string;                         // AnyValue 简化为 string（大多数场景）
  traceId?: string;
  spanId?: string;
  attributes: KeyValue[];               // OTel 标准 KeyValue
  resource: KeyValue[];                 // OTel 标准 KeyValue
  // 衍生字段
  serviceName: string;
  appId?: string;
}
```

---

## 四、与当前实现的差异对比

### 4.1 后端 types.go 需要调整

| 当前字段 | OTel 标准 | 改动说明 |
|----------|-----------|---------|
| `Span.Attributes map[string]any` | `Attributes []KeyValue` | 改为 OTel 标准 KeyValue 数组 |
| `Span.Resource map[string]any` | `Resource []KeyValue` | 改为 OTel 标准 KeyValue 数组 |
| `Span.StartTime time.Time` | `StartTimeUnixNano string` | 改为纳秒时间戳字符串 |
| `Span.EndTime time.Time` | `EndTimeUnixNano string` | 同上 |
| `Span.DurationUS int64` | 由前端计算 `end - start` | 可保留为衍生字段 |
| `Span.SpanKind string` | `Kind SpanKind` | 使用 OTel 枚举值 |
| `Span.StatusCode string` | `Status.Code StatusCode` | 使用标准 Status 结构 |
| `Span.OperationName string` | `Name string` | OTel 标准用 `name` |
| `Span.Events []SpanEvent` | 基本一致，时间改为纳秒 | 小改 |
| `MetricDataPoint.Time time.Time` | `TimeUnixNano string` | 统一纳秒时间戳 |
| `LogRecord.Timestamp time.Time` | `TimeUnixNano string` | 统一纳秒时间戳 |
| `LogRecord.Severity string` | `SeverityText string` | 字段名对齐 |
| `LogRecord.Attributes map[string]any` | `Attributes []KeyValue` | 改为 OTel KeyValue |

### 4.2 前端 types/ 需要调整

| 当前 | 目标 | 说明 |
|------|------|------|
| `types/trace.ts` (Jaeger 格式) | 新 `types/otel-trace.ts` (OTel 格式) | 完全重写 |
| `types/metric.ts` (Prometheus 格式) | 新 `types/otel-metric.ts` (OTel 格式) | 完全重写 |
| `types/log.ts` | 小调整字段名为 camelCase | 基本保持 |
| `api/client.ts` 的 Trace/Metric 方法 | 更新返回类型 | 对齐新模型 |

### 4.3 前端 Pages 需要调整

| 页面 | 改动范围 |
|------|----------|
| `TracesPage.tsx` | 数据解析逻辑（`JaegerTrace` → `TraceData`） |
| `TraceDetail.tsx` | Span 树构建逻辑（`processID` → `serviceName`） |
| `MetricsPage.tsx` | 图表数据映射（Prometheus matrix → `MetricSeries`） |
| `LogsPage.tsx` | 小改（字段名 camelCase） |
| `StorageAdminPage.tsx` | 无改动 |

---

## 五、分步实施计划

### Phase A: 后端 Response 模型对齐 OTel（Go 代码）

1. **重新定义 `types.go`** — Span/Trace/Metric/Log 的 JSON 输出遵循 OTel 规范字段名
2. **新增 `otel_model.go`** — 定义 `KeyValue`/`AnyValue`/`SpanKind`/`StatusCode` 标准类型
3. **修改 Reader Adapter** — 从 ES 读取的原始数据转换为 OTel 标准模型
4. **修改 V2 Handler** — 确保 Response 结构符合上述 API 设计

### Phase B: 前端类型系统重建

1. **新建 `types/otel.ts`** — 公共 OTel 类型（KeyValue, AnyValue, SpanKind 等）
2. **重写 `types/trace.ts`** — 替换 Jaeger 类型为 OTel 类型
3. **重写 `types/metric.ts`** — 替换 Prometheus 类型为自定义 OTel 格式
4. **小改 `types/log.ts`** — 字段名对齐

### Phase C: 前端页面适配

1. **TracesPage + TraceDetail** — 适配新的 OTel Span 数据结构
2. **MetricsPage** — 适配新的 MetricSeries 数据结构
3. **LogsPage** — 小改
4. **API Client** — 更新方法签名和返回类型

### Phase D: 端到端验证

1. 发送 OTel 数据 → ES 存储
2. API 查询 → 返回 OTel 标准格式
3. 前端渲染 → 正确展示 Trace 时间轴/Metric 图表/Log 列表

---

## 六、关于"衍生字段"的设计哲学

OTel 原始模型是**层级嵌套的**（Resource → Scope → Span），但 UI 渲染需要**扁平化**的数据。

我们的策略是：
- **核心字段严格遵循 OTel 命名和类型**（`traceId`, `spanId`, `startTimeUnixNano`, `attributes: KeyValue[]`）
- **额外添加衍生字段**方便前端（`serviceName`, `durationUs`, `rootServiceName`）
- **衍生字段由后端计算**，前端不需要遍历 resource.attributes 去找 `service.name`

```typescript
interface OTelSpan {
  // ===== OTel 标准字段 =====
  traceId: string;
  spanId: string;
  parentSpanId: string;
  name: string;
  kind: SpanKind;
  startTimeUnixNano: string;
  endTimeUnixNano: string;
  attributes: KeyValue[];
  events: SpanEvent[];
  links: SpanLink[];
  status: SpanStatus;
  resource: KeyValue[];

  // ===== 衍生字段（后端预计算） =====
  serviceName: string;         // 从 resource.attributes["service.name"] 提取
  durationNano: string;        // endTime - startTime
}
```

这样既保持了 OTel 标准兼容性（核心字段可直接序列化为 OTLP JSON），又方便前端渲染。

---

## 七、与业界对标

| 平台 | Trace 格式 | Metric 格式 | 说明 |
|------|-----------|-------------|------|
| **Grafana Tempo** | OTLP JSON + 衍生字段 | — | Trace 查询返回 OTLP 格式 |
| **Jaeger (v2)** | 迁移中 → OTLP | — | Jaeger v2 全面拥抱 OTel |
| **SigNoz** | OTLP 原生 | OTLP 原生 | 前后端完全 OTel 模型 |
| **Elastic APM** | ECS → OTel 对齐中 | — | 8.x 开始 OTel 原生 |
| **我们（目标）** | OTLP JSON + 衍生字段 | OTel-based + 简化 | 与 Tempo/SigNoz 对齐 |

---

## 八、实施进展

### Phase A: 后端 Response 模型对齐 — ✅ 已完成（2026-06-01）

- `types.go`: 全面采用 OTel 标准字段命名（`KeyValue[]`, `startTimeUnixNano`, `SpanKind` 枚举等）
- `reader_adapter.go`: ES 原始数据转换为 OTel 标准模型
- `pg_reader_adapter.go`: PostgreSQL 存储适配器对齐

### Phase B: 前端类型系统重建 — ✅ 已完成（2026-06-01）

- `types/trace.ts`: 完全重写为 OTel 标准（`OTelTrace`, `OTelSpan`, `KeyValue`, `AnyValue`, 枚举类型）
- `types/metric.ts`: 替换 Prometheus 类型为结构化 OTel 格式（`MetricRangeResult`, `MetricSeries`）
- `types/log.ts`: 字段名对齐 camelCase（`timeUnixNano`, `severityText`, `serviceName`, `attributes: KeyValue[]`）
- `api/client.ts`: 更新所有 Trace/Metric/Log API 方法签名和返回类型
- `utils/trace.ts`: 完全重写工具函数（纳秒转换、KeyValue 处理、span tree 构建）
- `utils/metric.ts`: 从 PromQL 转换为结构化 metric 查询模型

### Phase C: 前端页面适配 — ✅ 已完成（2026-06-01）

| 文件 | 改动 | 状态 |
|------|------|------|
| `TracesPage.tsx` | `traceID→traceId`, duration/timestamp 格式化对齐纳秒 | ✅ |
| `TraceDetail.tsx` | 全面重写 (~680 行)，去除 `processes` 字典，使用 `span.serviceName` | ✅ |
| `TraceStatisticsView.tsx` | 全面重写，使用 OTel 类型 | ✅ |
| `TraceSpanTableView.tsx` | 全面重写，FlatSpan + 纳秒排序 | ✅ |
| `TraceFlamegraphView.tsx` | 全面重写，直接使用 `span.serviceName` | ✅ |
| `TraceGraphView.tsx` | 全面重写，`parentSpanId` 替代 `references[]` | ✅ |
| `TraceDiffView.tsx` | 全面重写，从 JaegerTrace 迁移到 OTelTrace | ✅ |
| `TraceComparePage.tsx` | 迁移到 OTelTrace 类型 | ✅ |
| `MetricsPage.tsx` | 从 PromQL 迁移到结构化 metric 查询 | ✅ |
| `LogsPage.tsx` | snake_case → camelCase, `KeyValue[]` attributes | ✅ |
| `TimeSeriesChart.tsx` | 无需修改（已使用正确的 `ChartSeries` 类型） | ✅ |

### Phase D: 端到端验证 — ✅ 已完成（2026-06-02）

**部署环境**: Minikube K8s 集群，服务地址 `http://custom-otlp-collector.default.svc.cluster.local:8088/`

**配置要点**:
- Admin Extension 启用 V2 Storage Extension 模式: `storage_extension: observability_storage`
- ES Reader 层移除 AppID 强制校验，支持 Admin 模式全局查询（`indexPattern("")` → `otel-traces-*` 通配）
- Handler 层添加可选 `app_id` 查询参数，保留 app 级别数据隔离能力

**验证结果**:

| 验证项 | 接口 | 结果 | 备注 |
|--------|------|------|------|
| 数据接入 | `POST /v1/traces` (4318) | ✅ | 多 Service（payment, order, e2e-test）OTLP 数据成功写入 |
| 数据接入 | `POST /v1/logs` (4318) | ✅ | 关联 traceId/spanId 的日志成功写入 |
| 数据接入 | `POST /v1/metrics` (4318) | ✅ | Counter 类型 metric 成功写入 |
| Trace 搜索 | `GET /api/v2/observability/traces` | ✅ | 返回 3 条 traces，含跨服务 trace（2 services, 2 spans） |
| Trace 详情 | `GET /api/v2/observability/traces/{id}` | ✅ | 完整 span 树，OTel 标准 JSON 格式 |
| Service 列表 | `GET /api/v2/observability/traces/services` | ✅ | 返回 3 个 services |
| Operation 列表 | `GET .../services/{name}/operations` | ✅ | 返回 operation name + spanKind |
| 服务依赖 | `GET /api/v2/observability/dependencies` | ✅ | 自动计算 order-service → payment-service |
| Metric 名称 | `GET /api/v2/observability/metrics/names` | ✅ | 返回 `http_requests_total`, `payment_total` |
| Metric 查询 | `GET /api/v2/observability/metrics/query` | ✅ | instant query 返回 labels + value |
| Metric 范围 | `GET /api/v2/observability/metrics/query_range` | ✅ | range query 返回 time series |
| Log 搜索 | `GET /api/v2/observability/logs` | ✅ | 返回 3 条日志，含 severity/traceId/serviceName |
| Log 统计 | `GET /api/v2/observability/logs/stats` | ✅ | severity/service 分布 + 时间直方图 |
| 存储状态 | `GET /api/v2/observability/admin/status` | ✅ | ES 集群 green，2 节点，413 分片 |
| 前端 WebUI | `GET /ui/` | ✅ | 页面可访问，200 OK |

**数据格式验证**:
- ✅ `traceId` camelCase（非 `trace_id`）
- ✅ `spans[].name` 作为 operation name
- ✅ `spans[].kind`: `"SPAN_KIND_SERVER"` / `"SPAN_KIND_CLIENT"` 枚举字符串
- ✅ `spans[].startTimeUnixNano`: 纳秒字符串
- ✅ `spans[].attributes`: `KeyValue[]` 格式（`{key, value: {stringValue/doubleValue/intValue}}`）
- ✅ `spans[].status`: `{code: "STATUS_CODE_OK"}`
- ✅ `spans[].resource`: `KeyValue[]` 格式
- ✅ `durationNano`: trace/span 级别持续时间（纳秒字符串）
- ✅ `spanCount` / `serviceCount` / `rootServiceName` / `rootSpanName` 衍生字段

**前端类型兼容性**: API 返回数据与 `types/trace.ts` 中 `OTelTrace`/`OTelSpan` TypeScript 接口 100% 匹配。

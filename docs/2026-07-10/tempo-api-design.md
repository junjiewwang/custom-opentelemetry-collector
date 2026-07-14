# Grafana Tempo 数据源兼容 API 设计文档

## 1. 需求背景

Grafana 支持 Tempo 作为原生 Trace 数据源。通过实现 Tempo HTTP API 兼容层，可以让 Grafana 直接将本系统配置为 Tempo 数据源，实现 Trace 的搜索、查看和标签浏览，无需任何额外的 Tempo 实例部署。

## 2. 方案选择

### 2.1 候选方案对比

| 方案 | 描述 | 优点 | 缺点 |
|------|------|------|------|
| **A. 自行实现适配层** | 实现 Tempo HTTP API 响应格式，复用现有 TraceReader | 零外部依赖、与 Prometheus handler 架构一致、完全可控 | 需要手动实现 OTLP JSON 序列化 |
| B. 引入 Tempo 依赖 | go.mod 引入 grafana/tempo | 可复用 Tempo 内部序列化 | 引入大量无用依赖（对象存储、WAL 等）、版本耦合 |
| C. 使用 OTEL pdata 序列化 | 用 pdata 的 JSON marshaler | 标准兼容 | pdata 的 JSON 格式与 Tempo 返回格式有细微差异 |

### 2.2 最终选择：方案 A — 自行实现适配层

理由：
1. 项目已有完整的 `TraceReader` 接口和 ES 实现（`SearchTraces`、`GetTrace`、`GetServices`、`GetOperations`）
2. Tempo 返回的 trace 格式就是标准 OTLP JSON（`resourceSpans → scopeSpans → spans`），我们的 `StoredSpan` 包含所有需要的字段
3. 与现有 Prometheus handler（`prometheus_handler.go`）保持一致的架构模式：单文件 handler + 路由注册
4. 引入 Tempo 会带来不可接受的依赖膨胀（>100MB 的无用代码）

## 3. Tempo HTTP API 端点规格

### 3.1 需要实现的端点

| 优先级 | 端点 | 说明 | 对应内部方法 |
|--------|------|------|-------------|
| P0 | `GET /api/traces/{traceID}` | 获取完整 trace（OTLP JSON，V1） | `TraceReader.GetTrace()` |
| P0 | `GET /api/v2/traces/{traceID}` | 获取完整 trace（OTLP protobuf，V2） | `TraceReader.GetTrace()` |
| P0 | `GET /api/search` | 搜索 traces（V1，JSON） | `TraceReader.SearchTraces()` |
| P0 | `GET /api/search/tags` | 列出可用标签名 | `TraceReader.GetServices()` + 静态 intrinsic 列表 |
| P0 | `GET /api/search/tag/{tagName}/values` | 列出标签值 | `TraceReader.GetOperations()` + 新接口 |
| P1 | `GET /api/v2/search/tags` | V2 标签（按 scope 分组） | 同上，分组输出 |
| P1 | `GET /api/v2/search/tag/{tagName}/values` | V2 标签值（带类型） | 同上，包含 type 字段 |
| P2 | `GET /api/echo` | 健康检查 | 直接返回 200 |
| P2 | `GET /api/status/buildinfo` | 版本信息（Grafana 探测用） | 直接返回版本 JSON |

### 3.2 Grafana 配置方式

```
Type: Tempo
URL: http://<collector>:8088/api/v2/tempo
Access: Server (proxy)
Auth: Basic Auth (same as admin API)
```

路由前缀：`/api/v2/tempo` → 内部映射到 Tempo 的 `/api/*` 路径。

### 3.3 响应格式规格

#### GET /api/traces/{traceID}

返回 OTLP JSON 格式（Tempo 标准）：

```json
{
  "batches": [
    {
      "resource": {
        "attributes": [
          { "key": "service.name", "value": { "stringValue": "my-service" } }
        ]
      },
      "scopeSpans": [
        {
          "scope": {
            "name": "otel-sdk",
            "version": "1.0.0"
          },
          "spans": [
            {
              "traceId": "abc123...",
              "spanId": "def456...",
              "parentSpanId": "",
              "name": "HTTP GET /api/users",
              "kind": 2,
              "startTimeUnixNano": "1720000000000000000",
              "endTimeUnixNano": "1720000000500000000",
              "attributes": [
                { "key": "http.method", "value": { "stringValue": "GET" } }
              ],
              "status": { "code": 0 }
            }
          ]
        }
      ]
    }
  ]
}
```

> **注意**：Tempo 使用 `batches` 作为顶层 key（而非 OTLP 标准的 `resourceSpans`），`kind` 使用整数编码。

#### GET /api/search

```json
{
  "traces": [
    {
      "traceID": "2f3e0cee77ae5dc9c17ade3689eb2e54",
      "rootServiceName": "shop-backend",
      "rootTraceName": "update-billing",
      "startTimeUnixNano": "1684778327699392724",
      "durationMs": 557,
      "spanSets": [
        {
          "spans": [
            {
              "spanID": "563d623c76514f8e",
              "startTimeUnixNano": "1684778327735077898",
              "durationNanos": "446979497",
              "attributes": []
            }
          ],
          "matched": 1
        }
      ]
    }
  ],
  "metrics": {
    "inspectedTraces": 100,
    "inspectedBytes": "0"
  }
}
```

#### GET /api/search/tags

```json
{
  "tagNames": [
    "service.name",
    "http.method",
    "http.status_code",
    "span.kind",
    "status"
  ],
  "metrics": {}
}
```

#### GET /api/search/tag/{tagName}/values

```json
{
  "tagValues": [
    "my-service",
    "other-service"
  ],
  "metrics": {}
}
```

#### GET /api/v2/search/tags

```json
{
  "scopes": [
    {
      "name": "resource",
      "tags": ["service.name", "host.name", "deployment.environment"]
    },
    {
      "name": "span",
      "tags": ["http.method", "http.status_code", "http.url"]
    },
    {
      "name": "intrinsic",
      "tags": ["duration", "kind", "name", "status", "statusMessage", "rootName", "rootServiceName"]
    }
  ],
  "metrics": {}
}
```

#### GET /api/v2/search/tag/{tagName}/values

```json
{
  "tagValues": [
    { "type": "string", "value": "my-service" },
    { "type": "string", "value": "other-service" }
  ],
  "metrics": {}
}
```

## 4. 架构设计

### 4.1 数据转换流程

```
Grafana Tempo Request
    │
    ▼
┌─────────────────────────────┐
│  tempo_handler.go           │  ← 新文件（~500 行）
│  - 解析 Tempo 请求参数       │
│  - 调用 TraceReader 接口     │
│  - 转换响应为 Tempo 格式     │
└─────────────────────────────┘
    │
    ▼
┌─────────────────────────────┐
│  TraceReader Interface      │  ← 已有
│  (observabilitystorageext)  │
└─────────────────────────────┘
    │
    ▼
┌─────────────────────────────┐
│  ES TraceReader 实现         │  ← 已有
│  (composite agg + bulk get) │
└─────────────────────────────┘
```

### 4.2 关键转换：StoredSpan → OTLP Batch JSON

核心转换逻辑：将平铺的 `[]Span` 按 `(ServiceName, Scope)` 分组为 OTLP 的 `batches[].scopeSpans[].spans[]` 层级结构。

```go
// StoredSpan 已有字段：
// - Resource map[string]any      → batches[].resource.attributes
// - Scope.Name/Version           → scopeSpans[].scope
// - Attributes map[string]any    → spans[].attributes
// - Kind string                  → spans[].kind (需要转为整数)
// - Status.Code string           → spans[].status.code (需要转为整数)
```

SpanKind 映射表（Tempo 使用整数）：

| 字符串 | 整数 |
|--------|------|
| SPAN_KIND_UNSPECIFIED | 0 |
| SPAN_KIND_INTERNAL | 1 |
| SPAN_KIND_SERVER | 2 |
| SPAN_KIND_CLIENT | 3 |
| SPAN_KIND_PRODUCER | 4 |
| SPAN_KIND_CONSUMER | 5 |

StatusCode 映射表：

| 字符串 | 整数 |
|--------|------|
| STATUS_CODE_UNSET | 0 |
| STATUS_CODE_OK | 1 |
| STATUS_CODE_ERROR | 2 |

### 4.3 需要扩展的接口

现有 `TraceReader` 接口不需要修改。但对于 tags/tag values 的完整支持，需要新增一个方法：

```go
// TraceReader 接口扩展（可选，P1 阶段）
type TraceReader interface {
    // ... existing methods ...
    
    // GetTagKeys returns all distinct attribute keys from spans within the time range.
    // scope: "resource", "span", or "" (all)
    GetTagKeys(ctx context.Context, scope string, timeRange TimeRange) ([]string, error)
    
    // GetTagValues returns distinct values for a specific tag key.
    GetTagValues(ctx context.Context, tagKey string, scope string, timeRange TimeRange) ([]string, error)
}
```

**P0 阶段**不修改接口，通过以下策略实现 tags：
- `service.name` → 调用 `GetServices()`
- `name`（span name/operation） → 通过已有搜索来推断
- 其他标签 → 返回静态 intrinsic 列表 + ES aggregation 查询（后续 P1 实现）

### 4.4 文件组织

```
extension/adminext/
├── tempo_handler.go          ← 新增：Tempo API handler（~500行）
├── prometheus_handler.go     ← 已有：参考实现模式
└── router.go                 ← 修改：注册 Tempo 路由
```

## 5. 实施计划

### Sprint 1：P0 — 核心端点（已完成 ✅）

- [x] 创建 `tempo_handler.go`，实现核心结构（~520 行）
- [x] 实现 `GET /api/traces/{traceID}` — OTLP JSON 格式返回
- [x] 实现 `GET /api/search` — Trace 搜索
- [x] 实现 `GET /api/search/tags` — 标签名列表（基于 GetServices + 静态列表）
- [x] 实现 `GET /api/search/tag/{tagName}/values` — 标签值列表
- [x] 实现 `GET /api/echo` — 健康检查
- [x] 在 `router.go` 注册 Tempo 路由块
- [x] 编译验证（`go build ./...` 通过）

### Sprint 2：P1 — V2 API + 标签增强（已完成 ✅）

- [x] 扩展 `TraceReader` 接口，新增 `GetTagKeys`/`GetTagValues`
- [x] ES 实现 `GetTagKeys`（sampler + top_hits + Go 侧 key 提取，兼容 flattened 类型）
- [x] ES 实现 `GetTagValues`（terms aggregation on `attributes.{key}` / `resource.{key}`）
- [x] 实现 `GET /api/v2/search/tags`（按 scope 分组：resource/span/intrinsic）
- [x] 实现 `GET /api/v2/search/tag/{tagName}/values`（带 type 标注的值列表）
- [x] PG Adapter stub 实现（返回空，不影响编译）

### Sprint 3：P2 — TraceQL 查询支持（已完成 ✅）

- [x] 支持 `q` 参数的基本 TraceQL 解析（`tempo_handler.go` +120 行）
- [x] 支持 `{ .key = "value" }` 单条件和 `&&` 多条件 AND
- [x] 支持 scope 前缀（`resource.` / `span.` / `.`）
- [x] 支持 `=` / `!=` / `=~` 运算符解析（`=` 直接映射为 tag 精确匹配）
- [x] 与 `tags` 参数合并（`q` 优先级更高）
- [x] 自动从 `service.name` 提取 `ServiceName` 过滤

## 6. Grafana 兼容性

### 6.1 Grafana Tempo 数据源发起的典型请求

1. **初始化连接**：`GET /api/echo`
2. **浏览标签**：`GET /api/search/tags` 或 `GET /api/v2/search/tags`
3. **获取标签值**：`GET /api/search/tag/service.name/values`
4. **搜索 trace**：`GET /api/search?tags=service.name%3Dmy-svc&minDuration=100ms&limit=20&start=1720000000&end=1720003600`
5. **查看 trace 详情**：`GET /api/traces/abc123def456...`

### 6.2 注意事项

- Grafana 可能同时发送 V1 和 V2 的 tags 请求，需要两套都支持
- `traceID` 为 32 位十六进制字符串（128 bit），不需要 dash 分隔
- 搜索结果中 `durationMs` 为整数毫秒
- `startTimeUnixNano` 为纳秒字符串

## 7. 序列化策略：参考 Tempo 内部实现降低联调试错成本

### 7.1 Tempo 内部序列化机制分析

通过分析 Tempo 源码（`modules/querier/http.go`），确认其序列化策略如下：

| 方面 | Tempo 内部实现 | 说明 |
|------|-------------|------|
| **JSON 库** | `github.com/golang/protobuf/jsonpb`（已标记 deprecated） | 不是现代的 `protojson`，也不是标准 `encoding/json` |
| **Marshaler 配置** | `new(jsonpb.Marshaler)` — 零值初始化 | 无自定义 option |
| **数据流向** | `tempopb.Trace` (proto message) → `jsonpb.Marshaler.Marshal(w, m)` | 直接流式写入 ResponseWriter |
| **Content-Type** | `application/json` | 通过 `Accept` header 协商（默认 JSON，`application/protobuf` 返回二进制） |

### 7.2 `jsonpb.Marshaler{}` 零值的关键行为

Tempo 使用零值 `jsonpb.Marshaler`（未设置任何 option），这意味着：

| Option | 默认值 | 效果 |
|--------|--------|------|
| `EmitDefaults` | `false` | **零值字段不输出**（如 `kind=0` / `status.code=0` 不会出现在 JSON 中） |
| `OrigName` | `false` | **使用 camelCase** 字段名（proto `start_time_unix_nano` → JSON `startTimeUnixNano`） |
| `EnumsAsInts` | `false` | **枚举输出为字符串名**（proto 定义的 enum 名称） |
| `Indent` | `""` | 紧凑输出，无缩进 |

### 7.3 对我们自行序列化的关键影响

基于上述分析，我们的手动序列化必须遵循以下规则，否则 Grafana 将无法正确解析：

#### ✅ 字段命名规则（camelCase）

```
Proto 定义                    → JSON 输出
─────────────────────────────────────────────
trace_id                     → "traceId"
span_id                      → "spanId"
parent_span_id               → "parentSpanId"
start_time_unix_nano         → "startTimeUnixNano"
end_time_unix_nano           → "endTimeUnixNano"
dropped_attributes_count     → "droppedAttributesCount"
dropped_events_count         → "droppedEventsCount"
dropped_links_count          → "droppedLinksCount"
scope_spans                  → "scopeSpans"
schema_url                   → "schemaUrl"
```

#### ✅ 顶层结构：`batches`（而非 `resourceSpans`）

Tempo 的 proto 定义：
```protobuf
message Trace {
    repeated ResourceSpans resourceSpans = 1;  // proto 字段名
}
```

但因为 `OrigName=false`，JSON 输出时 proto 字段名 `resourceSpans` → camelCase 仍是 `resourceSpans`。**而 Tempo 响应中实际使用 `batches` 是因为 Tempo 旧版 proto 定义的字段名就是 `batches`**，新版已更名为 `resourceSpans`。

> **结论**：Grafana Tempo 数据源插件**同时支持** `batches` 和 `resourceSpans` 作为顶层 key。推荐使用 `resourceSpans` 以与 OTLP 标准一致。

#### ✅ bytes 字段的编码：hex 字符串

`jsonpb` 对 proto `bytes` 类型默认输出 base64。但 Tempo 的 `traceId`/`spanId` 在 proto 中定义为 `bytes`，实际 JSON 输出为 **hex 字符串**（32 位 / 16 位）。这是 Tempo 自定义的序列化处理（在 `tempopb` 层做了特殊的 MarshalJSON）。

我们的实现中 `traceId`/`spanId` 本身已是 hex 字符串存储，直接输出即可。

#### ✅ 零值省略规则

| 场景 | 行为 | 我们的处理 |
|------|------|-----------|
| `kind = SPAN_KIND_UNSPECIFIED (0)` | **不输出** `kind` 字段 | 使用 `omitempty` |
| `status.code = STATUS_CODE_UNSET (0)` | **不输出** `code` 字段 | 使用 `omitempty` |
| `parentSpanId = ""` | **不输出** | 使用 `omitempty` |
| `droppedAttributesCount = 0` | **不输出** | 使用 `omitempty` |
| `attributes = []` | **不输出** | 空切片不输出 |
| `events = []` | **不输出** | 空切片不输出 |
| `links = []` | **不输出** | 空切片不输出 |

#### ✅ SpanKind 编码：整数

虽然 `jsonpb` 默认 `EnumsAsInts=false`（输出字符串），但 **Grafana Tempo 数据源插件接受整数和字符串两种形式**。从实际抓包观察，Tempo 返回的是整数（因为 Tempo 对 kind 有自定义处理）。

**推荐**：使用整数编码，与文档 4.2 节的映射表一致。

#### ✅ Attribute 的 value 结构

OTLP JSON 中 attribute value 必须使用 typed wrapper：

```json
{"key": "http.method", "value": {"stringValue": "GET"}}
{"key": "http.status_code", "value": {"intValue": "200"}}
{"key": "success", "value": {"boolValue": true}}
{"key": "latency", "value": {"doubleValue": 1.5}}
```

> **注意**：`intValue` 在 OTLP JSON 中是**字符串**（proto int64 → JSON string 以避免精度丢失），`doubleValue` 是 number。

#### ✅ 时间戳格式

```json
"startTimeUnixNano": "1720000000000000000"
```

纳秒级 Unix 时间戳，类型为 **字符串**（proto `fixed64` → jsonpb 输出为 quoted string）。

### 7.4 推荐的 Go 序列化结构体定义

基于以上分析，推荐使用标准 `encoding/json` + 精确的 `json` tag：

```go
// tempoTrace is the top-level response for GET /api/traces/{traceID}.
type tempoTrace struct {
    ResourceSpans []tempoResourceSpans `json:"resourceSpans,omitempty"`
}

type tempoResourceSpans struct {
    Resource  *tempoResource   `json:"resource,omitempty"`
    ScopeSpans []tempoScopeSpans `json:"scopeSpans,omitempty"`
}

type tempoResource struct {
    Attributes []tempoKeyValue `json:"attributes,omitempty"`
}

type tempoScopeSpans struct {
    Scope *tempoScope   `json:"scope,omitempty"`
    Spans []tempoSpan   `json:"spans,omitempty"`
}

type tempoScope struct {
    Name    string `json:"name,omitempty"`
    Version string `json:"version,omitempty"`
}

type tempoSpan struct {
    TraceID                string          `json:"traceId"`
    SpanID                 string          `json:"spanId"`
    ParentSpanID           string          `json:"parentSpanId,omitempty"`
    Name                   string          `json:"name"`
    Kind                   int             `json:"kind,omitempty"`
    StartTimeUnixNano      string          `json:"startTimeUnixNano"`
    EndTimeUnixNano        string          `json:"endTimeUnixNano"`
    Attributes             []tempoKeyValue `json:"attributes,omitempty"`
    DroppedAttributesCount int             `json:"droppedAttributesCount,omitempty"`
    Events                 []tempoEvent    `json:"events,omitempty"`
    DroppedEventsCount     int             `json:"droppedEventsCount,omitempty"`
    Links                  []tempoLink     `json:"links,omitempty"`
    DroppedLinksCount      int             `json:"droppedLinksCount,omitempty"`
    Status                 *tempoStatus    `json:"status,omitempty"`
}

type tempoKeyValue struct {
    Key   string         `json:"key"`
    Value tempoAnyValue  `json:"value"`
}

type tempoAnyValue struct {
    StringValue *string  `json:"stringValue,omitempty"`
    IntValue    *string  `json:"intValue,omitempty"`     // int64 as string
    DoubleValue *float64 `json:"doubleValue,omitempty"`
    BoolValue   *bool    `json:"boolValue,omitempty"`
}

type tempoStatus struct {
    Code    int    `json:"code,omitempty"`
    Message string `json:"message,omitempty"`
}
```

### 7.5 联调验证 Checklist

实施时按以下清单逐项验证，可最大程度减少与 Grafana 的联调试错成本：

- [ ] **字段名 camelCase**：抓包确认无 snake_case 字段
- [ ] **时间戳为字符串**：`"startTimeUnixNano": "1720000000000000000"`（有引号）
- [ ] **kind 为整数**：`"kind": 2`（不是 `"SPAN_KIND_SERVER"`）
- [ ] **零值省略**：`kind=0` 时无 `kind` 字段；空 `attributes` 无该字段
- [ ] **intValue 为字符串**：`"intValue": "200"`（有引号）
- [ ] **traceId/spanId 为 hex**：32 位/16 位十六进制小写字符串
- [ ] **顶层 key 为 `resourceSpans`**：（Grafana 同时支持 `batches`，但推荐前者）
- [ ] **Content-Type**：`application/json; charset=utf-8`
- [ ] **空 trace 返回 404**：`GET /api/traces/{id}` 无结果时返回 HTTP 404

### 7.6 与 Tempo 不完全一致但 Grafana 可接受的差异

| 差异点 | Tempo 行为 | 我们的行为 | Grafana 是否接受 |
|--------|-----------|-----------|-----------------|
| 零值 kind 字段 | 不输出 | `"kind": 0` | ✅ 接受，但冗余 |
| status.code 零值 | 不输出 | 不输出（推荐） | ✅ |
| events/links 为空 | 不输出 | 不输出（推荐） | ✅ |
| 额外字段 | 无 | 允许有额外字段 | ✅ Grafana 忽略未知字段 |

## 8. 风险评估

| 风险 | 影响 | 缓解策略 |
|------|------|----------|
| Tempo JSON 格式细节不准确 | Grafana 无法解析 | ✅ 已参考 Tempo 源码确认序列化规则（见第 7 节）；上线后通过 Grafana 实际请求验证 |
| tags API 数据不完整 | 只能看到部分标签 | P0 先返回 service.name 等核心标签，P1 补充 ES 聚合 |
| 大 trace 性能 | 响应超时 | 复用已有 GetTrace 的分页逻辑 |
| 字段名大小写错误 | Grafana 丢失数据 | ✅ 使用精确 `json` tag + 联调验证 Checklist |
| 时间戳类型错误 | 时间显示异常 | ✅ 确认使用 string 类型（proto fixed64 → quoted string） |

## 9. 流式搜索方案分析

### 9.1 Tempo 流式搜索协议

Tempo 的 streaming search 使用 **gRPC-over-HTTP** 协议：
- 配置项：`stream_over_http_enabled: true`
- gRPC Service：`StreamingQuerier.Search`
- 传输格式：gRPC frame encoding（5-byte header + protobuf payload），非 SSE / chunked JSON
- Grafana 要求：Tempo datasource 通过 `grpc-web` 格式通信，需要 Tempo 2.2+

### 9.2 方案对比

| 方案 | 描述 | 复杂度 | Grafana 兼容性 |
|------|------|--------|---------------|
| **A. 不实现流式，仅支持非流式 `/api/search`** | Grafana 自动 fallback 到轮询模式 | ⭐ 零额外工作 | ✅ 完全兼容 |
| B. 实现 gRPC-over-HTTP 流式 | 完整实现 `StreamingQuerier` 协议 | ⭐⭐⭐⭐⭐ | ✅ |
| C. 使用 SSE（Server-Sent Events） | 自定义流式协议 | ⭐⭐⭐ | ❌ Grafana 不支持 |

### 9.3 结论：不实现流式搜索

**理由**：

1. **Grafana 已有 fallback 机制**：当后端不支持 streaming 时，Grafana 退化为普通 `GET /api/search` 一次性返回结果，仅缺少搜索进度条。

2. **我们的搜索响应时间足够快**：ES 后端使用 `terms aggregation + bulk fetch` 策略，典型响应时间 < 500ms。Tempo 需要流式是因为其分布式搜索数百万 block 文件可能耗时数十秒，我们没有这个问题。

3. **gRPC-over-HTTP 协议实现成本极高**：
   - 需要 gRPC frame encoding（5-byte header + protobuf payload）
   - 需要引入 Tempo 的 `SearchResponse` proto 定义
   - 需要 trailer frame 表示流结束
   - 预估工期 2-3 周，投入产出比极低

4. **未来优化方向**：如大时间范围搜索体验需提升，可在 WebUI 层做分页轮询（前端逐步请求较小时间窗口并合并结果），无需实现 gRPC 流式协议。

## 10. `/api/metrics/query_range` 端点方案

### 10.1 Tempo TraceQL Metrics 功能说明

Tempo `/api/metrics/query_range` 接受 TraceQL 查询 + 聚合函数，从 span 数据计算指标时间序列：

```
GET /api/metrics/query_range?q={status=error}&step=15s&start=...&end=...
```

支持函数：`rate()`、`count_over_time()`、`min_over_time()`、`max_over_time()`、`avg_over_time()`、`quantile_over_time()`、`histogram_over_time()`

响应格式（Prometheus 兼容）：
```json
{
  "series": [
    {
      "labels": [
        {"key": "service.name", "value": "shop-backend"}
      ],
      "samples": [
        {"timestampMs": 1684778327000, "value": 12.5}
      ]
    }
  ],
  "metrics": {
    "inspectedTraces": 10000,
    "inspectedSpans": 50000
  }
}
```

### 10.2 方案对比

| 方案 | 描述 | 复杂度 | Grafana 兼容性 |
|------|------|--------|---------------|
| **A. 复用已有 MetricReader + TraceQL 翻译层** | 将 TraceQL 查询翻译为 `MetricRangeQuery` | ⭐⭐ | ✅ |
| B. 直接对 span 数据做实时聚合 | 对 ES trace index 执行 date_histogram | ⭐⭐⭐⭐ | ✅ |
| C. 暂不实现，返回空结果 | 返回空 `series` + metrics 元数据 | ⭐ | ✅ 不影响其他功能 |

### 10.3 推荐方案：A — 复用 MetricReader + TraceQL 翻译层

**核心思路**：项目已有完整的 spanmetrics 预聚合数据（`calls_total`、`duration_milliseconds_*`）存储在 ES metric index，且 `MetricReader` 已支持 `QueryRange`/`QueryRaw` + PromQL 语义。只需实现一个薄翻译层：TraceQL filter → metric labels。

**翻译映射**：

| TraceQL Metrics 查询 | 翻译为内部查询 |
|---------------------|---------------|
| `{status=error}` + `rate()` | `MetricRangeQuery{MetricName: "calls_total", Labels: {"status_code": "Error"}}` |
| `{resource.service.name="X"}` + `count_over_time()` | `MetricRangeQuery{MetricName: "calls_total", Labels: {"service_name": "X"}}` |
| `{name="GET /api"}` + `quantile_over_time(0.95)` | `MetricRangeQuery{MetricName: "duration_milliseconds_bucket", Labels: {"span_name": "GET /api"}}` |

**实现路径**：

```go
// tempo_handler.go 中新增 ~200 行

func (e *Extension) handleTempoMetricsQueryRange(w http.ResponseWriter, r *http.Request) {
    // 1. 解析参数 (q, start, end, step)
    q := r.URL.Query().Get("q")
    start, end, step := parseTempoTimeParams(r)
    
    // 2. 解析 TraceQL filter → metric labels
    filter, aggFunc := parseTraceQLMetrics(q)
    
    // 3. 确定 spanmetric 名称
    metricName := mapTraceQLFuncToMetric(aggFunc) // rate → calls_total, etc.
    
    // 4. 调用已有 MetricReader
    result, err := e.storageMetricReader.QueryRange(ctx, MetricRangeQuery{
        MetricName: metricName,
        Labels:     filter.ToLabels(),
        TimeRange:  TimeRange{Start: start, End: end},
        Step:       step,
        Aggregation: mapAggFunc(aggFunc),
    })
    
    // 5. 转换为 Tempo metrics 响应格式
    writeTempoMetricsResponse(w, result)
}
```

**响应格式转换**（`MetricRangeResult` → Tempo metrics）：

```go
type tempoMetricsResponse struct {
    Series  []tempoMetricSeries `json:"series"`
    Metrics tempoSearchMetrics  `json:"metrics"`
}

type tempoMetricSeries struct {
    Labels  []tempoLabel        `json:"labels"`
    Samples []tempoMetricSample `json:"samples"`
}

type tempoLabel struct {
    Key   string `json:"key"`
    Value string `json:"value"`
}

type tempoMetricSample struct {
    TimestampMs int64   `json:"timestampMs"`
    Value       float64 `json:"value"`
}
```

### 10.4 实施分期

**P1（Sprint 2 顺带实现，预估 2-3 天，已完成 ✅）**：
- [x] 实现 TraceQL filter 基础解析（`{key=value}` 语法 + `| pipeline` 分割）
- [x] 支持 `rate()` → `calls_total` 翻译
- [x] 支持 `count_over_time()` → `calls_total` 翻译
- [x] 支持 `by(label1, label2)` → `GroupBy` 翻译
- [x] 注册路由 `GET /api/v2/tempo/api/metrics/query_range`
- [x] 返回 Tempo metrics 响应格式（Prometheus 兼容 series）

**P2（Sprint 3，已完成 ✅）**：
- [x] 支持 `quantile_over_time(0.95)` → `duration_milliseconds` + p95 聚合
- [x] 支持 `histogram_over_time()` → `duration_milliseconds` histogram 数据
- [x] 支持 `by()` 分组（翻译为 `GroupBy`）
- [x] 支持复合 filter（`&&` AND + `||` OR 逻辑）

### 10.5 与直接实时聚合 span 数据的取舍

方案 B（直接对 ES trace index 做聚合）虽然在语义上更精确（无 spanmetrics 预聚合的精度损失），但有以下问题：

- **性能代价大**：trace index 数据量远大于 metric index（每个 span 一条文档 vs 每分钟一个聚合点）
- **ES 查询复杂**：需要 nested aggregation 处理 attributes 字段
- **与现有架构不一致**：metric 查询应走 metric index，保持 read path 统一

因此选择方案 A，利用已有的预聚合数据，保持架构一致性。

## 12. 搜索架构优化：SearchTraceSummaries（已完成 ✅）

### 12.1 问题

Sprint 1 的 `/api/search` 复用 `SearchTraces` → `SearchSpans` 全量 span 拉取路径。
Grafana 发送 `limit=200`（最多返回 200 条 trace 摘要），我们的实现却尝试拉取 200 × 100 = 20000 个 span 文档，
触发 ES `max_result_window=10000` 限制。

### 12.2 根因（5-Why）

| # | 问题 | 答案 |
|---|------|------|
| Why 1 | ES 为什么 400？ | `fetchTracesByIDs` 中 `Size=200×100=20000 > 10000` |
| Why 2 | 为什么需要 20000 文档？ | `SearchSpans` 返回每 trace 的全量 span |
| Why 3 | 为什么搜索用全量 span 路径？ | `adapter.SearchTraces()` 复用 `SearchSpans`（全量路径） |
| Why 4 | 为什么 SearchSpans 返回全量？ | 它设计为 V2 observability 服务（需要瀑布图的全量数据） |
| Why 5 | 为什么没有轻量搜索路径？ | **缺乏搜索摘要专用 read path**。Tempo 用 Parquet 列投影只读 10 列，我们用一个 ES 文档全量返回 |

### 12.3 解决方案

新增 `SearchTraceSummaries` 接口方法，ES 实现用 **单次 ES 查询**：
`terms aggregation on traceID + top_hits(size=spss, _source=[11 fields])`。

```
                     TraceReader (interface)
                     ══════════════════════
                     SearchTraces()             → 全量 span（V2 Observability）
                     SearchTraceSummaries()    → 摘要（Tempo /api/search）
```

ES 查询量对比：
- 优化前: Step1 agg + Step2 bulk fetch(20000 docs) = **2 次 ES 请求**
- 优化后: terms agg + top_hits + _source projection = **1 次 ES 请求，~2000 个轻量 doc**

### 12.4 变更文件

| 文件 | 变更 |
|------|------|
| `provider.go` | `TraceReader` 接口 +1 方法 |
| `types.go` | 新增 `TraceSummary` / `TraceSummaryResult` |
| `elasticsearch/types_reader.go` | ES 本地 `TraceSummary` 类型 |
| `elasticsearch/trace_reader.go` | +130 行：`SearchTraceSummaries` + `parseTraceSummaryResult` |
| `reader_adapter.go` | Adapter 转换（ES StoredSpan → 公共 Span） |
| `pg_reader_adapter.go` | PG stub |
| `tempo_handler.go` | `handleTempoSearch` 切换为 `SearchTraceSummaries`，新增 `convertTraceSummaryToTempoSearchTrace` |

## 13. 遗留问题

- [ ] TraceQL 查询语法的完整支持（P2 阶段）
- [x] ~~流式搜索（Tempo 的 streaming search）暂不支持~~ → 已分析，结论：不实现（见第 9 节）
- [ ] `/api/metrics` 端点 → 已有方案，排入 P1（见第 10 节）

## 14. 修复记录

### 14.1 V2 search 响应格式不兼容导致 TraceQL 搜索 500（2026-07-13）

**现象**：Grafana 12.0.1 Explore 中执行 `queryType: "traceql"` 搜索时返回 500：
```
Failed to convert tempo response to Otlp: proto: illegal wireType 6
```

**根因分析**：
- Grafana 12 的 Tempo 插件对 V2 端点默认期望 **protobuf 编码的 OTLP 响应**（`Content-Type: application/protobuf`）
- 我们注册了 `/api/v2/search` 并返回 JSON，Grafana 尝试按 protobuf 二进制解析 JSON 文本，触发 `illegal wireType 6`
- `proto: illegal wireType 6` 是因为 JSON 字符（如 `{`、`"`）被 protobuf 解码器解释为无效的 wire type 标记
- 同一原因也影响了 trace 查询：`proto: wrong wireType = 0 for field Message`

**修复策略**：
1. **实现 OTLP protobuf 编码**（`tempo_handler.go` +180行）：
   - 新增 `convertTraceToProtobuf()`：内部 Trace → proto `TracesData` → `proto.Marshal()` → binary
   - 全字段覆盖：TraceID/SpanID hex→[]byte、SpanKind/StatusCode enum 映射、AnyValue 多态转换、Events/Links/ArrayValue/KvlistValue
   - 依赖：`go.opentelemetry.io/proto/otlp` v1.5.0（已从 indirect 升级为 direct）
2. **新增 V2 trace handler** `handleTempoV2GetTrace`：
   - 返回 `Content-Type: application/protobuf` + protobuf binary body
   - V1 handler `handleTempoGetTrace` 保持不变（返回 JSON，向后兼容）
3. **恢复 `/api/v2/traces/{traceID}` 路由**：指向新的 protobuf handler
4. **保留 `/api/v2/search` 移除 + `/api/status/buildinfo` 新增**（第 14.1 节的 search 降级策略不变）

> **架构决策**：search 仍走 V1（JSON 摘要，性能更好），trace fetch 走 V2（protobuf 完整数据）。各取所需而非一刀切。

**变更文件**：
| 文件 | 变更 |
|------|------|
| `router.go` | 移除 `/api/v2/search`，恢复 `/api/v2/traces/{traceID}`（指向 `handleTempoV2GetTrace`），新增 `/api/status/buildinfo` |
| `tempo_handler.go` | 新增 `handleTempoV2GetTrace` + protobuf 编码层（~180行） |
| `go.mod` | `go.opentelemetry.io/proto/otlp` 升级为 direct 依赖 |

### 14.2 SearchTraceSummaries DurationMs 始终为 0（2026-07-14）

**现象**：Grafana Tempo search 结果列表中，所有 trace 的 duration 显示为 0ms，尽管 `{duration>1.2s}` 过滤本身工作正常。

**根因分析**：
- `SearchTraceSummaries` 的 ES 查询中 `_source` 投影包含 `durationNano`，但**不包含** `endTimeUnixNano`
- `parseTraceSummaryResult` 计算 trace 级别 DurationMs 时使用 `ss.EndUnixNano`（反序列化后始终为 0，因为 ES 未返回该字段）
- 导致 `maxEnd` 永远 = 0，条件 `maxEnd > minStart` 永不成立，`DurationMs` 始终 = 0

**修复**：改用 `StartUnixNano + DurationNano` 计算每个 span 的结束时间，取代对 `EndUnixNano` 的依赖：
```go
// 修改前
if ss.EndUnixNano > maxEnd {
    maxEnd = ss.EndUnixNano
}

// 修改后
end := ss.StartUnixNano + ss.DurationNano
if end > maxEnd {
    maxEnd = end
}
```

**变更文件**：
| 文件 | 变更 |
|------|------|
| `provider/elasticsearch/trace_reader.go` | `parseTraceSummaryResult` 中用 `StartUnixNano + DurationNano` 计算 maxEnd |

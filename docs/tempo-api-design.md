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
| P0 | `GET /api/traces/{traceID}` | 获取完整 trace（OTLP JSON） | `TraceReader.GetTrace()` |
| P0 | `GET /api/search` | 搜索 traces | `TraceReader.SearchTraces()` |
| P0 | `GET /api/search/tags` | 列出可用标签名 | `TraceReader.GetServices()` + 静态 intrinsic 列表 |
| P0 | `GET /api/search/tag/{tagName}/values` | 列出标签值 | `TraceReader.GetOperations()` + 新接口 |
| P1 | `GET /api/v2/search/tags` | V2 标签（按 scope 分组） | 同上，分组输出 |
| P1 | `GET /api/v2/search/tag/{tagName}/values` | V2 标签值（带类型） | 同上，包含 type 字段 |
| P2 | `GET /api/echo` | 健康检查 | 直接返回 200 |

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

### Sprint 1：P0 — 核心端点（本次实施）

- [ ] 创建 `tempo_handler.go`，实现核心结构
- [ ] 实现 `GET /api/traces/{traceID}` — OTLP JSON 格式返回
- [ ] 实现 `GET /api/search` — Trace 搜索
- [ ] 实现 `GET /api/search/tags` — 标签名列表（基于 GetServices + 静态列表）
- [ ] 实现 `GET /api/search/tag/{tagName}/values` — 标签值列表
- [ ] 实现 `GET /api/echo` — 健康检查
- [ ] 在 `router.go` 注册 Tempo 路由块
- [ ] 编译验证

### Sprint 2：P1 — V2 API + 标签增强

- [ ] 扩展 `TraceReader` 接口，新增 `GetTagKeys`/`GetTagValues`
- [ ] ES 实现 `GetTagKeys`（terms aggregation on attributes field names）
- [ ] ES 实现 `GetTagValues`（terms aggregation on specific field value）
- [ ] 实现 `GET /api/v2/search/tags`
- [ ] 实现 `GET /api/v2/search/tag/{tagName}/values`

### Sprint 3：P2 — TraceQL 查询支持

- [ ] 支持 `q` 参数的基本 TraceQL 解析
- [ ] 支持 `{ .service.name = "xxx" && .http.method = "GET" }` 格式

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

## 7. 风险评估

| 风险 | 影响 | 缓解策略 |
|------|------|----------|
| Tempo JSON 格式细节不准确 | Grafana 无法解析 | 参考 Tempo 源码确认；上线后通过 Grafana 实际请求验证 |
| tags API 数据不完整 | 只能看到部分标签 | P0 先返回 service.name 等核心标签，P1 补充 ES 聚合 |
| 大 trace 性能 | 响应超时 | 复用已有 GetTrace 的分页逻辑 |

## 8. 遗留问题

- [ ] TraceQL 查询语法的完整支持（P2 阶段）
- [ ] 流式搜索（Tempo 的 streaming search）暂不支持
- [ ] `/api/metrics` 端点（Tempo metrics generation from spans）暂不实现

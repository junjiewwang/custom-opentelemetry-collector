# Design: Loki Datasource Implementation for AdminExt

## 1. 参考架构分析

### 1.1 Grafana Loki Datasource（`grafana-loki-datasource`）

前端 TypeScript/React 插件，后端 Go proxy layer。核心架构：

```
Grafana Frontend                    Backend (Go)                   Loki
┌─────────────────────┐    ┌─────────────────────┐    ┌─────────────────┐
│ datasource.ts       │───▶│ pkg/loki/loki.go    │───▶│ /loki/api/v1/*   │
│   queryData()       │    │   QueryData()       │    │                  │
│   metricFindQuery() │    │   CallResource()    │    │ query_range      │
│                       │   │   SubscribeStream() │    │ labels           │
│ LiveStreams.ts      │    │                       │    │ label/{name}/val │
│   live tailing      │    │ pkg/loki/streaming.go │    │ series           │
│                       │    │   RunStream()        │───▶│ tail (WS)        │
└─────────────────────┘    └─────────────────────┘    └─────────────────┘
```

**34 个代理路由** 覆盖所有 Loki API：
- 查询：`/loki/api/v1/query`, `/loki/api/v1/query_range`
- 标签：`/loki/api/v1/labels`, `/loki/api/v1/label/{name}/values`
- 系列：`/loki/api/v1/series`
- 统计：`/loki/api/v1/index/stats`, `/loki/api/v1/index/volume`
- 模式：`/loki/api/v1/patterns`
- 实时：`/loki/api/v2alpha/tail`（WebSocket）
- 规则：`/api/v1/rules`（alerting）

### 1.2 Logs Drilldown（`grafana-lokiexplore-app`）

Grafana App 插件，Scene-based 架构。核心依赖：

**调用的 Loki API**：
| resource | Loki API | 用途 |
|----------|----------|------|
| `volume` | `/loki/api/v1/index/volume` | 日志量按标签分组 |
| `patterns` | `/loki/api/v1/patterns` | 日志模式时间序列 |
| `detected_labels` | `/loki/api/v1/detected_labels` | 检测到的标签+基数 |
| `detected_fields` | `/loki/api/v1/detected_fields` | 检测到的字段+类型 |
| `labels` | `/loki/api/v1/labels` | 所有可用标签名 |
| *(default)* | `/loki/api/v1/query_range` | LogQL 数据查询 |

**ExpressionBuilder** 负责将 filter 状态转换为 LogQL，不依赖 Grafana 内置的 LogQL parser。

---

## 2. 现有基础设施

### 2.1 存储层（`observabilitystorageext`）

日志读写接口**已存在**：

```go
// provider.go
type LogReader interface {
    SearchLogs(ctx, query LogQuery) (*LogSearchResult, error)
    GetLogContext(ctx, logID string, lines int) (*LogContext, error)
    ListLogFields(ctx, timeRange TimeRange) ([]LogField, error)
    GetLogStats(ctx, query LogStatsQuery) (*LogStats, error)
}

type LogWriter interface {
    WriteLogs(ctx, ld plog.Logs) error
    Flush(ctx context.Context) error
}
```

日志类型**已存在**，与 OTLP LogRecord 对齐：
```go
type LogRecord struct {
    ID, TimeUnixNano, TraceID, SpanID, SeverityText string
    SeverityNumber int32
    Body           string
    Attributes     []KeyValue   // span attributes
    Resource       []KeyValue   // resource attributes
    ServiceName    string
}
```

### 2.2 AdminExt Handler 层

现有 handler 模式（以 Tempo 为例）：

```
router.go
  └── /api/v2/search          → tempo_handler.go (handleTempoV2Search)
  └── /api/v2/search/tags     → tempo_handler.go (handleTempoV2Tags)
  └── /api/v2/search/tag/*    → tempo_handler.go (handleTempoV2TagValues)
  └── /api/v2/traces/*        → tempo_handler.go (handleTempoV2Trace)
  └── /api/tempo/metrics      → tempo_handler.go (handleTempoMetrics*)
```

**模式**：
- `handler.go`：HTTP 层，参数解析 + 响应写入
- `planner.go`：查询解析 + 优化
- `ast.go`：AST 定义
- `trace_reader.go`：ES 查询构建 + 执行

---

## 3. 实施方案

### 3.1 架构总览

```
Grafana                          AdminExt                        ES
┌──────────────┐    ┌───────────────────────────────┐    ┌──────────────┐
│ Loki DS      │───▶│ loki_handler.go               │───▶│ otel-logs-*  │
│ /loki/api/v1 │    │   handleLokiQueryRange()      │    │              │
│              │    │   handleLokiLabels()          │    │              │
│ Drilldown    │    │   handleLokiLabelValues()     │    │              │
│ App          │───▶│   handleLokiSeries()          │───▶│              │
│              │    │   handleLokiIndexStats()      │    │              │
│              │    │   handleLokiIndexVolume()     │    │              │
└──────────────┘    │                                │    └──────────────┘
                    │ logql/                         │
                    │   parser.go   (LogQL → AST)    │
                    │   evaluator.go(AST → LogQuery) │
                    │   ast.go     (AST 定义)        │
                    │                                │
                    │ log_reader.go (ES logs)        │
                    └───────────────────────────────┘
```

### 3.2 文件结构

```
extension/
├── adminext/
│   ├── loki_handler.go          # [NEW] Loki HTTP API handlers
│   ├── loki_handler_test.go     # [NEW] Unit tests
│   ├── logql/                   # [NEW] LogQL parser
│   │   ├── parser.go            #   LogQL → AST
│   │   ├── parser_test.go
│   │   ├── evaluator.go         #   AST → LogQuery
│   │   ├── evaluator_test.go
│   │   └── ast.go               #   AST types
│   └── router.go                # [MODIFIED] 加 Loki routes
│
├── observabilitystorageext/
│   ├── provider.go              # [MODIFIED] LogReader 加 Loki 方法
│   ├── types.go                 # [MODIFIED] 加 Loki 查询类型
│   ├── reader_adapter.go        # [MODIFIED] adapter 透传
│   └── provider/elasticsearch/
│       ├── log_reader.go        # [NEW] ES 日志查询实现
│       ├── log_reader_test.go   # [NEW] 单元测试
│       └── admin.go             # [MODIFIED] 加 otel-logs 索引模板
```

### 3.3 MVP 接口（Phase 1）

| Loki API | AdminExt Handler | LogReader 方法 |
|----------|-----------------|----------------|
| `GET /loki/api/v1/query` | `handleLokiInstantQuery` | `SearchLogs` |
| `GET /loki/api/v1/query_range` | `handleLokiQueryRange` | `SearchLogs` |
| `GET /loki/api/v1/labels` | `handleLokiLabels` | `ListLogLabels` [NEW] |
| `GET /loki/api/v1/label/{name}/values` | `handleLokiLabelValues` | `ListLogLabelValues` [NEW] |

### 3.4 完整接口（Phase 2）

| Loki API | 用途 | Drilldown 依赖？ |
|----------|------|:---:|
| `/loki/api/v1/series` | Series matching | |
| `/loki/api/v1/index/stats` | Index stats | |
| `/loki/api/v1/index/volume` | Index volume | ✅ |
| `/loki/api/v1/patterns` | Log patterns | ✅ |
| `/loki/api/v1/detected_labels` | Detected labels | ✅ |
| `/loki/api/v1/detected_fields` | Detected fields | ✅ |
| `/loki/api/v1/tail` | Live tailing (WebSocket) | |

---

## 4. 关键设计

### 4.1 LogQL Parser（`logql/` 包）

**必须支持的语法子集**：

```
<logql>         = <log_stream_selector> [<line_filters>] [<pipeline>]

<log_stream_selector> = "{" <label_matcher> {"," <label_matcher>} "}"
<label_matcher>  = <identifier> <op> <quoted_string>
<op>             = "=" | "!=" | "=~" | "!~"

<line_filters>   = {<line_filter>}
<line_filter>    = "|=" <quoted_string>   (contains)
                 | "!=" <quoted_string>   (not contains)
                 | "|~" <quoted_string>   (regex match)
                 | "!~" <quoted_string>   (not regex match)

<pipeline>       = {<stage_expression>}
<stage_expression> = "|" <parser>         (json, logfmt, unpack)
                    | "|" <label_filter>
                    | "|" <line_format>
```

**AST 设计**：
```go
type LogQLQuery struct {
    StreamSelector []LabelMatcher   // {app="foo", env=~"prod|staging"}
    LineFilters    []LineFilter     // |= "error", |~ "timeout|failed"
    Pipeline       []PipelineStage
}

type LabelMatcher struct {
    Name  string
    Type  MatchType  // Equal, NotEqual, Regex, NotRegex
    Value string
}

type LineFilter struct {
    Type    FilterType  // Contains, NotContains, Regex, NotRegex
    Pattern string
}
```

**不引入第三方依赖**：手动实现 recursive descent parser，约 300-400 行（参考 `traceql/parser.go` 风格）。

### 4.2 LogReader 接口扩展

```go
// 新增方法
type LogReader interface {
    SearchLogs(...) ...              // 已有
    GetLogContext(...) ...           // 已有
    ListLogFields(...) ...           // 已有
    GetLogStats(...) ...             // 已有

    // [NEW] Loki-specific
    ListLogLabels(ctx, timeRange TimeRange, appID string) ([]string, error)
    ListLogLabelValues(ctx, label string, timeRange TimeRange, appID string) ([]string, error)
    GetLogVolume(ctx, timeRange TimeRange, labels []string, appID string) (*LogVolumeResult, error)
    DetectLogFields(ctx, timeRange TimeRange, appID string) ([]DetectedLogField, error)
}
```

### 4.3 Loki HTTP Response 格式

**query_range 响应（streams 格式）**：
```json
{
  "status": "success",
  "data": {
    "resultType": "streams",
    "result": [
      {
        "stream": { "app": "order-service", "level": "error" },
        "values": [
          ["1784707266594000000", "Failed to process order #1234: connection timeout"],
          ["1784707266650000000", "Retry attempt #1 failed"]
        ]
      }
    ],
    "stats": { "summary": { "bytesProcessedPerSecond": 12345 } }
  }
}
```

**labels 响应**：
```json
{
  "status": "success",
  "data": ["app", "level", "service_name", "container_name"]
}
```

### 4.4 ES 日志索引设计

**索引模板**：`otel-logs-{appId}-{date}`，参考 traces 模板。

```json
{
  "mappings": {
    "dynamic_templates": [{
      "strings_as_keyword": {
        "match_mapping_type": "string",
        "mapping": {
          "type": "text",
          "fields": { "keyword": { "type": "keyword", "ignore_above": 256 } }
        }
      }
    }],
    "properties": {
      "timeUnixNano":  { "type": "date_nanos" },
      "traceId":       { "type": "keyword" },
      "spanId":        { "type": "keyword" },
      "severityNumber": { "type": "integer" },
      "severityText":  { "type": "keyword" },
      "body":          { "type": "text" },
      "serviceName":   { "type": "keyword" },
      "appId":         { "type": "keyword" },
      "resource": {
        "type": "object",
        "properties": {
          "service.name":       { "type": "keyword" },
          "service.namespace":  { "type": "keyword" },
          "host.name":          { "type": "keyword" },
          "container.id":       { "type": "keyword" }
        }
      },
      "attributes": { "type": "flattened" }
    }
  }
}
```

**LogQL → ES 查询映射**：

| LogQL | ES 查询 |
|-------|---------|
| `{app="foo"}` | `{"term": {"resource.app.keyword": "foo"}}` |
| `{app=~"foo\|bar"}` | `{"terms": {"resource.app.keyword": ["foo","bar"]}}` |
| `\|= "error"` | `{"match": {"body": "error"}}` |
| `\|~ "timeout\|failed"` | `{"query_string": {"query": "body:/timeout\|failed/"}}` |
| `\| json` | 应用层解析（不推送 ES） |

### 4.5 Query → LogQuery 转换

```go
func (e *Evaluator) Evaluate(query *LogQLQuery) *observabilitystorageext.LogQuery {
    lq := &LogQuery{
        TimeRange: query.TimeRange,
        Limit:     query.Limit,
    }
    
    // Stream selector → Labels + LabelMatch
    for _, m := range query.StreamSelector {
        switch m.Type {
        case Equal, NotEqual:
            lq.Labels[m.Name] = m.Value
        case Regex, NotRegex:
            lq.LabelMatch[m.Name] = m.Value  // 复用 traces LabelMatch 机制
        }
    }
    
    // Line filters → Query body field
    for _, f := range query.LineFilters {
        lq.Query = buildLineFilterQuery(f)  // 构建 ES body 查询
    }
    
    return lq
}
```

---

## 5. 实施计划

### Sprint 1：MVP（query_range + labels + label/values）

| 文件 | 变更 |
|------|------|
| `logql/parser.go` + `ast.go` | LogQL parser（stream selector + label matchers） |
| `observabilitystorageext/provider.go` | `LogReader` 加 `ListLogLabels` / `ListLogLabelValues` |
| `observabilitystorageext/types.go` | 加 `LokiQuery` 类型 |
| `adminext/loki_handler.go` | `handleLokiQueryRange` / `handleLokiLabels` / `handleLokiLabelValues` |
| `adminext/router.go` | 加 4 条 routes |
| `provider/elasticsearch/log_reader.go` | ES 日志查询实现 |
| `provider/elasticsearch/admin.go` | 加 `otel-logs` 索引模板 |

**验收标准**：
- Grafana Loki datasource 可配置本项目为 datasource URL
- Explore 页可输入 `{app="order-service"} |= "error"` 并返回日志
- Label browser 可列出所有标签和值

### Sprint 2：完整 LogQL + Pipeline

| 文件 | 变更 |
|------|------|
| `logql/parser.go` | line filters, json/logfmt pipelines |
| `logql/evaluator.go` | AST → LogQuery with pipeline support |
| `loki_handler.go` | 加 `handleLokiInstantQuery` |

**验收标准**：
- `{app="foo"} |= "error" | json | level="warn"` 正确解析和执行
- Instant query 返回最新日志

### Sprint 3：Drilldown 支持

| 文件 | 变更 |
|------|------|
| `loki_handler.go` | `handleLokiIndexVolume` / `handleLokiPatterns` / `handleLokiDetectedFields` |
| `log_reader.go` | `GetLogVolume` / `DetectLogFields` / `GetLogPatterns` |

**验收标准**：
- Logs Drilldown App 可正常浏览和过滤日志（无需 LogQL）

---

## 6. 设计约束

1. **不引入第三方 LogQL parser**：参考 `traceql/parser.go` 的自研模式
2. **复用现有基础设施**：`LogReader` 接口、`LabelMatch` 机制、`AttributeResolver`、ES `flattened` 映射模式
3. **与 Tempo handler 风格一致**：chi router、结构化日志、统一错误处理
4. **动态模板覆盖 logs**：索引模板含 `strings_as_keyword`，不需要逐字段加 `.keyword`

## 7. 状态

- [x] 需求分析（Loki datasource + Drilldown 参考）
- [x] 架构设计
- [ ] Sprint 1 实施
- [ ] Sprint 2 实施
- [ ] Sprint 3 实施
- [ ] ES 集成测试

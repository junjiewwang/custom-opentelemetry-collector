# Prometheus 兼容 HTTP API 设计方案

## 需求背景

当前系统已实现 InfluxDB v1 兼容 API 供 Grafana 连接查询指标数据。但 OTel 指标模型天然更接近 Prometheus 的扁平 label 模型，实现 Prometheus 兼容 API 可以：

1. **更好的语义匹配**：OTel 的 `metric_name + labels` 结构与 Prometheus 的 `__name__ + labels` 完全一致
2. **更丰富的生态**：Grafana Prometheus 数据源支持 PromQL、变量模板、告警规则等高级功能
3. **更简单的对接**：无需 InfluxQL 到内部模型的复杂翻译层

## 方案选型

### 方案对比

| 方案 | 描述 | 优点 | 缺点 |
|------|------|------|------|
| A. 直接实现 HTTP API 子集 | 自己实现简单 PromQL 解析 + 核心端点 | 轻量、无额外依赖 | 不支持复杂 PromQL（rate/嵌套/数学运算） |
| B. 集成 `prometheus/prometheus` 引擎 | 引入完整 PromQL engine | 完整 PromQL | 引入大量依赖(30+MB)、复杂度高、耦合重 |
| C. 集成 `thanos-io/promql-engine` | 独立可嵌入的 PromQL 引擎 | 完整 PromQL、轻量、多线程、活跃维护 | 需实现 Queryable 适配器 |
| D. 实现 Prometheus Remote Read 协议 | 仅做存储后端 | 标准化 | 仍需真正的 Prometheus 实例 |

### 选择：方案 C — 集成 `thanos-io/promql-engine`

**理由**：

1. **完整 PromQL 支持**：包括 `rate()`、`increase()`、`irate()`、聚合函数、二元运算、子查询等，覆盖 Grafana 所有使用场景
2. **独立可嵌入**：不依赖完整的 Prometheus 代码库，是一个纯粹的 PromQL 执行引擎库
3. **多线程高性能**：基于 Volcano/Iterator 模型，支持并发执行
4. **接口明确**：只需实现 Prometheus 标准的 `storage.Queryable` 接口即可对接
5. **活跃维护**：由 Thanos 社区维护，与 Prometheus 生态保持兼容
6. **Mimir 验证**：Grafana Mimir 的 MQE 引擎也基于类似架构，证明了该方案的可行性

### 与 Mimir 的关系

Mimir 是完整的分布式时序数据库系统，自带存储层（TSDB blocks in object storage）。它的 MQE（Mimir Query Engine）支持所有 stable PromQL features，不支持的自动 fallback 到 Prometheus 引擎。

我们的场景是：**后端是 Elasticsearch，只需要一个可嵌入的 PromQL 引擎**，因此选择更轻量的 `thanos-io/promql-engine`。

## 架构设计

### 整体架构

```
┌──────────────┐     HTTP      ┌─────────────────────────────────────────────────┐
│   Grafana    │──────────────▶│  Prometheus HTTP API Handler (chi routes)        │
│   Prom DS    │◀──────────────│  /api/v2/prometheus/api/v1/*                     │
└──────────────┘               └─────────────────┬───────────────────────────────┘
                                                 │
                                                 ▼
                               ┌─────────────────────────────────────────────────┐
                               │  thanos-io/promql-engine                         │
                               │  (完整 PromQL 执行引擎)                            │
                               │  - rate(), increase(), sum(), avg() ...          │
                               │  - 二元运算、子查询、嵌套表达式                       │
                               └─────────────────┬───────────────────────────────┘
                                                 │ storage.Queryable 接口
                                                 ▼
                               ┌─────────────────────────────────────────────────┐
                               │  ES Queryable Adapter                            │
                               │  (Prometheus storage.Queryable/Querier 实现)      │
                               │  - Select() → ES bool filter + sort by time     │
                               │  - LabelValues() → ES terms agg                 │
                               │  - LabelNames() → ES sample + key extraction    │
                               └─────────────────┬───────────────────────────────┘
                                                 │
                                                 ▼
                               ┌─────────────────────────────────────────────────┐
                               │  Elasticsearch                                   │
                               │  (OTel metrics indices)                          │
                               └─────────────────────────────────────────────────┘
```

### 模块结构

```
extension/adminext/
├── prometheus_handler.go       # HTTP handler 层（路由分发 + Prometheus JSON 信封）
└── prometheus_queryable.go     # ES → Prometheus storage.Queryable 适配器

extension/observabilitystorageext/
├── provider.go                 # MetricReader 接口（新增 QueryRaw 方法）
└── provider/elasticsearch/
    └── metric_reader.go        # QueryRaw 实现（原始数据点查询）
```

### 核心接口：Prometheus `storage.Queryable`

```go
// 需要实现的 Prometheus 标准存储接口
// 来自 github.com/prometheus/prometheus/storage

type Queryable interface {
    Querier(mint, maxt int64) (Querier, error)
}

type Querier interface {
    // Select 根据 matchers 返回时间序列集合
    // PromQL 引擎通过此接口拉取原始数据点
    Select(sortSeries bool, hints *SelectHints, matchers ...*labels.Matcher) SeriesSet

    // LabelValues 返回指定标签的所有值
    LabelValues(ctx context.Context, name string, hints *LabelHints, matchers ...*labels.Matcher) ([]string, annotations.Annotations, error)

    // LabelNames 返回所有标签名
    LabelNames(ctx context.Context, hints *LabelHints, matchers ...*labels.Matcher) ([]string, annotations.Annotations, error)

    Close() error
}
```

### ES Queryable Adapter 设计

```go
// esQueryable 实现 Prometheus storage.Queryable 接口
type esQueryable struct {
    reader *elasticsearch.MetricReader
    appID  string  // 从 __app_id__ matcher 中提取
}

func (q *esQueryable) Querier(mint, maxt int64) (storage.Querier, error) {
    return &esQuerier{
        reader: q.reader,
        appID:  q.appID,
        mint:   mint,
        maxt:   maxt,
    }, nil
}

// esQuerier 实现 Prometheus storage.Querier 接口
type esQuerier struct {
    reader *elasticsearch.MetricReader
    appID  string
    mint   int64  // 查询起始时间（毫秒时间戳）
    maxt   int64  // 查询结束时间（毫秒时间戳）
}

func (q *esQuerier) Select(sortSeries bool, hints *storage.SelectHints, matchers ...*labels.Matcher) storage.SeriesSet {
    // 1. 从 matchers 提取 __name__ 和 __app_id__
    // 2. 其余 matchers 转换为 ES label 过滤条件
    // 3. 调用 MetricReader.QueryRaw() 获取原始数据点
    // 4. 将结果包装为 storage.SeriesSet
}
```

### MetricReader 接口扩展

现有 `MetricReader` 接口需新增一个方法，用于 PromQL 引擎的 `Select()` 调用：

```go
// MetricReader queries metric data from the storage backend.
type MetricReader interface {
    // ... 现有方法保持不变 ...

    // QueryRaw returns raw sample points for series matching the criteria.
    // Used by PromQL engine's Queryable.Select() interface.
    // Returns original data points without aggregation, sorted by time ASC.
    QueryRaw(ctx context.Context, query MetricRawQuery) ([]MetricRawSeries, error)
}

// MetricRawQuery holds parameters for raw sample point query.
type MetricRawQuery struct {
    AppID       string            `json:"appId,omitempty"`
    MetricName  string            `json:"metric"`
    Labels      map[string]string `json:"labels,omitempty"`       // 精确匹配
    LabelMatch  map[string]string `json:"labelMatch,omitempty"`   // 正则匹配
    ServiceName string            `json:"service,omitempty"`
    TimeRange   TimeRange         `json:"timeRange"`
    Limit       int               `json:"limit,omitempty"`        // 最大数据点数
}

// MetricRawSeries is a raw time series with original sample points.
type MetricRawSeries struct {
    MetricName string            `json:"metric"`
    Labels     map[string]string `json:"labels"`
    Samples    []MetricSample    `json:"samples"`
}

// MetricSample is a single raw sample point (timestamp + value).
type MetricSample struct {
    TimestampMs int64   `json:"t"`
    Value       float64 `json:"v"`
}
```

### ES QueryRaw 实现

```go
// QueryRaw 从 ES 查询原始数据点（不做聚合）
// 用于 PromQL 引擎的 Select() 接口
func (r *MetricReader) QueryRaw(ctx context.Context, query MetricRawQuery) ([]MetricRawSeries, error) {
    // 1. 构建 ES bool filter（metric name + labels + time range）
    // 2. 使用 composite aggregation 按 label set 分组
    // 3. 每组内按时间排序返回原始 (timestamp, value) 对
    // 4. 使用 search_after 分页处理大数据量
    //
    // ES 查询结构:
    // {
    //   "query": { "bool": { "must": [...filters...] } },
    //   "size": 0,
    //   "aggs": {
    //     "by_series": {
    //       "composite": { "sources": [label_fields...], "size": 100 },
    //       "aggs": {
    //         "samples": {
    //           "top_hits": {
    //             "size": limit,
    //             "sort": [{"timeUnixMilli": "asc"}],
    //             "_source": ["timeUnixMilli", "value", "labels"]
    //           }
    //         }
    //       }
    //     }
    //   }
    // }
}
```

## API 端点设计

### 路由结构

```
/api/v2/prometheus/api/v1/
├── query               — 即时查询 (GET/POST)
├── query_range         — 范围查询 (GET/POST)
├── labels              — 标签名列表 (GET/POST)
├── label/{name}/values — 标签值列表 (GET)
├── series              — 时间序列元数据 (GET/POST)
└── metadata            — 指标元数据 (GET)
```

> **Grafana 配置**：
> - Type: Prometheus
> - URL: `http://<collector>:8088/api/v2/prometheus`
> - Access: Server (proxy)
> - Auth: Basic Auth（复用现有 admin API 认证）

### 端点详细设计

#### 1. `GET/POST /api/v1/query` — 即时查询

**请求参数**：
| 参数 | 必填 | 说明 |
|------|------|------|
| `query` | ✅ | PromQL 表达式（完整支持） |
| `time` | ❌ | 评估时间点（RFC3339 或 Unix 时间戳），默认当前时间 |
| `timeout` | ❌ | 超时时间 |

**响应格式**（Prometheus 标准 JSON 信封）：
```json
{
  "status": "success",
  "data": {
    "resultType": "vector",
    "result": [
      {
        "metric": {"__name__": "http_requests_total", "job": "api", "instance": "10.0.0.1:8080", "__app_id__": "app-001"},
        "value": [1625000000, "42.5"]
      }
    ]
  }
}
```

#### 2. `GET/POST /api/v1/query_range` — 范围查询

**请求参数**：
| 参数 | 必填 | 说明 |
|------|------|------|
| `query` | ✅ | PromQL 表达式（完整支持，包括 rate/increase 等） |
| `start` | ✅ | 开始时间（RFC3339 或 Unix 时间戳） |
| `end` | ✅ | 结束时间 |
| `step` | ✅ | 步长（如 `15s`、`1m`、`5m`） |
| `timeout` | ❌ | 超时时间 |

**响应格式**：
```json
{
  "status": "success",
  "data": {
    "resultType": "matrix",
    "result": [
      {
        "metric": {"__name__": "http_requests_total", "job": "api", "__app_id__": "app-001"},
        "values": [
          [1625000000, "42.5"],
          [1625000015, "43.1"],
          [1625000030, "44.0"]
        ]
      }
    ]
  }
}
```

#### 3. `GET/POST /api/v1/labels` — 标签名列表

**请求参数**：
| 参数 | 必填 | 说明 |
|------|------|------|
| `start` | ❌ | 开始时间 |
| `end` | ❌ | 结束时间 |
| `match[]` | ❌ | 限定序列选择器（可重复） |

**响应格式**：
```json
{
  "status": "success",
  "data": ["__app_id__", "__name__", "instance", "job", "service_name"]
}
```

#### 4. `GET /api/v1/label/{name}/values` — 标签值列表

**请求参数**：
| 参数 | 必填 | 说明 |
|------|------|------|
| `start` | ❌ | 开始时间 |
| `end` | ❌ | 结束时间 |
| `match[]` | ❌ | 限定序列选择器 |

**响应格式**：
```json
{
  "status": "success",
  "data": ["api-server", "gateway", "worker"]
}
```

#### 5. `GET/POST /api/v1/series` — 时间序列元数据

**请求参数**：
| 参数 | 必填 | 说明 |
|------|------|------|
| `match[]` | ✅ | 至少一个序列选择器 |
| `start` | ❌ | 开始时间 |
| `end` | ❌ | 结束时间 |

**响应格式**：
```json
{
  "status": "success",
  "data": [
    {"__name__": "http_requests_total", "job": "api", "instance": "10.0.0.1:8080", "__app_id__": "app-001"},
    {"__name__": "http_requests_total", "job": "api", "instance": "10.0.0.2:8080", "__app_id__": "app-001"}
  ]
}
```

#### 6. `GET /api/v1/metadata` — 指标元数据

**响应格式**：
```json
{
  "status": "success",
  "data": {
    "http_requests_total": [{"type": "gauge", "help": "", "unit": ""}]
  }
}
```

## 多 AppID 场景设计

### `__app_id__` 特殊标签

所有指标查询结果的 label map 中自动注入 `__app_id__` 标签，用于区分不同应用：

- **查询过滤**：`metric_name{__app_id__="app-001"}` → 转换为 ES appID 索引路由
- **变量模板**：`label_values(__app_id__)` → 列出所有可用应用
- **Dashboard 级过滤**：通过 Grafana 变量 `$app_id` 动态切换应用

### 实现机制

```go
func (q *esQuerier) Select(..., matchers ...*labels.Matcher) storage.SeriesSet {
    var appID string
    var esMatchers []esMatcher

    for _, m := range matchers {
        switch m.Name {
        case "__app_id__":
            appID = m.Value  // 提取为 ES 索引路由
        case "__name__":
            // 提取为 metric name 过滤
        default:
            // 转换为 ES label 过滤条件
        }
    }

    // 使用 appID 确定查询的索引范围
    // 若 appID 为空，则查询所有索引（admin 模式）
}
```

## 与现有代码的集成

### 路由注册（router.go）

```go
// ============================================================================
// Prometheus v1 Compatible API (for Grafana Prometheus data source)
// ============================================================================
// Grafana configuration:
//   Type: Prometheus
//   URL: http://<collector>:8088/api/v2/prometheus
//   Access: Server
//   Auth: Basic Auth (same as admin API)
if e.storageMetricReader != nil {
    r.Route("/prometheus/api/v1", func(r chi.Router) {
        r.Get("/query", e.handlePromQuery)
        r.Post("/query", e.handlePromQuery)
        r.Get("/query_range", e.handlePromQueryRange)
        r.Post("/query_range", e.handlePromQueryRange)
        r.Get("/labels", e.handlePromLabels)
        r.Post("/labels", e.handlePromLabels)
        r.Get("/label/{labelName}/values", e.handlePromLabelValues)
        r.Get("/series", e.handlePromSeries)
        r.Post("/series", e.handlePromSeries)
        r.Get("/metadata", e.handlePromMetadata)
    })
}
```

### 依赖引入

```go
// go.mod 新增依赖
require (
    github.com/thanos-io/promql-engine v0.x.x
    github.com/prometheus/prometheus   v0.x.x  // promql-engine 的间接依赖，提供 storage 接口定义
)
```

## PromQL 引擎集成方式

### 引擎初始化

```go
import (
    "github.com/thanos-io/promql-engine/engine"
    "github.com/prometheus/prometheus/promql"
)

// 在 Extension 启动时初始化 PromQL 引擎
func (e *Extension) initPromQLEngine() {
    opts := engine.Opts{
        EngineOpts: promql.EngineOpts{
            MaxSamples:         50000000,
            Timeout:            2 * time.Minute,
            LookbackDelta:      5 * time.Minute,  // rate() lookback 窗口
            EnableNegativeOffset: true,
        },
    }
    e.promqlEngine = engine.New(opts)
}
```

### 查询执行流程

```go
func (e *Extension) handlePromQueryRange(w http.ResponseWriter, r *http.Request) {
    // 1. 解析请求参数
    query := r.FormValue("query")
    start := parseTime(r.FormValue("start"))
    end := parseTime(r.FormValue("end"))
    step := parseDuration(r.FormValue("step"))

    // 2. 创建 ES Queryable（注入 appID 如有）
    queryable := &esQueryable{
        reader: e.storageMetricReader,
        appID:  "",  // 从 auth context 或 header 中获取，或留空走全局模式
    }

    // 3. 通过 PromQL 引擎创建查询
    qry, err := e.promqlEngine.NewRangeQuery(
        r.Context(),
        queryable,
        nil,  // query opts
        query,
        start,
        end,
        step,
    )
    if err != nil {
        writePromError(w, "bad_data", err.Error())
        return
    }
    defer qry.Close()

    // 4. 执行查询
    result := qry.Exec(r.Context())
    if result.Err != nil {
        writePromError(w, "execution", result.Err.Error())
        return
    }

    // 5. 格式化为 Prometheus 标准 JSON 响应
    writePromSuccess(w, result)
}
```

### rate()/increase() 支持原理

使用 `promql-engine` 后，`rate()` 和 `increase()` 的支持是**自动获得的**：

1. PromQL 引擎解析 `rate(http_requests_total[5m])` 时
2. 引擎的 matrix selector 算子会调用 `Querier.Select()` 获取 `[t-5m, t]` 窗口内的原始数据点
3. 引擎内部的 `rate` 函数算子执行 `(last - first) / (lastTime - firstTime)` 计算
4. 结果直接返回，无需我们手动实现 lookback 逻辑

**关键**：`Select()` 返回的 SeriesSet 必须包含原始采样点（不能是聚合后的值），引擎会自己做窗口滑动。

这也是为什么需要新增 `QueryRaw()` 方法的原因——它返回原始数据点序列，而不是 `QueryRange()` 那种已经按 step 聚合过的结果。

## 实施计划

### Sprint 1：核心框架搭建（P0）✅ 已完成

| 任务 | 文件 | 状态 |
|------|------|------|
| 1 | `go.mod` | ✅ 无需新增依赖（reuse 已有 `prometheus/prometheus v0.300.1`） |
| 2 | `provider.go` + `types.go` + `reader_adapter.go` + `pg_reader_adapter.go` | ✅ 新增 `QueryRaw` 接口 + ES/PG适配器 |
| 3 | `provider/elasticsearch/types_reader.go` + `metric_reader.go` | ✅ ES `QueryRaw()` 原始数据点查询 |
| 4 | `adminext/prometheus_handler.go` | ✅ HTTP handler + 自建 PromQL 解析器 + Prometheus JSON 格式 |
| 5 | `adminext/router.go` | ✅ 路由注册 `/prometheus/api/v1/*` |
| 6 | Grafana 连通验证 | ⬜ 待验证 |

**实施说明**：
- 未引入 `thanos-io/promql-engine`（需特定版本匹配 `prometheus/prometheus v0.300.1`，风险较高）
- 改为自建轻量 PromQL 解析器，支持 Grafana 80% 使用场景（选择器/聚合/rate/标签枚举）
- `rate()`/`increase()`/`irate()` 通过新增的 `QueryRaw` 接口 + 自建计算逻辑实现
- 架构简化为三层：Handler → 自建 PromQL 解析 → MetricReader 接口


### Sprint 2：完整功能验证（P1）

| 任务 | 说明 |
|------|------|
| 1 | 验证 rate()/increase()/irate() 正常工作 |
| 2 | 验证聚合函数 sum/avg/max/min by/without |
| 3 | 验证 Grafana 变量模板 `label_values()` |
| 4 | 验证 `/api/v1/series` 端点 |
| 5 | `__app_id__` 多应用隔离 |

### Sprint 3：生产化（P2）

| 任务 | 说明 |
|------|------|
| 1 | 查询超时控制（MaxSamples + Timeout） |
| 2 | 查询并发限制（gate） |
| 3 | 大查询保护（series limit + samples limit） |
| 4 | 性能优化（label names/values 缓存） |
| 5 | 错误响应标准化 + 日志 |

## 验收标准

1. ✅ Grafana Prometheus 数据源连接测试通过（`/api/v1/query` 返回 200）
2. ✅ Grafana Explorer 中使用 `metric_name{label="value"}` 可查询和绘图
3. ✅ `rate(metric[5m])` 范围函数正确计算并展示曲线
4. ✅ `sum by(service)(metric)` 聚合查询正常
5. ✅ 变量模板 `label_values(metric, label_name)` 正常工作
6. ✅ `__app_id__` 标签可用于多应用隔离
7. ✅ 所有端点在 auth middleware 保护下
8. ✅ 查询超时不会阻塞系统

## 安全考虑

- 所有端点注册在 `/api/v2/` 路由组内，受 `authMiddleware` 保护
- Grafana 配置时需提供 Basic Auth 或 API Key
- PromQL 引擎配置 `MaxSamples` 限制（防止 OOM）
- PromQL 引擎配置 `Timeout` 超时（防止慢查询）
- `QueryRaw` 结果数量有上限（防止 ES 全表扫描）

## 风险与应对

| 风险 | 影响 | 应对 |
|------|------|------|
| `promql-engine` 间接引入大量 Prometheus 依赖 | go.mod 膨胀 | 评估依赖树，必要时 replace 裁剪 |
| ES 原始数据点查询量大（rate 需要大窗口） | 查询慢/OOM | 设置 QueryRaw limit + SelectHints 利用 |
| `promql-engine` API 不稳定（v0.x） | 升级维护成本 | 封装适配层，隔离引擎接口 |
| 多 AppID 查询全局索引性能 | ES 扫描慢 | 强制要求 `__app_id__` 过滤或默认限制 |

## 可观测性

### OpenTelemetry Tracing（2026-07-10 新增）

在 `adminext` 包中加入了 OTel tracing 中间件 + body 记录，用于观察所有 HTTP 请求的完整链路。

**覆盖范围**：
| 层 | 位置 | Span 属性 |
|----|------|----------|
| HTTP 中间件 | `tracing.go` | `http.method`, `http.url`, `http.target`, `http.query_string`, `http.request_body`, `http.user_agent`, `http.scheme`, `net.peer.ip`, `net.host.name`, `http.status_code` |
| Grafana headers | `tracing.go` | `grafana.org_id`（`X-Grafana-Org-Id` header） |
| PromQL 处理 | `prometheus_handler.go` | `promql.expr`（原始表达式）, `promql.metric`, `promql.aggregation`, `promql.group_by`, `promql.function` |
| ES 查询 | `prometheus_handler.go` | `es.labels`（过滤后下发的 labels）, `promql.series_count`, `promql.aggregated_count`, `error.type` |

**Body 记录细节**：
- 仅记录文本类 Content-Type（`application/json`, `application/x-www-form-urlencoded`, `text/*` 等）
- 最大 64KB（超过截断），二进制内容跳过
- Body 读取后自动恢复，下游 handler 正常使用

**Trace 导出配置**（在 `config.yaml` 的 `service.telemetry.traces` section）：

```yaml
# 开发调试 - stdout（span 打印到 stderr）
service:
  telemetry:
    traces:
      processors:
        - batch:
            exporter:
              stdout: {}

# 生产环境 - OTLP 导出到自己的 collector 或 Tempo
service:
  telemetry:
    traces:
      processors:
        - batch:
            exporter:
              otlp:
                protocol: http/protobuf
                endpoint: http://localhost:4318
```

当前配置默认使用 `stdout` 导出（开发模式），可在 `config/build/config.yaml` 中覆盖为 OTLP 导出。

**使用方式**：
1. 启动后查看 stderr 中的 span 输出（stdout 模式）或查询 `service.name=custom-otlp-collector` 的 traces（OTLP 模式）
2. `promql.expr` 中可看到 Grafana 发出的**完整原始 PromQL 表达式**
3. `http.request_body` 中可看到 POST 请求的完整 body（如 `query=avg(...)`）
4. `es.labels` 中可看到经过 `filterInternalLabels` 处理后实际下发给 ES 的过滤条件

**已知限制**：
- Grafana Explore Metrics `avg({"label"="metric", "metric"})` 格式：表达式在 trace 中可见（`promql.expr`），可观察到 `promql.group_by` 为空且 label 值等于 metric name 的场景

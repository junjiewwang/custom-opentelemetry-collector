# Metrics 查询链路重构方案

> **文档状态**：设计阶段  
> **创建时间**：2026-07-10  
> **实施策略**：方案 C（结构化 API 语义对齐 InfluxQL），不向后兼容  
> **未来演进**：方案 A（InfluxDB 兼容端点，Grafana 直连）

---

## 1. 目标与范围

### 1.1 核心目标

以 **Grafana InfluxDB Query Builder** 为参照，重构 Metric 查询链路，使 API 在语义上完全覆盖 InfluxQL Visual Query Builder 的能力。

### 1.2 当前问题

| # | 问题 | 根因 |
|---|------|------|
| 1 | 前端 `Object.entries(null)` 崩溃 | 后端返回 `labels: null` |
| 2 | 不同时序混合 avg，结果无意义 | 无 labels 分组聚合 |
| 3 | 只能 avg，无法 sum/max/p99 等 | 聚合函数硬编码 |
| 4 | 查询结果只有一条线，无法对比 | 无 group by 能力 |
| 5 | 空桶处理不可控 | 无 fill 策略 |
| 6 | metadata API 不传时间范围 | 列表不完整 |

### 1.3 设计约束

- **不考虑向后兼容**：直接用新 API 替换旧实现，不保留旧行为
- **前后端同步重构**：API 参数、数据结构一步到位

---

## 2. 查询模型设计（对标 Grafana InfluxQL）

### 2.1 InfluxQL 等价映射

```sql
-- Grafana InfluxQL Query Builder 生成的典型查询
SELECT  <aggregation>("value")          -- 聚合函数
FROM    <measurement>                   -- metric name
WHERE   <tag_filters>                   -- label 过滤
        AND $timeFilter                 -- 时间范围
GROUP BY time($__interval)              -- 时间分桶
       , "tag1", "tag2"                 -- 按标签分组
FILL(null | none | 0 | previous)        -- 空桶填充
ORDER BY time ASC
LIMIT   <n>
SLIMIT  <n>                             -- 限制序列数
```

### 2.2 API 参数设计

```
GET /api/v2/observability/metrics/query_range
```

| InfluxQL 等价 | API 参数 | 类型 | 必填 | 默认值 | 说明 |
|:---:|:---:|:---:|:---:|:---:|------|
| `FROM <measurement>` | `metric` | string | ✅ | — | 指标名称 |
| `WHERE tag = 'value'` | `labels` | string | — | — | `key:value,key2:value2` 精确匹配 |
| `WHERE tag =~ /regex/` | `labelMatch` | string | — | — | `key:/regex/` 正则匹配 |
| `AND time >= start` | `start` | number | ✅ | — | Unix 毫秒 |
| `AND time <= end` | `end` | number | ✅ | — | Unix 毫秒 |
| `SELECT <func>("value")` | `aggregation` | string | — | `avg` | 聚合函数 |
| `GROUP BY time(interval)` | `step` | string | — | auto | 时间分桶间隔 |
| `GROUP BY "tag1", "tag2"` | `groupBy` | string | — | — | 按 label 分组 |
| `FILL(strategy)` | `fill` | string | — | `null` | 空桶填充策略 |
| `LIMIT n` | `limit` | number | — | 10000 | 数据点上限 |
| `SLIMIT n` | `seriesLimit` | number | — | 100 | 序列数上限 |

### 2.3 Metadata API

所有 metadata 接口必须传时间范围：

| 接口 | 参数 |
|------|------|
| `GET /metrics/names` | `start`, `end` |
| `GET /metrics/labels` | `metric`, `start`, `end` |
| `GET /metrics/labels/{key}/values` | `metric`, `start`, `end` |

---

## 3. 后端设计

### 3.1 数据结构

```go
// MetricRangeQuery — 查询参数（对齐 InfluxQL 语义）
type MetricRangeQuery struct {
    MetricName  string
    Labels      map[string]string   // WHERE tag = 'value'
    LabelMatch  map[string]string   // WHERE tag =~ /regex/
    TimeRange   TimeRange           // start/end
    Aggregation string              // SELECT <func>
    Step        time.Duration       // GROUP BY time(interval)
    GroupBy     []string            // GROUP BY "tag1", "tag2"
    Fill        string              // FILL(strategy)
    Limit       int                 // LIMIT
    SeriesLimit int                 // SLIMIT
    AppID       string
}
```

### 3.2 聚合函数注册表（策略模式）

```go
// AggregationFunc — 聚合函数的 ES DSL 构建器 + 结果解析器
type AggregationFunc struct {
    Build      func(field string) map[string]any
    ParseValue func(raw json.RawMessage) *float64
}

// aggregationRegistry — 注册表（OCP：新增只需注册）
var aggregationRegistry = map[string]*AggregationFunc{
    "avg":   {Build: buildAvg, ParseValue: parseSimpleValue},
    "sum":   {Build: buildSum, ParseValue: parseSimpleValue},
    "max":   {Build: buildMax, ParseValue: parseSimpleValue},
    "min":   {Build: buildMin, ParseValue: parseSimpleValue},
    "count": {Build: buildCount, ParseValue: parseSimpleValue},
    "last":  {Build: buildLast, ParseValue: parseTopHitsValue},
    "first": {Build: buildFirst, ParseValue: parseTopHitsValue},
    "p50":   {Build: buildPercentile(50), ParseValue: parsePercentileValue(50)},
    "p90":   {Build: buildPercentile(90), ParseValue: parsePercentileValue(90)},
    "p95":   {Build: buildPercentile(95), ParseValue: parsePercentileValue(95)},
    "p99":   {Build: buildPercentile(99), ParseValue: parsePercentileValue(99)},
}
```

| 聚合函数 | ES Aggregation | 适用场景 |
|---------|:---:|------|
| `avg` | `avg` | Gauge（CPU/延迟） |
| `sum` | `sum` | Counter 跨实例汇总 |
| `max` | `max` | 峰值监控 |
| `min` | `min` | 最低水位 |
| `count` | `value_count` | QPS/数据点密度 |
| `last` | `top_hits(size=1,desc)` | 最新值/仪表盘 |
| `first` | `top_hits(size=1,asc)` | 最早值 |
| `p50` | `percentiles(50)` | 延迟分位数 |
| `p90` | `percentiles(90)` | 延迟分位数 |
| `p95` | `percentiles(95)` | 延迟分位数 |
| `p99` | `percentiles(99)` | 延迟分位数 |

### 3.3 Fill 策略（后处理，纯函数）

```go
type FillStrategy func(values []MetricDataPoint) []MetricDataPoint

var fillStrategies = map[string]FillStrategy{
    "null":     fillNull,      // 保留 null（前端渲染为断线）
    "none":     fillNone,      // 过滤空桶
    "0":        fillZero,      // null → 0
    "previous": fillPrevious,  // null → last non-nil
    "linear":   fillLinear,    // 线性插值
}
```

| Fill 策略 | ES `min_doc_count` | 后处理 |
|-----------|:---:|------|
| `null` | `0` | 保留 null |
| `none` | `1` | 不返回空桶 |
| `0` | `0` | `null → 0` |
| `previous` | `0` | `null → lastNonNull` |
| `linear` | `0` | 前后非 null 点做线性插值 |

### 3.4 ES 聚合策略

**带 GroupBy（composite aggregation）**：

```json
{
  "aggs": {
    "by_group": {
      "composite": {
        "size": 100,
        "sources": [
          { "service_name": { "terms": { "field": "labels.service_name" } } },
          { "method": { "terms": { "field": "labels.method" } } }
        ]
      },
      "aggs": {
        "time_series": {
          "date_histogram": { "field": "timeUnixMilli", "fixed_interval": "60s" },
          "aggs": { "agg_value": { "<aggregation>": { "field": "value" } } }
        }
      }
    }
  }
}
```

**不带 GroupBy（单一时序）**：

```json
{
  "aggs": {
    "time_series": {
      "date_histogram": { "field": "timeUnixMilli", "fixed_interval": "60s" },
      "aggs": { "agg_value": { "<aggregation>": { "field": "value" } } }
    }
  }
}
```

### 3.5 QueryRange 核心逻辑

```go
func (r *MetricReader) QueryRange(ctx context.Context, query MetricRangeQuery) (*MetricRangeResult, error) {
    // 1. 获取聚合函数
    aggFunc, err := getAggregation(query.Aggregation)
    if err != nil {
        return nil, err
    }

    // 2. 构建查询条件（标签过滤 + 时间范围）
    esQuery := r.buildQueryFilter(query)

    // 3. 计算时间间隔
    interval := r.calculateInterval(query.TimeRange, query.Step)

    // 4. 构建 ES 聚合（根据是否有 groupBy）
    aggs := r.buildAggregation(query.GroupBy, interval, aggFunc, query.SeriesLimit)

    // 5. 执行查询
    resp, err := r.client.Search(ctx, r.indexPattern(query.AppID), &SearchRequest{
        Query:        esQuery,
        Size:         0,
        Aggregations: aggs,
    })
    if err != nil {
        return nil, err
    }

    // 6. 解析结果
    result, err := r.parseResult(resp, query.GroupBy, aggFunc)
    if err != nil {
        return nil, err
    }

    // 7. 应用 fill 策略
    fillFn := getFillStrategy(query.Fill)
    for i := range result.Data {
        result.Data[i].Values = fillFn(result.Data[i].Values)
    }

    // 8. 应用 limit
    r.applyLimits(result, query.Limit)

    return result, nil
}
```

### 3.6 Labels 保证非 nil

所有返回的 `MetricSeries.Labels` 必须为非 nil map：

```go
type MetricSeries struct {
    Labels map[string]string   `json:"labels"` // 保证不为 nil
    Values []MetricDataPoint   `json:"values"`
}
```

构造时统一用 `make(map[string]string)` 初始化。

---

## 4. 前端设计（对标 Grafana Query Builder）

### 4.1 UI 布局

```
┌─ Metric Query Builder ─────────────────────────────────────────────────────┐
│                                                                             │
│  FROM       [▼ http_server_request_duration_seconds  🔍]                   │
│                                                                             │
│  SELECT     [avg ▼]  (value)                                               │
│              ↑ avg / sum / max / min / count / last / p50 / p95 / p99      │
│                                                                             │
│  WHERE      [service_name ▼] [= ▼]  [my-app       ▼]  [×]                │
│             [status_code  ▼] [=~▼]  [/[45]../     ▼]  [×]                │
│             [+ Add filter]                                                  │
│                                                                             │
│  GROUP BY   time(auto)  ,  [service_name ▼] [method ▼]  [+ Add]          │
│                                                                             │
│  FILL       [null ▼]    ← null / none / 0 / previous / linear             │
│                                                                             │
│             [▶ Run Query]                                                   │
│                                                                             │
│  ── Query Preview (collapsed) ──────────────────────────────────────        │
│  SELECT avg("value") FROM "http_server_request_duration_seconds"            │
│  WHERE "service_name" = 'my-app' AND time >= ... AND time <= ...           │
│  GROUP BY time(auto), "service_name", "method" FILL(null)                  │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 4.2 API 客户端

```typescript
interface MetricRangeQueryParams {
  metric: string;
  labels?: string;         // "key:value,key:value"
  labelMatch?: string;     // "key:/regex/"
  start: number;
  end: number;
  aggregation?: string;    // 默认 "avg"
  step?: string;           // 默认 auto
  groupBy?: string;        // "service_name,method"
  fill?: string;           // 默认 "null"
  limit?: number;          // 默认 10000
  seriesLimit?: number;    // 默认 100
}

// Metadata API（必须传时间范围）
getMetricNames(start: number, end: number): Promise<{ data: string[] }>;
getMetricLabels(metric: string, start: number, end: number): Promise<{ data: string[] }>;
getMetricLabelValues(metric: string, label: string, start: number, end: number): Promise<{ data: string[] }>;
```

### 4.3 数据类型

```typescript
interface MetricSeries {
  labels: Record<string, string>;  // 后端保证非 null
  values: MetricTimeValue[];
}

interface MetricRangeResult {
  data: MetricSeries[];
}
```

### 4.4 多时序渲染

- 每条 `MetricSeries` 对应图表中一条线
- Legend 使用 `labels` 组合作为标识（如 `service=api, method=GET`）
- 超过 20 条线时默认折叠 Legend

---

## 5. 查询示例（InfluxQL ↔ API 等价）

| # | InfluxQL | API 请求 |
|---|----------|---------|
| 1 | `SELECT mean("value") FROM "http_requests" WHERE "service"='api' GROUP BY time(1m)` | `?metric=http_requests&labels=service:api&aggregation=avg&step=1m` |
| 2 | `SELECT sum("value") FROM "http_requests" GROUP BY time(1m), "method" FILL(0)` | `?metric=http_requests&aggregation=sum&step=1m&groupBy=method&fill=0` |
| 3 | `SELECT max("value") FROM "cpu" WHERE "host"=~'/server.*/' GROUP BY time(5m), "host"` | `?metric=cpu&labelMatch=host:/server.*/&aggregation=max&step=5m&groupBy=host` |
| 4 | `SELECT last("value") FROM "temperature" GROUP BY time(30s) FILL(previous)` | `?metric=temperature&aggregation=last&step=30s&fill=previous` |
| 5 | `SELECT percentile("value", 95) FROM "latency" GROUP BY time(1m), "endpoint" SLIMIT 20` | `?metric=latency&aggregation=p95&step=1m&groupBy=endpoint&seriesLimit=20` |
| 6 | `SELECT count("value") FROM "errors" WHERE "level"='ERROR' GROUP BY time(5m), "service"` | `?metric=errors&labels=level:ERROR&aggregation=count&step=5m&groupBy=service` |

---

## 6. 实施路径

### ✅ Sprint 1：后端核心重构（已完成 2026-07-10）

| # | 内容 | 改动文件 | 状态 |
|---|------|---------|:---:|
| 1 | `AggregationFunc` 注册表 + 11 种聚合函数实现 | `aggregation.go` (新) | ✅ |
| 2 | `FillStrategy` 注册表 + 5 种填充策略（NaN sentinel） | `fill.go` (新) | ✅ |
| 3 | `MetricRangeQuery` 新增 Aggregation/GroupBy/Fill/SeriesLimit/LabelMatch | `types_reader.go`, `types.go` | ✅ |
| 4 | `QueryRange` 重构：composite 聚合 + 动态 aggregation + fill 后处理 | `metric_reader.go` | ✅ |
| 5 | `Labels` 保证非 nil；NaN sentinel 在 converter 过滤 | `metric_reader.go`, `reader_adapter.go` | ✅ |
| 6 | Handler 解析新参数（aggregation/groupBy/fill/seriesLimit/labelMatch） | `observability_handler_v2.go` | ✅ |
| 7 | metadata API 支持 start/end（后端已支持，前端暂未全量调用） | `observability_handler_v2.go` | ✅ |
| 8 | `labelMatch` 正则过滤（ES regexp query） | `metric_reader.go` buildQueryFilter | ✅ |
| 9 | 聚合函数 + fill 策略单元测试（30+ 测试通过） | `aggregation_test.go`, `fill_test.go` | ✅ |

### ✅ Sprint 2：前端 Query Builder（已完成 2026-07-10）

| # | 内容 | 改动文件 | 状态 |
|---|------|---------|:---:|
| 1 | API 客户端 `metricQueryRange` 支持新参数 | `api/client.ts` | ✅ |
| 2 | API 客户端 metadata 方法支持 start/end | `api/client.ts` | ✅ |
| 3 | `MetricRangeQueryParams` 类型扩展 + AGGREGATION_OPTIONS/FILL_OPTIONS 常量 | `types/metric.ts` | ✅ |
| 4 | `formatMetricLabels` null guard（防 `labels: null` 崩溃） | `utils/metric.ts` | ✅ |
| 5 | `useMetricQuery` hook 新增 aggregation/groupBy/fill 状态 | `useMetricQuery.ts` | ✅ |
| 6 | `MetricQueryPanel` UI 升级：FROM/SELECT/WHERE/GROUP BY/FILL 布局 | `MetricQueryPanel.tsx` | ✅ |

### ✅ Sprint 4（已完成 2026-07-10）: 方案 A — InfluxDB 兼容 API

| # | 内容 | 结果 |
|---|------|:---:|
| 1 | InfluxQL 解析器集成（`github.com/influxdata/influxql` v1.4.1） | ✅ |
| 2 | `/api/v2/influxdb/query` 端点（GET + POST） | ✅ |
| 3 | InfluxQL → MetricRangeQuery 映射（mean→avg, percentile→pXX） | ✅ |
| 4 | Grafana 宏变量支持（`$timeFilter` → 毫秒时间戳, `$__interval` → 自动步长） | ✅ |
| 5 | Result 格式转换（→ InfluxDB v1 `{"results":[{"series":[...]}]}` ） | ✅ |
| 6 | 支持 epoch=ms 参数（毫秒时间戳） | ✅ |

**Grafana 配置**:
```
Type: InfluxDB
URL: http://<collector>:8088/api/v2
Access: Server
Database: <app_id>
```

**已知约束**: InfluxQL 保留字（如 `duration`）作为度量名时需加引号（Grafana 自动处理）。

### 🎉 全部完成

| 阶段 | 内容 | 状态 |
|------|------|:---:|
| Sprint 1 | 后端核心重构（Aggregation/Fill/GroupBy） | ✅ |
| Sprint 2 | 前端 Query Builder UI 升级 | ✅ |
| Sprint 3 | 端到端联调验证 | ✅ |
| Sprint 4 | 方案 A — InfluxDB 兼容 API | ✅ |

---

## 7. 未来演进：方案 A（InfluxDB 兼容 API）

当方案 C 全部实施完成后，核心查询能力已具备。接入 Grafana 只需一个**薄翻译层**：

```
Grafana InfluxDB DataSource                我们的服务
───────────────────────         ────────────────────────
POST /query                     /api/influxdb/query (新增端点)
  q=SELECT mean(...)              ↓
  db=metrics                   InfluxQL Parser (解析 SQL 子集)
                                  ↓
                               转换为 MetricRangeQuery
                                  ↓
                               调用内部 MetricReader.QueryRange(...)
                                  ↓
                               结果转换为 InfluxDB 响应格式
                                  ↓
                               返回 {"results":[{"series":[...]}]}
```

### 方案 A 额外工作

| # | 内容 |
|---|------|
| 1 | InfluxQL 子集 Parser（SELECT/FROM/WHERE/GROUP BY/FILL） |
| 2 | `/api/influxdb/query` 端点 |
| 3 | 结果格式转换（→ InfluxDB series JSON） |
| 4 | Grafana 宏变量支持（`$timeFilter`/`$__interval`） |
| 5 | 端到端验证：Grafana 添加 InfluxDB 数据源 → 直连 |

### 方案 A 前置条件

方案 C 的所有查询能力必须先到位（aggregation、groupBy、fill 等），否则 parser 转出来的参数无法执行。

---

## 8. 设计原则

| 原则 | 体现 |
|------|------|
| **SRP** | `AggregationFunc` / `FillStrategy` 各自单一职责 |
| **OCP** | 注册表模式：新增聚合/fill 只需注册，不改已有代码 |
| **DRY** | 聚合构建、结果解析、fill 逻辑各只有一处实现 |
| **策略模式** | `aggregationRegistry` + `fillStrategies` 替代 if-else |
| **健壮性** | Labels 保证非 nil；无效 aggregation 返回明确错误 |
| **可测试** | 聚合构建、结果解析、fill 策略均为纯函数 |

---

## 9. 风险

| 风险 | 级别 | 缓解 |
|------|------|------|
| ES composite aggregation 性能 | 中 | `seriesLimit` 限制桶数 |
| `last`/`first` 用 top_hits 性能 | 中 | 仅用户显式选择时使用 |
| 分组数过多图表拥挤 | 低 | 前端限制最多显示 20 条线 |
| 正则过滤（labelMatch） | ⚠️ | **已知约束**: labels 为 ES `flattened` 类型，不支持 regexp。待改为 object+keyword 或 wildcard |
| 方案 A Parser 复杂度 | 中 | 只支持 InfluxQL 子集 |

---

## 10. 变更日志

| 日期 | 版本 | 说明 |
|------|------|------|
| 2026-07-10 | v1.0 | 初始设计（groupBy + labels null 修复） |
| 2026-07-10 | v2.0 | 对齐 Grafana InfluxDB Query Builder：aggregation/fill/seriesLimit/labelMatch |
| 2026-07-10 | v3.0 | 整理：聚焦方案 C 实施（不向后兼容），方案 A 作为未来演进方向；合并 Sprint |
| 2026-07-10 | v4.0 | 方案 C 实施完成：Aggregation 注册表 + Fill 策略 + composite groupBy + UI 升级；30+ 测试通过 |
| 2026-07-10 | v4.1 | Sprint 3 验证：11 聚合函数/5 fill 策略/groupBy/metadata 全部通过；labelMatch 标记已知约束 |
| 2026-07-10 | v5.0 | **方案 A 实施完成**：InfluxQL 解析器集成 + `/api/v2/influxdb/query` 端点 + Grafana 宏变量支持；端到端验证全部通过 |
| 2026-07-10 | v5.1 | **Grafana 连接修复**：添加 `/ping` 健康检查端点 + `SHOW MEASUREMENTS/DATABASES/TAG KEYS/TAG VALUES/FIELD KEYS/RETENTION POLICIES` 支持；Grafana test connection 依赖 `SHOW MEASUREMENTS` 而非 `/ping` |

# Fix: PromQL Regex Label Match for ES Flattened Fields

## 需求背景

Grafana 通过 PromQL 查询 span metrics 时，正则匹配的 label（`=~` 操作符）无法正确返回结果。

**根因分析（双重问题）：**

1. **上层 `mergeLabels` 丢失语义**：`prometheus_handler.go` 中的 `mergeLabels` 函数将精确匹配 labels 和正则匹配 labelMatch 合并成单一 map，导致下游全部按精确匹配（`term`）处理。

2. **ES `flattened` 字段不支持 `regexp` 查询**：即使正确传递 `LabelMatch`，ES 7.10 的 `flattened` 类型字段只支持 `term`、`terms`、`prefix`、`exists` 查询，**不支持** `regexp` 和 `wildcard`。

## 实施方案

### 设计原则

- **单一职责**：正则翻译逻辑独立为 `regex_translator.go`，与查询构建解耦
- **开闭原则**：新增翻译策略不影响现有代码，通过 Strategy 枚举扩展
- **DRY**：所有查询路径（Query/QueryRange/QueryFlat/QueryRaw）统一使用 `buildMetricFilter` + `postFilter`
- **可测试性**：翻译器纯函数无副作用，100% 可单元测试

### 架构变更

```
Grafana PromQL: span_name=~"value1\.svc|value2\.svc"
        │
        ▼
prometheus_handler.go
  ├─ Labels:     {"service_name": "customcol"}    (精确匹配)
  └─ LabelMatch: {"span_name": "value1\\.svc|value2\\.svc"}  (正则匹配)
        │
        ▼ (不再 mergeLabels，分别传递)
dispatch*Query → exec*
        │
        ▼
buildMetricFilter()
  ├─ Labels → ES term 查询
  ├─ LabelMatch → TranslatePromQLRegex()
  │     ├─ StrategyTerm:   单值精确 → ES term
  │     ├─ StrategyTerms:  多值 OR  → ES terms
  │     ├─ StrategyPrefix: 前缀匹配 → ES prefix
  │     └─ StrategyUnsupported: 复杂正则 → PostFilters
  └─ return metricFilterResult{Query, PostFilters}
        │
        ▼
Query*/QueryRange/QueryFlat/QueryRaw
  └─ postFilter*(results, PostFilters) → 应用层正则过滤
```

### 文件变更清单

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| `extension/observabilitystorageext/provider/elasticsearch/regex_translator.go` | **新增** | 正则翻译器：PromQL regex → ES 查询策略 |
| `extension/observabilitystorageext/provider/elasticsearch/regex_translator_test.go` | **新增** | 翻译器单元测试（12 个场景） |
| `extension/observabilitystorageext/provider/elasticsearch/metric_reader.go` | **修改** | 集成翻译器，添加后过滤 |
| `extension/observabilitystorageext/provider/elasticsearch/metric_reader_test.go` | **修改** | 新增集成测试 |
| `extension/observabilitystorageext/provider/elasticsearch/types_reader.go` | **修改** | `MetricQuery` 添加 `LabelMatch` 字段 |
| `extension/observabilitystorageext/types.go` | **修改** | 外层 `MetricQuery` 添加 `LabelMatch` 字段 |
| `extension/adminext/prometheus_handler.go` | **修改** | 删除 `mergeLabels`，分别传递 labels/labelMatch |

### 正则翻译策略

| 输入模式 | 策略 | ES 查询 | 示例 |
|----------|------|---------|------|
| `value` (无特殊字符) | `StrategyTerm` | `{"term": {"field": "value"}}` | `POST /api/v1/query` |
| `val1\|val2\|val3` | `StrategyTerms` | `{"terms": {"field": [...]}}` | `svc-a\|svc-b\|svc-c` |
| `literal\.with\.dots` | `StrategyTerm` | `{"term": {"field": "literal.with.dots"}}` | `otel\.proto\.svc/Export` |
| `prefix.*` | `StrategyPrefix` | `{"prefix": {"field": "prefix"}}` | `opentelemetry\.proto.*` |
| `complex.*regex` | `StrategyUnsupported` | 无 ES 过滤 + 应用层过滤 | `[a-z]+\.service` |

### ES 验证结果

| 场景 | 修复前 | 修复后 |
|------|--------|--------|
| `span_name=~"POST /api/v2/..."` | ✅ (碰巧工作) | ✅ term 查询 |
| `span_name=~"otel\.proto\.\|...\|..."` | ❌ (mergeLabels 导致 term 匹配带反斜杠的值) | ✅ terms 多值匹配 |
| `span_name=~"complex.*regex"` | ❌ (ES regexp 报错) | ✅ 应用层过滤兜底 |

## 测试覆盖

- `TestTranslatePromQLRegex`: 12 个正则模式场景
- `TestBuildESClauseFromRegex`: 4 个策略→ES查询场景
- `TestPostFilterByRegex`: 6 个后过滤场景
- `TestBuildMetricFilter_*`: 3 个集成场景
- `TestPostFilter*`: 5 个后过滤函数测试

## 遗留问题

1. **`buildMetricQuery`（旧方法）** 仍被 `ListLabelCombinations` 使用，暂未支持 `LabelMatch`——因为 label exploration 场景不涉及正则匹配。
2. **QueryRaw 的 composite aggregation** 使用硬编码 painless script，对于不在 groupBy 中的 label 的后过滤依赖 series 级别 Labels 提取的完整性。
3. 当前 `StrategyUnsupported` 的后过滤在数据量大时可能有性能影响——但这是 ES `flattened` 类型的固有限制，除非将 labels mapping 改为 `keyword` 子字段。

## 状态

- [x] 正则翻译器实现及测试
- [x] `buildMetricFilter` 集成翻译器
- [x] 所有查询路径添加后过滤
- [x] 上层 `mergeLabels` 删除，正确传递 labels/labelMatch
- [x] 全量编译通过
- [x] 全量测试通过（无回归）

---

# Fix 2: `_sum` Histogram Sub-Series Detection

## 需求背景

Grafana 查询 `traces_service_graph_request_server_seconds_sum{...}` 时返回空，根因是 `detectHistogramSub()` 故意不检测 `_sum` 后缀，导致 ES 查询使用完整名称（含 `_sum`）而非基名，命中 0 条。

## 根因分析

```go
// 修复前: _sum 后缀被故意忽略
func detectHistogramSub(name string) (string, bool) {
    if strings.HasSuffix(name, HistogramSuffixBucket) {
        return HistogramSubBucket, true
    }
    return "", false  // _sum 会被跳过
}
```

注释中声称 "_sum 可能是独立的 counter 名称而非 histogram 子序列"，但实际场景中 `traces_service_graph_request_server_seconds` 确实是 histogram 指标：
- 基名 `traces_service_graph_request_server_seconds` 在 ES 中有 81,220 条文档
- 含 `_sum` 后缀的 `traces_service_graph_request_server_seconds_sum` 在 ES 中有 0 条

## 实施方案

### 设计原则

- **最小修改**：只改 `detectHistogramSub` 添加 `_sum` 检测，`stripHistogramSuffix` 已支持 `_sum`
- **稳定性保障**：同步修复 `execRateRange` 和 `execRateInstant` 中 metric name 重建遗漏

### 文件变更

| 文件 | 变更 | 说明 |
|------|------|------|
| `extension/adminext/prometheus_handler.go` | **修改** | 3 处改动 |
| `extension/adminext/prometheus_handler_test.go` | **修改** | 更新测试期望值 |

### 改动 1: `detectHistogramSub` 添加 `_sum` 检测

```go
func detectHistogramSub(name string) (string, bool) {
    if strings.HasSuffix(name, HistogramSuffixSum) {
        return HistogramSubSum, true  // 新增
    }
    if strings.HasSuffix(name, HistogramSuffixBucket) {
        return HistogramSubBucket, true
    }
    return "", false
}
```

### 改动 2 & 3: `execRateRange` / `execRateInstant` metric name 重建

rate/increase/irate 路径中 `expr.MetricName` 是去掉后缀的基名，需重建为含 `_sum`/`_bucket` 的名称：

```go
// 修复后
name := expr.MetricName
if expr.HistogramSub != "" {
    name = expr.BaseMetric + "_" + expr.HistogramSub
}
m := promMetric{PromLabelName: name}
```

此前 `_bucket` 在 rate 路径（无 histogram_quantile）也存在此问题，一并修复。

### PromQL 解析流程示例

```
sum by (client, server) (rate(traces_service_graph_request_server_seconds_sum{...}[3600s]))
        │
        ▼ parsePromQL()
  MetricName:  traces_service_graph_request_server_seconds   (strip _sum)
  HistogramSub: "sum"
  BaseMetric: traces_service_graph_request_server_seconds_sum
  Function: rate
  Aggregation: sum, GroupBy: [client, server]
        │
        ▼ execRateInstant()
  QueryFlat(MetricName="traces_service_graph_request_server_seconds")
  → ES 查询基名指标 ✓
        │
        ▼ 返回结果
  PromLabelName: traces_service_graph_request_server_seconds_sum  ✓
```

## 测试覆盖

- `TestDetectHistogramSub`: 更新 `_sum` 期望值从 `("", false)` → `("sum", true)`
- `TestStripHistogramSuffix`: 已有 `_sum` 测试，无需修改

## 遗留问题

本文档中 `traces_service_graph_*` 查询不返回数据的另一个原因是 **`span_name` 标签根本不存在于 `traces_service_graph_*` 指标中**——这些指标按服务对（client↔server）聚合，label 只有 `client`、`server`、`connection_type`，没有 `span.name`。这不是代码 bug，而是 Grafana 面板配置错误。

## 状态

- [x] `detectHistogramSub` 添加 `_sum` 检测
- [x] `execRateRange` metric name 重建
- [x] `execRateInstant` metric name 重建
- [x] 测试用例更新
- [x] 全量编译通过
- [x] 全量测试通过（无回归）

---

# Fix 3: GroupBy Label Key Translation in ES Aggregation

## 需求背景

Grafana Explore Metrics 按 `http_method` 分组查询 `traces_spanmetrics_calls_total` 时，返回数据但 `labels` 为空 `{}`——所有 series 被合并为一个。

## 根因分析

`buildAggregation` 构建 ES composite aggregation 时，`GroupBy` 中的 label key 直接拼接到 ES field path 中，未从 PromQL 下划线格式翻译为 ES 点格式：

```go
// 修复前: http_method (下划线) → labels.http_method → ES 中不存在
sources = append(sources, map[string]any{
    label: map[string]any{
        "terms": map[string]any{
            "field": fmt.Sprintf("%s.%s", FieldMetricLabels, label),
        },
    },
})
```

而 ES 实际存储的是 `labels.http.method`。`translateLabelKey("http_method")` → `"http.method"` 映射已存在，但只用于过滤标签（LABELS/LabelMatch 经 `normalizeMetricQueryLabels` 翻译），**GroupBy 直接使用原始值无翻译**。

### 问题链路

```
Grafana: avg by (http_method) (traces_spanmetrics_calls_total)
    │
    ▼ parsePromQL → GroupBy = ["http_method"]
    │
    ▼ buildAggregation → ES composite agg on labels.http_method (不存在)
    │
    ▼ ES missing_bucket → null group
    │
    ▼ stripMatrixMetricToGroupBy → labels = {} ❌
```

## 实施方案

在 `buildAggregation` 中对每个 GroupBy label 调用 `translateLabelKey`：

```go
// 修复后
for _, label := range groupBy {
    esKey := translateLabelKey(label)  // http_method → http.method
    sources = append(sources, map[string]any{
        label: map[string]any{  // composite 结果 key 保持 PromQL 格式
            "terms": map[string]any{
                "field": fmt.Sprintf("%s.%s", FieldMetricLabels, esKey),  // labels.http.method ✓
                "missing_bucket": true,
            },
        },
    })
}
```

**注意**：composite source 的**外层 key 名**保持 PromQL 格式，仅 **field path** 用点格式。

### 文件变更

| 文件 | 变更 |
|------|------|
| `extension/observabilitystorageext/provider/elasticsearch/metric_reader.go` | `buildAggregation` 添加 `translateLabelKey` |

## 影响的标签

| PromQL GroupBy | ES Field | 之前 | 之后 |
|---|---|---|---|
| `http_method` | `labels.http.method` | ❌ | ✅ |
| `http_route` | `labels.http.route` | ❌ | ✅ |
| `service_name` | `labels.service.name` | ❌ | ✅ |
| `rpc_method` | `labels.rpc.method` | ❌ | ✅ |
| `peer_service` | `labels.peer.service` | ❌ | ✅ |
| `status_code` | `labels.status.code` | ❌ | ✅ |
| `span_name` | `labels.span.name` | ✅ (巧合) | ✅ |

## 状态

- [x] `buildAggregation` 添加 `translateLabelKey`
- [x] 全量编译通过
- [x] 全量测试通过（无回归）

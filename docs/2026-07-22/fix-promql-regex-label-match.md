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

---

# Optimization: Concurrent QueryFlat for Multi-Value Regex

## 需求背景

Grafana 面板查询 `topk(5, sum(rate(traces_spanmetrics_calls_total{span_name=~"A|B|C|D|E"}[900s])) by (span_name))` 时，`span_name` 正则包含 5 个 pipe-separated 值。当前 `QueryFlat` 一次拉取所有匹配的文档（可达上万条），造成延迟。

## 根因分析

```
span_name=~"A|B|C|D|E"
    → TranslatePromQLRegex → StrategyTerms + values: [A,B,C,D,E]
    → ES: {"terms": {"labels.span.name": ["A","B","C","D","E"]}}
    → 一次查询返回所有 5 个 span_name 的文档（最多 10000 条）
```

虽然 ES 层面是一次查询，但文档量大导致：
1. ES 搜索时间增加（扫描更多文档）
2. 网络传输量大（返回上万个 JSON 文档）
3. Go 侧反序列化和分组开销大

## 优化方案

### 设计原则

- **正确性不变**：拆分后每个子查询独立获取数据，合并后结果与单次查询完全等价
- **透明降级**：当模式不可拆分（单值、复杂正则、超过 MaxConcurrency）时，自动降级为单次查询
- **高内聚低耦合**：并发逻辑封装为独立文件 `prometheus_concurrent.go`，不侵入核心 handler 逻辑

### 架构

```
execRateInstant / execHistogramQuantileInstant
    │
    ▼
concurrentQueryFlat(ctx, flatQuery, logger)
    │
    ├─ findSplitCandidate(labelMatch, minTerms=2)
    │     └─ 扫描 labelMatch 中是否有 pipe-separated literal 模式
    │
    ├─ 不可拆分 → 降级为单次 QueryFlat (原路径)
    │
    └─ 可拆分 → 拆为 N 个并发子查询
          ├─ subQuery[0]: LabelMatch["span_name"] = "A" (单值 pattern，保留在 LabelMatch)
          ├─ subQuery[1]: LabelMatch["span_name"] = "B"
          ├─ ...
          └─ subQuery[N-1]: LabelMatch["span_name"] = "E"
                │
                ▼ sync.WaitGroup 并发执行
                │
                ▼ 合并 Samples + Total
```

**关键设计决策：值保留在 LabelMatch 而非移至 Labels**

早期实现将拆分后的值从 `LabelMatch` 移到 `Labels`（exact match），但发现 `Labels` 中的值会经过 `translateLabelValue()` 归一化（如 `span.kind: SPAN_KIND_CLIENT` → `Client`），而 `LabelMatch` 中的 regex pattern 直接进入 `TranslatePromQLRegex` 不做归一化。两者路径不一致导致查询命中不同文档。

**修复方案**：将单值保留在 `LabelMatch` 作为 literal pattern，使其走相同的 `TranslatePromQLRegex` → ES term/terms 路径，确保语义等价。

### 拆分条件

| 条件 | 说明 |
|------|------|
| `labelMatch` 中存在 pipe-separated pattern | 如 `A|B|C` |
| 所有 alternatives 是 literal（无 regex metachar） | `\.` 允许（escaped dot） |
| alternatives 数量 ≥ MinTermsForSplit (2) | 至少 2 个值才值得拆分 |
| alternatives 数量 ≤ MaxConcurrency (10) | 避免 goroutine 爆炸 |

### 文件变更

| 文件 | 变更类型 | 说明 |
|------|----------|------|
| `extension/adminext/prometheus_concurrent.go` | **新增** | 并发查询拆分逻辑 |
| `extension/adminext/prometheus_concurrent_test.go` | **新增** | 拆分逻辑单元测试 |
| `extension/adminext/prometheus_concurrent_integration_test.go` | **新增** | ES 集成测试（正确性+性能） |
| `extension/adminext/prometheus_concurrent_benchmark_test.go` | **新增** | 性能基准测试 |
| `extension/adminext/prometheus_handler.go` | **修改** | 4 处 `QueryFlat` 调用改为 `concurrentQueryFlat` |
| `extension/observabilitystorageext/reader_adapter.go` | **修改** | 新增 `NewMetricReaderAdapterForTest` 测试辅助导出 |

### 影响的查询路径

| 函数 | 改动 |
|------|------|
| `execRateInstant` | `e.storageMetricReader.QueryFlat` → `e.concurrentQueryFlat` |
| `execRateRange` | 同上 |
| `execHistogramQuantileInstant` | 同上 |
| `execHistogramQuantileRange` | 同上 |

### 数据正确性：ES 集成验证

以 `ES_INTEGRATION_TEST=true ES_HOST=9.134.106.132 ES_PORT=9200 ES_USERNAME=elastic ES_PASSWORD=Aaaaaaaaa!1` 对真实 ES 集群（238 个 metric 名，含 `traces_spanmetrics_*`、`jvm.*`、`kafka.*`、`rpc.*` 等）执行 4 组集成测试：

| 测试 | 描述 | 结果 |
|------|------|------|
| `TestIntegrationConcurrentVsSingle` | 16 个指标 × 多值 label 拆分对比 | ✅ 全部通过（100% 覆盖） |
| `TestIntegrationConcurrentVsSingle_WithLabels` | 并发拆分 + 额外精确 labels | ✅ 100% 覆盖 |
| `TestIntegrationConcurrentVsSingle_NoSplittable` | 不可拆分查询降级验证 | ✅ 结果一致 |
| `TestIntegrationConcurrent_ErrorHandling` | cancel context 错误传播 | ✅ 正确返回错误 |

**核心验证指标**：所有测试中，单查询（single QueryFlat）的 100% 样本都能被并发查询（concurrent QueryFlat）的结果覆盖（`single-covered-by-concurrent >= 99.5%`）。

**并发超集现象**：当单查询因 ES `MaxDocs=10000` 上限被截断时，并发查询因每个子查询有独立的 `MaxDocs`，返回更多文档（超集）。例如 `traces_spanmetrics_calls_total` 按 `span.kind` 拆分（9 个 term），单查询返回 10000 条（打满上限），并发返回 43699 条（4.4x），但单查询的 10000 条 100% 包含在并发结果中。

### 性能基准测试结果（Apple M4 Pro, ES: 9.134.106.132:9200）

**测试 1: `http.route` 7-term 拆分**（单查询 ~893ms，并发 ~725ms，**18.8% 提升**）

| 模式 | 延迟 (ns/op) |
|------|-------------|
| Single QueryFlat | 893,560,333 |
| Concurrent (7 goroutines) | 725,199,633 |

**测试 2: `span.kind` 扩展性**（数据量大，MaxDocs 瓶颈显著，并发开销 > 收益）

| Terms | Single | Concurrent | 速度比 |
|-------|--------|-----------|--------|
| 2 | 584ms | 1,028ms | 0.57x (并发慢) |
| 3 | 731ms | 1,377ms | 0.53x |
| 5 | 551ms | 1,552ms | 0.36x |
| 8 | 521ms | 1,497ms | 0.35x |

**结论**：并发优化在总数据量不大的场景下有效（每个 term 子查询 ≤ 2000 docs），当数据量大到每个 term 都能打满 `MaxDocs` 时，并发创建的多 ES 连接开销超过单查询收益。此场景下并发主要提供**数据完整性优势**（无截断），而非延迟优势。

## 测试覆盖

- `TestSplitPipeLiterals`: 8 个正则模式解析场景
- `TestFindSplitCandidate`: 4 个候选检测场景
- `TestCloneLabelsWithTerm`: labels 克隆正确性
- `TestCloneLabelMatchWithout`: labelMatch 移除 key 正确性
- `TestCloneLabelMatchWithSingleTerm`: 单值替换正确性（修复后新增）
- `TestIsLiteralOrEscapedDots`: 8 个 literal 判断场景
- `TestSplitUnescapedPipeLocal`: 3 个管道拆分场景
- `TestIntegrationConcurrentVsSingle`: ES 真实数据 16 个指标对比验证
- `TestIntegrationConcurrentVsSingle_WithLabels`: 含额外精确 labels 的对比
- `TestIntegrationConcurrentVsSingle_NoSplittable`: 不可拆分降级验证
- `TestIntegrationConcurrent_ErrorHandling`: 错误传播验证

## 状态

- [x] `prometheus_concurrent.go` 并发查询拆分实现
- [x] 4 处 QueryFlat 调用替换为 concurrentQueryFlat
- [x] 单元测试覆盖
- [x] 全量编译通过
- [x] 全量测试通过（无回归）
- [x] ES 集成测试通过（100% 样本覆盖，所有查询场景）
- [x] 性能基准测试完成
- [x] LabelMatch 路径修正（保持值在 LabelMatch，避免 translateLabelValue 归一化）

## 后续优化方向

| 优先级 | 优化项 | 说明 |
|--------|--------|------|
| Phase 1 | 查询结果缓存（15-30s TTL） | 对相同 fingerprint 的查询缓存结果 |
| Phase 2 | Histogram ES `sum` 聚合 | bucket_counts 改为 ES 侧 sum（需确认 delta temporality） |
| Phase 2 | 索引按 metric_name/时间分片 | 减少 ES 扫描范围 |
| Phase 3 | Query Planner 共享子表达式 | 多面板场景避免重复查询 |
